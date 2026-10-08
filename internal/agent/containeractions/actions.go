// Package containeractions executes the allowlisted Docker container lifecycle
// operations. It owns no transport and never accepts a client-provided Compose
// classification. Every mutation is preceded by a durable Agent journal begin
// and followed by a fresh Docker inspect.
package containeractions

import (
	"context"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/CST-Cat/NodeDance/internal/agent/taskjournal"
	"github.com/CST-Cat/NodeDance/internal/protocol"
	"github.com/CST-Cat/NodeDance/internal/taskstate"
	"github.com/containerd/errdefs"
)

type Action = protocol.TaskAction

const (
	ActionStart   = protocol.TaskStart
	ActionStop    = protocol.TaskStop
	ActionRestart = protocol.TaskRestart
	ActionPause   = protocol.TaskPause
	ActionResume  = protocol.TaskResume
	ActionDelete  = protocol.TaskDelete
	ActionRename  = protocol.TaskRename
)

var (
	ErrInvalidRequest        = errors.New("invalid container action request")
	ErrComposeRename         = errors.New("Compose-managed containers cannot be renamed")
	ErrDeleteRunning         = errors.New("running containers must be stopped by a separate task before deletion")
	ErrOutcomeUnknown        = errors.New("container action outcome is unknown and must be reconciled")
	ErrTaskInProgress        = errors.New("container action is already in progress")
	ErrOperationFailed       = errors.New("container action failed")
	ErrTargetMismatch        = errors.New("Docker Engine returned a different container ID")
	ErrPreconditionFailed    = errors.New("container action precondition failed")
	ErrBootIDUnavailable     = errors.New("host boot identity is unavailable")
	ErrBaselinePersistence   = errors.New("durable pre-mutation baseline could not be confirmed")
	ErrBaselineUnverifiable  = errors.New("durable execution baseline cannot prove the action result")
	ErrRunningUnconfirmed    = errors.New("durable running state could not be confirmed")
	ErrTaskNotReconcileable  = errors.New("only unknown tasks can be reconciled")
	dockerIDPattern          = regexp.MustCompile(`^[0-9a-f]{64}$`)
	bootIDPattern            = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	startedAtLayout          = time.RFC3339Nano
	defaultOperationLimit    = 30 * time.Second
	defaultVerificationLimit = 5 * time.Second
)

// Request is a typed, non-secret action. DeleteConfirmationID is an ephemeral
// confirmation binding; it must equal ContainerID and is deliberately omitted
// from the persisted canonical intent because Core's digest contract contains
// only action, container_id, new_name, and delete_confirmed.
type Request struct {
	TaskID               string
	NodeID               string
	IdempotencyKey       string
	Action               Action
	ContainerID          string
	NewName              string
	DeleteConfirmed      bool
	DeleteConfirmationID string
}

// Container is a small sanitized projection of the Docker Inspect response.
// ComposeManaged is derived only inside the Docker SDK adapter from real
// Inspect labels; it is never accepted in Request.
type Container struct {
	ID             string
	Name           string
	Running        bool
	Paused         bool
	Restarting     bool
	StartedAt      string
	RestartCount   int
	ComposeManaged bool
}

// Engine contains only the lifecycle calls this component is allowed to make.
// Remove has no options so implementations must keep force and volume removal
// disabled.
type Engine interface {
	Inspect(context.Context, string) (Container, error)
	Start(context.Context, string) error
	Stop(context.Context, string) error
	Restart(context.Context, string) error
	Pause(context.Context, string) error
	Resume(context.Context, string) error
	Remove(context.Context, string) error
	Rename(context.Context, string, string) error
}

// Journal is the durable execution boundary provided by the existing Agent
// task journal. Enqueue must persist the intent before BeginExecution; callers
// must not invoke a Docker mutation unless BeginExecution succeeds.
type Journal interface {
	Enqueue(context.Context, taskstate.Identity) (taskjournal.EnqueueResult, error)
	BeginExecution(context.Context, string) error
	PrepareMutation(context.Context, string, taskjournal.ExecutionBaseline) error
	Finish(context.Context, string, taskstate.Status, taskstate.Evidence, taskjournal.Result) error
	MarkUnknown(context.Context, string) error
	Get(context.Context, string) (taskjournal.Snapshot, error)
}

