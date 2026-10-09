package images

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/CST-Cat/NodeDance/internal/agent/taskjournal"
	"github.com/CST-Cat/NodeDance/internal/protocol"
	"github.com/CST-Cat/NodeDance/internal/taskstate"
	"github.com/containerd/errdefs"
	"github.com/distribution/reference"
	"github.com/moby/moby/api/types/registry"
)

const (
	defaultOperationTimeout = 20 * time.Minute
	defaultVerifyTimeout    = 10 * time.Second
)

type Journal interface {
	Get(context.Context, string) (taskjournal.Snapshot, error)
	BeginExecution(context.Context, string) error
	UpdateProgress(context.Context, string, taskjournal.Progress) error
	Finish(context.Context, string, taskstate.Status, taskstate.Evidence, taskjournal.Result) error
	MarkUnknown(context.Context, string) error
}

type Executor struct {
	engine              Engine
	journal             Journal
	operationTimeout    time.Duration
	verificationTimeout time.Duration
}

func NewExecutor(engine Engine, journal Journal) (*Executor, error) {
	if engine == nil || journal == nil {
		return nil, errors.New("image engine and task journal are required")
	}
	return &Executor{engine: engine, journal: journal, operationTimeout: defaultOperationTimeout,
		verificationTimeout: defaultVerifyTimeout}, nil
}

// ExecuteImage runs one already-durable task dispatch. Registry credentials
// live only in this call and are cleared as soon as Docker accepts the pull.
func (e *Executor) ExecuteImage(ctx context.Context, dispatch protocol.TaskDispatch, onProgress func()) (taskjournal.Snapshot, error) {
	defer wipeCredentials(dispatch.RegistryAuth)
	if ctx == nil || dispatch.Intent.Action != protocol.TaskImagePull && dispatch.Intent.Action != protocol.TaskImageDelete ||
		protocol.ValidateTaskIntent(dispatch.Intent) != nil || dispatch.TargetID != dispatch.Intent.ContainerID {
		return taskjournal.Snapshot{}, errors.New("invalid image task dispatch")
	}
	identity, err := protocol.TaskIdentity(dispatch.TaskID, dispatch.NodeID, dispatch.IdempotencyKey, dispatch.Intent)
	if err != nil {
		return taskjournal.Snapshot{}, errors.New("invalid image task identity")
	}
	digest, err := taskstate.RequestDigest(identity)
	if err != nil {
		return taskjournal.Snapshot{}, errors.New("invalid image task digest")
	}
	before, err := e.journal.Get(ctx, dispatch.TaskID)
	if err != nil || before.NodeID != dispatch.NodeID || before.TargetID != dispatch.TargetID || before.Action != string(dispatch.Intent.Action) ||
		before.RequestDigest != digest || before.IdempotencyKey != dispatch.IdempotencyKey {
		return taskjournal.Snapshot{}, errors.New("image task does not match the durable Agent journal")
	}
	if before.Status != taskstate.Queued {
		return before, nil
	}
	if err := e.journal.BeginExecution(ctx, dispatch.TaskID); err != nil {
		latest, readErr := e.journal.Get(context.WithoutCancel(ctx), dispatch.TaskID)
		if readErr == nil {
			return latest, nil
		}
		return before, errors.New("could not begin durable image task")
	}
	running, err := e.journal.Get(context.WithoutCancel(ctx), dispatch.TaskID)
	if err != nil || running.Status != taskstate.Running || !running.Evidence.ExecutionAttempted {
		_ = e.journal.MarkUnknown(context.WithoutCancel(ctx), dispatch.TaskID)
		return running, errors.New("durable image task start could not be confirmed")
	}
	if onProgress != nil {
		onProgress()
	}
	if dispatch.Intent.Action == protocol.TaskImagePull {
		return e.pull(ctx, dispatch, running, onProgress)
	}
	return e.remove(ctx, dispatch, running)
}

func wipeCredentials(credentials *protocol.RegistryCredentials) {
	if credentials != nil {
		credentials.Username, credentials.Password = "", ""
	}
}

