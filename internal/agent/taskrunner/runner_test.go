package taskrunner

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/CST-Cat/NodeDance/internal/agent/containeractions"
	"github.com/CST-Cat/NodeDance/internal/agent/taskjournal"
	"github.com/CST-Cat/NodeDance/internal/protocol"
	"github.com/CST-Cat/NodeDance/internal/taskstate"
	_ "modernc.org/sqlite"
)

const (
	runnerNodeID = "b738a2d2-a255-4912-9e2e-26f974ac2529"
	runnerBootID = "11111111-2222-3333-4444-555555555555"
)

type memoryEngine struct {
	mu           sync.Mutex
	containers   map[string]containeractions.Container
	mutations    map[string]int
	inspectCalls int
	started      chan string
	release      chan struct{}
	blockOnce    bool
	inspectErr   error
	mutationErr  error
}

func newMemoryEngine(ids ...string) *memoryEngine {
	e := &memoryEngine{containers: make(map[string]containeractions.Container), mutations: make(map[string]int)}
	for _, id := range ids {
		e.containers[id] = containeractions.Container{ID: id, Name: "fixture", Running: true,
			StartedAt: time.Now().UTC().Add(-time.Hour).Format(time.RFC3339Nano)}
	}
	return e
}

func (e *memoryEngine) Inspect(ctx context.Context, id string) (containeractions.Container, error) {
	if err := ctx.Err(); err != nil {
		return containeractions.Container{}, err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.inspectCalls++
	if e.inspectErr != nil {
		return containeractions.Container{}, e.inspectErr
	}
	container, ok := e.containers[id]
	if !ok {
		return containeractions.Container{}, errors.New("not found")
	}
	return container, nil
}

func (e *memoryEngine) Start(ctx context.Context, id string) error { return e.mutate(ctx, "start", id) }
func (e *memoryEngine) Stop(ctx context.Context, id string) error  { return e.mutate(ctx, "stop", id) }
func (e *memoryEngine) Restart(ctx context.Context, id string) error {
	return e.mutate(ctx, "restart", id)
}
func (e *memoryEngine) Pause(ctx context.Context, id string) error { return e.mutate(ctx, "pause", id) }
func (e *memoryEngine) Resume(ctx context.Context, id string) error {
	return e.mutate(ctx, "resume", id)
}
func (e *memoryEngine) Remove(ctx context.Context, id string) error {
	return e.mutate(ctx, "delete", id)
}
func (e *memoryEngine) Rename(ctx context.Context, id, name string) error {
	return e.mutate(ctx, "rename", id)
}

func (e *memoryEngine) mutate(ctx context.Context, action, id string) error {
	e.mu.Lock()
	e.mutations[action]++
	count := e.mutations[action]
	started, release, block := e.started, e.release, e.blockOnce && count == 1
	if !block && e.mutationErr == nil {
		e.applyLocked(action, id)
	}
	err := e.mutationErr
	e.mu.Unlock()
	if block {
		if started != nil {
			started <- action
		}
		select {
		case <-release:
		case <-ctx.Done():
			return ctx.Err()
		}
		e.mu.Lock()
		e.applyLocked(action, id)
		err = e.mutationErr
		e.mu.Unlock()
	}
	return err
}

func (e *memoryEngine) applyLocked(action, id string) {
	container, ok := e.containers[id]
	if !ok {
		return
	}
	switch action {
	case "start":
		container.Running = true
		container.Paused = false
		container.StartedAt = time.Now().UTC().Format(time.RFC3339Nano)
	case "stop":
		container.Running = false
		container.Paused = false
	case "restart":
		container.Running = true
		container.Paused = false
		container.StartedAt = time.Now().UTC().Format(time.RFC3339Nano)
	case "pause":
		container.Running = true
		container.Paused = true
	case "resume":
		container.Running = true
		container.Paused = false
	case "delete":
		delete(e.containers, id)
	case "rename":
		container.Name = "renamed"
	}
	e.containers[id] = container
}

func (e *memoryEngine) counts() (int, int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	mutations := 0
	for _, count := range e.mutations {
		mutations += count
	}
	return mutations, e.inspectCalls
}

func openRunnerJournal(t *testing.T) (*taskjournal.Store, string) {
	t.Helper()
	base := t.TempDir()
	if err := os.Chmod(base, 0o700); err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(base, "private")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "tasks.sqlite")
	store, err := taskjournal.Open(context.Background(), path, runnerNodeID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store, path
}

func newRunner(t *testing.T, actionJournal *taskjournal.Store, journal Journal, engine *memoryEngine, options Options) (*Runner, context.CancelFunc) {
	t.Helper()
	executor, err := containeractions.New(engine, actionJournal, containeractions.Options{BootIDSource: func(context.Context) (string, error) { return runnerBootID, nil }})
	if err != nil {
		t.Fatal(err)
	}
	runner, err := New(runnerNodeID, journal, executor, options)
	if err != nil {
		t.Fatal(err)
	}
	processCtx, cancel := context.WithCancel(context.Background())
	if err := runner.Start(processCtx); err != nil {
		cancel()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cancel()
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer stopCancel()
		_ = runner.Stop(stopCtx)
	})
	return runner, cancel
}

