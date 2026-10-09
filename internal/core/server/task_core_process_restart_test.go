package server

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/CST-Cat/NodeDance/internal/agent"
	"github.com/CST-Cat/NodeDance/internal/agent/taskjournal"
	"github.com/CST-Cat/NodeDance/internal/protocol"
	"github.com/CST-Cat/NodeDance/internal/taskstate"
)

// TestS05CoreAgentProcessKillRecoveryOnOwnedDIND kills and restarts the real
// Core process against its persistent database while a separate real Agent
// process has an operation stopped at controlled Docker request boundaries.
// The task must remain durable and must never be blindly dispatched again.
func TestS05CoreAgentProcessKillRecoveryOnOwnedDIND(t *testing.T) {
	root, endpoint, engineVersion := requireOwnedS04DIND(t)
	if engineVersion == "" {
		t.Fatal("owned Engine version was not identified")
	}
	runID := fmt.Sprintf("nd-s05-core-process-%d", time.Now().UnixNano())
	containerIDs := make(map[string]string, 3)
	t.Cleanup(func() { cleanupS05CoreRecoveryFixtures(t, endpoint, runID, containerIDs) })
	for _, phase := range []string{"before", "during", "after"} {
		name := runID + "-" + phase
		command := "n=0; if [ -f /tmp/nodedance-start-count ]; then n=$(cat /tmp/nodedance-start-count); fi; echo $((n+1)) >/tmp/nodedance-start-count; exec sleep 3600"
		id, err := runS04DockerCLI(endpoint, "container", "run", "--detach", "--name", name,
			"--label", "io.nodedance.test=true", "--label", "io.nodedance.suite="+runID,
			s04BusyboxImage, "sh", "-c", command)
		if err != nil {
			t.Fatalf("create %s Core-process recovery fixture on Engine %s: %v", phase, engineVersion, err)
		}
		id = strings.TrimSpace(id)
		if !protocol.IsFullContainerID(id) {
			t.Fatalf("owned Engine returned malformed %s fixture ID %q", phase, id)
		}
		containerIDs[phase] = id
		t.Logf("S05_CORE_RECOVERY_FIXTURE suite=%s phase=%s id=%s name=%s", runID, phase, id, name)
	}
	workRoot := filepath.Join(root, ".artifacts", "work-s05")
	if err := os.MkdirAll(workRoot, 0o700); err != nil {
		t.Fatal("create isolated S05 work root:", err)
	}
	work, err := os.MkdirTemp(workRoot, "core-process-recovery-")
	if err != nil {
		t.Fatal("create isolated Core-process recovery directory:", err)
	}
	if err := os.Chmod(work, 0o700); err != nil {
		t.Fatal(err)
	}
	var coreChild *s05CoreProcess
	var dockerProxy *s05DockerFaultProxy
	var agentChild *s05AgentProcess
	var proxyDir, ownerMarker, proxySocket string
	t.Cleanup(func() {
		if agentChild != nil {
			if err := agentChild.stopAndWait(5 * time.Second); err != nil {
				t.Errorf("stop Core-process test Agent child: %v", err)
			}
		}
		if dockerProxy != nil {
			if err := dockerProxy.Close(); err != nil {
				t.Errorf("close Core-process Docker proxy: %v", err)
			}
		}
		if proxyDir != "" {
			owner, readErr := os.ReadFile(ownerMarker)
			if readErr != nil || string(owner) != runID {
				t.Errorf("refuse to remove Docker proxy directory without this suite's owner marker: owner=%q err=%v", owner, readErr)
			} else {
				_ = os.Remove(proxySocket)
				if err := os.Remove(ownerMarker); err != nil {
					t.Errorf("remove Docker proxy owner marker: %v", err)
				} else if err := os.Remove(proxyDir); err != nil {
					t.Errorf("remove own Docker proxy directory: %v", err)
				}
			}
		}
		if coreChild != nil {
			if err := coreChild.killAndWait(4 * time.Second); err != nil {
				t.Errorf("kill Core child during cleanup: %v", err)
			}
		}
		if err := os.RemoveAll(work); err != nil {
			t.Errorf("remove own Core-process work directory: %v", err)
		}
	})

	certificate, rootPEM, err := makeAgentTestCertificate()
	if err != nil {
		t.Fatal(err)
	}
	certPath := filepath.Join(work, "core-cert.pem")
	keyPath := filepath.Join(work, "core-key.pem")
	caPath := filepath.Join(work, "trusted-ca.pem")
	if err := writeS05CoreCertificate(certPath, keyPath, caPath, certificate, rootPEM); err != nil {
		t.Fatal("write test-only Core TLS identity:", err)
	}
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal("allocate local Core test address:", err)
	}
	listenAddress := probe.Addr().String()
	_ = probe.Close()
	publicOrigin := "https://" + listenAddress
	dataDir := filepath.Join(work, "core-data")
	initializer, err := New("s05-core-process-initializer", Options{DataDir: dataDir, PublicOrigin: publicOrigin})
	if err != nil {
		t.Fatal("initialize persistent Core database:", err)
	}
	session, csrf, err := installIntegrationAdmin(initializer)
	if err != nil {
		_ = initializer.Close()
		t.Fatal("create durable test administrator session:", err)
	}
	enrollment, err := initializer.agents.CreateEnrollment(context.Background(), "S05 Core process recovery "+engineVersion,
		"127.0.0.1", sql.NullInt64{Int64: 1, Valid: true})
	if err != nil {
		_ = initializer.Close()
		t.Fatal("create one-use Agent enrollment before Core process launch:", err)
	}
	if err := initializer.Close(); err != nil {
		t.Fatal("close Core initializer before child launch:", err)
	}

	client := tlsHTTPClient(rootPEM)
	client.Timeout = 8 * time.Second
	startCore := func() {
		t.Helper()
		if coreChild != nil {
			t.Fatal("refuse to start a second Core child before joining the first")
		}
		testBinary, executableErr := os.Executable()
		if executableErr != nil {
			t.Fatal("resolve running Go test binary:", executableErr)
		}
		var startErr error
		coreChild, startErr = startS05CoreProcess(testBinary, dataDir, publicOrigin, listenAddress,
			certPath, keyPath, filepath.Join(work, fmt.Sprintf("core-process-%02d.log", time.Now().UnixNano())))
		if startErr != nil {
			t.Fatalf("start real Core child process: %v", startErr)
		}
		if err := waitForS05CoreHealth(client, publicOrigin, coreChild, 15*time.Second); err != nil {
			t.Fatalf("real Core child did not become ready: %v log=%q", err, coreChild.logText())
		}
	}
	stopCore := func() {
		t.Helper()
		if coreChild == nil {
			return
		}
		current := coreChild
		coreChild = nil
		if err := current.killAndWait(4 * time.Second); err != nil {
			t.Fatalf("SIGKILL real Core child: %v log=%q", err, current.logText())
		}
		if transport, ok := client.Transport.(*http.Transport); ok {
			transport.CloseIdleConnections()
		}
	}
	startCore()
	configPath := filepath.Join(work, "agent", "agent.json")
	if err := agent.Enroll(context.Background(), publicOrigin, caPath, false, strings.NewReader(enrollment.Token), configPath); err != nil {
		t.Fatalf("enroll Core-process recovery Agent: %v", err)
	}
	config, err := agent.LoadConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	proxyDir, err = os.MkdirTemp(os.TempDir(), "nodedance-s05-core-proxy-")
	if err != nil {
		t.Fatal("create private short-path Docker proxy directory:", err)
	}
	if err := os.Chmod(proxyDir, 0o700); err != nil {
		_ = os.Remove(proxyDir)
		t.Fatal("protect private Docker proxy directory:", err)
	}
	ownerMarker = filepath.Join(proxyDir, ".nodedance-owner")
	if err := os.WriteFile(ownerMarker, []byte(runID), 0o600); err != nil {
		_ = os.Remove(proxyDir)
		t.Fatal("write Docker proxy owner marker:", err)
	}
	proxySocket = filepath.Join(proxyDir, "docker.sock")
	dockerProxy, err = newS05DockerFaultProxy(proxySocket, endpoint)
	if err != nil {
		t.Fatal("start Core-process Docker proxy:", err)
	}
	binaryPath := buildS05AgentBinary(t, root, work)
	startAgent := func() {
		t.Helper()
		var startErr error
		agentChild, startErr = startS05AgentProcess(binaryPath, configPath, dockerProxy.Host(), filepath.Join(work, "agent-child.log"))
		if startErr != nil {
			t.Fatalf("start real Agent child: %v", startErr)
		}
	}
	startAgent()
	waitInventory := func(description string) {
		t.Helper()
		waitForS05RemoteInventory(t, client, publicOrigin, session, config.NodeID, containerIDs, 25*time.Second, description)
	}
	waitInventory("initial Agent did not publish a fresh Engine inventory")
	for phase, id := range containerIDs {
		if count, err := waitForS05ContainerStartCount(endpoint, id, 10*time.Second); err != nil || count != 1 {
			t.Fatalf("%s fixture did not start exactly once before tasks: count=%d err=%v", phase, count, err)
		}
	}

	postRestart := func(targetID, key string) string {
		t.Helper()
		deadline := time.Now().Add(20 * time.Second)
		for time.Now().Before(deadline) {
			request, err := http.NewRequest(http.MethodPost, publicOrigin+"/api/v1/nodes/"+config.NodeID+"/containers/"+targetID+"/actions", strings.NewReader(`{"action":"restart"}`))
			if err != nil {
				t.Fatal(err)
			}
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("Origin", publicOrigin)
			request.Header.Set(csrfHeaderName, csrf)
			request.Header.Set("Idempotency-Key", key)
			request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: session})
			request.AddCookie(&http.Cookie{Name: csrfCookieName, Value: csrf})
			response, err := client.Do(request)
			if err != nil {
				t.Fatal("submit Core-process recovery task:", err)
			}
			data, readErr := io.ReadAll(io.LimitReader(response.Body, 1<<20))
			_ = response.Body.Close()
			if readErr != nil {
				t.Fatal("read Core task acceptance:", readErr)
			}
			if response.StatusCode == http.StatusServiceUnavailable {
				time.Sleep(100 * time.Millisecond)
				continue
			}
			var accepted s05TaskAccepted
			if response.StatusCode != http.StatusAccepted || json.Unmarshal(data, &accepted) != nil || accepted.TaskID == "" {
				t.Fatalf("restart task returned HTTP %d body=%q", response.StatusCode, data)
			}
			return accepted.TaskID
		}
		t.Fatal("Core did not accept a task after authenticated Agent and Engine synchronization")
		return ""
	}
	waitTask := func(taskID string, timeout time.Duration, predicate func(s05TaskAPIView) bool, description string) s05TaskAPIView {
		t.Helper()
		deadline := time.Now().Add(timeout)
		var last s05TaskAPIView
		for time.Now().Before(deadline) {
			var getErr error
			last, getErr = getS05Task(client, publicOrigin, session, config.NodeID, taskID)
			if getErr == nil && predicate(last) {
				return last
			}
			time.Sleep(50 * time.Millisecond)
		}
		t.Fatalf("%s: last task=%+v", description, last)
		return s05TaskAPIView{}
	}
	assertAudit := func(taskID string, wanted ...string) {
		t.Helper()
		request, err := http.NewRequest(http.MethodGet, publicOrigin+"/api/v1/nodes/"+config.NodeID+"/tasks/"+taskID+"/audit", nil)
		if err != nil {
			t.Fatal(err)
		}
		request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: session})
		response, err := client.Do(request)
		if err != nil {
			t.Fatal("query persistent Core task audit:", err)
		}
		body, readErr := io.ReadAll(io.LimitReader(response.Body, 1<<20))
		_ = response.Body.Close()
		if readErr != nil || response.StatusCode != http.StatusOK {
			t.Fatalf("Core task audit returned %d readErr=%v body=%q", response.StatusCode, readErr, body)
		}
		var payload struct {
			Events []taskAuditEventView `json:"events"`
		}
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Fatal("decode Core task audit:", err)
		}
		seen := make(map[string]bool, len(payload.Events))
		for _, event := range payload.Events {
			seen[event.Event+":"+event.ToStatus] = true
		}
		for _, event := range wanted {
			if !seen[event] {
				t.Fatalf("Core process restart lost audit event %q for task %s: %+v", event, taskID, payload.Events)
			}
		}
	}
	assertCounter := func(targetID string, want int) {
		t.Helper()
		count, err := readS05ContainerStartCount(endpoint, targetID)
		if err != nil || count != want {
			t.Fatalf("Engine mutation count after Core restart: target=%s got=%d want=%d err=%v", targetID, count, want, err)
		}
	}

	// Core dies after Agent has durably committed a running record and reached
	// the Docker request path, but before the Engine sees the restart request.
	beforeID := containerIDs["before"]
	beforeStarted, err := runS04DockerCLI(endpoint, "container", "inspect", "--format", "{{.State.StartedAt}}", beforeID)
	if err != nil {
		t.Fatal(err)
	}
	beforeHit := dockerProxy.Arm(beforeID, s05ProxyBeforeMutation)
	beforeTask := postRestart(beforeID, "s05-core-process-before-"+runID)
	select {
	case <-beforeHit:
	case <-time.After(20 * time.Second):
		t.Fatal("Agent did not reach the Core pre-mutation crash barrier")
	}
	waitTask(beforeTask, 10*time.Second, func(task s05TaskAPIView) bool { return task.Status == "running" }, "Core did not persist running before Core SIGKILL")
	stopCore()
	startCore()
	waitInventory("Agent did not reconnect after pre-mutation Core SIGKILL")
	dockerProxy.Release()
	waitTask(beforeTask, 25*time.Second, func(task s05TaskAPIView) bool { return task.Status == "unknown" }, "Core restart did not retain uncertain pre-mutation task")
	assertAudit(beforeTask, "accepted:queued", "delivery_claimed:queued", "agent_status:running", "agent_status:unknown")
	assertCounter(beforeID, 1)
	afterBefore, err := runS04DockerCLI(endpoint, "container", "inspect", "--format", "{{.State.StartedAt}}", beforeID)
	if err != nil || strings.TrimSpace(afterBefore) != strings.TrimSpace(beforeStarted) {
		t.Fatalf("pre-mutation Core SIGKILL changed Engine StartedAt: before=%q after=%q err=%v", strings.TrimSpace(beforeStarted), strings.TrimSpace(afterBefore), err)
	}
	t.Log("S05_CORE_RECOVERY_CASE phase=before_mutation core_sigkill=true core_restart=true agent_process_alive=true task=unknown engine_mutation=false replay=false verified=true")

	// The Engine completes restart while Agent holds the HTTP response. Core is
	// killed in that uncertain interval, then must recover the durable task and
	// accept Agent's verified result without issuing another mutation.
	duringID := containerIDs["during"]
	duringCount, err := readS05ContainerStartCount(endpoint, duringID)
	if err != nil {
		t.Fatal(err)
	}
	duringStarted, err := runS04DockerCLI(endpoint, "container", "inspect", "--format", "{{.State.StartedAt}}", duringID)
	if err != nil {
		t.Fatal(err)
	}
	duringHit := dockerProxy.Arm(duringID, s05ProxyAfterMutation)
	duringTask := postRestart(duringID, "s05-core-process-during-"+runID)
	select {
	case <-duringHit:
	case <-time.After(20 * time.Second):
		t.Fatal("Agent did not complete the real Engine restart before the Core crash barrier")
	}
	duringAfter, err := runS04DockerCLI(endpoint, "container", "inspect", "--format", "{{.State.StartedAt}}", duringID)
	if err != nil || strings.TrimSpace(duringAfter) == strings.TrimSpace(duringStarted) {
		t.Fatalf("Core crash barrier arrived without a real Engine restart: before=%q after=%q err=%v", strings.TrimSpace(duringStarted), strings.TrimSpace(duringAfter), err)
	}
	assertCounter(duringID, duringCount+1)
	waitTask(duringTask, 10*time.Second, func(task s05TaskAPIView) bool { return task.Status == "running" }, "Core task was not durably running before mid-mutation Core SIGKILL")
	stopCore()
	startCore()
	waitInventory("Agent did not reconnect after post-mutation Core SIGKILL")
	dockerProxy.Release()
	waitTask(duringTask, 25*time.Second, func(task s05TaskAPIView) bool { return task.Status == "succeeded" && task.Result.Code == "verified" }, "Core did not recover Agent's post-restart verified result")
	assertAudit(duringTask, "accepted:queued", "delivery_claimed:queued", "agent_status:running", "agent_status:succeeded")
	assertCounter(duringID, duringCount+1)
	t.Log("S05_CORE_RECOVERY_CASE phase=during_mutation core_sigkill=true core_restart=true agent_process_alive=true task=succeeded engine_restart_count=1 replay=false verified=true")

	// A successful terminal task survives a later Core crash and restart; the
	// full Agent journal snapshot must not schedule it for execution again.
	afterID := containerIDs["after"]
	afterCount, err := readS05ContainerStartCount(endpoint, afterID)
	if err != nil {
		t.Fatal(err)
	}
	afterTask := postRestart(afterID, "s05-core-process-after-"+runID)
	waitTask(afterTask, 25*time.Second, func(task s05TaskAPIView) bool { return task.Status == "succeeded" && task.Result.Code == "verified" }, "post-mutation task did not reach terminal success")
	assertAudit(afterTask, "accepted:queued", "delivery_claimed:queued", "agent_status:running", "agent_status:succeeded")
	assertCounter(afterID, afterCount+1)
	stopCore()
	startCore()
	waitInventory("Agent did not reconnect after terminal-task Core SIGKILL")
	waitTask(afterTask, 10*time.Second, func(task s05TaskAPIView) bool { return task.Status == "succeeded" }, "Core restart changed terminal task status")
	assertCounter(afterID, afterCount+1)
	t.Log("S05_CORE_RECOVERY_CASE phase=after_terminal core_sigkill=true core_restart=true task=succeeded replay=false verified=true")
	assertAudit(beforeTask, "agent_status:unknown")
	assertAudit(duringTask, "agent_status:succeeded")
	assertAudit(afterTask, "agent_status:succeeded")
	if agentChild != nil {
		current := agentChild
		agentChild = nil
		if err := current.stopAndWait(5 * time.Second); err != nil {
			t.Fatalf("stop real Agent before journal audit: %v", err)
		}
	}
	journal, err := taskjournal.Open(context.Background(), filepath.Join(filepath.Dir(configPath), "tasks.sqlite"), config.NodeID)
	if err != nil {
		t.Fatal("open real Agent task journal after Core recovery suite:", err)
	}
	for taskID, want := range map[string]taskstate.Status{beforeTask: taskstate.Unknown, duringTask: taskstate.Succeeded, afterTask: taskstate.Succeeded} {
		entry, getErr := journal.Get(context.Background(), taskID)
		if getErr != nil || entry.Status != want {
			_ = journal.Close()
			t.Fatalf("Agent journal after Core process recovery: task=%s status=%s want=%s err=%v", taskID, entry.Status, want, getErr)
		}
	}
	if err := journal.Close(); err != nil {
		t.Fatal("close Agent task journal:", err)
	}
	t.Log("S05_EVIDENCE unknown_task_audit=true agent_journal_unknown=true verified=true")
	t.Log("S05_CASE S05-05 core_agent_process_restart=before,during,after verified=true")
}

