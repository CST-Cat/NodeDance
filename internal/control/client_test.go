package control_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/CST-Cat/NodeDance/internal/control"
	"github.com/CST-Cat/NodeDance/internal/core/server"
	"github.com/CST-Cat/NodeDance/internal/protocol"
	"github.com/coder/websocket"
)

func TestCoreSessionLifecycle(t *testing.T) {
	core, err := server.New("test", server.Options{DataDir: t.TempDir(), Development: true})
	if err != nil {
		t.Fatal(err)
	}
	defer core.Close()
	host := httptest.NewServer(core)
	defer host.Close()
	dir := filepath.Join(t.TempDir(), "client")
	options := control.ClientOptions{Server: host.URL, Development: true, StateDir: dir}
	c, err := control.NewClient(options)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	ctx := context.Background()
	call := func(c *control.Client, name string, body string) control.Result {
		t.Helper()
		o, e := control.Find(name)
		if e != nil {
			t.Fatal(e)
		}
		return c.Execute(ctx, o, control.Input{Body: json.RawMessage(body)}, nil, nil)
	}
	if r := call(c, "nodes.list", ""); r.Status != 401 {
		t.Fatalf("unauthenticated: %+v", r)
	}
	path, _ := core.SetupCredentialPath()
	credential, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]string{"credential": strings.TrimSpace(string(credential)), "password": "long-password-for-tests", "displayName": "admin", "username": "test-admin"})
	if r := call(c, "auth.setup", string(body)); r.Error != "" {
		t.Fatalf("setup: %+v", r)
	}
	enrollment := call(c, "agents.enrollment", `{"displayName":"node","coreUrl":"https://core.example"}`)
	if enrollment.Error != "" || !strings.Contains(string(enrollment.Data), "installCommand") || !strings.Contains(string(enrollment.Data), "agent-v") {
		t.Fatalf("shared installer: %+v", enrollment)
	}
	if r := call(c, "core.protocol", ""); r.Error != "" || !strings.Contains(string(r.Data), "capabilities") {
		t.Fatalf("protocol descriptor: %+v", r)
	}
	if r := call(c, "nodes.list", ""); r.Error != "" {
		t.Fatalf("nodes: %+v", r)
	}
	c2, err := control.NewClient(options)
	if err != nil {
		t.Fatal(err)
	}
	defer c2.Close()
	if r := call(c2, "auth.me", ""); r.Error != "" {
		t.Fatalf("persisted session: %+v", r)
	}
	if r := call(c2, "auth.login", `{"username":"test-admin","password":"long-password-for-tests"}`); r.Error != "" {
		t.Fatalf("login with prior csrf: %+v", r)
	}
	if r := call(c2, "auth.logout", ""); r.Error != "" {
		t.Fatalf("logout CSRF: %+v", r)
	}
	if r := call(c2, "nodes.list", ""); r.Status != 401 {
		t.Fatalf("logout session: %+v", r)
	}
	for _, p := range []string{dir, filepath.Join(dir, "session.json")} {
		info, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		want := os.FileMode(0600)
		if p == dir {
			want = 0700
		}
		if info.Mode().Perm() != want {
			t.Fatalf("%s: %v", p, info.Mode())
		}
	}
	options.Server = "http://127.0.0.1:1"
	if other, err := control.NewClient(options); err == nil {
		other.Close()
		t.Fatal("session accepted for another Core")
	}
}
func TestPrivateSessionAndTransport(t *testing.T) {
	for _, url := range []string{"http://example.com", "http://localhost:8180", "https://user:pass@example.com", "https://example.com/path"} {
		if c, err := control.NewClient(control.ClientOptions{Server: url, Development: true}); err == nil {
			c.Close()
			t.Fatalf("unsafe server %s", url)
		}
	}
	dir := filepath.Join(t.TempDir(), "state")
	os.Mkdir(dir, 0700)
	outside := filepath.Join(t.TempDir(), "outside")
	os.WriteFile(outside, []byte(`{}`), 0600)
	os.Symlink(outside, filepath.Join(dir, "session.json"))
	if c, err := control.NewClient(control.ClientOptions{Server: "https://example.com", StateDir: dir}); err == nil {
		c.Close()
		t.Fatal("symlink session accepted")
	}
	os.Remove(filepath.Join(dir, "session.json"))
	os.Chmod(dir, 0755)
	if c, err := control.NewClient(control.ClientOptions{Server: "https://example.com", StateDir: dir}); err == nil {
		c.Close()
		t.Fatal("public session directory accepted")
	}
}
func TestTaskRequestAndDownload(t *testing.T) {
	var redirected bool
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { redirected = true }))
	defer target.Close()
	host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/auth/csrf":
			http.SetCookie(w, &http.Cookie{Name: "nodedance_csrf", Value: "csrf", Path: "/"})
			io.WriteString(w, `{"token":"csrf"}`)
		case "/api/v1/nodes/node/files/tasks":
			if r.Header.Get("Idempotency-Key") != "stable-intent" || r.Header.Get("X-CSRF-Token") != "csrf" || r.Header.Get("Origin") == "" {
				t.Error("missing security/idempotency headers")
			}
			w.WriteHeader(202)
			io.WriteString(w, `{"taskId":"task"}`)
		case "/api/v1/nodes/node/files/download":
			w.Write([]byte{0, 255, 1})
		default:
			http.Redirect(w, r, target.URL, 302)
		}
	}))
	defer host.Close()
	c, err := control.NewClient(control.ClientOptions{Server: host.URL, Development: true})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	o, _ := control.Find("files.action")
	in := control.Input{Params: map[string]string{"nodeId": "node"}, Body: json.RawMessage(`{"action":"mkdir","path":"/tmp/new"}`)}
	if r := c.Execute(context.Background(), o, in, nil, nil); r.Error == "" {
		t.Fatal("missing idempotency accepted")
	}
	in.IdempotencyKey = "stable-intent"
	if r := c.Execute(context.Background(), o, in, nil, nil); r.Status != 202 || r.Error != "" {
		t.Fatalf("task: %+v", r)
	}
	o, _ = control.Find("files.download")
	var out strings.Builder
	if r := c.Execute(context.Background(), o, control.Input{Params: in.Params, Query: map[string]string{"path": "/tmp/a"}}, nil, &out); r.Error != "" || out.String() != string([]byte{0, 255, 1}) {
		t.Fatalf("download: %+v", r)
	}
	o, _ = control.Find("core.health")
	if r := c.Execute(context.Background(), o, control.Input{}, nil, nil); r.Status != 302 || r.Error == "" {
		t.Fatalf("redirect: %+v", r)
	}
	if redirected {
		t.Fatal("redirect was followed")
	}
}
func TestWebSocketUsesSessionAndOrigin(t *testing.T) {
	host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/auth/csrf" {
			http.SetCookie(w, &http.Cookie{Name: "nodedance_csrf", Value: "csrf", Path: "/"})
			io.WriteString(w, `{"token":"csrf"}`)
			return
		}
		if r.URL.Path == "/api/v1/auth/login" {
			http.SetCookie(w, &http.Cookie{Name: "nodedance_session", Value: "session", Path: "/"})
			io.WriteString(w, `{}`)
			return
		}
		cookie, e := r.Cookie("nodedance_session")
		if e != nil || cookie.Value != "session" || r.Header.Get("Origin") == "" {
			http.Error(w, "unauthorized", 401)
			return
		}
		conn, e := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
		if e != nil {
			return
		}
		defer conn.CloseNow()
		conn.Write(r.Context(), websocket.MessageText, []byte(`{"type":"metrics"}`))
	}))
	defer host.Close()
	c, err := control.NewClient(control.ClientOptions{Server: host.URL, Development: true})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	login, _ := control.Find("auth.login")
	if r := c.Execute(context.Background(), login, control.Input{Body: json.RawMessage(`{"password":"secret"}`)}, nil, nil); r.Error != "" {
		t.Fatal(r.Error)
	}
	watch, _ := control.Find("dashboard.watch")
	conn, err := c.Connect(context.Background(), watch, control.Input{})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	_, raw, err := conn.Read(context.Background())
	if err != nil || string(raw) != `{"type":"metrics"}` {
		t.Fatalf("stream: %s %v", raw, err)
	}
}
func TestCatalog(t *testing.T) {
	seen := map[string]bool{}
	for _, o := range control.Operations() {
		if seen[o.Name] {
			t.Fatalf("duplicate %s", o.Name)
		}
		seen[o.Name] = true
		if o.Description == "" || o.Path == "" {
			t.Fatalf("incomplete %+v", o)
		}
		params := map[string]string{}
		for _, p := range o.Parameters {
			params[p] = "id"
		}
		if _, err := o.URLPath(params); err != nil {
			t.Fatal(err)
		}
		for _, p := range o.Parameters {
			bad := map[string]string{}
			for k, v := range params {
				bad[k] = v
			}
			bad[p] = "../escape"
			if _, err := o.URLPath(bad); err == nil {
				t.Fatal("path traversal accepted")
			}
		}
	}
}

