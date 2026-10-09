package containerrebuild

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/CST-Cat/NodeDance/internal/agent/taskjournal"
	"github.com/CST-Cat/NodeDance/internal/protocol"
	"github.com/CST-Cat/NodeDance/internal/taskstate"
	"github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/api/types/network"
)

const (
	rebuildOriginalID = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	rebuildNewID      = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

type memoryEngine struct {
	mu             sync.Mutex
	containers     map[string]Container
	images         map[string]string
	imageLabels    map[string]map[string]string
	root           string
	failNewStart   bool
	removed        []string
	removedVolumes bool
}

func newMemoryEngine(t *testing.T, failNewStart bool) *memoryEngine {
	t.Helper()
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	port, err := network.ParsePort("80/tcp")
	if err != nil {
		t.Fatal(err)
	}
	base := Container{
		ID: rebuildOriginalID, Name: "/nd-web", ImageID: "sha256:base-image", State: "running", Running: true,
		WritableSize: 1024, Health: "healthy",
		Config: &container.Config{Image: "sha256:base-image", Env: []string{"APP_MODE=test"}, Cmd: []string{"serve"},
			Labels: map[string]string{"app": "demo"}, ExposedPorts: network.PortSet{port: struct{}{}}, Volumes: map[string]struct{}{"/data": {}}},
		HostConfig: &container.HostConfig{NetworkMode: container.NetworkMode("nd-net"),
			RestartPolicy: container.RestartPolicy{Name: "always"},
			PortBindings:  network.PortMap{port: {{HostIP: netip.MustParseAddr("127.0.0.1"), HostPort: "18080"}}},
			Mounts:        []mount.Mount{{Type: mount.TypeVolume, Source: "nd-volume-id", Target: "/data"}}},
		Networks:       map[string]*network.EndpointSettings{"nd-net": {Aliases: []string{"nd-web"}}},
		PublishedPorts: network.PortMap{port: {{HostIP: netip.MustParseAddr("127.0.0.1"), HostPort: "18080"}}},
		Mounts:         []container.MountPoint{{Type: mount.TypeVolume, Name: "nd-volume-id", Destination: "/data", RW: true}},
	}
	return &memoryEngine{containers: map[string]Container{base.ID: base}, images: make(map[string]string),
		imageLabels: make(map[string]map[string]string), root: root, failNewStart: failNewStart}
}

func (e *memoryEngine) Inspect(_ context.Context, id string) (Container, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if value, ok := e.containers[id]; ok {
		return cloneContainer(value)
	}
	for _, value := range e.containers {
		if strings.TrimPrefix(value.Name, "/") == strings.TrimPrefix(id, "/") {
			return cloneContainer(value)
		}
	}
	return Container{}, errdefs.ErrNotFound.WithMessage("container does not exist")
}

func (e *memoryEngine) Info(context.Context) (DockerInfo, error) {
	return DockerInfo{RootDirectory: e.root}, nil
}

func (e *memoryEngine) ImageInspect(_ context.Context, reference string) (string, map[string]string, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	id, ok := e.images[reference]
	if !ok {
		return "", nil, errdefs.ErrNotFound.WithMessage("image does not exist")
	}
	return id, cloneStrings(e.imageLabels[reference]), nil
}

func (e *memoryEngine) Commit(_ context.Context, id, reference string, labels map[string]string) (string, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if _, ok := e.containers[id]; !ok {
		return "", errdefs.ErrNotFound
	}
	imageID := "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	e.images[reference] = imageID
	e.imageLabels[reference] = cloneStrings(labels)
	return imageID, nil
}

func (e *memoryEngine) Stop(_ context.Context, id string, _ int) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	item, ok := e.containers[id]
	if !ok {
		return errdefs.ErrNotFound
	}
	item.Running, item.Paused, item.Restarting, item.State = false, false, false, "exited"
	e.containers[id] = item
	return nil
}

