//go:build linux

package terminal

import (
	"context"
	"errors"
	"io"
	"os"
	"os/signal"
	"syscall"

	agentdocker "github.com/CST-Cat/NodeDance/internal/agent/docker"
	"github.com/CST-Cat/NodeDance/internal/protocol"
	"github.com/creack/pty"
	"golang.org/x/term"
)

// RunContainerShell is the fixed, root-only child process placed in a tmux pane.
// It uses the existing Docker API provider; no Docker CLI or container-side
// installation is required. Its stdin/stdout are terminal bytes, never i18n text.
func RunContainerShell(ctx context.Context, id string, stdin, stdout *os.File) error {
	if os.Geteuid() != 0 {
		return errors.New("Agent terminal requires root")
	}
	request := protocol.TerminalFrame{StreamID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Action: protocol.TerminalActionOpen, TargetKind: protocol.TerminalTargetContainer, ContainerID: id, Rows: 24, Columns: 80}
	if err := protocol.ValidateTerminalFrame(request, false); err != nil {
		return err
	}
	engine, err := agentdocker.NewSDKEngine(os.Getenv("DOCKER_HOST"))
	if err != nil {
		return err
	}
	defer engine.Client().Close()
	ctx, stop := signal.NotifyContext(ctx, syscall.SIGTERM, syscall.SIGHUP, os.Interrupt)
	defer stop()
	if size, err := pty.GetsizeFull(stdin); err == nil {
		request.Rows, request.Columns = size.Rows, size.Cols
	}
	provider := &SystemProvider{docker: engine.Client()}
	// Keep the attached stream writable during signal cleanup. Cancelling it
	// first would prevent Close from terminating the Docker Exec shell.
	attachCtx, cancelAttach := context.WithCancel(context.Background())
	defer cancelAttach()
	endpoint, err := provider.openContainer(ctx, attachCtx, request)
	if err != nil {
		return err
	}
	defer endpoint.Close()
	state, err := term.MakeRaw(int(stdin.Fd()))
	if err != nil {
		return err
	}
	defer term.Restore(int(stdin.Fd()), state)
	resize := make(chan os.Signal, 1)
	signal.Notify(resize, syscall.SIGWINCH)
	defer signal.Stop(resize)
	done := make(chan struct{})
	defer close(done)
	go func() {
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				_ = endpoint.Close()
				return
			case <-resize:
				if size, err := pty.GetsizeFull(stdin); err == nil {
					_ = endpoint.Resize(size.Rows, size.Cols)
				}
			}
		}
	}()
	go func() { _, _ = io.Copy(endpoint, stdin) }()
	_, err = io.Copy(stdout, endpoint)
	return err
}
