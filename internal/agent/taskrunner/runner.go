// Package taskrunner owns the Agent task worker lifetime independently of any
// one WebSocket connection. It persists receipt before acknowledging a task,
// delegates actual work to the verified container-actions executor, and never
// replays interrupted or uncertain tasks after process restart.
package taskrunner

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/CST-Cat/NodeDance/internal/agent/containeractions"
	"github.com/CST-Cat/NodeDance/internal/agent/taskjournal"
	"github.com/CST-Cat/NodeDance/internal/protocol"
	"github.com/CST-Cat/NodeDance/internal/taskstate"
)

const (
	defaultWorkers           = 2
	defaultQueueCapacity     = 8
	defaultReportQueue       = 256
	defaultJournalWriteLimit = 5 * time.Second
	maxSnapshotAcks          = 4
)

var (
	ErrNotStarted               = errors.New("Agent task runner has not started")
	ErrAlreadyStarted           = errors.New("Agent task runner already started")
	ErrStopped                  = errors.New("Agent task runner is stopped")
	ErrStaleGeneration          = errors.New("Agent task message belongs to a stale connection generation")
	ErrNotSynchronized          = errors.New("Agent task journal has not completed Core synchronization")
	ErrJournalMismatch          = errors.New("Agent task message has a different journal identity")
	ErrCapabilityRequired       = errors.New("task bridge capability was not negotiated")
	ErrCapacityExceeded         = errors.New("Agent task runner has no free durable task capacity")
	ErrIdentityConflict         = errors.New("Agent task identity conflicts with its durable journal entry")
	ErrTaskNotReconcileable     = errors.New("Agent task is not eligible for read-only reconciliation")
	ErrInvalidSnapshot          = errors.New("invalid Agent task snapshot request")
	ErrStaleReportAck           = errors.New("Agent task report acknowledgement is stale")
	ErrImageExecutorUnavailable = errors.New("Agent image task executor is unavailable")
)

type Journal interface {
	JournalID() string
	EnqueueDelivered(context.Context, taskstate.Identity) (taskjournal.EnqueueResult, error)
	Get(context.Context, string) (taskjournal.Snapshot, error)
	SnapshotAll(context.Context) ([]taskjournal.Snapshot, error)
	RecoverInterrupted(context.Context) (int64, error)
}

type Executor interface {
	ExecuteObserved(context.Context, containeractions.Request, func(taskjournal.Snapshot)) (taskjournal.Snapshot, error)
	Reconcile(context.Context, string) (taskjournal.Snapshot, error)
}

type ImageExecutor interface {
	ExecuteImage(context.Context, protocol.TaskDispatch, func()) (taskjournal.Snapshot, error)
	ReconcileImage(context.Context, string, protocol.TaskIntent) (taskjournal.Snapshot, error)
}

type ComposeExecutor interface {
	ExecuteCompose(context.Context, protocol.TaskDispatch, func(taskjournal.Snapshot)) (taskjournal.Snapshot, error)
	ReconcileCompose(context.Context, string, protocol.TaskIntent) (taskjournal.Snapshot, error)
}

type Options struct {
	Workers             int
	QueueCapacity       int
	ReportQueueCapacity int
	JournalWriteTimeout time.Duration
}

type ReportBatch struct {
	Reports          []protocol.TaskReport
	SnapshotRequired bool
}

type Runner struct {
	nodeID   string
	journal  Journal
	executor Executor
	images   ImageExecutor
	compose  ComposeExecutor
	options  Options

	mu                sync.Mutex
	started           bool
	starting          bool
	stopped           bool
	processCtx        context.Context
	processCancel     context.CancelFunc
	activeGeneration  uint64
	lastGeneration    uint64
	activeJournalID   string
	synchronized      bool
	syncAckGeneration uint64
	syncAckRevision   uint64
	activeTasks       map[string]struct{}
	activeImagePulls  map[string]context.CancelCauseFunc
	work              chan taskJob
	reportHints       chan string
	reportWake        chan struct{}
	queuedReports     map[string]struct{}
	pendingReports    map[string]uint64
	snapshotDirty     bool
	reportRevision    uint64
	snapshotRevisions map[string]snapshotRevision
	wg                sync.WaitGroup
	joinDone          chan struct{}
	joinOnce          sync.Once
}