func (e *Executor) pull(ctx context.Context, dispatch protocol.TaskDispatch, _ taskjournal.Snapshot, onProgress func()) (taskjournal.Snapshot, error) {
	ref := dispatch.Intent.ImageReference
	verifyCtx, verifyCancel := context.WithTimeout(context.WithoutCancel(ctx), e.verificationTimeout)
	_, beforeErr := e.engine.Inspect(verifyCtx, ref)
	verifyCancel()
	if beforeErr == nil {
		return e.finish(ctx, dispatch.TaskID, taskstate.Succeeded, taskstate.Evidence{ExecutionAttempted: true,
			ExecutionCompleted: true, PostconditionVerified: true, ActualResultConfirmed: true}, taskjournal.Result{
			Code: taskjournal.ResultVerified, ObservedState: "present"})
	}
	if !errdefs.IsNotFound(beforeErr) {
		return e.markUnknown(ctx, dispatch.TaskID, beforeErr)
	}
	authHeader := ""
	if dispatch.RegistryAuth != nil {
		if !dispatch.RegistryAuth.Valid() {
			return e.finish(ctx, dispatch.TaskID, taskstate.Failed, failureEvidence(), taskjournal.Result{
				Code: taskjournal.ResultFailed, ObservedState: "invalid_auth"})
		}
		encoded, err := json.Marshal(registry.AuthConfig{Username: dispatch.RegistryAuth.Username, Password: dispatch.RegistryAuth.Password})
		// Avoid retaining the credential-bearing fields after constructing Docker's
		// one-operation header. These values are never journaled or logged.
		dispatch.RegistryAuth.Username = ""
		dispatch.RegistryAuth.Password = ""
		dispatch.RegistryAuth = nil
		if err != nil {
			return e.finish(ctx, dispatch.TaskID, taskstate.Failed, failureEvidence(), taskjournal.Result{
				Code: taskjournal.ResultFailed, ObservedState: "invalid_auth"})
		}
		authHeader = base64.URLEncoding.EncodeToString(encoded)
		clear(encoded)
	}
	operationCtx, operationCancel := context.WithTimeout(ctx, e.operationTimeout)
	progressAt := time.Time{}
	var latest taskjournal.Progress
	var persisted taskjournal.Progress
	hasLatest, hasPersisted := false, false
	persistProgress := func(force bool) {
		if !hasLatest || hasPersisted && latest == persisted {
			return
		}
		now := time.Now()
		if !force && !progressAt.IsZero() && now.Sub(progressAt) < 250*time.Millisecond {
			return
		}
		progress := latest
		writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		writeErr := e.journal.UpdateProgress(writeCtx, dispatch.TaskID, progress)
		cancel()
		if writeErr != nil {
			return
		}
		progressAt, persisted, hasPersisted = now, progress, true
		if onProgress != nil {
			onProgress()
		}
	}
	pullErr := e.engine.Pull(operationCtx, ref, authHeader, func(value PullProgress) {
		// Total zero means Docker has not supplied an aggregate byte count.
		// Invalid samples are discarded rather than manufacturing a capped value.
		if value.Total == 0 || value.Completed > value.Total {
			return
		}
		latest = taskjournal.Progress{Phase: taskjournal.PhaseExecuting, Completed: value.Completed, Total: value.Total}
		hasLatest = true
		persistProgress(false)
	})
	// The last Engine update can arrive within the throttle window. Flush the
	// most recent observed counters before finalizing the task so a fast pull
	// does not leave only an earlier or zero snapshot in the durable journal.
	persistProgress(true)
	operationCancel()
	authHeader = ""
	verifyCtx, verifyCancel = context.WithTimeout(context.WithoutCancel(ctx), e.verificationTimeout)
	image, inspectErr := e.engine.Inspect(verifyCtx, ref)
	verifyCancel()
	if inspectErr == nil && imageMatchesReference(image, ref) {
		return e.finish(context.WithoutCancel(ctx), dispatch.TaskID, taskstate.Succeeded, taskstate.Evidence{
			ExecutionAttempted: true, ExecutionCompleted: true, PostconditionVerified: true, ActualResultConfirmed: true,
		}, taskjournal.Result{Code: taskjournal.ResultVerified, ObservedState: "present"})
	}
	if errors.Is(context.Cause(ctx), taskstate.ErrCancellationRequested) && errors.Is(pullErr, context.Canceled) && errdefs.IsNotFound(inspectErr) {
		return e.finish(context.WithoutCancel(ctx), dispatch.TaskID, taskstate.Canceled, taskstate.Evidence{
			ExecutionAttempted: true, ProcessTerminated: true, ActualResultConfirmed: true, CancellationConfirmed: true,
		}, taskjournal.Result{Code: taskjournal.ResultCanceled, ObservedState: "absent_after_cancel"})
	}
	if errors.Is(pullErr, ErrUnauthorized) && errdefs.IsNotFound(inspectErr) {
		return e.finish(context.WithoutCancel(ctx), dispatch.TaskID, taskstate.Failed, failureEvidence(), taskjournal.Result{
			Code: taskjournal.ResultFailed, ObservedState: "registry_auth_failed"})
	}
	if (pullErr == nil || errors.Is(pullErr, ErrImageNotFound)) && errdefs.IsNotFound(inspectErr) {
		return e.finish(context.WithoutCancel(ctx), dispatch.TaskID, taskstate.Failed, failureEvidence(), taskjournal.Result{
			Code: taskjournal.ResultFailed, ObservedState: "missing_after_pull"})
	}
	return e.markUnknown(context.WithoutCancel(ctx), dispatch.TaskID, errors.Join(ErrOutcomeUnknown, pullErr, inspectErr))
}

