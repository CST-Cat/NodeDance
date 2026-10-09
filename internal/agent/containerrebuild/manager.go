package containerrebuild

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/CST-Cat/NodeDance/internal/agent/taskjournal"
	"github.com/CST-Cat/NodeDance/internal/protocol"
	"github.com/CST-Cat/NodeDance/internal/taskstate"
	"github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
)

const (
	defaultOperationTimeout = 15 * time.Minute
	defaultVerifyTimeout    = 2 * time.Minute
	defaultHealthPoll       = time.Second
	maxStopTimeout          = 30
)

type Options struct {
	OperationTimeout time.Duration
	VerifyTimeout    time.Duration
	HealthPoll       time.Duration
	Now              func() time.Time
}

type Manager struct {
	engine  Engine
	journal TaskJournal
	store   *Store
	opts    Options
}

func NewManager(engine Engine, journal TaskJournal, store *Store, options Options) (*Manager, error) {
	if engine == nil || journal == nil || store == nil {
		return nil, errors.New("container rebuild Engine, task journal, and durable store are required")
	}
	if options.OperationTimeout == 0 {
		options.OperationTimeout = defaultOperationTimeout
	}
	if options.VerifyTimeout == 0 {
		options.VerifyTimeout = defaultVerifyTimeout
	}
	if options.HealthPoll == 0 {
		options.HealthPoll = defaultHealthPoll
	}
	if options.OperationTimeout < time.Second || options.OperationTimeout > 30*time.Minute ||
		options.VerifyTimeout < time.Second || options.VerifyTimeout > 5*time.Minute ||
		options.HealthPoll < 100*time.Millisecond || options.HealthPoll > 10*time.Second {
		return nil, errors.New("container rebuild timeouts are outside supported bounds")
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	return &Manager{engine: engine, journal: journal, store: store, opts: options}, nil
}

// Plan performs a fresh Engine Inspect and refuses unsupported settings before
// any task is accepted or resource is changed.
func (m *Manager) Plan(ctx context.Context, containerID string, spec protocol.RebuildSpec) (Plan, error) {
	current, err := m.engine.Inspect(ctx, containerID)
	if err != nil {
		return Plan{}, err
	}
	if current.ID != containerID {
		return Plan{}, ErrUnsupportedConfiguration
	}
	return BuildPlan(current, spec)
}

func (m *Manager) ExecuteObserved(ctx context.Context, request Request, onRunning func(taskjournal.Snapshot)) (taskjournal.Snapshot, error) {
	if request.Action == protocol.TaskRebuildCleanup {
		return m.CleanupObserved(ctx, request, onRunning)
	}
	if request.Action != protocol.TaskRebuild || request.Spec == nil || ctx == nil {
		return taskjournal.Snapshot{}, ErrUnsupportedConfiguration
	}
	intent := protocol.TaskIntent{Action: protocol.TaskRebuild, ContainerID: request.ContainerID, Rebuild: request.Spec}
	identity, err := protocol.TaskIdentity(request.TaskID, request.NodeID, request.IdempotencyKey, intent)
	if err != nil {
		return taskjournal.Snapshot{}, ErrUnsupportedConfiguration
	}
	enqueued, err := m.journal.Enqueue(ctx, identity)
	if err != nil {
		return taskjournal.Snapshot{}, err
	}
	if done, doneErr := terminal(enqueued.Task); done {
		return enqueued.Task, doneErr
	}
	if enqueued.Task.Status == taskstate.Unknown {
		return enqueued.Task, ErrRecoveryRequired
	}
	if enqueued.Task.Status == taskstate.Running {
		return enqueued.Task, errors.New("container rebuild is already running")
	}
	if err := m.journal.BeginExecution(ctx, request.TaskID); err != nil {
		return m.taskAfterError(ctx, request.TaskID, err)
	}
	running, err := m.journal.Get(context.WithoutCancel(ctx), request.TaskID)
	if err != nil || running.Status != taskstate.Running || !running.Evidence.ExecutionAttempted {
		return m.markUnknown(ctx, request.TaskID, ErrRecoveryRequired)
	}
	if onRunning != nil {
		onRunning(running)
	}
	return m.executeNew(ctx, request)
}

func (m *Manager) executeNew(ctx context.Context, request Request) (taskjournal.Snapshot, error) {
	inspectCtx, cancel := context.WithTimeout(ctx, m.opts.VerifyTimeout)
	current, err := m.engine.Inspect(inspectCtx, request.ContainerID)
	cancel()
	if err != nil || current.ID != request.ContainerID {
		return m.markUnknown(ctx, request.TaskID, ErrRecoveryRequired)
	}
	plan, err := BuildPlan(current, *request.Spec)
	if err != nil {
		return m.finishFailed(ctx, request.TaskID, "unchanged", "rebuild_rejected")
	}
	if plan.SnapshotRequired {
		if err := m.ensureSnapshotSpace(ctx, current.WritableSize); err != nil {
			if errors.Is(err, ErrInsufficientSpace) {
				return m.finishFailed(ctx, request.TaskID, "unchanged", "snapshot_space_low")
			}
			return m.finishFailed(ctx, request.TaskID, "unchanged", "snapshot_preflight_failed")
		}
	}
	if current.State == "dead" || current.State == "removing" || current.Restarting {
		return m.finishFailed(ctx, request.TaskID, "unchanged", "unsupported_state")
	}
	backupName, snapshotRef := generatedNames(request.TaskID)
	policyJSON, _ := json.Marshal(current.HostConfig.RestartPolicy)
	record, created, err := m.store.PutNew(ctx, Record{
		TaskID: request.TaskID, NodeID: request.NodeID, OriginalID: current.ID, OriginalName: strings.TrimPrefix(current.Name, "/"),
		BackupName: backupName, SnapshotRef: snapshotRef, PreviousRunning: current.Running,
		RestartPolicy: string(policyJSON), Phase: PhasePreflight, Spec: *request.Spec, Networks: current.Networks,
	})
	if err != nil {
		return m.markUnknown(ctx, request.TaskID, err)
	}
	if !created {
		return m.markUnknown(ctx, request.TaskID, ErrRecoveryRequired)
	}
	if err := m.runForward(ctx, &record, current); err != nil {
		if record.Phase == PhasePreflight || record.Phase == PhaseSnapshotting {
			if err := m.verifyOriginalUnchanged(ctx, record); err == nil {
				record.Outcome, record.ErrorCode = "failed", sanitizedCode(err)
				_ = m.store.Save(context.WithoutCancel(ctx), record)
				return m.finishFailed(ctx, request.TaskID, "unchanged", record.ErrorCode)
			}
		}
		return m.rollback(ctx, record, sanitizedCode(err))
	}
	return m.finishSucceeded(ctx, request.TaskID, "running", record.NewID)
}

func (m *Manager) runForward(ctx context.Context, record *Record, initial Container) error {
	current, err := m.engine.Inspect(ctx, record.OriginalID)
	if err != nil || current.ID != record.OriginalID {
		return ErrRecoveryRequired
	}
	if record.Phase == PhasePreflight || record.Phase == PhaseSnapshotting {
		if current.Name != "/"+record.OriginalName && current.Name != record.OriginalName {
			return ErrRecordConflict
		}
		if current.WritableSize > 0 {
			if err := m.ensureSnapshotSpace(ctx, current.WritableSize); err != nil {
				return err
			}
			if err := m.setPhase(ctx, record, PhaseSnapshotting, 1, 6); err != nil {
				return err
			}
			imageID, imageLabels, inspectErr := m.engine.ImageInspect(ctx, record.SnapshotRef)
			if inspectErr == nil {
				if imageLabels[markerTaskLabel] != record.TaskID || imageID == "" {
					return ErrRecordConflict
				}
				record.SnapshotID = imageID
			} else {
				labels := map[string]string{markerTaskLabel: record.TaskID, markerOwnerLabel: "true"}
				imageID, err = m.engine.Commit(ctx, record.OriginalID, record.SnapshotRef, labels)
				if err != nil || imageID == "" {
					return ErrSnapshotFailed
				}
				verifiedID, verifiedLabels, verifyErr := m.engine.ImageInspect(ctx, record.SnapshotRef)
				if verifyErr != nil || verifiedID != imageID || verifiedLabels[markerTaskLabel] != record.TaskID {
					return ErrSnapshotFailed
				}
				record.SnapshotID = imageID
			}
		}
		if err := m.setPhase(ctx, record, PhaseSnapshotted, 2, 6); err != nil {
			return err
		}
		current, err = m.engine.Inspect(ctx, record.OriginalID)
		if err != nil || current.ID != record.OriginalID {
			return ErrRecoveryRequired
		}
	}
	if record.Phase == PhaseSnapshotted || record.Phase == PhaseStopping {
		if err := m.setPhase(ctx, record, PhaseStopping, 2, 6); err != nil {
			return err
		}
		if current.Running || current.Paused || current.Restarting {
			if err := m.engine.Stop(ctx, current.ID, maxStopTimeout); err != nil {
				return err
			}
		}
		current, err = m.engine.Inspect(ctx, record.OriginalID)
		if err != nil || current.Running || current.Paused || current.Restarting {
			return ErrRecoveryRequired
		}
		if err := m.engine.UpdateRestartPolicy(ctx, record.OriginalID, container.RestartPolicy{Name: "no"}); err != nil {
			return err
		}
		if err := m.setPhase(ctx, record, PhaseStopped, 3, 6); err != nil {
			return err
		}
	}
	if record.Phase == PhaseStopped || record.Phase == PhaseRenaming {
		if err := m.setPhase(ctx, record, PhaseRenaming, 3, 6); err != nil {
			return err
		}
		current, err = m.engine.Inspect(ctx, record.OriginalID)
		if err != nil {
			return err
		}
		if strings.TrimPrefix(current.Name, "/") == record.OriginalName {
			if err := m.engine.Rename(ctx, record.OriginalID, record.BackupName); err != nil {
				return err
			}
		} else if strings.TrimPrefix(current.Name, "/") != record.BackupName {
			return ErrRecordConflict
		}
		if err := m.setPhase(ctx, record, PhaseRenamed, 4, 6); err != nil {
			return err
		}
	}
	if record.Phase == PhaseRenamed || record.Phase == PhaseDetaching {
		if err := m.setPhase(ctx, record, PhaseDetaching, 4, 6); err != nil {
			return err
		}
		current, err = m.engine.Inspect(ctx, record.OriginalID)
		if err != nil {
			return err
		}
		networks := sortedNetworkNames(current.Networks)
		for _, name := range networks {
			endpoint := current.Networks[name]
			if endpoint == nil {
				return ErrRecoveryRequired
			}
			if err := m.engine.DisconnectNetwork(ctx, networkIdentifier(name, endpoint), record.OriginalID); err != nil {
				// A prior attempt may already have disconnected this endpoint.
				latest, inspectErr := m.engine.Inspect(ctx, record.OriginalID)
				if inspectErr != nil || hasNetwork(latest.Networks, name) {
					return err
				}
			}
		}
		if err := m.setPhase(ctx, record, PhaseDetached, 5, 6); err != nil {
			return err
		}
	}
	if record.Phase == PhaseDetached || record.Phase == PhaseCreating {
		if err := m.setPhase(ctx, record, PhaseCreating, 5, 6); err != nil {
			return err
		}
		current, err = m.engine.Inspect(ctx, record.OriginalID)
		if err != nil {
			return err
		}
		newContainer, inspectErr := m.engine.Inspect(ctx, record.OriginalName)
		if inspectErr == nil {
			if newContainer.Config == nil || newContainer.Config.Labels[markerTaskLabel] != record.TaskID || newContainer.Name != "/"+record.OriginalName {
				return ErrRecordConflict
			}
			record.NewID = newContainer.ID
		} else {
			config, hostConfig, endpoints, err := createConfig(current, record.Spec, record.TaskID, record.SnapshotRef, record.SnapshotID, record.RestartPolicy, record.Networks)
			if err != nil {
				return err
			}
			newID, createErr := m.engine.Create(ctx, record.OriginalName, config, hostConfig, endpoints)
			if createErr != nil || newID == "" {
				return createErr
			}
			record.NewID = newID
		}
		if err := m.store.Save(ctx, *record); err != nil {
			return err
		}
		if err := m.setPhase(ctx, record, PhaseCreated, 5, 6); err != nil {
			return err
		}
	}
	if record.Phase == PhaseCreated || record.Phase == PhaseStarting || record.Phase == PhaseStarted || record.Phase == PhaseVerifying {
		if record.NewID == "" {
			latest, err := m.engine.Inspect(ctx, record.OriginalName)
			if err != nil || latest.Config == nil || latest.Config.Labels[markerTaskLabel] != record.TaskID {
				return ErrRecordConflict
			}
			record.NewID = latest.ID
			if err := m.store.Save(ctx, *record); err != nil {
				return err
			}
		}
		if err := m.setPhase(ctx, record, PhaseStarting, 5, 6); err != nil {
			return err
		}
		newContainer, err := m.engine.Inspect(ctx, record.NewID)
		if err != nil || newContainer.ID != record.NewID || newContainer.Config == nil || newContainer.Config.Labels[markerTaskLabel] != record.TaskID {
			return ErrRecordConflict
		}
		if record.PreviousRunning && !newContainer.Running {
			if err := m.engine.Start(ctx, record.NewID); err != nil {
				return err
			}
		}
		if err := m.setPhase(ctx, record, PhaseStarted, 6, 6); err != nil {
			return err
		}
		if record.PreviousRunning {
			if err := m.waitHealthyAndPorts(ctx, record); err != nil {
				return err
			}
		} else {
			newContainer, err = m.engine.Inspect(ctx, record.NewID)
			if err != nil || newContainer.Running || !portsConfigured(newContainer, record.Spec, current) {
				return ErrPortVerificationFailed
			}
		}
		if err := m.setPhase(ctx, record, PhaseVerified, 6, 6); err != nil {
			return err
		}
		record.Outcome = "succeeded"
		if err := m.store.Save(ctx, *record); err != nil {
			return err
		}
	}
	return nil
}

func (m *Manager) rollback(ctx context.Context, record Record, code string) (taskjournal.Snapshot, error) {
	if record.Outcome == "succeeded" {
		return m.finishSucceeded(ctx, record.TaskID, "running", record.NewID)
	}
	record.Phase = PhaseRollingBack
	record.ErrorCode = boundedCode(code)
	if err := m.store.Save(context.WithoutCancel(ctx), record); err != nil {
		return m.markUnknown(ctx, record.TaskID, err)
	}
	if record.NewID == "" {
		if candidate, err := m.engine.Inspect(context.WithoutCancel(ctx), record.OriginalName); err == nil {
			if candidate.Config == nil || candidate.Config.Labels[markerTaskLabel] != record.TaskID {
				return m.markUnknown(ctx, record.TaskID, ErrRecordConflict)
			}
			record.NewID = candidate.ID
		}
	}
	if record.NewID != "" {
		newContainer, err := m.engine.Inspect(context.WithoutCancel(ctx), record.NewID)
		if err == nil {
			if newContainer.Config == nil || newContainer.Config.Labels[markerTaskLabel] != record.TaskID {
				return m.markUnknown(ctx, record.TaskID, ErrRecordConflict)
			}
			if newContainer.Running || newContainer.Paused || newContainer.Restarting {
				if err := m.engine.Stop(context.WithoutCancel(ctx), record.NewID, maxStopTimeout); err != nil {
					return m.markUnknown(ctx, record.TaskID, err)
				}
			}
			if err := m.engine.Remove(context.WithoutCancel(ctx), record.NewID); err != nil {
				return m.markUnknown(ctx, record.TaskID, err)
			}
		} else if !isNotFound(err) {
			return m.markUnknown(ctx, record.TaskID, err)
		}
	}
	old, err := m.engine.Inspect(context.WithoutCancel(ctx), record.OriginalID)
	if err != nil || old.ID != record.OriginalID {
		return m.markUnknown(ctx, record.TaskID, ErrRecordConflict)
	}
	if strings.TrimPrefix(old.Name, "/") == record.BackupName {
		if err := m.reconnectNetworks(context.WithoutCancel(ctx), record.OriginalID, record.Networks); err != nil {
			return m.markUnknown(ctx, record.TaskID, err)
		}
		if err := m.engine.Rename(context.WithoutCancel(ctx), record.OriginalID, record.OriginalName); err != nil {
			return m.markUnknown(ctx, record.TaskID, err)
		}
	} else if strings.TrimPrefix(old.Name, "/") != record.OriginalName {
		return m.markUnknown(ctx, record.TaskID, ErrRecordConflict)
	}
	policy := container.RestartPolicy{}
	if err := json.Unmarshal([]byte(record.RestartPolicy), &policy); err != nil {
		return m.markUnknown(ctx, record.TaskID, ErrRecordConflict)
	}
	if err := m.engine.UpdateRestartPolicy(context.WithoutCancel(ctx), record.OriginalID, policy); err != nil {
		return m.markUnknown(ctx, record.TaskID, err)
	}
	old, err = m.engine.Inspect(context.WithoutCancel(ctx), record.OriginalID)
	if err != nil {
		return m.markUnknown(ctx, record.TaskID, err)
	}
	if record.PreviousRunning && !old.Running {
		if err := m.engine.Start(context.WithoutCancel(ctx), record.OriginalID); err != nil {
			return m.markUnknown(ctx, record.TaskID, err)
		}
	} else if !record.PreviousRunning && old.Running {
		if err := m.engine.Stop(context.WithoutCancel(ctx), record.OriginalID, maxStopTimeout); err != nil {
			return m.markUnknown(ctx, record.TaskID, err)
		}
	}
	record.Phase, record.Outcome = PhaseRolledBack, "failed"
	record.ErrorCode = boundedCode(code)
	if err := m.store.Save(context.WithoutCancel(ctx), record); err != nil {
		return m.markUnknown(ctx, record.TaskID, err)
	}
	return m.finishRestoredFailure(ctx, record.TaskID, record.ErrorCode, record.OriginalID)
}

func (m *Manager) Reconcile(ctx context.Context, taskID string) (taskjournal.Snapshot, error) {
	task, err := m.journal.Get(ctx, taskID)
	if err != nil {
		return taskjournal.Snapshot{}, err
	}
	if task.Action == string(protocol.TaskRebuildCleanup) {
		record, err := m.store.GetByCleanupTask(ctx, taskID)
		if err != nil {
			return m.markUnknown(ctx, taskID, ErrRecoveryRequired)
		}
		return m.resumeCleanup(ctx, task, record)
	}
	if task.Action != string(protocol.TaskRebuild) {
		return task, ErrRecoveryRequired
	}
	record, err := m.store.Get(ctx, taskID)
	if err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			return m.markUnknown(ctx, taskID, err)
		}
		if original, inspectErr := m.engine.Inspect(ctx, task.TargetID); inspectErr == nil && original.ID == task.TargetID {
			return m.finishFailed(ctx, taskID, "unchanged", "interrupted_before_mutation")
		}
		return m.markUnknown(ctx, taskID, ErrRecoveryRequired)
	}
	if record.Outcome == "succeeded" {
		newContainer, inspectErr := m.engine.Inspect(ctx, record.NewID)
		if inspectErr == nil && newContainer.ID == record.NewID && newContainer.Running == record.PreviousRunning {
			return m.finishSucceeded(ctx, taskID, stateToken(newContainer), record.NewID)
		}
	}
	if record.Outcome == "failed" && record.Phase == PhaseRolledBack {
		old, inspectErr := m.engine.Inspect(ctx, record.OriginalID)
		if inspectErr == nil && strings.TrimPrefix(old.Name, "/") == record.OriginalName && old.Running == record.PreviousRunning {
			return m.finishRestoredFailure(ctx, taskID, record.ErrorCode, record.OriginalID)
		}
	}
	// Recovery never replays forward. It either verifies a fully-created
	// replacement, or performs an explicit compensating restore of the original.
	return m.rollback(ctx, record, firstNonEmpty(record.ErrorCode, "agent_restart_recovery"))
}

