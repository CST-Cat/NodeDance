package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/CST-Cat/NodeDance/internal/i18n"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/CST-Cat/NodeDance/internal/control"
	"github.com/CST-Cat/NodeDance/internal/protocol"
	"golang.org/x/sys/unix"
	"golang.org/x/term"
)

func interactiveTerminal(ctx context.Context, client *control.Client, in control.Input, tty *os.File, output io.Writer) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var body map[string]json.RawMessage
	if err := json.Unmarshal(in.Body, &body); err != nil {
		return err
	}
	if body == nil {
		return errors.New("invalid terminal request")
	}
	if _, declared := body["capabilities"]; !declared {
		body["capabilities"], _ = json.Marshal(localTerminalCapabilities(ctx))
		in.Body, _ = json.Marshal(body)
	}
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

func localTerminalCapabilities(ctx context.Context) protocol.TerminalCapabilities {
	capabilities := protocol.TerminalCapabilities{}
	if termType := os.Getenv("TERM"); termType == "" || termType == "dumb" {
		return capabilities
	}
	probe := func(name string) ([]byte, error) {
		probeCtx, cancel := context.WithTimeout(ctx, time.Second)
		defer cancel()
		return exec.CommandContext(probeCtx, "tput", name).Output()
	}
	output, _ := probe("colors")
	colors, _ := strconv.Atoi(strings.TrimSpace(string(output)))
	switch {
	case colors >= 1<<24:
		capabilities.ColorDepth = 24
	case colors >= 256:
		capabilities.ColorDepth = 8
	case colors >= 8:
		capabilities.ColorDepth = 4
	}
	switch strings.ToLower(os.Getenv("COLORTERM")) {
	case "truecolor", "24bit":
		capabilities.ColorDepth = 24
	default:
		if _, err := probe("RGB"); err == nil {
			capabilities.ColorDepth = 24
		} else if _, err := probe("Tc"); err == nil {
			capabilities.ColorDepth = 24
		}
	}
	output, err := probe("Hls")
	capabilities.Hyperlinks = err == nil && len(output) != 0
	return capabilities
}
