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

func TestIdempotentCreateReturnsExistingTaskAndConflictsOnChangedIntent(t *testing.T) {
	ctx := context.Background()
	store, tasks := openStore(t, filepath.Join(t.TempDir(), "core"), nil)
	defer store.Close()
	actor := sql.NullInt64{Int64: 1, Valid: true}
	intent := filetasks.Intent{Operation: filetasks.OperationSaveText, TargetPath: "/srv/app.conf", ExpectedVersion: "version-1", ContentSHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}
	digest, err := filetasks.DigestIntent(intent)
	if err != nil {
		t.Fatal("digest file intent:", err)
	}
	first, created, err := tasks.CreateIdempotent(ctx, filetasks.CreateRequest{TaskID: taskOneID, NodeID: testNodeID,
		IdempotencyKey: "save-once", RequestDigest: digest, Operation: filetasks.OperationSaveText, TargetPath: intent.TargetPath,
		ActorID: actor, RemoteAddr: "127.0.0.1"})
	if err != nil || !created || first.TaskID != taskOneID {
		t.Fatalf("first idempotent create task=%+v created=%t err=%v", first, created, err)
	}
	second, created, err := tasks.CreateIdempotent(ctx, filetasks.CreateRequest{TaskID: taskTwoID, NodeID: testNodeID,
		IdempotencyKey: "save-once", RequestDigest: digest, Operation: filetasks.OperationSaveText, TargetPath: intent.TargetPath,
		ActorID: actor, RemoteAddr: "127.0.0.1"})
	if err != nil || created || second.TaskID != first.TaskID {
		t.Fatalf("same key/digest did not reuse the original task: task=%+v created=%t err=%v", second, created, err)
	}
	changedDigest, err := filetasks.DigestIntent(filetasks.Intent{Operation: intent.Operation, TargetPath: intent.TargetPath,
		ExpectedVersion: intent.ExpectedVersion, ContentSHA256: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"})
	if err != nil {
		t.Fatal("digest changed file intent:", err)
	}
	if _, _, err := tasks.CreateIdempotent(ctx, filetasks.CreateRequest{TaskID: taskTriID, NodeID: testNodeID,
		IdempotencyKey: "save-once", RequestDigest: changedDigest, Operation: filetasks.OperationSaveText, TargetPath: intent.TargetPath,
		ActorID: actor, RemoteAddr: "127.0.0.1"}); !errors.Is(err, filetasks.ErrIdempotencyConflict) {
		t.Fatalf("same key with changed content digest error=%v; want conflict", err)
	}
	var rows, accepted int
	if err := store.DB.QueryRow(`SELECT count(*) FROM file_write_tasks WHERE node_id=? AND idempotency_key='save-once'`, testNodeID).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if err := store.DB.QueryRow(`SELECT count(*) FROM audit_entries WHERE action='file_save_text' AND outcome='accepted'`).Scan(&accepted); err != nil {
		t.Fatal(err)
	}
	if rows != 1 || accepted != 1 {
		t.Fatalf("idempotent replay duplicated task/audit: tasks=%d accepted=%d", rows, accepted)
	}
}

func TestCanceledRequiresConfirmedAgentCancellation(t *testing.T) {
	ctx := context.Background()
	store, tasks := openStore(t, filepath.Join(t.TempDir(), "core"), nil)
	defer store.Close()
	actor := sql.NullInt64{Int64: 1, Valid: true}
	if _, err := tasks.Create(ctx, filetasks.CreateRequest{TaskID: taskOneID, NodeID: testNodeID, Operation: filetasks.OperationUpload,
		TargetPath: "/srv/upload.bin", ActorID: actor}); err != nil {
		t.Fatal("create upload task:", err)
	}
	if err := tasks.MarkDispatched(ctx, testNodeID, taskOneID, actor, "127.0.0.1"); err != nil {
		t.Fatal("mark upload dispatched:", err)
	}
	if err := tasks.MarkRunning(ctx, testNodeID, taskOneID, actor, "127.0.0.1"); err != nil {
		t.Fatal("record Agent upload-begin ACK:", err)
	}
	if err := tasks.Resolve(ctx, testNodeID, taskOneID, taskstate.Canceled, "cancel_confirmed", actor, "127.0.0.1"); err != nil {
		t.Fatal("resolve Agent-confirmed cancellation:", err)
	}
	canceled, err := tasks.Get(ctx, testNodeID, taskOneID)
	if err != nil || canceled.Status != taskstate.Canceled || canceled.ResultCode != "cancel_confirmed" || canceled.FinishedAt == nil {
		t.Fatalf("confirmed cancellation state=%+v err=%v", canceled, err)
	}
	events, err := tasks.AuditEvents(ctx, testNodeID, taskOneID)
	if err != nil || len(events) != 4 || events[2].ToStatus.String != string(taskstate.Running) || events[3].ToStatus.String != string(taskstate.Canceled) {
		t.Fatalf("confirmed cancellation event history=%+v err=%v", events, err)
	}
	if _, err := tasks.Create(ctx, filetasks.CreateRequest{TaskID: taskTwoID, NodeID: testNodeID, Operation: filetasks.OperationUpload,
		TargetPath: "/srv/uncertain-upload.bin", ActorID: actor}); err != nil {
		t.Fatal("create second upload task:", err)
	}
	if err := tasks.MarkDispatched(ctx, testNodeID, taskTwoID, actor, "127.0.0.1"); err != nil {
		t.Fatal("mark second upload dispatched:", err)
	}
	if err := tasks.MarkRunning(ctx, testNodeID, taskTwoID, actor, "127.0.0.1"); err != nil {
		t.Fatal("record second Agent upload-begin ACK:", err)
	}
	if err := tasks.Resolve(ctx, testNodeID, taskTwoID, taskstate.Canceled, "not_committed", actor, "127.0.0.1"); !errors.Is(err, filetasks.ErrInvalidRequest) {
		t.Fatalf("cancellation without typed Agent confirmation error=%v, want invalid request", err)
	}
	stillRunning, err := tasks.Get(ctx, testNodeID, taskTwoID)
	if err != nil || stillRunning.Status != taskstate.Running {
		t.Fatalf("unconfirmed cancellation changed task state: task=%+v err=%v", stillRunning, err)
	}
}

func TestAgentConfirmedMutationUncertaintyRecordsRunningBeforeUnknown(t *testing.T) {
	ctx := context.Background()
	store, tasks := openStore(t, filepath.Join(t.TempDir(), "core"), nil)
	defer store.Close()
	actor := sql.NullInt64{Int64: 1, Valid: true}
	if _, err := tasks.Create(ctx, filetasks.CreateRequest{TaskID: taskOneID, NodeID: testNodeID, Operation: filetasks.OperationSaveText,
		TargetPath: "/srv/app.conf", ActorID: actor}); err != nil {
		t.Fatal("create save task:", err)
	}
	if err := tasks.MarkDispatched(ctx, testNodeID, taskOneID, actor, "127.0.0.1"); err != nil {
		t.Fatal("mark save dispatched:", err)
	}
	if err := tasks.Resolve(ctx, testNodeID, taskOneID, taskstate.Unknown, "mutation_uncertain", actor, "127.0.0.1"); err != nil {
		t.Fatal("record Agent-confirmed uncertain mutation:", err)
	}
	task, err := tasks.Get(ctx, testNodeID, taskOneID)
	if err != nil || task.Status != taskstate.Unknown || task.ResultCode != "mutation_uncertain" || task.StartedAt == nil {
		t.Fatalf("uncertain mutation state=%+v err=%v; Agent response should prove start", task, err)
	}
	events, err := tasks.AuditEvents(ctx, testNodeID, taskOneID)
	if err != nil || len(events) != 4 || events[2].Event != "agent_started" || events[2].ToStatus.String != string(taskstate.Running) || events[3].ToStatus.String != string(taskstate.Unknown) {
		t.Fatalf("uncertain mutation event history=%+v err=%v", events, err)
	}
	var unknownAudits int
	if err := store.DB.QueryRow(`SELECT count(*) FROM audit_entries WHERE action='file_save_text' AND outcome='unknown'`).Scan(&unknownAudits); err != nil || unknownAudits != 1 {
		t.Fatalf("uncertain mutation terminal audit count=%d err=%v", unknownAudits, err)
	}
	if _, err := tasks.Create(ctx, filetasks.CreateRequest{TaskID: taskTwoID, NodeID: testNodeID, Operation: filetasks.OperationMkdir,
		TargetPath: "/srv/existing", ActorID: actor}); err != nil {
		t.Fatal("create mkdir task:", err)
	}
	if err := tasks.MarkDispatched(ctx, testNodeID, taskTwoID, actor, "127.0.0.1"); err != nil {
		t.Fatal("mark mkdir dispatched:", err)
	}
	if err := tasks.Resolve(ctx, testNodeID, taskTwoID, taskstate.Failed, "agent_rejected", actor, "127.0.0.1"); err != nil {
		t.Fatal("record Agent-confirmed pre-mutation rejection:", err)
	}
	rejected, err := tasks.Get(ctx, testNodeID, taskTwoID)
	if err != nil || rejected.Status != taskstate.Failed || rejected.ResultCode != "agent_rejected" || rejected.StartedAt == nil {
		t.Fatalf("confirmed pre-mutation rejection state=%+v err=%v", rejected, err)
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
