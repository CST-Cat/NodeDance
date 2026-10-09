package server

import (
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
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/CST-Cat/NodeDance/internal/agent"
	coredocker "github.com/CST-Cat/NodeDance/internal/core/docker"
	"github.com/CST-Cat/NodeDance/internal/protocol"
	"github.com/coder/websocket"
)

// TestRealAgentDockerInventoryTraceOnOwnedDIND follows an exact 11-container
// fixture set through Docker Engine API responses, Agent WSS frames, and the
// Core's final committed view. The fixtures are created while the Agent is
// already online to reproduce the event-driven path before a reconnect snapshot.
func TestRealAgentDockerInventoryTraceOnOwnedDIND(t *testing.T) {
	root, endpoint, engineVersion := requireOwnedS04DIND(t)
	t.Setenv("DOCKER_HOST", endpoint)
	if _, err := runS04DockerCLI(endpoint, "image", "inspect", "--format", "{{.Id}}", s04BusyboxImage); err != nil {
		if _, pullErr := runS04DockerCLI(endpoint, "pull", s04BusyboxImage); pullErr != nil {
			t.Fatalf("ensure locked S04 trace fixture image exists on owned DIND: inspect=%v pull=%v", err, pullErr)
		}
	}

	baselineIDs, err := s04EngineContainerIDs(endpoint)
	if err != nil {
		t.Fatal("read exact DIND baseline before Agent starts:", err)
	}
	fixtureRunID := fmt.Sprintf("nd-s04-inventory-trace-%d", time.Now().UnixNano())
	fixtureIDs := make([]string, 0, 11)
	fixtureNames := make([]string, 0, 11)
	t.Cleanup(func() {
		cleanupS04InventoryTraceFixtures(t, endpoint, fixtureRunID, fixtureNames, fixtureIDs, baselineIDs)
	})

	workRoot := filepath.Join(root, ".artifacts", "work-s04")
	if err := os.MkdirAll(workRoot, 0o700); err != nil {
		t.Fatal("create S04 inventory-trace work root:", err)
	}
	work, err := os.MkdirTemp(workRoot, "inventory-trace-")
	if err != nil {
		t.Fatal("create S04 inventory-trace work directory:", err)
	}
	if err := os.Chmod(work, 0o700); err != nil {
		t.Fatal("restrict S04 inventory-trace work directory:", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(work) })

	certificate, rootPEM, err := makeAgentTestCertificate()
	if err != nil {
		t.Fatal("create S04 inventory-trace test certificate:", err)
	}
	core, err := New("s04-inventory-trace", Options{
		DataDir: filepath.Join(work, "core"), PublicOrigin: "https://panel.test",
	})
	if err != nil {
		t.Fatal("start S04 inventory-trace Core:", err)
	}
	coreHTTP := httptest.NewUnstartedServer(core)
	coreHTTP.TLS = &tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS12}
	coreHTTP.StartTLS()
	var stopAgent func()
	t.Cleanup(func() {
		if stopAgent != nil {
			stopAgent()
		}
		coreHTTP.Close()
		if err := core.Close(); err != nil {
			t.Errorf("close S04 inventory-trace Core: %v", err)
		}
	})

	enrollment, err := core.agents.CreateEnrollment(context.Background(), "S04 inventory trace "+engineVersion,
		"127.0.0.1", sql.NullInt64{Int64: 1, Valid: true})
	if err != nil {
		t.Fatal("create S04 inventory-trace enrollment:", err)
	}
	caPath := filepath.Join(work, "trusted-ca.pem")
	if err := os.WriteFile(caPath, rootPEM, 0o600); err != nil {
		t.Fatal("write S04 inventory-trace CA:", err)
	}
	configPath := filepath.Join(work, "agent", "agent.json")
	if err := agent.Enroll(context.Background(), coreHTTP.URL, caPath, false, strings.NewReader(enrollment.Token), configPath); err != nil {
		t.Fatal("enroll S04 inventory-trace Agent:", err)
	}
	config, err := agent.LoadConfig(configPath)
	if err != nil {
		t.Fatal("load S04 inventory-trace Agent config:", err)
	}

	wssProxy := &s04InventoryTraceWSSProxy{coreURL: coreHTTP.URL, rootPEM: rootPEM}
	wssServer := httptest.NewUnstartedServer(wssProxy)
	wssServer.TLS = &tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS12}
	wssServer.StartTLS()
	config.Server = wssServer.URL
	if err := agent.SaveConfig(configPath, config, false); err != nil {
		t.Fatal("point test Agent at the inventory-trace WSS proxy:", err)
	}
	t.Cleanup(wssServer.Close)

	engineTap, agentDockerHost := startS04DockerAPITap(t, endpoint)
	t.Setenv("DOCKER_HOST", agentDockerHost)
	var agentCancel context.CancelFunc
	var agentDone chan error
	startAgent := func() {
		t.Helper()
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		agentCancel, agentDone = cancel, done
		go func() { done <- agent.Run(ctx, configPath, "s04-inventory-trace", io.Discard) }()
		waitForAgentStatus(t, core, config.NodeID, "online", 0)
	}
	stopAgent = func() {
		if agentCancel == nil {
			return
		}
		cancel, done := agentCancel, agentDone
		agentCancel, agentDone = nil, nil
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("stop S04 inventory-trace Agent: %v", err)
			}
		case <-time.After(12 * time.Second):
			t.Error("S04 inventory-trace Agent did not stop within 12 seconds")
		}
	}
	t.Cleanup(func() {
		if stopAgent != nil {
			stopAgent()
		}
	})
	startAgent()
	initial := waitForS04DockerView(t, core, config.NodeID, 30*time.Second, func(view coredocker.View) bool {
		return view.ActiveGeneration != 0 && view.AgentOnline && !view.DataStale &&
			view.DockerSnapshotFresh && view.DockerAvailability == "available" &&
			sameS04StringSet(dockerViewContainerIDs(view), baselineIDs)
	}, "fresh initial snapshot of the exact DIND baseline")
	t.Logf("S04_INVENTORY_TRACE initial generation=%d baseline_ids=%v", initial.ActiveGeneration, sortedStrings(baselineIDs))

	createStartedAt := time.Now().UTC()
	for slot := 0; slot < 11; slot++ {
		name := fmt.Sprintf("%s-slot-%02d", fixtureRunID, slot)
		fixtureNames = append(fixtureNames, name)
		id, err := runS04DockerCLI(endpoint, "container", "run", "--detach", "--name", name,
			"--label", "io.nodedance.test=true", "--label", "io.nodedance.suite="+fixtureRunID,
			s04BusyboxImage, "sh", "-c", "sleep 300")
		if err != nil {
			t.Fatalf("create exact inventory-trace fixture slot %02d: %v", slot, err)
		}
		id = strings.TrimSpace(id)
		if !protocol.IsFullContainerID(id) {
			t.Fatalf("Engine returned a non-full ID for inventory-trace fixture slot %02d: %q", slot, id)
		}
		fixtureIDs = append(fixtureIDs, id)
		t.Logf("S04_INVENTORY_TRACE fixture slot=%02d id=%s name=%s", slot, id, name)
	}
	expectedIDs := append(append([]string(nil), baselineIDs...), fixtureIDs...)
	engineIDsAfterCreate, err := s04EngineContainerIDs(endpoint)
	if err != nil || !sameS04StringSet(engineIDsAfterCreate, expectedIDs) {
		t.Fatalf("DIND exact full-ID inventory after fixture creation differs: got=%v expected=%v err=%v",
			engineIDsAfterCreate, sortedStrings(expectedIDs), err)
	}

	preReconnectView, eventViewComplete := waitForS04TraceEventInventory(core, config.NodeID, initial.ActiveGeneration, expectedIDs, 30*time.Second)
	preReconnectWire := wssProxy.wireEventsSince(createStartedAt)
	engineBeforeReconnect := engineTap.eventsSince(createStartedAt)
	t.Logf("S04_INVENTORY_TRACE event_path engine_api=%s agent_wss=%s core_view_ids=%v complete=%t",
		formatS04EngineTrace(engineBeforeReconnect), formatS04WireTrace(preReconnectWire, initial.ActiveGeneration),
		dockerViewContainerIDs(preReconnectView), eventViewComplete)

	previousGeneration := generationForNode(t, core, config.NodeID)
	reconnectStartedAt := time.Now().UTC()
	core.closeAgentConnection(config.AgentID, previousGeneration)
	finalView, snapshotComplete := waitForS04TraceSnapshotInventory(core, config.NodeID, previousGeneration, expectedIDs, 40*time.Second)
	engineAfterReconnect := engineTap.eventsSince(reconnectStartedAt)
	allWire := wssProxy.wireEventsSince(createStartedAt)
	matchedSnapshot, snapshotTraceComplete := findS04TraceSnapshot(allWire, finalView.ActiveGeneration, expectedIDs)
	engineScanComplete := s04EngineTraceHasFullScan(engineAfterReconnect, expectedIDs)
	storedIDs := s04StoredContainerIDs(t, core, config.NodeID)
	storedInventoryComplete := sameS04StringSet(storedIDs, expectedIDs)
	t.Logf("S04_INVENTORY_TRACE reconnect engine_api=%s snapshot=%s core_final_ids=%v snapshot_complete=%t engine_scan_complete=%t",
		formatS04EngineTrace(engineAfterReconnect), formatS04SnapshotTrace(matchedSnapshot),
		dockerViewContainerIDs(finalView), snapshotComplete && snapshotTraceComplete, engineScanComplete)
	t.Logf("S04_INVENTORY_TRACE core_stored_ids=%v stored_inventory_complete=%t", sortedStrings(storedIDs), storedInventoryComplete)

	if !eventViewComplete {
		t.Errorf("event-driven inventory did not contain all exact Engine fixture IDs within 30 seconds: got=%v expected=%v",
			dockerViewContainerIDs(preReconnectView), sortedStrings(expectedIDs))
	}
	if !engineScanComplete {
		t.Errorf("SDK ListAll/Inspect trace after reconnect did not prove every exact Engine ID was listed and inspected")
	}
	if !snapshotTraceComplete {
		t.Errorf("Agent WSS full snapshot after reconnect did not contain every exact Engine ID with contiguous index/final markers")
	}
	if !snapshotComplete || !sameS04StringSet(dockerViewContainerIDs(finalView), expectedIDs) {
		t.Errorf("Core final committed view does not exactly match DIND Engine IDs: complete=%t got=%v expected=%v",
			snapshotComplete, dockerViewContainerIDs(finalView), sortedStrings(expectedIDs))
	}
	if !storedInventoryComplete {
		t.Errorf("Core stored inventory does not exactly match DIND Engine IDs: got=%v expected=%v",
			sortedStrings(storedIDs), sortedStrings(expectedIDs))
	}
}