type Options struct {
	OperationTimeout    time.Duration
	VerificationTimeout time.Duration
	BootIDSource        func(context.Context) (string, error)
}

type Executor struct {
	engine              Engine
	journal             Journal
	operationTimeout    time.Duration
	verificationTimeout time.Duration
	bootIDSource        func(context.Context) (string, error)
}

func New(engine Engine, journal Journal, options Options) (*Executor, error) {
	if engine == nil || journal == nil {
		return nil, errors.New("container action engine and journal are required")
	}
	if options.OperationTimeout == 0 {
		options.OperationTimeout = defaultOperationLimit
	}
	if options.VerificationTimeout == 0 {
		options.VerificationTimeout = defaultVerificationLimit
	}
	if options.BootIDSource == nil {
		options.BootIDSource = readHostBootID
	}
	if options.OperationTimeout < time.Second || options.OperationTimeout > 10*time.Minute ||
		options.VerificationTimeout < time.Second || options.VerificationTimeout > time.Minute {
		return nil, errors.New("container action timeouts are outside the supported bounds")
	}
	return &Executor{engine: engine, journal: journal, operationTimeout: options.OperationTimeout,
		verificationTimeout: options.VerificationTimeout, bootIDSource: options.BootIDSource}, nil
}

// Execute accepts an idempotent safe intent, durably begins it, performs at
// most one Docker mutation, then verifies actual Engine state. A running or
// unknown journal entry is never replayed; unknown entries retain their claim
// until a separate reconciliation flow resolves them.
func (e *Executor) Execute(ctx context.Context, request Request) (taskjournal.Snapshot, error) {
	return e.ExecuteObserved(ctx, request, nil)
}

