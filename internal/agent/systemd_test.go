package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"

	agentdocker "github.com/CST-Cat/NodeDance/internal/agent/docker"
	agentfiles "github.com/CST-Cat/NodeDance/internal/agent/files"
)

func TestPrepareSystemdStateRejectsSymlinkPathsWithoutChangingTargets(t *testing.T) {
	serviceUID, serviceGID := os.Geteuid(), os.Getegid()

	t.Run("state directory symlink", func(t *testing.T) {
		root := t.TempDir()
		victim := filepath.Join(root, "victim")
		if err := os.Mkdir(victim, 0o751); err != nil {
			t.Fatal(err)
		}
		victimConfig := filepath.Join(victim, "agent.json")
		if err := os.WriteFile(victimConfig, []byte("unrelated-state\n"), 0o640); err != nil {
			t.Fatal(err)
		}
		stateDir := filepath.Join(root, "nodedance-agent")
		if err := os.Symlink(victim, stateDir); err != nil {
			t.Fatal(err)
		}
		beforeDir := statePathSnapshot(t, victim)
		beforeConfig := statePathSnapshot(t, victimConfig)

		if _, err := prepareSystemdState(filepath.Join(stateDir, "agent.json"), serviceUID, serviceGID); err == nil {
			t.Fatal("state directory symlink was accepted")
		}
		assertStatePathSnapshot(t, victim, beforeDir)
		assertStatePathSnapshot(t, victimConfig, beforeConfig)
	})

	t.Run("config symlink", func(t *testing.T) {
		root := t.TempDir()
		stateDir := filepath.Join(root, "nodedance-agent")
		if err := os.Mkdir(stateDir, 0o755); err != nil {
			t.Fatal(err)
		}
		victim := filepath.Join(root, "unrelated.json")
		if err := os.WriteFile(victim, []byte("unrelated-config\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		configPath := filepath.Join(stateDir, "agent.json")
		if err := os.Symlink(victim, configPath); err != nil {
			t.Fatal(err)
		}
		beforeDir := statePathSnapshot(t, stateDir)
		beforeConfig := statePathSnapshot(t, victim)

		if _, err := prepareSystemdState(configPath, serviceUID, serviceGID); err == nil {
			t.Fatal("config symlink was accepted")
		}
		assertStatePathSnapshot(t, stateDir, beforeDir)
		assertStatePathSnapshot(t, victim, beforeConfig)
	})

	t.Run("config hard link", func(t *testing.T) {
		root := t.TempDir()
		stateDir := filepath.Join(root, "nodedance-agent")
		if err := os.Mkdir(stateDir, 0o755); err != nil {
			t.Fatal(err)
		}
		victim := filepath.Join(root, "unrelated.json")
		if err := os.WriteFile(victim, []byte("unrelated-config\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		configPath := filepath.Join(stateDir, "agent.json")
		if err := os.Link(victim, configPath); err != nil {
			t.Fatal(err)
		}
		beforeDir := statePathSnapshot(t, stateDir)
		beforeConfig := statePathSnapshot(t, victim)

		if _, err := prepareSystemdState(configPath, serviceUID, serviceGID); err == nil || !strings.Contains(err.Error(), "hard links") {
			t.Fatalf("expected hard-linked config rejection, got %v", err)
		}
		assertStatePathSnapshot(t, stateDir, beforeDir)
		assertStatePathSnapshot(t, victim, beforeConfig)
	})
}

func TestPrepareSystemdStateCreatesNewAndPreservesValidConfig(t *testing.T) {
	serviceUID, serviceGID := os.Geteuid(), os.Getegid()
	root := t.TempDir()

	newConfig := filepath.Join(root, "new-state", "agent.json")
	exists, err := prepareSystemdState(newConfig, serviceUID, serviceGID)
	if err != nil || exists {
		t.Fatalf("prepare new state: exists=%v err=%v", exists, err)
	}
	newInfo, err := os.Stat(filepath.Dir(newConfig))
	if err != nil || newInfo.Mode().Perm() != 0o700 {
		t.Fatalf("new state directory is not private: info=%v err=%v", newInfo, err)
	}

	stateDir := filepath.Join(root, "existing-state")
	if err := os.Mkdir(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(stateDir, "agent.json")
	config := Config{Schema: ConfigSchema, Server: "https://core.example", Credential: strings.Repeat("a", 64)}
	if err := SaveConfig(configPath, config, true); err != nil {
		t.Fatalf("write existing Agent config: %v", err)
	}
	original, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(stateDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(configPath, 0o644); err != nil {
		t.Fatal(err)
	}

	exists, err = prepareSystemdState(configPath, serviceUID, serviceGID)
	if err != nil || !exists {
		t.Fatalf("prepare existing state: exists=%v err=%v", exists, err)
	}
	stored, err := os.ReadFile(configPath)
	if err != nil || string(stored) != string(original) {
		t.Fatalf("existing Agent config content changed: err=%v", err)
	}
	stateInfo, err := os.Stat(stateDir)
	if err != nil || stateInfo.Mode().Perm() != 0o700 {
		t.Fatalf("existing state directory was not secured: info=%v err=%v", stateInfo, err)
	}
	configInfo, err := os.Stat(configPath)
	if err != nil || configInfo.Mode().Perm() != 0o600 {
		t.Fatalf("existing config was not secured: info=%v err=%v", configInfo, err)
	}
	if _, err := LoadConfig(configPath); err != nil {
		t.Fatalf("existing Agent identity no longer loads: %v", err)
	}
}

func TestPrepareNewSystemdStateReturnsOpenedIdentityAndRejectsExistingDirectory(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("root state preparation requires a root test process")
	}
	stateDir := filepath.Join(t.TempDir(), "nodedance-agent")
	configPath := filepath.Join(stateDir, "agent.json")
	identity, err := PrepareNewSystemdStateForUser("root", configPath)
	if err != nil {
		t.Fatalf("create fresh Agent state: %v", err)
	}
	info, err := os.Stat(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		t.Fatalf("unexpected state directory stat type %T", info.Sys())
	}
	wantIdentity := fmt.Sprintf("%d:%d", stat.Dev, stat.Ino)
	if identity != wantIdentity {
		t.Fatalf("helper returned identity %q, want descriptor identity %q", identity, wantIdentity)
	}
	if err := os.Chmod(stateDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := PrepareNewSystemdStateForUser("root", configPath); err == nil {
		t.Fatal("fresh-state preparation adopted an existing directory")
	}
	info, err = os.Stat(stateDir)
	if err != nil || info.Mode().Perm() != 0o755 {
		t.Fatalf("rejected adoption changed existing directory metadata: info=%v err=%v", info, err)
	}
}

func TestOpenSystemdStateDirectoryReportsMissingWithoutCreating(t *testing.T) {
	t.Run("nested final component", func(t *testing.T) {
		stateDir := filepath.Join(t.TempDir(), "missing-state")
		assertMissingSystemdStatePath(t, stateDir)
	})
	t.Run("root descriptor is final parent", func(t *testing.T) {
		stateDir := filepath.Join(string(filepath.Separator), ".nodedance-missing-state-"+filepath.Base(t.TempDir()))
		assertMissingSystemdStatePath(t, stateDir)
	})
}

func assertMissingSystemdStatePath(t *testing.T, stateDir string) {
	t.Helper()
	configPath := filepath.Join(stateDir, "agent.json")
	fd, missing, created, err := openSystemdStateDirectoryDetailed(configPath, false)
	if err != nil || !missing || created || fd != -1 {
		if fd >= 0 {
			_ = syscall.Close(fd)
		}
		t.Fatalf("inspect absent state directory: fd=%d missing=%v created=%v err=%v", fd, missing, created, err)
	}
	if _, err := os.Lstat(stateDir); !os.IsNotExist(err) {
		t.Fatalf("read-only missing-path inspection created state: err=%v", err)
	}
}

func TestPrepareSystemdStateSecuresRootCredentials(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("root ownership checks require a root test process")
	}
	stateDir := filepath.Join(t.TempDir(), "root-state")
	if err := os.Mkdir(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(stateDir, "agent.json")
	config := Config{Schema: ConfigSchema, Server: "https://core.example", Credential: strings.Repeat("b", 64)}
	if err := SaveConfig(configPath, config, true); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(stateDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(configPath, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := prepareSystemdState(configPath, 0, 0); err != nil {
		t.Fatalf("prepare root Agent identity: %v", err)
	}
	directoryInfo, err := os.Stat(stateDir)
	if err != nil || directoryInfo.Mode().Perm() != 0o700 {
		t.Fatalf("root Agent state directory is not private: info=%v err=%v", directoryInfo, err)
	}
	configInfo, err := os.Stat(configPath)
	if err != nil || configInfo.Mode().Perm() != 0o600 {
		t.Fatalf("root Agent config is not private: info=%v err=%v", configInfo, err)
	}
	if stat, ok := configInfo.Sys().(*syscall.Stat_t); !ok || stat.Uid != 0 || stat.Gid != 0 {
		t.Fatalf("root Agent config owner is not root: stat=%v", configInfo.Sys())
	}
	if _, err := LoadConfig(configPath); err != nil {
		t.Fatalf("root Agent identity does not load after preparation: %v", err)
	}
}

type systemdStateSnapshot struct {
	mode os.FileMode
	uid  uint32
	gid  uint32
	data string
}

func statePathSnapshot(t *testing.T, path string) systemdStateSnapshot {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		t.Fatalf("unexpected stat type for %s", path)
	}
	snapshot := systemdStateSnapshot{mode: info.Mode(), uid: stat.Uid, gid: stat.Gid}
	if info.Mode().IsRegular() {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		snapshot.data = string(data)
	}
	return snapshot
}

func assertStatePathSnapshot(t *testing.T, path string, want systemdStateSnapshot) {
	t.Helper()
	if got := statePathSnapshot(t, path); got != want {
		t.Fatalf("target changed at %s: got=%+v want=%+v", path, got, want)
	}
}

func TestWriteSystemdUnitRootInstallAndForeignRefusal(t *testing.T) {
	path := filepath.Join(t.TempDir(), AgentUnitName)
	first := renderSystemdUnit("/var/lib/nodedance-agent/agent.json", "/usr/local/bin/nodedance-agent", "/", nil)
	second := renderSystemdUnit("/var/lib/nodedance-agent/agent.json", "/opt/nodedance-agent", "/", nil)

	if err := writeSystemdUnit(path, first); err != nil {
		t.Fatalf("write new root unit: %v", err)
	}
	if err := writeSystemdUnit(path, second); err != nil {
		t.Fatalf("update managed root unit: %v", err)
	}
	written, err := os.ReadFile(path)
	if err != nil || string(written) != second {
		t.Fatalf("managed root unit was not updated: err=%v", err)
	}

	foreign := "[Unit]\nDescription=Unrelated service\n[Service]\nExecStart=/usr/bin/true\n"
	if err := os.WriteFile(path, []byte(foreign), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeSystemdUnit(path, first); err == nil || !strings.Contains(err.Error(), "not marked as NodeDance-managed") {
		t.Fatalf("expected unrelated unit refusal, got %v", err)
	}
	written, err = os.ReadFile(path)
	if err != nil || string(written) != foreign {
		t.Fatalf("foreign unit changed after refusal: err=%v content=%q", err, written)
	}

	unmarked := strings.TrimPrefix(first, "# NodeDanceAgentUnit=1\n")
	if err := os.WriteFile(path, []byte(unmarked), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeSystemdUnit(path, second); err == nil || !strings.Contains(err.Error(), "not marked as NodeDance-managed") {
		t.Fatalf("expected unmarked unit refusal, got %v", err)
	}
	written, err = os.ReadFile(path)
	if err != nil || string(written) != unmarked {
		t.Fatalf("unmarked unit changed after refusal: err=%v content=%q", err, written)
	}
}

func TestSystemdRootOnlyUnitAndServiceIdentity(t *testing.T) {
	rootUnit := renderSystemdUnit("/var/lib/nodedance-agent/agent.json", "/usr/local/bin/nodedance-agent", "/", nil)
	for _, required := range []string{"User=root\n", "Group=root\n", `Environment=NODEDANCE_AGENT_FILE_ROOT="/"`} {
		if !strings.Contains(rootUnit, required) {
			t.Errorf("root unit missing %q", required)
		}
	}
	for _, forbidden := range []string{"NoNewPrivileges=", "ProtectSystem=", "ProtectHome=", "PrivateTmp=", "ReadWritePaths=", "BindPaths="} {
		if strings.Contains(rootUnit, forbidden) {
			t.Errorf("root unit unexpectedly contains sandbox directive %q", forbidden)
		}
	}
	if !isNodeDanceSystemdUnit(rootUnit) {
		t.Fatal("root systemd unit is not recognized as NodeDance-managed")
	}
	rootDisabled := renderSystemdUnit("/var/lib/nodedance-agent/agent.json", "/usr/local/bin/nodedance-agent", "", nil)
	if !strings.Contains(rootDisabled, "Environment=NODEDANCE_AGENT_FILE_ACCESS=disabled\n") {
		t.Fatal("explicit root-mode file access disablement is not represented in the unit")
	}
	if !isNodeDanceSystemdUnit(rootDisabled) {
		t.Fatal("root unit with file access disabled is not recognized as NodeDance-managed")
	}
	if got, err := resolveSystemdServiceName("", "/unused"); err != nil || got != "root" {
		t.Fatalf("omitted systemd identity did not default to root: user=%q err=%v", got, err)
	}
	for _, user := range []string{"nobody", "systemd", "toor"} {
		if _, err := resolveSystemdServiceName(user, "/unused"); err == nil || !strings.Contains(err.Error(), "only supports the root account") {
			t.Errorf("non-root service identity %q was accepted: %v", user, err)
		}
	}
	if err := validateSystemdServiceUID("root", 0); err != nil {
		t.Fatalf("literal root account was rejected: %v", err)
	}
	if err := validateSystemdServiceUID("root-alias", 0); err == nil || !strings.Contains(err.Error(), "literal root") {
		t.Fatalf("UID 0 alias was accepted: %v", err)
	}
	if err := validateSystemdServiceUID("nobody", 65534); err == nil {
		t.Fatal("non-root systemd identity was accepted")
	}
	if fileRoot, err := resolveSystemdFileRoot(SystemdInstallOptions{}, filepath.Join(t.TempDir(), AgentUnitName), "/var/lib/nodedance-agent"); err != nil || fileRoot != "/" {
		t.Fatalf("root-only mode should default to host root: root=%q err=%v", fileRoot, err)
	}
}

func TestInstallSystemdRootServiceConfirmsFreshCoreConnection(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("root Agent installation requires root credentials")
	}
	server, requests := newInstallerIdentityServer(t)
	defer server.Close()
	configPath, unitDir, binaryPath := makeRootInstallFixture(t, server.URL)
	state := &fakeSystemdState{}
	_, err := InstallSystemd(context.Background(), SystemdInstallOptions{
		ConfigPath: configPath, UnitDir: unitDir, BinaryPath: binaryPath, EnableNow: true,
		commandRunner: state.runner(t, ""),
	})
	if err != nil {
		t.Fatalf("install and verify active root service: %v", err)
	}
	unit, err := os.ReadFile(filepath.Join(unitDir, AgentUnitName))
	if err != nil || !strings.Contains(string(unit), "User=root\n") || !strings.Contains(string(unit), "Group=root\n") {
		t.Fatalf("root unit identity missing: err=%v unit=%s", err, unit)
	}
	if !state.active || !state.enabled {
		t.Fatalf("Agent service did not remain active and enabled: %+v", state)
	}
	if count := requests.Load(); count < 2 {
		t.Fatalf("installer did not verify baseline and a fresh Core connection: requests=%d", count)
	}
}

func TestInstallSystemdRootInstallRollsBackConfigAndUnitOnActivationFailure(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("root Agent rollback requires root credentials")
	}
	for _, failure := range []string{"enable", "start"} {
		t.Run(failure, func(t *testing.T) {
			server, _ := newInstallerIdentityServer(t)
			defer server.Close()
			configPath, unitDir, binaryPath := makeRootInstallFixture(t, server.URL)
			unitPath := filepath.Join(unitDir, AgentUnitName)
			if err := os.Chmod(unitPath, 0o640); err != nil {
				t.Fatal(err)
			}
			configBefore := statePathSnapshot(t, configPath)
			dirBefore := statePathSnapshot(t, filepath.Dir(configPath))
			unitBefore := statePathSnapshot(t, unitPath)
			state := &fakeSystemdState{active: true, enabled: true}
			_, installErr := InstallSystemd(context.Background(), SystemdInstallOptions{
				User: "root", ConfigPath: configPath, UnitDir: unitDir, BinaryPath: binaryPath,
				Reload: true, EnableNow: true, commandRunner: state.runner(t, failure),
			})
			if installErr == nil {
				t.Fatal("expected injected systemd activation failure")
			}
			if failure == "enable" && !strings.Contains(installErr.Error(), "enable --now") {
				t.Fatalf("enable error lost its cause: %v", installErr)
			}
			if failure == "start" && !strings.Contains(installErr.Error(), "did not remain active") {
				t.Fatalf("start error was not reported: %v", installErr)
			}
			if got := statePathSnapshot(t, configPath); got != configBefore {
				t.Fatalf("Agent config was not restored after %s failure: got=%+v want=%+v installErr=%v", failure, got, configBefore, installErr)
			}
			assertStatePathSnapshot(t, filepath.Dir(configPath), dirBefore)
			assertStatePathSnapshot(t, unitPath, unitBefore)
			if !state.active || !state.enabled {
				t.Fatalf("old root service state was not restored: %+v", state)
			}
		})
	}
}

func TestInstallSystemdDoesNotStopForeignUnitCreatedAfterPreflight(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("root Agent installation requires root credentials")
	}
	var unitPath string
	foreignUnit := "[Unit]\nDescription=Foreign service created during installation\n[Service]\nExecStart=/usr/bin/true\n"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/agents/identity" || r.Method != http.MethodGet {
			http.Error(w, "unexpected identity request", http.StatusNotFound)
			return
		}
		if err := os.WriteFile(unitPath, []byte(foreignUnit), 0o640); err != nil {
			http.Error(w, "create foreign unit", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"agentId": "11111111-1111-4111-8111-111111111111", "nodeId": "22222222-2222-4222-8222-222222222222",
			"displayName": "fixture", "status": "offline", "credentialState": "active", "generation": 4,
		})
	}))
	defer server.Close()
	configPath, unitDir, binaryPath := makeRootInstallFixture(t, server.URL)
	unitPath = filepath.Join(unitDir, AgentUnitName)
	if err := os.Remove(unitPath); err != nil {
		t.Fatal(err)
	}
	state := &fakeSystemdState{active: true, enabled: true}
	_, installErr := InstallSystemd(context.Background(), SystemdInstallOptions{
		ConfigPath: configPath, UnitDir: unitDir, BinaryPath: binaryPath,
		EnableNow: true, commandRunner: state.runner(t, ""),
	})
	if installErr == nil || !strings.Contains(installErr.Error(), "systemd unit") {
		t.Fatalf("install should reject a unit created after preflight: %v", installErr)
	}
	assertStatePathSnapshot(t, unitPath, systemdUnitSnapshot(t, unitPath, foreignUnit, 0o640))
	if !state.active || !state.enabled {
		t.Fatalf("foreign service state changed after rejected installation: %+v", state)
	}
	if len(state.commands) != 0 {
		t.Fatalf("installer invoked systemctl against a foreign unit: %v", state.commands)
	}
}

func TestInstallSystemdDoesNotEnableUnitReplacedDuringDaemonReload(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("root Agent installation requires root credentials")
	}
	server, _ := newInstallerIdentityServer(t)
	configPath, unitDir, binaryPath := makeRootInstallFixture(t, server.URL)
	unitPath := filepath.Join(unitDir, AgentUnitName)
	if err := os.Remove(unitPath); err != nil {
		t.Fatal(err)
	}
	foreignUnit := "[Unit]\nDescription=Foreign service substituted during daemon-reload\n[Service]\nExecStart=/usr/bin/true\n"
	state := &fakeSystemdState{active: true, enabled: true}
	state.onDaemonReload = func() error {
		if err := os.WriteFile(unitPath, []byte(foreignUnit), 0o640); err != nil {
			return err
		}
		return os.Chmod(unitPath, 0o640)
	}
	_, installErr := InstallSystemd(context.Background(), SystemdInstallOptions{
		ConfigPath: configPath, UnitDir: unitDir, BinaryPath: binaryPath,
		EnableNow: true, commandRunner: state.runner(t, ""),
	})
	if installErr == nil || !strings.Contains(installErr.Error(), "refusing to replace an existing systemd unit") {
		t.Fatalf("install should reject a unit replaced during daemon-reload: %v", installErr)
	}
	assertStatePathSnapshot(t, unitPath, systemdUnitSnapshot(t, unitPath, foreignUnit, 0o640))
	if !state.active || !state.enabled {
		t.Fatalf("foreign service state changed after rejected activation: %+v", state)
	}
	if len(state.commands) != 1 || state.commands[0] != "daemon-reload" {
		t.Fatalf("installer ran service state commands after Unit replacement: %v", state.commands)
	}
}

func systemdUnitSnapshot(t *testing.T, path, contents string, mode os.FileMode) systemdStateSnapshot {
	t.Helper()
	got := statePathSnapshot(t, path)
	if got.data != contents || got.mode.Perm() != mode.Perm() {
		t.Fatalf("unexpected test unit snapshot: got=%+v contents=%q mode=%o", got, got.data, got.mode.Perm())
	}
	return got
}

func makeRootInstallFixture(t *testing.T, serverURL string) (string, string, string) {
	t.Helper()
	directory := t.TempDir()
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	stateDir := filepath.Join(directory, "agent-state")
	if err := os.Mkdir(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(stateDir, "agent.json")
	config := Config{Schema: ConfigSchema, Server: serverURL, Development: true, Credential: strings.Repeat("c", 64),
		AgentID: "11111111-1111-4111-8111-111111111111", NodeID: "22222222-2222-4222-8222-222222222222"}
	if err := SaveConfig(configPath, config, true); err != nil {
		t.Fatal(err)
	}
	unitDir := filepath.Join(directory, "units")
	if err := os.Mkdir(unitDir, 0o700); err != nil {
		t.Fatal(err)
	}
	binaryPath := filepath.Join(directory, "nodedance-agent")
	if err := os.WriteFile(binaryPath, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	previousBinaryPath := filepath.Join(directory, "nodedance-agent-previous")
	if err := os.WriteFile(previousBinaryPath, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	unitPath := filepath.Join(unitDir, AgentUnitName)
	unit := renderSystemdUnit(configPath, previousBinaryPath, "/", nil)
	if err := os.WriteFile(unitPath, []byte(unit), 0o600); err != nil {
		t.Fatal(err)
	}
	return configPath, unitDir, binaryPath
}

type fakeSystemdState struct {
	active         bool
	enabled        bool
	failedEnable   bool
	failedStart    bool
	commands       []string
	onDaemonReload func() error
}

func (s *fakeSystemdState) runner(t *testing.T, failure string) func(context.Context, ...string) ([]byte, error) {
	t.Helper()
	return func(_ context.Context, args ...string) ([]byte, error) {
		if len(args) == 0 {
			return nil, errors.New("missing systemctl command")
		}
		s.commands = append(s.commands, strings.Join(args, " "))
		switch args[0] {
		case "daemon-reload":
			if s.onDaemonReload != nil {
				if err := s.onDaemonReload(); err != nil {
					return nil, err
				}
			}
			return nil, nil
		case "is-active":
			if len(args) > 1 && args[1] == "--quiet" && failure == "start" && !s.failedStart {
				s.failedStart = true
				s.active = false
				return []byte("inactive\n"), errors.New("injected start failure")
			}
			if s.active {
				return []byte("active\n"), nil
			}
			return []byte("inactive\n"), errors.New("inactive")
		case "is-enabled":
			if s.enabled {
				return []byte("enabled\n"), nil
			}
			return []byte("disabled\n"), errors.New("disabled")
		case "enable":
			if len(args) > 1 && args[1] == "--now" {
				s.active, s.enabled = true, true
				if failure == "enable" && !s.failedEnable {
					s.failedEnable = true
					return []byte("injected partial enable failure"), errors.New("injected enable failure")
				}
			} else {
				s.enabled = true
			}
			return nil, nil
		case "disable":
			s.active, s.enabled = false, false
			return nil, nil
		case "start":
			s.active = true
			return nil, nil
		case "stop":
			s.active = false
			return nil, nil
		default:
			return nil, fmt.Errorf("unexpected systemctl command: %v", args)
		}
	}
}

type installerIdentityFixture struct {
	requests atomic.Int32
}

func newInstallerIdentityServer(t *testing.T) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	fixture := &installerIdentityFixture{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/agents/identity" || r.Method != http.MethodGet || r.Header.Get("Authorization") != "Bearer "+strings.Repeat("c", 64) {
			http.Error(w, "unexpected identity request", http.StatusUnauthorized)
			return
		}
		count := fixture.requests.Add(1)
		status, generation := "offline", uint64(4)
		if count > 1 {
			status, generation = "online", 5
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"agentId": "11111111-1111-4111-8111-111111111111", "nodeId": "22222222-2222-4222-8222-222222222222",
			"displayName": "fixture", "status": status, "credentialState": "active", "generation": generation,
		})
	}))
	t.Cleanup(server.Close)
	return server, &fixture.requests
}