func (m *Manager) CleanupObserved(ctx context.Context, request Request, onRunning func(taskjournal.Snapshot)) (taskjournal.Snapshot, error) {
	if request.Action != protocol.TaskRebuildCleanup || request.Spec == nil || request.Spec.CleanupTaskID == "" ||
		request.ConfirmationID != request.ContainerID || ctx == nil {
		return taskjournal.Snapshot{}, ErrCleanupConfirmation
	}
	intent := protocol.TaskIntent{Action: protocol.TaskRebuildCleanup, ContainerID: request.ContainerID, Rebuild: request.Spec}
	identity, err := protocol.TaskIdentity(request.TaskID, request.NodeID, request.IdempotencyKey, intent)
	if err != nil {
		return taskjournal.Snapshot{}, ErrUnsupportedConfiguration
	}
	enqueued, err := m.journal.Enqueue(ctx, identity)
	if err != nil {
		return taskjournal.Snapshot{}, err
	}
	if done, doneErr := terminal(enqueued.Task); done {
		return enqueued.Task, doneErr
	}
	if enqueued.Task.Status == taskstate.Unknown {
		return enqueued.Task, ErrRecoveryRequired
	}
	if enqueued.Task.Status == taskstate.Running {
		return enqueued.Task, ErrRecoveryRequired
	}
	if err := m.journal.BeginExecution(ctx, request.TaskID); err != nil {
		return m.taskAfterError(ctx, request.TaskID, err)
	}
	running, err := m.journal.Get(context.WithoutCancel(ctx), request.TaskID)
	if err != nil || running.Status != taskstate.Running {
		return m.markUnknown(ctx, request.TaskID, ErrRecoveryRequired)
	}
	if onRunning != nil {
		onRunning(running)
	}
	record, err := m.store.BindCleanup(ctx, request.Spec.CleanupTaskID, request.TaskID, request.ContainerID)
	if err != nil {
		return m.finishFailed(ctx, request.TaskID, "unchanged", "cleanup_rejected")
	}
	return m.resumeCleanup(ctx, running, record)
}

