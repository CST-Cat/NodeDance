package server

import (
	"context"
	"crypto/tls"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/CST-Cat/NodeDance/internal/agent"
	"github.com/CST-Cat/NodeDance/internal/agent/taskjournal"
	coredocker "github.com/CST-Cat/NodeDance/internal/core/docker"
	"github.com/CST-Cat/NodeDance/internal/protocol"
	"github.com/CST-Cat/NodeDance/internal/taskstate"
)

// TestS05AgentProcessKillRecoveryOnOwnedDIND exercises the durable Core and
// Agent journals around three actual Agent process kills. The Unix Docker
// proxy only gates one restart request at a time and forwards all other Engine
// traffic to the explicitly owned DIND socket.
func TestS05AgentProcessKillRecoveryOnOwnedDIND(t *testing.T) {
	root, endpoint, engineVersion := requireOwnedS04DIND(t)
	imageID, err := runS04DockerCLI(endpoint, "image", "inspect", "--format", "{{.Id}}", s04BusyboxImage)
	if err != nil || strings.TrimSpace(imageID) == "" {
		if _, pullErr := runS04DockerCLI(endpoint, "pull", s04BusyboxImage); pullErr != nil {
			t.Fatalf("ensure locked recovery fixture image exists on owned DIND: inspect=%v pull=%v", err, pullErr)
		}
	}
	runID := fmt.Sprintf("nd-s05-process-recovery-%d", time.Now().UnixNano())
	containerIDs := make(map[string]string, 3)
	t.Cleanup(func() { cleanupS05RecoveryFixtures(t, endpoint, runID, containerIDs) })
	for _, phase := range []string{"before", "during", "after"} {
		name := runID + "-" + phase
		command := "n=0; if [ -f /tmp/nodedance-start-count ]; then n=$(cat /tmp/nodedance-start-count); fi; echo $((n+1)) >/tmp/nodedance-start-count; exec sleep 3600"
		id, err := runS04DockerCLI(endpoint, "container", "run", "--detach", "--name", name,
			"--label", "io.nodedance.test=true", "--label", "io.nodedance.suite="+runID,
			s04BusyboxImage, "sh", "-c", command)
		if err != nil {
			t.Fatalf("create %s recovery fixture on Engine %s: %v", phase, engineVersion, err)
		}
		id = strings.TrimSpace(id)
		if !protocol.IsFullContainerID(id) {
			t.Fatalf("owned Engine returned malformed %s fixture ID %q", phase, id)
		}
		containerIDs[phase] = id
		t.Logf("S05_RECOVERY_FIXTURE suite=%s phase=%s id=%s name=%s", runID, phase, id, name)
	}
	for phase, id := range containerIDs {
		if count, err := waitForS05ContainerStartCount(endpoint, id, 10*time.Second); err != nil || count != 1 {
			t.Fatalf("%s fixture did not start exactly once: count=%d err=%v", phase, count, err)
		}
	}

	work, err := os.MkdirTemp(filepath.Join(root, ".artifacts", "work-s05"), "process-recovery-")
	if err != nil {
		t.Fatal("create isolated recovery work directory:", err)
	}
	if err := os.Chmod(work, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(work); err != nil {
			t.Errorf("remove Agent-process recovery work directory: %v", err)
		}
	})
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
	core, err := New("s05-process-recovery", Options{DataDir: filepath.Join(work, "core"), PublicOrigin: publicOrigin})
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
			t.Errorf("close process-recovery Core: %v", err)
		}
	})
	session, csrf, err := installIntegrationAdmin(core)
	if err != nil {
		t.Fatal(err)
	}
	enrollment, err := core.agents.CreateEnrollment(context.Background(), "S05 process recovery "+engineVersion, "127.0.0.1", sql.NullInt64{Int64: 1, Valid: true})
	if err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(work, "agent", "agent.json")
	if err := agent.Enroll(context.Background(), coreHTTP.URL, caPath, false, strings.NewReader(enrollment.Token), configPath); err != nil {
		t.Fatalf("enroll recovery Agent: %v", err)
	}
	config, err := agent.LoadConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	processNumber := 0
	lastAgentGeneration := uint64(0)
	var latestAgentLog string
	// AF_UNIX paths have a small platform limit. Keep this unique test-owned
	// socket under /tmp instead of nesting it under the long worktree path.
	proxyDir, err := os.MkdirTemp(os.TempDir(), "nodedance-s05-proxy-")
	if err != nil {
		t.Fatal("create private Docker proxy directory:", err)
	}
	if err := os.Chmod(proxyDir, 0o700); err != nil {
		_ = os.Remove(proxyDir)
		t.Fatal("protect private Docker proxy directory:", err)
	}
	ownerMarker := filepath.Join(proxyDir, ".nodedance-owner")
	if err := os.WriteFile(ownerMarker, []byte(runID), 0o600); err != nil {
		_ = os.Remove(proxyDir)
		t.Fatal("write private Docker proxy owner marker:", err)
	}
	proxySocket := filepath.Join(proxyDir, "docker.sock")
	t.Cleanup(func() {
		owner, readErr := os.ReadFile(ownerMarker)
		if readErr != nil || string(owner) != runID {
			t.Errorf("refuse to clean Docker proxy directory without matching owner marker: path=%s owner=%q err=%v", proxyDir, owner, readErr)
			return
		}
		_ = os.Remove(proxySocket)
		if err := os.Remove(ownerMarker); err != nil {
			t.Errorf("remove Docker proxy owner marker: %v", err)
			return
		}
		if err := os.Remove(proxyDir); err != nil {
			t.Errorf("remove own Docker proxy directory: %v", err)
		}
	})
	dockerProxy, err := newS05DockerFaultProxy(proxySocket, endpoint)
	if err != nil {
		t.Fatal("start fault-gated Docker Unix proxy:", err)
	}
	t.Cleanup(func() {
		if err := dockerProxy.Close(); err != nil {
			t.Errorf("close Docker fault proxy: %v", err)
		}
	})
	binaryPath := buildS05AgentBinary(t, root, work)
	var child *s05AgentProcess
	startChild := func() {
		t.Helper()
		previousGeneration := lastAgentGeneration
		if active := core.activeAgentConnectionForNode(config.NodeID); active != nil && active.generation > previousGeneration {
			previousGeneration = active.generation
		}
		processNumber++
		latestAgentLog = filepath.Join(work, fmt.Sprintf("agent-process-%02d.log", processNumber))
		var startErr error
		child, startErr = startS05AgentProcess(binaryPath, configPath, dockerProxy.Host(), latestAgentLog)
		if startErr != nil {
			t.Fatalf("start real Agent process: %v", startErr)
		}
		t.Logf("S05_AGENT_PROCESS_START phase=%d pid=%d docker_proxy=ready task_journal_path_configured=true", processNumber, child.command.Process.Pid)
		generation, ready := awaitS05AgentBridgeReady(core, config.NodeID, previousGeneration, 20*time.Second)
		if !ready {
			t.Fatalf("real Agent process did not complete full task-journal synchronization: generation=%d core=%s process=%s journal_file=%s log=%q",
				generation, describeS05AgentTaskBridge(core, config.NodeID), child.diagnostic(), describeS05JournalFile(filepath.Join(filepath.Dir(configPath), "tasks.sqlite")), readS05AgentLog(latestAgentLog))
		}
		lastAgentGeneration = generation
		t.Logf("S05_AGENT_PROCESS_SYNC previous_generation=%d active_generation=%d task_bridge_ready=true node_status_online=true verified=true",
			previousGeneration, generation)
		waitForS04DockerView(t, core, config.NodeID, 20*time.Second, func(view coredocker.View) bool {
			if view.ActiveGeneration != generation || !view.AgentOnline || view.DataStale || !view.DockerSnapshotFresh || view.DockerAvailability != "available" {
				return false
			}
			for _, id := range containerIDs {
				if !dockerViewContains(view, id) {
					return false
				}
			}
			return true
		}, "real Agent process did not publish a fresh Engine inventory")
	}
	stopChild := func(kill bool) {
		t.Helper()
		if child == nil {
			return
		}
		current := child
		child = nil
		if kill {
			if err := current.killAndWait(4 * time.Second); err != nil {
				t.Fatalf("SIGKILL real Agent process: %v", err)
			}
			return
		}
		if err := current.stopAndWait(5 * time.Second); err != nil {
			t.Fatalf("stop real Agent process: %v", err)
		}
	}
	t.Cleanup(func() { stopChild(false) })
	startChild()

	client := tlsHTTPClient(rootPEM)
	client.Timeout = 8 * time.Second
	postRestart := func(targetID, key string) string {
		t.Helper()
		body := strings.NewReader(`{"action":"restart"}`)
		request, err := http.NewRequest(http.MethodPost, coreHTTP.URL+"/api/v1/nodes/"+config.NodeID+"/containers/"+targetID+"/actions", body)
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Origin", coreHTTP.URL)
		request.Header.Set(csrfHeaderName, csrf)
		request.Header.Set("Idempotency-Key", key)
		request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: session})
		request.AddCookie(&http.Cookie{Name: csrfCookieName, Value: csrf})
		response, err := client.Do(request)
		if err != nil {
			t.Fatal("submit process-recovery restart task:", err)
		}
		defer response.Body.Close()
		data, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
		if err != nil || response.StatusCode != http.StatusAccepted {
			t.Fatalf("restart task returned HTTP %d body=%q err=%v", response.StatusCode, data, err)
		}
		var accepted s05TaskAccepted
		if err := json.Unmarshal(data, &accepted); err != nil || accepted.TaskID == "" {
			t.Fatalf("decode restart task acceptance %q: %v", data, err)
		}
		return accepted.TaskID
	}
	waitForTask := func(taskID string, timeout time.Duration, predicate func(s05TaskAPIView) bool, description string) s05TaskAPIView {
		t.Helper()
		deadline := time.Now().Add(timeout)
		var last s05TaskAPIView
		for time.Now().Before(deadline) {
			var readErr error
			last, readErr = getS05Task(client, coreHTTP.URL, session, config.NodeID, taskID)
			if readErr == nil && predicate(last) {
				return last
			}
			time.Sleep(50 * time.Millisecond)
		}
		t.Fatalf("%s: last task view=%+v", description, last)
		return s05TaskAPIView{}
	}
	journalPath := filepath.Join(filepath.Dir(configPath), "tasks.sqlite")
	assertInterruptedRecord := func(taskID string) {
		t.Helper()
		journal, err := taskjournal.Open(context.Background(), journalPath, config.NodeID)
		if err != nil {
			t.Fatalf("open Agent journal after SIGKILL: %v", err)
		}
		entry, err := journal.Get(context.Background(), taskID)
		_ = journal.Close()
		if err != nil {
			t.Fatalf("read Agent task record after SIGKILL: %v", err)
		}
		if entry.Status != taskstate.Running || entry.ExecutionPhase != taskjournal.ExecutionPhaseMutationMayHaveStarted || entry.Baseline == nil {
			t.Fatalf("SIGKILL boundary did not leave a durable, verified mutation baseline: %+v", entry)
		}
	}
	assertAudit := func(taskID string, want ...string) {
		t.Helper()
		events, err := core.tasks.AuditEvents(context.Background(), config.NodeID, taskID)
		if err != nil {
			t.Fatalf("read Core task audit for %s: %v", taskID, err)
		}
		seen := make(map[string]bool, len(events))
		for _, event := range events {
			seen[event.Event+":"+event.ToStatus.String] = true
		}
		for _, status := range want {
			if !seen["agent_status:"+status] {
				t.Fatalf("Core task %s audit omitted agent_status:%s: %+v", taskID, status, events)
			}
		}
	}
	assertCounter := func(targetID string, want int) {
		t.Helper()
		got, err := readS05ContainerStartCount(endpoint, targetID)
		if err != nil || got != want {
			t.Fatalf("real Engine process start count changed: target=%s got=%d want=%d err=%v", targetID, got, want, err)
		}
	}
	// Before mutation: the Agent has durably saved its baseline and entered the
	// Docker request path, but the proxy has not forwarded the request to DIND.
	beforeID := containerIDs["before"]
	beforeStarted, err := runS04DockerCLI(endpoint, "container", "inspect", "--format", "{{.State.StartedAt}}", beforeID)
	if err != nil {
		t.Fatal(err)
	}
	proxyHit := dockerProxy.Arm(beforeID, s05ProxyBeforeMutation)
	beforeTask := postRestart(beforeID, "s05-process-before-"+runID)
	select {
	case <-proxyHit:
	case <-time.After(20 * time.Second):
		t.Fatal("Agent never reached the pre-mutation Docker request barrier")
	}
	waitForTask(beforeTask, 10*time.Second, func(task s05TaskAPIView) bool { return task.Status == "running" }, "Core did not persist running before Docker mutation")
	stopChild(true)
	assertInterruptedRecord(beforeTask)
	startChild()
	waitForTask(beforeTask, 20*time.Second, func(task s05TaskAPIView) bool { return task.Status == "unknown" }, "pre-mutation SIGKILL was not recovered to unknown")
	assertAudit(beforeTask, "running", "unknown")
	assertCounter(beforeID, 1)
	afterBefore, err := runS04DockerCLI(endpoint, "container", "inspect", "--format", "{{.State.StartedAt}}", beforeID)
	if err != nil || strings.TrimSpace(afterBefore) != strings.TrimSpace(beforeStarted) {
		t.Fatalf("pre-mutation kill changed Engine StartedAt: before=%q after=%q err=%v", strings.TrimSpace(beforeStarted), strings.TrimSpace(afterBefore), err)
	}
	t.Log("S05_RECOVERY_CASE phase=before_mutation agent_sigkill=true agent_journal=unknown core=unknown engine_mutation=false replay=false verified=true")

	// During mutation: the Engine accepted restart, while the proxy withholds
	// the response so SIGKILL leaves a durable Agent running record.
	duringID := containerIDs["during"]
	duringStarted, err := runS04DockerCLI(endpoint, "container", "inspect", "--format", "{{.State.StartedAt}}", duringID)
	if err != nil {
		t.Fatal(err)
	}
	duringCount, err := readS05ContainerStartCount(endpoint, duringID)
	if err != nil {
		t.Fatal(err)
	}
	proxyHit = dockerProxy.Arm(duringID, s05ProxyAfterMutation)
	duringTask := postRestart(duringID, "s05-process-during-"+runID)
	select {
	case <-proxyHit:
	case <-time.After(20 * time.Second):
		t.Fatal("Agent did not complete the real Engine restart before response barrier")
	}
	duringStartedAfterMutation, err := runS04DockerCLI(endpoint, "container", "inspect", "--format", "{{.State.StartedAt}}", duringID)
	if err != nil || strings.TrimSpace(duringStartedAfterMutation) == strings.TrimSpace(duringStarted) {
		t.Fatalf("proxy response barrier was reached without a real Engine restart: before=%q after=%q err=%v", strings.TrimSpace(duringStarted), strings.TrimSpace(duringStartedAfterMutation), err)
	}
	assertCounter(duringID, duringCount+1)
	stopChild(true)
	assertInterruptedRecord(duringTask)
	startChild()
	waitForTask(duringTask, 25*time.Second, func(task s05TaskAPIView) bool { return task.Status == "succeeded" && task.Result.Code == "verified" }, "during-mutation SIGKILL did not reconcile to verified success")
	assertAudit(duringTask, "running", "unknown", "succeeded")
	assertCounter(duringID, duringCount+1)
	t.Log("S05_RECOVERY_CASE phase=during_mutation agent_sigkill=true agent_unknown=true core_audit_unknown=true reconciled_success=true engine_restart_count=1 replay=false verified=true")

	// After completion: the terminal task is already durable in both systems.
	// Killing and starting the Agent again must preserve that result without a
	// second Engine restart.
	afterID := containerIDs["after"]
	afterCount, err := readS05ContainerStartCount(endpoint, afterID)
	if err != nil {
		t.Fatal(err)
	}
	afterTask := postRestart(afterID, "s05-process-after-"+runID)
	waitForTask(afterTask, 25*time.Second, func(task s05TaskAPIView) bool { return task.Status == "succeeded" && task.Result.Code == "verified" }, "normal post-mutation task did not reach verified success")
	assertAudit(afterTask, "running", "succeeded")
	assertCounter(afterID, afterCount+1)
	stopChild(true)
	startChild()
	waitForTask(afterTask, 10*time.Second, func(task s05TaskAPIView) bool { return task.Status == "succeeded" }, "Agent restart changed a durable terminal task")
	assertCounter(afterID, afterCount+1)
	t.Log("S05_RECOVERY_CASE phase=after_terminal agent_sigkill=true core_terminal=succeeded replay=false verified=true")

	stopChild(false)
	for taskID, want := range map[string]taskstate.Status{beforeTask: taskstate.Unknown, duringTask: taskstate.Succeeded, afterTask: taskstate.Succeeded} {
		journal, err := taskjournal.Open(context.Background(), journalPath, config.NodeID)
		if err != nil {
			t.Fatalf("open final Agent journal for exact task verification: %v", err)
		}
		entry, getErr := journal.Get(context.Background(), taskID)
		closeErr := journal.Close()
		if getErr != nil || closeErr != nil || entry.Status != want {
			t.Fatalf("final Agent journal task=%s status=%s want=%s get=%v close=%v", taskID, entry.Status, want, getErr, closeErr)
		}
	}
	if got := len(containerIDs); got != 3 {
		t.Fatalf("recovery suite fixture count=%d, want three exact identities", got)
	}
	t.Log("S05_CASE S05-05 core_agent_process_restart=before,during,after verified=true")
}

