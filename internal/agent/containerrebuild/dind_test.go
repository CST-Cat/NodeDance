package containerrebuild

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
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
	if err := createDindFixture(ctx, engineClient, containerName, image, suite, networkName, networkName2, staticIPA, staticIPB, volumeName, bindRoot); err != nil {
		t.Fatal(err)
	}
	anonymousOutput, err := runS12Docker(ctx, socket, "inspect", "--format", "{{range .Mounts}}{{if eq .Destination \"/anonymous\"}}{{.Name}}{{end}}{{end}}", containerName)
	if err != nil || strings.TrimSpace(string(anonymousOutput)) == "" {
		t.Fatalf("record unique anonymous volume identity before rebuild: %q err=%v", anonymousOutput, err)
	}
	anonymousVolume = strings.TrimSpace(string(anonymousOutput))
	if _, err := runS12Docker(ctx, socket, "exec", containerName, "sh", "-c", "printf writable-"+runID+" > /writable-marker"); err != nil {
		t.Fatalf("write a fixture-only writable-layer marker after startup: %v", err)
	}
	markerPaths := []string{"/data/named-marker", "/anonymous/anonymous-marker", "/bind/bind-marker", "/writable-marker"}
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
		if endpoint == nil || endpoint.IPAMConfig == nil || endpoint.IPAMConfig.IPv4Address != wantIP {
			t.Fatalf("replacement lost requested static address for %s: %+v want=%s", name, endpoint, wantIP)
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

func createDindFixture(ctx context.Context, engineClient *client.Client, name, image, suite, networkName, networkName2 string,
	staticIPA, staticIPB netip.Addr, volumeName, bindRoot string) error {
	port, err := network.ParsePort("80/tcp")
	if err != nil {
		return err
	}
	labels := map[string]string{"io.nodedance.test": "true", "io.nodedance.suite": suite}
	config := &container.Config{Image: image, Cmd: []string{"sh", "-c", "mkdir -p /data /anonymous /bind; printf named > /data/named-marker; printf anonymous > /anonymous/anonymous-marker; while :; do sleep 1; done"},
		Labels: labels, ExposedPorts: network.PortSet{port: struct{}{}}}
	settings := &network.EndpointSettings{Aliases: []string{name, "nd-s12-primary"},
		IPAMConfig: &network.EndpointIPAMConfig{IPv4Address: staticIPA}}
	settings2 := &network.EndpointSettings{Aliases: []string{name, "nd-s12-secondary"},
		IPAMConfig: &network.EndpointIPAMConfig{IPv4Address: staticIPB}}
	host := &container.HostConfig{NetworkMode: container.NetworkMode("bridge"), RestartPolicy: container.RestartPolicy{Name: "always"},
		PortBindings: network.PortMap{port: {{HostIP: netip.MustParseAddr("127.0.0.1"), HostPort: "38080"}}},
		Mounts: []mount.Mount{
			{Type: mount.TypeVolume, Source: volumeName, Target: "/data"},
			{Type: mount.TypeVolume, Target: "/anonymous"},
			{Type: mount.TypeBind, Source: bindRoot, Target: "/bind"},
		}}
	created, err := engineClient.ContainerCreate(ctx, client.ContainerCreateOptions{Config: config, HostConfig: host,
		NetworkingConfig: &network.NetworkingConfig{EndpointsConfig: map[string]*network.EndpointSettings{
			networkName: settings, networkName2: settings2,
		}}, Name: name})
	if err != nil {
		return fmt.Errorf("create real DIND fixture container: %w", err)
	}
	if _, err := engineClient.ContainerStart(ctx, created.ID, client.ContainerStartOptions{}); err != nil {
		_, _ = engineClient.ContainerRemove(context.WithoutCancel(ctx), created.ID, client.ContainerRemoveOptions{RemoveVolumes: false, Force: true})
		return fmt.Errorf("start real DIND fixture container: %w", err)
	}
	return nil
}

func requireS12DIND(t *testing.T, root string) (string, string, string) {
	t.Helper()
	root, err := filepath.Abs(root)
	if err != nil {
		t.Fatal(err)
	}
	repoRoot := findS12RepoRoot(t)
	relative, err := filepath.Rel(filepath.Join(repoRoot, ".artifacts", "dind"), root)
	if err != nil || (relative != "v28" && relative != "v29") {
		t.Fatalf("S12 DIND root must be this checkout's dedicated .artifacts/dind/v28 or v29 directory: %s", root)
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
	if owner.Suite != "nodedance-s00-dind" || owner.Socket != wantSocket || owner.HostDaemon == "" ||
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
