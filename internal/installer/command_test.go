package installer

import (
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"

	"github.com/CST-Cat/NodeDance"
)

func TestNativeInstallerPreservesFailureAndIncludesEnrollment(t *testing.T) {
	token := strings.Repeat("a", 64)
	command, err := Command("https://core.example", token, false)
	if err != nil {
		t.Fatal(err)
	}
	fixture := `curl() { printf '%s\n' 'fixture'; }; sudo() { [[ "$1" == bash ]] || { printf 'UNEXPECTED_DOCKER'; return 1; }; cat >/dev/null; return "$FIXTURE_INSTALL_STATUS"; }; export -f curl sudo; `
	for _, status := range []int{0, 23} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			cmd := exec.Command("bash", "-c", fixture+command)
			cmd.Env = append(os.Environ(), "FIXTURE_INSTALL_STATUS="+strconv.Itoa(status))
			output, err := cmd.CombinedOutput()
			if len(output) != 0 {
				t.Fatalf("unexpected installer output: %s", output)
			}
			if status == 0 && err != nil {
				t.Fatal(err)
			}
			if status != 0 {
				if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != status {
					t.Fatalf("installer failure was hidden: %v", err)
				}
			}
		})
	}
	failure := `curl() { return 22; }; sudo() { cat >/dev/null; return 0; }; export -f curl sudo; `
	if _, err := exec.Command("bash", "-c", failure+command).CombinedOutput(); err == nil {
		t.Fatal("download failure was hidden")
	}
}

func TestCommandsPinPairedAgentAndDeliverLiteralToken(t *testing.T) {
	token := strings.Repeat("b", 64)
	tag := strings.TrimSpace(nodedance.PairedAgentRelease)
	for _, mode := range []string{"native", "docker"} {
		render := Command
		if mode == "docker" {
			render = DockerCommand
		}
		command, err := render("https://core.example", token, false)
		if err != nil {
			t.Fatal(err)
		}
		if len(command) > 600 {
			t.Fatalf("installer command is unnecessarily long: %d", len(command))
		}
		fixture := `curl() { printf '%s\n' 'fixture'; }; sudo() { cat >/dev/null; printf '%s\n' "$@"; }; export -f curl sudo; `
		raw, err := exec.Command("bash", "-c", fixture+command).CombinedOutput()
		if err != nil {
			t.Fatalf("mock installer: %s %v", raw, err)
		}
		for _, required := range []string{tag, "--mode\n" + mode, "--server\nhttps://core.example", "--token\n" + token} {
			if !strings.Contains(string(raw), required) {
				t.Fatalf("command lost %q: %s", required, raw)
			}
		}
		if !strings.Contains(command, "/releases/download/"+tag+"/install-agent.sh") || strings.Contains(command, "releases/latest") || strings.Contains(command, "api.github.com") || strings.Contains(command, "/dev/tty") {
			t.Fatal("command must directly use its published paired Agent installer")
		}
	}
}

func TestCommandsRejectUnsafeTokensAndURLs(t *testing.T) {
	token := strings.Repeat("c", 64)
	for _, render := range []func(string, string, bool) (string, error){Command, DockerCommand} {
		for _, server := range []string{"http://example.com", "http://localhost:8180", "https://u:p@example.com", "https://example.com/path", "https://example.com?token=secret"} {
			if _, err := render(server, token, true); err == nil {
				t.Fatalf("accepted %s", server)
			}
		}
		for _, value := range []string{"", strings.Repeat("a", 63), strings.Repeat("a", 65), "$(touch /tmp/never-execute)", strings.Repeat("a", 63) + "'"} {
			if _, err := render("https://core.example", value, false); err == nil {
				t.Fatal("accepted invalid token")
			}
		}
		command, err := render("http://[::1]:8180", token, true)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(command, "--dev") {
			t.Fatal("dev flag missing")
		}
		if raw, err := exec.Command("bash", "-n", "-c", command).CombinedOutput(); err != nil {
			t.Fatalf("invalid shell quoting: %s %v", raw, err)
		}
	}
}
