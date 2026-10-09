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
	"mime"
	"net/http"
	"os"
	"path"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/CST-Cat/NodeDance/internal/core/audit"
	corefiletasks "github.com/CST-Cat/NodeDance/internal/core/filetasks"
	"github.com/CST-Cat/NodeDance/internal/protocol"
	"github.com/CST-Cat/NodeDance/internal/taskstate"
)

type fileWriteRequest struct {
	Path        string `json:"path"`
	NewPath     string `json:"newPath,omitempty"`
	ConfirmPath string `json:"confirmPath,omitempty"`
	Version     string `json:"version,omitempty"`
	Text        string `json:"text,omitempty"`
}

type fileRemoteError struct{ code string }

func (e fileRemoteError) Error() string { return "Agent file operation failed: " + e.code }

var errFileOperationUnknown = errors.New("file operation result is not confirmed")

type fileAuditMetadata struct {
	action     string
	target     string
	transferID string
}

func newFileAuditMetadata(nodeID, transferID string, request protocol.FileRequest) (fileAuditMetadata, error) {
	action := ""
	switch request.Operation {
	case protocol.FileMkdir:
		action = "file_mkdir"
	case protocol.FileRename:
		action = "file_rename"
	case protocol.FileDelete:
		action = "file_delete"
	case protocol.FileSaveText:
		action = "file_save_text"
	case protocol.FileUploadBegin, protocol.FileUploadChunk, protocol.FileUploadCommit:
		action = "file_upload"
	default:
		return fileAuditMetadata{}, errors.New("file operation is not auditable as a write")
	}
	target, err := json.Marshal([]string{request.Path})
	if err != nil {
		return fileAuditMetadata{}, err
	}
	if request.NewPath != "" {
		target, err = json.Marshal([]string{request.Path, request.NewPath})
		if err != nil {
			return fileAuditMetadata{}, err
		}
	}
	return fileAuditMetadata{action: action, target: audit.FileTarget(nodeID, transferID, string(target)), transferID: transferID}, nil
}

func (s *Server) handleFilesAPI(w http.ResponseWriter, r *http.Request, current *session) bool {
	nodeID, operation, ok := filesRoute(r.URL.Path)
	if !ok {
		return false
	}
	switch operation {
	case "list", "stat":
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return true
		}
		filePath := r.URL.Query().Get("path")
		if filePath == "" {
			filePath = "/"
		}
		requestOperation := protocol.FileList
		if operation == "stat" {
			requestOperation = protocol.FileStat
		}
		response, _, err := s.runFileOperation(r.Context(), current, nodeID, protocol.FileRequest{Operation: requestOperation, Path: filePath}, false, "")
		if err != nil {
			s.writeFileAPIError(w, err)
			return true
		}
		if operation == "list" {
			writeJSON(w, http.StatusOK, map[string]any{"entries": response.Entries, "path": filePath})
		} else if response.Entry != nil {
			writeJSON(w, http.StatusOK, response.Entry)
		} else {
			http.Error(w, "Agent did not return file metadata", http.StatusBadGateway)
		}
		return true
	case "text":
		s.handleFileText(w, r, current, nodeID)
		return true
	case "directories", "rename", "delete":
		s.handleFileMutation(w, r, current, nodeID, operation)
		return true
	case "upload":
		s.handleFileUpload(w, r, current, nodeID)
		return true
	case "download":
		s.handleFileDownload(w, r, current, nodeID)
		return true
	default:
		http.NotFound(w, r)
		return true
	}
}

func filesRoute(requestPath string) (nodeID, operation string, ok bool) {
	if !strings.HasPrefix(requestPath, "/api/v1/nodes/") {
		return "", "", false
	}
	parts := strings.Split(strings.TrimPrefix(requestPath, "/api/v1/nodes/"), "/")
	if len(parts) < 2 || !validUUID(parts[0]) || parts[1] != "files" {
		return "", "", false
	}
	if len(parts) == 2 {
		return parts[0], "list", true
	}
	if len(parts) == 3 {
		switch parts[2] {
		case "stat", "text", "directories", "rename", "delete", "upload", "download":
			return parts[0], parts[2], true
		}
	}
	return "", "", false
}

