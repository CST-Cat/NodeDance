package server

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/CST-Cat/NodeDance/internal/core/geolocation"
	coretasks "github.com/CST-Cat/NodeDance/internal/core/tasks"
	"github.com/CST-Cat/NodeDance/internal/protocol"
	"github.com/CST-Cat/NodeDance/internal/taskstate"
)

var errRsyncUnavailable = errors.New("rsync is unavailable; use the chunk data relay")

type rsyncCommitError struct{ cause error }

func (e rsyncCommitError) Error() string { return e.cause.Error() }
func (e rsyncCommitError) Unwrap() error { return e.cause }

func (s *Server) runRsyncMode(ctx context.Context, current *session, descriptor protocol.FileTransferDescriptor, mode string) (string, error) {
	attempts := []string{"tcp", "reverse", "ice"}
	if mode == protocol.FileTransferModeRelay {
		attempts = []string{"", ""}
	}
	var attemptErrors []error
	for _, peerMode := range attempts {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		if err := s.runRsyncAttempt(ctx, current, descriptor, mode, peerMode); err == nil {
			return "rsync_" + mode, nil
		} else {
			attemptErrors = append(attemptErrors, fmt.Errorf("%s: %w", mode+"/"+peerMode, err))
			var committing rsyncCommitError
			if errors.As(err, &committing) {
				return "", err
			}
			if errors.Is(err, errRsyncUnavailable) {
				break
			}
		}
	}
	return "", errors.Join(attemptErrors...)
}

func (s *Server) waitRsyncConnections(ctx context.Context, descriptor protocol.FileTransferDescriptor) (*agentConnection, *agentConnection, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		source, sourceErr := s.activeFileConnection(ctx, descriptor.File.Transfer.SourceNodeID)
		target, targetErr := s.activeFileConnection(ctx, descriptor.TargetNodeID)
		if sourceErr == nil && targetErr == nil {
			return source, target, nil
		}
		select {
		case <-ctx.Done():
			return nil, nil, errors.New("file Agents did not reconnect")
		case <-ticker.C:
		}
	}
}

func (s *Server) abortRsyncAttempt(connection *agentConnection, descriptor protocol.FileTransferDescriptor, sessionID string) {
	if connection == nil || connection.ctx.Err() != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	request := protocol.FileRsyncRequest{Operation: "abort", SessionID: sessionID, Descriptor: descriptor}
	waiter, cleanup, err := s.sendRsyncRequest(ctx, connection, request)
	if err != nil {
		return
	}
	defer cleanup()
	_, _ = waitRsyncResponse(ctx, connection, waiter)
}

func (s *Server) rsyncProgress(connection *agentConnection, descriptor protocol.FileTransferDescriptor, sessionID string, completed uint64) {
	id, err := newContainerStreamRequestID()
	if err != nil || connection.ctx.Err() != nil {
		return
	}
	request := protocol.FileRsyncRequest{Operation: "progress", SessionID: sessionID, Descriptor: descriptor, Completed: completed}
	envelope := protocol.Envelope{Version: protocol.CurrentVersion, Type: protocol.TypeFileRsyncRequest, RequestID: id, Generation: connection.generation, Payload: marshalAgentPayload(request)}
	select {
	case connection.commands <- envelope:
	default:
	}
}

func rsyncResponseError(response protocol.FileRsyncResponse) error {
	if response.State == "unavailable" && response.Code == "rsync_unavailable" {
		return errRsyncUnavailable
	}
	return errors.New("Agent rsync attempt failed: " + response.Code)
}

