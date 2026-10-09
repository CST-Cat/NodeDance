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
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/CST-Cat/NodeDance/internal/agent"
	coredocker "github.com/CST-Cat/NodeDance/internal/core/docker"
	"github.com/CST-Cat/NodeDance/internal/protocol"
	"github.com/coder/websocket"
)

const s04BusyboxImage = "busybox:1.37.0@sha256:bdf57e528e45e4433820e045b29b4597825a1c9e38353532d90a01445013f82e"

func TestRealDockerAgentCoreMultiChunkSnapshotReconnect(t *testing.T) {
	root, endpoint, engineVersion := requireOwnedS04DIND(t)
	t.Setenv("DOCKER_HOST", endpoint)
	workRoot := filepath.Join(root, ".artifacts", "work-s04")
	if err := os.MkdirAll(workRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	work, err := os.MkdirTemp(workRoot, "core-agent-docker-*")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(work, 0o700); err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(work)

	if _, err := runS04DockerCLI(endpoint, "image", "inspect", s04BusyboxImage); err != nil {
		if _, pullErr := runS04DockerCLI(endpoint, "pull", s04BusyboxImage); pullErr != nil {
			t.Fatalf("pull locked S04 fixture image: %v", pullErr)
		}
	}
	runID := fmt.Sprintf("nd-s04-%d", time.Now().UnixNano())
	composeProject := runID + "-compose"
	composeFile := filepath.Join(work, "compose.yaml")
	t.Logf("owned DIND multi-chunk run=%s Engine=%s", runID, engineVersion)
	containerIDs := make([]string, 0, 201)
	defer func() {
		cleanupFailures := 0
		for _, id := range containerIDs {
			if _, err := runS04DockerCLI(endpoint, "container", "rm", "--force", id); err != nil {
				cleanupFailures++
				t.Errorf("cleanup exact S04 fixture container %s: %v", id, err)
			}
		}
		if _, err := runS04DockerCompose(filepath.Dir(composeFile), "--project-name", composeProject, "--file", composeFile, "down", "--volumes"); err != nil {
			cleanupFailures++
			t.Errorf("cleanup exact Compose project %s and its network: %v", composeProject, err)
		}
		t.Logf("owned DIND multi-chunk run=%s exact-container-cleanup=%d/%d failures=%d", runID, len(containerIDs)-cleanupFailures, len(containerIDs), cleanupFailures)
	}()
	for index := 0; index < 200; index++ {
		name := fmt.Sprintf("%s-%03d", runID, index)
		id, err := runS04DockerCLI(endpoint, "container", "create", "--name", name,
			"--label", "io.nodedance.test=true", "--label", "io.nodedance.suite="+runID,
			s04BusyboxImage, "sh", "-c", "sleep 300")
		if err != nil {
			t.Fatalf("create owned multi-chunk fixture %d: %v", index, err)
		}
		containerIDs = append(containerIDs, strings.TrimSpace(id))
	}
	composeYAML := fmt.Sprintf("services:\n  web:\n    image: %s\n    command: [\"sh\", \"-c\", \"sleep 300\"]\n    labels:\n      io.nodedance.test: \"true\"\n      io.nodedance.suite: %q\n", s04BusyboxImage, runID)
	if err := os.WriteFile(composeFile, []byte(composeYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := runS04DockerCompose(filepath.Dir(composeFile), "--project-name", composeProject, "--file", composeFile, "create"); err != nil {
		t.Fatalf("create an actual Compose-managed stopped fixture: %v", err)
	}
	composeIDs, err := runS04DockerCompose(filepath.Dir(composeFile), "--project-name", composeProject, "--file", composeFile, "ps", "--all", "--quiet")
	if err != nil {
		t.Fatalf("inspect exact Compose project fixture IDs: %v", err)
	}
	composeIDLines := strings.Fields(composeIDs)
	if len(composeIDLines) != 1 {
		t.Fatalf("expected one created-only Compose container, got %d: %q", len(composeIDLines), composeIDs)
	}
	composeID := composeIDLines[0]
	containerIDs = append(containerIDs, composeID)

	certificate, rootPEM, err := makeAgentTestCertificate()
	if err != nil {
		t.Fatal(err)
	}
	caPath := filepath.Join(work, "trusted-ca.pem")
	if err := os.WriteFile(caPath, rootPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	core, err := New("s04-docker-integration", Options{DataDir: filepath.Join(work, "core"), PublicOrigin: "https://panel.test", WebSocketCheckInterval: 100 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	var current atomic.Pointer[Server]
	current.Store(core)
	coreHTTP := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		active := current.Load()
		if active == nil {
			http.Error(w, "Core restarting", http.StatusServiceUnavailable)
			return
		}
		active.ServeHTTP(w, r)
	}))
	coreHTTP.TLS = &tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS12}
	coreHTTP.StartTLS()
	proxy := newAgentTestProxy(t, &current, coreHTTP.URL, rootPEM)
	proxyServer := httptest.NewUnstartedServer(proxy)
	proxyServer.TLS = &tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS12}
	proxyServer.StartTLS()
	proxy.proxyURL = proxyServer.URL
	cleanupAgent := func() {}
	defer func() {
		cleanupAgent()
		current.Store(nil)
		proxyServer.Close()
		coreHTTP.Close()
		_ = core.Close()
	}()

	session, csrf, err := installIntegrationAdmin(core)
	if err != nil {
		t.Fatal(err)
	}
	enrollment, err := core.agents.CreateEnrollment(context.Background(), "S04 multi-chunk DIND "+engineVersion,
		"127.0.0.1", sql.NullInt64{Int64: 1, Valid: true})
	if err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(work, "agent", "agent.json")
	if err := agent.Enroll(context.Background(), proxyServer.URL, caPath, false, strings.NewReader(enrollment.Token), configPath); err != nil {
		t.Fatalf("enroll real Agent against Core TLS/WSS: %v", err)
	}
	config, err := agent.LoadConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	agentCtx, cancelAgent := context.WithCancel(context.Background())
	agentDone := make(chan error, 1)
	agentLog := &agentTestLog{}
	go func() { agentDone <- agent.Run(agentCtx, configPath, "s04-integration", agentLog) }()
	cleanupAgent = func() {
		cancelAgent()
		select {
		case <-agentDone:
		case <-time.After(10 * time.Second):
			t.Errorf("real Agent did not stop after test cancellation")
		}
	}
	proxy.dropNextDockerSnapshotChunk()
	waitForAgentStatus(t, core, config.NodeID, "online", 0)
	firstGeneration := generationForNode(t, core, config.NodeID)
	select {
	case <-proxy.droppedDockerChunk:
	case <-time.After(15 * time.Second):
		t.Fatal("real Agent did not send the first non-final Docker snapshot chunk")
	}
	proxy.networkDown.Store(true)
	waitForCondition(t, 5*time.Second, func() bool { return proxy.connectionAttempts.Load() > 0 }, "Agent did not attempt reconnect during injected snapshot interruption")
	var partialRows int
	if err := core.store.DB.QueryRow(`SELECT count(*) FROM docker_containers WHERE node_id=?`, config.NodeID).Scan(&partialRows); err != nil || partialRows != 0 {
		t.Fatalf("incomplete multi-chunk snapshot became visible: rows=%d err=%v", partialRows, err)
	}

	proxy.networkDown.Store(false)
	waitForAgentStatusWithin(t, core, config.NodeID, "online", firstGeneration, 30*time.Second)
	inventory := waitForDockerInventoryContains(t, coreHTTP.URL, rootPEM, session, config.NodeID, containerIDs, 45*time.Second)
	if got := proxy.dockerChunks.Load(); got < 3 {
		t.Fatalf("Core-Agent path did not carry multiple Docker snapshot frames: observed %d", got)
	}
	if generation := generationForNode(t, core, config.NodeID); generation <= firstGeneration {
		t.Fatalf("snapshot reconnect did not use a newer Core generation: before=%d after=%d", firstGeneration, generation)
	}
	var committedRows int
	if err := core.store.DB.QueryRow(`SELECT count(*) FROM docker_containers WHERE node_id=?`, config.NodeID).Scan(&committedRows); err != nil || committedRows < 200 {
		t.Fatalf("final complete snapshot was not atomically committed: rows=%d err=%v", committedRows, err)
	}

	if inventory.Inventory == nil || len(inventory.Inventory.Containers) != 201 {
		t.Fatalf("private Core container inventory did not expose the committed Agent snapshot: count=%d", inventoryCount(inventory))
	}
	var composeRecord *coredocker.ContainerRecord
	for index := range inventory.Inventory.Containers {
		record := &inventory.Inventory.Containers[index]
		if record.Container.ID == composeID {
			composeRecord = record
			break
		}
	}
	if composeRecord == nil || composeRecord.Container.Compose == nil || composeRecord.Container.Compose.Project != composeProject || composeRecord.Container.Compose.Service != "web" || composeRecord.Container.State != "created" {
		t.Fatalf("Core did not discover the real stopped Compose service with project/service identity: %+v", composeRecord)
	}
	verifyS04ContainerDetailAPI(t, coreHTTP.URL, rootPEM, session, config.NodeID, containerIDs[0])
	verifyS04DashboardAuthorization(t, coreHTTP.URL, rootPEM)
	dashboard := openS04Dashboard(t, coreHTTP.URL, rootPEM, session)
	defer dashboard.CloseNow()
	initialPush := readS04DashboardInventory(t, dashboard, config.NodeID, 15*time.Second)
	if len(initialPush.Containers) != len(inventory.Inventory.Containers) || len(initialPush.Containers) != 201 {
		t.Fatalf("Dashboard initial push does not contain the committed inventory: API=%d WebSocket=%d", len(inventory.Inventory.Containers), len(initialPush.Containers))
	}
	dashboardEvents, stopDashboardReader := startS04DashboardReader(dashboard)
	defer stopDashboardReader()
	assertNoRepeatedDashboardInventoryWhileHeartbeatAdvances(t, dashboardEvents, core, config.NodeID, 7*time.Second)
	eventName := fmt.Sprintf("%s-event", runID)
	eventID, err := runS04DockerCLI(endpoint, "container", "create", "--name", eventName,
		"--label", "io.nodedance.test=true", "--label", "io.nodedance.suite="+runID,
		s04BusyboxImage, "sh", "-c", "sleep 300")
	if err != nil {
		t.Fatalf("create live-event fixture: %v", err)
	}
	eventID = strings.TrimSpace(eventID)
	containerIDs = append(containerIDs, eventID)
	pushed := waitForS04DashboardInventory(t, dashboardEvents, config.NodeID, func(view coredocker.View) bool {
		return dockerViewContains(view, eventID)
	}, 10*time.Second)
	if len(pushed.Containers) < 201 {
		t.Fatalf("Dashboard event push did not converge to the new external container: count=%d", len(pushed.Containers))
	}
	if record, err := waitForS04ContainerRecordAPI(coreHTTP.URL, rootPEM, session, config.NodeID, eventID, func(record coredocker.ContainerRecord) bool {
		return record.Container.State == "created" && !record.Container.Running
	}, 10*time.Second); err != nil || record.Container.ID != eventID {
		t.Fatalf("created external independent container was not available via private API: record=%+v err=%v", record, err)
	}
	t.Logf("S04-02 external lifecycle API observed create container=%s state=created", eventID)
	if _, err := runS04DockerCLI(endpoint, "container", "start", eventID); err != nil {
		t.Fatalf("external start: %v", err)
	}
	if _, err := waitForS04ContainerRecordAPI(coreHTTP.URL, rootPEM, session, config.NodeID, eventID, func(record coredocker.ContainerRecord) bool {
		return record.Container.State == "running" && record.Container.Running
	}, 10*time.Second); err != nil {
		t.Fatalf("Core did not apply external start event: %v", err)
	}
	t.Logf("S04-02 external lifecycle API observed start container=%s state=running", eventID)
	if _, err := runS04DockerCLI(endpoint, "container", "pause", eventID); err != nil {
		t.Fatalf("external pause: %v", err)
	}
	if _, err := waitForS04ContainerRecordAPI(coreHTTP.URL, rootPEM, session, config.NodeID, eventID, func(record coredocker.ContainerRecord) bool {
		return record.Container.State == "paused" && record.Container.Paused
	}, 10*time.Second); err != nil {
		t.Fatalf("Core did not apply external pause event: %v", err)
	}
	t.Logf("S04-02 external lifecycle API observed pause container=%s state=paused", eventID)
	proxy.replayNextDockerChangeOutOfOrderAndDuplicate()
	if _, err := runS04DockerCLI(endpoint, "container", "unpause", eventID); err != nil {
		t.Fatalf("external unpause: %v", err)
	}
	var replayEvidence dockerReplayEvidence
	select {
	case replayEvidence = <-proxy.dockerReplayResults:
	case <-time.After(10 * time.Second):
		t.Fatal("authenticated Agent WSS proxy did not inject an older Docker frame and duplicate after a newer event")
	}
	if replayEvidence.OlderSequence == 0 || replayEvidence.NewerSequence <= replayEvidence.OlderSequence ||
		replayEvidence.OlderState != "paused" || replayEvidence.NewerState != "running" || replayEvidence.DuplicateWrites != 1 {
		t.Fatalf("WSS replay injection did not carry paused→running newer-then-older and duplicate frames: %+v", replayEvidence)
	}
	if _, err := waitForS04ContainerRecordAPI(coreHTTP.URL, rootPEM, session, config.NodeID, eventID, func(record coredocker.ContainerRecord) bool {
		return record.Container.State == "running" && !record.Container.Paused
	}, 10*time.Second); err != nil {
		t.Fatalf("Core did not apply external unpause event: %v", err)
	}
	t.Logf("S04-09 authenticated WSS accepted newer frame seq=%d state=%s, then ignored older seq=%d state=%s and duplicate; Core API remained running",
		replayEvidence.NewerSequence, replayEvidence.NewerState, replayEvidence.OlderSequence, replayEvidence.OlderState)
	t.Logf("S04-02 external lifecycle API observed unpause container=%s state=running", eventID)
	if _, err := runS04DockerCLI(endpoint, "container", "stop", "--time", "0", eventID); err != nil {
		t.Fatalf("external stop: %v", err)
	}
	if _, err := waitForS04ContainerRecordAPI(coreHTTP.URL, rootPEM, session, config.NodeID, eventID, func(record coredocker.ContainerRecord) bool {
		return record.Container.State == "exited" && !record.Container.Running
	}, 10*time.Second); err != nil {
		t.Fatalf("Core did not apply external stop event: %v", err)
	}
	t.Logf("S04-02 external lifecycle API observed stop container=%s state=exited", eventID)
	renamedEvent := eventName + "-renamed"
	if _, err := runS04DockerCLI(endpoint, "container", "rename", eventID, renamedEvent); err != nil {
		t.Fatalf("external rename: %v", err)
	}
	if record, err := waitForS04ContainerRecordAPI(coreHTTP.URL, rootPEM, session, config.NodeID, eventID, func(record coredocker.ContainerRecord) bool {
		return record.Container.Name == renamedEvent
	}, 10*time.Second); err != nil || record.Container.Name != renamedEvent {
		t.Fatalf("Core did not apply external rename event: record=%+v err=%v", record, err)
	}
	t.Logf("S04-02 external lifecycle API observed rename container=%s name=%s", eventID, renamedEvent)
	if _, err := runS04DockerCLI(endpoint, "container", "rm", "--force", eventID); err != nil {
		t.Fatalf("external delete: %v", err)
	}
	if err := waitForS04ContainerAbsentAPI(coreHTTP.URL, rootPEM, session, config.NodeID, eventID, 10*time.Second); err != nil {
		t.Fatal(err)
	}
	containerIDs = removeS04FixtureID(containerIDs, eventID)
	if _, err := waitForS04DashboardInventoryAbsent(t, dashboardEvents, config.NodeID, eventID, 10*time.Second); err != nil {
		t.Fatal(err)
	}
	t.Logf("S04-02 external lifecycle API observed delete container=%s absent from API and dashboard", eventID)
	waitForDockerInventoryContains(t, coreHTTP.URL, rootPEM, session, config.NodeID, containerIDs, 10*time.Second)
	// Interrupt a second multi-chunk snapshot after a complete inventory has
	// committed. This proves that the incomplete replacement does not erase or
	// partially publish the last authoritative rows.
	committedGeneration := generationForNode(t, core, config.NodeID)
	connectionAttempts := proxy.connectionAttempts.Load()
	proxy.dropNextDockerSnapshotChunkAndBlockReconnect()
	proxy.closeAgentTunnels()
	select {
	case <-proxy.droppedDockerChunk:
	case <-time.After(15 * time.Second):
		t.Fatal("real Agent did not send the deliberately interrupted replacement snapshot chunk")
	}
	waitForCondition(t, 10*time.Second, func() bool {
		return proxy.connectionAttempts.Load() > connectionAttempts && generationForNode(t, core, config.NodeID) > committedGeneration
	}, "Agent did not enter the blocked reconnect after the replacement snapshot was interrupted")
	partialGeneration := generationForNode(t, core, config.NodeID)
	partialView := waitForS04DockerView(t, core, config.NodeID, 5*time.Second, func(view coredocker.View) bool {
		return view.ActiveGeneration == partialGeneration && view.DataStale && len(view.Containers) == len(containerIDs)
	}, "incomplete replacement snapshot retains the old inventory as stale")
	if got := dockerViewContainerIDs(partialView); strings.Join(got, ",") != strings.Join(sortedStrings(containerIDs), ",") {
		t.Fatalf("incomplete replacement snapshot changed the prior committed container IDs: got=%v want=%v", got, sortedStrings(containerIDs))
	}
	var retainedRows int
	if err := core.store.DB.QueryRow(`SELECT count(*) FROM docker_containers WHERE node_id=?`, config.NodeID).Scan(&retainedRows); err != nil || retainedRows != len(containerIDs) {
		t.Fatalf("incomplete replacement snapshot changed persisted inventory rows: got=%d want=%d err=%v", retainedRows, len(containerIDs), err)
	}
	t.Logf("incomplete replacement snapshot retained %d previously committed IDs and SQLite rows as stale at generation=%d", retainedRows, partialGeneration)
	proxy.networkDown.Store(false)
	waitForAgentStatusWithin(t, core, config.NodeID, "online", partialGeneration, 30*time.Second)
	recoveredView := waitForS04DockerView(t, core, config.NodeID, 30*time.Second, func(view coredocker.View) bool {
		return view.AgentOnline && view.DockerAvailability == "available" && view.DockerSnapshotFresh && !view.DataStale && len(view.Containers) == len(containerIDs)
	}, "complete replacement snapshot restores the retained inventory")
	if got := dockerViewContainerIDs(recoveredView); strings.Join(got, ",") != strings.Join(sortedStrings(containerIDs), ",") {
		t.Fatalf("recovered replacement snapshot changed the authoritative container IDs: got=%v want=%v", got, sortedStrings(containerIDs))
	}
	sequenceBeforeDrop := heartbeatSequenceForNode(t, core, config.NodeID)
	droppedBefore := proxy.droppedDockerMessages.Load()
	proxy.dropDockerMessages.Store(true)
	waitForCondition(t, 8*time.Second, func() bool { return proxy.droppedDockerMessages.Load() > droppedBefore }, "test proxy did not suppress Docker-only health frames")
	time.Sleep(200 * time.Millisecond)
	var lastDockerHealth int64
	if err := core.store.DB.QueryRow(`SELECT health_received_at FROM docker_node_state WHERE node_id=?`, config.NodeID).Scan(&lastDockerHealth); err != nil {
		t.Fatal(err)
	}
	staleAfter := time.Unix(0, lastDockerHealth).Add(coredocker.DefaultDockerHealthFresh)
	staleWait := time.Until(staleAfter)
	staleView := waitForS04DockerStaleEvent(t, dashboardEvents, config.NodeID, staleAfter, max(0, staleWait)+7*time.Second)
	if !staleView.AgentOnline || heartbeatSequenceForNode(t, core, config.NodeID) <= sequenceBeforeDrop {
		t.Fatalf("Docker freshness expired without continued Agent heartbeat: online=%t before=%d after=%d", staleView.AgentOnline, sequenceBeforeDrop, heartbeatSequenceForNode(t, core, config.NodeID))
	}
	state, _, err := core.currentMetricsState(context.Background(), config.NodeID)
	if err != nil || state.Status != "online" {
		t.Fatalf("Docker health suppression took the Agent offline: state=%+v err=%v", state, err)
	}
	var sessionList struct {
		Sessions []browserSessionView `json:"sessions"`
	}
	listRequest, err := http.NewRequest(http.MethodGet, coreHTTP.URL+"/api/v1/auth/sessions", nil)
	if err != nil {
		t.Fatal(err)
	}
	listRequest.AddCookie(&http.Cookie{Name: sessionCookieName, Value: session})
	listResponse, err := tlsHTTPClient(rootPEM).Do(listRequest)
	if err != nil {
		t.Fatalf("list real browser sessions before revoke: %v", err)
	}
	decodeErr := json.NewDecoder(io.LimitReader(listResponse.Body, 1<<20)).Decode(&sessionList)
	listResponse.Body.Close()
	if listResponse.StatusCode != http.StatusOK || decodeErr != nil {
		t.Fatalf("list browser sessions before revoke: status=%d decode=%v", listResponse.StatusCode, decodeErr)
	}
	var dashboardSessionID string
	for _, item := range sessionList.Sessions {
		if item.Current {
			dashboardSessionID = item.ID
			break
		}
	}
	if dashboardSessionID == "" {
		t.Fatal("authenticated session list did not identify the current Dashboard session")
	}
	revokeRequest, err := http.NewRequest(http.MethodDelete, coreHTTP.URL+"/api/v1/auth/sessions/"+dashboardSessionID, nil)
	if err != nil {
		t.Fatal(err)
	}
	revokeRequest.AddCookie(&http.Cookie{Name: sessionCookieName, Value: session})
	revokeRequest.AddCookie(&http.Cookie{Name: csrfCookieName, Value: csrf})
	revokeRequest.Header.Set(csrfHeaderName, csrf)
	revokeRequest.Header.Set("Origin", "https://panel.test")
	revokeResponse, err := tlsHTTPClient(rootPEM).Do(revokeRequest)
	if err != nil {
		t.Fatalf("revoke browser Session through Core API: %v", err)
	}
	revokeResponse.Body.Close()
	if revokeResponse.StatusCode != http.StatusNoContent {
		t.Fatalf("revoke browser Session through Core API: status=%d", revokeResponse.StatusCode)
	}
	waitForS04DashboardClosed(t, dashboardEvents, 2*time.Second)
	t.Logf("S04 multi-chunk Core-Agent snapshot passed: Engine=%s initialGeneration=%d finalGeneration=%d fixtureCount=200 snapshotFrames=%d", engineVersion, firstGeneration, generationForNode(t, core, config.NodeID), proxy.dockerChunks.Load())
}

func TestRealAgentDockerUnavailableKeepsHeartbeatAndHostMetrics(t *testing.T) {
	t.Setenv("DOCKER_HOST", "unix:///nonexistent/nodedance-s04-engine-unavailable.sock")
	workRoot := filepath.Join("..", "..", "..", ".artifacts", "work-s04")
	if err := os.MkdirAll(workRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	work, err := os.MkdirTemp(workRoot, "engine-unavailable-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(work)
	if err := os.Chmod(work, 0o700); err != nil {
		t.Fatal(err)
	}

	certificate, rootPEM, err := makeAgentTestCertificate()
	if err != nil {
		t.Fatal(err)
	}
	caPath := filepath.Join(work, "trusted-ca.pem")
	if err := os.WriteFile(caPath, rootPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	core, err := New("s04-engine-unavailable", Options{DataDir: filepath.Join(work, "core"), PublicOrigin: "https://panel.test"})
	if err != nil {
		t.Fatal(err)
	}
	var current atomic.Pointer[Server]
	current.Store(core)
	coreHTTP := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		active := current.Load()
		if active == nil {
			http.Error(w, "Core restarting", http.StatusServiceUnavailable)
			return
		}
		active.ServeHTTP(w, r)
	}))
	coreHTTP.TLS = &tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS12}
	coreHTTP.StartTLS()
	defer coreHTTP.Close()
	defer func() {
		current.Store(nil)
		if err := core.Close(); err != nil {
			t.Errorf("close real Core: %v", err)
		}
	}()

	enrollment, err := core.agents.CreateEnrollment(context.Background(), "S04 unavailable Docker socket", "127.0.0.1", sql.NullInt64{Int64: 1, Valid: true})
	if err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(work, "agent", "agent.json")
	if err := agent.Enroll(context.Background(), coreHTTP.URL, caPath, false, strings.NewReader(enrollment.Token), configPath); err != nil {
		t.Fatalf("enroll real Agent against Core TLS/WSS: %v", err)
	}
	config, err := agent.LoadConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	agentCtx, cancelAgent := context.WithCancel(context.Background())
	agentDone := make(chan error, 1)
	go func() { agentDone <- agent.Run(agentCtx, configPath, "s04-integration", &agentTestLog{}) }()
	defer func() {
		cancelAgent()
		select {
		case err := <-agentDone:
			if err != nil {
				t.Errorf("real Agent stopped with an error: %v", err)
			}
		case <-time.After(10 * time.Second):
			t.Error("real Agent did not stop after test cancellation")
		}
	}()

	waitForAgentStatus(t, core, config.NodeID, "online", 0)
	waitForCondition(t, 12*time.Second, func() bool {
		var raw sql.NullString
		if err := core.store.DB.QueryRow(`SELECT health_json FROM docker_node_state WHERE node_id=?`, config.NodeID).Scan(&raw); err != nil || !raw.Valid {
			return false
		}
		var health struct {
			Availability string `json:"availability"`
		}
		return json.Unmarshal([]byte(raw.String), &health) == nil && health.Availability == "unavailable"
	}, "real Agent did not report Docker unavailable for the isolated nonexistent Unix socket")

	initialHeartbeat := heartbeatSequenceForNode(t, core, config.NodeID)
	waitForCondition(t, 12*time.Second, func() bool {
		return heartbeatSequenceForNode(t, core, config.NodeID) > initialHeartbeat
	}, "Core heartbeat sequence did not advance while Docker Engine was unavailable")

	var onlineMetrics bool
	waitForCondition(t, 12*time.Second, func() bool {
		state, lease, err := core.currentMetricsState(context.Background(), config.NodeID)
		if err != nil || state.Status != "online" || lease == nil {
			return false
		}
		view, ok := core.metrics.SnapshotAt(config.NodeID, lease, core.now())
		if !ok || view.NodeStatus != "online" || view.Record.Metrics.Memory.Status != "known" || view.Record.Metrics.Memory.Value == nil {
			return false
		}
		onlineMetrics = true
		return true
	}, "real Agent host metrics did not remain available while Docker Engine was unavailable")
	if !onlineMetrics {
		t.Fatal("host metrics were not available")
	}
	waitForAgentStatus(t, core, config.NodeID, "online", 0)
	var availability string
	if err := core.store.DB.QueryRow(`SELECT json_extract(health_json, '$.availability') FROM docker_node_state WHERE node_id=?`, config.NodeID).Scan(&availability); err != nil || availability != "unavailable" {
		t.Fatalf("Docker unavailable state changed unexpectedly: availability=%q err=%v", availability, err)
	}
	t.Logf("real Core-Agent unavailable-Engine path kept Agent online, heartbeat advanced %d→%d, and host memory metrics remained available; only the isolated nonexistent Unix socket was used", initialHeartbeat, heartbeatSequenceForNode(t, core, config.NodeID))
}

