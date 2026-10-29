package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"
)

type accountTestClient struct {
	t      *testing.T
	base   string
	client *http.Client
}

func (c accountTestClient) call(method, path string, body any, want int) []byte {
	c.t.Helper()
	var data []byte
	if body != nil {
		data, _ = json.Marshal(body)
	}
	req, _ := http.NewRequest(method, c.base+path, bytes.NewReader(data))
	req.Header.Set("Origin", c.base)
	req.Header.Set("Content-Type", "application/json")
	if method != "GET" {
		csrfReq, _ := http.NewRequest("GET", c.base+"/api/v1/auth/csrf", nil)
		res, err := c.client.Do(csrfReq)
		if err != nil {
			c.t.Fatal(err)
		}
		var csrf struct{ Token string }
		json.NewDecoder(res.Body).Decode(&csrf)
		res.Body.Close()
		req.Header.Set("X-CSRF-Token", csrf.Token)
	}
	res, err := c.client.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer res.Body.Close()
	data, _ = io.ReadAll(res.Body)
	if res.StatusCode != want {
		c.t.Fatalf("%s %s: got %d want %d: %s", method, path, res.StatusCode, want, data)
	}
	return data
}
func (c accountTestClient) login(name, password string) {
	c.call("POST", "/api/v1/auth/login", map[string]any{"username": name, "password": password}, 200)
}

