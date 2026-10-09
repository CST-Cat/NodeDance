package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestSystemdInstallRequiresExplicitUserAndPrivateConfig(t *testing.T) {
	base := t.TempDir()
	stateDir := filepath.Join(base, "state $ % \" private")
	configPath := filepath.Join(stateDir, "agent.json")
	config := Config{Schema: ConfigSchema, Server: "https://core.example", Credential: strings.Repeat("a", 64),
		AgentID: "11234567-89ab-4cde-8fab-0123456789ab", NodeID: "21234567-89ab-4cde-8fab-0123456789ab"}
	if err := SaveConfig(configPath, config, true); err != nil {
		t.Fatal(err)
	}
	serviceUser, err := user.LookupId(strconv.Itoa(os.Geteuid()))
	if err != nil {
		t.Fatal(err)
	}
	unitDir := filepath.Join(base, "units")
	if err := os.Mkdir(unitDir, 0o700); err != nil {
		t.Fatal(err)
	}
	binaryPath := filepath.Join(base, "binary $ % \" path", "nodedance-agent")
	if err := os.Mkdir(filepath.Dir(binaryPath), 0o700); err != nil {
		t.Fatal(err)
	}
	binary, err := os.ReadFile(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(binaryPath, binary, 0o700); err != nil {
		t.Fatal(err)
	}
	options := SystemdInstallOptions{ConfigPath: configPath, UnitDir: unitDir, BinaryPath: binaryPath}
	if _, err := InstallSystemd(context.Background(), options); err == nil {
		t.Fatal("systemd installation guessed a service user")
	}
	options.User = serviceUser.Username
	unitPath, err := InstallSystemd(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	unit, err := os.ReadFile(unitPath)
	if err != nil {
		t.Fatal(err)
	}
	text := string(unit)
	helperPath := filepath.Join(stateDir, "bin", "nodedance-agent-helper")
	for _, required := range []string{"User=" + serviceUser.Username, "UMask=0077", "update-helper supervise", "Restart=always",
		systemdQuote(helperPath), systemdQuote(configPath)} {
		if !strings.Contains(text, required) {
			t.Errorf("unit missing %q:\n%s", required, text)
		}
	}
	for _, forbidden := range []string{"StateDirectory=", "NoNewPrivileges=", "ProtectSystem=", "ProtectHome=", "ReadWritePaths=", "MemoryDenyWriteExecute="} {
		if strings.Contains(text, forbidden) {
			t.Errorf("unit unexpectedly restricts the selected OS user's operations with %q", forbidden)
		}
	}
	if strings.Contains(text, strings.Repeat("a", 64)) {
		t.Fatal("systemd unit leaked the Agent credential")
	}
}

func TestSystemdInstallRejectsConfigOwnedByDifferentUser(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("different-user ownership test requires root in Linux CI")
	}
	base := t.TempDir()
	state := filepath.Join(base, "state")
	if err := os.Mkdir(state, 0o700); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(state, "agent.json")
	if err := os.WriteFile(configPath, []byte(`{"schema":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chown(state, 65534, 65534); err != nil {
		t.Fatal(err)
	}
	if err := os.Chown(configPath, 65534, 65534); err != nil {
		t.Fatal(err)
	}
	unitDir := filepath.Join(base, "units")
	if err := os.Mkdir(unitDir, 0o700); err != nil {
		t.Fatal(err)
	}
	options := SystemdInstallOptions{User: "root", ConfigPath: configPath, UnitDir: unitDir, BinaryPath: os.Args[0]}
	if _, err := InstallSystemd(context.Background(), options); err == nil {
		t.Fatal("systemd install accepted credentials owned by another user")
	}
}

func TestSystemdUnitParsesWithSystemdAnalyze(t *testing.T) {
	analyzer, err := exec.LookPath("systemd-analyze")
	if err != nil {
		t.Skip("systemd-analyze is not installed")
	}
	base := t.TempDir()
	stateDir := filepath.Join(base, "state $ % \" private")
	configPath := filepath.Join(stateDir, "agent.json")
	config := Config{Schema: ConfigSchema, Server: "https://core.example", Credential: strings.Repeat("b", 64),
		AgentID: "11234567-89ab-4cde-8fab-0123456789ab", NodeID: "21234567-89ab-4cde-8fab-0123456789ab"}
	if err := SaveConfig(configPath, config, true); err != nil {
		t.Fatal(err)
	}
	unitDir := filepath.Join(base, "units")
	if err := os.Mkdir(unitDir, 0o700); err != nil {
		t.Fatal(err)
	}
	unitPath := filepath.Join(unitDir, "nodedance-agent-quote-test.service")
	binaryDir := filepath.Join(base, "binary $ % \" path")
	if err := os.Mkdir(binaryDir, 0o700); err != nil {
		t.Fatal(err)
	}
	binaryPath := filepath.Join(binaryDir, "agent")
	binary, err := os.ReadFile(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(binaryPath, binary, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := writeSystemdUnit(unitPath, renderSystemdUnit("root", "0", configPath, binaryPath)); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, analyzer, "verify", unitPath).CombinedOutput()
	if err != nil {
		t.Fatalf("systemd-analyze rejected generated unit: %s", strings.TrimSpace(string(output)))
	}
}

func TestSystemdExecStartUsesLiteralSpecialPathsOnRunningManager(t *testing.T) {
	if os.Getenv("NODEDANCE_TEST_SYSTEMD") != "1" {
		t.Skip("actual system manager execution is enabled only in the isolated Linux CI job")
	}
	if os.Geteuid() != 0 {
		t.Fatal("NODEDANCE_TEST_SYSTEMD requires root in the disposable CI runner")
	}
	systemctl, err := exec.LookPath("systemctl")
	if err != nil {
		t.Fatal("systemctl is required for the actual manager test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if output, err := exec.CommandContext(ctx, systemctl, "show", "-p", "Version", "--value").CombinedOutput(); err != nil || strings.TrimSpace(string(output)) == "" {
		t.Fatalf("system manager is unavailable: %s", strings.TrimSpace(string(output)))
	}

	serviceUser, err := user.Lookup("nobody")
	if err != nil {
		t.Fatal("nobody account is required for isolated systemd unit test")
	}
	serviceUID, err := strconv.Atoi(serviceUser.Uid)
	if err != nil {
		t.Fatal("invalid nobody UID")
	}
	serviceGID, err := strconv.Atoi(serviceUser.Gid)
	if err != nil {
		t.Fatal("invalid nobody GID")
	}
	base, err := os.MkdirTemp("/var/tmp", "nodedance-systemd-s02-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(base)
	stateDir := filepath.Join(base, "state $ % \" private")
	if err := os.Mkdir(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(stateDir, "agent.json")
	if err := SaveConfig(configPath, Config{Schema: ConfigSchema, Server: "https://core.example", Credential: strings.Repeat("c", 64),
		AgentID: "11234567-89ab-4cde-8fab-0123456789ab", NodeID: "21234567-89ab-4cde-8fab-0123456789ab"}, true); err != nil {
		t.Fatal(err)
	}
	outputPath := filepath.Join(base, "captured arguments.json")
	binaryDir := filepath.Join(base, "binary $ % \" path")
	if err := os.Mkdir(binaryDir, 0o755); err != nil {
		t.Fatal(err)
	}
	binaryPath := filepath.Join(binaryDir, "probe")
	script := fmt.Sprintf("#!/usr/bin/python3\nimport json,sys,time\nwith open(%s, 'w') as out: json.dump(sys.argv[1:], out)\ntime.sleep(30)\n", pythonString(outputPath))
	if err := os.WriteFile(binaryPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chown(base, serviceUID, serviceGID); err != nil {
		t.Fatal(err)
	}
	if err := os.Chown(stateDir, serviceUID, serviceGID); err != nil {
		t.Fatal(err)
	}
	if err := os.Chown(configPath, serviceUID, serviceGID); err != nil {
		t.Fatal(err)
	}
	if err := os.Chown(binaryDir, serviceUID, serviceGID); err != nil {
		t.Fatal(err)
	}
	if err := os.Chown(binaryPath, serviceUID, serviceGID); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(base, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := loadConfigForUID(configPath, serviceUID); err != nil {
		t.Fatalf("test service-user config is not valid: %v", err)
	}
	unitName := fmt.Sprintf("nodedance-agent-s02-%d.service", os.Getpid())
	unitPath := filepath.Join("/run/systemd/system", unitName)
	if _, err := os.Lstat(unitPath); !os.IsNotExist(err) {
		t.Fatalf("unique temporary unit path already exists: %v", err)
	}
	unit := renderSystemdUnit(serviceUser.Username, serviceUser.Gid, configPath, binaryPath)
	if err := writeSystemdUnit(unitPath, unit); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = exec.Command(systemctl, "stop", unitName).Run()
		_ = os.Remove(unitPath)
		_ = exec.Command(systemctl, "daemon-reload").Run()
		_ = exec.Command(systemctl, "reset-failed", unitName).Run()
	}()
	if output, err := exec.CommandContext(ctx, systemctl, "daemon-reload").CombinedOutput(); err != nil {
		t.Fatalf("systemd daemon-reload failed: %s", strings.TrimSpace(string(output)))
	}
	if output, err := exec.CommandContext(ctx, systemctl, "start", unitName).CombinedOutput(); err != nil {
		t.Fatalf("systemd did not start generated Agent unit: %s", strings.TrimSpace(string(output)))
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(outputPath); err == nil {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	data, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatal("systemd-launched probe did not create argument evidence")
	}
	var actual []string
	if err := json.Unmarshal(data, &actual); err != nil {
		t.Fatal("decode systemd-launched argument evidence")
	}
	expected := []string{"update-helper", "supervise", "--state-dir", stateDir, "--config", configPath}
	if len(actual) != len(expected) {
		t.Fatalf("systemd passed %d arguments; want %d", len(actual), len(expected))
	}
	for index := range expected {
		if actual[index] != expected[index] {
			t.Fatalf("systemd argument %d did not preserve the literal path", index)
		}
	}
	info, err := os.Stat(outputPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Sys().(*syscall.Stat_t).Uid != uint32(serviceUID) {
		t.Fatal("systemd-launched Agent ran as a different OS user")
	}
}

func pythonString(value string) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}
