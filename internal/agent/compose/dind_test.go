package compose

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	agentdocker "github.com/CST-Cat/NodeDance/internal/agent/docker"
	"github.com/CST-Cat/NodeDance/internal/protocol"
)

type s07DindOwner struct {
	Suite         string `json:"suite"`
	ServerVersion string `json:"server_version"`
	Socket        string `json:"socket"`
}

type s07ImageLock struct {
	Images map[string]string `json:"images"`
}

// TestDINDComposeLifecycle exercises the actual CLI plugin, Compose files,
// Docker Engine inventory, profile filtering, replica verification, and
// persistent named volumes. It only connects to an already-owned DIND socket;
// it never starts, stops, or cleans up the outer daemon.
func TestDINDComposeLifecycle(t *testing.T) {
	root := os.Getenv("NODEDANCE_S07_DIND_ROOT")
	if root == "" {
		t.Skip("set NODEDANCE_S07_DIND_ROOT to an already-running, marker-verified NodeDance DIND")
	}
	root, engineVersion := requireS07DIND(t, root)
	socket := "unix://" + filepath.Join(root, "socket", "docker.sock")
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
	defer cancel()
	lockBytes, err := os.ReadFile(filepath.Join(findRepoRoot(t), "test-images.lock.json"))
	if err != nil {
		t.Fatal(err)
	}
	var lock s07ImageLock
	if err := json.Unmarshal(lockBytes, &lock); err != nil {
		t.Fatal(err)
	}
	image := lock.Images["busybox"]
	if !strings.Contains(image, "@sha256:") {
		t.Fatalf("busybox test image is not digest locked: %q", image)
	}
	runner := ExecRunner{DockerHost: socket}
	if output, err := runner.Run(ctx, ".", []string{"version", "--format", "{{.Server.Version}}"}, socket); err != nil || strings.TrimSpace(string(output)) != engineVersion {
		t.Fatalf("wrong DIND Engine: version=%q err=%v", output, err)
	}
	if _, err := runner.Run(ctx, ".", []string{"pull", image}, socket); err != nil {
		t.Fatalf("pull digest-locked BusyBox fixture: %v", err)
	}
	engine, err := agentdocker.NewSDKEngine(socket)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := engine.Close(); err != nil {
			t.Errorf("close test Engine: %v", err)
		}
	})
	manager, err := NewManager(engine, runner, Options{DockerHost: socket, OperationTimeout: 5 * time.Minute, VerifyTimeout: 45 * time.Second, PollInterval: 250 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}

	var random [6]byte
	if _, err := rand.Read(random[:]); err != nil {
		t.Fatal(err)
	}
	runID := hex.EncodeToString(random[:])
	suite := "nodedance-s07-" + runID
	projectName := "nd-s07-" + strings.Split(engineVersion, ".")[0] + "-" + runID
	workdir := filepath.Join(t.TempDir(), "compose project ; ' quotes "+runID)
	if err := os.MkdirAll(workdir, 0o700); err != nil {
		t.Fatal(err)
	}
	mainFile := filepath.Join(workdir, "compose.yaml")
	overrideFile := filepath.Join(workdir, "compose.override.yaml")
	envFile := filepath.Join(workdir, "environment ; values.env")
	volumeName := "state"
	canary := filepath.Join(filepath.Dir(workdir), "shell-injection-"+runID)
	main := fmt.Sprintf(`services:
  data:
    image: ${ND_FIXTURE_IMAGE}
    command: ["sh", "-c", "mkdir -p /data; while true; do sleep 3600; done"]
    volumes:
      - nd_data:/data
    labels:
      io.nodedance.suite: %s
    deploy:
      replicas: 2
  optional:
    image: ${ND_FIXTURE_IMAGE}
    profiles: ["verification"]
    command: ["sh", "-c", "while true; do sleep 3600; done"]
    labels:
      io.nodedance.suite: %s
volumes:
  nd_data:
    name: %s_%s
    labels:
      io.nodedance.suite: %s
`, suite, suite, projectName, volumeName, suite)
	override := fmt.Sprintf("services:\n  data:\n    labels:\n      io.nodedance.override-loaded: 'yes'\n")
	if err := os.WriteFile(mainFile, []byte(main), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(overrideFile, []byte(override), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(envFile, []byte("ND_FIXTURE_IMAGE="+image+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ref := protocol.ComposeProjectRef{Name: projectName, WorkingDirectory: workdir, ConfigFiles: []string{mainFile, overrideFile}}
	ref.Key = protocol.ComposeProjectKey(ref.Name, ref.WorkingDirectory, ref.ConfigFiles)
	cleanup := func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cleanupCancel()
		_, _ = runner.Run(cleanupCtx, workdir, dindSubcommand(composePrefix(ref, []string{envFile}, nil), "down"), socket)
		// The fixture volume is removed only after its Engine labels prove that
		// this test owns it; Compose down itself must preserve it.
		inspect, inspectErr := runner.Run(cleanupCtx, ".", []string{"volume", "inspect", volumeProjectName(projectName, volumeName)}, socket)
		if inspectErr == nil && strings.Contains(string(inspect), suite) {
			_, _ = runner.Run(cleanupCtx, ".", []string{"volume", "rm", volumeProjectName(projectName, volumeName)}, socket)
		}
	}
	t.Cleanup(cleanup)

	first, err := manager.Execute(ctx, protocol.ComposeRequest{OperationID: "dind-up-" + runID, Action: protocol.ComposeUp,
		Project: ref, EnvFiles: []string{envFile}})
	if err != nil {
		t.Fatalf("Compose up from a source-bound project ref failed: %v", err)
	}
	if first.Project == nil || !first.Verified {
		t.Fatalf("up did not return verified project state: %+v", first)
	}
	for _, service := range first.Project.Services {
		for _, instance := range service.Instances {
			if !strings.Contains(instance.ContainerName, projectName) {
				t.Errorf("instance belongs to another project: %+v", instance)
			}
		}
	}
	dataInstances := serviceInstances(first.Project, "data")
	optionalInstances := serviceInstances(first.Project, "optional")
	if len(dataInstances) != 2 || len(optionalInstances) != 0 {
		t.Fatalf("Compose profile or replica state mismatch: data=%d optional=%d project=%+v", len(dataInstances), len(optionalInstances), first.Project)
	}
	if err := writeVolumeMarker(ctx, runner, workdir, ref, envFile, nil, runID, socket); err != nil {
		t.Fatal(err)
	}

	for _, action := range []protocol.ComposeAction{protocol.ComposeStop, protocol.ComposeStart, protocol.ComposeRestart} {
		result, err := manager.Execute(ctx, protocol.ComposeRequest{OperationID: "dind-" + string(action) + "-" + runID, Action: action,
			Project: ref, EnvFiles: []string{envFile}})
		if err != nil || result.Status != "succeeded" || !result.Verified {
			t.Fatalf("%s failed Engine state verification: response=%+v err=%v", action, result, err)
		}
	}
	down, err := manager.Execute(ctx, protocol.ComposeRequest{OperationID: "dind-down-" + runID, Action: protocol.ComposeDown,
		Project: ref, EnvFiles: []string{envFile}})
	if err != nil || !down.Verified {
		t.Fatalf("Compose down was not verified: response=%+v err=%v", down, err)
	}
	if _, exists := os.Stat(canary); !errors.Is(exists, os.ErrNotExist) {
		t.Fatalf("special path caused an unintended command/file: %v", exists)
	}
	volume, err := runner.Run(ctx, ".", []string{"volume", "inspect", volumeProjectName(projectName, volumeName)}, socket)
	if err != nil || !strings.Contains(string(volume), suite) {
		t.Fatalf("Compose down did not preserve its labeled named volume: output=%s err=%v", volume, err)
	}

	// Compose down deletes all containers and their source labels. The exact
	// previously authorized ref still allows an up; service-name guessing is
	// never used to reconstruct project context.
	second, err := manager.Execute(ctx, protocol.ComposeRequest{OperationID: "dind-up-again-" + runID, Action: protocol.ComposeUp,
		Project: ref, EnvFiles: []string{envFile}, Profiles: []string{"verification"}})
	if err != nil || second.Project == nil || !second.Verified {
		t.Fatalf("up after down failed: response=%+v err=%v", second, err)
	}
	if got := len(serviceInstances(second.Project, "optional")); got != 1 {
		t.Fatalf("activating the optional profile produced %d instances, want 1: %+v", got, second.Project)
	}
	output, err := runner.Run(ctx, workdir, dindSubcommand(composePrefix(ref, []string{envFile}, []string{"verification"}), "exec", "-T", "data", "sh", "-c", "cat /data/marker"), socket)
	if err != nil || strings.TrimSpace(string(output)) != runID {
		t.Fatalf("named volume contents were not preserved through down/up: %q err=%v", output, err)
	}
	if _, err := manager.Execute(ctx, protocol.ComposeRequest{OperationID: "dind-validate-" + runID, Action: protocol.ComposeValidate, Project: ref,
		EnvFiles: []string{envFile}, Profiles: []string{"verification"}}); err != nil {
		t.Fatalf("Compose validate rejected ordered source context: %v", err)
	}
}

func dindSubcommand(prefix []string, command string, extra ...string) []string {
	result := append([]string(nil), prefix...)
	return append(result, append([]string{command}, extra...)...)
}

func volumeProjectName(project, volume string) string { return project + "_" + volume }

func serviceInstances(project *protocol.ComposeProject, name string) []protocol.ComposeServiceInstance {
	for _, service := range project.Services {
		if service.Name == name {
			return service.Instances
		}
	}
	return nil
}

func writeVolumeMarker(ctx context.Context, runner ExecRunner, directory string, ref protocol.ComposeProjectRef, envFile string, profiles []string, marker, host string) error {
	args := dindSubcommand(composePrefix(ref, []string{envFile}, profiles), "exec", "-T", "data", "sh", "-c", `printf '%s' "$1" > /data/marker`, "nodedance-s07", marker)
	_, err := runner.Run(ctx, directory, args, host)
	return err
}

func requireS07DIND(t *testing.T, root string) (string, string) {
	t.Helper()
	root, err := filepath.Abs(root)
	if err != nil {
		t.Fatal(err)
	}
	markerBytes, err := os.ReadFile(filepath.Join(root, "owner.json"))
	if err != nil {
		t.Fatalf("DIND owner marker is required: %v", err)
	}
	var marker s07DindOwner
	if err := json.Unmarshal(markerBytes, &marker); err != nil || marker.Suite != "nodedance-s00-dind" {
		t.Fatal("refusing unowned DIND environment")
	}
	if !strings.HasPrefix(marker.ServerVersion, "28.") && !strings.HasPrefix(marker.ServerVersion, "29.") {
		t.Fatalf("unsupported DIND Engine marker version %q", marker.ServerVersion)
	}
	socket := filepath.Join(root, "socket", "docker.sock")
	if marker.Socket != "" && filepath.Clean(marker.Socket) != filepath.Clean(socket) {
		t.Fatal("DIND marker socket does not match requested test endpoint")
	}
	if info, err := os.Stat(socket); err != nil || info.Mode()&os.ModeSocket == 0 {
		t.Fatalf("DIND socket is unavailable: %v", err)
	}
	return root, marker.ServerVersion
}

func findRepoRoot(t *testing.T) string {
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
			t.Fatal("repository root is not available from working directory")
		}
		current = parent
	}
}
