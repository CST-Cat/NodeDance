package docker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/containerd/errdefs"
)

type fakeEngine struct {
	mu         sync.Mutex
	ids        []string
	containers map[string]Container
	listErr    error
	inspectErr map[string]error
	pingErr    error
	opened     chan struct{}
	streamDone chan struct{}
	closeCalls int
}

func (e *fakeEngine) Ping(context.Context) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.pingErr
}

func (e *fakeEngine) ListAll(context.Context) ([]string, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.ids...), e.listErr
}

func (e *fakeEngine) Inspect(_ context.Context, id string) (Container, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if err := e.inspectErr[id]; err != nil {
		return Container{}, err
	}
	container, ok := e.containers[id]
	if !ok {
		return Container{}, errors.New("not found")
	}
	return cloneContainer(container), nil
}

func (e *fakeEngine) OpenEvents(ctx context.Context) (EventStream, error) {
	streamCtx, cancel := context.WithCancel(ctx)
	events := make(chan Event)
	errorsOut := make(chan error)
	closed := make(chan struct{})
	if e.opened != nil {
		select {
		case e.opened <- struct{}{}:
		default:
		}
	}
	go func() {
		<-streamCtx.Done()
		close(events)
		close(errorsOut)
		if e.streamDone != nil {
			close(e.streamDone)
		}
		close(closed)
	}()
	return EventStream{
		Events: events, Errors: errorsOut,
		Close: func() {
			cancel()
			<-closed
		},
	}, nil
}

func (e *fakeEngine) Close() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.closeCalls++
	return nil
}

func baseContainer(id, name, state string) Container {
	return Container{ID: id, Name: name, Image: "test:image", State: state, Health: HealthNone, ObservedAt: time.Date(2026, 10, 8, 0, 0, 0, 0, time.UTC)}
}

func TestScanUsesFullInventoryAndRejectsIncompleteInspect(t *testing.T) {
	engine := &fakeEngine{
		ids: []string{"stopped", "running"},
		containers: map[string]Container{
			"stopped": baseContainer("stopped", "nd-stopped", "exited"),
			"running": baseContainer("running", "nd-web", "running"),
		},
		inspectErr: make(map[string]error),
	}
	discoverer, err := NewDiscoverer(engine, nil, Options{})
	if err != nil {
		t.Fatal(err)
	}
	complete := discoverer.scan(context.Background())
	if !complete.complete || len(complete.containers) != 2 || complete.containers[0].ID != "running" || complete.containers[1].ID != "stopped" {
		t.Fatalf("all states must be discovered from sorted full inventory: %+v", complete)
	}

	engine.inspectErr["stopped"] = errors.New("permission denied during Inspect")
	incomplete := discoverer.scan(context.Background())
	if incomplete.complete || incomplete.err == nil || len(incomplete.containers) != 0 {
		t.Fatalf("partial Inspect results must not become a complete snapshot: %+v", incomplete)
	}
}

func TestSnapshotFailureLeavesLastKnownInventoryUntouched(t *testing.T) {
	engine := &fakeEngine{ids: []string{"one"}, containers: map[string]Container{"one": baseContainer("one", "nd-one", "running")}, inspectErr: map[string]error{}}
	discoverer, _ := NewDiscoverer(engine, nil, Options{})
	initial := discoverer.scan(context.Background())
	discoverer.cache.applySnapshot(initial.startedSequence, initial.containers)

	engine.listErr = errors.New("permission denied")
	failed := discoverer.scan(context.Background())
	if failed.complete {
		t.Fatal("failed list was accepted as complete")
	}
	changes := discoverer.cache.markInventoryStale(safeError(failed.err), time.Now())
	if len(changes) != 1 || changes[0].Action != ChangeStale {
		t.Fatalf("last-known inventory was not marked stale after scan failure: %+v", changes)
	}
	discoverer.cache.setSnapshotFailure(failed.err)
	containers, health := discoverer.Current()
	if len(containers) != 1 || containers[0].ID != "one" {
		t.Fatalf("failed snapshot deleted last known asset: %+v", containers)
	}
	if health.SnapshotFresh || health.ErrorKind != "permission_denied" || !containers[0].Stale || containers[0].UnavailableReason == "" {
		t.Fatalf("snapshot error was not visible separately: %+v", health)
	}

	engine.listErr = nil
	recovered := discoverer.scan(context.Background())
	discoverer.cache.applySnapshot(recovered.startedSequence, recovered.containers)
	containers, health = discoverer.Current()
	if !recovered.complete || containers[0].Stale || containers[0].UnavailableReason != "" || !health.SnapshotFresh {
		t.Fatalf("successful full scan did not clear stale inventory state: items=%+v health=%+v", containers, health)
	}
}

