package server

import (
	"context"
	"errors"
	"net/http"
	"path"
	"strconv"
	"sync"
	"time"

	"github.com/CST-Cat/NodeDance/internal/protocol"
	"github.com/CST-Cat/NodeDance/internal/taskstate"
)

type fileTransferResult struct {
	SourcePath string `json:"sourcePath"`
	TargetPath string `json:"targetPath"`
	Size       int64  `json:"size"`
	SHA256     string `json:"sha256"`
	TaskID     string `json:"taskId"`
}

type fileTransferFailure struct {
	SourcePath string `json:"sourcePath"`
	Error      string `json:"error"`
}

func (s *Server) handleFileTransfer(w http.ResponseWriter, r *http.Request, current *session, sourceNodeID string) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var request protocol.FileTransferRequest
	if !decodeJSON(w, r, &request) {
		return
	}
	key := r.Header.Get("Idempotency-Key")
	if key == "" {
		http.Error(w, "Idempotency-Key is required", http.StatusBadRequest)
		return
	}
	if !validUUID(request.TargetNodeID) || len(request.Paths) == 0 || len(request.Paths) > 100 || !protocol.ValidVirtualFilePath(request.TargetPath) {
		http.Error(w, "invalid file transfer", http.StatusBadRequest)
		return
	}
	seen := map[string]struct{}{}
	for _, sourcePath := range request.Paths {
		if !protocol.ValidVirtualFilePath(sourcePath) {
			http.Error(w, "invalid file transfer", http.StatusBadRequest)
			return
		}
		if _, duplicate := seen[sourcePath]; duplicate {
			http.Error(w, "invalid file transfer", http.StatusBadRequest)
			return
		}
		seen[sourcePath] = struct{}{}
	}
	target, err := s.runFileOperation(r.Context(), current, request.TargetNodeID, protocol.FileRequest{Operation: protocol.FileStat, Path: request.TargetPath})
	if err != nil {
		s.writeFileAPIError(w, err)
		return
	}
	if target.Entry == nil || target.Entry.Kind != "directory" {
		http.Error(w, "transfer target is not a directory", http.StatusBadRequest)
		return
	}
	results := make([]*fileTransferResult, len(request.Paths))
	failures := make([]*fileTransferFailure, len(request.Paths))
	workers := 4
	if sourceNodeID == request.TargetNodeID {
		workers = 2
	}
	if workers > len(request.Paths) {
		workers = len(request.Paths)
	}
	jobs := make(chan int)
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for index := range jobs {
				result, transferErr := s.transferFile(r.Context(), current, sourceNodeID, request.Paths[index], request.TargetNodeID, request.TargetPath, key+"-"+strconv.Itoa(index))
				if transferErr != nil {
					failures[index] = &fileTransferFailure{SourcePath: request.Paths[index], Error: transferErr.Error()}
				} else {
					results[index] = &result
				}
			}
		}()
	}
	for index := range request.Paths {
		jobs <- index
	}
	close(jobs)
	wg.Wait()
	transferred := make([]fileTransferResult, 0, len(request.Paths))
	failed := make([]fileTransferFailure, 0)
	for index := range request.Paths {
		if results[index] != nil {
			transferred = append(transferred, *results[index])
		}
		if failures[index] != nil {
			failed = append(failed, *failures[index])
		}
	}
	payload := map[string]any{"transferred": transferred}
	if len(failed) > 0 {
		payload["failed"] = failed
	}
	writeJSON(w, http.StatusOK, payload)
}