type taskJob struct {
	taskID    string
	dispatch  *protocol.TaskDispatch
	reconcile bool
	intent    *protocol.TaskIntent
}

func (r *Runner) SetImageExecutor(executor ImageExecutor) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.started || r.starting || r.stopped {
		return ErrAlreadyStarted
	}
	r.images = executor
	return nil
}

func (r *Runner) SetComposeExecutor(executor ComposeExecutor) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.started || r.starting || r.stopped {
		return ErrAlreadyStarted
	}
	r.compose = executor
	return nil
}

type snapshotRevision struct {
	generation uint64
	revision   uint64
}

func New(nodeID string, journal Journal, executor Executor, options Options) (*Runner, error) {
	if nodeID == "" || journal == nil || executor == nil {
		return nil, errors.New("Agent task runner requires a node ID, task journal, and executor")
	}
	if options.Workers == 0 {
		options.Workers = defaultWorkers
	}
	if options.QueueCapacity == 0 {
		options.QueueCapacity = defaultQueueCapacity
	}
	if options.ReportQueueCapacity == 0 {
		options.ReportQueueCapacity = defaultReportQueue
	}
	if options.JournalWriteTimeout == 0 {
		options.JournalWriteTimeout = defaultJournalWriteLimit
	}
	if options.Workers < 1 || options.Workers > 32 || options.QueueCapacity < 0 || options.QueueCapacity > 1024 ||
		options.ReportQueueCapacity < 1 || options.ReportQueueCapacity > 4096 || options.JournalWriteTimeout < time.Second || options.JournalWriteTimeout > 30*time.Second {
		return nil, errors.New("Agent task runner limits are outside supported bounds")
	}
	return &Runner{
		nodeID: nodeID, journal: journal, executor: executor, options: options,
		activeTasks:      make(map[string]struct{}),
		activeImagePulls: make(map[string]context.CancelCauseFunc),
		work:             make(chan taskJob, options.Workers+options.QueueCapacity),
		reportHints:      make(chan string, options.ReportQueueCapacity),
		reportWake:       make(chan struct{}, 1),
		queuedReports:    make(map[string]struct{}), pendingReports: make(map[string]uint64),
		snapshotDirty: true, snapshotRevisions: make(map[string]snapshotRevision),
		reportRevision: 1, joinDone: make(chan struct{}),
	}, nil
}

