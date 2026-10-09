package server

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

func TestProbeAPIIsSessionCSRFProtectedAndAudited(t *testing.T) {
	core, err := New("probe-api-test", Options{DataDir: filepath.Join(t.TempDir(), "data"), Development: true, PublicOrigin: "https://panel.test"})
	if err != nil {
		t.Fatal(err)
	}
	defer core.Close()
	unauthenticated := httptest.NewRecorder()
	core.ServeHTTP(unauthenticated, httptest.NewRequest(http.MethodGet, "https://panel.test/api/v1/probes", nil))
	if unauthenticated.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated probe list status=%d, want 401", unauthenticated.Code)
	}
	sessionToken, csrfToken, err := installIntegrationAdmin(core)
	if err != nil {
		t.Fatal(err)
	}
	enrollment, err := core.agents.CreateEnrollment(context.Background(), "probe api node", "127.0.0.1", sql.NullInt64{Int64: 1, Valid: true})
	if err != nil {
		t.Fatal(err)
	}
	request := func(method, path string, body any, withCSRF bool) *httptest.ResponseRecorder {
		var encoded []byte
		if body != nil {
			encoded, _ = json.Marshal(body)
		}
		r := httptest.NewRequest(method, "https://panel.test"+path, bytes.NewReader(encoded))
		r.Header.Set("Origin", "https://panel.test")
		r.AddCookie(&http.Cookie{Name: sessionCookieName, Value: sessionToken})
		r.AddCookie(&http.Cookie{Name: csrfCookieName, Value: csrfToken})
		if withCSRF {
			r.Header.Set(csrfHeaderName, csrfToken)
		}
		w := httptest.NewRecorder()
		core.ServeHTTP(w, r)
		return w
	}
	payload := map[string]any{"nodeId": enrollment.NodeID, "name": "Primary web", "kind": "http",
		"target": "http://127.0.0.1:18080/health", "expectedHttpStatus": 200,
		"intervalSeconds": 30, "timeoutSeconds": 3, "enabled": true}
	if rejected := request(http.MethodPost, "/api/v1/probes", payload, false); rejected.Code != http.StatusForbidden {
		t.Fatalf("cross-site write without CSRF status=%d, want 403 body=%s", rejected.Code, rejected.Body.String())
	}
	created := request(http.MethodPost, "/api/v1/probes", payload, true)
	if created.Code != http.StatusCreated {
		t.Fatalf("create probe status=%d body=%s", created.Code, created.Body.String())
	}
	var config struct {
		ID       string `json:"id"`
		Revision int64  `json:"revision"`
		Status   string `json:"status"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &config); err != nil || config.ID == "" || config.Revision != 1 || config.Status != "unknown" {
		t.Fatalf("invalid create response: %+v err=%v", config, err)
	}
	update := map[string]any{"nodeId": enrollment.NodeID, "name": "Changed", "kind": "https",
		"target": "https://127.0.0.1/health", "expectedHttpStatus": 204,
		"intervalSeconds": 60, "timeoutSeconds": 4, "enabled": true, "revision": config.Revision}
	updated := request(http.MethodPut, "/api/v1/probes/"+config.ID, update, true)
	if updated.Code != http.StatusOK {
		t.Fatalf("update probe status=%d body=%s", updated.Code, updated.Body.String())
	}
	stale := request(http.MethodPut, "/api/v1/probes/"+config.ID, update, true)
	if stale.Code != http.StatusConflict {
		t.Fatalf("stale probe revision status=%d, want 409 body=%s", stale.Code, stale.Body.String())
	}
	list := request(http.MethodGet, "/api/v1/probes", nil, false)
	if list.Code != http.StatusOK || !bytes.Contains(list.Body.Bytes(), []byte(config.ID)) {
		t.Fatalf("probe list omitted created config: status=%d body=%s", list.Code, list.Body.String())
	}
	var auditCount int
	if err := core.store.DB.QueryRow(`SELECT count(*) FROM audit_entries WHERE action IN ('probe_create','probe_update') AND target_kind='node' AND target_id=?`, enrollment.NodeID).Scan(&auditCount); err != nil || auditCount != 2 {
		t.Fatalf("probe writes were not audited atomically: count=%d err=%v", auditCount, err)
	}
}
