package server

import (
	"bufio"
	"context"
	"crypto/tls"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/CST-Cat/NodeDance/internal/agent"
	coredocker "github.com/CST-Cat/NodeDance/internal/core/docker"
	"github.com/CST-Cat/NodeDance/internal/protocol"
)

// This is a component integration test, not full S05 acceptance. It drives a
// real Core, enrolled Agent, authenticated task API, browser DOM, journal, and
// one owner-labelled container on the project's isolated Docker-in-Docker
// Engine. It never touches the host Docker daemon.
func TestS05CoreAgentBrowserRestartIdempotencyOnOwnedDIND(t *testing.T) {
	root, endpoint, engineVersion := requireOwnedS04DIND(t)
	t.Setenv("DOCKER_HOST", endpoint)
	imageID, err := runS04DockerCLI(endpoint, "image", "inspect", "--format", "{{.Id}}", s04BusyboxImage)
	if err != nil || strings.TrimSpace(imageID) == "" {
		if _, pullErr := runS04DockerCLI(endpoint, "pull", s04BusyboxImage); pullErr != nil {
			t.Fatalf("ensure locked S05 fixture image exists on owned DIND: inspect=%v pull=%v", err, pullErr)
		}
	}

	runID := fmt.Sprintf("nd-s05-restart-%d", time.Now().UnixNano())
	containerName := runID + "-fixture"
	command := "n=0; if [ -f /tmp/nodedance-start-count ]; then n=$(cat /tmp/nodedance-start-count); fi; echo $((n+1)) >/tmp/nodedance-start-count; trap '' TERM; while :; do sleep 1; done"
	containerID, err := runS04DockerCLI(endpoint, "container", "run", "--detach", "--name", containerName,
		"--label", "io.nodedance.test=true", "--label", "io.nodedance.suite="+runID,
		s04BusyboxImage, "sh", "-c", command)
	if err != nil {
		t.Fatalf("create one S05 restart fixture on owned Engine %s: %v", engineVersion, err)
	}
	containerID = strings.TrimSpace(containerID)
	if !protocol.IsFullContainerID(containerID) {
		t.Fatalf("owned Engine returned malformed fixture ID %q", containerID)
	}
	defer func() {
		label, inspectErr := runS04DockerCLI(endpoint, "container", "inspect", "--format", `{{ index .Config.Labels "io.nodedance.suite" }}`, containerID)
		if inspectErr != nil || strings.TrimSpace(label) != runID {
			t.Errorf("refusing to remove S05 fixture without exact ownership proof: id=%s label=%q err=%v", containerID, strings.TrimSpace(label), inspectErr)
			return
		}
		if _, removeErr := runS04DockerCLI(endpoint, "container", "rm", "--force", containerID); removeErr != nil {
			t.Errorf("remove exact owned S05 fixture %s: %v", containerID, removeErr)
		}
	}()
	initialCount, err := waitForS05ContainerStartCount(endpoint, containerID, 10*time.Second)
	if err != nil || initialCount != 1 {
		t.Fatalf("fixture did not start exactly once before NodeDance action: count=%d err=%v", initialCount, err)
	}
	initialStartedAt, err := runS04DockerCLI(endpoint, "container", "inspect", "--format", "{{.State.StartedAt}}", containerID)
	if err != nil {
		t.Fatal("read initial fixture StartedAt:", err)
	}

	workRoot := filepath.Join(root, ".artifacts", "work-s05")
	if err := os.MkdirAll(workRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	work, err := os.MkdirTemp(workRoot, "core-agent-browser-restart-")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(work, 0o700); err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(work)

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
	core, err := New("s05-task-bridge-integration", Options{DataDir: filepath.Join(work, "core"), PublicOrigin: publicOrigin})
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
			t.Errorf("close task bridge Core: %v", err)
		}
	})

	session, _, err := installIntegrationAdmin(core)
	if err != nil {
		t.Fatal(err)
	}
	enrollment, err := core.agents.CreateEnrollment(context.Background(), "S05 real restart "+engineVersion, "127.0.0.1", sql.NullInt64{Int64: 1, Valid: true})
	if err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(work, "agent", "agent.json")
	if err := agent.Enroll(context.Background(), coreHTTP.URL, caPath, false, strings.NewReader(enrollment.Token), configPath); err != nil {
		t.Fatalf("enroll Agent into real Core: %v", err)
	}
	agentConfig, err := agent.LoadConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	agentCtx, cancelAgent := context.WithCancel(context.Background())
	agentDone := make(chan error, 1)
	agentLog := &agentTestLog{}
	go func() { agentDone <- agent.Run(agentCtx, configPath, "s05-task-bridge", agentLog) }()
	defer func() {
		cancelAgent()
		select {
		case err := <-agentDone:
			if err != nil {
				t.Errorf("real Agent returned an error during cleanup: %v", err)
			}
		case <-time.After(12 * time.Second):
			t.Error("real Agent did not stop and join after task bridge integration")
		}
	}()
	waitForAgentStatusWithin(t, core, agentConfig.NodeID, "online", 0, 15*time.Second)
	waitForS04DockerView(t, core, agentConfig.NodeID, 30*time.Second, func(view coredocker.View) bool {
		return view.AgentOnline && view.DockerAvailability == "available" && view.DockerSnapshotFresh && !view.DataStale && dockerViewContains(view, containerID)
	}, "fresh Agent inventory and synchronized task bridge")
	waitForCondition(t, 10*time.Second, func() bool {
		return core.taskBridgeReady(agentConfig.NodeID, generationForNode(t, core, agentConfig.NodeID))
	}, "Agent task journal did not complete its authenticated full-snapshot handshake")

	browser := startS05TaskBrowser(t, root, filepath.Join(work, "browser-config.json"), s05TaskBrowserConfig{
		URL: coreHTTP.URL, Session: session, NodeID: agentConfig.NodeID, ContainerID: containerID, ContainerName: containerName,
	})
	opened, err := browser.Call("open")
	if err != nil || opened.ContainerFound != true {
		t.Fatalf("real browser did not render the fresh task target: result=%+v err=%v", opened, err)
	}
	clicked, err := browser.Call("clickRestart")
	if err != nil || clicked.IdempotencyKey == "" || clicked.CSRFToken == "" || !clicked.RunningVisible {
		t.Fatalf("real browser did not submit a restart and render its running progress: key_present=%t csrf_present=%t running_visible=%t err=%v",
			clicked.IdempotencyKey != "", clicked.CSRFToken != "", clicked.RunningVisible, err)
	}
	if !validS05IdempotencyKey(clicked.IdempotencyKey) {
		t.Fatalf("browser produced malformed Idempotency-Key %q", clicked.IdempotencyKey)
	}
	logf := func(format string, values ...any) { t.Logf(format, values...) }
	logf("S05_TASK_BRIDGE run=%s Engine=%s container=%s browser_key_captured=true", runID, engineVersion, containerID)

	client := tlsHTTPClient(rootPEM)
	client.Timeout = 5 * time.Second
	postRestart := func(action string) (int, s05TaskAccepted, string, error) {
		body, err := json.Marshal(map[string]string{"action": action})
		if err != nil {
			return 0, s05TaskAccepted{}, "", err
		}
		request, err := http.NewRequest(http.MethodPost,
			coreHTTP.URL+"/api/v1/nodes/"+agentConfig.NodeID+"/containers/"+containerID+"/actions", strings.NewReader(string(body)))
		if err != nil {
			return 0, s05TaskAccepted{}, "", err
		}
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Origin", coreHTTP.URL)
		request.Header.Set(csrfHeaderName, clicked.CSRFToken)
		request.Header.Set("Idempotency-Key", clicked.IdempotencyKey)
		request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: session})
		request.AddCookie(&http.Cookie{Name: csrfCookieName, Value: clicked.CSRFToken})
		response, err := client.Do(request)
		if err != nil {
			return 0, s05TaskAccepted{}, "", err
		}
		defer response.Body.Close()
		responseBody, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
		if err != nil {
			return response.StatusCode, s05TaskAccepted{}, "", err
		}
		var accepted s05TaskAccepted
		if response.StatusCode == http.StatusAccepted || response.StatusCode == http.StatusOK {
			if err := json.Unmarshal(responseBody, &accepted); err != nil {
				return response.StatusCode, accepted, string(responseBody), err
			}
		}
		return response.StatusCode, accepted, string(responseBody), nil
	}
	var taskID string
	for attempt := 0; attempt < 9; attempt++ {
		status, accepted, responseBody, err := postRestart("restart")
		if err != nil || status != http.StatusOK || accepted.TaskID == "" {
			page, listErr := core.tasks.List(context.Background(), agentConfig.NodeID, 10, nil)
			var taskStates []string
			if listErr == nil {
				for _, task := range page.Tasks {
					taskStates = append(taskStates, fmt.Sprintf("%s:%s:%s", task.TaskID, task.Status, task.DeliveryState))
				}
			}
			t.Fatalf("idempotent duplicate %d did not return existing task: status=%d task=%+v body=%q ready=%t states=%v listErr=%v agentLog=%q err=%v",
				attempt+2, status, accepted, responseBody, core.taskBridgeReady(agentConfig.NodeID, generationForNode(t, core, agentConfig.NodeID)), taskStates, listErr, agentLog.String(), err)
		}
		if taskID == "" {
			taskID = accepted.TaskID
		} else if accepted.TaskID != taskID {
			t.Fatalf("repeated identical operation returned a new task ID: first=%q current=%q", taskID, accepted.TaskID)
		}
	}
	statusView, err := browser.Call("waitSucceeded")
	if err != nil || statusView.TaskStatus != "succeeded" || !strings.Contains(statusView.StatusText, "已验证完成") {
		t.Fatalf("real browser did not render the verified terminal result: result=%+v err=%v", statusView, err)
	}
	apiTask, err := getS05Task(client, coreHTTP.URL, session, agentConfig.NodeID, taskID)
	if err != nil || apiTask.Status != "succeeded" || apiTask.Result.Code != "verified" || apiTask.Result.ObservedState != "running" {
		t.Fatalf("authenticated task result was not postcondition-verified: task=%+v err=%v", apiTask, err)
	}
	waitForS04DockerView(t, core, agentConfig.NodeID, 15*time.Second, func(view coredocker.View) bool {
		record := s04RecordForID(view, containerID)
		return view.AgentOnline && view.DockerSnapshotFresh && !view.DataStale && record.Container.ID == containerID && record.Container.Running
	}, "fresh Docker inventory after the verified restart")
	if status, _, responseBody, err := postRestart("stop"); err != nil || status != http.StatusConflict || !strings.Contains(responseBody, "task conflicts with existing state") {
		t.Fatalf("different intent with the same idempotency key returned status=%d body=%q err=%v, want idempotency conflict 409", status, responseBody, err)
	}
	finalCount, err := readS05ContainerStartCount(endpoint, containerID)
	if err != nil || finalCount != initialCount+1 {
		t.Fatalf("ten identical submissions caused %d process starts; want exactly one restart (%d→%d), err=%v", finalCount-initialCount, initialCount, finalCount, err)
	}
	finalStartedAt, err := runS04DockerCLI(endpoint, "container", "inspect", "--format", "{{.State.StartedAt}}", containerID)
	if err != nil || strings.TrimSpace(finalStartedAt) == strings.TrimSpace(initialStartedAt) {
		t.Fatalf("verified restart did not change the real Engine StartedAt: before=%q after=%q err=%v", strings.TrimSpace(initialStartedAt), strings.TrimSpace(finalStartedAt), err)
	}
	var acceptedAudits, deliveryAudits int
	if err := core.store.DB.QueryRow(`SELECT count(*) FROM core_task_audit_events WHERE task_id=? AND event='accepted'`, taskID).Scan(&acceptedAudits); err != nil {
		t.Fatal("count durable task acceptance audit rows:", err)
	}
	if err := core.store.DB.QueryRow(`SELECT count(*) FROM core_task_audit_events WHERE task_id=? AND event='delivery_claimed'`, taskID).Scan(&deliveryAudits); err != nil {
		t.Fatal("count durable task delivery audit rows:", err)
	}
	if acceptedAudits != 1 || deliveryAudits != 1 {
		t.Fatalf("duplicate submit produced duplicate durable acceptance or delivery audits: accepted=%d delivered=%d", acceptedAudits, deliveryAudits)
	}
	t.Logf("S05_TASK_BRIDGE verified one durable restart: task=%s status=%s process_starts=%d→%d audits={accepted:%d,delivery:%d}",
		taskID, apiTask.Status, initialCount, finalCount, acceptedAudits, deliveryAudits)
	if strings.Contains(agentLog.String(), "unavailable; retrying") {
		t.Fatalf("Agent transport unexpectedly reconnected during the steady task operation: %s", agentLog.String())
	}
	if err := browser.Close(); err != nil {
		t.Errorf("close real browser bridge: %v", err)
	}
}