func (s *Server) handleFileText(w http.ResponseWriter, r *http.Request, current *session, nodeID string) {
	switch r.Method {
	case http.MethodGet:
		filePath := r.URL.Query().Get("path")
		response, _, err := s.runFileOperation(r.Context(), current, nodeID, protocol.FileRequest{Operation: protocol.FileReadText, Path: filePath}, false, "")
		if err != nil {
			s.writeFileAPIError(w, err)
			return
		}
		data, err := base64.StdEncoding.DecodeString(response.Text)
		if err != nil || len(data) > protocol.MaxTextFileBytes || !utf8.Valid(data) {
			http.Error(w, "Agent returned invalid text content", http.StatusBadGateway)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"path": filePath, "text": string(data), "version": response.Version, "size": len(data)})
	case http.MethodPut:
		var request fileWriteRequest
		if !decodeFileJSON(w, r, &request) {
			return
		}
		if request.Path == "" || request.Version == "" || len(request.Text) > protocol.MaxTextFileBytes || !utf8.ValidString(request.Text) || strings.ContainsRune(request.Text, '\x00') {
			http.Error(w, "invalid text edit", http.StatusBadRequest)
			return
		}
		encoded := base64.StdEncoding.EncodeToString([]byte(request.Text))
		if len(encoded) > protocol.MaxFileControlBytes-2048 {
			http.Error(w, "text file exceeds the editor limit", http.StatusRequestEntityTooLarge)
			return
		}
		fileRequest := protocol.FileRequest{Operation: protocol.FileSaveText, Path: request.Path, ExpectedVersion: request.Version, Text: encoded}
		task, created, err := s.createFileTaskIdempotently(r.Context(), current, nodeID, fileRequest, r.Header.Get("Idempotency-Key"), 0)
		if err != nil {
			s.writeFileTaskCreateError(w, err)
			return
		}
		if !created {
			writeReusedFileTask(w, task)
			return
		}
		response, taskID, err := s.runFileOperation(r.Context(), current, nodeID, fileRequest, true, task.TaskID)
		if err != nil {
			s.writeFileTaskMutationError(w, nodeID, taskID, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"taskId": taskID, "transferId": taskID, "status": "succeeded", "entry": response.Entry, "backupPath": response.BackupPath})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleFileMutation(w http.ResponseWriter, r *http.Request, current *session, nodeID, operation string) {
	if r.Method != http.MethodPost && !(operation == "delete" && r.Method == http.MethodDelete) {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var input fileWriteRequest
	if !decodeJSON(w, r, &input) {
		return
	}
	var request protocol.FileRequest
	switch operation {
	case "directories":
		request = protocol.FileRequest{Operation: protocol.FileMkdir, Path: input.Path}
	case "rename":
		request = protocol.FileRequest{Operation: protocol.FileRename, Path: input.Path, NewPath: input.NewPath}
	case "delete":
		if input.Path == "" || input.ConfirmPath != input.Path {
			http.Error(w, "delete confirmation must match the exact target path", http.StatusBadRequest)
			return
		}
		request = protocol.FileRequest{Operation: protocol.FileDelete, Path: input.Path, Confirmed: true}
	}
	task, created, err := s.createFileTaskIdempotently(r.Context(), current, nodeID, request, r.Header.Get("Idempotency-Key"), 0)
	if err != nil {
		s.writeFileTaskCreateError(w, err)
		return
	}
	if !created {
		writeReusedFileTask(w, task)
		return
	}
	response, taskID, err := s.runFileOperation(r.Context(), current, nodeID, request, true, task.TaskID)
	if err != nil {
		s.writeFileTaskMutationError(w, nodeID, taskID, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"taskId": taskID, "transferId": taskID, "status": "succeeded", "entry": response.Entry})
}

func (s *Server) createFileTaskIdempotently(ctx context.Context, current *session, nodeID string, request protocol.FileRequest, key string, expectedSize int64) (corefiletasks.Task, bool, error) {
	operation := request.Operation
	contentDigest := ""
	switch request.Operation {
	case protocol.FileMkdir:
		operation = corefiletasks.OperationMkdir
	case protocol.FileRename:
		operation = corefiletasks.OperationRename
	case protocol.FileDelete:
		operation = corefiletasks.OperationDelete
	case protocol.FileSaveText:
		operation = corefiletasks.OperationSaveText
		content, err := base64.StdEncoding.DecodeString(request.Text)
		if err != nil {
			return corefiletasks.Task{}, false, corefiletasks.ErrInvalidRequest
		}
		digest := sha256.Sum256(content)
		contentDigest = hex.EncodeToString(digest[:])
	case protocol.FileUploadBegin:
		operation = corefiletasks.OperationUpload
		contentDigest = request.ExpectedSHA256
	default:
		return corefiletasks.Task{}, false, corefiletasks.ErrInvalidRequest
	}
	requestDigest, err := corefiletasks.DigestIntent(corefiletasks.Intent{Operation: operation, TargetPath: request.Path, NewPath: request.NewPath,
		ExpectedVersion: request.ExpectedVersion, ExpectedSize: expectedSize, ContentSHA256: contentDigest})
	if err != nil {
		return corefiletasks.Task{}, false, err
	}
	taskID, err := newContainerStreamRequestID()
	if err != nil {
		return corefiletasks.Task{}, false, err
	}
	return s.fileTasks.CreateIdempotent(ctx, corefiletasks.CreateRequest{TaskID: taskID, NodeID: nodeID,
		IdempotencyKey: strings.TrimSpace(key), RequestDigest: requestDigest, Operation: operation,
		TargetPath: request.Path, NewPath: request.NewPath, ActorID: sql.NullInt64{Int64: 1, Valid: true}, RemoteAddr: current.RemoteAddr})
}

func (s *Server) writeFileTaskCreateError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, corefiletasks.ErrIdempotencyConflict):
		http.Error(w, "Idempotency-Key was already used for a different file operation", http.StatusConflict)
	case errors.Is(err, corefiletasks.ErrInvalidRequest):
		http.Error(w, "a valid Idempotency-Key is required for file writes", http.StatusBadRequest)
	default:
		http.Error(w, "file operation could not be persisted", http.StatusInternalServerError)
	}
}

