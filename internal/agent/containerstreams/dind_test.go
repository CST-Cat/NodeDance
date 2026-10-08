package containerstreams

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type streamsDINDOwner struct {
	Suite         string `json:"suite"`
	ContainerName string `json:"container_name"`
	Image         string `json:"image"`
	Socket        string `json:"socket"`
	HostDaemon    string `json:"host_daemon"`
	ServerVersion string `json:"server_version"`
}

type streamsLockedImages struct {
	Images map[string]string `json:"images"`
}

// TestDINDLogsAndStats uses only a repository-owned nested Docker daemon.
// It creates and removes its exact labeled fixtures and never stops the daemon
// or runs a prune command. Set NODEDANCE_S05_DIND_ROOT to its marked v28/v29
// directory after the S04 worker releases its test window.
func TestDINDLogsAndStats(t *testing.T) {
	root := os.Getenv("NODEDANCE_S05_DIND_ROOT")
	if root == "" {
		t.Skip("set NODEDANCE_S05_DIND_ROOT to a marked NodeDance DIND v28 or v29 directory")
	}
	root, version, image := requireStreamsDIND(t, root)
	socketPath := filepath.Join(root, "socket", "docker.sock")
	endpoint := "unix://" + socketPath
	if got := streamsDockerCommand(t, endpoint, "version", "--format", "{{.Server.Version}}"); !strings.HasPrefix(got, version+".") {
		t.Fatalf("DIND server version %q does not match owner marker v%s", got, version)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if _, err := streamsDockerCommandResult(ctx, endpoint, "image", "inspect", image); err != nil {
		if _, pullErr := streamsDockerCommandResult(ctx, endpoint, "pull", image); pullErr != nil {
			t.Fatalf("pull locked BusyBox image: %v", pullErr)
		}
	}

	random := make([]byte, 6)
	if _, err := rand.Read(random); err != nil {
		t.Fatal(err)
	}
	runID := hex.EncodeToString(random)
	suite := "nodedance-s05-containerstreams-" + runID
	label := "io.nodedance.suite=" + suite
	base := "nd-s05-streams-" + version + "-" + runID
	var ids []string
	t.Cleanup(func() {
		cleanupStreamsDIND(t, endpoint, ids, label)
	})

	plainID := createStreamsDINDContainer(t, ctx, endpoint, image, base+"-plain", label,
		nil, "sh", "-c", "printf 'stdout-你好\\n'; printf 'stderr-错误\\n' >&2")
	ids = append(ids, plainID)
	ttyID := createStreamsDINDContainer(t, ctx, endpoint, image, base+"-tty", label,
		[]string{"--tty"}, "sh", "-c", "printf 'tty-out-你好\\n'; printf 'tty-err-错误\\n' >&2")
	ids = append(ids, ttyID)
	largeID := createStreamsDINDContainer(t, ctx, endpoint, image, base+"-large", label,
		nil, "sh", "-c", "head -c 2097152 /dev/zero | tr '\\000' a; head -c 2097152 /dev/zero | tr '\\000' b >&2")
	ids = append(ids, largeID)
	followID := createStreamsDINDContainer(t, ctx, endpoint, image, base+"-follow", label,
		nil, "sh", "-c", "printf 'follow-start\\n'; while :; do printf 'follow-你好\\n'; sleep 1; done")
	ids = append(ids, followID)
	waitStreamsDINDExited(t, ctx, endpoint, plainID)
	waitStreamsDINDExited(t, ctx, endpoint, ttyID)
	waitStreamsDINDExited(t, ctx, endpoint, largeID)

	engine, err := NewSDKEngine(endpoint)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := engine.Close(); err != nil {
			t.Errorf("close DIND stream SDK: %v", err)
		}
	})

	plain, plainErr, err := readDINDLogs(ctx, engine, plainID, LogsOptions{Timestamps: false})
	if err != nil || plainErr != nil {
		t.Fatalf("read non-TTY history: error=%v stream=%v", err, plainErr)
	}
	if !bytes.Contains(plain.stdout.Bytes(), []byte("stdout-你好")) || !bytes.Contains(plain.stderr.Bytes(), []byte("stderr-错误")) {
		t.Fatalf("non-TTY streams lost Unicode/stdout/stderr: stdout=%q stderr=%q", plain.stdout.String(), plain.stderr.String())
	}
	if bytes.Contains(plain.stdout.Bytes(), []byte{1, 0, 0, 0}) || bytes.Contains(plain.stderr.Bytes(), []byte{2, 0, 0, 0}) {
		t.Fatal("Docker stream header bytes leaked into non-TTY log payload")
	}

	tty, ttyErr, err := readDINDLogs(ctx, engine, ttyID, LogsOptions{})
	if err != nil || ttyErr != nil {
		t.Fatalf("read TTY history: error=%v stream=%v", err, ttyErr)
	}
	if !bytes.Contains(tty.stdout.Bytes(), []byte("tty-out-你好")) || !bytes.Contains(tty.stdout.Bytes(), []byte("tty-err-错误")) || tty.stderr.Len() != 0 {
		t.Fatalf("TTY output was not preserved as raw merged stdout: stdout=%q stderr=%q", tty.stdout.String(), tty.stderr.String())
	}

	large, largeErr, err := readDINDLogs(ctx, engine, largeID, LogsOptions{})
	if err != nil || largeErr != nil {
		t.Fatalf("read large Docker history: error=%v stream=%v", err, largeErr)
	}
	if large.stdout.Len() != 2*1024*1024 || large.stderr.Len() != 2*1024*1024 ||
		bytes.Count(large.stdout.Bytes(), []byte{'a'}) != 2*1024*1024 || bytes.Count(large.stderr.Bytes(), []byte{'b'}) != 2*1024*1024 {
		t.Fatalf("large stream output sizes stdout=%d stderr=%d", large.stdout.Len(), large.stderr.Len())
	}

	followCtx, stopFollow := context.WithCancel(ctx)
	logReader, err := OpenLogs(followCtx, engine, followID, LogsOptions{Follow: true, Tail: "0", ShowStdout: true})
	if err != nil {
		stopFollow()
		t.Fatalf("open live follow stream: %v", err)
	}
	t.Cleanup(func() {
		stopFollow()
		if err := logReader.Close(); err != nil {
			t.Errorf("close DIND follow stream during cleanup: %v", err)
		}
	})
	followDeadline := time.After(5 * time.Second)
	followSeen := false
	for !followSeen {
		select {
		case frame, ok := <-logReader.Frames:
			if !ok {
				t.Fatal("follow stream closed before live log arrived")
			}
			followSeen = bytes.Contains(frame.Data, []byte("follow-你好"))
		case streamErr, ok := <-logReader.Errors:
			if ok && streamErr != nil {
				t.Fatalf("live follow stream: %v", streamErr)
			}
		case <-followDeadline:
			t.Fatal("live follow did not deliver a new log line")
		}
	}
	stopFollow()
	if err := logReader.Close(); err != nil {
		t.Fatalf("close follow log stream: %v", err)
	}
	select {
	case <-logReader.done:
	case <-time.After(time.Second):
		t.Fatal("follow reader goroutine remained after cancellation")
	}

	counted := &countedStatsEngine{StatsEngine: engine}
	manager, err := NewStatsManager(counted, StatsManagerOptions{CloseTimeout: 2 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := manager.Close(); err != nil {
			t.Errorf("close DIND stats manager during cleanup: %v", err)
		}
	})
	if counted.opens.Load() != 0 {
		t.Fatal("stats engine opened before any subscriber existed")
	}
	first, err := manager.Subscribe(ctx, followID)
	if err != nil {
		t.Fatal(err)
	}
	second, err := manager.Subscribe(ctx, followID)
	if err != nil {
		t.Fatal(err)
	}
	firstStats := waitDINDStats(t, first, 10*time.Second)
	secondStats := waitDINDStats(t, second, 10*time.Second)
	if firstStats.ContainerID != followID || secondStats.ContainerID != followID || firstStats.ObservedAt == nil || secondStats.ObservedAt == nil {
		t.Fatalf("DIND stats samples do not bind to the real container: first=%+v second=%+v", firstStats, secondStats)
	}
	if counted.opens.Load() != 1 {
		t.Fatalf("two subscribers opened %d SDK stats requests, want one", counted.opens.Load())
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	if counted.closes.Load() != 0 {
		t.Fatal("closing one subscriber closed the shared SDK stats response body")
	}
	if err := second.Close(); err != nil {
		t.Fatal(err)
	}
	if counted.closes.Load() != 1 {
		t.Fatalf("last subscriber closed SDK stats bodies %d times, want exactly one", counted.closes.Load())
	}
	if err := manager.Close(); err != nil {
		t.Fatalf("close DIND stats manager: %v", err)
	}

	t.Logf("DIND stream evidence: engine=%s suite=%s plain=%s tty=%s large=%s follow=%s socket=%s stats_opens=%d stats_body_closes=%d",
		version, suite, plainID, ttyID, largeID, followID, socketPath, counted.opens.Load(), counted.closes.Load())
}

