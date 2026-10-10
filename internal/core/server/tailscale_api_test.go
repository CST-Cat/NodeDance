package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	agentruntime "github.com/CST-Cat/NodeDance/internal/agent"
	"github.com/CST-Cat/NodeDance/internal/core/agents"
	"github.com/CST-Cat/NodeDance/internal/core/auth"
	coremetrics "github.com/CST-Cat/NodeDance/internal/core/metrics"
	"github.com/CST-Cat/NodeDance/internal/core/storage"
	"github.com/CST-Cat/NodeDance/internal/core/tailscale"
	coretasks "github.com/CST-Cat/NodeDance/internal/core/tasks"
	"github.com/CST-Cat/NodeDance/internal/protocol"
	"github.com/CST-Cat/NodeDance/internal/taskstate"
	"github.com/coder/websocket"
)

func newUnknownDeploymentForTest(t *testing.T, dataDir string) (*storage.Store, *coretasks.Store, coretasks.Task) {
	t.Helper()
	ctx := context.Background()
	database, err := storage.Open(ctx, dataDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	store, err := coretasks.New(database.DB, coretasks.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.DB.ExecContext(ctx, `INSERT INTO nodes(id,display_name,status,created_at,updated_at) VALUES('node-1','stage6-test','pending',1,1)`); err != nil {
		t.Fatal(err)
	}
	intent := protocol.TaskIntent{
		Action: protocol.TaskAgentDeploy, ContainerID: "tailscale-peer:peer-key",
		AgentDeploy: &protocol.AgentDeployTaskSpec{PeerIdentity: "peer-key", PeerName: "stage6-test", CoreURL: "https://core.example",
			HostFingerprint: "SHA256:abcdefghijklmnopqrstuvwxyz0123456789", SSHUser: "root", Authentication: "password"},
	}
	accepted, err := store.EnqueueAndStartLocal(ctx, coretasks.EnqueueRequest{NodeID: "node-1", IdempotencyKey: "resolve-test-key", Intent: intent})
	if err != nil {
		t.Fatal(err)
	}
	unknown, err := store.MarkLocalUnknown(ctx, "node-1", accepted.Task.TaskID, sql.NullInt64{}, "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	return database, store, unknown
}

func TestResolveUnknownDeploymentAssociatesPeerBeforeSuccess(t *testing.T) {
	dataDir := t.TempDir()
	_, store, task := newUnknownDeploymentForTest(t, dataDir)
	if err := tailscale.NewStateStore(dataDir).Associate("peer-key", "node-1"); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest("POST", "/api/v1/discovery/deployments/"+task.TaskID+"/resolve", strings.NewReader(`{"outcome":"succeeded","observedState":"connected","confirmed":true}`))
	request.RemoteAddr = "192.0.2.10:1234"
	response := httptest.NewRecorder()
	(&Server{tasks: store, dataDir: dataDir}).handleResolveTailscaleDeployment(response, request, &session{}, task.TaskID)
	if response.Code != 200 {
		t.Fatalf("resolve response: status=%d body=%s", response.Code, response.Body.String())
	}
	state, err := tailscale.NewStateStore(dataDir).Read()
	if err != nil {
		t.Fatal(err)
	}
	if state.Managed["peer-key"] != "node-1" {
		t.Fatalf("resolved successful deployment was not associated: %#v", state.Managed)
	}
	resolved, err := store.Get(context.Background(), "node-1", task.TaskID)
	if err != nil || resolved.Status != taskstate.Succeeded {
		t.Fatalf("Core task should be durably succeeded: status=%s err=%v", resolved.Status, err)
	}
}

func TestResolveFailedDeploymentClearsMatchingPeerAssociation(t *testing.T) {
	dataDir := t.TempDir()
	_, store, task := newUnknownDeploymentForTest(t, dataDir)
	if err := tailscale.NewStateStore(dataDir).Associate("peer-key", "node-1"); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest("POST", "/api/v1/discovery/deployments/"+task.TaskID+"/resolve", strings.NewReader(`{"outcome":"failed","observedState":"installation_failed","confirmed":true}`))
	request.RemoteAddr = "192.0.2.10:1234"
	response := httptest.NewRecorder()
	(&Server{tasks: store, dataDir: dataDir}).handleResolveTailscaleDeployment(response, request, &session{}, task.TaskID)
	if response.Code != 200 {
		t.Fatalf("resolve response: status=%d body=%s", response.Code, response.Body.String())
	}
	state, err := tailscale.NewStateStore(dataDir).Read()
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := state.Managed["peer-key"]; exists {
		t.Fatalf("failed deployment retained peer association: %#v", state.Managed)
	}
	resolved, err := store.Get(context.Background(), "node-1", task.TaskID)
	if err != nil || resolved.Status != taskstate.Failed {
		t.Fatalf("Core task should be durably failed: status=%s err=%v", resolved.Status, err)
	}
}

func TestAgentCapabilityReportAllowsOnlyNegotiatedDowngrades(t *testing.T) {
	connection := &agentConnection{}
	connection.setCapabilities([]string{protocol.CapabilityMetrics, protocol.CapabilityProbes})
	if err := connection.applyCapabilityReport([]string{protocol.CapabilityMetrics}); err != nil {
		t.Fatalf("valid capability downgrade rejected: %v", err)
	}
	if connection.capabilityEnabled(protocol.CapabilityProbes) || !connection.capabilityEnabled(protocol.CapabilityMetrics) {
		t.Fatalf("live capability state did not apply the reported downgrade: %v", connection.activeCapabilityList())
	}

	if err := connection.applyCapabilityReport([]string{protocol.CapabilityMetrics, protocol.CapabilityDocker}); err == nil {
		t.Fatal("unnegotiated Docker capability was accepted")
	}
	if err := connection.applyCapabilityReport([]string{protocol.CapabilityMetrics, "agent.unknown.v1"}); err == nil {
		t.Fatal("unknown capability was accepted")
	}
	if err := connection.applyCapabilityReport([]string{protocol.CapabilityMetrics, protocol.CapabilityMetrics}); err == nil {
		t.Fatal("duplicate capability was accepted")
	}
	if err := connection.applyCapabilityReport([]string{protocol.CapabilityMetrics, protocol.CapabilityProbes}); err == nil {
		t.Fatal("dropped capability was re-enabled on the same connection")
	}
}

func TestHeartbeatPersistsLiveCapabilityDowngrade(t *testing.T) {
	ctx := context.Background()
	database, err := storage.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	now := time.Now().UTC()
	if _, err := database.DB.ExecContext(ctx, `INSERT INTO nodes(id, display_name, status, connection_generation, last_seen_at, created_at, updated_at)
		VALUES('node-capabilities', 'test', 'online', 7, ?, ?, ?)`, now.UnixNano(), now.UnixNano(), now.UnixNano()); err != nil {
		t.Fatal(err)
	}
	repository := agents.NewRepository(database.DB, func() time.Time { return now })
	identity := agents.Identity{AgentID: "agent-capabilities", NodeID: "node-capabilities"}
	if _, err := repository.AcceptHeartbeat(ctx, identity, 7, 1, 30*time.Second, []string{protocol.CapabilityMetrics}); err != nil {
		t.Fatalf("accept heartbeat with live capabilities: %v", err)
	}
	nodes, err := repository.ListNodes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 1 || len(nodes[0].Capabilities) != 1 || nodes[0].Capabilities[0] != protocol.CapabilityMetrics {
		t.Fatalf("live capability downgrade was not persisted: %#v", nodes)
	}
}

func TestCapabilityDowngradeDiscardsInFlightFramesAndKeepsAgentSocket(t *testing.T) {
	ctx := context.Background()
	core, err := New("test", Options{DataDir: t.TempDir(), Development: true, AgentOfflineTimeout: 15 * time.Second})
	if err != nil {
		t.Fatalf("start local Core: %v", err)
	}
	t.Cleanup(func() {
		if err := core.Close(); err != nil {
			t.Errorf("stop local Core: %v", err)
		}
	})
	coreHTTP := httptest.NewServer(core)
	t.Cleanup(coreHTTP.Close)

	caps := []string{protocol.CapabilityMetrics, protocol.CapabilityDocker}
	conn, identity, welcome := connectProtocolAgentForTest(t, core, coreHTTP.URL, caps)
	writeProtocolAgentEnvelope(t, conn, protocol.Envelope{Version: protocol.CurrentVersion, Type: protocol.TypeHeartbeat,
		Generation: welcome.Generation, Sequence: 1, Payload: marshalAgentPayload(protocol.Heartbeat{Capabilities: caps})})
	readProtocolAgentHeartbeatAck(t, conn, welcome.Generation, 1)

	now := time.Now().UTC()
	waitUntil := func(description string, condition func() bool) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			if condition() {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatalf("timed out waiting for %s", description)
	}
	containerID := strings.Repeat("a", 64)
	container := protocol.DockerContainer{ID: containerID, Name: "queued-test", Image: "busybox:latest", ImageID: "sha256:" + strings.Repeat("b", 64),
		State: "running", Running: true, Health: protocol.DockerHealthNone, Ports: []protocol.DockerPort{}, Networks: []protocol.DockerNetwork{},
		Mounts: []protocol.DockerMount{}, ObservedAt: now}
	dockerBatch := protocol.DockerBatch{Sequence: 1, SnapshotID: 1, FullSnapshot: true, SnapshotFinal: true,
		Changes: []protocol.DockerChange{{Sequence: 1, Action: protocol.DockerChangeUpsert, ContainerID: containerID, Container: &container, ObservedAt: now}},
		Health:  &protocol.DockerHealth{Sequence: 1, Availability: protocol.DockerAvailabilityAvailable, SnapshotFresh: true, ObservedAt: now}}
	dockerPayload, err := protocol.MarshalDockerBatch(dockerBatch)
	if err != nil {
		t.Fatalf("marshal Docker inventory: %v", err)
	}
	writeProtocolAgentEnvelope(t, conn, protocol.Envelope{Version: protocol.CurrentVersion, Type: protocol.TypeDocker,
		Generation: welcome.Generation, Sequence: 1, Payload: dockerPayload})
	waitUntil("Core to accept the Docker inventory", func() bool {
		core.dockerMu.Lock()
		defer core.dockerMu.Unlock()
		saved, ok := core.docker.PersistentNode(identity.NodeID)
		return ok && len(saved.Containers) == 1 && saved.Containers[0].Container.ID == containerID
	})

	metricsPayload, err := protocol.MarshalMetricsSnapshot(testMetricsSnapshot(now))
	if err != nil {
		t.Fatalf("marshal host metrics: %v", err)
	}
	writeProtocolAgentEnvelope(t, conn, protocol.Envelope{Version: protocol.CurrentVersion, Type: protocol.TypeMetrics,
		Generation: welcome.Generation, Sequence: 1, Payload: metricsPayload})
	waitUntil("Core to accept the first host metrics sequence", func() bool {
		lease := &coremetrics.Lease{Identity: coremetrics.Identity{AgentID: identity.AgentID, NodeID: identity.NodeID},
			Generation: welcome.Generation, ValidUntil: time.Now().Add(15 * time.Second), Status: coremetrics.LeaseOnline}
		view, ok := core.metrics.SnapshotAt(identity.NodeID, lease, time.Now())
		return ok && view.Sequence == 1
	})

	writeProtocolAgentEnvelope(t, conn, protocol.Envelope{Version: protocol.CurrentVersion, Type: protocol.TypeHeartbeat,
		Generation: welcome.Generation, Sequence: 2, Payload: marshalAgentPayload(protocol.Heartbeat{Capabilities: []string{}})})

	// These frames follow the control-lane downgrade heartbeat on the socket,
	// matching lower-priority frames that were queued before it was sent.
	lateDocker := protocol.DockerBatch{Sequence: 2, Health: &protocol.DockerHealth{Sequence: 2,
		Availability: protocol.DockerAvailabilityAvailable, SnapshotFresh: true, ObservedAt: now.Add(time.Second)}}
	lateDockerPayload, err := protocol.MarshalDockerBatch(lateDocker)
	if err != nil {
		t.Fatalf("marshal delayed Docker inventory: %v", err)
	}
	writeProtocolAgentEnvelope(t, conn, protocol.Envelope{Version: protocol.CurrentVersion, Type: protocol.TypeDocker,
		Generation: welcome.Generation, Sequence: 2, Payload: lateDockerPayload})
	writeProtocolAgentEnvelope(t, conn, protocol.Envelope{Version: protocol.CurrentVersion, Type: protocol.TypeMetrics,
		Generation: welcome.Generation, Sequence: 2, Payload: metricsPayload})
	writeProtocolAgentEnvelope(t, conn, protocol.Envelope{Version: protocol.CurrentVersion, Type: protocol.TypeHeartbeat,
		Generation: welcome.Generation, Sequence: 3, Payload: marshalAgentPayload(protocol.Heartbeat{Capabilities: []string{}})})
	readProtocolAgentHeartbeatAck(t, conn, welcome.Generation, 2)
	readProtocolAgentHeartbeatAck(t, conn, welcome.Generation, 3)

	core.dockerMu.Lock()
	saved, ok := core.docker.PersistentNode(identity.NodeID)
	core.dockerMu.Unlock()
	if !ok || saved.StaleReason != "docker_capability_unavailable" || len(saved.Containers) != 1 ||
		!saved.Containers[0].Container.Stale || saved.Containers[0].Container.UnavailableReason != "docker_capability_unavailable" {
		t.Fatalf("Docker inventory was not marked stale on capability downgrade: %#v", saved)
	}
	loadedDocker, err := loadDockerNodes(ctx, core.store.DB)
	if err != nil {
		t.Fatalf("reload persisted Docker state: %v", err)
	}
	persistedStale := false
	for _, node := range loadedDocker {
		if node.Identity.NodeID == identity.NodeID && node.StaleReason == "docker_capability_unavailable" && len(node.Containers) == 1 && node.Containers[0].Container.Stale {
			persistedStale = true
		}
	}
	if !persistedStale {
		t.Fatal("Docker capability downgrade was not persisted as stale inventory")
	}
	lease := &coremetrics.Lease{Identity: coremetrics.Identity{AgentID: identity.AgentID, NodeID: identity.NodeID},
		Generation: welcome.Generation, ValidUntil: time.Now().Add(15 * time.Second), Status: coremetrics.LeaseOnline}
	metricView, ok := core.metrics.SnapshotAt(identity.NodeID, lease, time.Now())
	if !ok || metricView.Sequence != 1 {
		t.Fatalf("delayed metrics changed the accepted host sequence: found=%v sequence=%d", ok, metricView.Sequence)
	}

	// The same Docker frame remains forbidden when the capability was never
	// negotiated in Hello.
	unnegotiatedConn, _, unnegotiatedWelcome := connectProtocolAgentForTest(t, core, coreHTTP.URL, []string{protocol.CapabilityMetrics})
	writeProtocolAgentEnvelope(t, unnegotiatedConn, protocol.Envelope{Version: protocol.CurrentVersion, Type: protocol.TypeDocker,
		Generation: unnegotiatedWelcome.Generation, Sequence: 1, Payload: lateDockerPayload})
	readCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	_, _, closeErr := unnegotiatedConn.Read(readCtx)
	if status := websocket.CloseStatus(closeErr); status != websocket.StatusPolicyViolation {
		t.Fatalf("unnegotiated Docker frame was not rejected with policy violation: err=%v status=%v", closeErr, status)
	}
}

func connectProtocolAgentForTest(t *testing.T, core *Server, coreURL string, capabilities []string) (*websocket.Conn, agents.Identity, protocol.Welcome) {
	t.Helper()
	ctx := context.Background()
	enrollment, err := core.agents.CreateEnrollment(ctx, "capability-order-test", "127.0.0.1", sql.NullInt64{})
	if err != nil {
		t.Fatal(err)
	}
	credential, err := agentruntime.NewCredential()
	if err != nil {
		t.Fatal(err)
	}
	requestID, err := agentruntime.NewRequestID()
	if err != nil {
		t.Fatal(err)
	}
	identity, err := core.agents.ConsumeEnrollment(ctx, auth.DigestToken(enrollment.Token), auth.DigestToken(credential), requestID, "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	endpoint := "ws" + strings.TrimPrefix(coreURL, "http") + "/ws/v1/agent"
	conn, _, err := websocket.Dial(ctx, endpoint, &websocket.DialOptions{HTTPHeader: http.Header{"Authorization": {"Bearer " + credential}}})
	if err != nil {
		t.Fatalf("connect Agent WebSocket: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close(websocket.StatusNormalClosure, "test complete") })
	permissions := protocol.RuntimePermissions{OS: "linux", Architecture: "amd64", EffectiveUID: 1000, EffectiveGID: 1000, SupplementaryGroups: []int{}}
	hello := protocol.Hello{AgentID: identity.AgentID, NodeID: identity.NodeID, AgentVersion: "test", Capabilities: capabilities, Permissions: permissions}
	helloPayload, err := json.Marshal(hello)
	if err != nil {
		t.Fatal(err)
	}
	writeProtocolAgentEnvelope(t, conn, protocol.Envelope{Version: protocol.CurrentVersion, Type: protocol.TypeHello, Payload: helloPayload})
	readCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	messageType, raw, err := conn.Read(readCtx)
	if err != nil || messageType != websocket.MessageText {
		t.Fatalf("read Agent welcome: type=%v err=%v", messageType, err)
	}
	var envelope protocol.Envelope
	if err := json.Unmarshal(raw, &envelope); err != nil || envelope.Type != protocol.TypeWelcome {
		t.Fatalf("invalid Agent welcome envelope: %s err=%v", raw, err)
	}
	var welcome protocol.Welcome
	if err := json.Unmarshal(envelope.Payload, &welcome); err != nil || welcome.Generation == 0 {
		t.Fatalf("invalid Agent welcome payload: %s err=%v", envelope.Payload, err)
	}
	return conn, agents.Identity{AgentID: identity.AgentID, NodeID: identity.NodeID}, welcome
}

func TestDockerRestoreFailurePreservesHistoryAndHostMonitoring(t *testing.T) {
	for _, mode := range []string{"load_decode", "single_node_restore"} {
		t.Run(mode, func(t *testing.T) {
			dataDir := t.TempDir()
			identity, credential, containerID := seedDockerRestoreFixture(t, dataDir)

			corruptStore, err := storage.Open(context.Background(), dataDir)
			if err != nil {
				t.Fatalf("reopen fixture database: %v", err)
			}
			var originalRecord string
			if err := corruptStore.DB.QueryRowContext(context.Background(), `SELECT record_json FROM docker_containers WHERE node_id=? AND container_id=?`, identity.NodeID, containerID).Scan(&originalRecord); err != nil {
				t.Fatalf("read seeded Docker history: %v", err)
			}
			switch mode {
			case "load_decode":
				if _, err := corruptStore.DB.ExecContext(context.Background(), `UPDATE docker_containers SET record_json='{' WHERE node_id=? AND container_id=?`, identity.NodeID, containerID); err != nil {
					t.Fatalf("corrupt Docker record JSON: %v", err)
				}
				originalRecord = "{"
			case "single_node_restore":
				if _, err := corruptStore.DB.ExecContext(context.Background(), `UPDATE docker_containers SET container_id=?, record_json=? WHERE node_id=? AND container_id=?`, " bad-id", `{"container":{"id":" bad-id"}}`, identity.NodeID, containerID); err != nil {
					t.Fatalf("make Docker record fail stale restore: %v", err)
				}
				containerID = " bad-id"
				originalRecord = `{"container":{"id":" bad-id"}}`
			}
			if err := corruptStore.Close(); err != nil {
				t.Fatalf("close corrupted fixture database: %v", err)
			}

			core, err := New("test", Options{DataDir: dataDir, Development: true, AgentOfflineTimeout: 15 * time.Second})
			if err != nil {
				t.Fatalf("start Core with failed Docker restore: %v", err)
			}
			coreClosed := false
			t.Cleanup(func() {
				if !coreClosed {
					if err := core.Close(); err != nil {
						t.Errorf("stop Core: %v", err)
					}
				}
			})
			coreHTTP := httptest.NewServer(core)
			t.Cleanup(coreHTTP.Close)
			health, err := http.Get(coreHTTP.URL + "/api/v1/health")
			if err != nil {
				t.Fatalf("Core health endpoint unavailable after Docker restore failure: %v", err)
			}
			_ = health.Body.Close()
			if health.StatusCode != http.StatusOK {
				t.Fatalf("Core health endpoint status after Docker restore failure: %d", health.StatusCode)
			}

			conn, welcome := connectPersistedProtocolAgentForTest(t, coreHTTP.URL, identity, credential,
				[]string{protocol.CapabilityMetrics, protocol.CapabilityDocker})
			defer conn.Close(websocket.StatusNormalClosure, "test complete")
			if containsCapability(welcome.Capabilities, protocol.CapabilityDocker) {
				t.Fatalf("Core negotiated Docker despite failed restore: %#v", welcome.Capabilities)
			}
			if !containsCapability(welcome.Capabilities, protocol.CapabilityMetrics) {
				t.Fatalf("Docker restore failure blocked host metrics: %#v", welcome.Capabilities)
			}
			metricsPayload, err := protocol.MarshalMetricsSnapshot(testMetricsSnapshot(time.Now().UTC()))
			if err != nil {
				t.Fatalf("marshal host metrics: %v", err)
			}
			writeProtocolAgentEnvelope(t, conn, protocol.Envelope{Version: protocol.CurrentVersion, Type: protocol.TypeMetrics,
				Generation: welcome.Generation, Sequence: 1, Payload: metricsPayload})
			writeProtocolAgentEnvelope(t, conn, protocol.Envelope{Version: protocol.CurrentVersion, Type: protocol.TypeHeartbeat,
				Generation: welcome.Generation, Sequence: 1, Payload: marshalAgentPayload(protocol.Heartbeat{Capabilities: []string{protocol.CapabilityMetrics}})})
			readProtocolAgentHeartbeatAck(t, conn, welcome.Generation, 1)
			waitForDockerRestoreTest(t, "Core to accept post-restore host metrics", func() bool {
				lease := &coremetrics.Lease{Identity: coremetrics.Identity{AgentID: identity.AgentID, NodeID: identity.NodeID},
					Generation: welcome.Generation, ValidUntil: time.Now().Add(15 * time.Second), Status: coremetrics.LeaseOnline}
				view, ok := core.metrics.SnapshotAt(identity.NodeID, lease, time.Now())
				return ok && view.Sequence == 1
			})
			_, dockerView, _, err := core.dockerViewStateForNode(context.Background(), identity.NodeID)
			if err != nil {
				t.Fatalf("read Docker status after restore failure: %v", err)
			}
			if dockerView.DockerAvailability != protocol.DockerAvailabilityUnavailable || !dockerView.DataStale {
				t.Fatalf("failed Docker restore was not shown as unavailable/stale: %#v", dockerView)
			}
			if mode == "load_decode" && dockerView.StaleReason != "docker_state_restore_failed" {
				t.Fatalf("wrong Docker store restore reason: %q", dockerView.StaleReason)
			}
			if mode == "single_node_restore" && dockerView.StaleReason != "docker_node_state_restore_failed" {
				t.Fatalf("wrong per-node Docker restore reason: %q", dockerView.StaleReason)
			}
			var afterRecord string
			var afterCount int
			if err := core.store.DB.QueryRowContext(context.Background(), `SELECT record_json FROM docker_containers WHERE node_id=? AND container_id=?`, identity.NodeID, containerID).Scan(&afterRecord); err != nil {
				t.Fatalf("read Docker history after Agent reconnect: %v", err)
			}
			if err := core.store.DB.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM docker_containers WHERE node_id=?`, identity.NodeID).Scan(&afterCount); err != nil {
				t.Fatalf("count Docker history after Agent reconnect: %v", err)
			}
			if afterRecord != originalRecord || afterCount != 1 {
				t.Fatalf("Docker restore failure overwrote history: record=%q count=%d want record=%q count=1", afterRecord, afterCount, originalRecord)
			}
			conn.Close(websocket.StatusNormalClosure, "test complete")
			coreHTTP.Close()
			if err := core.Close(); err != nil {
				t.Fatalf("stop Core: %v", err)
			}
			coreClosed = true
		})
	}
}

func seedDockerRestoreFixture(t *testing.T, dataDir string) (agents.Identity, string, string) {
	t.Helper()
	core, err := New("test", Options{DataDir: dataDir, Development: true, AgentOfflineTimeout: 15 * time.Second})
	if err != nil {
		t.Fatalf("start fixture Core: %v", err)
	}
	coreClosed := false
	t.Cleanup(func() {
		if !coreClosed {
			if err := core.Close(); err != nil {
				t.Errorf("stop fixture Core: %v", err)
			}
		}
	})
	coreHTTP := httptest.NewServer(core)
	t.Cleanup(coreHTTP.Close)
	enrollment, err := core.agents.CreateEnrollment(context.Background(), "docker-restore-test", "127.0.0.1", sql.NullInt64{})
	if err != nil {
		t.Fatalf("create fixture Agent enrollment: %v", err)
	}
	credential, err := agentruntime.NewCredential()
	if err != nil {
		t.Fatalf("create fixture Agent credential: %v", err)
	}
	requestID, err := agentruntime.NewRequestID()
	if err != nil {
		t.Fatalf("create fixture Agent request ID: %v", err)
	}
	identity, err := core.agents.ConsumeEnrollment(context.Background(), auth.DigestToken(enrollment.Token), auth.DigestToken(credential), requestID, "127.0.0.1")
	if err != nil {
		t.Fatalf("consume fixture Agent enrollment: %v", err)
	}
	conn, welcome := connectPersistedProtocolAgentForTest(t, coreHTTP.URL, identity, credential,
		[]string{protocol.CapabilityMetrics, protocol.CapabilityDocker})
	containerID := strings.Repeat("d", 64)
	now := time.Now().UTC()
	container := protocol.DockerContainer{ID: containerID, Name: "restore-test", Image: "busybox:latest", ImageID: "sha256:" + strings.Repeat("e", 64),
		State: "running", Running: true, Health: protocol.DockerHealthNone, Ports: []protocol.DockerPort{}, Networks: []protocol.DockerNetwork{},
		Mounts: []protocol.DockerMount{}, ObservedAt: now}
	batch := protocol.DockerBatch{Sequence: 1, SnapshotID: 1, FullSnapshot: true, SnapshotFinal: true,
		Changes: []protocol.DockerChange{{Sequence: 1, Action: protocol.DockerChangeUpsert, ContainerID: containerID, Container: &container, ObservedAt: now}},
		Health:  &protocol.DockerHealth{Sequence: 1, Availability: protocol.DockerAvailabilityAvailable, SnapshotFresh: true, ObservedAt: now}}
	payload, err := protocol.MarshalDockerBatch(batch)
	if err != nil {
		t.Fatalf("marshal fixture Docker inventory: %v", err)
	}
	writeProtocolAgentEnvelope(t, conn, protocol.Envelope{Version: protocol.CurrentVersion, Type: protocol.TypeDocker,
		Generation: welcome.Generation, Sequence: 1, Payload: payload})
	waitForDockerRestoreTest(t, "Docker fixture inventory to persist", func() bool {
		var count int
		return core.store.DB.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM docker_containers WHERE node_id=? AND container_id=?`, identity.NodeID, containerID).Scan(&count) == nil && count == 1
	})
	conn.Close(websocket.StatusNormalClosure, "fixture complete")
	coreHTTP.Close()
	if err := core.Close(); err != nil {
		t.Fatalf("stop fixture Core: %v", err)
	}
	coreClosed = true
	return identity, credential, containerID
}

func connectPersistedProtocolAgentForTest(t *testing.T, coreURL string, identity agents.Identity, credential string, capabilities []string) (*websocket.Conn, protocol.Welcome) {
	t.Helper()
	ctx := context.Background()
	endpoint := "ws" + strings.TrimPrefix(coreURL, "http") + "/ws/v1/agent"
	conn, _, err := websocket.Dial(ctx, endpoint, &websocket.DialOptions{HTTPHeader: http.Header{"Authorization": {"Bearer " + credential}}})
	if err != nil {
		t.Fatalf("connect persisted Agent WebSocket: %v", err)
	}
	permissions := protocol.RuntimePermissions{OS: "linux", Architecture: "amd64", EffectiveUID: 1000, EffectiveGID: 1000, SupplementaryGroups: []int{}}
	helloPayload, err := json.Marshal(protocol.Hello{AgentID: identity.AgentID, NodeID: identity.NodeID, AgentVersion: "test", Capabilities: capabilities, Permissions: permissions})
	if err != nil {
		t.Fatalf("marshal persisted Agent hello: %v", err)
	}
	writeProtocolAgentEnvelope(t, conn, protocol.Envelope{Version: protocol.CurrentVersion, Type: protocol.TypeHello, Payload: helloPayload})
	readCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	messageType, raw, err := conn.Read(readCtx)
	if err != nil || messageType != websocket.MessageText {
		conn.CloseNow()
		t.Fatalf("read persisted Agent welcome: type=%v err=%v", messageType, err)
	}
	var envelope protocol.Envelope
	if err := json.Unmarshal(raw, &envelope); err != nil || envelope.Type != protocol.TypeWelcome {
		conn.CloseNow()
		t.Fatalf("invalid persisted Agent welcome envelope: %s err=%v", raw, err)
	}
	var welcome protocol.Welcome
	if err := json.Unmarshal(envelope.Payload, &welcome); err != nil || welcome.Generation == 0 {
		conn.CloseNow()
		t.Fatalf("invalid persisted Agent welcome payload: %s err=%v", envelope.Payload, err)
	}
	return conn, welcome
}

func waitForDockerRestoreTest(t *testing.T, description string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", description)
}

func containsCapability(capabilities []string, wanted string) bool {
	for _, capability := range capabilities {
		if capability == wanted {
			return true
		}
	}
	return false
}

func writeProtocolAgentEnvelope(t *testing.T, conn *websocket.Conn, envelope protocol.Envelope) {
	t.Helper()
	data, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := conn.Write(ctx, websocket.MessageText, data); err != nil {
		t.Fatalf("write Agent envelope %s: %v", envelope.Type, err)
	}
}

func readProtocolAgentHeartbeatAck(t *testing.T, conn *websocket.Conn, generation, sequence uint64) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	messageType, raw, err := conn.Read(ctx)
	if err != nil || messageType != websocket.MessageText {
		t.Fatalf("read heartbeat acknowledgement: type=%v err=%v", messageType, err)
	}
	var envelope protocol.Envelope
	var acknowledgement protocol.HeartbeatAck
	if json.Unmarshal(raw, &envelope) != nil || envelope.Type != protocol.TypeHeartbeatAck || envelope.Generation != generation || envelope.Sequence != sequence ||
		json.Unmarshal(envelope.Payload, &acknowledgement) != nil || acknowledgement.AcceptedAt == 0 {
		t.Fatalf("invalid heartbeat acknowledgement: %s", raw)
	}
}

func testMetric[T any](sampledAt time.Time) protocol.Metric[T] {
	return protocol.Metric[T]{Status: protocol.MetricUnknown, Reason: "not_sampled", SampledAt: sampledAt}
}

func testMetricsSnapshot(sampledAt time.Time) protocol.MetricsSnapshot {
	return protocol.MetricsSnapshot{CollectedAt: sampledAt,
		System: protocol.MetricsSystem{Hostname: testMetric[string](sampledAt), OS: testMetric[string](sampledAt), Architecture: testMetric[string](sampledAt),
			Platform: testMetric[string](sampledAt), PlatformFamily: testMetric[string](sampledAt), PlatformVersion: testMetric[string](sampledAt), KernelVersion: testMetric[string](sampledAt)},
		CPU:    protocol.MetricsCPU{UsagePercent: testMetric[float64](sampledAt), LogicalCores: testMetric[int](sampledAt)},
		Memory: testMetric[protocol.Memory](sampledAt), Network: protocol.MetricsNetwork{Summary: testMetric[protocol.NetworkRate](sampledAt), Interfaces: []protocol.MetricsInterface{}},
		Disk: protocol.MetricsDisk{Status: protocol.MetricUnknown, Reason: "not_sampled", SampledAt: sampledAt, Mounts: []protocol.MetricsMount{}}, Uptime: testMetric[protocol.Uptime](sampledAt)}
}

func TestLocalAgentAndCoreKeepMonitoringAfterProbeWorkerFailure(t *testing.T) {
	ctx := context.Background()
	core, err := New("test", Options{DataDir: t.TempDir(), Development: true, AgentOfflineTimeout: 15 * time.Second})
	if err != nil {
		t.Fatalf("start local Core: %v", err)
	}
	t.Cleanup(func() {
		if err := core.Close(); err != nil {
			t.Errorf("stop local Core: %v", err)
		}
	})
	coreHTTP := httptest.NewServer(core)
	t.Cleanup(coreHTTP.Close)
	enrollment, err := core.agents.CreateEnrollment(ctx, "local-runtime-test", "127.0.0.1", sql.NullInt64{})
	if err != nil {
		t.Fatal(err)
	}
	credential, err := agentruntime.NewCredential()
	if err != nil {
		t.Fatal(err)
	}
	requestID, err := agentruntime.NewRequestID()
	if err != nil {
		t.Fatal(err)
	}
	identity, err := core.agents.ConsumeEnrollment(ctx, auth.DigestToken(enrollment.Token), auth.DigestToken(credential), requestID, "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(t.TempDir(), "agent", "agent.json")
	if err := agentruntime.SaveConfig(configPath, agentruntime.Config{Schema: agentruntime.ConfigSchema, Server: coreHTTP.URL,
		Development: true, Credential: credential, NodeID: identity.NodeID, AgentID: identity.AgentID, CreatedAt: time.Now()}, true); err != nil {
		t.Fatalf("write local Agent config: %v", err)
	}
	// SDK initialization succeeds for a Unix socket URL even though no daemon
	// listens there. This exercises Engine-unreachable capability degradation.
	t.Setenv("DOCKER_HOST", "unix:///tmp/nodedance-nonexistent-docker.sock")
	agentCtx, cancelAgent := context.WithCancel(ctx)
	agentDone := make(chan error, 1)
	go func() { agentDone <- agentruntime.Run(agentCtx, configPath, "test", io.Discard) }()
	t.Cleanup(func() {
		cancelAgent()
		select {
		case err := <-agentDone:
			if err != nil {
				t.Errorf("stop local Agent: %v", err)
			}
		case <-time.After(10 * time.Second):
			t.Error("local Agent did not stop after cancellation")
		}
	})

	deadline := time.NewTimer(20 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	var connection *agentConnection
	probeDispatchQueued := false
	for {
		select {
		case err := <-agentDone:
			if err != nil {
				t.Fatalf("local Agent stopped before verification: %v", err)
			}
			t.Fatal("local Agent disconnected while verification was active")
		case <-deadline.C:
			t.Fatal("local Core did not retain the Agent heartbeat and host metrics after probe failure")
		case <-ticker.C:
			nodes, err := core.agents.ListNodes(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if len(nodes) != 1 || nodes[0].Status != "online" {
				continue
			}
			node := nodes[0]
			hasCapability := func(capability string) bool {
				for _, active := range node.Capabilities {
					if active == capability {
						return true
					}
				}
				return false
			}
			core.agentConnectionsMu.Lock()
			currentConnection := core.agentConnections[identity.AgentID]
			core.agentConnectionsMu.Unlock()
			if currentConnection == nil {
				continue
			}
			if connection == nil {
				if !hasCapability(protocol.CapabilityProbes) || !hasCapability(protocol.CapabilityMetrics) {
					continue
				}
				connection = currentConnection
				if !connection.capabilityNegotiated(protocol.CapabilityDocker) {
					t.Fatal("Agent did not negotiate Docker after SDK client initialization succeeded")
				}
				if !connection.tryEnqueue(protocol.Envelope{Version: protocol.CurrentVersion, Type: protocol.TypeProbeDispatch,
					Generation: connection.generation, RequestID: "invalid-probe", Payload: json.RawMessage(`{}`)}) {
					t.Fatal("could not send the invalid probe request to the local Agent")
				}
				probeDispatchQueued = true
				continue
			}
			if currentConnection != connection || !probeDispatchQueued {
				t.Fatal("Agent reconnected instead of isolating the probe worker failure")
			}
			if hasCapability(protocol.CapabilityProbes) || !hasCapability(protocol.CapabilityMetrics) || hasCapability(protocol.CapabilityDocker) {
				continue
			}
			var heartbeatSequence uint64
			if err := core.store.DB.QueryRowContext(ctx, `SELECT heartbeat_sequence FROM nodes WHERE id=?`, node.NodeID).Scan(&heartbeatSequence); err != nil {
				t.Fatal(err)
			}
			lease := &coremetrics.Lease{Identity: coremetrics.Identity{AgentID: identity.AgentID, NodeID: identity.NodeID},
				Generation: node.ConnectionGeneration, ValidUntil: time.Now().Add(15 * time.Second), Status: coremetrics.LeaseOnline}
			metrics, hasMetrics := core.metrics.SnapshotAt(node.NodeID, lease, time.Now())
			if heartbeatSequence >= 3 && hasMetrics && metrics.Sequence >= 2 {
				return
			}
		}
	}
}

func TestTailscaleDeploymentListRestoresUnknownTaskAfterRefresh(t *testing.T) {
	dataDir := t.TempDir()
	_, store, task := newUnknownDeploymentForTest(t, dataDir)
	request := httptest.NewRequest("GET", "/api/v1/discovery/deployments", nil)
	request.RemoteAddr = "192.0.2.10:1234"
	response := httptest.NewRecorder()
	if !(&Server{tasks: store, dataDir: dataDir}).handleTailscaleAPI(response, request, &session{}) {
		t.Fatal("deployment collection route was not handled")
	}
	if response.Code != 200 {
		t.Fatalf("list response: status=%d body=%s", response.Code, response.Body.String())
	}
	var result struct {
		Tasks []tailscale.DeploymentTask `json:"tasks"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Tasks) != 1 || result.Tasks[0].ID != task.TaskID || result.Tasks[0].Status != string(taskstate.Unknown) {
		t.Fatalf("refresh did not restore the unresolved Core task: %#v", result.Tasks)
	}
}

func TestResolveUnknownDeploymentDoesNotSucceedWhenAssociationCannotPersist(t *testing.T) {
	dataDir := t.TempDir()
	_, store, task := newUnknownDeploymentForTest(t, dataDir)
	if err := os.Mkdir(filepath.Join(dataDir, "tailscale-deployments.json"), 0o700); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest("POST", "/api/v1/discovery/deployments/"+task.TaskID+"/resolve", strings.NewReader(`{"outcome":"succeeded","observedState":"connected","confirmed":true}`))
	request.RemoteAddr = "192.0.2.10:1234"
	response := httptest.NewRecorder()
	(&Server{tasks: store, dataDir: dataDir}).handleResolveTailscaleDeployment(response, request, &session{}, task.TaskID)
	if response.Code != 500 {
		t.Fatalf("association failure should not be reported as success: status=%d body=%s", response.Code, response.Body.String())
	}
	current, err := store.Get(context.Background(), "node-1", task.TaskID)
	if err != nil || current.Status != taskstate.Unknown {
		t.Fatalf("failed peer association must leave task unresolved and claimed: status=%s err=%v", current.Status, err)
	}
}