type s05ProxyBarrier uint8

const (
	s05ProxyUnarmed s05ProxyBarrier = iota
	s05ProxyBeforeMutation
	s05ProxyAfterMutation
)

type s05DockerFaultProxy struct {
	endpoint  string
	listener  net.Listener
	server    *http.Server
	transport *http.Transport
	mu        sync.Mutex
	targetID  string
	mode      s05ProxyBarrier
	fired     bool
	hit       chan struct{}
	release   chan struct{}
}

func newS05DockerFaultProxy(path, engineEndpoint string) (*s05DockerFaultProxy, error) {
	parsed, err := url.Parse(engineEndpoint)
	if err != nil || parsed.Scheme != "unix" || parsed.Path == "" {
		return nil, errors.New("fault proxy requires an explicit owned Unix Engine socket")
	}
	listener, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = listener.Close()
		return nil, err
	}
	proxy := &s05DockerFaultProxy{endpoint: "unix://" + path, listener: listener,
		transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", parsed.Path)
		}},
	}
	proxy.server = &http.Server{Handler: http.HandlerFunc(proxy.serveHTTP), ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = proxy.server.Serve(listener) }()
	return proxy, nil
}

func (p *s05DockerFaultProxy) Host() string { return p.endpoint }

func (p *s05DockerFaultProxy) Arm(targetID string, mode s05ProxyBarrier) <-chan struct{} {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.release != nil {
		select {
		case <-p.release:
		default:
			close(p.release)
		}
	}
	p.targetID, p.mode, p.fired = targetID, mode, false
	p.hit, p.release = make(chan struct{}), make(chan struct{})
	return p.hit
}