func TestSystemdFilePolicyPersistsExplicitRootDisableAndFileRoot(t *testing.T) {
	unitPath := filepath.Join(t.TempDir(), AgentUnitName)
	if err := os.WriteFile(unitPath, []byte(renderSystemdUnit("/var/lib/nodedance-agent/agent.json", "/usr/local/bin/nodedance-agent", "", nil)), 0o600); err != nil {
		t.Fatal(err)
	}
	root, disabled, found, err := readPersistedSystemdFilePolicy(unitPath)
	if err != nil || !found || !disabled || root != "" {
		t.Fatalf("explicit disable policy was not restored: root=%q disabled=%v found=%v err=%v", root, disabled, found, err)
	}
	resolved, err := resolveSystemdFileRoot(SystemdInstallOptions{User: "root"}, unitPath, "/var/lib/nodedance-agent")
	if err != nil || resolved != "" {
		t.Fatalf("reinstall should preserve explicit root-mode disablement: root=%q err=%v", resolved, err)
	}

	rootUnit := renderSystemdUnit("/var/lib/nodedance-agent/agent.json", "/usr/local/bin/nodedance-agent", "/", nil)
	if err := os.WriteFile(unitPath, []byte(rootUnit), 0o600); err != nil {
		t.Fatal(err)
	}
	root, disabled, found, err = readPersistedSystemdFilePolicy(unitPath)
	if err != nil || !found || disabled || root != "/" {
		t.Fatalf("root file policy was not restored: root=%q disabled=%v found=%v err=%v", root, disabled, found, err)
	}

}