func connectAndSync(t *testing.T, runner *Runner, store Journal, generation uint64) {
	t.Helper()
	journalID := store.JournalID()
	if err := runner.Connect(generation, journalID, []string{protocol.CapabilityTaskBridge}); err != nil {
		t.Fatal(err)
	}
	pages, err := runner.SnapshotPages(generation, fmt.Sprintf("initial-%d", generation))
	if err != nil {
		t.Fatalf("build initial snapshot: %v", err)
	}
	if len(pages) == 0 || !pages[len(pages)-1].Final {
		t.Fatalf("initial snapshot was incomplete: %+v", pages)
	}
	if err := runner.AcknowledgeSnapshot(generation, fmt.Sprintf("initial-%d", generation)); err != nil {
		t.Fatal(err)
	}
	if err := runner.MarkSynchronized(generation, journalID); err != nil {
		t.Fatal(err)
	}
}

func makeDispatch(taskID, key, containerID string, action protocol.TaskAction) (protocol.Envelope, protocol.TaskDispatch) {
	intent := protocol.TaskIntent{Action: action, ContainerID: containerID}
	if action == protocol.TaskDelete {
		intent.DeleteConfirmed = true
	}
	digest, _ := protocol.TaskRequestDigest(taskID, runnerNodeID, key, intent)
	dispatch := protocol.TaskDispatch{TaskID: taskID, NodeID: runnerNodeID, JournalID: "", TargetID: containerID,
		IdempotencyKey: key, RequestDigest: protocol.DigestString(digest), Intent: intent}
	envelope := protocol.Envelope{Version: protocol.CurrentVersion, Type: protocol.TypeTaskDispatch, Generation: 1, RequestID: taskID}
	return envelope, dispatch
}

func waitStatus(t *testing.T, store *taskjournal.Store, taskID string, status taskstate.Status) taskjournal.Snapshot {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		entry, err := store.Get(context.Background(), taskID)
		if err == nil && entry.Status == status {
			return entry
		}
		time.Sleep(10 * time.Millisecond)
	}
	entry, err := store.Get(context.Background(), taskID)
	t.Fatalf("task %s did not reach %s: snapshot=%+v err=%v", taskID, status, entry, err)
	return taskjournal.Snapshot{}
}

func waitRunnerIdle(t *testing.T, runner *Runner) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		runner.mu.Lock()
		active := len(runner.activeTasks)
		runner.mu.Unlock()
		if active == 0 {
			// runJob records its durable-result notification before it removes
			// the task from activeTasks. This is the synchronization boundary
			// needed before asserting a stable overflow-repair snapshot.
			return
		}
		time.Sleep(time.Millisecond)
	}
	runner.mu.Lock()
	active := len(runner.activeTasks)
	runner.mu.Unlock()
	t.Fatalf("runner did not finish notifying accepted jobs before deadline; active=%d", active)
}

