package images_test

import (
	"context"
	"strings"
	"testing"

	"github.com/containerd/errdefs"

	agentimages "github.com/CST-Cat/NodeDance/internal/agent/images"
	"github.com/CST-Cat/NodeDance/internal/agent/taskjournal"
	"github.com/CST-Cat/NodeDance/internal/protocol"
	"github.com/CST-Cat/NodeDance/internal/taskstate"
)

type fakeEngine struct {
	inspect     func(context.Context, string) (agentimages.Image, error)
	pull        func(context.Context, string, string, func(agentimages.PullProgress)) error
	containers  []string
	removeError error
	removed     []string
}

func (e *fakeEngine) List(context.Context) ([]agentimages.Image, error) { return nil, nil }
func (e *fakeEngine) Inspect(ctx context.Context, reference string) (agentimages.Image, error) {
	return e.inspect(ctx, reference)
}
func (e *fakeEngine) Pull(ctx context.Context, reference, authHeader string, progress func(agentimages.PullProgress)) error {
	return e.pull(ctx, reference, authHeader, progress)
}
func (e *fakeEngine) ContainersUsing(context.Context, string) ([]string, error) {
	return e.containers, nil
}
func (e *fakeEngine) Remove(ctx context.Context, imageID string) error {
	if e.removeError != nil {
		return e.removeError
	}
	e.removed = append(e.removed, imageID)
	return nil
}

type fakeJournal struct {
	snapshot  taskjournal.Snapshot
	begun     int
	finished  int
	unknowns  int
	progress  []taskjournal.Progress
	nowStatus taskstate.Status
}

func (j *fakeJournal) Get(context.Context, string) (taskjournal.Snapshot, error) {
	snapshot := j.snapshot
	if j.nowStatus != "" {
		snapshot.Status = j.nowStatus
	}
	return snapshot, nil
}
func (j *fakeJournal) BeginExecution(context.Context, string) error {
	j.begun++
	j.nowStatus = taskstate.Running
	j.snapshot.Evidence.ExecutionAttempted = true
	return nil
}
func (j *fakeJournal) UpdateProgress(_ context.Context, _ string, progress taskjournal.Progress) error {
	j.progress = append(j.progress, progress)
	return nil
}
func (j *fakeJournal) Finish(_ context.Context, _ string, status taskstate.Status, evidence taskstate.Evidence, result taskjournal.Result) error {
	j.finished++
	j.nowStatus = status
	j.snapshot.Status = status
	j.snapshot.Evidence = evidence
	j.snapshot.Result = result
	return nil
}
func (j *fakeJournal) MarkUnknown(context.Context, string) error {
	j.unknowns++
	j.nowStatus = taskstate.Unknown
	return nil
}

func pullDispatch(reference string, credentials *protocol.RegistryCredentials) protocol.TaskDispatch {
	intent := protocol.TaskIntent{Action: protocol.TaskImagePull, ImageReference: reference, ContainerID: protocol.ImageTargetKey("pull:" + reference)}
	return protocol.TaskDispatch{
		TaskID: "task-1", NodeID: "node-1", TargetID: intent.ContainerID,
		IdempotencyKey: "idem-1", Intent: intent, RegistryAuth: credentials,
	}
}

func queuedSnapshot(dispatch protocol.TaskDispatch) taskjournal.Snapshot {
	identity, err := protocol.TaskIdentity(dispatch.TaskID, dispatch.NodeID, dispatch.IdempotencyKey, dispatch.Intent)
	if err != nil {
		panic(err)
	}
	digest, err := taskstate.RequestDigest(identity)
	if err != nil {
		panic(err)
	}
	return taskjournal.Snapshot{
		TaskID: dispatch.TaskID, NodeID: dispatch.NodeID, TargetID: dispatch.TargetID,
		Action: string(dispatch.Intent.Action), Status: taskstate.Queued,
		IdempotencyKey: dispatch.IdempotencyKey, RequestDigest: digest,
	}
}