func (e *memoryEngine) UpdateRestartPolicy(_ context.Context, id string, policy container.RestartPolicy) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	item, ok := e.containers[id]
	if !ok {
		return errdefs.ErrNotFound
	}
	item.HostConfig.RestartPolicy = policy
	e.containers[id] = item
	return nil
}

func (e *memoryEngine) Rename(_ context.Context, id, name string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	item, ok := e.containers[id]
	if !ok {
		return errdefs.ErrNotFound
	}
	for otherID, other := range e.containers {
		if otherID != id && strings.TrimPrefix(other.Name, "/") == strings.TrimPrefix(name, "/") {
			return errors.New("name already in use")
		}
	}
	item.Name = "/" + strings.TrimPrefix(name, "/")
	e.containers[id] = item
	return nil
}

func (e *memoryEngine) DisconnectNetwork(_ context.Context, name, id string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	item, ok := e.containers[id]
	if !ok {
		return errdefs.ErrNotFound
	}
	for networkName, endpoint := range item.Networks {
		if networkName == name || endpoint != nil && endpoint.NetworkID == name {
			delete(item.Networks, networkName)
		}
	}
	e.containers[id] = item
	return nil
}

func (e *memoryEngine) ConnectNetwork(_ context.Context, name, id string, endpoint *network.EndpointSettings) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	item, ok := e.containers[id]
	if !ok || endpoint == nil {
		return errdefs.ErrNotFound
	}
	copyEndpoint, err := cloneJSON(*endpoint)
	if err != nil {
		return err
	}
	item.Networks[name] = &copyEndpoint
	e.containers[id] = item
	return nil
}

func (e *memoryEngine) Create(_ context.Context, name string, config *container.Config, host *container.HostConfig, endpoints map[string]*network.EndpointSettings) (string, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, current := range e.containers {
		if strings.TrimPrefix(current.Name, "/") == strings.TrimPrefix(name, "/") {
			return "", errors.New("name already in use")
		}
	}
	if config == nil || host == nil {
		return "", ErrUnsupportedConfiguration
	}
	configCopy, err := cloneJSON(*config)
	if err != nil {
		return "", err
	}
	hostCopy, err := cloneJSON(*host)
	if err != nil {
		return "", err
	}
	endpointCopy := make(map[string]*network.EndpointSettings, len(endpoints))
	for key, endpoint := range endpoints {
		copyEndpoint, err := cloneJSON(*endpoint)
		if err != nil {
			return "", err
		}
		endpointCopy[key] = &copyEndpoint
	}
	port, _ := network.ParsePort("80/tcp")
	e.containers[rebuildNewID] = Container{ID: rebuildNewID, Name: "/" + strings.TrimPrefix(name, "/"), ImageID: config.Image,
		State: "created", Health: "healthy", Config: &configCopy, HostConfig: &hostCopy, Networks: endpointCopy,
		Mounts:         []container.MountPoint{{Type: mount.TypeVolume, Name: "nd-volume-id", Destination: "/data", RW: true}},
		PublishedPorts: network.PortMap{port: append([]network.PortBinding(nil), host.PortBindings[port]...)}}
	return rebuildNewID, nil
}

func (e *memoryEngine) Start(_ context.Context, id string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	item, ok := e.containers[id]
	if !ok {
		return errdefs.ErrNotFound
	}
	if id == rebuildNewID && e.failNewStart {
		return errors.New("injected replacement start failure")
	}
	item.Running, item.Paused, item.Restarting, item.State = true, false, false, "running"
	e.containers[id] = item
	return nil
}

func (e *memoryEngine) Remove(_ context.Context, id string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if _, ok := e.containers[id]; !ok {
		return errdefs.ErrNotFound
	}
	e.removed = append(e.removed, id)
	delete(e.containers, id)
	return nil
}

func (e *memoryEngine) RemoveImage(_ context.Context, reference string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	id, ok := e.images[reference]
	if !ok {
		return errdefs.ErrNotFound
	}
	for _, current := range e.containers {
		if current.Config != nil && current.Config.Image == id {
			return errors.New("image is used by a container")
		}
	}
	delete(e.images, reference)
	delete(e.imageLabels, reference)
	return nil
}

