package cli

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/CST-Cat/NodeDance/internal/protocol"
)

func TestLocalTerminalCapabilitiesUseDeclarationsAndTerminfo(t *testing.T) {
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "tput"), []byte("#!/bin/sh\ncase \"$1\" in\n colors) printf '%s\\n' \"$TEST_COLORS\";;\n RGB|Tc) test \"$TEST_RGB\" = 1;;\n Hls) test \"$TEST_LINKS\" = 1 && printf 'OSC8';;\n *) exit 1;;\nesac\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", directory)
	for _, test := range []struct {
		name, term, colorTerm, colors, rgb, links string
		want                                      protocol.TerminalCapabilities
	}{
		{"unknown", "", "truecolor", "256", "1", "1", protocol.TerminalCapabilities{}},
		{"monochrome", "dumb", "truecolor", "256", "1", "1", protocol.TerminalCapabilities{}},
		{"ansi", "local", "", "8", "", "", protocol.TerminalCapabilities{ColorDepth: 4}},
		{"256", "local", "", "256", "", "", protocol.TerminalCapabilities{ColorDepth: 8}},
		{"declared", "local", "truecolor", "256", "", "", protocol.TerminalCapabilities{ColorDepth: 24}},
		{"terminfo", "local", "", "256", "1", "1", protocol.TerminalCapabilities{ColorDepth: 24, Hyperlinks: true}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("TERM", test.term)
			t.Setenv("COLORTERM", test.colorTerm)
			t.Setenv("TEST_COLORS", test.colors)
			t.Setenv("TEST_RGB", test.rgb)
			t.Setenv("TEST_LINKS", test.links)
			if got := localTerminalCapabilities(context.Background()); got != test.want {
				t.Fatalf("got %+v, want %+v", got, test.want)
			}
		})
	}
	t.Setenv("PATH", filepath.Join(directory, "missing"))
	t.Setenv("COLORTERM", "")
	t.Setenv("TERM", "local")
	if got := localTerminalCapabilities(context.Background()); got != (protocol.TerminalCapabilities{}) {
		t.Fatalf("missing terminfo tools fabricated capabilities: %+v", got)
	}
}