func (m *Manager) resumeCleanup(ctx context.Context, task taskjournal.Snapshot, record Record) (taskjournal.Snapshot, error) {
	if record.CleanupTaskID != task.TaskID || record.CleanupTargetID != task.TargetID || task.Action != string(protocol.TaskRebuildCleanup) {
		return m.markUnknown(ctx, task.TaskID, ErrRecordConflict)
	}
	current, err := m.engine.Inspect(ctx, record.CleanupTargetID)
	if err != nil || current.ID != record.CleanupTargetID || current.Config == nil {
		return m.finishFailed(ctx, task.TaskID, "unchanged", "cleanup_target_missing")
	}
	if record.Outcome == "succeeded" {
		if record.NewID == "" || current.ID != record.NewID {
			return m.finishFailed(ctx, task.TaskID, "unchanged", "cleanup_target_mismatch")
		}
		if strings.TrimPrefix(current.Name, "/") != record.OriginalName || current.Running != record.PreviousRunning ||
			current.Config.Labels[markerTaskLabel] != record.TaskID || current.Config.Labels[markerOwnerLabel] != "true" {
			return m.markUnknown(ctx, task.TaskID, ErrRecordConflict)
		}
	} else if record.Outcome == "failed" {
		if current.ID != record.OriginalID || strings.TrimPrefix(current.Name, "/") != record.OriginalName || current.Running != record.PreviousRunning {
			return m.finishFailed(ctx, task.TaskID, "unchanged", "cleanup_target_mismatch")
		}
	} else {
		return m.finishFailed(ctx, task.TaskID, "unchanged", "cleanup_not_ready")
	}
	// Inspect and validate every resource before deleting the old container.
	// In particular, never destroy the only rollback instance when the new
	// instance or a snapshot has an unexpected identity or owner marker.
	var oldContainer *Container
	if record.Outcome == "succeeded" {
		old, inspectErr := m.engine.Inspect(context.WithoutCancel(ctx), record.OriginalID)
		if inspectErr == nil {
			if strings.TrimPrefix(old.Name, "/") != record.BackupName || old.Running || old.Paused || old.Restarting {
				return m.finishFailed(ctx, task.TaskID, "unchanged", "rollback_resource_changed")
			}
			oldContainer = &old
		} else if !isNotFound(inspectErr) {
			return m.markUnknown(ctx, task.TaskID, inspectErr)
		}
	}
	snapshotRetained := false
	var snapshotID string
	snapshotRemoveRef := record.SnapshotRef
	var snapshotExists bool
	if record.SnapshotRef != "" {
		imageID, labels, inspectErr := m.engine.ImageInspect(context.WithoutCancel(ctx), record.SnapshotRef)
		if isNotFound(inspectErr) && record.SnapshotID != "" {
			snapshotRemoveRef = record.SnapshotID
			imageID, labels, inspectErr = m.engine.ImageInspect(context.WithoutCancel(ctx), record.SnapshotID)
		}
		if inspectErr == nil {
			if imageID == "" || labels[markerTaskLabel] != record.TaskID || labels[markerOwnerLabel] != "true" || (record.SnapshotID != "" && imageID != record.SnapshotID) {
				return m.markUnknown(ctx, task.TaskID, ErrRecordConflict)
			}
			snapshotID, snapshotExists = imageID, true
			if record.Outcome == "succeeded" && record.NewID != "" {
				snapshotRetained = current.Config.Image == imageID
			}
		} else if !isNotFound(inspectErr) || record.SnapshotID != "" {
			return m.markUnknown(ctx, task.TaskID, inspectErr)
		}
	}
	if record.SnapshotID == "" && snapshotExists {
		record.SnapshotID = snapshotID
		if err := m.store.Save(context.WithoutCancel(ctx), record); err != nil {
			return m.markUnknown(ctx, task.TaskID, err)
		}
	}
	if oldContainer != nil {
		if err := m.engine.Remove(context.WithoutCancel(ctx), oldContainer.ID); err != nil {
			return m.markUnknown(ctx, task.TaskID, err)
		}
	}
	if snapshotExists && !snapshotRetained {
		if err := m.engine.RemoveImage(context.WithoutCancel(ctx), snapshotRemoveRef); err != nil {
			return m.markUnknown(ctx, task.TaskID, err)
		}
	}
	record.CleanupPhase = PhaseCleaned
	if err := m.store.Save(context.WithoutCancel(ctx), record); err != nil {
		return m.markUnknown(ctx, task.TaskID, err)
	}
	if snapshotRetained {
		return m.finishSucceeded(ctx, task.TaskID, "rollback_container_removed_snapshot_retained", "rollback_resources_cleaned")
	}
	return m.finishSucceeded(ctx, task.TaskID, "rollback_resources_cleaned", "rollback_resources_removed")
}

