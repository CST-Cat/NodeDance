//go:build linux

package terminal

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/CST-Cat/NodeDance/internal/agent/terminal/tmuxbundle"
	"github.com/CST-Cat/NodeDance/internal/protocol"
	"github.com/creack/pty"
	"github.com/moby/moby/client"
)

type SystemProvider struct {
	shell           string
	docker          *client.Client
	mu              sync.Mutex
	directory       string
	socketDirectory string
	stateDirectory  string
}

func NewSystemProvider(shell string, dockerClient *client.Client, stateDirectory string) *SystemProvider {
	shell = strings.TrimSpace(shell)
	if shell == "" {
		shell = strings.TrimSpace(os.Getenv("SHELL"))
	}
	if shell == "" {
		shell = "/bin/sh"
	}
	return &SystemProvider{shell: shell, docker: dockerClient, stateDirectory: stateDirectory}
}

func (p *SystemProvider) Open(ctx context.Context, request protocol.TerminalFrame) (Endpoint, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !filepath.IsAbs(p.shell) || strings.ContainsAny(p.shell, "\x00\r\n") {
		return nil, errors.New("configured host shell must be an absolute executable path")
	}
	info, err := os.Stat(p.shell)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&0111 == 0 {
		return nil, errors.New("configured host shell is unavailable")
	}
	args := []string{p.shell, "-i"}
	if request.TargetKind == protocol.TerminalTargetContainer {
		if p.docker == nil {
			return nil, errors.New("Docker Engine is unavailable")
		}
		inspect, err := p.docker.ContainerInspect(ctx, request.ContainerID, client.ContainerInspectOptions{})
		if err != nil || inspect.Container.ID != request.ContainerID || inspect.Container.State == nil || !inspect.Container.State.Running {
			return nil, errors.New("target container is unavailable or not running")
		}
		// Docker Agent enters the host mount namespace through an open image
		// executable. Its original pathname need not exist on the host. The
		// running process's executable inode is the authoritative child binary.
		executable := fmt.Sprintf("/proc/%d/exe", os.Getpid())
		args = []string{executable, "terminal-container", request.ContainerID}
	} else if request.TargetKind != protocol.TerminalTargetHost {
		return nil, errors.New("unsupported terminal target")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.directory == "" {
		directory, err := tmuxbundle.Install(p.stateDirectory)
		if err != nil {
			return nil, err
		}
		config := "set -g status off\nset -g default-terminal tmux-256color\nset -g history-limit 3000\nset -g destroy-unattached off\n"
		if err := os.WriteFile(filepath.Join(directory, "tmux.conf"), []byte(config), 0600); err != nil {
			_ = os.RemoveAll(directory)
			return nil, err
		}
		socketDirectory, err := os.MkdirTemp("/run", "nodedance-terminal-")
		if err != nil {
			_ = os.RemoveAll(directory)
			return nil, err
		}
		p.socketDirectory = socketDirectory
		p.directory = directory
	}
	return newTmuxEndpoint(ctx, p.directory, p.socketDirectory, terminalEnvironment(p.shell), request, args)
}

// Close is called only after the manager has terminated all its tmux servers.
func (p *SystemProvider) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.directory == "" {
		return nil
	}
	_ = os.RemoveAll(p.socketDirectory)
	err := os.RemoveAll(p.directory)
	p.directory = ""
	return err
}

func terminalEnvironment(shell string) []string {
	values := []string{"TERM=xterm-256color", "SHELL=" + shell}
	for _, key := range []string{"PATH", "HOME", "USER", "LANG", "LC_ALL", "DOCKER_HOST"} {
		if value := os.Getenv(key); value != "" {
			values = append(values, key+"="+value)
		}
	}
	return values
}

type ptyClient struct {
	file      *os.File
	command   *exec.Cmd
	waitDone  chan struct{}
	closeOnce sync.Once
}

func (e *ptyClient) Read(data []byte) (int, error)  { return e.file.Read(data) }
func (e *ptyClient) Write(data []byte) (int, error) { return e.file.Write(data) }

func (e *ptyClient) Resize(rows, columns uint16) error {
	return pty.Setsize(e.file, &pty.Winsize{Rows: rows, Cols: columns})
}

func (e *ptyClient) Close() error {
	var closeErr error
	e.closeOnce.Do(func() {
		if e.command.Process != nil {
			_ = syscall.Kill(-e.command.Process.Pid, syscall.SIGTERM)
		}
		_ = e.file.Close()
		select {
		case <-e.waitDone:
		case <-time.After(500 * time.Millisecond):
			if e.command.Process != nil {
				_ = syscall.Kill(-e.command.Process.Pid, syscall.SIGKILL)
			}
			select {
			case <-e.waitDone:
			case <-time.After(3 * time.Second):
				closeErr = errors.New("tmux client did not stop after terminal close")
			}
		}
	})
	return closeErr
}

