package filejournal

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func openTestStore(t *testing.T) *Store {
	t.Helper()
	stateDir := filepath.Join(t.TempDir(), "state")
	if err := os.Mkdir(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	store, err := Open(context.Background(), filepath.Join(stateDir, "file-writes.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func testIntent(taskID, target string) Intent {
	return Intent{TaskID: taskID, Operation: "mkdir", TargetPath: target, BaselineCaptured: true}
}

func testUploadIntent(taskID string, temporary string) Intent {
	return Intent{TaskID: taskID, Operation: "upload", TargetPath: "/files/..draft.bin", ExpectedSize: 0,
		ContentSHA256: strings.Repeat("a", 64), BaselineCaptured: true, TemporaryPath: temporary, TemporaryOwned: true}
}

func TestBeginPersistsUploadTemporaryPathBeforeFileCreationAndAllowsDotNames(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	const taskID = "00000000-0000-4000-8000-000000000041"
	temporary := "/files/...draft.bin.nodedance-upload-0123456789abcdef01234567"
	intent := testUploadIntent(taskID, temporary)
	if _, created, err := store.Begin(ctx, intent); err != nil || !created {
		t.Fatalf("persist reserved upload path: created=%t err=%v", created, err)
	}
	record, err := store.Get(ctx, taskID)
	if err != nil || record.TemporaryPath != temporary || !record.TemporaryOwned || record.Status != Accepted {
		t.Fatalf("upload path ownership was not durable before creation: record=%+v err=%v", record, err)
	}
	if replay, created, err := store.Begin(ctx, testUploadIntent(taskID, "/files/...draft.bin.nodedance-upload-aaaaaaaaaaaaaaaaaaaaaaaa")); err != nil || created || replay.TemporaryPath != temporary {
		t.Fatalf("same upload replay did not reuse persisted Agent path: record=%+v created=%t err=%v", replay, created, err)
	}
	changed := intent
	changed.ContentSHA256 = strings.Repeat("b", 64)
	if _, _, err := store.Begin(ctx, changed); !errors.Is(err, ErrTaskConflict) {
		t.Fatalf("changed upload body was accepted under same task ID: %v", err)
	}
	traversal := intent
	traversal.TemporaryPath = "/files/../outside.nodedance-upload-0123456789abcdef01234567"
	if _, _, err := store.Begin(ctx, traversal); !errors.Is(err, ErrInvalidIntent) {
		t.Fatalf("path-segment traversal was accepted: %v", err)
	}
}

func TestJournalPersistsOnlyContentDigestNotBody(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	const taskID = "00000000-0000-4000-8000-000000000042"
	body := "file-content-must-not-enter-agent-journal-3c674ea2"
	digest := sha256.Sum256([]byte(body))
	intent := Intent{TaskID: taskID, Operation: "save_text", TargetPath: "/private/document.txt", ExpectedSize: int64(len(body)),
		ContentSHA256: hex.EncodeToString(digest[:]), BaselineCaptured: true}
	if _, created, err := store.Begin(ctx, intent); err != nil || !created {
		t.Fatalf("persist body-free file intent: created=%t err=%v", created, err)
	}
	databasePath := store.lock.Name()
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{databasePath, databasePath + "-wal", databasePath + "-shm"} {
		database, err := os.ReadFile(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			t.Fatalf("read Agent journal test database %s: %v", filepath.Base(path), err)
		}
		if strings.Contains(string(database), body) {
			t.Fatal("Agent file journal persisted file contents")
		}
	}
}

func TestTerminalRetentionAndHardCapacityPreserveUnknownAndActive(t *testing.T) {
	store := openTestStore(t)
	store.maxRecords = 3
	ctx := context.Background()
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return now }

	oldTerminal, _, err := store.Begin(ctx, testIntent("00000000-0000-4000-8000-000000000051", "/old-terminal"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Complete(ctx, oldTerminal.TaskID, Succeeded, "verified"); err != nil {
		t.Fatal(err)
	}
	old := now.Add(-TerminalRetention - time.Second).UnixNano()
	if _, err := store.db.Exec(`UPDATE agent_file_journal SET created_at_ns=?,updated_at_ns=? WHERE task_id=?`, old, old, oldTerminal.TaskID); err != nil {
		t.Fatal(err)
	}
	unknown, _, err := store.Begin(ctx, testIntent("00000000-0000-4000-8000-000000000052", "/unknown"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Complete(ctx, unknown.TaskID, Unknown, "result_pending"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE agent_file_journal SET created_at_ns=?,updated_at_ns=? WHERE task_id=?`, old, old, unknown.TaskID); err != nil {
		t.Fatal(err)
	}
	running, _, err := store.Begin(ctx, testIntent("00000000-0000-4000-8000-000000000053", "/running"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.StartMutation(ctx, running.TaskID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE agent_file_journal SET created_at_ns=?,updated_at_ns=? WHERE task_id=?`, old, old, running.TaskID); err != nil {
		t.Fatal(err)
	}

	newIntent := testIntent("00000000-0000-4000-8000-000000000054", "/new-terminal")
	if _, created, err := store.Begin(ctx, newIntent); err != nil || !created {
		t.Fatalf("new operation after retention cleanup: created=%t err=%v", created, err)
	}
	if _, err := store.Get(ctx, oldTerminal.TaskID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expired terminal was retained: err=%v", err)
	}
	for _, taskID := range []string{unknown.TaskID, running.TaskID} {
		if _, err := store.Get(ctx, taskID); err != nil {
			t.Fatalf("active/unknown record %s was pruned: %v", taskID, err)
		}
	}
	if err := store.Complete(ctx, newIntent.TaskID, Succeeded, "verified"); err != nil {
		t.Fatal(err)
	}
	if _, created, err := store.Begin(ctx, testIntent("00000000-0000-4000-8000-000000000055", "/at-cap")); !errors.Is(err, ErrCapacity) || created {
		t.Fatalf("hard-cap did not reject a write without pruning in-window terminal: created=%t err=%v", created, err)
	}
	if _, err := store.Get(ctx, unknown.TaskID); err != nil {
		t.Fatalf("hard-cap cleanup evicted unknown result: %v", err)
	}
	if _, err := store.Get(ctx, running.TaskID); err != nil {
		t.Fatalf("hard-cap cleanup evicted running operation: %v", err)
	}
	if _, err := store.Get(ctx, newIntent.TaskID); err != nil {
		t.Fatalf("hard-cap cleanup evicted in-window terminal: %v", err)
	}
	if _, err := store.db.Exec(`UPDATE agent_file_journal SET updated_at_ns=? WHERE task_id=?`, old, newIntent.TaskID); err != nil {
		t.Fatal(err)
	}
	if _, created, err := store.Begin(ctx, testIntent("00000000-0000-4000-8000-000000000055", "/after-expiry")); err != nil || !created {
		t.Fatalf("expired terminal was not pruned to restore capacity: created=%t err=%v", created, err)
	}
	if _, err := store.Get(ctx, newIntent.TaskID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("terminal older than retention was retained after capacity cleanup: %v", err)
	}

	activeOnly := openTestStore(t)
	activeOnly.maxRecords = 2
	activeUnknown, _, err := activeOnly.Begin(ctx, testIntent("00000000-0000-4000-8000-000000000056", "/active-unknown"))
	if err != nil {
		t.Fatal(err)
	}
	if err := activeOnly.Complete(ctx, activeUnknown.TaskID, Unknown, "result_pending"); err != nil {
		t.Fatal(err)
	}
	activeRunning, _, err := activeOnly.Begin(ctx, testIntent("00000000-0000-4000-8000-000000000057", "/active-running"))
	if err != nil {
		t.Fatal(err)
	}
	if err := activeOnly.StartMutation(ctx, activeRunning.TaskID); err != nil {
		t.Fatal(err)
	}
	if _, created, err := activeOnly.Begin(ctx, testIntent("00000000-0000-4000-8000-000000000058", "/capacity-rejected")); !errors.Is(err, ErrCapacity) || created {
		t.Fatalf("full active/unknown journal did not backpressure new operation: created=%t err=%v", created, err)
	}
	var count int
	if err := activeOnly.db.QueryRow(`SELECT COUNT(*) FROM agent_file_journal`).Scan(&count); err != nil || count != 2 {
		t.Fatalf("capacity rejection changed journal rows: count=%d err=%v", count, err)
	}
}