func TestRealAgentDockerAPIIncompatibilitySecretRedaction(t *testing.T) {
	workRoot := filepath.Join("..", "..", "..", ".artifacts", "work-s04")
	workRoot, err := filepath.Abs(workRoot)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(workRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	work, err := os.MkdirTemp(workRoot, "api-incompatibility-redaction-*")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(work, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(work) })

	const (
		tokenSecret         = "ND_TEST_TOKEN_SECRET_8f33"
		passwordSecret      = "ND_TEST_PASSWORD_SECRET_a721"
		authorizationSecret = "ND_TEST_AUTH_SECRET_4b90"
		envSecret           = "ND_TEST_ENV_SECRET_75c1"
	)
	responseMessage := "client version 1.12 is too old. Minimum supported API version is 1.44. " +
		"Authorization: Bearer " + authorizationSecret + "; token=" + tokenSecret +
		"; password=" + passwordSecret + "; Env=[ND_PRIVATE=" + envSecret + "]"
	engineSocketDir, err := os.MkdirTemp("", "nd-s04-api-")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(engineSocketDir, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(engineSocketDir) })
	engineSocket := filepath.Join(engineSocketDir, "docker.sock")
	listener, err := net.Listen("unix", engineSocket)
	if err != nil {
		t.Fatalf("create private Docker API fixture socket: %v", err)
	}
	var listRequests atomic.Int64
	var pingRequests atomic.Int64
	var eventRequests atomic.Int64
	var otherRequests atomic.Int64
	var partialScanSuccessfulInspects atomic.Int64
	var partialScanInspectFailures atomic.Int64
	engineHTTP := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/_ping"):
			pingRequests.Add(1)
			w.Header().Set("API-Version", "1.44")
			w.Header().Set("Docker-Experimental", "false")
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, "OK")
		case strings.Contains(r.URL.Path, "/events"):
			eventRequests.Add(1)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			if flusher, ok := w.(http.Flusher); ok {
				flusher.Flush()
			}
			<-r.Context().Done()
		case strings.Contains(r.URL.Path, "/containers/json"):
			call := listRequests.Add(1)
			partialScanSuccessfulInspects.Store(0)
			w.Header().Set("Content-Type", "application/json")
			entries := []map[string]any{{
				"Id": strings.Repeat("a", 64), "Names": []string{"/s04-redaction-fixture"},
				"Image": "busybox:fixture", "State": "running", "Status": "Up",
			}}
			if call > 1 {
				entries = append(entries, map[string]any{
					"Id": strings.Repeat("b", 64), "Names": []string{"/s04-partial-inspect-failure"},
					"Image": "busybox:fixture", "State": "running", "Status": "Up",
				})
			}
			_ = json.NewEncoder(w).Encode(entries)
		case strings.Contains(r.URL.Path, "/containers/") && strings.HasSuffix(r.URL.Path, "/json"):
			if strings.Contains(r.URL.Path, strings.Repeat("b", 64)) && listRequests.Load() > 1 {
				deadline := time.Now().Add(3 * time.Second)
				for partialScanSuccessfulInspects.Load() == 0 && time.Now().Before(deadline) {
					select {
					case <-r.Context().Done():
						return
					case <-time.After(5 * time.Millisecond):
					}
				}
				partialScanInspectFailures.Add(1)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusBadRequest)
				_ = json.NewEncoder(w).Encode(map[string]string{"message": responseMessage})
				return
			}
			if strings.Contains(r.URL.Path, strings.Repeat("a", 64)) && listRequests.Load() > 1 {
				partialScanSuccessfulInspects.Add(1)
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"Id": strings.Repeat("a", 64), "Name": "/s04-redaction-fixture", "Image": "sha256:fixture",
				"Config":          map[string]any{"Image": "busybox:fixture", "Labels": map[string]string{}},
				"State":           map[string]any{"Status": "running", "Running": true},
				"HostConfig":      map[string]any{"NetworkMode": "bridge", "PortBindings": map[string]any{}},
				"NetworkSettings": map[string]any{"Ports": map[string]any{}, "Networks": map[string]any{}},
			})
			return
		default:
			otherRequests.Add(1)
			http.NotFound(w, r)
		}
	})}
	engineDone := make(chan error, 1)
	go func() { engineDone <- engineHTTP.Serve(listener) }()
	t.Cleanup(func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = engineHTTP.Shutdown(shutdownCtx)
		select {
		case serveErr := <-engineDone:
			if serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
				t.Errorf("stop private Docker API fixture: %v", serveErr)
			}
		case <-time.After(4 * time.Second):
			t.Error("private Docker API fixture did not stop")
		}
	})

	t.Setenv("DOCKER_HOST", "unix://"+engineSocket)

	certificate, rootPEM, err := makeAgentTestCertificate()
	if err != nil {
		t.Fatal(err)
	}
	caPath := filepath.Join(work, "trusted-ca.pem")
	if err := os.WriteFile(caPath, rootPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	core, err := New("s04-docker-api-incompatible", Options{DataDir: filepath.Join(work, "core"), PublicOrigin: "https://panel.test"})
	if err != nil {
		t.Fatal(err)
	}
	var current atomic.Pointer[Server]
	current.Store(core)
	coreHTTP := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		active := current.Load()
		if active == nil {
			http.Error(w, "Core restarting", http.StatusServiceUnavailable)
			return
		}
		active.ServeHTTP(w, r)
	}))
	coreHTTP.TLS = &tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS12}
	coreHTTP.StartTLS()
	t.Cleanup(func() {
		current.Store(nil)
		coreHTTP.Close()
		if closeErr := core.Close(); closeErr != nil {
			t.Errorf("close Core after Docker API incompatibility test: %v", closeErr)
		}
	})

	session, _, err := installIntegrationAdmin(core)
	if err != nil {
		t.Fatal(err)
	}
	enrollment, err := core.agents.CreateEnrollment(context.Background(), "S04 incompatible Docker API", "127.0.0.1", sql.NullInt64{Int64: 1, Valid: true})
	if err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(work, "agent", "agent.json")
	if err := agent.Enroll(context.Background(), coreHTTP.URL, caPath, false, strings.NewReader(enrollment.Token), configPath); err != nil {
		t.Fatalf("enroll real Agent against Core TLS/WSS: %v", err)
	}
	config, err := agent.LoadConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	agentLog := &agentTestLog{}
	agentCtx, cancelAgent := context.WithCancel(context.Background())
	agentDone := make(chan error, 1)
	go func() { agentDone <- agent.Run(agentCtx, configPath, "s04-integration", agentLog) }()
	t.Cleanup(func() {
		cancelAgent()
		select {
		case runErr := <-agentDone:
			if runErr != nil {
				t.Errorf("real Agent stopped with an error")
			}
		case <-time.After(10 * time.Second):
			t.Error("real Agent did not stop after test cancellation")
		}
	})

	waitForAgentStatus(t, core, config.NodeID, "online", 0)
	deadline := time.Now().Add(15 * time.Second)
	reported := false
	for time.Now().Before(deadline) {
		var raw sql.NullString
		if listRequests.Load() > 1 && core.store.DB.QueryRow(`SELECT health_json FROM docker_node_state WHERE node_id=?`, config.NodeID).Scan(&raw) == nil && raw.Valid {
			var health map[string]any
			if json.Unmarshal([]byte(raw.String), &health) == nil && health["availability"] == "available" &&
				health["errorKind"] == "api_incompatible" && health["reason"] == "Docker Engine API version is incompatible" && health["snapshotFresh"] == false {
				reported = true
				break
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	if !reported {
		var healthRaw sql.NullString
		_ = core.store.DB.QueryRow(`SELECT health_json FROM docker_node_state WHERE node_id=?`, config.NodeID).Scan(&healthRaw)
		var observed map[string]any
		_ = json.Unmarshal([]byte(healthRaw.String), &observed)
		observedReason, _ := observed["reason"].(string)
		reasonIsSafe := observedReason == "" || observedReason == "Docker Engine API version is incompatible" ||
			observedReason == "Docker Engine request failed" || observedReason == "Docker Engine unavailable"
		reasonForLog := observedReason
		if !reasonIsSafe {
			reasonForLog = "<redacted>"
		}
		t.Fatalf("real Docker SDK API incompatibility was not safely reported: ping=%d events=%d list=%d other=%d availability=%v errorKind=%v reason=%q snapshotFresh=%v",
			pingRequests.Load(), eventRequests.Load(), listRequests.Load(), otherRequests.Load(), observed["availability"], observed["errorKind"], reasonForLog, observed["snapshotFresh"])
	}
	if partialScanSuccessfulInspects.Load() < 1 || partialScanInspectFailures.Load() < 1 {
		t.Fatalf("S04-07 did not fail a multi-container scan after a successful Inspect: successful_inspects=%d injected_failures=%d list_requests=%d",
			partialScanSuccessfulInspects.Load(), partialScanInspectFailures.Load(), listRequests.Load())
	}

	var savedHealth string
	if err := core.store.DB.QueryRow(`SELECT health_json FROM docker_node_state WHERE node_id=?`, config.NodeID).Scan(&savedHealth); err != nil {
		t.Fatalf("read persisted Docker health: %v", err)
	}
	var savedContainer string
	if err := core.store.DB.QueryRow(`SELECT record_json FROM docker_containers WHERE node_id=? AND container_id=?`, config.NodeID, strings.Repeat("a", 64)).Scan(&savedContainer); err != nil {
		t.Fatalf("read persisted stale Docker container: %v", err)
	}
	request, err := http.NewRequest(http.MethodGet, coreHTTP.URL+"/api/v1/nodes/"+config.NodeID+"/containers", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: session})
	response, err := tlsHTTPClient(rootPEM).Do(request)
	if err != nil {
		t.Fatal(err)
	}
	apiBody, readErr := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	response.Body.Close()
	if readErr != nil || response.StatusCode != http.StatusOK {
		t.Fatalf("read private Docker inventory API: status=%d readErr=%v", response.StatusCode, readErr)
	}
	for field, secret := range map[string]string{"token": tokenSecret, "password": passwordSecret, "authorization": authorizationSecret, "environment": envSecret, "header-name": "Authorization"} {
		if strings.Contains(savedHealth, secret) || strings.Contains(savedContainer, secret) || strings.Contains(string(apiBody), secret) || strings.Contains(agentLog.String(), secret) {
			t.Fatalf("injected Docker error leaked through persisted/API/log field: %s", field)
		}
	}
	if !strings.Contains(string(apiBody), "Docker Engine API version is incompatible") || !strings.Contains(savedHealth, "Docker Engine API version is incompatible") || !strings.Contains(savedContainer, "Docker Engine API version is incompatible") {
		t.Fatal("safe API incompatibility reason was not retained in health/container SQLite rows and the authenticated inventory API")
	}
	if !strings.Contains(string(apiBody), `"dockerAvailability":"available"`) || !strings.Contains(string(apiBody), `"dataStale":true`) || !strings.Contains(string(apiBody), `"agentOnline":true`) {
		t.Fatal("Docker API incompatibility was not distinguished from Agent connectivity and the stale inventory in the API")
	}
	if agentLog.String() != "" {
		t.Fatalf("unexpected Agent logs during steady WSS Docker API failure")
	}
	var apiMessage dashboardDockerMessage
	if err := json.Unmarshal(apiBody, &apiMessage); err != nil || apiMessage.Inventory == nil || len(apiMessage.Inventory.Containers) != 1 ||
		apiMessage.Inventory.Containers[0].Container.ID != strings.Repeat("a", 64) || !apiMessage.Inventory.Containers[0].Container.Stale {
		t.Fatalf("S04-07 failed partial scan did not retain exactly the last committed stale inventory: err=%v count=%d", err, inventoryCount(apiMessage))
	}
	var uncommittedIDCount int
	if err := core.store.DB.QueryRow(`SELECT count(*) FROM docker_containers WHERE node_id=? AND container_id=?`,
		config.NodeID, strings.Repeat("b", 64)).Scan(&uncommittedIDCount); err != nil || uncommittedIDCount != 0 {
		t.Fatalf("S04-07 incomplete scan committed an inspected-prefix/new container row: count=%d err=%v", uncommittedIDCount, err)
	}
	heartbeatBefore := heartbeatSequenceForNode(t, core, config.NodeID)
	metricsBefore := metricSequenceForNode(t, core, config.NodeID)
	waitForCondition(t, 15*time.Second, func() bool {
		return heartbeatSequenceForNode(t, core, config.NodeID) >= heartbeatBefore+2 &&
			metricSequenceForNode(t, core, config.NodeID) >= metricsBefore+2
	}, "two new Agent heartbeats and host-metric samples did not arrive while Engine API was incompatible")
	state, metrics, err := core.metricsViewForNode(context.Background(), config.NodeID)
	if err != nil || state.Status != "online" || metrics == nil || metrics.Sequence < metricsBefore+2 ||
		metrics.Metrics.Memory.Status != protocol.MetricKnown || metrics.Metrics.Memory.Value == nil ||
		metrics.Metrics.Uptime.Status != protocol.MetricKnown || metrics.Metrics.Uptime.Value == nil {
		t.Fatal("two fresh samples with known memory and uptime metrics did not remain live while Docker API was incompatible")
	}
	t.Logf("S04-07 partial full scan failed after %d successful Inspect(s); prior committed inventory remained exactly one stale row and the uncommitted second ID was absent; S04-08 Moby SDK API incompatibility crossed Agent→authenticated WSS→Core SQLite/API with Engine ping=available, snapshot stale, error_kind=api_incompatible, heartbeat=%d→%d, metrics=%d→%d with memory/uptime known, listRequests=%d inspectFailures=%d; injected response secrets absent from health/container SQLite/API/Agent logs",
		partialScanSuccessfulInspects.Load(), heartbeatBefore, heartbeatSequenceForNode(t, core, config.NodeID), metricsBefore, metrics.Sequence,
		listRequests.Load(), partialScanInspectFailures.Load())
}

func TestRealDockerAgentCoreExternalStateChangeP95(t *testing.T) {
	root, endpoint, engineVersion := requireOwnedS04DIND(t)
	t.Setenv("DOCKER_HOST", endpoint)
	workRoot := filepath.Join(root, ".artifacts", "work-s04")
	if err := os.MkdirAll(workRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	work, err := os.MkdirTemp(workRoot, "external-state-p95-*")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(work, 0o700); err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(work)
	runID := fmt.Sprintf("nd-s04-p95-%d", time.Now().UnixNano())
	containerID, err := runS04DockerCLI(endpoint, "container", "create", "--name", runID,
		"--label", "io.nodedance.test=true", "--label", "io.nodedance.suite="+runID,
		s04BusyboxImage, "sh", "-c", "sleep 300")
	if err != nil {
		t.Fatalf("create isolated S04 latency fixture: %v", err)
	}
	containerID = strings.TrimSpace(containerID)
	t.Logf("real Engine state-change P95 run=%s Engine=%s container=%s sample_target=100", runID, engineVersion, containerID)
	defer func() {
		if _, err := runS04DockerCLI(endpoint, "container", "rm", "--force", containerID); err != nil {
			t.Errorf("remove exact isolated latency fixture %s: %v", containerID[:12], err)
		}
	}()

	certificate, rootPEM, err := makeAgentTestCertificate()
	if err != nil {
		t.Fatal(err)
	}
	caPath := filepath.Join(work, "trusted-ca.pem")
	if err := os.WriteFile(caPath, rootPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	core, err := New("s04-docker-p95-integration", Options{DataDir: filepath.Join(work, "core"), PublicOrigin: "https://panel.test"})
	if err != nil {
		t.Fatal(err)
	}
	var current atomic.Pointer[Server]
	current.Store(core)
	coreHTTP := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		active := current.Load()
		if active == nil {
			http.Error(w, "Core restarting", http.StatusServiceUnavailable)
			return
		}
		active.ServeHTTP(w, r)
	}))
	coreHTTP.TLS = &tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS12}
	coreHTTP.StartTLS()
	proxy := newAgentTestProxy(t, &current, coreHTTP.URL, rootPEM)
	proxyServer := httptest.NewUnstartedServer(proxy)
	proxyServer.TLS = &tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS12}
	proxyServer.StartTLS()
	proxy.proxyURL = proxyServer.URL
	defer func() {
		current.Store(nil)
		proxyServer.Close()
		coreHTTP.Close()
		if err := core.Close(); err != nil {
			t.Errorf("close Core after S04 latency test: %v", err)
		}
	}()
	session, _, err := installIntegrationAdmin(core)
	if err != nil {
		t.Fatal(err)
	}
	enrollment, err := core.agents.CreateEnrollment(context.Background(), "S04 external state change P95 "+engineVersion,
		"127.0.0.1", sql.NullInt64{Int64: 1, Valid: true})
	if err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(work, "agent", "agent.json")
	if err := agent.Enroll(context.Background(), proxyServer.URL, caPath, false, strings.NewReader(enrollment.Token), configPath); err != nil {
		t.Fatalf("enroll real Agent against Core TLS/WSS: %v", err)
	}
	config, err := agent.LoadConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	agentCtx, cancelAgent := context.WithCancel(context.Background())
	agentDone := make(chan error, 1)
	go func() { agentDone <- agent.Run(agentCtx, configPath, "s04-p95", &agentTestLog{}) }()
	defer func() {
		cancelAgent()
		select {
		case err := <-agentDone:
			if err != nil {
				t.Errorf("real Agent stopped with an error after P95 run: %v", err)
			}
		case <-time.After(10 * time.Second):
			t.Error("real Agent did not stop after P95 test cancellation")
		}
	}()
	waitForAgentStatus(t, core, config.NodeID, "online", 0)
	waitForS04DockerView(t, core, config.NodeID, 30*time.Second, func(view coredocker.View) bool {
		return view.AgentOnline && view.DockerAvailability == "available" && view.DockerSnapshotFresh && !view.DataStale && dockerViewContains(view, containerID)
	}, "initial authoritative state-change fixture snapshot")
	if _, err := waitForS04ContainerStateAPI(coreHTTP.URL, rootPEM, session, config.NodeID, containerID, false, 10*time.Second); err != nil {
		t.Fatal(err)
	}

	latencies := make([]time.Duration, 0, 100)
	for index := 0; index < 100; index++ {
		wantRunning := index%2 == 0
		action := "stop"
		args := []string{"container", "stop", "--time", "0", containerID}
		if wantRunning {
			action = "start"
			args = []string{"container", "start", containerID}
		}
		requestedAt := time.Now().UTC()
		if _, err := runS04DockerCLI(endpoint, args...); err != nil {
			t.Fatalf("real Engine state change %03d (%s) failed: %v", index+1, action, err)
		}
		observedAt, err := waitForS04ContainerStateAPI(coreHTTP.URL, rootPEM, session, config.NodeID, containerID, wantRunning, 10*time.Second)
		if err != nil {
			t.Fatalf("NodeDance did not observe real Engine state change %03d (%s): %v", index+1, action, err)
		}
		latency := observedAt.Sub(requestedAt)
		latencies = append(latencies, latency)
		t.Logf("S04_STATE_CHANGE index=%03d action=%s engine_request_at=%s nodedance_observed_at=%s latency_ms=%d", index+1, action, requestedAt.Format(time.RFC3339Nano), observedAt.Format(time.RFC3339Nano), latency.Milliseconds())
	}
	if len(latencies) != 100 {
		t.Fatalf("real Engine latency sample count=%d, want exactly 100", len(latencies))
	}
	sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
	p95 := latencies[(len(latencies)*95+99)/100-1]
	var total time.Duration
	for _, latency := range latencies {
		total += latency
	}
	mean := total / time.Duration(len(latencies))
	t.Logf("S04_SUP_01 real Core-Agent-API latency summary: Engine=%s count=%d missed=0 min_ms=%d mean_ms=%d p50_ms=%d p95_ms=%d max_ms=%d", engineVersion, len(latencies), latencies[0].Milliseconds(), mean.Milliseconds(), latencies[len(latencies)/2].Milliseconds(), p95.Milliseconds(), latencies[len(latencies)-1].Milliseconds())
	if p95 > 5*time.Second {
		t.Fatalf("real Core-Agent Docker state synchronization P95=%s exceeds the 5s acceptance budget", p95)
	}
}

