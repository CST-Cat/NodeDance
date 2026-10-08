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

	binaryPath := strings.TrimSpace(os.Getenv("NODEDANCE_TEST_AGENT_BINARY"))
	if binaryPath == "" {
		t.Fatal("NODEDANCE_TEST_AGENT_BINARY must point to the compiled NodeDance Agent CLI")
	}
	binaryPath, err = filepath.Abs(binaryPath)
	if err != nil {
		t.Fatalf("resolve compiled Agent path: %v", err)
	}
	if info, err := os.Stat(binaryPath); err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		t.Fatalf("compiled Agent CLI is not an executable regular file: %v", err)
	}
	versionCtx, cancelVersion := context.WithTimeout(testCtx, 10*time.Second)
	versionOutput, err := exec.CommandContext(versionCtx, binaryPath, "version").CombinedOutput()
	cancelVersion()
	if err != nil || !strings.Contains(string(versionOutput), "NodeDance Agent ") {
		t.Fatalf("configured Agent path is not the compiled NodeDance CLI: %v", err)
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

	workRoot, err := systemdS02WorkRoot()
	if err != nil {
		t.Fatal(err)
	}
	rootInfo, err := os.Lstat(workRoot)
	if err != nil || !rootInfo.IsDir() || rootInfo.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("S02 private evidence root is not a real directory: %v", err)
	}
	rootStat, ok := rootInfo.Sys().(*syscall.Stat_t)
	if !ok {
		t.Fatal("cannot identify owner of S02 private evidence root")
	}
	allowedOwner := os.Geteuid()
	if sudoUID := strings.TrimSpace(os.Getenv("SUDO_UID")); sudoUID != "" {
		if parsed, parseErr := strconv.Atoi(sudoUID); parseErr == nil {
			allowedOwner = parsed
		}
	}
	if int(rootStat.Uid) != 0 && int(rootStat.Uid) != allowedOwner {
		t.Fatal("S02 private evidence root is owned by neither root nor the invoking CI user")
	}
	oldRootMode := rootInfo.Mode().Perm()
	if err := os.Chmod(workRoot, 0o711); err != nil {
		t.Fatalf("temporarily allow the service account to traverse the private evidence root: %v", err)
	}
	var work string
	preserveWork := false
	t.Cleanup(func() {
		if work != "" && !preserveWork {
			_ = os.RemoveAll(work)
		}
		if err := os.Chmod(workRoot, oldRootMode); err != nil {
			t.Errorf("restore private evidence root permissions: %v", err)
		}
	})
	work, err = os.MkdirTemp(workRoot, "systemd-agent-connect-")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(work, 0o711); err != nil {
		t.Fatalf("prepare service-traversable private test directory: %v", err)
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
		if !unitOwnedByTest || !unitReferencesTestFiles(contents, binaryPath, configPath) {
			t.Errorf("system unit no longer matches this test's private binary/config; refusing cleanup")
			preserveWork = true
			return
		}
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if output, err := exec.CommandContext(cleanupCtx, systemctl, "disable", "--now", agent.AgentUnitName).CombinedOutput(); err != nil {
			t.Errorf("stop and disable test-owned Agent service: %s", strings.TrimSpace(string(output)))
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
			t.Errorf("reload systemd after removing test-owned unit: %s", strings.TrimSpace(string(output)))
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
		if contents, readErr := os.ReadFile(unitPath); readErr == nil && unitReferencesTestFiles(contents, binaryPath, configPath) {
			unitOwnedByTest = true
		}
	}
	if installErr != nil {
		t.Fatalf("install and start real NodeDance Agent systemd service: %v", installErr)
	}
	if !unitReferencesTestFilesMustRead(t, unitPath, binaryPath, configPath) {
		t.Fatal("installed systemd unit does not reference the compiled Agent and private config")
	}

	firstPID := waitForSystemdMainPID(t, systemctl, 20*time.Second)
	assertSystemdAgentProcess(t, firstPID, serviceUID, binaryPath)
	firstGeneration := waitForAgentGenerationAndHeartbeat(t, core, config.NodeID, 0, 20*time.Second)
	firstDigest := readAgentCredentialDigest(t, core, config.AgentID)
	if !bytes.Equal(firstDigest, auth.DigestToken(config.Credential)) {
		t.Fatal("Core credential verifier does not match the enrolled Agent credential")
	}
	t.Logf("real systemd Agent connected: pid=%d uid=%d node=%s agent=%s generation=%d heartbeat=%d", firstPID, serviceUID, config.NodeID, config.AgentID, firstGeneration, heartbeatSequenceForNode(t, core, config.NodeID))

	restartCtx, cancelRestart := context.WithTimeout(testCtx, 30*time.Second)
	restartOutput, restartErr := exec.CommandContext(restartCtx, systemctl, "restart", agent.AgentUnitName).CombinedOutput()
	cancelRestart()
	if restartErr != nil {
		t.Fatalf("restart actual Agent service: %s", strings.TrimSpace(string(restartOutput)))
	}
	secondPID := waitForSystemdMainPIDDifferent(t, systemctl, firstPID, 20*time.Second)
	assertSystemdAgentProcess(t, secondPID, serviceUID, binaryPath)
	secondGeneration := waitForAgentGenerationAndHeartbeat(t, core, config.NodeID, firstGeneration, 20*time.Second)
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

func systemdS02WorkRoot() (string, error) {
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		return "", errors.New("resolve S02 systemd test source directory")
	}
	repositoryRoot := filepath.Clean(filepath.Join(filepath.Dir(source), "..", "..", ".."))
	artifactRoot := filepath.Join(repositoryRoot, ".artifacts")
	artifactInfo, err := os.Lstat(artifactRoot)
	if err != nil || !artifactInfo.IsDir() || artifactInfo.Mode()&os.ModeSymlink != 0 {
		if err != nil {
			return "", fmt.Errorf("inspect S02 artifact directory: %w", err)
		}
		return "", errors.New("S02 artifact directory must be a real directory")
	}
	workRoot := filepath.Join(repositoryRoot, ".artifacts", "work-s02")
	if err := os.MkdirAll(workRoot, 0o700); err != nil {
		return "", fmt.Errorf("create S02 private systemd work root: %w", err)
	}
	return filepath.Abs(workRoot)
}

