package server

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/CST-Cat/NodeDance/internal/agent"
	"github.com/CST-Cat/NodeDance/internal/core/auth"
)

// TestSystemdAgentInstallConnectRestart is enabled only in the disposable,
// root-owned GitHub Actions systemd runner. It installs the fixed production
// unit name only after proving that neither the unit file nor a loaded unit
// already exists, and it removes only the unit and files created by this test.
func TestSystemdAgentInstallConnectRestart(t *testing.T) {
	if os.Getenv("NODEDANCE_TEST_SYSTEMD") != "1" {
		t.Skip("actual systemd Agent installation is enabled only in the isolated Linux CI job")
	}
	if runtime.GOOS != "linux" || os.Geteuid() != 0 {
		t.Fatal("actual systemd Agent installation requires root in the disposable Linux CI runner")
	}
	testCtx, cancelTest := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancelTest()
	systemctl, err := exec.LookPath("systemctl")
	if err != nil {
		t.Fatal("systemctl is required for the actual Agent service integration")
	}
	managerOutput, err := runSystemdCommand(systemctl, "show", "-p", "Version", "--value")
	if err != nil || strings.TrimSpace(managerOutput) == "" {
		t.Fatalf("system manager is unavailable: %s", strings.TrimSpace(managerOutput))
	}

	unitPath := filepath.Join("/etc/systemd/system", agent.AgentUnitName)
	if _, err := os.Lstat(unitPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("refusing to overwrite pre-existing system unit %s (lstat=%v)", unitPath, err)
	}
	enabledPath := filepath.Join("/etc/systemd/system/multi-user.target.wants", agent.AgentUnitName)
	if _, err := os.Lstat(enabledPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("refusing to overwrite a pre-existing enabled-service link %s (lstat=%v)", enabledPath, err)
	}
	loadState, err := runSystemdCommand(systemctl, "show", agent.AgentUnitName, "-p", "LoadState", "--value")
	if err != nil || strings.TrimSpace(loadState) != "not-found" {
		t.Fatalf("refusing to install over an existing or loaded service (LoadState=%q, err=%v)", strings.TrimSpace(loadState), err)
	}

	compiledBinaryPath := strings.TrimSpace(os.Getenv("NODEDANCE_TEST_AGENT_BINARY"))
	if compiledBinaryPath == "" {
		t.Fatal("NODEDANCE_TEST_AGENT_BINARY must point to the compiled NodeDance Agent CLI")
	}
	compiledBinaryPath, err = filepath.Abs(compiledBinaryPath)
	if err != nil {
		t.Fatalf("resolve compiled Agent path: %v", err)
	}
	if info, err := os.Stat(compiledBinaryPath); err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		t.Fatalf("compiled Agent CLI is not an executable regular file: %v", err)
	}

	serviceUser, err := user.Lookup("nobody")
	if err != nil {
		t.Fatal("nobody service account is required for isolated systemd acceptance")
	}
	serviceUID, err := strconv.Atoi(serviceUser.Uid)
	if err != nil || serviceUID <= 0 {
		t.Fatal("nobody service account has an invalid UID")
	}
	serviceGID, err := strconv.Atoi(serviceUser.Gid)
	if err != nil {
		t.Fatal("nobody service account has an invalid primary GID")
	}

	work, err := os.MkdirTemp("/var/tmp", "nodedance-s02-systemd-agent-")
	if err != nil {
		t.Fatalf("create private systemd fixture under /var/tmp: %v", err)
	}
	if err := os.Chmod(work, 0o711); err != nil {
		_ = os.RemoveAll(work)
		t.Fatalf("allow the selected service account to traverse its private fixture root: %v", err)
	}
	preserveWork := false
	t.Cleanup(func() {
		if work != "" && !preserveWork {
			_ = os.RemoveAll(work)
		}
	})
	if info, err := os.Lstat(work); err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("private /var/tmp fixture is not a real directory: %v", err)
	}
	binaryPath, err := stageSystemdTestAgentBinary(compiledBinaryPath, work)
	if err != nil {
		t.Fatalf("stage compiled Agent CLI into private fixture: %v", err)
	}
	versionCtx, cancelVersion := context.WithTimeout(testCtx, 10*time.Second)
	versionOutput, err := exec.CommandContext(versionCtx, binaryPath, "version").CombinedOutput()
	cancelVersion()
	if err != nil || !strings.Contains(string(versionOutput), "NodeDance Agent ") {
		t.Fatalf("configured Agent path is not the compiled NodeDance CLI: %v", err)
	}

	certificate, rootPEM, err := makeAgentTestCertificate()
	if err != nil {
		t.Fatal("create ephemeral test TLS certificate")
	}
	caPath := filepath.Join(work, "trusted-test-ca.pem")
	if err := os.WriteFile(caPath, rootPEM, 0o644); err != nil {
		t.Fatalf("write test CA in private evidence directory: %v", err)
	}
	core, err := New("s02-systemd", Options{DataDir: filepath.Join(work, "core-data"), PublicOrigin: "https://panel.test"})
	if err != nil {
		t.Fatalf("create real Core: %v", err)
	}
	t.Cleanup(func() { _ = core.Close() })
	coreHTTP := httptest.NewUnstartedServer(http.HandlerFunc(core.ServeHTTP))
	coreHTTP.TLS = &tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS12}
	coreHTTP.StartTLS()
	t.Cleanup(coreHTTP.Close)

	adminClient, err := httpClientForRoots(rootPEM)
	if err != nil {
		t.Fatal("create administrator TLS client")
	}
	defer adminClient.CloseIdleConnections()
	session, csrf, err := installIntegrationAdmin(core)
	if err != nil {
		t.Fatalf("create real Core administrator session: %v", err)
	}
	enrollmentRequest, err := http.NewRequestWithContext(testCtx, http.MethodPost, coreHTTP.URL+"/api/v1/agents/enrollments",
		strings.NewReader(`{"displayName":"systemd-ci-agent"}`))
	if err != nil {
		t.Fatal("construct authenticated enrollment request")
	}
	enrollmentRequest.Header.Set("Origin", "https://panel.test")
	enrollmentRequest.Header.Set("Cookie", sessionCookieName+"="+session+"; "+csrfCookieName+"="+csrf)
	enrollmentRequest.Header.Set(csrfHeaderName, csrf)
	enrollmentRequest.Header.Set("Content-Type", "application/json")
	enrollmentResponse, err := adminClient.Do(enrollmentRequest)
	if err != nil {
		t.Fatalf("create enrollment through real Core HTTP API: %v", err)
	}
	var enrollment struct {
		NodeID string `json:"nodeId"`
		Token  string `json:"token"`
	}
	decodeErr := json.NewDecoder(enrollmentResponse.Body).Decode(&enrollment)
	_ = enrollmentResponse.Body.Close()
	if decodeErr != nil || enrollmentResponse.StatusCode != http.StatusCreated || enrollment.NodeID == "" || enrollment.Token == "" {
		t.Fatalf("Core enrollment API failed (status=%d, decode=%v)", enrollmentResponse.StatusCode, decodeErr)
	}

	stateDir := filepath.Join(work, "service-state")
	helperPath, currentPath, installedBinaryPath := systemdInstalledAgentPaths(stateDir)
	if err := os.Mkdir(stateDir, 0o700); err != nil {
		t.Fatalf("create private Agent state directory: %v", err)
	}
	configPath := filepath.Join(stateDir, "agent.json")
	if err := agent.Enroll(testCtx, coreHTTP.URL, caPath, false, strings.NewReader(enrollment.Token), configPath); err != nil {
		t.Fatalf("enroll real Agent against TLS Core: %v", err)
	}
	enrollment.Token = ""
	config, err := agent.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("load enrolled Agent identity: %v", err)
	}
	if config.NodeID != enrollment.NodeID || config.AgentID == "" || config.EnrollmentToken != "" {
		t.Fatal("Core enrollment did not persist the expected device identity")
	}
	if err := os.Chown(stateDir, serviceUID, serviceGID); err != nil {
		t.Fatalf("assign private Agent state directory to the selected service account: %v", err)
	}
	if err := os.Chown(configPath, serviceUID, serviceGID); err != nil {
		t.Fatalf("assign Agent credential file to the selected service account: %v", err)
	}
	if err := os.Chmod(stateDir, 0o700); err != nil {
		t.Fatal("enforce private Agent state directory permissions")
	}
	if err := os.Chmod(configPath, 0o600); err != nil {
		t.Fatal("enforce private Agent credential file permissions")
	}
	serviceGroups := []int{serviceGID}
	if groupIDs, groupErr := serviceUser.GroupIds(); groupErr == nil {
		for _, value := range groupIDs {
			if groupID, parseErr := strconv.Atoi(value); parseErr == nil {
				serviceGroups = appendUniqueInt(serviceGroups, groupID)
			}
		}
	}
	diagnostics := systemdAgentDiagnostics{
		systemctl: systemctl, sourceBinary: compiledBinaryPath, binaryPath: binaryPath,
		stateDir: stateDir, helperPath: helperPath, currentPath: currentPath,
		installedBinaryPath: installedBinaryPath,
		configPath:          configPath, caPath: caPath, serviceUID: serviceUID,
		serviceGroups: serviceGroups, secrets: []string{config.Credential},
	}
	t.Logf("service-user path access before systemd start (component names omitted):\n%s",
		strings.Join(diagnostics.pathAccessSummary(), "\n"))

	// InstallSystemd has a fixed production unit path. The preflight above and
	// this final absence check make the destructive boundary explicit; cleanup
	// below verifies our private paths before touching this one unit.
	if _, err := os.Lstat(unitPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("system unit appeared during test setup; refusing to overwrite it: %v", err)
	}
	unitOwnedByTest := false
	t.Cleanup(func() {
		contents, readErr := os.ReadFile(unitPath)
		if readErr != nil {
			if !errors.Is(readErr, os.ErrNotExist) {
				t.Errorf("inspect owned system unit during cleanup: %v", readErr)
			}
			return
		}
		if !unitOwnedByTest || !unitReferencesInstalledAgent(contents, helperPath, stateDir, configPath) {
			t.Errorf("system unit no longer matches this test's private binary/config; refusing cleanup")
			preserveWork = true
			return
		}
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if output, err := exec.CommandContext(cleanupCtx, systemctl, "disable", "--now", agent.AgentUnitName).CombinedOutput(); err != nil {
			t.Errorf("stop and disable test-owned Agent service: %s", strings.TrimSpace(diagnostics.sanitize(string(output))))
			_, _ = exec.CommandContext(cleanupCtx, systemctl, "kill", "--signal=SIGKILL", "--kill-whom=all", agent.AgentUnitName).CombinedOutput()
		}
		if active, err := runSystemdCommand(systemctl, "is-active", agent.AgentUnitName); err == nil && strings.TrimSpace(active) == "active" {
			t.Errorf("test-owned Agent service remained active; preserving its binary and configuration")
			preserveWork = true
			return
		}
		if err := os.Remove(unitPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			t.Errorf("remove test-owned system unit: %v", err)
			preserveWork = true
			return
		}
		if output, err := exec.CommandContext(cleanupCtx, systemctl, "daemon-reload").CombinedOutput(); err != nil {
			t.Errorf("reload systemd after removing test-owned unit: %s", strings.TrimSpace(diagnostics.sanitize(string(output))))
			preserveWork = true
			return
		}
		_, _ = exec.CommandContext(cleanupCtx, systemctl, "reset-failed", agent.AgentUnitName).CombinedOutput()
	})

	installCtx, cancelInstall := context.WithTimeout(testCtx, 30*time.Second)
	installedPath, installErr := agent.InstallSystemd(installCtx, agent.SystemdInstallOptions{
		User: "nobody", ConfigPath: configPath, BinaryPath: binaryPath, EnableNow: true,
	})
	cancelInstall()
	if installedPath == unitPath {
		if contents, readErr := os.ReadFile(unitPath); readErr == nil && unitReferencesInstalledAgent(contents, helperPath, stateDir, configPath) {
			unitOwnedByTest = true
		}
	}
	if installErr != nil {
		safeError := diagnostics.sanitize(installErr.Error())
		t.Fatalf("install and start real NodeDance Agent systemd service: %s\n%s", safeError, diagnostics.failure("install/start failed", 0))
	}
	if !unitReferencesInstalledAgentMustRead(t, unitPath, helperPath, stateDir, configPath) {
		t.Fatal("installed systemd unit does not reference the installed Agent helper and private config")
	}
	if err := assertSystemdAgentServicePaths(diagnostics); err != nil {
		t.Fatalf("installed Agent files are not accessible to the selected service account: %v\n%s", err, diagnostics.failure("installed path access check failed", 0))
	}

	firstPID := waitForSystemdMainPID(t, diagnostics, 20*time.Second)
	assertSystemdAgentProcess(t, firstPID, serviceUID, helperPath, diagnostics)
	firstGeneration := waitForAgentGenerationAndHeartbeat(t, core, config.NodeID, 0, 20*time.Second, diagnostics)
	assertSystemdAgentStillCurrent(t, diagnostics, firstPID, 0, serviceUID, helperPath)
	firstDigest := readAgentCredentialDigest(t, core, config.AgentID)
	if !bytes.Equal(firstDigest, auth.DigestToken(config.Credential)) {
		t.Fatal("Core credential verifier does not match the enrolled Agent credential")
	}
	t.Logf("real systemd Agent connected: pid=%d uid=%d node=%s agent=%s generation=%d heartbeat=%d", firstPID, serviceUID, config.NodeID, config.AgentID, firstGeneration, heartbeatSequenceForNode(t, core, config.NodeID))

	restartCtx, cancelRestart := context.WithTimeout(testCtx, 30*time.Second)
	restartsBeforeManualRestart, stateErr := systemdAgentRestartCount(systemctl)
	if stateErr != nil {
		cancelRestart()
		t.Fatalf("read systemd restart counter before explicit restart: %v\n%s", stateErr, diagnostics.failure("cannot read systemd automatic restart count", firstPID))
	}
	restartOutput, restartErr := exec.CommandContext(restartCtx, systemctl, "restart", agent.AgentUnitName).CombinedOutput()
	cancelRestart()
	if restartErr != nil {
		t.Fatalf("restart actual Agent service: %s\n%s", strings.TrimSpace(diagnostics.sanitize(string(restartOutput))), diagnostics.failure("systemctl restart command failed", firstPID))
	}
	secondPID := waitForSystemdMainPIDDifferent(t, diagnostics, firstPID, restartsBeforeManualRestart, 20*time.Second)
	assertSystemdAgentProcess(t, secondPID, serviceUID, helperPath, diagnostics)
	secondGeneration := waitForAgentGenerationAndHeartbeat(t, core, config.NodeID, firstGeneration, 20*time.Second, diagnostics)
	assertSystemdAgentStillCurrent(t, diagnostics, secondPID, restartsBeforeManualRestart, serviceUID, helperPath)
	secondDigest := readAgentCredentialDigest(t, core, config.AgentID)
	if secondGeneration <= firstGeneration || !bytes.Equal(firstDigest, secondDigest) || !bytes.Equal(secondDigest, auth.DigestToken(config.Credential)) {
		t.Fatal("systemd restart did not preserve the same Core device identity and credential")
	}
	nodes, err := core.agents.ListNodes(context.Background())
	if err != nil {
		t.Fatal("list Core nodes after systemd restart")
	}
	found := false
	for _, node := range nodes {
		if node.NodeID == config.NodeID && node.AgentID == config.AgentID && node.Status == "online" && node.ConnectionGeneration == secondGeneration {
			found = true
		}
	}
	if !found {
		t.Fatal("Core did not retain the same online node/device identity after restart")
	}
	t.Logf("systemd restart preserved identity: old_pid=%d new_pid=%d uid=%d node=%s agent=%s generation=%d heartbeat=%d", firstPID, secondPID, serviceUID, config.NodeID, config.AgentID, secondGeneration, heartbeatSequenceForNode(t, core, config.NodeID))
}

