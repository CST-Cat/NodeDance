package server

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

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
		"/api/v1/nodes/" + nodeID + "/containers/" + containerID + "/actions",
	} {
		if response := request(http.MethodGet, path, false, false, false); response.Code != http.StatusUnauthorized {
			t.Errorf("unauthenticated GET %s returned %d, want 401", path, response.Code)
		}
	}
	if response := request(http.MethodPost, "/api/v1/nodes/"+nodeID+"/containers/"+containerID+"/actions", false, false, false); response.Code != http.StatusUnauthorized {
		t.Errorf("unauthenticated action returned %d, want 401", response.Code)
	}
	if response := request(http.MethodPost, "/api/v1/nodes/"+nodeID+"/containers/"+containerID+"/actions", true, false, true); response.Code != http.StatusForbidden {
		t.Errorf("authenticated action without a trusted Origin returned %d, want 403", response.Code)
	}
	if response := request(http.MethodPost, "/api/v1/nodes/"+nodeID+"/containers/"+containerID+"/actions", true, true, false); response.Code != http.StatusForbidden {
		t.Errorf("authenticated action without a CSRF token returned %d, want 403", response.Code)
	}
	if response := request(http.MethodGet, "/api/v1/nodes/"+nodeID+"/tasks", true, false, false); response.Code != http.StatusOK {
		t.Errorf("authenticated task list returned %d, want 200", response.Code)
	}
	if _, err := core.store.DB.Exec(`DELETE FROM browser_sessions WHERE id=?`, "0123456789abcdef0123456789abcdef"); err != nil {
		t.Fatal("revoke test Session:", err)
	}
	if response := request(http.MethodGet, "/api/v1/nodes/"+nodeID+"/tasks", true, false, false); response.Code != http.StatusUnauthorized {
		t.Errorf("revoked Session still accessed task API: status=%d", response.Code)
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