func cleanupS04InventoryTraceFixtures(t *testing.T, endpoint, suite string, names, recordedIDs, baselineIDs []string) {
	t.Helper()
	nameSet := make(map[string]struct{}, len(names))
	for _, name := range names {
		nameSet[name] = struct{}{}
	}
	candidates := make(map[string]struct{}, len(recordedIDs)+len(names))
	for _, id := range recordedIDs {
		if protocol.IsFullContainerID(id) {
			candidates[id] = struct{}{}
		}
	}
	output, err := runS04DockerCLIRaw(endpoint, "container", "ls", "--all", "--quiet", "--no-trunc",
		"--filter", "label=io.nodedance.suite="+suite)
	if err != nil {
		t.Errorf("list only suite-labeled S04 inventory-trace fixtures during cleanup: %v: %s", err, strings.TrimSpace(output))
	} else {
		for _, id := range strings.Fields(output) {
			if !protocol.IsFullContainerID(id) {
				t.Errorf("cleanup label query returned a non-full container ID %q; refusing to remove it", id)
				continue
			}
			candidates[id] = struct{}{}
		}
	}
	for _, name := range names {
		identity, inspectErr := inspectS04TraceFixture(endpoint, name)
		if inspectErr != nil {
			if !dockerS04InspectNotFound(identity) {
				t.Errorf("inspect exact registered inventory-trace fixture name %s during cleanup: %v: %s", name, inspectErr, strings.TrimSpace(identity))
			}
			continue
		}
		if !s04TraceFixtureIdentityOwned(identity, "", name, suite, nameSet) {
			t.Errorf("refusing to clean fixture name with mismatched ID/labels: name=%s inspect=%q", name, strings.TrimSpace(identity))
			continue
		}
		parts := strings.Split(strings.TrimSpace(identity), "|")
		candidates[parts[0]] = struct{}{}
	}
	for id := range candidates {
		identity, inspectErr := inspectS04TraceFixture(endpoint, id)
		if inspectErr != nil {
			if !dockerS04InspectNotFound(identity) {
				t.Errorf("inspect exact inventory-trace fixture ID %s during cleanup: %v: %s", id, inspectErr, strings.TrimSpace(identity))
			}
			continue
		}
		if !s04TraceFixtureIdentityOwned(identity, id, "", suite, nameSet) {
			t.Errorf("refusing to clean fixture ID with mismatched name or labels: id=%s inspect=%q", id, strings.TrimSpace(identity))
			continue
		}
		if _, removeErr := runS04DockerCLIRaw(endpoint, "container", "rm", "--force", id); removeErr != nil {
			t.Errorf("remove exact suite-owned inventory-trace fixture %s: %v", id, removeErr)
		}
	}
	for id := range candidates {
		identity, inspectErr := inspectS04TraceFixture(endpoint, id)
		if inspectErr == nil || !dockerS04InspectNotFound(identity) {
			t.Errorf("inventory-trace fixture ID remains or cannot be verified absent: id=%s output=%q err=%v", id, strings.TrimSpace(identity), inspectErr)
		}
	}
	for _, name := range names {
		identity, inspectErr := inspectS04TraceFixture(endpoint, name)
		if inspectErr == nil || !dockerS04InspectNotFound(identity) {
			t.Errorf("inventory-trace fixture name remains or cannot be verified absent: name=%s output=%q err=%v", name, strings.TrimSpace(identity), inspectErr)
		}
	}
	finalIDs, finalErr := s04EngineContainerIDs(endpoint)
	if finalErr != nil || !sameS04StringSet(finalIDs, baselineIDs) {
		t.Errorf("Engine IDs after exact inventory-trace cleanup differ from the pre-test baseline: got=%v want=%v err=%v",
			sortedStrings(finalIDs), sortedStrings(baselineIDs), finalErr)
	}
}