func TestRealAgentTaskJournalHandshakeDoesNotQueueWhenDockerIsUnavailable(t *testing.T) {
	work := t.TempDir()
	if err := os.Chmod(work, 0o700); err != nil {
		t.Fatal(err)
	}
	dockerSocket := filepath.Join(work, "missing-docker.sock")
	t.Setenv("DOCKER_HOST", "unix://"+dockerSocket)
	certificate, rootPEM, err := makeAgentTestCertificate()
	if err != nil {
		t.Fatal(err)
	}
	caPath := filepath.Join(work, "trusted-ca.pem")
	if err := os.WriteFile(caPath, rootPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	core, err := New("s05-task-bridge-unavailable-docker", Options{DataDir: filepath.Join(work, "core"), PublicOrigin: "https://panel.test"})
	if err != nil {
		t.Fatal(err)
	}
	coreHTTP := httptest.NewUnstartedServer(core)
	coreHTTP.TLS = &tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS12}
	coreHTTP.StartTLS()
	t.Cleanup(func() {
		coreHTTP.Close()
		if err := core.Close(); err != nil {
			t.Errorf("close task bridge Core: %v", err)
		}
	})
	session, csrf, err := installIntegrationAdmin(core)
	if err != nil {
		t.Fatal(err)
	}
	enrollment, err := core.agents.CreateEnrollment(context.Background(), "S05 task bridge unavailable Docker", "127.0.0.1", sql.NullInt64{Int64: 1, Valid: true})
	if err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(work, "agent", "agent.json")
	if err := agent.Enroll(context.Background(), coreHTTP.URL, caPath, false, strings.NewReader(enrollment.Token), configPath); err != nil {
		t.Fatalf("enroll real Agent: %v", err)
	}
	config, err := agent.LoadConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	agentCtx, cancelAgent := context.WithCancel(context.Background())
	agentDone := make(chan error, 1)
	agentLog := &agentTestLog{}
	go func() { agentDone <- agent.Run(agentCtx, configPath, "s05-task-bridge-unavailable-docker", agentLog) }()
	t.Cleanup(func() {
		cancelAgent()
		select {
		case err := <-agentDone:
			if err != nil {
				t.Errorf("real Agent stopped with an error: %v", err)
			}
		case <-time.After(12 * time.Second):
			t.Error("Agent did not stop within the bounded cleanup deadline")
		}
	})
	waitForAgentStatusWithin(t, core, config.NodeID, "online", 0, 10*time.Second)
	view := waitForS04DockerView(t, core, config.NodeID, 15*time.Second, func(view coredocker.View) bool {
		return view.AgentOnline && view.DockerAvailability == "unavailable"
	}, "Agent did not report its isolated missing Docker socket while retaining the online lease")
	waitForCondition(t, 10*time.Second, func() bool {
		return core.taskBridgeReady(config.NodeID, view.ActiveGeneration)
	}, "real Agent did not negotiate and reconcile its durable task journal")
	var journalID string
	var reviewRequired int
	if err := core.store.DB.QueryRow(`SELECT journal_id,review_required FROM core_task_agent_state WHERE node_id=?`, config.NodeID).Scan(&journalID, &reviewRequired); err != nil || len(journalID) != 64 || reviewRequired != 0 {
		t.Fatalf("Core did not persist the Agent journal handshake: journal-length=%d review=%d err=%v", len(journalID), reviewRequired, err)
	}

	client := tlsHTTPClient(rootPEM)
	client.Timeout = 5 * time.Second
	body := strings.NewReader(`{"action":"restart"}`)
	request, err := http.NewRequest(http.MethodPost, coreHTTP.URL+"/api/v1/nodes/"+config.NodeID+"/containers/"+strings.Repeat("a", 64)+"/actions", body)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", "https://panel.test")
	request.Header.Set(csrfHeaderName, csrf)
	request.Header.Set("Idempotency-Key", "s05-unavailable-docker-test-key")
	request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: session})
	request.AddCookie(&http.Cookie{Name: csrfCookieName, Value: csrf})
	response, err := client.Do(request)
	if err != nil {
		t.Fatal("submit action while Docker is unavailable:", err)
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 1<<20))
	_ = response.Body.Close()
	if response.StatusCode != http.StatusConflict && response.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("action against unavailable Docker returned %d, want 409 or 503", response.StatusCode)
	}
	var persisted int
	if err := core.store.DB.QueryRow(`SELECT count(*) FROM core_tasks WHERE node_id=?`, config.NodeID).Scan(&persisted); err != nil {
		t.Fatal("check that offline action was not queued:", err)
	}
	if persisted != 0 {
		t.Fatalf("unavailable Docker action created %d durable task rows", persisted)
	}
	heartbeatBeforeAction := heartbeatSequenceForNode(t, core, config.NodeID)
	waitForCondition(t, 12*time.Second, func() bool {
		return heartbeatSequenceForNode(t, core, config.NodeID) > heartbeatBeforeAction
	}, "task bridge initialization blocked real Agent heartbeat")
	t.Logf("S05_TASK_BRIDGE real Core-Agent journal handshake accepted; Docker socket absent; action rejected HTTP %d with zero durable task rows; generation=%d", response.StatusCode, view.ActiveGeneration)
}

