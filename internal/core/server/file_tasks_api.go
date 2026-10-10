package server

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"time"

	coretasks "github.com/CST-Cat/NodeDance/internal/core/tasks"
	"github.com/CST-Cat/NodeDance/internal/protocol"
	"github.com/CST-Cat/NodeDance/internal/taskstate"
)

const fileTaskPayloadLifetime = 10 * time.Minute

type pendingFileContent struct {
	nodeID  string
	digest  string
	content []byte
	expires time.Time
}

type fileTaskRequest struct {
	Action           string `json:"action"`
	Path             string `json:"path"`
	NewPath          string `json:"newPath,omitempty"`
	ExpectedVersion  string `json:"expectedVersion,omitempty"`
	DeleteConfirmed  bool   `json:"deleteConfirmed,omitempty"`
	ConfirmationPath string `json:"confirmationPath,omitempty"`
	Content          string `json:"content,omitempty"`
}

func (s *Server) handleCreateFileTask(w http.ResponseWriter, r *http.Request, current *session, nodeID string) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var request fileTaskRequest
	r.Body = http.MaxBytesReader(w, r.Body, 2*protocol.MaxTextFileBytes+8*1024)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		fileTaskBadRequest(w)
		return
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		fileTaskBadRequest(w)
		return
	}
	if !protocol.ValidVirtualFilePath(request.Path) {
		http.Error(w, "invalid file path", http.StatusBadRequest)
		return
	}
	var action protocol.TaskAction
	switch request.Action {
	case "mkdir":
		action = protocol.TaskFileMkdir
		if request.NewPath != "" || request.ExpectedVersion != "" || request.DeleteConfirmed || request.ConfirmationPath != "" || request.Content != "" {
			fileTaskBadRequest(w)
			return
		}
	case "rename":
		action = protocol.TaskFileRename
		if !protocol.ValidVirtualFilePath(request.NewPath) || request.NewPath == request.Path || request.DeleteConfirmed || request.ConfirmationPath != "" || request.Content != "" || request.ExpectedVersion != "" {
			fileTaskBadRequest(w)
			return
		}
	case "delete":
		action = protocol.TaskFileDelete
		if !request.DeleteConfirmed || request.ConfirmationPath != request.Path || request.NewPath != "" || request.ExpectedVersion != "" || request.Content != "" {
			http.Error(w, "file deletion requires exact target confirmation", http.StatusBadRequest)
			return
		}
	case "save_text":
		action = protocol.TaskFileSaveText
		if len(request.Content) > protocol.MaxTextFileBytes || request.NewPath != "" || request.DeleteConfirmed || request.ConfirmationPath != "" {
			fileTaskBadRequest(w)
			return
		}
	default:
		fileTaskBadRequest(w)
		return
	}
	key := r.Header.Get("Idempotency-Key")
	if key == "" {
		http.Error(w, "Idempotency-Key is required", http.StatusBadRequest)
		return
	}
	intent := protocol.TaskIntent{Action: action, ContainerID: protocol.FileTargetKey(request.Path)}
	intent.File = &protocol.FileTaskSpec{Path: request.Path, NewPath: request.NewPath, ExpectedVersion: request.ExpectedVersion}
	var content []byte
	if action == protocol.TaskFileDelete {
		intent.DeleteConfirmed = true
	}
	if action == protocol.TaskFileSaveText {
		content = []byte(request.Content)
		digest := sha256.Sum256(content)
		intent.File.Size = int64(len(content))
		intent.File.SHA256 = hex.EncodeToString(digest[:])
	}
	if err := protocol.ValidateTaskIntent(intent); err != nil {
		fileTaskBadRequest(w)
		return
	}
	result, err := s.enqueueFileTask(r.Context(), current, nodeID, key, intent, content)
	if err != nil {
		s.writeTaskStoreError(w, err)
		return
	}
	if result.Created {
		s.signalAgentTasks(nodeID)
	}
	status := http.StatusAccepted
	if !result.Created {
		status = http.StatusOK
	}
	writeJSON(w, status, map[string]any{"taskId": result.Task.TaskID, "status": result.Task.Status, "action": result.Task.Intent.Action})
}

func fileTaskBadRequest(w http.ResponseWriter) {
	http.Error(w, "invalid file operation", http.StatusBadRequest)
}

