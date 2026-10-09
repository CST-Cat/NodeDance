package server

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/CST-Cat/NodeDance/internal/agent"
	agentcompose "github.com/CST-Cat/NodeDance/internal/agent/compose"
	agentdocker "github.com/CST-Cat/NodeDance/internal/agent/docker"
	corecomposeedit "github.com/CST-Cat/NodeDance/internal/core/composeedit"
	"github.com/CST-Cat/NodeDance/internal/protocol"
)

// TestDINDComposeEditorCoreAgentHTTPS exercises the administrator HTTPS API,
// an enrolled Agent WSS connection, the locked official Compose plugin, and
// the job-owned Engine socket as one end-to-end S11 transaction.
func TestDINDComposeEditorCoreAgentHTTPS(t *testing.T) {
	dindRoot := strings.TrimSpace(os.Getenv("NODEDANCE_S11_DIND_ROOT"))
	fixtureRootValue := strings.TrimSpace(os.Getenv("NODEDANCE_S11_FIXTURE_ROOT"))
	engineVersion := strings.TrimSpace(os.Getenv("NODEDANCE_S11_ENGINE"))
	runID := strings.TrimSpace(os.Getenv("NODEDANCE_S11_RUN_ID"))
	if dindRoot == "" || fixtureRootValue == "" || (engineVersion != "28" && engineVersion != "29") {
		t.Skip("NOT_READY: set the S11 DIND root, unique fixture root, and Engine version")
	}
	if runID == "" {
		runID = fmt.Sprintf("local-%d", time.Now().UnixNano())
	}
	runSuffix := s11SafeRunSuffix(runID)
	if runSuffix == "" {
		t.Fatal("S11 run identifier has no safe characters")
	}

	repoRoot, err := findS11RepositoryRoot()
	if err != nil {
		t.Fatal(err)
	}
	dindRoot, err = filepath.Abs(dindRoot)
	if err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(dindRoot, "socket", "docker.sock")
	markerPath := filepath.Join(dindRoot, "owner.json")
	var dindMarker struct {
		Suite         string `json:"suite"`
		Socket        string `json:"socket"`
		ServerVersion string `json:"server_version"`
	}
	markerBytes, err := os.ReadFile(markerPath)
	if err != nil || json.Unmarshal(markerBytes, &dindMarker) != nil || dindMarker.Suite != "nodedance-s00-dind" || filepath.Clean(dindMarker.Socket) != socket || !strings.HasPrefix(dindMarker.ServerVersion, engineVersion+".") {
		t.Fatalf("S11 requires this job's owner-marked Engine %s socket; marker=%+v err=%v", engineVersion, dindMarker, err)
	}
	if info, err := os.Stat(socket); err != nil || info.Mode()&os.ModeSocket == 0 {
		t.Fatalf("owner-marked DIND socket is unavailable: %v", err)
	}
	dockerHost := "unix://" + socket
	// Agent runtime and every direct Docker operation are pinned to the same
	// owner-marked socket. This test never relies on the default Docker daemon.
	t.Setenv("DOCKER_HOST", dockerHost)
	if strings.TrimSpace(os.Getenv("DOCKER_CONFIG")) == "" {
		t.Fatal("locked Docker CLI configuration is missing; run scripts/test/docker-test-config.sh first")
	}
	if _, err := exec.LookPath("docker"); err != nil {
		t.Fatalf("Docker CLI with the locked Compose plugin is required: %v", err)
	}
	runner := agentcompose.ExecRunner{DockerHost: dockerHost}
	setupCtx, setupCancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer setupCancel()
	pluginVersion, err := runner.Run(setupCtx, repoRoot, []string{"compose", "version", "--short"}, dockerHost)
	if err != nil || strings.TrimSpace(string(pluginVersion)) == "" {
		t.Fatalf("locked official Docker Compose plugin is unavailable: %v (%s)", err, pluginVersion)
	}
	engine, err := agentdocker.NewSDKEngine(dockerHost)
	if err != nil {
		t.Fatal(err)
	}
	if err := engine.Ping(setupCtx); err != nil {
		_ = engine.Close()
		t.Fatalf("owner-marked Engine ping failed: %v", err)
	}
	defer engine.Close()

	fixtureRoot, err := filepath.Abs(fixtureRootValue)
	if err != nil {
		t.Fatal(err)
	}
	fixtureParent := filepath.Join(repoRoot, ".artifacts", "fixtures")
	if !s11PathWithin(fixtureParent, fixtureRoot) || fixtureRoot == fixtureParent {
		t.Fatalf("fixture root must be a unique child of %s, got %s", fixtureParent, fixtureRoot)
	}
	if err := s11RejectSymlinkPath(repoRoot, fixtureRoot); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(fixtureRoot); err == nil {
		t.Fatalf("run-scoped S11 fixture already exists; preserving it: %s", fixtureRoot)
	} else if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("inspect run-scoped fixture path: %v", err)
	}
	if err := os.MkdirAll(fixtureParent, 0o700); err != nil {
		t.Fatalf("create S11 fixture parent: %v", err)
	}
	if err := s11RejectSymlinkPath(repoRoot, fixtureRoot); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(fixtureRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(fixtureRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	suite := "nodedance-s11-core-agent-" + runSuffix + "-engine" + engineVersion
	ownerPath := filepath.Join(fixtureRoot, ".nodedance-s11-owner.json")
	ownerBytes, _ := json.Marshal(map[string]string{"suite": suite, "run_id": runID, "path": fixtureRoot, "docker_socket": socket})
	if err := os.WriteFile(ownerPath, ownerBytes, 0o600); err != nil {
		_ = os.RemoveAll(fixtureRoot)
		t.Fatal(err)
	}
	var cleanupFixture func()
	t.Cleanup(func() {
		if cleanupFixture != nil {
			cleanupFixture()
			return
		}
		data, err := os.ReadFile(ownerPath)
		var marker map[string]string
		if err != nil || json.Unmarshal(data, &marker) != nil || marker["suite"] != suite || marker["path"] != fixtureRoot || marker["docker_socket"] != socket {
			t.Errorf("S11 fixture owner marker changed; preserving %s", fixtureRoot)
			return
		}
		if err := os.RemoveAll(fixtureRoot); err != nil {
			t.Errorf("remove only this unstarted S11 fixture root: %v", err)
		}
	})
	projectName := "nd-s11-api-" + runSuffix + "-e" + engineVersion
	if len(projectName) > 55 {
		projectName = projectName[:55]
	}
	if err := s11AssertProjectAbsent(setupCtx, runner, repoRoot, projectName); err != nil {
		t.Fatalf("run-scoped Compose project name is already present on the owner-marked Engine: %v", err)
	}
	projectDir := filepath.Join(fixtureRoot, "project")
	if err := os.Mkdir(projectDir, 0o700); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(projectDir, "compose.yaml")
	initialPort := s11FreeTCPPort(t)
	lockedImages := s11ReadLockedImages(t, repoRoot)
	composeSource := fmt.Sprintf(`services:
  web:
    image: %s
    ports:
      - "127.0.0.1:%d:80/tcp"
    labels:
      io.nodedance.test: "true"
      io.nodedance.suite: %q
  data:
    image: %s
    command: ["sh", "-c", "if [ ! -f /data/marker ]; then echo 's11-volume-marker-%s' > /data/marker; fi; exec sleep 600"]
    volumes:
      - state:/data
    labels:
      io.nodedance.test: "true"
      io.nodedance.suite: %q
volumes:
  state:
    labels:
      io.nodedance.test: "true"
      io.nodedance.suite: %q
networks:
  default:
    labels:
      io.nodedance.test: "true"
      io.nodedance.suite: %q
`, lockedImages.Nginx, initialPort, suite, lockedImages.Busybox, suite, suite, suite, suite)
	if err := os.WriteFile(configPath, []byte(composeSource), 0o600); err != nil {
		t.Fatal(err)
	}
	composePrefix := []string{"compose", "--project-name", projectName, "--project-directory", projectDir, "-f", configPath}
	projectStarted := false
	cleanupFixture = func() {
		if err := s11RemoveOwnedComposeProject(t, runner, projectDir, composePrefix, projectName, suite); err != nil {
			t.Errorf("could not prove and clean only the S11 project; preserving fixture: %v", err)
			return
		}
		if projectStarted {
			if _, err := os.Stat(filepath.Join(projectDir, "compose.yaml")); err != nil {
				t.Errorf("expected owned Compose source to remain until fixture cleanup: %v", err)
				return
			}
		}
		data, err := os.ReadFile(ownerPath)
		var marker map[string]string
		if err != nil || json.Unmarshal(data, &marker) != nil || marker["suite"] != suite || marker["path"] != fixtureRoot || marker["docker_socket"] != socket {
			t.Errorf("S11 fixture owner marker changed; preserving %s", fixtureRoot)
			return
		}
		if err := os.RemoveAll(fixtureRoot); err != nil {
			t.Errorf("remove only this S11 fixture root: %v", err)
		}
	}

	if _, err := runner.Run(setupCtx, projectDir, append(append([]string(nil), composePrefix...), "up", "--detach"), dockerHost); err != nil {
		t.Fatalf("start two-service project on owner-marked Engine: %v", err)
	}
	projectStarted = true
	if err := s11WaitFor(t, 60*time.Second, func() (bool, error) {
		web, data := s11ComposeServiceIDs(setupCtx, engine, projectName)
		if web == "" || data == "" {
			return false, nil
		}
		webContainer, webErr := engine.Inspect(setupCtx, web)
		dataContainer, dataErr := engine.Inspect(setupCtx, data)
		if webErr != nil || dataErr != nil {
			return false, errors.Join(webErr, dataErr)
		}
		return webContainer.Running && dataContainer.Running, nil
	}); err != nil {
		t.Fatal(err)
	}
	webBeforeID, dataBeforeID := s11ComposeServiceIDs(setupCtx, engine, projectName)
	webBefore, err := engine.Inspect(setupCtx, webBeforeID)
	if err != nil {
		t.Fatal(err)
	}
	oldPort, ok := s11PublishedPort(webBefore, 80, "tcp", "127.0.0.1")
	if !ok {
		t.Fatalf("initial web container does not have the expected actual IPv4 published port: %+v", webBefore.Ports)
	}
	dataBefore, err := engine.Inspect(setupCtx, dataBeforeID)
	if err != nil {
		t.Fatal(err)
	}
	volumeBefore := s11NamedVolumeAt(dataBefore, "/data")
	if volumeBefore == "" {
		t.Fatalf("data service has no named volume mounted at /data: %+v", dataBefore.Mounts)
	}
	markerBefore, err := runner.Run(setupCtx, projectDir, append(append([]string(nil), composePrefix...), "exec", "-T", "data", "cat", "/data/marker"), dockerHost)
	if err != nil || strings.TrimSpace(string(markerBefore)) != "s11-volume-marker-"+suite {
		t.Fatalf("named volume marker was not created: %v (%q)", err, markerBefore)
	}

	certificate, rootPEM, err := makeAgentTestCertificate()
	if err != nil {
		t.Fatal(err)
	}
	core, err := New("s11-core-agent-compose-editor", Options{
		DataDir: filepath.Join(fixtureRoot, "core-data"), PublicOrigin: "https://panel.test",
		AgentOfflineTimeout: 30 * time.Second, AgentSweepInterval: 250 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	coreHTTP := httptest.NewUnstartedServer(core)
	coreHTTP.TLS = &tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS12}
	coreHTTP.StartTLS()
	var adminClient *http.Client
	var closeCoreOnce sync.Once
	closeCore := func() {
		closeCoreOnce.Do(func() {
			if adminClient != nil {
				adminClient.CloseIdleConnections()
			}
			coreHTTP.Close()
			if err := core.Close(); err != nil {
				t.Errorf("close S11 test Core: %v", err)
			}
		})
	}
	t.Cleanup(closeCore)
	sessionToken, csrfToken, err := installIntegrationAdmin(core)
	if err != nil {
		closeCore()
		t.Fatal(err)
	}
	adminClient, err = httpClientForRoots(rootPEM)
	if err != nil {
		closeCore()
		t.Fatal(err)
	}
	admin := s11HTTPSAdminClient{client: adminClient, baseURL: coreHTTP.URL, origin: "https://panel.test", session: sessionToken, csrf: csrfToken}

	enrollmentResponse := admin.request(t, http.MethodPost, "/api/v1/agents/enrollments", map[string]string{"displayName": "S11 real Engine Compose editor"}, "")
	if enrollmentResponse.status != http.StatusCreated {
		closeCore()
		t.Fatalf("create Agent enrollment through authenticated HTTPS API: HTTP %d: %s", enrollmentResponse.status, enrollmentResponse.body)
	}
	var enrollment struct {
		NodeID string `json:"nodeId"`
		Token  string `json:"token"`
	}
	if err := json.Unmarshal(enrollmentResponse.body, &enrollment); err != nil || enrollment.NodeID == "" || enrollment.Token == "" {
		closeCore()
		t.Fatalf("decode HTTPS enrollment response: %+v err=%v", enrollment, err)
	}
	caPath := filepath.Join(fixtureRoot, "trusted-ca.pem")
	if err := os.WriteFile(caPath, rootPEM, 0o600); err != nil {
		closeCore()
		t.Fatal(err)
	}
	agentConfigPath := filepath.Join(fixtureRoot, "agent", "agent.json")
	if err := agent.Enroll(context.Background(), coreHTTP.URL, caPath, false, strings.NewReader(enrollment.Token+"\n"), agentConfigPath); err != nil {
		closeCore()
		t.Fatalf("real Agent TLS enrollment failed: %v", err)
	}
	agentConfig, err := agent.LoadConfig(agentConfigPath)
	if err != nil || agentConfig.NodeID != enrollment.NodeID {
		closeCore()
		t.Fatalf("Agent identity did not match Core enrollment: config=%+v err=%v", agentConfig, err)
	}
	agentCtx, stopAgent := context.WithCancel(context.Background())
	agentDone := make(chan error, 1)
	go func() { agentDone <- agent.Run(agentCtx, agentConfigPath, "s11-core-agent", nil) }()
	t.Cleanup(func() {
		stopAgent()
		select {
		case runErr := <-agentDone:
			if runErr != nil {
				t.Errorf("real Agent returned an error while stopping: %v", runErr)
			}
		case <-time.After(8 * time.Second):
			t.Error("real Agent did not stop after context cancellation")
		}
	})
	waitForAgentStatusWithin(t, core, enrollment.NodeID, "online", 0, 20*time.Second)
	if err := s11WaitFor(t, 20*time.Second, func() (bool, error) {
		nodes, err := core.agents.ListNodes(context.Background())
		if err != nil {
			return false, err
		}
		for _, node := range nodes {
			if node.NodeID == enrollment.NodeID {
				return hasCapability(node.Capabilities, protocol.CapabilityComposeEditor) && hasCapability(node.Capabilities, protocol.CapabilityCompose), nil
			}
		}
		return false, nil
	}); err != nil {
		t.Fatalf("real Agent did not negotiate Compose and editor capabilities: %v", err)
	}

	projectAPIPath := "/api/v1/nodes/" + enrollment.NodeID + "/compose/projects"
	var project protocol.ComposeProject
	if err := s11WaitFor(t, 30*time.Second, func() (bool, error) {
		response := admin.request(t, http.MethodGet, projectAPIPath, nil, "")
		if response.status != http.StatusOK {
			return false, fmt.Errorf("list Compose projects returned HTTP %d: %s", response.status, response.body)
		}
		var payload struct {
			Projects []protocol.ComposeProject `json:"projects"`
		}
		if err := json.Unmarshal(response.body, &payload); err != nil {
			return false, err
		}
		for _, candidate := range payload.Projects {
			if candidate.Ref.Name == projectName && filepath.Clean(candidate.Ref.WorkingDirectory) == filepath.Clean(projectDir) {
				project = candidate
				return len(candidate.Ref.ConfigFiles) == 1 && filepath.Clean(candidate.Ref.ConfigFiles[0]) == filepath.Clean(configPath) && candidate.ConfigAvailable, nil
			}
		}
		return false, nil
	}); err != nil {
		t.Fatalf("real Agent inventory did not expose this exact Compose project: %v", err)
	}

	baseEditorPath := "/api/v1/nodes/" + enrollment.NodeID + "/compose/projects/" + project.Ref.Key + "/editor"
	sourceResponse := admin.request(t, http.MethodGet, baseEditorPath+"/source", nil, "")
	if sourceResponse.status != http.StatusOK {
		t.Fatalf("read Compose source through authenticated HTTPS API: HTTP %d: %s", sourceResponse.status, sourceResponse.body)
	}
	var source struct {
		Files []protocol.ComposeSourceFile `json:"files"`
	}
	if err := json.Unmarshal(sourceResponse.body, &source); err != nil || len(source.Files) != 1 || filepath.Clean(source.Files[0].Path) != filepath.Clean(configPath) || source.Files[0].Version == "" {
		t.Fatalf("HTTPS source response did not bind the exact versioned project file: %+v err=%v", source, err)
	}
	newPort := s11FreeTCPPortDifferent(t, oldPort)
	input := protocol.ComposeEditorInput{
		ExpectedVersions: map[string]string{source.Files[0].Path: source.Files[0].Version},
		PortEdits:        []protocol.ComposePortEdit{{File: configPath, Service: "web", Target: 80, Protocol: "tcp", OldHostIP: "127.0.0.1", OldPublished: oldPort, NewHostIP: "127.0.0.1", NewPublished: newPort}},
	}
	previewResponse := admin.request(t, http.MethodPost, baseEditorPath+"/preview", map[string]any{"editor": input}, "")
	if previewResponse.status != http.StatusOK {
		t.Fatalf("preview port change through authenticated HTTPS API: HTTP %d: %s", previewResponse.status, previewResponse.body)
	}
	var preview protocol.ComposeEditorResult
	if err := json.Unmarshal(previewResponse.body, &preview); err != nil || len(preview.AffectedServices) != 1 || preview.AffectedServices[0] != "web" {
		t.Fatalf("preview did not isolate the web service: preview=%+v err=%v", preview, err)
	}
	if !strings.Contains(preview.ResolvedConfig, fmt.Sprintf("%d", newPort)) {
		t.Fatalf("resolved preview does not contain requested published port %d", newPort)
	}

	applyResponse := admin.request(t, http.MethodPost, baseEditorPath+"/apply", map[string]any{"editor": input}, "s11-core-agent-"+runSuffix)
	if applyResponse.status != http.StatusAccepted {
		t.Fatalf("apply port edit through authenticated HTTPS API: HTTP %d: %s", applyResponse.status, applyResponse.body)
	}
	var operation struct {
		OperationID string                 `json:"operationId"`
		Status      corecomposeedit.Status `json:"status"`
		Verified    bool                   `json:"verified"`
	}
	if err := json.Unmarshal(applyResponse.body, &operation); err != nil || operation.OperationID == "" {
		t.Fatalf("decode persisted Core operation response: operation=%+v err=%v", operation, err)
	}
	operationPath := "/api/v1/nodes/" + enrollment.NodeID + "/compose/editor/operations/" + operation.OperationID
	operationDeadline := time.Now().Add(5 * time.Minute)
	for time.Now().Before(operationDeadline) {
		response := admin.request(t, http.MethodGet, operationPath, nil, "")
		if response.status != http.StatusOK {
			t.Fatalf("read persisted Compose edit operation returned HTTP %d: %s", response.status, response.body)
		}
		var current struct {
			OperationID      string                 `json:"operationId"`
			Status           corecomposeedit.Status `json:"status"`
			Verified         bool                   `json:"verified"`
			AffectedServices []string               `json:"affectedServices"`
		}
		if err := json.Unmarshal(response.body, &current); err != nil {
			t.Fatalf("decode persisted Compose edit operation: %v", err)
		}
		if current.Status == corecomposeedit.StatusFailed || current.Status == corecomposeedit.StatusUnknown {
			t.Fatalf("persisted Compose operation ended %s: %s", current.Status, response.body)
		}
		if current.Status == corecomposeedit.StatusSucceeded {
			if current.OperationID != operation.OperationID || !current.Verified || len(current.AffectedServices) != 1 || current.AffectedServices[0] != "web" {
				t.Fatalf("persisted operation lacks verified web-only success: %s", response.body)
			}
			break
		}
		time.Sleep(150 * time.Millisecond)
	}
	if time.Now().After(operationDeadline) {
		t.Fatal("Core did not persist a verified Compose edit result before the deadline")
	}
	webAfterID, dataAfterID := s11ComposeServiceIDs(setupCtx, engine, projectName)
	if webAfterID == "" || dataAfterID == "" || webAfterID == webBeforeID || dataAfterID != dataBeforeID {
		t.Fatalf("real Engine did not recreate only web: web %s->%s, data %s->%s", webBeforeID, webAfterID, dataBeforeID, dataAfterID)
	}
	webAfter, err := engine.Inspect(setupCtx, webAfterID)
	if err != nil {
		t.Fatal(err)
	}
	if actual, ok := s11PublishedPort(webAfter, 80, "tcp", "127.0.0.1"); !ok || actual != newPort {
		t.Fatalf("Engine published port did not change from %d to %d: actual=%d found=%t mappings=%+v", oldPort, newPort, actual, ok, webAfter.Ports)
	}
	dataAfter, err := engine.Inspect(setupCtx, dataAfterID)
	if err != nil {
		t.Fatal(err)
	}
	volumeAfter := s11NamedVolumeAt(dataAfter, "/data")
	if volumeAfter != volumeBefore {
		t.Fatalf("unaffected data service volume changed: before=%q after=%q", volumeBefore, volumeAfter)
	}
	markerAfter, err := runner.Run(setupCtx, projectDir, append(append([]string(nil), composePrefix...), "exec", "-T", "data", "cat", "/data/marker"), dockerHost)
	if err != nil || strings.TrimSpace(string(markerAfter)) != strings.TrimSpace(string(markerBefore)) {
		t.Fatalf("named volume data was not preserved across editor apply: before=%q after=%q err=%v", markerBefore, markerAfter, err)
	}
	t.Logf("S11 Core HTTPS/WSS Engine %s verified apply: project=%s web=%s->%s port=%d->%d data=%s volume=%s marker=%s", dindMarker.ServerVersion, projectName, webBeforeID, webAfterID, oldPort, newPort, dataAfterID, volumeAfter, strings.TrimSpace(string(markerAfter)))
}

type s11HTTPSAdminClient struct {
	client  *http.Client
	baseURL string
	origin  string
	session string
	csrf    string
}

type s11HTTPSResponse struct {
	status int
	body   []byte
}

func (client s11HTTPSAdminClient) request(t *testing.T, method, path string, payload any, idempotencyKey string) s11HTTPSResponse {
	t.Helper()
	var body io.Reader
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		body = strings.NewReader(string(encoded))
	}
	request, err := http.NewRequest(method, client.baseURL+path, body)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Origin", client.origin)
	request.Header.Set("Cookie", sessionCookieName+"="+client.session+"; "+csrfCookieName+"="+client.csrf)
	request.Header.Set(csrfHeaderName, client.csrf)
	if payload != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if idempotencyKey != "" {
		request.Header.Set("Idempotency-Key", idempotencyKey)
	}
	response, err := client.client.Do(request)
	if err != nil {
		t.Fatalf("HTTPS %s %s: %v", method, path, err)
	}
	defer response.Body.Close()
	responseBytes, err := io.ReadAll(io.LimitReader(response.Body, 2<<20))
	if err != nil {
		t.Fatalf("read HTTPS response: %v", err)
	}
	return s11HTTPSResponse{status: response.StatusCode, body: responseBytes}
}

func s11WaitFor(t *testing.T, timeout time.Duration, check func() (bool, error)) error {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		ready, err := check()
		if err != nil {
			lastErr = err
		} else if ready {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	if lastErr != nil {
		return fmt.Errorf("condition not reached within %s (last error: %w)", timeout, lastErr)
	}
	return fmt.Errorf("condition not reached within %s", timeout)
}

func s11ReadLockedImages(t *testing.T, repoRoot string) struct{ Nginx, Busybox string } {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(repoRoot, "test-images.lock.json"))
	if err != nil {
		t.Fatal(err)
	}
	var lock struct {
		Images map[string]string `json:"images"`
	}
	if err := json.Unmarshal(data, &lock); err != nil {
		t.Fatal(err)
	}
	nginx, busybox := lock.Images["nginx"], lock.Images["busybox"]
	if !strings.Contains(nginx, "@sha256:") || !strings.Contains(busybox, "@sha256:") {
		t.Fatal("S11 DIND images must be pinned to locked digests")
	}
	return struct{ Nginx, Busybox string }{Nginx: nginx, Busybox: busybox}
}

func s11ComposeServiceIDs(ctx context.Context, engine *agentdocker.SDKEngine, project string) (web, data string) {
	ids, err := engine.ListAll(ctx)
	if err != nil {
		return "", ""
	}
	for _, id := range ids {
		item, inspectErr := engine.Inspect(ctx, id)
		if inspectErr != nil || item.Compose == nil || item.Compose.Project != project {
			continue
		}
		switch item.Compose.Service {
		case "web":
			web = id
		case "data":
			data = id
		}
	}
	return web, data
}

func s11PublishedPort(item agentdocker.Container, target uint16, protocolName, ip string) (uint16, bool) {
	for _, mapping := range item.Ports {
		if mapping.ContainerPort != target || !strings.EqualFold(mapping.Protocol, protocolName) {
			continue
		}
		for _, binding := range mapping.Published {
			if binding.IP != ip {
				continue
			}
			var port uint16
			if _, err := fmt.Sscanf(binding.Port, "%d", &port); err == nil && port != 0 {
				return port, true
			}
		}
	}
	return 0, false
}

func s11NamedVolumeAt(item agentdocker.Container, destination string) string {
	for _, mount := range item.Mounts {
		if mount.Type == "volume" && filepath.Clean(mount.Destination) == filepath.Clean(destination) {
			return mount.Name
		}
	}
	return ""
}

func s11FreeTCPPort(t *testing.T) uint16 {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	return uint16(listener.Addr().(*net.TCPAddr).Port)
}

func s11FreeTCPPortDifferent(t *testing.T, old uint16) uint16 {
	t.Helper()
	for range 16 {
		candidate := s11FreeTCPPort(t)
		if candidate != old {
			return candidate
		}
	}
	t.Fatalf("could not select a published port distinct from %d", old)
	return 0
}

func s11SafeRunSuffix(value string) string {
	var out strings.Builder
	for _, char := range strings.ToLower(value) {
		if char >= 'a' && char <= 'z' || char >= '0' && char <= '9' || char == '-' {
			out.WriteRune(char)
		} else if char == '_' || char == '.' {
			out.WriteByte('-')
		}
	}
	value = strings.Trim(out.String(), "-")
	if len(value) > 38 {
		value = value[:38]
	}
	return strings.Trim(value, "-")
}

func findS11RepositoryRoot() (string, error) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		return "", errors.New("could not locate S11 integration test source")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..", ".."))
	if _, err := os.Stat(filepath.Join(root, "test-images.lock.json")); err != nil {
		return "", fmt.Errorf("S11 test repository root is invalid: %w", err)
	}
	return root, nil
}