func runSystemdCommand(systemctl string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, systemctl, args...).CombinedOutput()
	return string(output), err
}

func stageSystemdTestAgentBinary(sourcePath, fixtureRoot string) (string, error) {
	binaryPath := filepath.Join(fixtureRoot, "nodedance-agent")
	compiledBinary, err := os.ReadFile(sourcePath)
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(binaryPath, compiledBinary, 0o755); err != nil {
		return "", err
	}
	return binaryPath, nil
}

func systemdInstalledAgentPaths(stateDir string) (helperPath, currentPath, installedBinaryPath string) {
	helperPath = filepath.Join(stateDir, "bin", "nodedance-agent-helper")
	currentPath = filepath.Join(stateDir, "bin", "current")
	installedBinaryPath = filepath.Join(stateDir, "bin", "versions", "bootstrap", "nodedance-agent")
	return helperPath, currentPath, installedBinaryPath
}

func unitReferencesInstalledAgent(contents []byte, helperPath, stateDir, configPath string) bool {
	text := string(contents)
	for _, line := range strings.Split(text, "\n") {
		if line == systemdAgentExecStart(helperPath, stateDir, configPath) {
			return strings.Contains(text, "Description=NodeDance Agent\n") && strings.Contains(text, "User=nobody\n")
		}
	}
	return false
}

