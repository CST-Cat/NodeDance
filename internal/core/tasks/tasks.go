// Package tasks persists Core task intent before any caller can deliver it.
// It has no HTTP, transport, or executor dependency; callers must apply their
// own authentication and must only report results after querying Docker.
package tasks

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/CST-Cat/NodeDance/internal/protocol"
	"github.com/CST-Cat/NodeDance/internal/taskstate"
)

const (
	DefaultLeaseTTL       = 30 * time.Second
	MaxPageSize           = 100
	MaxJournalTasks       = 10000
	MaxAuditEventsPerTask = 512
)

var (
	ErrInvalidRequest       = errors.New("invalid Core task request")
	ErrIdempotencyConflict  = errors.New("idempotency key conflicts with an existing request")
	ErrTaskIDConflict       = errors.New("task ID conflicts with an existing task")
	ErrResourceBusy         = errors.New("target resource has an unresolved task")
	ErrNodeOffline          = errors.New("target node is offline")
	ErrNodeNotFound         = errors.New("target node does not exist")
	ErrStaleAgent           = errors.New("Agent connection is stale")
	ErrJournalNotObserved   = errors.New("Agent journal identity has not been observed")
	ErrJournalChanged       = errors.New("Agent task journal identity changed; reconciliation is required")
	ErrReconciliationNeeded = errors.New("Agent journal reconciliation is required")
	ErrTaskNotFound         = errors.New("task not found")
	ErrTaskStateConflict    = errors.New("task state conflicts with the durable record")
	ErrManagedRename        = errors.New("Compose-managed containers cannot be renamed")
	ErrManagedRebuild       = errors.New("Compose-managed containers cannot be rebuilt by the independent container workflow")
	ErrNotDelivered         = errors.New("task has not been durably marked for delivery")
)

// SchemaSQL is the current Core task schema fragment. The Core storage
// initializer applies it as part of the current v0 database schema.
//
//go:embed schema.sql
var schemaSQL string

func SchemaSQL() string { return schemaSQL }

// SchemaStatements returns the Core task statements for current-schema
// initialization. The embedded SQL is split only on statement-ending
// semicolons.
func SchemaStatements() []string {
	parts := strings.Split(schemaSQL, ";\n\n")
	statements := make([]string, 0, len(parts))
	for _, part := range parts {
		statement := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(part), ";"))
		if statement != "" {
			statements = append(statements, statement)
		}
	}
	return statements
}

type Action = protocol.TaskAction

const (
	ActionStart          = protocol.TaskStart
	ActionStop           = protocol.TaskStop
	ActionRestart        = protocol.TaskRestart
	ActionPause          = protocol.TaskPause
	ActionResume         = protocol.TaskResume
	ActionDelete         = protocol.TaskDelete
	ActionRename         = protocol.TaskRename
	ActionImagePull      = protocol.TaskImagePull
	ActionImageDelete    = protocol.TaskImageDelete
	ActionComposeStart   = protocol.TaskComposeStart
	ActionComposeStop    = protocol.TaskComposeStop
	ActionComposeRestart = protocol.TaskComposeRestart
	ActionComposeDeploy  = protocol.TaskComposeDeploy
	ActionComposeSave    = protocol.TaskComposeSave
)

// Intent is the complete allowlisted, non-secret remote container command. No
// arbitrary JSON or freeform error field is persisted. Credentials for future
// registry or other secret-bearing actions need a separate encrypted,
// one-use transport contract before those actions are added here.
type Intent = protocol.TaskIntent

type EnqueueRequest struct {
	TaskID         string
	NodeID         string
	IdempotencyKey string
	Intent         Intent
	// RegistryAuthRequired is part of idempotency semantics for image pulls.
	// The credential values themselves are one-use transport data and are never
	// included in task identity or persistence.
	RegistryAuthRequired bool
	// ComposeManaged must come from trusted Core asset state, never from an
	// HTTP request boolean. The eventual authenticated route must overwrite any
	// client value with the current Engine-derived classification.
	ComposeManaged bool
	ActorID        sql.NullInt64
	RemoteAddr     string
}

// EnqueueGate is evaluated only after Store has checked both the idempotency
// key and task ID inside the write transaction. It supplies trusted,
// connection-scoped acceptance facts for a genuinely new row. release is held
// until the task, audit, and resource claim transaction commits or rolls back.
// A retry or conflict never invokes the gate.
type EnqueueGate func(context.Context) (composeManaged bool, release func(), err error)

type Options struct {
	LeaseTTL time.Duration
	Now      func() time.Time
}

type Store struct {
	db       *sql.DB
	leaseTTL time.Duration
	now      func() time.Time
	writeMu  sync.Mutex
}

type EnqueueResult struct {
	Task    Task
	Created bool
}

type ProgressPhase string

const (
	PhaseAccepted    ProgressPhase = "accepted"
	PhasePreparing   ProgressPhase = "preparing"
	PhaseExecuting   ProgressPhase = "executing"
	PhaseVerifying   ProgressPhase = "verifying"
	PhaseReconciling ProgressPhase = "reconciling"
)

type Progress struct {
	Phase     ProgressPhase
	Completed uint64
	Total     uint64
}

type ResultCode string

const (
	ResultVerified                       ResultCode = "verified"
	ResultFailed                         ResultCode = "failed"
	ResultRegistryCredentialsUnavailable ResultCode = "registry_credentials_unavailable"
	ResultTimedOut                       ResultCode = "timed_out"
	ResultCanceled                       ResultCode = "canceled"
	ResultUncertain                      ResultCode = "result_pending"
)

// Result has only bounded tokens, never a command, request body, credential,
// or arbitrary error string.
type Result struct {
	Code             ResultCode
	ObservedState    string
	ResourceRevision string
}

type Evidence = taskstate.Evidence

type Task struct {
	TaskID                 string
	NodeID                 string
	IdempotencyKey         string
	RequestDigest          [sha256.Size]byte
	AcceptedGeneration     uint64
	Intent                 Intent
	RegistryAuthRequired   bool
	ResourceKey            string
	Status                 taskstate.Status
	DeliveryState          string
	DispatchJournalID      string
	ReconciliationRequired bool
	CreatedAt              time.Time
	UpdatedAt              time.Time
	StartedAt              *time.Time
	FinishedAt             *time.Time
	Evidence               Evidence
	Progress               Progress
	Result                 Result
}

type AgentConnection struct {
	NodeID               string
	ConnectionGeneration uint64
	JournalID            string
}

// AgentTask is a typed summary from the Agent journal. The caller must derive
// terminal evidence from a fresh Docker inspection before applying it.
type AgentTask struct {
	TaskID         string
	NodeID         string
	JournalID      string
	TargetID       string
	IdempotencyKey string
	RequestDigest  [sha256.Size]byte
	Status         taskstate.Status
	Evidence       Evidence
	Progress       Progress
	Result         Result
}

// AgentTaskFromProtocol converts a bounded wire report to the Core's typed
// transaction input. DeliveryCommitted is intentionally absent from the wire
// DTO and therefore cannot be supplied here; applyAgentReportTx loads that
// Core-owned evidence from the durable task row.
func AgentTaskFromProtocol(report protocol.TaskReport) (AgentTask, error) {
	digest, err := protocol.ParseDigest(report.RequestDigest)
	if err != nil {
		return AgentTask{}, err
	}
	return AgentTask{
		TaskID: report.TaskID, NodeID: report.NodeID, JournalID: report.JournalID, TargetID: report.TargetID,
		IdempotencyKey: report.IdempotencyKey, RequestDigest: digest, Status: report.Status,
		Evidence: report.Evidence.TaskStateEvidence(),
		Progress: Progress{Phase: ProgressPhase(report.Progress.Phase), Completed: report.Progress.Completed, Total: report.Progress.Total},
		Result:   Result{Code: ResultCode(report.Result.Code), ObservedState: report.Result.ObservedState, ResourceRevision: report.Result.ResourceRevision},
	}, nil
}

type JournalObservation struct {
	Changed         bool
	ReviewRequired  bool
	AffectedTaskIDs []string
}

type ReconcileResult struct {
	Observed int
	Pending  []string
}

type Cursor struct {
	CreatedAtNS int64
	TaskID      string
}

type Page struct {
	Tasks      []Task
	NextCursor *Cursor
}

// AuditEvent is the allowlisted, secret-free history of one durable task.
// It deliberately excludes request payloads, credentials, and free-form errors.
type AuditEvent struct {
	ID         int64
	Event      string
	FromStatus sql.NullString
	ToStatus   sql.NullString
	ActorID    sql.NullInt64
	OccurredAt time.Time
	RemoteAddr string
}