func TestRealAgentDockerDINDStopAndSocketPermissionRecovery(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("DAC permission fault requires the real Agent process to run without root/CAP_DAC_OVERRIDE")
	}
	root, endpoint, engineVersion := requireOwnedS04DIND(t)
	t.Setenv("DOCKER_HOST", endpoint)
	baselineOutput, err := runS04DockerCLI(endpoint, "container", "ls", "--all", "--quiet", "--no-trunc")
	if err != nil {
		t.Fatalf("list baseline containers on the owned Engine: %v", err)
	}
	baselineIDs := strings.Fields(baselineOutput)
	engine := strings.SplitN(engineVersion, ".", 2)[0]
	owner := readOwnedS04DINDOwner(t, root, endpoint, engineVersion)
	dindScriptRoot := root
	if configuredRoot := os.Getenv("NODEDANCE_S04_DIND_ROOT"); configuredRoot != "" {
		var rootErr error
		dindScriptRoot, rootErr = filepath.Abs(configuredRoot)
		if rootErr != nil {
			t.Fatal(rootErr)
		}
	}
	containerID := verifyOwnedS04DINDHostContainer(t, owner)
	socketPath := strings.TrimPrefix(endpoint, "unix://")
	originalSocketInfo, err := os.Stat(socketPath)
	if err != nil || originalSocketInfo.Mode()&os.ModeSocket == 0 {
		t.Fatalf("inspect exact owned DIND socket before fault injection: mode=%v err=%v", originalSocketInfo, err)
	}
	originalSocketMode := originalSocketInfo.Mode().Perm()
	if originalSocketMode&0o444 == 0 {
		t.Fatalf("owned DIND socket is already inaccessible before test: mode=%#o", originalSocketMode)
	}
	workRoot := filepath.Join(root, ".artifacts", "work-s04")
	if err := os.MkdirAll(workRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	work, err := os.MkdirTemp(workRoot, "engine-fault-recovery-*")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(work, 0o700); err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(work)

	runID := fmt.Sprintf("nd-s04-fault-%d", time.Now().UnixNano())
	name := runID + "-kept"
	fixtureID, err := runS04DockerCLI(endpoint, "container", "create", "--name", name,
		"--label", "io.nodedance.test=true", "--label", "io.nodedance.suite="+runID,
		s04BusyboxImage, "sh", "-c", "sleep 300")
	if err != nil {
		t.Fatalf("create stopped-container fault fixture on owned DIND: %v", err)
	}
	fixtureID = strings.TrimSpace(fixtureID)
	expectedIDs := append(append([]string(nil), baselineIDs...), fixtureID)
	t.Logf("owned DIND fault run=%s Engine=%s ownerContainer=%s fixture=%s processUID=%d", runID, engineVersion, containerID[:12], fixtureID[:12], os.Geteuid())
	t.Logf("owned DIND fault run=%s baselineContainerCount=%d expectedRecoveryContainerCount=%d", runID, len(baselineIDs), len(expectedIDs))

	certificate, rootPEM, err := makeAgentTestCertificate()
	if err != nil {
		t.Fatal(err)
	}
	caPath := filepath.Join(work, "trusted-ca.pem")
	if err := os.WriteFile(caPath, rootPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	core, err := New("s04-docker-fault-integration", Options{DataDir: filepath.Join(work, "core"), PublicOrigin: "https://panel.test"})
	if err != nil {
		t.Fatal(err)
	}
	var current atomic.Pointer[Server]
	current.Store(core)
	coreHTTP := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		active := current.Load()
		if active == nil {
			http.Error(w, "Core restarting", http.StatusServiceUnavailable)
			return
		}
		active.ServeHTTP(w, r)
	}))
	coreHTTP.TLS = &tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS12}
	coreHTTP.StartTLS()
	proxy := newAgentTestProxy(t, &current, coreHTTP.URL, rootPEM)
	proxyServer := httptest.NewUnstartedServer(proxy)
	proxyServer.TLS = &tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS12}
	proxyServer.StartTLS()
	proxy.proxyURL = proxyServer.URL
	var stopAgent func() error
	var agentCancel context.CancelFunc
	var agentDone chan error
	startAgent := func(configPath string) {
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		agentCancel, agentDone = cancel, done
		go func() { done <- agent.Run(ctx, configPath, "s04-dind-fault", &agentTestLog{}) }()
		stopAgent = func() error {
			if agentCancel == nil {
				return nil
			}
			cancelCurrent, doneCurrent := agentCancel, agentDone
			agentCancel, agentDone = nil, nil
			cancelCurrent()
			select {
			case err := <-doneCurrent:
				if err != nil {
					return err
				}
				return nil
			case <-time.After(10 * time.Second):
				return errors.New("real Agent did not stop after test cancellation")
			}
		}
	}
	dindStopped := false
	socketModeChanged := false
	defer func() {
		if stopAgent != nil {
			if err := stopAgent(); err != nil {
				t.Errorf("stop owned test Agent during cleanup: %v", err)
			}
		}
		if dindStopped {
			if err := runS04DINDScript(dindScriptRoot, "start", engine); err != nil {
				t.Errorf("restart exact owned DIND Engine during cleanup: %v", err)
			} else {
				dindStopped = false
			}
		}
		if socketModeChanged {
			if err := setOwnedS04DINDSocketMode(owner, originalSocketMode); err != nil {
				t.Errorf("restore exact owned DIND socket mode %#o: %v", originalSocketMode, err)
			}
		}
		if _, err := runS04DockerCLI(endpoint, "container", "rm", "--force", fixtureID); err != nil {
			t.Errorf("remove exact owned DIND fault fixture %s: %v", fixtureID[:12], err)
		}
		current.Store(nil)
		proxyServer.Close()
		coreHTTP.Close()
		if err := core.Close(); err != nil {
			t.Errorf("close Core after DIND fault test: %v", err)
		}
	}()

	_, _, err = installIntegrationAdmin(core)
	if err != nil {
		t.Fatal(err)
	}
	enrollment, err := core.agents.CreateEnrollment(context.Background(), "S04 DIND fault recovery "+engineVersion,
		"127.0.0.1", sql.NullInt64{Int64: 1, Valid: true})
	if err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(work, "agent", "agent.json")
	if err := agent.Enroll(context.Background(), proxyServer.URL, caPath, false, strings.NewReader(enrollment.Token), configPath); err != nil {
		t.Fatalf("enroll real Agent against Core TLS/WSS: %v", err)
	}
	config, err := agent.LoadConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	startAgent(configPath)
	waitForAgentStatus(t, core, config.NodeID, "online", 0)
	waitForS04DockerView(t, core, config.NodeID, 30*time.Second, func(view coredocker.View) bool {
		return view.AgentOnline && view.DockerAvailability == "available" && view.DockerSnapshotFresh && !view.DataStale && sameS04StringSet(s04DockerViewContainerIDs(view), expectedIDs)
	}, "initial owned DIND snapshot containing the stopped fixture")

	// Stop only the exact marker-owned nested Engine. The host Docker daemon and
	// its unrelated business containers remain running and untouched.
	heartbeatBeforeStop := heartbeatSequenceForNode(t, core, config.NodeID)
	metricsBeforeStop := metricSequenceForNode(t, core, config.NodeID)
	if err := runS04DINDScript(dindScriptRoot, "stop", engine); err != nil {
		t.Fatalf("stop exact owner-validated DIND Engine container %s: %v", containerID[:12], err)
	}
	dindStopped = true
	stoppedView := waitForS04DockerView(t, core, config.NodeID, 20*time.Second, func(view coredocker.View) bool {
		return view.AgentOnline && view.DockerAvailability == "unavailable" && view.DataStale && dockerViewContains(view, fixtureID)
	}, "Docker Engine stopped while Agent remains online")
	if !dockerViewRecordStale(stoppedView, fixtureID) {
		t.Fatalf("Docker stop did not mark the retained old asset stale: %+v", s04RecordForID(stoppedView, fixtureID))
	}
	waitForS04TelemetryAdvance(t, core, config.NodeID, heartbeatBeforeStop, metricsBeforeStop, 15*time.Second, "Docker stop")
	t.Logf("DIND stop observed Docker unavailable/stale while Agent online; heartbeat %d→%d and host metrics %d→%d", heartbeatBeforeStop, heartbeatSequenceForNode(t, core, config.NodeID), metricsBeforeStop, metricSequenceForNode(t, core, config.NodeID))
	if err := runS04DINDScript(dindScriptRoot, "start", engine); err != nil {
		t.Fatalf("restart exact owner-validated DIND Engine container %s: %v", containerID[:12], err)
	}
	dindStopped = false
	restoredView := waitForS04DockerView(t, core, config.NodeID, 40*time.Second, func(view coredocker.View) bool {
		return view.AgentOnline && view.DockerAvailability == "available" && view.DockerSnapshotFresh && !view.DataStale && dockerViewContains(view, fixtureID)
	}, "complete inventory rescan after DIND restart")
	if !sameS04StringSet(s04DockerViewContainerIDs(restoredView), expectedIDs) {
		t.Fatalf("DIND recovery inventory differs from the exact pre-outage Engine IDs: expected=%v actual=%v", expectedIDs, s04DockerViewContainerIDs(restoredView))
	}

	if err := stopAgent(); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(socketPath)
	if err != nil || info.Mode()&os.ModeSocket == 0 {
		t.Fatalf("reinspect exact owned socket before DAC fault: mode=%v err=%v", info, err)
	}
	originalSocketMode = info.Mode().Perm()
	socketModeChanged = true
	if err := setOwnedS04DINDSocketMode(owner, 0); err != nil {
		t.Fatalf("restrict only the owned DIND socket for DAC fault: %v", err)
	}
	probeErr := dialUnixSocketForDACProbe(socketPath)
	if !errors.Is(probeErr, syscall.EACCES) && !errors.Is(probeErr, syscall.EPERM) {
		t.Fatalf("same-uid Unix socket connect did not prove the test Agent identity is denied: uid=%d gid=%d socket=%q mode=%#o err=%v",
			os.Geteuid(), os.Getegid(), socketPath, socketMode(socketPath), probeErr)
	}
	groups, groupsErr := os.Getgroups()
	t.Logf("socket DAC proof: Agent and probe run in-process as uid=%d gid=%d groups=%v groups_error=%v; exact DIND socket mode=%#o denied connect with %v",
		os.Geteuid(), os.Getegid(), groups, groupsErr, socketMode(socketPath), probeErr)
	heartbeatBeforeDAC := heartbeatSequenceForNode(t, core, config.NodeID)
	metricsBeforeDAC := metricSequenceForNode(t, core, config.NodeID)
	previousGeneration := generationForNode(t, core, config.NodeID)
	startAgent(configPath)
	waitForAgentStatusWithin(t, core, config.NodeID, "online", previousGeneration, 20*time.Second)
	deniedView := waitForS04DockerView(t, core, config.NodeID, 20*time.Second, func(view coredocker.View) bool {
		return view.AgentOnline && view.DockerAvailability == "unavailable" && view.DataStale && dockerViewContains(view, fixtureID)
	}, "real Agent with DAC-denied Docker socket")
	if !dockerViewRecordStale(deniedView, fixtureID) {
		t.Fatalf("Docker socket permission failure did not mark the retained old asset stale: %+v", s04RecordForID(deniedView, fixtureID))
	}
	waitForS04TelemetryAdvance(t, core, config.NodeID, heartbeatBeforeDAC, metricsBeforeDAC, 15*time.Second, "Docker socket permission denial")
	t.Logf("socket permission denial kept Agent online; heartbeat %d→%d and host metrics %d→%d", heartbeatBeforeDAC, heartbeatSequenceForNode(t, core, config.NodeID), metricsBeforeDAC, metricSequenceForNode(t, core, config.NodeID))
	if err := setOwnedS04DINDSocketMode(owner, originalSocketMode); err != nil {
		t.Fatalf("restore exact owned DIND socket mode after DAC fault: %v", err)
	}
	socketModeChanged = false
	finalView := waitForS04DockerView(t, core, config.NodeID, 40*time.Second, func(view coredocker.View) bool {
		return view.AgentOnline && view.DockerAvailability == "available" && view.DockerSnapshotFresh && !view.DataStale && dockerViewContains(view, fixtureID)
	}, "full Docker resync after restoring socket permissions")
	if len(finalView.Containers) != 1 {
		t.Fatalf("socket permission recovery changed the stopped inventory: count=%d", len(finalView.Containers))
	}
	t.Logf("socket DAC recovery preserved the exact stopped asset; final availability=%s stale=%t generation=%d", finalView.DockerAvailability, finalView.DataStale, finalView.ActiveGeneration)
}

