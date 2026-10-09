package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
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
	for _, path := range []string{"/api/v1/nodes", "/api/v1/nodes/00000000-0000-4000-8000-000000000001/containers", "/api/v1/nodes/00000000-0000-4000-8000-000000000001/containers/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "/ws/v1/agent", "/ws/v1/dashboard"} {
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

func TestFreshCoreStartsWithOperationalRetentionIndexes(t *testing.T) {
	s := newTestServer(t)
	for _, index := range []string{"core_task_audit_events_retention", "core_tasks_retention", "compose_operations_retention", "compose_editor_operations_retention", "file_write_tasks_retention"} {
		var count int
		if err := s.store.DB.QueryRowContext(context.Background(), `SELECT count(*) FROM sqlite_master WHERE type='index' AND name=?`, index).Scan(&count); err != nil || count != 1 {
			t.Fatalf("fresh Core startup retention index %q present=%d err=%v", index, count, err)
		}
	}
}

func TestNonMetricHistoryRetentionWorkerCleansAndShutdownWaits(t *testing.T) {
	ctx := context.Background()
	clock := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	s, err := New("retention-worker", Options{
		DataDir: filepath.Join(t.TempDir(), "data"), Development: true,
		NonMetricHistoryRetention: 90 * 24 * time.Hour,
		HistoryRetentionInterval:  10 * time.Millisecond,
		Now:                       func() time.Time { return clock },
	})
	if err != nil {
		t.Fatal("start Core:", err)
	}
	old := clock.Add(-91 * 24 * time.Hour).Unix()
	if _, err := s.store.DB.ExecContext(ctx, `INSERT INTO audit_entries(occurred_at,action,outcome,remote_addr) VALUES(?,'login','succeeded','unknown')`, old); err != nil {
		_ = s.Close()
		t.Fatal("insert expired audit entry:", err)
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		var count int
		if err := s.store.DB.QueryRowContext(ctx, `SELECT count(*) FROM audit_entries WHERE occurred_at=?`, old).Scan(&count); err != nil {
			_ = s.Close()
			t.Fatal(err)
		}
		if count == 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	var count int
	if err := s.store.DB.QueryRowContext(ctx, `SELECT count(*) FROM audit_entries WHERE occurred_at=?`, old).Scan(&count); err != nil || count != 0 {
		_ = s.Close()
		t.Fatalf("retention worker did not delete expired entry: count=%d err=%v", count, err)
	}

	failedOld := clock.Add(-100 * 24 * time.Hour).Unix()
	if _, err := s.store.DB.ExecContext(ctx, `INSERT INTO audit_entries(occurred_at,action,outcome,remote_addr) VALUES(?,'login','succeeded','unknown')`, failedOld); err != nil {
		_ = s.Close()
		t.Fatal("insert second expired audit entry:", err)
	}
	if _, err := s.store.DB.ExecContext(ctx, `CREATE TRIGGER fail_retention_audit BEFORE DELETE ON audit_entries BEGIN SELECT RAISE(ABORT,'injected cleanup failure'); END`); err != nil {
		_ = s.Close()
		t.Fatal("install failure trigger:", err)
	}
	time.Sleep(30 * time.Millisecond)
	if err := s.store.DB.QueryRowContext(ctx, `SELECT count(*) FROM audit_entries WHERE occurred_at=?`, failedOld).Scan(&count); err != nil || count != 1 {
		_ = s.Close()
		t.Fatalf("failed cleanup falsely removed or changed history: count=%d err=%v", count, err)
	}
	w := httptest.NewRecorder()
	s.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/health", nil))
	if w.Code != http.StatusOK {
		_ = s.Close()
		t.Fatalf("cleanup failure made Core unavailable: health status=%d body=%q", w.Code, w.Body.String())
	}
	closed := make(chan error, 1)
	go func() { closed <- s.Close() }()
	select {
	case err := <-closed:
		if err != nil {
			t.Fatal("close Core after retention worker:", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Core shutdown did not wait for the retention worker to stop")
	}
}
