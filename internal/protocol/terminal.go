package protocol

import (
	"errors"
	"strings"
	"unicode/utf8"
)

const (
	TerminalActionOpen   = "open"
	TerminalActionReady  = "ready"
	TerminalActionInput  = "input"
	TerminalActionOutput = "output"
	TerminalActionResize = "resize"
	TerminalActionClose  = "close"
	TerminalActionClosed = "closed"
	TerminalActionError  = "error"
	TerminalActionExit   = "exit"

	TerminalTargetHost      = "host"
	TerminalTargetContainer = "container"

	// Terminal frame data is small enough for the bounded Agent writer queues,
	// yet large enough to make interactive Unicode paste practical.
	MaxTerminalDataBytes = 24 << 10
	MaxTerminalFrameJSON = 40 << 10
	MaxTerminalRows      = 500
	MaxTerminalColumns   = 500
)

// TerminalFrame is a deliberately small interactive terminal contract. Open
// selects only a fixed host shell or Docker Exec; no shell command is accepted.
type TerminalFrame struct {
	StreamID    string `json:"streamId"`
	Action      string `json:"action"`
	TargetKind  string `json:"targetKind,omitempty"`
	ContainerID string `json:"containerId,omitempty"`
	Data        []byte `json:"data,omitempty"`
	Rows        uint16 `json:"rows,omitempty"`
	Columns     uint16 `json:"columns,omitempty"`
	ExitCode    *int   `json:"exitCode,omitempty"`
	Message     string `json:"message,omitempty"`
}

func ValidateTerminalFrame(frame TerminalFrame, fromAgent bool) error {
	if len(frame.StreamID) < 32 || len(frame.StreamID) > 64 || strings.TrimSpace(frame.StreamID) != frame.StreamID {
		return errors.New("terminal stream ID is invalid")
	}
	for _, character := range frame.StreamID {
		if !(character >= 'A' && character <= 'Z' || character >= 'a' && character <= 'z' || character >= '0' && character <= '9' || character == '-' || character == '_') {
			return errors.New("terminal stream ID is invalid")
		}
	}
	if len(frame.Data) > MaxTerminalDataBytes || len(frame.Message) > 512 || !utf8.ValidString(frame.Message) {
		return errors.New("terminal frame exceeds its bounds")
	}
	if frame.Rows > MaxTerminalRows || frame.Columns > MaxTerminalColumns {
		return errors.New("terminal dimensions exceed their bounds")
	}
	if fromAgent {
		switch frame.Action {
		case TerminalActionReady:
			if frame.TargetKind != "" || frame.ContainerID != "" || len(frame.Data) != 0 || frame.Message != "" || frame.ExitCode != nil {
				return errors.New("invalid terminal ready frame")
			}
		case TerminalActionOutput:
			if len(frame.Data) == 0 || frame.TargetKind != "" || frame.ContainerID != "" || frame.Message != "" || frame.ExitCode != nil {
				return errors.New("invalid terminal output frame")
			}
		case TerminalActionError:
			if frame.Message == "" || frame.Data != nil || frame.ExitCode != nil {
				return errors.New("invalid terminal error frame")
			}
		case TerminalActionClosed:
			if frame.Message != "" || frame.Data != nil || frame.ExitCode != nil {
				return errors.New("invalid terminal close frame")
			}
		case TerminalActionExit:
			if frame.ExitCode == nil || frame.Data != nil || frame.Message != "" {
				return errors.New("invalid terminal exit frame")
			}
		default:
			return errors.New("unsupported Agent terminal action")
		}
		return nil
	}
	switch frame.Action {
	case TerminalActionOpen:
		if frame.TargetKind != TerminalTargetHost && frame.TargetKind != TerminalTargetContainer {
			return errors.New("terminal target kind is invalid")
		}
		if frame.TargetKind == TerminalTargetHost && frame.ContainerID != "" || frame.TargetKind == TerminalTargetContainer && !validFullContainerID(frame.ContainerID) {
			return errors.New("terminal target ID is invalid")
		}
		if len(frame.Data) != 0 || frame.Message != "" || frame.ExitCode != nil {
			return errors.New("invalid terminal open frame")
		}
	case TerminalActionInput:
		if len(frame.Data) == 0 || frame.TargetKind != "" || frame.ContainerID != "" || frame.Message != "" || frame.ExitCode != nil {
			return errors.New("invalid terminal input frame")
		}
	case TerminalActionResize:
		if frame.Rows == 0 || frame.Columns == 0 || len(frame.Data) != 0 || frame.Message != "" || frame.ExitCode != nil {
			return errors.New("invalid terminal resize frame")
		}
	case TerminalActionClose:
		if len(frame.Data) != 0 || frame.Message != "" || frame.ExitCode != nil {
			return errors.New("invalid terminal close frame")
		}
	default:
		return errors.New("unsupported browser terminal action")
	}
	return nil
}

func validFullContainerID(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, character := range value {
		if !(character >= '0' && character <= '9' || character >= 'a' && character <= 'f') {
			return false
		}
	}
	return true
}