func dialUnixSocketForDACProbe(path string) error {
	conn, err := net.DialTimeout("unix", path, 3*time.Second)
	if err != nil {
		return err
	}
	return conn.Close()
}

func socketMode(path string) os.FileMode {
	info, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return info.Mode().Perm()
}

func TestS04UnixSocketDACProbeUsesConnectErrno(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("Unix socket DAC probe requires a non-root test identity")
	}
	path := filepath.Join(t.TempDir(), "docker.sock")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal("create Unix socket DAC fixture:", err)
	}
	defer listener.Close()
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal("set accessible socket mode:", err)
	}
	if err := dialUnixSocketForDACProbe(path); err != nil {
		t.Fatalf("same-identity probe unexpectedly failed before denial: %v", err)
	}
	if err := os.Chmod(path, 0); err != nil {
		t.Fatal("remove all socket access bits:", err)
	}
	err = dialUnixSocketForDACProbe(path)
	if !errors.Is(err, syscall.EACCES) && !errors.Is(err, syscall.EPERM) {
		t.Fatalf("same-identity Unix socket probe did not return a DAC errno: mode=%#o err=%v", socketMode(path), err)
	}
}

func s04DockerViewContainerIDs(view coredocker.View) []string {
	ids := make([]string, 0, len(view.Containers))
	for _, record := range view.Containers {
		ids = append(ids, record.Container.ID)
	}
	return ids
}