func (s *Server) transferFile(ctx context.Context, current *session, sourceNodeID, sourcePath, targetNodeID, targetDirectory, key string) (fileTransferResult, error) {
	targetPath := path.Join(targetDirectory, path.Base(sourcePath))
	hashed, err := s.runFileOperation(ctx, current, sourceNodeID, protocol.FileRequest{Operation: protocol.FileHash, Path: sourcePath})
	if err != nil {
		return fileTransferResult{}, err
	}
	if hashed.Entry == nil || hashed.Entry.Kind != "file" || hashed.SHA256 == "" || hashed.Size != hashed.Entry.Size || hashed.Size < 0 || hashed.Size > s.fileTransferLimit {
		return fileTransferResult{}, errors.New("source file metadata is inconsistent")
	}
	size, digest := hashed.Size, hashed.SHA256
	var expectedVersion string
	resolved := false
	if existing, found, lookupErr := s.tasks.FindByIdempotency(ctx, targetNodeID, key); lookupErr == nil && found && existing.Intent.File != nil &&
		existing.Intent.File.Path == targetPath && existing.Intent.File.Size == size && existing.Intent.File.SHA256 == digest {
		switch existing.Status {
		case taskstate.Succeeded:
			return fileTransferResult{SourcePath: sourcePath, TargetPath: targetPath, Size: size, SHA256: digest, TaskID: existing.TaskID}, nil
		case taskstate.Failed, taskstate.TimedOut, taskstate.Canceled:
			return fileTransferResult{}, errors.New("target upload task did not succeed: " + string(existing.Status))
		default:
			expectedVersion, resolved = existing.Intent.File.ExpectedVersion, true
		}
	}
	for _, nodeID := range []string{sourceNodeID, targetNodeID} {
		connection, err := s.activeFileConnection(ctx, nodeID)
		if err != nil {
			return fileTransferResult{}, err
		}
		if !connection.capabilityEnabled(protocol.CapabilityFileRsyncStream) {
			return fileTransferResult{}, errors.New("Agent does not support rsync stream transfer")
		}
	}
	if !resolved {
		expectedVersion, err = s.targetFileVersion(ctx, current, targetNodeID, targetPath)
		if err != nil {
			return fileTransferResult{}, err
		}
	}
	intent := protocol.TaskIntent{Action: protocol.TaskFileUpload, ContainerID: protocol.FileTargetKey(targetPath),
		File: &protocol.FileTaskSpec{Path: targetPath, ExpectedVersion: expectedVersion, Size: size, SHA256: digest}}
	if err := protocol.ValidateTaskIntent(intent); err != nil {
		return fileTransferResult{}, err
	}
	enqueued, err := s.enqueueFileTask(ctx, current, targetNodeID, key, intent, nil)
	if err != nil {
		return fileTransferResult{}, err
	}
	taskID := enqueued.Task.TaskID
	result := fileTransferResult{SourcePath: sourcePath, TargetPath: targetPath, Size: size, SHA256: digest, TaskID: taskID}
	if !enqueued.Created {
		final, err := s.waitFileTask(ctx, current.ID, targetNodeID, taskID)
		if err != nil {
			return fileTransferResult{}, err
		}
		if final.Status != taskstate.Succeeded {
			return fileTransferResult{}, errors.New("target upload task did not succeed: " + string(final.Status))
		}
		return result, nil
	}
	s.signalAgentTasks(targetNodeID)
	readyTask, ready, err := s.waitFileTaskReady(ctx, current.ID, targetNodeID, taskID)
	if err != nil {
		return fileTransferResult{}, err
	}
	if !ready {
		if readyTask.Status == taskstate.Succeeded {
			return result, nil
		}
		return fileTransferResult{}, errors.New("target upload task did not become ready: " + string(readyTask.Status))
	}
	targetConnection, receiveTransfer, err := s.newCoreFileTransfer(ctx, targetNodeID, current, []string{protocol.FileRsyncReceive}, false)
	if err != nil {
		return fileTransferResult{}, err
	}
	defer s.closeCoreFileTransfer(targetConnection, receiveTransfer, false, nil)
	sourceConnection, sendTransfer, err := s.newCoreFileTransfer(ctx, sourceNodeID, current, []string{protocol.FileRsyncSend}, false)
	if err != nil {
		return fileTransferResult{}, err
	}
	defer s.closeCoreFileTransfer(sourceConnection, sendTransfer, true, nil)
	streamID := taskID
	s.registerStreamRoute(streamID, sourceConnection, targetConnection)
	defer s.unregisterStreamRoute(streamID)
	receive := protocol.FileRequest{Operation: protocol.FileRsyncReceive, TransferID: receiveTransfer.requestID, TaskID: taskID,
		Path: targetPath, ExpectedVersion: expectedVersion, Size: size, SHA256: digest}
	if err := s.sendFileRequest(ctx, targetConnection, receiveTransfer, receive); err != nil {
		return fileTransferResult{}, err
	}
	receiveEnvelope, err := s.waitFileMessage(ctx, targetConnection, receiveTransfer)
	if err != nil {
		return fileTransferResult{}, err
	}
	if err := decodeFileOperationResponse(receiveEnvelope, protocol.FileRsyncReceive); err != nil {
		return fileTransferResult{}, err
	}
	send := protocol.FileRequest{Operation: protocol.FileRsyncSend, TransferID: sendTransfer.requestID, TaskID: taskID, Path: sourcePath}
	if err := s.sendFileRequest(ctx, sourceConnection, sendTransfer, send); err != nil {
		s.abortRsyncReceive(ctx, current, targetNodeID, taskID)
		return fileTransferResult{}, err
	}
	prelimEnvelope, err := s.waitStreamResponse(ctx, sourceConnection, sendTransfer)
	if err != nil {
		s.abortRsyncReceive(ctx, current, targetNodeID, taskID)
		return fileTransferResult{}, err
	}
	serverArgs, err := decodeRsyncPrelimResponse(prelimEnvelope)
	if err != nil {
		s.abortRsyncReceive(ctx, current, targetNodeID, taskID)
		return fileTransferResult{}, err
	}
	if _, err := s.runFileOperation(ctx, current, targetNodeID, protocol.FileRequest{Operation: protocol.FileRsyncStart, TaskID: taskID, Args: serverArgs}); err != nil {
		s.abortRsyncReceive(ctx, current, targetNodeID, taskID)
		return fileTransferResult{}, err
	}
	if _, err := s.runFileOperation(ctx, current, sourceNodeID, protocol.FileRequest{Operation: protocol.FileRsyncGo, TaskID: taskID}); err != nil {
		s.abortRsyncReceive(ctx, current, targetNodeID, taskID)
		return fileTransferResult{}, err
	}
	sendEnvelope, err := s.waitStreamResponse(ctx, sourceConnection, sendTransfer)
	if err != nil {
		s.abortRsyncReceive(ctx, current, targetNodeID, taskID)
		return fileTransferResult{}, err
	}
	if err := decodeFileOperationResponse(sendEnvelope, protocol.FileRsyncSend); err != nil {
		s.abortRsyncReceive(ctx, current, targetNodeID, taskID)
		return fileTransferResult{}, err
	}
	final, err := s.waitFileTask(ctx, current.ID, targetNodeID, taskID)
	if err != nil {
		return fileTransferResult{}, err
	}
	if final.Status != taskstate.Succeeded {
		return fileTransferResult{}, errors.New("target upload task did not succeed: " + string(final.Status))
	}
	return result, nil
}