func (e *Executor) remove(ctx context.Context, dispatch protocol.TaskDispatch, _ taskjournal.Snapshot) (taskjournal.Snapshot, error) {
	imageID := dispatch.Intent.ImageID
	verifyCtx, verifyCancel := context.WithTimeout(context.WithoutCancel(ctx), e.verificationTimeout)
	_, err := e.engine.Inspect(verifyCtx, imageID)
	verifyCancel()
	if errdefs.IsNotFound(err) {
		return e.finish(context.WithoutCancel(ctx), dispatch.TaskID, taskstate.Succeeded, successEvidence(), taskjournal.Result{
			Code: taskjournal.ResultVerified, ObservedState: "absent"})
	}
	if err != nil {
		return e.markUnknown(context.WithoutCancel(ctx), dispatch.TaskID, err)
	}
	verifyCtx, verifyCancel = context.WithTimeout(ctx, e.verificationTimeout)
	refs, err := e.engine.ContainersUsing(verifyCtx, imageID)
	verifyCancel()
	if err != nil {
		return e.markUnknown(context.WithoutCancel(ctx), dispatch.TaskID, err)
	}
	if len(refs) > 0 {
		return e.finish(context.WithoutCancel(ctx), dispatch.TaskID, taskstate.Failed, failureEvidence(), taskjournal.Result{
			Code: taskjournal.ResultFailed, ObservedState: "in_use"})
	}
	operationCtx, operationCancel := context.WithTimeout(ctx, e.verificationTimeout)
	removeErr := e.engine.Remove(operationCtx, imageID)
	operationCancel()
	verifyCtx, verifyCancel = context.WithTimeout(context.WithoutCancel(ctx), e.verificationTimeout)
	_, inspectErr := e.engine.Inspect(verifyCtx, imageID)
	verifyCancel()
	if errdefs.IsNotFound(inspectErr) {
		return e.finish(context.WithoutCancel(ctx), dispatch.TaskID, taskstate.Succeeded, successEvidence(), taskjournal.Result{
			Code: taskjournal.ResultVerified, ObservedState: "absent"})
	}
	if errors.Is(removeErr, ErrImageInUse) && inspectErr == nil {
		return e.finish(context.WithoutCancel(ctx), dispatch.TaskID, taskstate.Failed, failureEvidence(), taskjournal.Result{
			Code: taskjournal.ResultFailed, ObservedState: "in_use"})
	}
	if inspectErr == nil && removeErr != nil {
		return e.finish(context.WithoutCancel(ctx), dispatch.TaskID, taskstate.Failed, failureEvidence(), taskjournal.Result{
			Code: taskjournal.ResultFailed, ObservedState: "still_present"})
	}
	return e.markUnknown(context.WithoutCancel(ctx), dispatch.TaskID, errors.Join(removeErr, inspectErr))
}