// TestS05CoreProcessChildHarness is run only as a child test process of the
// integration case above. Killing that process exercises SQLite/WAL recovery
// from an actual Core process boundary instead of replacing a Server object.
func TestS05CoreProcessChildHarness(t *testing.T) {
	if os.Getenv("NODEDANCE_S05_CORE_CHILD") != "1" {
		return
	}
	dataDir := os.Getenv("NODEDANCE_S05_CORE_DATA")
	origin := os.Getenv("NODEDANCE_S05_CORE_ORIGIN")
	address := os.Getenv("NODEDANCE_S05_CORE_LISTEN")
	certPath := os.Getenv("NODEDANCE_S05_CORE_CERT")
	keyPath := os.Getenv("NODEDANCE_S05_CORE_KEY")
	if dataDir == "" || origin == "" || address == "" || certPath == "" || keyPath == "" {
		t.Fatal("Core child process is missing required test configuration")
	}
	core, err := New("s05-core-process-child", Options{DataDir: dataDir, PublicOrigin: origin})
	if err != nil {
		t.Fatal("open persistent Core database in child process:", err)
	}
	certificate, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		_ = core.Close()
		t.Fatal("load test-only Core TLS identity:", err)
	}
	listener, err := net.Listen("tcp", address)
	if err != nil {
		_ = core.Close()
		t.Fatal("listen for Core child process:", err)
	}
	secureListener := tls.NewListener(listener, &tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS12})
	server := &http.Server{Handler: core, ReadHeaderTimeout: 5 * time.Second}
	serveDone := make(chan error, 1)
	go func() { serveDone <- server.Serve(secureListener) }()
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(signals)
	select {
	case received := <-signals:
		_ = received
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = server.Shutdown(ctx)
		cancel()
		if err := core.Close(); err != nil {
			t.Fatal("close Core child process:", err)
		}
	case err := <-serveDone:
		if !errors.Is(err, http.ErrServerClosed) {
			t.Fatal("Core child HTTP server exited unexpectedly:", err)
		}
		if err := core.Close(); err != nil {
			t.Fatal("close Core child process:", err)
		}
	}
}