func writeReusedFileTask(w http.ResponseWriter, task corefiletasks.Task) {
	statusCode := http.StatusOK
	if task.Status == taskstate.Queued || task.Status == taskstate.Running || task.Status == taskstate.Unknown {
		statusCode = http.StatusAccepted
	}
	writeJSON(w, statusCode, map[string]any{"taskId": task.TaskID, "transferId": task.TaskID, "status": task.Status, "resultCode": task.ResultCode})
}

func verifyUploadReplayBody(body io.Reader, expectedSize int64, expectedDigest string, limit int64) error {
	if expectedSize < 0 || expectedSize > limit || !validSHA256(expectedDigest) {
		return errors.New("upload replay identity is invalid")
	}
	hash := sha256.New()
	count, err := io.CopyBuffer(hash, io.LimitReader(body, expectedSize+1), make([]byte, protocol.MaxFileChunkBytes))
	if err != nil || count != expectedSize || hex.EncodeToString(hash.Sum(nil)) != expectedDigest {
		return errors.New("upload body does not match the original Idempotency-Key request")
	}
	return nil
}

func (s *Server) runFileOperation(ctx context.Context, current *session, nodeID string, request protocol.FileRequest, write bool, taskID string) (protocol.FileResponse, string, error) {
	allowed := []string{request.Operation}
	var connection *agentConnection
	var transfer *coreFileTransfer
	var err error
	if write {
		connection, transfer, err = s.newCoreFileTransferWithID(ctx, nodeID, current, allowed, false, taskID, false)
	} else {
		connection, transfer, err = s.newCoreFileTransfer(ctx, nodeID, current, allowed, false)
	}
	if err != nil {
		if write {
			_ = s.resolveFileTask(nodeID, taskID, taskstate.Failed, "not_dispatched", current.RemoteAddr)
		}
		return protocol.FileResponse{}, taskID, err
	}
	if write && !connection.fileJournalEnabled {
		s.closeCoreFileTransfer(connection, transfer, false, errors.New("Agent does not support durable file task reconciliation"))
		_ = s.resolveFileTask(nodeID, taskID, taskstate.Failed, "not_dispatched", current.RemoteAddr)
		return protocol.FileResponse{}, taskID, errContainerStreamUnavailable
	}
	transferID := transfer.requestID
	cancelOnExit := false
	defer func() {
		s.closeCoreFileTransfer(connection, transfer, cancelOnExit, nil)
	}()
	if write {
		cancelOnExit = true
		if err := validateCoreFileRequest(transfer, request); err != nil {
			_ = s.resolveFileTask(nodeID, transferID, taskstate.Failed, "not_dispatched", current.RemoteAddr)
			return protocol.FileResponse{}, transferID, err
		}
		if err := s.fileTasks.MarkDispatched(ctx, nodeID, transferID, sql.NullInt64{Int64: 1, Valid: true}, current.RemoteAddr); err != nil {
			_ = s.resolveFileTask(nodeID, transferID, taskstate.Failed, "not_dispatched", current.RemoteAddr)
			return protocol.FileResponse{}, transferID, errors.New("file operation could not be dispatched safely")
		}
	}
	if err := s.sendFileRequest(ctx, connection, transfer, request); err != nil {
		if write {
			_ = s.resolveFileTask(nodeID, transferID, taskstate.Unknown, "result_pending", current.RemoteAddr)
		}
		return protocol.FileResponse{}, transferID, err
	}
	envelope, err := s.waitFileMessage(ctx, connection, transfer)
	if err != nil {
		if write {
			_ = s.resolveFileTask(nodeID, transferID, taskstate.Unknown, "result_pending", current.RemoteAddr)
		}
		return protocol.FileResponse{}, transferID, err
	}
	var response protocol.FileResponse
	if err := decodeAgentPayload(envelope.Payload, &response); err != nil {
		if write {
			_ = s.resolveFileTask(nodeID, transferID, taskstate.Unknown, "result_pending", current.RemoteAddr)
		}
		return protocol.FileResponse{}, transferID, errors.New("Agent file response could not be decoded")
	}
	if response.Operation != request.Operation {
		if write {
			_ = s.resolveFileTask(nodeID, transferID, taskstate.Unknown, "result_pending", current.RemoteAddr)
		}
		return protocol.FileResponse{}, transferID, errors.New("Agent file response operation did not match request")
	}
	// A matching terminal Agent response confirms that execution started. For
	// synchronous mutations the store records the start and terminal events in
	// one transaction using the response receipt time; uploads have a distinct
	// begin acknowledgment and enter running before their chunk stream.
	cancelOnExit = false
	if response.Code != "" {
		if write {
			status := fileAgentFailureStatus(request.Operation, response.Code)
			resultCode := "agent_rejected"
			if status == taskstate.Unknown {
				resultCode = "result_pending"
				if request.Operation == protocol.FileDelete || response.Code == "result_unknown" {
					resultCode = "mutation_uncertain"
				}
			}
			if err := s.resolveFileTask(nodeID, transferID, status, resultCode, current.RemoteAddr); err != nil {
				return protocol.FileResponse{}, transferID, err
			}
			if status == taskstate.Unknown {
				return protocol.FileResponse{}, transferID, errFileOperationUnknown
			}
		}
		return response, transferID, fileRemoteError{code: response.Code}
	}
	if write && !validFileWriteResult(request, response) {
		_ = s.resolveFileTask(nodeID, transferID, taskstate.Unknown, "result_pending", current.RemoteAddr)
		return protocol.FileResponse{}, transferID, errors.New("Agent file operation result could not be verified")
	}
	if write {
		if err := s.resolveFileTask(nodeID, transferID, taskstate.Succeeded, "verified", current.RemoteAddr); err != nil {
			return protocol.FileResponse{}, transferID, err
		}
	}
	return response, transferID, nil
}

