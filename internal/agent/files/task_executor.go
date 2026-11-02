package files

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path"
	"reflect"
	"sync"
	"time"

	"github.com/CST-Cat/NodeDance/internal/agent/taskjournal"
	"github.com/CST-Cat/NodeDance/internal/agent/transferbuffer"
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
	UpdateProgress(context.Context, string, taskjournal.Progress) error
}

type pendingUpload struct {
	spec        protocol.FileTaskSpec
	reader      chan *io.PipeReader
	used        bool
	upload      *Upload
	ctx         context.Context
	cancel      context.CancelCauseFunc
	nodeID      string
	notify      func(taskjournal.Snapshot)
	attemptDone chan struct{}
	stage       *RsyncStage
	completed   bool
	committing  bool
	transport   string
}

// TaskExecutor performs filesystem mutations through the Agent's existing
// durable task journal. The pending upload map is a connection rendezvous for
// one streaming body; it is not persistent task state.
type TaskExecutor struct {
	service       *Service
	journal       fileTaskJournal
	mu            sync.Mutex
	uploads       map[string]*pendingUpload
	stateRoot     *os.File
	stateDir      string
	cancellations map[string]bool
	cancelOrder   []string
}

func NewTaskExecutor(service *Service, journal fileTaskJournal) (*TaskExecutor, error) {
	if service == nil || journal == nil {
		return nil, errors.New("file task executor requires the configured file service and shared task journal")
	}
	return &TaskExecutor{service: service, journal: journal, uploads: make(map[string]*pendingUpload), cancellations: make(map[string]bool)}, nil
}

func (e *TaskExecutor) AttachUpload(ctx context.Context, taskID string, request protocol.FileRequest) (*io.PipeWriter, error) {
	return e.attachUpload(ctx, taskID, request, false)
}

