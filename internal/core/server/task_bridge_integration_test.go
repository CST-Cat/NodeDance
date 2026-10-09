package server

import (
	"bufio"
	"context"
	"crypto/tls"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
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
	"github.com/CST-Cat/NodeDance/internal/protocol"
	modernsqlite "modernc.org/sqlite"
)

var (
	s05JournalInsertAttemptFunctionOnce sync.Once
	s05JournalInsertAttemptFunctionErr  error
	s05JournalInsertAttemptCount        atomic.Int64
)

// This is a component integration test, not full S05 acceptance. It drives a
// real Core, enrolled Agent, authenticated task API, browser DOM, journal, and
// one owner-labelled container on the project's isolated Docker-in-Docker
// Engine. It never touches the host Docker daemon.
func TestS05CoreAgentBrowserRestartIdempotencyOnOwnedDIND(t *testing.T) {
	if err := ensureS05JournalInsertAttemptFunction(); err != nil {
		t.Fatal("register non-transactional Agent journal insert attempt observer:", err)
	}
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
	fixtures := newS05DINDFixtureManifest(endpoint, runID)
	fixtures.add(t, "main", containerID)
	defer func() { fixtures.cleanup(t) }()
	t.Logf("S05_FIXTURE suite=%s kind=main id=%s name=%s", runID, containerID, containerName)
	var composeID string
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
	proxyRoot, err := os.MkdirTemp("/tmp", "nd-s05-journal-")
	if err != nil {
		t.Fatal("create owner-scoped short Docker proxy directory:", err)
	}
	if err := os.Chmod(proxyRoot, 0o700); err != nil {
		_ = os.RemoveAll(proxyRoot)
		t.Fatal("restrict owner-scoped Docker proxy directory:", err)
	}
	proxyOwner := runID + "\n"
	proxyOwnerPath := filepath.Join(proxyRoot, "owner.json")
	if err := os.WriteFile(proxyOwnerPath, []byte(proxyOwner), 0o600); err != nil {
		_ = os.RemoveAll(proxyRoot)
		t.Fatal("write Docker proxy owner marker:", err)
	}
	dockerProxy, err := newS05CancellationBarrierProxy(filepath.Join(proxyRoot, "engine.sock"), endpoint)
	if err != nil {
		_ = os.RemoveAll(proxyRoot)
		t.Fatal("start owner-scoped Docker mutation counter proxy:", err)
	}
	dockerProxy.EnableEngineForwarding()
	defer func() {
		owner, readErr := os.ReadFile(proxyOwnerPath)
		if readErr != nil || string(owner) != proxyOwner {
			t.Errorf("refuse cleanup of Docker proxy directory without matching owner marker: owner=%q err=%v", owner, readErr)
			return
		}
		info, statErr := os.Lstat(proxyRoot)
		if statErr != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			t.Errorf("refuse cleanup of unexpected Docker proxy directory: info=%v err=%v", info, statErr)
			return
		}
		if err := os.RemoveAll(proxyRoot); err != nil {
			t.Errorf("remove owner-scoped Docker proxy directory: %v", err)
		}
	}()
	defer func() {
		if err := dockerProxy.Close(); err != nil {
			t.Errorf("close Docker mutation counter proxy: %v", err)
		}
	}()
	t.Setenv("DOCKER_HOST", dockerProxy.Host())
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
	postTaskForTarget := func(targetID, action, idempotencyKey string, fields map[string]any) (int, s05TaskAccepted, string, error) {
		payload := map[string]any{"action": action}
		for key, value := range fields {
			payload[key] = value
		}
		body, err := json.Marshal(payload)
		if err != nil {
			return 0, s05TaskAccepted{}, "", err
		}
		request, err := http.NewRequest(http.MethodPost,
			coreHTTP.URL+"/api/v1/nodes/"+agentConfig.NodeID+"/containers/"+targetID+"/actions", strings.NewReader(string(body)))
		if err != nil {
			return 0, s05TaskAccepted{}, "", err
		}
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Origin", coreHTTP.URL)
		request.Header.Set(csrfHeaderName, clicked.CSRFToken)
		request.Header.Set("Idempotency-Key", idempotencyKey)
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
	postTask := func(action, idempotencyKey string, fields map[string]any) (int, s05TaskAccepted, string, error) {
		return postTaskForTarget(containerID, action, idempotencyKey, fields)
	}
	postRestart := func(action string) (int, s05TaskAccepted, string, error) {
		return postTask(action, clicked.IdempotencyKey, nil)
	}
	for _, conflicting := range []struct {
		action string
		fields map[string]any
	}{
		{action: "stop"},
		{action: "restart"},
		{action: "delete", fields: map[string]any{"deleteConfirmed": true, "deleteConfirmationId": containerID}},
	} {
		status, _, body, err := postTask(conflicting.action, "s05-conflict-"+conflicting.action+"-"+fmt.Sprint(time.Now().UnixNano()), conflicting.fields)
		if err != nil || status != http.StatusConflict {
			t.Fatalf("conflicting %s while restart claim is held returned %d body=%q err=%v, want 409", conflicting.action, status, body, err)
		}
	}
	t.Log("S05_CASE S05-04 in_flight_resource_conflicts=stop,restart,delete verified=true")
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
	auditRequest, err := http.NewRequest(http.MethodGet, coreHTTP.URL+"/api/v1/nodes/"+agentConfig.NodeID+"/tasks/"+taskID+"/audit", nil)
	if err != nil {
		t.Fatal("create authenticated task audit query:", err)
	}
	auditRequest.AddCookie(&http.Cookie{Name: sessionCookieName, Value: session})
	auditResponse, err := client.Do(auditRequest)
	if err != nil {
		t.Fatal("query task audit API:", err)
	}
	auditBody, readErr := io.ReadAll(io.LimitReader(auditResponse.Body, 1<<20))
	_ = auditResponse.Body.Close()
	if readErr != nil || auditResponse.StatusCode != http.StatusOK {
		t.Fatalf("task audit query returned %d, read error=%v body=%q", auditResponse.StatusCode, readErr, auditBody)
	}
	var auditPayload struct {
		Events []taskAuditEventView `json:"events"`
	}
	if err := json.Unmarshal(auditBody, &auditPayload); err != nil {
		t.Fatal("decode task audit response:", err)
	}
	seenAudit := map[string]bool{}
	for _, event := range auditPayload.Events {
		seenAudit[event.Event+":"+event.ToStatus] = true
	}
	for _, required := range []string{"accepted:queued", "delivery_claimed:queued", "agent_status:running", "agent_status:succeeded"} {
		if !seenAudit[required] {
			t.Fatalf("task audit history omitted %q: %+v", required, auditPayload.Events)
		}
	}
	t.Log("S05_EVIDENCE success_task_audit=true verified=true")
	if strings.Contains(string(auditBody), clicked.IdempotencyKey) || strings.Contains(string(auditBody), "requestDigest") || strings.Contains(string(auditBody), "intentJson") {
		t.Fatalf("audit response exposed request identity material: %s", auditBody)
	}
	waitForS04DockerView(t, core, agentConfig.NodeID, 15*time.Second, func(view coredocker.View) bool {
		record := s04RecordForID(view, containerID)
		return view.AgentOnline && view.DockerSnapshotFresh && !view.DataStale && record.Container.ID == containerID && record.Container.Running
	}, "fresh Docker inventory after the verified restart")
	if status, _, responseBody, err := postRestart("stop"); err != nil || status != http.StatusConflict || !strings.Contains(responseBody, "task conflicts with existing state") {
		t.Fatalf("different intent with the same idempotency key returned status=%d body=%q err=%v, want idempotency conflict 409", status, responseBody, err)
	}
	t.Log("S05_CASE S05-03 same_key_different_intent=409 verified=true")
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
	t.Log("S05_CASE S05-02 identical_submissions=10 actual_restarts=1 verified=true")

	// Exercise the whole Core -> authenticated Agent bridge -> real Engine
	// lifecycle set with unique idempotency keys. The dedicated containeractions
	// DIND suite tests executor internals; these calls prove the integrated wire
	// and Core task state for each supported action.
	performAPIAction := func(action string, fields map[string]any) s05TaskAPIView {
		t.Helper()
		key := fmt.Sprintf("s05-%s-%d", action, time.Now().UnixNano())
		status, accepted, body, err := postTask(action, key, fields)
		if err != nil || status != http.StatusAccepted || accepted.TaskID == "" {
			t.Fatalf("submit %s task returned %d body=%q accepted=%+v err=%v", action, status, body, accepted, err)
		}
		deadline := time.Now().Add(35 * time.Second)
		for time.Now().Before(deadline) {
			task, getErr := getS05Task(client, coreHTTP.URL, session, agentConfig.NodeID, accepted.TaskID)
			if getErr != nil {
				t.Fatalf("read %s task result: %v", action, getErr)
			}
			if task.Status == "succeeded" || task.Status == "failed" || task.Status == "unknown" || task.Status == "timed_out" || task.Status == "canceled" {
				if task.Status != "succeeded" || task.Result.Code != "verified" {
					t.Fatalf("%s task did not reach a verified success: %+v", action, task)
				}
				return task
			}
			time.Sleep(100 * time.Millisecond)
		}
		t.Fatalf("%s task did not complete before deadline; task_id=%s", action, accepted.TaskID)
		return s05TaskAPIView{}
	}
	performAPIAction("stop", nil)
	if got, err := runS04DockerCLI(endpoint, "container", "inspect", "--format", "{{.State.Running}}", containerID); err != nil || strings.TrimSpace(got) != "false" {
		t.Fatalf("Core-Agent stop did not change real Engine state: state=%q err=%v", strings.TrimSpace(got), err)
	}
	performAPIAction("start", nil)
	if got, err := runS04DockerCLI(endpoint, "container", "inspect", "--format", "{{.State.Running}}", containerID); err != nil || strings.TrimSpace(got) != "true" {
		t.Fatalf("Core-Agent start did not change real Engine state: state=%q err=%v", strings.TrimSpace(got), err)
	}
	performAPIAction("pause", nil)
	if got, err := runS04DockerCLI(endpoint, "container", "inspect", "--format", "{{.State.Paused}}", containerID); err != nil || strings.TrimSpace(got) != "true" {
		t.Fatalf("Core-Agent pause did not change real Engine state: state=%q err=%v", strings.TrimSpace(got), err)
	}
	performAPIAction("resume", nil)
	if got, err := runS04DockerCLI(endpoint, "container", "inspect", "--format", "{{.State.Paused}}", containerID); err != nil || strings.TrimSpace(got) != "false" {
		t.Fatalf("Core-Agent resume did not change real Engine state: state=%q err=%v", strings.TrimSpace(got), err)
	}
	renamed := runID + "-renamed"
	performAPIAction("rename", map[string]any{"newName": renamed})
	if got, err := runS04DockerCLI(endpoint, "container", "inspect", "--format", "{{.Name}}", containerID); err != nil || strings.TrimSpace(got) != "/"+renamed {
		t.Fatalf("Core-Agent independent rename result=%q err=%v", strings.TrimSpace(got), err)
	}
	composeName := runID + "-compose"
	composeID, err = runS04DockerCLI(endpoint, "container", "run", "--detach", "--name", composeName,
		"--label", "io.nodedance.test=true", "--label", "io.nodedance.suite="+runID,
		"--label", "com.docker.compose.project=nodedance_s05_"+runID, "--label", "com.docker.compose.service=web",
		s04BusyboxImage, "sh", "-c", "while :; do sleep 1; done")
	if err != nil {
		t.Fatal("create owned Compose-labelled fixture:", err)
	}
	composeID = strings.TrimSpace(composeID)
	if !protocol.IsFullContainerID(composeID) {
		t.Fatalf("Compose fixture returned malformed full ID %q", composeID)
	}
	fixtures.add(t, "compose", composeID)
	t.Logf("S05_FIXTURE suite=%s kind=compose id=%s name=%s", runID, composeID, composeName)
	waitForS04DockerView(t, core, agentConfig.NodeID, 20*time.Second, func(view coredocker.View) bool {
		record := s04RecordForID(view, composeID)
		return view.AgentOnline && view.DockerSnapshotFresh && !view.DataStale && record.Container.ID == composeID && record.Container.Compose != nil
	}, "Core did not derive Compose ownership from the real Agent inventory")
	var taskCountBefore int
	if err := core.store.DB.QueryRow(`SELECT count(*) FROM core_tasks`).Scan(&taskCountBefore); err != nil {
		t.Fatal("count tasks before Core storage failure injection:", err)
	}
	startedBeforeStorageFailure, err := runS04DockerCLI(endpoint, "container", "inspect", "--format", "{{.State.StartedAt}}", composeID)
	if err != nil {
		t.Fatal("inspect Compose fixture before Core storage failure injection:", err)
	}
	startsBeforeStorageFailure, err := readS05ContainerStartCount(endpoint, containerID)
	if err != nil {
		t.Fatal("read main fixture process count before Core storage failure injection:", err)
	}
	failureWindowStart := time.Now().UTC()
	if _, err := core.store.DB.Exec(`CREATE TRIGGER nodedance_s05_fail_task_insert BEFORE INSERT ON core_tasks BEGIN SELECT RAISE(ABORT, 'injected Core task insert failure'); END`); err != nil {
		t.Fatal("install targeted Core task transaction failure injection:", err)
	}
	storageFailureStatus, _, storageFailureBody, storageFailureRequestErr := postTaskForTarget(
		composeID, "restart", "s05-core-readonly-"+fmt.Sprint(time.Now().UnixNano()), nil)
	_, restoreStorageErr := core.store.DB.Exec(`DROP TRIGGER nodedance_s05_fail_task_insert`)
	if restoreStorageErr != nil {
		t.Fatal("remove targeted Core task transaction failure injection:", restoreStorageErr)
	}
	if storageFailureRequestErr != nil || storageFailureStatus != http.StatusInternalServerError {
		t.Fatalf("Core task insert failure returned status=%d body=%q err=%v, want 500 before dispatch", storageFailureStatus, storageFailureBody, storageFailureRequestErr)
	}
	var taskCountAfter int
	if err := core.store.DB.QueryRow(`SELECT count(*) FROM core_tasks`).Scan(&taskCountAfter); err != nil || taskCountAfter != taskCountBefore {
		t.Fatalf("failed Core task transaction left task rows: before=%d after=%d err=%v", taskCountBefore, taskCountAfter, err)
	}
	startedAfterStorageFailure, err := runS04DockerCLI(endpoint, "container", "inspect", "--format", "{{.State.StartedAt}}", composeID)
	if err != nil || strings.TrimSpace(startedAfterStorageFailure) != strings.TrimSpace(startedBeforeStorageFailure) {
		t.Fatalf("Core SQLite write failure reached Docker or changed the fixture: before=%q after=%q err=%v", strings.TrimSpace(startedBeforeStorageFailure), strings.TrimSpace(startedAfterStorageFailure), err)
	}
	startsAfterStorageFailure, err := readS05ContainerStartCount(endpoint, containerID)
	if err != nil || startsAfterStorageFailure != startsBeforeStorageFailure {
		t.Fatalf("failed Core task transaction restarted the main Engine fixture: starts_before=%d starts_after=%d err=%v", startsBeforeStorageFailure, startsAfterStorageFailure, err)
	}
	failureWindowEnd := time.Now().UTC()
	engineEvents, err := runS04DockerCLI(endpoint, "events", "--since", failureWindowStart.Format(time.RFC3339Nano),
		"--until", failureWindowEnd.Format(time.RFC3339Nano), "--filter", "container="+composeID, "--format", "{{.Action}}")
	if err != nil || strings.TrimSpace(engineEvents) != "" {
		t.Fatalf("Core task insert failure produced Engine events for the Compose target: events=%q err=%v", strings.TrimSpace(engineEvents), err)
	}
	t.Log("S05_EVIDENCE core_task_insert_failure=500 task_rows_unchanged=true docker_started_at_unchanged=true docker_start_count_unchanged=true docker_events_unchanged=true verified=true")
	if status, _, body, err := postTaskForTarget(composeID, "rename", "s05-compose-rename-"+fmt.Sprint(time.Now().UnixNano()), map[string]any{"newName": runID + "-compose-renamed"}); err != nil || status != http.StatusConflict {
		t.Fatalf("Compose container rename returned %d body=%q err=%v, want Core conflict", status, body, err)
	}
	if got, err := runS04DockerCLI(endpoint, "container", "inspect", "--format", "{{.Name}}", composeID); err != nil || strings.TrimSpace(got) != "/"+composeName {
		t.Fatalf("rejected Compose rename changed the Engine name: name=%q err=%v", strings.TrimSpace(got), err)
	}
	t.Log("S05_CASE S05-08 independent_rename=succeeded compose_rename=409 verified=true")
	if status, _, body, err := postTask("delete", "s05-delete-wrong-id-"+fmt.Sprint(time.Now().UnixNano()), map[string]any{
		"deleteConfirmed": true, "deleteConfirmationId": strings.Repeat("b", 64),
	}); err != nil || status != http.StatusBadRequest {
		t.Fatalf("delete confirmation for a different target returned %d body=%q err=%v, want 400", status, body, err)
	}
	runningDeleteKey := "s05-delete-running-" + fmt.Sprint(time.Now().UnixNano())
	status, acceptedDelete, body, err := postTask("delete", runningDeleteKey, map[string]any{
		"deleteConfirmed": true, "deleteConfirmationId": containerID,
	})
	if err != nil || status != http.StatusAccepted {
		t.Fatalf("submit exact-target running delete returned %d body=%q err=%v", status, body, err)
	}
	deadline := time.Now().Add(20 * time.Second)
	var runningDelete s05TaskAPIView
	for time.Now().Before(deadline) {
		runningDelete, err = getS05Task(client, coreHTTP.URL, session, agentConfig.NodeID, acceptedDelete.TaskID)
		if err != nil {
			t.Fatalf("read running-delete refusal: %v", err)
		}
		if runningDelete.Status == "failed" || runningDelete.Status == "succeeded" || runningDelete.Status == "unknown" {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if runningDelete.Status != "failed" {
		t.Fatalf("running delete result=%+v, want a confirmed refusal", runningDelete)
	}
	auditURL := coreHTTP.URL + "/api/v1/nodes/" + agentConfig.NodeID + "/tasks/" + acceptedDelete.TaskID + "/audit"
	auditRequest, err = http.NewRequest(http.MethodGet, auditURL, nil)
	if err != nil {
		t.Fatal("create failed-task audit query:", err)
	}
	auditRequest.AddCookie(&http.Cookie{Name: sessionCookieName, Value: session})
	auditResponse, err = client.Do(auditRequest)
	if err != nil {
		t.Fatal("query failed-task audit API:", err)
	}
	failedAuditBody, failedAuditReadErr := io.ReadAll(io.LimitReader(auditResponse.Body, 1<<20))
	_ = auditResponse.Body.Close()
	if failedAuditReadErr != nil || auditResponse.StatusCode != http.StatusOK {
		t.Fatalf("failed-task audit query returned %d, read error=%v body=%q", auditResponse.StatusCode, failedAuditReadErr, failedAuditBody)
	}
	var failedAuditPayload struct {
		Events []taskAuditEventView `json:"events"`
	}
	if err := json.Unmarshal(failedAuditBody, &failedAuditPayload); err != nil {
		t.Fatal("decode failed-task audit response:", err)
	}
	failedTransitions := make(map[string]bool, len(failedAuditPayload.Events))
	for _, event := range failedAuditPayload.Events {
		failedTransitions[event.Event+":"+event.ToStatus] = true
	}
	for _, required := range []string{"accepted:queued", "delivery_claimed:queued", "agent_status:failed"} {
		if !failedTransitions[required] {
			t.Fatalf("failed task audit omitted %q: %+v", required, failedAuditPayload.Events)
		}
	}
	if strings.Contains(string(failedAuditBody), runningDeleteKey) || strings.Contains(string(failedAuditBody), "requestDigest") || strings.Contains(string(failedAuditBody), "intentJson") {
		t.Fatalf("failed-task audit exposed request identity material: %s", failedAuditBody)
	}
	t.Log("S05_EVIDENCE failed_task_audit=true secret_redacted=true verified=true")
	// Force a genuine Agent task-journal insert failure for a uniquely keyed
	// restart. Core delivery is already durable, but the Agent must reject the
	// task before Docker mutation, reconnect with the same journal, and leave the
	// uncertain task claimed instead of replaying it.
	agentJournalPath := filepath.Join(filepath.Dir(configPath), "tasks.sqlite")
	agentJournalURL := (&url.URL{Scheme: "file", Path: agentJournalPath}).String()
	agentJournalDB, err := sql.Open("sqlite", agentJournalURL)
	if err != nil {
		t.Fatal("open Agent task journal for targeted failure injection:", err)
	}
	if _, err := agentJournalDB.Exec(`CREATE TRIGGER nodedance_s05_fail_agent_task_enqueue BEFORE INSERT ON task_journal
		WHEN NEW.idempotency_key LIKE 's05-agent-journal-failure-%'
		BEGIN SELECT nodedance_s05_note_journal_insert_attempt(NEW.task_id);
		SELECT RAISE(ABORT, 'injected Agent task journal insert failure'); END`); err != nil {
		_ = agentJournalDB.Close()
		t.Fatal("install targeted Agent task journal failure trigger:", err)
	}
	defer func() {
		_, _ = agentJournalDB.Exec(`DROP TRIGGER IF EXISTS nodedance_s05_fail_agent_task_enqueue`)
		_ = agentJournalDB.Close()
	}()
	journalFailureKey := "s05-agent-journal-failure-" + fmt.Sprint(time.Now().UnixNano())
	journalFailureStartedAt, err := runS04DockerCLI(endpoint, "container", "inspect", "--format", "{{.State.StartedAt}}", containerID)
	if err != nil {
		_, _ = agentJournalDB.Exec(`DROP TRIGGER IF EXISTS nodedance_s05_fail_agent_task_enqueue`)
		_ = agentJournalDB.Close()
		t.Fatal("inspect Engine before Agent journal failure injection:", err)
	}
	journalFailureStartCount, err := readS05ContainerStartCount(endpoint, containerID)
	if err != nil {
		_, _ = agentJournalDB.Exec(`DROP TRIGGER IF EXISTS nodedance_s05_fail_agent_task_enqueue`)
		_ = agentJournalDB.Close()
		t.Fatal("read Engine start count before Agent journal failure injection:", err)
	}
	journalFailureEventsSince := time.Now().UTC()
	dockerProxy.ArmTargets([]string{containerID})
	agentMutationCallsBefore := dockerProxy.RestartHitCount(containerID)
	engineMutationCallsBefore := dockerProxy.ForwardedRestartCount(containerID)
	agentJournalInsertAttemptsBefore := s05JournalInsertAttemptCount.Load()
	previousAgentGeneration := generationForNode(t, core, agentConfig.NodeID)
	previousConnection := core.activeAgentConnectionForNode(agentConfig.NodeID)
	if previousConnection == nil || previousConnection.generation != previousAgentGeneration {
		t.Fatalf("journal failure injection requires the current Agent connection: expected_generation=%d active=%t",
			previousAgentGeneration, previousConnection != nil)
	}
	previousJournalID, previousJournalSynced := previousConnection.taskBridgeState()
	if !previousJournalSynced || previousJournalID == "" {
		t.Fatalf("journal failure injection requires a fully synchronized journal: id_present=%t synced=%t", previousJournalID != "", previousJournalSynced)
	}
	agentLogBeforeJournalFailure := agentLog.String()
	if strings.Contains(agentLogBeforeJournalFailure, "Agent connection unavailable; retrying") {
		t.Fatalf("Agent had already retried its connection before journal failure injection: %q", agentLogBeforeJournalFailure)
	}
	status, journalFailureTask, body, err := postTaskForTarget(containerID, "restart", journalFailureKey, nil)
	if err != nil || status != http.StatusAccepted || journalFailureTask.TaskID == "" {
		_, _ = agentJournalDB.Exec(`DROP TRIGGER IF EXISTS nodedance_s05_fail_agent_task_enqueue`)
		_ = agentJournalDB.Close()
		t.Fatalf("Core did not durably accept task for Agent-journal failure test: status=%d task=%+v body=%q err=%v", status, journalFailureTask, body, err)
	}
	waitForCondition(t, 25*time.Second, func() bool {
		task, getErr := core.tasks.Get(context.Background(), agentConfig.NodeID, journalFailureTask.TaskID)
		return getErr == nil && task.Status == "unknown" && task.Result.Code == "result_pending" && task.ReconciliationRequired
	}, "Core did not preserve uncertain delivered task after Agent journal insert failure")
	var agentTaskRows int
	if err := agentJournalDB.QueryRow(`SELECT count(*) FROM task_journal WHERE task_id=? OR idempotency_key=?`, journalFailureTask.TaskID, journalFailureKey).Scan(&agentTaskRows); err != nil || agentTaskRows != 0 {
		_, _ = agentJournalDB.Exec(`DROP TRIGGER IF EXISTS nodedance_s05_fail_agent_task_enqueue`)
		_ = agentJournalDB.Close()
		t.Fatalf("failed Agent journal transaction left a task row: rows=%d err=%v", agentTaskRows, err)
	}
	if _, err := agentJournalDB.Exec(`DROP TRIGGER nodedance_s05_fail_agent_task_enqueue`); err != nil {
		_ = agentJournalDB.Close()
		t.Fatal("remove targeted Agent journal failure trigger:", err)
	}
	if err := agentJournalDB.Close(); err != nil {
		t.Fatal("close Agent journal failure injector:", err)
	}
	newAgentGeneration, reconnected := awaitS05AgentBridgeReady(core, agentConfig.NodeID, previousAgentGeneration, 25*time.Second)
	if !reconnected || newAgentGeneration <= previousAgentGeneration {
		t.Fatalf("real Agent did not reconnect and synchronize after journal write failure: generation=%d active=%s logs=%q",
			previousAgentGeneration, describeS05AgentTaskBridge(core, agentConfig.NodeID), agentLog.String())
	}
	newConnection := core.activeAgentConnectionForNode(agentConfig.NodeID)
	if newConnection == nil || newConnection.generation != newAgentGeneration {
		t.Fatalf("reconnected Agent connection was not the synchronized generation %d", newAgentGeneration)
	}
	newJournalID, newJournalSynced := newConnection.taskBridgeState()
	if !newJournalSynced || newJournalID != previousJournalID {
		t.Fatalf("Agent journal failure changed journal identity or did not resynchronize: before=%q after=%q synced=%t",
			previousJournalID, newJournalID, newJournalSynced)
	}
	waitForS04DockerView(t, core, agentConfig.NodeID, 20*time.Second, func(view coredocker.View) bool {
		return view.ActiveGeneration == newAgentGeneration && view.AgentOnline && !view.DataStale && view.DockerSnapshotFresh &&
			view.DockerAvailability == protocol.DockerAvailabilityAvailable && dockerViewContains(view, containerID)
	}, "fresh inventory did not return after Agent journal insert failure")
	journalFailureTaskState, err := core.tasks.Get(context.Background(), agentConfig.NodeID, journalFailureTask.TaskID)
	if err != nil || journalFailureTaskState.Status != "unknown" || journalFailureTaskState.Result.Code != "result_pending" ||
		!journalFailureTaskState.ReconciliationRequired || !journalFailureTaskState.Evidence.DeliveryCommitted {
		t.Fatalf("Agent journal failure did not retain an unresolved Core claim: task=%+v err=%v", journalFailureTaskState, err)
	}
	var retainedClaims int
	if err := core.store.DB.QueryRow(`SELECT count(*) FROM core_task_resource_claims WHERE node_id=? AND task_id=?`, agentConfig.NodeID, journalFailureTask.TaskID).Scan(&retainedClaims); err != nil || retainedClaims != 1 {
		t.Fatalf("Agent journal failure released its unresolved resource claim: claims=%d err=%v", retainedClaims, err)
	}
	journalRowsByKey, err := s05AgentJournalCountByIdempotencyKey(agentJournalPath, journalFailureKey)
	if err != nil || journalRowsByKey != 0 {
		t.Fatalf("failed Agent journal write reappeared after reconnection: rows=%d err=%v", journalRowsByKey, err)
	}
	journalFailureStartedAfter, err := runS04DockerCLI(endpoint, "container", "inspect", "--format", "{{.State.StartedAt}}", containerID)
	if err != nil || strings.TrimSpace(journalFailureStartedAfter) != strings.TrimSpace(journalFailureStartedAt) {
		t.Fatalf("Agent journal write failure changed Docker StartedAt: before=%q after=%q err=%v", strings.TrimSpace(journalFailureStartedAt), strings.TrimSpace(journalFailureStartedAfter), err)
	}
	journalFailureStartCountAfter, err := readS05ContainerStartCount(endpoint, containerID)
	if err != nil || journalFailureStartCountAfter != journalFailureStartCount {
		t.Fatalf("Agent journal failure executed restart before its durable record: starts_before=%d after=%d err=%v", journalFailureStartCount, journalFailureStartCountAfter, err)
	}
	journalFailureEvents, err := runS04DockerCLI(endpoint, "events", "--since", journalFailureEventsSince.Format(time.RFC3339Nano),
		"--until", time.Now().UTC().Add(250*time.Millisecond).Format(time.RFC3339Nano), "--filter", "container="+containerID, "--format", "{{.Action}}")
	if err != nil || strings.TrimSpace(journalFailureEvents) != "" {
		t.Fatalf("Agent journal failure emitted Docker events: events=%q err=%v", strings.TrimSpace(journalFailureEvents), err)
	}
	journalFailureAudit, err := core.tasks.AuditEvents(context.Background(), agentConfig.NodeID, journalFailureTask.TaskID)
	if err != nil {
		t.Fatal("read Agent-journal-failure task audit:", err)
	}
	unknownAudit := false
	for _, event := range journalFailureAudit {
		if event.Event == "agent_status" && event.ToStatus.String == "unknown" {
			unknownAudit = true
		}
	}
	if !unknownAudit {
		t.Fatalf("uncertain Agent journal failure is missing its durable unknown audit: events=%+v", journalFailureAudit)
	}
	if !strings.Contains(agentLog.String()[len(agentLogBeforeJournalFailure):], "Agent connection unavailable; retrying") {
		t.Fatalf("expected Agent did not log a bounded reconnect after journal insert failure: before=%q after=%q", agentLogBeforeJournalFailure, agentLog.String())
	}
	agentJournalInsertAttempts := s05JournalInsertAttemptCount.Load() - agentJournalInsertAttemptsBefore
	agentMutationCalls := dockerProxy.RestartHitCount(containerID) - agentMutationCallsBefore
	engineMutationCalls := dockerProxy.ForwardedRestartCount(containerID) - engineMutationCallsBefore
	if agentJournalInsertAttempts != 1 {
		t.Fatalf("Agent journal trigger did not observe exactly one durable INSERT attempt: count=%d", agentJournalInsertAttempts)
	}
	if agentMutationCalls != 0 || engineMutationCalls != 0 {
		t.Fatalf("Agent attempted Docker restart before its journal write succeeded: local_api_calls=%d Engine_api_calls=%d",
			agentMutationCalls, engineMutationCalls)
	}
	t.Logf("S05_EVIDENCE agent_task_journal_failure=unknown_result_pending journal_rows=0 journal_insert_attempts=%d agent_mutation_api_calls=%d engine_mutation_api_calls=%d core_unknown_reason=delivery_committed_agent_journal_absent cross_connection_result_unproven=true resource_claim_retained=true docker_started_at_unchanged=true docker_start_count_unchanged=true docker_events_unchanged=true unknown_audit=true generation=%d->%d verified=true",
		agentJournalInsertAttempts, agentMutationCalls, engineMutationCalls,
		previousAgentGeneration, newAgentGeneration)
	performAPIAction("stop", nil)
	deleteTask := performAPIAction("delete", map[string]any{"deleteConfirmed": true, "deleteConfirmationId": containerID})
	if deleteTask.Result.ObservedState != "missing" {
		t.Fatalf("verified delete observed state=%q, want missing", deleteTask.Result.ObservedState)
	}
	if _, err := runS04DockerCLI(endpoint, "container", "inspect", containerID); err == nil {
		t.Fatal("Core-Agent delete task succeeded but real Engine still has the container")
	}
	if present, err := s05OwnedContainerPresent(endpoint, containerID); err != nil || present {
		t.Fatalf("deleted main fixture was not verified absent from the live Engine: id=%s present=%t err=%v", containerID, present, err)
	}
	fixtures.markExpectedDeleted(t, containerID)
	t.Logf("S05_FIXTURE_CLEANUP suite=%s id=%s removed_by_task=true verified_absent=true", runID, containerID)
	fixtures.verifyExpected(t)
	t.Log("S05_CASE S05-07 wrong_delete_confirmation=400 running_delete=failed stopped_delete=succeeded verified=true")
	t.Log("S05_CASE S05-01 lifecycle=start,stop,restart,pause,resume,delete,rename verified=true")
	if err := browser.Close(); err != nil {
		t.Errorf("close real browser bridge: %v", err)
	}
}

// s05DINDFixtureManifest is the complete set of Engine containers created by
// this test run. Only a fixture explicitly marked expectedDeleted may be
// absent before teardown; all other entries must still exist under their
// exact full Engine IDs and suite labels.
type s05DINDFixtureManifest struct {
	endpoint string
	suite    string
	items    map[string]*s05DINDFixture
}

type s05DINDFixture struct {
	kind            string
	id              string
	expectedDeleted bool
}

func newS05DINDFixtureManifest(endpoint, suite string) *s05DINDFixtureManifest {
	return &s05DINDFixtureManifest{endpoint: endpoint, suite: suite, items: make(map[string]*s05DINDFixture)}
}

func (m *s05DINDFixtureManifest) add(t *testing.T, kind, id string) {
	t.Helper()
	if !protocol.IsFullContainerID(id) {
		t.Fatalf("S05 %s fixture manifest requires a full Engine ID, got %q", kind, id)
	}
	if _, exists := m.items[id]; exists {
		t.Fatalf("duplicate S05 fixture manifest ID %s", id)
	}
	m.items[id] = &s05DINDFixture{kind: kind, id: id}
}

func (m *s05DINDFixtureManifest) markExpectedDeleted(t *testing.T, id string) {
	t.Helper()
	fixture, exists := m.items[id]
	if !exists || fixture.kind != "main" {
		t.Fatalf("only the registered main fixture may be marked as intentionally deleted: id=%s", id)
	}
	fixture.expectedDeleted = true
}

func (m *s05DINDFixtureManifest) verifyExpected(t *testing.T) {
	t.Helper()
	actual, err := s05ListSuiteFixtureIDs(m.endpoint, m.suite)
	if err != nil {
		t.Fatalf("list exact S05 suite fixture manifest: %v", err)
	}
	actualSet := make(map[string]bool, len(actual))
	for _, id := range actual {
		actualSet[id] = true
	}
	for _, fixture := range m.items {
		present := actualSet[fixture.id]
		wantPresent := !fixture.expectedDeleted
		if present != wantPresent {
			t.Errorf("S05 fixture manifest mismatch: kind=%s id=%s present=%t want_present=%t expected_deleted=%t", fixture.kind, fixture.id, present, wantPresent, fixture.expectedDeleted)
			continue
		}
		if !present {
			continue
		}
		if err := s05VerifySuiteFixtureIdentity(m.endpoint, m.suite, fixture.id); err != nil {
			t.Errorf("S05 fixture identity mismatch: kind=%s id=%s: %v", fixture.kind, fixture.id, err)
		}
		delete(actualSet, fixture.id)
	}
	for id := range actualSet {
		t.Errorf("unexpected Engine container with this unique S05 suite label: id=%s", id)
	}
	t.Logf("S05_FIXTURE_MANIFEST suite=%s expected_present=%d expected_deleted=%d unexpected=%d verified=true",
		m.suite, m.expectedPresentCount(), m.expectedDeletedCount(), len(actualSet))
}

func (m *s05DINDFixtureManifest) cleanup(t *testing.T) {
	t.Helper()
	// Check the suite-tagged inventory first. A Docker daemon version response
	// alone is not cleanup evidence; every recorded full ID is checked below.
	actual, err := s05ListSuiteFixtureIDs(m.endpoint, m.suite)
	if err != nil {
		t.Errorf("cannot inspect S05 fixture manifest before cleanup: %v", err)
		return
	}
	actualSet := make(map[string]bool, len(actual))
	for _, id := range actual {
		actualSet[id] = true
	}
	for _, fixture := range m.items {
		if !actualSet[fixture.id] {
			if !fixture.expectedDeleted {
				t.Errorf("S05 fixture missing before cleanup without an expected task deletion: kind=%s id=%s", fixture.kind, fixture.id)
			}
			continue
		}
		if err := s05VerifySuiteFixtureIdentity(m.endpoint, m.suite, fixture.id); err != nil {
			t.Errorf("refusing cleanup without exact S05 fixture identity: kind=%s id=%s: %v", fixture.kind, fixture.id, err)
			continue
		}
		if _, err := runS04DockerCLI(m.endpoint, "container", "rm", "--force", fixture.id); err != nil {
			t.Errorf("remove exact S05 fixture kind=%s id=%s: %v", fixture.kind, fixture.id, err)
			continue
		}
		if present, err := s05OwnedContainerPresent(m.endpoint, fixture.id); err != nil || present {
			t.Errorf("S05 fixture cleanup is unverified: kind=%s id=%s present=%t err=%v", fixture.kind, fixture.id, present, err)
			continue
		}
		t.Logf("S05_FIXTURE_CLEANUP suite=%s kind=%s id=%s removed=true verified_absent=true", m.suite, fixture.kind, fixture.id)
	}
	remaining, err := s05ListSuiteFixtureIDs(m.endpoint, m.suite)
	if err != nil {
		t.Errorf("cannot verify final S05 fixture manifest: %v", err)
		return
	}
	if len(remaining) != 0 {
		t.Errorf("S05 fixture manifest has uncleaned suite resources: suite=%s ids=%v", m.suite, remaining)
		return
	}
	t.Logf("S05_FIXTURE_MANIFEST_CLEANUP suite=%s expected_ids=%d remaining_ids=0 verified=true", m.suite, len(m.items))
}

func (m *s05DINDFixtureManifest) expectedPresentCount() int {
	count := 0
	for _, fixture := range m.items {
		if !fixture.expectedDeleted {
			count++
		}
	}
	return count
}

func (m *s05DINDFixtureManifest) expectedDeletedCount() int {
	return len(m.items) - m.expectedPresentCount()
}

func s05ListSuiteFixtureIDs(endpoint, suite string) ([]string, error) {
	output, err := runS04DockerCLI(endpoint, "container", "ls", "--all", "--quiet", "--no-trunc", "--filter", "label=io.nodedance.suite="+suite)
	if err != nil {
		return nil, err
	}
	ids := strings.Fields(output)
	for _, id := range ids {
		if !protocol.IsFullContainerID(id) {
			return nil, fmt.Errorf("Engine returned non-full container ID %q for suite %q", id, suite)
		}
	}
	return ids, nil
}

func s05VerifySuiteFixtureIdentity(endpoint, suite, id string) error {
	actualID, err := runS04DockerCLI(endpoint, "container", "inspect", "--format", "{{.Id}}", id)
	if err != nil {
		return fmt.Errorf("inspect exact ID: %w", err)
	}
	if strings.TrimSpace(actualID) != id {
		return fmt.Errorf("inspected ID %q does not match manifest ID", strings.TrimSpace(actualID))
	}
	label, err := runS04DockerCLI(endpoint, "container", "inspect", "--format", `{{ index .Config.Labels "io.nodedance.suite" }}`, id)
	if err != nil {
		return fmt.Errorf("inspect suite ownership label: %w", err)
	}
	if strings.TrimSpace(label) != suite {
		return fmt.Errorf("suite ownership label %q does not match %q", strings.TrimSpace(label), suite)
	}
	return nil
}

func s05OwnedContainerPresent(endpoint, containerID string) (bool, error) {
	output, err := runS04DockerCLI(endpoint, "container", "ls", "--all", "--quiet", "--no-trunc", "--filter", "id="+containerID)
	if err != nil {
		return false, err
	}
	for _, value := range strings.Fields(output) {
		if value == containerID || strings.HasPrefix(containerID, value) {
			return true, nil
		}
	}
	return false, nil
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

func ensureS05JournalInsertAttemptFunction() error {
	s05JournalInsertAttemptFunctionOnce.Do(func() {
		s05JournalInsertAttemptFunctionErr = modernsqlite.RegisterScalarFunction(
			"nodedance_s05_note_journal_insert_attempt", 1,
			func(_ *modernsqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
				if len(args) != 1 {
					return nil, fmt.Errorf("journal insert observer requires one task ID")
				}
				if _, ok := args[0].(string); !ok {
					return nil, fmt.Errorf("journal insert observer requires a text task ID")
				}
				s05JournalInsertAttemptCount.Add(1)
				return int64(1), nil
			},
		)
	})
	return s05JournalInsertAttemptFunctionErr
}

func TestS05JournalInsertAttemptObserverSurvivesAbort(t *testing.T) {
	if err := ensureS05JournalInsertAttemptFunction(); err != nil {
		t.Fatal("register non-transactional Agent journal insert attempt observer:", err)
	}
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal("open isolated SQLite observer test database:", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`CREATE TABLE task_journal(task_id TEXT NOT NULL);
		CREATE TRIGGER fail_insert BEFORE INSERT ON task_journal BEGIN
		SELECT nodedance_s05_note_journal_insert_attempt(NEW.task_id);
		SELECT RAISE(ABORT, 'injected journal failure'); END`); err != nil {
		t.Fatal("create isolated trigger failure fixture:", err)
	}
	before := s05JournalInsertAttemptCount.Load()
	if _, err := db.Exec(`INSERT INTO task_journal(task_id) VALUES('task-observer-test')`); err == nil {
		t.Fatal("triggered journal insert unexpectedly succeeded")
	}
	if got := s05JournalInsertAttemptCount.Load() - before; got != 1 {
		t.Fatalf("observer did not record exactly one INSERT attempt before rollback: delta=%d", got)
	}
	var rows int
	if err := db.QueryRow(`SELECT count(*) FROM task_journal`).Scan(&rows); err != nil || rows != 0 {
		t.Fatalf("failed journal INSERT left a row behind: rows=%d err=%v", rows, err)
	}
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