func systemdAgentExecStart(helperPath, stateDir, configPath string) string {
	return "ExecStart=/usr/bin/env -- " + quoteSystemdExecArg(helperPath) +
		" update-helper supervise --state-dir " + quoteSystemdExecArg(stateDir) +
		" --config " + quoteSystemdExecArg(configPath)
}

func quoteSystemdExecArg(value string) string {
	quoted := strconv.Quote(value)
	quoted = strings.ReplaceAll(quoted, "%", "%%")
	return strings.ReplaceAll(quoted, "$", "$$")
}

func unitReferencesInstalledAgentMustRead(t *testing.T, unitPath, helperPath, stateDir, configPath string) bool {
	t.Helper()
	contents, err := os.ReadFile(unitPath)
	if err != nil {
		t.Errorf("read installed systemd unit: %v", err)
		return false
	}
	return unitReferencesInstalledAgent(contents, helperPath, stateDir, configPath)
}

func assertSystemdAgentServicePaths(diagnostics systemdAgentDiagnostics) error {
	for _, item := range []struct {
		label    string
		path     string
		required os.FileMode
	}{
		{label: "installed helper", path: diagnostics.helperPath, required: 0o5},
		{label: "installed current link", path: diagnostics.currentPath, required: 0o5},
		{label: "installed version binary", path: diagnostics.installedBinaryPath, required: 0o5},
		{label: "private config", path: diagnostics.configPath, required: 0o4},
		{label: "trusted test CA", path: diagnostics.caPath, required: 0o4},
	} {
		for _, line := range servicePathAccess(item.label, item.path, item.required, diagnostics.serviceUID, diagnostics.serviceGroups) {
			if !strings.HasSuffix(line, "service-access=true") {
				return fmt.Errorf("%s is not traversable/readable by service account", item.label)
			}
		}
	}
	return nil
}