func validateCoreFileRequest(transfer *coreFileTransfer, request protocol.FileRequest) error {
	if transfer == nil {
		return errors.New("file transfer is unavailable")
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
	return nil
}

func validFileWriteResult(request protocol.FileRequest, response protocol.FileResponse) bool {
	if response.Operation != request.Operation || response.Code != "" {
		return false
	}
	switch request.Operation {
	case protocol.FileMkdir, protocol.FileRename, protocol.FileDelete:
		return true
	case protocol.FileSaveText:
		return response.Entry != nil && response.Entry.Path == request.Path && response.Entry.Version != ""
	case protocol.FileUploadCommit:
		return response.Entry != nil && response.Entry.Path != "" && response.Entry.Size >= 0
	default:
		return false
	}
}

func fileAgentFailureStatus(operation, code string) taskstate.Status {
	if code == "result_unknown" {
		return taskstate.Unknown
	}
	if operation == protocol.FileDelete && code != "invalid_path" && code != "confirmation_required" {
		// RemoveAll can remove part of a directory tree before returning an
		// error. Only validation errors are proof that no entry was changed.
		return taskstate.Unknown
	}
	// mkdir and rename are single atomic filesystem operations. SaveText and
	// upload_commit explicitly report result_unknown for errors after rename;
	// other allowlisted errors are returned before their atomic replacement.
	return taskstate.Failed
}

func (s *Server) resolveFileTask(nodeID, taskID string, status taskstate.Status, resultCode, remoteAddr string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return s.fileTasks.Resolve(ctx, nodeID, taskID, status, resultCode, sql.NullInt64{Int64: 1, Valid: true}, remoteAddr)
}

func (s *Server) handleFileUpload(w http.ResponseWriter, r *http.Request, current *session, nodeID string) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	filePath := r.URL.Query().Get("path")
	if filePath == "" {
		http.Error(w, "destination path is required", http.StatusBadRequest)
		return
	}
	if r.ContentLength < 0 {
		http.Error(w, "Content-Length is required for file uploads", http.StatusLengthRequired)
		return
	}
	if r.ContentLength > s.fileTransferLimit {
		http.Error(w, "file exceeds configured transfer limit", http.StatusRequestEntityTooLarge)
		return
	}
	providedDigest := strings.ToLower(strings.TrimSpace(r.Header.Get("X-File-SHA256")))
	if !validSHA256(providedDigest) {
		http.Error(w, "X-File-SHA256 is required and must be a lowercase SHA-256 digest", http.StatusBadRequest)
		return
	}
	begin := protocol.FileRequest{Operation: protocol.FileUploadBegin, Path: filePath,
		ExpectedVersion: r.Header.Get("X-File-Version"), ExpectedSize: r.ContentLength, ExpectedSHA256: providedDigest}
	task, created, err := s.createFileTaskIdempotently(r.Context(), current, nodeID, begin, r.Header.Get("Idempotency-Key"), r.ContentLength)
	if err != nil {
		s.writeFileTaskCreateError(w, err)
		return
	}
	if !created {
		if err := verifyUploadReplayBody(r.Body, r.ContentLength, providedDigest, s.fileTransferLimit); err != nil {
			http.Error(w, "upload body does not match the original Idempotency-Key request", http.StatusConflict)
			return
		}
		writeReusedFileTask(w, task)
		return
	}
	connection, transfer, err := s.newCoreFileTransferWithID(r.Context(), nodeID, current,
		[]string{protocol.FileUploadBegin, protocol.FileUploadChunk, protocol.FileUploadCommit}, false, task.TaskID, true)
	if err != nil {
		_ = s.resolveFileTask(nodeID, task.TaskID, taskstate.Failed, "not_dispatched", current.RemoteAddr)
		s.writeFileTaskMutationError(w, nodeID, task.TaskID, err)
		return
	}
	transferID := transfer.requestID
	cancelOnExit := true
	defer func() { s.closeCoreFileTransfer(connection, transfer, cancelOnExit, nil) }()
	begin.TransferID = transferID
	if err := validateCoreFileRequest(transfer, begin); err != nil {
		_ = s.resolveFileTask(nodeID, transferID, taskstate.Failed, "not_dispatched", current.RemoteAddr)
		s.writeFileTaskMutationError(w, nodeID, transferID, err)
		return
	}
	if err := s.fileTasks.MarkDispatched(r.Context(), nodeID, transferID, sql.NullInt64{Int64: 1, Valid: true}, current.RemoteAddr); err != nil {
		_ = s.resolveFileTask(nodeID, transferID, taskstate.Failed, "not_dispatched", current.RemoteAddr)
		s.writeFileTaskMutationError(w, nodeID, transferID, err)
		return
	}
	if err := s.sendFileRequest(r.Context(), connection, transfer, begin); err != nil {
		_ = s.resolveFileTask(nodeID, transferID, taskstate.Unknown, "result_pending", current.RemoteAddr)
		s.writeFileTaskMutationError(w, nodeID, transferID, err)
		return
	}
	if _, err := s.waitFileResponseForOperation(r.Context(), connection, transfer, protocol.FileUploadBegin); err != nil {
		var remote fileRemoteError
		if errors.As(err, &remote) {
			_ = s.resolveFileTask(nodeID, transferID, taskstate.Failed, "agent_rejected", current.RemoteAddr)
		} else {
			_ = s.resolveFileTask(nodeID, transferID, taskstate.Unknown, "result_pending", current.RemoteAddr)
		}
		s.writeFileTaskMutationError(w, nodeID, transferID, err)
		return
	}
	if err := s.fileTasks.MarkRunning(r.Context(), nodeID, transferID, sql.NullInt64{Int64: 1, Valid: true}, current.RemoteAddr); err != nil {
		_ = s.resolveFileTask(nodeID, transferID, taskstate.Unknown, "result_pending", current.RemoteAddr)
		s.writeFileTaskMutationError(w, nodeID, transferID, err)
		return
	}
	failBeforeCommit := func(err error, cancellationRequested bool) {
		s.cancelFileTransfer(connection, transfer, r, current)
		cancelConfirmed := s.waitFileCancelAck(connection, transfer, 2*time.Second)
		status, resultCode := taskstate.Unknown, "result_pending"
		if cancelConfirmed && cancellationRequested {
			status, resultCode = taskstate.Canceled, "cancel_confirmed"
		} else if cancelConfirmed {
			status, resultCode = taskstate.Failed, "not_committed"
		}
		_ = s.resolveFileTask(nodeID, transferID, status, resultCode, current.RemoteAddr)
		s.writeFileTaskMutationError(w, nodeID, transferID, err)
	}
	var bodyActivity atomic.Int64
	bodyActivity.Store(time.Now().UnixNano())
	stopBodyWatch := s.watchFileUploadBody(r, current, connection, transfer, &bodyActivity)
	defer stopBodyWatch()
	buffer := make([]byte, protocol.MaxFileChunkBytes)
	hash := sha256.New()
	var total int64
	var chunkSequence uint64
	for {
		if !s.dashboardSessionStillValid(r.Context(), current.ID) {
			failBeforeCommit(errors.New("authentication required"), true)
			return
		}
		n, readErr := io.ReadFull(r.Body, buffer)
		if n > 0 {
			bodyActivity.Store(time.Now().UnixNano())
		}
		if !s.dashboardSessionStillValid(r.Context(), current.ID) {
			failBeforeCommit(errors.New("authentication required"), true)
			return
		}
		if n > 0 {
			chunk := buffer[:n]
			if total+int64(n) > s.fileTransferLimit || total+int64(n) > r.ContentLength {
				failBeforeCommit(errors.New("file exceeds configured transfer limit"), false)
				return
			}
			_, _ = hash.Write(chunk)
			total += int64(n)
			chunkSequence++
			request := protocol.FileRequest{Operation: protocol.FileUploadChunk, TransferID: transferID, Sequence: chunkSequence,
				Data: base64.StdEncoding.EncodeToString(chunk)}
			if err := s.sendFileRequest(r.Context(), connection, transfer, request); err != nil {
				failBeforeCommit(err, errors.Is(r.Context().Err(), context.Canceled))
				return
			}
			response, err := s.waitFileResponseForOperation(r.Context(), connection, transfer, protocol.FileUploadChunk)
			if err != nil || response.Completed != total {
				if err == nil {
					err = errors.New("Agent did not confirm the uploaded chunk")
				}
				failBeforeCommit(err, errors.Is(r.Context().Err(), context.Canceled))
				return
			}
		}
		if errors.Is(readErr, io.EOF) || errors.Is(readErr, io.ErrUnexpectedEOF) {
			break
		}
		if readErr != nil {
			failBeforeCommit(errors.New("file upload was interrupted"), errors.Is(r.Context().Err(), context.Canceled))
			return
		}
	}
	if total != r.ContentLength {
		failBeforeCommit(errors.New("file upload length did not match Content-Length"), false)
		return
	}
	stopBodyWatch()
	actualDigest := hex.EncodeToString(hash.Sum(nil))
	if actualDigest != providedDigest {
		failBeforeCommit(errors.New("file SHA-256 did not match the supplied digest"), false)
		return
	}
	if !s.dashboardSessionStillValid(r.Context(), current.ID) {
		failBeforeCommit(errors.New("authentication required"), true)
		return
	}
	commit := protocol.FileRequest{Operation: protocol.FileUploadCommit, TransferID: transferID, ExpectedSize: total, ExpectedSHA256: actualDigest}
	if err := s.sendFileRequest(r.Context(), connection, transfer, commit); err != nil {
		_ = s.resolveFileTask(nodeID, transferID, taskstate.Unknown, "result_pending", current.RemoteAddr)
		s.writeFileTaskMutationError(w, nodeID, transferID, err)
		return
	}
	response, err := s.waitFileResponseForOperation(r.Context(), connection, transfer, protocol.FileUploadCommit)
	if err != nil {
		var remote fileRemoteError
		if errors.As(err, &remote) {
			status := fileAgentFailureStatus(protocol.FileUploadCommit, remote.code)
			resultCode := "agent_rejected"
			if status == taskstate.Unknown {
				resultCode = "mutation_uncertain"
			}
			_ = s.resolveFileTask(nodeID, transferID, status, resultCode, current.RemoteAddr)
		} else {
			_ = s.resolveFileTask(nodeID, transferID, taskstate.Unknown, "result_pending", current.RemoteAddr)
		}
		s.writeFileTaskMutationError(w, nodeID, transferID, err)
		return
	}
	if response.Entry == nil || response.Entry.Path != filePath || response.Entry.Size != total || response.Entry.Version == "" {
		_ = s.resolveFileTask(nodeID, transferID, taskstate.Unknown, "result_pending", current.RemoteAddr)
		s.writeFileTaskMutationError(w, nodeID, transferID, errFileOperationUnknown)
		return
	}
	cancelOnExit = false
	if err := s.resolveFileTask(nodeID, transferID, taskstate.Succeeded, "verified", current.RemoteAddr); err != nil {
		s.writeFileTaskMutationError(w, nodeID, transferID, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"taskId": transferID, "transferId": transferID, "status": "succeeded", "entry": response.Entry, "sha256": actualDigest})
}