// ExecuteObserved is Execute with a notification hook for the durable running
// transition. The hook runs only after BeginExecution has committed and a
// bounded journal read confirms the persisted running state and evidence. It
// must be quick and nonblocking; taskrunner uses it only to enqueue a report
// wake-up hint.
func (e *Executor) ExecuteObserved(ctx context.Context, request Request, onRunning func(taskjournal.Snapshot)) (taskjournal.Snapshot, error) {
	if ctx == nil {
		return taskjournal.Snapshot{}, ErrInvalidRequest
	}
	identity, err := requestIdentity(request)
	if err != nil {
		return taskjournal.Snapshot{}, err
	}
	enqueued, err := e.journal.Enqueue(ctx, identity)
	if err != nil {
		return taskjournal.Snapshot{}, err
	}
	task := enqueued.Task
	switch task.Status {
	case taskstate.Succeeded, taskstate.Failed, taskstate.TimedOut, taskstate.Canceled:
		return task, nil
	case taskstate.Unknown:
		return task, ErrOutcomeUnknown
	case taskstate.Running:
		return task, ErrTaskInProgress
	case taskstate.Queued:
		// Queued means no executor boundary was crossed. Competing duplicate
		// callers race on this SQLite transition; only its winner may proceed.
	default:
		return task, ErrInvalidRequest
	}
	if err := e.journal.BeginExecution(ctx, task.TaskID); err != nil {
		readCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), e.verificationTimeout)
		latest, readErr := e.journal.Get(readCtx, task.TaskID)
		cancel()
		if readErr != nil {
			return task, fmt.Errorf("begin container action: %w", err)
		}
		if latest.Status == taskstate.Running && latest.Evidence.ExecutionAttempted && onRunning != nil {
			onRunning(latest)
		}
		if stateErr := statusError(latest.Status); stateErr != nil {
			return latest, stateErr
		}
		if latest.Status == taskstate.Succeeded || latest.Status == taskstate.Failed || latest.Status == taskstate.TimedOut || latest.Status == taskstate.Canceled {
			return latest, nil
		}
		return latest, fmt.Errorf("begin container action: %w", err)
	}
	// BeginExecution returning nil means its SQLite commit succeeded. Confirm
	// the durable row before notifying Core or crossing into any Engine call.
	// If the row cannot be read or does not carry the committed running proof,
	// keep the outcome conservative and do not mutate Docker.
	runningCtx, runningCancel := context.WithTimeout(context.WithoutCancel(ctx), e.verificationTimeout)
	running, runningErr := e.journal.Get(runningCtx, task.TaskID)
	runningCancel()
	if runningErr != nil || running.Status != taskstate.Running || !running.Evidence.ExecutionAttempted {
		cause := ErrRunningUnconfirmed
		if runningErr != nil {
			cause = errors.Join(cause, runningErr)
		}
		return e.markUnknown(ctx, task.TaskID, cause)
	}
	if onRunning != nil {
		onRunning(running)
	}

	preflightCtx, preflightCancel := context.WithTimeout(ctx, e.verificationTimeout)
	before, err := e.engine.Inspect(preflightCtx, request.ContainerID)
	preflightCancel()
	if err != nil {
		if isNotFound(err) && request.Action == ActionDelete {
			return e.finishSucceeded(ctx, task.TaskID, "missing", "")
		}
		return e.finishFailed(ctx, task.TaskID, "inspect_failed")
	}
	if before.ID != request.ContainerID {
		return e.finishFailed(ctx, task.TaskID, "target_mismatch")
	}
	if request.Action == ActionRestart && !validRestartObservation(before) {
		return e.finishFailed(ctx, task.TaskID, "invalid_inspect")
	}

	if err := checkPreconditions(request, before); err != nil {
		if errors.Is(err, ErrComposeRename) {
			return e.finishFailed(ctx, task.TaskID, "compose_managed")
		}
		if errors.Is(err, ErrDeleteRunning) {
			return e.finishFailed(ctx, task.TaskID, "running")
		}
		return e.finishFailed(ctx, task.TaskID, "invalid_state")
	}

	if alreadySatisfied(request, before) {
		return e.finishSucceeded(ctx, task.TaskID, observedState(before), "")
	}
	bootCtx, bootCancel := context.WithTimeout(ctx, e.verificationTimeout)
	bootID, bootErr := e.bootIDSource(bootCtx)
	bootCancel()
	if bootErr != nil || !bootIDPattern.MatchString(bootID) {
		return e.markUnknown(ctx, task.TaskID, ErrBootIDUnavailable)
	}
	startedAt, validStartedAt := canonicalStartedAt(before.StartedAt)
	if !validStartedAt {
		return e.finishFailed(ctx, task.TaskID, "invalid_inspect")
	}
	baseline := taskjournal.ExecutionBaseline{
		TargetID: before.ID, Action: string(request.Action), HostBootID: bootID,
		StartedAt: startedAt, RestartCount: before.RestartCount,
		Running: before.Running, Paused: before.Paused, Restarting: before.Restarting,
	}
	baselineCtx, baselineCancel := context.WithTimeout(context.WithoutCancel(ctx), e.verificationTimeout)
	baselineErr := e.journal.PrepareMutation(baselineCtx, task.TaskID, baseline)
	baselineCancel()
	if baselineErr != nil {
		// A failed/ambiguous SQLite commit does not authorize a Docker write.
		return e.markUnknown(ctx, task.TaskID, errors.Join(ErrBaselinePersistence, baselineErr))
	}

	mutationCtx, cancel := context.WithTimeout(ctx, e.operationTimeout)
	mutationErr := e.mutate(mutationCtx, request)
	cancel()

	verifyCtx, verifyCancel := context.WithTimeout(context.WithoutCancel(ctx), e.verificationTimeout)
	after, inspectErr := e.engine.Inspect(verifyCtx, request.ContainerID)
	verifyCancel()
	if request.Action == ActionDelete && isNotFound(inspectErr) {
		return e.finishSucceeded(context.WithoutCancel(ctx), task.TaskID, "missing", "")
	}
	if inspectErr != nil {
		return e.markUnknown(ctx, task.TaskID, ErrOutcomeUnknown)
	}
	if after.ID != request.ContainerID {
		return e.markUnknown(ctx, task.TaskID, ErrTargetMismatch)
	}
	if actionVerified(request, before, after) {
		revision := ""
		if request.Action == ActionRestart {
			if startedAtChanged(before.StartedAt, after.StartedAt) {
				revision = "started_at_changed"
			} else {
				revision = "restart_count_increased"
			}
		}
		return e.finishSucceeded(context.WithoutCancel(ctx), task.TaskID, observedState(after), revision)
	}
	// Do not persist the SDK's free-form error. A request error and a missing
	// postcondition are both uncertain: the call may have reached the daemon.
	cause := ErrOutcomeUnknown
	if mutationErr != nil {
		cause = ErrOperationFailed
	}
	return e.markUnknown(ctx, task.TaskID, cause)
}

