package containeractions

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/CST-Cat/NodeDance/internal/agent/taskjournal"
	"github.com/CST-Cat/NodeDance/internal/taskstate"
	"github.com/containerd/errdefs"
)

const testContainerID = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
const dockerZeroStartedAt = "0001-01-01T00:00:00Z"

type fakeEngine struct {
	mu                   sync.Mutex
	containers           map[string]Container
	inspectCalls         int
	inspectErrors        map[int]error
	actionCalls          map[Action]int
	mutationErrors       map[Action]error
	noOpRestart          bool
	countRestart         bool
	keepRestartStartedAt bool
	startEntered         chan struct{}
	continueStart        chan struct{}
}

func newFakeEngine(container Container) *fakeEngine {
	return &fakeEngine{
		containers:     map[string]Container{container.ID: container},
		inspectErrors:  make(map[int]error),
		actionCalls:    make(map[Action]int),
		mutationErrors: make(map[Action]error),
	}
}

func (f *fakeEngine) Inspect(_ context.Context, id string) (Container, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.inspectCalls++
	if err := f.inspectErrors[f.inspectCalls]; err != nil {
		return Container{}, err
	}
	container, ok := f.containers[id]
	if !ok {
		return Container{}, errdefs.ErrNotFound.WithMessage("container does not exist")
	}
	return container, nil
}

func (f *fakeEngine) mutate(action Action, id string, apply func(*Container)) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.actionCalls[action]++
	container, ok := f.containers[id]
	if !ok {
		return errdefs.ErrNotFound.WithMessage("container does not exist")
	}
	if apply != nil {
		apply(&container)
		f.containers[id] = container
	}
	return f.mutationErrors[action]
}

