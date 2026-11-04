//go:build linux

package terminal

import (
	"fmt"
	"strings"

	"github.com/CST-Cat/NodeDance/internal/protocol"
)

func terminalPaneType(capabilities protocol.TerminalCapabilities) string {
	switch capabilities.ColorDepth {
	case 0:
		return "dumb"
	case 4:
		return "tmux"
	default:
		return "tmux-256color"
	}
}

func terminalColorEnvironment(capabilities protocol.TerminalCapabilities, termType string) []string {
	colorTerm := ""
	if capabilities.ColorDepth == 24 {
		colorTerm = "truecolor"
	}
	return []string{"TERM=" + termType, "COLORTERM=" + colorTerm}
}

func terminalTmuxConfig(capabilities protocol.TerminalCapabilities) string {
	features := []string{}
	limits := []string{"*"}
	if capabilities.ColorDepth == 24 {
		features = append(features, "RGB")
	} else {
		limits = append(limits, "RGB@", "Tc@", "setrgbf@", "setrgbb@")
	}
	if capabilities.Hyperlinks {
		features = append(features, "hyperlinks")
	} else {
		limits = append(limits, "Hls@")
	}
	if capabilities.ColorDepth < 8 {
		colors := 0
		if capabilities.ColorDepth == 4 {
			colors = 16
		}
		limits = append(limits, fmt.Sprintf("colors=%d", colors))
		if colors == 0 {
			limits = append(limits, "setaf@", "setab@")
		}
	}
	featureValue := ""
	if len(features) != 0 {
		featureValue = "*:" + strings.Join(features, ":")
	}
	return "set -g status off\nset -g history-limit 3000\nset -g destroy-unattached off\n" +
		"set -g default-terminal " + terminalPaneType(capabilities) + "\n" +
		"set -g terminal-features '" + featureValue + "'\n" +
		"set -g terminal-overrides '" + strings.Join(limits, ":") + "'\n"
}
