package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/CST-Cat/NodeDance/internal/control"
)

func TestTerminalSessionSurvivesIdleAndClientRestartButCanBeRevoked(t *testing.T) {
	core, newBrowser := newAccountFixture(t)
	host := httptest.NewServer(core)
	defer host.Close()
	state := filepath.Join(t.TempDir(), "client")
	open := func() *control.Client {
		c, err := control.NewClient(control.ClientOptions{Server: host.URL, StateDir: state, Development: true})
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	client := open()
	catalog, err := client.Catalog(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	call := func(name, body string) control.Result {
		op, err := control.FindIn(catalog, name)
		if err != nil {
			t.Fatal(err)
		}
		return client.Execute(context.Background(), op, control.Input{Body: json.RawMessage(body)}, nil, nil)
	}
	if result := call("auth.login", `{"username":"admin","password":"administrator-password"}`); result.Err() != nil {
		t.Fatal(result.Error)
	}
	client.Close()
	client = open()
	defer func() { client.Close() }()
	var id string
	if err := core.store.DB.QueryRow(`SELECT session_id FROM persistent_sessions`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	age := func() {
		if _, err := core.store.DB.Exec(`UPDATE browser_sessions SET last_seen_at=?`, time.Now().AddDate(-10, 0, 0).UnixNano()); err != nil {
			t.Fatal(err)
		}
	}
	age()
	if !core.sessionStillValid(context.Background(), id) {
		t.Fatal("persistent stream authorization expired")
	}
	if result := call("auth.me", ""); result.Err() != nil {
		t.Fatal(result.Error)
	}
	// A normal browser session still expires, even with terminal-looking headers.
	browser := newBrowser()
	browser.login("admin", "administrator-password")
	age()
	req, _ := http.NewRequest("GET", browser.base+"/api/v1/auth/me", nil)
	req.Header.Set("User-Agent", "NodeDance CLI/TUI")
	req.Header.Set("X-NodeDance-Session", "persistent")
	res, err := browser.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 401 {
		t.Fatalf("browser expiry bypass: %d", res.StatusCode)
	}
	// Password changes remain possible after arbitrary idle and revoke all own sessions.
	if result := call("auth.password", `{"currentPassword":"administrator-password","newPassword":"replacement-password-long"}`); result.Err() != nil {
		t.Fatal(result.Error)
	}
	if core.sessionStillValid(context.Background(), id) {
		t.Fatal("revoked persistent session authorized a stream")
	}
	if result := call("auth.me", ""); result.Status != 401 {
		t.Fatalf("revoked session: %+v", result)
	}
	var count int
	if err := core.store.DB.QueryRow(`SELECT count(*) FROM persistent_sessions`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("orphaned policy: %d %v", count, err)
	}
}