func inspectS04TraceFixture(endpoint, idOrName string) (string, error) {
	return runS04DockerCLIRaw(endpoint, "container", "inspect", "--format",
		`{{.Id}}|{{.Name}}|{{index .Config.Labels "io.nodedance.suite"}}|{{index .Config.Labels "io.nodedance.test"}}`, idOrName)
}

func s04TraceFixtureIdentityOwned(identity, expectedID, expectedName, suite string, names map[string]struct{}) bool {
	parts := strings.Split(strings.TrimSpace(identity), "|")
	if len(parts) != 4 || !protocol.IsFullContainerID(parts[0]) || parts[2] != suite || parts[3] != "true" {
		return false
	}
	if expectedID != "" && parts[0] != expectedID {
		return false
	}
	containerName := strings.TrimPrefix(parts[1], "/")
	if expectedName != "" && containerName != expectedName {
		return false
	}
	_, exactName := names[containerName]
	return exactName
}

func runS04DockerCLIRaw(endpoint string, args ...string) (string, error) {
	commandArgs := append([]string{"--host", endpoint}, args...)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "docker", commandArgs...)
	output, err := command.CombinedOutput()
	if err != nil {
		return string(output), fmt.Errorf("docker --host <owned-s04-socket> %s: %w", strings.Join(args, " "), err)
	}
	return string(output), nil
}