func (m *Manager) ensureSnapshotSpace(ctx context.Context, size int64) error {
	if size <= 0 {
		return nil
	}
	info, err := m.engine.Info(ctx)
	if err != nil || info.RootDirectory == "" {
		return errors.New("Docker storage location is unavailable")
	}
	var stats syscall.Statfs_t
	if err := syscall.Statfs(info.RootDirectory, &stats); err != nil {
		return errors.New("Docker storage capacity could not be checked")
	}
	available := uint64(stats.Bavail) * uint64(stats.Bsize)
	need := uint64(size)
	reserve := need / 5
	if reserve < minimumSnapshotReserve {
		reserve = minimumSnapshotReserve
	}
	if need > ^uint64(0)-reserve || available < need+reserve {
		return ErrInsufficientSpace
	}
	return nil
}

func (m *Manager) setPhase(ctx context.Context, record *Record, phase Phase, completed, total uint64) error {
	record.Phase = phase
	if err := m.store.Save(ctx, *record); err != nil {
		return err
	}
	return m.journal.UpdateProgress(ctx, record.TaskID, taskjournal.Progress{Phase: progressPhase(phase), Completed: completed, Total: total})
}

func progressPhase(phase Phase) taskjournal.ProgressPhase {
	switch phase {
	case PhasePreflight, PhaseSnapshotting, PhaseSnapshotted:
		return taskjournal.PhasePreparing
	case PhaseVerified:
		return taskjournal.PhaseVerifying
	case PhaseRollingBack, PhaseRolledBack, PhaseCleaning, PhaseCleaned:
		return taskjournal.PhaseReconciling
	default:
		return taskjournal.PhaseExecuting
	}
}