type s05TaskAccepted struct {
	TaskID string `json:"taskId"`
	Status string `json:"status"`
}

type s05TaskAPIView struct {
	TaskID string `json:"taskId"`
	Status string `json:"status"`
	Result struct {
		Code          string `json:"code"`
		ObservedState string `json:"observedState"`
	} `json:"result"`
}

func getS05Task(client *http.Client, serverURL, session, nodeID, taskID string) (s05TaskAPIView, error) {
	request, err := http.NewRequest(http.MethodGet, serverURL+"/api/v1/nodes/"+nodeID+"/tasks/"+taskID, nil)
	if err != nil {
		return s05TaskAPIView{}, err
	}
	request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: session})
	response, err := client.Do(request)
	if err != nil {
		return s05TaskAPIView{}, err
	}
	defer response.Body.Close()
	var result s05TaskAPIView
	if response.StatusCode != http.StatusOK {
		return result, fmt.Errorf("task API returned HTTP %d", response.StatusCode)
	}
	err = json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&result)
	return result, err
}

func readS05ContainerStartCount(endpoint, containerID string) (int, error) {
	value, err := runS04DockerCLI(endpoint, "container", "exec", containerID, "cat", "/tmp/nodedance-start-count")
	if err != nil {
		return 0, err
	}
	var count int
	if _, err := fmt.Sscanf(strings.TrimSpace(value), "%d", &count); err != nil || count < 1 {
		return 0, fmt.Errorf("invalid fixture start counter %q", strings.TrimSpace(value))
	}
	return count, nil
}