func runSystemdCommand(systemctl string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, systemctl, args...).CombinedOutput()
	return string(output), err
}

func unitReferencesTestFiles(contents []byte, binaryPath, configPath string) bool {
	text := string(contents)
	return strings.Contains(text, "Description=NodeDance Agent") &&
		strings.Contains(text, "User=nobody") &&
		strings.Contains(text, binaryPath) && strings.Contains(text, configPath)
}

func unitReferencesTestFilesMustRead(t *testing.T, unitPath, binaryPath, configPath string) bool {
	t.Helper()
	contents, err := os.ReadFile(unitPath)
	if err != nil {
		t.Errorf("read installed systemd unit: %v", err)
		return false
	}
	return unitReferencesTestFiles(contents, binaryPath, configPath)
}

func waitForSystemdMainPID(t *testing.T, systemctl string, timeout time.Duration) int {
	return waitForSystemdMainPIDAfter(t, systemctl, 0, timeout)
}

func waitForSystemdMainPIDDifferent(t *testing.T, systemctl string, oldPID int, timeout time.Duration) int {
	return waitForSystemdMainPIDAfter(t, systemctl, oldPID, timeout)
}

func waitForSystemdMainPIDAfter(t *testing.T, systemctl string, oldPID int, timeout time.Duration) int {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		value, err := runSystemdCommand(systemctl, "show", agent.AgentUnitName, "-p", "MainPID", "--value")
		if err == nil {
			pid, parseErr := strconv.Atoi(strings.TrimSpace(value))
			if parseErr == nil && pid > 1 && pid != oldPID {
				return pid
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("systemd did not report a new NodeDance Agent MainPID within %s", timeout)
	return 0
}

func assertSystemdAgentProcess(t *testing.T, pid, expectedUID int, binaryPath string) {
	t.Helper()
	status, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid))
	if err != nil {
		t.Fatalf("read actual Agent process status: %v", err)
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
		t.Fatalf("systemd Agent effective UID=%d, want selected service UID=%d", effectiveUID, expectedUID)
	}
	actualExe, err := os.Readlink(fmt.Sprintf("/proc/%d/exe", pid))
	if err != nil {
		t.Fatalf("resolve actual systemd Agent executable: %v", err)
	}
	actualExe, _ = filepath.EvalSymlinks(actualExe)
	expectedExe, _ := filepath.EvalSymlinks(binaryPath)
	if actualExe != expectedExe {
		t.Fatalf("systemd MainPID executable %q does not match compiled NodeDance Agent %q", actualExe, expectedExe)
	}
}

func waitForAgentGenerationAndHeartbeat(t *testing.T, core *Server, nodeID string, greaterThan uint64, timeout time.Duration) uint64 {
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
	t.Fatalf("real systemd Agent did not establish a new online Core lease and heartbeat within %s", timeout)
	return 0
}

func readAgentCredentialDigest(t *testing.T, core *Server, agentID string) []byte {
	t.Helper()
	var digest []byte
	if err := core.store.DB.QueryRow(`SELECT credential_digest FROM agent_devices WHERE id=?`, agentID).Scan(&digest); err != nil {
		t.Fatal("read Core device credential digest")
	}
	return digest
}
