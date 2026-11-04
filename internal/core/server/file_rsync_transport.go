package server

import (
	"context"
	"crypto/subtle"
	"errors"
	"net/http"
	"reflect"
	"sync"
	"time"

	"github.com/CST-Cat/NodeDance/internal/core/auth"
	"github.com/CST-Cat/NodeDance/internal/filetransport"
	"github.com/CST-Cat/NodeDance/internal/protocol"
	"github.com/CST-Cat/NodeDance/internal/taskstate"
	"github.com/coder/websocket"
)

type coreRsyncWaiter struct {
	request  protocol.FileRsyncRequest
	updates  chan protocol.FileRsyncResponse
	ready    bool
	offered  bool
	terminal bool
}

func (s *Server) sendRsyncRequest(ctx context.Context, connection *agentConnection, request protocol.FileRsyncRequest) (*coreRsyncWaiter, func(), error) {
	if connection == nil || connection.ctx.Err() != nil || !connection.capabilityEnabled(protocol.CapabilityFiles) {
		return nil, nil, errors.New("Agent rsync control is unavailable")
	}
	id, err := newContainerStreamRequestID()
	if err != nil {
		return nil, nil, err
	}
	envelope := protocol.Envelope{Version: protocol.CurrentVersion, Type: protocol.TypeFileRsyncRequest, Generation: connection.generation, RequestID: id, Payload: marshalAgentPayload(request)}
	if len(envelope.Payload) > protocol.MaxFileControlBytes || protocol.ValidateFileRsyncRequest(envelope, connection.generation, request) != nil {
		return nil, nil, errors.New("invalid rsync control request")
	}
	waiter := &coreRsyncWaiter{request: request, updates: make(chan protocol.FileRsyncResponse, 8)}
	connection.rsyncMu.Lock()
	if connection.rsyncWaiters == nil {
		connection.rsyncWaiters = make(map[string]*coreRsyncWaiter)
		connection.rsyncTombstones = make(map[string]bool)
	}
	if len(connection.rsyncWaiters) >= 8*maxCoreFileTransfers {
		connection.rsyncMu.Unlock()
		return nil, nil, errors.New("Agent rsync control capacity reached")
	}
	connection.rsyncWaiters[id] = waiter
	connection.rsyncMu.Unlock()
	cleanup := func() {
		connection.rsyncMu.Lock()
		delete(connection.rsyncWaiters, id)
		connection.rsyncTombstones[id] = true
		connection.rsyncTombstoneOrder = append(connection.rsyncTombstoneOrder, id)
		if len(connection.rsyncTombstoneOrder) > 128 {
			old := connection.rsyncTombstoneOrder[0]
			connection.rsyncTombstoneOrder = connection.rsyncTombstoneOrder[1:]
			delete(connection.rsyncTombstones, old)
		}
		connection.rsyncMu.Unlock()
	}
	select {
	case connection.commands <- envelope:
		return waiter, cleanup, nil
	case <-ctx.Done():
		cleanup()
		return nil, nil, ctx.Err()
	case <-connection.ctx.Done():
		cleanup()
		return nil, nil, errors.New("Agent disconnected")
	}
}