func (f *fakeEngine) Start(ctx context.Context, id string) error {
	if f.startEntered != nil {
		select {
		case f.startEntered <- struct{}{}:
		default:
		}
		select {
		case <-f.continueStart:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return f.mutate(ActionStart, id, func(c *Container) {
		c.Running, c.Paused, c.Restarting = true, false, false
		if c.StartedAt == "" {
			c.StartedAt = "2026-10-08T10:00:00Z"
		}
	})
}

func (f *fakeEngine) Stop(_ context.Context, id string) error {
	return f.mutate(ActionStop, id, func(c *Container) { c.Running, c.Paused, c.Restarting = false, false, false })
}

func (f *fakeEngine) Restart(_ context.Context, id string) error {
	return f.mutate(ActionRestart, id, func(c *Container) {
		if f.noOpRestart {
			return
		}
		if f.countRestart {
			c.RestartCount++
		}
		if !f.keepRestartStartedAt {
			old, err := time.Parse(time.RFC3339Nano, c.StartedAt)
			if err == nil {
				c.StartedAt = old.Add(time.Second).Format(time.RFC3339Nano)
			}
		}
		c.Running, c.Paused, c.Restarting = true, false, false
	})
}

func (f *fakeEngine) Pause(_ context.Context, id string) error {
	return f.mutate(ActionPause, id, func(c *Container) { c.Running, c.Paused = true, true })
}

func (f *fakeEngine) Resume(_ context.Context, id string) error {
	return f.mutate(ActionResume, id, func(c *Container) { c.Running, c.Paused = true, false })
}

func (f *fakeEngine) Remove(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.actionCalls[ActionDelete]++
	delete(f.containers, id)
	return f.mutationErrors[ActionDelete]
}

func (f *fakeEngine) Rename(_ context.Context, id, name string) error {
	return f.mutate(ActionRename, id, func(c *Container) { c.Name = "/" + name })
}

func (f *fakeEngine) calls(action Action) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.actionCalls[action]
}

func newTestExecutor(t *testing.T, engine Engine) (*Executor, *taskjournal.Store) {
	t.Helper()
	parentDir := t.TempDir()
	if err := os.Chmod(parentDir, 0o700); err != nil {
		t.Fatal(err)
	}
	journalDir := parentDir + "/journal"
	if err := os.Mkdir(journalDir, 0o700); err != nil {
		t.Fatal(err)
	}
	store, err := taskjournal.Open(context.Background(), journalDir+"/tasks.db", "node-test")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("close task journal: %v", err)
		}
	})
	executor, err := New(engine, store, Options{OperationTimeout: 3 * time.Second, VerificationTimeout: 2 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	return executor, store
}

func request(action Action) Request {
	return Request{TaskID: "task-1", NodeID: "node-test", IdempotencyKey: "idem-1", Action: action,
		ContainerID: testContainerID}
}

func runningContainer() Container {
	return Container{ID: testContainerID, Name: "/nd-test", Running: true,
		StartedAt: "2026-10-08T09:00:00Z", RestartCount: 0}
}

func TestAllLifecycleActionsUseJournalAndVerifyFreshEngineState(t *testing.T) {
	tests := []struct {
		name      string
		action    Action
		container Container
		prepare   func(*Request)
	}{
		{name: "start", action: ActionStart, container: Container{ID: testContainerID, Name: "/nd-test"}},
		{name: "stop", action: ActionStop, container: runningContainer()},
		{name: "restart", action: ActionRestart, container: runningContainer()},
		{name: "pause", action: ActionPause, container: runningContainer()},
		{name: "resume", action: ActionResume, container: Container{ID: testContainerID, Name: "/nd-test", Running: true, Paused: true}},
		{name: "delete", action: ActionDelete, container: Container{ID: testContainerID, Name: "/nd-test"}, prepare: func(r *Request) {
			r.DeleteConfirmed, r.DeleteConfirmationID = true, testContainerID
		}},
		{name: "rename", action: ActionRename, container: Container{ID: testContainerID, Name: "/nd-test"}, prepare: func(r *Request) {
			r.NewName = "nd-renamed"
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			engine := newFakeEngine(test.container)
			executor, store := newTestExecutor(t, engine)
			req := request(test.action)
			if test.prepare != nil {
				test.prepare(&req)
			}
			task, err := executor.Execute(context.Background(), req)
			if err != nil {
				t.Fatalf("Execute() error = %v", err)
			}
			if task.Status != taskstate.Succeeded {
				t.Fatalf("status = %q, want succeeded", task.Status)
			}
			if task.Evidence.ExecutionAttempted != true || !task.Evidence.ExecutionCompleted || !task.Evidence.PostconditionVerified {
				t.Fatalf("success evidence = %+v", task.Evidence)
			}
			if test.action != ActionDelete && engine.calls(test.action) != 1 {
				t.Fatalf("engine calls = %d, want one", engine.calls(test.action))
			}
			if test.action == ActionDelete && engine.calls(ActionDelete) != 1 {
				t.Fatalf("remove calls = %d, want one", engine.calls(ActionDelete))
			}
			stored, err := store.Get(context.Background(), req.TaskID)
			if err != nil || stored.Status != taskstate.Succeeded {
				t.Fatalf("persisted task = %+v, %v", stored, err)
			}
		})
	}
}

func TestDeleteConfirmationMustBindExactFullIDBeforeJournalWrite(t *testing.T) {
	engine := newFakeEngine(Container{ID: testContainerID, Name: "/nd-test"})
	executor, store := newTestExecutor(t, engine)
	req := request(ActionDelete)
	req.DeleteConfirmed = true
	req.DeleteConfirmationID = strings.Repeat("a", 63)
	if _, err := executor.Execute(context.Background(), req); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("Execute() error = %v, want invalid request", err)
	}
	if engine.calls(ActionDelete) != 0 || engine.inspectCalls != 0 {
		t.Fatalf("engine was touched: remove=%d inspect=%d", engine.calls(ActionDelete), engine.inspectCalls)
	}
	if _, err := store.Get(context.Background(), req.TaskID); !errors.Is(err, taskjournal.ErrTaskNotFound) {
		t.Fatalf("invalid confirmation wrote a task: %v", err)
	}
}

