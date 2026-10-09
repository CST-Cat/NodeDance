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

	"github.com/CST-Cat/NodeDance/internal/core/dashboard"
	"github.com/CST-Cat/NodeDance/internal/protocol"
)

func TestDashboardPreferencesAndHistoryAPIAreAuthenticatedAndPersisted(t *testing.T) {
	// Keep the fixed Core clock just ahead of the real timestamp written by
	// installIntegrationAdmin; session validation rejects a clock that moves
	// backwards by even a small amount.
	now := time.Now().UTC().Add(time.Second)
	core, err := New("dashboard-api-test", Options{DataDir: filepath.Join(t.TempDir(), "core"), Development: true,
		PublicOrigin: "https://panel.test", Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = core.Close() })
	session, csrf, err := installIntegrationAdmin(core)
	if err != nil {
		t.Fatal(err)
	}
	identity := insertDockerTestNode(t, core)
	request := func(method, path, body string, authenticated, write bool) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Origin", "https://panel.test")
		if body != "" {
			r.Header.Set("Content-Type", "application/json")
		}
		if authenticated {
			r.AddCookie(&http.Cookie{Name: sessionCookieName, Value: session})
		}
		if write {
			r.AddCookie(&http.Cookie{Name: csrfCookieName, Value: csrf})
			r.Header.Set(csrfHeaderName, csrf)
		}
		w := httptest.NewRecorder()
		core.ServeHTTP(w, r)
		return w
	}
	for _, path := range []string{"/api/v1/dashboard/settings", "/api/v1/nodes/" + identity.NodeID + "/preferences",
		"/api/v1/nodes/" + identity.NodeID + "/history?resolution=minute&from=" + now.Add(-time.Hour).Format(time.RFC3339) + "&to=" + now.Format(time.RFC3339)} {
		if got := request(http.MethodGet, path, "", false, false).Code; got != http.StatusUnauthorized {
			t.Fatalf("private dashboard API %s returned %d without a session", path, got)
		}
	}
	settings := dashboard.Settings{ViewMode: "manage", GroupBy: "compose", SortBy: "name", FeaturedLimit: 6, CustomFields: []string{"state", "ports", "health"}}
	encoded, _ := json.Marshal(settings)
	settingsWrite := request(http.MethodPut, "/api/v1/dashboard/settings", string(encoded), true, true)
	if settingsWrite.Code != http.StatusNoContent {
		t.Fatalf("dashboard settings PUT returned %d: %s", settingsWrite.Code, settingsWrite.Body.String())
	}
	var settingsResult dashboard.Settings
	if got := request(http.MethodGet, "/api/v1/dashboard/settings", "", true, false); got.Code != http.StatusOK || json.Unmarshal(got.Body.Bytes(), &settingsResult) != nil ||
		settingsResult.ViewMode != "manage" || settingsResult.FeaturedLimit != 6 {
		t.Fatalf("dashboard settings GET status=%d body=%s decoded=%+v", got.Code, got.Body, settingsResult)
	}
	preference := dashboard.Preference{NodeID: identity.NodeID, TargetKind: "node", Identity: dashboard.NodeIdentity(identity.NodeID), Alias: "Production", Icon: "server", Notes: "primary", Visible: true, Pinned: true}
	encoded, _ = json.Marshal(preference)
	path := "/api/v1/nodes/" + identity.NodeID + "/preferences"
	if got := request(http.MethodPut, path, string(encoded), true, true); got.Code != http.StatusNoContent {
		t.Fatalf("node preference PUT returned %d: %s", got.Code, got.Body)
	}
	list := request(http.MethodGet, path, "", true, false)
	var preferenceResponse struct {
		Preferences []dashboard.Preference `json:"preferences"`
	}
	if list.Code != http.StatusOK || json.Unmarshal(list.Body.Bytes(), &preferenceResponse) != nil || len(preferenceResponse.Preferences) != 1 || preferenceResponse.Preferences[0].Alias != "Production" {
		t.Fatalf("node preference GET status=%d body=%s decoded=%+v", list.Code, list.Body, preferenceResponse)
	}
	unsafe := preference
	unsafe.ServiceURL = "javascript:alert(1)"
	encoded, _ = json.Marshal(unsafe)
	if got := request(http.MethodPut, path, string(encoded), true, true).Code; got != http.StatusBadRequest {
		t.Fatal("unsafe dashboard service link was accepted")
	}

	boundary := now.Truncate(time.Minute)
	for index, cpu := range []float64{25, 75} {
		snapshot := protocol.MetricsSnapshot{CPU: protocol.MetricsCPU{UsagePercent: protocol.Metric[float64]{Status: protocol.MetricKnown, Value: &cpu}}}
		if err := core.history.AppendSnapshot(context.Background(), identity.NodeID, snapshot, boundary.Add(-2*time.Minute+time.Duration(index)*time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	// The bucket starting exactly at `to` is excluded by the documented [from,to) contract.
	cpuAtBoundary := 100.0
	if err := core.history.AppendSnapshot(context.Background(), identity.NodeID,
		protocol.MetricsSnapshot{CPU: protocol.MetricsCPU{UsagePercent: protocol.Metric[float64]{Status: protocol.MetricKnown, Value: &cpuAtBoundary}}}, boundary); err != nil {
		t.Fatal(err)
	}
	historyURL := "/api/v1/nodes/" + identity.NodeID + "/history?resolution=minute&from=" + boundary.Add(-3*time.Minute).Format(time.RFC3339) + "&to=" + boundary.Format(time.RFC3339)
	historyResponse := request(http.MethodGet, historyURL, "", true, false)
	var result struct {
		Series []struct {
			Key    string `json:"key"`
			Points []struct {
				Value   float64 `json:"value"`
				Samples int64   `json:"samples"`
			} `json:"points"`
		} `json:"series"`
	}
	if historyResponse.Code != http.StatusOK || json.Unmarshal(historyResponse.Body.Bytes(), &result) != nil {
		t.Fatalf("history API status=%d body=%s", historyResponse.Code, historyResponse.Body)
	}
	if len(result.Series) != 1 || result.Series[0].Key != "cpu.usage_percent" || len(result.Series[0].Points) != 2 ||
		result.Series[0].Points[0].Value != 25 || result.Series[0].Points[1].Value != 75 {
		t.Fatalf("history API did not expose real known samples without the to bucket: %+v", result)
	}
	if got := request(http.MethodGet, "/api/v1/nodes/"+identity.NodeID+"/history?resolution=minute&from="+now.Add(-31*24*time.Hour).Format(time.RFC3339)+"&to="+now.Format(time.RFC3339), "", true, false).Code; got != http.StatusBadRequest {
		t.Fatalf("history API accepted range beyond 30-day minute retention: %d", got)
	}
}
