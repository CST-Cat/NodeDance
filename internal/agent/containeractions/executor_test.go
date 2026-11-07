package containeractions_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	agentactions "github.com/CST-Cat/NodeDance/internal/agent/containeractions"
	"github.com/CST-Cat/NodeDance/internal/agent/taskjournal"
	"github.com/CST-Cat/NodeDance/internal/taskstate"
	"github.com/containerd/errdefs"
)

const containerID = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

type fakeEngine struct {
	containers   map[string]agentactions.Container
	inspectError error
	noOpStart    bool
	calls        []string
}

func (e *fakeEngine) Inspect(_ context.Context, id string) (agentactions.Container, error) {
	e.calls = append(e.calls, "inspect")
	if e.inspectError != nil {
		return agentactions.Container{}, e.inspectError
	}
	if container, ok := e.containers[id]; ok {
		return container, nil
	}
	return agentactions.Container{}, errdefs.ErrNotFound
}

func (e *fakeEngine) Start(_ context.Context, id string) error {
	e.calls = append(e.calls, "start:"+id)
	if e.noOpStart {
		return nil
	}
	if container, ok := e.containers[id]; ok {
		container.Running, container.StartedAt = true, "2026-11-06T12:00:00Z"
		e.containers[id] = container
	}
	return nil
}
func (e *fakeEngine) Stop(_ context.Context, id string) error {
	e.calls = append(e.calls, "stop:"+id)
	if container, ok := e.containers[id]; ok {
		container.Running, container.StartedAt = false, ""
		e.containers[id] = container
	}
	return nil
}
func (e *fakeEngine) Restart(_ context.Context, id string) error {
	e.calls = append(e.calls, "restart:"+id)
	return nil
}
func (e *fakeEngine) Pause(_ context.Context, id string) error {
	e.calls = append(e.calls, "pause:"+id)
	return nil
}
func (e *fakeEngine) Resume(_ context.Context, id string) error {
	e.calls = append(e.calls, "resume:"+id)
	return nil
}
func (e *fakeEngine) Remove(_ context.Context, id string) error {
	e.calls = append(e.calls, "remove:"+id)
	delete(e.containers, id)
	return nil
}
func (e *fakeEngine) Rename(_ context.Context, id, name string) error {
	e.calls = append(e.calls, "rename:"+id+":"+name)
	if container, ok := e.containers[id]; ok {
		container.Name = name
		e.containers[id] = container
	}
	return nil
}

type fakeJournal struct {
	status          taskstate.Status
	evidence        taskstate.Evidence
	result          taskjournal.Result
	phase           taskjournal.ExecutionPhase
	baseline        *taskjournal.ExecutionBaseline
	beginError      error
	prepareError    error
	finished        []taskstate.Status
	unknowns        int
	baselines       []taskjournal.ExecutionBaseline
	enqueuedActions []string
	getSnapshot     *taskjournal.Snapshot
}

func (j *fakeJournal) Enqueue(_ context.Context, identity taskstate.Identity) (taskjournal.EnqueueResult, error) {
	j.enqueuedActions = append(j.enqueuedActions, identity.Action)
	return taskjournal.EnqueueResult{Task: taskjournal.Snapshot{
		TaskID: identity.TaskID, NodeID: identity.NodeID, TargetID: identity.TargetID,
		Action: identity.Action, Status: j.status, Evidence: j.evidence,
		IdempotencyKey: identity.IdempotencyKey,
	}}, nil
}
func (j *fakeJournal) BeginExecution(context.Context, string) error {
	if j.beginError != nil {
		return j.beginError
	}
	j.status = taskstate.Running
	j.evidence.ExecutionAttempted = true
	return nil
}
func (j *fakeJournal) PrepareMutation(_ context.Context, _ string, baseline taskjournal.ExecutionBaseline) error {
	if j.prepareError != nil {
		return j.prepareError
	}
	j.baselines = append(j.baselines, baseline)
	return nil
}
func (j *fakeJournal) Finish(_ context.Context, _ string, status taskstate.Status, evidence taskstate.Evidence, result taskjournal.Result) error {
	j.status, j.evidence, j.result = status, evidence, result
	j.finished = append(j.finished, status)
	return nil
}
func (j *fakeJournal) MarkUnknown(context.Context, string) error {
	j.unknowns++
	j.status = taskstate.Unknown
	return nil
}
func (j *fakeJournal) Get(context.Context, string) (taskjournal.Snapshot, error) {
	if j.getSnapshot != nil {
		merged := *j.getSnapshot
		if len(j.finished) > 0 {
			merged.Status, merged.Evidence, merged.Result = j.status, j.evidence, j.result
		}
		return merged, nil
	}
	return taskjournal.Snapshot{TaskID: "task-1", Status: j.status, Evidence: j.evidence, Result: j.result}, nil
}