type s05CoreProcess struct {
	command *exec.Cmd
	done    chan error
	logPath string
}

func startS05CoreProcess(testBinary, dataDir, origin, address, certPath, keyPath, logPath string) (*s05CoreProcess, error) {
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	command := exec.Command(testBinary, "-test.run=^TestS05CoreProcessChildHarness$", "-test.v")
	command.Stdout, command.Stderr = logFile, logFile
	childEnv := os.Environ()
	for key, value := range map[string]string{
		"NODEDANCE_S05_CORE_CHILD": "1", "NODEDANCE_S05_CORE_DATA": dataDir, "NODEDANCE_S05_CORE_ORIGIN": origin,
		"NODEDANCE_S05_CORE_LISTEN": address, "NODEDANCE_S05_CORE_CERT": certPath, "NODEDANCE_S05_CORE_KEY": keyPath,
	} {
		childEnv = replaceEnvironment(childEnv, key, value)
	}
	command.Env = childEnv
	if err := command.Start(); err != nil {
		_ = logFile.Close()
		return nil, err
	}
	process := &s05CoreProcess{command: command, done: make(chan error, 1), logPath: logPath}
	go func() {
		waitErr := command.Wait()
		_ = logFile.Close()
		process.done <- waitErr
	}()
	return process, nil
}