func createConfig(current Container, spec protocol.RebuildSpec, taskID, snapshotRef, snapshotID, restartPolicyJSON string, priorNetworks map[string]*network.EndpointSettings) (*container.Config, *container.HostConfig, map[string]*network.EndpointSettings, error) {
	config, err := cloneJSON(*current.Config)
	if err != nil {
		return nil, nil, nil, ErrUnsupportedConfiguration
	}
	host, err := cloneJSON(*current.HostConfig)
	if err != nil {
		return nil, nil, nil, ErrUnsupportedConfiguration
	}
	if err := json.Unmarshal([]byte(restartPolicyJSON), &host.RestartPolicy); err != nil {
		return nil, nil, nil, ErrUnsupportedConfiguration
	}
	config.Image = current.ImageID
	if snapshotID != "" {
		config.Image = snapshotID
	} else if snapshotRef != "" && current.WritableSize > 0 {
		config.Image = snapshotRef
	}
	config.Labels = cloneStrings(config.Labels)
	config.Labels[markerTaskLabel] = taskID
	config.Labels[markerOwnerLabel] = "true"
	bindings, err := buildPortMap(host.PortBindings, config.ExposedPorts, spec)
	if err != nil {
		return nil, nil, nil, err
	}
	host.PortBindings = bindings
	if err := preserveMounts(&config, &host, current.Mounts); err != nil {
		return nil, nil, nil, err
	}
	endpoints := make(map[string]*network.EndpointSettings, len(priorNetworks))
	for name, endpoint := range priorNetworks {
		if endpoint == nil || strings.TrimSpace(name) == "" {
			return nil, nil, nil, ErrUnsupportedConfiguration
		}
		copyEndpoint, err := cloneJSON(*endpoint)
		if err != nil {
			return nil, nil, nil, ErrUnsupportedConfiguration
		}
		clearEndpointRuntimeFields(&copyEndpoint)
		endpoints[name] = &copyEndpoint
	}
	return &config, &host, endpoints, nil
}

