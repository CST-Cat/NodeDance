package compose

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"time"

	"github.com/CST-Cat/NodeDance/internal/agent/taskjournal"
	"github.com/CST-Cat/NodeDance/internal/protocol"
	"github.com/CST-Cat/NodeDance/internal/taskstate"
)

type taskJournal interface {
	EnqueueDelivered(context.Context, taskstate.Identity) (taskjournal.EnqueueResult, error)
	BeginExecution(context.Context, string) error
	Get(context.Context, string) (taskjournal.Snapshot, error)
	Finish(context.Context, string, taskstate.Status, taskstate.Evidence, taskjournal.Result) error
	MarkUnknown(context.Context, string) error
}

type TaskExecutor struct {
	manager *Manager
	journal taskJournal
}

func NewTaskExecutor(manager *Manager, journal taskJournal) (*TaskExecutor, error) {
	if manager == nil || journal == nil {
		return nil, errors.New("Compose task executor requires manager and shared task journal")
	}
	return &TaskExecutor{manager: manager, journal: journal}, nil
}

func (e *TaskExecutor) ExecuteCompose(ctx context.Context, dispatch protocol.TaskDispatch, onRunning func(taskjournal.Snapshot)) (taskjournal.Snapshot, error) {
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
		return enqueued.Task, errors.New("Compose task requires result reconciliation")
	}
	if err := e.journal.BeginExecution(ctx, dispatch.TaskID); err != nil {
		return e.afterError(ctx, dispatch.TaskID, err)
	}
	running, err := e.journal.Get(context.WithoutCancel(ctx), dispatch.TaskID)
	if err != nil || running.Status != taskstate.Running || !running.Evidence.ExecutionAttempted {
		return e.markUnknown(ctx, dispatch.TaskID)
	}
	if onRunning != nil {
		onRunning(running)
	}
	if dispatch.Intent.Action == protocol.TaskComposeSave {
		contentDigest := sha256.Sum256(nil)
		if dispatch.ComposeContent != nil {
			contentDigest = sha256.Sum256(dispatch.ComposeContent.Content)
		}
		if dispatch.ComposeContent == nil || dispatch.ComposeContent.SHA256 != dispatch.Intent.Compose.ContentSHA256 ||
			hex.EncodeToString(contentDigest[:]) != dispatch.Intent.Compose.ContentSHA256 {
			return e.finish(ctx, dispatch.TaskID, taskstate.Failed, "unchanged", "payload_unavailable")
		}
		committed, err := e.manager.SaveConfig(ctx, dispatch.Intent.Compose.Project, dispatch.Intent.Compose.FileIndex,
			dispatch.Intent.Compose.BaseSHA256, string(dispatch.ComposeContent.Content))
		if err != nil {
			if committed || errors.Is(err, ErrOperationUncertain) {
				return e.markUnknown(ctx, dispatch.TaskID)
			}
			return e.finish(ctx, dispatch.TaskID, taskstate.Failed, "unchanged", stableErrorCode(err))
		}
		return e.finish(ctx, dispatch.TaskID, taskstate.Succeeded, "saved", dispatch.Intent.Compose.ContentSHA256)
	}
	state, err := e.manager.ExecuteProject(ctx, dispatch.Intent.Compose.Project, dispatch.Intent.Action)
	if err != nil {
		if errors.Is(err, ErrConfigInvalid) || errors.Is(err, ErrProjectUnavailable) {
			return e.finish(ctx, dispatch.TaskID, taskstate.Failed, "unchanged", stableErrorCode(err))
		}
		return e.markUnknown(ctx, dispatch.TaskID)
	}
	return e.finish(ctx, dispatch.TaskID, taskstate.Succeeded, state, dispatch.Intent.Compose.Project.Key)
}

func (e *TaskExecutor) ReconcileCompose(ctx context.Context, taskID string, intent protocol.TaskIntent) (taskjournal.Snapshot, error) {
	task, err := e.journal.Get(ctx, taskID)
	if err != nil || task.Status != taskstate.Unknown {
		return task, err
	}
	if intent.Action == protocol.TaskComposeSave && intent.Compose != nil {
		if err := e.manager.verifyConfigDigest(intent.Compose.Project, intent.Compose.FileIndex, intent.Compose.ContentSHA256); err == nil {
			evidence := taskstate.Evidence{ExecutionAttempted: true, ExecutionCompleted: true, ActualResultConfirmed: true, PostconditionVerified: true}
			result := taskjournal.Result{Code: taskjournal.ResultVerified, ObservedState: "saved", ResourceRevision: intent.Compose.ContentSHA256}
			if err := e.journal.Finish(ctx, taskID, taskstate.Succeeded, evidence, result); err != nil {
				return taskjournal.Snapshot{}, err
			}
			return e.journal.Get(ctx, taskID)
		}
	}
	// Project lifecycle commands remain unknown after an interrupted delivery.
	// Inspecting current state alone cannot prove whether this task caused it, so
	// never replay Compose mutations during reconciliation.
	return task, nil
}

func (e *TaskExecutor) finish(ctx context.Context, taskID string, status taskstate.Status, state, code string) (taskjournal.Snapshot, error) {
	evidence := taskstate.Evidence{ExecutionAttempted: true, ExecutionCompleted: true, ActualResultConfirmed: true}
	result := taskjournal.Result{Code: taskjournal.ResultFailed, ObservedState: state, ResourceRevision: code}
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

func stableErrorCode(err error) string {
	switch {
	case errors.Is(err, ErrConfigChanged):
		return "config_changed"
	case errors.Is(err, ErrConfigInvalid):
		return "config_invalid"
	case errors.Is(err, ErrProjectUnavailable):
		return "project_unavailable"
	case errors.Is(err, ErrConfigUnsafe):
		return "config_unsafe"
	default:
		return "operation_rejected"
	}
}