func New(db *sql.DB, options Options) (*Store, error) {
	if db == nil {
		return nil, errors.New("Core task database is nil")
	}
	if options.LeaseTTL == 0 {
		options.LeaseTTL = DefaultLeaseTTL
	}
	if options.LeaseTTL < time.Second || options.LeaseTTL > 10*time.Minute {
		return nil, errors.New("Core task Agent lease TTL must be between one second and ten minutes")
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	return &Store{db: db, leaseTTL: options.LeaseTTL, now: options.Now}, nil
}

func (s *Store) Enqueue(ctx context.Context, request EnqueueRequest) (EnqueueResult, error) {
	return s.EnqueueWithGate(ctx, request, func(context.Context) (bool, func(), error) {
		return request.ComposeManaged, nil, nil
	})
}

// EnqueueWithGate performs idempotency lookup before evaluating new-task
// readiness. The gate remains held through commit, so callers can pin a
// synchronized Agent connection while the durable acceptance transaction is
// committed without rejecting safe retries during journal synchronization.
func (s *Store) EnqueueWithGate(ctx context.Context, request EnqueueRequest, gate EnqueueGate) (EnqueueResult, error) {
	if request.TaskID == "" {
		var err error
		request.TaskID, err = newTaskID()
		if err != nil {
			return EnqueueResult{}, fmt.Errorf("generate task ID: %w", err)
		}
	}
	intentJSON, payload, resourceKey, err := validateIntent(request.Intent, false)
	if err != nil {
		return EnqueueResult{}, err
	}
	if request.RegistryAuthRequired && request.Intent.Action != ActionImagePull {
		return EnqueueResult{}, ErrInvalidRequest
	}
	identity, err := protocol.TaskIdentity(request.TaskID, request.NodeID, request.IdempotencyKey, request.Intent)
	if err != nil {
		return EnqueueResult{}, fmt.Errorf("build canonical task identity: %w", err)
	}
	if identity.ResourceKey != resourceKey || string(identity.Payload) != string(payload) {
		return EnqueueResult{}, ErrInvalidRequest
	}
	digest, err := taskstate.RequestDigest(identity)
	if err != nil {
		return EnqueueResult{}, fmt.Errorf("validate task identity: %w", err)
	}
	if len(request.TaskID) > taskstate.MaxIdentityBytes {
		return EnqueueResult{}, ErrInvalidRequest
	}

	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return EnqueueResult{}, fmt.Errorf("begin durable task acceptance: %w", err)
	}
	var releaseGate func()
	defer func() {
		if releaseGate != nil {
			releaseGate()
		}
	}()
	defer tx.Rollback()

	byKey, keyErr := loadByIdempotency(ctx, tx, request.NodeID, request.IdempotencyKey)
	if keyErr != nil && !errors.Is(keyErr, sql.ErrNoRows) {
		return EnqueueResult{}, fmt.Errorf("read task idempotency key: %w", keyErr)
	}
	byID, idErr := loadByTaskIDAnyNode(ctx, tx, request.TaskID)
	if idErr != nil && !errors.Is(idErr, sql.ErrNoRows) {
		return EnqueueResult{}, fmt.Errorf("read task ID: %w", idErr)
	}
	if idErr == nil {
		if byID.NodeID != request.NodeID || byID.IdempotencyKey != request.IdempotencyKey || byID.RequestDigest != digest || byID.RegistryAuthRequired != request.RegistryAuthRequired {
			return EnqueueResult{}, ErrTaskIDConflict
		}
		if keyErr != nil || byKey.TaskID != byID.TaskID {
			return EnqueueResult{}, ErrIdempotencyConflict
		}
		if err := tx.Commit(); err != nil {
			return EnqueueResult{}, fmt.Errorf("commit task retry lookup: %w", err)
		}
		return EnqueueResult{Task: byID, Created: false}, nil
	}
	if keyErr == nil {
		if byKey.RequestDigest != digest || byKey.RegistryAuthRequired != request.RegistryAuthRequired {
			return EnqueueResult{}, ErrIdempotencyConflict
		}
		if err := tx.Commit(); err != nil {
			return EnqueueResult{}, fmt.Errorf("commit idempotent task lookup: %w", err)
		}
		return EnqueueResult{Task: byKey, Created: false}, nil
	}

	composeManaged := request.ComposeManaged
	if gate != nil {
		composeManaged, releaseGate, err = gate(ctx)
		if err != nil {
			return EnqueueResult{}, err
		}
	}
	if composeManaged && request.Intent.Action == ActionRename {
		return EnqueueResult{}, ErrManagedRename
	}
	if composeManaged && (request.Intent.Action == protocol.TaskRebuild || request.Intent.Action == protocol.TaskRebuildCleanup) {
		return EnqueueResult{}, ErrManagedRebuild
	}

	now := s.now().UTC()
	acceptedGeneration, err := requireOnlineNode(ctx, tx, request.NodeID, now, s.leaseTTL)
	if err != nil {
		return EnqueueResult{}, err
	}
	var claimedTaskID string
	err = tx.QueryRowContext(ctx, `SELECT task_id FROM core_task_resource_claims WHERE node_id=? AND resource_key=?`, request.NodeID, resourceKey).Scan(&claimedTaskID)
	if err == nil {
		return EnqueueResult{}, fmt.Errorf("%w: task %s", ErrResourceBusy, claimedTaskID)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return EnqueueResult{}, fmt.Errorf("check resource claim: %w", err)
	}

	nowNS := now.UnixNano()
	_, err = tx.ExecContext(ctx, `INSERT INTO core_tasks(task_id,node_id,idempotency_key,request_digest,accepted_generation,target_id,resource_key,action,intent_json,registry_auth_required,status,created_at_ns,updated_at_ns)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`, request.TaskID, request.NodeID, request.IdempotencyKey, digest[:], acceptedGeneration, request.Intent.ContainerID,
		resourceKey, request.Intent.Action, intentJSON, boolInt(request.RegistryAuthRequired), taskstate.Queued, nowNS, nowNS)
	if err != nil {
		return EnqueueResult{}, fmt.Errorf("persist Core task intent: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO core_task_resource_claims(node_id,resource_key,task_id) VALUES(?,?,?)`, request.NodeID, resourceKey, request.TaskID); err != nil {
		return EnqueueResult{}, fmt.Errorf("claim task resource: %w", err)
	}
	if err := writeAudit(ctx, tx, request.NodeID, request.TaskID, "accepted", "", taskstate.Queued, request.ActorID, request.RemoteAddr, nowNS); err != nil {
		return EnqueueResult{}, fmt.Errorf("persist task acceptance audit: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return EnqueueResult{}, fmt.Errorf("commit queued task, audit, and resource claim: %w", err)
	}
	return EnqueueResult{Task: Task{TaskID: request.TaskID, NodeID: request.NodeID, IdempotencyKey: request.IdempotencyKey,
		RequestDigest: digest, AcceptedGeneration: acceptedGeneration, Intent: request.Intent, RegistryAuthRequired: request.RegistryAuthRequired, ResourceKey: resourceKey, Status: taskstate.Queued,
		DeliveryState: "ready", CreatedAt: now, UpdatedAt: now, Progress: Progress{Phase: PhaseAccepted}}, Created: true}, nil
}

// ClaimNext uses the fail-closed no-credential policy. It durably marks a
// deliverable task as sent before returning it to a transport caller, never
// reclaims a sent task after disconnect, and leaves missing reports to the
// existing reconciliation path.
func (s *Store) ClaimNext(ctx context.Context, connection AgentConnection, actorID sql.NullInt64, remoteAddr string) (Task, bool, error) {
	return s.ClaimNextWithRegistryAuth(ctx, connection, actorID, remoteAddr, nil)
}

// ClaimNextWithRegistryAuth commits one ready task for Agent delivery. An
// authenticated image pull is failed before that commit if the caller cannot
// prove its one-use credentials remain available. The credential itself never
// crosses this storage boundary.
func (s *Store) ClaimNextWithRegistryAuth(ctx context.Context, connection AgentConnection, actorID sql.NullInt64, remoteAddr string, credentialsAvailable func(Task) bool) (Task, bool, error) {
	if !validConnection(connection) {
		return Task{}, false, ErrInvalidRequest
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Task{}, false, fmt.Errorf("begin task delivery claim: %w", err)
	}
	defer tx.Rollback()
	if err := requireOnlineConnection(ctx, tx, connection, s.now().UTC(), s.leaseTTL); err != nil {
		return Task{}, false, err
	}
	journalID, review, err := readAgentJournal(ctx, tx, connection.NodeID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Task{}, false, ErrJournalNotObserved
		}
		return Task{}, false, err
	}
	if review {
		return Task{}, false, ErrReconciliationNeeded
	}
	if journalID != connection.JournalID {
		return Task{}, false, ErrJournalChanged
	}
	task, err := loadNextReady(ctx, tx, connection.NodeID, connection.ConnectionGeneration)
	if errors.Is(err, sql.ErrNoRows) {
		if err := tx.Commit(); err != nil {
			return Task{}, false, fmt.Errorf("commit empty delivery poll: %w", err)
		}
		return Task{}, false, nil
	}
	if err != nil {
		return Task{}, false, fmt.Errorf("select queued task: %w", err)
	}
	nowNS := s.now().UTC().UnixNano()
	if task.RegistryAuthRequired && (credentialsAvailable == nil || !credentialsAvailable(task)) {
		task, err = resolveRegistryCredentialsUnavailableTx(ctx, tx, task, actorID, remoteAddr, time.Unix(0, nowNS).UTC())
		if err != nil {
			return Task{}, false, err
		}
		if err := tx.Commit(); err != nil {
			return Task{}, false, fmt.Errorf("commit image pull credential-unavailable result: %w", err)
		}
		return task, false, nil
	}
	result, err := tx.ExecContext(ctx, `UPDATE core_tasks SET delivery_state='sent',dispatch_journal_id=?,delivery_committed=1,updated_at_ns=?
		WHERE task_id=? AND node_id=? AND status='queued' AND delivery_state='ready' AND reconciliation_required=0`,
		connection.JournalID, nowNS, task.TaskID, connection.NodeID)
	if err != nil {
		return Task{}, false, fmt.Errorf("durably claim task for delivery: %w", err)
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return Task{}, false, ErrTaskStateConflict
	}
	if err := writeAudit(ctx, tx, connection.NodeID, task.TaskID, "delivery_claimed", taskstate.Queued, taskstate.Queued, actorID, remoteAddr, nowNS); err != nil {
		return Task{}, false, fmt.Errorf("audit task delivery claim: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Task{}, false, fmt.Errorf("commit durable delivery claim: %w", err)
	}
	task.DeliveryState = "sent"
	task.DispatchJournalID = connection.JournalID
	task.Evidence.DeliveryCommitted = true
	task.UpdatedAt = time.Unix(0, nowNS).UTC()
	return task, true, nil
}

// CancelUndelivered safely cancels only a queued task that Core has not
// committed for Agent delivery. Once delivery may have occurred, the outcome
// is uncertain and cancellation requires Agent-side termination evidence.
// Repeating cancellation of the same proven-not-dispatched result is safe.
func (s *Store) CancelUndelivered(ctx context.Context, nodeID, taskID string, actorID sql.NullInt64, remoteAddr string) (Task, error) {
	if ctx == nil || !validTaskIdentifier(nodeID) || !validTaskIdentifier(taskID) {
		return Task{}, ErrInvalidRequest
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Task{}, fmt.Errorf("begin queued task cancellation: %w", err)
	}
	defer tx.Rollback()
	task, err := loadByTaskID(ctx, tx, nodeID, taskID)
	if err != nil {
		return Task{}, lookupError(err)
	}
	if task.Status == taskstate.Canceled && task.Result.Code == ResultCanceled && task.Result.ObservedState == "not_dispatched" &&
		task.Evidence.CancellationConfirmed && task.Evidence.ActualResultConfirmed && !task.Evidence.DeliveryCommitted {
		if err := tx.Commit(); err != nil {
			return Task{}, fmt.Errorf("commit repeated queued task cancellation read: %w", err)
		}
		return task, nil
	}
	if task.Status != taskstate.Queued || task.DeliveryState != "ready" || task.Evidence.DeliveryCommitted || task.ReconciliationRequired {
		return Task{}, ErrNotDelivered
	}
	evidence := mergeEvidence(task.Evidence, Evidence{CancellationConfirmed: true, ActualResultConfirmed: true})
	if err := taskstate.CanTransition(taskstate.Queued, taskstate.Canceled, evidence); err != nil {
		return Task{}, err
	}
	result := Result{Code: ResultCanceled, ObservedState: "not_dispatched"}
	now := s.now().UTC()
	if err := finishTaskTx(ctx, tx, task, taskstate.Canceled, evidence, result, task.Progress, now); err != nil {
		return Task{}, err
	}
	if err := writeAudit(ctx, tx, nodeID, taskID, "task_resolved", taskstate.Queued, taskstate.Canceled, actorID, remoteAddr, now.UnixNano()); err != nil {
		return Task{}, fmt.Errorf("audit safe queued task cancellation: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Task{}, fmt.Errorf("commit safe queued task cancellation: %w", err)
	}
	return loadByTaskID(ctx, s.db, nodeID, taskID)
}

func (s *Store) ObserveAgentConnection(ctx context.Context, connection AgentConnection) (JournalObservation, error) {
	if !validConnection(connection) {
		return JournalObservation{}, ErrInvalidRequest
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return JournalObservation{}, fmt.Errorf("begin Agent journal observation: %w", err)
	}
	defer tx.Rollback()
	if err := requireOnlineConnection(ctx, tx, connection, s.now().UTC(), s.leaseTTL); err != nil {
		return JournalObservation{}, err
	}
	if err := cancelStaleUndelivered(ctx, tx, connection, s.now().UTC()); err != nil {
		return JournalObservation{}, err
	}
	current, pending, review, err := readAgentJournalState(ctx, tx, connection.NodeID)
	if errors.Is(err, sql.ErrNoRows) {
		var sent int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM core_tasks WHERE node_id=? AND delivery_state IN ('sent','needs_reconciliation') AND status IN ('queued','running','unknown')`, connection.NodeID).Scan(&sent); err != nil {
			return JournalObservation{}, err
		}
		nowNS := s.now().UTC().UnixNano()
		if sent > 0 {
			if _, err := tx.ExecContext(ctx, `INSERT INTO core_task_agent_state(node_id,journal_id,pending_journal_id,review_required,updated_at_ns) VALUES(?,?,?,?,?)`, connection.NodeID, connection.JournalID, connection.JournalID, 1, nowNS); err != nil {
				return JournalObservation{}, fmt.Errorf("record untrusted initial Agent journal identity: %w", err)
			}
		} else if _, err := tx.ExecContext(ctx, `INSERT INTO core_task_agent_state(node_id,journal_id,updated_at_ns) VALUES(?,?,?)`, connection.NodeID, connection.JournalID, nowNS); err != nil {
			return JournalObservation{}, fmt.Errorf("record initial Agent journal identity: %w", err)
		}
		if err := writeAudit(ctx, tx, connection.NodeID, "", "journal_accepted", "", "", sql.NullInt64{}, "unknown", nowNS); err != nil {
			return JournalObservation{}, err
		}
		var affected []string
		if sent > 0 {
			affected, err = markSentTasksForReconciliation(ctx, tx, connection.NodeID, s.now().UTC())
			if err != nil {
				return JournalObservation{}, err
			}
		}
		if err := tx.Commit(); err != nil {
			return JournalObservation{}, fmt.Errorf("commit initial Agent journal identity: %w", err)
		}
		return JournalObservation{Changed: sent > 0, ReviewRequired: sent > 0, AffectedTaskIDs: affected}, nil
	}
	if err != nil {
		return JournalObservation{}, fmt.Errorf("read Agent journal identity: %w", err)
	}
	if connection.JournalID == current {
		if err := tx.Commit(); err != nil {
			return JournalObservation{}, fmt.Errorf("commit unchanged Agent journal observation: %w", err)
		}
		return JournalObservation{ReviewRequired: review, AffectedTaskIDs: s.reconciliationTaskIDs(ctx, connection.NodeID)}, nil
	}
	if review && pending == connection.JournalID {
		if err := tx.Commit(); err != nil {
			return JournalObservation{}, fmt.Errorf("commit repeated changed-journal observation: %w", err)
		}
		return JournalObservation{Changed: true, ReviewRequired: true, AffectedTaskIDs: s.reconciliationTaskIDs(ctx, connection.NodeID)}, nil
	}
	if _, err := tx.ExecContext(ctx, `UPDATE core_task_agent_state SET pending_journal_id=?,review_required=1,updated_at_ns=? WHERE node_id=?`, connection.JournalID, s.now().UTC().UnixNano(), connection.NodeID); err != nil {
		return JournalObservation{}, fmt.Errorf("hold changed Agent journal for reconciliation: %w", err)
	}
	_, err = markSentTasksForReconciliation(ctx, tx, connection.NodeID, s.now().UTC())
	if err != nil {
		return JournalObservation{}, err
	}
	if err := tx.Commit(); err != nil {
		return JournalObservation{}, fmt.Errorf("commit Agent journal change barrier: %w", err)
	}
	return JournalObservation{Changed: true, ReviewRequired: true, AffectedTaskIDs: s.reconciliationTaskIDs(ctx, connection.NodeID)}, nil
}

func markSentTasksForReconciliation(ctx context.Context, tx *sql.Tx, nodeID string, now time.Time) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT task_id,status FROM core_tasks WHERE node_id=? AND delivery_state IN ('sent','needs_reconciliation') AND status IN ('queued','running','unknown') ORDER BY created_at_ns,task_id`, nodeID)
	if err != nil {
		return nil, fmt.Errorf("find tasks exposed to Agent journal loss: %w", err)
	}
	type affected struct {
		id     string
		status taskstate.Status
	}
	var tasks []affected
	for rows.Next() {
		var item affected
		var status string
		if err := rows.Scan(&item.id, &status); err != nil {
			_ = rows.Close()
			return nil, err
		}
		item.status = taskstate.Status(status)
		tasks = append(tasks, item)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(tasks))
	for _, item := range tasks {
		toStatus := item.status
		if item.status == taskstate.Queued || item.status == taskstate.Running {
			toStatus = taskstate.Unknown
			evidence := Evidence{}
			if item.status == taskstate.Queued {
				evidence.DeliveryCommitted = true
			}
			if err := updateStatusTx(ctx, tx, item.id, nodeID, toStatus, evidence, Progress{Phase: PhaseReconciling}, Result{Code: ResultUncertain}, now); err != nil {
				return nil, fmt.Errorf("mark task uncertain after Agent journal loss: %w", err)
			}
		}
		if _, err := tx.ExecContext(ctx, `UPDATE core_tasks SET delivery_state='needs_reconciliation',reconciliation_required=1 WHERE task_id=? AND node_id=?`, item.id, nodeID); err != nil {
			return nil, err
		}
		if err := writeAudit(ctx, tx, nodeID, item.id, "journal_changed", item.status, toStatus, sql.NullInt64{}, "unknown", now.UnixNano()); err != nil {
			return nil, err
		}
		ids = append(ids, item.id)
	}
	return ids, nil
}

func cancelStaleUndelivered(ctx context.Context, tx *sql.Tx, connection AgentConnection, now time.Time) error {
	rows, err := tx.QueryContext(ctx, `SELECT task_id FROM core_tasks WHERE node_id=? AND accepted_generation<>? AND status='queued' AND delivery_state='ready' AND delivery_committed=0 ORDER BY created_at_ns,task_id`, connection.NodeID, connection.ConnectionGeneration)
	if err != nil {
		return fmt.Errorf("find queued tasks from an expired Agent connection: %w", err)
	}
	var taskIDs []string
	for rows.Next() {
		var taskID string
		if err := rows.Scan(&taskID); err != nil {
			_ = rows.Close()
			return err
		}
		taskIDs = append(taskIDs, taskID)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, taskID := range taskIDs {
		task, err := loadByTaskID(ctx, tx, connection.NodeID, taskID)
		if err != nil {
			return fmt.Errorf("reload stale undelivered task: %w", err)
		}
		evidence := mergeEvidence(task.Evidence, Evidence{CancellationConfirmed: true, ActualResultConfirmed: true})
		if err := taskstate.CanTransition(taskstate.Queued, taskstate.Canceled, evidence); err != nil {
			return err
		}
		result := Result{Code: ResultCanceled, ObservedState: "not_dispatched"}
		if err := finishTaskTx(ctx, tx, task, taskstate.Canceled, evidence, result, task.Progress, now); err != nil {
			return err
		}
		if err := writeAudit(ctx, tx, connection.NodeID, taskID, "task_resolved", taskstate.Queued, taskstate.Canceled, sql.NullInt64{}, "unknown", now.UnixNano()); err != nil {
			return fmt.Errorf("audit stale undelivered task cancellation: %w", err)
		}
	}
	return nil
}

func resolveRegistryCredentialsUnavailableTx(ctx context.Context, tx *sql.Tx, task Task, actorID sql.NullInt64, remoteAddr string, now time.Time) (Task, error) {
	evidence := mergeEvidence(task.Evidence, Evidence{FailureConfirmed: true, ActualResultConfirmed: true})
	if err := taskstate.CanTransition(task.Status, taskstate.Failed, evidence); err != nil {
		return Task{}, err
	}
	result := Result{Code: ResultRegistryCredentialsUnavailable, ObservedState: "credentials_unavailable"}
	if err := finishTaskTx(ctx, tx, task, taskstate.Failed, evidence, result, task.Progress, now); err != nil {
		return Task{}, fmt.Errorf("resolve image pull without one-use Registry credentials: %w", err)
	}
	if err := writeAudit(ctx, tx, task.NodeID, task.TaskID, "task_resolved", task.Status, taskstate.Failed, actorID, remoteAddr, now.UnixNano()); err != nil {
		return Task{}, fmt.Errorf("audit image pull credential-unavailable result: %w", err)
	}
	task.Status = taskstate.Failed
	task.DeliveryState = "done"
	task.Evidence = evidence
	task.Result = result
	task.FinishedAt = timePtr(now)
	task.UpdatedAt = now
	return task, nil
}

// ReconcileAgentJournal applies Agent snapshots only when the durable journal
// identity still matches. Missing entries never prove non-execution, even for
// a complete snapshot or an unchanged JournalID: retention, restore, or local
// history loss can remove records. Any sent task absent from a complete
// snapshot is held for explicit reconciliation and is never automatically
// requeued.
func (s *Store) ReconcileAgentJournal(ctx context.Context, connection AgentConnection, reports []AgentTask, complete bool) (ReconcileResult, error) {
	if !validConnection(connection) || len(reports) > MaxJournalTasks {
		return ReconcileResult{}, ErrInvalidRequest
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ReconcileResult{}, fmt.Errorf("begin Agent journal reconciliation: %w", err)
	}
	defer tx.Rollback()
	if err := requireOnlineConnection(ctx, tx, connection, s.now().UTC(), s.leaseTTL); err != nil {
		return ReconcileResult{}, err
	}
	journalID, review, err := readAgentJournal(ctx, tx, connection.NodeID)
	if err != nil {
		return ReconcileResult{}, err
	}
	if review {
		return ReconcileResult{}, ErrReconciliationNeeded
	}
	if journalID != connection.JournalID {
		return ReconcileResult{}, ErrJournalChanged
	}
	seen := make(map[string]struct{}, len(reports))
	result := ReconcileResult{}
	for _, report := range reports {
		if report.TaskID == "" {
			return ReconcileResult{}, ErrInvalidRequest
		}
		if _, duplicate := seen[report.TaskID]; duplicate {
			return ReconcileResult{}, fmt.Errorf("%w: duplicate Agent task ID", ErrInvalidRequest)
		}
		seen[report.TaskID] = struct{}{}
		if _, err := applyAgentReportTx(ctx, tx, connection, report, s.now().UTC()); err != nil {
			return ReconcileResult{}, err
		}
		result.Observed++
	}
	if complete {
		rows, err := tx.QueryContext(ctx, `SELECT task_id,status FROM core_tasks WHERE node_id=? AND dispatch_journal_id=? AND delivery_state='sent' AND status IN ('queued','running','unknown') ORDER BY created_at_ns,task_id`, connection.NodeID, connection.JournalID)
		if err != nil {
			return ReconcileResult{}, fmt.Errorf("list tasks absent from complete Agent journal: %w", err)
		}
		type absentTask struct {
			id     string
			status taskstate.Status
		}
		var absent []absentTask
		for rows.Next() {
			var item absentTask
			var status string
			if err := rows.Scan(&item.id, &status); err != nil {
				_ = rows.Close()
				return ReconcileResult{}, err
			}
			item.status = taskstate.Status(status)
			absent = append(absent, item)
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return ReconcileResult{}, err
		}
		if err := rows.Close(); err != nil {
			return ReconcileResult{}, err
		}
		for _, item := range absent {
			if _, ok := seen[item.id]; ok {
				continue
			}
			if item.status == taskstate.Queued || item.status == taskstate.Running {
				now := s.now().UTC()
				evidence := Evidence{}
				if item.status == taskstate.Queued {
					evidence.DeliveryCommitted = true
				}
				if err := updateStatusTx(ctx, tx, item.id, connection.NodeID, taskstate.Unknown, evidence, Progress{Phase: PhaseReconciling}, Result{Code: ResultUncertain}, now); err != nil {
					return ReconcileResult{}, fmt.Errorf("mark missing Agent history unknown: %w", err)
				}
				if err := writeAudit(ctx, tx, connection.NodeID, item.id, "agent_status", item.status, taskstate.Unknown, sql.NullInt64{}, "unknown", now.UnixNano()); err != nil {
					return ReconcileResult{}, err
				}
			}
			if _, err := tx.ExecContext(ctx, `UPDATE core_tasks SET reconciliation_required=1,delivery_state='needs_reconciliation' WHERE task_id=? AND node_id=?`, item.id, connection.NodeID); err != nil {
				return ReconcileResult{}, err
			}
			result.Pending = append(result.Pending, item.id)
		}
	}
	if err := tx.Commit(); err != nil {
		return ReconcileResult{}, fmt.Errorf("commit Agent journal reconciliation: %w", err)
	}
	return result, nil
}

// ResolveUncertainTask records a caller-supplied, independently verified
// terminal outcome for a task held behind a journal-loss barrier. It still
// passes the shared seven-state proof rules and releases the claim only after
// a valid terminal transition.
func (s *Store) ResolveUncertainTask(ctx context.Context, nodeID, taskID string, status taskstate.Status, evidence Evidence, result Result, actorID sql.NullInt64, remoteAddr string) error {
	if nodeID == "" || taskID == "" || !taskstate.IsTerminal(status) || validateResult(status, result) != nil {
		return ErrInvalidRequest
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin verified task resolution: %w", err)
	}
	defer tx.Rollback()
	task, err := loadByTaskID(ctx, tx, nodeID, taskID)
	if err != nil {
		return lookupError(err)
	}
	if !task.ReconciliationRequired {
		return ErrTaskStateConflict
	}
	merged := mergeEvidence(task.Evidence, evidence)
	if err := taskstate.CanTransition(task.Status, status, merged); err != nil {
		return err
	}
	if err := finishTaskTx(ctx, tx, task, status, merged, result, Progress{Phase: PhaseReconciling}, s.now().UTC()); err != nil {
		return err
	}
	if err := writeAudit(ctx, tx, nodeID, taskID, "task_resolved", task.Status, status, actorID, remoteAddr, s.now().UTC().UnixNano()); err != nil {
		return err
	}
	return tx.Commit()
}

// CompleteJournalReplacement changes the trusted JournalID only after every
// task marked for reconciliation has been resolved from actual resource state.
func (s *Store) CompleteJournalReplacement(ctx context.Context, connection AgentConnection) error {
	if !validConnection(connection) {
		return ErrInvalidRequest
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin Agent journal replacement: %w", err)
	}
	defer tx.Rollback()
	if err := requireOnlineConnection(ctx, tx, connection, s.now().UTC(), s.leaseTTL); err != nil {
		return err
	}
	var pending sql.NullString
	var review int
	if err := tx.QueryRowContext(ctx, `SELECT pending_journal_id,review_required FROM core_task_agent_state WHERE node_id=?`, connection.NodeID).Scan(&pending, &review); err != nil {
		return fmt.Errorf("read pending Agent journal replacement: %w", err)
	}
	if review == 0 || !pending.Valid || pending.String != connection.JournalID {
		return ErrJournalChanged
	}
	var unresolved int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM core_tasks WHERE node_id=? AND reconciliation_required=1`, connection.NodeID).Scan(&unresolved); err != nil {
		return err
	}
	if unresolved != 0 {
		return ErrReconciliationNeeded
	}
	if _, err := tx.ExecContext(ctx, `UPDATE core_task_agent_state SET journal_id=pending_journal_id,pending_journal_id=NULL,review_required=0,updated_at_ns=? WHERE node_id=?`, s.now().UTC().UnixNano(), connection.NodeID); err != nil {
		return err
	}
	if err := writeAudit(ctx, tx, connection.NodeID, "", "journal_replaced", "", "", sql.NullInt64{}, "unknown", s.now().UTC().UnixNano()); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) Get(ctx context.Context, nodeID, taskID string) (Task, error) {
	task, err := loadByTaskID(ctx, s.db, nodeID, taskID)
	if err != nil {
		return Task{}, lookupError(err)
	}
	return task, nil
}

// ReconciliationCandidates returns only Agent-reported unknown tasks whose
// durable dispatch journal still matches this authenticated connection. Tasks
// inferred missing from a complete snapshot set reconciliation_required and
// are deliberately excluded: an absent journal entry is not proof that the
// Agent can safely inspect or replay it.
func (s *Store) ReconciliationCandidates(ctx context.Context, connection AgentConnection, limit int) ([]Task, error) {
	if ctx == nil || !validConnection(connection) || limit < 1 || limit > MaxPageSize {
		return nil, ErrInvalidRequest
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin task reconciliation candidate snapshot: %w", err)
	}
	defer tx.Rollback()
	if err := requireOnlineConnection(ctx, tx, connection, s.now().UTC(), s.leaseTTL); err != nil {
		return nil, err
	}
	journalID, review, err := readAgentJournal(ctx, tx, connection.NodeID)
	if err != nil {
		return nil, fmt.Errorf("read Agent journal identity for reconciliation: %w", err)
	}
	if review {
		return nil, ErrReconciliationNeeded
	}
	if journalID != connection.JournalID {
		return nil, ErrJournalChanged
	}
	rows, err := tx.QueryContext(ctx, `SELECT `+taskColumns+` FROM core_tasks
		WHERE node_id=? AND dispatch_journal_id=? AND status='unknown' AND reconciliation_required=0
		ORDER BY updated_at_ns,task_id LIMIT ?`, connection.NodeID, connection.JournalID, limit)
	if err != nil {
		return nil, fmt.Errorf("query Agent-reported unknown tasks for reconciliation: %w", err)
	}
	tasks := make([]Task, 0, limit)
	for rows.Next() {
		task, err := scanTask(rows)
		if err != nil {
			_ = rows.Close()
			return nil, err
		}
		tasks = append(tasks, task)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, fmt.Errorf("read Agent-reported unknown tasks for reconciliation: %w", err)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("close Agent reconciliation candidate query: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit task reconciliation candidate snapshot: %w", err)
	}
	return tasks, nil
}

func (s *Store) List(ctx context.Context, nodeID string, limit int, after *Cursor) (Page, error) {
	if nodeID == "" || limit < 1 || limit > MaxPageSize {
		return Page{}, ErrInvalidRequest
	}
	query := `SELECT ` + taskColumns + ` FROM core_tasks WHERE node_id=?`
	args := []any{nodeID}
	if after != nil {
		if after.CreatedAtNS <= 0 || after.TaskID == "" {
			return Page{}, ErrInvalidRequest
		}
		query += ` AND (created_at_ns < ? OR (created_at_ns = ? AND task_id < ?))`
		args = append(args, after.CreatedAtNS, after.CreatedAtNS, after.TaskID)
	}
	query += ` ORDER BY created_at_ns DESC,task_id DESC LIMIT ?`
	args = append(args, limit+1)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return Page{}, fmt.Errorf("list Core tasks: %w", err)
	}
	defer rows.Close()
	page := Page{Tasks: make([]Task, 0, limit)}
	for rows.Next() {
		task, err := scanTask(rows)
		if err != nil {
			return Page{}, err
		}
		if len(page.Tasks) == limit {
			last := page.Tasks[len(page.Tasks)-1]
			page.NextCursor = &Cursor{CreatedAtNS: last.CreatedAt.UnixNano(), TaskID: last.TaskID}
			break
		}
		page.Tasks = append(page.Tasks, task)
	}
	if err := rows.Err(); err != nil {
		return Page{}, fmt.Errorf("read Core task page: %w", err)
	}
	return page, nil
}

// AuditEvents returns the bounded sanitized audit trail for an existing task.
// The node and task predicates are both required so task IDs cannot expose
// another node's audit records.
func (s *Store) AuditEvents(ctx context.Context, nodeID, taskID string) ([]AuditEvent, error) {
	if ctx == nil || nodeID == "" || taskID == "" {
		return nil, ErrInvalidRequest
	}
	if _, err := s.Get(ctx, nodeID, taskID); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id,event,from_status,to_status,actor_id,occurred_at_ns,remote_addr
		FROM core_task_audit_events WHERE node_id=? AND task_id=? ORDER BY id LIMIT ?`, nodeID, taskID, MaxAuditEventsPerTask+1)
	if err != nil {
		return nil, fmt.Errorf("read durable task audit events: %w", err)
	}
	defer rows.Close()
	events := make([]AuditEvent, 0, 8)
	for rows.Next() {
		var event AuditEvent
		var occurredAtNS int64
		if err := rows.Scan(&event.ID, &event.Event, &event.FromStatus, &event.ToStatus, &event.ActorID, &occurredAtNS, &event.RemoteAddr); err != nil {
			return nil, fmt.Errorf("decode durable task audit event: %w", err)
		}
		if len(events) == MaxAuditEventsPerTask {
			return nil, errors.New("durable task audit history exceeds its bound")
		}
		event.OccurredAt = time.Unix(0, occurredAtNS).UTC()
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate durable task audit events: %w", err)
	}
	return events, nil
}

func (s *Store) reconciliationTaskIDs(ctx context.Context, nodeID string) []string {
	rows, err := s.db.QueryContext(ctx, `SELECT task_id FROM core_tasks WHERE node_id=? AND reconciliation_required=1 ORDER BY created_at_ns,task_id`, nodeID)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if rows.Scan(&id) != nil {
			return nil
		}
		ids = append(ids, id)
	}
	return ids
}

func applyAgentReportTx(ctx context.Context, tx *sql.Tx, connection AgentConnection, report AgentTask, now time.Time) (Task, error) {
	task, err := loadByTaskID(ctx, tx, connection.NodeID, report.TaskID)
	if err != nil {
		return Task{}, lookupError(err)
	}
	if task.DispatchJournalID != connection.JournalID || report.NodeID != connection.NodeID ||
		report.JournalID != connection.JournalID || report.TargetID != task.Intent.ContainerID ||
		report.IdempotencyKey != task.IdempotencyKey ||
		subtle.ConstantTimeCompare(report.RequestDigest[:], task.RequestDigest[:]) != 1 || report.Evidence.DeliveryCommitted {
		return Task{}, ErrTaskStateConflict
	}
	if !validProgress(report.Progress) {
		return Task{}, ErrInvalidRequest
	}
	if taskstate.IsTerminal(task.Status) {
		if task.Status == report.Status && task.Result == report.Result {
			return task, nil
		}
		return Task{}, ErrTaskStateConflict
	}
	if task.DeliveryState != "sent" && task.DeliveryState != "needs_reconciliation" {
		return Task{}, ErrNotDelivered
	}
	status := report.Status
	if status != taskstate.Queued && status != taskstate.Running && status != taskstate.Unknown && !taskstate.IsTerminal(status) {
		return Task{}, ErrTaskStateConflict
	}
	// Result fields are terminal-only. In particular, never persist arbitrary
	// Agent strings from a progress report into the durable result columns.
	switch status {
	case taskstate.Queued, taskstate.Running:
		if report.Result != (Result{}) {
			return Task{}, ErrTaskStateConflict
		}
	case taskstate.Unknown:
		if report.Result != (Result{}) && report.Result != (Result{Code: ResultUncertain}) {
			return Task{}, ErrTaskStateConflict
		}
	}
	merged := mergeEvidence(task.Evidence, report.Evidence)
	from := task.Status
	if from == taskstate.Queued {
		switch {
		case status == taskstate.Queued:
			// A durable Agent queue entry proves receipt but does not prove
			// execution. Keep the Core task queued and sent; never dispatch it a
			// second time. Agent queued records must not carry execution/result
			// evidence or a terminal result.
			if report.Evidence != (Evidence{}) || report.Result != (Result{}) {
				return Task{}, ErrTaskStateConflict
			}
			if err := persistProgressTx(ctx, tx, task, merged, report.Progress, Result{}, now); err != nil {
				return Task{}, err
			}
			return loadByTaskID(ctx, tx, connection.NodeID, report.TaskID)
		case status == taskstate.Running:
			if err := persistNonterminalTx(ctx, tx, task, taskstate.Queued, taskstate.Running, merged, report.Progress, Result{}, now, true); err != nil {
				return Task{}, err
			}
			task.Status = taskstate.Running
			task.Evidence = merged
			task.StartedAt = timePtr(now)
			from = taskstate.Running
		case status == taskstate.Unknown:
			if report.Evidence.ExecutionAttempted {
				// A real Agent execution record may have lost its running ACK. Preserve
				// that evidence as the intermediate status transition.
				if err := persistNonterminalTx(ctx, tx, task, taskstate.Queued, taskstate.Running, merged, report.Progress, Result{}, now, true); err != nil {
					return Task{}, err
				}
				task.Status = taskstate.Running
				task.Evidence = merged
				task.StartedAt = timePtr(now)
				from = taskstate.Running
			} else {
				// Core's committed dispatch marker proves only that the task may have
				// reached Agent. It is Core-owned and is read from the durable row,
				// never manufactured from an Agent report.
				if err := taskstate.CanTransition(taskstate.Queued, taskstate.Unknown, task.Evidence); err != nil {
					return Task{}, err
				}
				report.Result = Result{Code: ResultUncertain}
				report.Progress.Phase = PhaseReconciling
				if err := persistNonterminalTx(ctx, tx, task, taskstate.Queued, taskstate.Unknown, task.Evidence, report.Progress, report.Result, now, false); err != nil {
					return Task{}, err
				}
				return loadByTaskID(ctx, tx, connection.NodeID, report.TaskID)
			}
		case taskstate.IsTerminal(status) && (status == taskstate.Failed || status == taskstate.Canceled) && !merged.ExecutionAttempted:
			if err := taskstate.CanTransition(taskstate.Queued, status, merged); err != nil {
				return Task{}, err
			}
			if err := validateResult(status, report.Result); err != nil {
				return Task{}, err
			}
			if err := finishTaskTx(ctx, tx, task, status, merged, report.Result, report.Progress, now); err != nil {
				return Task{}, err
			}
			if err := writeAudit(ctx, tx, connection.NodeID, report.TaskID, "agent_status", taskstate.Queued, status, sql.NullInt64{}, "unknown", now.UnixNano()); err != nil {
				return Task{}, err
			}
			return loadByTaskID(ctx, tx, connection.NodeID, report.TaskID)
		default:
			// A terminal result may arrive before the running acknowledgement.
			// Record running only when the Agent supplied durable attempt proof.
			if err := persistNonterminalTx(ctx, tx, task, taskstate.Queued, taskstate.Running, merged, report.Progress, Result{}, now, true); err != nil {
				return Task{}, err
			}
			task.Status = taskstate.Running
			task.Evidence = merged
			task.StartedAt = timePtr(now)
			from = taskstate.Running
		}
	}
	if status == from {
		if status != taskstate.Running && status != taskstate.Unknown {
			return Task{}, ErrTaskStateConflict
		}
		if status == taskstate.Unknown {
			if report.Result.Code != "" && report.Result.Code != ResultUncertain {
				return Task{}, ErrTaskStateConflict
			}
			report.Result = Result{Code: ResultUncertain}
			report.Progress.Phase = PhaseReconciling
		}
		if err := persistProgressTx(ctx, tx, task, merged, report.Progress, report.Result, now); err != nil {
			return Task{}, err
		}
		return loadByTaskID(ctx, tx, connection.NodeID, report.TaskID)
	}
	if status == taskstate.Unknown {
		if report.Result.Code != "" && report.Result.Code != ResultUncertain {
			return Task{}, ErrTaskStateConflict
		}
		report.Result = Result{Code: ResultUncertain}
		report.Progress.Phase = PhaseReconciling
	} else if taskstate.IsTerminal(status) {
		if err := validateResult(status, report.Result); err != nil {
			return Task{}, err
		}
	}
	if taskstate.IsTerminal(status) {
		if err := taskstate.CanTransition(from, status, merged); err != nil {
			return Task{}, err
		}
		if err := finishTaskTx(ctx, tx, task, status, merged, report.Result, report.Progress, now); err != nil {
			return Task{}, err
		}
		if err := writeAudit(ctx, tx, connection.NodeID, report.TaskID, "agent_status", from, status, sql.NullInt64{}, "unknown", now.UnixNano()); err != nil {
			return Task{}, err
		}
	} else {
		if err := taskstate.CanTransition(from, status, merged); err != nil {
			return Task{}, err
		}
		if err := persistNonterminalTx(ctx, tx, task, from, status, merged, report.Progress, report.Result, now, false); err != nil {
			return Task{}, err
		}
	}
	return loadByTaskID(ctx, tx, connection.NodeID, report.TaskID)
}

func persistNonterminalTx(ctx context.Context, tx *sql.Tx, task Task, from, to taskstate.Status, evidence Evidence, progress Progress, result Result, now time.Time, started bool) error {
	if err := taskstate.CanTransition(from, to, evidence); err != nil {
		return err
	}
	if to == taskstate.Unknown {
		result = Result{Code: ResultUncertain}
		progress.Phase = PhaseReconciling
	}
	nowNS := now.UnixNano()
	var startedAt any
	if started {
		startedAt = nowNS
	}
	_, err := tx.ExecContext(ctx, `UPDATE core_tasks SET status=?,execution_attempted=?,execution_completed=?,failure_confirmed=?,postcondition_verified=?,process_terminated=?,actual_result_confirmed=?,cancellation_confirmed=?,delivery_committed=?,
		started_at_ns=COALESCE(started_at_ns,?),progress_phase=?,progress_completed=?,progress_total=?,result_code=?,updated_at_ns=?,reconciliation_required=0,delivery_state='sent'
		WHERE node_id=? AND task_id=?`,
		to, boolInt(evidence.ExecutionAttempted), boolInt(evidence.ExecutionCompleted), boolInt(evidence.FailureConfirmed), boolInt(evidence.PostconditionVerified), boolInt(evidence.ProcessTerminated), boolInt(evidence.ActualResultConfirmed), boolInt(evidence.CancellationConfirmed), boolInt(evidence.DeliveryCommitted),
		startedAt, progress.Phase, progress.Completed, progress.Total, result.Code, nowNS, task.NodeID, task.TaskID)
	if err != nil {
		return fmt.Errorf("persist Agent task state: %w", err)
	}
	return writeAudit(ctx, tx, task.NodeID, task.TaskID, "agent_status", from, to, sql.NullInt64{}, "unknown", nowNS)
}

func persistProgressTx(ctx context.Context, tx *sql.Tx, task Task, evidence Evidence, progress Progress, result Result, now time.Time) error {
	if !validProgress(progress) {
		return ErrInvalidRequest
	}
	if task.Status == taskstate.Unknown {
		result = Result{Code: ResultUncertain}
		progress.Phase = PhaseReconciling
	}
	_, err := tx.ExecContext(ctx, `UPDATE core_tasks SET execution_attempted=?,execution_completed=?,failure_confirmed=?,postcondition_verified=?,process_terminated=?,actual_result_confirmed=?,cancellation_confirmed=?,delivery_committed=?,
		progress_phase=?,progress_completed=?,progress_total=?,result_code=?,updated_at_ns=?,reconciliation_required=0,delivery_state='sent' WHERE node_id=? AND task_id=?`,
		boolInt(evidence.ExecutionAttempted), boolInt(evidence.ExecutionCompleted), boolInt(evidence.FailureConfirmed), boolInt(evidence.PostconditionVerified), boolInt(evidence.ProcessTerminated), boolInt(evidence.ActualResultConfirmed), boolInt(evidence.CancellationConfirmed), boolInt(evidence.DeliveryCommitted),
		progress.Phase, progress.Completed, progress.Total, result.Code, now.UnixNano(), task.NodeID, task.TaskID)
	if err != nil {
		return fmt.Errorf("persist Agent task progress: %w", err)
	}
	return nil
}

func finishTaskTx(ctx context.Context, tx *sql.Tx, task Task, status taskstate.Status, evidence Evidence, result Result, progress Progress, now time.Time) error {
	if err := validateResult(status, result); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE core_tasks SET status=?,delivery_state='done',dispatch_journal_id=COALESCE(dispatch_journal_id,''),reconciliation_required=0,
		updated_at_ns=?,finished_at_ns=?,execution_attempted=?,execution_completed=?,failure_confirmed=?,postcondition_verified=?,process_terminated=?,actual_result_confirmed=?,cancellation_confirmed=?,delivery_committed=?,
		progress_phase=?,progress_completed=?,progress_total=?,result_code=?,observed_state=?,resource_revision=? WHERE node_id=? AND task_id=?`,
		status, now.UnixNano(), now.UnixNano(), boolInt(evidence.ExecutionAttempted), boolInt(evidence.ExecutionCompleted), boolInt(evidence.FailureConfirmed), boolInt(evidence.PostconditionVerified), boolInt(evidence.ProcessTerminated), boolInt(evidence.ActualResultConfirmed), boolInt(evidence.CancellationConfirmed), boolInt(evidence.DeliveryCommitted),
		progress.Phase, progress.Completed, progress.Total, result.Code, result.ObservedState, result.ResourceRevision, task.NodeID, task.TaskID); err != nil {
		return fmt.Errorf("persist verified task result: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM core_task_resource_claims WHERE node_id=? AND task_id=?`, task.NodeID, task.TaskID); err != nil {
		return fmt.Errorf("release verified task resource: %w", err)
	}
	return nil
}

func updateStatusTx(ctx context.Context, tx *sql.Tx, taskID, nodeID string, status taskstate.Status, evidence Evidence, progress Progress, result Result, now time.Time) error {
	task, err := loadByTaskID(ctx, tx, nodeID, taskID)
	if err != nil {
		return err
	}
	merged := mergeEvidence(task.Evidence, evidence)
	if err := taskstate.CanTransition(task.Status, status, merged); err != nil {
		return err
	}
	if status == taskstate.Unknown {
		result = Result{Code: ResultUncertain}
	}
	_, err = tx.ExecContext(ctx, `UPDATE core_tasks SET status=?,execution_attempted=?,execution_completed=?,failure_confirmed=?,postcondition_verified=?,process_terminated=?,actual_result_confirmed=?,cancellation_confirmed=?,delivery_committed=?,progress_phase=?,result_code=?,updated_at_ns=? WHERE node_id=? AND task_id=?`,
		status, boolInt(merged.ExecutionAttempted), boolInt(merged.ExecutionCompleted), boolInt(merged.FailureConfirmed), boolInt(merged.PostconditionVerified), boolInt(merged.ProcessTerminated), boolInt(merged.ActualResultConfirmed), boolInt(merged.CancellationConfirmed), boolInt(merged.DeliveryCommitted), progress.Phase, result.Code, now.UnixNano(), nodeID, taskID)
	return err
}

const taskColumns = `task_id,node_id,idempotency_key,request_digest,accepted_generation,target_id,resource_key,action,intent_json,registry_auth_required,status,delivery_state,COALESCE(dispatch_journal_id,''),reconciliation_required,created_at_ns,updated_at_ns,started_at_ns,finished_at_ns,execution_attempted,execution_completed,failure_confirmed,postcondition_verified,process_terminated,actual_result_confirmed,cancellation_confirmed,delivery_committed,progress_phase,progress_completed,progress_total,result_code,observed_state,resource_revision`

type rowScanner interface{ Scan(...any) error }

func scanTask(row rowScanner) (Task, error) {
	var task Task
	var digest []byte
	var intentJSON string
	var action, status, phase string
	var authRequired, reconciliation, attempted, completed, failed, verified, terminated, confirmed, canceled, delivered int
	var created, updated int64
	var started, finished sql.NullInt64
	var progressCompleted, progressTotal uint64
	err := row.Scan(&task.TaskID, &task.NodeID, &task.IdempotencyKey, &digest, &task.AcceptedGeneration, &task.Intent.ContainerID, &task.ResourceKey, &action, &intentJSON, &authRequired, &status,
		&task.DeliveryState, &task.DispatchJournalID, &reconciliation, &created, &updated, &started, &finished,
		&attempted, &completed, &failed, &verified, &terminated, &confirmed, &canceled, &delivered,
		&phase, &progressCompleted, &progressTotal, &task.Result.Code, &task.Result.ObservedState, &task.Result.ResourceRevision)
	if err != nil {
		return Task{}, err
	}
	if len(digest) != sha256.Size {
		return Task{}, errors.New("stored task digest has invalid length")
	}
	copy(task.RequestDigest[:], digest)
	if err := json.Unmarshal([]byte(intentJSON), &task.Intent); err != nil {
		return Task{}, fmt.Errorf("decode stored typed task intent: %w", err)
	}
	if task.Intent.Action != Action(action) || task.Intent.ContainerID == "" {
		return Task{}, errors.New("stored task intent does not match indexed fields")
	}
	task.Status = taskstate.Status(status)
	task.RegistryAuthRequired = authRequired != 0
	task.ReconciliationRequired = reconciliation != 0
	task.CreatedAt = time.Unix(0, created).UTC()
	task.UpdatedAt = time.Unix(0, updated).UTC()
	if started.Valid {
		task.StartedAt = timePtr(time.Unix(0, started.Int64).UTC())
	}
	if finished.Valid {
		task.FinishedAt = timePtr(time.Unix(0, finished.Int64).UTC())
	}
	task.Evidence = Evidence{ExecutionAttempted: attempted != 0, ExecutionCompleted: completed != 0, FailureConfirmed: failed != 0, PostconditionVerified: verified != 0, ProcessTerminated: terminated != 0, ActualResultConfirmed: confirmed != 0, CancellationConfirmed: canceled != 0, DeliveryCommitted: delivered != 0}
	task.Progress = Progress{Phase: ProgressPhase(phase), Completed: progressCompleted, Total: progressTotal}
	return task, nil
}

func loadByTaskID(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, nodeID, taskID string) (Task, error) {
	return scanTask(q.QueryRowContext(ctx, `SELECT `+taskColumns+` FROM core_tasks WHERE node_id=? AND task_id=?`, nodeID, taskID))
}

func loadByTaskIDAnyNode(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, taskID string) (Task, error) {
	return scanTask(q.QueryRowContext(ctx, `SELECT `+taskColumns+` FROM core_tasks WHERE task_id=?`, taskID))
}

func loadByIdempotency(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, nodeID, key string) (Task, error) {
	return scanTask(q.QueryRowContext(ctx, `SELECT `+taskColumns+` FROM core_tasks WHERE node_id=? AND idempotency_key=?`, nodeID, key))
}

func loadNextReady(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, nodeID string, generation uint64) (Task, error) {
	return scanTask(q.QueryRowContext(ctx, `SELECT `+taskColumns+` FROM core_tasks WHERE node_id=? AND accepted_generation=? AND status='queued' AND delivery_state='ready' AND reconciliation_required=0 ORDER BY created_at_ns,task_id LIMIT 1`, nodeID, generation))
}

func requireOnlineNode(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, nodeID string, now time.Time, ttl time.Duration) (uint64, error) {
	var status string
	var lastSeen sql.NullInt64
	var generation uint64
	err := q.QueryRowContext(ctx, `SELECT status,connection_generation,last_seen_at FROM nodes WHERE id=?`, nodeID).Scan(&status, &generation, &lastSeen)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrNodeNotFound
	}
	if err != nil {
		return 0, fmt.Errorf("read target node lease: %w", err)
	}
	if status != "online" || !lastSeen.Valid {
		return 0, ErrNodeOffline
	}
	seen := time.Unix(0, lastSeen.Int64)
	if now.Before(seen) || !now.Before(seen.Add(ttl)) {
		return 0, ErrNodeOffline
	}
	return generation, nil
}

