//go:build linux

package terminal

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/CST-Cat/NodeDance/internal/protocol"
)

func TestBundledTmuxHonorsClientColorAndLinkCapabilities(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("root-only Agent integration")
	}
	t.Setenv("COLORTERM", "truecolor")
	for _, depth := range []uint8{0, 4, 8, 24} {
		t.Run(fmt.Sprint(depth), func(t *testing.T) {
			provider := NewSystemProvider("/bin/sh", nil, privateTestState(t))
			defer provider.Close()
			capabilities := protocol.TerminalCapabilities{ColorDepth: depth, Hyperlinks: depth == 24}
			endpoint, err := provider.Open(context.Background(), protocol.TerminalFrame{
				StreamID: strings.Repeat("c", 32), Action: protocol.TerminalActionOpen,
				TargetKind: protocol.TerminalTargetHost, Rows: 24, Columns: 100, Capabilities: capabilities,
			})
			if err != nil {
				t.Fatal(err)
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
			defer func() { endpoint.Close(); <-done }()
			command := "stty -echo; printf '\\033[38;2;17;149;231mFG_TRUE\\033[0m\\n\\033[48;2;29;47;61mBG_TRUE\\033[0m\\n'; printf '\\033]8;;https://example.org/terminal\\033\\\\LINK_TRUE\\033]8;;\\033\\\\\\n'; printf 'ENV:%s:%s\\n' \"$TERM\" \"$COLORTERM\"; printf 'COLOR_DONE\\n'\r"
			if _, err := endpoint.Write([]byte(command)); err != nil {
				t.Fatal(err)
			}
			var raw string
			for deadline := time.Now().Add(5 * time.Second); ; {
				mu.Lock()
				raw = output.String()
				mu.Unlock()
				if strings.Contains(raw, "ENV:"+terminalPaneType(capabilities)+":") && strings.Contains(raw, "COLOR_DONE") {
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("shell did not finish: %q", raw)
				}
				time.Sleep(10 * time.Millisecond)
			}
			trueColor := regexp.MustCompile("\x1b\\[[0-9;]*38;2;17;149;231m").MatchString(raw) && regexp.MustCompile("\x1b\\[[0-9;]*48;2;29;47;61m").MatchString(raw)
			if trueColor != (depth == 24) {
				t.Fatalf("color depth %d, truecolor=%t: %q", depth, trueColor, raw)
			}
			if strings.Contains(raw, "\x1b]8;") != capabilities.Hyperlinks {
				t.Fatalf("OSC 8 capability lost: %q", raw)
			}
			// tmux always sets COLORTERM=truecolor inside panes
			// (environ_for_session in tmux 3.7c); it renders or
			// down-converts for the outer client itself, so the pane
			// value is depth-independent by design. The capability
			// contract is the pane TERM, which must follow the depth.
			envPattern := regexp.MustCompile("ENV:" + regexp.QuoteMeta(terminalPaneType(capabilities)) + ":truecolor(\\x1b|\r\n)")
			if !envPattern.MatchString(raw) {
				t.Fatalf("Agent environment leaked or lost capabilities: %q", raw)
			}
		})
	}
}