func (p *s05CoreProcess) killAndWait(timeout time.Duration) error {
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
		return errors.New("killed Core child did not join before deadline")
	}
}

func (p *s05CoreProcess) logText() string {
	data, err := os.ReadFile(p.logPath)
	if err != nil {
		return "<unavailable: " + err.Error() + ">"
	}
	if len(data) > 16<<10 {
		data = data[len(data)-(16<<10):]
	}
	return strings.TrimSpace(string(data))
}

func writeS05CoreCertificate(certPath, keyPath, caPath string, certificate tls.Certificate, rootPEM []byte) error {
	if len(certificate.Certificate) == 0 || certificate.PrivateKey == nil {
		return errors.New("test certificate is incomplete")
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(certificate.PrivateKey)
	if err != nil {
		return err
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificate.Certificate[0]})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	for path, data := range map[string][]byte{certPath: certPEM, keyPath: keyPEM, caPath: rootPEM} {
		if err := os.WriteFile(path, data, 0o600); err != nil {
			return err
		}
	}
	return nil
}

func waitForS05CoreHealth(client *http.Client, origin string, process *s05CoreProcess, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		response, err := client.Get(origin + "/api/v1/health")
		if err == nil {
			_ = response.Body.Close()
			if response.StatusCode == http.StatusOK {
				return nil
			}
			lastErr = fmt.Errorf("health endpoint returned %d", response.StatusCode)
		} else {
			lastErr = err
		}
		select {
		case childErr := <-process.done:
			process.done <- childErr
			return fmt.Errorf("Core child exited before health endpoint: %v", childErr)
		default:
		}
		time.Sleep(50 * time.Millisecond)
	}
	return fmt.Errorf("health endpoint deadline: %w", lastErr)
}