func waitForS05ContainerStartCount(endpoint, containerID string, timeout time.Duration) (int, error) {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		count, err := readS05ContainerStartCount(endpoint, containerID)
		if err == nil {
			return count, nil
		}
		lastErr = err
		time.Sleep(100 * time.Millisecond)
	}
	return 0, fmt.Errorf("container start marker was not available before deadline: %w", lastErr)
}

func validS05IdempotencyKey(key string) bool {
	return len(key) >= 20 && len(key) <= 128 && !strings.ContainsAny(key, "\r\n")
}

type s05TaskBrowserConfig struct {
	URL           string `json:"url"`
	Session       string `json:"session"`
	NodeID        string `json:"nodeId"`
	ContainerID   string `json:"containerId"`
	ContainerName string `json:"containerName"`
}

type s05TaskBrowserResult struct {
	ContainerFound bool   `json:"containerFound"`
	IdempotencyKey string `json:"idempotencyKey"`
	CSRFToken      string `json:"csrfToken"`
	RunningVisible bool   `json:"runningVisible"`
	TaskStatus     string `json:"taskStatus"`
	StatusText     string `json:"statusText"`
}

type s05TaskBrowserResponse struct {
	ID     int                  `json:"id"`
	Result s05TaskBrowserResult `json:"result"`
	Error  string               `json:"error"`
}