// clearEndpointRuntimeFields retains configured aliases and IPAM requests but
// drops address/ID values assigned to the old container endpoint.
func clearEndpointRuntimeFields(endpoint *network.EndpointSettings) {
	if endpoint == nil {
		return
	}
	endpoint.EndpointID = ""
	endpoint.NetworkID = ""
	endpoint.Gateway = netip.Addr{}
	endpoint.IPAddress = netip.Addr{}
	endpoint.GlobalIPv6Address = netip.Addr{}
	endpoint.IPv6Gateway = netip.Addr{}
	endpoint.IPPrefixLen = 0
	endpoint.GlobalIPv6PrefixLen = 0
}

func (m *Manager) waitHealthyAndPorts(ctx context.Context, record *Record) error {
	verifyCtx, cancel := context.WithTimeout(ctx, m.opts.VerifyTimeout)
	defer cancel()
	ticker := time.NewTicker(m.opts.HealthPoll)
	defer ticker.Stop()
	for {
		current, err := m.engine.Inspect(verifyCtx, record.NewID)
		if err != nil || current.ID != record.NewID || !current.Running || current.Paused || current.Restarting {
			return ErrHealthCheckFailed
		}
		if current.Health == "unhealthy" {
			return ErrHealthCheckFailed
		}
		if current.Health == "healthy" || current.Health == "none" || current.Health == "" {
			original, oldErr := m.engine.Inspect(verifyCtx, record.OriginalID)
			if oldErr != nil || !portsConfigured(current, record.Spec, original) {
				return ErrPortVerificationFailed
			}
			return nil
		}
		select {
		case <-verifyCtx.Done():
			return ErrHealthCheckFailed
		case <-ticker.C:
		}
	}
}