func TestDurableDispatchRunsOnceAcrossDisconnectAndReconnectSnapshot(t *testing.T) {
	store, _ := openRunnerJournal(t)
	target := strings.Repeat("a", 64)
	engine := newMemoryEngine(target)
	engine.started = make(chan string, 1)
	engine.release = make(chan struct{})
	engine.blockOnce = true
	runner, _ := newRunner(t, store, store, engine, Options{Workers: 1, QueueCapacity: 1})
	connectAndSync(t, runner, store, 1)

	envelope, dispatch := makeDispatch("task-disconnect", "restart-disconnect", target, protocol.TaskRestart)
	dispatch.JournalID = store.JournalID()
	report, err := runner.AcceptDispatch(context.Background(), 1, envelope, dispatch)
	if err != nil {
		t.Fatal(err)
	}
	if report.Status != taskstate.Queued {
		t.Fatalf("durable receipt report = %s, want queued", report.Status)
	}
	if report.ReportRevision == 0 {
		t.Fatal("durable receipt omitted report revision")
	}
	entry, err := store.Get(context.Background(), dispatch.TaskID)
	if err != nil || entry.Status != taskstate.Queued && entry.Status != taskstate.Running || !entry.DeliveryCommitted {
		t.Fatalf("dispatch was not durably recorded before receipt: %+v err=%v", entry, err)
	}
	select {
	case action := <-engine.started:
		if action != "restart" {
			t.Fatalf("executor called %s, want restart", action)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("worker did not reach the fake Engine mutation")
	}

	const concurrentDuplicates = 10
	var duplicates sync.WaitGroup
	duplicateErrors := make(chan error, concurrentDuplicates)
	for index := 0; index < concurrentDuplicates; index++ {
		duplicates.Add(1)
		go func() {
			defer duplicates.Done()
			duplicate, err := runner.AcceptDispatch(context.Background(), 1, envelope, dispatch)
			if err != nil {
				duplicateErrors <- err
			} else if duplicate.Status != taskstate.Running && duplicate.Status != taskstate.Queued {
				duplicateErrors <- fmt.Errorf("duplicate receipt status = %s", duplicate.Status)
			}
		}()
	}
	duplicates.Wait()
	close(duplicateErrors)
	for err := range duplicateErrors {
		t.Fatal(err)
	}
	if mutations, _ := engine.counts(); mutations != 1 {
		t.Fatalf("concurrent duplicate dispatches reached Engine %d times", mutations)
	}
	runningBatch, err := runner.DrainReports(1, protocol.TaskSnapshotPageSize)
	if err != nil || len(runningBatch.Reports) != 1 {
		t.Fatalf("durable running notification unavailable while Engine is blocked: batch=%+v err=%v", runningBatch, err)
	}
	runningReport := runningBatch.Reports[0]
	if runningReport.Status != taskstate.Running || !runningReport.Evidence.ExecutionAttempted || runningReport.ReportRevision <= report.ReportRevision {
		t.Fatalf("running report lacks a newer durable execution proof: %+v receipt=%+v", runningReport, report)
	}
	runner.Disconnect(1)
	close(engine.release)
	finished := waitStatus(t, store, dispatch.TaskID, taskstate.Succeeded)
	if !finished.Evidence.PostconditionVerified || !finished.Evidence.ExecutionCompleted {
		t.Fatalf("executor result lacks proof: %+v", finished)
	}
	if mutations, _ := engine.counts(); mutations != 1 {
		t.Fatalf("duplicate dispatch mutated Engine %d times", mutations)
	}

	// The SQLite terminal status commits before runJob emits its final durable
	// report notification. Wait for the worker's notification boundary before
	// taking the reconnect snapshot, or the snapshot can correctly require a
	// second synchronization pass under the race detector.
	waitRunnerIdle(t, runner)
	connectAndSync(t, runner, store, 2)
	pages, err := runner.SnapshotPages(2, "after-reconnect")
	if err != nil {
		t.Fatal(err)
	}
	if len(pages) != 1 || len(pages[0].Reports) != 1 || pages[0].Reports[0].Status != taskstate.Succeeded || !pages[0].Final {
		t.Fatalf("reconnect snapshot did not reconcile terminal journal result: %+v", pages)
	}
	if err := runner.AcknowledgeSnapshot(2, "after-reconnect"); err != nil {
		t.Fatal(err)
	}
	if err := runner.MarkSynchronized(2, store.JournalID()); err != nil {
		t.Fatal(err)
	}
	if _, err := runner.AcceptDispatch(context.Background(), 1, envelope, dispatch); !errors.Is(err, ErrStaleGeneration) {
		t.Fatalf("old generation dispatch accepted after reconnect: %v", err)
	}
	if mutations, _ := engine.counts(); mutations != 1 {
		t.Fatalf("snapshot/reconnect replayed Engine operation %d times", mutations)
	}
}

func TestOldQueuedReportAckCannotClearNewerTerminalNotification(t *testing.T) {
	store, _ := openRunnerJournal(t)
	target := strings.Repeat("9", 64)
	engine := newMemoryEngine(target)
	engine.started = make(chan string, 1)
	engine.release = make(chan struct{})
	engine.blockOnce = true
	runner, _ := newRunner(t, store, store, engine, Options{Workers: 1, QueueCapacity: 1})
	connectAndSync(t, runner, store, 1)

	envelope, dispatch := makeDispatch("task-report-revision", "report-revision", target, protocol.TaskRestart)
	dispatch.JournalID = store.JournalID()
	queued, err := runner.AcceptDispatch(context.Background(), 1, envelope, dispatch)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-engine.started:
	case <-time.After(5 * time.Second):
		t.Fatal("task did not reach the intentionally blocked Engine")
	}
	runningBatch, err := runner.DrainReports(1, protocol.TaskSnapshotPageSize)
	if err != nil || len(runningBatch.Reports) != 1 || runningBatch.Reports[0].Status != taskstate.Running {
		t.Fatalf("running report was not delivered while the Engine was blocked: batch=%+v err=%v", runningBatch, err)
	}
	close(engine.release)
	waitStatus(t, store, dispatch.TaskID, taskstate.Succeeded)
	terminal := waitForReportStatus(t, runner, dispatch.TaskID, taskstate.Succeeded)
	if terminal.ReportRevision <= runningBatch.Reports[0].ReportRevision || terminal.ReportRevision <= queued.ReportRevision {
		t.Fatalf("terminal report revision did not advance: queued=%d running=%d terminal=%d", queued.ReportRevision, runningBatch.Reports[0].ReportRevision, terminal.ReportRevision)
	}
	if err := runner.AcknowledgeReport(1, dispatch.TaskID, queued.ReportRevision); !errors.Is(err, ErrStaleReportAck) {
		t.Fatalf("old queued receipt ACK cleared a newer terminal report: %v", err)
	}
	if err := runner.AcknowledgeReport(1, dispatch.TaskID, runningBatch.Reports[0].ReportRevision); !errors.Is(err, ErrStaleReportAck) {
		t.Fatalf("old running report ACK cleared a newer terminal report: %v", err)
	}
	if err := runner.AcknowledgeReport(1, dispatch.TaskID, terminal.ReportRevision); err != nil {
		t.Fatalf("acknowledge current terminal report: %v", err)
	}
}