func TestOpenTerminalAuthorizesAndSendsTarget(t *testing.T) {
	frames := make(chan map[string]any, 2)
	host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/auth/csrf":
			http.SetCookie(w, &http.Cookie{Name: "nodedance_csrf", Value: "csrf", Path: "/"})
			io.WriteString(w, `{"token":"csrf"}`)
		case "/api/v1/nodes/node/terminals":
			io.WriteString(w, `{"streamId":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","ticket":"single-use-ticket"}`)
		case "/ws/v1/streams/terminal":
			if r.URL.Query().Get("ticket") != "single-use-ticket" {
				http.Error(w, "bad ticket", 401)
				return
			}
			conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
			if err != nil {
				return
			}
			defer conn.CloseNow()
			_, raw, err := conn.Read(r.Context())
			if err != nil {
				return
			}
			var frame map[string]any
			json.Unmarshal(raw, &frame)
			frames <- frame
			conn.Write(r.Context(), websocket.MessageText, []byte(`{"type":"terminal","frame":{"action":"ready"}}`))
			_, raw, err = conn.Read(r.Context())
			if err != nil {
				return
			}
			json.Unmarshal(raw, &frame)
			frames <- frame
		}
	}))
	defer host.Close()
	client, err := control.NewClient(control.ClientOptions{Server: host.URL, Development: true})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	conn, streamID, err := client.OpenTerminal(ctx, control.Input{Params: map[string]string{"nodeId": "node"}, Body: json.RawMessage(`{"targetKind":"host"}`)}, 40, 120)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	if frame := <-frames; frame["action"] != "open" || frame["targetKind"] != "host" || frame["rows"] != float64(40) || frame["streamId"] != streamID {
		t.Fatalf("incorrect open: %v", frame)
	}
	if _, _, err = conn.Read(ctx); err != nil {
		t.Fatal(err)
	}
	if err = control.SendTerminal(ctx, conn, protocol.TerminalFrame{StreamID: streamID, Action: protocol.TerminalActionInput, Data: []byte("ls\n")}); err != nil {
		t.Fatal(err)
	}
	if frame := <-frames; frame["action"] != "input" || frame["data"] != "bHMK" {
		t.Fatalf("incorrect input: %v", frame)
	}
}