func dockerS04InspectNotFound(output string) bool {
	message := strings.ToLower(output)
	return strings.Contains(message, "no such object") || strings.Contains(message, "no such container")
}

type s04EngineTraceEvent struct {
	At                 time.Time
	Kind               string
	Method             string
	Path               string
	Query              string
	Status             int
	ListIDs            []string
	InspectRequestID   string
	InspectResponseID  string
	TransportErrorType string
}

type s04DockerAPITap struct {
	mu     sync.Mutex
	events []s04EngineTraceEvent
}

func startS04DockerAPITap(t *testing.T, endpoint string) (*s04DockerAPITap, string) {
	t.Helper()
	endpointSocket := strings.TrimPrefix(endpoint, "unix://")
	tapDir, err := os.MkdirTemp("", "nd-s04-api-")
	if err != nil {
		t.Fatal("create isolated Agent Docker API tap directory:", err)
	}
	if err := os.Chmod(tapDir, 0o700); err != nil {
		_ = os.RemoveAll(tapDir)
		t.Fatal("restrict isolated Agent Docker API tap directory:", err)
	}
	socketPath := filepath.Join(tapDir, "engine.sock")
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		_ = os.RemoveAll(tapDir)
		t.Fatal("listen on isolated Agent Docker API tap socket:", err)
	}
	if err := os.Chmod(socketPath, 0o600); err != nil {
		_ = listener.Close()
		_ = os.RemoveAll(tapDir)
		t.Fatal("restrict isolated Agent Docker API tap socket:", err)
	}
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", endpointSocket)
	}}
	apiTrace := &s04DockerAPITap{}
	target, _ := url.Parse("http://docker")
	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.Transport = transport
	proxy.ModifyResponse = func(response *http.Response) error {
		if response.Request == nil {
			return nil
		}
		path := response.Request.URL.Path
		if response.Request.Method != http.MethodGet ||
			(!strings.Contains(path, "/containers/json") && !isS04InspectPath(path)) {
			return nil
		}
		body, err := io.ReadAll(response.Body)
		if err != nil {
			return err
		}
		_ = response.Body.Close()
		response.Body = io.NopCloser(bytes.NewReader(body))
		response.ContentLength = int64(len(body))
		response.Header.Set("Content-Length", fmt.Sprint(len(body)))
		event := s04EngineTraceEvent{
			At: time.Now().UTC(), Method: response.Request.Method, Path: path,
			Query: response.Request.URL.RawQuery, Status: response.StatusCode,
		}
		if strings.Contains(path, "/containers/json") {
			event.Kind = "list_all"
			var items []struct {
				ID string `json:"Id"`
			}
			if response.StatusCode == http.StatusOK && json.Unmarshal(body, &items) == nil {
				for _, item := range items {
					event.ListIDs = append(event.ListIDs, item.ID)
				}
				sort.Strings(event.ListIDs)
			}
		} else {
			event.Kind = "inspect"
			event.InspectRequestID = s04InspectIDFromPath(path)
			var item struct {
				ID string `json:"Id"`
			}
			if response.StatusCode == http.StatusOK && json.Unmarshal(body, &item) == nil {
				event.InspectResponseID = item.ID
			}
		}
		apiTrace.record(event)
		return nil
	}
	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		path := r.URL.Path
		if strings.Contains(path, "/containers/json") || isS04InspectPath(path) {
			kind := "list_all"
			if isS04InspectPath(path) {
				kind = "inspect"
			}
			event := s04EngineTraceEvent{At: time.Now().UTC(), Kind: kind, Method: r.Method,
				Path: path, Query: r.URL.RawQuery, Status: http.StatusBadGateway,
				TransportErrorType: fmt.Sprintf("%T", err)}
			if kind == "inspect" {
				event.InspectRequestID = s04InspectIDFromPath(path)
			}
			apiTrace.record(event)
		}
		http.Error(w, "owned DIND API tap forwarding failed", http.StatusBadGateway)
	}
	server := &http.Server{Handler: proxy}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() {
		_ = server.Close()
		transport.CloseIdleConnections()
		_ = os.RemoveAll(tapDir)
	})
	return apiTrace, "unix://" + socketPath
}

