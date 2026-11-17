//go:build linux

package terminal

import (
	"bytes"
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
	"syscall"
	"time"

	"github.com/CST-Cat/NodeDance/internal/protocol"
	"github.com/creack/pty"
)

type tmuxEndpoint struct {
	mu                     sync.Mutex
	client                 *ptyClient
	changed                chan struct{}
	closed                 bool
	closeOnce              sync.Once
	closeErr               error
	server                 *trackedProcess
	release                func()
	binary, socket, config string
	env                    []string
	rows, columns          uint16
}

func newTmuxEndpoint(ctx context.Context, directory, socketDirectory string, env []string, request protocol.TerminalFrame, args []string) (*tmuxEndpoint, error) {
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
	e.config = e.socket + ".conf"
	if err := os.WriteFile(e.config, []byte(terminalTmuxConfig(request.Capabilities)), 0600); err != nil {
		return nil, err
	}
	command := exec.CommandContext(ctx, e.binary, "-f", e.config, "-S", e.socket, "new-session", "-d", "-s", "main", "-x", strconv.Itoa(int(e.columns)), "-y", strconv.Itoa(int(e.rows)), "--")
	command.Args = append(command.Args, args...)
	command.Env = e.env
	if output, err := command.CombinedOutput(); err != nil {
		_ = e.Close()
		return nil, fmt.Errorf("could not create bundled tmux session: %s: %w", output, err)
	}
	if err := e.track(ctx); err != nil {
		_ = e.Close()
		return nil, fmt.Errorf("could not identify bundled tmux server: %w", err)
	}
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
	client := e.client
	e.mu.Unlock()
	if client == nil {
		return 0, errors.New("terminal is detached")
	}
	return client.Write(data)
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

func (e *tmuxEndpoint) ExitCode() *int { return nil }

func (e *tmuxEndpoint) Close() error {
	e.closeOnce.Do(func() { e.closeErr = e.terminate() })
	return e.closeErr
}

func (e *tmuxEndpoint) terminate() error {
	e.mu.Lock()
	e.closed = true
	client := e.client
	e.client = nil
	close(e.changed)
	e.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if e.server == nil {
		_ = e.track(ctx)
	}
	processes := e.panes(ctx)
	if e.server != nil {
		processes = append(processes, e.server)
	}
	command := exec.CommandContext(ctx, e.binary, "-S", e.socket, "kill-server")
	command.Env = e.env
	if command.Run() != nil && e.server != nil {
		e.server.kill()
	}
	graceful, _ := ctx.Deadline()
	survivors := awaitExit(processes, graceful)
	for _, process := range survivors {
		process.kill()
	}
	survivors = awaitExit(survivors, time.Now().Add(2*time.Second))
	stopped := e.server != nil && !e.server.running()
	if e.server == nil {
		stopped = e.vacant()
	}
	for _, process := range processes {
		process.close()
	}
	if stopped {
		_ = os.Remove(e.socket)
	}
	_ = os.Remove(e.config)
	var err error
	if len(survivors) > 0 {
		err = errors.New("bundled tmux session processes did not stop after terminal close")
	}
	if client != nil {
		err = errors.Join(err, client.Close())
	}
	if e.release != nil {
		e.release()
	}
	return err
}

func (e *tmuxEndpoint) vacant() bool {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	probe := exec.CommandContext(ctx, e.binary, "-S", e.socket, "has-session", "-t", "main")
	probe.Env = e.env
	var exit *exec.ExitError
	return errors.As(probe.Run(), &exit) && exit.ExitCode() == 1
}

func (e *tmuxEndpoint) track(ctx context.Context) error {
	command := exec.CommandContext(ctx, e.binary, "-S", e.socket, "display-message", "-p", "#{pid}")
	command.Env = e.env
	output, err := command.Output()
	if err != nil {
		return err
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(output)))
	if err != nil {
		return err
	}
	server, err := openProcess(pid)
	if err != nil {
		return err
	}
	bundled, err := os.Stat(e.binary)
	if err == nil {
		var running os.FileInfo
		if running, err = os.Stat(fmt.Sprintf("/proc/%d/exe", pid)); err == nil && !os.SameFile(bundled, running) {
			err = errors.New("tmux server is not the bundled binary")
		}
	}
	if err != nil {
		server.close()
		return err
	}
	e.server = server
	return nil
}

func (e *tmuxEndpoint) panes(ctx context.Context) []*trackedProcess {
	if e.server == nil {
		return nil
	}
	command := exec.CommandContext(ctx, e.binary, "-S", e.socket, "list-panes", "-a", "-F", "#{pane_pid}")
	command.Env = e.env
	output, err := command.Output()
	if err != nil {
		return nil
	}
	var found []*trackedProcess
	for _, field := range strings.Fields(string(output)) {
		pid, err := strconv.Atoi(field)
		if err != nil {
			continue
		}
		process, err := openProcess(pid)
		if err != nil {
			continue
		}
		if _, parent, _ := process.status(); parent != e.server.pid {
			process.close()
			continue
		}
		found = append(found, process)
	}
	return found
}

const (
	processStatState  = 0
	processStatParent = 1
	processStatStart  = 19
)

type trackedProcess struct {
	pid    int
	start  string
	handle *os.Process
}

func openProcess(pid int) (*trackedProcess, error) {
	handle, err := os.FindProcess(pid)
	if err != nil {
		return nil, err
	}
	process := &trackedProcess{pid: pid, handle: handle}
	state, _, start := process.status()
	if state == "" {
		process.close()
		return nil, os.ErrProcessDone
	}
	process.start = start
	return process, nil
}

func (p *trackedProcess) status() (state string, parent int, start string) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", p.pid))
	if err != nil {
		return "", 0, ""
	}
	fields := strings.Fields(string(data[bytes.LastIndexByte(data, ')')+1:]))
	if len(fields) <= processStatStart {
		return "", 0, ""
	}
	parent, _ = strconv.Atoi(fields[processStatParent])
	return fields[processStatState], parent, fields[processStatStart]
}

func (p *trackedProcess) running() bool {
	if p.handle.Signal(syscall.Signal(0)) != nil {
		return false
	}
	state, _, start := p.status()
	return state != "" && state != "Z" && state != "X" && start == p.start
}

func (p *trackedProcess) kill() {
	if p.running() {
		_ = p.handle.Kill()
	}
}

func (p *trackedProcess) close() {
	_ = p.handle.Release()
}

func awaitExit(processes []*trackedProcess, deadline time.Time) []*trackedProcess {
	for {
		var survivors []*trackedProcess
		for _, process := range processes {
			if process.running() {
				survivors = append(survivors, process)
			}
		}
		if len(survivors) == 0 || !time.Now().Before(deadline) {
			return survivors
		}
		processes = survivors
		time.Sleep(terminalPollInterval)
	}
}