func TestConnectedCorePublishesSharedCatalogWithoutGrantingAccess(t *testing.T) {
	core, err := server.New("test", server.Options{DataDir: t.TempDir(), Development: true})
	if err != nil {
		t.Fatal(err)
	}
	defer core.Close()
	host := httptest.NewServer(core)
	defer host.Close()
	client, err := control.NewClient(control.ClientOptions{Server: host.URL, Development: true, StateDir: filepath.Join(t.TempDir(), "client")})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	catalog, err := client.Catalog(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want, _ := json.Marshal(control.Describe())
	got, _ := json.Marshal(catalog)
	if string(want) != string(got) {
		t.Fatal("Core published a different contract")
	}
	operation, _ := control.FindIn(catalog, "nodes.list")
	if result := client.Execute(context.Background(), operation, control.Input{}, nil, nil); result.Status != http.StatusUnauthorized {
		t.Fatal("catalog bypassed authentication")
	}
}

func TestRemoteCatalogRejectsInvalidDefinitions(t *testing.T) {
	for _, broken := range []control.Operation{
		{Name: "bad", Path: "https://evil.example/api/", Method: "GET", Transport: "json"},
		{Name: "bad", Path: "/api/v1/test", Method: "TRACE", Transport: "json"},
		{Name: "bad", Path: "/api/v1/test", Method: "GET", Transport: "local"},
	} {
		host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			json.NewEncoder(w).Encode(control.Catalog{Version: 1, Operations: []control.Operation{broken}})
		}))
		client, err := control.NewClient(control.ClientOptions{Server: host.URL, Development: true, StateDir: filepath.Join(t.TempDir(), "client")})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := client.Catalog(context.Background()); err == nil {
			t.Fatal("invalid catalog accepted")
		}
		client.Close()
		host.Close()
	}
}