func isS04InspectPath(path string) bool {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	return len(parts) >= 4 && parts[len(parts)-3] == "containers" && parts[len(parts)-1] == "json"
}

func s04InspectIDFromPath(path string) string {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) < 4 || parts[len(parts)-3] != "containers" || parts[len(parts)-1] != "json" {
		return ""
	}
	return parts[len(parts)-2]
}

func (p *s04DockerAPITap) record(event s04EngineTraceEvent) {
	p.mu.Lock()
	p.events = append(p.events, event)
	p.mu.Unlock()
}

func (p *s04DockerAPITap) eventsSince(after time.Time) []s04EngineTraceEvent {
	p.mu.Lock()
	defer p.mu.Unlock()
	var events []s04EngineTraceEvent
	for _, event := range p.events {
		if !event.At.Before(after) {
			event.ListIDs = append([]string(nil), event.ListIDs...)
			events = append(events, event)
		}
	}
	return events
}

func formatS04EngineTrace(events []s04EngineTraceEvent) string {
	encoded, err := json.Marshal(events)
	if err != nil {
		return fmt.Sprintf("marshal_error=%v count=%d", err, len(events))
	}
	return string(encoded)
}

func s04EngineTraceHasFullScan(events []s04EngineTraceEvent, expected []string) bool {
	for _, list := range events {
		all, queryErr := url.ParseQuery(list.Query)
		if list.Kind != "list_all" || list.Status != http.StatusOK || queryErr != nil || all.Get("all") != "1" ||
			!sameS04StringSet(list.ListIDs, expected) {
			continue
		}
		inspected := make(map[string]string, len(expected))
		for _, event := range events {
			if event.Kind == "inspect" && event.Status == http.StatusOK && !event.At.Before(list.At) &&
				event.InspectRequestID != "" && event.InspectRequestID == event.InspectResponseID {
				inspected[event.InspectRequestID] = event.InspectResponseID
			}
		}
		ids := make([]string, 0, len(inspected))
		for id := range inspected {
			ids = append(ids, id)
		}
		if sameS04StringSet(ids, expected) {
			return true
		}
	}
	return false
}

