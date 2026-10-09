package tasks

import (
	"bufio"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/CST-Cat/NodeDance/internal/protocol"
	"github.com/CST-Cat/NodeDance/internal/taskstate"
	_ "modernc.org/sqlite"
)

const testNodeID = "b738a2d2-a255-4912-9e2e-26f974ac2529"

type testDB struct {
	db    *sql.DB
	store *Store
	now   time.Time
}

func openTestDB(t *testing.T, databasePath string, now time.Time) testDB {
	t.Helper()
	if databasePath == "" {
		databasePath = filepath.Join(t.TempDir(), "tasks.sqlite")
	}
	dsnURL := url.URL{Scheme: "file", Path: filepath.ToSlash(databasePath)}
	dsnURL.RawQuery = "_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)&_pragma=synchronous(FULL)"
	db, err := sql.Open("sqlite", dsnURL.String())
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	if err := db.Ping(); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`PRAGMA journal_mode=WAL`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS nodes(id TEXT PRIMARY KEY,status TEXT NOT NULL,connection_generation INTEGER NOT NULL,last_seen_at INTEGER)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO nodes(id,status,connection_generation,last_seen_at) VALUES(?,?,?,?) ON CONFLICT(id) DO UPDATE SET status=excluded.status,connection_generation=excluded.connection_generation,last_seen_at=excluded.last_seen_at`, testNodeID, "online", 7, now.UnixNano()); err != nil {
		t.Fatal(err)
	}
	var schemaPresent int
	if err := db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name='core_tasks'`).Scan(&schemaPresent); err != nil {
		t.Fatal(err)
	}
	if schemaPresent == 0 {
		for _, statement := range SchemaStatements() {
			if _, err := db.Exec(statement); err != nil {
				t.Fatalf("apply standalone Core task SQL %q: %v", statement, err)
			}
		}
		for _, statement := range MigrationV14Statements() {
			if _, err := db.Exec(statement); err != nil {
				t.Fatalf("apply standalone Core task migration %q: %v", statement, err)
			}
		}
	}
	store, err := New(db, Options{LeaseTTL: DefaultLeaseTTL, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	return testDB{db: db, store: store, now: now}
}

func defaultRequest() EnqueueRequest {
	return EnqueueRequest{NodeID: testNodeID, IdempotencyKey: "restart-001", Intent: Intent{Action: ActionRestart, ContainerID: strings.Repeat("a", 64)}, ActorID: sql.NullInt64{Int64: 1, Valid: true}, RemoteAddr: "192.0.2.45"}
}

func testConnection(journal string) AgentConnection {
	return AgentConnection{NodeID: testNodeID, ConnectionGeneration: 7, JournalID: journal}
}

func bindAgentReport(task Task, connection AgentConnection, report AgentTask) AgentTask {
	report.TaskID = task.TaskID
	report.NodeID = task.NodeID
	report.JournalID = connection.JournalID
	report.TargetID = task.Intent.ContainerID
	report.IdempotencyKey = task.IdempotencyKey
	report.RequestDigest = task.RequestDigest
	return report
}

func TestEnqueuePersistsIntentAuditAndClaimBeforeDelivery(t *testing.T) {
	fixture := openTestDB(t, "", time.Now().UTC())
	request := defaultRequest()
	accepted, err := fixture.store.Enqueue(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if !accepted.Created || accepted.Task.Status != taskstate.Queued || accepted.Task.DeliveryState != "ready" {
		t.Fatalf("unexpected acceptance: %+v", accepted)
	}
	var taskCount, auditCount, claimCount int
	if err := fixture.db.QueryRow(`SELECT count(*) FROM core_tasks`).Scan(&taskCount); err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.QueryRow(`SELECT count(*) FROM core_task_audit_events WHERE task_id=? AND event='accepted'`, accepted.Task.TaskID).Scan(&auditCount); err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.QueryRow(`SELECT count(*) FROM core_task_resource_claims WHERE task_id=?`, accepted.Task.TaskID).Scan(&claimCount); err != nil {
		t.Fatal(err)
	}
	if taskCount != 1 || auditCount != 1 || claimCount != 1 {
		t.Fatalf("atomic acceptance rows task/audit/claim=%d/%d/%d", taskCount, auditCount, claimCount)
	}
	connection := testConnection(strings.Repeat("a", 64))
	if _, _, err := fixture.store.ClaimNext(context.Background(), connection, request.ActorID, request.RemoteAddr); !errors.Is(err, ErrJournalNotObserved) {
		t.Fatalf("task was deliverable without an observed Agent journal: %v", err)
	}
	if _, err := fixture.store.ObserveAgentConnection(context.Background(), connection); err != nil {
		t.Fatal(err)
	}
	delivered, ok, err := fixture.store.ClaimNext(context.Background(), connection, request.ActorID, request.RemoteAddr)
	if err != nil || !ok {
		t.Fatalf("claim after journal handshake: ok=%t err=%v", ok, err)
	}
	if delivered.TaskID != accepted.Task.TaskID || delivered.DeliveryState != "sent" || delivered.DispatchJournalID != connection.JournalID {
		t.Fatalf("wrong delivered task: %+v", delivered)
	}
	if _, ok, err := fixture.store.ClaimNext(context.Background(), connection, request.ActorID, request.RemoteAddr); err != nil || ok {
		t.Fatalf("sent task was automatically replayed: ok=%t err=%v", ok, err)
	}
}

func TestRegistryAuthRequirementIsIdempotentAndFailsBeforeDelivery(t *testing.T) {
	fixture := openTestDB(t, "", time.Now().UTC())
	intent := Intent{Action: ActionImagePull, ImageReference: "registry.example/private:v1"}
	intent.ContainerID = protocol.ImageTargetKey("pull:" + intent.ImageReference)
	request := EnqueueRequest{NodeID: testNodeID, IdempotencyKey: "private-pull-auth", Intent: intent, RegistryAuthRequired: true}
	accepted, err := fixture.store.Enqueue(context.Background(), request)
	if err != nil || !accepted.Created || !accepted.Task.RegistryAuthRequired {
		t.Fatalf("authenticated image pull acceptance = %+v, err=%v", accepted, err)
	}
	changed := request
	changed.RegistryAuthRequired = false
	if _, err := fixture.store.Enqueue(context.Background(), changed); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("same idempotency key with changed credential presence returned %v, want conflict", err)
	}
	connection := testConnection(strings.Repeat("8", 64))
	if _, err := fixture.store.ObserveAgentConnection(context.Background(), connection); err != nil {
		t.Fatal("observe Agent journal:", err)
	}
	resolved, claimed, err := fixture.store.ClaimNextWithRegistryAuth(context.Background(), connection, request.ActorID, request.RemoteAddr, nil)
	if err != nil || claimed {
		t.Fatalf("missing one-use credentials were dispatched: claimed=%t task=%+v err=%v", claimed, resolved, err)
	}
	if resolved.Status != taskstate.Failed || resolved.DeliveryState != "done" || resolved.Evidence.DeliveryCommitted ||
		!resolved.Evidence.FailureConfirmed || !resolved.Evidence.ActualResultConfirmed || resolved.Result.Code != ResultRegistryCredentialsUnavailable {
		t.Fatalf("missing credentials were not a confirmed pre-delivery failure: %+v", resolved)
	}
	var claims int
	if err := fixture.db.QueryRow(`SELECT count(*) FROM core_task_resource_claims WHERE task_id=?`, accepted.Task.TaskID).Scan(&claims); err != nil || claims != 0 {
		t.Fatalf("credential-unavailable task retained %d resource claims, err=%v", claims, err)
	}
	var resolvedAudits int
	if err := fixture.db.QueryRow(`SELECT count(*) FROM core_task_audit_events WHERE task_id=? AND event='task_resolved' AND from_status='queued' AND to_status='failed'`, accepted.Task.TaskID).Scan(&resolvedAudits); err != nil || resolvedAudits != 1 {
		t.Fatalf("credential-unavailable task resolution audit count=%d err=%v", resolvedAudits, err)
	}

	publicIntent := Intent{Action: ActionImagePull, ImageReference: "registry.example/public:v1"}
	publicIntent.ContainerID = protocol.ImageTargetKey("pull:" + publicIntent.ImageReference)
	public, err := fixture.store.Enqueue(context.Background(), EnqueueRequest{NodeID: testNodeID, IdempotencyKey: "public-pull-no-auth", Intent: publicIntent})
	if err != nil {
		t.Fatal("enqueue public unauthenticated image pull:", err)
	}
	delivered, claimed, err := fixture.store.ClaimNext(context.Background(), connection, sql.NullInt64{}, "unknown")
	if err != nil || !claimed || delivered.TaskID != public.Task.TaskID || delivered.RegistryAuthRequired || delivered.DeliveryState != "sent" {
		t.Fatalf("public image pull without credentials did not dispatch: claimed=%t task=%+v err=%v", claimed, delivered, err)
	}
}

func TestCancelUndeliveredTaskIsIdempotentAndNeverCancelsSentWork(t *testing.T) {
	fixture := openTestDB(t, "", time.Now().UTC())
	request := defaultRequest()
	request.TaskID = "task-cancel-before-delivery"
	accepted, err := fixture.store.Enqueue(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	canceled, err := fixture.store.CancelUndelivered(context.Background(), testNodeID, accepted.Task.TaskID, request.ActorID, request.RemoteAddr)
	if err != nil {
		t.Fatal("cancel queued task before delivery:", err)
	}
	if canceled.Status != taskstate.Canceled || canceled.DeliveryState != "done" || canceled.Evidence.DeliveryCommitted ||
		!canceled.Evidence.CancellationConfirmed || !canceled.Evidence.ActualResultConfirmed ||
		canceled.Result.Code != ResultCanceled || canceled.Result.ObservedState != "not_dispatched" {
		t.Fatalf("safe pre-delivery cancellation lacks exact proof: %+v", canceled)
	}
	repeated, err := fixture.store.CancelUndelivered(context.Background(), testNodeID, accepted.Task.TaskID, request.ActorID, request.RemoteAddr)
	if err != nil || repeated.Status != taskstate.Canceled {
		t.Fatalf("identical cancellation retry was not idempotent: task=%+v err=%v", repeated, err)
	}
	var cancelAudits, claims int
	if err := fixture.db.QueryRow(`SELECT count(*) FROM core_task_audit_events WHERE task_id=? AND event='task_resolved'`, accepted.Task.TaskID).Scan(&cancelAudits); err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.QueryRow(`SELECT count(*) FROM core_task_resource_claims WHERE task_id=?`, accepted.Task.TaskID).Scan(&claims); err != nil {
		t.Fatal(err)
	}
	if cancelAudits != 1 || claims != 0 {
		t.Fatalf("cancellation audit/claim result=%d/%d, want one event and released claim", cancelAudits, claims)
	}

	sentRequest := defaultRequest()
	sentRequest.TaskID = "task-cancel-after-delivery"
	sentRequest.IdempotencyKey = "restart-cancel-sent"
	sentRequest.Intent.ContainerID = strings.Repeat("b", 64)
	sent, err := fixture.store.Enqueue(context.Background(), sentRequest)
	if err != nil {
		t.Fatal(err)
	}
	connection := testConnection(strings.Repeat("9", 64))
	if _, err := fixture.store.ObserveAgentConnection(context.Background(), connection); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := fixture.store.ClaimNext(context.Background(), connection, sql.NullInt64{}, "unknown"); err != nil || !ok {
		t.Fatalf("durably deliver second task before cancel test: ok=%t err=%v", ok, err)
	}
	if _, err := fixture.store.CancelUndelivered(context.Background(), testNodeID, sent.Task.TaskID, sentRequest.ActorID, sentRequest.RemoteAddr); !errors.Is(err, ErrNotDelivered) {
		t.Fatalf("sent Agent task was canceled without termination evidence: %v", err)
	}
	unchanged, err := fixture.store.Get(context.Background(), testNodeID, sent.Task.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if unchanged.Status != taskstate.Queued || unchanged.DeliveryState != "sent" || !unchanged.Evidence.DeliveryCommitted {
		t.Fatalf("rejected cancellation changed a sent task: %+v", unchanged)
	}
}

func TestAuditEventsAreNodeScopedTypedAndBounded(t *testing.T) {
	fixture := openTestDB(t, "", time.Now().UTC())
	accepted, err := fixture.store.Enqueue(context.Background(), defaultRequest())
	if err != nil {
		t.Fatal(err)
	}
	events, err := fixture.store.AuditEvents(context.Background(), testNodeID, accepted.Task.TaskID)
	if err != nil {
		t.Fatal("read task audit events:", err)
	}
	if len(events) != 1 || events[0].Event != "accepted" || events[0].ToStatus.String != string(taskstate.Queued) ||
		!events[0].ActorID.Valid || events[0].ActorID.Int64 != 1 || events[0].RemoteAddr != "192.0.2.45" || events[0].OccurredAt.IsZero() {
		t.Fatalf("unexpected typed audit history: %+v", events)
	}
	if _, err := fixture.store.AuditEvents(context.Background(), "00000000-0000-4000-8000-000000000001", accepted.Task.TaskID); !errors.Is(err, ErrTaskNotFound) {
		t.Fatalf("another node could read task audit history: %v", err)
	}
	if _, err := fixture.store.AuditEvents(context.Background(), testNodeID, "missing-task"); !errors.Is(err, ErrTaskNotFound) {
		t.Fatalf("missing task audit lookup returned %v", err)
	}
}

func TestReconciliationCandidatesRequireAgentKnownUnknownOnCurrentJournal(t *testing.T) {
	fixture := openTestDB(t, "", time.Now().UTC())
	connection := testConnection(strings.Repeat("a", 64))
	if _, err := fixture.store.ObserveAgentConnection(context.Background(), connection); err != nil {
		t.Fatal(err)
	}
	firstRequest := defaultRequest()
	firstRequest.TaskID = "task-agent-unknown"
	first, err := fixture.store.Enqueue(context.Background(), firstRequest)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok, err := fixture.store.ClaimNext(context.Background(), connection, sql.NullInt64{}, "unknown"); err != nil || !ok {
		t.Fatalf("claim first task: ok=%t err=%v", ok, err)
	}
	running := bindAgentReport(first.Task, connection, AgentTask{Status: taskstate.Running,
		Evidence: Evidence{ExecutionAttempted: true}, Progress: Progress{Phase: PhaseExecuting}})
	if _, err := fixture.store.ReconcileAgentJournal(context.Background(), connection, []AgentTask{running}, false); err != nil {
		t.Fatalf("persist Agent running report: %v", err)
	}
	unknown := bindAgentReport(first.Task, connection, AgentTask{Status: taskstate.Unknown,
		Evidence: Evidence{ExecutionAttempted: true}, Progress: Progress{Phase: PhaseReconciling}, Result: Result{Code: ResultUncertain}})
	if _, err := fixture.store.ReconcileAgentJournal(context.Background(), connection, []AgentTask{unknown}, false); err != nil {
		t.Fatalf("persist Agent unknown report: %v", err)
	}

	secondRequest := defaultRequest()
	secondRequest.TaskID = "task-journal-absence"
	secondRequest.IdempotencyKey = "restart-absent"
	secondRequest.Intent.ContainerID = strings.Repeat("b", 64)
	second, err := fixture.store.Enqueue(context.Background(), secondRequest)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok, err := fixture.store.ClaimNext(context.Background(), connection, sql.NullInt64{}, "unknown"); err != nil || !ok {
		t.Fatalf("claim second task: ok=%t err=%v", ok, err)
	}
	if _, err := fixture.store.ReconcileAgentJournal(context.Background(), connection, []AgentTask{unknown}, true); err != nil {
		t.Fatalf("reconcile complete snapshot with one known Agent unknown: %v", err)
	}
	candidates, err := fixture.store.ReconciliationCandidates(context.Background(), connection, 10)
	if err != nil {
		t.Fatal("list same-journal reconciliation candidates:", err)
	}
	if len(candidates) != 1 || candidates[0].TaskID != first.Task.TaskID || candidates[0].Status != taskstate.Unknown || candidates[0].ReconciliationRequired {
		t.Fatalf("candidate query included absent history or omitted Agent-known unknown: %+v", candidates)
	}
	absent, err := fixture.store.Get(context.Background(), testNodeID, second.Task.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if absent.Status != taskstate.Unknown || !absent.ReconciliationRequired {
		t.Fatalf("missing journal history did not remain gated unknown: %+v", absent)
	}
	wrongJournal := connection
	wrongJournal.JournalID = strings.Repeat("c", 64)
	if _, err := fixture.store.ReconciliationCandidates(context.Background(), wrongJournal, 10); !errors.Is(err, ErrJournalChanged) {
		t.Fatalf("stale journal received reconciliation candidates: %v", err)
	}
}

func TestAgentReportIdentityMustMatchStoredTaskInsideSQLiteTransaction(t *testing.T) {
	fixture := openTestDB(t, "", time.Now().UTC())
	accepted, err := fixture.store.Enqueue(context.Background(), defaultRequest())
	if err != nil {
		t.Fatal(err)
	}
	connection := testConnection(strings.Repeat("a", 64))
	if _, err := fixture.store.ObserveAgentConnection(context.Background(), connection); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := fixture.store.ClaimNext(context.Background(), connection, sql.NullInt64{}, "unknown"); err != nil || !ok {
		t.Fatalf("claim: %t %v", ok, err)
	}
	base := bindAgentReport(accepted.Task, connection, AgentTask{Status: taskstate.Running,
		Evidence: Evidence{ExecutionAttempted: true}, Progress: Progress{Phase: PhaseExecuting}})
	tests := []struct {
		name   string
		mutate func(*AgentTask)
	}{
		{"node", func(report *AgentTask) { report.NodeID = "other-node" }},
		{"journal", func(report *AgentTask) { report.JournalID = strings.Repeat("b", 64) }},
		{"target", func(report *AgentTask) { report.TargetID = strings.Repeat("c", 64) }},
		{"idempotency-key", func(report *AgentTask) { report.IdempotencyKey = "other-key" }},
		{"digest", func(report *AgentTask) { report.RequestDigest[0] ^= 0xff }},
		{"agent-cannot-claim-core-delivery", func(report *AgentTask) { report.Evidence.DeliveryCommitted = true }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			report := base
			test.mutate(&report)
			if _, err := fixture.store.ReconcileAgentJournal(context.Background(), connection, []AgentTask{report}, false); !errors.Is(err, ErrTaskStateConflict) {
				t.Fatalf("mismatched identity was accepted: %v", err)
			}
			task, err := fixture.store.Get(context.Background(), testNodeID, accepted.Task.TaskID)
			if err != nil {
				t.Fatal(err)
			}
			if task.Status != taskstate.Queued || task.Evidence.ExecutionAttempted {
				t.Fatalf("rejected report mutated durable task: %+v", task)
			}
			var auditCount, claimCount int
			if err := fixture.db.QueryRow(`SELECT count(*) FROM core_task_audit_events WHERE task_id=? AND event='agent_status'`, task.TaskID).Scan(&auditCount); err != nil {
				t.Fatal(err)
			}
			if err := fixture.db.QueryRow(`SELECT count(*) FROM core_task_resource_claims WHERE task_id=?`, task.TaskID).Scan(&claimCount); err != nil {
				t.Fatal(err)
			}
			if auditCount != 0 || claimCount != 1 {
				t.Fatalf("rejected report changed audit/claim: %d/%d", auditCount, claimCount)
			}
		})
	}
	if _, err := fixture.store.ReconcileAgentJournal(context.Background(), connection, []AgentTask{base}, false); err != nil {
		t.Fatalf("valid bound report rejected after identity failures: %v", err)
	}
}

func TestAcceptanceAuditFailureRollsBackTaskAndClaim(t *testing.T) {
	fixture := openTestDB(t, "", time.Now().UTC())
	if _, err := fixture.db.Exec(`CREATE TRIGGER reject_task_acceptance BEFORE INSERT ON core_task_audit_events
		WHEN NEW.event='accepted' BEGIN SELECT RAISE(ABORT, 'injected audit failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.store.Enqueue(context.Background(), defaultRequest()); err == nil {
		t.Fatal("acceptance succeeded despite its audit transaction failing")
	}
	var tasks, audits, claims int
	if err := fixture.db.QueryRow(`SELECT count(*) FROM core_tasks`).Scan(&tasks); err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.QueryRow(`SELECT count(*) FROM core_task_audit_events`).Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.QueryRow(`SELECT count(*) FROM core_task_resource_claims`).Scan(&claims); err != nil {
		t.Fatal(err)
	}
	if tasks != 0 || audits != 0 || claims != 0 {
		t.Fatalf("audit failure left partial task/audit/claim rows=%d/%d/%d", tasks, audits, claims)
	}
}

func TestDeliveryAuditFailureRollsBackBeforeTaskCanBeSent(t *testing.T) {
	fixture := openTestDB(t, "", time.Now().UTC())
	accepted, err := fixture.store.Enqueue(context.Background(), defaultRequest())
	if err != nil {
		t.Fatal(err)
	}
	connection := testConnection(strings.Repeat("4", 64))
	if _, err := fixture.store.ObserveAgentConnection(context.Background(), connection); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.db.Exec(`CREATE TRIGGER reject_delivery_audit BEFORE INSERT ON core_task_audit_events
		WHEN NEW.event='delivery_claimed' BEGIN SELECT RAISE(ABORT, 'injected delivery audit failure'); END`); err != nil {
		t.Fatal(err)
	}
	if task, ok, err := fixture.store.ClaimNext(context.Background(), connection, sql.NullInt64{}, "unknown"); err == nil || ok {
		t.Fatalf("delivery was returned before its transaction committed: task=%+v ok=%t err=%v", task, ok, err)
	}
	task, err := fixture.store.Get(context.Background(), testNodeID, accepted.Task.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if task.Status != taskstate.Queued || task.DeliveryState != "ready" || task.Evidence.DeliveryCommitted || task.DispatchJournalID != "" {
		t.Fatalf("failed delivery transaction was left as sent: %+v", task)
	}
	var claims int
	if err := fixture.db.QueryRow(`SELECT count(*) FROM core_task_resource_claims WHERE task_id=?`, task.TaskID).Scan(&claims); err != nil || claims != 1 {
		t.Fatalf("failed delivery transaction lost resource claim: count=%d err=%v", claims, err)
	}
}

func TestIdempotencyAndCrossIdentityConflicts(t *testing.T) {
	fixture := openTestDB(t, "", time.Now().UTC())
	firstRequest := defaultRequest()
	firstRequest.TaskID = "task-original"
	first, err := fixture.store.Enqueue(context.Background(), firstRequest)
	if err != nil {
		t.Fatal(err)
	}

	retry := firstRequest
	retry.TaskID = "task-retry-generated"
	retry.RemoteAddr = "198.51.100.7"
	retried, err := fixture.store.Enqueue(context.Background(), retry)
	if err != nil {
		t.Fatal(err)
	}
	if retried.Created || retried.Task.TaskID != first.Task.TaskID {
		t.Fatalf("same key/request did not return original task: %+v", retried)
	}

	changed := retry
	changed.TaskID = "task-changed"
	changed.Intent.Action = ActionStop
	if _, err := fixture.store.Enqueue(context.Background(), changed); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("changed request reused key: %v", err)
	}

	second := defaultRequest()
	second.TaskID = "task-second"
	second.IdempotencyKey = "restart-002"
	second.Intent.Action = ActionStop
	if _, err := fixture.store.Enqueue(context.Background(), second); !errors.Is(err, ErrResourceBusy) {
		t.Fatalf("second task bypassed active resource claim: %v", err)
	}

	cross := second
	cross.TaskID = first.Task.TaskID
	cross.IdempotencyKey = "restart-003"
	if _, err := fixture.store.Enqueue(context.Background(), cross); !errors.Is(err, ErrTaskIDConflict) {
		t.Fatalf("task ID cross collision accepted: %v", err)
	}

	var count int
	if err := fixture.db.QueryRow(`SELECT count(*) FROM core_tasks`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("conflicting submissions wrote %d task rows", count)
	}
}

func TestEnqueueGateAllowsExistingRetryButRejectsNewWhileUnsynced(t *testing.T) {
	fixture := openTestDB(t, "", time.Now().UTC())
	request := defaultRequest()
	request.TaskID = "task-gated-original"
	first, err := fixture.store.Enqueue(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.db.Exec(`UPDATE nodes SET status='offline' WHERE id=?`, testNodeID); err != nil {
		t.Fatal(err)
	}

	gateCalls := 0
	unsynced := func(context.Context) (bool, func(), error) {
		gateCalls++
		return false, nil, ErrJournalNotObserved
	}
	retry := request
	retry.TaskID = "task-gated-retry"
	retried, err := fixture.store.EnqueueWithGate(context.Background(), retry, unsynced)
	if err != nil {
		t.Fatalf("identical retry failed while node/Agent was offline and unsynced: %v", err)
	}
	if retried.Created || retried.Task.TaskID != first.Task.TaskID {
		t.Fatalf("retry did not return the existing durable task: %+v", retried)
	}
	if gateCalls != 0 {
		t.Fatalf("readiness gate ran for existing idempotent retry %d times", gateCalls)
	}

	changed := retry
	changed.TaskID = "task-gated-changed"
	changed.Intent.Action = ActionStop
	if _, err := fixture.store.EnqueueWithGate(context.Background(), changed, unsynced); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("changed request under existing key did not conflict before readiness: %v", err)
	}
	if gateCalls != 0 {
		t.Fatalf("readiness gate ran for idempotency conflict %d times", gateCalls)
	}

	newRequest := defaultRequest()
	newRequest.TaskID = "task-gated-new"
	newRequest.IdempotencyKey = "restart-new-unsynced"
	newRequest.Intent.ContainerID = strings.Repeat("b", 64)
	if _, err := fixture.store.EnqueueWithGate(context.Background(), newRequest, unsynced); !errors.Is(err, ErrJournalNotObserved) {
		t.Fatalf("new request was not rejected by unsynced readiness gate: %v", err)
	}
	if gateCalls != 1 {
		t.Fatalf("new request readiness gate calls=%d, want 1", gateCalls)
	}
	var taskCount, auditCount, claimCount int
	if err := fixture.db.QueryRow(`SELECT count(*) FROM core_tasks`).Scan(&taskCount); err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.QueryRow(`SELECT count(*) FROM core_task_audit_events`).Scan(&auditCount); err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.QueryRow(`SELECT count(*) FROM core_task_resource_claims`).Scan(&claimCount); err != nil {
		t.Fatal(err)
	}
	if taskCount != 1 || auditCount != 1 || claimCount != 1 {
		t.Fatalf("unsynced new request persisted rows task/audit/claim=%d/%d/%d", taskCount, auditCount, claimCount)
	}
}

func TestEnqueueGateReleaseRunsAfterCommitOrRollback(t *testing.T) {
	fixture := openTestDB(t, "", time.Now().UTC())
	request := defaultRequest()
	releaseObservedRows := -1
	gate := func(context.Context) (bool, func(), error) {
		return false, func() {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if err := fixture.db.QueryRowContext(ctx, `SELECT count(*) FROM core_tasks`).Scan(&releaseObservedRows); err != nil {
				t.Errorf("read task rows while releasing gate: %v", err)
			}
		}, nil
	}
	if _, err := fixture.store.EnqueueWithGate(context.Background(), request, gate); err != nil {
		t.Fatal(err)
	}
	if releaseObservedRows != 1 {
		t.Fatalf("successful gate released before committed task was visible: rows=%d", releaseObservedRows)
	}

	releaseObservedRows = -1
	failedGate := func(context.Context) (bool, func(), error) {
		return false, func() {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if err := fixture.db.QueryRowContext(ctx, `SELECT count(*) FROM core_tasks`).Scan(&releaseObservedRows); err != nil {
				t.Errorf("read task rows while releasing rejected gate: %v", err)
			}
		}, ErrJournalNotObserved
	}
	newRequest := defaultRequest()
	newRequest.TaskID = "task-gate-rollback"
	newRequest.IdempotencyKey = "restart-gate-rollback"
	newRequest.Intent.ContainerID = strings.Repeat("b", 64)
	if _, err := fixture.store.EnqueueWithGate(context.Background(), newRequest, failedGate); !errors.Is(err, ErrJournalNotObserved) {
		t.Fatalf("failed readiness gate unexpectedly accepted: %v", err)
	}
	if releaseObservedRows != 1 {
		t.Fatalf("failed gate released before transaction rollback completed: rows=%d", releaseObservedRows)
	}
}

func TestConcurrentIdempotentRequestsCreateOneTaskAndAudit(t *testing.T) {
	fixture := openTestDB(t, "", time.Now().UTC())
	secondStore, err := New(fixture.db, Options{LeaseTTL: DefaultLeaseTTL, Now: func() time.Time { return fixture.now }})
	if err != nil {
		t.Fatal(err)
	}
	const callers = 16
	var group sync.WaitGroup
	group.Add(callers)
	results := make(chan EnqueueResult, callers)
	errs := make(chan error, callers)
	for i := 0; i < callers; i++ {
		go func(index int) {
			defer group.Done()
			request := defaultRequest()
			request.TaskID = fmt.Sprintf("retry-%02d", index)
			store := fixture.store
			if index%2 != 0 {
				store = secondStore
			}
			result, err := store.Enqueue(context.Background(), request)
			if err != nil {
				errs <- err
				return
			}
			results <- result
		}(i)
	}
	group.Wait()
	close(results)
	close(errs)
	for err := range errs {
		t.Errorf("concurrent duplicate: %v", err)
	}
	var taskID string
	created := 0
	for result := range results {
		if taskID == "" {
			taskID = result.Task.TaskID
		}
		if result.Task.TaskID != taskID {
			t.Errorf("duplicate key returned different IDs %q and %q", taskID, result.Task.TaskID)
		}
		if result.Created {
			created++
		}
	}
	var tasks, audits, claims int
	_ = fixture.db.QueryRow(`SELECT count(*) FROM core_tasks`).Scan(&tasks)
	_ = fixture.db.QueryRow(`SELECT count(*) FROM core_task_audit_events WHERE event='accepted'`).Scan(&audits)
	_ = fixture.db.QueryRow(`SELECT count(*) FROM core_task_resource_claims`).Scan(&claims)
	if tasks != 1 || audits != 1 || claims != 1 || created != 1 {
		t.Fatalf("created/task/audit/claim=%d/%d/%d/%d", created, tasks, audits, claims)
	}
}

func TestConcurrentDifferentRequestsCannotClaimSameContainer(t *testing.T) {
	fixture := openTestDB(t, "", time.Now().UTC())
	secondStore, err := New(fixture.db, Options{LeaseTTL: DefaultLeaseTTL, Now: func() time.Time { return fixture.now }})
	if err != nil {
		t.Fatal(err)
	}
	requests := []EnqueueRequest{defaultRequest(), defaultRequest()}
	requests[0].TaskID, requests[0].IdempotencyKey = "resource-task-a", "resource-key-a"
	requests[0].Intent.Action = ActionStop
	requests[1].TaskID, requests[1].IdempotencyKey = "resource-task-b", "resource-key-b"
	requests[1].Intent.Action = ActionRestart
	start := make(chan struct{})
	results := make(chan error, len(requests))
	var group sync.WaitGroup
	group.Add(len(requests))
	for index, request := range requests {
		request := request
		go func(index int) {
			defer group.Done()
			<-start
			store := fixture.store
			if index%2 != 0 {
				store = secondStore
			}
			_, err := store.Enqueue(context.Background(), request)
			results <- err
		}(index)
	}
	close(start)
	group.Wait()
	close(results)
	accepted, busy := 0, 0
	for err := range results {
		switch {
		case err == nil:
			accepted++
		case errors.Is(err, ErrResourceBusy):
			busy++
		default:
			t.Errorf("unexpected concurrent resource result: %v", err)
		}
	}
	var tasks, claims, audits int
	_ = fixture.db.QueryRow(`SELECT count(*) FROM core_tasks`).Scan(&tasks)
	_ = fixture.db.QueryRow(`SELECT count(*) FROM core_task_resource_claims`).Scan(&claims)
	_ = fixture.db.QueryRow(`SELECT count(*) FROM core_task_audit_events WHERE event='accepted'`).Scan(&audits)
	if accepted != 1 || busy != 1 || tasks != 1 || claims != 1 || audits != 1 {
		t.Fatalf("accepted/busy/tasks/claims/audits=%d/%d/%d/%d/%d", accepted, busy, tasks, claims, audits)
	}
}

func TestOfflineExpiredOrFutureLeaseRejectsWithoutQueueing(t *testing.T) {
	base := time.Now().UTC()
	for _, tc := range []struct {
		name     string
		status   string
		lastSeen time.Time
	}{
		{name: "offline", status: "offline", lastSeen: base},
		{name: "expired", status: "online", lastSeen: base.Add(-DefaultLeaseTTL)},
		{name: "future", status: "online", lastSeen: base.Add(time.Second)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := openTestDB(t, "", base)
			if _, err := fixture.db.Exec(`UPDATE nodes SET status=?,last_seen_at=? WHERE id=?`, tc.status, tc.lastSeen.UnixNano(), testNodeID); err != nil {
				t.Fatal(err)
			}
			if _, err := fixture.store.Enqueue(context.Background(), defaultRequest()); !errors.Is(err, ErrNodeOffline) {
				t.Fatalf("offline action accepted: %v", err)
			}
			var count int
			if err := fixture.db.QueryRow(`SELECT count(*) FROM core_tasks`).Scan(&count); err != nil || count != 0 {
				t.Fatalf("offline request persisted: count=%d err=%v", count, err)
			}
		})
	}
}

func TestUndeliveredIntentIsCanceledWhenAgentConnectionGenerationChanges(t *testing.T) {
	fixture := openTestDB(t, "", time.Now().UTC())
	accepted, err := fixture.store.Enqueue(context.Background(), defaultRequest())
	if err != nil {
		t.Fatal(err)
	}
	authIntent := Intent{Action: ActionImagePull, ImageReference: "registry.example/stale-auth:v1"}
	authIntent.ContainerID = protocol.ImageTargetKey("pull:" + authIntent.ImageReference)
	authAccepted, err := fixture.store.Enqueue(context.Background(), EnqueueRequest{NodeID: testNodeID, IdempotencyKey: "stale-auth-pull",
		Intent: authIntent, RegistryAuthRequired: true})
	if err != nil {
		t.Fatal("enqueue stale authenticated image pull:", err)
	}
	oldConnection := testConnection(strings.Repeat("3", 64))
	if _, err := fixture.store.ObserveAgentConnection(context.Background(), oldConnection); err != nil {
		t.Fatal(err)
	}
	newConnection := oldConnection
	newConnection.ConnectionGeneration++
	if _, err := fixture.db.Exec(`UPDATE nodes SET connection_generation=?,last_seen_at=? WHERE id=?`, newConnection.ConnectionGeneration, fixture.now.UnixNano(), testNodeID); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.store.ObserveAgentConnection(context.Background(), newConnection); err != nil {
		t.Fatal(err)
	}
	task, err := fixture.store.Get(context.Background(), testNodeID, accepted.Task.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if task.Status != taskstate.Canceled || task.DeliveryState != "done" || task.Evidence.DeliveryCommitted || task.Result.ObservedState != "not_dispatched" {
		t.Fatalf("stale undelivered task was left for a later automatic run: %+v", task)
	}
	staleAuthTask, err := fixture.store.Get(context.Background(), testNodeID, authAccepted.Task.TaskID)
	if err != nil || staleAuthTask.Status != taskstate.Canceled || staleAuthTask.Result.ObservedState != "not_dispatched" {
		t.Fatalf("auth-required task unexpectedly bypassed the existing stale-generation cancellation: task=%+v err=%v", staleAuthTask, err)
	}
	var claims int
	if err := fixture.db.QueryRow(`SELECT count(*) FROM core_task_resource_claims WHERE task_id=?`, task.TaskID).Scan(&claims); err != nil || claims != 0 {
		t.Fatalf("confirmed never-dispatched task kept its resource claim: %d err=%v", claims, err)
	}
	if _, ok, err := fixture.store.ClaimNext(context.Background(), newConnection, sql.NullInt64{}, "unknown"); err != nil || ok {
		t.Fatalf("stale task was sent on the new connection: ok=%t err=%v", ok, err)
	}
}

func TestOnlyProofVerifiedTerminalResultReleasesClaim(t *testing.T) {
	fixture := openTestDB(t, "", time.Now().UTC())
	accepted, err := fixture.store.Enqueue(context.Background(), defaultRequest())
	if err != nil {
		t.Fatal(err)
	}
	connection := testConnection(strings.Repeat("b", 64))
	if _, err := fixture.store.ObserveAgentConnection(context.Background(), connection); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := fixture.store.ClaimNext(context.Background(), connection, sql.NullInt64{}, "unknown"); err != nil || !ok {
		t.Fatalf("claim: %t %v", ok, err)
	}
	if _, err := fixture.store.ReconcileAgentJournal(context.Background(), connection, []AgentTask{bindAgentReport(accepted.Task, connection, AgentTask{
		Status: taskstate.Running, Progress: Progress{Phase: PhaseExecuting}})}, false); !errors.Is(err, taskstate.ErrInvalidStatus) {
		t.Fatalf("running report without Agent execution evidence accepted: %v", err)
	}
	unchanged, err := fixture.store.Get(context.Background(), testNodeID, accepted.Task.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if unchanged.Status != taskstate.Queued || unchanged.Evidence.ExecutionAttempted {
		t.Fatalf("failed running report partially changed durable state: %+v", unchanged)
	}
	started := bindAgentReport(accepted.Task, connection, AgentTask{Status: taskstate.Running, Evidence: Evidence{ExecutionAttempted: true}, Progress: Progress{Phase: PhaseExecuting}})
	if _, err := fixture.store.ReconcileAgentJournal(context.Background(), connection, []AgentTask{started}, false); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.store.ReconcileAgentJournal(context.Background(), connection, []AgentTask{bindAgentReport(accepted.Task, connection, AgentTask{Status: taskstate.Succeeded,
		Evidence: Evidence{ExecutionAttempted: true, ExecutionCompleted: true}, Progress: Progress{Phase: PhaseVerifying, Completed: 1, Total: 1}, Result: Result{Code: ResultVerified, ObservedState: "running"}})}, false); !errors.Is(err, taskstate.ErrInvalidStatus) {
		t.Fatalf("unverified operation became successful: %v", err)
	}
	if _, err := fixture.store.Get(context.Background(), testNodeID, accepted.Task.TaskID); err != nil {
		t.Fatal(err)
	}
	var claimCount int
	if err := fixture.db.QueryRow(`SELECT count(*) FROM core_task_resource_claims WHERE task_id=?`, accepted.Task.TaskID).Scan(&claimCount); err != nil || claimCount != 1 {
		t.Fatalf("unverified result released claim: count=%d err=%v", claimCount, err)
	}
	finished := bindAgentReport(accepted.Task, connection, AgentTask{Status: taskstate.Succeeded,
		Evidence: Evidence{ExecutionAttempted: true, ExecutionCompleted: true, PostconditionVerified: true},
		Progress: Progress{Phase: PhaseVerifying, Completed: 1, Total: 1}, Result: Result{Code: ResultVerified, ObservedState: "running", ResourceRevision: "sha256.abc123"}})
	if _, err := fixture.store.ReconcileAgentJournal(context.Background(), connection, []AgentTask{finished}, false); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.store.ReconcileAgentJournal(context.Background(), connection, []AgentTask{finished}, false); err != nil {
		t.Fatalf("lost response terminal retry should be idempotent: %v", err)
	}
	var auditCount int
	if err := fixture.db.QueryRow(`SELECT count(*) FROM core_task_audit_events WHERE task_id=? AND event='agent_status' AND to_status='succeeded'`, accepted.Task.TaskID).Scan(&auditCount); err != nil || auditCount != 1 {
		t.Fatalf("terminal audit count=%d err=%v", auditCount, err)
	}
	if err := fixture.db.QueryRow(`SELECT count(*) FROM core_task_resource_claims WHERE task_id=?`, accepted.Task.TaskID).Scan(&claimCount); err != nil || claimCount != 0 {
		t.Fatalf("verified result kept claim: count=%d err=%v", claimCount, err)
	}
}

func TestVerifiedTerminalMayArriveBeforeRunningAckWithoutInventedEvidence(t *testing.T) {
	fixture := openTestDB(t, "", time.Now().UTC())
	accepted, err := fixture.store.Enqueue(context.Background(), defaultRequest())
	if err != nil {
		t.Fatal(err)
	}
	connection := testConnection(strings.Repeat("9", 64))
	if _, err := fixture.store.ObserveAgentConnection(context.Background(), connection); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := fixture.store.ClaimNext(context.Background(), connection, sql.NullInt64{}, "unknown"); err != nil || !ok {
		t.Fatalf("claim: %t %v", ok, err)
	}
	terminal := bindAgentReport(accepted.Task, connection, AgentTask{Status: taskstate.Succeeded,
		Evidence: Evidence{ExecutionAttempted: true, ExecutionCompleted: true, PostconditionVerified: true},
		Progress: Progress{Phase: PhaseVerifying, Completed: 1, Total: 1}, Result: Result{Code: ResultVerified, ObservedState: "running"}})
	if _, err := fixture.store.ReconcileAgentJournal(context.Background(), connection, []AgentTask{terminal}, false); err != nil {
		t.Fatal(err)
	}
	task, err := fixture.store.Get(context.Background(), testNodeID, accepted.Task.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if task.Status != taskstate.Succeeded || !task.Evidence.ExecutionAttempted || !task.Evidence.ExecutionCompleted || !task.Evidence.PostconditionVerified {
		t.Fatalf("verified terminal report was not committed: %+v", task)
	}
	var runningAudits, terminalAudits int
	if err := fixture.db.QueryRow(`SELECT count(*) FROM core_task_audit_events WHERE task_id=? AND from_status='queued' AND to_status='running'`, task.TaskID).Scan(&runningAudits); err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.QueryRow(`SELECT count(*) FROM core_task_audit_events WHERE task_id=? AND from_status='running' AND to_status='succeeded'`, task.TaskID).Scan(&terminalAudits); err != nil {
		t.Fatal(err)
	}
	if runningAudits != 1 || terminalAudits != 1 {
		t.Fatalf("lost running ACK audit chain queued/running->terminal=%d/%d", runningAudits, terminalAudits)
	}
}

func TestQueuedDeliveredTaskReportedUnknownUsesCoreDeliveryEvidenceOnly(t *testing.T) {
	fixture := openTestDB(t, "", time.Now().UTC())
	accepted, err := fixture.store.Enqueue(context.Background(), defaultRequest())
	if err != nil {
		t.Fatal(err)
	}
	connection := testConnection(strings.Repeat("8", 64))
	if _, err := fixture.store.ObserveAgentConnection(context.Background(), connection); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := fixture.store.ClaimNext(context.Background(), connection, sql.NullInt64{}, "unknown"); err != nil || !ok {
		t.Fatalf("claim: %t %v", ok, err)
	}
	report := bindAgentReport(accepted.Task, connection, AgentTask{Status: taskstate.Unknown,
		Progress: Progress{Phase: PhaseReconciling}, Result: Result{Code: ResultUncertain}})
	if _, err := fixture.store.ReconcileAgentJournal(context.Background(), connection, []AgentTask{report}, false); err != nil {
		t.Fatalf("durably dispatched queued task did not become unknown without invented execution evidence: %v", err)
	}
	task, err := fixture.store.Get(context.Background(), testNodeID, accepted.Task.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if task.Status != taskstate.Unknown || task.Evidence.ExecutionAttempted || !task.Evidence.DeliveryCommitted {
		t.Fatalf("unknown report did not preserve only Core delivery evidence: %+v", task)
	}
	var claims int
	if err := fixture.db.QueryRow(`SELECT count(*) FROM core_task_resource_claims WHERE task_id=?`, task.TaskID).Scan(&claims); err != nil || claims != 1 {
		t.Fatalf("unknown task claim was released: claims=%d err=%v", claims, err)
	}
}

func TestAgentQueuedJournalEntryIsAcknowledgedWithoutReplay(t *testing.T) {
	fixture := openTestDB(t, "", time.Now().UTC())
	accepted, err := fixture.store.Enqueue(context.Background(), defaultRequest())
	if err != nil {
		t.Fatal(err)
	}
	connection := testConnection(strings.Repeat("7", 64))
	if _, err := fixture.store.ObserveAgentConnection(context.Background(), connection); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := fixture.store.ClaimNext(context.Background(), connection, sql.NullInt64{}, "unknown"); err != nil || !ok {
		t.Fatalf("claim: %t %v", ok, err)
	}
	report := bindAgentReport(accepted.Task, connection, AgentTask{Status: taskstate.Queued, Progress: Progress{Phase: PhaseAccepted}})
	if _, err := fixture.store.ReconcileAgentJournal(context.Background(), connection, []AgentTask{report}, true); err != nil {
		t.Fatalf("Agent's durable queued entry was rejected: %v", err)
	}
	task, err := fixture.store.Get(context.Background(), testNodeID, accepted.Task.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if task.Status != taskstate.Queued || task.DeliveryState != "sent" || !task.Evidence.DeliveryCommitted || task.Evidence.ExecutionAttempted {
		t.Fatalf("queued Agent entry changed Core state or invented execution: %+v", task)
	}
	if _, ok, err := fixture.store.ClaimNext(context.Background(), connection, sql.NullInt64{}, "unknown"); err != nil || ok {
		t.Fatalf("Agent's durable queue entry caused a redelivery: ok=%t err=%v", ok, err)
	}
}

func TestNonterminalAgentReportsCannotPersistResultPayloads(t *testing.T) {
	fixture := openTestDB(t, "", time.Now().UTC())
	accepted, err := fixture.store.Enqueue(context.Background(), defaultRequest())
	if err != nil {
		t.Fatal(err)
	}
	connection := testConnection(strings.Repeat("6", 64))
	if _, err := fixture.store.ObserveAgentConnection(context.Background(), connection); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := fixture.store.ClaimNext(context.Background(), connection, sql.NullInt64{}, "unknown"); err != nil || !ok {
		t.Fatalf("claim: %t %v", ok, err)
	}
	secret := "token_DO_NOT_PERSIST_7e1811"
	maliciousRunning := bindAgentReport(accepted.Task, connection, AgentTask{Status: taskstate.Running,
		Evidence: Evidence{ExecutionAttempted: true}, Progress: Progress{Phase: PhaseExecuting, Completed: 1, Total: 2},
		Result: Result{Code: ResultCode(secret), ObservedState: secret, ResourceRevision: secret}})
	if _, err := fixture.store.ReconcileAgentJournal(context.Background(), connection, []AgentTask{maliciousRunning}, false); !errors.Is(err, ErrTaskStateConflict) {
		t.Fatalf("running report carried an arbitrary result payload: %v", err)
	}
	task, err := fixture.store.Get(context.Background(), testNodeID, accepted.Task.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if task.Status != taskstate.Queued || task.Evidence.ExecutionAttempted || task.Progress != (Progress{Phase: PhaseAccepted}) {
		t.Fatalf("rejected result payload partially changed queued task: %+v", task)
	}
	var agentAudits, secretRows int
	if err := fixture.db.QueryRow(`SELECT count(*) FROM core_task_audit_events WHERE task_id=? AND event='agent_status'`, task.TaskID).Scan(&agentAudits); err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.QueryRow(`SELECT count(*) FROM core_tasks WHERE instr(result_code,?)>0 OR instr(observed_state,?)>0 OR instr(resource_revision,?)>0`, secret, secret, secret).Scan(&secretRows); err != nil {
		t.Fatal(err)
	}
	if agentAudits != 0 || secretRows != 0 {
		t.Fatalf("rejected report wrote audit/result data: agentAudits=%d secretRows=%d", agentAudits, secretRows)
	}

	validRunning := bindAgentReport(accepted.Task, connection, AgentTask{Status: taskstate.Running,
		Evidence: Evidence{ExecutionAttempted: true}, Progress: Progress{Phase: PhaseExecuting, Completed: 1, Total: 2}})
	if _, err := fixture.store.ReconcileAgentJournal(context.Background(), connection, []AgentTask{validRunning}, false); err != nil {
		t.Fatalf("valid running progress rejected: %v", err)
	}
	maliciousUnknown := bindAgentReport(accepted.Task, connection, AgentTask{Status: taskstate.Unknown,
		Progress: Progress{Phase: PhaseReconciling}, Result: Result{Code: ResultUncertain, ObservedState: secret}})
	if _, err := fixture.store.ReconcileAgentJournal(context.Background(), connection, []AgentTask{maliciousUnknown}, false); !errors.Is(err, ErrTaskStateConflict) {
		t.Fatalf("unknown report carried terminal observations: %v", err)
	}
	task, err = fixture.store.Get(context.Background(), testNodeID, accepted.Task.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if task.Status != taskstate.Running || task.Progress != validRunning.Progress || task.Result.Code != "" {
		t.Fatalf("rejected unknown payload changed running task: %+v", task)
	}
	if err := fixture.db.QueryRow(`SELECT count(*) FROM core_tasks WHERE instr(result_code,?)>0 OR instr(observed_state,?)>0 OR instr(resource_revision,?)>0`, secret, secret, secret).Scan(&secretRows); err != nil {
		t.Fatal(err)
	}
	if secretRows != 0 {
		t.Fatalf("rejected unknown payload leaked secret into task result fields: %d", secretRows)
	}
	validProgress := bindAgentReport(accepted.Task, connection, AgentTask{Status: taskstate.Running,
		Evidence: Evidence{ExecutionAttempted: true}, Progress: Progress{Phase: PhaseExecuting, Completed: 2, Total: 2}})
	if _, err := fixture.store.ReconcileAgentJournal(context.Background(), connection, []AgentTask{validProgress}, false); err != nil {
		t.Fatalf("valid running progress update rejected: %v", err)
	}
	validUnknown := bindAgentReport(accepted.Task, connection, AgentTask{Status: taskstate.Unknown,
		Evidence: Evidence{ExecutionAttempted: true}, Progress: Progress{Phase: PhaseReconciling}, Result: Result{Code: ResultUncertain}})
	if _, err := fixture.store.ReconcileAgentJournal(context.Background(), connection, []AgentTask{validUnknown}, false); err != nil {
		t.Fatalf("contract-valid unknown report rejected: %v", err)
	}
	final, err := fixture.store.Get(context.Background(), testNodeID, accepted.Task.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if final.Status != taskstate.Unknown || final.Progress.Phase != PhaseReconciling {
		t.Fatalf("contract-valid progress/unknown report was not stored: %+v", final)
	}
}

func TestMissingSameJournalTaskHistoryNeverAutomaticallyReplays(t *testing.T) {
	fixture := openTestDB(t, "", time.Now().UTC())
	accepted, err := fixture.store.Enqueue(context.Background(), defaultRequest())
	if err != nil {
		t.Fatal(err)
	}
	connection := testConnection(strings.Repeat("c", 64))
	if _, err := fixture.store.ObserveAgentConnection(context.Background(), connection); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := fixture.store.ClaimNext(context.Background(), connection, sql.NullInt64{}, "unknown"); err != nil || !ok {
		t.Fatalf("claim: %t %v", ok, err)
	}
	incomplete, err := fixture.store.ReconcileAgentJournal(context.Background(), connection, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(incomplete.Pending) != 0 {
		t.Fatalf("incomplete snapshot was treated as authoritative: %+v", incomplete)
	}
	if _, ok, err := fixture.store.ClaimNext(context.Background(), connection, sql.NullInt64{}, "unknown"); err != nil || ok {
		t.Fatalf("incomplete snapshot replayed sent task: %t %v", ok, err)
	}
	complete, err := fixture.store.ReconcileAgentJournal(context.Background(), connection, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(complete.Pending) != 1 || complete.Pending[0] != accepted.Task.TaskID {
		t.Fatalf("complete but missing journal history was not held for reconciliation: %+v", complete)
	}
	if _, ok, err := fixture.store.ClaimNext(context.Background(), connection, sql.NullInt64{}, "unknown"); err != nil || ok {
		t.Fatalf("same JournalID absence replayed task: ok=%t err=%v", ok, err)
	}
	task, err := fixture.store.Get(context.Background(), testNodeID, accepted.Task.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if task.Status != taskstate.Unknown || !task.ReconciliationRequired || task.DeliveryState != "needs_reconciliation" || !task.Evidence.DeliveryCommitted || task.Evidence.ExecutionAttempted {
		t.Fatalf("missing task history did not remain blocked and durable: %+v", task)
	}
}

func TestChangedJournalBlocksAbsentTaskInferenceUntilVerifiedResolution(t *testing.T) {
	fixture := openTestDB(t, "", time.Now().UTC())
	accepted, err := fixture.store.Enqueue(context.Background(), defaultRequest())
	if err != nil {
		t.Fatal(err)
	}
	oldConnection := testConnection(strings.Repeat("d", 64))
	if _, err := fixture.store.ObserveAgentConnection(context.Background(), oldConnection); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := fixture.store.ClaimNext(context.Background(), oldConnection, sql.NullInt64{}, "unknown"); err != nil || !ok {
		t.Fatalf("claim: %t %v", ok, err)
	}
	started := bindAgentReport(accepted.Task, oldConnection, AgentTask{Status: taskstate.Running, Evidence: Evidence{ExecutionAttempted: true}, Progress: Progress{Phase: PhaseExecuting}})
	if _, err := fixture.store.ReconcileAgentJournal(context.Background(), oldConnection, []AgentTask{started}, false); err != nil {
		t.Fatal(err)
	}
	newConnection := oldConnection
	newConnection.ConnectionGeneration++
	newConnection.JournalID = strings.Repeat("e", 64)
	if _, err := fixture.db.Exec(`UPDATE nodes SET connection_generation=?,last_seen_at=? WHERE id=?`, newConnection.ConnectionGeneration, fixture.now.UnixNano(), testNodeID); err != nil {
		t.Fatal(err)
	}
	observed, err := fixture.store.ObserveAgentConnection(context.Background(), newConnection)
	if err != nil {
		t.Fatal(err)
	}
	if !observed.Changed || !observed.ReviewRequired || len(observed.AffectedTaskIDs) != 1 {
		t.Fatalf("journal loss not surfaced: %+v", observed)
	}
	if _, err := fixture.store.ReconcileAgentJournal(context.Background(), newConnection, nil, true); !errors.Is(err, ErrReconciliationNeeded) {
		t.Fatalf("absence after journal loss was accepted: %v", err)
	}
	if _, ok, err := fixture.store.ClaimNext(context.Background(), newConnection, sql.NullInt64{}, "unknown"); !errors.Is(err, ErrReconciliationNeeded) || ok {
		t.Fatalf("blocked task was dispatched: ok=%t err=%v", ok, err)
	}
	var claimCount int
	if err := fixture.db.QueryRow(`SELECT count(*) FROM core_task_resource_claims WHERE task_id=?`, accepted.Task.TaskID).Scan(&claimCount); err != nil || claimCount != 1 {
		t.Fatalf("journal loss released claim: count=%d err=%v", claimCount, err)
	}
	if err := fixture.store.ResolveUncertainTask(context.Background(), testNodeID, accepted.Task.TaskID, taskstate.Canceled,
		Evidence{ExecutionAttempted: true, ProcessTerminated: true, CancellationConfirmed: true, ActualResultConfirmed: true}, Result{Code: ResultCanceled, ObservedState: "stopped"}, sql.NullInt64{Int64: 1, Valid: true}, "192.0.2.8"); err != nil {
		t.Fatal(err)
	}
	if err := fixture.store.CompleteJournalReplacement(context.Background(), newConnection); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := fixture.store.ClaimNext(context.Background(), newConnection, sql.NullInt64{}, "unknown"); err != nil || ok {
		t.Fatalf("terminal task re-enqueued: ok=%t err=%v", ok, err)
	}
}

func TestMissingCoreJournalLedgerCannotTrustNewIDForSentTasks(t *testing.T) {
	fixture := openTestDB(t, "", time.Now().UTC())
	accepted, err := fixture.store.Enqueue(context.Background(), defaultRequest())
	if err != nil {
		t.Fatal(err)
	}
	oldConnection := testConnection(strings.Repeat("1", 64))
	if _, err := fixture.store.ObserveAgentConnection(context.Background(), oldConnection); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := fixture.store.ClaimNext(context.Background(), oldConnection, sql.NullInt64{}, "unknown"); err != nil || !ok {
		t.Fatalf("claim: %t %v", ok, err)
	}
	if _, err := fixture.db.Exec(`DELETE FROM core_task_agent_state WHERE node_id=?`, testNodeID); err != nil {
		t.Fatal(err)
	}
	newConnection := oldConnection
	newConnection.ConnectionGeneration++
	newConnection.JournalID = strings.Repeat("2", 64)
	if _, err := fixture.db.Exec(`UPDATE nodes SET connection_generation=?,last_seen_at=? WHERE id=?`, newConnection.ConnectionGeneration, fixture.now.UnixNano(), testNodeID); err != nil {
		t.Fatal(err)
	}
	observation, err := fixture.store.ObserveAgentConnection(context.Background(), newConnection)
	if err != nil {
		t.Fatal(err)
	}
	if !observation.Changed || !observation.ReviewRequired || len(observation.AffectedTaskIDs) != 1 || observation.AffectedTaskIDs[0] != accepted.Task.TaskID {
		t.Fatalf("missing Core JournalID ledger trusted sent work as fresh: %+v", observation)
	}
	task, err := fixture.store.Get(context.Background(), testNodeID, accepted.Task.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if task.Status != taskstate.Unknown || !task.ReconciliationRequired || !task.Evidence.DeliveryCommitted || task.Evidence.ExecutionAttempted {
		t.Fatalf("sent task was not made unknown using only committed-delivery evidence: %+v", task)
	}
	if _, ok, err := fixture.store.ClaimNext(context.Background(), newConnection, sql.NullInt64{}, "unknown"); !errors.Is(err, ErrReconciliationNeeded) || ok {
		t.Fatalf("missing ledger allowed dispatch during reconciliation: ok=%t err=%v", ok, err)
	}
}

func TestReadOnlySQLiteFailureDoesNotPersistPartialAcceptance(t *testing.T) {
	now := time.Now().UTC()
	path := filepath.Join(t.TempDir(), "readonly.sqlite")
	fixture := openTestDB(t, path, now)
	_ = fixture.db.Close()
	dsnURL := url.URL{Scheme: "file", Path: filepath.ToSlash(path)}
	query := dsnURL.Query()
	query.Set("mode", "ro")
	query.Add("_pragma", "foreign_keys(1)")
	dsnURL.RawQuery = query.Encode()
	readOnlyDB, err := sql.Open("sqlite", dsnURL.String())
	if err != nil {
		t.Fatal(err)
	}
	readOnlyDB.SetMaxOpenConns(1)
	defer readOnlyDB.Close()
	store, err := New(readOnlyDB, Options{Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Enqueue(context.Background(), defaultRequest()); err == nil {
		t.Fatal("read-only Core database accepted task")
	}
	var tasks, audits, claims int
	if err := readOnlyDB.QueryRow(`SELECT count(*) FROM core_tasks`).Scan(&tasks); err != nil {
		t.Fatal(err)
	}
	if err := readOnlyDB.QueryRow(`SELECT count(*) FROM core_task_audit_events`).Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if err := readOnlyDB.QueryRow(`SELECT count(*) FROM core_task_resource_claims`).Scan(&claims); err != nil {
		t.Fatal(err)
	}
	if tasks != 0 || audits != 0 || claims != 0 {
		t.Fatalf("read-only failure left partial rows task/audit/claim=%d/%d/%d", tasks, audits, claims)
	}
}

func TestUnresolvedTasksCannotBePruned(t *testing.T) {
	fixture := openTestDB(t, "", time.Now().UTC())
	accepted, err := fixture.store.Enqueue(context.Background(), defaultRequest())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.db.Exec(`DELETE FROM core_tasks WHERE task_id=?`, accepted.Task.TaskID); err == nil {
		t.Fatal("unresolved task was deleted")
	}
	var count int
	if err := fixture.db.QueryRow(`SELECT count(*) FROM core_tasks WHERE task_id=?`, accepted.Task.TaskID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatal("unresolved task row disappeared")
	}
}

func TestTaskProcessKillAndReopenKeepsIntentAuditAndClaim(t *testing.T) {
	if os.Getenv("NODEDANCE_CORE_TASK_HELPER") == "1" {
		path := os.Getenv("NODEDANCE_CORE_TASK_DB")
		ns, err := time.ParseDuration(os.Getenv("NODEDANCE_CORE_TASK_NOW_NS") + "ns")
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		dsnURL := url.URL{Scheme: "file", Path: filepath.ToSlash(path), RawQuery: "_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)&_pragma=synchronous(FULL)"}
		db, err := sql.Open("sqlite", dsnURL.String())
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		store, err := New(db, Options{Now: func() time.Time { return time.Unix(0, ns.Nanoseconds()).UTC() }})
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		request := defaultRequest()
		request.TaskID = "task-from-killed-process"
		if _, err := store.Enqueue(context.Background(), request); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		fmt.Println("READY")
		select {}
	}

	now := time.Now().UTC()
	path := filepath.Join(t.TempDir(), "kill-reopen.sqlite")
	fixture := openTestDB(t, path, now)
	if err := fixture.db.Close(); err != nil {
		t.Fatal(err)
	}
	commandCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	command := exec.CommandContext(commandCtx, os.Args[0], "-test.run=^TestTaskProcessKillAndReopenKeepsIntentAuditAndClaim$")
	command.Env = append(os.Environ(), "NODEDANCE_CORE_TASK_HELPER=1", "NODEDANCE_CORE_TASK_DB="+path, fmt.Sprintf("NODEDANCE_CORE_TASK_NOW_NS=%d", now.UnixNano()))
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr strings.Builder
	command.Stderr = &stderr
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	var waitOnce sync.Once
	waitResult := make(chan error, 1)
	stopChild := func() {
		waitOnce.Do(func() {
			if command.ProcessState == nil {
				_ = command.Process.Kill()
			}
			go func() { waitResult <- command.Wait() }()
		})
	}
	ready := make(chan string, 1)
	go func() { line, _ := bufio.NewReader(stdout).ReadString('\n'); ready <- strings.TrimSpace(line) }()
	select {
	case line := <-ready:
		if line != "READY" {
			stopChild()
			t.Fatalf("helper exited before durable commit, line=%q stderr=%s", line, stderr.String())
		}
	case <-time.After(5 * time.Second):
		stopChild()
		select {
		case <-waitResult:
		case <-time.After(5 * time.Second):
			t.Fatal("child Go test process did not exit after kill")
		}
		t.Fatalf("child did not reach bounded READY checkpoint, stderr=%s", stderr.String())
	case <-commandCtx.Done():
		stopChild()
		t.Fatalf("child process timed out: %v stderr=%s", commandCtx.Err(), stderr.String())
	}
	stopChild()
	select {
	case <-waitResult:
	case <-time.After(5 * time.Second):
		t.Fatal("child Go test process did not exit after kill")
	}

	reopened := openTestDB(t, path, now)
	task, err := reopened.store.Get(context.Background(), testNodeID, "task-from-killed-process")
	if err != nil {
		t.Fatalf("read task after process kill and reopen: %v", err)
	}
	if task.Status != taskstate.Queued || task.DeliveryState != "ready" {
		t.Fatalf("reopened task lost durable queued state: %+v", task)
	}
	var audits, claims int
	if err := reopened.db.QueryRow(`SELECT count(*) FROM core_task_audit_events WHERE task_id=? AND event='accepted'`, task.TaskID).Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if err := reopened.db.QueryRow(`SELECT count(*) FROM core_task_resource_claims WHERE task_id=?`, task.TaskID).Scan(&claims); err != nil {
		t.Fatal(err)
	}
	if audits != 1 || claims != 1 {
		t.Fatalf("reopen lost audit/claim rows=%d/%d", audits, claims)
	}
}

func TestTypedIntentRejectsUnsafeOperations(t *testing.T) {
	fixture := openTestDB(t, "", time.Now().UTC())
	containerID := strings.Repeat("f", 64)
	for _, request := range []EnqueueRequest{
		{NodeID: testNodeID, IdempotencyKey: "unsafe-delete", Intent: Intent{Action: ActionDelete, ContainerID: containerID}},
		{NodeID: testNodeID, IdempotencyKey: "compose-rename", Intent: Intent{Action: ActionRename, ContainerID: containerID, NewName: "new-name"}, ComposeManaged: true},
		{NodeID: testNodeID, IdempotencyKey: "bad-name", Intent: Intent{Action: ActionRename, ContainerID: containerID, NewName: "../escape"}},
		{NodeID: testNodeID, IdempotencyKey: "unsupported", Intent: Intent{Action: Action("exec"), ContainerID: containerID}},
		{NodeID: testNodeID, IdempotencyKey: "short-id", Intent: Intent{Action: ActionStop, ContainerID: "abc"}},
	} {
		if _, err := fixture.store.Enqueue(context.Background(), request); err == nil {
			t.Errorf("unsafe intent accepted: %+v", request)
		}
	}
}
