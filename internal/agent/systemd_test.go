package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteSystemdUnitCreateReinstallAndForeignRefusal(t *testing.T) {
	path := filepath.Join(t.TempDir(), AgentUnitName)
	first := renderSystemdUnit("nodedance-agent", "1001", "/var/lib/nodedance-agent/agent.json", "/usr/local/bin/nodedance-agent", "", nil)
	second := renderSystemdUnit("nodedance-agent", "1001", "/var/lib/nodedance-agent/agent.json", "/opt/nodedance-agent", "", nil)

	t.Run("creates absent unit", func(t *testing.T) {
		if err := writeSystemdUnit(path, first); err != nil {
			t.Fatalf("write new unit: %v", err)
		}
		written, err := os.ReadFile(path)
		if err != nil || string(written) != first {
			t.Fatalf("new unit content mismatch: err=%v", err)
		}
	})

	t.Run("reinstalls managed unit", func(t *testing.T) {
		if err := writeSystemdUnit(path, second); err != nil {
			t.Fatalf("reinstall managed unit: %v", err)
		}
		written, err := os.ReadFile(path)
		if err != nil || string(written) != second {
			t.Fatalf("managed unit was not updated: err=%v", err)
		}
	})

	t.Run("preserves unrelated regular unit", func(t *testing.T) {
		foreign := "[Unit]\nDescription=Unrelated service\n[Service]\nExecStart=/usr/bin/true\n"
		if err := os.WriteFile(path, []byte(foreign), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := writeSystemdUnit(path, first); err == nil || !strings.Contains(err.Error(), "not marked as NodeDance-managed") {
			t.Fatalf("expected unrelated unit refusal, got %v", err)
		}
		written, err := os.ReadFile(path)
		if err != nil || string(written) != foreign {
			t.Fatalf("foreign unit changed after refusal: err=%v content=%q", err, written)
		}
	})

	t.Run("refuses an unmarked NodeDance-shaped unit", func(t *testing.T) {
		unmarked := strings.TrimPrefix(first, "# NodeDanceAgentUnit=1\n")
		if err := os.WriteFile(path, []byte(unmarked), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := writeSystemdUnit(path, second); err == nil || !strings.Contains(err.Error(), "not marked as NodeDance-managed") {
			t.Fatalf("expected unmarked unit refusal, got %v", err)
		}
		written, err := os.ReadFile(path)
		if err != nil || string(written) != unmarked {
			t.Fatalf("unmarked unit changed after refusal: err=%v content=%q", err, written)
		}
	})
}

func TestInstallSystemdRejectsRootServiceUser(t *testing.T) {
	if _, err := InstallSystemd(context.Background(), SystemdInstallOptions{User: "root", UnitDir: t.TempDir()}); err == nil || !strings.Contains(err.Error(), "non-root UID") {
		t.Fatalf("systemd installation must reject UID 0 before creating a unit, got %v", err)
	}
}

func TestRefuseSystemdUnitShadowRejectsVendorFragment(t *testing.T) {
	directory := t.TempDir()
	systemctl := filepath.Join(directory, "systemctl")
	script := "#!/bin/sh\nprintf '%s\\n' '/usr/lib/systemd/system/nodedance-agent.service'\n"
	if err := os.WriteFile(systemctl, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", directory)
	err := refuseSystemdUnitShadow(context.Background(), "/etc/systemd/system/nodedance-agent.service")
	if err == nil || !strings.Contains(err.Error(), "/usr/lib/systemd/system/nodedance-agent.service") {
		t.Fatalf("expected vendor unit shadow refusal, got %v", err)
	}
}

func TestRefuseSystemdUnitShadowFailsClosedWhenEffectiveStateIsUnknown(t *testing.T) {
	for _, testCase := range []struct {
		name           string
		fragment       string
		fragmentStatus string
		loadState      string
		loadStatus     string
		wantError      string
	}{
		{name: "fragment query fails", fragmentStatus: "1", wantError: "inspect effective systemd Agent unit"},
		{name: "empty fragment but unit is not not-found", loadState: "loaded", wantError: "without proving"},
		{name: "empty fragment and load state query fails", loadState: "not-found", loadStatus: "1", wantError: "confirm absent"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			directory := t.TempDir()
			systemctl := filepath.Join(directory, "systemctl")
			script := `#!/bin/sh
case "$2" in
  --property=FragmentPath) printf '%s\n' "$FRAGMENT"; exit "${FRAGMENT_STATUS:-0}" ;;
  --property=LoadState) printf '%s\n' "$LOAD_STATE"; exit "${LOAD_STATUS:-0}" ;;
esac
exit 2
`
			if err := os.WriteFile(systemctl, []byte(script), 0o755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", directory)
			t.Setenv("FRAGMENT", testCase.fragment)
			t.Setenv("FRAGMENT_STATUS", testCase.fragmentStatus)
			t.Setenv("LOAD_STATE", testCase.loadState)
			t.Setenv("LOAD_STATUS", testCase.loadStatus)
			err := refuseSystemdUnitShadow(context.Background(), "/etc/systemd/system/nodedance-agent.service")
			if err == nil || !strings.Contains(err.Error(), testCase.wantError) {
				t.Fatalf("unknown effective systemd state should be rejected, got %v", err)
			}
		})
	}
}

func TestRefuseSystemdUnitShadowAcceptsProvenAbsentUnit(t *testing.T) {
	for _, path := range []string{
		"/run/systemd/system/nodedance-agent.service",
		"/usr/local/lib/systemd/system/nodedance-agent.service",
		"/usr/lib/systemd/system/nodedance-agent.service",
		"/lib/systemd/system/nodedance-agent.service",
	} {
		if _, err := os.Lstat(path); err == nil || !os.IsNotExist(err) {
			t.Skipf("cannot isolate absent-unit case because %s exists or cannot be inspected", path)
		}
	}
	directory := t.TempDir()
	systemctl := filepath.Join(directory, "systemctl")
	script := `#!/bin/sh
case "$2" in
  --property=FragmentPath) printf '\n'; exit 0 ;;
  --property=LoadState) printf 'not-found\n'; exit 0 ;;
esac
exit 2
`
	if err := os.WriteFile(systemctl, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", directory)
	if err := refuseSystemdUnitShadow(context.Background(), "/etc/systemd/system/nodedance-agent.service"); err != nil {
		t.Fatalf("proven absent unit should be installable: %v", err)
	}
}

func TestWriteSystemdInstallResultCreatesPrivateOneShotFingerprint(t *testing.T) {
	directory := t.TempDir()
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "installed-unit.sha256")
	sum := sha256.Sum256([]byte("unit contents"))
	digest := hex.EncodeToString(sum[:])
	if err := writeSystemdInstallResult(path, digest); err != nil {
		t.Fatalf("write systemd install fingerprint: %v", err)
	}
	contents, err := os.ReadFile(path)
	if err != nil || string(contents) != digest+"\n" {
		t.Fatalf("unexpected systemd install fingerprint: err=%v contents=%q", err, contents)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("fingerprint file must be mode 0600: err=%v mode=%v", err, info.Mode())
	}
	if err := writeSystemdInstallResult(path, digest); err == nil {
		t.Fatal("fingerprint helper must not overwrite an existing one-shot result")
	}
}
