package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func newTestServer(t *testing.T) *Server {
	t.Helper()
	s, err := New("test", Options{DataDir: filepath.Join(t.TempDir(), "data"), Development: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestHealthIsPublicAndContainsNoPrivateData(t *testing.T) {
	s := newTestServer(t)
	r := httptest.NewRequest(http.MethodGet, "/api/v1/health", nil)
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("health status = %d, want 200", w.Code)
	}
	var body map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["status"] != "ok" || body["version"] != "test" {
		t.Fatalf("unexpected health response: %#v", body)
	}
	if strings.Contains(w.Body.String(), "node") || strings.Contains(w.Body.String(), "container") || strings.Contains(w.Body.String(), "password") {
		t.Fatalf("health response contains management data: %s", w.Body.String())
	}
}

func TestManagementEntrypointsAreAbsent(t *testing.T) {
	s := newTestServer(t)
	for _, path := range []string{"/api/v1/nodes", "/api/v1/nodes/1/containers", "/ws/v1/agent", "/ws/v1/dashboard"} {
		r := httptest.NewRequest(http.MethodGet, path, nil)
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		if w.Code != http.StatusUnauthorized {
			t.Errorf("%s status=%d, want 401", path, w.Code)
		}
	}
	r := httptest.NewRequest(http.MethodGet, "/api/v1/auth/login", nil)
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET /api/v1/auth/login status=%d, want 405", w.Code)
	}
}

func TestEmbeddedHomeIsServed(t *testing.T) {
	s := newTestServer(t)
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "NodeDance") {
		t.Fatalf("home response status=%d body=%q", w.Code, w.Body.String())
	}
	if w.Code >= 300 && w.Code < 400 {
		t.Fatalf("home unexpectedly redirected: %d %s", w.Code, w.Header().Get("Location"))
	}
}
