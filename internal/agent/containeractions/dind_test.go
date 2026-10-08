package containeractions

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/CST-Cat/NodeDance/internal/agent/taskjournal"
	"github.com/CST-Cat/NodeDance/internal/taskstate"
	"github.com/containerd/errdefs"
)

type dindOwner struct {
	Suite         string `json:"suite"`
	ContainerName string `json:"container_name"`
	Image         string `json:"image"`
	Socket        string `json:"socket"`
	HostDaemon    string `json:"host_daemon"`
	ServerVersion string `json:"server_version"`
}

type lockedImages struct {
	Images map[string]string `json:"images"`
}

// TestDINDLifecycleActions exercises the real SDK executor against a
// repository-owned Docker-in-Docker Engine 28 or 29. It does not start, stop,
// or clean the outer host daemon. Set NODEDANCE_S05_DIND_ROOT to the exact
// .artifacts/dind/v28 or v29 directory containing the DIND owner.json marker.
func TestDINDLifecycleActions(t *testing.T) {
	root := os.Getenv("NODEDANCE_S05_DIND_ROOT")
	if root == "" {
		t.Skip("set NODEDANCE_S05_DIND_ROOT to a marked NodeDance DIND v28 or v29 directory")
	}
	root, version, image := requireOwnedDIND(t, root)
	socket := "unix://" + filepath.Join(root, "socket", "docker.sock")
	if got := dockerCommand(t, socket, "version", "--format", "{{.Server.Version}}"); !strings.HasPrefix(got, version+".") {
		t.Fatalf("DIND server version %q does not match owner marker version %s", got, version)
	}
	if _, err := dockerCommandResult(socket, "pull", image); err != nil {
		t.Fatalf("pull locked fixture image into owned DIND: %v", err)
	}

	random := make([]byte, 6)
	if _, err := rand.Read(random); err != nil {
		t.Fatal(err)
	}
	runID := hex.EncodeToString(random)
	suite := "nodedance-s05-container-actions-" + runID
	label := "io.nodedance.suite=" + suite
	baseName := "nd-s05-" + version + "-" + runID
	volumeName := baseName + "-data"
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	if _, err := dockerCommandResult(socket, "volume", "create", "--label", "io.nodedance.test=true", "--label", label, volumeName); err != nil {
		t.Fatalf("create owned named volume: %v", err)
	}
	var mainID, composeID, neverStartedID, noRestartID string
	t.Cleanup(func() {
		if mainID != "" {
			cleanupOwnedDINDContainer(t, socket, mainID, label)
		}
		if composeID != "" {
			cleanupOwnedDINDContainer(t, socket, composeID, label)
		}
		if neverStartedID != "" {
			cleanupOwnedDINDContainer(t, socket, neverStartedID, label)
		}
		if noRestartID != "" {
			cleanupOwnedDINDContainer(t, socket, noRestartID, label)
		}
		cleanupOwnedDINDVolume(t, socket, volumeName, label)
	})
	mainID = createDINDContainer(t, ctx, socket, image, baseName, label,
		"--mount", "type=volume,source="+volumeName+",target=/nodedance-data")
	composeID = createDINDContainer(t, ctx, socket, image, baseName+"-compose", label,
		"--label", "com.docker.compose.project=nodedance_s05_"+runID,
		"--label", "com.docker.compose.service=web")
	neverStartedID = createNeverStartedDINDContainer(t, ctx, socket, image, baseName+"-created", label)
	noRestartID = createDINDContainer(t, ctx, socket, image, baseName+"-no-restart", label)

	journal := openDINDJournal(t, version)
	defer func() {
		if err := journal.Close(); err != nil {
			t.Errorf("close DIND test journal: %v", err)
		}
	}()
	engine, err := NewSDKEngine(socket)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := engine.Close(); err != nil {
			t.Errorf("close DIND SDK engine: %v", err)
		}
	})
	executor, err := New(engine, journal, Options{OperationTimeout: 45 * time.Second, VerificationTimeout: 10 * time.Second})
	if err != nil {
		t.Fatal(err)
	}

	counter := 0
	perform := func(action Action, id string, prepare func(*Request)) taskjournal.Snapshot {
		t.Helper()
		counter++
		req := Request{TaskID: fmt.Sprintf("dind-%s-%03d", version, counter), NodeID: "dind-" + version,
			IdempotencyKey: fmt.Sprintf("action-%03d", counter), Action: action, ContainerID: id}
		if prepare != nil {
			prepare(&req)
		}
		task, execErr := executor.Execute(ctx, req)
		if execErr != nil {
			t.Fatalf("%s action returned %s, %v", action, task.Status, execErr)
		}
		if task.Status != taskstate.Succeeded {
			t.Fatalf("%s action status = %s, want succeeded", action, task.Status)
		}
		return task
	}

	neverBefore, err := engine.Inspect(ctx, neverStartedID)
	if err != nil {
		t.Fatalf("inspect never-started container: %v", err)
	}
	zeroStartedAt, parseErr := time.Parse(time.RFC3339Nano, neverBefore.StartedAt)
	if parseErr != nil || !zeroStartedAt.IsZero() || neverBefore.Running || neverBefore.Paused || neverBefore.Restarting {
		t.Fatalf("fixture is not a valid never-started container: %+v", neverBefore)
	}
	neverRestart := perform(ActionRestart, neverStartedID, nil)
	neverAfter, err := engine.Inspect(ctx, neverStartedID)
	if err != nil || neverRestart.Result.ResourceRevision != "started_at_changed" || !neverAfter.Running ||
		!startedAtChanged(neverBefore.StartedAt, neverAfter.StartedAt) {
		t.Fatalf("restart of never-started container lacked fresh start evidence: task=%+v after=%+v err=%v", neverRestart, neverAfter, err)
	}

	perform(ActionStop, mainID, nil)
	assertDINDState(t, socket, mainID, "false", "false")
	perform(ActionStart, mainID, nil)
	assertDINDState(t, socket, mainID, "true", "false")
	perform(ActionPause, mainID, nil)
	assertDINDState(t, socket, mainID, "true", "true")
	perform(ActionResume, mainID, nil)
	assertDINDState(t, socket, mainID, "true", "false")

	before, err := engine.Inspect(ctx, mainID)
	if err != nil {
		t.Fatalf("inspect before lost-ACK restart: %v", err)
	}
	lostAck := &lostRestartAckEngine{Engine: engine}
	lostAckExecutor, err := New(lostAck, journal, Options{OperationTimeout: 45 * time.Second, VerificationTimeout: 10 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	restartTask := performWithExecutor(t, ctx, lostAckExecutor, version, counter+1, ActionRestart, mainID, nil)
	counter++
	after, err := engine.Inspect(ctx, mainID)
	if err != nil {
		t.Fatalf("inspect after lost-ACK restart: %v", err)
	}
	if !after.Running || after.Paused || (!startedAtChanged(before.StartedAt, after.StartedAt) && after.RestartCount <= before.RestartCount) {
		t.Fatalf("DIND restart lacked instance evidence: before=%+v after=%+v", before, after)
	}
	if restartTask.Status != taskstate.Succeeded {
		t.Fatalf("lost-ACK restart task status = %s", restartTask.Status)
	}

	perform(ActionRename, mainID, func(request *Request) { request.NewName = baseName + "-renamed" })
	if got := dockerCommand(t, socket, "inspect", "--format", "{{.Name}}", mainID); got != "/"+baseName+"-renamed" {
		t.Fatalf("renamed container name = %q", got)
	}
	composeTask := counter + 1
	composeRequest := Request{TaskID: fmt.Sprintf("dind-%s-%03d", version, composeTask), NodeID: "dind-" + version,
		IdempotencyKey: fmt.Sprintf("action-%03d", composeTask), Action: ActionRename, ContainerID: composeID, NewName: baseName + "-compose-renamed"}
	composeResult, err := executor.Execute(ctx, composeRequest)
	if err != nil || composeResult.Status != taskstate.Failed {
		t.Fatalf("Compose rename = status %s, err %v; want confirmed failed", composeResult.Status, err)
	}
	if got := dockerCommand(t, socket, "inspect", "--format", "{{.Name}}", composeID); got != "/"+baseName+"-compose" {
		t.Fatalf("Compose container was renamed: %q", got)
	}
	counter++

	deleteRequest := Request{TaskID: fmt.Sprintf("dind-%s-%03d", version, counter+1), NodeID: "dind-" + version,
		IdempotencyKey: fmt.Sprintf("action-%03d", counter+1), Action: ActionDelete, ContainerID: mainID,
		DeleteConfirmed: true, DeleteConfirmationID: mainID}
	deleteResult, err := executor.Execute(ctx, deleteRequest)
	if err != nil || deleteResult.Status != taskstate.Failed {
		t.Fatalf("running delete = status %s, err %v; want confirmed refusal", deleteResult.Status, err)
	}
	if got := dockerCommand(t, socket, "inspect", "--format", "{{.State.Running}}", mainID); got != "true" {
		t.Fatalf("running delete unexpectedly changed container state: %s", got)
	}
	counter++
	perform(ActionStop, mainID, nil)
	deleteRequest.TaskID = fmt.Sprintf("dind-%s-%03d", version, counter+1)
	deleteRequest.IdempotencyKey = fmt.Sprintf("action-%03d", counter+1)
	deleteResult, err = executor.Execute(ctx, deleteRequest)
	if err != nil || deleteResult.Status != taskstate.Succeeded {
		t.Fatalf("stopped delete = status %s, err %v", deleteResult.Status, err)
	}
	if _, err := engine.Inspect(ctx, mainID); !errdefs.IsNotFound(err) {
		t.Fatalf("post-delete Inspect error = %v, want typed Docker NotFound", err)
	}
	if got := dockerCommand(t, socket, "volume", "inspect", "--format", `{{index .Labels "io.nodedance.suite"}}`, volumeName); got != suite {
		t.Fatalf("named volume did not survive deletion with ownership label: %q", got)
	}
	counter++

	noRestartBefore, err := engine.Inspect(ctx, noRestartID)
	if err != nil {
		t.Fatalf("inspect no-restart fixture before: %v", err)
	}
	noRestartExecutor, err := New(&noOpRestartEngine{Engine: engine}, journal, Options{OperationTimeout: 45 * time.Second, VerificationTimeout: 10 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	noRestartRequest := Request{TaskID: fmt.Sprintf("dind-%s-%03d", version, counter+1), NodeID: "dind-" + version,
		IdempotencyKey: fmt.Sprintf("action-%03d", counter+1), Action: ActionRestart, ContainerID: noRestartID}
	noRestartTask, noRestartErr := noRestartExecutor.Execute(ctx, noRestartRequest)
	noRestartAfter, inspectErr := engine.Inspect(ctx, noRestartID)
	if !errors.Is(noRestartErr, ErrOutcomeUnknown) || noRestartTask.Status != taskstate.Unknown || inspectErr != nil ||
		noRestartAfter.StartedAt != noRestartBefore.StartedAt || noRestartAfter.RestartCount != noRestartBefore.RestartCount {
		t.Fatalf("unchanged restart should remain unknown: task=%+v err=%v before=%+v after=%+v inspectErr=%v",
			noRestartTask, noRestartErr, noRestartBefore, noRestartAfter, inspectErr)
	}
	t.Logf("DIND fixture evidence: version=%s suite=%s main=%s compose=%s never_started=%s no_restart=%s volume=%s socket=%s",
		version, suite, mainID, composeID, neverStartedID, noRestartID, volumeName, filepath.Join(root, "socket", "docker.sock"))
}

type lostRestartAckEngine struct{ Engine }

func (e *lostRestartAckEngine) Restart(ctx context.Context, id string) error {
	if err := e.Engine.Restart(ctx, id); err != nil {
		return err
	}
	return context.DeadlineExceeded
}

type noOpRestartEngine struct{ Engine }

func (e *noOpRestartEngine) Restart(context.Context, string) error { return nil }

func performWithExecutor(t *testing.T, ctx context.Context, executor *Executor, version string, number int, action Action, id string, prepare func(*Request)) taskjournal.Snapshot {
	t.Helper()
	req := Request{TaskID: fmt.Sprintf("dind-%s-%03d", version, number), NodeID: "dind-" + version,
		IdempotencyKey: fmt.Sprintf("action-%03d", number), Action: action, ContainerID: id}
	if prepare != nil {
		prepare(&req)
	}
	task, err := executor.Execute(ctx, req)
	if err != nil || task.Status != taskstate.Succeeded {
		t.Fatalf("%s action status=%s err=%v", action, task.Status, err)
	}
	return task
}

func requireOwnedDIND(t *testing.T, root string) (string, string, string) {
	t.Helper()
	root, err := filepath.Abs(root)
	if err != nil {
		t.Fatalf("resolve DIND root: %v", err)
	}
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil || filepath.Clean(resolved) != filepath.Clean(root) {
		t.Fatalf("DIND root must not use symlinks: %s (%v)", root, err)
	}
	version := strings.TrimPrefix(filepath.Base(root), "v")
	if version != "28" && version != "29" {
		t.Fatalf("DIND root must be named v28 or v29, got %q", filepath.Base(root))
	}
	var owner dindOwner
	marker, err := os.ReadFile(filepath.Join(root, "owner.json"))
	if err != nil || json.Unmarshal(marker, &owner) != nil {
		t.Fatalf("read DIND owner marker at %s: %v", root, err)
	}
	expectedName := "nodedance-s00-dind-v" + version
	expectedSocket := filepath.Join(root, "socket", "docker.sock")
	if owner.Suite != "nodedance-s00-dind" || owner.ContainerName != expectedName ||
		owner.Socket != expectedSocket || !strings.HasPrefix(owner.HostDaemon, "unix://") ||
		!strings.HasPrefix(owner.ServerVersion, version+".") {
		t.Fatalf("DIND owner marker does not match expected owned Engine: %+v", owner)
	}
	if info, err := os.Stat(expectedSocket); err != nil || info.Mode()&os.ModeSocket == 0 {
		t.Fatalf("owned DIND Unix socket is unavailable: %v", err)
	}
	repositoryRoot := filepath.Dir(filepath.Dir(filepath.Dir(root)))
	var lock lockedImages
	data, err := os.ReadFile(filepath.Join(repositoryRoot, "test-images.lock.json"))
	if err != nil || json.Unmarshal(data, &lock) != nil {
		t.Fatalf("read locked images beside DIND owner: %v", err)
	}
	image := lock.Images["busybox"]
	if image == "" || !strings.Contains(image, "@sha256:") {
		t.Fatal("test-images.lock.json does not pin a BusyBox digest")
	}
	lockedEngine := lock.Images["engine"+version]
	if owner.Image != lockedEngine {
		t.Fatalf("owner engine image %q does not match locked image %q", owner.Image, lockedEngine)
	}
	return root, version, image
}

func openDINDJournal(t *testing.T, version string) *taskjournal.Store {
	t.Helper()
	parent := t.TempDir()
	if err := os.Chmod(parent, 0o700); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(parent, "journal")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	store, err := taskjournal.Open(context.Background(), filepath.Join(dir, "tasks.db"), "dind-"+version)
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func createDINDContainer(t *testing.T, ctx context.Context, socket, image, name, suiteLabel string, extra ...string) string {
	t.Helper()
	args := []string{"run", "--detach", "--name", name,
		"--label", "io.nodedance.test=true", "--label", suiteLabel}
	args = append(args, extra...)
	args = append(args, image, "sh", "-c", "printf 'NodeDance S05 fixture\\n' >/nodedance-data/fixture.txt 2>/dev/null || true; while :; do sleep 3600; done")
	id, err := dockerCommandResultContext(ctx, socket, args...)
	if err != nil {
		t.Fatalf("create owned lifecycle fixture: %v", err)
	}
	id = strings.TrimSpace(id)
	if !dockerIDPattern.MatchString(id) {
		t.Fatalf("DIND returned invalid fixture container ID %q", id)
	}
	return id
}

func createNeverStartedDINDContainer(t *testing.T, ctx context.Context, socket, image, name, suiteLabel string) string {
	t.Helper()
	output, err := dockerCommandResultContext(ctx, socket, "create", "--name", name,
		"--label", "io.nodedance.test=true", "--label", suiteLabel,
		image, "sh", "-c", "while :; do sleep 3600; done")
	if err != nil {
		t.Fatalf("create never-started DIND fixture: %v", err)
	}
	id := strings.TrimSpace(output)
	if !dockerIDPattern.MatchString(id) {
		t.Fatalf("DIND returned invalid never-started container ID %q", id)
	}
	return id
}

func assertDINDState(t *testing.T, socket, id, running, paused string) {
	t.Helper()
	got := dockerCommand(t, socket, "inspect", "--format", `{{.State.Running}} {{.State.Paused}}`, id)
	want := running + " " + paused
	if got != want {
		t.Fatalf("container state = %q, want %q", got, want)
	}
}

func cleanupOwnedDINDContainer(t *testing.T, socket, id, suiteLabel string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	owned, err := dockerCommandResultContext(ctx, socket, "inspect", "--format", `{{index .Config.Labels "io.nodedance.suite"}}`, id)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "no such object") || strings.Contains(strings.ToLower(err.Error()), "not found") {
			return
		}
		t.Errorf("verify owned container before cleanup: %v", err)
		return
	}
	if strings.TrimSpace(owned) != strings.TrimPrefix(suiteLabel, "io.nodedance.suite=") {
		t.Errorf("refusing cleanup for container with mismatched suite label %q", strings.TrimSpace(owned))
		return
	}
	if _, err := dockerCommandResultContext(ctx, socket, "rm", "--force", id); err != nil {
		t.Errorf("remove exact owned test container: %v", err)
	}
}

func cleanupOwnedDINDVolume(t *testing.T, socket, name, suiteLabel string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	owned, err := dockerCommandResultContext(ctx, socket, "volume", "inspect", "--format", `{{index .Labels "io.nodedance.suite"}}`, name)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "no such volume") || strings.Contains(strings.ToLower(err.Error()), "not found") {
			return
		}
		t.Errorf("verify owned volume before cleanup: %v", err)
		return
	}
	if strings.TrimSpace(owned) != strings.TrimPrefix(suiteLabel, "io.nodedance.suite=") {
		t.Errorf("refusing cleanup for volume with mismatched suite label %q", strings.TrimSpace(owned))
		return
	}
	if _, err := dockerCommandResultContext(ctx, socket, "volume", "rm", name); err != nil {
		t.Errorf("remove exact owned test volume: %v", err)
	}
}

func dockerCommand(t *testing.T, socket string, args ...string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	output, err := dockerCommandResultContext(ctx, socket, args...)
	if err != nil {
		t.Fatalf("Docker CLI %v failed: %v", args, err)
	}
	return strings.TrimSpace(output)
}

func dockerCommandResult(socket string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	return dockerCommandResultContext(ctx, socket, args...)
}

func dockerCommandResultContext(ctx context.Context, socket string, args ...string) (string, error) {
	all := append([]string{"--host", socket}, args...)
	command := exec.CommandContext(ctx, "docker", all...)
	output, err := command.CombinedOutput()
	if err != nil {
		message := strings.TrimSpace(string(output))
		if message == "" {
			return "", fmt.Errorf("docker %s: %w", args[0], err)
		}
		return "", fmt.Errorf("docker %s: %s", args[0], message)
	}
	return string(output), nil
}