func TestDeleteEventAndInFlightOlderSnapshotCannotResurrectContainer(t *testing.T) {
	cache := newStateCache(func() time.Time { return time.Now().UTC() })
	container := baseContainer("stable-id", "before-rename", "running")
	cache.applySnapshot(0, []Container{container})
	startedSequence := cache.currentSequence()

	revision := cache.observeEvent(Event{ContainerID: container.ID, Action: "destroy"})
	if _, changed := cache.applyEventDelete(container.ID, revision, time.Now()); !changed {
		t.Fatal("404 after destroy should remove the existing container")
	}
	cache.applySnapshot(startedSequence, []Container{container})
	current, _ := cache.view()
	if len(current) != 0 {
		t.Fatalf("old full snapshot resurrected a deleted ID: %+v", current)
	}
}

func TestCreateEventAfterSnapshotStartIsNotDeletedByOldList(t *testing.T) {
	cache := newStateCache(func() time.Time { return time.Now().UTC() })
	startedSequence := cache.currentSequence()
	created := baseContainer("created-late", "nd-late", "running")
	revision := cache.observeEvent(Event{ContainerID: created.ID, Action: "create"})
	cache.applySnapshot(startedSequence, nil)
	if _, changed := cache.applyEventUpsert(created.ID, revision, created); !changed {
		t.Fatal("create event failed to add a container absent from the old list")
	}
	current, _ := cache.view()
	if len(current) != 1 || current[0].ID != created.ID {
		t.Fatalf("new container was deleted by an older list: %+v", current)
	}
}

func TestRenameKeepsImmutableContainerID(t *testing.T) {
	cache := newStateCache(func() time.Time { return time.Now().UTC() })
	before := baseContainer("immutable-engine-id", "old-name", "running")
	cache.applySnapshot(0, []Container{before})
	revision := cache.observeEvent(Event{ContainerID: before.ID, Action: "rename"})
	after := baseContainer("immutable-engine-id", "new-name", "running")
	change, changed := cache.applyEventUpsert(before.ID, revision, after)
	if !changed || change.Container == nil || change.Container.ID != before.ID || change.Container.Name != "new-name" {
		t.Fatalf("rename changed asset identity or was missed: %+v", change)
	}
	current, _ := cache.view()
	if len(current) != 1 || current[0].ID != before.ID || current[0].Name != "new-name" {
		t.Fatalf("rename did not update the existing identity: %+v", current)
	}
}

func TestContainerReconciliationUsesOnlyExplicitNotFoundAsDelete(t *testing.T) {
	cache := newStateCache(func() time.Time { return time.Now().UTC() })
	container := baseContainer("one", "nd-one", "running")
	cache.applySnapshot(0, []Container{container})
	revision := cache.observeEvent(Event{ContainerID: "one", Action: "die"})
	if !isNotFound(errdefs.ErrNotFound) {
		t.Fatal("Moby SDK's explicit 404 classification was not recognized")
	}
	if isNotFound(errors.New("HTTP 404: permission denied by a proxy")) {
		t.Fatal("error text was misclassified as a confirmed container delete")
	}
	if _, changed := cache.applyEventUpsert("one", revision, baseContainer("one", "nd-one", "exited")); !changed {
		t.Fatal("non-404 Inspect failure path should leave the last known record intact")
	}
	current, _ := cache.view()
	if len(current) != 1 || current[0].State != "exited" {
		t.Fatalf("container inventory was unexpectedly removed: %+v", current)
	}
}

func TestEventInspectFailureMarksOnlyLastKnownContainerStale(t *testing.T) {
	cache := newStateCache(func() time.Time { return time.Now().UTC() })
	known := baseContainer("one", "nd-one", "running")
	cache.applySnapshot(0, []Container{known})
	when := time.Now().UTC()
	change, changed := cache.markContainerStale(known.ID, "permission denied", when)
	if !changed || change.Action != ChangeStale || change.ContainerID != known.ID || change.Reason != "permission denied" {
		t.Fatalf("Inspect failure did not produce explicit stale notification: %+v", change)
	}
	items, _ := cache.view()
	if len(items) != 1 || items[0].Name != known.Name || items[0].State != known.State || !items[0].Stale || items[0].UnavailableReason != "permission denied" {
		t.Fatalf("Inspect failure discarded last known resource state: %+v", items)
	}
	if _, changed := cache.markContainerStale("unknown-id", "permission denied", when); changed {
		t.Fatal("Inspect failure for an unknown identity created a blank asset")
	}
}

func TestOversizedUpdatePreservesLastKnownAndMarksStale(t *testing.T) {
	cache := newStateCache(func() time.Time { return time.Now().UTC() })
	known := baseContainer("one", "nd-one", "running")
	cache.applySnapshot(0, []Container{known})
	revision := cache.observeEvent(Event{ContainerID: known.ID, Action: "update"})
	large := baseContainer("one", "nd-one", "running")
	large.Compose = &ComposeIdentity{Project: strings.Repeat("p", MaxBatchBytes+1)}
	change, changed := cache.applyEventUpsert(known.ID, revision, large)
	if !changed || change.Action != ChangeStale || change.Reason == "" {
		t.Fatalf("oversized record did not become an explicit stale update: %+v", change)
	}
	current, _ := cache.view()
	if len(current) != 1 || current[0].Image != known.Image || current[0].Stale != true || current[0].UnavailableReason == "" {
		t.Fatalf("last known fields were replaced by a blank unknown: %+v", current)
	}

	newRevision := cache.observeEvent(Event{ContainerID: "new", Action: "create"})
	first := large
	first.ID = "new"
	first.Name = "nd-new"
	if change, changed = cache.applyEventUpsert("new", newRevision, first); !changed || change.Container == nil || change.Container.State != "unknown" {
		t.Fatalf("first oversized record must be retained as an unknown identity: %+v", change)
	}
}