func (s *Server) watchFileUploadBody(r *http.Request, current *session, connection *agentConnection, transfer *coreFileTransfer, activity *atomic.Int64) func() {
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		cancelFor := func(err error) {
			_ = r.Body.Close()
			s.closeCoreFileTransfer(connection, transfer, true, err)
		}
		for {
			select {
			case <-stop:
				return
			case <-r.Context().Done():
				cancelFor(r.Context().Err())
				return
			case <-ticker.C:
				if !s.dashboardSessionStillValid(context.Background(), current.ID) {
					cancelFor(errors.New("browser Session was revoked or expired"))
					return
				}
				if time.Since(time.Unix(0, activity.Load())) >= fileTransferIdle {
					cancelFor(errors.New("file upload body is idle"))
					return
				}
			}
		}
	}()
	var stopOnce sync.Once
	return func() {
		stopOnce.Do(func() {
			close(stop)
			<-done
		})
	}
}

func (s *Server) handleFileDownload(w http.ResponseWriter, r *http.Request, current *session, nodeID string) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	filePath := r.URL.Query().Get("path")
	if filePath == "" {
		http.Error(w, "file path is required", http.StatusBadRequest)
		return
	}
	connection, transfer, err := s.newCoreFileTransfer(r.Context(), nodeID, current, []string{protocol.FileDownload}, true)
	if err != nil {
		s.writeFileAPIError(w, err)
		return
	}
	defer s.closeCoreFileTransfer(connection, transfer, true, nil)
	request := protocol.FileRequest{Operation: protocol.FileDownload, Path: filePath, TransferID: transfer.requestID}
	if err := s.sendFileRequest(r.Context(), connection, transfer, request); err != nil {
		s.writeFileAPIError(w, err)
		return
	}
	first, err := s.waitFileMessage(r.Context(), connection, transfer)
	if err != nil {
		s.writeFileAPIError(w, err)
		return
	}
	var response protocol.FileResponse
	if decodeAgentPayload(first.Payload, &response) != nil || response.Entry == nil {
		http.Error(w, "Agent download metadata is invalid", http.StatusBadGateway)
		return
	}
	if response.Code != "" {
		s.writeFileAPIError(w, fileRemoteError{code: response.Code})
		return
	}
	if response.Operation != protocol.FileDownload || response.Size != response.Entry.Size || response.Size < 0 || response.Size > s.fileTransferLimit {
		http.Error(w, "Agent download metadata is inconsistent", http.StatusBadGateway)
		return
	}
	spool, err := os.CreateTemp(s.dataDir, ".nodedance-download-*")
	if err != nil {
		http.Error(w, "temporary download storage is unavailable", http.StatusInsufficientStorage)
		return
	}
	spoolName := spool.Name()
	defer func() {
		_ = spool.Close()
		_ = os.Remove(spoolName)
	}()
	if err := spool.Chmod(0o600); err != nil {
		http.Error(w, "temporary download storage is unavailable", http.StatusInsufficientStorage)
		return
	}
	name := path.Base(response.Entry.Path)
	if name == "." || name == "/" || name == "" {
		name = "download"
	}
	hash := sha256.New()
	var total int64
	var finalDigest string
	for {
		envelope, waitErr := s.waitFileMessage(r.Context(), connection, transfer)
		if waitErr != nil {
			s.writeFileAPIError(w, waitErr)
			return
		}
		if envelope.Type == protocol.TypeFileResponse {
			var failure protocol.FileResponse
			if decodeAgentPayload(envelope.Payload, &failure) == nil && failure.Code != "" {
				s.writeFileAPIError(w, fileRemoteError{code: failure.Code})
			} else {
				http.Error(w, "Agent download stream failed", http.StatusBadGateway)
			}
			return
		}
		var chunk protocol.FileChunk
		if decodeAgentPayload(envelope.Payload, &chunk) != nil || protocol.ValidateFileChunkEnvelope(envelope, transfer.generation, chunk) != nil {
			http.Error(w, "Agent download chunk is invalid", http.StatusBadGateway)
			return
		}
		data, decodeErr := protocol.DecodeFileChunk(chunk)
		if decodeErr != nil {
			http.Error(w, "Agent download chunk is invalid", http.StatusBadGateway)
			return
		}
		if chunk.Final {
			if total != response.Size || hex.EncodeToString(hash.Sum(nil)) != chunk.SHA256 {
				http.Error(w, "download integrity verification failed", http.StatusBadGateway)
				return
			}
			finalDigest = chunk.SHA256
			break
		}
		if len(data) == 0 || total+int64(len(data)) > response.Size {
			http.Error(w, "Agent download length is inconsistent", http.StatusBadGateway)
			return
		}
		if !s.dashboardSessionStillValid(r.Context(), current.ID) {
			s.closeCoreFileTransfer(connection, transfer, true, errors.New("browser Session was revoked or expired"))
			http.Error(w, "authentication required", http.StatusUnauthorized)
			return
		}
		n, writeErr := spool.Write(data)
		if writeErr != nil || n != len(data) {
			http.Error(w, "temporary download storage failed", http.StatusInsufficientStorage)
			return
		}
		_, _ = hash.Write(data)
		total += int64(n)
		ack := protocol.FileRequest{Operation: protocol.FileDownloadAck, TransferID: transfer.requestID, Sequence: chunk.Sequence}
		if err := s.sendFileRequest(r.Context(), connection, transfer, ack); err != nil {
			s.writeFileAPIError(w, err)
			return
		}
	}
	if finalDigest == "" || !s.dashboardSessionStillValid(r.Context(), current.ID) {
		if r.Context().Err() == nil {
			http.Error(w, "authentication required", http.StatusUnauthorized)
		}
		return
	}
	if err := spool.Sync(); err != nil {
		http.Error(w, "temporary download storage failed", http.StatusInsufficientStorage)
		return
	}
	if _, err := spool.Seek(0, io.SeekStart); err != nil {
		http.Error(w, "temporary download storage failed", http.StatusInsufficientStorage)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", strconv.FormatInt(response.Size, 10))
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": name}))
	w.Header().Set("X-File-Version", response.Entry.Version)
	w.Header().Set("X-File-SHA256", finalDigest)
	w.Header().Set("X-NodeDance-Transfer-ID", transfer.requestID)
	w.WriteHeader(http.StatusOK)
	if _, err := io.CopyN(w, spool, response.Size); err != nil {
		return
	}
}