func requireOwnedS04DIND(t *testing.T) (root, endpoint, engineVersion string) {
	t.Helper()
	endpoint = os.Getenv("NODEDANCE_S04_DIND_HOST")
	if !strings.HasPrefix(endpoint, "unix://") {
		t.Skip("NODEDANCE_S04_DIND_HOST is required for real Core-Agent Docker acceptance")
	}
	root, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	dindRoot := root
	if configuredRoot := os.Getenv("NODEDANCE_S04_DIND_ROOT"); configuredRoot != "" {
		dindRoot, err = filepath.Abs(configuredRoot)
		if err != nil {
			t.Fatal(err)
		}
		if expectedSibling := filepath.Join(filepath.Dir(root), "NodeDance-s04"); dindRoot != expectedSibling {
			t.Fatalf("refusing DIND root outside the designated sibling S04 fixture worktree: %s", dindRoot)
		}
	}
	socket := strings.TrimPrefix(endpoint, "unix://")
	if socket != filepath.Join(dindRoot, ".artifacts", "dind", "v28", "socket", "docker.sock") && socket != filepath.Join(dindRoot, ".artifacts", "dind", "v29", "socket", "docker.sock") {
		t.Fatalf("refusing Docker endpoint outside the owned S04 DIND sockets: %s", socket)
	}
	markerPath := filepath.Join(filepath.Dir(filepath.Dir(socket)), "owner.json")
	markerBytes, err := os.ReadFile(markerPath)
	if err != nil {
		t.Skipf("owned S04 DIND marker is unavailable: %v", err)
	}
	var marker struct {
		Suite         string `json:"suite"`
		Socket        string `json:"socket"`
		ServerVersion string `json:"server_version"`
	}
	if err := json.Unmarshal(markerBytes, &marker); err != nil || marker.Suite != "nodedance-s00-dind" || marker.Socket != socket {
		t.Fatalf("S04 DIND owner marker does not match the configured test socket")
	}
	version, err := runS04DockerCLI(endpoint, "version", "--format", "{{.Server.Version}}")
	if err != nil {
		t.Skipf("owned S04 DIND Engine is unavailable: %v", err)
	}
	version = strings.TrimSpace(version)
	if version != marker.ServerVersion || !(strings.HasPrefix(version, "28.") || strings.HasPrefix(version, "29.")) {
		t.Fatalf("S04 Docker Engine marker/version mismatch: marker=%q actual=%q", marker.ServerVersion, version)
	}
	return root, endpoint, version
}