func waitForReportStatus(t *testing.T, runner *Runner, taskID string, status taskstate.Status) protocol.TaskReport {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		batch, err := runner.DrainReports(1, protocol.TaskSnapshotPageSize)
		if err != nil {
			t.Fatalf("drain task reports: %v", err)
		}
		for _, report := range batch.Reports {
			if report.TaskID == taskID && report.Status == status {
				return report
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("task %s report did not reach %s", taskID, status)
	return protocol.TaskReport{}
}

func TestDispatcherEnforcesWorkerAndPendingQueueBounds(t *testing.T) {
	store, _ := openRunnerJournal(t)
	target1, target2, target3 := strings.Repeat("1", 64), strings.Repeat("2", 64), strings.Repeat("3", 64)
	engine := newMemoryEngine(target1, target2, target3)
	engine.started = make(chan string, 1)
	engine.release = make(chan struct{})
	engine.blockOnce = true
	runner, _ := newRunner(t, store, store, engine, Options{Workers: 1, QueueCapacity: 1})
	connectAndSync(t, runner, store, 1)
	firstEnvelope, first := makeDispatch("task-bound-1", "key-bound-1", target1, protocol.TaskRestart)
	first.JournalID = store.JournalID()
	if _, err := runner.AcceptDispatch(context.Background(), 1, firstEnvelope, first); err != nil {
		t.Fatal(err)
	}
	select {
	case <-engine.started:
	case <-time.After(5 * time.Second):
		t.Fatal("first task did not enter its bounded worker")
	}
	secondEnvelope, second := makeDispatch("task-bound-2", "key-bound-2", target2, protocol.TaskRestart)
	second.JournalID = store.JournalID()
	if _, err := runner.AcceptDispatch(context.Background(), 1, secondEnvelope, second); err != nil {
		t.Fatalf("one queued task should fit the configured pending capacity: %v", err)
	}
	if available := runner.AvailableCapacity(); available != 0 {
		t.Fatalf("available capacity=%d with one running and one queued task", available)
	}
	thirdEnvelope, third := makeDispatch("task-bound-3", "key-bound-3", target3, protocol.TaskRestart)
	third.JournalID = store.JournalID()
	if _, err := runner.AcceptDispatch(context.Background(), 1, thirdEnvelope, third); !errors.Is(err, ErrCapacityExceeded) {
		t.Fatalf("over-capacity task accepted: %v", err)
	}
	if _, err := store.Get(context.Background(), third.TaskID); !errors.Is(err, taskjournal.ErrTaskNotFound) {
		t.Fatalf("over-capacity dispatch was durably accepted: %v", err)
	}
	close(engine.release)
	waitStatus(t, store, first.TaskID, taskstate.Succeeded)
	waitStatus(t, store, second.TaskID, taskstate.Succeeded)
	if mutations, _ := engine.counts(); mutations != 2 {
		t.Fatalf("bounded worker ran %d mutations; want exactly two accepted tasks", mutations)
	}
}

type blockingExecutor struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
	calls   atomic.Int32
}

func (e *blockingExecutor) ExecuteObserved(context.Context, containeractions.Request, func(taskjournal.Snapshot)) (taskjournal.Snapshot, error) {
	e.calls.Add(1)
	e.once.Do(func() { close(e.entered) })
	<-e.release // Deliberately ignores process cancellation to model a stuck SDK call.
	return taskjournal.Snapshot{}, nil
}

func (*blockingExecutor) Reconcile(context.Context, string) (taskjournal.Snapshot, error) {
	return taskjournal.Snapshot{}, nil
}

func TestRepeatedStopTimeoutsShareOneWorkerJoin(t *testing.T) {
	store, _ := openRunnerJournal(t)
	executor := &blockingExecutor{entered: make(chan struct{}), release: make(chan struct{})}
	runner, err := New(runnerNodeID, store, executor, Options{Workers: 1, QueueCapacity: 1})
	if err != nil {
		t.Fatal(err)
	}
	processCtx, cancelProcess := context.WithCancel(context.Background())
	if err := runner.Start(processCtx); err != nil {
		cancelProcess()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cancelProcess()
		select {
		case <-executor.release:
		default:
			close(executor.release)
		}
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer stopCancel()
		_ = runner.Stop(stopCtx)
	})
	connectAndSync(t, runner, store, 1)
	target := strings.Repeat("8", 64)
	envelope, dispatch := makeDispatch("task-stop-join", "stop-join", target, protocol.TaskRestart)
	dispatch.JournalID = store.JournalID()
	if _, err := runner.AcceptDispatch(context.Background(), 1, envelope, dispatch); err != nil {
		t.Fatal(err)
	}
	select {
	case <-executor.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("worker did not enter the deliberately blocked executor")
	}
	joinDone := runner.joinDone
	for attempt := 0; attempt < 5; attempt++ {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
		err := runner.Stop(stopCtx)
		stopCancel()
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("Stop attempt %d error = %v, want deadline exceeded while executor is blocked", attempt, err)
		}
		if runner.joinDone != joinDone {
			t.Fatal("repeated Stop replaced the shared join completion channel")
		}
		select {
		case <-joinDone:
			t.Fatal("worker join completed before blocked executor was released")
		default:
		}
	}
	if got := executor.calls.Load(); got != 1 {
		t.Fatalf("repeated Stop caused %d executor calls, want one", got)
	}
	close(executor.release)
	joinCtx, joinCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer joinCancel()
	if err := runner.Stop(joinCtx); err != nil {
		t.Fatalf("Stop did not join after the executor released: %v", err)
	}
	select {
	case <-joinDone:
	default:
		t.Fatal("shared worker join channel was not closed")
	}
}