func waitForSystemdMainPID(t *testing.T, diagnostics systemdAgentDiagnostics, timeout time.Duration) int {
	return waitForSystemdMainPIDAfter(t, diagnostics, 0, 0, timeout)
}

func waitForSystemdMainPIDDifferent(t *testing.T, diagnostics systemdAgentDiagnostics, oldPID int, expectedRestarts uint64, timeout time.Duration) int {
	return waitForSystemdMainPIDAfter(t, diagnostics, oldPID, expectedRestarts, timeout)
}

func waitForSystemdMainPIDAfter(t *testing.T, diagnostics systemdAgentDiagnostics, oldPID int, expectedRestarts uint64, timeout time.Duration) int {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		state, err := readSystemdUnitSnapshot(diagnostics.systemctl)
		if err == nil && state.Restarts != expectedRestarts {
			t.Fatalf("systemd automatic restart count=%d, want %d\n%s", state.Restarts, expectedRestarts, diagnostics.failure("systemd automatically restarted the Agent before its health checks passed", state.MainPID))
		}
		if err == nil && state.Active == "active" && state.Sub == "running" && state.MainPID > 1 && state.MainPID != oldPID {
			return state.MainPID
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("systemd did not report a new NodeDance Agent MainPID within %s\n%s", timeout, diagnostics.failure("systemd did not report a new MainPID", 0))
	return 0
}

type systemdUnitSnapshot struct {
	Active   string
	Sub      string
	MainPID  int
	Restarts uint64
}

func readSystemdUnitSnapshot(systemctl string) (systemdUnitSnapshot, error) {
	output, err := runSystemdCommand(systemctl, "show", agent.AgentUnitName,
		"-p", "ActiveState", "-p", "SubState", "-p", "MainPID", "-p", "NRestarts")
	if err != nil {
		return systemdUnitSnapshot{}, errors.New("systemctl show failed")
	}
	return parseSystemdUnitSnapshot(output)
}

func parseSystemdUnitSnapshot(output string) (systemdUnitSnapshot, error) {
	values := make(map[string]string, 4)
	for _, line := range strings.Split(output, "\n") {
		key, value, ok := strings.Cut(line, "=")
		if ok {
			values[key] = value
		}
	}
	mainPID, pidErr := strconv.Atoi(values["MainPID"])
	restarts, restartErr := strconv.ParseUint(values["NRestarts"], 10, 64)
	if values["ActiveState"] == "" || values["SubState"] == "" || pidErr != nil || restartErr != nil {
		return systemdUnitSnapshot{}, errors.New("systemd show omitted required service state fields")
	}
	return systemdUnitSnapshot{Active: values["ActiveState"], Sub: values["SubState"], MainPID: mainPID, Restarts: restarts}, nil
}

func validateSystemdAgentState(state systemdUnitSnapshot, expectedPID int, expectedRestarts uint64) error {
	if state.Active != "active" || state.Sub != "running" {
		return fmt.Errorf("systemd state is %s/%s, want active/running", state.Active, state.Sub)
	}
	if state.MainPID <= 1 || expectedPID > 1 && state.MainPID != expectedPID {
		return fmt.Errorf("systemd MainPID=%d, want %d", state.MainPID, expectedPID)
	}
	if state.Restarts != expectedRestarts {
		return fmt.Errorf("systemd automatic restart count=%d, want %d", state.Restarts, expectedRestarts)
	}
	return nil
}

func systemdAgentRestartCount(systemctl string) (uint64, error) {
	snapshot, err := readSystemdUnitSnapshot(systemctl)
	if err != nil {
		return 0, err
	}
	return snapshot.Restarts, nil
}

func assertSystemdAgentProcess(t *testing.T, pid, expectedUID int, binaryPath string, diagnostics systemdAgentDiagnostics) {
	t.Helper()
	status, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid))
	if err != nil {
		t.Fatalf("read actual Agent process status: %v\n%s", err, diagnostics.failure("MainPID /proc status disappeared", pid))
	}
	var effectiveUID int
	for _, line := range strings.Split(string(status), "\n") {
		if strings.HasPrefix(line, "Uid:") {
			fields := strings.Fields(strings.TrimPrefix(line, "Uid:"))
			if len(fields) < 2 {
				t.Fatal("Agent /proc status has malformed UID fields")
			}
			effectiveUID, err = strconv.Atoi(fields[1])
			if err != nil {
				t.Fatal("Agent /proc effective UID is invalid")
			}
			break
		}
	}
	if effectiveUID != expectedUID {
		t.Fatalf("systemd Agent effective UID=%d, want selected service UID=%d\n%s", effectiveUID, expectedUID, diagnostics.failure("MainPID effective UID mismatch", pid))
	}
	actualExe, err := os.Readlink(fmt.Sprintf("/proc/%d/exe", pid))
	if err != nil {
		t.Fatalf("resolve actual systemd Agent executable: %v\n%s", err, diagnostics.failure("MainPID executable link disappeared", pid))
	}
	actualExe, _ = filepath.EvalSymlinks(actualExe)
	expectedExe, _ := filepath.EvalSymlinks(binaryPath)
	if actualExe != expectedExe {
		t.Fatalf("systemd MainPID executable %q does not match configured compiled NodeDance Agent\n%s", actualExe, diagnostics.failure("MainPID executable mismatch", pid))
	}
}