func runS04DockerCLI(endpoint string, args ...string) (string, error) {
	commandArgs := append([]string{"--host", endpoint}, args...)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "docker", commandArgs...)
	output, err := command.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("docker --host <owned-s04-socket> %s: %w", strings.Join(args, " "), err)
	}
	return string(output), nil
}

func s04EngineContainerIDs(endpoint string, filters ...string) ([]string, error) {
	args := []string{"container", "ls", "--all", "--quiet", "--no-trunc"}
	args = append(args, filters...)
	output, err := runS04DockerCLI(endpoint, args...)
	if err != nil {
		return nil, err
	}
	ids := strings.Fields(output)
	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		if _, duplicate := seen[id]; duplicate {
			return nil, fmt.Errorf("Engine returned duplicate container ID %q", id)
		}
		seen[id] = struct{}{}
	}
	sort.Strings(ids)
	return ids, nil
}

func runS04DINDScript(root, action, engine string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, filepath.Join(root, "scripts", "test", "dind.sh"), action, engine)
	output, err := command.CombinedOutput()
	if err != nil {
		return fmt.Errorf("owned DIND %s Engine %s: %w: %s", action, engine, err, strings.TrimSpace(string(output)))
	}
	return nil
}

type s04DINDOwner struct {
	Suite         string `json:"suite"`
	ContainerName string `json:"container_name"`
	Image         string `json:"image"`
	Socket        string `json:"socket"`
	HostDaemon    string `json:"host_daemon"`
	ServerVersion string `json:"server_version"`
}