type collectedLogs struct {
	stdout bytes.Buffer
	stderr bytes.Buffer
}

func readDINDLogs(ctx context.Context, engine Engine, id string, options LogsOptions) (collectedLogs, error, error) {
	reader, err := OpenLogs(ctx, engine, id, options)
	if err != nil {
		return collectedLogs{}, nil, err
	}
	defer reader.Close()
	var output collectedLogs
	var streamErr error
	for reader.Frames != nil || reader.Errors != nil {
		select {
		case frame, ok := <-reader.Frames:
			if !ok {
				reader.Frames = nil
				continue
			}
			switch frame.Channel {
			case LogStdout:
				_, _ = output.stdout.Write(frame.Data)
			case LogStderr:
				_, _ = output.stderr.Write(frame.Data)
			}
		case err, ok := <-reader.Errors:
			if !ok {
				reader.Errors = nil
			} else if err != nil {
				streamErr = err
			}
		case <-ctx.Done():
			return output, streamErr, ctx.Err()
		}
	}
	return output, streamErr, nil
}

func waitDINDStats(t *testing.T, sub *StatsSubscription, timeout time.Duration) StatsSnapshot {
	t.Helper()
	select {
	case sample, ok := <-sub.Updates:
		if !ok {
			t.Fatal("DIND stats subscription closed before a sample")
			return StatsSnapshot{}
		}
		return sample
	case err, ok := <-sub.Errors:
		if ok && err != nil {
			t.Fatalf("DIND stats stream error: %v", err)
		}
		t.Fatal("DIND stats stream ended before a sample")
		return StatsSnapshot{}
	case <-time.After(timeout):
		t.Fatal("timed out waiting for live DIND stats")
		return StatsSnapshot{}
	}
}