func TestOpenAgentFileServiceUsesRootDefaultAndHonorsExplicitDisable(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("Agent file service requires the root-only Agent runtime")
	}
	configPath := filepath.Join(t.TempDir(), "agent-state", "agent.json")
	t.Setenv("NODEDANCE_AGENT_FILE_ROOT", "")
	t.Setenv("NODEDANCE_AGENT_FILE_ACCESS", "")
	service, err := openAgentFileService(configPath)
	if err != nil {
		t.Fatalf("open default Agent file service: %v", err)
	}
	if service == nil {
		t.Fatal("root Agent should default to full-host file access")
	}
	defer service.Close()
	if _, err := service.Stat(filepath.ToSlash(filepath.Join(filepath.Dir(configPath), "agent.json"))); !errors.Is(err, agentfiles.ErrInvalidPath) {
		t.Fatalf("Agent identity path was not protected: %v", err)
	}
	if _, err := service.List("/proc"); !errors.Is(err, agentfiles.ErrInvalidPath) {
		t.Fatalf("kernel proc tree was not protected: %v", err)
	}

	t.Setenv("NODEDANCE_AGENT_FILE_ACCESS", "disabled")
	disabled, err := openAgentFileService(configPath)
	if err != nil || disabled != nil {
		if disabled != nil {
			_ = disabled.Close()
		}
		t.Fatalf("explicit file access disablement was ignored: service=%v err=%v", disabled, err)
	}
}

