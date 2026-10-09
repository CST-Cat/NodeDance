package files

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path"
	"sync"
	"time"

	"github.com/CST-Cat/NodeDance/internal/agent/taskjournal"
	"github.com/CST-Cat/NodeDance/internal/protocol"
	"github.com/CST-Cat/NodeDance/internal/taskstate"
)

const uploadAttachTimeout = 2 * time.Minute

type fileTaskJournal interface {
	EnqueueDelivered(context.Context, taskstate.Identity) (taskjournal.EnqueueResult, error)
	BeginExecution(context.Context, string) error
	Get(context.Context, string) (taskjournal.Snapshot, error)
	Finish(context.Context, string, taskstate.Status, taskstate.Evidence, taskjournal.Result) error
	MarkUnknown(context.Context, string) error
}

type pendingUpload struct {
	spec   protocol.FileTaskSpec
	reader chan *io.PipeReader
	used   bool
	upload *Upload
}

// TaskExecutor performs filesystem mutations through the Agent's existing
// durable task journal. The pending upload map is a connection rendezvous for
// one streaming body; it is not persistent task state.
type TaskExecutor struct {
	service *Service
	journal fileTaskJournal
	mu      sync.Mutex
	uploads map[string]*pendingUpload
}

func NewTaskExecutor(service *Service, journal fileTaskJournal) (*TaskExecutor, error) {
	if service == nil || journal == nil {
		return nil, errors.New("file task executor requires the configured file service and shared task journal")
	}
	return &TaskExecutor{service: service, journal: journal, uploads: make(map[string]*pendingUpload)}, nil
}

func (e *TaskExecutor) AttachUpload(ctx context.Context, taskID string, request protocol.FileRequest) (*io.PipeWriter, error) {
	deadline := time.NewTimer(uploadAttachTimeout)
	defer deadline.Stop()
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		e.mu.Lock()
		pending := e.uploads[taskID]
		if pending != nil {
			if pending.used || request.Operation != protocol.FileUploadBegin || request.Path != pending.spec.Path ||
				request.ExpectedVersion != pending.spec.ExpectedVersion || request.Size != pending.spec.Size || request.SHA256 != pending.spec.SHA256 {
				e.mu.Unlock()
				return nil, errors.New("upload metadata does not match the accepted file task")
			}
			reader, writer := io.Pipe()
			pending.used = true
			pending.reader <- reader
			e.mu.Unlock()
			return writer, nil
		}
		e.mu.Unlock()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-deadline.C:
			return nil, errors.New("file upload task is not ready")
		case <-ticker.C:
		}
	}
}

