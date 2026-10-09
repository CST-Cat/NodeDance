package containerrebuild

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/CST-Cat/NodeDance/internal/agent/taskjournal"
	"github.com/CST-Cat/NodeDance/internal/protocol"
	"github.com/CST-Cat/NodeDance/internal/taskstate"
	"github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"
)

type s12DindOwner struct {
	Suite         string `json:"suite"`
	ServerVersion string `json:"server_version"`
	Socket        string `json:"socket"`
	HostDaemon    string `json:"host_daemon"`
	ContainerName string `json:"container_name"`
	NetworkName   string `json:"network_name"`
}

type s12ImageLock struct {
	Images map[string]string `json:"images"`
}

type dindStorageEngine struct {
	*DockerEngine
	storageRoot string
}

func (e dindStorageEngine) Info(context.Context) (DockerInfo, error) {
	return DockerInfo{RootDirectory: e.storageRoot}, nil
}

// TestDINDRebuildLifecycle runs a real image snapshot, named/anonymous volume,
// bind mount, network, restart-policy, port-change, and explicit rollback
// cleanup against the repository-owned, marker-verified inner Docker Engine.
// It never sends a mutation to the outer/shared Docker daemon.
func TestDINDRebuildLifecycle(t *testing.T) {
	root := os.Getenv("NODEDANCE_S12_DIND_ROOT")
	if root == "" {
		t.Skip("set NODEDANCE_S12_DIND_ROOT to this checkout's marker-verified DIND fixture")
	}
	root, socket, engineVersion := requireS12DIND(t, root)
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
	defer cancel()

	lockBytes, err := os.ReadFile(filepath.Join(findS12RepoRoot(t), "test-images.lock.json"))
	if err != nil {
		t.Fatal(err)
	}
	var lock s12ImageLock
	if err := json.Unmarshal(lockBytes, &lock); err != nil {
		t.Fatal(err)
	}
	image := lock.Images["busybox"]
	if !strings.Contains(image, "@sha256:") {
		t.Fatalf("BusyBox fixture is not digest locked: %q", image)
	}
	if output, err := runS12Docker(ctx, socket, "version", "--format", "{{.Server.Version}}"); err != nil || strings.TrimSpace(string(output)) != engineVersion {
		t.Fatalf("wrong inner Engine: version=%q err=%v", output, err)
	}
	if _, err := runS12Docker(ctx, socket, "pull", image); err != nil {
		t.Fatalf("pull digest-locked BusyBox fixture: %v", err)
	}

	var tokenBytes [8]byte
	if _, err := rand.Read(tokenBytes[:]); err != nil {
		t.Fatal(err)
	}
	runID := hex.EncodeToString(tokenBytes[:])
	suite := "nodedance-s12-" + runID
	containerName := "nd-s12-" + runID
	networkName := containerName + "-net"
	networkName2 := containerName + "-net2"
	subnetA := fmt.Sprintf("10.230.%d.0/24", tokenBytes[2])
	subnetB := fmt.Sprintf("10.231.%d.0/24", tokenBytes[2])
	staticIPA := netip.MustParseAddr(fmt.Sprintf("10.230.%d.10", tokenBytes[2]))
	staticIPB := netip.MustParseAddr(fmt.Sprintf("10.231.%d.10", tokenBytes[2]))
	volumeName := containerName + "-volume"
	var taskID, snapshotRef string
	var anonymousVolume string
	bindRoot := filepath.Join(findS12RepoRoot(t), ".artifacts", "fixtures", suite)
	var engineClient *client.Client
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cleanupCancel()
		if output, err := runS12Docker(cleanupCtx, socket, "ps", "-aq", "--filter", "label=io.nodedance.suite="+suite, "--no-trunc"); err == nil {
			for _, id := range strings.Fields(string(output)) {
				labels, inspectErr := runS12Docker(cleanupCtx, socket, "inspect", "--format", "{{ index .Config.Labels \"io.nodedance.suite\" }}", id)
				if inspectErr == nil && strings.TrimSpace(string(labels)) == suite {
					_, _ = runS12Docker(cleanupCtx, socket, "rm", "-f", id)
				}
			}
		}
		if taskID != "" && snapshotRef != "" {
			imageID, imageErr := runS12Docker(cleanupCtx, socket, "image", "inspect", "--format", "{{.Id}}", snapshotRef)
			taskLabel, taskErr := runS12Docker(cleanupCtx, socket, "image", "inspect", "--format", "{{ index .Config.Labels \""+markerTaskLabel+"\" }}", snapshotRef)
			ownerLabel, ownerErr := runS12Docker(cleanupCtx, socket, "image", "inspect", "--format", "{{ index .Config.Labels \""+markerOwnerLabel+"\" }}", snapshotRef)
			if imageErr == nil && strings.TrimSpace(string(imageID)) != "" && taskErr == nil && strings.TrimSpace(string(taskLabel)) == taskID && ownerErr == nil && strings.TrimSpace(string(ownerLabel)) == "true" {
				_, _ = runS12Docker(cleanupCtx, socket, "image", "rm", snapshotRef)
			}
		}
		if labels, inspectErr := runS12Docker(cleanupCtx, socket, "volume", "inspect", "--format", "{{ index .Labels \"io.nodedance.suite\" }}", volumeName); inspectErr == nil && strings.TrimSpace(string(labels)) == suite {
			_, _ = runS12Docker(cleanupCtx, socket, "volume", "rm", volumeName)
		}
		if anonymousVolume != "" {
			// The anonymous mount has no Docker ownership label. Its exact name
			// was captured from this suite-labeled fixture container before any
			// rebuild mutation, so remove only that unique volume after containers.
			if _, inspectErr := runS12Docker(cleanupCtx, socket, "volume", "inspect", anonymousVolume); inspectErr == nil {
				_, _ = runS12Docker(cleanupCtx, socket, "volume", "rm", anonymousVolume)
			}
		}
		if volumes, listErr := runS12Docker(cleanupCtx, socket, "volume", "ls", "-q", "--filter", "label=io.nodedance.suite="+suite); listErr == nil {
			for _, volume := range strings.Fields(string(volumes)) {
				labels, inspectErr := runS12Docker(cleanupCtx, socket, "volume", "inspect", "--format", "{{ index .Labels \"io.nodedance.suite\" }}", volume)
				if inspectErr == nil && strings.TrimSpace(string(labels)) == suite {
					_, _ = runS12Docker(cleanupCtx, socket, "volume", "rm", volume)
				}
			}
		}
		for _, fixtureNetwork := range []string{networkName, networkName2} {
			if labels, inspectErr := runS12Docker(cleanupCtx, socket, "network", "inspect", "--format", "{{ index .Labels \"io.nodedance.suite\" }}", fixtureNetwork); inspectErr == nil && strings.TrimSpace(string(labels)) == suite {
				_, _ = runS12Docker(cleanupCtx, socket, "network", "rm", fixtureNetwork)
			}
		}
		if owner, readErr := os.ReadFile(filepath.Join(bindRoot, "owner.json")); readErr == nil && strings.TrimSpace(string(owner)) == suite {
			_ = os.RemoveAll(bindRoot)
		}
	})
	for _, fixtureNetwork := range []struct{ name, subnet string }{{networkName, subnetA}, {networkName2, subnetB}} {
		if _, err := runS12Docker(ctx, socket, "network", "create", "--subnet", fixtureNetwork.subnet, "--label", "io.nodedance.test=true", "--label", "io.nodedance.suite="+suite, fixtureNetwork.name); err != nil {
			t.Fatalf("create test-owned inner network %s: %v", fixtureNetwork.name, err)
		}
	}
	if _, err := runS12Docker(ctx, socket, "volume", "create", "--label", "io.nodedance.test=true", "--label", "io.nodedance.suite="+suite, volumeName); err != nil {
		t.Fatalf("create test-owned inner volume: %v", err)
	}
	if err := os.Mkdir(bindRoot, 0o700); err != nil {
		t.Fatalf("create unique repository-owned bind fixture: %v", err)
	}
	if err := os.Chmod(bindRoot, 0o777); err != nil {
		t.Fatalf("make unique bind fixture traversable by its configured container user: %v", err)
	}
	if err := os.WriteFile(filepath.Join(bindRoot, "owner.json"), []byte(suite+"\n"), 0o600); err != nil {
		t.Fatalf("write bind fixture owner marker: %v", err)
	}
	if err := os.WriteFile(filepath.Join(bindRoot, "bind-marker"), []byte("bind-"+runID), 0o600); err != nil {
		t.Fatalf("write unique bind mount marker: %v", err)
	}
	engineClient, err = client.NewClientWithOpts(client.WithHost(socket), client.WithAPIVersionNegotiation())
	if err != nil {
		t.Fatal(err)
	}
	defer engineClient.Close()
	dockerEngine, err := NewDockerEngine(engineClient)
	if err != nil {
		t.Fatal(err)
	}
	engine := dindStorageEngine{DockerEngine: dockerEngine, storageRoot: filepath.Join(root, "data")}
	anonymousVolume, err = createDindFixture(ctx, engineClient, socket, containerName, image, suite, networkName, networkName2, staticIPA, staticIPB, volumeName, bindRoot)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runS12Docker(ctx, socket, "exec", containerName, "sh", "-c", "printf writable-"+runID+" > /tmp/writable-marker"); err != nil {
		t.Fatalf("write a fixture-only writable-layer marker after startup: %v", err)
	}
	markerPaths := []string{"/data/named-marker", "/anonymous/anonymous-marker", "/bind/bind-marker", "/tmp/writable-marker"}
	markerHashes := make(map[string][32]byte, len(markerPaths))
	for _, path := range markerPaths {
		contents, err := runS12Docker(ctx, socket, "exec", containerName, "cat", path)
		if err != nil {
			t.Fatalf("read pre-rebuild marker %s: %v", path, err)
		}
		markerHashes[path] = sha256.Sum256(contents)
	}

	base := t.TempDir()
	if err := os.Chmod(base, 0o700); err != nil {
		t.Fatal(err)
	}
	stateDir := filepath.Join(base, "state")
	if err := os.Mkdir(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	store, err := OpenStore(ctx, filepath.Join(stateDir, "rebuild.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	journal, err := taskjournal.Open(ctx, filepath.Join(stateDir, "tasks.sqlite"), "node-test")
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
	manager, err := NewManager(engine, journal, store, Options{OperationTimeout: 10 * time.Minute, VerifyTimeout: 2 * time.Minute, HealthPoll: 250 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	initial, err := engine.Inspect(ctx, containerName)
	if err != nil {
		t.Fatal(err)
	}
	if initial.WritableSize < 1 || len(initial.Mounts) != 3 || len(initial.Networks) != 2 {
		t.Fatalf("fixture lacks writable layer/mount/network coverage: size=%d mounts=%+v networks=%+v", initial.WritableSize, initial.Mounts, initial.Networks)
	}
	assertRichDindFixture(t, initial)
	newPort := protocol.RebuildSpec{PortBindings: []protocol.RebuildPortBinding{{ContainerPort: "80/tcp", HostIP: "127.0.0.1", HostPort: "38081"}}}
	plan, err := manager.Plan(ctx, initial.ID, newPort)
	if err != nil || !plan.SnapshotRequired || len(plan.Mounts) != 3 {
		t.Fatalf("fresh rebuild plan = %+v, err=%v", plan, err)
	}
	taskID = "dind-rebuild-" + runID
	request := Request{TaskID: taskID, NodeID: "node-test", IdempotencyKey: "dind-rebuild-key-" + runID,
		Action: protocol.TaskRebuild, ContainerID: initial.ID, Spec: &newPort}
	result, err := manager.ExecuteObserved(ctx, request, nil)
	if err != nil || result.Status != taskstate.Succeeded {
		t.Fatalf("real Engine rebuild = %+v, err=%v", result, err)
	}
	newID := result.Result.ResourceRevision
	if !protocol.IsFullContainerID(newID) {
		t.Fatalf("verified result does not expose the replacement full ID: %q", newID)
	}
	replacement, err := engine.Inspect(ctx, newID)
	if err != nil || !replacement.Running || replacement.HostConfig.RestartPolicy.Name != "always" {
		t.Fatalf("replacement state/restart policy = %+v, err=%v", replacement, err)
	}
	assertRebuildPreservesRichInspectConfig(t, initial, replacement)
	port, _ := network.ParsePort("80/tcp")
	if replacement.PublishedPorts[port] == nil || replacement.PublishedPorts[port][0].HostPort != "38081" {
		t.Fatalf("new published port was not applied: %+v", replacement.PublishedPorts)
	}
	for _, mountPoint := range replacement.Mounts {
		if mountPoint.Destination == "/data" && mountPoint.Name != volumeName {
			t.Fatalf("named volume identity changed: %+v", mountPoint)
		}
		if mountPoint.Destination == "/anonymous" {
			if mountPoint.Name == "" || mountPoint.Name != mountNameAt(initial.Mounts, "/anonymous") {
				t.Fatalf("anonymous volume identity changed: %+v old=%q", mountPoint, mountNameAt(initial.Mounts, "/anonymous"))
			}
		}
		if mountPoint.Destination == "/bind" && filepath.Clean(mountPoint.Source) != filepath.Clean(bindRoot) {
			t.Fatalf("bind source changed: got %q want %q", mountPoint.Source, bindRoot)
		}
	}
	if len(replacement.Networks) != 2 || !hasNetwork(replacement.Networks, networkName) || !hasNetwork(replacement.Networks, networkName2) ||
		!contains(replacement.Networks[networkName].Aliases, "nd-s12-primary") || !contains(replacement.Networks[networkName2].Aliases, "nd-s12-secondary") {
		t.Fatalf("replacement lost its network identity: %+v", replacement.Networks)
	}
	for name, wantIP := range map[string]netip.Addr{networkName: staticIPA, networkName2: staticIPB} {
		endpoint := replacement.Networks[name]
		if endpoint == nil || endpoint.NetworkID == "" || endpoint.EndpointID == "" || endpoint.IPAMConfig == nil ||
			endpoint.IPAMConfig.IPv4Address != wantIP || endpoint.IPAddress != wantIP {
			t.Fatalf("replacement lost its live network endpoint or requested static address for %s: %+v want=%s", name, endpoint, wantIP)
		}
	}
	for _, path := range markerPaths {
		contents, err := runS12Docker(ctx, socket, "exec", newID, "cat", path)
		if err != nil {
			t.Fatalf("read post-rebuild marker %s: %v", path, err)
		}
		gotHash := sha256.Sum256(contents)
		beforeHash := markerHashes[path]
		if gotHash != beforeHash {
			t.Fatalf("marker %s changed across rebuild: before=%s after=%s", path, hex.EncodeToString(beforeHash[:]), hex.EncodeToString(gotHash[:]))
		}
		t.Logf("marker %s sha256=%s", path, hex.EncodeToString(gotHash[:]))
	}
	old, err := engine.Inspect(ctx, initial.ID)
	if err != nil || strings.TrimPrefix(old.Name, "/") != recordBackupName(t, store, taskID) || old.Running {
		t.Fatalf("rollback container was not retained after success: %+v, err=%v", old, err)
	}
	record, err := store.Get(ctx, taskID)
	if err != nil {
		t.Fatal(err)
	}
	snapshotRef = record.SnapshotRef
	cleanup := Request{TaskID: "dind-cleanup-" + runID, NodeID: "node-test", IdempotencyKey: "dind-cleanup-key-" + runID,
		Action: protocol.TaskRebuildCleanup, ContainerID: newID, Spec: &protocol.RebuildSpec{CleanupTaskID: taskID}, ConfirmationID: newID}
	cleaned, err := manager.CleanupObserved(ctx, cleanup, nil)
	if err != nil || cleaned.Status != taskstate.Succeeded || cleaned.Result.ObservedState != "rollback_container_removed_snapshot_retained" {
		t.Fatalf("explicit real Engine rollback cleanup = %+v, err=%v", cleaned, err)
	}
	if _, err := engine.Inspect(ctx, initial.ID); !errdefs.IsNotFound(err) {
		t.Fatalf("rollback container remains after explicit cleanup: %v", err)
	}
	if _, _, err := engine.ImageInspect(ctx, snapshotRef); err != nil {
		t.Fatalf("snapshot backing the replacement was removed: %v", err)
	} else if snapshotID, labels, inspectErr := engine.ImageInspect(ctx, snapshotRef); inspectErr != nil || snapshotID != record.SnapshotID || labels[markerTaskLabel] != taskID {
		t.Fatalf("snapshot identity/owner changed after cleanup: id=%s record=%s labels=%+v err=%v", snapshotID, record.SnapshotID, labels, inspectErr)
	} else {
		t.Logf("active writable-layer snapshot image=%s", snapshotID)
	}
	for _, path := range markerPaths {
		contents, err := runS12Docker(ctx, socket, "exec", newID, "cat", path)
		if err != nil || sha256.Sum256(contents) != markerHashes[path] {
			t.Fatalf("cleanup damaged retained volume/bind/writable data at %s: err=%v", path, err)
		}
	}
}

func createDindFixture(ctx context.Context, engineClient *client.Client, socket, name, image, suite, networkName, networkName2 string,
	staticIPA, staticIPB netip.Addr, volumeName, bindRoot string) (string, error) {
	port, err := network.ParsePort("80/tcp")
	if err != nil {
		return "", err
	}
	labels := map[string]string{"io.nodedance.test": "true", "io.nodedance.suite": suite}
	// Prime the generated anonymous volume and all marker contents as root,
	// then run the inspected/rebuilt fixture as a non-root user. This makes the
	// User setting meaningful without making volume writes depend on Engine
	// defaults or mutating any pre-existing volume.
	initConfig := &container.Config{Image: image,
		Cmd:    []string{"sh", "-c", "set -eu; mkdir -p /data /anonymous /bind; printf named > /data/named-marker; printf anonymous > /anonymous/anonymous-marker; chown -R 1000:1000 /data /anonymous; chown 1000:1000 /bind/bind-marker"},
		Labels: labels}
	anonymousVolumeLabels := map[string]string{"io.nodedance.test": "true", "io.nodedance.suite": suite, "fixture.role": "anonymous-volume"}
	initHost := &container.HostConfig{NetworkMode: container.NetworkMode("bridge"), Mounts: []mount.Mount{
		{Type: mount.TypeVolume, Source: volumeName, Target: "/data"},
		{Type: mount.TypeVolume, Target: "/anonymous", VolumeOptions: &mount.VolumeOptions{Labels: anonymousVolumeLabels}},
		{Type: mount.TypeBind, Source: bindRoot, Target: "/bind"},
	}}
	initName := name + "-mount-init"
	initCreated, err := engineClient.ContainerCreate(ctx, client.ContainerCreateOptions{Config: initConfig, HostConfig: initHost, Name: initName})
	if err != nil {
		return "", fmt.Errorf("create real DIND mount initializer: %w", err)
	}
	if _, err := engineClient.ContainerStart(ctx, initCreated.ID, client.ContainerStartOptions{}); err != nil {
		_, _ = engineClient.ContainerRemove(context.WithoutCancel(ctx), initCreated.ID, client.ContainerRemoveOptions{RemoveVolumes: false, Force: true})
		return "", fmt.Errorf("start real DIND mount initializer: %w", err)
	}
	waitOutput, err := runS12Docker(ctx, socket, "wait", initName)
	if err != nil || strings.TrimSpace(string(waitOutput)) != "0" {
		return "", fmt.Errorf("wait for real DIND mount initializer: exit=%q err=%v", strings.TrimSpace(string(waitOutput)), err)
	}
	initInspect, err := engineClient.ContainerInspect(ctx, initCreated.ID, client.ContainerInspectOptions{})
	if err != nil {
		return "", fmt.Errorf("inspect real DIND mount initializer: %w", err)
	}
	anonymousVolume := ""
	for _, mountPoint := range initInspect.Container.Mounts {
		if mountPoint.Destination == "/anonymous" {
			anonymousVolume = mountPoint.Name
			break
		}
	}
	if anonymousVolume == "" {
		return "", errors.New("real DIND mount initializer did not allocate the anonymous volume")
	}
	if _, err := engineClient.ContainerRemove(ctx, initCreated.ID, client.ContainerRemoveOptions{RemoveVolumes: false, Force: true}); err != nil {
		return "", fmt.Errorf("remove completed real DIND mount initializer while retaining its volumes: %w", err)
	}

	config := &container.Config{
		Image: image, User: "1000:1000", WorkingDir: "/tmp",
		Entrypoint: []string{"sh", "-c"}, Cmd: []string{"while :; do sleep 1; done"},
		Env: []string{"NODEDANCE_FIXTURE=rich-inspect", "APP_MODE=rebuild-fixture", "FEATURE_FLAG=preserve-me"},
		Labels: map[string]string{
			"io.nodedance.test": "true", "io.nodedance.suite": suite,
			"app.kubernetes.io/name": "nodedance-rebuild-fixture", "fixture.role": "rich-config",
		},
		ExposedPorts: network.PortSet{port: struct{}{}}}
	settings := &network.EndpointSettings{Aliases: []string{name, "nd-s12-primary"},
		IPAMConfig: &network.EndpointIPAMConfig{IPv4Address: staticIPA}}
	settings2 := &network.EndpointSettings{Aliases: []string{name, "nd-s12-secondary"},
		IPAMConfig: &network.EndpointIPAMConfig{IPv4Address: staticIPB}}
	host := &container.HostConfig{NetworkMode: container.NetworkMode("bridge"), RestartPolicy: container.RestartPolicy{Name: "always"},
		Resources: container.Resources{Memory: 128 * 1024 * 1024, CPUPeriod: 100_000, CPUQuota: 50_000,
			PidsLimit: int64Pointer(128)},
		PortBindings: network.PortMap{port: {{HostIP: netip.MustParseAddr("127.0.0.1"), HostPort: "38080"}}},
		Mounts: []mount.Mount{
			{Type: mount.TypeVolume, Source: volumeName, Target: "/data"},
			{Type: mount.TypeVolume, Source: anonymousVolume, Target: "/anonymous"},
			{Type: mount.TypeBind, Source: bindRoot, Target: "/bind"},
		}}
	created, err := engineClient.ContainerCreate(ctx, client.ContainerCreateOptions{Config: config, HostConfig: host,
		NetworkingConfig: &network.NetworkingConfig{EndpointsConfig: map[string]*network.EndpointSettings{
			networkName: settings, networkName2: settings2,
		}}, Name: name})
	if err != nil {
		return "", fmt.Errorf("create real DIND fixture container: %w", err)
	}
	if _, err := engineClient.ContainerStart(ctx, created.ID, client.ContainerStartOptions{}); err != nil {
		_, _ = engineClient.ContainerRemove(context.WithoutCancel(ctx), created.ID, client.ContainerRemoveOptions{RemoveVolumes: false, Force: true})
		return "", fmt.Errorf("start real DIND fixture container: %w", err)
	}
	return anonymousVolume, nil
}

func requireS12DIND(t *testing.T, root string) (string, string, string) {
	t.Helper()
	root, err := filepath.Abs(root)
	if err != nil {
		t.Fatal(err)
	}
	repoRoot := findS12RepoRoot(t)
	relative, err := filepath.Rel(filepath.Join(repoRoot, ".artifacts", "dind"), root)
	if err != nil || (relative != "v28" && relative != "v29" && relative != "s12-rich-config-v28" && relative != "s12-rich-config-v29") {
		t.Fatalf("S12 DIND root must be this checkout's dedicated Engine 28/29 fixture directory: %s", root)
	}
	markerPath := filepath.Join(root, "owner.json")
	markerBytes, err := os.ReadFile(markerPath)
	if err != nil {
		t.Fatalf("S12 DIND owner marker is unavailable: %v", err)
	}
	var owner s12DindOwner
	if err := json.Unmarshal(markerBytes, &owner); err != nil {
		t.Fatalf("decode S12 DIND owner marker: %v", err)
	}
	wantSocket := filepath.Join(root, "socket", "docker.sock")
	wantSuite := "nodedance-s00-dind"
	if relative == "s12-rich-config-v28" || relative == "s12-rich-config-v29" {
		engine := strings.TrimPrefix(relative, "s12-rich-config-v")
		if owner.ContainerName != "nodedance-s12-rich-config-dind-v"+engine || owner.NetworkName != "nodedance-s12-rich-config-net-v"+engine {
			t.Fatalf("S12 rich-config owner marker does not identify its exact dedicated Engine resources: %+v", owner)
		}
	}
	if owner.Suite != wantSuite || owner.Socket != wantSocket || owner.HostDaemon == "" ||
		!strings.HasPrefix(owner.ServerVersion, "28.") && !strings.HasPrefix(owner.ServerVersion, "29.") {
		t.Fatalf("S12 DIND owner marker does not identify a dedicated NodeDance 28/29 Engine: %+v", owner)
	}
	version, err := runS12Docker(context.Background(), "unix://"+wantSocket, "version", "--format", "{{.Server.Version}}")
	if err != nil || strings.TrimSpace(string(version)) != owner.ServerVersion {
		t.Fatalf("DIND socket/version differs from owner marker: %q %v", version, err)
	}
	return root, "unix://" + wantSocket, owner.ServerVersion
}

func runS12Docker(ctx context.Context, socket string, args ...string) ([]byte, error) {
	commandArgs := append([]string{"--host", socket}, args...)
	command := exec.CommandContext(ctx, "docker", commandArgs...)
	output, err := command.CombinedOutput()
	if err != nil {
		return output, fmt.Errorf("docker --host %s %s: %w: %s", socket, strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}
	return output, nil
}

func findS12RepoRoot(t *testing.T) string {
	t.Helper()
	current, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(current, "test-images.lock.json")); err == nil {
			return current
		}
		parent := filepath.Dir(current)
		if parent == current {
			t.Fatal("could not find repository root containing test-images.lock.json")
		}
		current = parent
	}
}

func recordBackupName(t *testing.T, store *Store, taskID string) string {
	t.Helper()
	record, err := store.Get(context.Background(), taskID)
	if err != nil {
		t.Fatal(err)
	}
	return record.BackupName
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func mountNameAt(mounts []container.MountPoint, destination string) string {
	for _, item := range mounts {
		if item.Destination == destination {
			return item.Name
		}
	}
	return ""
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

func assertRichDindFixture(t *testing.T, item Container) {
	t.Helper()
	if item.Config == nil || item.HostConfig == nil {
		t.Fatal("rich DIND fixture did not return Config and HostConfig")
	}
	wantEnv := []string{"NODEDANCE_FIXTURE=rich-inspect", "APP_MODE=rebuild-fixture", "FEATURE_FLAG=preserve-me"}
	if item.Config.User != "1000:1000" || item.Config.WorkingDir != "/tmp" ||
		!reflect.DeepEqual(item.Config.Entrypoint, []string{"sh", "-c"}) ||
		!reflect.DeepEqual(item.Config.Cmd, []string{"while :; do sleep 1; done"}) ||
		!contains(item.Config.Env, wantEnv[0]) || !contains(item.Config.Env, wantEnv[1]) || !contains(item.Config.Env, wantEnv[2]) ||
		item.Config.Labels["app.kubernetes.io/name"] != "nodedance-rebuild-fixture" || item.Config.Labels["fixture.role"] != "rich-config" {
		t.Fatalf("real DIND fixture is missing its non-default Config values: user=%q workdir=%q entrypoint=%q cmd=%q env=%q labels=%v",
			item.Config.User, item.Config.WorkingDir, item.Config.Entrypoint, item.Config.Cmd, item.Config.Env, item.Config.Labels)
	}
	resources := item.HostConfig.Resources
	if item.HostConfig.RestartPolicy.Name != "always" || resources.Memory != 128*1024*1024 || resources.NanoCPUs != 0 ||
		resources.CPUPeriod != 100_000 || resources.CPUQuota != 50_000 || resources.PidsLimit == nil || *resources.PidsLimit != 128 {
		t.Fatalf("real DIND fixture is missing its non-default HostConfig values: restart=%+v resources=%+v", item.HostConfig.RestartPolicy, resources)
	}
}

func assertRebuildPreservesRichInspectConfig(t *testing.T, before, after Container) {
	t.Helper()
	if before.Config == nil || after.Config == nil || before.HostConfig == nil || after.HostConfig == nil {
		t.Fatal("real Engine Inspect omitted Config or HostConfig before/after rebuild")
	}
	beforeConfig, err := cloneJSON(*before.Config)
	if err != nil {
		t.Fatalf("copy pre-rebuild Config: %v", err)
	}
	afterConfig, err := cloneJSON(*after.Config)
	if err != nil {
		t.Fatalf("copy post-rebuild Config: %v", err)
	}
	// The snapshot is the expected replacement image; the two labels identify
	// the controlled rebuild task/owned snapshot. Neither is user configuration.
	beforeConfig.Image = afterConfig.Image
	delete(afterConfig.Labels, markerTaskLabel)
	delete(afterConfig.Labels, markerOwnerLabel)
	if !reflect.DeepEqual(beforeConfig, afterConfig) {
		beforeJSON, _ := json.Marshal(beforeConfig)
		afterJSON, _ := json.Marshal(afterConfig)
		t.Fatalf("real Engine rebuild changed Config beyond the snapshot image and task labels:\nbefore=%s\nafter=%s", beforeJSON, afterJSON)
	}

	beforeHost, err := cloneJSON(*before.HostConfig)
	if err != nil {
		t.Fatalf("copy pre-rebuild HostConfig: %v", err)
	}
	afterHost, err := cloneJSON(*after.HostConfig)
	if err != nil {
		t.Fatalf("copy post-rebuild HostConfig: %v", err)
	}
	// The requested host-port change is the only expected HostConfig difference.
	beforeHost.PortBindings = afterHost.PortBindings
	if !reflect.DeepEqual(beforeHost, afterHost) {
		beforeJSON, _ := json.Marshal(beforeHost)
		afterJSON, _ := json.Marshal(afterHost)
		t.Fatalf("real Engine rebuild changed HostConfig beyond the requested port binding:\nbefore=%s\nafter=%s", beforeJSON, afterJSON)
	}

	beforeNetworks, err := normalizedDindNetworks(before.Networks, before.ID)
	if err != nil {
		t.Fatalf("copy pre-rebuild network configuration: %v", err)
	}
	afterNetworks, err := normalizedDindNetworks(after.Networks, after.ID)
	if err != nil {
		t.Fatalf("copy post-rebuild network configuration: %v", err)
	}
	if !reflect.DeepEqual(beforeNetworks, afterNetworks) {
		beforeJSON, _ := json.Marshal(beforeNetworks)
		afterJSON, _ := json.Marshal(afterNetworks)
		t.Fatalf("real Engine rebuild changed configured network endpoints:\nbefore=%s\nafter=%s", beforeJSON, afterJSON)
	}
}

func normalizedDindNetworks(source map[string]*network.EndpointSettings, containerID string) (map[string]*network.EndpointSettings, error) {
	result := make(map[string]*network.EndpointSettings, len(source))
	for name, endpoint := range source {
		if endpoint == nil {
			return nil, fmt.Errorf("network %q has a nil endpoint", name)
		}
		copyEndpoint, err := cloneJSON(*endpoint)
		if err != nil {
			return nil, err
		}
		clearEndpointRuntimeFields(&copyEndpoint)
		// Docker derives DNSNames and may expose the container short ID in
		// Aliases. Those identity-derived names change across a rebuild; compare
		// only aliases that were configured independently of the old container ID.
		copyEndpoint.DNSNames = nil
		configuredAliases := make([]string, 0, len(copyEndpoint.Aliases))
		for _, alias := range copyEndpoint.Aliases {
			if !isOriginalContainerIDAlias(alias, containerID) {
				configuredAliases = append(configuredAliases, alias)
			}
		}
		copyEndpoint.Aliases = configuredAliases
		result[name] = &copyEndpoint
	}
	return result, nil
}

func int64Pointer(value int64) *int64 { return &value }
