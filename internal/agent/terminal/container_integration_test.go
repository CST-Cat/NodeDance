//go:build linux

package terminal

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	agentdocker "github.com/CST-Cat/NodeDance/internal/agent/docker"
	"github.com/CST-Cat/NodeDance/internal/protocol"
)

func TestMain(m *testing.M) {
	// Provider's child uses the same bridge as the production Agent executable.
	if len(os.Args) == 3 && os.Args[1] == "terminal-container" {
		if err := RunContainerShell(context.Background(), os.Args[2], os.Stdin, os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestBundledTmuxDockerBridgeKeepsExecAcrossDetach(t *testing.T) {
	if os.Getenv("NODEDANCE_TEST_DOCKER") != "1" || os.Geteuid() != 0 {
		t.Skip("set NODEDANCE_TEST_DOCKER=1 and run as root for Docker bridge integration")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "docker", "run", "--rm", "-d", "--label", "nodedance.test=tmux", "busybox:1.37.0", "sleep", "300")
	data, err := command.Output()
	if err != nil {
		t.Fatal(err)
	}
	id := strings.TrimSpace(string(data))
	defer exec.Command("docker", "rm", "-f", id).Run()
	engine, err := agentdocker.NewSDKEngine(os.Getenv("DOCKER_HOST"))
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Client().Close()
	provider := NewSystemProvider("/bin/sh", engine.Client(), privateTestState(t))
	defer provider.Close()
	endpoint, err := provider.Open(ctx, protocol.TerminalFrame{StreamID: strings.Repeat("b", 32), Action: "open", TargetKind: "container", ContainerID: id, Rows: 24, Columns: 80})
	if err != nil {
		t.Fatal(err)
	}
	defer endpoint.Close()
	done := make(chan struct{})
	go func() { _, _ = io.Copy(io.Discard, endpoint); close(done) }()
	_, err = endpoint.Write([]byte("state=kept; printf started > /tmp/start; (sleep 0.3; printf persisted > /tmp/job) &\r"))
	if err != nil {
		t.Fatal(err)
	}
	waitFile := func(path, value string) {
		t.Helper()
		for deadline := time.Now().Add(5 * time.Second); ; {
			data, err := exec.CommandContext(ctx, "docker", "exec", id, "cat", path).Output()
			if err == nil && string(data) == value {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("container %s did not contain %s", path, value)
			}
			time.Sleep(25 * time.Millisecond)
		}
	}
	waitFile("/tmp/start", "started")
	if err := endpoint.Detach(); err != nil {
		t.Fatal(err)
	}
	waitFile("/tmp/job", "persisted")
	if err := endpoint.Attach(); err != nil {
		t.Fatal(err)
	}
	_, err = endpoint.Write([]byte("printf '%s' \"$state\" > /tmp/state\r"))
	if err != nil {
		t.Fatal(err)
	}
	waitFile("/tmp/state", "kept")
	e := endpoint.(*tmuxEndpoint)
	command = exec.CommandContext(ctx, e.binary, "-S", e.socket, "split-window", "-d", "-h", "-t", "main")
	command.Env = e.env
	if data, err := command.CombinedOutput(); err != nil {
		t.Fatalf("container split: %s %v", data, err)
	}
	for deadline := time.Now().Add(5 * time.Second); ; {
		data, err := exec.CommandContext(ctx, "docker", "exec", id, "pgrep", "-f", "-x", "/bin/sh -i").Output()
		if err == nil && len(strings.Fields(string(data))) == 2 {
			break
		}
		if time.Now().After(deadline) {
			ps, _ := exec.CommandContext(ctx, "docker", "exec", id, "ps").CombinedOutput()
			panes, _ := exec.CommandContext(ctx, e.binary, "-S", e.socket, "list-panes", "-F", "#{pane_pid} #{pane_current_command} #{pane_dead}").CombinedOutput()
			t.Fatalf("new pane did not stay in container: %s %v\nps: %s\npanes: %s", data, err, ps, panes)
		}
		time.Sleep(25 * time.Millisecond)
	}
	command = exec.CommandContext(ctx, e.binary, "-S", e.socket, "list-panes", "-F", "#{pane_pid}")
	command.Env = e.env
	child, err := command.Output()
	if err != nil {
		t.Fatal(err)
	}
	if err := endpoint.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("container reader did not stop")
	}
	// Helper receives SIGHUP on tmux termination and explicitly closes Docker Exec.
	for deadline := time.Now().Add(5 * time.Second); ; {
		data, _ := exec.Command("docker", "exec", id, "pgrep", "-f", "-x", "/bin/sh -i").Output()
		if len(data) == 0 {
			break
		}
		if time.Now().After(deadline) {
			ps, _ := exec.Command("docker", "exec", id, "ps", "-o", "pid,stat,args").CombinedOutput()
			t.Fatalf("Docker Exec remained running after helper %s exited: %s", child, ps)
		}
		time.Sleep(25 * time.Millisecond)
	}
}
