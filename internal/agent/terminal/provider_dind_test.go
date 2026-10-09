package terminal

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/CST-Cat/NodeDance/internal/protocol"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
)

type dindOwner struct {
	Suite         string `json:"suite"`
	ContainerName string `json:"container_name"`
	ServerVersion string `json:"server_version"`
}

func TestDINDContainerTerminalExec(t *testing.T) {
	socket := os.Getenv("NODEDANCE_S09_DIND_SOCKET")
	if socket == "" {
		t.Skip("real Docker Exec case requires NODEDANCE_S09_DIND_SOCKET pointing at the dedicated repository DIND socket")
	}
	socket, err := filepath.Abs(socket)
	if err != nil {
		t.Fatal(err)
	}
	engineDir := filepath.Dir(filepath.Dir(socket))
	version := filepath.Base(engineDir)
	if version != "v28" && version != "v29" || filepath.Base(filepath.Dir(socket)) != "socket" || filepath.Base(socket) != "docker.sock" {
		t.Fatalf("refusing Docker mutation outside an exact v28/v29 test socket path: %q", socket)
	}
	marker, err := os.ReadFile(filepath.Join(engineDir, "owner.json"))
	if err != nil {
		t.Fatalf("read dedicated DIND owner marker: %v", err)
	}
	var owner dindOwner
	if err := json.Unmarshal(marker, &owner); err != nil || owner.Suite != "nodedance-s00-dind" || owner.ContainerName != "nodedance-s00-dind-"+version || !strings.HasPrefix(owner.ServerVersion, strings.TrimPrefix(version, "v")+".") {
		t.Fatalf("refusing unverified DIND engine marker: %+v err=%v", owner, err)
	}

	dockerHost := "unix://" + socket
	cli, err := client.New(client.WithHTTPClient(&http.Client{
		Transport: &http.Transport{ResponseHeaderTimeout: 10 * time.Second}, CheckRedirect: client.CheckRedirect,
	}), client.WithHost(dockerHost), client.WithScheme("http"), client.WithAPIVersionNegotiation())
	if err != nil {
		t.Fatal(err)
	}
	defer cli.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	image := lockedBusyboxImage(t)
	if _, err := cli.ImageInspect(ctx, image); err != nil {
		reader, pullErr := cli.ImagePull(ctx, image, client.ImagePullOptions{})
		if pullErr != nil {
			t.Fatalf("pull locked terminal fixture image: %v", pullErr)
		}
		_, copyErr := io.Copy(io.Discard, reader)
		closeErr := reader.Close()
		if copyErr != nil || closeErr != nil {
			t.Fatalf("finish locked terminal fixture pull: copy=%v close=%v", copyErr, closeErr)
		}
	}
	name := fmt.Sprintf("nodedance-s09-terminal-%d", time.Now().UnixNano())
	labels := map[string]string{"io.nodedance.test": "true", "io.nodedance.suite": name}
	created, err := cli.ContainerCreate(ctx, client.ContainerCreateOptions{Config: &container.Config{
		Image: image, Cmd: []string{"sleep", "300"}, Labels: labels,
	}, Name: name})
	if err != nil || created.ID == "" {
		t.Fatalf("create exact-owner container fixture: id=%q err=%v", created.ID, err)
	}
	defer func() {
		inspectCtx, inspectCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer inspectCancel()
		inspected, inspectErr := cli.ContainerInspect(inspectCtx, created.ID, client.ContainerInspectOptions{})
		if inspectErr != nil {
			return
		}
		if inspected.Container.ID != created.ID || inspected.Container.Config == nil || inspected.Container.Config.Labels["io.nodedance.suite"] != name {
			t.Errorf("refusing to remove container with mismatched fixture owner: %s", created.ID)
			return
		}
		removeCtx, removeCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer removeCancel()
		if _, removeErr := cli.ContainerRemove(removeCtx, created.ID, client.ContainerRemoveOptions{Force: true}); removeErr != nil {
			t.Errorf("remove exact-owner terminal fixture: %v", removeErr)
		}
	}()
	if _, err := cli.ContainerStart(ctx, created.ID, client.ContainerStartOptions{}); err != nil {
		t.Fatalf("start exact-owner terminal fixture: %v", err)
	}
	provider := NewSystemProvider("/bin/sh", dockerHost)
	endpoint, err := provider.Open(ctx, protocol.TerminalFrame{StreamID: "abcdefghijklmnopqrstuvwxyz0123456789DIND", Action: protocol.TerminalActionOpen,
		TargetKind: protocol.TerminalTargetContainer, ContainerID: created.ID, Rows: 24, Columns: 80})
	if err != nil {
		t.Fatalf("open real Docker Exec terminal: %v", err)
	}
	exec := endpoint.(*containerEndpoint)
	output := make(chan []byte, 16)
	readDone := make(chan error, 1)
	go func() {
		buffer := make([]byte, 4096)
		for {
			count, readErr := endpoint.Read(buffer)
			if count > 0 {
				output <- append([]byte(nil), buffer[:count]...)
			}
			if readErr != nil {
				readDone <- readErr
				return
			}
		}
	}()
	if _, err := endpoint.Write([]byte("printf 'ND_DOCKER_UTF8_你好\\n'\r")); err != nil {
		t.Fatalf("write Docker terminal input: %v", err)
	}
	deadline := time.NewTimer(10 * time.Second)
	var outputText strings.Builder
	for !strings.Contains(outputText.String(), "ND_DOCKER_UTF8_你好") {
		select {
		case data := <-output:
			outputText.Write(data)
		case readErr := <-readDone:
			t.Fatalf("Docker Exec ended before output: %v; captured=%q", readErr, outputText.String())
		case <-deadline.C:
			t.Fatalf("Docker Exec did not return Unicode command output: %q", outputText.String())
		}
	}
	if err := endpoint.Resize(38, 116); err != nil {
		t.Fatalf("resize real Docker Exec TTY: %v", err)
	}
	if _, err := endpoint.Write([]byte("stty size\r")); err != nil {
		t.Fatalf("write Docker terminal resize query: %v", err)
	}
	for !strings.Contains(outputText.String(), "38 116") {
		select {
		case data := <-output:
			outputText.Write(data)
		case readErr := <-readDone:
			t.Fatalf("Docker Exec ended before resize output: %v; captured=%q", readErr, outputText.String())
		case <-deadline.C:
			t.Fatalf("Docker Exec did not report resized dimensions: %q", outputText.String())
		}
	}
	if _, err := endpoint.Write([]byte("printf 'ND_TERMINAL_SLEEP_STARTED\\n'; sleep 300\r")); err != nil {
		t.Fatalf("start foreground Docker Exec sleep: %v", err)
	}
	sleepDeadline := time.NewTimer(10 * time.Second)
	defer sleepDeadline.Stop()
	for !strings.Contains(outputText.String(), "ND_TERMINAL_SLEEP_STARTED") {
		select {
		case data := <-output:
			outputText.Write(data)
		case readErr := <-readDone:
			t.Fatalf("Docker Exec ended before foreground sleep marker: %v; captured=%q", readErr, outputText.String())
		case <-sleepDeadline.C:
			t.Fatalf("Docker Exec did not start foreground sleep: %q", outputText.String())
		}
	}
	// Allow the shell to hand control to sleep before the close path sends
	// Ctrl-C followed by the fixed exit line.
	time.Sleep(150 * time.Millisecond)
	if err := endpoint.Close(); err != nil {
		t.Fatalf("close Docker Exec with foreground sleep: %v", err)
	}
	for {
		inspectCtx, inspectCancel := context.WithTimeout(context.Background(), time.Second)
		result, inspectErr := cli.ExecInspect(inspectCtx, exec.execID, client.ExecInspectOptions{})
		inspectCancel()
		if inspectErr == nil && !result.Running {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("Docker Exec process remained running after terminal close")
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func lockedBusyboxImage(t *testing.T) string {
	t.Helper()
	path := filepath.Join(findRepositoryRoot(t), "test-images.lock.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var lock struct {
		Images map[string]string `json:"images"`
	}
	if err := json.Unmarshal(data, &lock); err != nil || lock.Images["busybox"] == "" {
		t.Fatalf("locked BusyBox test image is missing: %v", err)
	}
	return lock.Images["busybox"]
}

func findRepositoryRoot(t *testing.T) string {
	t.Helper()
	working, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for directory := working; ; directory = filepath.Dir(directory) {
		if _, err := os.Stat(filepath.Join(directory, "test-images.lock.json")); err == nil {
			return directory
		}
		parent := filepath.Dir(directory)
		if parent == directory {
			t.Fatal("could not locate repository root for locked test images")
		}
	}
}

func TestDINDTerminalNoShellAndStoppedContainerFailures(t *testing.T) {
	cli, ctx, cancel, host := connectS09DIND(t)
	defer cancel()
	defer cli.Close()
	name := fmt.Sprintf("nodedance-s09-failure-%d", time.Now().UnixNano())
	labels := map[string]string{"io.nodedance.test": "true", "io.nodedance.suite": name}
	created, err := cli.ContainerCreate(ctx, client.ContainerCreateOptions{Config: &container.Config{
		Image: lockedBusyboxImage(t), Cmd: []string{"true"}, Labels: labels,
	}, Name: name})
	if err != nil || created.ID == "" {
		t.Fatalf("create exact-owner failure fixture: id=%q err=%v", created.ID, err)
	}
	cleanupDINDContainer(t, cli, created.ID, name)
	waiter := cli.ContainerWait(ctx, created.ID, client.ContainerWaitOptions{Condition: container.WaitConditionNotRunning})
	if _, err := cli.ContainerStart(ctx, created.ID, client.ContainerStartOptions{}); err != nil {
		t.Fatalf("start exact-owner short-lived fixture: %v", err)
	}
	select {
	case err := <-waiter.Error:
		t.Fatalf("wait for short-lived fixture to stop: %v", err)
	case <-ctx.Done():
		t.Fatalf("short-lived fixture did not stop: %v", ctx.Err())
	case <-waiter.Result:
	}
	waitDINDContainerStopped(t, ctx, cli, created.ID)
	provider := NewSystemProvider("/bin/sh", host)
	if endpoint, err := provider.Open(ctx, protocol.TerminalFrame{StreamID: "abcdefghijklmnopqrstuvwxyz0123456789STOP", Action: protocol.TerminalActionOpen,
		TargetKind: protocol.TerminalTargetContainer, ContainerID: created.ID, Rows: 24, Columns: 80}); err == nil {
		_ = endpoint.Close()
		t.Fatal("terminal opened against a stopped container")
	}

	imageTag := "nodedance-s09-shellless:" + strings.TrimPrefix(name, "nodedance-s09-failure-")
	removeImage := func() {
		inspectCtx, inspectCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer inspectCancel()
		image, inspectErr := cli.ImageInspect(inspectCtx, imageTag)
		if inspectErr != nil {
			return
		}
		if image.ID == "" || !containsString(image.RepoTags, imageTag) {
			t.Errorf("refusing to remove image with mismatched fixture owner: ref=%q id=%q tags=%v", imageTag, image.ID, image.RepoTags)
			return
		}
		removeCtx, removeCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer removeCancel()
		if _, removeErr := cli.ImageRemove(removeCtx, image.ID, client.ImageRemoveOptions{}); removeErr != nil {
			t.Errorf("remove exact-owner shell-less image: %v", removeErr)
		}
	}
	t.Cleanup(removeImage)
	createShelllessImage(t, ctx, cli, created.ID, imageTag)
	noShellName := name + "-no-shell"
	noShell, err := cli.ContainerCreate(ctx, client.ContainerCreateOptions{Config: &container.Config{
		Image: imageTag, Cmd: []string{"/bin/busybox", "sleep", "300"}, Labels: labels,
	}, Name: noShellName})
	if err != nil || noShell.ID == "" {
		t.Fatalf("create exact-owner shell-less fixture: id=%q err=%v", noShell.ID, err)
	}
	cleanupDINDContainer(t, cli, noShell.ID, name)
	if _, err := cli.ContainerStart(ctx, noShell.ID, client.ContainerStartOptions{}); err != nil {
		t.Fatalf("start exact-owner shell-less fixture: %v", err)
	}
	if endpoint, err := provider.Open(ctx, protocol.TerminalFrame{StreamID: "abcdefghijklmnopqrstuvwxyz0123456789NOSHELL", Action: protocol.TerminalActionOpen,
		TargetKind: protocol.TerminalTargetContainer, ContainerID: noShell.ID, Rows: 24, Columns: 80}); err == nil {
		_ = endpoint.Close()
		t.Fatal("terminal opened in a shell-less container")
	}
}

func connectS09DIND(t *testing.T) (*client.Client, context.Context, context.CancelFunc, string) {
	t.Helper()
	socket := os.Getenv("NODEDANCE_S09_DIND_SOCKET")
	if socket == "" {
		t.Skip("real Docker terminal cases require NODEDANCE_S09_DIND_SOCKET")
	}
	socket, err := filepath.Abs(socket)
	if err != nil {
		t.Fatal(err)
	}
	engineDir := filepath.Dir(filepath.Dir(socket))
	version := filepath.Base(engineDir)
	if version != "v28" && version != "v29" || filepath.Base(filepath.Dir(socket)) != "socket" || filepath.Base(socket) != "docker.sock" {
		t.Fatalf("refusing Docker mutation outside an exact v28/v29 test socket path: %q", socket)
	}
	marker, err := os.ReadFile(filepath.Join(engineDir, "owner.json"))
	if err != nil {
		t.Fatalf("read dedicated DIND owner marker: %v", err)
	}
	var owner dindOwner
	if err := json.Unmarshal(marker, &owner); err != nil || owner.Suite != "nodedance-s00-dind" || owner.ContainerName != "nodedance-s00-dind-"+version || !strings.HasPrefix(owner.ServerVersion, strings.TrimPrefix(version, "v")+".") {
		t.Fatalf("refusing unverified DIND engine marker: %+v err=%v", owner, err)
	}
	dockerHost := "unix://" + socket
	cli, err := client.New(client.WithHTTPClient(&http.Client{
		Transport: &http.Transport{ResponseHeaderTimeout: 10 * time.Second}, CheckRedirect: client.CheckRedirect,
	}), client.WithHost(dockerHost), client.WithScheme("http"), client.WithAPIVersionNegotiation())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	return cli, ctx, cancel, dockerHost
}

func waitDINDContainerStopped(t *testing.T, ctx context.Context, cli *client.Client, containerID string) {
	t.Helper()
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		inspectCtx, cancel := context.WithTimeout(ctx, time.Second)
		inspected, err := cli.ContainerInspect(inspectCtx, containerID, client.ContainerInspectOptions{})
		cancel()
		if err != nil {
			t.Fatalf("inspect short-lived fixture while awaiting stopped state: %v", err)
		}
		if inspected.Container.ID != containerID || inspected.Container.State == nil {
			t.Fatalf("short-lived fixture identity or state changed while awaiting stop: id=%q state=%+v", inspected.Container.ID, inspected.Container.State)
		}
		if !inspected.Container.State.Running {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("test context expired while awaiting stopped container state: %v", ctx.Err())
		case <-deadline.C:
			t.Fatal("ContainerWait completed but ContainerInspect.State.Running remained true")
		case <-ticker.C:
		}
	}
}

func cleanupDINDContainer(t *testing.T, cli *client.Client, containerID, owner string) {
	t.Helper()
	t.Cleanup(func() {
		inspectCtx, inspectCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer inspectCancel()
		inspected, inspectErr := cli.ContainerInspect(inspectCtx, containerID, client.ContainerInspectOptions{})
		if inspectErr != nil {
			return
		}
		if inspected.Container.ID != containerID || inspected.Container.Config == nil || inspected.Container.Config.Labels["io.nodedance.suite"] != owner {
			t.Errorf("refusing to remove container with mismatched fixture owner: %s", containerID)
			return
		}
		removeCtx, removeCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer removeCancel()
		if _, removeErr := cli.ContainerRemove(removeCtx, containerID, client.ContainerRemoveOptions{Force: true}); removeErr != nil {
			t.Errorf("remove exact-owner terminal fixture %s: %v", containerID, removeErr)
		}
	})
}

func createShelllessImage(t *testing.T, ctx context.Context, cli *client.Client, sourceContainerID, imageTag string) {
	t.Helper()
	rootFS, err := cli.ContainerExport(ctx, sourceContainerID, client.ContainerExportOptions{})
	if err != nil {
		t.Fatalf("export locked BusyBox test fixture: %v", err)
	}
	defer rootFS.Close()
	reader := tar.NewReader(rootFS)
	var busybox []byte
	for {
		header, nextErr := reader.Next()
		if nextErr == io.EOF {
			break
		}
		if nextErr != nil {
			t.Fatalf("read locked BusyBox fixture archive: %v", nextErr)
		}
		cleanName := strings.TrimPrefix(header.Name, "./")
		// The locked image stores the single BusyBox binary under the `[` applet
		// name and hard-links every other applet to it, including /bin/sh. Copying
		// only this regular file into a fresh rootfs removes all shell aliases.
		if cleanName == "bin/[" && (header.Typeflag == tar.TypeReg || header.Typeflag == tar.TypeRegA) {
			if header.Size <= 0 || header.Size > 8<<20 {
				t.Fatalf("locked BusyBox executable has unsafe size: %d", header.Size)
			}
			busybox, err = io.ReadAll(io.LimitReader(reader, header.Size+1))
			if err != nil || int64(len(busybox)) != header.Size {
				t.Fatalf("read locked BusyBox executable: size=%d err=%v", len(busybox), err)
			}
			break
		}
	}
	if len(busybox) == 0 {
		t.Fatal("locked BusyBox root filesystem did not contain its regular applet binary")
	}
	var rootArchive bytes.Buffer
	writer := tar.NewWriter(&rootArchive)
	if err := writer.WriteHeader(&tar.Header{Name: "bin/busybox", Mode: 0o755, Size: int64(len(busybox)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatalf("write shell-less rootfs header: %v", err)
	}
	if _, err := writer.Write(busybox); err != nil {
		t.Fatalf("write shell-less rootfs executable: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close shell-less rootfs archive: %v", err)
	}
	loaded, err := cli.ImageImport(ctx, client.ImageImportSource{SourceName: "-", Source: bytes.NewReader(rootArchive.Bytes())}, imageTag,
		client.ImageImportOptions{Changes: []string{`CMD ["/bin/busybox","sleep","300"]`}})
	if err != nil {
		t.Fatalf("import shell-less image from locked BusyBox executable: %v", err)
	}
	_, copyErr := io.Copy(io.Discard, loaded)
	closeErr := loaded.Close()
	if copyErr != nil || closeErr != nil {
		t.Fatalf("finish shell-less image import: copy=%v close=%v", copyErr, closeErr)
	}
}

func containsString(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}