// Reconcile confirms only a restart postcondition from a verified durable
// baseline on the same host boot. It never calls the Docker mutation API. A
// changed StartedAt/RestartCount proves a post-baseline restart-like state
// change, not that this process issued exactly one restart request; an external
// actor may have caused the same observation.
func (e *Executor) Reconcile(ctx context.Context, taskID string) (taskjournal.Snapshot, error) {
	if ctx == nil || !validText(taskID, taskstate.MaxIdentityBytes) {
		return taskjournal.Snapshot{}, ErrInvalidRequest
	}
	base := context.WithoutCancel(ctx)
	readCtx, readCancel := context.WithTimeout(base, e.verificationTimeout)
	task, err := e.journal.Get(readCtx, taskID)
	readCancel()
	if err != nil {
		return taskjournal.Snapshot{}, fmt.Errorf("read task for reconciliation: %w", err)
	}
	if task.Status != taskstate.Unknown {
		if err := statusError(task.Status); err != nil {
			return task, err
		}
		return task, ErrTaskNotReconcileable
	}
	if task.ExecutionPhase != taskjournal.ExecutionPhaseMutationMayHaveStarted || task.Baseline == nil ||
		task.Baseline.TargetID != task.TargetID || task.Baseline.Action != task.Action || task.Action != string(ActionRestart) {
		return task, errors.Join(ErrOutcomeUnknown, ErrBaselineUnverifiable)
	}
	bootCtx, bootCancel := context.WithTimeout(base, e.verificationTimeout)
	bootID, err := e.bootIDSource(bootCtx)
	bootCancel()
	if err != nil || !bootIDPattern.MatchString(bootID) || bootID != task.Baseline.HostBootID {
		return task, errors.Join(ErrOutcomeUnknown, ErrBaselineUnverifiable)
	}
	inspectCtx, inspectCancel := context.WithTimeout(base, e.verificationTimeout)
	after, inspectErr := e.engine.Inspect(inspectCtx, task.TargetID)
	inspectCancel()
	if inspectErr != nil || after.ID != task.TargetID || !validRestartObservation(after) ||
		!reconciledRestartEvidence(*task.Baseline, after) {
		return task, errors.Join(ErrOutcomeUnknown, ErrBaselineUnverifiable)
	}
	revision := "reconciled_started_at_change"
	if after.RestartCount > task.Baseline.RestartCount {
		revision = "reconciled_restart_count_increase"
	}
	writeCtx, writeCancel := context.WithTimeout(base, e.verificationTimeout)
	finishErr := e.journal.Finish(writeCtx, task.TaskID, taskstate.Succeeded, taskstate.Evidence{
		ExecutionAttempted: task.Evidence.ExecutionAttempted, ExecutionCompleted: true, PostconditionVerified: true,
	}, taskjournal.Result{Code: taskjournal.ResultVerified, ObservedState: observedState(after), ResourceRevision: revision})
	writeCancel()
	return e.readAfterFinish(ctx, task.TaskID, finishErr)
}

func reconciledRestartEvidence(baseline taskjournal.ExecutionBaseline, after Container) bool {
	return (after.RestartCount > baseline.RestartCount || startedAtChanged(baseline.StartedAt, after.StartedAt)) &&
		after.Running && !after.Paused && !after.Restarting
}

func readHostBootID(context.Context) (string, error) {
	contents, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return "", ErrBootIDUnavailable
	}
	bootID := strings.TrimSpace(string(contents))
	if !bootIDPattern.MatchString(bootID) {
		return "", ErrBootIDUnavailable
	}
	return bootID, nil
}