func (e *TaskExecutor) ExecuteFile(ctx context.Context, dispatch protocol.TaskDispatch, onRunning func(taskjournal.Snapshot)) (taskjournal.Snapshot, error) {
	identity, err := protocol.TaskIdentity(dispatch.TaskID, dispatch.NodeID, dispatch.IdempotencyKey, dispatch.Intent)
	if err != nil {
		return taskjournal.Snapshot{}, err
	}
	enqueued, err := e.journal.EnqueueDelivered(ctx, identity)
	if err != nil {
		return taskjournal.Snapshot{}, err
	}
	if taskstate.IsTerminal(enqueued.Task.Status) {
		return enqueued.Task, nil
	}
	if enqueued.Task.Status == taskstate.Unknown || enqueued.Task.Status == taskstate.Running {
		return enqueued.Task, errors.New("file task requires result reconciliation")
	}
	if err := e.journal.BeginExecution(ctx, dispatch.TaskID); err != nil {
		return e.afterError(ctx, dispatch.TaskID, err)
	}
	running, err := e.journal.Get(context.WithoutCancel(ctx), dispatch.TaskID)
	if err != nil || running.Status != taskstate.Running || !running.Evidence.ExecutionAttempted {
		return e.markUnknown(ctx, dispatch.TaskID)
	}
	if dispatch.Intent.Action == protocol.TaskFileUpload {
		if err := e.prepareUpload(dispatch.TaskID, *dispatch.Intent.File); err != nil {
			return e.finish(ctx, dispatch.TaskID, taskstate.Failed, "unchanged", stableFileError(err))
		}
		defer e.removePending(dispatch.TaskID)
	}
	var baseline MutationBaseline
	baseline, err = e.service.CaptureMutationBaseline(fileOperation(dispatch.Intent.Action), dispatch.Intent.File.Path, dispatch.Intent.File.NewPath)
	if err != nil {
		if dispatch.Intent.Action == protocol.TaskFileUpload {
			e.removePending(dispatch.TaskID)
		}
		return e.finish(ctx, dispatch.TaskID, taskstate.Failed, "unchanged", stableFileError(err))
	}
	if onRunning != nil {
		onRunning(running)
	}
	var expectedSize int64
	var expectedDigest string
	var observed string
	switch dispatch.Intent.Action {
	case protocol.TaskFileMkdir:
		err = e.service.Mkdir(dispatch.Intent.File.Path)
		observed = "created"
	case protocol.TaskFileRename:
		err = e.service.Rename(dispatch.Intent.File.Path, dispatch.Intent.File.NewPath)
		observed = "renamed"
	case protocol.TaskFileDelete:
		err = e.service.Delete(dispatch.Intent.File.Path, dispatch.Intent.DeleteConfirmed)
		observed = "deleted"
	case protocol.TaskFileSaveText:
		if dispatch.FileContent == nil || dispatch.FileContent.SHA256 != dispatch.Intent.File.SHA256 {
			return e.finish(ctx, dispatch.TaskID, taskstate.Failed, "unchanged", "payload_unavailable")
		}
		entry, _, saveErr := e.service.SaveText(dispatch.Intent.File.Path, dispatch.Intent.File.ExpectedVersion, string(dispatch.FileContent.Content))
		err = saveErr
		if err == nil && entry.Size != int64(len(dispatch.FileContent.Content)) {
			return e.markUnknown(ctx, dispatch.TaskID)
		}
		observed = "saved"
		expectedSize, expectedDigest = int64(len(dispatch.FileContent.Content)), dispatch.Intent.File.SHA256
	case protocol.TaskFileUpload:
		err = e.receiveUpload(ctx, dispatch.TaskID, *dispatch.Intent.File)
		observed = "uploaded"
		expectedSize, expectedDigest = dispatch.Intent.File.Size, dispatch.Intent.File.SHA256
	default:
		return e.finish(ctx, dispatch.TaskID, taskstate.Failed, "unchanged", "operation_rejected")
	}
	if err != nil {
		if errors.Is(err, ErrMutationResultUnknown) {
			verified, verifyErr := e.service.VerifyMutation(fileOperation(dispatch.Intent.Action), dispatch.Intent.File.Path,
				dispatch.Intent.File.NewPath, expectedSize, expectedDigest, baseline)
			if verifyErr == nil && verified {
				return e.finish(ctx, dispatch.TaskID, taskstate.Succeeded, observed, expectedDigestOrPath(dispatch.Intent.File, expectedDigest))
			}
			return e.markUnknown(ctx, dispatch.TaskID)
		}
		return e.finish(ctx, dispatch.TaskID, taskstate.Failed, "unchanged", stableFileError(err))
	}
	verified, verifyErr := e.service.VerifyMutation(fileOperation(dispatch.Intent.Action), dispatch.Intent.File.Path,
		dispatch.Intent.File.NewPath, expectedSize, expectedDigest, baseline)
	if verifyErr != nil || !verified {
		return e.markUnknown(ctx, dispatch.TaskID)
	}
	return e.finish(ctx, dispatch.TaskID, taskstate.Succeeded, observed, expectedDigestOrPath(dispatch.Intent.File, expectedDigest))
}

func (e *TaskExecutor) prepareUpload(taskID string, spec protocol.FileTaskSpec) error {
	temporary := uploadTemporaryPath(spec.Path, taskID)
	upload, err := e.service.BeginUploadAtTemporary(spec.Path, spec.ExpectedVersion, spec.Size, spec.SHA256, temporary)
	if err != nil {
		return err
	}
	// Keep the staged file on the executor while the Core streams bytes. A
	// failed or revoked transfer closes the reader and the deferred Abort
	// removes this exact random sibling temporary path.
	pending := &pendingUpload{spec: spec, reader: make(chan *io.PipeReader, 1), upload: upload}
	e.mu.Lock()
	if e.uploads[taskID] != nil {
		e.mu.Unlock()
		upload.Abort()
		return errors.New("duplicate file upload task")
	}
	e.uploads[taskID] = pending
	e.mu.Unlock()
	return nil
}

func (e *TaskExecutor) receiveUpload(ctx context.Context, taskID string, spec protocol.FileTaskSpec) error {
	reader, err := e.waitUploadReader(ctx, taskID)
	if err != nil {
		return err
	}
	defer reader.Close()
	e.mu.Lock()
	pending := e.uploads[taskID]
	e.mu.Unlock()
	if pending == nil || pending.upload == nil || pending.spec != spec {
		return errors.New("file upload task state is unavailable")
	}
	upload := pending.upload
	defer upload.Abort()
	buffer := make([]byte, protocol.MaxFileChunkBytes)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		n, readErr := reader.Read(buffer)
		if n > 0 {
			if err := upload.WriteChunk(buffer[:n]); err != nil {
				return err
			}
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return readErr
		}
	}
	_, err = upload.CommitWithDigest(spec.SHA256)
	return err
}

