package taskjournal

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/CST-Cat/NodeDance/internal/taskstate"
)

func openTestStore(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(privateTempDir(t), "agent", "tasks.sqlite")
	store, err := Open(context.Background(), path, "node-test")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store, path
}

func privateTempDir(t *testing.T) string {
	t.Helper()
	base := t.TempDir()
	if err := os.Chmod(base, 0o700); err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(base, "private")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	return directory
}

func testIdentity(taskID, key string) taskstate.Identity {
	return taskstate.Identity{
		TaskID: taskID, NodeID: "node-test", IdempotencyKey: key,
		TargetID: "container-test", ResourceKey: "container-test", Action: "restart",
		Payload: []byte(`{"timeout":5,"force":false}`),
	}
}

func TestEnqueueIdempotencyTenConcurrentRequestsAndResourceConflict(t *testing.T) {
	store, dbPath := openTestStore(t)
	base := testIdentity("task-original", "key-original")
	const repeats = 10
	results := make([]EnqueueResult, repeats)
	errorsByIndex := make([]error, repeats)
	var group sync.WaitGroup
	for i := 0; i < repeats; i++ {
		group.Add(1)
		go func(index int) {
			defer group.Done()
			identity := base
			identity.TaskID = fmt.Sprintf("task-retry-%02d", index)
			results[index], errorsByIndex[index] = store.Enqueue(context.Background(), identity)
		}(i)
	}
	group.Wait()
	created := 0
	stableTaskID := results[0].Task.TaskID
	for i, err := range errorsByIndex {
		if err != nil {
			t.Fatalf("request %d: %v", i, err)
		}
		if results[i].Task.TaskID != stableTaskID {
			t.Fatalf("request %d got task %q, want stable task %q", i, results[i].Task.TaskID, stableTaskID)
		}
		if results[i].Created {
			created++
		}
	}
	if created != 1 {
		t.Fatalf("created %d tasks for %d duplicate requests, want one", created, repeats)
	}
	var count int
	if err := store.db.QueryRow(`SELECT count(*) FROM task_journal`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("stored %d task rows, want one", count)
	}

	differentBody := base
	differentBody.TaskID = "task-different-body"
	differentBody.Payload = []byte(`{"timeout":6,"force":false}`)
	if _, err := store.Enqueue(context.Background(), differentBody); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("same idempotency key with different request = %v, want conflict", err)
	}

	differentKeySameTaskID := base
	differentKeySameTaskID.TaskID = stableTaskID
	differentKeySameTaskID.IdempotencyKey = "key-different"
	if _, err := store.Enqueue(context.Background(), differentKeySameTaskID); !errors.Is(err, ErrTaskIDConflict) {
		t.Fatalf("same task ID with different key = %v, want task ID conflict", err)
	}

	otherTaskSameResource := testIdentity("task-other", "key-other")
	if _, err := store.Enqueue(context.Background(), otherTaskSameResource); !errors.Is(err, ErrResourceBusy) {
		t.Fatalf("second active task on same resource = %v, want resource busy", err)
	}

	otherNode := otherTaskSameResource
	otherNode.NodeID = "node-other"
	if _, err := store.Enqueue(context.Background(), otherNode); !errors.Is(err, ErrWrongNode) {
		t.Fatalf("task from another node entered this journal: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(context.Background(), dbPath, "node-other"); !errors.Is(err, ErrWrongNode) {
		t.Fatalf("opening a journal under another node identity = %v, want wrong-node refusal", err)
	}
}