// Start initializes restart recovery once, before any connection can admit a
// dispatch. The supplied context is the Agent process context, never a session
// context. A delivered queued record and any interrupted running record become
// unknown before workers are enabled.
func (r *Runner) Start(ctx context.Context) error {
	if ctx == nil {
		return errors.New("Agent task runner process context is nil")
	}
	r.mu.Lock()
	if r.started || r.starting {
		r.mu.Unlock()
		return ErrAlreadyStarted
	}
	if r.stopped {
		r.mu.Unlock()
		return ErrStopped
	}
	r.starting = true
	r.mu.Unlock()

	recoveryCtx, cancel := context.WithTimeout(ctx, r.options.JournalWriteTimeout)
	_, err := r.journal.RecoverInterrupted(recoveryCtx)
	cancel()

	r.mu.Lock()
	defer r.mu.Unlock()
	r.starting = false
	if err != nil {
		return fmt.Errorf("recover durable Agent tasks before dispatch: %w", err)
	}
	if r.stopped {
		return ErrStopped
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	r.processCtx, r.processCancel = context.WithCancel(ctx)
	r.started = true
	for worker := 0; worker < r.options.Workers; worker++ {
		r.wg.Add(1)
		go r.worker()
	}
	return nil
}

// Connect attaches one negotiated bridge-capable connection. A new connection
// remains unable to accept dispatches until the complete journal snapshot was
// reconciled and MarkSynchronized is called. Replacing a connection never
// cancels process-scoped work.
func (r *Runner) Connect(generation uint64, journalID string, capabilities []string) error {
	if generation == 0 || !validJournalID(journalID) {
		return ErrStaleGeneration
	}
	if !hasCapability(capabilities, protocol.CapabilityTaskBridge) {
		return ErrCapabilityRequired
	}
	if journalID != r.journal.JournalID() {
		return ErrJournalMismatch
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.started {
		return ErrNotStarted
	}
	if r.stopped {
		return ErrStopped
	}
	if generation <= r.lastGeneration {
		return ErrStaleGeneration
	}
	r.lastGeneration = generation
	r.activeGeneration = generation
	r.activeJournalID = journalID
	r.synchronized = false
	r.syncAckGeneration = 0
	r.syncAckRevision = 0
	r.snapshotDirty = true
	return nil
}

func (r *Runner) MarkSynchronized(generation uint64, journalID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.checkConnectionLocked(generation, journalID); err != nil {
		return err
	}
	if r.syncAckGeneration != generation || r.syncAckRevision != r.reportRevision {
		return ErrNotSynchronized
	}
	r.synchronized = true
	return nil
}

// Disconnect detaches only the sender/receiver for the matching generation.
// Accepted jobs and their process contexts remain intact.
func (r *Runner) Disconnect(generation uint64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if generation != 0 && r.activeGeneration == generation {
		r.activeGeneration = 0
		r.activeJournalID = ""
		r.synchronized = false
		r.syncAckGeneration = 0
		r.syncAckRevision = 0
	}
}

func (r *Runner) AvailableCapacity() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	capacity := r.options.Workers + r.options.QueueCapacity - len(r.activeTasks)
	if capacity < 0 {
		return 0
	}
	return capacity
}

func (r *Runner) CapacityLimit() int {
	return r.options.Workers + r.options.QueueCapacity
}

// CancelImagePull interrupts only an active image pull. The executor remains
// responsible for verifying Engine state before it reports a canceled result.
func (r *Runner) CancelImagePull(taskID string) bool {
	r.mu.Lock()
	cancel := r.activeImagePulls[taskID]
	r.mu.Unlock()
	if cancel == nil {
		return false
	}
	cancel(taskstate.ErrCancellationRequested)
	return true
}

// ReportsChanged is a coalesced, nonblocking wake signal for the currently
// attached transport. The SQLite journal remains the source of truth; callers
// must DrainReports after a wake and use a complete snapshot when requested.
// The signal is process-lifetime state and is intentionally not tied to one
// WebSocket generation.
func (r *Runner) ReportsChanged() <-chan struct{} { return r.reportWake }

// AcceptDispatch durably writes task identity and receipt before it returns an
// acknowledgement. Queue admission is reserved first, so full capacity never
// leaves a newly accepted-but-unserviceable row. Ambiguous SQLite errors never
// call the executor; a repeated identical dispatch can safely inspect and then
// schedule a still-queued row within this process.
func (r *Runner) AcceptDispatch(ctx context.Context, generation uint64, envelope protocol.Envelope, dispatch protocol.TaskDispatch) (protocol.TaskReport, error) {
	if ctx == nil {
		return protocol.TaskReport{}, protocol.ErrInvalidTaskMessage
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.started {
		return protocol.TaskReport{}, ErrNotStarted
	}
	if r.stopped {
		return protocol.TaskReport{}, ErrStopped
	}
	if generation != r.activeGeneration {
		return protocol.TaskReport{}, ErrStaleGeneration
	}
	if !r.synchronized {
		return protocol.TaskReport{}, ErrNotSynchronized
	}
	if err := protocol.ValidateTaskDispatch(envelope, dispatch, r.nodeID, r.activeJournalID, generation); err != nil {
		return protocol.TaskReport{}, err
	}
	if isImageAction(dispatch.Intent.Action) && r.images == nil {
		return protocol.TaskReport{}, ErrImageExecutorUnavailable
	}
	if protocol.IsComposeTaskAction(dispatch.Intent.Action) && r.compose == nil {
		return protocol.TaskReport{}, errors.New("Agent Compose task executor is unavailable")
	}
	_, alreadyActive := r.activeTasks[dispatch.TaskID]
	if !alreadyActive && len(r.activeTasks) >= r.options.Workers+r.options.QueueCapacity {
		return protocol.TaskReport{}, ErrCapacityExceeded
	}
	identity, err := protocol.TaskIdentity(dispatch.TaskID, dispatch.NodeID, dispatch.IdempotencyKey, dispatch.Intent)
	if err != nil {
		return protocol.TaskReport{}, err
	}
	writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), r.options.JournalWriteTimeout)
	accepted, err := r.journal.EnqueueDelivered(writeCtx, identity)
	cancel()
	if err != nil {
		return protocol.TaskReport{}, fmt.Errorf("persist delivered Agent task before ACK: %w", err)
	}
	if !matchesDispatch(accepted.Task, dispatch) || !accepted.Task.DeliveryCommitted {
		return protocol.TaskReport{}, ErrIdentityConflict
	}
	report, err := reportFromSnapshot(accepted.Task, r.nodeID, r.activeJournalID)
	if err != nil {
		return protocol.TaskReport{}, err
	}
	r.reportRevision++
	report.ReportRevision = r.reportRevision
	if accepted.Task.Status == taskstate.Queued && !alreadyActive {
		r.activeTasks[dispatch.TaskID] = struct{}{}
		copyOfDispatch := dispatch
		if dispatch.RegistryAuth != nil {
			credentials := *dispatch.RegistryAuth
			copyOfDispatch.RegistryAuth = &credentials
		}
		select {
		case r.work <- taskJob{taskID: dispatch.TaskID, dispatch: &copyOfDispatch}:
		default:
			delete(r.activeTasks, dispatch.TaskID)
			if copyOfDispatch.RegistryAuth != nil {
				copyOfDispatch.RegistryAuth.Username, copyOfDispatch.RegistryAuth.Password = "", ""
			}
			return protocol.TaskReport{}, ErrCapacityExceeded
		}
	}
	if len(r.pendingReports) < protocol.MaxTaskSnapshotTasks {
		r.pendingReports[dispatch.TaskID] = r.reportRevision
	} else {
		r.snapshotDirty = true
	}
	return report, nil
}