func (e *TaskExecutor) waitUploadReader(ctx context.Context, taskID string) (*io.PipeReader, error) {
	e.mu.Lock()
	pending := e.uploads[taskID]
	e.mu.Unlock()
	if pending == nil {
		return nil, errors.New("file upload task is not registered")
	}
	timer := time.NewTimer(uploadAttachTimeout)
	defer timer.Stop()
	select {
	case reader := <-pending.reader:
		return reader, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-timer.C:
		return nil, errors.New("file upload was not received")
	}
}

func (e *TaskExecutor) removePending(taskID string) {
	e.mu.Lock()
	pending := e.uploads[taskID]
	delete(e.uploads, taskID)
	e.mu.Unlock()
	if pending != nil && pending.upload != nil {
		pending.upload.Abort()
	}
}

func (e *TaskExecutor) ReconcileFile(ctx context.Context, taskID string, intent protocol.TaskIntent) (taskjournal.Snapshot, error) {
	// No file mutation is replayed after a process interruption. The previous
	// durable result remains unknown until an administrator starts a new task.
	task, err := e.journal.Get(ctx, taskID)
	if err != nil || task.Status != taskstate.Unknown {
		return task, err
	}
	if intent.Action == protocol.TaskFileUpload && intent.File != nil {
		if err := e.service.RemoveUploadTemporary(intent.File.Path, uploadTemporaryPath(intent.File.Path, taskID)); err != nil {
			return task, err
		}
	}
	return task, nil
}

func uploadTemporaryPath(target, taskID string) string {
	digest := sha256.Sum256([]byte(taskID))
	filename := "." + path.Base(target) + ".nodedance-upload-" + hex.EncodeToString(digest[:12])
	return path.Join(path.Dir(target), filename)
}

func fileOperation(action protocol.TaskAction) string {
	switch action {
	case protocol.TaskFileMkdir:
		return protocol.FileMkdir
	case protocol.TaskFileRename:
		return protocol.FileRename
	case protocol.TaskFileDelete:
		return protocol.FileDelete
	case protocol.TaskFileSaveText:
		return protocol.FileSaveText
	case protocol.TaskFileUpload:
		return "upload"
	default:
		return ""
	}
}

func expectedDigestOrPath(spec *protocol.FileTaskSpec, digest string) string {
	if digest != "" {
		return digest
	}
	return protocol.FileTargetKey(spec.Path)
}

func stableFileError(err error) string {
	switch {
	case errors.Is(err, ErrInvalidPath):
		return "invalid_path"
	case errors.Is(err, ErrConflict):
		return "version_conflict"
	case errors.Is(err, ErrExists):
		return "destination_exists"
	case errors.Is(err, ErrConfirmation):
		return "confirmation_required"
	case errors.Is(err, ErrLimitExceeded), errors.Is(err, ErrTextTooLarge):
		return "limit_exceeded"
	case errors.Is(err, ErrNotText):
		return "not_text"
	case errors.Is(err, ErrTransferDigest):
		return "digest_mismatch"
	case errors.Is(err, os.ErrNotExist):
		return "not_found"
	default:
		return "operation_rejected"
	}
}

func (e *TaskExecutor) finish(ctx context.Context, taskID string, status taskstate.Status, observed, revision string) (taskjournal.Snapshot, error) {
	evidence := taskstate.Evidence{ExecutionAttempted: true, ExecutionCompleted: true, ActualResultConfirmed: true}
	result := taskjournal.Result{Code: taskjournal.ResultFailed, ObservedState: observed, ResourceRevision: revision}
	if status == taskstate.Succeeded {
		evidence.PostconditionVerified = true
		result.Code = taskjournal.ResultVerified
	} else {
		evidence.FailureConfirmed = true
	}
	writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	err := e.journal.Finish(writeCtx, taskID, status, evidence, result)
	cancel()
	readCtx, readCancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	task, readErr := e.journal.Get(readCtx, taskID)
	readCancel()
	if err != nil {
		return task, err
	}
	return task, readErr
}

func (e *TaskExecutor) markUnknown(ctx context.Context, taskID string) (taskjournal.Snapshot, error) {
	writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	markErr := e.journal.MarkUnknown(writeCtx, taskID)
	cancel()
	readCtx, readCancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	task, readErr := e.journal.Get(readCtx, taskID)
	readCancel()
	if markErr != nil {
		return task, markErr
	}
	return task, readErr
}

func (e *TaskExecutor) afterError(ctx context.Context, taskID string, cause error) (taskjournal.Snapshot, error) {
	task, err := e.journal.Get(context.WithoutCancel(ctx), taskID)
	if err == nil && taskstate.IsTerminal(task.Status) {
		return task, nil
	}
	return task, cause
}