func (s *Server) waitFileResponse(ctx context.Context, connection *agentConnection, transfer *coreFileTransfer) (protocol.FileResponse, error) {
	return s.waitFileResponseForOperation(ctx, connection, transfer, "")
}

func (s *Server) waitFileResponseForOperation(ctx context.Context, connection *agentConnection, transfer *coreFileTransfer, operation string) (protocol.FileResponse, error) {
	envelope, err := s.waitFileMessage(ctx, connection, transfer)
	if err != nil {
		return protocol.FileResponse{}, err
	}
	if envelope.Type != protocol.TypeFileResponse {
		return protocol.FileResponse{}, errors.New("Agent returned an unexpected file frame")
	}
	var response protocol.FileResponse
	if err := decodeAgentPayload(envelope.Payload, &response); err != nil {
		return protocol.FileResponse{}, err
	}
	if operation != "" && response.Operation != operation {
		return response, errors.New("Agent returned a file response for a different operation")
	}
	if response.Code != "" {
		return response, fileRemoteError{code: response.Code}
	}
	return response, nil
}

func (s *Server) cancelFileTransfer(connection *agentConnection, transfer *coreFileTransfer, r *http.Request, current *session) {
	if transfer == nil || connection == nil {
		return
	}
	_ = r
	_ = current
	s.closeCoreFileTransfer(connection, transfer, true, errors.New("file transfer canceled"))
}