func cloneContainer(value Container) (Container, error) {
	cloned, err := cloneJSON(value)
	if err != nil {
		return Container{}, err
	}
	return cloned, nil
}

func setupManager(t *testing.T, engine *memoryEngine) (*Manager, *Store, *taskjournal.Store) {
	t.Helper()
	base := t.TempDir()
	if err := os.Chmod(base, 0o700); err != nil {
		t.Fatal(err)
	}
	stateDir := filepath.Join(base, "state")
	if err := os.Mkdir(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	store, err := OpenStore(ctx, filepath.Join(stateDir, "rebuild.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	journal, err := taskjournal.Open(ctx, filepath.Join(stateDir, "tasks.sqlite"), "node-test")
	if err != nil {
		_ = store.Close()
		t.Fatal(err)
	}
	manager, err := NewManager(engine, journal, store, Options{OperationTimeout: time.Minute, VerifyTimeout: time.Second, HealthPoll: 100 * time.Millisecond})
	if err != nil {
		_ = journal.Close()
		_ = store.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = journal.Close(); _ = store.Close() })
	return manager, store, journal
}

func rebuildRequest(taskID, key string, action protocol.TaskAction, containerID string, spec protocol.RebuildSpec) Request {
	return Request{TaskID: taskID, NodeID: "node-test", IdempotencyKey: key, Action: action, ContainerID: containerID, Spec: &spec}
}

func TestBuildPlanRejectsUnsafeAndRedactsBindSource(t *testing.T) {
	engine := newMemoryEngine(t, false)
	current, err := engine.Inspect(context.Background(), rebuildOriginalID)
	if err != nil {
		t.Fatal(err)
	}
	current.HostConfig.Mounts[0] = mount.Mount{Type: mount.TypeBind, Source: "/srv/private/customer-data", Target: "/data"}
	current.Mounts[0] = container.MountPoint{Type: mount.TypeBind, Source: "/srv/private/customer-data", Destination: "/data", RW: true}
	plan, err := BuildPlan(current, protocol.RebuildSpec{})
	if err != nil {
		t.Fatalf("BuildPlan valid isolated container: %v", err)
	}
	if strings.Contains(strings.Join(plan.Risks, " ")+strings.Join(plan.Preserved, " "), "/srv/private/customer-data") {
		t.Fatal("redacted plan leaked a host bind source path")
	}
	encoded, err := jsonMarshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "/srv/private/customer-data") {
		t.Fatal("plan JSON leaked a host bind source path")
	}
	for name, mutate := range map[string]func(*Container){
		"host-network": func(item *Container) {
			item.HostNetwork = true
			item.HostConfig.NetworkMode = container.NetworkMode("host")
		},
		"auto-remove": func(item *Container) { item.AutoRemove = true },
		"compose":     func(item *Container) { item.Compose = true },
	} {
		t.Run(name, func(t *testing.T) {
			unsafe, cloneErr := cloneContainer(current)
			if cloneErr != nil {
				t.Fatal(cloneErr)
			}
			mutate(&unsafe)
			if _, err := BuildPlan(unsafe, protocol.RebuildSpec{}); !errors.Is(err, ErrUnsupportedConfiguration) {
				t.Fatalf("unsafe plan error = %v", err)
			}
		})
	}
}

func TestExecuteRebuildPersistsPlanPreservesDataAndCleanupRetainsActiveSnapshot(t *testing.T) {
	engine := newMemoryEngine(t, false)
	manager, store, journal := setupManager(t, engine)
	spec := protocol.RebuildSpec{PortBindings: []protocol.RebuildPortBinding{{ContainerPort: "80/tcp", HostIP: "127.0.0.1", HostPort: "18081"}}}
	plan, err := manager.Plan(context.Background(), rebuildOriginalID, spec)
	if err != nil || !plan.SnapshotRequired || len(plan.Mounts) != 1 {
		t.Fatalf("plan = %+v, err=%v", plan, err)
	}
	request := rebuildRequest("task-rebuild-success", "key-rebuild-success", protocol.TaskRebuild, rebuildOriginalID, spec)
	got, err := manager.ExecuteObserved(context.Background(), request, nil)
	if err != nil || got.Status != taskstate.Succeeded || got.Result.ResourceRevision != rebuildNewID {
		t.Fatalf("rebuild result = %+v, err=%v", got, err)
	}
	replacement, err := engine.Inspect(context.Background(), rebuildNewID)
	if err != nil {
		t.Fatal(err)
	}
	if replacement.Name != "/nd-web" || !replacement.Running || replacement.HostConfig.RestartPolicy.Name != "always" {
		t.Fatalf("replacement lost name, running state, or restart policy: %+v", replacement)
	}
	port, _ := network.ParsePort("80/tcp")
	if replacement.HostConfig.PortBindings[port][0].HostPort != "18081" {
		t.Fatalf("replacement port mapping = %+v", replacement.HostConfig.PortBindings)
	}
	if replacement.HostConfig.Mounts[0].Source != "nd-volume-id" || len(replacement.Networks["nd-net"].Aliases) != 1 {
		t.Fatalf("replacement lost volume/network references: mounts=%+v networks=%+v", replacement.HostConfig.Mounts, replacement.Networks)
	}
	record, err := store.Get(context.Background(), request.TaskID)
	if err != nil || record.Outcome != "succeeded" || record.SnapshotID == "" {
		t.Fatalf("durable rebuild record = %+v, err=%v", record, err)
	}
	cleanup := rebuildRequest("task-rebuild-cleanup", "key-rebuild-cleanup", protocol.TaskRebuildCleanup, rebuildNewID,
		protocol.RebuildSpec{CleanupTaskID: request.TaskID})
	cleanup.ConfirmationID = rebuildNewID
	cleaned, err := manager.CleanupObserved(context.Background(), cleanup, nil)
	if err != nil || cleaned.Status != taskstate.Succeeded || cleaned.Result.ObservedState != "rollback_container_removed_snapshot_retained" {
		t.Fatalf("rollback cleanup result = %+v, err=%v", cleaned, err)
	}
	if _, err := engine.Inspect(context.Background(), rebuildOriginalID); !errors.Is(err, errdefs.ErrNotFound) {
		t.Fatalf("old rollback container remains after explicit cleanup: %v", err)
	}
	if _, _, err := engine.ImageInspect(context.Background(), record.SnapshotRef); err != nil {
		t.Fatalf("snapshot backing the replacement was removed: %v", err)
	}
	if engine.removedVolumes {
		t.Fatal("rebuild cleanup removed mounted volume data")
	}
	if _, err := journal.Get(context.Background(), cleanup.TaskID); err != nil {
		t.Fatalf("cleanup task was not durably journaled: %v", err)
	}
}

func TestExecuteRebuildFailureRestoresOriginalBeforeReportingFailure(t *testing.T) {
	engine := newMemoryEngine(t, true)
	manager, store, _ := setupManager(t, engine)
	request := rebuildRequest("task-rebuild-rollback", "key-rebuild-rollback", protocol.TaskRebuild, rebuildOriginalID, protocol.RebuildSpec{})
	got, err := manager.ExecuteObserved(context.Background(), request, nil)
	if got.Status != taskstate.Failed || got.Result.ObservedState != "restored:rebuild_operation_failed" || got.Result.ResourceRevision != rebuildOriginalID {
		t.Fatalf("failed rebuild result = %+v, err=%v", got, err)
	}
	old, inspectErr := engine.Inspect(context.Background(), rebuildOriginalID)
	if inspectErr != nil || old.Name != "/nd-web" || !old.Running || old.HostConfig.RestartPolicy.Name != "always" || len(old.Networks) != 1 {
		t.Fatalf("original was not restored before failure was reported: %+v, err=%v", old, inspectErr)
	}
	if _, inspectErr := engine.Inspect(context.Background(), rebuildNewID); !errors.Is(inspectErr, errdefs.ErrNotFound) {
		t.Fatalf("failed replacement remains after rollback: %v", inspectErr)
	}
	record, err := store.Get(context.Background(), request.TaskID)
	if err != nil || record.SnapshotID == "" {
		t.Fatalf("failed rebuild did not retain a cleanupable writable-layer snapshot: %+v err=%v", record, err)
	}
	cleanup := rebuildRequest("task-rebuild-failed-cleanup", "key-rebuild-failed-cleanup", protocol.TaskRebuildCleanup,
		rebuildOriginalID, protocol.RebuildSpec{CleanupTaskID: request.TaskID})
	cleanup.ConfirmationID = rebuildOriginalID
	cleaned, err := manager.CleanupObserved(context.Background(), cleanup, nil)
	if err != nil || cleaned.Status != taskstate.Succeeded {
		t.Fatalf("explicit cleanup after restored failure = %+v, err=%v", cleaned, err)
	}
	old, inspectErr = engine.Inspect(context.Background(), rebuildOriginalID)
	if inspectErr != nil || old.Name != "/nd-web" || !old.Running {
		t.Fatalf("failed-rebuild cleanup changed the restored original container: %+v err=%v", old, inspectErr)
	}
	if _, _, inspectErr := engine.ImageInspect(context.Background(), record.SnapshotRef); !errors.Is(inspectErr, errdefs.ErrNotFound) {
		t.Fatalf("failed-rebuild cleanup left the unused snapshot: %v", inspectErr)
	}
}

func TestExecuteRebuildPreservesMultipleNetworksAliasesAndStaticAddresses(t *testing.T) {
	engine := newMemoryEngine(t, false)
	engine.mu.Lock()
	original := engine.containers[rebuildOriginalID]
	original.HostConfig.NetworkMode = container.NetworkMode("nd-blue")
	original.Networks = map[string]*network.EndpointSettings{
		"nd-blue": {
			NetworkID: "network-blue-id", Aliases: []string{"nd-web", "blue-web"},
			IPAddress:  netip.MustParseAddr("172.28.1.10"),
			IPAMConfig: &network.EndpointIPAMConfig{IPv4Address: netip.MustParseAddr("172.28.1.10")},
		},
		"nd-green": {
			NetworkID: "network-green-id", Aliases: []string{"nd-web", "green-web"},
			IPAddress:  netip.MustParseAddr("172.29.1.10"),
			IPAMConfig: &network.EndpointIPAMConfig{IPv4Address: netip.MustParseAddr("172.29.1.10")},
		},
	}
	engine.containers[rebuildOriginalID] = original
	engine.mu.Unlock()

	manager, _, _ := setupManager(t, engine)
	request := rebuildRequest("task-rebuild-networks", "key-rebuild-networks", protocol.TaskRebuild, rebuildOriginalID, protocol.RebuildSpec{})
	got, err := manager.ExecuteObserved(context.Background(), request, nil)
	if err != nil || got.Status != taskstate.Succeeded {
		t.Fatalf("multiple-network rebuild = %+v, err=%v", got, err)
	}
	replacement, err := engine.Inspect(context.Background(), got.Result.ResourceRevision)
	if err != nil {
		t.Fatal(err)
	}
	if len(replacement.Networks) != 2 {
		t.Fatalf("replacement has %d networks, want 2: %+v", len(replacement.Networks), replacement.Networks)
	}
	for name, want := range map[string]string{"nd-blue": "172.28.1.10", "nd-green": "172.29.1.10"} {
		endpoint := replacement.Networks[name]
		if endpoint == nil || endpoint.IPAMConfig == nil || endpoint.IPAMConfig.IPv4Address.String() != want {
			t.Fatalf("static IP for %s was not retained: %+v", name, endpoint)
		}
		wantAlias := strings.TrimPrefix(name, "nd-") + "-web"
		if !contains(endpoint.Aliases, wantAlias) || !contains(endpoint.Aliases, "nd-web") {
			t.Fatalf("aliases for %s were not retained: %+v", name, endpoint.Aliases)
		}
		if endpoint.IPAddress.IsValid() {
			t.Fatalf("runtime address was copied as endpoint configuration for %s: %s", name, endpoint.IPAddress)
		}
	}
}

func TestReconcileAfterRestartRestoresOriginalFromDurablePhase(t *testing.T) {
	engine := newMemoryEngine(t, false)
	manager, store, journal := setupManager(t, engine)
	engine.mu.Lock()
	original := engine.containers[rebuildOriginalID]
	original.HostConfig.NetworkMode = container.NetworkMode("nd-blue")
	original.Networks = map[string]*network.EndpointSettings{
		"nd-blue": {
			Aliases: []string{"nd-web", "blue-web"}, IPAddress: netip.MustParseAddr("172.28.1.10"),
			IPAMConfig: &network.EndpointIPAMConfig{IPv4Address: netip.MustParseAddr("172.28.1.10")},
		},
		"nd-green": {
			Aliases: []string{"nd-web", "green-web"}, IPAddress: netip.MustParseAddr("172.29.1.10"),
			IPAMConfig: &network.EndpointIPAMConfig{IPv4Address: netip.MustParseAddr("172.29.1.10")},
		},
	}
	engine.containers[rebuildOriginalID] = original
	engine.mu.Unlock()
	request := rebuildRequest("task-rebuild-restart-reconcile", "key-rebuild-restart-reconcile", protocol.TaskRebuild,
		rebuildOriginalID, protocol.RebuildSpec{})
	intent := protocol.TaskIntent{Action: protocol.TaskRebuild, ContainerID: request.ContainerID, Rebuild: request.Spec}
	identity, err := protocol.TaskIdentity(request.TaskID, request.NodeID, request.IdempotencyKey, intent)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := journal.Enqueue(context.Background(), identity); err != nil {
		t.Fatal(err)
	}
	if err := journal.BeginExecution(context.Background(), request.TaskID); err != nil {
		t.Fatal(err)
	}
	original, err = engine.Inspect(context.Background(), rebuildOriginalID)
	if err != nil {
		t.Fatal(err)
	}
	policy, err := json.Marshal(original.HostConfig.RestartPolicy)
	if err != nil {
		t.Fatal(err)
	}
	backupName, snapshotRef := generatedNames(request.TaskID)
	if _, created, err := store.PutNew(context.Background(), Record{
		TaskID: request.TaskID, NodeID: request.NodeID, OriginalID: original.ID, OriginalName: "nd-web",
		BackupName: backupName, SnapshotRef: snapshotRef, PreviousRunning: true, RestartPolicy: string(policy),
		Phase: PhaseRenamed, Spec: *request.Spec, Networks: original.Networks,
	}); err != nil || !created {
		t.Fatalf("persist pre-restart rename phase: created=%t err=%v", created, err)
	}
	if err := engine.Stop(context.Background(), original.ID, maxStopTimeout); err != nil {
		t.Fatal(err)
	}
	if err := engine.UpdateRestartPolicy(context.Background(), original.ID, container.RestartPolicy{Name: "no"}); err != nil {
		t.Fatal(err)
	}
	if err := engine.Rename(context.Background(), original.ID, backupName); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"nd-blue", "nd-green"} {
		if err := engine.DisconnectNetwork(context.Background(), name, original.ID); err != nil {
			t.Fatal(err)
		}
	}

	recovered, err := manager.Reconcile(context.Background(), request.TaskID)
	if err != nil || recovered.Status != taskstate.Failed || recovered.Result.ObservedState != "restored:agent_restart_recovery" || recovered.Result.ResourceRevision != rebuildOriginalID {
		t.Fatalf("restart reconciliation result = %+v err=%v", recovered, err)
	}
	restored, err := engine.Inspect(context.Background(), rebuildOriginalID)
	if err != nil || restored.Name != "/nd-web" || !restored.Running || restored.HostConfig.RestartPolicy.Name != "always" || len(restored.Networks) != 2 {
		t.Fatalf("original container was not restored after Agent restart: %+v err=%v", restored, err)
	}
	for name, want := range map[string]string{"nd-blue": "172.28.1.10", "nd-green": "172.29.1.10"} {
		endpoint := restored.Networks[name]
		if endpoint == nil || endpoint.IPAMConfig == nil || endpoint.IPAMConfig.IPv4Address.String() != want {
			t.Fatalf("restored static IP config for %s = %+v want=%s", name, endpoint, want)
		}
		if !contains(endpoint.Aliases, "nd-web") {
			t.Fatalf("restored alias missing on %s: %+v", name, endpoint.Aliases)
		}
	}
}