func (e *TaskExecutor) attachUpload(ctx context.Context, taskID string, request protocol.FileRequest, rsyncCommit bool) (*io.PipeWriter, error) {
	deadline := time.NewTimer(uploadAttachTimeout)
	defer deadline.Stop()
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		e.mu.Lock()
		pending := e.uploads[taskID]
		if pending != nil {
			if pending.used || pending.committing != rsyncCommit || pending.attemptDone != nil || pending.ctx.Err() != nil || request.Operation != protocol.FileUploadBegin || request.Path != pending.spec.Path ||
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
	uploadTask := dispatch.Intent.Action == protocol.TaskFileUpload || dispatch.Intent.Action == protocol.TaskFileSaveText
	if uploadTask {
		if err := e.prepareUpload(ctx, dispatch.TaskID, *dispatch.Intent.File); err != nil {
			return e.finish(ctx, dispatch.TaskID, taskstate.Failed, "unchanged", stableFileError(err))
		}
		e.mu.Lock()
		pending := e.uploads[dispatch.TaskID]
		pending.nodeID, pending.notify = dispatch.NodeID, onRunning
		e.mu.Unlock()
		defer e.removePending(dispatch.TaskID)
	}
	if dispatch.Intent.Action == protocol.TaskFileCopy || dispatch.Intent.Action == protocol.TaskFileArchive || dispatch.Intent.Action == protocol.TaskFileExtract {
		if onRunning != nil {
			onRunning(running)
		}
		err := e.service.ExecuteExtended(ctx, dispatch.Intent.Action, *dispatch.Intent.File, dispatch.TaskID)
		if errors.Is(err, ErrMutationResultUnknown) {
			return e.markUnknown(ctx, dispatch.TaskID)
		}
		if err != nil {
			return e.finish(ctx, dispatch.TaskID, taskstate.Failed, "unchanged", stableFileError(err))
		}
		return e.finish(ctx, dispatch.TaskID, taskstate.Succeeded, "completed", protocol.FileTargetKey(dispatch.Intent.File.Path))
	}
	var baseline MutationBaseline
	baseline, err = e.service.CaptureMutationBaseline(fileOperation(dispatch.Intent.Action), dispatch.Intent.File.Path, dispatch.Intent.File.NewPath)
	if err != nil {
		if uploadTask {
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
		err = e.receiveTextUpload(ctx, dispatch.TaskID, *dispatch.Intent.File)
		observed = "saved"
		expectedSize, expectedDigest = dispatch.Intent.File.Size, dispatch.Intent.File.SHA256
	case protocol.TaskFileUpload:
		err = e.receiveUpload(ctx, dispatch.TaskID, *dispatch.Intent.File)
		observed = "uploaded"
		if dispatch.Intent.File.Transfer != nil {
			e.mu.Lock()
			observed = e.uploads[dispatch.TaskID].transport
			e.mu.Unlock()
			if observed == "" {
				observed = "chunk_relay"
			}
		}
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
		if dispatch.Intent.File.Transfer != nil && errors.Is(err, taskstate.ErrCancellationRequested) {
			return e.finishCanceled(ctx, dispatch.TaskID)
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

func (e *TaskExecutor) prepareUpload(ctx context.Context, taskID string, spec protocol.FileTaskSpec) error {
	temporary := uploadTemporaryPath(spec.Path, taskID)
	upload, err := e.service.BeginUploadAtTemporary(spec.Path, spec.ExpectedVersion, spec.Size, spec.SHA256, temporary)
	if err != nil {
		return err
	}
	// Keep the staged file on the executor while the Core streams bytes. A
	// failed or revoked transfer closes the reader and the deferred Abort
	// removes this exact random sibling temporary path.
	uploadCtx, cancel := context.WithCancelCause(ctx)
	pending := &pendingUpload{spec: spec, reader: make(chan *io.PipeReader, 1), upload: upload, ctx: uploadCtx, cancel: cancel}
	e.mu.Lock()
	if e.uploads[taskID] != nil {
		e.mu.Unlock()
		cancel(context.Canceled)
		upload.Abort()
		return errors.New("duplicate file upload task")
	}
	e.uploads[taskID] = pending
	if spec.Transfer != nil && e.cancellations[taskID] {
		cancel(taskstate.ErrCancellationRequested)
	}
	e.mu.Unlock()
	return nil
}

func (e *TaskExecutor) receiveUpload(ctx context.Context, taskID string, spec protocol.FileTaskSpec) error {
	e.mu.Lock()
	pending := e.uploads[taskID]
	e.mu.Unlock()
	if pending != nil && spec.Transfer != nil {
		ctx = pending.ctx
	}
	reader, err := e.waitUploadReader(ctx, taskID)
	if err != nil {
		return uploadCause(ctx, err)
	}
	defer reader.Close()
	stopClose := context.AfterFunc(ctx, func() { _ = reader.CloseWithError(ctx.Err()) })
	defer stopClose()
	e.mu.Lock()
	pending = e.uploads[taskID]
	e.mu.Unlock()
	if pending == nil || pending.upload == nil || !reflect.DeepEqual(pending.spec, spec) {
		return errors.New("file upload task state is unavailable")
	}
	upload := pending.upload
	defer upload.Abort()
	if spec.Transfer != nil && pending.transport == "" {
		if pending.stage == nil {
			stage, err := e.openRsyncStage(ctx, protocol.FileTransferDescriptor{TaskID: taskID, TargetNodeID: pending.nodeID, File: spec})
			if err != nil {
				return err
			}
			e.mu.Lock()
			pending.stage = stage
			e.mu.Unlock()
		}
		defer pending.stage.Payload.Sync()
	}
	lease := transferbuffer.Acquire()
	defer transferbuffer.Release(lease)
	buffer := lease[:]
	lastProgress := time.Time{}
	for {
		if err := ctx.Err(); err != nil {
			return uploadCause(ctx, err)
		}
		n, readErr := reader.Read(buffer)
		if err := ctx.Err(); err != nil {
			return uploadCause(ctx, err)
		}
		if n > 0 {
			if spec.Transfer != nil && pending.transport == "" {
				if int64(n) > spec.Size-upload.written {
					return ErrLimitExceeded
				}
				if _, err := pending.stage.Payload.WriteAt(buffer[:n], upload.written); err != nil {
					return err
				}
			}
			if err := upload.WriteChunk(buffer[:n]); err != nil {
				return err
			}
			if time.Since(lastProgress) >= 250*time.Millisecond {
				lastProgress = time.Now()
				if pending.transport == "" {
					e.TransferProgress(taskID, uint64(upload.written), taskjournal.PhaseExecuting)
				}
			}
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return uploadCause(ctx, readErr)
		}
	}
	e.TransferProgress(taskID, uint64(spec.Size), taskjournal.PhaseVerifying)
	_, err = upload.CommitWithDigest(spec.SHA256)
	if err == nil {
		e.mu.Lock()
		pending.completed = true
		e.mu.Unlock()
	}
	return err
}

func (e *TaskExecutor) receiveTextUpload(ctx context.Context, taskID string, spec protocol.FileTaskSpec) error {
	reader, err := e.waitUploadReader(ctx, taskID)
	if err != nil {
		return err
	}
	defer reader.Close()
	stop := context.AfterFunc(ctx, func() { _ = reader.CloseWithError(ctx.Err()) })
	defer stop()
	buffer := bytes.NewBuffer(make([]byte, 0, int(spec.Size)))
	defer func() { clear(buffer.Bytes()) }()
	n, err := io.Copy(buffer, transferContextReader{ctx: ctx, reader: io.LimitReader(reader, spec.Size+1)})
	if err != nil {
		return err
	}
	digest := sha256.Sum256(buffer.Bytes())
	if n != spec.Size || hex.EncodeToString(digest[:]) != spec.SHA256 {
		return ErrTransferDigest
	}
	entry, err := e.service.SaveText(spec.Path, spec.ExpectedVersion, buffer.String())
	if err == nil && entry.Size != spec.Size {
		return ErrMutationResultUnknown
	}
	return err
}

func (e *TaskExecutor) waitUploadReader(ctx context.Context, taskID string) (*io.PipeReader, error) {
	e.mu.Lock()
	pending := e.uploads[taskID]
	e.mu.Unlock()
	if pending == nil {
		return nil, errors.New("file upload task is not registered")
	}
	timeout := uploadAttachTimeout
	if pending.spec.Transfer != nil {
		timeout = 30 * time.Minute
	}
	timer := time.NewTimer(timeout)
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
	var done chan struct{}
	if pending != nil {
		pending.cancel(context.Canceled)
		done = pending.attemptDone
	}
	e.mu.Unlock()
	if done != nil {
		<-done
	}
	e.mu.Lock()
	delete(e.uploads, taskID)
	e.mu.Unlock()
	if pending != nil && pending.upload != nil {
		pending.upload.Abort()
	}
	if pending != nil && pending.stage != nil {
		pending.stage.Close()
		if pending.completed {
			e.removeRsyncStage(taskID)
		}
	}
}

func uploadCause(ctx context.Context, err error) error {
	if cause := context.Cause(ctx); cause != nil {
		return cause
	}
	return err
}

func (e *TaskExecutor) CancelFileTransfer(taskID string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	pending := e.uploads[taskID]
	if pending == nil {
		if e.cancellations == nil {
			e.cancellations = make(map[string]bool)
		}
		if !e.cancellations[taskID] {
			e.cancellations[taskID] = true
			e.cancelOrder = append(e.cancelOrder, taskID)
			if len(e.cancelOrder) > 128 {
				delete(e.cancellations, e.cancelOrder[0])
				e.cancelOrder = e.cancelOrder[1:]
			}
		}
		return true
	}
	if pending.spec.Transfer == nil || pending.completed {
		return false
	}
	pending.cancel(taskstate.ErrCancellationRequested)
	return true
}

func (e *TaskExecutor) TransferProgress(taskID string, completed uint64, phase taskjournal.ProgressPhase) {
	e.mu.Lock()
	pending := e.uploads[taskID]
	if pending == nil || e.journal == nil || completed > uint64(pending.spec.Size) {
		e.mu.Unlock()
		return
	}
	notify := pending.notify
	total := uint64(pending.spec.Size)
	e.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if e.journal.UpdateProgress(ctx, taskID, taskjournal.Progress{Phase: phase, Completed: completed, Total: total}) == nil && notify != nil {
		if snapshot, err := e.journal.Get(ctx, taskID); err == nil {
			notify(snapshot)
		}
	}
}

func (e *TaskExecutor) finishCanceled(ctx context.Context, taskID string) (taskjournal.Snapshot, error) {
	e.mu.Lock()
	pending := e.uploads[taskID]
	var done chan struct{}
	if pending != nil {
		done = pending.attemptDone
	}
	e.mu.Unlock()
	if done != nil {
		<-done
	}
	writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	evidence := taskstate.Evidence{ExecutionAttempted: true, ExecutionCompleted: true, ProcessTerminated: true, CancellationConfirmed: true, ActualResultConfirmed: true}
	err := e.journal.Finish(writeCtx, taskID, taskstate.Canceled, evidence, taskjournal.Result{Code: taskjournal.ResultCanceled, ObservedState: "unchanged"})
	snapshot, readErr := e.journal.Get(writeCtx, taskID)
	if err != nil {
		return snapshot, err
	}
	return snapshot, readErr
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
		if intent.File.Transfer != nil {
			return e.reconcileTransfer(ctx, task, *intent.File)
		}
	}
	if intent.File != nil && (intent.Action == protocol.TaskFileCopy || intent.Action == protocol.TaskFileArchive || intent.Action == protocol.TaskFileExtract) {
		target := intent.File.NewPath
		if intent.Action == protocol.TaskFileArchive {
			target = intent.File.Path
		}
		stage := path.Join(path.Dir(target), "."+path.Base(target)+".nodedance-file-"+taskID)
		if intent.Action == protocol.TaskFileExtract {
			stage = path.Join(target, ".nodedance-file-"+taskID)
		}
		if err := e.service.Delete(stage, true); err != nil && !errors.Is(err, os.ErrNotExist) {
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