func s11PathWithin(parent, child string) bool {
	relative, err := filepath.Rel(parent, child)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative)
}

func s11RejectSymlinkPath(root, path string) error {
	if !s11PathWithin(root, path) {
		return fmt.Errorf("S11 fixture path escapes repository: %s", path)
	}
	for current := path; s11PathWithin(root, current); current = filepath.Dir(current) {
		info, err := os.Lstat(current)
		if err == nil && info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("refusing symlink in S11 fixture path: %s", current)
		}
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("inspect S11 fixture path %s: %w", current, err)
		}
		if current == root {
			break
		}
	}
	return nil
}

func s11RemoveOwnedComposeProject(t *testing.T, runner agentcompose.ExecRunner, directory string, prefix []string, project, suite string) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	if _, err := s11InspectComposeResources(ctx, runner, directory, project, suite); err != nil {
		return fmt.Errorf("pre-cleanup ownership check failed; preserving fixture: %w", err)
	}
	_, err := runner.Run(ctx, directory, append(append([]string(nil), prefix...), "down", "--volumes", "--remove-orphans"), runner.DockerHost)
	if err != nil {
		return fmt.Errorf("Compose down for exact marked project %s: %w", project, err)
	}
	after, err := s11InspectComposeResources(ctx, runner, directory, project, suite)
	if err != nil {
		return fmt.Errorf("post-cleanup ownership check failed; preserving fixture: %w", err)
	}
	for kind, ids := range after {
		if len(ids) != 0 {
			return fmt.Errorf("Compose down left owner-labeled %s resources for project %s: %s", kind, project, strings.Join(ids, ","))
		}
	}
	return nil
}

