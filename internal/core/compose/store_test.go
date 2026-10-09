package compose

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/CST-Cat/NodeDance/internal/protocol"
	_ "modernc.org/sqlite"
)

const testNodeID = "b738a2d2-a255-4912-9e2e-26f974ac2529"

func TestComposeStoreRequiresExplicitMigrationAndPreservesProjectAfterDown(t *testing.T) {
	ctx := context.Background()
	db := testComposeDB(t)
	storeBeforeMigration, err := NewStore(db, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if recovered, err := storeBeforeMigration.RecoverUnfinished(ctx); err != nil || recovered != 0 {
		t.Fatalf("recovery without later-stage migration should be a no-op: recovered=%d err=%v", recovered, err)
	}
	applyComposeSchema(t, db)
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	store, err := NewStore(db, Options{Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	project := testProjectValue(t)
	if err := store.RegisterProjects(ctx, testNodeID, []protocol.ComposeProject{project}); err != nil {
		t.Fatal(err)
	}
	if err := store.RegisterProjects(ctx, testNodeID, nil); err != nil {
		t.Fatal(err)
	}
	projects, err := store.Projects(ctx, testNodeID)
	if err != nil || len(projects) != 1 {
		t.Fatalf("down removed durable project context: projects=%+v err=%v", projects, err)
	}
	if projects[0].Value.Ref.Key != project.Ref.Key || !projects[0].Value.ConfigAvailable {
		t.Fatalf("stored project context changed: %+v", projects[0])
	}
}

func TestRecoverUnfinishedMarksAmbiguousWritesUnknownWithoutReplay(t *testing.T) {
	ctx := context.Background()
	db := testComposeDB(t)
	applyComposeSchema(t, db)
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	store, err := NewStore(db, Options{Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	project := testProjectValue(t)
	if err := store.RegisterProjects(ctx, testNodeID, []protocol.ComposeProject{project}); err != nil {
		t.Fatal(err)
	}
	queuedRequest := protocol.ComposeRequest{OperationID: "00000000-0000-4000-8000-000000000011", Action: protocol.ComposeUp, Project: project.Ref}
	queued, created, err := store.Enqueue(ctx, testNodeID, "compose-recovery-queued", queuedRequest, sql.NullInt64{Int64: 1, Valid: true}, "192.0.2.20")
	if err != nil || !created {
		t.Fatalf("enqueue queued operation: %+v created=%t err=%v", queued, created, err)
	}
	runningRequest := protocol.ComposeRequest{OperationID: "00000000-0000-4000-8000-000000000012", Action: protocol.ComposeDown, Project: project.Ref}
	running, created, err := store.Enqueue(ctx, testNodeID, "compose-recovery-running", runningRequest, sql.NullInt64{Int64: 1, Valid: true}, "192.0.2.20")
	if err != nil || !created {
		t.Fatalf("enqueue running operation: %+v created=%t err=%v", running, created, err)
	}
	if _, err := store.SetStatus(ctx, testNodeID, running.ID, StatusRunning, "", false); err != nil {
		t.Fatal(err)
	}
	if recovered, err := store.RecoverUnfinished(ctx); err != nil || recovered != 2 {
		t.Fatalf("recover unfinished operations: recovered=%d err=%v", recovered, err)
	}
	for _, operationID := range []string{queued.ID, running.ID} {
		operation, err := store.Get(ctx, testNodeID, operationID)
		if err != nil || operation.Status != StatusUnknown || operation.ErrorCode != "result_pending" || operation.Verified {
			t.Fatalf("operation %s was not moved to result_pending: %+v err=%v", operationID, operation, err)
		}
		events, err := store.Events(ctx, testNodeID, operationID)
		if err != nil || events[len(events)-1].Event != "result_pending" || events[len(events)-1].ToStatus != string(StatusUnknown) {
			t.Fatalf("operation %s has no recovery event: %+v err=%v", operationID, events, err)
		}
	}
	if recovered, err := store.RecoverUnfinished(ctx); err != nil || recovered != 0 {
		t.Fatalf("recovery is not idempotent: recovered=%d err=%v", recovered, err)
	}
	pending, err := store.PendingResultChecks(ctx, testNodeID)
	if err != nil || len(pending) != 2 {
		t.Fatalf("read-only reconciliation candidates were not persisted: operations=%+v err=%v", pending, err)
	}
	var unknownAuditCount int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM audit_entries WHERE action='compose_operation' AND outcome='unknown' AND target_id=?`, testNodeID).Scan(&unknownAuditCount); err != nil || unknownAuditCount != 2 {
		t.Fatalf("recovery outcomes were not audited: count=%d err=%v", unknownAuditCount, err)
	}
	// The durable record remains unknown; recovery must not enqueue or replay
	// the original write action.
	duplicate, created, err := store.Enqueue(ctx, testNodeID, "compose-recovery-running", runningRequest, sql.NullInt64{Int64: 1, Valid: true}, "192.0.2.20")
	if err != nil || created || duplicate.ID != running.ID || duplicate.Status != StatusUnknown {
		t.Fatalf("idempotent retry changed/replayed recovered operation: %+v created=%t err=%v", duplicate, created, err)
	}
}

func TestComposeOperationIdempotencyAuditAndVerifiedTerminalRules(t *testing.T) {
	ctx := context.Background()
	db := testComposeDB(t)
	applyComposeSchema(t, db)
	store, err := NewStore(db, Options{Now: func() time.Time { return time.Unix(200, 0).UTC() }})
	if err != nil {
		t.Fatal(err)
	}
	project := testProjectValue(t)
	if err := store.RegisterProjects(ctx, testNodeID, []protocol.ComposeProject{project}); err != nil {
		t.Fatal(err)
	}
	request := protocol.ComposeRequest{OperationID: "00000000-0000-4000-8000-000000000001", Action: protocol.ComposeUp, Project: project.Ref}
	first, created, err := store.Enqueue(ctx, testNodeID, "compose-idem-1", request, sql.NullInt64{Int64: 1, Valid: true}, "192.0.2.20")
	if err != nil || !created || first.Status != StatusQueued {
		t.Fatalf("enqueue: operation=%+v created=%t err=%v", first, created, err)
	}
	request.OperationID = "00000000-0000-4000-8000-000000000002"
	duplicate, created, err := store.Enqueue(ctx, testNodeID, "compose-idem-1", request, sql.NullInt64{Int64: 1, Valid: true}, "192.0.2.20")
	if err != nil || created || duplicate.ID != first.ID {
		t.Fatalf("same request was not deduplicated: %+v %t %v", duplicate, created, err)
	}
	request.Action = protocol.ComposeDown
	if _, _, err := store.Enqueue(ctx, testNodeID, "compose-idem-1", request, sql.NullInt64{Int64: 1, Valid: true}, "192.0.2.20"); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("idempotency key reused for different action: %v", err)
	}
	if _, err := store.SetStatus(ctx, testNodeID, first.ID, StatusSucceeded, "", false); err == nil {
		t.Fatal("unverified Compose action was marked successful")
	}
	if _, err := store.SetStatus(ctx, testNodeID, first.ID, StatusRunning, "", false); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SetStatus(ctx, testNodeID, first.ID, StatusSucceeded, "", true); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SetStatus(ctx, testNodeID, first.ID, StatusFailed, "compose_failed", false); !errors.Is(err, ErrOperationConflict) {
		t.Fatalf("terminal result was overwritten: %v", err)
	}
	events, err := store.Events(ctx, testNodeID, first.ID)
	if err != nil || len(events) != 3 || events[0].Event != "accepted" || events[1].Event != "dispatched" || events[2].Event != "verified" {
		t.Fatalf("operation event trail incomplete: %+v err=%v", events, err)
	}
	var auditCount int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM audit_entries WHERE action='compose_operation' AND outcome='accepted' AND target_id=?`, testNodeID).Scan(&auditCount); err != nil || auditCount != 1 {
		t.Fatalf("acceptance audit missing or duplicated: count=%d err=%v", auditCount, err)
	}
}

func testComposeDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "compose.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`PRAGMA foreign_keys=ON;
		CREATE TABLE nodes(id TEXT PRIMARY KEY);
		INSERT INTO nodes(id) VALUES('` + testNodeID + `');
		CREATE TABLE audit_entries(id INTEGER PRIMARY KEY, occurred_at INTEGER NOT NULL, action TEXT NOT NULL,
			outcome TEXT NOT NULL, actor_id INTEGER, remote_addr TEXT NOT NULL, target_kind TEXT, target_id TEXT);`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func applyComposeSchema(t *testing.T, db *sql.DB) {
	t.Helper()
	for _, statement := range SchemaStatements() {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("apply explicit Compose schema statement %q: %v", statement, err)
		}
	}
}

func testProjectValue(t *testing.T) protocol.ComposeProject {
	t.Helper()
	directory := filepath.Join(t.TempDir(), "compose project")
	files := []string{filepath.Join(directory, "compose.yaml"), filepath.Join(directory, "compose.override.yaml")}
	ref := protocol.ComposeProjectRef{Name: "nodedance-demo", WorkingDirectory: directory, ConfigFiles: files}
	ref.Key = protocol.ComposeProjectKey(ref.Name, ref.WorkingDirectory, files)
	return protocol.ComposeProject{Ref: ref, ConfigAvailable: true, Services: []protocol.ComposeService{}}
}
