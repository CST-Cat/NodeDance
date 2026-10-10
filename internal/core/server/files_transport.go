package server

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/CST-Cat/NodeDance/internal/protocol"
)

const (
	maxCoreFileTransfers = 4
	fileTransferIdle     = 30 * time.Second
	fileTransferQueue    = 4
)

var errFileTransferSlowConsumer = errors.New("file transfer consumer is too slow")

type coreFileTransfer struct {
	requestID  string
	nodeID     string
	sessionID  string
	generation uint64
	allowed    map[string]struct{}
	download   bool
	updates    chan protocol.Envelope
	done       chan struct{}
	mu         sync.Mutex
	lastSeq    uint64
	terminal   bool
	closed     bool
	err        error
}

func (transfer *coreFileTransfer) finish(err error) {
	if transfer == nil {
		return
	}
	transfer.mu.Lock()
	if !transfer.closed {
		transfer.finishLocked(err)
	}
	transfer.mu.Unlock()
}

func (transfer *coreFileTransfer) finishLocked(err error) {
	transfer.closed = true
	transfer.err = err
	close(transfer.done)
}

func (transfer *coreFileTransfer) error() error {
	transfer.mu.Lock()
	defer transfer.mu.Unlock()
	return transfer.err
}

func (transfer *coreFileTransfer) enqueue(envelope protocol.Envelope, operation string, terminal bool) error {
	transfer.mu.Lock()
	defer transfer.mu.Unlock()
	if transfer.closed || transfer.terminal || envelope.Sequence <= transfer.lastSeq {
		return errors.New("Agent file transfer sequence did not increase")
	}
	if operation != "" {
		if _, ok := transfer.allowed[operation]; !ok {
			return errors.New("Agent file operation does not match the request")
		}
	}
	select {
	case transfer.updates <- envelope:
		transfer.lastSeq = envelope.Sequence
		transfer.terminal = terminal
		return nil
	default:
		return errFileTransferSlowConsumer
	}
}

func (connection *agentConnection) addFileTransfer(transfer *coreFileTransfer) error {
	if connection == nil || transfer == nil || !connection.capabilityEnabled(protocol.CapabilityFiles) || connection.ctx == nil || connection.ctx.Err() != nil {
		return errors.New("Agent file service is unavailable")
	}
	connection.fileMu.Lock()
	defer connection.fileMu.Unlock()
	if connection.fileTransfers == nil || len(connection.fileTransfers) >= maxCoreFileTransfers {
		return errors.New("Agent file transfer limit reached")
	}
	if _, exists := connection.fileTransfers[transfer.requestID]; exists {
		return errors.New("duplicate file transfer request ID")
	}
	connection.fileTransfers[transfer.requestID] = transfer
	return nil
}

func (connection *agentConnection) findFileTransfer(id string) (*coreFileTransfer, bool) {
	if connection == nil {
		return nil, false
	}
	connection.fileMu.Lock()
	defer connection.fileMu.Unlock()
	transfer := connection.fileTransfers[id]
	if transfer != nil {
		return transfer, false
	}
	_, tombstone := connection.fileTombstones[id]
	return nil, tombstone
}

func (connection *agentConnection) removeFileTransfer(transfer *coreFileTransfer, err error) bool {
	if connection == nil || transfer == nil {
		return false
	}
	connection.fileMu.Lock()
	if connection.fileTransfers[transfer.requestID] != transfer {
		connection.fileMu.Unlock()
		transfer.finish(err)
		return false
	}
	transfer.mu.Lock()
	delete(connection.fileTransfers, transfer.requestID)
	connection.rememberFileTombstoneLocked(transfer.requestID)
	if !transfer.closed {
		transfer.finishLocked(err)
	}
	transfer.mu.Unlock()
	connection.fileMu.Unlock()
	return true
}

func (connection *agentConnection) rememberFileTombstoneLocked(id string) {
	if connection.fileTombstones == nil {
		connection.fileTombstones = make(map[string]struct{})
	}
	if _, exists := connection.fileTombstones[id]; exists {
		return
	}
	connection.fileTombstones[id] = struct{}{}
	connection.fileTombstoneOrder = append(connection.fileTombstoneOrder, id)
	if len(connection.fileTombstoneOrder) > 64 {
		oldest := connection.fileTombstoneOrder[0]
		connection.fileTombstoneOrder = connection.fileTombstoneOrder[1:]
		delete(connection.fileTombstones, oldest)
	}
}

func (connection *agentConnection) closeFileTransfers() {
	if connection == nil {
		return
	}
	connection.fileMu.Lock()
	for id, transfer := range connection.fileTransfers {
		connection.rememberFileTombstoneLocked(id)
		transfer.mu.Lock()
		if !transfer.closed {
			transfer.finishLocked(errors.New("Agent connection ended"))
		}
		transfer.mu.Unlock()
		delete(connection.fileTransfers, id)
	}
	connection.fileMu.Unlock()
}