func (s *Server) handleFileRsyncResponse(connection *agentConnection, envelope protocol.Envelope) error {
	var response protocol.FileRsyncResponse
	if len(envelope.Payload) > protocol.MaxFileControlBytes || decodeAgentPayload(envelope.Payload, &response) != nil || protocol.ValidateFileRsyncResponse(envelope, connection.generation, response) != nil {
		return errors.New("invalid Agent rsync response")
	}
	connection.rsyncMu.Lock()
	defer connection.rsyncMu.Unlock()
	waiter := connection.rsyncWaiters[envelope.RequestID]
	if waiter == nil {
		if connection.rsyncTombstones[envelope.RequestID] {
			return nil
		}
		return errors.New("unknown rsync response identity")
	}
	if response.SessionID != waiter.request.SessionID || response.Operation != waiter.request.Operation || response.Completed > uint64(waiter.request.Descriptor.File.Size) || waiter.terminal {
		return errors.New("rsync response does not match its task")
	}
	if response.State == "ready" || response.State == "offer" {
		mode := waiter.request.PeerMode
		hasOffer := mode != "" && !(response.State == "ready" && mode == "reverse")
		if hasOffer && !protocol.ValidFileRsyncOffer(mode, response.Endpoints, response.Fingerprint, response.SDP) || !hasOffer && (len(response.Endpoints) != 0 || response.Fingerprint != "" || response.SDP != "") {
			return errors.New("peer offer does not match the selected transport")
		}
	} else if len(response.Endpoints) != 0 || response.Fingerprint != "" || response.SDP != "" {
		return errors.New("unexpected peer offer payload")
	}
	if response.State == "offer" {
		if waiter.request.Operation != "send" || waiter.request.Transport != "direct" || waiter.request.PeerMode == "tcp" || waiter.offered || response.Completed != 0 {
			return errors.New("unexpected peer offer")
		}
		waiter.offered = true
	} else if response.State == "ready" {
		if waiter.request.Operation != "prepare" || waiter.ready {
			return errors.New("unexpected rsync ready response")
		}
		waiter.ready = true
	} else if response.State == "progress" {
		if waiter.request.Operation != "send" {
			return errors.New("unexpected rsync progress response")
		}
	} else {
		if response.State == "received" && !waiter.ready {
			return errors.New("rsync receiver was not ready")
		}
		waiter.terminal = true
	}
	select {
	case waiter.updates <- response:
	default:
		if response.State != "progress" {
			<-waiter.updates
			waiter.updates <- response
		}
	}
	return nil
}

func waitRsyncResponse(ctx context.Context, connection *agentConnection, waiter *coreRsyncWaiter) (protocol.FileRsyncResponse, error) {
	select {
	case response := <-waiter.updates:
		return response, nil
	case <-ctx.Done():
		return protocol.FileRsyncResponse{}, ctx.Err()
	case <-connection.ctx.Done():
		return protocol.FileRsyncResponse{}, errors.New("Agent control connection ended")
	}
}

type coreFileTransferRun struct {
	sessionID string
	nodeID    string
	cancel    context.CancelCauseFunc
}

func (s *Server) beginFileTransferRun(parent context.Context, current *session, nodeID, taskID string) (context.Context, func()) {
	ctx, cancel := context.WithCancelCause(parent)
	run := &coreFileTransferRun{sessionID: current.ID, nodeID: nodeID, cancel: cancel}
	s.fileTransferMu.Lock()
	s.fileTransferRuns[taskID] = run
	s.fileTransferMu.Unlock()
	stop := context.AfterFunc(s.agentContext, func() { cancel(errors.New("Core stopped")) })
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if !s.dashboardSessionStillValid(ctx, current.ID) {
					cancel(taskstate.ErrCancellationRequested)
					return
				}
			}
		}
	}()
	cleanup := func() {
		stop()
		cancel(context.Canceled)
		<-done
		s.fileTransferMu.Lock()
		delete(s.fileTransferRuns, taskID)
		s.fileTransferMu.Unlock()
		readCtx, cancelRead := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancelRead()
		if task, err := s.tasks.Get(readCtx, nodeID, taskID); err == nil && !taskstate.IsTerminal(task.Status) {
			_ = s.signalFileTransferCancellation(task)
		}
	}
	return ctx, cleanup
}

func (s *Server) cancelFileTransferSession(sessionID string) {
	s.fileTransferMu.Lock()
	defer s.fileTransferMu.Unlock()
	for _, run := range s.fileTransferRuns {
		if run.sessionID == sessionID {
			run.cancel(taskstate.ErrCancellationRequested)
		}
	}
}

type coreRsyncRelay struct {
	descriptor  protocol.FileTransferDescriptor
	sessionID   string
	source      *agentConnection
	target      *agentConnection
	grants      map[string]protocol.FileStreamGrant
	secrets     map[string][]byte
	mu          sync.Mutex
	used        map[string]bool
	connections map[string]*websocket.Conn
	paired      chan struct{}
	ctx         context.Context
	cancel      context.CancelFunc
}