type recordingObserver struct {
	mu      sync.Mutex
	batches []Batch
}

func (o *recordingObserver) ApplyDockerBatch(_ context.Context, batch Batch) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.batches = append(o.batches, cloneBatch(batch))
	return nil
}

func TestObserverSnapshotBatchesAreBoundedByActualEncodedBytes(t *testing.T) {
	observer := &recordingObserver{}
	containers := make(map[string]Container)
	for i := 0; i < MaxBatchChanges+7; i++ {
		id := fmt.Sprintf("container-%03d", i)
		item := baseContainer(id, "nd-"+id, "running")
		item.Compose = &ComposeIdentity{Project: "demo", Service: "web", WorkingDir: "/srv/demo", ConfigFiles: "compose.yaml"}
		containers[id] = item
	}
	cache := newStateCache(func() time.Time { return time.Now().UTC() })
	items := make([]Container, 0, len(containers))
	for _, item := range containers {
		items = append(items, item)
	}
	cache.applySnapshot(0, items)
	bus := newObserverBus(observer, 2, cache.currentBatch)
	if err := bus.sendSnapshot(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(observer.batches) < 2 {
		t.Fatalf("expected chunking beyond the item limit: %d batches", len(observer.batches))
	}
	for i, batch := range observer.batches {
		encoded, err := json.Marshal(batch)
		if err != nil {
			t.Fatal(err)
		}
		if len(encoded) > MaxBatchBytes || len(batch.Changes) > MaxBatchChanges {
			t.Fatalf("batch %d exceeded its actual byte/item cap: bytes=%d changes=%d", i, len(encoded), len(batch.Changes))
		}
		if batch.SnapshotIndex != i || batch.SnapshotFinal != (i == len(observer.batches)-1) {
			t.Fatalf("snapshot chunk order/commit marker invalid at %d: %+v", i, batch)
		}
	}
}

func TestObserverQueueOverflowRequestsSnapshotWithoutBlockingPublisher(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	observer := &blockingObserver{started: started, release: release}
	cache := newStateCache(func() time.Time { return time.Now().UTC() })
	bus := newObserverBus(observer, 1, cache.currentBatch)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go bus.run(ctx)
	bus.publishChange(Change{Sequence: 1, Action: ChangeDelete, ContainerID: "first"})
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("observer did not start")
	}
	begin := time.Now()
	for i := 0; i < 1000; i++ {
		bus.publishChange(Change{Sequence: uint64(i + 2), Action: ChangeDelete, ContainerID: fmt.Sprintf("id-%d", i)})
	}
	if elapsed := time.Since(begin); elapsed > 100*time.Millisecond {
		t.Fatalf("slow observer blocked event publication for %s", elapsed)
	}
	bus.mu.Lock()
	queued, resync := len(bus.queue), bus.resync
	bus.mu.Unlock()
	if queued > 1 || !resync {
		t.Fatalf("overflow was unbounded or did not request a resync: queued=%d resync=%v", queued, resync)
	}
	close(release)
	cancel()
}

func TestRunCancelsAndJoinsEventStreamBeforeClosingEngine(t *testing.T) {
	engine := &fakeEngine{
		ids:        []string{},
		containers: map[string]Container{},
		inspectErr: map[string]error{},
		opened:     make(chan struct{}, 1),
		streamDone: make(chan struct{}),
	}
	discoverer, err := NewDiscoverer(engine, nil, Options{HealthInterval: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- discoverer.Run(ctx) }()
	select {
	case <-engine.opened:
	case <-time.After(time.Second):
		cancel()
		t.Fatal("Run did not open its event stream")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not join its workers after cancellation")
	}
	select {
	case <-engine.streamDone:
	default:
		t.Fatal("Run returned before the event stream released its context")
	}
	engine.mu.Lock()
	closeCalls := engine.closeCalls
	engine.mu.Unlock()
	if closeCalls != 1 {
		t.Fatalf("Engine.Close calls = %d, want 1 after all workers exit", closeCalls)
	}
}

type blockingObserver struct {
	once    sync.Once
	started chan struct{}
	release chan struct{}
}

func (o *blockingObserver) ApplyDockerBatch(ctx context.Context, _ Batch) error {
	blocked := false
	o.once.Do(func() {
		blocked = true
		close(o.started)
	})
	if !blocked {
		return nil
	}
	select {
	case <-o.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
