package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/CST-Cat/NodeDance/internal/i18n"
	"io"
	"os"

	"github.com/CST-Cat/NodeDance/internal/control"
	"github.com/CST-Cat/NodeDance/internal/protocol"
	"golang.org/x/sys/unix"
	"golang.org/x/term"
)

func interactiveTerminal(ctx context.Context, client *control.Client, in control.Input, tty *os.File, output io.Writer) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	dimensions := func() (uint16, uint16) {
		w, h, err := term.GetSize(int(tty.Fd()))
		if err != nil {
			return 24, 80
		}
		return uint16(min(500, max(1, h))), uint16(min(500, max(1, w)))
	}
	rows, cols := dimensions()
	conn, streamID, err := client.OpenTerminal(ctx, in, rows, cols)
	if err != nil {
		return err
	}
	defer conn.CloseNow()
	fmt.Fprint(output, i18n.TextFor(output, "\r\nConnected. Ctrl-] returns to NodeDance.\r\n"))
	keyboardDone := make(chan error, 1)
	go func() {
		var keyboardErr error
		defer func() { keyboardDone <- keyboardErr; cancel() }()
		fds := []unix.PollFd{{Fd: int32(tty.Fd()), Events: unix.POLLIN}}
		buf := make([]byte, 4096)
		for ctx.Err() == nil {
			n, err := unix.Poll(fds, 100)
			if err == unix.EINTR {
				continue
			}
			if err != nil {
				keyboardErr = err
				return
			}
			nextRows, nextCols := dimensions()
			if nextRows != rows || nextCols != cols {
				rows, cols = nextRows, nextCols
				if err = control.SendTerminal(ctx, conn, protocol.TerminalFrame{StreamID: streamID, Action: protocol.TerminalActionResize, Rows: rows, Columns: cols}); err != nil {
					keyboardErr = err
					return
				}
			}
			if n == 0 {
				continue
			}
			if fds[0].Revents&(unix.POLLERR|unix.POLLHUP) != 0 {
				keyboardErr = io.EOF
				return
			}
			n, err = tty.Read(buf)
			if err != nil {
				keyboardErr = err
				return
			}
			for i, b := range buf[:n] {
				if b == 29 {
					if i > 0 {
						control.SendTerminal(ctx, conn, protocol.TerminalFrame{StreamID: streamID, Action: protocol.TerminalActionInput, Data: buf[:i]})
					}
					control.SendTerminal(ctx, conn, protocol.TerminalFrame{StreamID: streamID, Action: protocol.TerminalActionClose})
					return
				}
			}
			if n > 0 {
				if err = control.SendTerminal(ctx, conn, protocol.TerminalFrame{StreamID: streamID, Action: protocol.TerminalActionInput, Data: buf[:n]}); err != nil {
					keyboardErr = err
					return
				}
			}
		}
	}()
	defer func() { cancel(); <-keyboardDone }()
	for {
		_, raw, err := conn.Read(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return errors.New("terminal connection closed")
		}
		var message struct {
			Frame protocol.TerminalFrame `json:"frame"`
		}
		if json.Unmarshal(raw, &message) != nil {
			return errors.New("invalid terminal message")
		}
		switch message.Frame.Action {
		case protocol.TerminalActionOutput:
			if _, err = output.Write(message.Frame.Data); err != nil {
				return err
			}
		case protocol.TerminalActionError:
			return errors.New(message.Frame.Message)
		case protocol.TerminalActionExit, protocol.TerminalActionClosed:
			return nil
		}
	}
}