func requestIdentity(request Request) (taskstate.Identity, error) {
	if !protocol.IsFullContainerID(request.ContainerID) || !validText(request.TaskID, taskstate.MaxIdentityBytes) ||
		!validText(request.NodeID, taskstate.MaxIdentityBytes) || !validText(request.IdempotencyKey, taskstate.MaxIdempotencyKeyBytes) {
		return taskstate.Identity{}, ErrInvalidRequest
	}
	if strings.TrimSpace(request.TaskID) != request.TaskID || strings.TrimSpace(request.NodeID) != request.NodeID {
		return taskstate.Identity{}, ErrInvalidRequest
	}
	intent := protocol.TaskIntent{Action: request.Action, ContainerID: request.ContainerID,
		NewName: request.NewName, DeleteConfirmed: request.DeleteConfirmed}
	if err := protocol.ValidateTaskIntent(intent); err != nil {
		return taskstate.Identity{}, ErrInvalidRequest
	}
	if request.Action == ActionDelete {
		if request.DeleteConfirmationID != request.ContainerID {
			return taskstate.Identity{}, ErrInvalidRequest
		}
	} else if request.DeleteConfirmationID != "" {
		return taskstate.Identity{}, ErrInvalidRequest
	}
	identity, err := protocol.TaskIdentity(request.TaskID, request.NodeID, request.IdempotencyKey, intent)
	if err != nil {
		return taskstate.Identity{}, ErrInvalidRequest
	}
	if _, err := taskstate.RequestDigest(identity); err != nil {
		return taskstate.Identity{}, ErrInvalidRequest
	}
	return identity, nil
}

func validText(value string, maximum int) bool {
	return value != "" && len(value) <= maximum && strings.TrimSpace(value) == value && !strings.ContainsAny(value, "\x00\r\n")
}

func checkPreconditions(request Request, current Container) error {
	if request.Action == ActionRename && current.ComposeManaged {
		return ErrComposeRename
	}
	if request.Action == ActionDelete && (current.Running || current.Paused || current.Restarting) {
		return ErrDeleteRunning
	}
	if request.Action == ActionStart && current.Running && current.Paused {
		return ErrPreconditionFailed
	}
	if request.Action == ActionPause && !current.Running {
		return ErrPreconditionFailed
	}
	if request.Action == ActionResume && !current.Paused && !current.Running {
		return ErrPreconditionFailed
	}
	return nil
}

func alreadySatisfied(request Request, current Container) bool {
	switch request.Action {
	case ActionStart:
		return current.Running && !current.Paused && !current.Restarting
	case ActionStop:
		return !current.Running && !current.Paused && !current.Restarting
	case ActionPause:
		return current.Running && current.Paused
	case ActionResume:
		return current.Running && !current.Paused && !current.Restarting
	case ActionDelete:
		return false
	case ActionRename:
		return canonicalName(current.Name) == request.NewName
	default:
		return false
	}
}

func (e *Executor) mutate(ctx context.Context, request Request) error {
	switch request.Action {
	case ActionStart:
		return e.engine.Start(ctx, request.ContainerID)
	case ActionStop:
		return e.engine.Stop(ctx, request.ContainerID)
	case ActionRestart:
		return e.engine.Restart(ctx, request.ContainerID)
	case ActionPause:
		return e.engine.Pause(ctx, request.ContainerID)
	case ActionResume:
		return e.engine.Resume(ctx, request.ContainerID)
	case ActionDelete:
		return e.engine.Remove(ctx, request.ContainerID)
	case ActionRename:
		return e.engine.Rename(ctx, request.ContainerID, request.NewName)
	default:
		return ErrInvalidRequest
	}
}

func actionVerified(request Request, before, after Container) bool {
	switch request.Action {
	case ActionStart, ActionResume:
		return after.Running && !after.Paused && !after.Restarting
	case ActionStop:
		return !after.Running && !after.Paused && !after.Restarting
	case ActionPause:
		return after.Running && after.Paused
	case ActionRename:
		return canonicalName(after.Name) == request.NewName
	case ActionRestart:
		if !after.Running || after.Paused || after.Restarting {
			return false
		}
		if !validRestartObservation(before) || !validRestartObservation(after) {
			return false
		}
		return startedAtChanged(before.StartedAt, after.StartedAt) || after.RestartCount > before.RestartCount
	default:
		return false
	}
}