// closeFileTransfersForSession revokes every active Core-to-Agent file stream
// owned by a browser Session. It marks each transfer closed while holding the
// same transfer lock used to enqueue Agent requests, so no later upload chunk
// or commit can be queued behind the cancellation.
func (s *Server) closeFileTransfersForSession(sessionID string) {
	if sessionID == "" {
		return
	}
	s.agentConnectionsMu.Lock()
	connections := make([]*agentConnection, 0, len(s.agentConnections))
	for _, connection := range s.agentConnections {
		connections = append(connections, connection)
	}
	s.agentConnectionsMu.Unlock()
	for _, connection := range connections {
		if connection == nil {
			continue
		}
		connection.fileMu.Lock()
		var canceled []*coreFileTransfer
		for id, transfer := range connection.fileTransfers {
			if transfer.sessionID != sessionID {
				continue
			}
			transfer.mu.Lock()
			if !transfer.closed {
				transfer.finishLocked(errors.New("browser Session was revoked or expired"))
			}
			transfer.mu.Unlock()
			delete(connection.fileTransfers, id)
			connection.rememberFileTombstoneLocked(id)
			canceled = append(canceled, transfer)
		}
		connection.fileMu.Unlock()
		for _, transfer := range canceled {
			_ = s.enqueueFileCancel(connection, transfer.requestID)
		}
	}
}

func (s *Server) closeAllFileTransfers(err error) {
	s.agentConnectionsMu.Lock()
	connections := make([]*agentConnection, 0, len(s.agentConnections))
	for _, connection := range s.agentConnections {
		connections = append(connections, connection)
	}
	s.agentConnectionsMu.Unlock()
	for _, connection := range connections {
		if connection == nil {
			continue
		}
		connection.fileMu.Lock()
		var canceled []*coreFileTransfer
		for id, transfer := range connection.fileTransfers {
			transfer.mu.Lock()
			if !transfer.closed {
				transfer.finishLocked(err)
			}
			transfer.mu.Unlock()
			delete(connection.fileTransfers, id)
			connection.rememberFileTombstoneLocked(id)
			canceled = append(canceled, transfer)
		}
		connection.fileMu.Unlock()
		for _, transfer := range canceled {
			_ = s.enqueueFileCancel(connection, transfer.requestID)
		}
	}
}

func (s *Server) handleAgentFileMessage(connection *agentConnection, envelope protocol.Envelope) error {
	if connection == nil || !connection.capabilityEnabled(protocol.CapabilityFiles) || len(envelope.Payload) == 0 || len(envelope.Payload) > protocol.MaxFileControlBytes {
		return errors.New("Agent file message is unavailable or too large")
	}
	transfer, tombstone := connection.findFileTransfer(envelope.RequestID)
	if transfer == nil {
		if tombstone {
			return nil
		}
		return errors.New("Agent sent a frame for an unknown file transfer")
	}
	var operation string
	var terminal bool
	switch envelope.Type {
	case protocol.TypeFileResponse:
		var response protocol.FileResponse
		if err := decodeAgentPayload(envelope.Payload, &response); err != nil || protocol.ValidateFileResponse(envelope, connection.generation, response) != nil {
			return errors.New("Agent file response is invalid")
		}
		operation = response.Operation
		terminal = response.Code != ""
	case protocol.TypeFileChunk:
		var chunk protocol.FileChunk
		if err := decodeAgentPayload(envelope.Payload, &chunk); err != nil || protocol.ValidateFileChunkEnvelope(envelope, connection.generation, chunk) != nil || !transfer.download {
			return errors.New("Agent file chunk is invalid")
		}
		operation = protocol.FileDownload
		terminal = chunk.Final
	default:
		return errors.New("Agent file message type is invalid")
	}
	if err := transfer.enqueue(envelope, operation, terminal); err != nil {
		if errors.Is(err, errFileTransferSlowConsumer) {
			connection.removeFileTransfer(transfer, err)
			_ = s.enqueueFileCancel(connection, transfer.requestID)
			return nil
		}
		return err
	}
	return nil
}

func (s *Server) enqueueFileCancel(connection *agentConnection, id string) bool {
	if connection == nil || connection.ctx == nil || connection.ctx.Err() != nil {
		return false
	}
	payload, _ := json.Marshal(protocol.FileCancel{TransferID: id})
	envelope := protocol.Envelope{Version: protocol.CurrentVersion, Type: protocol.TypeFileCancel,
		Generation: connection.generation, RequestID: id, Payload: payload}
	if protocol.ValidateFileCancel(envelope, connection.generation, protocol.FileCancel{TransferID: id}) != nil {
		return false
	}
	select {
	case connection.commands <- envelope:
		return true
	default:
		// Cancellation is mandatory for temporary files. If the shared command
		// queue cannot carry it, dropping the Agent connection forces its local
		// transfer bridge to abort every uncommitted temp file.
		if connection.cancel != nil {
			connection.cancel()
		}
		if connection.conn != nil {
			_ = connection.conn.CloseNow()
		}
		return false
	}
}