func (s *Server) enqueueFileTask(ctx context.Context, current *session, nodeID, key string, intent protocol.TaskIntent, content []byte) (coretasks.EnqueueResult, error) {
	connection, err := s.activeFileConnection(ctx, nodeID)
	if err != nil || connection == nil || !connection.capabilityEnabled(protocol.CapabilityFiles) {
		return coretasks.EnqueueResult{}, coretasks.ErrNodeOffline
	}
	gate := func(ctx context.Context) (bool, func(), error) {
		release, gateErr := s.lockTaskBridgeReady(nodeID, connection.generation)
		if gateErr != nil {
			return false, nil, gateErr
		}
		active := false
		for _, candidate := range s.agentConnections {
			if candidate == connection {
				active = true
				break
			}
		}
		if !active || !connection.capabilityEnabled(protocol.CapabilityFiles) || connection.ctx == nil || connection.ctx.Err() != nil {
			release()
			return false, nil, coretasks.ErrNodeOffline
		}
		return false, release, nil
	}
	withContent := intent.Action == protocol.TaskFileSaveText
	if withContent {
		s.fileContentMu.Lock()
		s.expireFileContentLocked()
	}
	result, err := s.tasks.EnqueueWithGate(ctx, coretasks.EnqueueRequest{NodeID: nodeID, IdempotencyKey: key,
		Intent: intent, ActorID: sql.NullInt64{Int64: 1, Valid: true}, RemoteAddr: current.RemoteAddr}, gate)
	if err != nil {
		if withContent {
			s.fileContentMu.Unlock()
		}
		return coretasks.EnqueueResult{}, err
	}
	if withContent && result.Task.Status == taskstate.Queued && result.Task.DeliveryState == "ready" && !result.Task.Evidence.DeliveryCommitted {
		s.storeFileContentLocked(result.Task.TaskID, nodeID, intent.File.SHA256, content)
	}
	if withContent {
		s.fileContentMu.Unlock()
	}
	return result, nil
}

func (s *Server) storeFileContentLocked(taskID, nodeID, digest string, content []byte) {
	s.expireFileContentLocked()
	if previous, ok := s.fileContents[taskID]; ok {
		clear(previous.content)
	}
	s.fileContents[taskID] = pendingFileContent{nodeID: nodeID, digest: digest, content: append([]byte(nil), content...), expires: s.now().Add(fileTaskPayloadLifetime)}
}

func (s *Server) takeFileContent(taskID, nodeID, digest string) *protocol.FileContent {
	s.fileContentMu.Lock()
	defer s.fileContentMu.Unlock()
	s.expireFileContentLocked()
	entry, ok := s.fileContents[taskID]
	if !ok {
		return nil
	}
	delete(s.fileContents, taskID)
	defer clear(entry.content)
	if entry.nodeID != nodeID || entry.digest != digest || s.now().After(entry.expires) {
		return nil
	}
	return &protocol.FileContent{SHA256: entry.digest, Content: append([]byte(nil), entry.content...)}
}

func (s *Server) expireFileContentLocked() {
	now := s.now()
	for taskID, entry := range s.fileContents {
		if !now.Before(entry.expires) {
			clear(entry.content)
			delete(s.fileContents, taskID)
		}
	}
}

func (s *Server) clearFileContent(taskID string) {
	s.fileContentMu.Lock()
	if entry, ok := s.fileContents[taskID]; ok {
		clear(entry.content)
		delete(s.fileContents, taskID)
	}
	s.fileContentMu.Unlock()
}

func (s *Server) clearAllFileContent() {
	s.fileContentMu.Lock()
	for taskID, entry := range s.fileContents {
		clear(entry.content)
		delete(s.fileContents, taskID)
	}
	s.fileContentMu.Unlock()
}