func assertSystemdAgentStillCurrent(t *testing.T, diagnostics systemdAgentDiagnostics, expectedPID int, expectedRestarts uint64, expectedUID int, binaryPath string) {
	t.Helper()
	state, err := readSystemdUnitSnapshot(diagnostics.systemctl)
	if err != nil {
		t.Fatalf("read systemd state after Core heartbeat: %v\n%s", err, diagnostics.failure("cannot verify service after Core heartbeat", expectedPID))
	}
	if err := validateSystemdAgentState(state, expectedPID, expectedRestarts); err != nil {
		t.Fatalf("systemd service changed after Core heartbeat: %v\n%s", err, diagnostics.failure("service no longer matches the verified MainPID after Core heartbeat", state.MainPID))
	}
	assertSystemdAgentProcess(t, state.MainPID, expectedUID, binaryPath, diagnostics)
}

func waitForAgentGenerationAndHeartbeat(t *testing.T, core *Server, nodeID string, greaterThan uint64, timeout time.Duration, diagnostics systemdAgentDiagnostics) uint64 {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		nodes, err := core.agents.ListNodes(context.Background())
		if err == nil {
			for _, node := range nodes {
				if node.NodeID == nodeID && node.Status == "online" && node.ConnectionGeneration > greaterThan && heartbeatSequenceForNode(t, core, nodeID) > 0 {
					return node.ConnectionGeneration
				}
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("real systemd Agent did not establish a new online Core lease and heartbeat within %s\n%s", timeout, diagnostics.failure("Core did not observe a new online lease and heartbeat", 0))
	return 0
}

const maxSystemdDiagnosticBytes = 12 * 1024

type boundedDiagnosticBuffer struct {
	bytes.Buffer
	limit     int
	truncated bool
}

func (b *boundedDiagnosticBuffer) Write(value []byte) (int, error) {
	length := len(value)
	remaining := b.limit - b.Len()
	if remaining > 0 {
		if remaining > length {
			remaining = length
		}
		_, _ = b.Buffer.Write(value[:remaining])
	}
	if remaining < length {
		b.truncated = true
	}
	return length, nil
}

type systemdAgentDiagnostics struct {
	systemctl           string
	sourceBinary        string
	binaryPath          string
	stateDir            string
	helperPath          string
	currentPath         string
	installedBinaryPath string
	configPath          string
	caPath              string
	serviceUID          int
	serviceGroups       []int
	secrets             []string
}

func (d systemdAgentDiagnostics) failure(reason string, pid int) string {
	var output strings.Builder
	fmt.Fprintf(&output, "\n--- bounded systemd failure diagnostics: %s ---\n", reason)
	if pid > 1 {
		if status, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid)); err == nil {
			for _, line := range strings.Split(string(status), "\n") {
				if strings.HasPrefix(line, "State:") || strings.HasPrefix(line, "Uid:") || strings.HasPrefix(line, "Gid:") {
					fmt.Fprintf(&output, "proc %s\n", line)
				}
			}
		} else {
			output.WriteString("proc status: unavailable (process exited or became inaccessible)\n")
		}
	}
	show := runDiagnosticCommand(d.systemctl, []string{"show", agent.AgentUnitName, "-p", "ActiveState", "-p", "SubState", "-p", "ExecMainStatus", "-p", "ExecMainCode", "-p", "MainPID", "-p", "NRestarts"}, 5*time.Second)
	output.WriteString("systemctl show:\n")
	output.WriteString(d.sanitize(show.output))
	if show.truncated {
		output.WriteString("\n[systemctl output truncated at the diagnostic limit]\n")
	}
	if show.err != nil {
		fmt.Fprintf(&output, "systemctl show exit: %v\n", show.err)
	}
	journal := runDiagnosticCommand("journalctl", []string{"-u", agent.AgentUnitName, "-n", "30", "--no-pager", "-o", "json"}, 5*time.Second)
	output.WriteString("journalctl selected unit messages (max 20, sanitized):\n")
	output.WriteString(sanitizeSystemdJournal(journal.output, d.sourceBinary, d.binaryPath, d.stateDir, d.configPath, d.caPath, d.secrets))
	if journal.truncated {
		output.WriteString("\n[journalctl output truncated at the diagnostic limit]\n")
	}
	if journal.err != nil {
		fmt.Fprintf(&output, "journalctl exit: %v\n", journal.err)
	}
	output.WriteString("service-user path access (component names omitted):\n")
	output.WriteString(strings.Join(d.pathAccessSummary(), "\n"))
	output.WriteByte('\n')
	return output.String()
}