type snapshotGateJournal struct {
	Journal
	ready   chan struct{}
	release chan struct{}
	paused  atomic.Bool
}

func (j *snapshotGateJournal) SnapshotAll(ctx context.Context) ([]taskjournal.Snapshot, error) {
	snapshots, err := j.Journal.SnapshotAll(ctx)
	if err != nil || !j.paused.CompareAndSwap(false, true) {
		return snapshots, err
	}
	close(j.ready)
	select {
	case <-j.release:
		return snapshots, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func TestSnapshotPagesAreContiguousFrozenAndRequireStableInitialAck(t *testing.T) {
	store, _ := openRunnerJournal(t)
	const count = 70
	for index := 0; index < count; index++ {
		taskID := fmt.Sprintf("snapshot-task-%03d", index)
		key := fmt.Sprintf("snapshot-key-%03d", index)
		target := strings.Repeat("a", 60) + fmt.Sprintf("%04x", index)
		intent := protocol.TaskIntent{Action: protocol.TaskRestart, ContainerID: target}
		identity, err := protocol.TaskIdentity(taskID, runnerNodeID, key, intent)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.EnqueueDelivered(context.Background(), identity); err != nil {
			t.Fatal(err)
		}
	}
	gate := &snapshotGateJournal{Journal: store, ready: make(chan struct{}), release: make(chan struct{})}
	engine := newMemoryEngine()
	runner, _ := newRunner(t, store, gate, engine, Options{Workers: 1, QueueCapacity: 1})
	if err := runner.Connect(1, store.JournalID(), []string{protocol.CapabilityTaskBridge}); err != nil {
		t.Fatal(err)
	}
	type snapshotResult struct {
		pages []protocol.TaskSnapshotPage
		err   error
	}
	result := make(chan snapshotResult, 1)
	go func() {
		pages, err := runner.SnapshotPages(1, "consistent-snapshot")
		result <- snapshotResult{pages: pages, err: err}
	}()
	select {
	case <-gate.ready:
	case <-time.After(5 * time.Second):
		t.Fatal("snapshot did not reach the frozen-candidate boundary")
	}
	changedTaskID := "snapshot-task-000"
	if err := store.Finish(context.Background(), changedTaskID, taskstate.Succeeded,
		taskstate.Evidence{ExecutionAttempted: true, ExecutionCompleted: true, PostconditionVerified: true},
		taskjournal.Result{Code: taskjournal.ResultVerified, ObservedState: "running"}); err != nil {
		t.Fatal(err)
	}
	runner.notify(changedTaskID)
	close(gate.release)
	first := <-result
	if first.err != nil {
		t.Fatal(first.err)
	}
	if len(first.pages) != 3 || len(first.pages[0].Reports) != protocol.TaskSnapshotPageSize ||
		len(first.pages[1].Reports) != protocol.TaskSnapshotPageSize || len(first.pages[2].Reports) != 6 {
		t.Fatalf("snapshot pages have incorrect chunking: sizes=%d/%d/%d", len(first.pages[0].Reports), len(first.pages[1].Reports), len(first.pages[2].Reports))
	}
	seen := make(map[string]struct{}, count)
	for pageIndex, page := range first.pages {
		if page.Page != uint32(pageIndex) || page.SnapshotID != "consistent-snapshot" || page.JournalID != store.JournalID() || page.Final != (pageIndex == len(first.pages)-1) {
			t.Fatalf("page ordering/final metadata invalid: %+v", page)
		}
		for _, report := range page.Reports {
			if _, duplicate := seen[report.TaskID]; duplicate {
				t.Fatalf("snapshot duplicated task %s", report.TaskID)
			}
			seen[report.TaskID] = struct{}{}
		}
	}
	if len(seen) != count {
		t.Fatalf("snapshot included %d tasks; want %d", len(seen), count)
	}
	if err := runner.AcknowledgeSnapshot(1, "consistent-snapshot"); err != nil {
		t.Fatal(err)
	}
	runner.mu.Lock()
	dirtyAfterOldAck := runner.snapshotDirty
	runner.mu.Unlock()
	if !dirtyAfterOldAck {
		t.Fatal("snapshot ACK from before the interleaved notify cleared the dirty barrier")
	}
	if err := runner.MarkSynchronized(1, store.JournalID()); !errors.Is(err, ErrNotSynchronized) {
		t.Fatalf("snapshot with an interleaved state change enabled dispatch: %v", err)
	}
	second, err := runner.SnapshotPages(1, "consistent-snapshot-2")
	if err != nil || len(second) != 3 || second[0].Reports[0].TaskID != changedTaskID || second[0].Reports[0].Status != taskstate.Succeeded {
		t.Fatalf("second stable snapshot did not include updated row first in order: pages=%+v err=%v", second, err)
	}
	if err := runner.AcknowledgeSnapshot(1, "consistent-snapshot-2"); err != nil {
		t.Fatal(err)
	}
	runner.mu.Lock()
	dirtyAfterStableAck := runner.snapshotDirty
	runner.mu.Unlock()
	if dirtyAfterStableAck {
		t.Fatal("stable complete snapshot did not clear the dirty barrier")
	}
	if err := runner.MarkSynchronized(1, store.JournalID()); err != nil {
		t.Fatalf("stable complete snapshot failed to enable dispatch: %v", err)
	}
}

func TestDeliveredQueuedProcessRestartBecomesUnknownAndCannotReplay(t *testing.T) {
	store, path := openRunnerJournal(t)
	target := strings.Repeat("b", 64)
	envelope, dispatch := makeDispatch("task-restart-unknown", "restart-unknown", target, protocol.TaskRestart)
	dispatch.JournalID = store.JournalID()
	identity, err := protocol.TaskIdentity(dispatch.TaskID, dispatch.NodeID, dispatch.IdempotencyKey, dispatch.Intent)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.EnqueueDelivered(context.Background(), identity); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := taskjournal.Open(context.Background(), path, runnerNodeID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	engine := newMemoryEngine(target)
	runner, _ := newRunner(t, reopened, reopened, engine, Options{Workers: 1, QueueCapacity: 1})
	connectAndSync(t, runner, reopened, 1)
	report, err := runner.AcceptDispatch(context.Background(), 1, envelope, dispatch)
	if err != nil {
		t.Fatal(err)
	}
	if report.Status != taskstate.Unknown || report.Evidence.ExecutionAttempted || report.Result.Code != string(taskjournal.ResultUncertain) {
		t.Fatalf("recovered delivered queued task report = %+v", report)
	}
	entry, err := reopened.Get(context.Background(), dispatch.TaskID)
	if err != nil || entry.Status != taskstate.Unknown || !entry.DeliveryCommitted {
		t.Fatalf("restart recovery did not persist unknown: %+v err=%v", entry, err)
	}
	if mutations, inspections := engine.counts(); mutations != 0 || inspections != 0 {
		t.Fatalf("unknown dispatch reached Engine: mutations=%d inspections=%d", mutations, inspections)
	}

	staleEnvelope := envelope
	staleEnvelope.Generation = 0
	if _, err := runner.AcceptDispatch(context.Background(), 1, staleEnvelope, dispatch); err == nil {
		t.Fatal("stale/invalid envelope was accepted")
	}
	request := protocol.TaskReconcileRequest{TaskID: dispatch.TaskID, NodeID: dispatch.NodeID, JournalID: dispatch.JournalID,
		TargetID: dispatch.TargetID, IdempotencyKey: dispatch.IdempotencyKey, RequestDigest: dispatch.RequestDigest, Intent: dispatch.Intent}
	reconcileEnvelope := protocol.Envelope{Version: protocol.CurrentVersion, Type: protocol.TypeTaskReconcile,
		Generation: 1, RequestID: dispatch.TaskID}
	if _, err := runner.AcceptReconcile(context.Background(), 1, reconcileEnvelope, request); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		runner.mu.Lock()
		active := len(runner.activeTasks)
		runner.mu.Unlock()
		if active == 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if entry, err = reopened.Get(context.Background(), dispatch.TaskID); err != nil || entry.Status != taskstate.Unknown {
		t.Fatalf("unverifiable restart reconciliation changed unknown: %+v err=%v", entry, err)
	}
	if mutations, inspections := engine.counts(); mutations != 0 || inspections != 0 {
		t.Fatalf("read-only reconcile called Engine mutation/inspect: %d/%d", mutations, inspections)
	}

	connectAndSync(t, runner, reopened, 2)
	oldGeneration := envelope
	if _, err := runner.AcceptDispatch(context.Background(), 1, oldGeneration, dispatch); !errors.Is(err, ErrStaleGeneration) {
		t.Fatalf("old connection generation accepted: %v", err)
	}
	newEnvelope := envelope
	newEnvelope.Generation = 2
	if report, err = runner.AcceptDispatch(context.Background(), 2, newEnvelope, dispatch); err != nil || report.Status != taskstate.Unknown {
		t.Fatalf("duplicate unknown task was replayed or rejected: report=%+v err=%v", report, err)
	}
	changedKey := dispatch
	changedKey.IdempotencyKey = "different-key"
	changedEnvelope := newEnvelope
	changedEnvelope.RequestID = dispatch.TaskID
	if _, err := runner.AcceptDispatch(context.Background(), 2, changedEnvelope, changedKey); !errors.Is(err, taskjournal.ErrTaskIDConflict) {
		t.Fatalf("same task ID with a different key did not conflict: %v", err)
	}
	replaced := dispatch
	replaced.JournalID = strings.Repeat("c", 64)
	if _, err := runner.AcceptDispatch(context.Background(), 2, newEnvelope, replaced); !errors.Is(err, protocol.ErrInvalidTaskMessage) {
		t.Fatalf("dispatch for replaced journal accepted: %v", err)
	}
	if mutations, inspections := engine.counts(); mutations != 0 || inspections != 0 {
		t.Fatalf("unknown restart/duplicate reached Engine: mutations=%d inspections=%d", mutations, inspections)
	}
}

type commitThenLoseAckJournal struct {
	Journal
	failNext bool
}

func (j *commitThenLoseAckJournal) EnqueueDelivered(ctx context.Context, identity taskstate.Identity) (taskjournal.EnqueueResult, error) {
	result, err := j.Journal.EnqueueDelivered(ctx, identity)
	if err == nil && j.failNext {
		j.failNext = false
		return taskjournal.EnqueueResult{}, errors.New("simulated lost SQLite commit acknowledgement")
	}
	return result, err
}

func TestAmbiguousJournalCommitNeverExecutesBeforeConfirmedRetry(t *testing.T) {
	store, _ := openRunnerJournal(t)
	journal := &commitThenLoseAckJournal{Journal: store, failNext: true}
	target := strings.Repeat("d", 64)
	engine := newMemoryEngine(target)
	runner, _ := newRunner(t, store, journal, engine, Options{Workers: 1, QueueCapacity: 1})
	connectAndSync(t, runner, journal, 1)
	envelope, dispatch := makeDispatch("task-commit-ambiguous", "commit-ambiguous", target, protocol.TaskRestart)
	dispatch.JournalID = journal.JournalID()
	if _, err := runner.AcceptDispatch(context.Background(), 1, envelope, dispatch); err == nil {
		t.Fatal("ambiguous commit was acknowledged")
	}
	entry, err := store.Get(context.Background(), dispatch.TaskID)
	if err != nil || !entry.DeliveryCommitted || entry.Status != taskstate.Queued {
		t.Fatalf("simulated ambiguous commit did not persist durable receipt: %+v err=%v", entry, err)
	}
	if mutations, _ := engine.counts(); mutations != 0 {
		t.Fatalf("ambiguous journal commit reached Engine %d times", mutations)
	}
	if _, err := runner.AcceptDispatch(context.Background(), 1, envelope, dispatch); err != nil {
		t.Fatalf("retry of the exact durable queued identity failed: %v", err)
	}
	waitStatus(t, store, dispatch.TaskID, taskstate.Succeeded)
	if mutations, _ := engine.counts(); mutations != 1 {
		t.Fatalf("confirmed retry executed %d mutations, want 1", mutations)
	}
}

func TestSQLiteReceiptFailureAndResourceConflictsCauseNoEngineCalls(t *testing.T) {
	store, path := openRunnerJournal(t)
	target := strings.Repeat("e", 64)
	engine := newMemoryEngine(target)
	runner, _ := newRunner(t, store, store, engine, Options{Workers: 1, QueueCapacity: 1})
	connectAndSync(t, runner, store, 1)
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TRIGGER reject_delivery BEFORE INSERT ON task_journal WHEN NEW.delivery_committed=1 BEGIN SELECT RAISE(ABORT,'injected write failure'); END`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	envelope, dispatch := makeDispatch("task-write-fails", "write-fails", target, protocol.TaskRestart)
	dispatch.JournalID = store.JournalID()
	if _, err := runner.AcceptDispatch(context.Background(), 1, envelope, dispatch); err == nil {
		t.Fatal("injected SQLite receipt failure was accepted")
	}
	if _, err := store.Get(context.Background(), dispatch.TaskID); !errors.Is(err, taskjournal.ErrTaskNotFound) {
		t.Fatalf("failed receipt write left a task row: %v", err)
	}
	if mutations, inspections := engine.counts(); mutations != 0 || inspections != 0 {
		t.Fatalf("failed receipt write reached Engine: mutations=%d inspections=%d", mutations, inspections)
	}
	db, err = sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DROP TRIGGER reject_delivery`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := runner.AcceptDispatch(context.Background(), 1, envelope, dispatch); err != nil {
		t.Fatal(err)
	}
	waitStatus(t, store, dispatch.TaskID, taskstate.Succeeded)

	conflictEnvelope, conflict := makeDispatch("task-resource-lock-owner", "resource-lock-owner", target, protocol.TaskRestart)
	conflict.JournalID = store.JournalID()
	conflictIdentity, err := protocol.TaskIdentity(conflict.TaskID, conflict.NodeID, conflict.IdempotencyKey, conflict.Intent)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.EnqueueDelivered(context.Background(), conflictIdentity); err != nil {
		t.Fatal(err)
	}
	if err := store.BeginExecution(context.Background(), conflict.TaskID); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkUnknown(context.Background(), conflict.TaskID); err != nil {
		t.Fatal(err)
	}
	conflictEnvelope.Generation = 1
	conflictEnvelope.RequestID = "task-resource-conflict"
	conflict.TaskID = "task-resource-conflict"
	conflict.IdempotencyKey = "resource-conflict"
	conflictDigest, err := protocol.TaskRequestDigest(conflict.TaskID, conflict.NodeID, conflict.IdempotencyKey, conflict.Intent)
	if err != nil {
		t.Fatal(err)
	}
	conflict.RequestDigest = protocol.DigestString(conflictDigest)
	if _, err := runner.AcceptDispatch(context.Background(), 1, conflictEnvelope, conflict); !errors.Is(err, taskjournal.ErrResourceBusy) {
		t.Fatalf("same full Docker resource was not locked: %v", err)
	}
	if mutations, _ := engine.counts(); mutations != 1 {
		t.Fatalf("conflicting resource request reached Engine; mutations=%d", mutations)
	}
}

func TestCapabilityAndFullTargetChecksPrecedeDurableAdmission(t *testing.T) {
	store, _ := openRunnerJournal(t)
	target := strings.Repeat("f", 64)
	engine := newMemoryEngine(target)
	runner, _ := newRunner(t, store, store, engine, Options{Workers: 1, QueueCapacity: 1})
	if err := runner.Connect(1, store.JournalID(), nil); !errors.Is(err, ErrCapabilityRequired) {
		t.Fatalf("non-negotiated task bridge attached: %v", err)
	}
	connectAndSync(t, runner, store, 1)
	envelope, dispatch := makeDispatch("task-bad-target", "bad-target", target, protocol.TaskRestart)
	dispatch.JournalID = store.JournalID()
	dispatch.TargetID = "short"
	if _, err := runner.AcceptDispatch(context.Background(), 1, envelope, dispatch); !errors.Is(err, protocol.ErrInvalidTaskMessage) {
		t.Fatalf("short target accepted: %v", err)
	}
	if _, err := store.Get(context.Background(), dispatch.TaskID); !errors.Is(err, taskjournal.ErrTaskNotFound) {
		t.Fatalf("invalid target wrote journal: %v", err)
	}
}

func TestReportQueueOverflowRequiresCompleteSnapshotAndDoesNotLeakEngineErrors(t *testing.T) {
	store, _ := openRunnerJournal(t)
	targets := []string{strings.Repeat("4", 64), strings.Repeat("5", 64), strings.Repeat("6", 64)}
	engine := newMemoryEngine(targets...)
	engine.inspectErr = errors.New("registry_password_DO_NOT_PERSIST")
	runner, _ := newRunner(t, store, store, engine, Options{Workers: 3, QueueCapacity: 3, ReportQueueCapacity: 1})
	connectAndSync(t, runner, store, 1)
	taskIDs := []string{"task-overflow-1", "task-overflow-2", "task-overflow-3"}
	for index, target := range targets {
		envelope, dispatch := makeDispatch(taskIDs[index], "key-overflow-"+fmt.Sprint(index), target, protocol.TaskRestart)
		dispatch.JournalID = store.JournalID()
		if _, err := runner.AcceptDispatch(context.Background(), 1, envelope, dispatch); err != nil {
			t.Fatal(err)
		}
	}
	for _, taskID := range taskIDs {
		waitStatus(t, store, taskID, taskstate.Failed)
	}
	waitRunnerIdle(t, runner)
	batch, err := runner.DrainReports(1, protocol.TaskSnapshotPageSize)
	if err != nil || !batch.SnapshotRequired {
		t.Fatalf("overflow did not require full durable snapshot: batch=%+v err=%v", batch, err)
	}
	pages, err := runner.SnapshotPages(1, "overflow-repair")
	if err != nil {
		t.Fatal(err)
	}
	if len(pages) != 1 || len(pages[0].Reports) != len(taskIDs) {
		t.Fatalf("repair snapshot omitted terminal rows: %+v", pages)
	}
	for _, report := range pages[0].Reports {
		encoded, err := json.Marshal(report)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(encoded), "registry_password_DO_NOT_PERSIST") {
			t.Fatalf("Engine error leaked into report: %s", encoded)
		}
		log, _, err := store.GetLog(context.Background(), report.TaskID)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(log), "registry_password_DO_NOT_PERSIST") {
			t.Fatal("Engine error leaked into durable task log")
		}
		entry, err := store.Get(context.Background(), report.TaskID)
		if err != nil || entry.Result.Code != taskjournal.ResultFailed || entry.Result.ObservedState != "inspect_failed" {
			t.Fatalf("task stored unbounded error text: %+v err=%v", entry, err)
		}
	}
	if err := runner.AcknowledgeSnapshot(1, "overflow-repair"); err != nil {
		t.Fatal(err)
	}
	batch, err = runner.DrainReports(1, protocol.TaskSnapshotPageSize)
	if err != nil || batch.SnapshotRequired {
		t.Fatalf("successful snapshot did not clear overflow barrier: batch=%+v err=%v", batch, err)
	}
}

func TestCancelImagePullSignalsOnlyTheRegisteredTask(t *testing.T) {
	ctx, cancel := context.WithCancelCause(context.Background())
	runner := &Runner{activeImagePulls: map[string]context.CancelCauseFunc{"image-pull-task": cancel}}
	if !runner.CancelImagePull("image-pull-task") {
		t.Fatal("active image pull was not canceled")
	}
	if !errors.Is(context.Cause(ctx), taskstate.ErrCancellationRequested) {
		t.Fatalf("image pull cancellation cause = %v", context.Cause(ctx))
	}
	if runner.CancelImagePull("other-task") {
		t.Fatal("cancellation was accepted for an unrelated or inactive task")
	}
}