func startedAtChanged(before, after string) bool {
	oldTime, oldErr := time.Parse(startedAtLayout, before)
	newTime, newErr := time.Parse(startedAtLayout, after)
	return oldErr == nil && newErr == nil && !newTime.IsZero() && !oldTime.Equal(newTime)
}

func canonicalStartedAt(value string) (string, bool) {
	parsed, err := time.Parse(startedAtLayout, value)
	if err != nil {
		return "", false
	}
	return parsed.UTC().Format(startedAtLayout), true
}

func validRestartObservation(container Container) bool {
	startedAt, err := time.Parse(startedAtLayout, container.StartedAt)
	if err != nil || container.RestartCount < 0 {
		return false
	}
	if (container.Running || container.Paused || container.Restarting) && startedAt.IsZero() {
		return false
	}
	if container.Paused && !container.Running {
		return false
	}
	return true
}

func canonicalName(name string) string { return strings.TrimPrefix(name, "/") }

func observedState(container Container) string {
	if container.Restarting {
		return "restarting"
	}
	if container.Paused {
		return "paused"
	}
	if container.Running {
		return "running"
	}
	return "stopped"
}

func (e *Executor) finishSucceeded(ctx context.Context, taskID, state, revision string) (taskjournal.Snapshot, error) {
	writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), e.verificationTimeout)
	defer cancel()
	err := e.journal.Finish(writeCtx, taskID, taskstate.Succeeded, taskstate.Evidence{
		ExecutionAttempted: true, ExecutionCompleted: true, PostconditionVerified: true,
	}, taskjournal.Result{Code: taskjournal.ResultVerified, ObservedState: state, ResourceRevision: revision})
	return e.readAfterFinish(ctx, taskID, err)
}

func (e *Executor) finishFailed(ctx context.Context, taskID, state string) (taskjournal.Snapshot, error) {
	writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), e.verificationTimeout)
	defer cancel()
	err := e.journal.Finish(writeCtx, taskID, taskstate.Failed, taskstate.Evidence{
		ExecutionAttempted: true, ExecutionCompleted: true, FailureConfirmed: true, ActualResultConfirmed: true,
	}, taskjournal.Result{Code: taskjournal.ResultFailed, ObservedState: state})
	return e.readAfterFinish(ctx, taskID, err)
}

func (e *Executor) readAfterFinish(ctx context.Context, taskID string, finishErr error) (taskjournal.Snapshot, error) {
	readCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), e.verificationTimeout)
	defer cancel()
	task, readErr := e.journal.Get(readCtx, taskID)
	if finishErr != nil {
		return task, fmt.Errorf("persist container action result: %w", finishErr)
	}
	if readErr != nil {
		return taskjournal.Snapshot{}, fmt.Errorf("read container action result: %w", readErr)
	}
	return task, nil
}

func (e *Executor) markUnknown(ctx context.Context, taskID string, cause error) (taskjournal.Snapshot, error) {
	base := context.WithoutCancel(ctx)
	writeCtx, cancel := context.WithTimeout(base, e.verificationTimeout)
	markErr := e.journal.MarkUnknown(writeCtx, taskID)
	cancel()
	readCtx, readCancel := context.WithTimeout(base, e.verificationTimeout)
	task, readErr := e.journal.Get(readCtx, taskID)
	readCancel()
	if markErr != nil {
		return task, fmt.Errorf("%w: persist uncertain task state: %v", ErrOutcomeUnknown, markErr)
	}
	if readErr != nil {
		return taskjournal.Snapshot{}, fmt.Errorf("%w: read task journal: %v", ErrOutcomeUnknown, readErr)
	}
	if cause != nil {
		return task, errors.Join(ErrOutcomeUnknown, cause)
	}
	return task, ErrOutcomeUnknown
}

func statusError(status taskstate.Status) error {
	switch status {
	case taskstate.Running:
		return ErrTaskInProgress
	case taskstate.Unknown:
		return ErrOutcomeUnknown
	case taskstate.Queued:
		return nil
	default:
		return nil
	}
}

func isNotFound(err error) bool {
	return err != nil && errdefs.IsNotFound(err)
}