func TestImageExecutorRejectsForeignDispatch(t *testing.T) {
	engine := &fakeEngine{inspect: func(context.Context, string) (agentimages.Image, error) {
		return agentimages.Image{}, errdefs.ErrNotFound
	}}
	journal := &fakeJournal{snapshot: queuedSnapshot(pullDispatch("nginx:1", nil))}
	executor, err := agentimages.NewExecutor(engine, journal)
	if err != nil {
		t.Fatal(err)
	}
	dispatch := pullDispatch("nginx:1", nil)
	dispatch.TargetID = protocol.ImageTargetKey("pull:redis:7")
	if _, err := executor.ExecuteImage(context.Background(), dispatch, nil); err == nil {
		t.Fatal("a dispatch for another target was accepted")
	}
	if journal.begun != 0 {
		t.Fatal("rejected dispatch started execution")
	}
}

func TestImageExecutorReturnsFinishedTaskWithoutExecution(t *testing.T) {
	engine := &fakeEngine{}
	snapshot := queuedSnapshot(pullDispatch("nginx:1", nil))
	snapshot.Status = taskstate.Succeeded
	journal := &fakeJournal{snapshot: snapshot, nowStatus: taskstate.Succeeded}
	executor, _ := agentimages.NewExecutor(engine, journal)
	result, err := executor.ExecuteImage(context.Background(), pullDispatch("nginx:1", nil), nil)
	if err != nil || result.Status != taskstate.Succeeded {
		t.Fatalf("finished task: %+v %v", result, err)
	}
	if journal.begun != 0 || journal.finished != 0 {
		t.Fatalf("finished task was re-executed: begun=%d finished=%d", journal.begun, journal.finished)
	}
}

func TestImageExecutorPullsVerifiesAndWipesCredentials(t *testing.T) {
	pulled := false
	engine := &fakeEngine{
		inspect: func(_ context.Context, reference string) (agentimages.Image, error) {
			if !pulled {
				return agentimages.Image{}, errdefs.ErrNotFound
			}
			return agentimages.Image{ID: "sha256:" + string(make([]byte, 0)), Tags: []string{"nginx:1"}}, nil
		},
		pull: func(_ context.Context, reference, authHeader string, progress func(agentimages.PullProgress)) error {
			pulled = true
			if authHeader == "" {
				t.Fatal("registry credentials were not forwarded to the Engine")
			}
			progress(agentimages.PullProgress{Completed: 50, Total: 100})
			return nil
		},
	}
	credentials := &protocol.RegistryCredentials{Username: "user", Password: "secret"}
	dispatch := pullDispatch("nginx:1", credentials)
	journal := &fakeJournal{snapshot: queuedSnapshot(dispatch)}
	executor, _ := agentimages.NewExecutor(engine, journal)
	result, err := executor.ExecuteImage(context.Background(), dispatch, nil)
	if err != nil || result.Status != taskstate.Succeeded {
		t.Fatalf("pull outcome: %+v %v", result.Status, err)
	}
	if result.Result.Code != taskjournal.ResultVerified || result.Result.ObservedState != "present" {
		t.Fatalf("verified pull result: %+v", result.Result)
	}
	if credentials.Username != "" || credentials.Password != "" {
		t.Fatalf("registry credentials survived the dispatch: %+v", credentials)
	}
	if len(journal.progress) == 0 || journal.progress[len(journal.progress)-1].Total != 100 {
		t.Fatalf("pull progress was not persisted: %+v", journal.progress)
	}
	if journal.begun != 1 || journal.finished != 1 {
		t.Fatalf("execution bookkeeping: begun=%d finished=%d", journal.begun, journal.finished)
	}
}