func (p *SystemProvider) openContainer(ctx, attachCtx context.Context, request protocol.TerminalFrame) (*containerEndpoint, error) {
	if p.docker == nil {
		return nil, errors.New("Docker Engine is unavailable")
	}
	if len(request.ContainerID) != 64 {
		return nil, errors.New("container target must be a full Docker ID")
	}
	for _, character := range request.ContainerID {
		if !(character >= '0' && character <= '9' || character >= 'a' && character <= 'f') {
			return nil, errors.New("container target must be a full Docker ID")
		}
	}
	cli := p.docker
	inspectCtx, cancelInspect := context.WithTimeout(ctx, 10*time.Second)
	inspected, err := cli.ContainerInspect(inspectCtx, request.ContainerID, client.ContainerInspectOptions{})
	cancelInspect()
	if err != nil || inspected.Container.ID != request.ContainerID || inspected.Container.State == nil || !inspected.Container.State.Running {
		return nil, errors.New("target container is unavailable or not running")
	}
	execResult, err := cli.ExecCreate(ctx, request.ContainerID, client.ExecCreateOptions{
		TTY: true, AttachStdin: true, AttachStdout: true, AttachStderr: true,
		ConsoleSize: client.ConsoleSize{Height: uint(request.Rows), Width: uint(request.Columns)},
		Cmd:         []string{"/bin/sh", "-i"},
	})
	if err != nil || execResult.ID == "" {
		return nil, errors.New("could not create Docker terminal Exec; container may not contain /bin/sh")
	}
	attached, err := cli.ExecAttach(attachCtx, execResult.ID, client.ExecAttachOptions{
		TTY: true, ConsoleSize: client.ConsoleSize{Height: uint(request.Rows), Width: uint(request.Columns)},
	})
	if err != nil || attached.Conn == nil || attached.Reader == nil {
		return nil, errors.New("could not attach Docker terminal Exec")
	}
	endpoint := &containerEndpoint{cli: cli, response: attached, execID: execResult.ID}
	if err := waitForContainerExecStart(ctx, cli, execResult.ID); err != nil {
		_ = endpoint.Close()
		return nil, errors.New("container shell is unavailable or exited before starting")
	}
	return endpoint, nil
}

func waitForContainerExecStart(ctx context.Context, cli *client.Client, execID string) error {
	// Docker can briefly report an Exec as running while the runtime is still
	// resolving its executable. In particular, an Exec for /bin/sh in a
	// shell-less image may transition through Running before exiting with 127.
	// Require a short continuous-running window so callers do not get a ready
	// terminal for a process that has already failed to start.
	const startupGrace = 100 * time.Millisecond
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	var runningSince time.Time
	for {
		inspectCtx, cancel := context.WithTimeout(ctx, 750*time.Millisecond)
		result, err := cli.ExecInspect(inspectCtx, execID, client.ExecInspectOptions{})
		cancel()
		if err != nil {
			return err
		}
		if !result.Running {
			if !runningSince.IsZero() {
				return errors.New("Docker Exec exited during terminal startup")
			}
		} else {
			if runningSince.IsZero() {
				runningSince = time.Now()
			}
			if time.Since(runningSince) >= startupGrace {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return errors.New("Docker Exec exited before the terminal session started")
		case <-ticker.C:
		}
	}
}

type containerEndpoint struct {
	cli       *client.Client
	response  client.ExecAttachResult
	execID    string
	closeOnce sync.Once
}

func (e *containerEndpoint) Read(data []byte) (int, error)  { return e.response.Reader.Read(data) }
func (e *containerEndpoint) Write(data []byte) (int, error) { return e.response.Conn.Write(data) }
func (e *containerEndpoint) Resize(rows, columns uint16) error {
	_, err := e.cli.ExecResize(context.Background(), e.execID, client.ExecResizeOptions{Height: uint(rows), Width: uint(columns)})
	if err != nil {
		return fmt.Errorf("resize Docker terminal: %w", err)
	}
	return nil
}

func (e *containerEndpoint) ExitCode() *int {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	result, err := e.cli.ExecInspect(ctx, e.execID, client.ExecInspectOptions{})
	if err != nil || result.Running {
		return nil
	}
	code := result.ExitCode
	return &code
}

func (e *containerEndpoint) Close() error {
	var closeErr error
	e.closeOnce.Do(func() {
		// Interrupt the foreground process first, then send a fixed exit line to
		// the interactive shell. Closing a hijacked Docker stream alone does not
		// guarantee the Engine terminates an attached Exec process.
		_ = e.response.Conn.SetWriteDeadline(time.Now().Add(time.Second))
		_, _ = e.response.Conn.Write([]byte{3})
		_, _ = e.response.Conn.Write([]byte("exit\r"))
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
			result, inspectErr := e.cli.ExecInspect(ctx, e.execID, client.ExecInspectOptions{})
			cancel()
			if inspectErr == nil && !result.Running {
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		result, inspectErr := e.cli.ExecInspect(ctx, e.execID, client.ExecInspectOptions{})
		cancel()
		e.response.Close()
		if inspectErr != nil {
			closeErr = fmt.Errorf("could not confirm Docker terminal Exec cleanup: %w", inspectErr)
		} else if inspectErr == nil && result.Running {
			closeErr = errors.New("Docker terminal Exec did not exit after terminal close")
		}
	})
	return closeErr
}
