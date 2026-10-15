package server

import (
	"context"
	"encoding/json"
	"github.com/CST-Cat/NodeDance/internal/control"
	"github.com/CST-Cat/NodeDance/internal/core/dashboard"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func TestOverviewContextPrivateMemoConflictAndContract(t *testing.T) {
	s := featureCore(t)
	ctx := context.Background()
	if err := s.BootstrapAdministrator(ctx, "overview-admin", "overview-password-strong"); err != nil {
		t.Fatal(err)
	}
	host := httptest.NewServer(s)
	defer host.Close()
	client, err := control.NewClient(control.ClientOptions{Server: host.URL, Development: true, StateDir: filepath.Join(t.TempDir(), "client")})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	catalog, err := client.Catalog(ctx)
	if err != nil {
		t.Fatal(err)
	}
	call := func(name string, body string) control.Result {
		op, err := control.FindIn(catalog, name)
		if err != nil {
			t.Fatal(err)
		}
		return client.Execute(ctx, op, control.Input{Body: json.RawMessage(body)}, nil, nil)
	}
	for _, path := range []string{"/api/v1/dashboard/core", "/api/v1/dashboard/memo"} {
		res, err := http.Get(host.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != 401 {
			t.Fatal(path, res.StatusCode)
		}
	}
	if result := call("auth.login", `{"username":"overview-admin","password":"overview-password-strong"}`); result.Err() != nil {
		t.Fatal(result)
	}
	if result := call("dashboard.core-info", ""); result.Err() != nil {
		t.Fatal(result)
	}
	result := call("dashboard.memo", "")
	if result.Err() != nil {
		t.Fatal(result)
	}
	var initial dashboard.Memo
	if err := json.Unmarshal(result.Data, &initial); err != nil || initial.Revision != 1 {
		t.Fatal(initial, err)
	}
	content := "<script>not executed</script>\nMaintenance 中文"
	raw, _ := json.Marshal(dashboard.MemoUpdate{Content: content, Revision: initial.Revision})
	result = call("dashboard.save-memo", string(raw))
	if result.Err() != nil {
		t.Fatal(result)
	}
	if result = call("dashboard.save-memo", string(raw)); result.Status != 409 {
		t.Fatal("stale revision overwrote memo", result)
	}
	saved, err := s.dashboardPreferences.Memo(ctx)
	if err != nil || saved.Content != content || saved.Revision != 2 {
		t.Fatal(saved, err)
	}
	raw, _ = json.Marshal(dashboard.MemoUpdate{Content: strings.Repeat("x", dashboard.MaxMemoBytes+1), Revision: saved.Revision})
	if result = call("dashboard.save-memo", string(raw)); result.Err() == nil {
		t.Fatal("oversized memo accepted by discovered client", result)
	}
	rejected := httptest.NewRecorder()
	s.handleDashboardContextAPI(rejected, httptest.NewRequest(http.MethodPut, "/api/v1/dashboard/memo", strings.NewReader(string(raw))))
	if rejected.Code != 400 {
		t.Fatal("oversized memo accepted by Core", rejected.Code)
	}
	raw, _ = json.Marshal(dashboard.MemoUpdate{Content: "", Revision: saved.Revision})
	if result = call("dashboard.save-memo", string(raw)); result.Err() != nil {
		t.Fatal("memo cannot be cleared", result)
	}
}