func TestImageExecutorClassifiesRegistryAuthFailure(t *testing.T) {
	engine := &fakeEngine{
		inspect: func(context.Context, string) (agentimages.Image, error) {
			return agentimages.Image{}, errdefs.ErrNotFound
		},
		pull: func(context.Context, string, string, func(agentimages.PullProgress)) error {
			return agentimages.ErrUnauthorized
		},
	}
	dispatch := pullDispatch("nginx:1", &protocol.RegistryCredentials{Username: "user", Password: "wrong"})
	journal := &fakeJournal{snapshot: queuedSnapshot(dispatch)}
	executor, _ := agentimages.NewExecutor(engine, journal)
	result, err := executor.ExecuteImage(context.Background(), dispatch, nil)
	if err != nil || result.Status != taskstate.Failed {
		t.Fatalf("auth failure outcome: %+v %v", result.Status, err)
	}
	if result.Result.ObservedState != "registry_auth_failed" {
		t.Fatalf("auth failure classification: %+v", result.Result)
	}
	if journal.unknowns != 0 {
		t.Fatalf("a confirmed failure must not become unknown: %d", journal.unknowns)
	}
}

func TestImageExecutorRefusesInUseRemoval(t *testing.T) {
	imageID := "sha256:" + strings.Repeat("a", 64)
	intent := protocol.TaskIntent{Action: protocol.TaskImageDelete, ImageID: imageID, ContainerID: protocol.ImageTargetKey("delete:" + imageID)}
	dispatch := protocol.TaskDispatch{TaskID: "task-2", NodeID: "node-1", TargetID: intent.ContainerID,
		IdempotencyKey: "idem-2", Intent: intent}
	engine := &fakeEngine{
		inspect: func(_ context.Context, target string) (agentimages.Image, error) {
			if target == imageID {
				return agentimages.Image{ID: imageID}, nil
			}
			return agentimages.Image{}, errdefs.ErrNotFound
		},
		containers: []string{"container-1"},
	}
	journal := &fakeJournal{snapshot: queuedSnapshot(dispatch)}
	executor, _ := agentimages.NewExecutor(engine, journal)
	result, err := executor.ExecuteImage(context.Background(), dispatch, nil)
	if err != nil || result.Status != taskstate.Failed {
		t.Fatalf("in-use outcome: %+v %v", result.Status, err)
	}
	if result.Result.ObservedState != "in_use" || len(engine.removed) != 0 {
		t.Fatalf("in-use removal must not call Remove: %+v removed=%v", result.Result, engine.removed)
	}
}

func TestImageExecutorReconcileReadsOnly(t *testing.T) {
	imageID := "sha256:" + strings.Repeat("b", 64)
	intent := protocol.TaskIntent{Action: protocol.TaskImageDelete, ImageID: imageID, ContainerID: protocol.ImageTargetKey("delete:" + imageID)}
	snapshot := queuedSnapshot(protocol.TaskDispatch{TaskID: "task-3", NodeID: "node-1", TargetID: intent.ContainerID,
		IdempotencyKey: "idem-3", Intent: intent})
	snapshot.Status = taskstate.Unknown
	engine := &fakeEngine{inspect: func(_ context.Context, target string) (agentimages.Image, error) {
		if target == imageID {
			return agentimages.Image{ID: imageID}, nil
		}
		return agentimages.Image{}, errdefs.ErrNotFound
	}}
	journal := &fakeJournal{snapshot: snapshot, nowStatus: taskstate.Unknown}
	executor, _ := agentimages.NewExecutor(engine, journal)
	result, err := executor.ReconcileImage(context.Background(), "task-3", intent)
	if err != nil || result.Status != taskstate.Failed || result.Result.ObservedState != "still_present" {
		t.Fatalf("reconcile a still-present image: %+v %v", result, err)
	}
	if journal.begun != 0 {
		t.Fatal("reconciliation must never re-execute")
	}
	absentJournal := &fakeJournal{snapshot: snapshot, nowStatus: taskstate.Unknown}
	absentEngine := &fakeEngine{inspect: func(context.Context, string) (agentimages.Image, error) {
		return agentimages.Image{}, errdefs.ErrNotFound
	}}
	absentExecutor, _ := agentimages.NewExecutor(absentEngine, absentJournal)
	result, err = absentExecutor.ReconcileImage(context.Background(), "task-3", intent)
	if err != nil || result.Status != taskstate.Succeeded || result.Result.ObservedState != "absent" {
		t.Fatalf("reconcile an absent image: %+v %v", result, err)
	}
}