func readOwnedS04DINDOwner(t *testing.T, root, endpoint, engineVersion string) s04DINDOwner {
	t.Helper()
	socket := strings.TrimPrefix(endpoint, "unix://")
	markerPath := filepath.Join(filepath.Dir(filepath.Dir(socket)), "owner.json")
	markerBytes, err := os.ReadFile(markerPath)
	if err != nil {
		t.Fatalf("read exact owned DIND owner marker: %v", err)
	}
	var marker s04DINDOwner
	if err := json.Unmarshal(markerBytes, &marker); err != nil {
		t.Fatalf("decode exact owned DIND owner marker: %v", err)
	}
	if marker.Suite != "nodedance-s00-dind" || marker.Socket != socket || marker.ContainerName == "" || marker.Image == "" || marker.HostDaemon != "unix:///var/run/docker.sock" || marker.ServerVersion != engineVersion {
		t.Fatalf("owned DIND marker does not identify the exact allowed test Engine: suite=%q socket=%q name=%q image=%q host=%q version=%q", marker.Suite, marker.Socket, marker.ContainerName, marker.Image, marker.HostDaemon, marker.ServerVersion)
	}
	return marker
}

func verifyOwnedS04DINDHostContainer(t *testing.T, marker s04DINDOwner) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "docker", "--host", marker.HostDaemon, "inspect", "--format",
		`{{.Id}}|{{.Config.Image}}|{{index .Config.Labels "io.nodedance.suite"}}|{{index .Config.Labels "io.nodedance.test"}}`, marker.ContainerName)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("inspect only exact owner-marked DIND container %q: %v: %s", marker.ContainerName, err, strings.TrimSpace(string(output)))
	}
	parts := strings.Split(strings.TrimSpace(string(output)), "|")
	if len(parts) != 4 || len(parts[0]) != 64 || parts[1] != marker.Image || parts[2] != marker.Suite || parts[3] != "true" {
		t.Fatalf("refusing DIND fault injection because the exact host container does not match its owner marker: %q", strings.TrimSpace(string(output)))
	}
	t.Logf("fault injection target verified: exact host container ID=%s name=%s image=%s suite=%s", parts[0], marker.ContainerName, marker.Image, marker.Suite)
	return parts[0]
}

func setOwnedS04DINDSocketMode(marker s04DINDOwner, mode os.FileMode) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "docker", "--host", marker.HostDaemon, "exec", marker.ContainerName,
		"chmod", fmt.Sprintf("%04o", mode.Perm()), "/run/nodedance-socket/docker.sock")
	output, err := command.CombinedOutput()
	if err != nil {
		return fmt.Errorf("chmod exact socket from its owner-validated DIND container: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}

func waitForS04DockerView(t *testing.T, core *Server, nodeID string, timeout time.Duration, predicate func(coredocker.View) bool, description string) coredocker.View {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var latest coredocker.View
	for time.Now().Before(deadline) {
		_, view, err := core.dockerViewForNode(context.Background(), nodeID)
		if err == nil {
			latest = view
			if predicate(view) {
				return view
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s: availability=%s online=%t stale=%t reason=%q containers=%d", description, latest.DockerAvailability, latest.AgentOnline, latest.DataStale, latest.StaleReason, len(latest.Containers))
	return coredocker.View{}
}

func dockerViewRecordStale(view coredocker.View, containerID string) bool {
	for _, record := range view.Containers {
		if record.Container.ID == containerID {
			return record.Container.Stale
		}
	}
	return false
}

func s04RecordForID(view coredocker.View, containerID string) coredocker.ContainerRecord {
	for _, record := range view.Containers {
		if record.Container.ID == containerID {
			return record
		}
	}
	return coredocker.ContainerRecord{}
}

func metricSequenceForNode(t *testing.T, core *Server, nodeID string) uint64 {
	t.Helper()
	_, view, err := core.metricsViewForNode(context.Background(), nodeID)
	if err != nil || view == nil {
		return 0
	}
	return view.Sequence
}

func waitForS04TelemetryAdvance(t *testing.T, core *Server, nodeID string, heartbeatBefore, metricsBefore uint64, timeout time.Duration, reason string) {
	t.Helper()
	waitForCondition(t, timeout, func() bool {
		return heartbeatSequenceForNode(t, core, nodeID) > heartbeatBefore && metricSequenceForNode(t, core, nodeID) > metricsBefore
	}, reason+" did not leave Agent heartbeat and host metrics live")
	state, metrics, err := core.metricsViewForNode(context.Background(), nodeID)
	if err != nil || state.Status != "online" || metrics == nil {
		t.Fatalf("%s interrupted host monitoring: status=%s metrics=%t err=%v", reason, state.Status, metrics != nil, err)
	}
}

func waitForDockerInventoryContains(t *testing.T, serverURL string, rootPEM []byte, session, nodeID string, expectedIDs []string, timeout time.Duration) dashboardDockerMessage {
	t.Helper()
	// Use an administrator Session for the private Core API. The Agent device
	// credential is intentionally not used for browser authority.
	client := tlsHTTPClient(rootPEM)
	client.Timeout = 3 * time.Second
	expected := make(map[string]struct{}, len(expectedIDs))
	for _, id := range expectedIDs {
		expected[id] = struct{}{}
	}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		request, err := http.NewRequest(http.MethodGet, serverURL+"/api/v1/nodes/"+nodeID+"/containers", nil)
		if err == nil {
			request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: session})
			response, requestErr := client.Do(request)
			if requestErr == nil {
				var inventory dashboardDockerMessage
				decodeErr := json.NewDecoder(io.LimitReader(response.Body, 16<<20)).Decode(&inventory)
				response.Body.Close()
				if response.StatusCode == http.StatusOK && decodeErr == nil && inventory.Inventory != nil {
					found := make(map[string]struct{}, len(inventory.Inventory.Containers))
					for _, record := range inventory.Inventory.Containers {
						if _, wanted := expected[record.Container.ID]; wanted {
							found[record.Container.ID] = struct{}{}
						}
					}
					if len(found) == len(expected) {
						return inventory
					}
				}
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("private Docker inventory did not expose all %d expected containers before timeout", len(expectedIDs))
	return dashboardDockerMessage{}
}

func verifyS04ContainerDetailAPI(t *testing.T, serverURL string, rootPEM []byte, session, nodeID, containerID string) {
	t.Helper()
	request, err := http.NewRequest(http.MethodGet, serverURL+"/api/v1/nodes/"+nodeID+"/containers/"+containerID, nil)
	if err != nil {
		t.Fatal(err)
	}
	request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: session})
	response, err := tlsHTTPClient(rootPEM).Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var detail struct {
		Type      string                      `json:"type"`
		NodeID    string                      `json:"nodeId"`
		Container *coredocker.ContainerRecord `json:"container"`
	}
	decodeErr := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&detail)
	if response.StatusCode != http.StatusOK || decodeErr != nil || detail.Type != "node_container" || detail.NodeID != nodeID || detail.Container == nil || detail.Container.Container.ID != containerID {
		t.Fatalf("private container detail API mismatch: status=%d type=%q node=%q id=%q decode=%v", response.StatusCode, detail.Type, detail.NodeID, containerID, decodeErr)
	}
}

func waitForS04ContainerStateAPI(serverURL string, rootPEM []byte, session, nodeID, containerID string, wantRunning bool, timeout time.Duration) (time.Time, error) {
	client := tlsHTTPClient(rootPEM)
	client.Timeout = 3 * time.Second
	path := serverURL + "/api/v1/nodes/" + nodeID + "/containers/" + containerID
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		request, err := http.NewRequest(http.MethodGet, path, nil)
		if err != nil {
			return time.Time{}, err
		}
		request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: session})
		response, err := client.Do(request)
		if err != nil {
			lastErr = err
			time.Sleep(50 * time.Millisecond)
			continue
		}
		var detail struct {
			Container *coredocker.ContainerRecord `json:"container"`
		}
		decodeErr := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&detail)
		response.Body.Close()
		if response.StatusCode == http.StatusOK && decodeErr == nil && detail.Container != nil {
			if detail.Container.Container.ID != containerID {
				return time.Time{}, fmt.Errorf("Core detail API returned container %q for requested %q", detail.Container.Container.ID, containerID)
			}
			if detail.Container.Container.Running == wantRunning && !detail.Container.Container.Stale {
				return time.Now().UTC(), nil
			}
		} else {
			lastErr = fmt.Errorf("Core detail API status=%d decode=%v", response.StatusCode, decodeErr)
		}
		time.Sleep(50 * time.Millisecond)
	}
	return time.Time{}, fmt.Errorf("container state did not converge to running=%t before timeout: %w", wantRunning, lastErr)
}

