package server

import (
	"context"
	"crypto/tls"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/CST-Cat/NodeDance/internal/agent"
	"github.com/CST-Cat/NodeDance/internal/core/docker"
	coretasks "github.com/CST-Cat/NodeDance/internal/core/tasks"
	"github.com/CST-Cat/NodeDance/internal/protocol"
	_ "modernc.org/sqlite"
)

// TestS05QueuedCancelAndOfflineNoBacklogOnOwnedDIND exercises the safe
// undelivered-cancellation and offline-rejection paths through the real
// authenticated Core API, Agent journal/dispatcher, and an owner-marked DIND
// Engine. A local Unix HTTP proxy holds every capacity-filling restart before
// it can reach Docker, leaving one additional Core task genuinely queued.
func TestS05QueuedCancelAndOfflineNoBacklogOnOwnedDIND(t *testing.T) {
	root, endpoint, engineVersion := requireOwnedS04DIND(t)
	t.Setenv("DOCKER_HOST", endpoint)
	if _, err := runS04DockerCLI(endpoint, "image", "inspect", "--format", "{{.Id}}", s04BusyboxImage); err != nil {
		if _, pullErr := runS04DockerCLI(endpoint, "pull", s04BusyboxImage); pullErr != nil {
			t.Fatalf("ensure locked S05 fixture image exists on owned DIND: inspect=%v pull=%v", err, pullErr)
		}
	}

	runID := fmt.Sprintf("nd-s05-cancel-offline-%d", time.Now().UnixNano())
	fixtureIDs := make([]string, 0, 12)
	fixtureNames := make(map[string]string)
	fixtureBySlot := make(map[int]string)
	t.Cleanup(func() { cleanupS05CancelOfflineFixtures(t, endpoint, runID, fixtureIDs, fixtureNames) })

	workRoot := filepath.Join(root, ".artifacts", "work-s05")
	if err := os.MkdirAll(workRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	work, err := os.MkdirTemp(workRoot, "queued-cancel-offline-")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(work, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(work) })

	certificate, rootPEM, err := makeAgentTestCertificate()
	if err != nil {
		t.Fatal(err)
	}
	coreHTTP := httptest.NewUnstartedServer(http.NotFoundHandler())
	publicOrigin := "https://" + coreHTTP.Listener.Addr().String()
	core, err := New("s05-queued-cancel-offline", Options{
		DataDir: filepath.Join(work, "core"), Development: true, PublicOrigin: publicOrigin,
		// Keep the ordinary five-second Agent heartbeat while making lease
		// expiry bounded for this real disconnect/reconnect integration case.
		AgentOfflineTimeout: 7 * time.Second, AgentSweepInterval: time.Second,
	})
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
			t.Errorf("close S05 cancellation Core: %v", err)
		}
	})

	session, csrf, err := installIntegrationAdmin(core)
	if err != nil {
		t.Fatal(err)
	}
	enrollment, err := core.agents.CreateEnrollment(context.Background(), "S05 queued cancellation "+engineVersion,
		"127.0.0.1", sql.NullInt64{Int64: 1, Valid: true})
	if err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(work, "agent", "agent.json")
	caPath := filepath.Join(work, "trusted-ca.pem")
	if err := os.WriteFile(caPath, rootPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := agent.Enroll(context.Background(), coreHTTP.URL, caPath, false, strings.NewReader(enrollment.Token), configPath); err != nil {
		t.Fatalf("enroll real Agent into Core: %v", err)
	}
	config, err := agent.LoadConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}

	// Start the Agent through a local Unix proxy so its negotiated task capacity
	// can be read before creating the exact number of distinct Engine targets
	// needed to fill every durable slot.
	proxyRoot, err := os.MkdirTemp("/tmp", "nd-s05-cancel-")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(proxyRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	ownerMarker := filepath.Join(proxyRoot, "owner.json")
	if err := os.WriteFile(ownerMarker, []byte(runID+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	proxySocket := filepath.Join(proxyRoot, "engine.sock")
	barrier, err := newS05CancellationBarrierProxy(proxySocket, endpoint)
	if err != nil {
		t.Fatal("start owner-scoped Docker dispatch barrier:", err)
	}
	t.Cleanup(func() {
		barrier.Release()
		if err := barrier.Close(); err != nil {
			t.Errorf("close S05 Docker dispatch barrier: %v", err)
		}
		marker, markerErr := os.ReadFile(ownerMarker)
		if markerErr != nil || string(marker) != runID+"\n" {
			t.Errorf("refuse cleanup of non-owned short Unix socket directory: marker=%q err=%v", marker, markerErr)
			return
		}
		info, statErr := os.Lstat(proxyRoot)
		if statErr != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			t.Errorf("refuse cleanup of unexpected proxy directory: info=%v err=%v", info, statErr)
			return
		}
		_ = os.RemoveAll(proxyRoot)
	})

	t.Setenv("DOCKER_HOST", barrier.Host())
	barrier.EnableEngineForwarding()
	var agentCancel context.CancelFunc
	var agentDone chan error
	var agentGeneration uint64
	startAgent := func(previousGeneration uint64) {
		t.Helper()
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		log := &agentTestLog{}
		go func() { done <- agent.Run(ctx, configPath, "s05-queued-cancel-offline", log) }()
		agentCancel, agentDone = cancel, done
		generation, ok := awaitS05AgentBridgeReady(core, config.NodeID, previousGeneration, 20*time.Second)
		if !ok {
			t.Fatalf("real Agent did not complete the negotiated task bridge; previous_generation=%d active=%s",
				previousGeneration, describeS05AgentTaskBridge(core, config.NodeID))
		}
		agentGeneration = generation
		view := waitForS04DockerView(t, core, config.NodeID, 20*time.Second, func(view docker.View) bool {
			return view.ActiveGeneration == generation && view.AgentOnline && !view.DataStale &&
				view.DockerSnapshotFresh && view.DockerEventsConnected &&
				view.DockerAvailability == protocol.DockerAvailabilityAvailable
		}, "Agent Docker event stream and initial snapshot must be ready before fixture creation")
		if view.ActiveGeneration != generation || !core.taskBridgeReady(config.NodeID, generation) {
			t.Fatalf("Agent is not ready on synchronized generation %d: view=%+v", generation, view)
		}
	}
	stopAgent := func() {
		t.Helper()
		if agentCancel == nil {
			return
		}
		cancel, done := agentCancel, agentDone
		agentCancel = nil
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("real Agent returned during bounded stop: %v", err)
			}
		case <-time.After(12 * time.Second):
			t.Error("real Agent did not join within the bounded stop deadline")
		}
		waitForAgentConnectionDetached(t, core, config.AgentID, 5*time.Second)
	}
	t.Cleanup(stopAgent)
	startAgent(0)

	connection := core.activeAgentConnectionForNode(config.NodeID)
	if connection == nil || connection.generation != agentGeneration || connection.taskCapacity < 1 || connection.taskCapacity > 32 {
		t.Fatalf("unsafe or unavailable negotiated task capacity: connection=%v generation=%d", connection != nil, agentGeneration)
	}
	capacity := connection.taskCapacity
	queuedSlot := capacity
	targetCount := capacity + 1
	t.Logf("S05_S06_CAPACITY negotiated_slots=%d held_slots=%d queued_target_slot=%d verified=true", capacity, capacity, queuedSlot)

	// The first `capacity` fixtures are held before mutation; the final fixture
	// is reserved for the genuinely queued cancellation and subsequent offline
	// request.
	const startCountScript = `n=0; if [ -f /tmp/nodedance-start-count ]; then n=$(cat /tmp/nodedance-start-count); fi; echo $((n+1)) >/tmp/nodedance-start-count; trap 'exit 0' TERM; while :; do sleep 1; done`
	for slot := 0; slot < targetCount; slot++ {
		name := fmt.Sprintf("%s-slot-%02d", runID, slot)
		containerID, runErr := runS04DockerCLI(endpoint, "container", "run", "--detach", "--name", name,
			"--label", "io.nodedance.test=true", "--label", "io.nodedance.suite="+runID,
			s04BusyboxImage, "sh", "-c", startCountScript)
		if runErr != nil {
			t.Fatalf("create exact owned slot %d fixture on Engine %s: %v", slot, engineVersion, runErr)
		}
		containerID = strings.TrimSpace(containerID)
		if !protocol.IsFullContainerID(containerID) {
			t.Fatalf("Engine returned malformed full container ID for slot %d: %q", slot, containerID)
		}
		fixtureIDs = append(fixtureIDs, containerID)
		fixtureNames[containerID] = name
		fixtureBySlot[slot] = containerID
		t.Logf("S05_S06_FIXTURE suite=%s slot=%d id=%s name=%s", runID, slot, containerID, name)
		if got, waitErr := waitForS05ContainerStartCount(endpoint, containerID, 15*time.Second); waitErr != nil || got != 1 {
			t.Fatalf("slot %d fixture did not start exactly once: count=%d err=%v", slot, got, waitErr)
		}
	}
	previousGeneration := agentGeneration
	core.closeAgentConnection(config.AgentID, previousGeneration)
	var reconnected bool
	agentGeneration, reconnected = awaitS05AgentBridgeReady(core, config.NodeID, previousGeneration, 20*time.Second)
	if !reconnected || agentGeneration <= previousGeneration {
		t.Fatalf("real Agent did not reconnect for an authoritative fixture snapshot: previous_generation=%d active=%s",
			previousGeneration, describeS05AgentTaskBridge(core, config.NodeID))
	}
	t.Logf("S05_FIXTURE_SYNC suite=%s strategy=real_agent_reconnect previous_generation=%d snapshot_generation=%d verified=true",
		runID, previousGeneration, agentGeneration)
	waitForS05FixtureInventory(t, core, endpoint, config.NodeID, agentGeneration, fixtureIDs, 30*time.Second)
	heldTargets := make([]string, 0, capacity)
	for slot := 0; slot < capacity; slot++ {
		heldTargets = append(heldTargets, fixtureBySlot[slot])
	}
	barrier.ArmTargets(heldTargets)

	client := tlsHTTPClient(rootPEM)
	client.Timeout = 8 * time.Second
	postAction := func(containerID, key string) (int, s05TaskAccepted, string, error) {
		t.Helper()
		request, err := http.NewRequest(http.MethodPost,
			coreHTTP.URL+"/api/v1/nodes/"+config.NodeID+"/containers/"+containerID+"/actions", strings.NewReader(`{"action":"restart"}`))
		if err != nil {
			return 0, s05TaskAccepted{}, "", err
		}
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Origin", coreHTTP.URL)
		request.Header.Set(csrfHeaderName, csrf)
		request.Header.Set("Idempotency-Key", key)
		request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: session})
		request.AddCookie(&http.Cookie{Name: csrfCookieName, Value: csrf})
		response, err := client.Do(request)
		if err != nil {
			return 0, s05TaskAccepted{}, "", err
		}
		defer response.Body.Close()
		body, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
		if err != nil {
			return response.StatusCode, s05TaskAccepted{}, string(body), err
		}
		var accepted s05TaskAccepted
		if response.StatusCode == http.StatusAccepted || response.StatusCode == http.StatusOK {
			if err := json.Unmarshal(body, &accepted); err != nil {
				return response.StatusCode, accepted, string(body), err
			}
		}
		return response.StatusCode, accepted, string(body), nil
	}
	cancelTask := func(taskID string) (int, s05TaskAPIView, string, error) {
		t.Helper()
		request, err := http.NewRequest(http.MethodDelete, coreHTTP.URL+"/api/v1/nodes/"+config.NodeID+"/tasks/"+taskID, nil)
		if err != nil {
			return 0, s05TaskAPIView{}, "", err
		}
		request.Header.Set("Origin", coreHTTP.URL)
		request.Header.Set(csrfHeaderName, csrf)
		request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: session})
		request.AddCookie(&http.Cookie{Name: csrfCookieName, Value: csrf})
		response, err := client.Do(request)
		if err != nil {
			return 0, s05TaskAPIView{}, "", err
		}
		defer response.Body.Close()
		body, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
		if err != nil {
			return response.StatusCode, s05TaskAPIView{}, string(body), err
		}
		var result s05TaskAPIView
		if response.StatusCode == http.StatusOK {
			if err := json.Unmarshal(body, &result); err != nil {
				return response.StatusCode, result, string(body), err
			}
		}
		return response.StatusCode, result, string(body), nil
	}

	capacityTasks := make([]string, 0, capacity)
	capacityTaskByTarget := make(map[string]string, capacity)
	for slot := 0; slot < capacity; slot++ {
		targetID := fixtureBySlot[slot]
		status, accepted, body, postErr := postAction(targetID, fmt.Sprintf("s05-held-%s-%02d", runID, slot))
		if postErr != nil || status != http.StatusAccepted || accepted.TaskID == "" {
			t.Fatalf("capacity slot %d action returned status=%d task=%+v body=%q err=%v", slot, status, accepted, body, postErr)
		}
		capacityTasks = append(capacityTasks, accepted.TaskID)
		capacityTaskByTarget[targetID] = accepted.TaskID
	}
	if !barrier.WaitForHits(min(capacity, 2), 15*time.Second) {
		t.Fatalf("Docker proxy did not hold enough distinct worker requests: hits=%v capacity=%d", barrier.HitIDs(), capacity)
	}
	waitForCondition(t, 20*time.Second, func() bool {
		for _, taskID := range capacityTasks {
			task, getErr := core.tasks.Get(context.Background(), config.NodeID, taskID)
			if getErr != nil || task.DeliveryState != "sent" || !task.Evidence.DeliveryCommitted {
				return false
			}
			if exists, journalErr := s05ReadAgentTaskJournal(filepath.Join(filepath.Dir(configPath), "tasks.sqlite"), taskID); journalErr != nil || !exists {
				return false
			}
		}
		return true
	}, "Agent journal did not durably record every task needed to exhaust negotiated capacity")
	if connection := core.activeAgentConnectionForNode(config.NodeID); connection == nil || connection.taskCapacity != capacity {
		t.Fatalf("dispatch barrier lost the Agent connection or changed negotiated capacity: expected=%d present=%t", capacity, connection != nil)
	}
	for _, taskID := range capacityTasks {
		task, getErr := core.tasks.Get(context.Background(), config.NodeID, taskID)
		if getErr != nil || (task.Status != "queued" && task.Status != "running") {
			t.Fatalf("held task is not an active durable delivery: task=%s state=%+v err=%v", taskID, task, getErr)
		}
	}

	queuedTargetID := fixtureBySlot[queuedSlot]
	queuedStartedAt, err := runS04DockerCLI(endpoint, "container", "inspect", "--format", "{{.State.StartedAt}}", queuedTargetID)
	if err != nil {
		t.Fatal("read queued target initial StartedAt:", err)
	}
	queuedStartCount, err := readS05ContainerStartCount(endpoint, queuedTargetID)
	if err != nil || queuedStartCount != 1 {
		t.Fatalf("read queued target initial start count: count=%d err=%v", queuedStartCount, err)
	}
	eventsSince := time.Now().UTC()
	queuedStatus, queuedAccepted, body, postErr := postAction(queuedTargetID, "s05-undelivered-"+runID)
	if postErr != nil || queuedStatus != http.StatusAccepted || queuedAccepted.TaskID == "" {
		t.Fatalf("queue task beyond negotiated Agent capacity returned status=%d task=%+v body=%q err=%v", queuedStatus, queuedAccepted, body, postErr)
	}
	queuedTask, err := core.tasks.Get(context.Background(), config.NodeID, queuedAccepted.TaskID)
	if err != nil || queuedTask.Status != "queued" || queuedTask.DeliveryState != "ready" || queuedTask.Evidence.DeliveryCommitted {
		t.Fatalf("extra action was not genuinely queued but undelivered: task=%+v err=%v", queuedTask, err)
	}
	if exists, err := s05ReadAgentTaskJournal(filepath.Join(filepath.Dir(configPath), "tasks.sqlite"), queuedAccepted.TaskID); err != nil || exists {
		t.Fatalf("undelivered task unexpectedly exists in durable Agent journal: exists=%t err=%v", exists, err)
	}
	var deliveredAudit int
	if err := core.store.DB.QueryRow(`SELECT count(*) FROM core_task_audit_events WHERE task_id=? AND event='delivery_claimed'`, queuedAccepted.TaskID).Scan(&deliveredAudit); err != nil || deliveredAudit != 0 {
		t.Fatalf("undelivered task has a Core dispatch audit: count=%d err=%v", deliveredAudit, err)
	}
	journalRows := 0
	for _, taskID := range capacityTasks {
		exists, journalErr := s05ReadAgentTaskJournal(filepath.Join(filepath.Dir(configPath), "tasks.sqlite"), taskID)
		if journalErr != nil || !exists {
			t.Fatalf("dispatch barrier lost a durable Agent task before safe cancellation: task=%s exists=%t err=%v", taskID, exists, journalErr)
		}
		journalRows++
	}
	t.Logf("S05_S06_DISPATCH_BARRIER negotiated_slots=%d delivered_slots=%d agent_journal_rows=%d extra_status=%s extra_delivery=%s extra_agent_journal_rows=0 verified=true",
		capacity, len(capacityTasks), journalRows, queuedTask.Status, queuedTask.DeliveryState)

	cancelStatus, canceled, cancelBody, cancelErr := cancelTask(queuedAccepted.TaskID)
	if cancelErr != nil || cancelStatus != http.StatusOK || canceled.TaskID != queuedAccepted.TaskID || canceled.Status != "canceled" ||
		canceled.Result.Code != "canceled" || canceled.Result.ObservedState != "not_dispatched" {
		t.Fatalf("real API safe queued cancellation returned status=%d task=%+v body=%q err=%v", cancelStatus, canceled, cancelBody, cancelErr)
	}
	retryStatus, retried, retryBody, retryErr := cancelTask(queuedAccepted.TaskID)
	if retryErr != nil || retryStatus != http.StatusOK || retried.TaskID != canceled.TaskID || retried.Status != canceled.Status ||
		retried.Result.Code != canceled.Result.Code || retried.Result.ObservedState != canceled.Result.ObservedState {
		t.Fatalf("repeated DELETE did not return the same durable cancellation result: first=%+v retry=%+v status=%d body=%q err=%v",
			canceled, retried, retryStatus, retryBody, retryErr)
	}
	afterQueuedStartedAt, err := runS04DockerCLI(endpoint, "container", "inspect", "--format", "{{.State.StartedAt}}", queuedTargetID)
	if err != nil || strings.TrimSpace(afterQueuedStartedAt) != strings.TrimSpace(queuedStartedAt) {
		t.Fatalf("queued cancel changed Docker StartedAt: before=%q after=%q err=%v", strings.TrimSpace(queuedStartedAt), strings.TrimSpace(afterQueuedStartedAt), err)
	}
	afterQueuedStartCount, err := readS05ContainerStartCount(endpoint, queuedTargetID)
	if err != nil || afterQueuedStartCount != queuedStartCount {
		t.Fatalf("queued cancel changed container start count: before=%d after=%d err=%v", queuedStartCount, afterQueuedStartCount, err)
	}
	queuedEvents, err := s05DockerEventsAfter(endpoint, queuedTargetID, eventsSince)
	if err != nil || len(queuedEvents) != 0 {
		t.Fatalf("queued cancel caused Docker Engine events for its target: events=%v err=%v", queuedEvents, err)
	}
	if final, err := core.tasks.Get(context.Background(), config.NodeID, queuedAccepted.TaskID); err != nil || final.Status != "canceled" || final.Evidence.DeliveryCommitted {
		t.Fatalf("safe cancel lost its durable not-dispatched evidence: task=%+v err=%v", final, err)
	}
	t.Logf("S05_S06_SAFE_CANCEL task_status=%s result=%s repeated_delete=same_result journal_rows=0 engine_events=0 verified=true", canceled.Status, canceled.Result.ObservedState)

	// Let only the capacity-filling tasks reach the real Engine, then wait for
	// their durable verified terminal reports before disconnecting the Agent.
	barrier.Release()
	for _, taskID := range capacityTasks {
		deadline := time.Now().Add(60 * time.Second)
		var final coretasks.Task
		for time.Now().Before(deadline) {
			final, err = core.tasks.Get(context.Background(), config.NodeID, taskID)
			if err == nil && final.Status == "succeeded" && final.Result.Code == coretasks.ResultVerified {
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
		if err != nil || final.Status != "succeeded" || final.Result.Code != coretasks.ResultVerified {
			t.Fatalf("held task did not reach real Engine-verified completion: task=%s result=%+v err=%v", taskID, final, err)
		}
		if got, countErr := readS05ContainerStartCount(endpoint, final.Intent.ContainerID); countErr != nil || got != 2 {
			t.Fatalf("capacity task did not perform exactly one real restart: target=%s starts=%d err=%v", final.Intent.ContainerID, got, countErr)
		}
	}
	if len(capacityTaskByTarget) != capacity {
		t.Fatalf("capacity fixture mapping incomplete: targets=%d capacity=%d", len(capacityTaskByTarget), capacity)
	}

	// A delivered restart that remains behind the Engine proxy longer than the
	// Agent's bounded operation deadline must become unknown. The canceled
	// request is not replayed after the barrier opens or after a fresh journal
	// synchronization; the actual Engine state is checked before retaining the
	// unresolved result.
	timeoutTargetID := fixtureBySlot[0]
	timeoutTaskKey := "s05-operation-timeout-" + runID
	timeoutStartedAt, err := runS04DockerCLI(endpoint, "container", "inspect", "--format", "{{.State.StartedAt}}", timeoutTargetID)
	if err != nil {
		t.Fatal("read timeout target StartedAt before held operation:", err)
	}
	timeoutStartCount, err := readS05ContainerStartCount(endpoint, timeoutTargetID)
	if err != nil || timeoutStartCount != 2 {
		t.Fatalf("read timeout target start count after capacity tasks: count=%d err=%v", timeoutStartCount, err)
	}
	timeoutRestartCount, err := readS05ContainerRestartCount(endpoint, timeoutTargetID)
	if err != nil {
		t.Fatal("read Engine RestartCount before held operation:", err)
	}
	timeoutEventsSince := time.Now().UTC()
	timeoutInspectBaseline := barrier.CompletedInspectCount(timeoutTargetID)
	timeoutRestartHitsBaseline := barrier.RestartHitCount(timeoutTargetID)
	timeoutForwardedRestartsBaseline := barrier.ForwardedRestartCount(timeoutTargetID)
	barrier.ArmTargets([]string{timeoutTargetID})
	timeoutStatus, timeoutAccepted, timeoutBody, timeoutPostErr := postAction(timeoutTargetID, timeoutTaskKey)
	if timeoutPostErr != nil || timeoutStatus != http.StatusAccepted || timeoutAccepted.TaskID == "" {
		t.Fatalf("held timeout task returned status=%d task=%+v body=%q err=%v", timeoutStatus, timeoutAccepted, timeoutBody, timeoutPostErr)
	}
	if !barrier.WaitForHits(1, 15*time.Second) {
		t.Fatalf("operation-timeout barrier did not intercept the delivered target: target=%s hits=%v", timeoutTargetID, barrier.HitIDs())
	}
	waitForCondition(t, 20*time.Second, func() bool {
		task, getErr := core.tasks.Get(context.Background(), config.NodeID, timeoutAccepted.TaskID)
		return getErr == nil && task.DeliveryState == "sent" && task.Evidence.DeliveryCommitted
	}, "operation-timeout task was not durably delivered before its deadline")
	if exists, journalErr := s05ReadAgentTaskJournal(filepath.Join(filepath.Dir(configPath), "tasks.sqlite"), timeoutAccepted.TaskID); journalErr != nil || !exists {
		t.Fatalf("delivered timeout task is missing from the Agent journal: exists=%t err=%v", exists, journalErr)
	}
	deliveredCancelStatus, _, deliveredCancelBody, deliveredCancelErr := cancelTask(timeoutAccepted.TaskID)
	if deliveredCancelErr != nil || deliveredCancelStatus != http.StatusConflict {
		t.Fatalf("DELETE must reject cancellation after durable delivery: status=%d body=%q err=%v", deliveredCancelStatus, deliveredCancelBody, deliveredCancelErr)
	}
	const agentOperationDeadline = 30 * time.Second
	deadline := time.Now().Add(agentOperationDeadline + 15*time.Second)
	var timedOut coretasks.Task
	for time.Now().Before(deadline) {
		timedOut, err = core.tasks.Get(context.Background(), config.NodeID, timeoutAccepted.TaskID)
		if err == nil && timedOut.Status == "unknown" && timedOut.Result.Code == coretasks.ResultUncertain &&
			timedOut.Evidence.ExecutionAttempted && timedOut.Evidence.DeliveryCommitted {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if err != nil || timedOut.Status != "unknown" || timedOut.Result.Code != coretasks.ResultUncertain ||
		!timedOut.Evidence.ExecutionAttempted || !timedOut.Evidence.DeliveryCommitted {
		t.Fatalf("held operation past its bounded Agent deadline must remain unknown/result_pending: task=%+v err=%v", timedOut, err)
	}
	if timedOut.Status == "timed_out" || timedOut.Status == "canceled" {
		t.Fatalf("uncertain delivered operation was converted to a terminal timeout/cancel state: %+v", timedOut)
	}
	timeoutAPI, err := getS05Task(client, coreHTTP.URL, session, config.NodeID, timeoutAccepted.TaskID)
	if err != nil || timeoutAPI.Status != "unknown" || timeoutAPI.Result.Code != string(coretasks.ResultUncertain) {
		t.Fatalf("authenticated task progress API did not expose unknown/result_pending: task=%+v err=%v", timeoutAPI, err)
	}
	timeoutStartedAfterDeadline, err := runS04DockerCLI(endpoint, "container", "inspect", "--format", "{{.State.StartedAt}}", timeoutTargetID)
	if err != nil || strings.TrimSpace(timeoutStartedAfterDeadline) != strings.TrimSpace(timeoutStartedAt) {
		t.Fatalf("held timeout request changed Engine state before proxy release: before=%q after=%q err=%v", strings.TrimSpace(timeoutStartedAt), strings.TrimSpace(timeoutStartedAfterDeadline), err)
	}
	timeoutCountAfterDeadline, err := readS05ContainerStartCount(endpoint, timeoutTargetID)
	if err != nil || timeoutCountAfterDeadline != timeoutStartCount {
		t.Fatalf("held timeout request reached container before proxy release: before=%d after=%d err=%v", timeoutStartCount, timeoutCountAfterDeadline, err)
	}
	timeoutRestartCountAfterDeadline, err := readS05ContainerRestartCount(endpoint, timeoutTargetID)
	if err != nil || timeoutRestartCountAfterDeadline != timeoutRestartCount {
		t.Fatalf("held timeout request changed Engine RestartCount before proxy release: before=%d after=%d err=%v", timeoutRestartCount, timeoutRestartCountAfterDeadline, err)
	}
	timeoutEvents, err := s05DockerEventsAfter(endpoint, timeoutTargetID, timeoutEventsSince)
	if err != nil || len(timeoutEvents) != 0 {
		t.Fatalf("held timeout request emitted Engine events before proxy release: events=%v err=%v", timeoutEvents, err)
	}
	waitForCondition(t, 15*time.Second, func() bool {
		// The first two completed Inspect requests are the pre-mutation baseline
		// and the post-deadline verification. The third is the same-generation
		// read-only reconciliation. Wait for its response to finish before
		// reconnecting, so the next counter increment proves a fresh-generation
		// journal+Engine reconciliation rather than a late old-generation read.
		return barrier.CompletedInspectCount(timeoutTargetID) >= timeoutInspectBaseline+3
	}, "Agent did not finish the same-generation read-only Engine reconciliation after the operation deadline")
	timeoutInspectCount := barrier.CompletedInspectCount(timeoutTargetID)
	timeoutRestartHits := barrier.RestartHitCount(timeoutTargetID)
	timeoutForwardedRestartCount := barrier.ForwardedRestartCount(timeoutTargetID)
	timeoutRestartIntercepts := timeoutRestartHits - timeoutRestartHitsBaseline
	timeoutForwardedRestarts := timeoutForwardedRestartCount - timeoutForwardedRestartsBaseline
	if timeoutRestartIntercepts != 1 || timeoutForwardedRestarts != 0 {
		t.Fatalf("timeout task should have one proxy-intercepted restart and zero Engine-forwarded restarts: total_hits=%d baseline_hits=%d total_forwarded=%d baseline_forwarded=%d",
			timeoutRestartHits, timeoutRestartHitsBaseline, timeoutForwardedRestartCount, timeoutForwardedRestartsBaseline)
	}
	if barrier.ForwardedRestartCount(timeoutTargetID) != timeoutForwardedRestartCount {
		t.Fatalf("proxy forwarded a restart despite holding its request through the operation deadline: before=%d after=%d",
			timeoutForwardedRestartCount, barrier.ForwardedRestartCount(timeoutTargetID))
	}
	barrier.Release()
	timeoutGeneration := agentGeneration
	connection = core.activeAgentConnectionForNode(config.NodeID)
	if connection == nil || connection.generation != timeoutGeneration {
		t.Fatalf("timeout task lost its authenticated Agent generation before reconciliation: active=%t expected=%d", connection != nil, timeoutGeneration)
	}
	core.closeAgentConnection(config.AgentID, timeoutGeneration)
	agentGeneration, reconnected = awaitS05AgentBridgeReady(core, config.NodeID, timeoutGeneration, 20*time.Second)
	if !reconnected || agentGeneration <= timeoutGeneration {
		t.Fatalf("Agent did not reconnect on a newer synchronized generation after timeout: previous=%d active=%s",
			timeoutGeneration, describeS05AgentTaskBridge(core, config.NodeID))
	}
	waitForS04DockerView(t, core, config.NodeID, 20*time.Second, func(view docker.View) bool {
		return view.ActiveGeneration == agentGeneration && view.AgentOnline && !view.DataStale &&
			view.DockerSnapshotFresh && view.DockerAvailability == protocol.DockerAvailabilityAvailable
	}, "same Agent did not restore fresh Engine state after timeout reconciliation")
	waitForCondition(t, 15*time.Second, func() bool {
		task, getErr := core.tasks.Get(context.Background(), config.NodeID, timeoutAccepted.TaskID)
		return getErr == nil && task.Status == "unknown" && task.Result.Code == coretasks.ResultUncertain && !task.ReconciliationRequired &&
			barrier.CompletedInspectCount(timeoutTargetID) > timeoutInspectCount
	}, "reconnected Agent did not read its durable execution history and inspect the actual Docker resource")
	journalEvidence, err := s05ReadAgentTaskExecutionEvidence(filepath.Join(filepath.Dir(configPath), "tasks.sqlite"), timeoutAccepted.TaskID)
	if err != nil || journalEvidence.Status != "unknown" || journalEvidence.ExecutionPhase != "mutation_may_have_started" ||
		!journalEvidence.BaselineVerified || journalEvidence.BaselineBootID == "" || journalEvidence.BaselineTargetID != timeoutTargetID ||
		journalEvidence.BaselineAction != "restart" || journalEvidence.BaselineRestartCount != timeoutRestartCount {
		t.Fatalf("Agent reconciliation did not preserve verified durable execution history: evidence=%+v err=%v", journalEvidence, err)
	}
	baselineStartedAt, baselineTimeErr := time.Parse(time.RFC3339Nano, journalEvidence.BaselineStartedAt)
	timeoutStartedAtTime, timeoutStartedAtErr := time.Parse(time.RFC3339Nano, strings.TrimSpace(timeoutStartedAt))
	if baselineTimeErr != nil || timeoutStartedAtErr != nil || !baselineStartedAt.Equal(timeoutStartedAtTime) {
		t.Fatalf("Agent execution baseline StartedAt did not match pre-operation Engine state: baseline=%q Engine=%q parse_errors=%v/%v",
			journalEvidence.BaselineStartedAt, strings.TrimSpace(timeoutStartedAt), baselineTimeErr, timeoutStartedAtErr)
	}
	timeoutAPI, err = getS05Task(client, coreHTTP.URL, session, config.NodeID, timeoutAccepted.TaskID)
	if err != nil || timeoutAPI.Status != "unknown" || timeoutAPI.Result.Code != string(coretasks.ResultUncertain) {
		t.Fatalf("reconnect changed the authenticated task progress API result: task=%+v err=%v", timeoutAPI, err)
	}
	timeoutStartedAfterReconnect, err := runS04DockerCLI(endpoint, "container", "inspect", "--format", "{{.State.StartedAt}}", timeoutTargetID)
	if err != nil || strings.TrimSpace(timeoutStartedAfterReconnect) != strings.TrimSpace(timeoutStartedAt) {
		t.Fatalf("reconnect replayed unknown timeout request: before=%q after=%q err=%v", strings.TrimSpace(timeoutStartedAt), strings.TrimSpace(timeoutStartedAfterReconnect), err)
	}
	timeoutCountAfterReconnect, err := readS05ContainerStartCount(endpoint, timeoutTargetID)
	if err != nil || timeoutCountAfterReconnect != timeoutStartCount {
		t.Fatalf("reconnect executed unknown timeout request again: before=%d after=%d err=%v", timeoutStartCount, timeoutCountAfterReconnect, err)
	}
	timeoutRestartCountAfterReconnect, err := readS05ContainerRestartCount(endpoint, timeoutTargetID)
	if err != nil || timeoutRestartCountAfterReconnect != timeoutRestartCount {
		t.Fatalf("reconnect changed Engine RestartCount for unknown request: before=%d after=%d err=%v", timeoutRestartCount, timeoutRestartCountAfterReconnect, err)
	}
	timeoutEventsAfterReconnect, err := s05DockerEventsAfter(endpoint, timeoutTargetID, timeoutEventsSince)
	if err != nil || len(timeoutEventsAfterReconnect) != 0 {
		t.Fatalf("reconnect emitted Engine events for the unknown timeout request: events=%v err=%v", timeoutEventsAfterReconnect, err)
	}
	if barrier.RestartHitCount(timeoutTargetID) != timeoutRestartHits || barrier.ForwardedRestartCount(timeoutTargetID) != timeoutForwardedRestartCount {
		t.Fatalf("reconnection replayed the uncertain restart: proxy_hits_before=%d after=%d forwarded_before=%d after=%d",
			timeoutRestartHits, barrier.RestartHitCount(timeoutTargetID), timeoutForwardedRestartCount,
			barrier.ForwardedRestartCount(timeoutTargetID))
	}
	t.Logf("S05_S06_RECONCILIATION agent_journal_status=%s execution_phase=%s baseline_verified=%t baseline_started_at=%s baseline_restart_count=%d engine_restart_count=%d engine_started_at_unchanged=true process_start_count_unchanged=true engine_events=0 agent_engine_inspect_requests=%d proxy_restart_intercepts=%d proxy_restart_forwarded=%d postcondition_proven=false ambiguity=daemon_acceptance_unproven unresolved_result=unknown verified=true",
		journalEvidence.Status, journalEvidence.ExecutionPhase, journalEvidence.BaselineVerified, journalEvidence.BaselineStartedAt,
		journalEvidence.BaselineRestartCount, timeoutRestartCountAfterReconnect,
		barrier.CompletedInspectCount(timeoutTargetID)-timeoutInspectCount, timeoutRestartIntercepts, timeoutForwardedRestarts)
	t.Logf("S05_S06_OPERATION_TIMEOUT task_status=unknown result=result_pending operation_deadline=%s delivered=true delete_status=409 engine_unchanged=true reconciliation_inspected=true proxy_forwarded_restart=0 reconnect_no_replay=true verified=true generation=%d->%d",
		agentOperationDeadline, timeoutGeneration, agentGeneration)

	previousGeneration = agentGeneration
	stopAgent()
	waitForAgentStatusWithin(t, core, config.NodeID, "offline", previousGeneration, 15*time.Second)
	offlineGeneration := generationForNode(t, core, config.NodeID)
	if offlineGeneration <= previousGeneration || core.activeAgentConnectionForNode(config.NodeID) != nil {
		t.Fatalf("offline submission precondition is not a genuinely expired Agent lease: previous=%d current=%d active=%t",
			previousGeneration, offlineGeneration, core.activeAgentConnectionForNode(config.NodeID) != nil)
	}
	offlineTargetID := fixtureBySlot[queuedSlot]
	offlineStartedAt, err := runS04DockerCLI(endpoint, "container", "inspect", "--format", "{{.State.StartedAt}}", offlineTargetID)
	if err != nil {
		t.Fatal("read offline target StartedAt before rejected write:", err)
	}
	offlineStartCount, err := readS05ContainerStartCount(endpoint, offlineTargetID)
	if err != nil {
		t.Fatal("read offline target process start count:", err)
	}
	offlineEventsSince := time.Now().UTC()
	offlineKey := "s05-offline-no-backlog-" + runID
	offlineStatus, _, offlineBody, err := postAction(offlineTargetID, offlineKey)
	if err != nil || offlineStatus != http.StatusServiceUnavailable {
		t.Fatalf("write against expired Agent lease returned status=%d body=%q err=%v, want 503", offlineStatus, offlineBody, err)
	}
	var coreTaskRows, dispatchAuditRows, resourceClaims int
	if err := core.store.DB.QueryRow(`SELECT count(*) FROM core_tasks WHERE node_id=? AND idempotency_key=?`, config.NodeID, offlineKey).Scan(&coreTaskRows); err != nil {
		t.Fatal("check offline Core task row:", err)
	}
	if err := core.store.DB.QueryRow(`SELECT count(*) FROM core_task_audit_events WHERE node_id=? AND task_id IN (SELECT task_id FROM core_tasks WHERE node_id=? AND idempotency_key=?)`, config.NodeID, config.NodeID, offlineKey).Scan(&dispatchAuditRows); err != nil {
		t.Fatal("check offline dispatch audit rows:", err)
	}
	if err := core.store.DB.QueryRow(`SELECT count(*) FROM core_task_resource_claims WHERE node_id=? AND resource_key=?`, config.NodeID, "docker-container:"+offlineTargetID).Scan(&resourceClaims); err != nil {
		t.Fatal("check offline resource claims:", err)
	}
	journalCount, err := s05AgentJournalCountByIdempotencyKey(filepath.Join(filepath.Dir(configPath), "tasks.sqlite"), offlineKey)
	if err != nil || coreTaskRows != 0 || dispatchAuditRows != 0 || journalCount != 0 || resourceClaims != 0 {
		t.Fatalf("offline write was queued, claimed, or journaled: core_rows=%d dispatch_audits=%d claims=%d agent_rows=%d err=%v",
			coreTaskRows, dispatchAuditRows, resourceClaims, journalCount, err)
	}
	startAgent(offlineGeneration)
	waitForS04DockerView(t, core, config.NodeID, 30*time.Second, func(view docker.View) bool {
		return view.ActiveGeneration == agentGeneration && view.AgentOnline && !view.DataStale && view.DockerSnapshotFresh &&
			view.DockerAvailability == protocol.DockerAvailabilityAvailable && dockerViewContains(view, offlineTargetID)
	}, "same Agent journal did not restore a fresh Engine view after the offline rejection")
	if rows, err := s05AgentJournalCountByIdempotencyKey(filepath.Join(filepath.Dir(configPath), "tasks.sqlite"), offlineKey); err != nil || rows != 0 {
		t.Fatalf("Agent journal acquired an offline request after reconnect: rows=%d err=%v", rows, err)
	}
	if rows, err := core.store.DB.Query(`SELECT task_id FROM core_tasks WHERE node_id=? AND idempotency_key=?`, config.NodeID, offlineKey); err != nil {
		t.Fatal("query offline request after Agent recovery:", err)
	} else {
		if rows.Next() {
			rows.Close()
			t.Fatal("Core created a task for an offline request after the Agent recovered")
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			t.Fatal("finish offline task query:", err)
		}
		rows.Close()
	}
	afterOfflineStartedAt, err := runS04DockerCLI(endpoint, "container", "inspect", "--format", "{{.State.StartedAt}}", offlineTargetID)
	if err != nil || strings.TrimSpace(afterOfflineStartedAt) != strings.TrimSpace(offlineStartedAt) {
		t.Fatalf("offline request was replayed after reconnect: StartedAt before=%q after=%q err=%v", strings.TrimSpace(offlineStartedAt), strings.TrimSpace(afterOfflineStartedAt), err)
	}
	afterOfflineStartCount, err := readS05ContainerStartCount(endpoint, offlineTargetID)
	if err != nil || afterOfflineStartCount != offlineStartCount {
		t.Fatalf("offline request changed target process count after reconnect: before=%d after=%d err=%v", offlineStartCount, afterOfflineStartCount, err)
	}
	offlineEvents, err := s05DockerEventsAfter(endpoint, offlineTargetID, offlineEventsSince)
	if err != nil || len(offlineEvents) != 0 {
		t.Fatalf("offline request created Engine events after reconnect: events=%v err=%v", offlineEvents, err)
	}
	t.Logf("S05_S06_OFFLINE post_status=503 core_rows=0 dispatch_rows=0 resource_claims=0 journal_rows=0 reconnect_backlog=0 engine_events=0 verified=true generation=%d->%d",
		previousGeneration, agentGeneration)
	t.Logf("S05_S06_CASE queued_cancel=not_dispatched repeated_delete=same_result docker_unchanged=true offline_post=503 no_rows=true reconnect_no_backlog=true delivered_timeout=unknown_result_pending no_replay=true verified=true Engine=%s", engineVersion)
}

func waitForS05FixtureInventory(t *testing.T, core *Server, endpoint, nodeID string, generation uint64, fixtureIDs []string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var latest docker.View
	var revision uint64
	for time.Now().Before(deadline) {
		_, view, err := core.dockerViewForNode(context.Background(), nodeID)
		if err == nil {
			latest = view
			_, revision, _ = core.dockerSnapshotWithRevision(nodeID, nil, time.Now().UTC())
			complete := view.ActiveGeneration == generation && view.AgentOnline && !view.DataStale && view.DockerSnapshotFresh &&
				view.DockerAvailability == protocol.DockerAvailabilityAvailable
			for _, id := range fixtureIDs {
				if !dockerViewContains(view, id) {
					complete = false
					break
				}
			}
			if complete {
				return
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	engineOutput, engineErr := runS04DockerCLI(endpoint, "container", "ls", "--all", "--quiet", "--no-trunc")
	engineIDs := strings.Fields(engineOutput)
	for i := range engineIDs {
		engineIDs[i] = strings.TrimSpace(engineIDs[i])
	}
	sort.Strings(engineIDs)
	viewIDs := make([]string, 0, len(latest.Containers))
	viewRecords := make([]string, 0, len(latest.Containers))
	viewSet := make(map[string]struct{}, len(latest.Containers))
	for _, record := range latest.Containers {
		id := record.Container.ID
		viewIDs = append(viewIDs, id)
		viewRecords = append(viewRecords, fmt.Sprintf("%s:g%d:s%d:%s:stale=%t", id, record.Generation, record.Sequence, record.Container.State, record.Container.Stale))
		viewSet[id] = struct{}{}
	}
	sort.Strings(viewIDs)
	sort.Strings(viewRecords)
	missing := make([]string, 0)
	for _, id := range fixtureIDs {
		if _, ok := viewSet[id]; !ok {
			missing = append(missing, id)
		}
	}
	sort.Strings(missing)
	missingInspect := make([]string, 0, len(missing))
	for _, id := range missing {
		state, inspectErr := runS04DockerCLI(endpoint, "container", "inspect", "--format", "{{json .State}}", id)
		missingInspect = append(missingInspect, fmt.Sprintf("%s:%s:error=%v", id, strings.TrimSpace(state), inspectErr))
	}
	fixtureSequences := make(map[uint64]struct{}, len(fixtureIDs))
	for _, record := range latest.Containers {
		for _, id := range fixtureIDs {
			if record.Container.ID == id {
				fixtureSequences[record.Sequence] = struct{}{}
				break
			}
		}
	}
	sequences := make([]uint64, 0, len(fixtureSequences))
	for sequence := range fixtureSequences {
		sequences = append(sequences, sequence)
	}
	sort.Slice(sequences, func(i, j int) bool { return sequences[i] < sequences[j] })
	t.Logf("S05_S06_INVENTORY_DIAGNOSTIC generation=%d view_generation=%d revision=%d fresh=%t events_connected=%t availability=%s engine_ids=%v engine_error=%v view_ids=%v view_records=%v missing_fixture_ids=%v missing_fixture_inspect=%v fixture_record_sequences=%v distinct_fixture_sequences=%d snapshot_chunk_count=not-retained-by-store",
		generation, latest.ActiveGeneration, revision, latest.DockerSnapshotFresh, latest.DockerEventsConnected, latest.DockerAvailability,
		engineIDs, engineErr, viewIDs, viewRecords, missing, missingInspect, sequences, len(sequences))
	t.Fatalf("fresh Core inventory failed to contain all exact S05 fixtures: fixtures=%d Core=%d generation=%d", len(fixtureIDs), len(latest.Containers), latest.ActiveGeneration)
}

type s05CancellationBarrierProxy struct {
	endpoint          string
	listener          net.Listener
	server            *http.Server
	transport         *http.Transport
	mu                sync.Mutex
	targets           map[string]struct{}
	inspectHits       map[string]int
	inspectCompleted  map[string]int
	restartHits       map[string]int
	forwardedRestarts map[string]int
	engineOn          bool
	released          chan struct{}
	releaseDo         sync.Once
	hits              chan string
}

func newS05CancellationBarrierProxy(path, engineEndpoint string) (*s05CancellationBarrierProxy, error) {
	parsed, err := url.Parse(engineEndpoint)
	if err != nil || parsed.Scheme != "unix" || parsed.Path == "" {
		return nil, fmt.Errorf("dispatch barrier requires an explicit owned Unix Engine endpoint")
	}
	listener, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = listener.Close()
		return nil, err
	}
	proxy := &s05CancellationBarrierProxy{
		endpoint: "unix://" + path, listener: listener,
		transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", parsed.Path)
		}},
		targets: make(map[string]struct{}), inspectHits: make(map[string]int), inspectCompleted: make(map[string]int), restartHits: make(map[string]int),
		forwardedRestarts: make(map[string]int),
		released:          make(chan struct{}), hits: make(chan string, 64),
	}
	proxy.server = &http.Server{Handler: http.HandlerFunc(proxy.serveHTTP), ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = proxy.server.Serve(listener) }()
	return proxy, nil
}

func (p *s05CancellationBarrierProxy) Host() string { return p.endpoint }

func (p *s05CancellationBarrierProxy) EnableEngineForwarding() {
	p.mu.Lock()
	p.engineOn = true
	p.mu.Unlock()
}

func (p *s05CancellationBarrierProxy) ArmTargets(targets []string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	// Callers re-arm only after every request from the previous barrier has
	// completed. Drain its request markers so WaitForHits is scoped to the next
	// barrier generation.
	for {
		select {
		case <-p.hits:
		default:
			goto drained
		}
	}
drained:
	p.targets = make(map[string]struct{}, len(targets))
	for _, target := range targets {
		p.targets[target] = struct{}{}
	}
	p.released = make(chan struct{})
	p.releaseDo = sync.Once{}
}

func (p *s05CancellationBarrierProxy) WaitForHits(count int, timeout time.Duration) bool {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	seen := make(map[string]struct{}, count)
	for len(seen) < count {
		select {
		case id := <-p.hits:
			seen[id] = struct{}{}
		case <-deadline.C:
			return false
		}
	}
	return true
}

func (p *s05CancellationBarrierProxy) HitIDs() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	ids := make([]string, 0, len(p.targets))
	for id := range p.targets {
		ids = append(ids, id)
	}
	return ids
}

func (p *s05CancellationBarrierProxy) InspectCount(containerID string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.inspectHits[containerID]
}

func (p *s05CancellationBarrierProxy) CompletedInspectCount(containerID string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.inspectCompleted[containerID]
}

func (p *s05CancellationBarrierProxy) RestartHitCount(containerID string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.restartHits[containerID]
}

func (p *s05CancellationBarrierProxy) ForwardedRestartCount(containerID string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.forwardedRestarts[containerID]
}

func (p *s05CancellationBarrierProxy) Release() {
	p.mu.Lock()
	p.releaseDo.Do(func() { close(p.released) })
	p.mu.Unlock()
}

func (p *s05CancellationBarrierProxy) serveHTTP(w http.ResponseWriter, request *http.Request) {
	target := ""
	p.mu.Lock()
	for id := range p.targets {
		if strings.Contains(request.URL.Path, "/containers/"+id+"/restart") && request.Method == http.MethodPost {
			target = id
			p.restartHits[id]++
			break
		}
		if strings.Contains(request.URL.Path, "/containers/"+id+"/json") && request.Method == http.MethodGet {
			p.inspectHits[id]++
		}
	}
	engineOn := p.engineOn
	released := p.released
	p.mu.Unlock()
	if !engineOn {
		http.Error(w, "test Docker barrier is not enabled", http.StatusServiceUnavailable)
		return
	}
	if target != "" {
		select {
		case p.hits <- target:
		default:
		}
		select {
		case <-request.Context().Done():
			return
		case <-released:
		}
		p.mu.Lock()
		p.forwardedRestarts[target]++
		p.mu.Unlock()
	}
	upstream := request.Clone(request.Context())
	upstream.RequestURI = ""
	upstream.URL = &url.URL{Scheme: "http", Host: "docker", Path: request.URL.Path, RawPath: request.URL.RawPath, RawQuery: request.URL.RawQuery}
	response, err := p.transport.RoundTrip(upstream)
	if err != nil {
		http.Error(w, "owned DIND proxy request failed", http.StatusBadGateway)
		return
	}
	defer response.Body.Close()
	for name, values := range response.Header {
		for _, value := range values {
			w.Header().Add(name, value)
		}
	}
	w.WriteHeader(response.StatusCode)
	if flusher, ok := w.(http.Flusher); ok {
		// Docker events may stay open without an immediate body frame. Flush
		// the upstream status and headers so the SDK can establish its stream.
		flusher.Flush()
	}
	if _, err := io.Copy(w, response.Body); err != nil {
		return
	}
	if request.Method == http.MethodGet && strings.Contains(request.URL.Path, "/containers/") && strings.HasSuffix(request.URL.Path, "/json") {
		p.mu.Lock()
		for id := range p.targets {
			if strings.Contains(request.URL.Path, "/containers/"+id+"/json") {
				p.inspectCompleted[id]++
				break
			}
		}
		p.mu.Unlock()
	}
}

func (p *s05CancellationBarrierProxy) Close() error {
	if p == nil {
		return nil
	}
	p.Release()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	err := p.server.Shutdown(ctx)
	p.transport.CloseIdleConnections()
	_ = os.Remove(strings.TrimPrefix(p.endpoint, "unix://"))
	return err
}

func cleanupS05CancelOfflineFixtures(t *testing.T, endpoint, suite string, ids []string, names map[string]string) {
	t.Helper()
	actual, err := s05ListSuiteFixtureIDs(endpoint, suite)
	if err != nil {
		t.Errorf("inspect S05-06 suite inventory before cleanup: %v", err)
		return
	}
	actualSet := make(map[string]bool, len(actual))
	for _, id := range actual {
		actualSet[id] = true
	}
	wantSet := make(map[string]bool, len(ids))
	for _, id := range ids {
		wantSet[id] = true
		if !actualSet[id] {
			t.Errorf("expected S05-06 fixture missing before cleanup: id=%s name=%s", id, names[id])
			continue
		}
		if err := s05VerifySuiteFixtureIdentity(endpoint, suite, id); err != nil {
			t.Errorf("refuse cleanup of S05-06 fixture without matching suite identity: id=%s err=%v", id, err)
			continue
		}
		if _, err := runS04DockerCLI(endpoint, "container", "rm", "--force", id); err != nil {
			t.Errorf("remove exact S05-06 fixture id=%s: %v", id, err)
			continue
		}
		if present, err := s05OwnedContainerPresent(endpoint, id); err != nil || present {
			t.Errorf("exact S05-06 fixture cleanup could not be verified: id=%s present=%t err=%v", id, present, err)
			continue
		}
		t.Logf("S05_S06_FIXTURE_CLEANUP suite=%s id=%s name=%s removed=true verified_absent=true", suite, id, names[id])
	}
	for id := range actualSet {
		if !wantSet[id] {
			t.Errorf("unexpected container carries this unique S05-06 suite label: id=%s", id)
		}
	}
	remaining, err := s05ListSuiteFixtureIDs(endpoint, suite)
	if err != nil || len(remaining) != 0 {
		t.Errorf("S05-06 suite cleanup left resources: remaining=%v err=%v", remaining, err)
		return
	}
	t.Logf("S05_S06_FIXTURE_MANIFEST suite=%s expected_ids=%d remaining_ids=0 verified=true", suite, len(ids))
}

func s05ReadAgentTaskJournal(path, taskID string) (bool, error) {
	fileURL := (&url.URL{Scheme: "file", Path: path}).String()
	db, err := sql.Open("sqlite", fileURL+"?mode=ro")
	if err != nil {
		return false, err
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var found int
	err = db.QueryRowContext(ctx, `SELECT count(*) FROM task_journal WHERE task_id=?`, taskID).Scan(&found)
	if err != nil {
		return false, err
	}
	return found == 1, nil
}

func s05AgentJournalCountByIdempotencyKey(path, key string) (int, error) {
	fileURL := (&url.URL{Scheme: "file", Path: path}).String()
	db, err := sql.Open("sqlite", fileURL+"?mode=ro")
	if err != nil {
		return 0, err
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var found int
	err = db.QueryRowContext(ctx, `SELECT count(*) FROM task_journal WHERE idempotency_key=?`, key).Scan(&found)
	return found, err
}

func readS05ContainerRestartCount(endpoint, containerID string) (int, error) {
	output, err := runS04DockerCLI(endpoint, "container", "inspect", "--format", "{{.RestartCount}}", containerID)
	if err != nil {
		return 0, err
	}
	count, err := strconv.Atoi(strings.TrimSpace(output))
	if err != nil {
		return 0, fmt.Errorf("parse Docker restart count %q: %w", output, err)
	}
	return count, nil
}

type s05AgentTaskExecutionEvidence struct {
	Status               string
	ExecutionPhase       string
	BaselineVerified     bool
	BaselineTargetID     string
	BaselineAction       string
	BaselineBootID       string
	BaselineStartedAt    string
	BaselineRestartCount int
}

func s05ReadAgentTaskExecutionEvidence(path, taskID string) (s05AgentTaskExecutionEvidence, error) {
	fileURL := (&url.URL{Scheme: "file", Path: path}).String()
	db, err := sql.Open("sqlite", fileURL+"?mode=ro")
	if err != nil {
		return s05AgentTaskExecutionEvidence{}, err
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var evidence s05AgentTaskExecutionEvidence
	var baselineVerified int
	err = db.QueryRowContext(ctx, `SELECT status,execution_phase,baseline_verified,baseline_target_id,baseline_action,baseline_host_boot_id,baseline_started_at,baseline_restart_count
		FROM task_journal WHERE task_id=?`, taskID).Scan(&evidence.Status, &evidence.ExecutionPhase, &baselineVerified,
		&evidence.BaselineTargetID, &evidence.BaselineAction, &evidence.BaselineBootID, &evidence.BaselineStartedAt, &evidence.BaselineRestartCount)
	evidence.BaselineVerified = baselineVerified == 1
	return evidence, err
}

func s05DockerEventsAfter(endpoint, containerID string, since time.Time) ([]string, error) {
	until := time.Now().UTC().Add(250 * time.Millisecond)
	output, err := runS04DockerCLI(endpoint, "events", "--since", since.UTC().Format(time.RFC3339Nano),
		"--until", until.Format(time.RFC3339Nano), "--filter", "container="+containerID, "--format", "{{.TimeNano}}|{{.Actor.ID}}|{{.Action}}")
	if err != nil {
		return nil, err
	}
	var events []string
	allowedCounterProbeEvents := map[string]struct{}{"exec_create": {}, "exec_start": {}, "exec_die": {}}
	for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "|", 3)
		if len(parts) != 3 {
			return nil, fmt.Errorf("unexpected Docker events output %q", line)
		}
		nanos, err := strconv.ParseInt(parts[0], 10, 64)
		if err != nil {
			return nil, fmt.Errorf("parse Docker event timestamp %q: %w", parts[0], err)
		}
		if strings.TrimSpace(parts[1]) != containerID {
			return nil, fmt.Errorf("Docker events filter for %q returned event for %q: %s", containerID, strings.TrimSpace(parts[1]), line)
		}
		if nanos > since.UnixNano() {
			action := strings.TrimSpace(parts[2])
			if index := strings.IndexByte(action, ':'); index >= 0 {
				action = action[:index]
			}
			if _, isCounterProbe := allowedCounterProbeEvents[strings.TrimSpace(action)]; !isCounterProbe {
				events = append(events, action)
			}
		}
	}
	return events, nil
}