func TestUninstallSystemdRemovesOnlyManagedRootUnit(t *testing.T) {
	unitDir := t.TempDir()
	unitPath := filepath.Join(unitDir, AgentUnitName)
	unit := renderSystemdUnit("/var/lib/nodedance-agent/agent.json", "/usr/local/bin/nodedance-agent", "/", nil)
	if err := os.WriteFile(unitPath, []byte(unit), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := UninstallSystemd(context.Background(), unitDir); err != nil {
		t.Fatalf("uninstall managed root unit: %v", err)
	}
	if _, err := os.Lstat(unitPath); !os.IsNotExist(err) {
		t.Fatalf("managed root unit remains after uninstall: %v", err)
	}

	unitDir = t.TempDir()
	unitPath = filepath.Join(unitDir, AgentUnitName)
	foreign := "[Unit]\nDescription=Unrelated service\n[Service]\nExecStart=/usr/bin/true\n"
	if err := os.WriteFile(unitPath, []byte(foreign), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := UninstallSystemd(context.Background(), unitDir); err == nil || !strings.Contains(err.Error(), "not marked as NodeDance-managed") {
		t.Fatalf("expected foreign service refusal, got %v", err)
	}
	got, err := os.ReadFile(unitPath)
	if err != nil || string(got) != foreign {
		t.Fatalf("foreign service changed after refusal: content=%q err=%v", got, err)
	}
}

func TestUninstallSystemdDoesNotStopForeignUnitCreatedAfterPrecheck(t *testing.T) {
	unitDir := t.TempDir()
	unitPath := filepath.Join(unitDir, AgentUnitName)
	managedUnit := renderSystemdUnit("/var/lib/nodedance-agent/agent.json", "/usr/local/bin/nodedance-agent", "/", nil)
	if err := os.WriteFile(unitPath, []byte(managedUnit), 0o640); err != nil {
		t.Fatal(err)
	}
	foreignUnit := "[Unit]\nDescription=Foreign service substituted during uninstall\n[Service]\nExecStart=/usr/bin/true\n"
	state := &fakeSystemdState{active: true, enabled: true}
	foreignSnapshot := systemdStateSnapshot{}
	runner := func(_ context.Context, args ...string) ([]byte, error) {
		state.commands = append(state.commands, strings.Join(args, " "))
		if len(args) > 0 && args[0] == "disable" {
			state.active, state.enabled = false, false
		}
		return nil, nil
	}
	err := uninstallSystemd(context.Background(), unitDir, runner, func() error {
		if err := os.WriteFile(unitPath, []byte(foreignUnit), 0o640); err != nil {
			return err
		}
		if err := os.Chmod(unitPath, 0o640); err != nil {
			return err
		}
		foreignSnapshot = systemdUnitSnapshot(t, unitPath, foreignUnit, 0o640)
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "refusing to replace an existing systemd unit") {
		t.Fatalf("uninstall should refuse a unit replaced after precheck: %v", err)
	}
	assertStatePathSnapshot(t, unitPath, foreignSnapshot)
	if !state.active || !state.enabled {
		t.Fatalf("foreign service state changed after rejected uninstall: %+v", state)
	}
	if len(state.commands) != 0 {
		t.Fatalf("uninstaller ran systemctl after Unit replacement: %v", state.commands)
	}
}

func TestOpenTaskBridgeKeepsBasicTasksWhenRebuildStoreIsUnavailable(t *testing.T) {
	directory := t.TempDir()
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(directory, "rebuilds.sqlite"), 0o700); err != nil {
		t.Fatal(err)
	}
	sharedDocker, err := agentdocker.NewSDKEngine("")
	if err != nil {
		t.Fatalf("create shared Docker client: %v", err)
	}
	defer sharedDocker.Close()

	bridge, err := openTaskBridge(context.Background(), filepath.Join(directory, "agent.json"), "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", sharedDocker, nil)
	if err != nil {
		t.Fatalf("rebuild storage failure must not disable the task bridge: %v", err)
	}
	if bridge == nil || bridge.runner == nil {
		t.Fatal("basic container task runner was not initialized")
	}
	if bridge.rebuild != nil || bridge.store != nil {
		t.Fatal("unavailable rebuild storage must not leave a partial rebuild capability")
	}
	if err := bridge.close(); err != nil {
		t.Fatalf("close task bridge: %v", err)
	}
}

func TestRenderSystemdUnitSupplementaryGroupsUseSystemdFieldSyntax(t *testing.T) {
	unit := renderSystemdUnit("/var/lib/nodedance-agent/agent.json", "/usr/local/bin/nodedance-agent", "", []string{"docker", "123"})
	if !strings.Contains(unit, "SupplementaryGroups=docker 123\n") {
		t.Fatalf("SupplementaryGroups must use space-separated unquoted names/GIDs, got unit line:\n%s", unit)
	}
	if strings.Contains(unit, `SupplementaryGroups="docker"`) || strings.Contains(unit, `SupplementaryGroups="123"`) {
		t.Fatalf("SupplementaryGroups values must not be quoted, got unit line:\n%s", unit)
	}
	for _, invalid := range []string{"docker group", "docker/extra", "docker;touch /tmp/unsafe"} {
		if validSupplementaryGroup(invalid) {
			t.Errorf("invalid supplementary group %q was accepted", invalid)
		}
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
