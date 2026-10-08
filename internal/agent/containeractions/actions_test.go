package containeractions

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
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
const testBootID = "00000000-0000-4000-8000-000000000001"

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

type crashBoundaryEngine struct {
	fake      *fakeEngine
	stage     string
	statePath string
}

func (e crashBoundaryEngine) Inspect(ctx context.Context, id string) (Container, error) {
	return e.fake.Inspect(ctx, id)
}
func (e crashBoundaryEngine) Start(ctx context.Context, id string) error {
	return e.fake.Start(ctx, id)
}
func (e crashBoundaryEngine) Stop(ctx context.Context, id string) error { return e.fake.Stop(ctx, id) }
func (e crashBoundaryEngine) Restart(ctx context.Context, id string) error {
	if e.stage == "before-mutation" {
		fmt.Println("READY")
		for {
			time.Sleep(time.Hour)
		}
	}
	if err := e.fake.Restart(ctx, id); err != nil {
		return err
	}
	container, err := e.fake.Inspect(ctx, id)
	if err != nil {
		return err
	}
	if err := os.WriteFile(e.statePath, []byte(fmt.Sprintf("%s\n%d\n", container.StartedAt, e.fake.calls(ActionRestart))), 0o600); err != nil {
		return err
	}
	fmt.Println("READY")
	for {
		time.Sleep(time.Hour)
	}
}
func (e crashBoundaryEngine) Pause(ctx context.Context, id string) error {
	return e.fake.Pause(ctx, id)
}
func (e crashBoundaryEngine) Resume(ctx context.Context, id string) error {
	return e.fake.Resume(ctx, id)
}
func (e crashBoundaryEngine) Remove(ctx context.Context, id string) error {
	return e.fake.Remove(ctx, id)
}
func (e crashBoundaryEngine) Rename(ctx context.Context, id, name string) error {
	return e.fake.Rename(ctx, id, name)
}

func newFakeEngine(container Container) *fakeEngine {
	if container.StartedAt == "" {
		container.StartedAt = dockerZeroStartedAt
	}
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
	executor, err := New(engine, store, Options{OperationTimeout: 3 * time.Second, VerificationTimeout: 2 * time.Second,
		BootIDSource: func(context.Context) (string, error) { return testBootID, nil }})
	if err != nil {
		t.Fatal(err)
	}
	return executor, store
}

type baselineFailJournal struct {
	*taskjournal.Store
}

func (journal baselineFailJournal) PrepareMutation(context.Context, string, taskjournal.ExecutionBaseline) error {
	return errors.New("injected baseline commit failure")
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
			if stored.ExecutionPhase != taskjournal.ExecutionPhaseResultPersisted || stored.Baseline == nil || stored.Baseline.HostBootID != testBootID {
				t.Fatalf("durable execution proof = phase %q baseline %+v", stored.ExecutionPhase, stored.Baseline)
			}
		})
	}
}