type countedStatsEngine struct {
	StatsEngine
	opens  atomic.Int32
	closes atomic.Int32
}

func (e *countedStatsEngine) OpenStats(ctx context.Context, id string) (io.ReadCloser, error) {
	body, err := e.StatsEngine.OpenStats(ctx, id)
	if err != nil {
		return nil, err
	}
	e.opens.Add(1)
	return &countedReadCloser{ReadCloser: body, onClose: func() { e.closes.Add(1) }}, nil
}

type countedReadCloser struct {
	io.ReadCloser
	once    atomic.Bool
	onClose func()
}

func (r *countedReadCloser) Close() error {
	if r.once.CompareAndSwap(false, true) {
		r.onClose()
	}
	return r.ReadCloser.Close()
}

func requireStreamsDIND(t *testing.T, root string) (string, string, string) {
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
	var owner streamsDINDOwner
	marker, err := os.ReadFile(filepath.Join(root, "owner.json"))
	if err != nil || json.Unmarshal(marker, &owner) != nil {
		t.Fatalf("read DIND owner marker at %s: %v", root, err)
	}
	expectedName := "nodedance-s00-dind-v" + version
	expectedSocket := filepath.Join(root, "socket", "docker.sock")
	if owner.Suite != "nodedance-s00-dind" || owner.ContainerName != expectedName || owner.Socket != expectedSocket ||
		!strings.HasPrefix(owner.HostDaemon, "unix://") || !strings.HasPrefix(owner.ServerVersion, version+".") {
		t.Fatalf("DIND owner marker does not match expected owned Engine: %+v", owner)
	}
	if info, err := os.Stat(expectedSocket); err != nil || info.Mode()&os.ModeSocket == 0 {
		t.Fatalf("owned DIND Unix socket is unavailable: %v", err)
	}
	repositoryRoot := filepath.Dir(filepath.Dir(filepath.Dir(root)))
	var lock streamsLockedImages
	data, err := os.ReadFile(filepath.Join(repositoryRoot, "test-images.lock.json"))
	if err != nil || json.Unmarshal(data, &lock) != nil {
		t.Fatalf("read locked test image list: %v", err)
	}
	image := lock.Images["busybox"]
	if image == "" || !strings.Contains(image, "@sha256:") || owner.Image != lock.Images["engine"+version] {
		t.Fatalf("DIND/test images are not digest-pinned or do not match owner marker: image=%q owner=%q", image, owner.Image)
	}
	return root, version, image
}

