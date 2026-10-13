//go:build linux

package terminal

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/CST-Cat/NodeDance/internal/agent/terminal/tmuxbundle"
	"github.com/CST-Cat/NodeDance/internal/protocol"
)

func TestBundledTmuxKeepsShellAndJobsAcrossDetachAndSupportsPanes(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("root-only Agent integration; release CI runs as root")
	}
	provider := NewSystemProvider("/bin/sh", nil, privateTestState(t))
	defer provider.Close()
	endpoint, err := provider.Open(context.Background(), protocol.TerminalFrame{StreamID: strings.Repeat("a", 32), Action: "open", TargetKind: "host", Rows: 24, Columns: 80})
	if err != nil {
		t.Fatal(err)
	}
	defer endpoint.Close()
	e := endpoint.(*tmuxEndpoint)
	if output, err := exec.Command(e.binary, "-V").Output(); err != nil || strings.TrimSpace(string(output)) != "tmux "+tmuxbundle.Version {
		t.Fatalf("bundled version %q: %v", output, err)
	}
	var mu sync.Mutex
	var output strings.Builder
	done := make(chan struct{})
	go func() {
		defer close(done)
		buffer := make([]byte, 4096)
		for {
			n, err := endpoint.Read(buffer)
			mu.Lock()
			output.Write(buffer[:n])
			mu.Unlock()
			if err != nil {
				return
			}
		}
	}()
	marker := filepath.Join(t.TempDir(), "job")
	_, err = endpoint.Write([]byte("stty -echo; state=kept; printf started > " + marker + ".start; (sleep 0.3; printf persisted > " + marker + ") &\r"))
	if err != nil {
		t.Fatal(err)
	}
	// Confirm command execution before deliberately dropping the tmux client.
	for deadline := time.Now().Add(3 * time.Second); ; {
		data, _ := os.ReadFile(marker + ".start")
		seen := string(data) == "started"
		if seen {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("shell did not accept input")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := endpoint.Detach(); err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(3 * time.Second); ; {
		data, _ := os.ReadFile(marker)
		if string(data) == "persisted" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("detaching killed background job")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := endpoint.Attach(); err != nil {
		t.Fatal(err)
	}
	_, err = endpoint.Write([]byte("printf 'STATE:%s\\n' \"$state\"\r"))
	if err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(3 * time.Second); ; {
		mu.Lock()
		seen := strings.Contains(output.String(), "STATE:kept")
		mu.Unlock()
		if seen {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("reattach lost the original shell state")
		}
		time.Sleep(10 * time.Millisecond)
	}
	command := exec.Command(e.binary, "-S", e.socket, "split-window", "-d", "-h", "-t", "main", "/bin/sh", "-i")
	// Loss of the local tmux client also keeps the server and original shell.
	e.mu.Lock()
	previous := e.client
	e.mu.Unlock()
	if err := previous.command.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(3 * time.Second); ; {
		e.mu.Lock()
		recovered := e.client != nil && e.client != previous
		e.mu.Unlock()
		if recovered {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("tmux client loss ended the session")
		}
		time.Sleep(10 * time.Millisecond)
	}
	command.Env = e.env
	if data, err := command.CombinedOutput(); err != nil {
		t.Fatalf("complete tmux split-window: %s %v", data, err)
	}
	command = exec.Command(e.binary, "-S", e.socket, "list-panes", "-t", "main", "-F", "#{pane_id}")
	command.Env = e.env
	panes, err := command.Output()
	if err != nil || len(strings.Fields(string(panes))) != 2 {
		t.Fatalf("panes %q: %v", panes, err)
	}
	if err := endpoint.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("close did not unblock terminal reader")
	}
	if err := exec.Command(e.binary, "-S", e.socket, "has-session", "-t", "main").Run(); err == nil {
		t.Fatal("explicit close retained the tmux server")
	}
}

func TestBundledTmuxRejectsNonRoot(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("non-root check")
	}
	if _, err := tmuxbundle.Install(t.TempDir()); err == nil {
		t.Fatal("non-root runtime accepted")
	}
}

var _ io.ReadWriteCloser = (*tmuxEndpoint)(nil)

func privateTestState(t *testing.T) string {
	t.Helper()
	directory := t.TempDir()
	if err := os.Chmod(directory, 0700); err != nil {
		t.Fatal(err)
	}
	return directory
}

func TestTmuxCloseInterruptsBlockedClientWrite(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	defer writer.Close()
	exited := make(chan struct{})
	close(exited)
	endpoint := &tmuxEndpoint{client: &ptyClient{file: writer, command: &exec.Cmd{}, waitDone: exited}, changed: make(chan struct{}), binary: filepath.Join(t.TempDir(), "no-tmux-process")}
	written := make(chan error, 1)
	go func() { _, err := endpoint.Write(make([]byte, 1<<20)); written <- err }()
	// Reading one byte confirms the oversized write has started; the unread
	// remainder keeps the pipe full until Close interrupts it.
	var first [1]byte
	if _, err := reader.Read(first[:]); err != nil {
		t.Fatal(err)
	}
	closed := make(chan struct{})
	go func() { _ = endpoint.Close(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(time.Second):
		reader.Close()
		<-closed
		t.Error("tmux mutex prevented client close")
	}
	if err := <-written; err == nil {
		t.Fatal("blocked write unexpectedly completed")
	}
}