func (p *s05DockerFaultProxy) Release() {
	if p == nil {
		return
	}
	p.mu.Lock()
	release := p.release
	p.mu.Unlock()
	if release == nil {
		return
	}
	select {
	case <-release:
	default:
		close(release)
	}
}

func (p *s05DockerFaultProxy) serveHTTP(w http.ResponseWriter, request *http.Request) {
	p.mu.Lock()
	match := request.Method == http.MethodPost && p.targetID != "" && strings.Contains(request.URL.Path, "/containers/"+p.targetID+"/restart") && !p.fired
	mode, hit, release := p.mode, p.hit, p.release
	if match {
		p.fired = true
	}
	p.mu.Unlock()
	if match && mode == s05ProxyBeforeMutation {
		close(hit)
		select {
		case <-request.Context().Done():
		case <-release:
			http.Error(w, "test barrier released without mutation", http.StatusServiceUnavailable)
		}
		return
	}
	upstream := request.Clone(request.Context())
	upstream.RequestURI = ""
	upstream.URL = &url.URL{Scheme: "http", Host: "docker", Path: request.URL.Path, RawPath: request.URL.RawPath, RawQuery: request.URL.RawQuery}
	response, err := p.transport.RoundTrip(upstream)
	if err != nil {
		http.Error(w, "Docker Engine proxy request failed", http.StatusBadGateway)
		return
	}
	if match && mode == s05ProxyAfterMutation {
		close(hit)
		select {
		case <-request.Context().Done():
			_ = response.Body.Close()
			return
		case <-release:
		}
	}
	defer response.Body.Close()
	for name, values := range response.Header {
		for _, value := range values {
			w.Header().Add(name, value)
		}
	}
	w.WriteHeader(response.StatusCode)
	if flusher, ok := w.(http.Flusher); ok {
		// Docker events and stats can remain open without producing an immediate
		// body chunk. Forward response headers now so the SDK can establish its
		// long-lived stream before the first event arrives.
		flusher.Flush()
	}
	_, _ = io.Copy(w, response.Body)
}

