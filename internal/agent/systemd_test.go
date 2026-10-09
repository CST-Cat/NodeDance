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
		"NoNewPrivileges=true", "ProtectSystem=strict", "ProtectHome=tmpfs", "PrivateTmp=true",
		"ReadWritePaths=" + systemdQuoteUnitValue(stateDir),
		systemdQuote(helperPath), systemdQuote(configPath)} {
		if !strings.Contains(text, required) {
			t.Errorf("unit missing %q:\n%s", required, text)
		}
	}
	for _, forbidden := range []string{"StateDirectory=", "ProtectHome=true", "ProtectHome=read-only", "ReadWritePaths=/\n", "BindPaths=/\n", "MemoryDenyWriteExecute="} {
		if strings.Contains(text, forbidden) {
			t.Errorf("unit unexpectedly contains unsafe or unsupported setting %q", forbidden)
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
	if err := writeSystemdUnit(unitPath, renderSystemdUnit("root", "0", configPath, binaryPath, "", nil)); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, analyzer, "verify", unitPath).CombinedOutput()
	if err != nil {
		t.Fatalf("systemd-analyze rejected generated unit: %s", strings.TrimSpace(string(output)))
	}
}

func TestSystemdInstallPersistsAndReplacesExplicitFileRoot(t *testing.T) {
	t.Setenv("NODEDANCE_AGENT_FILE_ROOT", "")
	base := t.TempDir()
	stateDir := filepath.Join(base, "private state")
	fileRoot := filepath.Join(base, `root $ % " 中文`)
	newRoot := filepath.Join(base, "new-root")
	for _, directory := range []string{stateDir, fileRoot, newRoot} {
		if err := os.Mkdir(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	configPath := filepath.Join(stateDir, "agent.json")
	if err := SaveConfig(configPath, Config{Schema: ConfigSchema, Server: "https://core.example", Credential: strings.Repeat("d", 64),
		AgentID: "11234567-89ab-4cde-8fab-0123456789ab", NodeID: "21234567-89ab-4cde-8fab-0123456789ab"}, true); err != nil {
		t.Fatal(err)
	}
	unitDir := filepath.Join(base, "units")
	if err := os.Mkdir(unitDir, 0o700); err != nil {
		t.Fatal(err)
	}
	serviceUser, err := user.LookupId(strconv.Itoa(os.Geteuid()))
	if err != nil {
		t.Fatal(err)
	}
	options := SystemdInstallOptions{User: serviceUser.Username, ConfigPath: configPath, UnitDir: unitDir, BinaryPath: os.Args[0], FileRoot: fileRoot, FileRootSpecified: true}
	unitPath, err := InstallSystemd(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	readUnit := func() string {
		t.Helper()
		data, err := os.ReadFile(unitPath)
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	unit := readUnit()
	for _, required := range []string{
		"ProtectHome=tmpfs", "ReadWritePaths=" + systemdQuoteUnitValue(stateDir),
		"BindPaths=" + systemdQuoteUnitValue(fileRoot),
		"Environment=NODEDANCE_AGENT_FILE_ROOT=" + systemdQuoteUnitValue(fileRoot),
		"# NodeDanceFileRootBase64=",
	} {
		if !strings.Contains(unit, required) {
			t.Errorf("explicit-root unit missing %q:\n%s", required, unit)
		}
	}
	if strings.Contains(unit, "ReadWritePaths=/\n") || strings.Contains(unit, "BindPaths=/\n") {
		t.Fatal("unit opened the whole host filesystem for writes")
	}

	options.FileRoot = ""
	options.FileRootSpecified = false
	if _, err := InstallSystemd(context.Background(), options); err != nil {
		t.Fatalf("repeat install should preserve its explicit persisted root: %v", err)
	}
	if got := readUnit(); !strings.Contains(got, "Environment=NODEDANCE_AGENT_FILE_ROOT="+systemdQuoteUnitValue(fileRoot)) {
		t.Fatal("repeat install discarded the previously persisted file root")
	}

	options.FileRoot = newRoot
	options.FileRootSpecified = true
	if _, err := InstallSystemd(context.Background(), options); err != nil {
		t.Fatalf("new explicit root should replace the old root: %v", err)
	}
	if got := readUnit(); !strings.Contains(got, "Environment=NODEDANCE_AGENT_FILE_ROOT="+systemdQuoteUnitValue(newRoot)) || strings.Contains(got, "Environment=NODEDANCE_AGENT_FILE_ROOT="+systemdQuoteUnitValue(fileRoot)) {
		t.Fatal("new explicit root did not supersede the prior persisted root")
	}

	options.DisableFileRoot = true
	options.FileRoot = ""
	options.FileRootSpecified = false
	if _, err := InstallSystemd(context.Background(), options); err != nil {
		t.Fatalf("explicit file-root disable failed: %v", err)
	}
	if got := readUnit(); strings.Contains(got, "NODEDANCE_AGENT_FILE_ROOT=") || strings.Contains(got, "BindPaths="+systemdQuoteUnitValue(newRoot)) {
		t.Fatal("--no-file-root left the previous file service enabled")
	}
}

func TestValidateFileRootRequiresNarrowRealDirectory(t *testing.T) {
	base := t.TempDir()
	stateDir := filepath.Join(base, "agent-state")
	if err := os.Mkdir(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(base, "files")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if got, err := ValidateFileRoot(root, stateDir); err != nil || got != root {
		t.Fatalf("valid root=%q error=%v", got, err)
	}
	for name, value := range map[string]string{
		"relative":                 "relative",
		"whole filesystem":         string(filepath.Separator),
		"home root":                "/home",
		"root home":                "/root",
		"proc":                     "/proc",
		"sys":                      "/sys",
		"dev":                      "/dev",
		"run":                      "/run",
		"systemd state":            "/etc/systemd",
		"state directory":          stateDir,
		"state directory ancestor": filepath.Dir(stateDir),
		"state directory child":    filepath.Join(stateDir, "files"),
		"newline":                  root + "\nEnvironment=BAD=1",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ValidateFileRoot(value, stateDir); err == nil {
				t.Fatalf("unsafe file root %q accepted", value)
			}
		})
	}
	link := filepath.Join(base, "linked-root")
	if err := os.Symlink(root, link); err != nil {
		t.Fatal(err)
	}
	if _, err := ValidateFileRoot(link, stateDir); err == nil {
		t.Fatal("symlinked file root accepted")
	}
	linkedParent := filepath.Join(base, "linked-parent")
	if err := os.Symlink(base, linkedParent); err != nil {
		t.Fatal(err)
	}
	if _, err := ValidateFileRoot(filepath.Join(linkedParent, "files"), stateDir); err == nil {
		t.Fatal("file root with symlinked ancestor accepted")
	}
	file := filepath.Join(base, "plain-file")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ValidateFileRoot(file, stateDir); err == nil {
		t.Fatal("regular file accepted as a file root")
	}
}

func TestAgentFileServiceIsDisabledWithoutExplicitFileRoot(t *testing.T) {
	t.Setenv("NODEDANCE_AGENT_FILE_ROOT", "")
	service, err := openAgentFileService(filepath.Join(t.TempDir(), "agent.json"))
	if err != nil || service != nil {
		t.Fatalf("unconfigured file service=%v error=%v; want disabled without error", service, err)
	}

	stateDir := t.TempDir()
	root := filepath.Join(t.TempDir(), "selected")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NODEDANCE_AGENT_FILE_ROOT", root)
	service, err = openAgentFileService(filepath.Join(stateDir, "agent.json"))
	if err != nil || service == nil {
		t.Fatalf("explicit file service=%v error=%v", service, err)
	}
	if err := service.Close(); err != nil {
		t.Fatal(err)
	}

	stateRoot := filepath.Join(stateDir, "files")
	if err := os.Mkdir(stateRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NODEDANCE_AGENT_FILE_ROOT", stateRoot)
	if service, err := openAgentFileService(filepath.Join(stateDir, "agent.json")); err == nil || service != nil {
		if service != nil {
			_ = service.Close()
		}
		t.Fatalf("Agent state directory was exposed as file root: service=%v error=%v", service, err)
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
	base, err := os.MkdirTemp("/var/lib", "nodedance-systemd-s02-")
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
	outputPath := filepath.Join(stateDir, "captured arguments.json")
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
	unit := renderSystemdUnit(serviceUser.Username, serviceUser.Gid, configPath, binaryPath, "", nil)
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

func TestSystemdFileRootWriteBoundaryOnRunningManager(t *testing.T) {
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
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	if output, err := exec.CommandContext(ctx, systemctl, "show", "-p", "Version", "--value").CombinedOutput(); err != nil || strings.TrimSpace(string(output)) == "" {
		t.Fatalf("system manager is unavailable: %s", strings.TrimSpace(string(output)))
	}
	serviceUser, err := user.Lookup("nobody")
	if err != nil {
		t.Fatal("nobody account is required for isolated systemd unit test")
	}
	serviceUID, _ := strconv.Atoi(serviceUser.Uid)
	serviceGID, _ := strconv.Atoi(serviceUser.Gid)
	base, err := os.MkdirTemp("/var/lib", "nodedance-systemd-root-boundary-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(base)
	stateDir := filepath.Join(base, "state")
	if err := os.Mkdir(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(stateDir, "agent.json")
	if err := SaveConfig(configPath, Config{Schema: ConfigSchema, Server: "https://core.example", Credential: strings.Repeat("e", 64),
		AgentID: "11234567-89ab-4cde-8fab-0123456789ab", NodeID: "21234567-89ab-4cde-8fab-0123456789ab"}, true); err != nil {
		t.Fatal(err)
	}
	homeRoot := filepath.Join("/home", fmt.Sprintf("nodedance file-root %d", os.Getpid()))
	selectedRoot := filepath.Join(homeRoot, "selected")
	unselectedRoot := filepath.Join(homeRoot, "unselected")
	for _, directory := range []string{selectedRoot, unselectedRoot} {
		if err := os.MkdirAll(directory, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	defer os.RemoveAll(homeRoot)
	if err := os.WriteFile(filepath.Join(selectedRoot, "before.txt"), []byte("visible"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(unselectedRoot, "secret.txt"), []byte("hidden"), 0o644); err != nil {
		t.Fatal(err)
	}
	outsideRoot := filepath.Join("/var/lib", fmt.Sprintf("nodedance-file-root-outside-%d", os.Getpid()))
	if err := os.Mkdir(outsideRoot, 0o777); err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(outsideRoot)
	if err := os.Chmod(outsideRoot, 0o777); err != nil {
		t.Fatal(err)
	}
	outputPath := filepath.Join(stateDir, "permission evidence.json")
	binaryPath := filepath.Join(base, "permission probe")
	probe := fmt.Sprintf(`#!/usr/bin/python3
import errno,json,os
root=os.environ['NODEDANCE_AGENT_FILE_ROOT']
with open(os.path.join(root,'write-ok.txt'),'w') as stream: stream.write('allowed')
hidden=%s
hidden_ok=False
try:
  open(os.path.join(hidden,'secret.txt')).read()
  hidden_ok=True
except OSError:
  pass
outside=%s
write_blocked=False
try:
  open(os.path.join(outside,'must-not-write'),'w').write('blocked')
except OSError as error:
  write_blocked=error.errno in (errno.EROFS,errno.EACCES,errno.EPERM)
with open(%s,'w') as stream:
  json.dump({'rootWritable':True,'unselectedHomeReadable':hidden_ok,'outsideWriteBlocked':write_blocked},stream)
import time; time.sleep(30)
`, pythonString(unselectedRoot), pythonString(outsideRoot), pythonString(outputPath))
	if err := os.WriteFile(binaryPath, []byte(probe), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{stateDir, configPath, selectedRoot, binaryPath} {
		if err := os.Chown(path, serviceUID, serviceGID); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chmod(base, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(homeRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	unitName := fmt.Sprintf("nodedance-agent-file-root-%d.service", os.Getpid())
	unitPath := filepath.Join("/run/systemd/system", unitName)
	if _, err := os.Lstat(unitPath); !os.IsNotExist(err) {
		t.Fatalf("unique temporary unit path already exists: %v", err)
	}
	unit := renderSystemdUnit(serviceUser.Username, serviceUser.Gid, configPath, binaryPath, selectedRoot, nil)
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
		t.Fatalf("systemd rejected exact file-root mounts: %s", strings.TrimSpace(string(output)))
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
		t.Fatal("systemd-launched probe did not create permission evidence")
	}
	var evidence struct {
		RootWritable           bool `json:"rootWritable"`
		UnselectedHomeReadable bool `json:"unselectedHomeReadable"`
		OutsideWriteBlocked    bool `json:"outsideWriteBlocked"`
	}
	if err := json.Unmarshal(data, &evidence); err != nil {
		t.Fatal(err)
	}
	if !evidence.RootWritable || evidence.UnselectedHomeReadable || !evidence.OutsideWriteBlocked {
		t.Fatalf("systemd file-root boundary evidence=%+v", evidence)
	}
	if content, err := os.ReadFile(filepath.Join(selectedRoot, "write-ok.txt")); err != nil || string(content) != "allowed" {
		t.Fatalf("configured root write content=%q error=%v", content, err)
	}
	if _, err := os.Stat(filepath.Join(outsideRoot, "must-not-write")); !os.IsNotExist(err) {
		t.Fatalf("unconfigured writable host path was modified: %v", err)
	}
}

func pythonString(value string) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}