func (s *Server) handleFileUpload(w http.ResponseWriter, r *http.Request, current *session, nodeID string) {
	if r.Method != http.MethodPut {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	filePath := r.URL.Query().Get("path")
	if !protocol.ValidVirtualFilePath(filePath) {
		http.Error(w, "invalid file path", http.StatusBadRequest)
		return
	}
	key := r.Header.Get("Idempotency-Key")
	if key == "" {
		http.Error(w, "Idempotency-Key is required", http.StatusBadRequest)
		return
	}
	if !s.dashboardSessionStillValid(r.Context(), current.ID) {
		http.Error(w, "authentication required", http.StatusUnauthorized)
		return
	}
	if r.ContentLength > s.fileTransferLimit {
		http.Error(w, "file exceeds the configured transfer limit", http.StatusRequestEntityTooLarge)
		return
	}
	spool, err := os.CreateTemp(s.dataDir, ".nodedance-upload-*")
	if err != nil {
		http.Error(w, "temporary upload storage is unavailable", http.StatusInsufficientStorage)
		return
	}
	spoolName := spool.Name()
	defer func() { _ = spool.Close(); _ = os.Remove(spoolName) }()
	if err := spool.Chmod(0o600); err != nil {
		http.Error(w, "temporary upload storage is unavailable", http.StatusInsufficientStorage)
		return
	}
	hasher := sha256.New()
	limited := http.MaxBytesReader(w, r.Body, s.fileTransferLimit)
	size, err := io.Copy(io.MultiWriter(spool, hasher), limited)
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			http.Error(w, "file exceeds the configured transfer limit", http.StatusRequestEntityTooLarge)
		} else {
			http.Error(w, "upload body could not be read", http.StatusBadRequest)
		}
		return
	}
	if size > s.fileTransferLimit {
		http.Error(w, "file exceeds the configured transfer limit", http.StatusRequestEntityTooLarge)
		return
	}
	if err := spool.Sync(); err != nil {
		http.Error(w, "temporary upload storage failed", http.StatusInsufficientStorage)
		return
	}
	if _, err := spool.Seek(0, io.SeekStart); err != nil {
		http.Error(w, "temporary upload storage failed", http.StatusInsufficientStorage)
		return
	}
	digest := hex.EncodeToString(hasher.Sum(nil))
	intent := protocol.TaskIntent{Action: protocol.TaskFileUpload, ContainerID: protocol.FileTargetKey(filePath),
		File: &protocol.FileTaskSpec{Path: filePath, ExpectedVersion: r.Header.Get("X-File-Version"), Size: size, SHA256: digest}}
	if len(intent.File.ExpectedVersion) > 128 || protocol.ValidateTaskIntent(intent) != nil {
		http.Error(w, "invalid file version", http.StatusBadRequest)
		return
	}
	result, err := s.enqueueFileTask(r.Context(), current, nodeID, key, intent, nil)
	if err != nil {
		s.writeTaskStoreError(w, err)
		return
	}
	if !result.Created {
		writeJSON(w, http.StatusOK, toTaskView(result.Task))
		return
	}
	s.signalAgentTasks(nodeID)
	readyTask, taskReady, err := s.waitFileTaskReady(r.Context(), current.ID, nodeID, result.Task.TaskID)
	if err != nil {
		s.writeFileAPIError(w, err)
		return
	}
	if !taskReady {
		writeJSON(w, http.StatusOK, toTaskView(readyTask))
		return
	}
	connection, transfer, err := s.newCoreFileTransfer(r.Context(), nodeID, current, []string{protocol.FileUploadBegin, protocol.FileUploadChunk}, false)
	if err != nil {
		s.writeFileAPIError(w, err)
		return
	}
	defer s.closeCoreFileTransfer(connection, transfer, true, nil)
	begin := protocol.FileRequest{Operation: protocol.FileUploadBegin, TransferID: transfer.requestID, TaskID: result.Task.TaskID,
		Path: filePath, ExpectedVersion: intent.File.ExpectedVersion, Size: size, SHA256: digest}
	if err := s.sendFileRequest(r.Context(), connection, transfer, begin); err != nil {
		s.writeFileAPIError(w, err)
		return
	}
	beginResponse, err := s.waitFileMessage(r.Context(), connection, transfer)
	if err != nil {
		s.writeFileAPIError(w, err)
		return
	}
	if beginResponse.Type != protocol.TypeFileResponse {
		http.Error(w, "Agent did not start the upload", http.StatusBadGateway)
		return
	}
	var ready protocol.FileResponse
	if decodeAgentPayload(beginResponse.Payload, &ready) != nil || ready.Operation != protocol.FileUploadBegin {
		http.Error(w, "Agent upload response is invalid", http.StatusBadGateway)
		return
	}
	if ready.Code != "" {
		s.writeFileAPIError(w, fileRemoteError{code: ready.Code})
		return
	}
	if ready.Size != size {
		http.Error(w, "Agent upload target is inconsistent", http.StatusBadGateway)
		return
	}
	if err := streamCoreFileUpload(r.Context(), s, current, connection, transfer, spool, size, digest); err != nil {
		s.writeFileAPIError(w, err)
		return
	}
	s.closeCoreFileTransfer(connection, transfer, false, nil)
	resultTask, err := s.waitFileTask(r.Context(), current.ID, nodeID, result.Task.TaskID)
	if err != nil {
		s.writeFileAPIError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toTaskView(resultTask))
}