func TestDeliveredQueuedRecoveryBecomesUnknownAndKeepsClaim(t *testing.T) {
	store, _ := openTestStore(t)
	identity := testIdentity("task-delivered-before-run", "key-delivered-before-run")
	delivered, err := store.EnqueueDelivered(context.Background(), identity)
	if err != nil {
		t.Fatal(err)
	}
	if delivered.Task.Status != taskstate.Queued || !delivered.Task.DeliveryCommitted {
		t.Fatalf("durable delivery marker missing: %+v", delivered.Task)
	}
	if recovered, err := store.RecoverInterrupted(context.Background()); err != nil || recovered != 1 {
		t.Fatalf("recover queued delivered row = %d, %v; want 1", recovered, err)
	}
	got, err := store.Get(context.Background(), identity.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != taskstate.Unknown || !got.DeliveryCommitted || got.Evidence.ExecutionAttempted || got.Result.Code != ResultUncertain {
		t.Fatalf("recovered row claimed execution or lost delivery marker: %+v", got)
	}
	var claims int
	if err := store.db.QueryRow(`SELECT count(*) FROM active_resource_claims WHERE task_id=?`, identity.TaskID).Scan(&claims); err != nil || claims != 1 {
		t.Fatalf("unknown recovery released resource claim: claims=%d err=%v", claims, err)
	}
	if err := store.BeginExecution(context.Background(), identity.TaskID); err == nil {
		t.Fatal("unknown delivered task was eligible for execution replay")
	}
}

func TestV2UpgradeDoesNotInventDeliveryForLegacyQueuedRows(t *testing.T) {
	store, path := openTestStore(t)
	identity := testIdentity("legacy-v2-queued", "legacy-v2-key")
	if _, err := store.Enqueue(context.Background(), identity); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`ALTER TABLE task_journal DROP COLUMN delivery_committed`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`PRAGMA user_version=2`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	upgraded, err := Open(context.Background(), path, "node-test")
	if err != nil {
		t.Fatalf("upgrade v2 journal: %v", err)
	}
	defer upgraded.Close()
	var version int
	if err := upgraded.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || version != 3 {
		t.Fatalf("upgraded schema version = %d err=%v", version, err)
	}
	got, err := upgraded.Get(context.Background(), identity.TaskID)
	if err != nil || got.Status != taskstate.Queued || got.DeliveryCommitted {
		t.Fatalf("v2 row was falsely marked delivered: %+v err=%v", got, err)
	}
	if recovered, err := upgraded.RecoverInterrupted(context.Background()); err != nil || recovered != 0 {
		t.Fatalf("legacy queued row incorrectly became unknown: recovered=%d err=%v", recovered, err)
	}
}

func TestSnapshotAllReturnsOneBoundedCandidateWithIdentityFields(t *testing.T) {
	store, _ := openTestStore(t)
	for index := 0; index < 40; index++ {
		identity := testIdentity(fmt.Sprintf("snapshot-task-%02d", index), fmt.Sprintf("snapshot-key-%02d", index))
		identity.TargetID = fmt.Sprintf("container-%02d", index)
		identity.ResourceKey = identity.TargetID
		if _, err := store.EnqueueDelivered(context.Background(), identity); err != nil {
			t.Fatal(err)
		}
	}
	snapshot, err := store.SnapshotAll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot) != 40 {
		t.Fatalf("snapshot returned %d records, want 40", len(snapshot))
	}
	seen := make(map[string]struct{}, len(snapshot))
	for _, row := range snapshot {
		if _, duplicate := seen[row.TaskID]; duplicate {
			t.Fatalf("snapshot duplicated task %q", row.TaskID)
		}
		seen[row.TaskID] = struct{}{}
		if !row.DeliveryCommitted || row.IdempotencyKey == "" || row.RequestDigest == ([32]byte{}) {
			t.Fatalf("snapshot omitted durable identity: %+v", row)
		}
	}
	newIdentity := testIdentity("snapshot-after-freeze", "snapshot-after-freeze-key")
	newIdentity.TargetID, newIdentity.ResourceKey = "container-new", "container-new"
	if _, err := store.EnqueueDelivered(context.Background(), newIdentity); err != nil {
		t.Fatal(err)
	}
	if len(snapshot) != 40 {
		t.Fatalf("candidate changed after it was returned: %d", len(snapshot))
	}
}

