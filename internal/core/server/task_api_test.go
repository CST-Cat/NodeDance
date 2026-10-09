package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	coretasks "github.com/CST-Cat/NodeDance/internal/core/tasks"
	"github.com/CST-Cat/NodeDance/internal/protocol"
	"github.com/CST-Cat/NodeDance/internal/taskstate"
)

func TestTaskDeleteRequiresExactFullTargetConfirmationBeforeQueue(t *testing.T) {
	core, err := New("task-delete-confirmation-test", Options{
		DataDir: filepath.Join(t.TempDir(), "core"), Development: true, PublicOrigin: "https://panel.test",
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = core.Close() })
	session, csrf, err := installIntegrationAdmin(core)
	if err != nil {
		t.Fatal(err)
	}
	const nodeID = "00000000-0000-4000-8000-000000000001"
	const containerID = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	endpoint := "/api/v1/nodes/" + nodeID + "/containers/" + containerID + "/actions"
	clientRequest := func(body string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(http.MethodPost, endpoint, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Origin", "https://panel.test")
		r.Header.Set(csrfHeaderName, csrf)
		r.Header.Set("Idempotency-Key", "delete-confirmation-test-key")
		r.AddCookie(&http.Cookie{Name: sessionCookieName, Value: session})
		r.AddCookie(&http.Cookie{Name: csrfCookieName, Value: csrf})
		w := httptest.NewRecorder()
		core.ServeHTTP(w, r)
		return w
	}
	for _, body := range []string{
		`{"action":"delete"}`,
		`{"action":"delete","deleteConfirmed":true,"deleteConfirmationId":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}`,
		`{"action":"delete","deleteConfirmed":false,"deleteConfirmationId":"` + containerID + `"}`,
	} {
		if response := clientRequest(body); response.Code != http.StatusBadRequest {
			t.Errorf("unsafe delete confirmation body returned %d, want 400: %s", response.Code, response.Body.String())
		}
	}
	if response := clientRequest(`{"action":"delete","deleteConfirmed":true,"deleteConfirmationId":"` + containerID + `"}`); response.Code != http.StatusServiceUnavailable {
		t.Errorf("matching explicit target confirmation returned %d, want later node-readiness rejection: %s", response.Code, response.Body.String())
	}
	var rows int
	if err := core.store.DB.QueryRow(`SELECT count(*) FROM core_tasks`).Scan(&rows); err != nil {
		t.Fatal("count durable task rows:", err)
	}
	if rows != 0 {
		t.Fatalf("invalid or unready deletion requests persisted %d task rows", rows)
	}
}

func TestTaskAPIRequiresSessionOriginCSRFAndHonorsRevocation(t *testing.T) {
	core, err := New("task-api-test", Options{
		DataDir: filepath.Join(t.TempDir(), "core"), Development: true, PublicOrigin: "https://panel.test",
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = core.Close() })
	session, csrf, err := installIntegrationAdmin(core)
	if err != nil {
		t.Fatal(err)
	}
	const nodeID = "00000000-0000-4000-8000-000000000001"
	const taskID = "task-api-fixture"
	const containerID = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

	request := func(method, path string, authenticated, withOrigin, withCSRF bool) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, path, nil)
		if authenticated {
			r.AddCookie(&http.Cookie{Name: sessionCookieName, Value: session})
			if withCSRF {
				r.AddCookie(&http.Cookie{Name: csrfCookieName, Value: csrf})
				r.Header.Set(csrfHeaderName, csrf)
			}
		}
		if withOrigin {
			r.Header.Set("Origin", "https://panel.test")
		}
		w := httptest.NewRecorder()
		core.ServeHTTP(w, r)
		return w
	}

	for _, path := range []string{
		"/api/v1/nodes/" + nodeID + "/tasks",
		"/api/v1/nodes/" + nodeID + "/tasks/" + taskID,
		"/api/v1/nodes/" + nodeID + "/tasks/" + taskID + "/audit",
		"/api/v1/nodes/" + nodeID + "/containers/" + containerID + "/actions",
	} {
		if response := request(http.MethodGet, path, false, false, false); response.Code != http.StatusUnauthorized {
			t.Errorf("unauthenticated GET %s returned %d, want 401", path, response.Code)
		}
	}
	if response := request(http.MethodPost, "/api/v1/nodes/"+nodeID+"/containers/"+containerID+"/actions", false, false, false); response.Code != http.StatusUnauthorized {
		t.Errorf("unauthenticated action returned %d, want 401", response.Code)
	}
	if response := request(http.MethodDelete, "/api/v1/nodes/"+nodeID+"/tasks/"+taskID, false, false, false); response.Code != http.StatusUnauthorized {
		t.Errorf("unauthenticated task cancellation returned %d, want 401", response.Code)
	}
	if response := request(http.MethodPost, "/api/v1/nodes/"+nodeID+"/containers/"+containerID+"/actions", true, false, true); response.Code != http.StatusForbidden {
		t.Errorf("authenticated action without a trusted Origin returned %d, want 403", response.Code)
	}
	if response := request(http.MethodPost, "/api/v1/nodes/"+nodeID+"/containers/"+containerID+"/actions", true, true, false); response.Code != http.StatusForbidden {
		t.Errorf("authenticated action without a CSRF token returned %d, want 403", response.Code)
	}
	if response := request(http.MethodDelete, "/api/v1/nodes/"+nodeID+"/tasks/"+taskID, true, false, true); response.Code != http.StatusForbidden {
		t.Errorf("authenticated task cancellation without a trusted Origin returned %d, want 403", response.Code)
	}
	if response := request(http.MethodDelete, "/api/v1/nodes/"+nodeID+"/tasks/"+taskID, true, true, false); response.Code != http.StatusForbidden {
		t.Errorf("authenticated task cancellation without CSRF returned %d, want 403", response.Code)
	}
	if response := request(http.MethodGet, "/api/v1/nodes/"+nodeID+"/tasks", true, false, false); response.Code != http.StatusOK {
		t.Errorf("authenticated task list returned %d, want 200", response.Code)
	}
	if response := request(http.MethodGet, "/api/v1/nodes/"+nodeID+"/tasks/"+taskID+"/audit", true, false, false); response.Code != http.StatusNotFound {
		t.Errorf("audit lookup for nonexistent task returned %d, want 404", response.Code)
	}
	if response := request(http.MethodDelete, "/api/v1/nodes/"+nodeID+"/tasks/"+taskID, true, true, true); response.Code != http.StatusNotFound {
		t.Errorf("cancellation of nonexistent task returned %d, want 404", response.Code)
	}
	if _, err := core.store.DB.Exec(`DELETE FROM browser_sessions WHERE id=?`, "0123456789abcdef0123456789abcdef"); err != nil {
		t.Fatal("revoke test Session:", err)
	}
	if response := request(http.MethodGet, "/api/v1/nodes/"+nodeID+"/tasks", true, false, false); response.Code != http.StatusUnauthorized {
		t.Errorf("revoked Session still accessed task API: status=%d", response.Code)
	}
}