type s05TaskBrowser struct {
	cmd        *exec.Cmd
	stdin      io.WriteCloser
	writer     *bufio.Writer
	stdout     *bufio.Scanner
	seq        int
	configPath string
}

func startS05TaskBrowser(t *testing.T, root, configPath string, config s05TaskBrowserConfig) *s05TaskBrowser {
	t.Helper()
	encoded, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	node := filepath.Join(root, ".tools", "node-v22.23.3", "bin", "node")
	if info, err := os.Stat(node); err != nil || !info.Mode().IsRegular() {
		t.Fatalf("locked Node.js 22 test runtime is required for real browser verification: %s (%v)", node, err)
	}
	script := filepath.Join(root, "web", "tests", "s05-task-browser.mjs")
	commandCtx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	cmd := exec.CommandContext(commandCtx, node, script, configPath)
	cmd.Dir = filepath.Join(root, "web")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		cancel()
		t.Fatalf("start the locked real-browser task harness: %v", err)
	}
	bridge := &s05TaskBrowser{cmd: cmd, stdin: stdin, writer: bufio.NewWriter(stdin), stdout: bufio.NewScanner(stdout), configPath: configPath}
	bridge.stdout.Buffer(make([]byte, 4096), 1<<20)
	t.Cleanup(func() {
		bridge.Close()
		cancel()
	})
	return bridge
}