// ReconcileImage performs reads only; it never repeats pull or remove. An
// unknown pull is successful only when the requested ref is actually present.
func (e *Executor) ReconcileImage(ctx context.Context, taskID string, intent protocol.TaskIntent) (taskjournal.Snapshot, error) {
	task, err := e.journal.Get(ctx, taskID)
	if err != nil || task.Status != taskstate.Unknown || task.Action != string(intent.Action) || !isImageAction(intent.Action) ||
		protocol.ValidateTaskIntent(intent) != nil || task.TargetID != intent.ContainerID {
		return taskjournal.Snapshot{}, errors.New("image task is not reconcilable")
	}
	identity, err := protocol.TaskIdentity(task.TaskID, task.NodeID, task.IdempotencyKey, intent)
	if err != nil {
		return taskjournal.Snapshot{}, errors.New("image task intent is invalid")
	}
	digest, err := taskstate.RequestDigest(identity)
	if err != nil || digest != task.RequestDigest {
		return taskjournal.Snapshot{}, errors.New("image task intent does not match its durable digest")
	}
	verifyCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), e.verificationTimeout)
	defer cancel()
	if intent.Action == protocol.TaskImagePull {
		image, inspectErr := e.engine.Inspect(verifyCtx, intent.ImageReference)
		if inspectErr == nil && imageMatchesReference(image, intent.ImageReference) {
			return e.finish(verifyCtx, taskID, taskstate.Succeeded, successEvidence(), taskjournal.Result{
				Code: taskjournal.ResultVerified, ObservedState: "present"})
		}
		if errdefs.IsNotFound(inspectErr) {
			return e.finish(verifyCtx, taskID, taskstate.Failed, failureEvidence(), taskjournal.Result{
				Code: taskjournal.ResultFailed, ObservedState: "absent_after_pull"})
		}
		return task, ErrOutcomeUnknown
	}
	_, inspectErr := e.engine.Inspect(verifyCtx, intent.ImageID)
	if errdefs.IsNotFound(inspectErr) {
		return e.finish(verifyCtx, taskID, taskstate.Succeeded, successEvidence(), taskjournal.Result{
			Code: taskjournal.ResultVerified, ObservedState: "absent"})
	}
	if inspectErr != nil {
		return task, ErrOutcomeUnknown
	}
	refs, refsErr := e.engine.ContainersUsing(verifyCtx, intent.ImageID)
	if refsErr != nil {
		return task, ErrOutcomeUnknown
	}
	state := "still_present"
	if len(refs) != 0 {
		state = "in_use"
	}
	return e.finish(verifyCtx, taskID, taskstate.Failed, failureEvidence(), taskjournal.Result{
		Code: taskjournal.ResultFailed, ObservedState: state})
}

func (e *Executor) finish(ctx context.Context, taskID string, status taskstate.Status, evidence taskstate.Evidence, result taskjournal.Result) (taskjournal.Snapshot, error) {
	if err := e.journal.Finish(ctx, taskID, status, evidence, result); err != nil {
		_ = e.journal.MarkUnknown(context.WithoutCancel(ctx), taskID)
		return taskjournal.Snapshot{}, errors.New("image task result could not be persisted")
	}
	return e.journal.Get(ctx, taskID)
}

func (e *Executor) markUnknown(ctx context.Context, taskID string, cause error) (taskjournal.Snapshot, error) {
	if err := e.journal.MarkUnknown(ctx, taskID); err != nil {
		return taskjournal.Snapshot{}, errors.New("image task outcome is unknown and could not be persisted")
	}
	task, _ := e.journal.Get(context.WithoutCancel(ctx), taskID)
	return task, errors.Join(ErrOutcomeUnknown, cause)
}

func failureEvidence() taskstate.Evidence {
	return taskstate.Evidence{ExecutionAttempted: true, ExecutionCompleted: true, FailureConfirmed: true, ActualResultConfirmed: true}
}

func successEvidence() taskstate.Evidence {
	return taskstate.Evidence{ExecutionAttempted: true, ExecutionCompleted: true, PostconditionVerified: true, ActualResultConfirmed: true}
}

func imageMatchesReference(image Image, ref string) bool {
	named, err := reference.ParseNormalizedNamed(ref)
	if err != nil {
		return false
	}
	normalized := reference.TagNameOnly(named).String()
	for _, candidate := range append(append([]string(nil), image.Tags...), image.Digests...) {
		if candidate == normalized || candidate == named.String() || candidate == reference.FamiliarString(named) {
			return true
		}
	}
	return strings.TrimSpace(ref) == image.ID
}

func isImageAction(action protocol.TaskAction) bool {
	return action == protocol.TaskImagePull || action == protocol.TaskImageDelete
}

var ErrOutcomeUnknown = errors.New("image operation outcome is unknown and requires read-only reconciliation")