type s04WireTraceEvent struct {
	At               time.Time
	Generation       uint64
	EnvelopeSequence uint64
	BatchSequence    uint64
	SnapshotID       uint64
	FullSnapshot     bool
	SnapshotIndex    int
	SnapshotFinal    bool
	ContainerIDs     []string
}

type s04InventoryTraceWSSProxy struct {
	coreURL string
	rootPEM []byte
	mu      sync.Mutex
	events  []s04WireTraceEvent
}

func (p *s04InventoryTraceWSSProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/ws/v1/agent" {
		http.NotFound(w, r)
		return
	}
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	upstreamURL := strings.Replace(p.coreURL, "https://", "wss://", 1) + "/ws/v1/agent"
	upstream, response, err := websocket.Dial(ctx, upstreamURL, &websocket.DialOptions{
		HTTPClient: tlsHTTPClient(p.rootPEM),
		HTTPHeader: http.Header{"Authorization": []string{r.Header.Get("Authorization")}},
	})
	if err != nil {
		status := http.StatusBadGateway
		if response != nil && response.StatusCode >= 400 {
			status = response.StatusCode
		}
		http.Error(w, "inventory-trace Core Agent channel unavailable", status)
		return
	}
	downstream, err := websocket.Accept(w, r, &websocket.AcceptOptions{})
	if err != nil {
		_ = upstream.CloseNow()
		return
	}
	upstream.SetReadLimit(protocol.MaxMessageBytes + 1024)
	downstream.SetReadLimit(protocol.MaxMessageBytes + 1024)
	done := make(chan struct{}, 2)
	go p.copyAgentToCore(ctx, downstream, upstream, done)
	go p.copyCoreToAgent(ctx, upstream, downstream, done)
	<-done
	cancel()
	_ = downstream.CloseNow()
	_ = upstream.CloseNow()
	<-done
}

func (p *s04InventoryTraceWSSProxy) copyAgentToCore(ctx context.Context, source, target *websocket.Conn, done chan<- struct{}) {
	defer func() { done <- struct{}{} }()
	for {
		messageType, data, err := source.Read(ctx)
		if err != nil {
			return
		}
		p.recordDockerEnvelope(data)
		if err := target.Write(ctx, messageType, data); err != nil {
			return
		}
	}
}

func (p *s04InventoryTraceWSSProxy) copyCoreToAgent(ctx context.Context, source, target *websocket.Conn, done chan<- struct{}) {
	defer func() { done <- struct{}{} }()
	for {
		messageType, data, err := source.Read(ctx)
		if err != nil {
			return
		}
		if err := target.Write(ctx, messageType, data); err != nil {
			return
		}
	}
}

func (p *s04InventoryTraceWSSProxy) recordDockerEnvelope(data []byte) {
	var envelope protocol.Envelope
	if json.Unmarshal(data, &envelope) != nil || envelope.Type != protocol.TypeDocker {
		return
	}
	var batch protocol.DockerBatch
	if json.Unmarshal(envelope.Payload, &batch) != nil {
		return
	}
	event := s04WireTraceEvent{
		At: time.Now().UTC(), Generation: envelope.Generation, EnvelopeSequence: envelope.Sequence,
		BatchSequence: batch.Sequence, SnapshotID: batch.SnapshotID, FullSnapshot: batch.FullSnapshot,
		SnapshotIndex: batch.SnapshotIndex, SnapshotFinal: batch.SnapshotFinal,
		ContainerIDs: make([]string, 0, len(batch.Changes)),
	}
	for _, change := range batch.Changes {
		id := change.ContainerID
		if change.Container != nil && change.Container.ID != "" {
			id = change.Container.ID
		}
		if id != "" {
			event.ContainerIDs = append(event.ContainerIDs, id)
		}
	}
	sort.Strings(event.ContainerIDs)
	p.mu.Lock()
	p.events = append(p.events, event)
	p.mu.Unlock()
}