func (s *Server) recordFileAudit(ctx context.Context, current *session, metadata fileAuditMetadata, outcome string) error {
	tx, err := s.store.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := audit.Record(ctx, tx, audit.Event{OccurredAt: s.now(), Action: metadata.action, Outcome: outcome,
		ActorID: sql.NullInt64{Int64: 1, Valid: true}, RemoteAddr: current.RemoteAddr,
		Target: audit.Target{Kind: audit.TargetFile, ID: metadata.target}}); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Server) finishFileAudit(_ context.Context, current *session, metadata fileAuditMetadata, outcome string) error {
	if metadata.transferID == "" {
		return nil
	}
	// Preserve the result of a dispatched write even when the browser
	// disconnects or its Session is revoked and the request context is canceled.
	// The accepted event was already durable before Agent delivery.
	resultCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return s.recordFileAudit(resultCtx, current, metadata, outcome)
}

func (s *Server) writeFileAuditFailure(w http.ResponseWriter, metadata fileAuditMetadata) {
	writeJSON(w, http.StatusServiceUnavailable, map[string]any{
		"transferId": metadata.transferID, "status": "succeeded", "auditStatus": "failed",
		"message": "文件操作已完成，但审计记录失败；请先检查目标后再决定是否重试。",
	})
}

func (s *Server) writeFileMutationError(w http.ResponseWriter, r *http.Request, current *session, metadata fileAuditMetadata, err error) {
	status := fileHTTPStatus(err)
	outcome := "failed"
	var body map[string]any
	var remote fileRemoteError
	if status == http.StatusAccepted || errors.Is(err, context.Canceled) || errors.Is(err, errFileOperationUnknown) ||
		(metadata.transferID != "" && status >= http.StatusInternalServerError && !errors.As(err, &remote)) {
		status = http.StatusAccepted
		outcome = "unknown"
		body = map[string]any{"transferId": metadata.transferID, "status": "unknown", "message": "结果待确认"}
	} else {
		body = map[string]any{"transferId": metadata.transferID, "status": "failed", "message": filePublicMessage(err)}
	}
	if auditErr := s.finishFileAudit(r.Context(), current, metadata, outcome); auditErr != nil {
		body["auditStatus"] = "failed"
	}
	writeJSON(w, status, body)
}