func (p *s05DockerFaultProxy) Close() error {
	if p == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	err := p.server.Shutdown(ctx)
	p.transport.CloseIdleConnections()
	_ = os.Remove(strings.TrimPrefix(p.endpoint, "unix://"))
	return err
}

type s05AgentProcess struct {
	command  *exec.Cmd
	done     chan error
	finished chan struct{}
	mu       sync.Mutex
	waitErr  error
}

func startS05AgentProcess(binaryPath, configPath, dockerHost, logPath string) (*s05AgentProcess, error) {
	command := exec.Command(binaryPath, "run", "--config", configPath)
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	command.Stdout = logFile
	command.Stderr = logFile
	command.Env = replaceEnvironment(os.Environ(), "DOCKER_HOST", dockerHost)
	if err := command.Start(); err != nil {
		_ = logFile.Close()
		return nil, err
	}
	process := &s05AgentProcess{command: command, done: make(chan error, 1), finished: make(chan struct{})}
	go func() {
		waitErr := command.Wait()
		_ = logFile.Close()
		process.mu.Lock()
		process.waitErr = waitErr
		process.mu.Unlock()
		close(process.finished)
		process.done <- waitErr
	}()
	return process, nil
}

func (p *s05AgentProcess) diagnostic() string {
	if p == nil || p.command == nil || p.command.Process == nil {
		return "started=false"
	}
	select {
	case <-p.finished:
		p.mu.Lock()
		err := p.waitErr
		p.mu.Unlock()
		if err != nil {
			return fmt.Sprintf("started=true pid=%d exited=true wait_error=%q", p.command.Process.Pid, err.Error())
		}
		return fmt.Sprintf("started=true pid=%d exited=true wait_error=none", p.command.Process.Pid)
	default:
		return fmt.Sprintf("started=true pid=%d exited=false", p.command.Process.Pid)
	}
}