// AcceptReconcile runs only the executor's read-only baseline reconciliation
// for an existing unknown task. It cannot enqueue or mutate Docker state.
func (r *Runner) AcceptReconcile(ctx context.Context, generation uint64, envelope protocol.Envelope, request protocol.TaskReconcileRequest) (protocol.TaskReport, error) {
	if ctx == nil {
		return protocol.TaskReport{}, protocol.ErrInvalidTaskMessage
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.checkSynchronizedLocked(generation); err != nil {
		return protocol.TaskReport{}, err
	}
	if err := protocol.ValidateTaskReconcile(envelope, request, r.nodeID, r.activeJournalID, generation); err != nil {
		return protocol.TaskReport{}, err
	}
	if _, busy := r.activeTasks[request.TaskID]; !busy && len(r.activeTasks) >= r.options.Workers+r.options.QueueCapacity {
		return protocol.TaskReport{}, ErrCapacityExceeded
	}
	readCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), r.options.JournalWriteTimeout)
	entry, err := r.journal.Get(readCtx, request.TaskID)
	cancel()
	if err != nil {
		return protocol.TaskReport{}, err
	}
	if !matchesReconcile(entry, request) || entry.Status != taskstate.Unknown {
		return protocol.TaskReport{}, ErrTaskNotReconcileable
	}
	if isImageAction(request.Intent.Action) && r.images == nil {
		return protocol.TaskReport{}, ErrImageExecutorUnavailable
	}
	if protocol.IsComposeTaskAction(request.Intent.Action) && r.compose == nil {
		return protocol.TaskReport{}, errors.New("Agent Compose task executor is unavailable")
	}
	if _, busy := r.activeTasks[request.TaskID]; !busy {
		r.activeTasks[request.TaskID] = struct{}{}
		intent := request.Intent
		select {
		case r.work <- taskJob{taskID: request.TaskID, reconcile: true, intent: &intent}:
		default:
			delete(r.activeTasks, request.TaskID)
			return protocol.TaskReport{}, ErrCapacityExceeded
		}
	}
	report, err := reportFromSnapshot(entry, r.nodeID, r.activeJournalID)
	if err != nil {
		return protocol.TaskReport{}, err
	}
	r.reportRevision++
	report.ReportRevision = r.reportRevision
	if len(r.pendingReports) < protocol.MaxTaskSnapshotTasks {
		r.pendingReports[request.TaskID] = r.reportRevision
	} else {
		r.snapshotDirty = true
	}
	return report, nil
}

