//go:build linux

package terminal

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/CST-Cat/NodeDance/internal/protocol"
	"github.com/creack/pty"
)

// Each managed terminal gets a private server. Closing it kills all windows and
// panes, while detaching kills only the client, leaving the shell with tmux.
type tmuxEndpoint struct {
	mu             sync.Mutex
	client         *ptyClient
	changed        chan struct{}
	closed         bool
	binary, socket string
	env            []string
	rows, columns  uint16
}

func newTmuxEndpoint(ctx context.Context, directory, socketDirectory string, env []string, request protocol.TerminalFrame, args []string) (*tmuxEndpoint, error) {
	// The complete executable is available inside the managed host shell too.
	// tmux's own TMUX environment selects this session's dedicated socket.
	filtered := make([]string, 0, len(env)+1)
	for _, value := range env {
		if !strings.HasPrefix(value, "PATH=") {
			filtered = append(filtered, value)
		}
	}
	path := os.Getenv("PATH")
	if path == "" {
		path = "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"
	}
	env = append(filtered, "PATH="+directory+":"+path)
	digest := sha256.Sum256([]byte(request.StreamID))
	e := &tmuxEndpoint{binary: filepath.Join(directory, "tmux"), socket: filepath.Join(socketDirectory, fmt.Sprintf("s-%x", digest[:12])), env: append(env, "TERMINFO="+filepath.Join(directory, "assets/terminfo")), changed: make(chan struct{}), rows: request.Rows, columns: request.Columns}
	command := exec.CommandContext(ctx, e.binary, "-f", filepath.Join(directory, "tmux.conf"), "-S", e.socket, "new-session", "-d", "-s", "main", "-x", strconv.Itoa(int(e.columns)), "-y", strconv.Itoa(int(e.rows)), "--")
	command.Args = append(command.Args, args...)
	command.Env = e.env
	if output, err := command.CombinedOutput(); err != nil {
		_ = e.Close()
		return nil, fmt.Errorf("could not create bundled tmux session: %s: %w", output, err)
	}
	// New windows/panes retain the selected target, including Docker terminals.
	quoted := make([]string, len(args))
	for index, value := range args {
		quoted[index] = "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
	}
	options := [][]string{{"default-command", "exec " + strings.Join(quoted, " ")}}
	if request.TargetKind == protocol.TerminalTargetContainer {
		options = append(options, []string{"default-shell", "/bin/sh"})
	}
	for _, option := range options {
		command := exec.CommandContext(ctx, e.binary, "-S", e.socket, "set-option", "-g", option[0], option[1])
		command.Env = e.env
		if err := command.Run(); err != nil {
			_ = e.Close()
			return nil, errors.New("could not configure bundled tmux target")
		}
	}
	if err := e.Attach(); err != nil {
		_ = e.Close()
		return nil, err
	}
	return e, nil
}

func (e *tmuxEndpoint) Attach() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.attachLocked()
}

func (e *tmuxEndpoint) attachLocked() error {
	if e.closed {
		return errors.New("terminal is closed")
	}
	if e.client != nil {
		return errors.New("terminal is already attached")
	}
	command := exec.Command(e.binary, "-S", e.socket, "attach-session", "-t", "main")
	command.Env = e.env
	file, err := pty.StartWithSize(command, &pty.Winsize{Rows: e.rows, Cols: e.columns})
	if err != nil {
		return errors.New("could not attach bundled tmux session")
	}
	client := &ptyClient{file: file, command: command, waitDone: make(chan struct{})}
	go func() { _ = command.Wait(); close(client.waitDone) }()

	e.client = client
	close(e.changed)
	e.changed = make(chan struct{})
	return nil
}

func (e *tmuxEndpoint) Detach() error {
	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		return nil
	}
	client := e.client
	e.client = nil
	close(e.changed)
	e.changed = make(chan struct{})
	e.mu.Unlock()
	if client != nil {
		return client.Close()
	}
	return nil
}

func (e *tmuxEndpoint) Read(data []byte) (int, error) {
	for {
		e.mu.Lock()
		client, changed, closed := e.client, e.changed, e.closed
		e.mu.Unlock()
		if closed {
			return 0, errors.New("terminal is closed")
		}
		if client == nil {
			<-changed
			continue
		}
		n, err := client.Read(data)
		e.mu.Lock()
		current := e.client == client
		e.mu.Unlock()
		if current {
			if err != nil && e.recoverClient(client) {
				continue
			}
			return n, err
		}
	}
}

// A tmux client can exit independently of its shell (including Ctrl-b d).
// Restore the presentation while the managed server still has a session.
func (e *tmuxEndpoint) recoverClient(client *ptyClient) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed || e.client != client {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, e.binary, "-S", e.socket, "has-session", "-t", "main")
	command.Env = e.env
	if command.Run() != nil {
		return false
	}
	e.client = nil
	_ = client.Close()
	return e.attachLocked() == nil
}
func (e *tmuxEndpoint) Write(data []byte) (int, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.client == nil {
		return 0, errors.New("terminal is detached")
	}
	return e.client.Write(data)
}
func (e *tmuxEndpoint) Resize(rows, columns uint16) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.rows, e.columns = rows, columns
	if e.client == nil {
		return nil
	}
	return e.client.Resize(rows, columns)
}

// The tmux client exit status is not the shell exit status. Report closure
// without fabricating a shell code after tmux has removed the final pane.
func (e *tmuxEndpoint) ExitCode() *int { return nil }

func (e *tmuxEndpoint) Close() error {
	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		return nil
	}
	e.closed = true
	client := e.client
	e.client = nil
	close(e.changed)
	e.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, e.binary, "-S", e.socket, "kill-server")
	command.Env = e.env
	// An exited shell may already have removed its server.
	_ = command.Run()
	if client != nil {
		return client.Close()
	}
	return nil
}