func portsConfigured(current Container, spec protocol.RebuildSpec, original Container) bool {
	if current.HostConfig == nil {
		return false
	}
	want, err := buildPortMap(original.HostConfig.PortBindings, current.Config.ExposedPorts, spec)
	if err != nil {
		return false
	}
	actual := current.PublishedPorts
	if !current.Running {
		actual = current.HostConfig.PortBindings
	}
	actualCount := 0
	for _, bindings := range actual {
		actualCount += len(bindings)
	}
	expectedCount := 0
	for _, bindings := range want {
		expectedCount += len(bindings)
	}
	if actualCount != expectedCount {
		return false
	}
	for port, bindings := range want {
		actualBindings, ok := actual[port]
		if !ok || len(actualBindings) != len(bindings) {
			return false
		}
		for _, expected := range bindings {
			matched := false
			for _, found := range actualBindings {
				if expected.HostIP.IsValid() && expected.HostIP != found.HostIP {
					continue
				}
				if expected.HostPort != "" && expected.HostPort != found.HostPort {
					continue
				}
				if !current.Running && expected.HostPort == "" && found.HostPort == "" {
					matched = true
					break
				}
				if expected.HostPort == "" && found.HostPort == "" {
					continue
				}
				matched = true
				break
			}
			if !matched {
				return false
			}
		}
	}
	if spec.ClearPortBindings {
		return actualCount == 0
	}
	return true
}

func (m *Manager) reconnectNetworks(ctx context.Context, oldID string, current map[string]*network.EndpointSettings) error {
	for _, name := range sortedNetworkNames(current) {
		endpoint := current[name]
		latest, err := m.engine.Inspect(ctx, oldID)
		if err != nil {
			return err
		}
		if hasNetwork(latest.Networks, name) {
			continue
		}
		if err := m.engine.ConnectNetwork(ctx, name, oldID, endpoint); err != nil {
			return err
		}
	}
	return nil
}

func (m *Manager) verifyOriginalUnchanged(ctx context.Context, record Record) error {
	original, err := m.engine.Inspect(ctx, record.OriginalID)
	if err != nil || original.ID != record.OriginalID || strings.TrimPrefix(original.Name, "/") != record.OriginalName || original.Running != record.PreviousRunning {
		return ErrRecoveryRequired
	}
	return nil
}

func (m *Manager) finishSucceeded(ctx context.Context, taskID, state, revision string) (taskjournal.Snapshot, error) {
	return m.finish(ctx, taskID, taskstate.Succeeded, taskjournal.Result{Code: taskjournal.ResultVerified, ObservedState: safeState(state), ResourceRevision: safeState(revision)})
}

func (m *Manager) finishFailed(ctx context.Context, taskID, state, code string) (taskjournal.Snapshot, error) {
	return m.finish(ctx, taskID, taskstate.Failed, taskjournal.Result{Code: taskjournal.ResultFailed, ObservedState: safeState(state), ResourceRevision: boundedCode(code)})
}

// finishRestoredFailure records the restored container identity as the result
// revision so Core can offer explicit cleanup of a failed rebuild's temporary
// writable-layer snapshot. The bounded observed-state token retains both the
// restoration outcome and the safe failure class.
func (m *Manager) finishRestoredFailure(ctx context.Context, taskID, code, originalID string) (taskjournal.Snapshot, error) {
	observed := safeState("restored:" + boundedCode(code))
	return m.finish(ctx, taskID, taskstate.Failed, taskjournal.Result{
		Code: taskjournal.ResultFailed, ObservedState: observed, ResourceRevision: safeState(originalID),
	})
}

func (m *Manager) finish(ctx context.Context, taskID string, status taskstate.Status, result taskjournal.Result) (taskjournal.Snapshot, error) {
	evidence := taskstate.Evidence{ExecutionAttempted: true, ExecutionCompleted: true, ActualResultConfirmed: true}
	if status == taskstate.Succeeded {
		evidence.PostconditionVerified = true
	} else {
		evidence.FailureConfirmed = true
	}
	writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), m.opts.VerifyTimeout)
	err := m.journal.Finish(writeCtx, taskID, status, evidence, result)
	cancel()
	readCtx, readCancel := context.WithTimeout(context.WithoutCancel(ctx), m.opts.VerifyTimeout)
	task, readErr := m.journal.Get(readCtx, taskID)
	readCancel()
	if err != nil {
		return task, fmt.Errorf("persist rebuild task result: %w", err)
	}
	if readErr != nil {
		return taskjournal.Snapshot{}, readErr
	}
	return task, nil
}

