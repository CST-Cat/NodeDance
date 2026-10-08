package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHealthIsPublicAndContainsNoPrivateData(t *testing.T) {
	s, err := New("test")
	if err != nil {
		t.Fatal(err)
	}
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
	s, err := New("test")
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/api/v1/nodes", "/api/v1/auth/login", "/ws/v1/agent", "/ws/v1/dashboard"} {
		r := httptest.NewRequest(http.MethodGet, path, nil)
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		if w.Code != http.StatusNotFound {
			t.Errorf("%s status=%d, want 404", path, w.Code)
		}
	}
}

func TestEmbeddedHomeIsServed(t *testing.T) {
	s, err := New("test")
	if err != nil {
		t.Fatal(err)
	}
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