func verifyS04DashboardAuthorization(t *testing.T, serverURL string, rootPEM []byte) {
	t.Helper()
	url := strings.Replace(serverURL, "https://", "wss://", 1) + "/ws/v1/dashboard"
	_, response, err := websocket.Dial(context.Background(), url, &websocket.DialOptions{
		HTTPClient: tlsHTTPClient(rootPEM),
		HTTPHeader: http.Header{"Origin": []string{"https://panel.test"}},
	})
	if err == nil || response == nil || response.StatusCode != http.StatusUnauthorized {
		t.Fatal("dashboard WebSocket accepted a request without browser Session")
	}
}

func openS04Dashboard(t *testing.T, serverURL string, rootPEM []byte, session string) *websocket.Conn {
	t.Helper()
	url := strings.Replace(serverURL, "https://", "wss://", 1) + "/ws/v1/dashboard"
	conn, response, err := websocket.Dial(context.Background(), url, &websocket.DialOptions{
		HTTPClient: tlsHTTPClient(rootPEM),
		HTTPHeader: http.Header{
			"Cookie": []string{sessionCookieName + "=" + session},
			"Origin": []string{"https://panel.test"},
		},
	})
	if err != nil {
		status := ""
		if response != nil {
			status = fmt.Sprintf("HTTP %d", response.StatusCode)
		}
		t.Fatalf("authenticated dashboard WebSocket failed (%s)", status)
	}
	conn.SetReadLimit(8 << 20)
	return conn
}

func readS04DashboardInventory(t *testing.T, conn *websocket.Conn, nodeID string, timeout time.Duration) coredocker.View {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			t.Fatalf("read dashboard inventory push: %v", err)
		}
		var message dashboardDockerMessage
		if json.Unmarshal(data, &message) == nil && message.Type == "node_containers" && message.NodeID == nodeID && message.Inventory != nil {
			return *message.Inventory
		}
	}
}

type s04DashboardRead struct {
	view coredocker.View
	err  error
}

func startS04DashboardReader(conn *websocket.Conn) (<-chan s04DashboardRead, context.CancelFunc) {
	ctx, cancel := context.WithCancel(context.Background())
	updates := make(chan s04DashboardRead, 16)
	go func() {
		defer close(updates)
		for {
			_, data, err := conn.Read(ctx)
			if err != nil {
				select {
				case updates <- s04DashboardRead{err: err}:
				case <-ctx.Done():
				}
				return
			}
			var message dashboardDockerMessage
			if json.Unmarshal(data, &message) != nil || message.Type != "node_containers" || message.Inventory == nil {
				continue
			}
			select {
			case updates <- s04DashboardRead{view: *message.Inventory}:
			case <-ctx.Done():
				return
			}
		}
	}()
	return updates, cancel
}

func assertNoRepeatedDashboardInventoryWhileHeartbeatAdvances(t *testing.T, events <-chan s04DashboardRead, core *Server, nodeID string, duration time.Duration) {
	t.Helper()
	before := heartbeatSequenceForNode(t, core, nodeID)
	previous := s04CurrentDockerFingerprint(t, core, nodeID)
	timer := time.NewTimer(duration)
	defer timer.Stop()
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case event, ok := <-events:
			if !ok || event.err != nil {
				t.Fatalf("dashboard closed during heartbeat-only interval: %v", event.err)
			}
			current := s04CurrentDockerFingerprint(t, core, nodeID)
			if current == previous {
				t.Fatalf("dashboard repeated an unchanged Docker inventory: generation=%d stale=%t", event.view.ActiveGeneration, event.view.DataStale)
			}
			// A state transition can race Dashboard subscription setup (for
			// example, the event stream becoming connected after the initial
			// complete snapshot). That push is required; use it as the baseline
			// for detecting subsequent heartbeat-only duplicates.
			previous = current
		case <-tick.C:
		case <-timer.C:
			if after := heartbeatSequenceForNode(t, core, nodeID); after <= before {
				t.Fatalf("heartbeat did not advance during Docker push deduplication interval: before=%d after=%d", before, after)
			}
			return
		}
	}
}

func s04CurrentDockerFingerprint(t *testing.T, core *Server, nodeID string) dockerPushFingerprint {
	t.Helper()
	_, view, revision, err := core.dockerViewStateForNode(context.Background(), nodeID)
	if err != nil {
		t.Fatalf("read current Docker state for Dashboard push assertion: %v", err)
	}
	return dockerPushFingerprint{
		revision: revision, generation: view.ActiveGeneration, agentOnline: view.AgentOnline,
		availability: string(view.DockerAvailability), eventsConnected: view.DockerEventsConnected,
		snapshotFresh: view.DockerSnapshotFresh, dataStale: view.DataStale,
		staleReason: view.StaleReason, containerCount: len(view.Containers),
	}
}

func waitForS04DashboardInventory(t *testing.T, events <-chan s04DashboardRead, nodeID string, predicate func(coredocker.View) bool, timeout time.Duration) coredocker.View {
	t.Helper()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for {
		select {
		case event, ok := <-events:
			if !ok || event.err != nil {
				t.Fatalf("dashboard closed while waiting for inventory update: %v", event.err)
			}
			if event.view.NodeID == nodeID && predicate(event.view) {
				return event.view
			}
		case <-timer.C:
			t.Fatal("dashboard inventory update did not arrive before timeout")
		}
	}
}

func waitForS04DockerStaleEvent(t *testing.T, events <-chan s04DashboardRead, nodeID string, staleAfter time.Time, timeout time.Duration) coredocker.View {
	t.Helper()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for {
		select {
		case event, ok := <-events:
			if !ok || event.err != nil {
				t.Fatalf("dashboard closed while waiting for expired Docker freshness: %v", event.err)
			}
			if event.view.NodeID != nodeID || !event.view.DataStale || !event.view.AgentOnline || event.view.StaleReason != "docker_health_stale" {
				t.Fatalf("unexpected Docker inventory push while waiting for health lease expiry: online=%t stale=%t reason=%q", event.view.AgentOnline, event.view.DataStale, event.view.StaleReason)
			}
			if event.view.ServerTime.Before(staleAfter) {
				t.Fatalf("Core advertised Docker health stale before its 15-second receive-time lease expired: serverTime=%s validUntil=%s", event.view.ServerTime.Format(time.RFC3339Nano), staleAfter.Format(time.RFC3339Nano))
			}
			return event.view
		case <-timer.C:
			t.Fatalf("Docker freshness did not transition stale within %s after its receive-time deadline", timeout)
		}
	}
}

func waitForS04DashboardClosed(t *testing.T, events <-chan s04DashboardRead, timeout time.Duration) {
	t.Helper()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for {
		select {
		case event, ok := <-events:
			if !ok || event.err != nil {
				return
			}
		case <-timer.C:
			t.Fatal("revoked browser Session did not close its Docker dashboard stream within two seconds")
		}
	}
}

func dockerViewContains(view coredocker.View, containerID string) bool {
	for _, record := range view.Containers {
		if record.Container.ID == containerID {
			return true
		}
	}
	return false
}

func dockerViewContainerIDs(view coredocker.View) []string {
	ids := make([]string, 0, len(view.Containers))
	for _, record := range view.Containers {
		ids = append(ids, record.Container.ID)
	}
	sort.Strings(ids)
	return ids
}

func sortedStrings(values []string) []string {
	result := append([]string(nil), values...)
	sort.Strings(result)
	return result
}

func s04StoredContainerIDs(t *testing.T, core *Server, nodeID string) []string {
	t.Helper()
	rows, err := core.store.DB.Query(`SELECT container_id FROM docker_containers WHERE node_id=? ORDER BY container_id`, nodeID)
	if err != nil {
		t.Fatalf("query persisted Docker inventory IDs: %v", err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatalf("scan persisted Docker inventory ID: %v", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate persisted Docker inventory IDs: %v", err)
	}
	return ids
}

func waitForCondition(t *testing.T, timeout time.Duration, condition func() bool, failure string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatal(failure)
}

func inventoryCount(message dashboardDockerMessage) int {
	if message.Inventory == nil {
		return 0
	}
	return len(message.Inventory.Containers)
}