func (m *Manager) markUnknown(ctx context.Context, taskID string, cause error) (taskjournal.Snapshot, error) {
	base := context.WithoutCancel(ctx)
	writeCtx, cancel := context.WithTimeout(base, m.opts.VerifyTimeout)
	markErr := m.journal.MarkUnknown(writeCtx, taskID)
	cancel()
	readCtx, readCancel := context.WithTimeout(base, m.opts.VerifyTimeout)
	task, readErr := m.journal.Get(readCtx, taskID)
	readCancel()
	if cause != nil {
		cause = errors.Join(ErrRecoveryRequired, cause)
	}
	if markErr != nil {
		return task, errors.Join(cause, markErr)
	}
	if readErr != nil {
		return taskjournal.Snapshot{}, errors.Join(cause, readErr)
	}
	return task, cause
}

func (m *Manager) taskAfterError(ctx context.Context, taskID string, cause error) (taskjournal.Snapshot, error) {
	readCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), m.opts.VerifyTimeout)
	defer cancel()
	task, err := m.journal.Get(readCtx, taskID)
	if err != nil {
		return taskjournal.Snapshot{}, cause
	}
	if task.Status == taskstate.Succeeded || task.Status == taskstate.Failed || task.Status == taskstate.TimedOut || task.Status == taskstate.Canceled {
		return task, nil
	}
	return task, cause
}

func terminal(task taskjournal.Snapshot) (bool, error) {
	switch task.Status {
	case taskstate.Succeeded, taskstate.Failed, taskstate.TimedOut, taskstate.Canceled:
		return true, nil
	case taskstate.Unknown:
		return true, ErrRecoveryRequired
	case taskstate.Running:
		return true, ErrRecoveryRequired
	default:
		return false, nil
	}
}

func generatedNames(taskID string) (string, string) {
	digest := sha256.Sum256([]byte(taskID))
	short := hex.EncodeToString(digest[:10])
	return "nodedance-rb-" + short, "nodedance-rebuild:" + short
}

func sortedNetworkNames(networks map[string]*network.EndpointSettings) []string {
	names := make([]string, 0, len(networks))
	for name := range networks {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func networkIdentifier(name string, endpoint *network.EndpointSettings) string {
	if endpoint != nil && endpoint.NetworkID != "" {
		return endpoint.NetworkID
	}
	return name
}

func hasNetwork(networks map[string]*network.EndpointSettings, name string) bool {
	_, ok := networks[name]
	return ok
}

func cloneJSON[T any](value T) (T, error) {
	var result T
	data, err := json.Marshal(value)
	if err != nil {
		return result, err
	}
	err = json.Unmarshal(data, &result)
	return result, err
}

func cloneStrings(values map[string]string) map[string]string {
	result := make(map[string]string, len(values)+2)
	for key, value := range values {
		result[key] = value
	}
	return result
}

func stateToken(current Container) string {
	if current.Running {
		return "running"
	}
	return "stopped"
}

func safeState(value string) string {
	value = strings.ToLower(value)
	for _, r := range value {
		if !((r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' || r == '-' || r == ':' || r == '.') {
			return "unknown"
		}
	}
	if value == "" || len(value) > 64 {
		return "unknown"
	}
	return value
}

func sanitizedCode(err error) string {
	switch {
	case errors.Is(err, ErrInsufficientSpace):
		return "snapshot_space_low"
	case errors.Is(err, ErrSnapshotFailed):
		return "snapshot_failed"
	case errors.Is(err, ErrUnsupportedConfiguration):
		return "unsupported_configuration"
	case errors.Is(err, ErrHealthCheckFailed):
		return "health_check_failed"
	case errors.Is(err, ErrPortVerificationFailed):
		return "port_verification_failed"
	case errors.Is(err, ErrRecordConflict):
		return "resource_conflict"
	default:
		return "rebuild_operation_failed"
	}
}

func boundedCode(value string) string {
	value = strings.ToLower(value)
	if value == "" || len(value) > 64 {
		return "rebuild_operation_failed"
	}
	for _, r := range value {
		if !((r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' || r == '-') {
			return "rebuild_operation_failed"
		}
	}
	return value
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return "rebuild_operation_failed"
}

func isNotFound(err error) bool {
	return err != nil && errdefs.IsNotFound(err)
}

func expectedBinding(spec protocol.RebuildSpec, current Container) (network.PortMap, error) {
	if current.Config == nil || current.HostConfig == nil {
		return nil, ErrUnsupportedConfiguration
	}
	return buildPortMap(current.HostConfig.PortBindings, current.Config.ExposedPorts, spec)
}

func equalNetworkSettings(a, b map[string]*network.EndpointSettings) bool {
	if len(a) != len(b) {
		return false
	}
	for key, value := range a {
		other, ok := b[key]
		if !ok || value == nil || other == nil || strings.Join(value.Aliases, "\x00") != strings.Join(other.Aliases, "\x00") {
			return false
		}
	}
	return true
}