func (s *Server) runRsyncAttempt(ctx context.Context, current *session, descriptor protocol.FileTransferDescriptor, transport, peerMode string) (failure error) {
	var sourceSDP, targetSDP string
	phase := "target_offer"
	connected := false
	defer func() {
		if failure != nil && peerMode == "ice" && targetSDP != "" && !connected {
			failure = iceFailureDetails(failure, phase, sourceSDP, targetSDP)
		}
	}()
	source, target, err := s.waitRsyncConnections(ctx, descriptor)
	if err != nil {
		return err
	}
	id, err := newContainerStreamRequestID()
	if err != nil {
		return err
	}
	binding := protocol.FileStreamDigest(descriptor)
	sender, err := newFileStreamGrant(id, descriptor.TaskID, source.nodeID, "sender", descriptor.File.Path, binding)
	if err != nil {
		return err
	}
	receiver, err := newFileStreamGrant(id, descriptor.TaskID, target.nodeID, "receiver", descriptor.File.Path, binding)
	if err != nil {
		return err
	}
	if transport == "relay" {
		_, cleanup := s.newRsyncRelay(ctx, current, descriptor, source, target, sender, receiver)
		defer cleanup()
	}
	defer s.abortRsyncAttempt(source, descriptor, id)
	if source != target {
		defer s.abortRsyncAttempt(target, descriptor, id)
	}
	prepareGrant := sender
	if transport == "relay" {
		prepareGrant = receiver
	}
	prepare := protocol.FileRsyncRequest{Operation: "prepare", SessionID: id, Descriptor: descriptor, Grant: prepareGrant, Transport: transport, PeerMode: peerMode}
	var directAsset protocol.NodeAsset
	if transport == "direct" && peerMode == "tcp" {
		if asset, assetErr := s.nodeAsset(ctx, descriptor.TargetNodeID); assetErr == nil {
			directAsset = asset
		}
		prepare.ListenPort = directAsset.DirectListenPort
	}
	prepared, cleanupPrepared, err := s.sendRsyncRequest(ctx, target, prepare)
	if err != nil {
		return err
	}
	defer cleanupPrepared()
	readyCtx, cancelReady := context.WithTimeout(ctx, 15*time.Second)
	offer, err := waitRsyncResponse(readyCtx, target, prepared)
	cancelReady()
	if err != nil {
		return err
	}
	if offer.State != "ready" {
		return rsyncResponseError(offer)
	}
	targetSDP = offer.SDP
	send := protocol.FileRsyncRequest{Operation: "send", SessionID: id, Descriptor: descriptor, Grant: sender, Transport: transport, Endpoints: offer.Endpoints, Fingerprint: offer.Fingerprint, PeerMode: peerMode, SDP: offer.SDP}
	if transport == "direct" && peerMode == "tcp" {
		if endpoint, ok := declaredDirectEndpoint(directAsset, s.connectionPublicIP(descriptor.TargetNodeID)); ok {
			send.Endpoints = append([]string{endpoint}, send.Endpoints...)
			if len(send.Endpoints) > 64 {
				send.Endpoints = send.Endpoints[:64]
			}
		}
	}
	if transport == "direct" && peerMode == "reverse" {
		if asset, assetErr := s.nodeAsset(ctx, descriptor.File.Transfer.SourceNodeID); assetErr == nil {
			send.ListenPort = asset.DirectListenPort
		}
	}
	sending, cleanupSending, err := s.sendRsyncRequest(ctx, source, send)
	if err != nil {
		return err
	}
	defer cleanupSending()
	var received protocol.FileRsyncResponse
	receivedUpdates := prepared.updates
	for {
		var response protocol.FileRsyncResponse
		select {
		case response = <-sending.updates:
		case received = <-receivedUpdates:
			if received.State != "received" {
				return rsyncResponseError(received)
			}
			connected = true
			receivedUpdates = nil
			continue
		case <-ctx.Done():
			return ctx.Err()
		case <-source.ctx.Done():
			return errors.New("source Agent disconnected")
		case <-target.ctx.Done():
			return errors.New("target Agent disconnected")
		}

		if response.State == "offer" {
			sourceSDP = response.SDP
			phase = "answer_forwarding"
			signal := protocol.FileRsyncRequest{Operation: "connect", SessionID: id, Descriptor: descriptor, Grant: sender, Transport: transport, PeerMode: peerMode, Endpoints: response.Endpoints, Fingerprint: response.Fingerprint, SDP: response.SDP}
			if peerMode == "reverse" {
				if asset, assetErr := s.nodeAsset(ctx, descriptor.File.Transfer.SourceNodeID); assetErr == nil {
					if endpoint, ok := declaredDirectEndpoint(asset, s.connectionPublicIP(source.nodeID)); ok {
						signal.Endpoints = append([]string{endpoint}, signal.Endpoints...)
						if len(signal.Endpoints) > 64 {
							signal.Endpoints = signal.Endpoints[:64]
						}
					}
				}
			}
			signaled, cleanup, err := s.sendRsyncRequest(ctx, target, signal)
			if err != nil {
				return err
			}
			response, err := waitRsyncResponse(ctx, target, signaled)
			cleanup()
			if err != nil {
				return err
			}
			if response.State != "signaled" {
				return rsyncResponseError(response)
			}
			phase = "connectivity_or_dtls"
			continue
		}
		if response.State == "progress" {
			connected = true
			s.rsyncProgress(target, descriptor, id, response.Completed)
			continue
		}
		if response.State != "sent" {
			return rsyncResponseError(response)
		}
		connected = true
		break
	}
	if receivedUpdates != nil {
		received, err = waitRsyncResponse(ctx, target, prepared)
		if err != nil {
			return err
		}
	}
	if received.State != "received" {
		return rsyncResponseError(received)
	}
	commit := protocol.FileRsyncRequest{Operation: "commit", SessionID: id, Descriptor: descriptor, Transport: transport}
	committing, cleanupCommitting, err := s.sendRsyncRequest(ctx, target, commit)
	if err != nil {
		return rsyncCommitError{cause: err}
	}
	defer cleanupCommitting()
	response, err := waitRsyncResponse(ctx, target, committing)
	if err != nil {
		return rsyncCommitError{cause: err}
	}
	if response.State != "committed" {
		return rsyncCommitError{cause: rsyncResponseError(response)}
	}
	return nil
}