func waitForS05RemoteInventory(t *testing.T, client *http.Client, origin, session, nodeID string,
	fixtureIDs map[string]string, timeout time.Duration, description string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var lastState string
	for time.Now().Before(deadline) {
		request, err := http.NewRequest(http.MethodGet, origin+"/api/v1/nodes/"+nodeID+"/containers", nil)
		if err != nil {
			t.Fatal(err)
		}
		request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: session})
		response, err := client.Do(request)
		if err == nil {
			var message dashboardDockerMessage
			decodeErr := json.NewDecoder(io.LimitReader(response.Body, 2<<20)).Decode(&message)
			_ = response.Body.Close()
			if response.StatusCode == http.StatusOK && decodeErr == nil && message.State != nil && message.Inventory != nil {
				lastState = fmt.Sprintf("state=%+v inventory={online:%t stale:%t fresh:%t available:%s containers:%d}",
					message.State, message.Inventory.AgentOnline, message.Inventory.DataStale,
					message.Inventory.DockerSnapshotFresh, message.Inventory.DockerAvailability, len(message.Inventory.Containers))
				allPresent := true
				for _, id := range fixtureIDs {
					if !dockerViewContains(*message.Inventory, id) {
						allPresent = false
						break
					}
				}
				if allPresent && message.Inventory.AgentOnline && !message.Inventory.DataStale &&
					message.Inventory.DockerSnapshotFresh && message.Inventory.DockerAvailability == "available" {
					return
				}
			} else {
				lastState = fmt.Sprintf("HTTP %d decode=%v", response.StatusCode, decodeErr)
			}
		} else {
			lastState = err.Error()
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("%s: last remote state %s", description, lastState)
}