func (b *s05TaskBrowser) Call(action string) (s05TaskBrowserResult, error) {
	b.seq++
	data, err := json.Marshal(map[string]any{"id": b.seq, "action": action})
	if err != nil {
		return s05TaskBrowserResult{}, err
	}
	if _, err := b.writer.Write(append(data, '\n')); err != nil {
		return s05TaskBrowserResult{}, err
	}
	if err := b.writer.Flush(); err != nil {
		return s05TaskBrowserResult{}, err
	}
	if !b.stdout.Scan() {
		if err := b.stdout.Err(); err != nil {
			return s05TaskBrowserResult{}, err
		}
		return s05TaskBrowserResult{}, errors.New("real browser task harness exited without a response")
	}
	var response s05TaskBrowserResponse
	if err := json.Unmarshal(b.stdout.Bytes(), &response); err != nil {
		return s05TaskBrowserResult{}, err
	}
	if response.ID != b.seq {
		return response.Result, fmt.Errorf("browser response ID=%d, want %d", response.ID, b.seq)
	}
	if response.Error != "" {
		return response.Result, errors.New(response.Error)
	}
	return response.Result, nil
}

func (b *s05TaskBrowser) Close() error {
	if b == nil || b.cmd == nil {
		return nil
	}
	_, _ = b.Call("close")
	if b.stdin != nil {
		_ = b.stdin.Close()
	}
	done := make(chan error, 1)
	go func() { done <- b.cmd.Wait() }()
	var err error
	select {
	case err = <-done:
	case <-time.After(10 * time.Second):
		_ = b.cmd.Process.Kill()
		select {
		case err = <-done:
		case <-time.After(2 * time.Second):
			err = errors.New("real browser child did not join after kill")
		}
	}
	b.cmd = nil
	_ = os.Remove(b.configPath)
	return err
}
