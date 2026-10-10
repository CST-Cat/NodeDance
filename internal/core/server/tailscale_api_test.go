package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
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
	t.Setenv("DOCKER_HOST", "tcp://127.0.0.1:2375")
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
				if hasCapability(protocol.CapabilityDocker) {
					t.Fatal("Agent claimed Docker support even though DOCKER_HOST could not initialize an SDK engine")
				}
				connection = currentConnection
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