func createStreamsDINDContainer(t *testing.T, ctx context.Context, endpoint, image, name, suiteLabel string, options []string, commandArgs ...string) string {
	t.Helper()
	containerCommand := []string{"run", "--detach", "--name", name, "--label", "io.nodedance.test=true", "--label", suiteLabel}
	containerCommand = append(containerCommand, options...)
	containerCommand = append(containerCommand, image)
	containerCommand = append(containerCommand, commandArgs...)
	output, err := streamsDockerCommandResult(ctx, endpoint, containerCommand...)
	if err != nil {
		t.Fatalf("create owned log/stats fixture %s: %v", name, err)
	}
	id := strings.TrimSpace(output)
	if len(id) != 64 {
		t.Fatalf("Docker returned invalid fixture ID %q", id)
	}
	for _, char := range id {
		if !(char >= '0' && char <= '9') && !(char >= 'a' && char <= 'f') {
			t.Fatalf("Docker returned invalid fixture ID %q", id)
		}
	}
	return id
}

func waitStreamsDINDExited(t *testing.T, ctx context.Context, endpoint, id string) {
	t.Helper()
	deadline := time.NewTimer(15 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		status, err := streamsDockerCommandResult(ctx, endpoint, "inspect", "--format", "{{.State.Status}}", id)
		if err == nil && strings.TrimSpace(status) == "exited" {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("wait for fixture exit: %v", ctx.Err())
		case <-deadline.C:
			t.Fatalf("container %s did not exit (last status=%q, error=%v)", id, strings.TrimSpace(status), err)
		case <-ticker.C:
		}
	}
}

func cleanupStreamsDIND(t *testing.T, endpoint string, ids []string, suiteLabel string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	expectedSuite := strings.TrimPrefix(suiteLabel, "io.nodedance.suite=")
	listed, err := streamsDockerCommandResult(ctx, endpoint, "ps", "--all", "--quiet", "--no-trunc", "--filter", "label="+suiteLabel)
	if err != nil {
		t.Errorf("list exact owned DIND fixtures during cleanup: %v", err)
		return
	}
	candidates := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		candidates[id] = struct{}{}
	}
	for _, id := range strings.Fields(listed) {
		candidates[id] = struct{}{}
	}
	for id := range candidates {
		owner, err := streamsDockerCommandResult(ctx, endpoint, "inspect", "--format", `{{index .Config.Labels "io.nodedance.suite"}}`, id)
		if err != nil {
			if strings.Contains(strings.ToLower(err.Error()), "no such object") || strings.Contains(strings.ToLower(err.Error()), "not found") {
				continue
			}
			t.Errorf("verify owned DIND fixture before cleanup: %v", err)
			continue
		}
		if strings.TrimSpace(owner) != expectedSuite {
			t.Errorf("refusing DIND cleanup for ID %s with suite label %q", id, strings.TrimSpace(owner))
			continue
		}
		if _, err := streamsDockerCommandResult(ctx, endpoint, "rm", "--force", id); err != nil {
			t.Errorf("remove exact owned DIND fixture %s: %v", id, err)
		}
	}
}

func streamsDockerCommand(t *testing.T, endpoint string, args ...string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	output, err := streamsDockerCommandResult(ctx, endpoint, args...)
	if err != nil {
		t.Fatalf("Docker CLI %v: %v", args, err)
	}
	return strings.TrimSpace(output)
}

func streamsDockerCommandResult(ctx context.Context, endpoint string, args ...string) (string, error) {
	command := exec.CommandContext(ctx, "docker", append([]string{"--host", endpoint}, args...)...)
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
