//go:build linux

package terminal

import (
	"context"
	"errors"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/CST-Cat/NodeDance/internal/protocol"
)

func TestRealHostPTYUnicodeResizeInterruptAndReap(t *testing.T) {
	shell := "/bin/sh"
	if configured := os.Getenv("SHELL"); configured != "" {
		if info, err := os.Stat(configured); err == nil && info.Mode().IsRegular() && info.Mode()&0o111 != 0 {
			shell = configured
		}
	}
	provider := NewSystemProvider(shell, "unix:///var/run/docker.sock")
	endpoint, err := provider.Open(context.Background(), protocol.TerminalFrame{
		StreamID: "abcdefghijklmnopqrstuvwxyz0123456789PTY", Action: protocol.TerminalActionOpen,
		TargetKind: protocol.TerminalTargetHost, Rows: 24, Columns: 80,
	})
	if err != nil {
		t.Fatal(err)
	}
	processID := endpoint.(*hostEndpoint).command.Process.Pid
	output := make(chan string, 1)
	readErrors := make(chan error, 1)
	go func() {
		var collected strings.Builder
		buffer := make([]byte, 4096)
		for {
			count, readErr := endpoint.Read(buffer)
			if count > 0 {
				collected.Write(buffer[:count])
				select {
				case output <- collected.String():
				default:
					select {
					case <-output:
					default:
					}
					select {
					case output <- collected.String():
					default:
					}
				}
			}
			if readErr != nil {
				readErrors <- readErr
				return
			}
		}
	}()

	waitFor := func(needle string) {
		t.Helper()
		deadline := time.NewTimer(5 * time.Second)
		defer deadline.Stop()
		for {
			select {
			case text := <-output:
				if strings.Contains(text, needle) {
					return
				}
			case readErr := <-readErrors:
				t.Fatalf("PTY ended before %q: %v", needle, readErr)
			case <-deadline.C:
				t.Fatalf("PTY output did not contain %q", needle)
			}
		}
	}

	if _, err := endpoint.Write([]byte("printf 'ND_UTF8_你好\\n'\r")); err != nil {
		t.Fatal(err)
	}
	waitFor("ND_UTF8_你好")
	if err := endpoint.Resize(42, 111); err != nil {
		t.Fatal(err)
	}
	if _, err := endpoint.Write([]byte("stty size\r")); err != nil {
		t.Fatal(err)
	}
	waitFor("42 111")
	if _, err := endpoint.Write([]byte("sleep 20\r")); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	if _, err := endpoint.Write([]byte{3}); err != nil {
		t.Fatal(err)
	}
	waitFor("^C")
	if _, err := endpoint.Write([]byte("printf 'ND_AFTER_INTERRUPT\\n'\r")); err != nil {
		t.Fatal(err)
	}
	waitFor("ND_AFTER_INTERRUPT")
	if err := endpoint.Close(); err != nil {
		t.Fatal(err)
	}
	if code := endpoint.ExitCode(); code == nil {
		t.Fatal("host shell exit code was not collected")
	}
	if err := syscall.Kill(processID, 0); !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("PTY shell process %d remains after close: %v", processID, err)
	}
	select {
	case <-readErrors: // Linux PTYs can report either EOF or EIO when the slave closes.
	case <-time.After(time.Second):
		t.Fatal("PTY reader remained blocked after close")
	}
}