func (p *s04InventoryTraceWSSProxy) wireEventsSince(after time.Time) []s04WireTraceEvent {
	p.mu.Lock()
	defer p.mu.Unlock()
	var events []s04WireTraceEvent
	for _, event := range p.events {
		if !event.At.Before(after) {
			event.ContainerIDs = append([]string(nil), event.ContainerIDs...)
			events = append(events, event)
		}
	}
	return events
}

func formatS04WireTrace(events []s04WireTraceEvent, generation uint64) string {
	var filtered []s04WireTraceEvent
	for _, event := range events {
		if event.Generation == generation {
			filtered = append(filtered, event)
		}
	}
	encoded, err := json.Marshal(filtered)
	if err != nil {
		return fmt.Sprintf("marshal_error=%v count=%d", err, len(filtered))
	}
	return string(encoded)
}

type s04SnapshotGroupKey struct {
	Generation uint64
	SnapshotID uint64
	Sequence   uint64
}

func findS04TraceSnapshot(events []s04WireTraceEvent, generation uint64, expected []string) ([]s04WireTraceEvent, bool) {
	groups := make(map[s04SnapshotGroupKey][]s04WireTraceEvent)
	for _, event := range events {
		if event.FullSnapshot && event.Generation == generation {
			key := s04SnapshotGroupKey{Generation: event.Generation, SnapshotID: event.SnapshotID, Sequence: event.BatchSequence}
			groups[key] = append(groups[key], event)
		}
	}
	for _, group := range groups {
		sort.Slice(group, func(i, j int) bool { return group[i].SnapshotIndex < group[j].SnapshotIndex })
		if s04SnapshotGroupMatches(group, expected) {
			return group, true
		}
	}
	return nil, false
}

func s04SnapshotGroupMatches(group []s04WireTraceEvent, expected []string) bool {
	if len(group) == 0 {
		return false
	}
	ids := make([]string, 0)
	for index, chunk := range group {
		if chunk.SnapshotIndex != index || chunk.SnapshotFinal != (index == len(group)-1) {
			return false
		}
		ids = append(ids, chunk.ContainerIDs...)
	}
	return sameS04StringSet(ids, expected)
}

func formatS04SnapshotTrace(group []s04WireTraceEvent) string {
	encoded, err := json.Marshal(group)
	if err != nil {
		return fmt.Sprintf("marshal_error=%v count=%d", err, len(group))
	}
	return string(encoded)
}

func waitForS04TraceEventInventory(core *Server, nodeID string, generation uint64, expected []string, timeout time.Duration) (coredocker.View, bool) {
	deadline := time.Now().Add(timeout)
	var latest coredocker.View
	for time.Now().Before(deadline) {
		_, view, err := core.dockerViewForNode(context.Background(), nodeID)
		if err == nil {
			latest = view
			if view.ActiveGeneration == generation && view.AgentOnline && !view.DataStale &&
				view.DockerSnapshotFresh && view.DockerAvailability == "available" &&
				sameS04StringSet(dockerViewContainerIDs(view), expected) {
				return view, true
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	return latest, false
}

func waitForS04TraceSnapshotInventory(core *Server, nodeID string, previousGeneration uint64, expected []string, timeout time.Duration) (coredocker.View, bool) {
	deadline := time.Now().Add(timeout)
	var latest coredocker.View
	for time.Now().Before(deadline) {
		_, view, err := core.dockerViewForNode(context.Background(), nodeID)
		if err == nil {
			latest = view
			if view.ActiveGeneration > previousGeneration && view.AgentOnline && !view.DataStale &&
				view.DockerSnapshotFresh && view.DockerAvailability == "available" &&
				sameS04StringSet(dockerViewContainerIDs(view), expected) {
				return view, true
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	return latest, false
}