func requireOnlineConnection(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, connection AgentConnection, now time.Time, ttl time.Duration) error {
	var status string
	var generation uint64
	var lastSeen sql.NullInt64
	err := q.QueryRowContext(ctx, `SELECT status,connection_generation,last_seen_at FROM nodes WHERE id=?`, connection.NodeID).Scan(&status, &generation, &lastSeen)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNodeNotFound
	}
	if err != nil {
		return fmt.Errorf("read authenticated Agent lease: %w", err)
	}
	if status != "online" || generation != connection.ConnectionGeneration || !lastSeen.Valid {
		return ErrStaleAgent
	}
	seen := time.Unix(0, lastSeen.Int64)
	if now.Before(seen) || !now.Before(seen.Add(ttl)) {
		return ErrStaleAgent
	}
	return nil
}

func readAgentJournal(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, nodeID string) (string, bool, error) {
	current, _, review, err := readAgentJournalState(ctx, q, nodeID)
	return current, review, err
}

func readAgentJournalState(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, nodeID string) (string, string, bool, error) {
	var current string
	var pending sql.NullString
	var review int
	err := q.QueryRowContext(ctx, `SELECT journal_id,pending_journal_id,review_required FROM core_task_agent_state WHERE node_id=?`, nodeID).Scan(&current, &pending, &review)
	return current, pending.String, review != 0, err
}