func TestTaskAPIOnlyCancelsDurablyUndeliveredTasks(t *testing.T) {
	core, err := New("task-api-cancel-test", Options{
		DataDir: filepath.Join(t.TempDir(), "core"), Development: true, PublicOrigin: "https://panel.test",
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = core.Close() })
	session, csrf, err := installIntegrationAdmin(core)
	if err != nil {
		t.Fatal(err)
	}
	const nodeID = "00000000-0000-4000-8000-000000000001"
	const targetID = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	now := time.Now().UnixNano()
	if _, err := core.store.DB.Exec(`INSERT INTO nodes(id,display_name,status,connection_generation,last_seen_at,created_at,updated_at)
		VALUES(?, 'Cancellation fixture', 'online', 7, ?, ?, ?)`, nodeID, now, now, now); err != nil {
		t.Fatal("create task API node fixture:", err)
	}
	accepted, err := core.tasks.Enqueue(context.Background(), coretasks.EnqueueRequest{NodeID: nodeID, IdempotencyKey: "cancel-undelivered-key",
		Intent: protocol.TaskIntent{Action: protocol.TaskRestart, ContainerID: targetID}, ActorID: sql.NullInt64{Int64: 1, Valid: true}, RemoteAddr: "127.0.0.1"})
	if err != nil {
		t.Fatal("persist safe cancellation fixture:", err)
	}
	endpoint := "/api/v1/nodes/" + nodeID + "/tasks/" + accepted.Task.TaskID
	cancel := func() *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodDelete, endpoint, nil)
		r.Header.Set("Origin", "https://panel.test")
		r.Header.Set(csrfHeaderName, csrf)
		r.AddCookie(&http.Cookie{Name: sessionCookieName, Value: session})
		r.AddCookie(&http.Cookie{Name: csrfCookieName, Value: csrf})
		w := httptest.NewRecorder()
		core.ServeHTTP(w, r)
		return w
	}
	response := cancel()
	if response.Code != http.StatusOK {
		t.Fatalf("undelivered task cancellation returned %d: %s", response.Code, response.Body.String())
	}
	var canceled taskView
	if err := json.Unmarshal(response.Body.Bytes(), &canceled); err != nil {
		t.Fatal("decode canceled task:", err)
	}
	if canceled.Status != taskstate.Canceled || canceled.Result.Code != string(coretasks.ResultCanceled) || canceled.Result.ObservedState != "not_dispatched" {
		t.Fatalf("API returned an unproven cancellation result: %+v", canceled)
	}
	if retry := cancel(); retry.Code != http.StatusOK {
		t.Fatalf("idempotent cancellation retry returned %d: %s", retry.Code, retry.Body.String())
	}

	sent, err := core.tasks.Enqueue(context.Background(), coretasks.EnqueueRequest{NodeID: nodeID, IdempotencyKey: "cancel-already-sent-key",
		Intent: protocol.TaskIntent{Action: protocol.TaskRestart, ContainerID: strings.Repeat("b", 64)}, ActorID: sql.NullInt64{Int64: 1, Valid: true}, RemoteAddr: "127.0.0.1"})
	if err != nil {
		t.Fatal("persist already-sent task fixture:", err)
	}
	connection := coretasks.AgentConnection{NodeID: nodeID, ConnectionGeneration: 7, JournalID: strings.Repeat("9", 64)}
	if _, err := core.tasks.ObserveAgentConnection(context.Background(), connection); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := core.tasks.ClaimNext(context.Background(), connection, sql.NullInt64{}, "unknown"); err != nil || !ok {
		t.Fatalf("durably mark fixture sent: ok=%t err=%v", ok, err)
	}
	r := httptest.NewRequest(http.MethodDelete, "/api/v1/nodes/"+nodeID+"/tasks/"+sent.Task.TaskID, nil)
	r.Header.Set("Origin", "https://panel.test")
	r.Header.Set(csrfHeaderName, csrf)
	r.AddCookie(&http.Cookie{Name: sessionCookieName, Value: session})
	r.AddCookie(&http.Cookie{Name: csrfCookieName, Value: csrf})
	w := httptest.NewRecorder()
	core.ServeHTTP(w, r)
	if w.Code != http.StatusConflict {
		t.Fatalf("API canceled a task already committed for delivery: status=%d body=%s", w.Code, w.Body.String())
	}
	unchanged, err := core.tasks.Get(context.Background(), nodeID, sent.Task.TaskID)
	if err != nil || unchanged.Status != taskstate.Queued || unchanged.DeliveryState != "sent" || !unchanged.Evidence.DeliveryCommitted {
		t.Fatalf("rejected cancel mutated sent task: task=%+v err=%v", unchanged, err)
	}
}

func TestContainerActionRouteRequiresFullContainerID(t *testing.T) {
	const nodeID = "00000000-0000-4000-8000-000000000001"
	for _, id := range []string{
		"aaaaaaaaaaaa",
		"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
		"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaz",
	} {
		path := "/api/v1/nodes/" + nodeID + "/containers/" + id + "/actions"
		if _, _, ok := containerActionRoute(path); ok {
			t.Errorf("accepted noncanonical Docker target ID %q", id)
		}
	}
	fullID := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if gotNode, gotID, ok := containerActionRoute("/api/v1/nodes/" + nodeID + "/containers/" + fullID + "/actions"); !ok || gotNode != nodeID || gotID != fullID {
		t.Fatalf("full-ID action route parsed as node=%q id=%q ok=%t", gotNode, gotID, ok)
	}
}
