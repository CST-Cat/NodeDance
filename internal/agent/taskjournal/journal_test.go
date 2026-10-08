package taskjournal

import (
	"bufio"
	"bytes"
	"context"
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