func TestCleanupSnapshotOwnerMismatchLeavesRollbackContainerIntact(t *testing.T) {
	engine := newMemoryEngine(t, false)
	manager, store, _ := setupManager(t, engine)
	rebuild := rebuildRequest("task-rebuild-cleanup-mismatch", "key-rebuild-cleanup-mismatch", protocol.TaskRebuild,
		rebuildOriginalID, protocol.RebuildSpec{})
	if got, err := manager.ExecuteObserved(context.Background(), rebuild, nil); err != nil || got.Status != taskstate.Succeeded {
		t.Fatalf("prepare successful rebuild = %+v, err=%v", got, err)
	}
	record, err := store.Get(context.Background(), rebuild.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	engine.mu.Lock()
	engine.imageLabels[record.SnapshotRef][markerTaskLabel] = "another-task"
	engine.mu.Unlock()
	cleanup := rebuildRequest("task-cleanup-mismatch", "key-cleanup-mismatch", protocol.TaskRebuildCleanup,
		rebuildNewID, protocol.RebuildSpec{CleanupTaskID: rebuild.TaskID})
	cleanup.ConfirmationID = rebuildNewID
	got, err := manager.CleanupObserved(context.Background(), cleanup, nil)
	if err == nil || got.Status != taskstate.Unknown {
		t.Fatalf("mismatched snapshot cleanup result = %+v, err=%v; want unknown", got, err)
	}
	old, inspectErr := engine.Inspect(context.Background(), rebuildOriginalID)
	if inspectErr != nil || strings.TrimPrefix(old.Name, "/") != record.BackupName {
		t.Fatalf("rollback container was removed before snapshot identity validation: old=%+v err=%v", old, inspectErr)
	}
	if len(engine.removed) != 0 {
		t.Fatalf("cleanup performed destructive removals before validation: %v", engine.removed)
	}
}

func TestSnapshotSpacePreflightFailsBeforeStoppingOriginal(t *testing.T) {
	engine := newMemoryEngine(t, false)
	engine.mu.Lock()
	original := engine.containers[rebuildOriginalID]
	original.WritableSize = 1 << 62
	engine.containers[rebuildOriginalID] = original
	engine.mu.Unlock()
	manager, store, _ := setupManager(t, engine)
	request := rebuildRequest("task-rebuild-space-low", "key-rebuild-space-low", protocol.TaskRebuild, rebuildOriginalID, protocol.RebuildSpec{})
	got, err := manager.ExecuteObserved(context.Background(), request, nil)
	if err != nil || got.Status != taskstate.Failed || got.Result.ResourceRevision != "snapshot_space_low" || got.Result.ObservedState != "unchanged" {
		t.Fatalf("space preflight result = %+v, err=%v", got, err)
	}
	original, err = engine.Inspect(context.Background(), rebuildOriginalID)
	if err != nil || !original.Running || original.Name != "/nd-web" {
		t.Fatalf("space refusal changed the original container: %+v, err=%v", original, err)
	}
	if _, err := store.Get(context.Background(), request.TaskID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("space refusal persisted a mutation phase: %v", err)
	}
}

// jsonMarshal is kept local to this focused package test so its evidence
// check exercises the exact redacted wire DTO rather than an intermediate.
func jsonMarshal(value any) ([]byte, error) {
	return json.Marshal(value)
}