func (s *Server) newRsyncRelay(ctx context.Context, current *session, descriptor protocol.FileTransferDescriptor, source, target *agentConnection, sender, receiver protocol.FileStreamGrant) (*coreRsyncRelay, func()) {
	ctx, cancel := context.WithCancel(ctx)
	relay := &coreRsyncRelay{descriptor: descriptor, sessionID: current.ID, source: source, target: target, grants: map[string]protocol.FileStreamGrant{"sender": sender, "receiver": receiver},
		secrets: map[string][]byte{"sender": auth.DigestToken(sender.Secret), "receiver": auth.DigestToken(receiver.Secret)}, used: make(map[string]bool), connections: make(map[string]*websocket.Conn), paired: make(chan struct{}), ctx: ctx, cancel: cancel}
	s.fileTransferMu.Lock()
	s.fileRsyncRelays[sender.SessionID] = relay
	s.fileTransferMu.Unlock()
	stopSource := context.AfterFunc(source.ctx, cancel)
	stopTarget := context.AfterFunc(target.ctx, cancel)
	done := make(chan struct{})
	go func() {
		defer close(done)
		timer := time.NewTimer(time.Until(sender.ExpiresAt))
		defer timer.Stop()
		select {
		case <-relay.paired:
		case <-ctx.Done():
			return
		case <-timer.C:
			cancel()
			return
		}
		relay.mu.Lock()
		a, b := relay.connections["sender"], relay.connections["receiver"]
		relay.mu.Unlock()
		copies := make(chan error, 2)
		go func() { copies <- filetransport.Forward(ctx, a, b) }()
		go func() { copies <- filetransport.Forward(ctx, b, a) }()
		<-copies
		cancel()
		_ = a.CloseNow()
		_ = b.CloseNow()
		<-copies
	}()
	cleanup := func() {
		cancel()
		stopSource()
		stopTarget()
		relay.mu.Lock()
		for _, conn := range relay.connections {
			_ = conn.CloseNow()
		}
		relay.mu.Unlock()
		<-done
		s.fileTransferMu.Lock()
		delete(s.fileRsyncRelays, sender.SessionID)
		s.fileTransferMu.Unlock()
	}
	return relay, cleanup
}

func (s *Server) handleFileRsyncData(w http.ResponseWriter, r *http.Request) {
	secret, ok := authorizationCredential(r.Header.Get("Authorization"), "Task")
	if r.Method != http.MethodGet || r.URL.RawQuery != "" || !ok {
		http.Error(w, "file task credential rejected", http.StatusUnauthorized)
		return
	}
	id, role := r.Header.Get("X-NodeDance-Stream"), r.Header.Get("X-NodeDance-Direction")
	s.fileTransferMu.Lock()
	relay := s.fileRsyncRelays[id]
	s.fileTransferMu.Unlock()
	if relay == nil || relay.ctx.Err() != nil || !s.dashboardSessionStillValid(r.Context(), relay.sessionID) {
		http.Error(w, "file task credential rejected", http.StatusUnauthorized)
		return
	}
	s.agentConnectionsMu.Lock()
	active := s.agentConnections[relay.source.agentID] == relay.source && s.agentConnections[relay.target.agentID] == relay.target
	s.agentConnectionsMu.Unlock()
	task, err := s.tasks.Get(r.Context(), relay.descriptor.TargetNodeID, relay.descriptor.TaskID)
	if !active || err != nil || task.Status != taskstate.Running || !reflect.DeepEqual(task.Intent.File, &relay.descriptor.File) || !relay.source.capabilityEnabled(protocol.CapabilityFiles) || !relay.target.capabilityEnabled(protocol.CapabilityFiles) {
		http.Error(w, "file task credential rejected", http.StatusUnauthorized)
		return
	}
	relay.mu.Lock()
	grant, exists := relay.grants[role]
	if !exists || relay.used[role] || !grant.ExpiresAt.After(time.Now()) || !grant.MatchesHeaders(r.Header) || subtle.ConstantTimeCompare(auth.DigestToken(secret), relay.secrets[role]) != 1 {
		relay.mu.Unlock()
		http.Error(w, "file task credential rejected", http.StatusUnauthorized)
		return
	}
	relay.used[role] = true
	relay.mu.Unlock()
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{CompressionMode: websocket.CompressionDisabled})
	if err != nil {
		return
	}
	defer conn.CloseNow()
	relay.mu.Lock()
	if relay.ctx.Err() != nil {
		relay.mu.Unlock()
		return
	}
	relay.connections[role] = conn
	if len(relay.connections) == 2 {
		close(relay.paired)
	}
	relay.mu.Unlock()
	<-relay.ctx.Done()
}
