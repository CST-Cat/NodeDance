package filetasks_test

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/CST-Cat/NodeDance/internal/core/filetasks"
	"github.com/CST-Cat/NodeDance/internal/core/storage"
	"github.com/CST-Cat/NodeDance/internal/taskstate"
)

const (
	testNodeID = "00000000-0000-4000-8000-000000000001"
	taskOneID  = "00000000-0000-4000-8000-000000000011"
	taskTwoID  = "00000000-0000-4000-8000-000000000012"
	taskTriID  = "00000000-0000-4000-8000-000000000013"
)

func openStore(t *testing.T, dir string, now func() time.Time) (*storage.Store, *filetasks.Store) {
	t.Helper()
	store, err := storage.Open(context.Background(), dir)
	if err != nil {
		t.Fatal("open real SQLite Core storage:", err)
	}
	if _, err := store.DB.Exec(`INSERT INTO nodes(id,display_name,status,created_at,updated_at) VALUES(?, 'test', 'pending', 1, 1)`, testNodeID); err != nil {
		_ = store.Close()
		t.Fatal("insert task node:", err)
	}
	tasks, err := filetasks.New(store.DB, now)
	if err != nil {
		_ = store.Close()
		t.Fatal("create file task store:", err)
	}
	return store, tasks
}

func TestDurableFileTaskLifecycleStoresOnlyAllowlistedMetadata(t *testing.T) {
	ctx := context.Background()
	clock := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	store, tasks := openStore(t, filepath.Join(t.TempDir(), "core"), func() time.Time { return clock })
	defer store.Close()
	actor := sql.NullInt64{Int64: 1, Valid: true}

	task, err := tasks.Create(ctx, filetasks.CreateRequest{TaskID: taskOneID, NodeID: testNodeID, Operation: filetasks.OperationSaveText,
		TargetPath: "/srv/secret.txt", ActorID: actor, RemoteAddr: "127.0.0.1"})
	if err != nil || task.Status != taskstate.Queued {
		t.Fatalf("create file task=%+v err=%v; want durable queued task", task, err)
	}
	var contentColumns int
	if err := store.DB.QueryRow(`SELECT count(*) FROM pragma_table_info('file_write_tasks') WHERE lower(name) LIKE '%text%' OR lower(name) LIKE '%content%' OR lower(name) LIKE '%data%'`).Scan(&contentColumns); err != nil || contentColumns != 0 {
		t.Fatalf("file task table contains content-bearing columns=%d err=%v", contentColumns, err)
	}
	clock = clock.Add(time.Second)
	if err := tasks.MarkDispatched(ctx, testNodeID, taskOneID, actor, "127.0.0.1"); err != nil {
		t.Fatal("persist dispatch boundary:", err)
	}
	dispatched, err := tasks.Get(ctx, testNodeID, taskOneID)
	if err != nil || dispatched.Status != taskstate.Queued || dispatched.DispatchStartedAt == nil || dispatched.StartedAt != nil {
		t.Fatalf("dispatch boundary forged started status/time: task=%+v err=%v", dispatched, err)
	}
	clock = clock.Add(time.Second)
	if err := tasks.MarkRunning(ctx, testNodeID, taskOneID, actor, "127.0.0.1"); err != nil {
		t.Fatal("Agent-confirmed start transition:", err)
	}
	running, err := tasks.Get(ctx, testNodeID, taskOneID)
	if err != nil || running.Status != taskstate.Running || running.StartedAt == nil || running.DispatchStartedAt == nil {
		t.Fatalf("Agent begin acknowledgment did not persist running: task=%+v err=%v", running, err)
	}
	clock = clock.Add(2 * time.Second)
	if err := tasks.Resolve(ctx, testNodeID, taskOneID, taskstate.Succeeded, "verified", actor, "127.0.0.1"); err != nil {
		t.Fatal("resolve verified file result:", err)
	}
	completed, err := tasks.Get(ctx, testNodeID, taskOneID)
	if err != nil || completed.Status != taskstate.Succeeded || completed.ResultCode != "verified" || completed.StartedAt == nil || completed.FinishedAt == nil || !completed.FinishedAt.After(*completed.StartedAt) {
		t.Fatalf("verified result was not durable: task=%+v err=%v", completed, err)
	}
	events, err := tasks.AuditEvents(ctx, testNodeID, taskOneID)
	if err != nil || len(events) != 4 || events[0].Event != "accepted" || events[1].Event != "dispatch_started" || events[2].Event != "agent_started" || events[3].ToStatus.String != "succeeded" {
		t.Fatalf("task transition audit=%+v err=%v", events, err)
	}
	var accepted, succeeded int
	if err := store.DB.QueryRow(`SELECT count(*) FROM audit_entries WHERE action='file_save_text' AND target_kind='file' AND outcome='accepted'`).Scan(&accepted); err != nil {
		t.Fatal(err)
	}
	if err := store.DB.QueryRow(`SELECT count(*) FROM audit_entries WHERE action='file_save_text' AND target_kind='file' AND outcome='succeeded'`).Scan(&succeeded); err != nil {
		t.Fatal(err)
	}
	if accepted != 1 || succeeded != 1 {
		t.Fatalf("audit entries accepted=%d succeeded=%d; expected task/audit outcomes to match", accepted, succeeded)
	}
	var leaked int
	if err := store.DB.QueryRow(`SELECT count(*) FROM file_write_tasks WHERE task_id=? AND (target_path LIKE '%text-body%' OR new_path LIKE '%text-body%')`, taskOneID).Scan(&leaked); err != nil || leaked != 0 {
		t.Fatalf("file bytes leaked into task metadata count=%d err=%v", leaked, err)
	}
}