func (d systemdAgentDiagnostics) sanitize(value string) string {
	return sanitizeSystemdDiagnostic(value, d.sourceBinary, d.binaryPath, d.stateDir, d.configPath, d.caPath, d.secrets)
}

func (d systemdAgentDiagnostics) pathAccessSummary() []string {
	return systemdPathAccessSummary(d.sourceBinary, d.binaryPath, d.helperPath, d.currentPath,
		d.installedBinaryPath, d.configPath, d.caPath, d.serviceUID, d.serviceGroups)
}

type diagnosticCommandResult struct {
	output    string
	err       error
	truncated bool
}

func runDiagnosticCommand(program string, args []string, timeout time.Duration) diagnosticCommandResult {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	buffer := &boundedDiagnosticBuffer{limit: maxSystemdDiagnosticBytes}
	command := exec.CommandContext(ctx, program, args...)
	command.Stdout = buffer
	command.Stderr = buffer
	err := command.Run()
	return diagnosticCommandResult{output: buffer.String(), err: err, truncated: buffer.truncated}
}

var diagnosticAssignmentPattern = regexp.MustCompile(`(?i)(\b(?:bearer|token|credential|password|authorization|cookie|csrf)\b\s*[:=]\s*)("[^"]*"|'[^']*'|[^\s,;]+)`)
var diagnosticJSONSecretPattern = regexp.MustCompile(`(?i)(["']?(?:token|credential|password|authorization|cookie|csrf)["']?\s*:\s*)("[^"]*"|'[^']*'|[^\s,;]+)`)

func sanitizeSystemdDiagnostic(value, sourceBinary, binaryPath, stateDir, configPath, caPath string, secrets []string) string {
	for _, privateValue := range append([]string{sourceBinary, binaryPath, stateDir, configPath, caPath}, secrets...) {
		if privateValue != "" {
			value = strings.ReplaceAll(value, privateValue, "[REDACTED_PATH_OR_SECRET]")
		}
	}
	value = diagnosticAssignmentPattern.ReplaceAllString(value, "$1[REDACTED]")
	value = diagnosticJSONSecretPattern.ReplaceAllString(value, "$1[REDACTED]")
	if len(value) > maxSystemdDiagnosticBytes {
		value = value[:maxSystemdDiagnosticBytes] + "\n[diagnostic output truncated]\n"
	}
	return value
}

func sanitizeSystemdJournal(value, sourceBinary, binaryPath, stateDir, configPath, caPath string, secrets []string) string {
	var output strings.Builder
	decoder := json.NewDecoder(strings.NewReader(value))
	count := 0
	for count < 20 {
		var entry map[string]any
		if err := decoder.Decode(&entry); err != nil {
			break
		}
		unit, _ := entry["_SYSTEMD_UNIT"].(string)
		managerUnit, _ := entry["UNIT"].(string)
		if unit != agent.AgentUnitName && managerUnit != agent.AgentUnitName {
			continue
		}
		comm, _ := entry["_COMM"].(string)
		identifier, _ := entry["SYSLOG_IDENTIFIER"].(string)
		if comm != "systemd" && comm != "nodedance-agent" && comm != "env" &&
			identifier != "systemd" && identifier != "nodedance-agent" && identifier != "env" {
			continue
		}
		message, _ := entry["MESSAGE"].(string)
		if message == "" {
			continue
		}
		message = sanitizeSystemdDiagnostic(message, sourceBinary, binaryPath, stateDir, configPath, caPath, secrets)
		if len(message) > 600 {
			message = message[:600] + "[message truncated]"
		}
		message = strings.Map(func(r rune) rune {
			if r == '\n' || r == '\r' || r == '\t' || r >= 0x20 {
				return r
			}
			return -1
		}, message)
		fmt.Fprintf(&output, "%s\n", message)
		count++
	}
	if output.Len() == 0 {
		output.WriteString("no bounded messages for this unit were available\n")
	}
	if output.Len() > maxSystemdDiagnosticBytes {
		return output.String()[:maxSystemdDiagnosticBytes] + "\n[diagnostic output truncated]\n"
	}
	return output.String()
}

