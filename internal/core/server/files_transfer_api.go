package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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
	Transport  string `json:"transport"`
	SourcePath string `json:"sourcePath"`
	TargetPath string `json:"targetPath"`
	Size       int64  `json:"size"`
	SHA256     string `json:"sha256"`
	TaskID     string `json:"taskId"`
}

type fileTransferFailure struct {
	TaskID     string `json:"taskId,omitempty"`
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
	if !validUUID(request.TargetNodeID) || len(request.Paths) == 0 || len(request.Paths) > 100 || !protocol.ValidVirtualFilePath(request.TargetPath) || !protocol.ValidFileBandwidth(request.BandwidthLimit) {
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
				result, transferErr := s.transferFile(r.Context(), current, sourceNodeID, request.Paths[index], request.TargetNodeID, request.TargetPath, key+"-"+strconv.Itoa(index), request.BandwidthLimit, "")
				if transferErr != nil {
					failures[index] = &fileTransferFailure{SourcePath: request.Paths[index], Error: transferErr.Error()}
					var taskErr fileTransferTaskError
					if errors.As(transferErr, &taskErr) {
						failures[index].TaskID = taskErr.taskID
					}
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

func (s *Server) transferFile(ctx context.Context, current *session, sourceNodeID, sourcePath, targetNodeID, targetDirectory, key string, bandwidthLimit int64, retryOfTaskID string) (_ fileTransferResult, failure error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()
	targetPath := path.Join(targetDirectory, path.Base(sourcePath))
	hashed, err := s.runFileOperation(ctx, current, sourceNodeID, protocol.FileRequest{Operation: protocol.FileHash, Path: sourcePath})
	if err != nil {
		return fileTransferResult{}, err
	}
	if hashed.Entry == nil || hashed.Entry.Kind != "file" || hashed.SHA256 == "" || hashed.Size != hashed.Entry.Size || hashed.Size < 0 || hashed.Size > s.fileTransferLimit {
		return fileTransferResult{}, errors.New("source file metadata is inconsistent")
	}
	size, digest := hashed.Size, hashed.SHA256
	transferSpec := &protocol.FileTransferSpec{SourceNodeID: sourceNodeID, SourcePath: sourcePath, SourceVersion: hashed.Entry.Version, BandwidthLimit: bandwidthLimit, RetryOfTaskID: retryOfTaskID}
	var expectedVersion string
	resolved := false
	if retryOfTaskID != "" {
		previous, err := s.tasks.Get(ctx, targetNodeID, retryOfTaskID)
		if err != nil || previous.Intent.File == nil || previous.Intent.File.Transfer == nil || previous.Intent.File.Size != size || previous.Intent.File.SHA256 != digest ||
			previous.Intent.File.Path != targetPath || previous.Intent.File.Transfer.SourceNodeID != sourceNodeID || previous.Intent.File.Transfer.SourcePath != sourcePath ||
			previous.Intent.File.Transfer.SourceVersion != transferSpec.SourceVersion || previous.Intent.File.Transfer.BandwidthLimit != bandwidthLimit ||
			previous.Status != taskstate.Failed && previous.Status != taskstate.Canceled && previous.Status != taskstate.TimedOut {
			return fileTransferResult{}, errors.New("retry source or destination differs from the original transfer")
		}
		expectedVersion, resolved = previous.Intent.File.ExpectedVersion, true
	}
	if existing, found, lookupErr := s.tasks.FindByIdempotency(ctx, targetNodeID, key); lookupErr == nil && found && existing.Intent.File != nil &&
		existing.Intent.File.Path == targetPath && existing.Intent.File.Size == size && existing.Intent.File.SHA256 == digest && existing.Intent.File.Transfer != nil &&
		existing.Intent.File.Transfer.SourceNodeID == sourceNodeID && existing.Intent.File.Transfer.SourcePath == sourcePath && existing.Intent.File.Transfer.BandwidthLimit == bandwidthLimit &&
		existing.Intent.File.Transfer.RetryOfTaskID == retryOfTaskID {
		transferSpec.SourceVersion = existing.Intent.File.Transfer.SourceVersion
		switch existing.Status {
		case taskstate.Succeeded:
			return fileTransferResult{SourcePath: sourcePath, TargetPath: targetPath, Size: size, SHA256: digest, TaskID: existing.TaskID, Transport: existing.Result.ObservedState}, nil
		case taskstate.Failed, taskstate.TimedOut, taskstate.Canceled:
			return fileTransferResult{}, fileTransferTaskError{taskID: existing.TaskID, cause: errors.New("target upload task did not succeed: " + string(existing.Status))}
		default:
			expectedVersion, resolved = existing.Intent.File.ExpectedVersion, true
		}
	}
	if !resolved {
		expectedVersion, err = s.targetFileVersion(ctx, current, targetNodeID, targetPath)
		if err != nil {
			return fileTransferResult{}, err
		}
	}
	intent := protocol.TaskIntent{Action: protocol.TaskFileUpload, ContainerID: protocol.FileTargetKey(targetPath),
		File: &protocol.FileTaskSpec{Path: targetPath, ExpectedVersion: expectedVersion, Size: size, SHA256: digest, Transfer: transferSpec}}
	if err := protocol.ValidateTaskIntent(intent); err != nil {
		return fileTransferResult{}, err
	}
	enqueued, err := s.enqueueFileTask(ctx, current, targetNodeID, key, intent, nil)
	if err != nil {
		return fileTransferResult{}, err
	}
	taskID := enqueued.Task.TaskID
	defer func() {
		if failure != nil {
			failure = fileTransferTaskError{taskID: taskID, cause: failure}
		}
	}()
	result := fileTransferResult{SourcePath: sourcePath, TargetPath: targetPath, Size: size, SHA256: digest, TaskID: taskID}
	if !enqueued.Created {
		final, err := s.waitFileTask(ctx, current.ID, targetNodeID, taskID)
		if err != nil {
			return fileTransferResult{}, err
		}
		if final.Status != taskstate.Succeeded {
			return fileTransferResult{}, errors.New("target upload task did not succeed: " + string(final.Status))
		}
		result.Transport = final.Result.ObservedState
		return result, nil
	}
	ctx, finishRun := s.beginFileTransferRun(ctx, current, targetNodeID, taskID)
	defer finishRun()
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
	descriptor := protocol.FileTransferDescriptor{TaskID: taskID, TargetNodeID: targetNodeID, File: *intent.File}
	if transport, err := s.tryRsyncTransfer(ctx, current, descriptor); err == nil {
		final, err := s.waitFileTask(ctx, current.ID, targetNodeID, taskID)
		if err != nil {
			return fileTransferResult{}, err
		}
		if final.Status != taskstate.Succeeded {
			return fileTransferResult{}, errors.New("target upload task did not succeed: " + string(final.Status))
		}
		result.Transport = transport
		return result, nil
	} else if ctx.Err() != nil {
		return fileTransferResult{}, ctx.Err()
	} else {
		var committing rsyncCommitError
		if errors.As(err, &committing) {
			final, observeErr := s.waitFileTask(ctx, current.ID, targetNodeID, taskID)
			if observeErr != nil {
				return fileTransferResult{}, observeErr
			}
			if final.Status != taskstate.Succeeded {
				return fileTransferResult{}, err
			}
			result.Transport = final.Result.ObservedState
			return result, nil
		}
	}
	return s.transferFileChunks(ctx, current, descriptor)
}

type fileTransferTaskError struct {
	taskID string
	cause  error
}

func (e fileTransferTaskError) Error() string { return e.cause.Error() }
func (e fileTransferTaskError) Unwrap() error { return e.cause }

func (s *Server) transferFileChunks(ctx context.Context, current *session, descriptor protocol.FileTransferDescriptor) (fileTransferResult, error) {
	sourceNodeID, sourcePath := descriptor.File.Transfer.SourceNodeID, descriptor.File.Transfer.SourcePath
	targetNodeID, targetPath, taskID := descriptor.TargetNodeID, descriptor.File.Path, descriptor.TaskID
	size, digest, expectedVersion := descriptor.File.Size, descriptor.File.SHA256, descriptor.File.ExpectedVersion
	result := fileTransferResult{SourcePath: sourcePath, TargetPath: targetPath, Size: size, SHA256: digest, TaskID: taskID, Transport: "chunk_relay"}
	sourceConnection, sourceTransfer, err := s.newCoreFileTransfer(ctx, sourceNodeID, current, []string{protocol.FileDownload}, true)
	if err != nil {
		return fileTransferResult{}, err
	}
	defer s.closeCoreFileTransfer(sourceConnection, sourceTransfer, true, nil)
	download := protocol.FileRequest{Operation: protocol.FileDownload, Path: sourcePath, TransferID: sourceTransfer.requestID}
	if err := s.sendFileRequest(ctx, sourceConnection, sourceTransfer, download); err != nil {
		return fileTransferResult{}, err
	}
	first, err := s.waitFileMessage(ctx, sourceConnection, sourceTransfer)
	if err != nil {
		return fileTransferResult{}, err
	}
	var metadata protocol.FileResponse
	if first.Type != protocol.TypeFileResponse || decodeAgentPayload(first.Payload, &metadata) != nil || metadata.Operation != protocol.FileDownload {
		return fileTransferResult{}, errors.New("Agent download metadata is invalid")
	}
	if metadata.Code != "" {
		return fileTransferResult{}, fileRemoteError{code: metadata.Code}
	}
	if metadata.Entry == nil || metadata.Entry.Kind != "file" || metadata.Size != size || metadata.Entry.Size != size || metadata.Entry.Version != descriptor.File.Transfer.SourceVersion {
		return fileTransferResult{}, errors.New("source file changed during transfer")
	}
	connection, transfer, err := s.newCoreFileTransfer(ctx, targetNodeID, current, []string{protocol.FileUploadBegin, protocol.FileUploadChunk}, false)
	if err != nil {
		return fileTransferResult{}, err
	}
	defer s.closeCoreFileTransfer(connection, transfer, true, nil)
	begin := protocol.FileRequest{Operation: protocol.FileUploadBegin, TransferID: transfer.requestID, TaskID: taskID,
		Path: targetPath, ExpectedVersion: expectedVersion, Size: size, SHA256: digest}
	if err := s.sendFileRequest(ctx, connection, transfer, begin); err != nil {
		return fileTransferResult{}, err
	}
	beginResponse, err := s.waitFileMessage(ctx, connection, transfer)
	if err != nil {
		return fileTransferResult{}, err
	}
	if beginResponse.Type != protocol.TypeFileResponse {
		return fileTransferResult{}, errors.New("Agent did not start the transfer upload")
	}
	var beginResult protocol.FileResponse
	if decodeAgentPayload(beginResponse.Payload, &beginResult) != nil || beginResult.Operation != protocol.FileUploadBegin {
		return fileTransferResult{}, errors.New("Agent transfer upload response is invalid")
	}
	if beginResult.Code != "" {
		return fileTransferResult{}, fileRemoteError{code: beginResult.Code}
	}
	if beginResult.Size != size {
		return fileTransferResult{}, errors.New("Agent transfer upload target is inconsistent")
	}
	hash := sha256.New()
	var total int64
	var uploadSequence uint64
	started := time.Now()
	for {
		envelope, waitErr := s.waitFileMessage(ctx, sourceConnection, sourceTransfer)
		if waitErr != nil {
			return fileTransferResult{}, waitErr
		}
		if envelope.Type == protocol.TypeFileResponse {
			var failure protocol.FileResponse
			if decodeAgentPayload(envelope.Payload, &failure) == nil && failure.Code != "" {
				return fileTransferResult{}, fileRemoteError{code: failure.Code}
			}
			return fileTransferResult{}, errors.New("Agent download stream failed")
		}
		var chunk protocol.FileChunk
		if decodeAgentPayload(envelope.Payload, &chunk) != nil || protocol.ValidateFileChunkEnvelope(envelope, sourceTransfer.generation, chunk) != nil {
			return fileTransferResult{}, errors.New("Agent download chunk is invalid")
		}
		if chunk.Final {
			if total != size || hex.EncodeToString(hash.Sum(nil)) != chunk.SHA256 || chunk.SHA256 != digest {
				return fileTransferResult{}, errors.New("source file changed during transfer")
			}
			uploadSequence++
			finalChunk := protocol.FileChunk{TransferID: transfer.requestID, Sequence: uploadSequence, Final: true, SHA256: digest}
			if err := sendCoreFileChunk(ctx, s, current, connection, transfer, finalChunk); err != nil {
				return fileTransferResult{}, err
			}
			if err := waitFileUploadAck(ctx, s, current, connection, transfer, uploadSequence); err != nil {
				return fileTransferResult{}, err
			}
			break
		}
		data, decodeErr := protocol.DecodeFileChunk(chunk)
		if decodeErr != nil || len(data) == 0 || total+int64(len(data)) > size {
			return fileTransferResult{}, errors.New("Agent download chunk is invalid")
		}
		_, _ = hash.Write(data)
		total += int64(len(data))
		if rate := descriptor.File.Transfer.BandwidthLimit; rate > 0 {
			delay := time.Until(started.Add(time.Duration(float64(total) / float64(rate) * float64(time.Second))))
			if delay > 0 {
				timer := time.NewTimer(delay)
				select {
				case <-ctx.Done():
					timer.Stop()
					return fileTransferResult{}, ctx.Err()
				case <-timer.C:
				}
			}
		}
		uploadSequence++
		forward := protocol.FileChunk{TransferID: transfer.requestID, Sequence: uploadSequence, Data: chunk.Data}
		if err := sendCoreFileChunk(ctx, s, current, connection, transfer, forward); err != nil {
			return fileTransferResult{}, err
		}
		if err := waitFileUploadAck(ctx, s, current, connection, transfer, uploadSequence); err != nil {
			return fileTransferResult{}, err
		}
		ack := protocol.FileRequest{Operation: protocol.FileDownloadAck, TransferID: sourceTransfer.requestID, Sequence: chunk.Sequence}
		if err := s.sendFileRequest(ctx, sourceConnection, sourceTransfer, ack); err != nil {
			return fileTransferResult{}, err
		}
		if !s.dashboardSessionStillValid(ctx, current.ID) {
			return fileTransferResult{}, errors.New("browser Session was revoked or expired")
		}
	}
	s.closeCoreFileTransfer(connection, transfer, false, nil)
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