func streamCoreFileUpload(ctx context.Context, s *Server, current *session, connection *agentConnection, transfer *coreFileTransfer, spool *os.File, size int64, digest string) error {
	hasher := sha256.New()
	buffer := make([]byte, protocol.MaxFileChunkBytes)
	var total int64
	var sequence uint64
	for {
		n, readErr := io.ReadFull(spool, buffer)
		if readErr != nil && !errors.Is(readErr, io.EOF) && !errors.Is(readErr, io.ErrUnexpectedEOF) {
			return readErr
		}
		if n > 0 {
			_, _ = hasher.Write(buffer[:n])
			total += int64(n)
			sequence++
			chunk := protocol.FileChunk{TransferID: transfer.requestID, Sequence: sequence,
				Data: base64.StdEncoding.EncodeToString(buffer[:n])}
			if err := sendCoreFileChunk(ctx, s, current, connection, transfer, chunk); err != nil {
				return err
			}
			if err := waitFileUploadAck(ctx, s, current, connection, transfer, sequence); err != nil {
				return err
			}
		}
		if errors.Is(readErr, io.EOF) || errors.Is(readErr, io.ErrUnexpectedEOF) {
			if total != size || hex.EncodeToString(hasher.Sum(nil)) != digest {
				return errors.New("temporary upload content changed before transfer")
			}
			sequence++
			final := protocol.FileChunk{TransferID: transfer.requestID, Sequence: sequence, Final: true, SHA256: digest}
			if err := sendCoreFileChunk(ctx, s, current, connection, transfer, final); err != nil {
				return err
			}
			return waitFileUploadAck(ctx, s, current, connection, transfer, sequence)
		}
	}
}

func sendCoreFileChunk(ctx context.Context, s *Server, current *session, connection *agentConnection, transfer *coreFileTransfer, chunk protocol.FileChunk) error {
	payload, err := json.Marshal(chunk)
	if err != nil || len(payload) > protocol.MaxFileControlBytes {
		return errors.New("file upload chunk exceeds its protocol bound")
	}
	envelope := protocol.Envelope{Version: protocol.CurrentVersion, Type: protocol.TypeFileChunk,
		Generation: transfer.generation, RequestID: transfer.requestID, Sequence: chunk.Sequence, Payload: payload}
	if protocol.ValidateFileChunkEnvelope(envelope, transfer.generation, chunk) != nil {
		return errors.New("file upload chunk is invalid")
	}
	transfer.mu.Lock()
	defer transfer.mu.Unlock()
	if transfer.closed || connection.ctx.Err() != nil || !s.dashboardSessionStillValid(ctx, transfer.sessionID) {
		return errors.New("file upload was canceled")
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

func waitFileUploadAck(ctx context.Context, s *Server, current *session, connection *agentConnection, transfer *coreFileTransfer, sequence uint64) error {
	envelope, err := s.waitFileMessage(ctx, connection, transfer)
	if err != nil {
		return err
	}
	if envelope.Type != protocol.TypeFileResponse {
		return errors.New("Agent upload acknowledgement is invalid")
	}
	var response protocol.FileResponse
	if decodeAgentPayload(envelope.Payload, &response) != nil || response.Operation != protocol.FileUploadChunk || response.AckSequence != sequence {
		return errors.New("Agent upload acknowledgement is invalid")
	}
	if response.Code != "" {
		return fileRemoteError{code: response.Code}
	}
	if !s.dashboardSessionStillValid(ctx, current.ID) {
		return errors.New("browser Session was revoked or expired")
	}
	return nil
}

func (s *Server) waitFileTask(ctx context.Context, sessionID, nodeID, taskID string) (coretasks.Task, error) {
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		if !s.dashboardSessionStillValid(ctx, sessionID) {
			return coretasks.Task{}, errors.New("browser Session was revoked or expired")
		}
		task, err := s.tasks.Get(ctx, nodeID, taskID)
		if err != nil {
			return coretasks.Task{}, err
		}
		if taskstate.IsTerminal(task.Status) {
			return task, nil
		}
		select {
		case <-ctx.Done():
			return coretasks.Task{}, ctx.Err()
		case <-ticker.C:
		}
	}
}

func (s *Server) waitFileTaskReady(ctx context.Context, sessionID, nodeID, taskID string) (coretasks.Task, bool, error) {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		if !s.dashboardSessionStillValid(ctx, sessionID) {
			return coretasks.Task{}, false, errors.New("browser Session was revoked or expired")
		}
		task, err := s.tasks.Get(ctx, nodeID, taskID)
		if err != nil {
			return coretasks.Task{}, false, err
		}
		if task.Status == taskstate.Running {
			return task, true, nil
		}
		if taskstate.IsTerminal(task.Status) || task.Status == taskstate.Unknown {
			return task, false, nil
		}
		select {
		case <-ctx.Done():
			return coretasks.Task{}, false, ctx.Err()
		case <-ticker.C:
		}
	}
}