func startRequest() agentactions.Request {
	return agentactions.Request{TaskID: "task-1", NodeID: "node-1", IdempotencyKey: "idem-1",
		Action: agentactions.ActionStart, ContainerID: containerID}
}

func runningContainer() agentactions.Container {
	return agentactions.Container{ID: containerID, Name: "/web", Image: "nginx:1", Running: true,
		StartedAt: "2026-11-06T11:00:00Z"}
}

func TestContainerActionsAlreadySatisfiedSkipsMutation(t *testing.T) {
	engine := &fakeEngine{containers: map[string]agentactions.Container{containerID: runningContainer()}}
	journal := &fakeJournal{status: taskstate.Queued}
	executor, err := agentactions.New(engine, journal, agentactions.Options{BootIDSource: func(context.Context) (string, error) {
		return "11111111-1111-1111-1111-111111111111", nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	result, err := executor.Execute(context.Background(), startRequest())
	if err != nil || result.Status != taskstate.Succeeded {
		t.Fatalf("already-satisfied start: %+v %v", result.Status, err)
	}
	for _, call := range engine.calls {
		if strings.HasPrefix(call, "start:") {
			t.Fatalf("a satisfied start still mutated Docker: %v", engine.calls)
		}
	}
	if len(journal.baselines) != 0 {
		t.Fatal("a satisfied start must not persist a mutation baseline")
	}
}

func TestContainerActionsBaselineFailurePreventsMutation(t *testing.T) {
	engine := &fakeEngine{containers: map[string]agentactions.Container{containerID: {ID: containerID, Name: "/web", StartedAt: "2026-11-06T11:00:00Z"}}}
	journal := &fakeJournal{status: taskstate.Queued, prepareError: errors.New("disk full")}
	executor, _ := agentactions.New(engine, journal, agentactions.Options{BootIDSource: func(context.Context) (string, error) {
		return "11111111-1111-1111-1111-111111111111", nil
	}})
	result, err := executor.Execute(context.Background(), startRequest())
	if err == nil || result.Status != taskstate.Unknown {
		t.Fatalf("baseline failure outcome: %+v %v", result.Status, err)
	}
	for _, call := range engine.calls {
		if strings.HasPrefix(call, "start:") {
			t.Fatal("an unpersisted baseline must never authorize a Docker mutation")
		}
	}
}

func TestContainerActionsBootIDUnavailableIsConservative(t *testing.T) {
	engine := &fakeEngine{containers: map[string]agentactions.Container{containerID: {ID: containerID, Name: "/web", StartedAt: "2026-11-06T11:00:00Z"}}}
	journal := &fakeJournal{status: taskstate.Queued}
	executor, _ := agentactions.New(engine, journal, agentactions.Options{BootIDSource: func(context.Context) (string, error) {
		return "", errors.New("no /proc/sys/kernel/random/boot_id")
	}})
	result, err := executor.Execute(context.Background(), startRequest())
	if err == nil || result.Status != taskstate.Unknown {
		t.Fatalf("boot id failure outcome: %+v %v", result.Status, err)
	}
	for _, call := range engine.calls {
		if strings.HasPrefix(call, "start:") {
			t.Fatal("unknown boot identity must not authorize a mutation")
		}
	}
}

func TestContainerActionsRefusesRunningDelete(t *testing.T) {
	engine := &fakeEngine{containers: map[string]agentactions.Container{containerID: runningContainer()}}
	journal := &fakeJournal{status: taskstate.Queued}
	executor, _ := agentactions.New(engine, journal, agentactions.Options{})
	request := agentactions.Request{TaskID: "task-1", NodeID: "node-1", IdempotencyKey: "idem-1",
		Action: agentactions.ActionDelete, ContainerID: containerID, DeleteConfirmed: true, DeleteConfirmationID: containerID}
	result, err := executor.Execute(context.Background(), request)
	if err != nil || result.Status != taskstate.Failed {
		t.Fatalf("running delete outcome: %+v %v", result.Status, err)
	}
	if result.Result.ObservedState != "running" {
		t.Fatalf("running delete classification: %+v", result.Result)
	}
	for _, call := range engine.calls {
		if strings.HasPrefix(call, "remove:") {
			t.Fatal("a running container must never be removed")
		}
	}
}

func TestContainerActionsDeleteMissingSucceedsWithoutMutation(t *testing.T) {
	engine := &fakeEngine{inspectError: errdefs.ErrNotFound}
	journal := &fakeJournal{status: taskstate.Queued}
	executor, _ := agentactions.New(engine, journal, agentactions.Options{})
	request := agentactions.Request{TaskID: "task-1", NodeID: "node-1", IdempotencyKey: "idem-1",
		Action: agentactions.ActionDelete, ContainerID: containerID, DeleteConfirmed: true, DeleteConfirmationID: containerID}
	result, err := executor.Execute(context.Background(), request)
	if err != nil || result.Status != taskstate.Succeeded || result.Result.ObservedState != "missing" {
		t.Fatalf("delete missing outcome: %+v %v", result.Status, err)
	}
	for _, call := range engine.calls {
		if strings.HasPrefix(call, "remove:") {
			t.Fatal("an absent container must not be removed")
		}
	}
}

func TestContainerActionsStartVerifiesPostcondition(t *testing.T) {
	engine := &fakeEngine{containers: map[string]agentactions.Container{containerID: {ID: containerID, Name: "/web", StartedAt: "2026-11-06T11:00:00Z"}}}
	journal := &fakeJournal{status: taskstate.Queued}
	executor, _ := agentactions.New(engine, journal, agentactions.Options{BootIDSource: func(context.Context) (string, error) {
		return "11111111-1111-1111-1111-111111111111", nil
	}})
	result, err := executor.Execute(context.Background(), startRequest())
	if err != nil || result.Status != taskstate.Succeeded || result.Result.ObservedState != "running" {
		t.Fatalf("start outcome: %+v %v", result.Status, err)
	}
	if len(journal.baselines) != 1 || journal.baselines[0].TargetID != containerID || journal.baselines[0].Running {
		t.Fatalf("baseline was not persisted before mutation: %+v", journal.baselines)
	}
	starts := 0
	for _, call := range engine.calls {
		if call == "start:"+containerID {
			starts++
		}
	}
	if starts != 1 {
		t.Fatalf("exactly one Docker start is allowed, got %d: %v", starts, engine.calls)
	}
}

func TestContainerActionsUnverifiedMutationStaysUnknown(t *testing.T) {
	// The Engine accepts the call but the observed state never changes: the
	// request may or may not have reached the daemon, so the outcome is unknown.
	engine := &fakeEngine{containers: map[string]agentactions.Container{containerID: {ID: containerID, Name: "/web", StartedAt: "2026-11-06T11:00:00Z"}}, noOpStart: true}
	journal := &fakeJournal{status: taskstate.Queued}
	executor, _ := agentactions.New(engine, journal, agentactions.Options{BootIDSource: func(context.Context) (string, error) {
		return "11111111-1111-1111-1111-111111111111", nil
	}})
	result, err := executor.Execute(context.Background(), startRequest())
	if err == nil || result.Status != taskstate.Unknown {
		t.Fatalf("unverified mutation outcome: %+v %v", result.Status, err)
	}
	if !errors.Is(err, agentactions.ErrOutcomeUnknown) {
		t.Fatalf("unverified mutation error: %v", err)
	}
	if len(journal.finished) != 0 {
		t.Fatalf("an unknown outcome must not be finished: %v", journal.finished)
	}
}

func TestContainerActionsReconcileRequiresVerifiedBaseline(t *testing.T) {
	bootID := "11111111-1111-1111-1111-111111111111"
	baselineStartedAt := "2026-11-06T11:00:00Z"
	baseline := taskjournal.ExecutionBaseline{TargetID: containerID, Action: "restart", HostBootID: bootID,
		StartedAt: baselineStartedAt, RestartCount: 0, Running: true}
	unknown := &taskjournal.Snapshot{
		TaskID: "task-1", TargetID: containerID, Action: "restart", Status: taskstate.Unknown,
		ExecutionPhase: taskjournal.ExecutionPhaseMutationMayHaveStarted, Baseline: &baseline,
		Evidence: taskstate.Evidence{ExecutionAttempted: true},
	}

	engine := &fakeEngine{containers: map[string]agentactions.Container{containerID: {
		ID: containerID, Name: "/web", Running: true, StartedAt: "2026-11-06T12:00:00Z", RestartCount: 0}}}
	journal := &fakeJournal{getSnapshot: unknown}
	executor, _ := agentactions.New(engine, journal, agentactions.Options{BootIDSource: func(context.Context) (string, error) {
		return bootID, nil
	}})

	// A terminal task is never reconciled again.
	finished := *unknown
	finished.Status = taskstate.Succeeded
	journal.getSnapshot = &finished
	if _, err := executor.Reconcile(context.Background(), "task-1"); !errors.Is(err, agentactions.ErrTaskNotReconcileable) {
		t.Fatalf("finished task reconcile: %v", err)
	}

	// A verified post-baseline restart-like change resolves the unknown task.
	journal.getSnapshot = unknown
	result, err := executor.Reconcile(context.Background(), "task-1")
	if err != nil || result.Status != taskstate.Succeeded {
		t.Fatalf("verified reconcile: %+v %v", result, err)
	}
	if result.Result.ResourceRevision != "reconciled_started_at_change" {
		t.Fatalf("reconcile revision: %+v", result.Result)
	}

	// A different host boot invalidates the durable baseline.
	otherBoot := &taskjournal.Snapshot{
		TaskID: "task-1", TargetID: containerID, Action: "restart", Status: taskstate.Unknown,
		ExecutionPhase: taskjournal.ExecutionPhaseMutationMayHaveStarted, Baseline: &baseline,
	}
	otherBoot.Baseline = &baseline
	otherBootJournal := &fakeJournal{getSnapshot: otherBoot}
	otherBootExecutor, _ := agentactions.New(engine, otherBootJournal, agentactions.Options{BootIDSource: func(context.Context) (string, error) {
		return "22222222-2222-2222-2222-222222222222", nil
	}})
	if _, err := otherBootExecutor.Reconcile(context.Background(), "task-1"); !errors.Is(err, agentactions.ErrBaselineUnverifiable) {
		t.Fatalf("reconcile across boots: %v", err)
	}

	// An unchanged observation proves nothing.
	unchanged := &taskjournal.Snapshot{
		TaskID: "task-1", TargetID: containerID, Action: "restart", Status: taskstate.Unknown,
		ExecutionPhase: taskjournal.ExecutionPhaseMutationMayHaveStarted, Baseline: &baseline,
	}
	unchangedEngine := &fakeEngine{containers: map[string]agentactions.Container{containerID: {
		ID: containerID, Name: "/web", Running: true, StartedAt: baselineStartedAt, RestartCount: 0}}}
	unchangedJournal := &fakeJournal{getSnapshot: unchanged}
	unchangedExecutor, _ := agentactions.New(unchangedEngine, unchangedJournal, agentactions.Options{BootIDSource: func(context.Context) (string, error) {
		return bootID, nil
	}})
	if _, err := unchangedExecutor.Reconcile(context.Background(), "task-1"); !errors.Is(err, agentactions.ErrBaselineUnverifiable) {
		t.Fatalf("reconcile without observed change: %v", err)
	}
}
