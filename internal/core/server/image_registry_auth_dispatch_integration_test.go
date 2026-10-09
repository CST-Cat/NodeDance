package server

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/CST-Cat/NodeDance/internal/core/agents"
	coredocker "github.com/CST-Cat/NodeDance/internal/core/docker"
	coretasks "github.com/CST-Cat/NodeDance/internal/core/tasks"
	"github.com/CST-Cat/NodeDance/internal/protocol"
	"github.com/CST-Cat/NodeDance/internal/taskstate"
	"github.com/coder/websocket"
)

func TestAuthenticatedImagePullFailsClosedAfterCoreRestart(t *testing.T) {
	ctx := context.Background()
	dataDir := filepath.Join(t.TempDir(), "core")
	const nodeID = "00000000-0000-4000-8000-000000000071"
	const agentID = "00000000-0000-4000-8000-000000000072"
	const generation = uint64(7)
	const secret = "sentinel-registry-secret-7f198a"
	const liveSecret = "one-use-live-registry-secret-24ea1d"
	var logs bytes.Buffer
	previousLogOutput := log.Writer()
	log.SetOutput(&logs)
	defer log.SetOutput(previousLogOutput)

	core, err := New("s08-image-auth-restart-test", Options{DataDir: dataDir, Development: true, PublicOrigin: "https://panel.test"})
	if err != nil {
		t.Fatal("create first Core:", err)
	}
	nowNS := time.Now().UTC().UnixNano()
	if _, err := core.store.DB.Exec(`INSERT INTO nodes(id,display_name,status,connection_generation,last_seen_at,created_at,updated_at)
		VALUES(?, 'Image-auth fixture', 'online', ?, ?, ?, ?)`, nodeID, generation, nowNS, nowNS, nowNS); err != nil {
		_ = core.Close()
		t.Fatal("create image-auth node fixture:", err)
	}
	if _, err := core.store.DB.Exec(`INSERT INTO agent_devices(id,node_id,credential_digest,created_at) VALUES(?,?,?,?)`,
		agentID, nodeID, bytes.Repeat([]byte{0x2a}, 32), nowNS); err != nil {
		_ = core.Close()
		t.Fatal("create image-auth Agent fixture:", err)
	}
	privateIntent := protocol.TaskIntent{Action: protocol.TaskImagePull, ImageReference: "registry.example/private:v1"}
	privateIntent.ContainerID = protocol.ImageTargetKey("pull:" + privateIntent.ImageReference)
	privatePull, err := core.tasks.Enqueue(ctx, coretasks.EnqueueRequest{NodeID: nodeID, IdempotencyKey: "restart-private-auth-pull",
		Intent: privateIntent, RegistryAuthRequired: true, ActorID: sql.NullInt64{Int64: 1, Valid: true}, RemoteAddr: "127.0.0.1"})
	if err != nil {
		_ = core.Close()
		t.Fatal("persist authenticated image pull:", err)
	}
	core.storeImageCredentials(privatePull.Task.TaskID, nodeID, protocol.RegistryCredentials{Username: "test-user", Password: secret})
	core.imageAuthMu.Lock()
	shutdownCredential := core.imageAuth[privatePull.Task.TaskID]
	core.imageAuthMu.Unlock()
	publicIntent := protocol.TaskIntent{Action: protocol.TaskImagePull, ImageReference: "registry.example/public:v1"}
	publicIntent.ContainerID = protocol.ImageTargetKey("pull:" + publicIntent.ImageReference)
	publicPull, err := core.tasks.Enqueue(ctx, coretasks.EnqueueRequest{NodeID: nodeID, IdempotencyKey: "restart-public-no-auth-pull", Intent: publicIntent})
	if err != nil {
		_ = core.Close()
		t.Fatal("persist unauthenticated image pull:", err)
	}
	if err := core.Close(); err != nil {
		t.Fatal("close first Core:", err)
	}
	if len(core.imageAuth) != 0 || !allBytesZero(shutdownCredential.username) || !allBytesZero(shutdownCredential.password) {
		t.Fatal("Core shutdown retained staged Registry credentials in memory")
	}

	// A new Server instance has an empty in-memory one-use credential map while
	// retaining the queued task's durable auth-required bit.
	core, err = New("s08-image-auth-restart-test", Options{DataDir: dataDir, Development: true, PublicOrigin: "https://panel.test"})
	if err != nil {
		t.Fatal("reopen Core after restart:", err)
	}
	coreClosed := false
	defer func() {
		if !coreClosed {
			if err := core.Close(); err != nil {
				t.Errorf("close reopened Core: %v", err)
			}
		}
	}()
	nowNS = time.Now().UTC().UnixNano()
	// Keep the task's accepted generation unchanged so this test reaches the
	// same-generation dispatch guard. Stale-generation cancellation is covered
	// independently by TestUndeliveredIntentIsCanceledWhenAgentConnectionGenerationChanges.
	if _, err := core.store.DB.Exec(`UPDATE nodes SET status='online',connection_generation=?,last_seen_at=?,updated_at=? WHERE id=?`, generation, nowNS, nowNS, nodeID); err != nil {
		t.Fatal("restore online node fixture after restart:", err)
	}
	journalID := strings.Repeat("7", 64)
	journal := coretasks.AgentConnection{NodeID: nodeID, ConnectionGeneration: generation, JournalID: journalID}
	if _, err := core.tasks.ObserveAgentConnection(ctx, journal); err != nil {
		t.Fatal("observe current Agent journal after restart:", err)
	}
	storedPrivate, err := core.tasks.Get(ctx, nodeID, privatePull.Task.TaskID)
	if err != nil || !storedPrivate.RegistryAuthRequired {
		t.Fatalf("reopened private pull lost auth-required intent: task=%+v err=%v", storedPrivate, err)
	}
	acceptedConn := make(chan *websocket.Conn, 1)
	wsServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{})
		if err != nil {
			return
		}
		acceptedConn <- conn
	}))
	defer wsServer.Close()
	clientConn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(wsServer.URL, "http"), nil)
	if err != nil {
		t.Fatal("open Agent dispatch capture WebSocket:", err)
	}
	defer clientConn.CloseNow()
	serverConn := <-acceptedConn
	defer serverConn.CloseNow()
	connectionCtx, cancelConnection := context.WithCancel(ctx)
	defer cancelConnection()
	connection := &agentConnection{conn: serverConn, ctx: connectionCtx, nodeID: nodeID, generation: generation,
		taskEnabled: true, taskCapacity: 3, taskSlots: 3, taskSignal: make(chan struct{}, 1), taskOutstanding: make(map[string]struct{})}
	connection.setTaskJournal(journalID, true)
	dockerIdentity := coredocker.Identity{AgentID: agentID, NodeID: nodeID}
	if err := core.bindDockerConnection(ctx, dockerIdentity, generation); err != nil {
		t.Fatal("bind the current Docker inventory fixture:", err)
	}
	if err := core.acceptDockerFrame(ctx, dockerIdentity, generation, dockerTestFrame(1, 1, true, nil, true)); err != nil {
		t.Fatal("commit the current Docker inventory fixture:", err)
	}
	core.agentConnectionsMu.Lock()
	core.agentConnections[agentID] = connection
	core.agentConnectionsMu.Unlock()

	session, csrf, err := installIntegrationAdmin(core)
	if err != nil {
		t.Fatal("install authenticated image API session:", err)
	}
	apiPayload, err := json.Marshal(imagePullRequest{ImageReference: "registry.example/live-auth:v1", Username: "live-user", Password: liveSecret})
	if err != nil {
		t.Fatal("encode authenticated image pull request:", err)
	}
	apiRequest := httptest.NewRequest(http.MethodPost, "/api/v1/nodes/"+nodeID+"/images/pull", bytes.NewReader(apiPayload))
	apiRequest.Header.Set("Content-Type", "application/json")
	apiRequest.Header.Set("Origin", "https://panel.test")
	apiRequest.Header.Set(csrfHeaderName, csrf)
	apiRequest.Header.Set("Cookie", sessionCookieName+"="+session+"; "+csrfCookieName+"="+csrf)
	apiRequest.Header.Set("Idempotency-Key", "restart-live-auth-pull")
	apiResponse := httptest.NewRecorder()
	core.ServeHTTP(apiResponse, apiRequest)
	clear(apiPayload)
	if apiResponse.Code != http.StatusAccepted {
		t.Fatalf("authenticated image pull API returned %d, want %d", apiResponse.Code, http.StatusAccepted)
	}
	var accepted struct {
		TaskID string           `json:"taskId"`
		Status taskstate.Status `json:"status"`
	}
	if err := json.Unmarshal(apiResponse.Body.Bytes(), &accepted); err != nil || accepted.TaskID == "" || accepted.Status != taskstate.Queued {
		t.Fatalf("authenticated image pull API returned an invalid task acknowledgement: task=%+v err=%v", accepted, err)
	}
	if bytes.Contains(apiResponse.Body.Bytes(), []byte("live-user")) || bytes.Contains(apiResponse.Body.Bytes(), []byte(liveSecret)) {
		t.Fatal("authenticated image pull API response exposed Registry credentials")
	}
	core.imageAuthMu.Lock()
	pending, staged := core.imageAuth[accepted.TaskID]
	core.imageAuthMu.Unlock()
	if !staged || pending.nodeID != nodeID || string(pending.username) != "live-user" || string(pending.password) != liveSecret {
		t.Fatal("accepted Core image pull did not stage its credential only for the matching task and node")
	}
	liveTaskID := accepted.TaskID
	if err := core.dispatchAgentTasks(ctx, connection, agents.Identity{NodeID: nodeID}); err != nil {
		t.Fatal("dispatch queued image pulls after restart:", err)
	}

	for i, want := range []struct {
		taskID   string
		username string
		password string
	}{{taskID: publicPull.Task.TaskID}, {taskID: liveTaskID, username: "live-user", password: liveSecret}} {
		readCtx, cancelRead := context.WithTimeout(ctx, 2*time.Second)
		_, frame, err := clientConn.Read(readCtx)
		cancelRead()
		if err != nil {
			t.Fatalf("read Agent image-pull dispatch %d: %v", i, err)
		}
		var envelope protocol.Envelope
		if err := json.Unmarshal(frame, &envelope); err != nil {
			clear(envelope.Payload)
			clear(frame)
			t.Fatal("decode Agent task envelope:", err)
		}
		var dispatch protocol.TaskDispatch
		if err := json.Unmarshal(envelope.Payload, &dispatch); err != nil {
			if dispatch.RegistryAuth != nil {
				dispatch.RegistryAuth.Username, dispatch.RegistryAuth.Password = "", ""
			}
			clear(envelope.Payload)
			clear(frame)
			t.Fatal("decode Agent task dispatch:", err)
		}
		clearDispatch := func() {
			if dispatch.RegistryAuth != nil {
				dispatch.RegistryAuth.Username, dispatch.RegistryAuth.Password = "", ""
				dispatch.RegistryAuth = nil
			}
			clear(envelope.Payload)
			clear(frame)
		}
		if envelope.Type != protocol.TypeTaskDispatch || dispatch.TaskID != want.taskID {
			authPresent := dispatch.RegistryAuth != nil
			clearDispatch()
			t.Fatalf("Agent received unexpected image dispatch %d after restart: type=%q task_id=%q auth_present=%t",
				i, envelope.Type, dispatch.TaskID, authPresent)
		}
		if want.password == "" {
			if dispatch.RegistryAuth != nil {
				clearDispatch()
				t.Fatal("unauthenticated public pull unexpectedly included Registry credentials")
			}
		} else if dispatch.RegistryAuth == nil || dispatch.RegistryAuth.Username != want.username || dispatch.RegistryAuth.Password != want.password {
			clearDispatch()
			t.Fatal("authenticated pull did not receive its one-use credentials")
		}
		clearDispatch()
	}
	if err := core.dispatchAgentTasks(ctx, connection, agents.Identity{NodeID: nodeID}); err != nil {
		t.Fatal("repeat ready-task poll after consuming one-use image credentials:", err)
	}

	noSecondFrame, cancelSecondRead := context.WithTimeout(ctx, 100*time.Millisecond)
	_, _, err = clientConn.Read(noSecondFrame)
	cancelSecondRead()
	if err == nil {
		t.Fatal("Agent received a duplicate frame after consuming the one-use authenticated image pull")
	}
	core.imageAuthMu.Lock()
	remainingCredentials := len(core.imageAuth)
	core.imageAuthMu.Unlock()
	if remainingCredentials != 0 {
		t.Fatalf("Core retained %d credentials after one-use Agent delivery", remainingCredentials)
	}
	failedPrivate, err := core.tasks.Get(ctx, nodeID, privatePull.Task.TaskID)
	if err != nil {
		t.Fatal("read resolved private image pull:", err)
	}
	if failedPrivate.Status != taskstate.Failed || failedPrivate.DeliveryState != "done" || failedPrivate.Evidence.DeliveryCommitted ||
		failedPrivate.Result.Code != coretasks.ResultRegistryCredentialsUnavailable {
		t.Fatalf("private pull was not resolved as confirmed credential-unavailable before delivery: %+v", failedPrivate)
	}
	var claims int
	if err := core.store.DB.QueryRow(`SELECT count(*) FROM core_task_resource_claims WHERE task_id=?`, privatePull.Task.TaskID).Scan(&claims); err != nil || claims != 0 {
		t.Fatalf("private pull resource claims=%d err=%v; want released", claims, err)
	}
	publicTask, err := core.tasks.Get(ctx, nodeID, publicPull.Task.TaskID)
	if err != nil || publicTask.Status != taskstate.Queued || publicTask.DeliveryState != "sent" || !publicTask.Evidence.DeliveryCommitted {
		t.Fatalf("unauthenticated public pull was not durably dispatched: task=%+v err=%v", publicTask, err)
	}
	liveTask, err := core.tasks.Get(ctx, nodeID, liveTaskID)
	if err != nil || liveTask.Status != taskstate.Queued || liveTask.DeliveryState != "sent" || !liveTask.Evidence.DeliveryCommitted {
		t.Fatalf("authenticated pull with credentials was not durably dispatched: task=%+v err=%v", liveTask, err)
	}

	audit, err := core.tasks.AuditEvents(ctx, nodeID, privatePull.Task.TaskID)
	if err != nil {
		t.Fatal("read private-pull audit:", err)
	}
	viewJSON, err := json.Marshal(toTaskView(failedPrivate))
	if err != nil {
		t.Fatal("marshal task result view:", err)
	}
	auditJSON, err := json.Marshal(audit)
	if err != nil {
		t.Fatal("marshal audit view:", err)
	}
	liveViewJSON, err := json.Marshal(toTaskView(liveTask))
	if err != nil {
		t.Fatal("marshal live image task view:", err)
	}
	liveAudit, err := core.tasks.AuditEvents(ctx, nodeID, liveTaskID)
	if err != nil {
		t.Fatal("read live-pull audit:", err)
	}
	liveAuditJSON, err := json.Marshal(liveAudit)
	if err != nil {
		t.Fatal("marshal live-pull audit view:", err)
	}
	apiResponseJSON := apiResponse.Body.Bytes()
	for _, candidate := range []string{"test-user", secret, "live-user", liveSecret} {
		if bytes.Contains(apiResponseJSON, []byte(candidate)) || bytes.Contains(viewJSON, []byte(candidate)) ||
			bytes.Contains(auditJSON, []byte(candidate)) || bytes.Contains(liveViewJSON, []byte(candidate)) ||
			bytes.Contains(liveAuditJSON, []byte(candidate)) || bytes.Contains(logs.Bytes(), []byte(candidate)) {
			t.Fatal("Registry secret escaped into task result, audit, or application logs")
		}
	}
	if err := core.Close(); err != nil {
		t.Fatal("close Core before checking database bytes:", err)
	}
	coreClosed = true
	for _, name := range []string{"nodedance.sqlite", "nodedance.sqlite-wal", "nodedance.sqlite-shm"} {
		contents, err := os.ReadFile(filepath.Join(dataDir, name))
		if err != nil {
			if os.IsNotExist(err) && (name == "nodedance.sqlite-wal" || name == "nodedance.sqlite-shm") {
				continue
			}
			t.Fatalf("read persisted %s: %v", name, err)
		}
		for _, candidate := range []string{"test-user", secret, "live-user", liveSecret} {
			if bytes.Contains(contents, []byte(candidate)) {
				t.Fatalf("Registry secret was persisted in %s", name)
			}
		}
	}
}