func describeS05AgentTaskBridge(core *Server, nodeID string) string {
	connection := core.activeAgentConnectionForNode(nodeID)
	if connection == nil {
		return "active_connection=false"
	}
	journalID, synced := connection.taskBridgeState()
	return fmt.Sprintf("active_connection=true generation=%d task_capability=%t journal_hello_received=%t synchronized=%t",
		connection.generation, connection.taskEnabled, journalID != "", synced)
}

func awaitS05AgentBridgeReady(core *Server, nodeID string, previousGeneration uint64, timeout time.Duration) (uint64, bool) {
	check := func() (uint64, bool) {
		connection := core.activeAgentConnectionForNode(nodeID)
		if connection == nil || connection.generation <= previousGeneration || !core.taskBridgeReady(nodeID, connection.generation) {
			return 0, false
		}
		nodes, err := core.agents.ListNodes(context.Background())
		if err != nil {
			return 0, false
		}
		for _, node := range nodes {
			if node.NodeID == nodeID && node.Status == "online" && node.ConnectionGeneration == connection.generation {
				return connection.generation, true
			}
		}
		return 0, false
	}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if generation, ready := check(); ready {
			return generation, true
		}
		time.Sleep(25 * time.Millisecond)
	}
	connection := core.activeAgentConnectionForNode(nodeID)
	if connection != nil {
		return connection.generation, false
	}
	return 0, false
}

