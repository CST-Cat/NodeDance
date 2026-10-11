package installer

import (
	"os/exec"
	"strings"
	"testing"
)

func TestInstallerCommandQuotingAndTransport(t *testing.T) {
	for _, server := range []string{"http://example.com", "http://localhost:8180", "https://u:p@example.com", "https://example.com/path", "https://example.com?token=secret"} {
		if _, err := Command(server, "node", true); err == nil {
			t.Fatalf("accepted %s", server)
		}
	}
	for _, test := range []struct {
		server, name string
		dev          bool
	}{{"https://example.com", "node' $(touch /tmp/never-execute) `false`", false}, {"http://[::1]:8180", "local", true}} {
		command, err := Command(test.server, test.name, test.dev)
		if err != nil {
			t.Fatal(err)
		}
		// Parse only: this must never execute curl or installer commands.
		if raw, err := exec.Command("bash", "-n", "-c", command).CombinedOutput(); err != nil {
			t.Fatalf("invalid quoting: %s %v", raw, err)
		}
		if !strings.Contains(command, "--release-tag") || strings.Contains(command, "releases/latest") || !strings.Contains(command, "agent-v[0-9]+") {
			t.Fatal("Agent release pin missing")
		}
		if test.dev && !strings.Contains(command, "--dev") {
			t.Fatal("dev flag missing")
		}
	}
}

func TestInstallerPinsTagAndPreservesLiteralName(t *testing.T) {
	name := "node' $(printf INJECTION) `printf INJECTION`"
	command, err := Command("https://core.example", name, false)
	if err != nil {
		t.Fatal(err)
	}
	// Export harmless stand-ins into the inner bash. Never access the network,
	// invoke sudo, or execute a downloaded script. Drain stdin like bash -s
	// so the mock does not race the producer into SIGPIPE with pipefail.
	fixture := `curl() { case "$*" in *api.github.com*) printf '%s\n' '[' '{"tag_name":"core-v9.0.0"},' '{"tag_name":"agent-v1.2.3"}' ']';; *) printf '%s\n' 'fixture';; esac; }; sudo() { cat >/dev/null; printf '%s\n' "$@"; }; export -f curl sudo; `
	raw, err := exec.Command("bash", "-c", fixture+command).CombinedOutput()
	if err != nil {
		t.Fatalf("mock installer: %s %v", raw, err)
	}
	if !strings.Contains(string(raw), "agent-v1.2.3") || !strings.Contains(string(raw), name) || strings.Contains(string(raw), "core-v9.0.0") {
		t.Fatalf("wrong pinned tag or shell escaping: %s", raw)
	}
	empty := `curl() { printf '%s\n' '[]'; }; sudo() { printf 'SUDO_INVOKED'; }; export -f curl sudo; `
	raw, err = exec.Command("bash", "-c", empty+command).CombinedOutput()
	if err == nil || strings.Contains(string(raw), "SUDO_INVOKED") {
		t.Fatalf("missing Agent release did not stop before sudo: %s", raw)
	}
}

func TestDockerInstallerCommandPinsImageAndProtectsToken(t *testing.T) {
	for _, server := range []string{"https://core.example", "http://[::1]:8180"} {
		command, err := DockerCommand(server, "node' $(false)", true)
		if err != nil {
			t.Fatal(err)
		}
		if raw, err := exec.Command("bash", "-n", "-c", command).CombinedOutput(); err != nil {
			t.Fatalf("invalid Docker command: %s %v", raw, err)
		}
		for _, required := range []string{"docker pull", "docker run", "--privileged", "--pid=host", "--network=host", "--token-stdin", "/dev/tty", "ghcr.io/cst-cat/nodedance-agent:$tag", "unset token", "0700"} {
			if !strings.Contains(command, required) {
				t.Fatalf("Docker command is missing %q", required)
			}
		}
		if strings.Contains(command, "releases/latest") || strings.Contains(command, "--token ") {
			t.Fatal("Docker installation must pin an Agent release and keep its token off argv")
		}
	}
	if _, err := DockerCommand("http://100.111.176.125", "node", false); err == nil {
		t.Fatal("Docker command accepted insecure enrollment transport")
	}
}