// writeFileTaskMutationError reports the durable task's actual persisted
// state. Final task status and audit outcome are committed together; if that
// transaction fails, the response does not claim success or failure.
func (s *Server) writeFileTaskMutationError(w http.ResponseWriter, nodeID, taskID string, err error) {
	status := fileHTTPStatus(err)
	var remote fileRemoteError
	uncertain := status == http.StatusAccepted || errors.Is(err, context.Canceled) || errors.Is(err, errFileOperationUnknown) ||
		(status >= http.StatusInternalServerError && !errors.As(err, &remote))
	if uncertain {
		status = http.StatusAccepted
	}
	body := map[string]any{"taskId": taskID, "transferId": taskID, "message": filePublicMessage(err)}
	if taskID != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		task, lookupErr := s.fileTasks.Get(ctx, nodeID, taskID)
		cancel()
		if lookupErr == nil {
			body["status"] = task.Status
			if task.Status == taskstate.Unknown {
				status = http.StatusAccepted
			}
		} else if errors.Is(lookupErr, corefiletasks.ErrNotFound) {
			delete(body, "taskId")
			delete(body, "transferId")
		}
	}
	if _, exists := body["status"]; !exists && uncertain {
		body["status"] = taskstate.Unknown
		body["message"] = "结果待确认；请查询任务状态"
	} else if _, exists := body["status"]; !exists {
		body["status"] = "failed"
	}
	writeJSON(w, status, body)
}

func (s *Server) writeFileAPIError(w http.ResponseWriter, err error) {
	if fileHTTPStatus(err) == http.StatusAccepted {
		writeJSON(w, http.StatusAccepted, map[string]any{"status": "unknown", "message": "结果待确认"})
		return
	}
	http.Error(w, filePublicMessage(err), fileHTTPStatus(err))
}

func fileHTTPStatus(err error) int {
	if errors.Is(err, errFileOperationUnknown) {
		return http.StatusAccepted
	}
	var remote fileRemoteError
	if errors.As(err, &remote) {
		switch remote.code {
		case "conflict", "destination_exists":
			return http.StatusConflict
		case "invalid_path":
			return http.StatusBadRequest
		case "limit_exceeded", "directory_too_large":
			return http.StatusRequestEntityTooLarge
		case "not_text":
			return http.StatusUnsupportedMediaType
		case "confirmation_required":
			return http.StatusBadRequest
		case "not_found":
			return http.StatusNotFound
		case "permission_denied":
			return http.StatusForbidden
		default:
			return http.StatusServiceUnavailable
		}
	}
	if errors.Is(err, errContainerStreamNotFound) {
		return http.StatusNotFound
	}
	if errors.Is(err, errContainerStreamUnavailable) {
		return http.StatusServiceUnavailable
	}
	if errors.Is(err, context.DeadlineExceeded) || strings.Contains(err.Error(), "Agent disconnected") || strings.Contains(err.Error(), "idle") {
		return http.StatusAccepted
	}
	if errors.Is(err, context.Canceled) {
		return http.StatusRequestTimeout
	}
	return http.StatusBadGateway
}

func filePublicMessage(err error) string {
	var remote fileRemoteError
	if errors.As(err, &remote) {
		return "Agent 文件操作失败（" + remote.code + "）"
	}
	if err == nil {
		return "文件操作失败"
	}
	return "文件操作暂时不可用"
}

func decodeFileJSON(w http.ResponseWriter, r *http.Request, target any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, protocol.MaxTextFileBytes+16*1024)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			http.Error(w, "text edit exceeds the limit", http.StatusRequestEntityTooLarge)
		} else {
			http.Error(w, "invalid file request", http.StatusBadRequest)
		}
		return false
	}
	var extra any
	if !errors.Is(decoder.Decode(&extra), io.EOF) {
		http.Error(w, "invalid file request", http.StatusBadRequest)
		return false
	}
	return true
}

func validSHA256(value string) bool {
	if len(value) != sha256.Size*2 || strings.ToLower(value) != value {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}