func describeS05JournalFile(path string) string {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Sprintf("present=false error=%q", err.Error())
	}
	return fmt.Sprintf("present=true size=%d mode=%#o", info.Size(), info.Mode().Perm())
}

func waitForS05Condition(timeout time.Duration, predicate func() bool) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if predicate() {
			return true
		}
		time.Sleep(50 * time.Millisecond)
	}
	return predicate()
}

func readS05AgentLog(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return "<unavailable: " + err.Error() + ">"
	}
	if len(data) > 16<<10 {
		data = data[len(data)-(16<<10):]
	}
	value := strings.TrimSpace(string(data))
	if value == "" {
		return "<empty>"
	}
	return value
}

func (p *s05AgentProcess) killAndWait(timeout time.Duration) error {
	if p == nil || p.command == nil || p.command.Process == nil {
		return nil
	}
	if err := p.command.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return err
	}
	select {
	case <-p.done:
		return nil
	case <-time.After(timeout):
		return errors.New("killed Agent child did not join before deadline")
	}
}

func (p *s05AgentProcess) stopAndWait(timeout time.Duration) error {
	if p == nil || p.command == nil || p.command.Process == nil {
		return nil
	}
	if err := p.command.Process.Signal(os.Interrupt); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return err
	}
	select {
	case <-p.done:
		return nil
	case <-time.After(timeout):
		_ = p.command.Process.Kill()
		select {
		case <-p.done:
			return errors.New("Agent required SIGKILL during graceful cleanup")
		case <-time.After(2 * time.Second):
			return errors.New("Agent child did not join after graceful stop and bounded kill")
		}
	}
}