func servicePathAccess(label, path string, required os.FileMode, serviceUID int, serviceGroups []int) []string {
	if path == "" {
		return []string{label + ": path not set"}
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return []string{label + ": absolute path resolution failed"}
	}
	clean := filepath.Clean(absolute)
	components := strings.Split(strings.TrimPrefix(clean, string(os.PathSeparator)), string(os.PathSeparator))
	current := string(os.PathSeparator)
	lines := make([]string, 0, len(components)+1)
	for index, component := range components {
		if component != "" {
			current = filepath.Join(current, component)
		}
		info, statErr := os.Lstat(current)
		if statErr != nil {
			lines = append(lines, fmt.Sprintf("%s component=%d stat=unavailable", label, index))
			break
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok {
			lines = append(lines, fmt.Sprintf("%s component=%d owner=unavailable", label, index))
			continue
		}
		mode := info.Mode().Perm()
		isFinal := index == len(components)-1
		need := os.FileMode(0o1)
		if !isFinal || info.IsDir() {
			need = 0o1
		} else {
			need = required
		}
		available := effectivePermission(mode, int(stat.Uid), int(stat.Gid), serviceUID, serviceGroups)
		okAccess := available&need == need
		kind := "file"
		if info.IsDir() {
			kind = "dir"
		}
		if info.Mode()&os.ModeSymlink != 0 {
			kind = "symlink"
		}
		lines = append(lines, fmt.Sprintf("%s component=%d kind=%s mode=%04o owner=%d:%d required=%03o service-access=%t", label, index, kind, mode, stat.Uid, stat.Gid, need, okAccess))
	}
	return lines
}

func systemdPathAccessSummary(sourceBinary, binaryPath, helperPath, currentPath, installedBinaryPath, configPath, caPath string, serviceUID int, serviceGroups []int) []string {
	var lines []string
	for _, item := range []struct {
		label string
		path  string
		mode  os.FileMode
	}{
		{label: "compiled-source", path: sourceBinary, mode: 0o5},
		{label: "staged-source-binary", path: binaryPath, mode: 0o5},
		{label: "installed-helper", path: helperPath, mode: 0o5},
		{label: "installed-current-link", path: currentPath, mode: 0o5},
		{label: "installed-version-binary", path: installedBinaryPath, mode: 0o5},
		{label: "agent-config", path: configPath, mode: 0o4},
		{label: "test-ca", path: caPath, mode: 0o4},
		{label: "former-artifact-work-root", path: systemdS02ArtifactWorkRoot(), mode: 0o5},
	} {
		lines = append(lines, servicePathAccess(item.label, item.path, item.mode, serviceUID, serviceGroups)...)
	}
	return lines
}

func effectivePermission(mode os.FileMode, ownerUID, ownerGID, serviceUID int, serviceGroups []int) os.FileMode {
	if ownerUID == serviceUID {
		return (mode >> 6) & 0o7
	}
	for _, groupID := range serviceGroups {
		if ownerGID == groupID {
			return (mode >> 3) & 0o7
		}
	}
	return mode & 0o7
}

func appendUniqueInt(values []int, value int) []int {
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

func systemdS02ArtifactWorkRoot() string {
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		return ""
	}
	if absolute, err := filepath.Abs(source); err == nil {
		source = absolute
	}
	repositoryRoot := filepath.Clean(filepath.Join(filepath.Dir(source), "..", "..", ".."))
	return filepath.Join(repositoryRoot, ".artifacts", "work-s02")
}

func TestSystemdDiagnosticRedactionAndBounds(t *testing.T) {
	secret := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	privatePath := "/private/agent.json"
	input := "Bearer " + secret + " credential=" + secret + " path=" + privatePath + ` {"token":"` + secret + `"}`
	redacted := sanitizeSystemdDiagnostic(input, "/private/source", "/private/binary", "/private", privatePath, "/private/ca.pem", []string{secret})
	if strings.Contains(redacted, secret) || strings.Contains(redacted, privatePath) || strings.Contains(redacted, "/private/binary") {
		t.Fatal("systemd diagnostic sanitizer retained a credential or private path")
	}
	if !strings.Contains(redacted, "[REDACTED_PATH_OR_SECRET]") || !strings.Contains(redacted, "[REDACTED]") {
		t.Fatal("systemd diagnostic sanitizer did not mark redactions")
	}

	buffer := &boundedDiagnosticBuffer{limit: 8}
	if written, err := buffer.Write([]byte("0123456789")); err != nil || written != 10 {
		t.Fatalf("bounded diagnostic buffer write = %d, %v; want consumed 10 bytes", written, err)
	}
	if got := buffer.String(); got != "01234567" || !buffer.truncated {
		t.Fatalf("bounded diagnostic buffer retained %q; want at most 8 bytes", got)
	}

	journal := strings.Join([]string{
		`{"_SYSTEMD_UNIT":"nodedance-agent.service","_COMM":"nodedance-agent","MESSAGE":"` + strings.Repeat("x", 590) + secret + `"}`,
		`{"_SYSTEMD_UNIT":"init.scope","UNIT":"nodedance-agent.service","_COMM":"systemd","MESSAGE":"nodedance-agent.service: Main process exited, status=1/FAILURE"}`,
		`{"_SYSTEMD_UNIT":"nodedance-agent.service","_COMM":"env","MESSAGE":"env: failed to execute Agent CLI: Permission denied"}`,
		`{"_SYSTEMD_UNIT":"other.service","_COMM":"other-agent","MESSAGE":"` + secret + `"}`,
	}, "\n")
	filtered := sanitizeSystemdJournal(journal, "/private/source", "/private/binary", "/private", privatePath, "/private/ca.pem", []string{secret})
	if strings.Contains(filtered, secret) || strings.Contains(filtered, "other-agent") ||
		!strings.Contains(filtered, "Main process exited") || !strings.Contains(filtered, "Permission denied") {
		t.Fatal("systemd journal filter retained an unrelated message or failed to redact a credential")
	}

	directory := t.TempDir()
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	privateFile := filepath.Join(directory, "agent.json")
	if err := os.WriteFile(privateFile, []byte("private"), 0o600); err != nil {
		t.Fatal(err)
	}
	serviceUID := os.Geteuid() + 10000
	paths := servicePathAccess("private-fixture", privateFile, 0o4, serviceUID, nil)
	joinedPaths := strings.Join(paths, "\n")
	if !strings.Contains(joinedPaths, "service-access=false") || strings.Contains(joinedPaths, directory) || strings.Contains(joinedPaths, privateFile) {
		t.Fatal("service-user path diagnostics did not report inaccessible ancestors without exposing paths")
	}
}

func TestSystemdAgentFixturePathsAreServiceAccessibleAndUseInstalledHelper(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("service-user path access requires Linux file ownership")
	}
	serviceUser, err := user.Lookup("nobody")
	if err != nil {
		t.Skip("nobody service account is unavailable")
	}
	serviceUID, err := strconv.Atoi(serviceUser.Uid)
	if err != nil {
		t.Fatalf("parse service UID: %v", err)
	}
	serviceGID, err := strconv.Atoi(serviceUser.Gid)
	if err != nil {
		t.Fatalf("parse service GID: %v", err)
	}

	base := t.TempDir()
	if err := os.Chmod(base, 0o711); err != nil {
		t.Fatal(err)
	}
	checkout := filepath.Join(base, "checkout")
	buildDir := filepath.Join(checkout, ".build")
	if err := os.MkdirAll(buildDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(checkout, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(buildDir, 0o750); err != nil {
		t.Fatal(err)
	}
	sourcePath := filepath.Join(buildDir, "nodedance-agent")
	compiled := []byte("compiled Agent fixture bytes")
	if err := os.WriteFile(sourcePath, compiled, 0o755); err != nil {
		t.Fatal(err)
	}
	groups := []int{serviceGID}
	if groupIDs, groupErr := serviceUser.GroupIds(); groupErr == nil {
		for _, value := range groupIDs {
			if groupID, parseErr := strconv.Atoi(value); parseErr == nil {
				groups = appendUniqueInt(groups, groupID)
			}
		}
	}
	if access := strings.Join(servicePathAccess("compiled-source", sourcePath, 0o5, serviceUID, groups), "\n"); !strings.Contains(access, "service-access=false") {
		t.Fatalf("regression fixture did not reproduce a service-inaccessible checkout build path:\n%s", access)
	}

	fixtureRoot := filepath.Join(base, "var-tmp")
	if err := os.Mkdir(fixtureRoot, 0o711); err != nil {
		t.Fatal(err)
	}
	stagedPath, err := stageSystemdTestAgentBinary(sourcePath, fixtureRoot)
	if err != nil {
		t.Fatalf("stage source binary outside inaccessible checkout: %v", err)
	}
	if staged, err := os.ReadFile(stagedPath); err != nil || !bytes.Equal(staged, compiled) {
		t.Fatalf("staged Agent bytes differ from compiled source: err=%v", err)
	}
	if access := strings.Join(servicePathAccess("staged-source", stagedPath, 0o5, serviceUID, groups), "\n"); !strings.Contains(access, "service-access=true") {
		t.Fatalf("staged fixture binary is not accessible to the systemd service identity:\n%s", access)
	}

	stateDir := filepath.Join(fixtureRoot, "service-state")
	helperPath, currentPath, versionedPath := systemdInstalledAgentPaths(stateDir)
	if helperPath != filepath.Join(stateDir, "bin", "nodedance-agent-helper") ||
		currentPath != filepath.Join(stateDir, "bin", "current") ||
		versionedPath != filepath.Join(stateDir, "bin", "versions", "bootstrap", "nodedance-agent") {
		t.Fatal("installed Agent path helper no longer matches the systemd updater layout")
	}
	configPath := filepath.Join(stateDir, "agent.json")
	unit := "[Unit]\nDescription=NodeDance Agent\n[Service]\nUser=nobody\n" + systemdAgentExecStart(helperPath, stateDir, configPath) + "\n"
	if !unitReferencesInstalledAgent([]byte(unit), helperPath, stateDir, configPath) {
		t.Fatal("systemd ownership check rejected the installed helper and private config paths")
	}
	wrongUnit := "[Unit]\nDescription=NodeDance Agent\n[Service]\nUser=nobody\n" + systemdAgentExecStart(sourcePath, stateDir, configPath) + "\n"
	if unitReferencesInstalledAgent([]byte(wrongUnit), helperPath, stateDir, configPath) {
		t.Fatal("systemd ownership check accepted the inaccessible source path instead of installed helper")
	}
}

func TestSystemdSnapshotAndAgentStateValidation(t *testing.T) {
	state, err := parseSystemdUnitSnapshot("ActiveState=active\nSubState=running\nMainPID=1234\nNRestarts=2\n")
	if err != nil {
		t.Fatalf("parse complete systemd snapshot: %v", err)
	}
	if err := validateSystemdAgentState(state, 1234, 2); err != nil {
		t.Fatalf("accept expected active Agent state: %v", err)
	}

	for _, test := range []struct {
		name  string
		state systemdUnitSnapshot
		pid   int
		rests uint64
	}{
		{name: "automatic restart", state: systemdUnitSnapshot{Active: "active", Sub: "running", MainPID: 1235, Restarts: 3}, pid: 1234, rests: 2},
		{name: "PID changed", state: systemdUnitSnapshot{Active: "active", Sub: "running", MainPID: 1235, Restarts: 2}, pid: 1234, rests: 2},
		{name: "failed", state: systemdUnitSnapshot{Active: "failed", Sub: "failed", MainPID: 0, Restarts: 2}, pid: 1234, rests: 2},
		{name: "auto-restart substate", state: systemdUnitSnapshot{Active: "activating", Sub: "auto-restart", MainPID: 0, Restarts: 2}, pid: 1234, rests: 2},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := validateSystemdAgentState(test.state, test.pid, test.rests); err == nil {
				t.Fatal("accepted a changed or non-running systemd Agent state")
			}
		})
	}
	if _, err := parseSystemdUnitSnapshot("ActiveState=active\nMainPID=1234\nNRestarts=0\n"); err == nil {
		t.Fatal("accepted a systemd snapshot missing the SubState field")
	}
}

func readAgentCredentialDigest(t *testing.T, core *Server, agentID string) []byte {
	t.Helper()
	var digest []byte
	if err := core.store.DB.QueryRow(`SELECT credential_digest FROM agent_devices WHERE id=?`, agentID).Scan(&digest); err != nil {
		t.Fatal("read Core device credential digest")
	}
	return digest
}
