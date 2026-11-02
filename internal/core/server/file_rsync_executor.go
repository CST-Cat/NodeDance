package server

import (
	"context"
	"errors"
	"time"

	coretasks "github.com/CST-Cat/NodeDance/internal/core/tasks"
	"github.com/CST-Cat/NodeDance/internal/protocol"
	"github.com/CST-Cat/NodeDance/internal/taskstate"
)

var errRsyncUnavailable = errors.New("rsync is unavailable; use the chunk data relay")

type rsyncCommitError struct{ cause error }

func (e rsyncCommitError) Error() string { return e.cause.Error() }
func (e rsyncCommitError) Unwrap() error { return e.cause }

func (s *Server) tryRsyncTransfer(ctx context.Context, current *session, descriptor protocol.FileTransferDescriptor) (string, error) {
	var lastErr error
	for _, transport := range []string{"direct", "relay", "relay"} {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		if err := s.runRsyncAttempt(ctx, current, descriptor, transport); err == nil {
			return "rsync_" + transport, nil
		} else {
			lastErr = err
			var committing rsyncCommitError
			if errors.As(err, &committing) {
				return "", err
			}
			if errors.Is(err, errRsyncUnavailable) {
				break
			}
		}
	}
	return "", lastErr
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

func (s *Server) runRsyncAttempt(ctx context.Context, current *session, descriptor protocol.FileTransferDescriptor, transport string) error {
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
	prepare := protocol.FileRsyncRequest{Operation: "prepare", SessionID: id, Descriptor: descriptor, Grant: prepareGrant, Transport: transport}
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
	send := protocol.FileRsyncRequest{Operation: "send", SessionID: id, Descriptor: descriptor, Grant: sender, Transport: transport, Endpoints: offer.Endpoints, Fingerprint: offer.Fingerprint}
	sending, cleanupSending, err := s.sendRsyncRequest(ctx, source, send)
	if err != nil {
		return err
	}
	defer cleanupSending()
	for {
		response, err := waitRsyncResponse(ctx, source, sending)
		if err != nil {
			return err
		}
		if response.State == "progress" {
			s.rsyncProgress(target, descriptor, id, response.Completed)
			continue
		}
		if response.State != "sent" {
			return rsyncResponseError(response)
		}
		break
	}
	received, err := waitRsyncResponse(ctx, target, prepared)
	if err != nil {
		return err
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