func (s *Server) targetFileVersion(ctx context.Context, current *session, nodeID, targetPath string) (string, error) {
	stated, err := s.runFileOperation(ctx, current, nodeID, protocol.FileRequest{Operation: protocol.FileStat, Path: targetPath})
	if err != nil {
		var remote fileRemoteError
		if errors.As(err, &remote) && remote.code == "not_found" {
			return "", nil
		}
		return "", err
	}
	if stated.Entry == nil {
		return "", errors.New("target file metadata is invalid")
	}
	if stated.Entry.Kind != "file" {
		return "", errors.New("transfer target exists and is not a file")
	}
	return stated.Entry.Version, nil
}

func decodeRsyncPrelimResponse(envelope protocol.Envelope) ([]string, error) {
	if envelope.Type != protocol.TypeFileResponse {
		return nil, errors.New("Agent returned an unexpected file frame")
	}
	var response protocol.FileResponse
	if decodeAgentPayload(envelope.Payload, &response) != nil || response.Operation != protocol.FileRsyncSend {
		return nil, errors.New("Agent file response does not match request")
	}
	if response.Code != "" {
		return nil, fileRemoteError{code: response.Code, message: response.Message}
	}
	if len(response.Args) == 0 {
		return nil, errors.New("Agent did not provide rsync server arguments")
	}
	return response.Args, nil
}

func decodeFileOperationResponse(envelope protocol.Envelope, operation string) error {
	if envelope.Type != protocol.TypeFileResponse {
		return errors.New("Agent returned an unexpected file frame")
	}
	var response protocol.FileResponse
	if decodeAgentPayload(envelope.Payload, &response) != nil || response.Operation != operation {
		return errors.New("Agent file response does not match request")
	}
	if response.Code != "" {
		return fileRemoteError{code: response.Code, message: response.Message}
	}
	return nil
}

func (s *Server) abortRsyncReceive(ctx context.Context, current *session, targetNodeID, taskID string) {
	abortCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	_, _ = s.runFileOperation(abortCtx, current, targetNodeID, protocol.FileRequest{Operation: protocol.FileRsyncAbort, TaskID: taskID})
}
