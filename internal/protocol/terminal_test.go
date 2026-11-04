package protocol

import (
	"strings"
	"testing"
)

func TestTerminalCapabilitiesAreBoundedAndOpenOnly(t *testing.T) {
	frame := TerminalFrame{StreamID: strings.Repeat("a", 32), Action: TerminalActionOpen, TargetKind: TerminalTargetHost}
	for _, depth := range []uint8{0, 4, 8, 24} {
		frame.Capabilities = TerminalCapabilities{ColorDepth: depth, Hyperlinks: true}
		if err := ValidateTerminalFrame(frame, false); err != nil {
			t.Fatalf("color depth %d: %v", depth, err)
		}
	}
	frame.Capabilities.ColorDepth = 16
	if err := ValidateTerminalFrame(frame, false); err == nil {
		t.Fatal("invalid color depth accepted")
	}
	frame.Capabilities.ColorDepth = 24
	for _, action := range []string{TerminalActionInput, TerminalActionResize, TerminalActionAttach, TerminalActionDetach, TerminalActionReady, TerminalActionOutput} {
		frame.Action = action
		frame.TargetKind = ""
		frame.Data = []byte("x")
		frame.Rows, frame.Columns = 24, 80
		if err := ValidateTerminalFrame(frame, action == TerminalActionReady || action == TerminalActionOutput); err == nil {
			t.Fatalf("%s changed session capabilities", action)
		}
	}
}