func declaredDirectEndpoint(asset protocol.NodeAsset, publicIP string) (string, bool) {
	if asset.DirectListenPort <= 0 || publicIP == "" {
		return "", false
	}
	port := asset.DirectPublicPort
	if port <= 0 {
		port = asset.DirectListenPort
	}
	return "wss://" + net.JoinHostPort(publicIP, strconv.Itoa(port)) + protocol.FileRsyncDataRoute, true
}

func (s *Server) signalFileTransferCancellation(task coretasks.Task) error {
	if task.Intent.File == nil || task.Intent.File.Transfer == nil || taskstate.IsTerminal(task.Status) {
		return errors.New("task is not an active file transfer")
	}
	s.fileTransferMu.Lock()
	if run := s.fileTransferRuns[task.TaskID]; run != nil {
		run.cancel(taskstate.ErrCancellationRequested)
	}
	s.fileTransferMu.Unlock()
	s.agentConnectionsMu.Lock()
	defer s.agentConnectionsMu.Unlock()
	for _, connection := range s.agentConnections {
		if connection.nodeID != task.NodeID || !connection.capabilityEnabled(protocol.CapabilityTaskBridge) || connection.ctx.Err() != nil {
			continue
		}
		connection.taskMu.RLock()
		journalID := connection.taskJournalID
		connection.taskMu.RUnlock()
		request := protocol.TaskCancelRequest{TaskID: task.TaskID, JournalID: journalID}
		command := protocol.Envelope{Version: protocol.CurrentVersion, Type: protocol.TypeTaskCancelRequest, Generation: connection.generation, RequestID: task.TaskID, Payload: marshalAgentPayload(request)}
		if protocol.ValidateTaskCancelRequest(command, request, connection.nodeID, journalID, connection.generation) != nil {
			return errors.New("file task cancellation binding is unavailable")
		}
		select {
		case connection.commands <- command:
			return nil
		default:
			return errors.New("Agent command queue is full")
		}
	}
	return errors.New("file Agent is offline")
}

func iceFailureDetails(cause error, phase, sourceSDP, targetSDP string) error {
	return fmt.Errorf("%w; ICE stage=%s source{%s} target{%s}", cause, phase, iceOfferSummary(sourceSDP), iceOfferSummary(targetSDP))
}

func iceOfferSummary(sdp string) string {
	if sdp == "" {
		return "not_advertised"
	}
	var host, publicHost, reflexive, relay int
	for line := range strings.SplitSeq(sdp, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 8 || !strings.HasPrefix(fields[0], "a=candidate:") || !strings.EqualFold(fields[2], "udp") || fields[6] != "typ" || net.ParseIP(fields[4]) == nil {
			continue
		}
		port, err := strconv.Atoi(fields[5])
		if err != nil || port < 1 || port > 65535 {
			continue
		}
		switch fields[7] {
		case "host":
			host++
			if geolocation.PublicIP(fields[4]) != "" {
				publicHost++
			}
		case "srflx":
			reflexive++
		case "relay":
			relay++
		}
	}
	return fmt.Sprintf("host=%d public_host=%d srflx=%d relay=%d", host, publicHost, reflexive, relay)
}
