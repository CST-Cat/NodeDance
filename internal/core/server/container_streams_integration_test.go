package server

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/CST-Cat/NodeDance/internal/agent"
	coredocker "github.com/CST-Cat/NodeDance/internal/core/docker"
)

// This is a real Core-Agent-browser stream test against the exclusively owned
// S04 Docker-in-Docker Engine. The test manifest contains only full IDs created
// by this suite, and cleanup verifies the unique suite label before removal.
func TestS05CoreAgentBrowserContainerStreamsOnOwnedDIND(t *testing.T) {
	root, endpoint, engineVersion := requireOwnedS04DIND(t)
	if !strings.HasPrefix(endpoint, "unix://") {
		t.Fatalf("owned DIND endpoint is not a Unix socket: %s", endpoint)
	}
	imageID, err := runS04DockerCLI(endpoint, "image", "inspect", "--format", "{{.Id}}", s04BusyboxImage)
	if err != nil || strings.TrimSpace(imageID) == "" {
		if _, pullErr := runS04DockerCLI(endpoint, "pull", s04BusyboxImage); pullErr != nil {
			t.Fatalf("ensure locked S05 stream fixture image exists: inspect=%v pull=%v", err, pullErr)
		}
	}
	runID := fmt.Sprintf("nd-s05-streams-%d", time.Now().UnixNano())
	manifest := newS05DINDFixtureManifest(endpoint, runID)
	t.Cleanup(func() { manifest.cleanup(t) })
	t.Log("S05_LOG_FIXTURE producer=1200_stdout_stderr_pairs line_padding=900_bytes sleep_per_pair=20ms start=browser_subscribed; expected total output exceeds 2 MiB without replaying a startup backlog")
	createFixture := func(kind string, args ...string) string {
		t.Helper()
		name := runID + "-" + kind
		base := []string{"container", "run", "--detach", "--name", name,
			"--label", "io.nodedance.test=true", "--label", "io.nodedance.suite=" + runID}
		output, err := runS04DockerCLI(endpoint, append(base, args...)...)
		if err != nil {
			t.Fatalf("create owned DIND stream fixture %s: %v", kind, err)
		}
		id := strings.TrimSpace(output)
		manifest.add(t, kind, id)
		t.Logf("S05_FIXTURE suite=%s kind=%s id=%s name=%s", runID, kind, id, name)
		return id
	}
	logContainerID := createFixture("logs", s04BusyboxImage, "sh", "-c",
		`while [ ! -f /tmp/nodedance-start-logs ]; do sleep 0.1; done; i=0; while [ "$i" -lt 1200 ]; do printf 'stdout-你好-%0900d-%s\n' "$i" "$i"; printf 'stderr-错误-%0900d-%s\n' "$i" "$i" >&2; i=$((i+1)); sleep 0.02; done; sleep 120`)
	ttyContainerID := createFixture("tty", "--tty", s04BusyboxImage, "sh", "-c", `printf 'TTY-raw-你好\n'; sleep 120`)
	statsContainerID := createFixture("stats", s04BusyboxImage, "sh", "-c", `while :; do sleep 1; done`)

	workRoot := filepath.Join(root, ".artifacts", "work-s05")
	if err := os.MkdirAll(workRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	work, err := os.MkdirTemp(workRoot, "core-agent-browser-streams-")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(work, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(work); err != nil {
			t.Errorf("remove private stream test work directory: %v", err)
		}
	})
	dockerProxy, err := startS05DockerStatsCounterProxy(endpoint)
	if err != nil {
		t.Fatal("start isolated Docker API counting proxy:", err)
	}
	t.Cleanup(func() {
		if err := dockerProxy.close(); err != nil {
			t.Errorf("close isolated Docker API proxy: %v", err)
		}
	})
	t.Setenv("DOCKER_HOST", dockerProxy.agentHost)
	certificate, rootPEM, err := makeAgentTestCertificate()
	if err != nil {
		t.Fatal(err)
	}
	caPath := filepath.Join(work, "trusted-ca.pem")
	if err := os.WriteFile(caPath, rootPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	coreHTTP := httptest.NewUnstartedServer(http.NotFoundHandler())
	publicOrigin := "https://" + coreHTTP.Listener.Addr().String()
	core, err := New("s05-container-streams-integration", Options{DataDir: filepath.Join(work, "core"), PublicOrigin: publicOrigin})
	if err != nil {
		_ = coreHTTP.Listener.Close()
		t.Fatal(err)
	}
	coreHTTP.Config.Handler = core
	coreHTTP.TLS = &tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS12}
	coreHTTP.StartTLS()
	t.Cleanup(func() {
		coreHTTP.Close()
		if err := core.Close(); err != nil {
			t.Errorf("close stream integration Core: %v", err)
		}
	})
	session, _, err := installIntegrationAdmin(core)
	if err != nil {
		t.Fatal(err)
	}
	enrollment, err := core.agents.CreateEnrollment(context.Background(), "S05 streams "+engineVersion, "127.0.0.1", sql.NullInt64{Int64: 1, Valid: true})
	if err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(work, "agent", "agent.json")
	if err := agent.Enroll(context.Background(), coreHTTP.URL, caPath, false, strings.NewReader(enrollment.Token), configPath); err != nil {
		t.Fatalf("enroll real stream Agent: %v", err)
	}
	agentConfig, err := agent.LoadConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	agentCtx, cancelAgent := context.WithCancel(context.Background())
	agentDone := make(chan error, 1)
	agentExited := false
	t.Cleanup(func() {
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			connection := core.activeAgentConnectionForNode(agentConfig.NodeID)
			if connection == nil {
				break
			}
			connection.streamMu.Lock()
			activeStreams := len(connection.streams)
			connection.streamMu.Unlock()
			if activeStreams == 0 {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		if connection := core.activeAgentConnectionForNode(agentConfig.NodeID); connection != nil {
			connection.streamMu.Lock()
			activeStreams := len(connection.streams)
			connection.streamMu.Unlock()
			if activeStreams != 0 {
				t.Errorf("Core still has %d browser stream(s) before fixture cleanup", activeStreams)
			}
		}
		if active := dockerProxy.statsActive.Load(); active != 0 {
			t.Errorf("Agent still has %d Docker stats Engine stream(s) after shutdown", active)
		}
	})
	go func() { agentDone <- agent.Run(agentCtx, configPath, "s05-streams", &agentTestLog{}) }()
	t.Cleanup(func() {
		if !agentExited {
			cancelAgent()
			select {
			case err := <-agentDone:
				if err != nil {
					t.Errorf("real stream Agent returned an error during cleanup: %v", err)
				}
			case <-time.After(12 * time.Second):
				t.Error("real stream Agent did not stop and join")
			}
		}
	})
	waitForAgentStatusWithin(t, core, agentConfig.NodeID, "online", 0, 15*time.Second)
	waitForS04DockerView(t, core, agentConfig.NodeID, 45*time.Second, func(view coredocker.View) bool {
		return view.AgentOnline && view.DockerAvailability == "available" && view.DockerSnapshotFresh && !view.DataStale &&
			dockerViewContains(view, logContainerID) && dockerViewContains(view, ttyContainerID) && dockerViewContains(view, statsContainerID)
	}, "fresh Agent inventory for all marked stream containers")

	browserConfigPath := filepath.Join(work, "browser-config.json")
	logStartPath := filepath.Join(work, "browser-log-stream-ready")
	browserConfig := map[string]string{"url": coreHTTP.URL, "session": session, "nodeId": agentConfig.NodeID,
		"logContainerId": logContainerID, "ttyContainerId": ttyContainerID, "statsContainerId": statsContainerID,
		"statsCounterUrl": dockerProxy.counterURL, "logStartPath": logStartPath}
	encoded, err := json.Marshal(browserConfig)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(browserConfigPath, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	heartbeatBeforeStreams := heartbeatSequenceForNode(t, core, agentConfig.NodeID)
	node := filepath.Join(root, ".tools", "node-v22.23.3", "bin", "node")
	if info, err := os.Stat(node); err != nil || !info.Mode().IsRegular() {
		t.Fatalf("locked Node runtime is required for real browser stream verification: %s (%v)", node, err)
	}
	script := filepath.Join(root, "web", "tests", "s05-container-stream-ui.mjs")
	commandCtx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	command := exec.CommandContext(commandCtx, node, script, browserConfigPath)
	command.Dir = filepath.Join(root, "web")
	var output []byte
	var commandErr error
	commandDone := make(chan struct{})
	go func() {
		output, commandErr = command.CombinedOutput()
		close(commandDone)
	}()
	readyDeadline := time.Now().Add(30 * time.Second)
	for {
		if _, err := os.Stat(logStartPath); err == nil {
			break
		}
		select {
		case <-commandDone:
			t.Fatalf("product browser exited before opening the live log stream: %v\n%s", commandErr, output)
		case <-commandCtx.Done():
			cancel()
			<-commandDone
			t.Fatalf("product browser did not open the live log stream in time: %v", commandCtx.Err())
		case <-time.After(25 * time.Millisecond):
		}
		if time.Now().After(readyDeadline) {
			cancel()
			<-commandDone
			t.Fatalf("product browser did not open the live log stream within 30 seconds\n%s", output)
		}
	}
	if _, err := runS04DockerCLI(endpoint, "container", "exec", logContainerID, "sh", "-c", "touch /tmp/nodedance-start-logs"); err != nil {
		t.Fatalf("start paced log output in the exact suite-owned container: %v", err)
	}
	<-commandDone
	if commandErr != nil {
		t.Fatalf("real Playwright Core-Agent stream test failed (Engine %s): %v\n%s", engineVersion, commandErr, output)
	}
	if commandCtx.Err() != nil {
		t.Fatalf("real Playwright stream browser exceeded its context: %v", commandCtx.Err())
	}
	var browserReport struct {
		Logs struct {
			Bytes    int      `json:"bytes"`
			Channels []string `json:"channels"`
		} `json:"logs"`
		StatsFields map[string]string `json:"statsFields"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(string(output))), &browserReport); err != nil {
		t.Fatalf("decode product browser stream evidence: %v\n%s", err, output)
	}
	if browserReport.Logs.Bytes < 512*1024 || !strings.Contains(strings.Join(browserReport.Logs.Channels, ","), "stdout") || !strings.Contains(strings.Join(browserReport.Logs.Channels, ","), "stderr") {
		t.Fatalf("product browser did not receive the required large stdout/stderr log stream: bytes=%d channels=%v", browserReport.Logs.Bytes, browserReport.Logs.Channels)
	}
	if strings.TrimSpace(browserReport.StatsFields["stream-block-io"]) == "" {
		t.Fatal("product browser stream evidence omitted the Block I/O metric")
	}
	t.Logf("S05_STREAM_BROWSER engine=%s node=%s containers=%s,%s,%s result=%s", engineVersion, agentConfig.NodeID,
		logContainerID, ttyContainerID, statsContainerID, strings.TrimSpace(string(output)))
	if opens := dockerProxy.statsOpens.Load(); opens != 2 {
		t.Fatalf("shared Engine stats requests=%d, want exactly 2 (first shared request plus re-open after last subscriber leaves)", opens)
	}
	if active := dockerProxy.statsActive.Load(); active != 0 {
		t.Fatalf("Engine stats stream remained active after all browser subscribers closed: %d", active)
	}
	statsSamples := dockerProxy.takeStatsEvidence()
	if len(statsSamples) < 2 {
		t.Fatalf("captured %d Engine stats records, want at least two samples for warmup verification", len(statsSamples))
	}
	statsEvidencePath, err := saveS05DockerStatsEvidence(engineVersion, runID, s04BusyboxImage, statsSamples)
	if err != nil {
		t.Fatalf("save sanitized raw Docker stats evidence: %v", err)
	}
	t.Logf("S05_STATS_RAW engine=%s fixture_image=%s fixture_id=<redacted> samples=%d file=%s json=%s", engineVersion,
		s04BusyboxImage, len(statsSamples), statsEvidencePath, strings.Join(rawJSONStrings(statsSamples), "|"))
	if !core.taskBridgeReady(agentConfig.NodeID, generationForNode(t, core, agentConfig.NodeID)) {
		t.Fatal("container stream traffic disrupted the Agent task/control bridge")
	}
	waitForCondition(t, 10*time.Second, func() bool {
		return heartbeatSequenceForNode(t, core, agentConfig.NodeID) > heartbeatBeforeStreams
	}, "Agent heartbeat did not remain live during the >10 second browser stream session")
	if err := runS05BrowserAgentDisconnectProbe(t, root, work, coreHTTP.URL, session, agentConfig.NodeID, statsContainerID, cancelAgent, agentDone); err != nil {
		t.Fatal("browser stream did not close cleanly after Agent disconnect:", err)
	}
	agentExited = true
	t.Logf("S05_CASE S05-09 logs=stdout+stderr+unicode+large tty=raw bytes=%d producer=paced-live verified=true engine=%s", browserReport.Logs.Bytes, engineVersion)
	t.Logf("S05_CASE S05-10 stats=shared-and-last-close block_io=shown active_engine_stats=0 verified=true engine=%s", engineVersion)
	t.Logf("S05_STREAM_PASS engine=%s browser_auth=401/403/404 stream_heartbeat>10s agent_disconnect=browser_closed=true", engineVersion)
}

func runS05BrowserAgentDisconnectProbe(t *testing.T, root, work, coreURL, session, nodeID, containerID string,
	cancelAgent context.CancelFunc, agentDone <-chan error) error {
	t.Helper()
	node := filepath.Join(root, ".tools", "node-v22.23.3", "bin", "node")
	configPath := filepath.Join(work, "disconnect-browser-config.json")
	config, err := json.Marshal(map[string]string{"url": coreURL, "session": session, "nodeId": nodeID, "containerId": containerID})
	if err != nil {
		return err
	}
	if err := os.WriteFile(configPath, config, 0o600); err != nil {
		return err
	}
	defer os.Remove(configPath)
	commandCtx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	command := exec.CommandContext(commandCtx, node, filepath.Join(root, "web", "tests", "s05-container-stream-disconnect.mjs"), configPath)
	command.Dir = filepath.Join(root, "web")
	stdout, err := command.StdoutPipe()
	if err != nil {
		return err
	}
	command.Stderr = os.Stderr
	if err := command.Start(); err != nil {
		return err
	}
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 4096), 1<<20)
	if !scanner.Scan() {
		_ = command.Process.Kill()
		_ = command.Wait()
		return fmt.Errorf("browser disconnect probe did not report ready: %v", scanner.Err())
	}
	if got := strings.TrimSpace(scanner.Text()); got != "READY" {
		_ = command.Process.Kill()
		_ = command.Wait()
		return fmt.Errorf("browser disconnect probe returned unexpected readiness marker %q", got)
	}
	cancelAgent()
	select {
	case err := <-agentDone:
		if err != nil {
			_ = command.Process.Kill()
			_ = command.Wait()
			return fmt.Errorf("Agent failed while closing browser streams: %w", err)
		}
	case <-time.After(12 * time.Second):
		_ = command.Process.Kill()
		_ = command.Wait()
		return fmt.Errorf("Agent did not stop after context cancellation")
	}
	if !scanner.Scan() {
		_ = command.Wait()
		return fmt.Errorf("browser disconnect probe ended without close result: %v", scanner.Err())
	}
	result := strings.TrimSpace(scanner.Text())
	if err := command.Wait(); err != nil {
		return fmt.Errorf("browser disconnect probe failed: %w result=%s", err, result)
	}
	var report struct {
		Closed    bool   `json:"closed"`
		CloseCode int    `json:"closeCode"`
		Reason    string `json:"reason"`
	}
	if err := json.Unmarshal([]byte(result), &report); err != nil {
		return fmt.Errorf("decode browser close result %q: %w", result, err)
	}
	if !report.Closed {
		return fmt.Errorf("Core left browser stream open after Agent disconnect: %+v", report)
	}
	t.Logf("S05_AGENT_DISCONNECT browser_stream_closed=true code=%d reason=%q", report.CloseCode, report.Reason)
	return nil
}

type s05DockerStatsCounterProxy struct {
	server      *http.Server
	listener    net.Listener
	transport   *http.Transport
	agentHost   string
	counterURL  string
	workDir     string
	counter     *httptest.Server
	statsOpens  atomic.Int64
	statsActive atomic.Int64
	statsMu     sync.Mutex
	statsBuffer []byte
	statsSample []json.RawMessage
}

func startS05DockerStatsCounterProxy(endpoint string) (*s05DockerStatsCounterProxy, error) {
	socket := strings.TrimPrefix(endpoint, "unix://")
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socket)
	}}
	proxy := &s05DockerStatsCounterProxy{transport: transport}
	workDir, err := os.MkdirTemp("", "nodedance-s05-docker-proxy-")
	if err != nil {
		transport.CloseIdleConnections()
		return nil, err
	}
	proxy.workDir = workDir
	socketPath := filepath.Join(workDir, "engine.sock")
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		_ = os.RemoveAll(workDir)
		transport.CloseIdleConnections()
		return nil, err
	}
	proxy.listener = listener
	proxy.agentHost = "unix://" + socketPath
	reverse := &httputil.ReverseProxy{Transport: transport, Director: func(request *http.Request) {
		request.URL.Scheme = "http"
		request.URL.Host = "docker-engine"
		request.Host = "docker-engine"
	}}
	reverse.ModifyResponse = func(response *http.Response) error {
		if response.Request != nil && strings.Contains(response.Request.URL.Path, "/containers/") && strings.HasSuffix(response.Request.URL.Path, "/stats") {
			response.Body = &s05DockerStatsCaptureBody{ReadCloser: response.Body, proxy: proxy}
		}
		return nil
	}
	proxy.counter = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/stats" || r.Method != http.MethodGet {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]int64{"opens": proxy.statsOpens.Load(), "active": proxy.statsActive.Load()})
	}))
	proxy.counterURL = proxy.counter.URL + "/stats"
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/containers/") && strings.HasSuffix(r.URL.Path, "/stats") {
			proxy.statsOpens.Add(1)
			proxy.statsActive.Add(1)
			defer proxy.statsActive.Add(-1)
		}
		reverse.ServeHTTP(w, r)
	})
	proxy.server = &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = proxy.server.Serve(listener) }()
	return proxy, nil
}

type s05DockerStatsCaptureBody struct {
	io.ReadCloser
	proxy *s05DockerStatsCounterProxy
}

func (body *s05DockerStatsCaptureBody) Read(data []byte) (int, error) {
	count, err := body.ReadCloser.Read(data)
	if count > 0 {
		body.proxy.captureStats(data[:count])
	}
	return count, err
}

func (proxy *s05DockerStatsCounterProxy) captureStats(data []byte) {
	proxy.statsMu.Lock()
	defer proxy.statsMu.Unlock()
	if len(proxy.statsSample) >= 2 {
		return
	}
	proxy.statsBuffer = append(proxy.statsBuffer, data...)
	for len(proxy.statsSample) < 2 {
		newline := bytes.IndexByte(proxy.statsBuffer, '\n')
		if newline < 0 {
			if len(proxy.statsBuffer) > 1<<20 {
				proxy.statsBuffer = nil
			}
			return
		}
		record := append([]byte(nil), proxy.statsBuffer[:newline]...)
		proxy.statsBuffer = append(proxy.statsBuffer[:0], proxy.statsBuffer[newline+1:]...)
		if len(record) == 0 {
			continue
		}
		var sample map[string]json.RawMessage
		if json.Unmarshal(record, &sample) != nil {
			continue
		}
		if _, exists := sample["id"]; exists {
			sample["id"] = json.RawMessage(`"<fixture-container-id>"`)
		}
		delete(sample, "name")
		sanitized, err := json.Marshal(sample)
		if err == nil && len(sanitized) <= 1<<20 {
			proxy.statsSample = append(proxy.statsSample, sanitized)
		}
	}
}

func (proxy *s05DockerStatsCounterProxy) takeStatsEvidence() []json.RawMessage {
	proxy.statsMu.Lock()
	defer proxy.statsMu.Unlock()
	samples := make([]json.RawMessage, len(proxy.statsSample))
	for index, sample := range proxy.statsSample {
		samples[index] = append(json.RawMessage(nil), sample...)
	}
	return samples
}

func saveS05DockerStatsEvidence(engineVersion, runID, image string, samples []json.RawMessage) (string, error) {
	workRoot, err := os.Getwd()
	if err != nil {
		return "", err
	}
	repositoryRoot := filepath.Clean(filepath.Join(workRoot, "../../.."))
	directory := filepath.Join(repositoryRoot, ".artifacts", "logs", "s05-component")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return "", err
	}
	cleanVersion := strings.NewReplacer(".", "-").Replace(engineVersion)
	path := filepath.Join(directory, "v"+cleanVersion+"-container-stream-stats-"+runID+".json")
	document := struct {
		Engine       string            `json:"engine"`
		FixtureImage string            `json:"fixture_image"`
		FixtureID    string            `json:"fixture_id"`
		Samples      []json.RawMessage `json:"samples"`
	}{Engine: engineVersion, FixtureImage: image, FixtureID: "<fixture-container-id>", Samples: samples}
	encoded, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(path, encoded, 0o600); err != nil {
		return "", err
	}
	return path, nil
}

func rawJSONStrings(samples []json.RawMessage) []string {
	result := make([]string, len(samples))
	for index, sample := range samples {
		result[index] = string(sample)
	}
	return result
}

func (proxy *s05DockerStatsCounterProxy) close() error {
	if proxy == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := proxy.server.Shutdown(ctx)
	proxy.counter.Close()
	proxy.transport.CloseIdleConnections()
	if removeErr := os.RemoveAll(proxy.workDir); err == nil {
		err = removeErr
	}
	return err
}