func buildS05AgentBinary(t *testing.T, root, work string) string {
	t.Helper()
	goBinary := os.Getenv("NODEDANCE_GO_BIN")
	if goBinary == "" {
		goBinary = "go"
	}
	binaryPath := filepath.Join(work, "nodedance-agent-recovery-test")
	command := exec.Command(goBinary, "build", "-trimpath", "-o", binaryPath, "./cmd/nodedance-agent")
	command.Dir = root
	command.Env = replaceEnvironment(os.Environ(), "GOTOOLCHAIN", "local")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("build real Agent child process: %v: %s", err, strings.TrimSpace(string(output)))
	}
	return binaryPath
}

func replaceEnvironment(environment []string, key, value string) []string {
	result := make([]string, 0, len(environment)+1)
	prefix := key + "="
	for _, entry := range environment {
		if !strings.HasPrefix(entry, prefix) {
			result = append(result, entry)
		}
	}
	return append(result, prefix+value)
}

func cleanupS05RecoveryFixtures(t *testing.T, endpoint, suite string, fixtures map[string]string) {
	t.Helper()
	for phase, id := range fixtures {
		if err := s05VerifySuiteFixtureIdentity(endpoint, suite, id); err != nil {
			t.Errorf("refuse to remove nonmatching S05 recovery fixture phase=%s id=%s: %v", phase, id, err)
			continue
		}
		if _, err := runS04DockerCLI(endpoint, "container", "rm", "--force", id); err != nil {
			t.Errorf("remove only S05 recovery fixture phase=%s id=%s: %v", phase, id, err)
			continue
		}
		if _, err := runS04DockerCLI(endpoint, "container", "inspect", id); err == nil {
			t.Errorf("S05 recovery fixture still exists after exact removal: phase=%s id=%s", phase, id)
			continue
		}
		t.Logf("S05_RECOVERY_FIXTURE_CLEANUP suite=%s phase=%s id=%s verified_absent=true", suite, phase, id)
	}
	remaining, err := s05ListSuiteFixtureIDs(endpoint, suite)
	if err != nil || len(remaining) != 0 {
		t.Errorf("S05 recovery fixture cleanup suite=%s remaining=%v err=%v", suite, remaining, err)
	}
	t.Logf("S05_RECOVERY_MANIFEST_CLEANUP suite=%s expected_ids=%d remaining_ids=%d verified=%t", suite, len(fixtures), len(remaining), err == nil && len(remaining) == 0)
}