func TestBaselinePersistenceFailurePreventsDockerMutation(t *testing.T) {
	engine := newFakeEngine(Container{ID: testContainerID, Name: "/nd-test"})
	baseExecutor, store := newTestExecutor(t, engine)
	executor, err := New(engine, baselineFailJournal{Store: store}, Options{
		OperationTimeout: baseExecutor.operationTimeout, VerificationTimeout: baseExecutor.verificationTimeout,
		BootIDSource: func(context.Context) (string, error) { return testBootID, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	task, err := executor.Execute(context.Background(), request(ActionStart))
	if !errors.Is(err, ErrOutcomeUnknown) || task.Status != taskstate.Unknown {
		t.Fatalf("Execute() = status %s, error %v; want unknown", task.Status, err)
	}
	if engine.calls(ActionStart) != 0 {
		t.Fatalf("Docker Start was called %d times after baseline persistence failed", engine.calls(ActionStart))
	}
	stored, err := store.Get(context.Background(), task.TaskID)
	if err != nil || stored.ExecutionPhase != taskjournal.ExecutionPhaseNone || stored.Baseline != nil {
		t.Fatalf("failed baseline write changed durable proof: %+v, %v", stored, err)
	}
	if _, err := executor.Execute(context.Background(), request(ActionStart)); !errors.Is(err, ErrOutcomeUnknown) {
		t.Fatalf("duplicate unknown action error = %v", err)
	}
	if engine.calls(ActionStart) != 0 {
		t.Fatal("unknown action was replayed after baseline write failure")
	}
}

func TestExecutorCanonicalizesValidDockerStartedAtBeforePersistence(t *testing.T) {
	for _, input := range []string{
		"2026-10-08T11:00:00.000000000+01:00",
		"2026-10-08T10:00:00.000000000Z",
	} {
		t.Run(input, func(t *testing.T) {
			container := Container{ID: testContainerID, Name: "/nd-test", StartedAt: input}
			engine := newFakeEngine(container)
			executor, store := newTestExecutor(t, engine)
			task, err := executor.Execute(context.Background(), request(ActionStart))
			if err != nil || task.Status != taskstate.Succeeded {
				t.Fatalf("Execute with legal Docker timestamp %q = %s, %v", input, task.Status, err)
			}
			stored, err := store.Get(context.Background(), task.TaskID)
			if err != nil || stored.Baseline == nil || stored.Baseline.StartedAt != "2026-10-08T10:00:00Z" {
				t.Fatalf("stored canonical baseline = %+v, %v", stored.Baseline, err)
			}
		})
	}
	if _, ok := canonicalStartedAt("not-a-docker-time"); ok {
		t.Fatal("invalid Docker timestamp was accepted for a durable baseline")
	}
}

func TestBootIDSourceReceivesBoundedPreMutationContext(t *testing.T) {
	engine := newFakeEngine(Container{ID: testContainerID, Name: "/nd-test"})
	_, store := newTestExecutor(t, engine)
	executor, err := New(engine, store, Options{OperationTimeout: 3 * time.Second, VerificationTimeout: time.Second,
		BootIDSource: func(ctx context.Context) (string, error) {
			<-ctx.Done()
			return "", ctx.Err()
		}})
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	task, err := executor.Execute(context.Background(), request(ActionStart))
	if !errors.Is(err, ErrOutcomeUnknown) || task.Status != taskstate.Unknown {
		t.Fatalf("Execute with stalled boot source = %s, %v; want unknown", task.Status, err)
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("boot source was not bounded: %s", elapsed)
	}
	if engine.calls(ActionStart) != 0 {
		t.Fatalf("Docker Start called %d times without a boot identity", engine.calls(ActionStart))
	}
}

func TestReconcileRestartRequiresSameBootAndFreshPostcondition(t *testing.T) {
	t.Run("same boot with new start evidence", func(t *testing.T) {
		engine := newFakeEngine(runningContainer())
		engine.noOpRestart = true
		executor, store := newTestExecutor(t, engine)
		req := request(ActionRestart)
		task, err := executor.Execute(context.Background(), req)
		if !errors.Is(err, ErrOutcomeUnknown) || task.Status != taskstate.Unknown || task.Baseline == nil {
			t.Fatalf("Execute() = %+v, %v; want unknown with durable baseline", task, err)
		}
		engine.mu.Lock()
		container := engine.containers[testContainerID]
		container.StartedAt = "2026-10-08T08:00:00Z" // wall clock moved backward
		engine.containers[testContainerID] = container
		engine.mu.Unlock()
		resolved, err := executor.Reconcile(context.Background(), req.TaskID)
		if err != nil || resolved.Status != taskstate.Succeeded || resolved.Result.ResourceRevision != "reconciled_started_at_change" {
			t.Fatalf("Reconcile() = status %s revision %q, error %v", resolved.Status, resolved.Result.ResourceRevision, err)
		}
		if engine.calls(ActionRestart) != 1 {
			t.Fatalf("reconciliation replayed restart, calls=%d", engine.calls(ActionRestart))
		}
		if _, err := store.Get(context.Background(), req.TaskID); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("unchanged postcondition remains unknown", func(t *testing.T) {
		engine := newFakeEngine(runningContainer())
		engine.noOpRestart = true
		executor, _ := newTestExecutor(t, engine)
		req := request(ActionRestart)
		if _, err := executor.Execute(context.Background(), req); !errors.Is(err, ErrOutcomeUnknown) {
			t.Fatal(err)
		}
		resolved, err := executor.Reconcile(context.Background(), req.TaskID)
		if !errors.Is(err, ErrOutcomeUnknown) || resolved.Status != taskstate.Unknown {
			t.Fatalf("Reconcile() = status %s error %v; want unknown", resolved.Status, err)
		}
		if engine.calls(ActionRestart) != 1 {
			t.Fatalf("reconciliation replayed restart, calls=%d", engine.calls(ActionRestart))
		}
	})

	t.Run("boot mismatch remains unknown", func(t *testing.T) {
		engine := newFakeEngine(runningContainer())
		executor, _ := newTestExecutor(t, engine)
		req := request(ActionRestart)
		engine.noOpRestart = true
		if _, err := executor.Execute(context.Background(), req); !errors.Is(err, ErrOutcomeUnknown) {
			t.Fatal(err)
		}
		executor.bootIDSource = func(context.Context) (string, error) { return "00000000-0000-4000-8000-000000000002", nil }
		resolved, err := executor.Reconcile(context.Background(), req.TaskID)
		if !errors.Is(err, ErrOutcomeUnknown) || resolved.Status != taskstate.Unknown {
			t.Fatalf("Reconcile() = status %s error %v; want unknown", resolved.Status, err)
		}
		if engine.inspectCalls != 2 || engine.calls(ActionRestart) != 1 {
			t.Fatalf("boot mismatch should not inspect or replay: inspect=%d restart=%d", engine.inspectCalls, engine.calls(ActionRestart))
		}
	})
}

func TestContainerActionCrashChild(t *testing.T) {
	stage := os.Getenv("NODEDANCE_ACTION_CRASH_STAGE")
	if stage == "" {
		return
	}
	path := os.Getenv("NODEDANCE_ACTION_CRASH_DB")
	statePath := os.Getenv("NODEDANCE_ACTION_CRASH_STATE")
	state, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatalf("read child container state: %v", err)
	}
	fields := strings.Split(strings.TrimSpace(string(state)), "\n")
	if len(fields) != 2 {
		t.Fatalf("invalid child state fixture %q", state)
	}
	restarted, err := strconv.Atoi(fields[1])
	if err != nil {
		t.Fatal(err)
	}
	store, err := taskjournal.Open(context.Background(), path, "node-test")
	if err != nil {
		t.Fatalf("child open: %v", err)
	}
	container := runningContainer()
	container.StartedAt, container.RestartCount = fields[0], restarted
	fake := newFakeEngine(container)
	engine := crashBoundaryEngine{fake: fake, stage: stage, statePath: statePath}
	executor, err := New(engine, store, Options{OperationTimeout: 3 * time.Second, VerificationTimeout: 2 * time.Second,
		BootIDSource: func(context.Context) (string, error) { return testBootID, nil }})
	if err != nil {
		t.Fatalf("child executor: %v", err)
	}
	if _, err := executor.Execute(context.Background(), request(ActionRestart)); err != nil {
		t.Fatalf("child Execute: %v", err)
	}
	t.Fatal("crash boundary returned without parent SIGKILL")
}

func TestSIGKILLBeforeAndAfterDockerMutationIsNeverReplayed(t *testing.T) {
	for _, stage := range []string{"before-mutation", "after-mutation"} {
		t.Run(stage, func(t *testing.T) {
			parent := t.TempDir()
			if err := os.Chmod(parent, 0o700); err != nil {
				t.Fatal(err)
			}
			statePath := parent + "/container-state"
			originalStartedAt := "2026-10-08T09:00:00Z"
			if err := os.WriteFile(statePath, []byte(originalStartedAt+"\n0\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			dbPath := parent + "/journal.sqlite"
			killActionChildAtReady(t, stage, dbPath, statePath)

			store, err := taskjournal.Open(context.Background(), dbPath, "node-test")
			if err != nil {
				t.Fatalf("reopen journal after SIGKILL: %v", err)
			}
			defer store.Close()
			if recovered, err := store.RecoverInterrupted(context.Background()); err != nil || recovered != 1 {
				t.Fatalf("recover child task = %d, %v", recovered, err)
			}
			task, err := store.Get(context.Background(), "task-1")
			if err != nil || task.Status != taskstate.Unknown || task.Baseline == nil || task.ExecutionPhase != taskjournal.ExecutionPhaseMutationMayHaveStarted {
				t.Fatalf("recovered proof = %+v, %v", task, err)
			}
			persisted, err := os.ReadFile(statePath)
			if err != nil {
				t.Fatal(err)
			}
			fields := strings.Split(strings.TrimSpace(string(persisted)), "\n")
			if len(fields) != 2 {
				t.Fatalf("invalid persisted container state %q", persisted)
			}
			callCount, err := strconv.Atoi(fields[1])
			if err != nil {
				t.Fatal(err)
			}
			if stage == "before-mutation" && (fields[0] != originalStartedAt || callCount != 0) {
				t.Fatalf("pre-mutation SIGKILL changed resource: %q", persisted)
			}
			if stage == "after-mutation" && (fields[0] == originalStartedAt || callCount != 1) {
				t.Fatalf("post-mutation SIGKILL did not persist changed resource: %q", persisted)
			}

			observed := runningContainer()
			observed.StartedAt = fields[0]
			observed.RestartCount = callCount
			recoveryEngine := newFakeEngine(observed)
			executor, err := New(recoveryEngine, store, Options{OperationTimeout: 3 * time.Second, VerificationTimeout: 2 * time.Second,
				BootIDSource: func(context.Context) (string, error) { return testBootID, nil }})
			if err != nil {
				t.Fatal(err)
			}
			resolved, reconcileErr := executor.Reconcile(context.Background(), "task-1")
			if stage == "before-mutation" {
				if !errors.Is(reconcileErr, ErrOutcomeUnknown) || resolved.Status != taskstate.Unknown {
					t.Fatalf("unchanged pre-mutation recovery = %s, %v; want unknown", resolved.Status, reconcileErr)
				}
				if _, err := executor.Execute(context.Background(), request(ActionRestart)); !errors.Is(err, ErrOutcomeUnknown) {
					t.Fatalf("unknown restart was eligible for replay: %v", err)
				}
			} else if reconcileErr != nil || resolved.Status != taskstate.Succeeded {
				t.Fatalf("post-mutation recovery = %s, %v; want verified success", resolved.Status, reconcileErr)
			}
			if recoveryEngine.calls(ActionRestart) != 0 {
				t.Fatalf("reconciliation issued %d restart mutations", recoveryEngine.calls(ActionRestart))
			}
		})
	}
}

func killActionChildAtReady(t *testing.T, stage, dbPath, statePath string) {
	t.Helper()
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	childCtx, cancelChild := context.WithTimeout(context.Background(), 20*time.Second)
	command := exec.CommandContext(childCtx, binary, "-test.run=^TestContainerActionCrashChild$")
	command.Env = append(os.Environ(), "NODEDANCE_ACTION_CRASH_STAGE="+stage,
		"NODEDANCE_ACTION_CRASH_DB="+dbPath, "NODEDANCE_ACTION_CRASH_STATE="+statePath)
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
	readyResult := make(chan struct {
		line string
		err  error
	}, 1)
	readyReadDone := make(chan struct{})
	go func() {
		defer close(readyReadDone)
		line, readErr := bufio.NewReader(stdout).ReadString('\n')
		readyResult <- struct {
			line string
			err  error
		}{line: line, err: readErr}
	}()
	childWaited := false
	var childWaitErr error
	awaitChild := func(timeout time.Duration) bool {
		if childWaited {
			return true
		}
		select {
		case childWaitErr = <-waitResult:
			childWaited = true
			return true
		case <-time.After(timeout):
			return false
		}
	}
	killChild := func() bool {
		if !childWaited && command.Process != nil {
			_ = command.Process.Kill()
		}
		return awaitChild(5 * time.Second)
	}
	t.Cleanup(func() {
		if !childWaited {
			cancelChild()
			if command.Process != nil {
				_ = command.Process.Kill()
			}
			if !awaitChild(5 * time.Second) {
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
	select {
	case ready := <-readyResult:
		if ready.err != nil || strings.TrimSpace(ready.line) != "READY" {
			if !killChild() {
				t.Fatal("child did not exit after invalid READY result")
			}
			t.Fatalf("child checkpoint %q error %v stderr %s", ready.line, ready.err, stderr.String())
		}
	case <-time.After(5 * time.Second):
		if !killChild() {
			t.Fatal("child did not exit after bounded READY timeout")
		}
		t.Fatalf("child did not emit READY: %s", stderr.String())
	case <-childCtx.Done():
		if !killChild() {
			t.Fatal("child did not exit after process timeout")
		}
		t.Fatalf("child context expired before READY: %s", stderr.String())
	}
	if !killChild() {
		t.Fatal("child did not exit after SIGKILL within five seconds")
	}
	if childWaitErr == nil {
		t.Fatal("SIGKILLed child exited successfully")
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
	baseline := taskjournal.ExecutionBaseline{StartedAt: "2026-10-08T10:00:00Z", RestartCount: 2}
	equivalentInstant := Container{Running: true, StartedAt: "2026-10-08T11:00:00.000000000+01:00", RestartCount: 2}
	if reconciledRestartEvidence(baseline, equivalentInstant) {
		t.Fatal("equivalent instants with different timestamp encodings must not prove a restart")
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