// DrainReports returns coalesced report hints reloaded from SQLite. A dirty
// flag asks the session to request/build a complete snapshot instead of trying
// to encode a possibly incomplete queue. max is bounded to one wire page.
func (r *Runner) DrainReports(generation uint64, max int) (ReportBatch, error) {
	if max < 1 || max > protocol.TaskSnapshotPageSize {
		return ReportBatch{}, ErrInvalidSnapshot
	}
	r.mu.Lock()
	if err := r.checkSynchronizedLocked(generation); err != nil {
		r.mu.Unlock()
		return ReportBatch{}, err
	}
	if r.snapshotDirty {
		r.mu.Unlock()
		return ReportBatch{SnapshotRequired: true}, nil
	}
	ids := make([]string, 0, max)
	revisions := make([]uint64, 0, max)
	for len(ids) < max {
		select {
		case taskID := <-r.reportHints:
			delete(r.queuedReports, taskID)
			revision, ok := r.pendingReports[taskID]
			if !ok || revision == 0 {
				r.snapshotDirty = true
				r.mu.Unlock()
				return ReportBatch{SnapshotRequired: true}, nil
			}
			ids = append(ids, taskID)
			revisions = append(revisions, revision)
		default:
			r.mu.Unlock()
			return r.loadReportBatch(generation, ids, revisions)
		}
	}
	r.mu.Unlock()
	return r.loadReportBatch(generation, ids, revisions)
}

func (r *Runner) loadReportBatch(generation uint64, ids []string, revisions []uint64) (ReportBatch, error) {
	if len(ids) != len(revisions) {
		return ReportBatch{}, ErrInvalidSnapshot
	}
	batch := ReportBatch{Reports: make([]protocol.TaskReport, 0, len(ids))}
	for index, taskID := range ids {
		ctx, cancel := context.WithTimeout(context.Background(), r.options.JournalWriteTimeout)
		entry, err := r.journal.Get(ctx, taskID)
		cancel()
		if err != nil {
			r.RequestSnapshot(generation)
			return ReportBatch{}, fmt.Errorf("read durable Agent task report: %w", err)
		}
		r.mu.Lock()
		journalID := r.activeJournalID
		if generation != r.activeGeneration || !r.synchronized {
			r.mu.Unlock()
			return ReportBatch{}, ErrStaleGeneration
		}
		report, err := reportFromSnapshot(entry, r.nodeID, journalID)
		if err != nil {
			r.mu.Unlock()
			return ReportBatch{}, err
		}
		report.ReportRevision = revisions[index]
		batch.Reports = append(batch.Reports, report)
		r.mu.Unlock()
	}
	return batch, nil
}

func (r *Runner) AcknowledgeReport(generation uint64, taskID string, reportRevision uint64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if generation == 0 || generation != r.activeGeneration {
		return ErrStaleGeneration
	}
	if reportRevision == 0 {
		return ErrStaleReportAck
	}
	current, ok := r.pendingReports[taskID]
	if !ok {
		// Duplicate acknowledgements are harmless after a report has already
		// been cleared, including by a complete snapshot acknowledgement.
		return nil
	}
	if current != reportRevision {
		return ErrStaleReportAck
	}
	delete(r.pendingReports, taskID)
	return nil
}

// RequestSnapshot is intended for reconnect synchronization, report queue
// overflow, and the session's periodic lost-ACK repair timer.
func (r *Runner) RequestSnapshot(generation uint64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if generation != 0 && generation == r.activeGeneration {
		r.snapshotDirty = true
		if !r.synchronized {
			r.syncAckGeneration = 0
			r.syncAckRevision = 0
		}
	}
}