func TestEnqueueChecksTaskIDAndIdempotencyKeyTogether(t *testing.T) {
	store, _ := openTestStore(t)
	first := testIdentity("task-a", "key-a")
	if _, err := store.Enqueue(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	second := testIdentity("task-b", "key-b")
	second.TargetID = "container-b"
	second.ResourceKey = "container-b"
	if _, err := store.Enqueue(context.Background(), second); err != nil {
		t.Fatal(err)
	}

	collision := first
	collision.TaskID = second.TaskID
	if _, err := store.Enqueue(context.Background(), collision); !errors.Is(err, ErrTaskIDConflict) {
		t.Fatalf("key-a/request-a paired with already-bound task-b = %v, want task ID conflict", err)
	}
}

func TestUnknownRetainsResourceUntilVerifiedResolution(t *testing.T) {
	store, _ := openTestStore(t)
	identity := testIdentity("task-unknown", "key-unknown")
	if _, err := store.Enqueue(context.Background(), identity); err != nil {
		t.Fatal(err)
	}
	if err := store.BeginExecution(context.Background(), identity.TaskID); err != nil {
		t.Fatal(err)
	}
	if err := store.Finish(context.Background(), identity.TaskID, taskstate.Succeeded,
		taskstate.Evidence{ExecutionCompleted: true}, Result{Code: ResultVerified, ObservedState: "running"}); err == nil {
		t.Fatal("succeeded without postcondition proof")
	}
	if err := store.MarkUnknown(context.Background(), identity.TaskID); err != nil {
		t.Fatal(err)
	}
	snapshot, err := store.Get(context.Background(), identity.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Status != taskstate.Unknown || snapshot.FinishedAt != nil {
		t.Fatalf("unknown task snapshot = %+v", snapshot)
	}
	if err := store.BeginExecution(context.Background(), identity.TaskID); err == nil {
		t.Fatal("unknown task was allowed to re-enter executor")
	}
	blocked := testIdentity("task-blocked", "key-blocked")
	if _, err := store.Enqueue(context.Background(), blocked); !errors.Is(err, ErrResourceBusy) {
		t.Fatalf("unknown task released its resource claim: %v", err)
	}
	if err := store.Finish(context.Background(), identity.TaskID, taskstate.Succeeded,
		taskstate.Evidence{ExecutionAttempted: true, ExecutionCompleted: true, PostconditionVerified: true},
		Result{Code: ResultVerified, ObservedState: "running", ResourceRevision: "rev-2"}); err != nil {
		t.Fatalf("reconcile unknown task with actual postcondition: %v", err)
	}
	if _, err := store.Enqueue(context.Background(), blocked); err != nil {
		t.Fatalf("verified terminal result did not release resource: %v", err)
	}
}

func TestPrepareMutationPersistsOnlyValidatedSafeBaseline(t *testing.T) {
	store, path := openTestStore(t)
	identity := testIdentity("task-baseline", "key-baseline")
	if _, err := store.Enqueue(context.Background(), identity); err != nil {
		t.Fatal(err)
	}
	if err := store.BeginExecution(context.Background(), identity.TaskID); err != nil {
		t.Fatal(err)
	}
	baseline := ExecutionBaseline{TargetID: identity.TargetID, Action: identity.Action,
		HostBootID: "00000000-0000-4000-8000-000000000001", StartedAt: "2026-10-08T09:00:00Z", RestartCount: 3,
		Running: true}
	secret := baseline
	secret.StartedAt = "raw-inspect-secret"
	if err := store.PrepareMutation(context.Background(), identity.TaskID, secret); !errors.Is(err, ErrInvalidBaseline) {
		t.Fatalf("raw Inspect data was accepted as baseline: %v", err)
	}
	if err := store.PrepareMutation(context.Background(), identity.TaskID, baseline); err != nil {
		t.Fatal(err)
	}
	snapshot, err := store.Get(context.Background(), identity.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.ExecutionPhase != ExecutionPhaseMutationMayHaveStarted || snapshot.Baseline == nil || *snapshot.Baseline != baseline {
		t.Fatalf("persisted baseline = phase %q baseline %+v, want %+v", snapshot.ExecutionPhase, snapshot.Baseline, baseline)
	}
	if err := store.PrepareMutation(context.Background(), identity.TaskID, baseline); !errors.Is(err, ErrBaselineAlreadySet) {
		t.Fatalf("second baseline prepare = %v, want already-set", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	for _, candidate := range []string{path, path + "-wal", path + "-shm"} {
		data, readErr := os.ReadFile(candidate)
		if errors.Is(readErr, os.ErrNotExist) {
			continue
		}
		if readErr != nil {
			t.Fatal(readErr)
		}
		if bytes.Contains(data, []byte("raw-inspect-secret")) {
			t.Fatalf("raw Inspect payload secret found in %s", filepath.Base(candidate))
		}
	}
}

func TestPrepareMutationFailureRollsBackPhaseAndBaseline(t *testing.T) {
	store, _ := openTestStore(t)
	identity := testIdentity("task-baseline-fail", "key-baseline-fail")
	if _, err := store.Enqueue(context.Background(), identity); err != nil {
		t.Fatal(err)
	}
	if err := store.BeginExecution(context.Background(), identity.TaskID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`CREATE TRIGGER reject_baseline BEFORE UPDATE OF execution_phase ON task_journal
		WHEN NEW.execution_phase='mutation_may_have_started' BEGIN SELECT RAISE(ABORT,'injected failure'); END`); err != nil {
		t.Fatal(err)
	}
	baseline := ExecutionBaseline{TargetID: identity.TargetID, Action: identity.Action,
		HostBootID: "00000000-0000-4000-8000-000000000001", StartedAt: "0001-01-01T00:00:00Z", RestartCount: 0}
	if err := store.PrepareMutation(context.Background(), identity.TaskID, baseline); err == nil {
		t.Fatal("injected baseline transaction failure was ignored")
	}
	snapshot, err := store.Get(context.Background(), identity.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.ExecutionPhase != ExecutionPhaseNone || snapshot.Baseline != nil || snapshot.Status != taskstate.Running {
		t.Fatalf("failed baseline transaction partially changed task: %+v", snapshot)
	}
	blocked := testIdentity("task-baseline-blocked", "key-baseline-blocked")
	if _, err := store.Enqueue(context.Background(), blocked); !errors.Is(err, ErrResourceBusy) {
		t.Fatalf("failed baseline unexpectedly released claim: %v", err)
	}
}

func TestPrepareMutationRejectsNoncanonicalOrMismatchedBaseline(t *testing.T) {
	store, _ := openTestStore(t)
	identity := testIdentity("task-baseline-invalid", "key-baseline-invalid")
	if _, err := store.Enqueue(context.Background(), identity); err != nil {
		t.Fatal(err)
	}
	if err := store.BeginExecution(context.Background(), identity.TaskID); err != nil {
		t.Fatal(err)
	}
	valid := ExecutionBaseline{TargetID: identity.TargetID, Action: identity.Action,
		HostBootID: "00000000-0000-4000-8000-000000000001", StartedAt: "2026-10-08T10:00:00Z", RestartCount: 0}
	invalidCases := []ExecutionBaseline{}
	wrongTarget := valid
	wrongTarget.TargetID = "another-target"
	invalidCases = append(invalidCases, wrongTarget)
	wrongBoot := valid
	wrongBoot.HostBootID = "not-a-boot-id"
	invalidCases = append(invalidCases, wrongBoot)
	noncanonicalTime := valid
	noncanonicalTime.StartedAt = "2026-10-08T11:00:00+01:00"
	invalidCases = append(invalidCases, noncanonicalTime)
	negativeRestartCount := valid
	negativeRestartCount.RestartCount = -1
	invalidCases = append(invalidCases, negativeRestartCount)
	invalidPausedState := valid
	invalidPausedState.Paused = true
	invalidCases = append(invalidCases, invalidPausedState)
	for index, invalid := range invalidCases {
		if err := store.PrepareMutation(context.Background(), identity.TaskID, invalid); !errors.Is(err, ErrInvalidBaseline) {
			t.Fatalf("invalid baseline %d error = %v, want invalid-baseline", index, err)
		}
	}
	snapshot, err := store.Get(context.Background(), identity.TaskID)
	if err != nil || snapshot.ExecutionPhase != ExecutionPhaseNone || snapshot.Baseline != nil {
		t.Fatalf("invalid baseline attempts changed task: %+v %v", snapshot, err)
	}
}

func TestV1MigrationPreservesRowsAndLegacyRunningBecomesUnknownWithoutBaseline(t *testing.T) {
	path := filepath.Join(privateTempDir(t), "agent", "tasks.sqlite")
	if err := os.Mkdir(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	createV1Fixture(t, path, false, 1)
	store, err := Open(context.Background(), path, "node-test")
	if err != nil {
		t.Fatalf("migrate v1 journal: %v", err)
	}
	defer store.Close()
	var version int
	if err := store.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || version != 3 {
		t.Fatalf("schema version = %d, error %v; want 3", version, err)
	}
	if store.JournalID() != strings.Repeat("0", 64) {
		t.Fatalf("migration changed journal identity: %q", store.JournalID())
	}
	snapshot, err := store.Get(context.Background(), "legacy-running")
	if err != nil || snapshot.Status != taskstate.Running || snapshot.TaskID != "legacy-running" ||
		snapshot.TargetID != "container-test" || snapshot.ResourceKey != "container-test" || snapshot.Action != "restart" ||
		!snapshot.Evidence.ExecutionAttempted || snapshot.StartedAt == nil || snapshot.Baseline != nil || snapshot.ExecutionPhase != ExecutionPhaseNone {
		t.Fatalf("migrated legacy running task = %+v, %v", snapshot, err)
	}
	var claims int
	if err := store.db.QueryRow(`SELECT count(*) FROM active_resource_claims WHERE task_id='legacy-running'`).Scan(&claims); err != nil || claims != 1 {
		t.Fatalf("legacy claim count = %d, error %v; want retained claim", claims, err)
	}
	recovered, err := store.RecoverInterrupted(context.Background())
	if err != nil || recovered != 1 {
		t.Fatalf("recover legacy running task = %d, %v", recovered, err)
	}
	snapshot, err = store.Get(context.Background(), "legacy-running")
	if err != nil || snapshot.Status != taskstate.Unknown || snapshot.Baseline != nil || snapshot.ExecutionPhase != ExecutionPhaseNone {
		t.Fatalf("recovered legacy task = %+v, %v; must remain unknown without synthesized baseline", snapshot, err)
	}
	if err := store.BeginExecution(context.Background(), snapshot.TaskID); err == nil {
		t.Fatal("migrated/recovered legacy task was eligible for replay")
	}
}

func TestV1MigrationFailureRollsBackAndFutureVersionIsRejected(t *testing.T) {
	t.Run("transaction rollback", func(t *testing.T) {
		path := filepath.Join(privateTempDir(t), "agent", "tasks.sqlite")
		if err := os.Mkdir(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		createV1Fixture(t, path, true, 1)
		if store, err := Open(context.Background(), path, "node-test"); err == nil {
			_ = store.Close()
			t.Fatal("malformed v1 schema unexpectedly migrated")
		}
		db, err := sql.Open("sqlite", path)
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		var version int
		if err := db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || version != 1 {
			t.Fatalf("failed migration changed version to %d: %v", version, err)
		}
		columns := tableColumns(t, db)
		if columns["execution_phase"] || !columns["baseline_verified"] {
			t.Fatalf("failed migration was not rolled back atomically; columns=%v", columns)
		}
		var status string
		if err := db.QueryRow(`SELECT status FROM task_journal WHERE task_id='legacy-running'`).Scan(&status); err != nil || status != "running" {
			t.Fatalf("legacy row changed after migration failure: status=%q err=%v", status, err)
		}
	})

	t.Run("future version", func(t *testing.T) {
		path := filepath.Join(privateTempDir(t), "agent", "tasks.sqlite")
		if err := os.Mkdir(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		db, err := sql.Open("sqlite", path)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`PRAGMA user_version=77`); err != nil {
			t.Fatal(err)
		}
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, 0o600); err != nil {
			t.Fatal(err)
		}
		if store, err := Open(context.Background(), path, "node-test"); err == nil {
			_ = store.Close()
			t.Fatal("future schema version unexpectedly opened")
		}
		db, err = sql.Open("sqlite", path)
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		var version int
		if err := db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || version != 77 {
			t.Fatalf("future version changed to %d: %v", version, err)
		}
		if columns := tableColumns(t, db); len(columns) != 0 {
			t.Fatalf("future schema was modified: columns=%v", columns)
		}
	})
}

func createV1Fixture(t *testing.T, path string, conflictingColumn bool, version int) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	legacyExtra := ""
	if conflictingColumn {
		legacyExtra = `, baseline_verified INTEGER NOT NULL DEFAULT 0`
	}
	statements := []string{
		`CREATE TABLE journal_owner(id INTEGER PRIMARY KEY CHECK(id=1),node_id TEXT NOT NULL,journal_id TEXT NOT NULL)`,
		`CREATE TABLE task_journal(task_id TEXT PRIMARY KEY,node_id TEXT NOT NULL,idempotency_key TEXT NOT NULL,request_digest BLOB NOT NULL CHECK(length(request_digest)=32),target_id TEXT NOT NULL,resource_key TEXT NOT NULL,action TEXT NOT NULL,status TEXT NOT NULL CHECK(status IN ('queued','running','succeeded','failed','timed_out','canceled','unknown')),created_at_ns INTEGER NOT NULL,updated_at_ns INTEGER NOT NULL,started_at_ns INTEGER,finished_at_ns INTEGER,execution_attempted INTEGER NOT NULL DEFAULT 0 CHECK(execution_attempted IN (0,1)),execution_completed INTEGER NOT NULL DEFAULT 0 CHECK(execution_completed IN (0,1)),failure_confirmed INTEGER NOT NULL DEFAULT 0 CHECK(failure_confirmed IN (0,1)),postcondition_verified INTEGER NOT NULL DEFAULT 0 CHECK(postcondition_verified IN (0,1)),process_terminated INTEGER NOT NULL DEFAULT 0 CHECK(process_terminated IN (0,1)),actual_result_confirmed INTEGER NOT NULL DEFAULT 0 CHECK(actual_result_confirmed IN (0,1)),cancellation_confirmed INTEGER NOT NULL DEFAULT 0 CHECK(cancellation_confirmed IN (0,1)),progress_phase TEXT NOT NULL DEFAULT 'accepted',progress_completed INTEGER NOT NULL DEFAULT 0,progress_total INTEGER NOT NULL DEFAULT 0,result_code TEXT NOT NULL DEFAULT '',observed_state TEXT NOT NULL DEFAULT '',resource_revision TEXT NOT NULL DEFAULT '',task_log BLOB NOT NULL DEFAULT X'',log_truncated INTEGER NOT NULL DEFAULT 0 CHECK(log_truncated IN (0,1))` + strings.TrimSpace(legacyExtra) + `,UNIQUE(node_id,idempotency_key))`,
		`CREATE INDEX task_journal_resource_status ON task_journal(node_id,resource_key,status)`,
		`CREATE TABLE active_resource_claims(node_id TEXT NOT NULL,resource_key TEXT NOT NULL,task_id TEXT NOT NULL UNIQUE REFERENCES task_journal(task_id) ON DELETE CASCADE,PRIMARY KEY(node_id,resource_key))`,
		`INSERT INTO journal_owner VALUES(1,'node-test','` + strings.Repeat("0", 64) + `')`,
		`PRAGMA user_version=` + fmt.Sprint(version),
	}
	for _, statement := range statements {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("create v1 fixture: %v", err)
		}
	}
	identity := testIdentity("legacy-running", "legacy-key")
	digest, err := taskstate.RequestDigest(identity)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().UnixNano()
	if _, err := db.Exec(`INSERT INTO task_journal(task_id,node_id,idempotency_key,request_digest,target_id,resource_key,action,status,created_at_ns,updated_at_ns,started_at_ns,execution_attempted)
		VALUES(?,?,?,?,?,?,?,'running',?,?,?,1)`, identity.TaskID, identity.NodeID, identity.IdempotencyKey, digest[:], identity.TargetID, identity.ResourceKey, identity.Action, now, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO active_resource_claims VALUES(?,?,?)`, identity.NodeID, identity.ResourceKey, identity.TaskID); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
}

func tableColumns(t *testing.T, db *sql.DB) map[string]bool {
	t.Helper()
	rows, err := db.Query(`PRAGMA table_info(task_journal)`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	columns := make(map[string]bool)
	for rows.Next() {
		var ordinal, notNull, primaryKey int
		var name, kind string
		var defaultValue any
		if err := rows.Scan(&ordinal, &name, &kind, &notNull, &defaultValue, &primaryKey); err != nil {
			t.Fatal(err)
		}
		columns[name] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return columns
}

func TestBoundedRedactedLogsAndTypedProgress(t *testing.T) {
	store, _ := openTestStore(t)
	identity := testIdentity("task-logs", "key-logs")
	if _, err := store.Enqueue(context.Background(), identity); err != nil {
		t.Fatal(err)
	}
	if err := store.BeginExecution(context.Background(), identity.TaskID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendLog(context.Background(), identity.TaskID, LogChunk{Bytes: []byte("credential=secret")}); !errors.Is(err, ErrUnredactedLog) {
		t.Fatalf("unredacted log append = %v", err)
	}
	if err := store.UpdateProgress(context.Background(), identity.TaskID, Progress{Phase: PhaseExecuting, Completed: 3, Total: 2}); !errors.Is(err, ErrInvalidProgress) {
		t.Fatalf("progress above total = %v", err)
	}
	if err := store.UpdateProgress(context.Background(), identity.TaskID, Progress{Phase: "secret-password", Completed: 1, Total: 2}); !errors.Is(err, ErrInvalidProgress) {
		t.Fatalf("freeform progress phase = %v", err)
	}
	if err := store.UpdateProgress(context.Background(), identity.TaskID, Progress{Phase: PhaseExecuting, Completed: 1, Total: 2}); err != nil {
		t.Fatal(err)
	}
	first := []byte("stdout: 正常\nstderr: denied\n")
	if truncated, err := store.AppendLog(context.Background(), identity.TaskID, LogChunk{Bytes: first, Redacted: true}); err != nil || truncated {
		t.Fatalf("append first log = truncated %t, err %v", truncated, err)
	}
	large := bytes.Repeat([]byte{'x'}, MaxTaskLogBytes)
	if truncated, err := store.AppendLog(context.Background(), identity.TaskID, LogChunk{Bytes: large, Redacted: true}); err != nil || !truncated {
		t.Fatalf("append over-limit log = truncated %t, err %v", truncated, err)
	}
	logBytes, truncated, err := store.GetLog(context.Background(), identity.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if len(logBytes) != MaxTaskLogBytes || !truncated || !bytes.HasPrefix(logBytes, first) {
		t.Fatalf("stored log len=%d truncated=%t prefix=%q", len(logBytes), truncated, logBytes[:min(len(logBytes), len(first))])
	}
	snapshot, err := store.Get(context.Background(), identity.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.LogBytes != MaxTaskLogBytes || !snapshot.LogTruncated || snapshot.Progress.Completed != 1 {
		t.Fatalf("snapshot lost log/progress metadata: %+v", snapshot)
	}
}

func TestLogTruncationAtExactLimitPersistsAcrossRestart(t *testing.T) {
	path := filepath.Join(privateTempDir(t), "agent", "tasks.sqlite")
	store, err := Open(context.Background(), path, "node-test")
	if err != nil {
		t.Fatal(err)
	}
	identity := testIdentity("task-log-exact-limit", "key-log-exact-limit")
	if _, err := store.Enqueue(context.Background(), identity); err != nil {
		t.Fatal(err)
	}
	if err := store.BeginExecution(context.Background(), identity.TaskID); err != nil {
		t.Fatal(err)
	}
	full := bytes.Repeat([]byte{'x'}, MaxTaskLogBytes)
	if truncated, err := store.AppendLog(context.Background(), identity.TaskID, LogChunk{Bytes: full, Redacted: true}); err != nil || truncated {
		t.Fatalf("append exact-limit log = truncated %t, err %v", truncated, err)
	}
	if truncated, err := store.AppendLog(context.Background(), identity.TaskID, LogChunk{Bytes: []byte{'y'}, Redacted: true}); err != nil || !truncated {
		t.Fatalf("append beyond exact limit = truncated %t, err %v", truncated, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(context.Background(), path, "node-test")
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	logBytes, truncated, err := reopened.GetLog(context.Background(), identity.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if len(logBytes) != MaxTaskLogBytes || !truncated {
		t.Fatalf("reopened log len=%d truncated=%t, want exact limit and persisted truncation", len(logBytes), truncated)
	}
}

func TestFailedJournalWritePreventsBeginExecution(t *testing.T) {
	store, _ := openTestStore(t)
	if _, err := store.db.Exec(`CREATE TRIGGER reject_task_insert BEFORE INSERT ON task_journal BEGIN SELECT RAISE(ABORT,'injected journal write failure'); END`); err != nil {
		t.Fatal(err)
	}
	identity := testIdentity("task-write-fails", "key-write-fails")
	if _, err := store.Enqueue(context.Background(), identity); err == nil {
		t.Fatal("injected journal failure was ignored")
	}
	if err := store.BeginExecution(context.Background(), identity.TaskID); !errors.Is(err, ErrTaskNotFound) {
		t.Fatalf("BeginExecution after failed enqueue = %v, want not found", err)
	}
	var count int
	if err := store.db.QueryRow(`SELECT count(*) FROM task_journal`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("failed enqueue left %d task rows", count)
	}
}

func TestRequestPayloadSecretIsNeverStored(t *testing.T) {
	store, path := openTestStore(t)
	identity := testIdentity("task-secret", "key-secret")
	identity.Payload = []byte(`{"password":"nd-test-secret-must-not-persist"}`)
	if _, err := store.Enqueue(context.Background(), identity); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	for _, candidate := range []string{path, path + "-wal", path + "-shm"} {
		data, err := os.ReadFile(candidate)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(data, []byte("nd-test-secret-must-not-persist")) {
			t.Fatalf("raw request secret found in %s", filepath.Base(candidate))
		}
	}
}

func TestJournalCrashChild(t *testing.T) {
	stage := os.Getenv("NODEDANCE_JOURNAL_CRASH_STAGE")
	if stage == "" {
		return
	}
	path := os.Getenv("NODEDANCE_JOURNAL_CRASH_DB")
	marker := os.Getenv("NODEDANCE_JOURNAL_CRASH_MARKER")
	store, err := Open(context.Background(), path, "node-test")
	if err != nil {
		t.Fatalf("child open: %v", err)
	}
	identity := testIdentity("task-crash", "key-crash")
	if _, err := store.Enqueue(context.Background(), identity); err != nil {
		t.Fatalf("child enqueue: %v", err)
	}
	if stage != "queued" {
		if err := store.BeginExecution(context.Background(), identity.TaskID); err != nil {
			t.Fatalf("child begin: %v", err)
		}
	}
	if stage == "possible-side-effect" {
		// This marker only indicates a crash checkpoint beyond the executor
		// boundary. It is not an Executor or a successful Docker operation.
		if err := os.WriteFile(marker, []byte("possible-side-effect-before-result-commit"), 0o600); err != nil {
			t.Fatalf("write crash checkpoint: %v", err)
		}
	}
	fmt.Println("READY")
	for {
		time.Sleep(time.Second)
	}
}

func TestSubprocessKillAndRestartRecovery(t *testing.T) {
	for _, stage := range []string{"queued", "running-before-executor", "possible-side-effect", "lock-owner"} {
		t.Run(stage, func(t *testing.T) {
			dir := privateTempDir(t)
			dbPath := filepath.Join(dir, "journal.sqlite")
			marker := filepath.Join(dir, "side-effect-checkpoint")
			binary, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			childCtx, cancelChild := context.WithTimeout(context.Background(), 20*time.Second)
			command := exec.CommandContext(childCtx, binary, "-test.run=^TestJournalCrashChild$")
			command.Env = append(os.Environ(),
				"NODEDANCE_JOURNAL_CRASH_STAGE="+stage,
				"NODEDANCE_JOURNAL_CRASH_DB="+dbPath,
				"NODEDANCE_JOURNAL_CRASH_MARKER="+marker,
			)
			stdout, childStdout, err := os.Pipe()
			if err != nil {
				cancelChild()
				t.Fatal(err)
			}
			command.Stdout = childStdout
			var stderr bytes.Buffer
			command.Stderr = &stderr
			if err := command.Start(); err != nil {
				_ = stdout.Close()
				_ = childStdout.Close()
				cancelChild()
				t.Fatal(err)
			}
			_ = childStdout.Close()
			waitResult := make(chan error, 1)
			go func() { waitResult <- command.Wait() }()
			var childWaitErr error
			childWaited := false
			awaitChild := func(timeout time.Duration) (error, bool) {
				if childWaited {
					return childWaitErr, true
				}
				select {
				case childWaitErr = <-waitResult:
					childWaited = true
					return childWaitErr, true
				case <-time.After(timeout):
					return nil, false
				}
			}
			killAndWait := func() (error, bool) {
				if !childWaited && command.Process != nil {
					_ = command.Process.Kill()
				}
				return awaitChild(5 * time.Second)
			}
			readyResult := make(chan struct {
				line string
				err  error
			}, 1)
			readyReadDone := make(chan struct{})
			t.Cleanup(func() {
				if !childWaited {
					cancelChild()
					if command.Process != nil {
						_ = command.Process.Kill()
					}
					if _, ok := awaitChild(5 * time.Second); !ok {
						t.Errorf("child process did not exit after bounded kill")
					}
				}
				_ = stdout.Close()
				select {
				case <-readyReadDone:
				case <-time.After(time.Second):
					t.Errorf("READY reader did not stop after child cleanup")
				}
				cancelChild()
			})
			go func() {
				defer close(readyReadDone)
				line, readErr := bufio.NewReader(stdout).ReadString('\n')
				readyResult <- struct {
					line string
					err  error
				}{line: line, err: readErr}
			}()
			select {
			case ready := <-readyResult:
				if ready.err != nil || strings.TrimSpace(ready.line) != "READY" {
					_, reaped := killAndWait()
					if !reaped {
						t.Fatalf("child did not exit after invalid READY result")
					}
					t.Fatalf("child checkpoint %q, read error %v, stderr %s", ready.line, ready.err, stderr.String())
				}
			case <-time.After(5 * time.Second):
				_, reaped := killAndWait()
				if !reaped {
					t.Fatalf("child did not exit after READY timeout")
				}
				t.Fatalf("child did not emit bounded READY checkpoint, stderr %s", stderr.String())
			case <-childCtx.Done():
				_, reaped := killAndWait()
				if !reaped {
					t.Fatalf("child did not exit after process context timeout")
				}
				t.Fatalf("child process context expired before READY, stderr %s", stderr.String())
			}
			if stage == "lock-owner" {
				openCtx, cancelOpen := context.WithTimeout(context.Background(), 5*time.Second)
				secondStore, openErr := Open(openCtx, dbPath, "node-test")
				cancelOpen()
				if secondStore != nil {
					_ = secondStore.Close()
				}
				if !errors.Is(openErr, ErrJournalInUse) {
					_, _ = killAndWait()
					t.Fatalf("second live Agent Open = %v, want journal-in-use", openErr)
				}
			}
			if err, reaped := killAndWait(); !reaped {
				t.Fatal("child process did not exit after kill within five seconds")
			} else if err == nil {
				t.Fatal("killed child unexpectedly exited successfully")
			}

			store, err := Open(context.Background(), dbPath, "node-test")
			if err != nil {
				t.Fatalf("reopen journal after kill: %v", err)
			}
			defer store.Close()
			recovered, err := store.RecoverInterrupted(context.Background())
			if err != nil {
				t.Fatalf("reconcile interrupted task: %v", err)
			}
			snapshot, err := store.Get(context.Background(), "task-crash")
			if err != nil {
				t.Fatal(err)
			}
			if stage == "queued" {
				if recovered != 0 || snapshot.Status != taskstate.Queued {
					t.Fatalf("pre-execution recovery count=%d status=%s", recovered, snapshot.Status)
				}
				if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("pre-execution checkpoint wrote side-effect marker: %v", err)
				}
				if err := store.BeginExecution(context.Background(), snapshot.TaskID); err != nil {
					t.Fatalf("known-unstarted queued task not resumable: %v", err)
				}
			} else {
				if recovered != 1 || snapshot.Status != taskstate.Unknown {
					t.Fatalf("uncertain recovery count=%d status=%s", recovered, snapshot.Status)
				}
				if err := store.BeginExecution(context.Background(), snapshot.TaskID); err == nil {
					t.Fatal("interrupted task was blindly replayed")
				}
				if _, err := os.Stat(marker); stage == "possible-side-effect" && err != nil {
					t.Fatalf("after-boundary checkpoint marker missing: %v", err)
				} else if stage == "running-before-executor" && !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("pre-executor checkpoint unexpectedly has marker: %v", err)
				}
				if _, err := store.Enqueue(context.Background(), testIdentity("task-blocked-after-restart", "key-blocked-after-restart")); !errors.Is(err, ErrResourceBusy) {
					t.Fatalf("uncertain task resource claim was released: %v", err)
				}
			}
		})
	}
}

func TestRecoverInterruptedIsNodeScoped(t *testing.T) {
	store, _ := openTestStore(t)
	current := testIdentity("task-current-node", "key-current-node")
	if _, err := store.Enqueue(context.Background(), current); err != nil {
		t.Fatal(err)
	}
	if err := store.BeginExecution(context.Background(), current.TaskID); err != nil {
		t.Fatal(err)
	}
	foreign := testIdentity("task-foreign-node", "key-foreign-node")
	foreign.NodeID = "node-foreign"
	digest, err := taskstate.RequestDigest(foreign)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().UnixNano()
	if _, err := store.db.Exec(`INSERT INTO task_journal(task_id,node_id,idempotency_key,request_digest,target_id,resource_key,action,status,created_at_ns,updated_at_ns,execution_attempted)
		VALUES(?,?,?,?,?,?,?,'running',?,?,1)`, foreign.TaskID, foreign.NodeID, foreign.IdempotencyKey, digest[:], foreign.TargetID, foreign.ResourceKey, foreign.Action, now, now); err != nil {
		t.Fatal(err)
	}
	recovered, err := store.RecoverInterrupted(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if recovered != 1 {
		t.Fatalf("recovered %d tasks, want only the owner node's task", recovered)
	}
	var foreignStatus string
	if err := store.db.QueryRow(`SELECT status FROM task_journal WHERE task_id=?`, foreign.TaskID).Scan(&foreignStatus); err != nil {
		t.Fatal(err)
	}
	if foreignStatus != "running" {
		t.Fatalf("foreign node's task was changed to %q", foreignStatus)
	}
}

func TestJournalIdentityPersistsAcrossRestartAndChangesOnRecreation(t *testing.T) {
	path := filepath.Join(privateTempDir(t), "agent", "tasks.sqlite")
	first, err := Open(context.Background(), path, "node-test")
	if err != nil {
		t.Fatal(err)
	}
	firstID := first.JournalID()
	if !validJournalID(firstID) {
		t.Fatalf("new journal ID %q is invalid", firstID)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	restarted, err := Open(context.Background(), path, "node-test")
	if err != nil {
		t.Fatal(err)
	}
	if restarted.JournalID() != firstID {
		t.Fatalf("journal ID changed across same-database restart: %q -> %q", firstID, restarted.JournalID())
	}
	if err := restarted.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(context.Background(), path, "node-other"); !errors.Is(err, ErrWrongNode) {
		t.Fatalf("same database opened under another node = %v, want wrong-node refusal", err)
	}

	for _, suffix := range []string{"", "-wal", "-shm"} {
		if err := os.Remove(path + suffix); err != nil && !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("remove old journal file %q: %v", suffix, err)
		}
	}
	recreated, err := Open(context.Background(), path, "node-test")
	if err != nil {
		t.Fatal(err)
	}
	defer recreated.Close()
	if recreated.JournalID() == firstID {
		t.Fatal("recreated empty journal reused the lost ledger identity")
	}
}

func TestJournalFilePermissionsAndWAL(t *testing.T) {
	directory := privateTempDir(t)
	path := filepath.Join(directory, "journal.sqlite")
	store, err := Open(context.Background(), path, "node-test")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("journal file permissions = %04o, want 0600", info.Mode().Perm())
	}
	var mode string
	if err := store.db.QueryRow(`PRAGMA journal_mode`).Scan(&mode); err != nil {
		t.Fatal(err)
	}
	if mode != "wal" {
		t.Fatalf("SQLite journal mode %q, want wal", mode)
	}
	dirInfo, err := os.Stat(directory)
	if err != nil {
		t.Fatal(err)
	}
	if dirInfo.Mode().Perm() != 0o700 {
		t.Fatalf("journal directory permissions = %04o, want 0700", dirInfo.Mode().Perm())
	}
	if err := verifyPrivateSidecars(path); err != nil {
		t.Fatalf("SQLite sidecar permissions: %v", err)
	}
	if _, err := os.Stat(path + ".lock"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unexpected persistent lockfile: %v", err)
	}
}

func TestOpenRejectsInsecureExistingPathsWithoutChangingThem(t *testing.T) {
	ctx := context.Background()
	unsafeDirectory := filepath.Join(privateTempDir(t), "unsafe")
	if err := os.Mkdir(unsafeDirectory, 0o750); err != nil {
		t.Fatal(err)
	}
	directoryInfo, err := os.Stat(unsafeDirectory)
	if err != nil {
		t.Fatal(err)
	}
	unsafePath := filepath.Join(unsafeDirectory, "journal.sqlite")
	if _, err := Open(ctx, unsafePath, "node-test"); err == nil {
		t.Fatal("Open accepted non-private directory")
	}
	afterDirectory, err := os.Stat(unsafeDirectory)
	if err != nil {
		t.Fatal(err)
	}
	if afterDirectory.Mode().Perm() != directoryInfo.Mode().Perm() {
		t.Fatalf("Open changed unsafe directory mode from %04o to %04o", directoryInfo.Mode().Perm(), afterDirectory.Mode().Perm())
	}

	private := privateTempDir(t)
	wideFile := filepath.Join(private, "wide.sqlite")
	if err := os.WriteFile(wideFile, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(wideFile, 0o640); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(ctx, wideFile, "node-test"); err == nil {
		t.Fatal("Open accepted database file with broad permissions")
	}
	wideInfo, err := os.Stat(wideFile)
	if err != nil {
		t.Fatal(err)
	}
	if wideInfo.Mode().Perm() != 0o640 {
		t.Fatalf("Open changed unsafe file mode to %04o", wideInfo.Mode().Perm())
	}

	symlinkTarget := filepath.Join(private, "target.sqlite")
	if err := os.WriteFile(symlinkTarget, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	symlinkPath := filepath.Join(private, "linked.sqlite")
	if err := os.Symlink(symlinkTarget, symlinkPath); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(ctx, symlinkPath, "node-test"); err == nil {
		t.Fatal("Open accepted database symlink")
	}

	sidecarDirectory := privateTempDir(t)
	sidecarDB := filepath.Join(sidecarDirectory, "journal.sqlite")
	if err := os.WriteFile(sidecarDB+"-wal", nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(sidecarDB+"-wal", 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(ctx, sidecarDB, "node-test"); err == nil {
		t.Fatal("Open accepted public WAL sidecar")
	}
	sidecarInfo, err := os.Stat(sidecarDB + "-wal")
	if err != nil {
		t.Fatal(err)
	}
	if sidecarInfo.Mode().Perm() != 0o644 {
		t.Fatalf("Open changed unsafe WAL mode to %04o", sidecarInfo.Mode().Perm())
	}
}
