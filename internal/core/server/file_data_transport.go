package server

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/CST-Cat/NodeDance/internal/core/auth"
	"github.com/CST-Cat/NodeDance/internal/protocol"
	"github.com/coder/websocket"
)

func newFileStreamGrant(id, taskID, nodeID, role, path, binding string) (protocol.FileStreamGrant, error) {
	data := make([]byte, 32)
	if _, err := rand.Read(data); err != nil {
		return protocol.FileStreamGrant{}, err
	}
	return protocol.FileStreamGrant{SessionID: id, TaskID: taskID, NodeID: nodeID, Role: role, Path: path, Binding: binding, ExpiresAt: time.Now().Add(protocol.FileStreamLifetime), Secret: hex.EncodeToString(data)}, nil
}

func (s *Server) openFileDataConnection(ctx context.Context, connection *agentConnection, transfer *coreFileTransfer, initial protocol.FileRequest) error {
	transfer.mu.Lock()
	if transfer.closed {
		transfer.mu.Unlock()
		return errors.New("file transfer is closed")
	}
	if transfer.dataGrant != nil {
		transfer.mu.Unlock()
		return nil
	}
	binding := protocol.FileDataRequestFor(initial)
	role := "sender"
	if initial.Operation == protocol.FileUploadBegin {
		role = "receiver"
	}
	grant, err := newFileStreamGrant(transfer.requestID, initial.TaskID, connection.nodeID, role, initial.Path, protocol.FileStreamDigest(binding))
	if err != nil {
		transfer.mu.Unlock()
		return err
	}
	transfer.dataGrant = &grant
	transfer.dataSecret = auth.DigestToken(grant.Secret)
	transfer.dataReady = make(chan struct{})
	transfer.mu.Unlock()
	open := protocol.FileRequest{Operation: protocol.FileOpenData, Data: &protocol.FileDataOpen{Grant: grant, Request: binding}}
	command := protocol.Envelope{Version: protocol.CurrentVersion, Type: protocol.TypeFileRequest, Generation: connection.generation, RequestID: transfer.requestID, Payload: marshalAgentPayload(open)}
	if err := protocol.ValidateFileRequest(command, connection.generation, open); err != nil {
		return err
	}
	select {
	case connection.commands <- command:
	case <-ctx.Done():
		return ctx.Err()
	case <-connection.ctx.Done():
		return errors.New("Agent disconnected")
	}
	timer := time.NewTimer(10 * time.Second)
	defer timer.Stop()
	select {
	case <-transfer.dataReady:
		return nil
	case failure := <-transfer.updates:
		var response protocol.FileResponse
		if json.Unmarshal(failure.Payload, &response) == nil && response.Code != "" {
			return fileRemoteError{code: response.Code}
		}
		return errors.New("file data connection rejected")
	case <-transfer.done:
		return errors.New("file data connection canceled")
	case <-connection.ctx.Done():
		return errors.New("Agent disconnected")
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return errors.New("file data connection did not become ready")
	}
}

func (s *Server) sendFileEnvelope(ctx context.Context, connection *agentConnection, transfer *coreFileTransfer, envelope protocol.Envelope) error {
	transfer.dataWriteMu.Lock()
	defer transfer.dataWriteMu.Unlock()
	transfer.mu.Lock()
	closed, data, required := transfer.closed, transfer.data, transfer.dataGrant != nil
	transfer.mu.Unlock()
	if closed || connection.ctx.Err() != nil || !s.dashboardSessionStillValid(ctx, transfer.sessionID) {
		return errors.New("file transfer was canceled")
	}
	if required {
		if data == nil {
			return errors.New("file data connection is unavailable")
		}
		payload, err := json.Marshal(envelope)
		if err != nil {
			return err
		}
		writeCtx, cancel := context.WithTimeout(ctx, fileTransferIdle)
		defer cancel()
		return data.Write(writeCtx, websocket.MessageText, payload)
	}
	select {
	case connection.commands <- envelope:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-connection.ctx.Done():
		return errors.New("Agent disconnected")
	default:
		return errors.New("Agent command queue is full")
	}
}

func (s *Server) handleFileChunkData(w http.ResponseWriter, r *http.Request) {
	secret, ok := authorizationCredential(r.Header.Get("Authorization"), "Task")
	if r.Method != http.MethodGet || r.URL.RawQuery != "" || !ok {
		http.Error(w, "file task credential rejected", http.StatusUnauthorized)
		return
	}
	id, nodeID := r.Header.Get("X-NodeDance-Stream"), r.Header.Get("X-NodeDance-Node")
	s.agentConnectionsMu.Lock()
	var connection *agentConnection
	for _, candidate := range s.agentConnections {
		if candidate.nodeID == nodeID && candidate.capabilityEnabled(protocol.CapabilityFiles) && candidate.ctx.Err() == nil {
			connection = candidate
			break
		}
	}
	s.agentConnectionsMu.Unlock()
	if connection == nil {
		http.Error(w, "file task credential rejected", http.StatusUnauthorized)
		return
	}
	transfer, _ := connection.findFileTransfer(id)
	if transfer == nil || !s.dashboardSessionStillValid(r.Context(), transfer.sessionID) {
		http.Error(w, "file task credential rejected", http.StatusUnauthorized)
		return
	}
	transfer.mu.Lock()
	grant := transfer.dataGrant
	if grant == nil || transfer.closed || transfer.dataUsed || !grant.ExpiresAt.After(time.Now()) || !grant.MatchesHeaders(r.Header) ||
		connection.generation != transfer.generation || subtle.ConstantTimeCompare(auth.DigestToken(secret), transfer.dataSecret) != 1 {
		transfer.mu.Unlock()
		http.Error(w, "file task credential rejected", http.StatusUnauthorized)
		return
	}
	transfer.dataUsed = true
	grant.Secret = ""
	transfer.mu.Unlock()
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{CompressionMode: websocket.CompressionDisabled})
	if err != nil {
		return
	}
	defer conn.CloseNow()
	transfer.mu.Lock()
	if transfer.closed {
		transfer.mu.Unlock()
		return
	}
	transfer.data = conn
	close(transfer.dataReady)
	transfer.mu.Unlock()
	ctx, cancel := context.WithCancel(connection.ctx)
	defer cancel()
	stop := context.AfterFunc(ctx, func() { _ = conn.CloseNow() })
	defer stop()
	conn.SetReadLimit(protocol.MaxTextControlBytes + (64 << 10))
	for {
		kind, data, err := conn.Read(ctx)
		if err != nil {
			transfer.finish(errors.New("file data connection ended"))
			return
		}
		var envelope protocol.Envelope
		if kind != websocket.MessageText || json.Unmarshal(data, &envelope) != nil || envelope.RequestID != transfer.requestID || s.handleAgentFileMessage(connection, envelope) != nil {
			transfer.finish(errors.New("invalid Agent file data frame"))
			return
		}
	}
}
