package protocol

import (
	"strings"
	"testing"
)

func TestValidateTerminalFrameScopesTargetsAndBounds(t *testing.T) {
	validOpen := TerminalFrame{StreamID: "abcdefghijklmnopqrstuvwxyz0123456789-_", Action: TerminalActionOpen, TargetKind: TerminalTargetHost, Rows: 24, Columns: 80}
	if err := ValidateTerminalFrame(validOpen, false); err != nil {
		t.Fatalf("valid host open: %v", err)
	}
	invalid := []TerminalFrame{
		{StreamID: validOpen.StreamID, Action: TerminalActionOpen, TargetKind: TerminalTargetHost, ContainerID: strings.Repeat("a", 64), Rows: 24, Columns: 80},
		{StreamID: validOpen.StreamID, Action: TerminalActionOpen, TargetKind: TerminalTargetContainer, ContainerID: "short", Rows: 24, Columns: 80},
		{StreamID: validOpen.StreamID, Action: TerminalActionInput, Data: make([]byte, MaxTerminalDataBytes+1)},
		{StreamID: validOpen.StreamID, Action: TerminalActionResize, Rows: MaxTerminalRows + 1, Columns: 80},
		{StreamID: validOpen.StreamID, Action: "exec", Message: "rm -rf /"},
		{StreamID: strings.Repeat("/", 40), Action: TerminalActionClose},
	}
	for index, frame := range invalid {
		if err := ValidateTerminalFrame(frame, false); err == nil {
			t.Errorf("invalid browser frame %d was accepted: %+v", index, frame)
		}
	}
}

func TestAgentTerminalFramesAreOutputOnly(t *testing.T) {
	valid := TerminalFrame{StreamID: "abcdefghijklmnopqrstuvwxyz0123456789DIND", Action: TerminalActionOutput, Data: []byte("hello")}
	if err := ValidateTerminalFrame(valid, true); err != nil {
		t.Fatalf("valid output frame: %v", err)
	}
	if err := ValidateTerminalFrame(TerminalFrame{StreamID: valid.StreamID, Action: TerminalActionInput, Data: []byte("secret")}, true); err == nil {
		t.Fatal("Agent input frame was accepted as an output")
	}
}