// SnapshotPages obtains one frozen bounded SQLite candidate, filters it to
// Core-delivered records, and only then splits it into contiguous wire pages.
// It never exposes a partial snapshot or pages over a live changing table.
func (r *Runner) SnapshotPages(generation uint64, snapshotID string) ([]protocol.TaskSnapshotPage, error) {
	if !validSnapshotID(snapshotID) {
		return nil, ErrInvalidSnapshot
	}
	r.mu.Lock()
	if err := r.checkConnectionLocked(generation, r.activeJournalID); err != nil {
		r.mu.Unlock()
		return nil, err
	}
	journalID := r.activeJournalID
	revision := r.reportRevision
	r.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), taskjournal.JournalSnapshotTimeout)
	snapshots, err := r.journal.SnapshotAll(ctx)
	cancel()
	if err != nil {
		return nil, err
	}
	reports := make([]protocol.TaskReport, 0, len(snapshots))
	var totalBytes int
	for _, entry := range snapshots {
		if !entry.DeliveryCommitted {
			continue
		}
		report, err := reportFromSnapshot(entry, r.nodeID, journalID)
		if err != nil {
			return nil, err
		}
		report.ReportRevision = revision
		encoded, err := json.Marshal(report)
		if err != nil {
			return nil, err
		}
		totalBytes += len(encoded)
		if len(reports) >= protocol.MaxTaskSnapshotTasks || totalBytes > protocol.MaxTaskSnapshotBytes {
			return nil, ErrInvalidSnapshot
		}
		reports = append(reports, report)
	}
	pages := make([]protocol.TaskSnapshotPage, 0, (len(reports)+protocol.TaskSnapshotPageSize-1)/protocol.TaskSnapshotPageSize)
	if len(reports) == 0 {
		pages = append(pages, protocol.TaskSnapshotPage{SnapshotID: snapshotID, JournalID: journalID, Final: true, Reports: []protocol.TaskReport{}})
	} else {
		for start, pageNumber := 0, uint32(0); start < len(reports); start, pageNumber = start+protocol.TaskSnapshotPageSize, pageNumber+1 {
			end := start + protocol.TaskSnapshotPageSize
			if end > len(reports) {
				end = len(reports)
			}
			page := protocol.TaskSnapshotPage{SnapshotID: snapshotID, JournalID: journalID, Page: pageNumber,
				Final: end == len(reports), Reports: append([]protocol.TaskReport(nil), reports[start:end]...)}
			encoded, err := json.Marshal(page)
			if err != nil || len(encoded) > protocol.MaxTaskPayloadBytes {
				return nil, ErrInvalidSnapshot
			}
			pages = append(pages, page)
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if generation != r.activeGeneration || journalID != r.activeJournalID {
		return nil, ErrStaleGeneration
	}
	if len(r.snapshotRevisions) >= maxSnapshotAcks {
		for key := range r.snapshotRevisions {
			delete(r.snapshotRevisions, key)
			break
		}
	}
	r.snapshotRevisions[snapshotID] = snapshotRevision{generation: generation, revision: revision}
	return pages, nil
}

// AcknowledgeSnapshot clears the resync barrier only if no durable report
// changed since the snapshot candidate began. Otherwise the caller must take a
// new complete snapshot; an interleaved update cannot disappear in a page gap.
func (r *Runner) AcknowledgeSnapshot(generation uint64, snapshotID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if generation != r.activeGeneration {
		return ErrStaleGeneration
	}
	snapshot, ok := r.snapshotRevisions[snapshotID]
	if !ok || snapshot.generation != generation {
		return ErrInvalidSnapshot
	}
	delete(r.snapshotRevisions, snapshotID)
	if snapshot.revision == r.reportRevision {
		r.snapshotDirty = false
		r.syncAckGeneration = generation
		r.syncAckRevision = snapshot.revision
		r.pendingReports = make(map[string]uint64)
		for len(r.reportHints) > 0 {
			taskID := <-r.reportHints
			delete(r.queuedReports, taskID)
		}
	} else {
		r.snapshotDirty = true
		r.syncAckGeneration = 0
		r.syncAckRevision = 0
	}
	return nil
}

// Stop cancels process-scoped worker contexts and joins within ctx. A timeout
// does not claim that an Engine call stopped; unfinished journal states remain
// for RecoverInterrupted to turn into unknown on the next process start.
func (r *Runner) Stop(ctx context.Context) error {
	if ctx == nil {
		return errors.New("Agent task runner stop context is nil")
	}
	r.mu.Lock()
	if !r.started {
		r.stopped = true
		r.mu.Unlock()
		r.joinOnce.Do(func() { close(r.joinDone) })
		return nil
	}
	if !r.stopped {
		r.stopped = true
		r.activeGeneration = 0
		r.activeJournalID = ""
		r.synchronized = false
		r.processCancel()
	}
	r.mu.Unlock()
	r.joinOnce.Do(func() {
		go func() {
			r.wg.Wait()
			close(r.joinDone)
		}()
	})
	select {
	case <-r.joinDone:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (r *Runner) worker() {
	defer r.wg.Done()
	for {
		select {
		case <-r.processCtx.Done():
			return
		case job := <-r.work:
			r.runJob(job)
		}
	}
}

func (r *Runner) runJob(job taskJob) {
	ctx := r.processCtx
	if job.reconcile {
		if job.intent != nil && isImageAction(job.intent.Action) && r.images != nil {
			_, _ = r.images.ReconcileImage(ctx, job.taskID, *job.intent)
		} else if job.intent != nil && protocol.IsComposeTaskAction(job.intent.Action) && r.compose != nil {
			_, _ = r.compose.ReconcileCompose(ctx, job.taskID, *job.intent)
		} else {
			_, _ = r.executor.Reconcile(ctx, job.taskID)
		}
	} else if job.dispatch != nil {
		if isImageAction(job.dispatch.Intent.Action) {
			if r.images != nil {
				imageCtx, cancel := context.WithCancelCause(ctx)
				if job.dispatch.Intent.Action == protocol.TaskImagePull {
					r.mu.Lock()
					r.activeImagePulls[job.taskID] = cancel
					r.mu.Unlock()
				}
				_, _ = r.images.ExecuteImage(imageCtx, *job.dispatch, func() { r.notify(job.taskID) })
				cancel(context.Canceled)
				r.mu.Lock()
				delete(r.activeImagePulls, job.taskID)
				r.mu.Unlock()
			}
			r.notify(job.taskID)
			r.mu.Lock()
			delete(r.activeTasks, job.taskID)
			r.mu.Unlock()
			return
		}
		if protocol.IsComposeTaskAction(job.dispatch.Intent.Action) {
			if r.compose != nil {
				_, _ = r.compose.ExecuteCompose(ctx, *job.dispatch, func(taskjournal.Snapshot) {
					r.notify(job.taskID)
				})
			}
			r.notify(job.taskID)
			r.mu.Lock()
			delete(r.activeTasks, job.taskID)
			r.mu.Unlock()
			return
		}
		request := containeractions.Request{
			TaskID: job.dispatch.TaskID, NodeID: job.dispatch.NodeID, IdempotencyKey: job.dispatch.IdempotencyKey,
			Action: job.dispatch.Intent.Action, ContainerID: job.dispatch.TargetID,
			NewName: job.dispatch.Intent.NewName, DeleteConfirmed: job.dispatch.Intent.DeleteConfirmed,
			Rebuild: job.dispatch.Intent.Rebuild,
		}
		if request.Action == protocol.TaskRebuildCleanup {
			request.ConfirmationID = request.ContainerID
		}
		if request.Action == protocol.TaskDelete {
			request.DeleteConfirmationID = request.ContainerID
		}
		_, _ = r.executor.ExecuteObserved(ctx, request, func(taskjournal.Snapshot) {
			// ExecuteObserved invokes this only after its durable running commit
			// has been confirmed by a bounded journal read.
			r.notify(job.taskID)
		})
	}
	r.notify(job.taskID)
	r.mu.Lock()
	delete(r.activeTasks, job.taskID)
	r.mu.Unlock()
}

func isImageAction(action protocol.TaskAction) bool {
	return action == protocol.TaskImagePull || action == protocol.TaskImageDelete
}

func (r *Runner) notify(taskID string) {
	r.mu.Lock()
	r.reportRevision++
	if len(r.pendingReports) < protocol.MaxTaskSnapshotTasks {
		r.pendingReports[taskID] = r.reportRevision
	} else {
		r.snapshotDirty = true
	}
	if _, exists := r.queuedReports[taskID]; exists {
		r.mu.Unlock()
		return
	}
	r.queuedReports[taskID] = struct{}{}
	select {
	case r.reportHints <- taskID:
	default:
		r.snapshotDirty = true
		delete(r.queuedReports, taskID)
	}
	select {
	case r.reportWake <- struct{}{}:
	default:
	}
	r.mu.Unlock()
}

func (r *Runner) checkConnectionLocked(generation uint64, journalID string) error {
	if !r.started {
		return ErrNotStarted
	}
	if r.stopped {
		return ErrStopped
	}
	if generation == 0 || generation != r.activeGeneration {
		return ErrStaleGeneration
	}
	if journalID == "" || journalID != r.activeJournalID {
		return ErrJournalMismatch
	}
	return nil
}

func (r *Runner) checkSynchronizedLocked(generation uint64) error {
	if err := r.checkConnectionLocked(generation, r.activeJournalID); err != nil {
		return err
	}
	if !r.synchronized {
		return ErrNotSynchronized
	}
	return nil
}

func hasCapability(capabilities []string, wanted string) bool {
	for _, capability := range capabilities {
		if capability == wanted {
			return true
		}
	}
	return false
}

func validJournalID(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size && hex.EncodeToString(decoded) == value
}

func validSnapshotID(value string) bool {
	if value == "" || len(value) > 64 {
		return false
	}
	for _, r := range value {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || strings.ContainsRune("._~-", r)) {
			return false
		}
	}
	return true
}

func matchesDispatch(snapshot taskjournal.Snapshot, dispatch protocol.TaskDispatch) bool {
	digest, err := protocol.ParseDigest(dispatch.RequestDigest)
	return err == nil && snapshot.TaskID == dispatch.TaskID && snapshot.NodeID == dispatch.NodeID &&
		snapshot.TargetID == dispatch.TargetID && snapshot.IdempotencyKey == dispatch.IdempotencyKey && snapshot.RequestDigest == digest
}

func matchesReconcile(snapshot taskjournal.Snapshot, request protocol.TaskReconcileRequest) bool {
	digest, err := protocol.ParseDigest(request.RequestDigest)
	computed, intentErr := protocol.TaskRequestDigest(request.TaskID, request.NodeID, request.IdempotencyKey, request.Intent)
	return err == nil && intentErr == nil && snapshot.TaskID == request.TaskID && snapshot.NodeID == request.NodeID &&
		snapshot.TargetID == request.TargetID && snapshot.IdempotencyKey == request.IdempotencyKey &&
		snapshot.RequestDigest == digest && subtle.ConstantTimeCompare(digest[:], computed[:]) == 1
}

func reportFromSnapshot(snapshot taskjournal.Snapshot, nodeID, journalID string) (protocol.TaskReport, error) {
	if snapshot.NodeID != nodeID || snapshot.TaskID == "" || snapshot.TargetID == "" || snapshot.IdempotencyKey == "" || snapshot.RequestDigest == ([sha256.Size]byte{}) {
		return protocol.TaskReport{}, ErrIdentityConflict
	}
	result := protocol.TaskResult{Code: string(snapshot.Result.Code), ObservedState: snapshot.Result.ObservedState, ResourceRevision: snapshot.Result.ResourceRevision}
	if snapshot.Status == taskstate.Unknown {
		result = protocol.TaskResult{Code: string(taskjournal.ResultUncertain)}
	}
	report := protocol.TaskReport{
		TaskID: snapshot.TaskID, ReportRevision: 1, NodeID: nodeID, JournalID: journalID, TargetID: snapshot.TargetID,
		IdempotencyKey: snapshot.IdempotencyKey, RequestDigest: protocol.DigestString(snapshot.RequestDigest), Status: snapshot.Status,
		Evidence: protocol.TaskEvidence{
			ExecutionAttempted: snapshot.Evidence.ExecutionAttempted, ExecutionCompleted: snapshot.Evidence.ExecutionCompleted,
			FailureConfirmed: snapshot.Evidence.FailureConfirmed, PostconditionVerified: snapshot.Evidence.PostconditionVerified,
			ProcessTerminated: snapshot.Evidence.ProcessTerminated, ActualResultConfirmed: snapshot.Evidence.ActualResultConfirmed,
			CancellationConfirmed: snapshot.Evidence.CancellationConfirmed,
		},
		Progress: protocol.TaskProgress{Phase: string(snapshot.Progress.Phase), Completed: snapshot.Progress.Completed, Total: snapshot.Progress.Total},
		Result:   result,
	}
	if err := protocol.ValidateTaskReport(protocol.Envelope{Version: protocol.CurrentVersion, Type: protocol.TypeTaskReport,
		Generation: 1, RequestID: report.TaskID}, report, nodeID, journalID, 1); err != nil {
		return protocol.TaskReport{}, err
	}
	return report, nil
}

func mustReport(snapshot taskjournal.Snapshot, nodeID, journalID string) protocol.TaskReport {
	report, _ := reportFromSnapshot(snapshot, nodeID, journalID)
	return report
}