func TestAppearanceSharkRosterPersists(t *testing.T) {
	_, newClient := newAccountFixture(t)
	admin := newClient()
	admin.login("admin", "administrator-password")
	animals := []map[string]any{
		{"kind": "classic", "size": 1},
		{"kind": "hammerhead", "size": 1},
		{"kind": "mako", "size": 0.9},
		{"kind": "greathammer", "size": 1.2},
		{"kind": "nurse", "size": 1},
	}
	admin.call("PUT", "/api/v1/settings/appearance", map[string]any{
		"displayName":     "Sharks",
		"theme":           "dark",
		"backgroundColor": "#101827",
		"sharks":          animals,
	}, 200)
	data := admin.call("GET", "/api/v1/public/appearance", nil, 200)
	var result struct {
		Sharks []struct {
			Kind string `json:"kind"`
		} `json:"sharks"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Sharks) != len(animals) {
		t.Fatalf("persisted animal count %d, want %d", len(result.Sharks), len(animals))
	}
	for i, animal := range animals {
		if result.Sharks[i].Kind != animal["kind"] {
			t.Fatalf("persisted animal %d kind %q, want %q", i, result.Sharks[i].Kind, animal["kind"])
		}
	}
}

func newAccountFixture(t *testing.T) (*Server, func() accountTestClient) {
	t.Helper()
	core, err := New("test", Options{DataDir: t.TempDir(), Development: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := core.BootstrapAdministrator(context.Background(), "admin", "administrator-password"); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(core)
	t.Cleanup(func() { server.Close(); core.Close() })
	return core, func() accountTestClient {
		jar, _ := cookiejar.New(nil)
		return accountTestClient{t, server.URL, &http.Client{Jar: jar}}
	}
}
func TestMultiAccountPermissionsSessionsAndLifecycle(t *testing.T) {
	core, newClient := newAccountFixture(t)
	admin, viewer, other := newClient(), newClient(), newClient()
	admin.login("admin", "administrator-password")
	create := func(name, role string) int64 {
		data := admin.call("POST", "/api/v1/accounts", map[string]any{"username": name, "displayName": name, "password": "initial-password-long", "role": role}, 201)
		var result struct{ Account accountView }
		if err := json.Unmarshal(data, &result); err != nil {
			t.Fatal(err)
		}
		return result.Account.ID
	}
	id := create("reader", "viewer")
	otherID := create("second-admin", "administrator")
	viewer.login("reader", "initial-password-long")
	other.login("second-admin", "initial-password-long")
	viewer.call("GET", "/api/v1/nodes", nil, 200)
	viewer.call("GET", "/api/v1/dashboard/core", nil, 200)
	for _, path := range []string{"/api/v1/guest/settings", "/api/v1/agents/enrollment-grants", "/api/v1/commands/batches", "/api/v1/updates/status", "/ws/v1/streams/terminal", "/ws/v1/streams/logs"} {
		viewer.call("GET", path, nil, 403)
	}
	viewer.call("POST", "/api/v1/accounts", map[string]any{"username": "forged", "role": "administrator"}, 403)
	viewer.call("PUT", "/api/v1/settings/appearance", map[string]any{"displayName": "forged", "theme": "light", "backgroundColor": "#000000"}, 403)
	viewer.call("PUT", "/api/v1/auth/profile", map[string]any{"displayName": "Reader profile", "theme": "dark"}, 200)
	if data := admin.call("GET", "/api/v1/auth/me", nil, 200); bytes.Contains(data, []byte("Reader profile")) {
		t.Fatal("profile crossed account boundary")
	}
	var sessions struct{ Sessions []browserSessionView }
	json.Unmarshal(admin.call("GET", "/api/v1/auth/sessions", nil, 200), &sessions)
	if len(sessions.Sessions) != 1 {
		t.Fatal("foreign sessions were listed")
	}
	viewer.call("DELETE", "/api/v1/auth/sessions/"+sessions.Sessions[0].ID, nil, 404)
	// A bad target password must leave the current account and session intact.
	admin.call("POST", "/api/v1/auth/switch", map[string]any{"username": "reader", "password": "incorrect-password"}, 401)
	var me struct{ User accountView }
	json.Unmarshal(admin.call("GET", "/api/v1/auth/me", nil, 200), &me)
	if me.User.ID != 1 {
		t.Fatal("failed switch changed identity")
	}
	// Reset revokes the target's sessions only.
	admin.call("POST", "/api/v1/accounts/"+strconv.FormatInt(id, 10)+"/password", map[string]any{"password": "replacement-password-long", "confirmed": true}, 204)
	viewer.call("GET", "/api/v1/auth/me", nil, 401)
	other.call("GET", "/api/v1/auth/me", nil, 200)
	admin.call("GET", "/api/v1/auth/me", nil, 200)
	viewer.login("reader", "replacement-password-long")
	// Switching rotates the current session and closes the replaced identity.
	u, _ := url.Parse(admin.base)
	var old []*http.Cookie
	for _, cookie := range admin.client.Jar.Cookies(u) {
		copy := *cookie
		old = append(old, &copy)
	}
	admin.call("POST", "/api/v1/auth/switch", map[string]any{"username": "reader", "password": "replacement-password-long"}, 200)
	json.Unmarshal(admin.call("GET", "/api/v1/auth/me", nil, 200), &me)
	if me.User.ID != id || me.User.Role != "viewer" {
		t.Fatal("switch did not bind target account")
	}
	stale := newClient()
	stale.client.Jar.SetCookies(u, old)
	stale.call("GET", "/api/v1/auth/me", nil, 401)
	// Disabling withdraws all of that account's sessions immediately.
	other.call("PUT", "/api/v1/accounts/"+strconv.FormatInt(id, 10), map[string]any{"username": "reader", "displayName": "Reader", "role": "viewer", "enabled": false, "confirmed": true}, 204)
	admin.call("GET", "/api/v1/auth/me", nil, 401)
	viewer.call("GET", "/api/v1/auth/me", nil, 401)
	// No mutation can remove the final enabled administrator.
	other.call("PUT", "/api/v1/accounts/1", map[string]any{"username": "admin", "displayName": "Admin", "role": "viewer", "enabled": true, "confirmed": true}, 204)
	other.call("PUT", "/api/v1/accounts/"+strconv.FormatInt(otherID, 10), map[string]any{"username": "second-admin", "displayName": "Second", "role": "viewer", "enabled": true, "confirmed": true}, 409)
	other.call("DELETE", "/api/v1/accounts/"+strconv.FormatInt(otherID, 10), map[string]any{"confirmed": true}, 409)
	other.call("DELETE", "/api/v1/accounts/"+strconv.FormatInt(id, 10), map[string]any{"confirmed": true}, 204)
	var count int
	if err := core.store.DB.QueryRow(`SELECT count(*) FROM browser_sessions WHERE user_id=?`, id).Scan(&count); err != nil || count != 0 {
		t.Fatal("deleted user sessions retained", err)
	}
}

func TestOwnPasswordChangeDoesNotRevokeAnotherAccount(t *testing.T) {
	_, newClient := newAccountFixture(t)
	admin, viewer := newClient(), newClient()
	admin.login("admin", "administrator-password")
	admin.call("POST", "/api/v1/accounts", map[string]any{"username": "reader", "displayName": "Reader", "password": "initial-password-long", "role": "viewer"}, 201)
	viewer.login("reader", "initial-password-long")
	viewer.call("POST", "/api/v1/auth/password", map[string]any{"currentPassword": "initial-password-long", "newPassword": "changed-password-long"}, 204)
	viewer.call("GET", "/api/v1/auth/me", nil, 401)
	admin.call("GET", "/api/v1/auth/me", nil, 200)
	viewer.login("reader", "changed-password-long")
}