func s11InspectComposeResources(ctx context.Context, runner agentcompose.ExecRunner, directory, project, suite string) (map[string][]string, error) {
	resources := []struct {
		kind    string
		list    string
		inspect string
	}{
		{kind: "container", list: "container", inspect: "{{json .Config.Labels}}"},
		{kind: "volume", list: "volume", inspect: "{{json .Labels}}"},
		{kind: "network", list: "network", inspect: "{{json .Labels}}"},
	}
	filters := []string{"label=com.docker.compose.project=" + project, "label=io.nodedance.suite=" + suite}
	found := make(map[string][]string, len(resources))
	for _, resource := range resources {
		found[resource.kind] = []string{}
		ids := make(map[string]struct{})
		for _, filter := range filters {
			args := []string{resource.list, "ls", "--quiet", "--filter", filter}
			if resource.kind == "container" {
				args = []string{resource.list, "ls", "--all", "--quiet", "--filter", filter}
			}
			output, err := runner.Run(ctx, directory, args, runner.DockerHost)
			if err != nil {
				return nil, fmt.Errorf("list %s resources with %q: %w (%s)", resource.kind, filter, err, output)
			}
			for _, id := range strings.Fields(string(output)) {
				ids[id] = struct{}{}
			}
		}
		for id := range ids {
			labelsOutput, err := runner.Run(ctx, directory, []string{resource.kind, "inspect", "--format", resource.inspect, id}, runner.DockerHost)
			if err != nil {
				return nil, fmt.Errorf("inspect %s %s: %w", resource.kind, id, err)
			}
			var labels map[string]string
			if json.Unmarshal(bytesTrimSpace(labelsOutput), &labels) != nil || labels["io.nodedance.test"] != "true" || labels["io.nodedance.suite"] != suite || labels["com.docker.compose.project"] != project {
				return nil, fmt.Errorf("refusing to remove or ignore unproven %s %s: labels=%s", resource.kind, id, labelsOutput)
			}
			found[resource.kind] = append(found[resource.kind], id)
		}
	}
	for kind := range found {
		sort.Strings(found[kind])
	}
	return found, nil
}

func s11AssertProjectAbsent(ctx context.Context, runner agentcompose.ExecRunner, directory, project string) error {
	for _, args := range [][]string{
		{"container", "ls", "--all", "--quiet", "--filter", "label=com.docker.compose.project=" + project},
		{"volume", "ls", "--quiet", "--filter", "label=com.docker.compose.project=" + project},
		{"network", "ls", "--quiet", "--filter", "label=com.docker.compose.project=" + project},
	} {
		output, err := runner.Run(ctx, directory, args, runner.DockerHost)
		if err != nil {
			return fmt.Errorf("inspect possible project resources: %w (%s)", err, output)
		}
		if len(strings.TrimSpace(string(output))) > 0 {
			return fmt.Errorf("project %s already owns resources: %s", project, strings.TrimSpace(string(output)))
		}
	}
	return nil
}

func bytesTrimSpace(value []byte) []byte { return []byte(strings.TrimSpace(string(value))) }
