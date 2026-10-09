package composeedit

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/CST-Cat/NodeDance/internal/protocol"
	_ "modernc.org/sqlite"
)

const editorTestNodeID = "b738a2d2-a255-4912-9e2e-26f974ac2529"

func TestContentFreeIdempotentOperationLedgerAndRecovery(t *testing.T) {
	ctx := context.Background()
	db, store, project := editorTestStore(t)
	if recovered, err := store.RecoverUnfinished(ctx); err != nil || recovered != 0 {
		t.Fatalf("unavailable later-stage migration must be a no-op: recovered=%d err=%v", recovered, err)
	}
	for _, statement := range SchemaStatements() {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			t.Fatalf("apply Compose editor schema %q: %v", statement, err)
		}
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO compose_projects(node_id,project_key,project_name,working_directory,config_files_json,config_available,discovered_at,last_seen_at) VALUES(?,?,?,?,?,1,1,1)`, editorTestNodeID, project.Key, project.Name, project.WorkingDirectory, `[]string{"/srv/compose/compose.yaml"}`); err != nil {
		t.Fatal(err)
	}
	store, err := NewStore(db, Options{Now: func() time.Time { return time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC) }})
	if err != nil {
		t.Fatal(err)
	}

	request := editorTestRequest(project, "00000000-0000-4000-8000-000000000001", "18081")
	first, created, err := store.Enqueue(ctx, editorTestNodeID, project.Key, "edit-1", request, sql.NullInt64{Int64: 1, Valid: true}, "192.0.2.8")
	if err != nil || !created || first.Status != StatusQueued {
		t.Fatalf("enqueue editor operation: operation=%+v created=%t err=%v", first, created, err)
	}
	if _, err := store.SetStatus(ctx, editorTestNodeID, first.ID, StatusRunning, "", false, nil); err != nil {
		t.Fatal(err)
	}
	// The transport operation ID changes on a retry. It must not affect the
	// idempotency digest or cause the source transaction to be dispatched twice.
	retry := editorTestRequest(project, "00000000-0000-4000-8000-000000000002", "18081")
	duplicate, created, err := store.Enqueue(ctx, editorTestNodeID, project.Key, "edit-1", retry, sql.NullInt64{Int64: 1, Valid: true}, "192.0.2.8")
	if err != nil || created || duplicate.ID != first.ID || duplicate.Status != StatusRunning {
		t.Fatalf("same request was not deduplicated: operation=%+v created=%t err=%v", duplicate, created, err)
	}
	conflicting := editorTestRequest(project, "00000000-0000-4000-8000-000000000003", "18082")
	if _, _, err := store.Enqueue(ctx, editorTestNodeID, project.Key, "edit-1", conflicting, sql.NullInt64{Int64: 1, Valid: true}, "192.0.2.8"); !errors.Is(err, ErrConflict) {
		t.Fatalf("same key with different source request must conflict, got %v", err)
	}

	if recovered, err := store.RecoverUnfinished(ctx); err != nil || recovered != 1 {
		t.Fatalf("recover interrupted write without replay: recovered=%d err=%v", recovered, err)
	}
	operation, err := store.Get(ctx, editorTestNodeID, first.ID)
	if err != nil || operation.Status != StatusUnknown || operation.ErrorCode != "result_pending" || operation.Verified {
		t.Fatalf("interrupted operation was not marked unknown: %+v err=%v", operation, err)
	}
	pending, err := store.PendingResultChecks(ctx, editorTestNodeID)
	if err != nil || len(pending) != 1 || pending[0].ID != first.ID {
		t.Fatalf("ambiguous result was not queued for Agent read-only reconciliation: pending=%+v err=%v", pending, err)
	}
	events, err := store.Events(ctx, editorTestNodeID, first.ID)
	if err != nil || len(events) != 3 || events[2].Event != "result_pending" || events[2].ToStatus != string(StatusUnknown) {
		t.Fatalf("recovery event is missing: events=%+v err=%v", events, err)
	}
	if recovered, err := store.RecoverUnfinished(ctx); err != nil || recovered != 0 {
		t.Fatalf("recovery was not idempotent: recovered=%d err=%v", recovered, err)
	}

	// Source YAML, resolved output, paths, and arbitrary response content must
	// never be copied into the Core transaction ledger.
	var operationDump string
	if err := db.QueryRowContext(ctx, `SELECT request_digest || summary_json FROM compose_editor_operations WHERE operation_id=?`, first.ID).Scan(&operationDump); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(operationDump, "TOP-SECRET") || strings.Contains(operationDump, "services:") || strings.Contains(operationDump, "/srv/compose") {
		t.Fatalf("Core editor ledger contains source content: %q", operationDump)
	}
}

func TestComposeEditorStoreRequiresExplicitMigration(t *testing.T) {
	db, store, _ := editorTestStore(t)
	if _, err := store.Get(context.Background(), editorTestNodeID, "00000000-0000-4000-8000-000000000001"); !errors.Is(err, ErrSchemaUnavailable) {
		t.Fatalf("missing migration should remain unavailable rather than silently enabling S11: %v", err)
	}
	if _, err := db.Exec(`SELECT 1 FROM compose_editor_operations`); err == nil {
		t.Fatal("Compose editor schema was installed outside the explicit migration owner")
	}
}

func editorTestStore(t *testing.T) (*sql.DB, *Store, protocol.ComposeProjectRef) {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "editor.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`PRAGMA foreign_keys=ON;
		CREATE TABLE nodes(id TEXT PRIMARY KEY);
		INSERT INTO nodes(id) VALUES('` + editorTestNodeID + `');
		CREATE TABLE compose_projects(node_id TEXT NOT NULL, project_key TEXT NOT NULL, project_name TEXT NOT NULL,
			working_directory TEXT NOT NULL, config_files_json TEXT NOT NULL, config_available INTEGER NOT NULL,
			discovered_at INTEGER NOT NULL, last_seen_at INTEGER NOT NULL, PRIMARY KEY(node_id,project_key));
		CREATE TABLE audit_entries(id INTEGER PRIMARY KEY, occurred_at INTEGER NOT NULL, action TEXT NOT NULL,
			outcome TEXT NOT NULL, actor_id INTEGER, remote_addr TEXT NOT NULL, target_kind TEXT, target_id TEXT);`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store, err := NewStore(db, Options{})
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "compose project")
	files := []string{filepath.Join(dir, "compose.yaml")}
	project := protocol.ComposeProjectRef{Name: "nodedance-demo", WorkingDirectory: dir, ConfigFiles: files}
	project.Key = protocol.ComposeProjectKey(project.Name, project.WorkingDirectory, files)
	return db, store, project
}

func editorTestRequest(project protocol.ComposeProjectRef, operationID, port string) protocol.ComposeRequest {
	content := "services:\n  web:\n    image: nginx:locked\n    ports:\n      - 127.0.0.1:" + port + ":80\n# TOP-SECRET source fixture\n"
	return protocol.ComposeRequest{OperationID: operationID, Action: protocol.ComposeEditApply, Project: project,
		Editor: &protocol.ComposeEditorInput{Files: []protocol.ComposeSourceFile{{Path: project.ConfigFiles[0], Content: content}},
			ExpectedVersions: map[string]string{project.ConfigFiles[0]: strings.Repeat("a", 64)}}}
}
