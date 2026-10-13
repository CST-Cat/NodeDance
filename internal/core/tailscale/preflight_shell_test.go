package tailscale

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestDockerPreflightWithoutDaemonIsValidShell(t *testing.T) {
	remote := &stage6Remote{preflight: "Linux\nx86_64\n0\nnew\n-\nroot\nunit|absent|-\ndocker|absent\n"}
	if _, err := InspectRemote(context.Background(), remote); err != nil {
		t.Fatal(err)
	}
	script := remote.commands[0]
	start := strings.LastIndex(script, "if [ -S /var/run/docker.sock ]")
	if start < 0 {
		t.Fatal("Docker preflight missing")
	}
	socket := filepath.Join(t.TempDir(), "docker.sock")
	for _, kind := range []string{"absent", "invalid"} {
		if kind == "invalid" {
			if err := os.WriteFile(socket, []byte("fixture"), 0600); err != nil {
				t.Fatal(err)
			}
		}
		command := exec.Command("sh", "-s")
		command.Stdin = strings.NewReader("set -eu\n" + strings.ReplaceAll(script[start:], "/var/run/docker.sock", socket))
		output, err := command.CombinedOutput()
		if err != nil || strings.TrimSpace(string(output)) != "docker|"+kind {
			t.Fatal(kind, string(output), err)
		}
	}
}