func (s *Server) activeFileConnection(ctx context.Context, nodeID string) (*agentConnection, error) {
	state, _, err := s.currentMetricsState(ctx, nodeID)
	if err != nil {
		return nil, err
	}
	if !state.Exists {
		return nil, errContainerStreamNotFound
	}
	if state.Status != "online" {
		return nil, errContainerStreamUnavailable
	}
	s.agentConnectionsMu.Lock()
	defer s.agentConnectionsMu.Unlock()
	for _, connection := range s.agentConnections {
		if connection.nodeID == nodeID && connection.generation == state.Generation && connection.capabilityEnabled(protocol.CapabilityFiles) && connection.ctx != nil && connection.ctx.Err() == nil {
			return connection, nil
		}
	}
	return nil, errContainerStreamUnavailable
}

func (s *Server) newCoreFileTransfer(ctx context.Context, nodeID string, current *session, operations []string, download bool) (*agentConnection, *coreFileTransfer, error) {
	connection, err := s.activeFileConnection(ctx, nodeID)
	if err != nil {
		return nil, nil, err
	}
	return s.newCoreFileTransferOnConnection(connection, current, operations, download, "")
}

func (s *Server) newCoreFileTransferOnConnection(connection *agentConnection, current *session, operations []string, download bool, requestID string) (*agentConnection, *coreFileTransfer, error) {
	id := requestID
	if id == "" {
		var err error
		id, err = newContainerStreamRequestID()
		if err != nil {
			return nil, nil, err
		}
	}
	allowed := make(map[string]struct{}, len(operations))
	for _, operation := range operations {
		allowed[operation] = struct{}{}
	}
	transfer := &coreFileTransfer{requestID: id, nodeID: connection.nodeID, sessionID: current.ID, generation: connection.generation,
		allowed: allowed, download: download, updates: make(chan protocol.Envelope, fileTransferQueue), done: make(chan struct{})}
	if err := connection.addFileTransfer(transfer); err != nil {
		return nil, nil, err
	}
	return connection, transfer, nil
}

func (s *Server) sendFileRequest(ctx context.Context, connection *agentConnection, transfer *coreFileTransfer, request protocol.FileRequest) error {
	if connection == nil || transfer == nil || connection.ctx.Err() != nil || connection.generation != transfer.generation || connection.nodeID != transfer.nodeID {
		return errors.New("file transfer connection generation changed")
	}
	if request.TransferID != "" && request.TransferID != transfer.requestID {
		return errors.New("file transfer identity mismatch")
	}
	payload, err := json.Marshal(request)
	if err != nil || len(payload) > protocol.MaxFileControlBytes {
		return errors.New("file request exceeds its protocol bound")
	}
	envelope := protocol.Envelope{Version: protocol.CurrentVersion, Type: protocol.TypeFileRequest, Generation: transfer.generation,
		RequestID: transfer.requestID, Payload: payload}
	if err := protocol.ValidateFileRequest(envelope, transfer.generation, request); err != nil {
		return err
	}
	transfer.mu.Lock()
	defer transfer.mu.Unlock()
	if transfer.closed {
		return errors.New("file transfer was canceled")
	}
	if !s.dashboardSessionStillValid(ctx, transfer.sessionID) {
		return errors.New("browser Session was revoked or expired")
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

func (s *Server) waitFileMessage(ctx context.Context, connection *agentConnection, transfer *coreFileTransfer) (protocol.Envelope, error) {
	timer := time.NewTimer(fileTransferIdle)
	defer timer.Stop()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		if !s.dashboardSessionStillValid(ctx, transfer.sessionID) {
			err := errors.New("browser Session was revoked or expired")
			s.closeCoreFileTransfer(connection, transfer, true, err)
			return protocol.Envelope{}, err
		}
		select {
		case envelope := <-transfer.updates:
			if !s.dashboardSessionStillValid(ctx, transfer.sessionID) {
				err := errors.New("browser Session was revoked or expired")
				s.closeCoreFileTransfer(connection, transfer, true, err)
				return protocol.Envelope{}, err
			}
			return envelope, nil
		case <-transfer.done:
			if err := transfer.error(); err != nil {
				return protocol.Envelope{}, err
			}
			return protocol.Envelope{}, errors.New("file transfer ended")
		case <-ctx.Done():
			err := ctx.Err()
			s.closeCoreFileTransfer(connection, transfer, true, err)
			return protocol.Envelope{}, err
		case <-connection.ctx.Done():
			return protocol.Envelope{}, errors.New("Agent disconnected")
		case <-timer.C:
			s.closeCoreFileTransfer(connection, transfer, true, errors.New("file transfer is idle"))
			return protocol.Envelope{}, errors.New("file transfer is idle")
		case <-ticker.C:
			// Re-check Session authorization while waiting; the transfer idle
			// deadline remains independent of the authorization polling interval.
		}
	}
}

func (s *Server) closeCoreFileTransfer(connection *agentConnection, transfer *coreFileTransfer, cancel bool, err error) {
	if connection == nil || transfer == nil {
		return
	}
	removed := connection.removeFileTransfer(transfer, err)
	if cancel && removed {
		_ = s.enqueueFileCancel(connection, transfer.requestID)
	}
}
