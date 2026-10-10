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
	MarkUnknownWithResult(context.Context, string, taskjournal.Result) error
	StoreComposeConfigBaselineIfAbsent(context.Context, string, [][]byte) (bool, error)
	LoadComposeConfigBaseline(context.Context, string) ([][]byte, bool, error)
	DeleteComposeConfigBaseline(context.Context, string) error
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
	if dispatch.Intent.Compose == nil {
		return e.finish(ctx, dispatch.TaskID, taskstate.Failed, "unchanged", "project_unavailable")
	}
	unlock, err := e.manager.lockProject(ctx, dispatch.Intent.Compose.Project)
	if err != nil {
		return e.afterError(ctx, dispatch.TaskID, err)
	}
	defer unlock()
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
	if dispatch.Intent.Action == protocol.TaskComposeSave || dispatch.Intent.Action == protocol.TaskComposeCreate {
		contentDigest := sha256.Sum256(nil)
		if dispatch.ComposeContent != nil {
			contentDigest = sha256.Sum256(dispatch.ComposeContent.Content)
		}
		if dispatch.ComposeContent == nil || dispatch.ComposeContent.SHA256 != dispatch.Intent.Compose.ContentSHA256 ||
			hex.EncodeToString(contentDigest[:]) != dispatch.Intent.Compose.ContentSHA256 {
			return e.finish(ctx, dispatch.TaskID, taskstate.Failed, "unchanged", "payload_unavailable")
		}
		if dispatch.Intent.Action == protocol.TaskComposeCreate {
			instances, err := e.manager.CreateProject(ctx, dispatch.Intent.Compose.Project, dispatch.TaskID, dispatch.ComposeContent.Content, dispatch.Intent.Compose.ContentSHA256)
			if err != nil {
				if errors.Is(err, ErrOperationUncertain) {
					return e.markUnknown(ctx, dispatch.TaskID)
				}
				return e.finish(ctx, dispatch.TaskID, taskstate.Failed, "unchanged", stableErrorCode(err))
			}
			if len(instances) == 0 {
				return e.markUnknown(ctx, dispatch.TaskID)
			}
			return e.finish(ctx, dispatch.TaskID, taskstate.Succeeded, "deployed", dispatch.Intent.Compose.Project.Key)
		}
		if err := e.captureBaselineBeforeSave(ctx, dispatch.Intent.Compose.Project, dispatch.Intent.Compose.FileIndex, dispatch.Intent.Compose.BaseSHA256); err != nil {
			return e.finish(ctx, dispatch.TaskID, taskstate.Failed, "unchanged", "baseline_unavailable")
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
	if dispatch.Intent.Action == protocol.TaskComposeDeploy {
		baseline, err := e.loadOrCaptureBaseline(ctx, dispatch.Intent.Compose.Project)
		if err != nil {
			return e.finish(ctx, dispatch.TaskID, taskstate.Failed, "unchanged", "baseline_unavailable")
		}
		state, err := e.manager.DeployProject(ctx, dispatch.Intent.Compose.Project, dispatch.TaskID, baseline)
		if err != nil {
			var deploymentErr *deploymentResultError
			if errors.As(err, &deploymentErr) {
				if deploymentErr.unknown {
					return e.markUnknownWithResult(ctx, dispatch.TaskID, taskjournal.Result{
						Code: taskjournal.ResultUncertain, ObservedState: "recovery_required", ResourceRevision: deploymentErr.code,
					})
				}
				if err := e.journal.DeleteComposeConfigBaseline(context.WithoutCancel(ctx), dispatch.Intent.Compose.Project.Key); err != nil {
					return e.markUnknownWithResult(ctx, dispatch.TaskID, taskjournal.Result{
						Code: taskjournal.ResultUncertain, ObservedState: "recovery_required", ResourceRevision: "baseline_cleanup_failed",
					})
				}
				return e.finish(ctx, dispatch.TaskID, taskstate.Failed, deploymentErr.state, deploymentErr.code)
			}
			if errors.Is(err, ErrConfigInvalid) || errors.Is(err, ErrProjectUnavailable) {
				return e.finish(ctx, dispatch.TaskID, taskstate.Failed, "unchanged", stableErrorCode(err))
			}
			return e.markUnknownWithResult(ctx, dispatch.TaskID, taskjournal.Result{
				Code: taskjournal.ResultUncertain, ObservedState: "recovery_required", ResourceRevision: "deployment_result_unknown",
			})
		}
		if err := e.journal.DeleteComposeConfigBaseline(context.WithoutCancel(ctx), dispatch.Intent.Compose.Project.Key); err != nil {
			return e.markUnknownWithResult(ctx, dispatch.TaskID, taskjournal.Result{
				Code: taskjournal.ResultUncertain, ObservedState: "deployment_verified", ResourceRevision: "baseline_cleanup_failed",
			})
		}
		return e.finish(ctx, dispatch.TaskID, taskstate.Succeeded, state, dispatch.Intent.Compose.Project.Key)
	}
	state, err := e.manager.ExecuteProject(ctx, dispatch.Intent.Compose.Project, dispatch.TaskID, dispatch.Intent.Action)
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
	if intent.Action == protocol.TaskComposeCreate && intent.Compose != nil {
		if instances, err := e.manager.verifyCreatedProject(ctx, intent.Compose.Project, intent.Compose.ContentSHA256); err == nil && len(instances) > 0 {
			evidence := taskstate.Evidence{ExecutionAttempted: true, ExecutionCompleted: true, ActualResultConfirmed: true, PostconditionVerified: true}
			result := taskjournal.Result{Code: taskjournal.ResultVerified, ObservedState: "deployed", ResourceRevision: intent.Compose.Project.Key}
			if err := e.journal.Finish(ctx, taskID, taskstate.Succeeded, evidence, result); err != nil {
				return taskjournal.Snapshot{}, err
			}
			return e.journal.Get(ctx, taskID)
		}
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

func (e *TaskExecutor) markUnknownWithResult(ctx context.Context, taskID string, result taskjournal.Result) (taskjournal.Snapshot, error) {
	writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	markErr := e.journal.MarkUnknownWithResult(writeCtx, taskID, result)
	cancel()
	readCtx, readCancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	task, readErr := e.journal.Get(readCtx, taskID)
	readCancel()
	if markErr != nil {
		return task, markErr
	}
	return task, readErr
}

func (e *TaskExecutor) captureBaselineBeforeSave(ctx context.Context, ref protocol.ComposeProjectRef, fileIndex int, baseSHA256 string) error {
	_, exists, err := e.journal.LoadComposeConfigBaseline(ctx, ref.Key)
	if err != nil || exists {
		return err
	}
	snapshot, err := e.manager.CaptureProjectConfig(ctx, ref)
	if err != nil {
		return err
	}
	if fileIndex < 0 || fileIndex >= len(snapshot.Files) {
		return ErrConfigUnsafe
	}
	digest := sha256.Sum256(snapshot.Files[fileIndex])
	if hex.EncodeToString(digest[:]) != baseSHA256 {
		return ErrConfigChanged
	}
	_, err = e.journal.StoreComposeConfigBaselineIfAbsent(ctx, ref.Key, snapshot.Files)
	return err
}

func (e *TaskExecutor) loadOrCaptureBaseline(ctx context.Context, ref protocol.ComposeProjectRef) (ProjectConfigSnapshot, error) {
	files, exists, err := e.journal.LoadComposeConfigBaseline(ctx, ref.Key)
	if err != nil {
		return ProjectConfigSnapshot{}, err
	}
	if !exists {
		current, captureErr := e.manager.CaptureProjectConfig(ctx, ref)
		if captureErr != nil {
			return ProjectConfigSnapshot{}, captureErr
		}
		if _, err := e.journal.StoreComposeConfigBaselineIfAbsent(ctx, ref.Key, current.Files); err != nil {
			return ProjectConfigSnapshot{}, err
		}
		files, exists, err = e.journal.LoadComposeConfigBaseline(ctx, ref.Key)
		if err != nil || !exists {
			return ProjectConfigSnapshot{}, errors.New("Compose deployment baseline is unavailable")
		}
	}
	if len(files) != len(ref.ConfigFiles) {
		return ProjectConfigSnapshot{}, ErrConfigUnsafe
	}
	return ProjectConfigSnapshot{Files: files}, nil
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
	case errors.Is(err, ErrProjectConflict):
		return "project_conflict"
	case errors.Is(err, ErrCreateFailed):
		return "creation_rolled_back"
	case errors.Is(err, ErrConfigUnsafe):
		return "config_unsafe"
	default:
		return "operation_rejected"
	}
}