func cleanupS05CoreRecoveryFixtures(t *testing.T, endpoint, suite string, fixtures map[string]string) {
	t.Helper()
	for phase, id := range fixtures {
		if err := s05VerifySuiteFixtureIdentity(endpoint, suite, id); err != nil {
			t.Errorf("refuse to remove nonmatching Core-process fixture phase=%s id=%s: %v", phase, id, err)
			continue
		}
		if _, err := runS04DockerCLI(endpoint, "container", "rm", "--force", id); err != nil {
			t.Errorf("remove exact Core-process fixture phase=%s id=%s: %v", phase, id, err)
			continue
		}
		if _, err := runS04DockerCLI(endpoint, "container", "inspect", id); err == nil {
			t.Errorf("Core-process fixture still exists after exact removal: phase=%s id=%s", phase, id)
			continue
		}
		t.Logf("S05_CORE_RECOVERY_FIXTURE_CLEANUP suite=%s phase=%s id=%s verified_absent=true", suite, phase, id)
	}
	remaining, err := s05ListSuiteFixtureIDs(endpoint, suite)
	if err != nil || len(remaining) != 0 {
		t.Errorf("Core-process fixture cleanup suite=%s remaining=%v err=%v", suite, remaining, err)
	}
	t.Logf("S05_CORE_RECOVERY_MANIFEST_CLEANUP suite=%s expected_ids=%d remaining_ids=%d verified=%t", suite, len(fixtures), len(remaining), err == nil && len(remaining) == 0)
}
