//go:build linux

package rsyncbundle

import (
	"os/exec"
	"strings"
	"testing"
)

func TestInstallProvidesRunnableRsync(t *testing.T) {
	binary, err := Install(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(binary, "--version").Output()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "rsync  version "+Version) {
		t.Fatalf("unexpected version output: %s", out)
	}
}