func TestRestartMarksDispatchedWritesUnknownAndNeverRequeues(t *testing.T) {
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "core")
	store, tasks := openStore(t, dir, nil)
	actor := sql.NullInt64{Int64: 1, Valid: true}
	if _, err := tasks.Create(ctx, filetasks.CreateRequest{TaskID: taskOneID, NodeID: testNodeID, Operation: filetasks.OperationDelete, TargetPath: "/tmp/not-sent", ActorID: actor}); err != nil {
		t.Fatal("create never-dispatched task:", err)
	}
	if _, err := tasks.Create(ctx, filetasks.CreateRequest{TaskID: taskTwoID, NodeID: testNodeID, Operation: filetasks.OperationMkdir, TargetPath: "/tmp/may-exist", ActorID: actor}); err != nil {
		t.Fatal("create dispatched task:", err)
	}
	if err := tasks.MarkDispatched(ctx, testNodeID, taskTwoID, actor, "127.0.0.1"); err != nil {
		t.Fatal("mark dispatched task:", err)
	}
	if _, err := tasks.Create(ctx, filetasks.CreateRequest{TaskID: taskTriID, NodeID: testNodeID, Operation: filetasks.OperationUpload, TargetPath: "/tmp/in-progress", ActorID: actor}); err != nil {
		t.Fatal("create running task:", err)
	}
	if err := tasks.MarkDispatched(ctx, testNodeID, taskTriID, actor, "127.0.0.1"); err != nil {
		t.Fatal(err)
	}
	if err := tasks.MarkRunning(ctx, testNodeID, taskTriID, actor, "127.0.0.1"); err != nil {
		t.Fatal("mark Agent-acknowledged upload running:", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal("close Core database before restart:", err)
	}

	reopened, tasks := openStoreAfterRestart(t, dir)
	defer reopened.Close()
	if err := tasks.RecoverUnfinished(ctx); err != nil {
		t.Fatal("recover unfinished file tasks:", err)
	}
	for id, want := range map[string]taskstate.Status{taskOneID: taskstate.Failed, taskTwoID: taskstate.Unknown, taskTriID: taskstate.Unknown} {
		got, err := tasks.Get(ctx, testNodeID, id)
		if err != nil || got.Status != want {
			t.Fatalf("recovered task %s=%+v err=%v; want %s", id, got, err, want)
		}
		if want == taskstate.Unknown && got.ResultCode != "result_pending" {
			t.Fatalf("dispatched task %s result code=%q want result_pending", id, got.ResultCode)
		}
		if want == taskstate.Failed && got.ResultCode != "not_dispatched" {
			t.Fatalf("unsent task result code=%q want not_dispatched", got.ResultCode)
		}
	}
	if err := tasks.RecoverUnfinished(ctx); err != nil {
		t.Fatal("repeat recovery:", err)
	}
	var recoveredEvents int
	if err := reopened.DB.QueryRow(`SELECT count(*) FROM file_write_task_events WHERE event='task_recovered'`).Scan(&recoveredEvents); err != nil || recoveredEvents != 1 {
		t.Fatalf("recovery events=%d err=%v; expected only never-sent task to fail as recovered", recoveredEvents, err)
	}
}

func openStoreAfterRestart(t *testing.T, dir string) (*storage.Store, *filetasks.Store) {
	t.Helper()
	store, err := storage.Open(context.Background(), dir)
	if err != nil {
		t.Fatal("reopen Core storage:", err)
	}
	tasks, err := filetasks.New(store.DB, nil)
	if err != nil {
		_ = store.Close()
		t.Fatal("recreate file task store after restart:", err)
	}
	return store, tasks
}

func TestFileTaskLookupIsScopedToNodeAndMissingTasksAreNotFound(t *testing.T) {
	ctx := context.Background()
	store, tasks := openStore(t, filepath.Join(t.TempDir(), "core"), nil)
	defer store.Close()
	if _, err := tasks.Get(ctx, "00000000-0000-4000-8000-000000000099", taskOneID); !errors.Is(err, filetasks.ErrNotFound) {
		t.Fatalf("cross-node lookup error=%v, want not found", err)
	}
	if _, err := tasks.Get(ctx, testNodeID, "not-a-task-id"); !errors.Is(err, filetasks.ErrInvalidRequest) {
		t.Fatalf("invalid task ID error=%v, want invalid request", err)
	}
}