func writeAudit(ctx context.Context, tx *sql.Tx, nodeID, taskID, event string, from, to taskstate.Status, actor sql.NullInt64, remote string, occurred int64) error {
	if remote == "" || remoteIP(remote) == nil {
		remote = "unknown"
	}
	var fromValue, toValue any
	if from != "" {
		fromValue = string(from)
	}
	if to != "" {
		toValue = string(to)
	}
	var task any
	if taskID != "" {
		task = taskID
	}
	var actorValue any
	if actor.Valid {
		actorValue = actor.Int64
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO core_task_audit_events(node_id,task_id,event,from_status,to_status,actor_id,occurred_at_ns,remote_addr) VALUES(?,?,?,?,?,?,?,?)`,
		nodeID, task, event, fromValue, toValue, actorValue, occurred, remote)
	return err
}

func remoteIP(value string) net.IP {
	if host, _, err := net.SplitHostPort(value); err == nil {
		value = host
	}
	return net.ParseIP(value)
}

func validateIntent(intent Intent, composeManaged bool) (string, []byte, string, error) {
	if composeManaged && intent.Action == ActionRename {
		return "", nil, "", ErrManagedRename
	}
	if err := protocol.ValidateTaskIntent(intent); err != nil {
		return "", nil, "", ErrInvalidRequest
	}
	resourceKey := "docker-container:" + intent.ContainerID
	if intent.Action == ActionImagePull || intent.Action == ActionImageDelete {
		resourceKey = "docker-image:" + intent.ContainerID
	} else if protocol.IsComposeTaskAction(intent.Action) {
		resourceKey = "docker-compose-project:" + intent.ContainerID
	} else if protocol.IsFileTaskAction(intent.Action) {
		resourceKey = "filesystem-path:" + intent.ContainerID
	}
	canonical, err := protocol.CanonicalTaskIntent(intent)
	if err != nil {
		return "", nil, "", fmt.Errorf("canonicalize typed safe task intent: %w", err)
	}
	return string(canonical), canonical, resourceKey, nil
}

func validTaskIdentifier(value string) bool {
	if value == "" || len(value) > taskstate.MaxIdentityBytes || strings.TrimSpace(value) != value {
		return false
	}
	for _, r := range value {
		if r < 0x21 || r == 0x7f {
			return false
		}
	}
	return true
}

func validConnection(connection AgentConnection) bool {
	return validTaskIdentifier(connection.NodeID) && connection.ConnectionGeneration > 0 && validJournalID(connection.JournalID)
}

func validJournalID(value string) bool {
	if len(value) != 64 {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == 32
}

func newTaskID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return "ndt_" + hex.EncodeToString(value[:]), nil
}

func lookupError(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return ErrTaskNotFound
	}
	return err
}

func validProgress(progress Progress) bool {
	if progress.Total > 0 && progress.Completed > progress.Total {
		return false
	}
	switch progress.Phase {
	case PhaseAccepted, PhasePreparing, PhaseExecuting, PhaseVerifying, PhaseReconciling:
		return true
	default:
		return false
	}
}

func validateResult(status taskstate.Status, result Result) error {
	want := map[taskstate.Status]ResultCode{taskstate.Succeeded: ResultVerified, taskstate.Failed: ResultFailed, taskstate.TimedOut: ResultTimedOut, taskstate.Canceled: ResultCanceled}[status]
	validFailedResult := status == taskstate.Failed && result.Code == ResultRegistryCredentialsUnavailable
	if result.Code != want && !validFailedResult || !safeToken(string(result.Code), 32) || !safeToken(result.ObservedState, 64) || !safeToken(result.ResourceRevision, 128) {
		return ErrTaskStateConflict
	}
	return nil
}

func safeToken(value string, maximum int) bool {
	if len(value) > maximum {
		return false
	}
	for _, r := range value {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || strings.ContainsRune("._:-", r)) {
			return false
		}
	}
	return true
}

func mergeEvidence(old, next Evidence) Evidence {
	return Evidence{ExecutionAttempted: old.ExecutionAttempted || next.ExecutionAttempted, ExecutionCompleted: old.ExecutionCompleted || next.ExecutionCompleted,
		FailureConfirmed: old.FailureConfirmed || next.FailureConfirmed, PostconditionVerified: old.PostconditionVerified || next.PostconditionVerified,
		ProcessTerminated: old.ProcessTerminated || next.ProcessTerminated, ActualResultConfirmed: old.ActualResultConfirmed || next.ActualResultConfirmed,
		CancellationConfirmed: old.CancellationConfirmed || next.CancellationConfirmed,
		DeliveryCommitted:     old.DeliveryCommitted || next.DeliveryCommitted}
}

func timePtr(value time.Time) *time.Time { return &value }

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