func TestComposeClassificationComesFromInspectAndBlocksRename(t *testing.T) {
	container := Container{ID: testContainerID, Name: "/nd-compose-web", ComposeManaged: true}
	engine := newFakeEngine(container)
	executor, _ := newTestExecutor(t, engine)
	req := request(ActionRename)
	req.NewName = "new-name"
	task, err := executor.Execute(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if task.Status != taskstate.Failed || engine.calls(ActionRename) != 0 {
		t.Fatalf("Compose rename status=%s calls=%d", task.Status, engine.calls(ActionRename))
	}
}

func TestRunningDeleteFailsWithoutStoppingOrRemoving(t *testing.T) {
	engine := newFakeEngine(runningContainer())
	executor, _ := newTestExecutor(t, engine)
	req := request(ActionDelete)
	req.DeleteConfirmed, req.DeleteConfirmationID = true, testContainerID
	task, err := executor.Execute(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if task.Status != taskstate.Failed || engine.calls(ActionStop) != 0 || engine.calls(ActionDelete) != 0 {
		t.Fatalf("running delete status=%s stop=%d remove=%d", task.Status, engine.calls(ActionStop), engine.calls(ActionDelete))
	}
}

func TestRestartRequiresFreshInstanceEvidenceAndUnknownNeverReplays(t *testing.T) {
	engine := newFakeEngine(runningContainer())
	engine.noOpRestart = true
	executor, store := newTestExecutor(t, engine)
	req := request(ActionRestart)
	task, err := executor.Execute(context.Background(), req)
	if !errors.Is(err, ErrOutcomeUnknown) || task.Status != taskstate.Unknown {
		t.Fatalf("Execute() = status %s, error %v; want unknown", task.Status, err)
	}
	if engine.calls(ActionRestart) != 1 {
		t.Fatalf("restart calls = %d, want one", engine.calls(ActionRestart))
	}
	if _, err := executor.Execute(context.Background(), req); !errors.Is(err, ErrOutcomeUnknown) {
		t.Fatalf("duplicate unknown action error = %v", err)
	}
	if engine.calls(ActionRestart) != 1 {
		t.Fatalf("unknown action was replayed: restart calls=%d", engine.calls(ActionRestart))
	}
	other := req
	other.TaskID, other.IdempotencyKey = "task-2", "idem-2"
	if _, err := executor.Execute(context.Background(), other); !errors.Is(err, taskjournal.ErrResourceBusy) {
		t.Fatalf("second task on claimed resource error = %v", err)
	}
	stored, err := store.Get(context.Background(), req.TaskID)
	if err != nil || stored.Status != taskstate.Unknown {
		t.Fatalf("unknown journal task = %+v, %v", stored, err)
	}
}

func TestAmbiguousMutationWithLostAckCanSucceedOnlyAfterInspect(t *testing.T) {
	engine := newFakeEngine(runningContainer())
	engine.mutationErrors[ActionRestart] = context.DeadlineExceeded
	executor, _ := newTestExecutor(t, engine)
	task, err := executor.Execute(context.Background(), request(ActionRestart))
	if err != nil {
		t.Fatalf("verified lost-ACK restart returned error: %v", err)
	}
	if task.Status != taskstate.Succeeded || task.Result.ResourceRevision != "started_at_changed" {
		t.Fatalf("task = %+v, want verified restart", task)
	}
}

func TestRestartCanUseRestartCountWhenStartedAtIsUnchanged(t *testing.T) {
	container := runningContainer()
	engine := newFakeEngine(container)
	engine.countRestart = true
	engine.keepRestartStartedAt = true
	executor, _ := newTestExecutor(t, engine)
	task, err := executor.Execute(context.Background(), request(ActionRestart))
	if err != nil || task.Status != taskstate.Succeeded || task.Result.ResourceRevision != "restart_count_increased" {
		t.Fatalf("restart = status %s, revision %q, error %v", task.Status, task.Result.ResourceRevision, err)
	}
}

func TestRestartStartedAtChangeAcceptsClockMovingBackward(t *testing.T) {
	if !startedAtChanged("2026-10-08T10:00:00Z", "2026-10-08T09:00:00Z") {
		t.Fatal("a different, nonzero post-restart StartedAt should prove a new start even if the wall clock moved backward")
	}
	if !startedAtChanged(dockerZeroStartedAt, "2026-10-08T09:00:00Z") {
		t.Fatal("the first start after Docker's zero StartedAt should prove a new start")
	}
	if startedAtChanged("not-a-time", "2026-10-08T09:00:00Z") || startedAtChanged("2026-10-08T10:00:00Z", dockerZeroStartedAt) {
		t.Fatal("invalid or zero post-restart StartedAt must not prove a new start")
	}
}

func TestRestartRejectsRunningInspectWithZeroStartedAt(t *testing.T) {
	container := runningContainer()
	container.StartedAt = dockerZeroStartedAt
	engine := newFakeEngine(container)
	executor, _ := newTestExecutor(t, engine)
	task, err := executor.Execute(context.Background(), request(ActionRestart))
	if err != nil || task.Status != taskstate.Failed || engine.calls(ActionRestart) != 0 {
		t.Fatalf("restart with inconsistent before snapshot = status %s, calls %d, err %v", task.Status, engine.calls(ActionRestart), err)
	}
}

func TestFailedPostMutationInspectIsUnknownAndRetainsClaim(t *testing.T) {
	engine := newFakeEngine(Container{ID: testContainerID, Name: "/nd-test"})
	engine.inspectErrors[2] = errors.New("not found: socket closed")
	executor, _ := newTestExecutor(t, engine)
	task, err := executor.Execute(context.Background(), request(ActionStart))
	if !errors.Is(err, ErrOutcomeUnknown) || task.Status != taskstate.Unknown {
		t.Fatalf("Execute() = status %s, error %v; want unknown", task.Status, err)
	}
}

func TestDeleteInspectTransportErrorContainingNotFoundDoesNotProveRemoval(t *testing.T) {
	engine := newFakeEngine(Container{ID: testContainerID, Name: "/nd-test"})
	engine.inspectErrors[2] = errors.New("not found in transport response; socket closed")
	executor, _ := newTestExecutor(t, engine)
	req := request(ActionDelete)
	req.DeleteConfirmed, req.DeleteConfirmationID = true, testContainerID
	task, err := executor.Execute(context.Background(), req)
	if !errors.Is(err, ErrOutcomeUnknown) || task.Status != taskstate.Unknown {
		t.Fatalf("delete = status %s, error %v; want unknown", task.Status, err)
	}
	if engine.calls(ActionDelete) != 1 {
		t.Fatalf("remove calls = %d, want one", engine.calls(ActionDelete))
	}
}

func TestConcurrentDuplicatesBeginAndMutateAtMostOnce(t *testing.T) {
	engine := newFakeEngine(Container{ID: testContainerID, Name: "/nd-test"})
	engine.startEntered = make(chan struct{}, 1)
	engine.continueStart = make(chan struct{})
	executor, _ := newTestExecutor(t, engine)
	request := request(ActionStart)
	type outcome struct {
		task taskjournal.Snapshot
		err  error
	}
	first := make(chan outcome, 1)
	go func() {
		task, err := executor.Execute(context.Background(), request)
		first <- outcome{task: task, err: err}
	}()
	select {
	case <-engine.startEntered:
	case <-time.After(3 * time.Second):
		t.Fatal("first executor did not reach its single Docker mutation")
	}
	secondTask, secondErr := executor.Execute(context.Background(), request)
	if !errors.Is(secondErr, ErrTaskInProgress) || secondTask.Status != taskstate.Running {
		t.Fatalf("duplicate = status %s, error %v; want running/in-progress", secondTask.Status, secondErr)
	}
	close(engine.continueStart)
	select {
	case result := <-first:
		if result.err != nil || result.task.Status != taskstate.Succeeded {
			t.Fatalf("first execution = %+v, %v", result.task, result.err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("first executor did not finish")
	}
	if engine.calls(ActionStart) != 1 {
		t.Fatalf("Start calls = %d, want one", engine.calls(ActionStart))
	}
}

func TestIntentPayloadMatchesCoreTypedContract(t *testing.T) {
	req := request(ActionDelete)
	req.DeleteConfirmed, req.DeleteConfirmationID = true, testContainerID
	identity, err := requestIdentity(req)
	if err != nil {
		t.Fatal(err)
	}
	want := fmt.Sprintf(`{"action":"delete","container_id":%q,"delete_confirmed":true}`, testContainerID)
	if string(identity.Payload) != want {
		t.Fatalf("canonical payload = %s, want %s", identity.Payload, want)
	}
	if strings.Contains(string(identity.Payload), "confirmation_id") {
		t.Fatal("ephemeral confirmation ID entered the persisted digest payload")
	}
}
