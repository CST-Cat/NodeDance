package cli

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"github.com/CST-Cat/NodeDance/internal/control"
	"github.com/CST-Cat/NodeDance/internal/update"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestLocalAgentUpdateEntrypointsShareRollbackCancellation(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("Agent maintenance requires root")
	}
	// No Agent binary or deployment connection is needed to cancel a local
	// rollback. Cancellation must happen before touching a managed service.
	t.Setenv("PATH", t.TempDir())
	for _, args := range [][]string{
		{"agent", "update", "--rollback", "unsubmitted"},
		{"call", "agent.update", "--rollback", "unsubmitted"},
	} {
		var output, stderr strings.Builder
		err := RunWithInput(context.Background(), args, func(string) (string, bool) { return "", false }, strings.NewReader("n\n"), &output, &stderr, "test")
		if err == nil || !strings.Contains(err.Error(), "rollback cancelled") {
			t.Fatalf("%v: expected local cancellation, got %v", args, err)
		}
		if output.Len() != 0 || !strings.Contains(stderr.String(), "[y/N]") {
			t.Fatalf("%v: unexpected cancellation output: %q / %q", args, output.String(), stderr.String())
		}
	}
}

func TestUpdateCLIUpdatesDirectlyAndConfirmsOnlyRollback(t *testing.T) {
	var submissions atomic.Int32
	host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/operations":
			catalog := control.Describe()
			for i := range catalog.Operations {
				if strings.HasPrefix(catalog.Operations[i].Name, "updates.") {
					catalog.Operations[i].Path = "/api/v1/discovered/" + strings.TrimPrefix(catalog.Operations[i].Name, "updates.")
				}
			}
			json.NewEncoder(w).Encode(catalog)
		case "/api/v1/discovered/check":
			json.NewEncoder(w).Encode(update.Check{Component: "core", Current: "core-v0.1.8", Latest: "core-v0.1.9", Available: true, Compatible: true, Configured: true, Release: update.Release{Manifest: update.Describe("core", "core-v0.1.9"), URL: "https://github.com/CST-Cat/NodeDance/releases/tag/core-v0.1.9"}})
		case "/api/v1/auth/csrf":
			io.WriteString(w, `{"token":"csrf"}`)
		case "/api/v1/discovered/apply":
			submissions.Add(1)
			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body)
			if body["confirm"] != true || body["version"] != "core-v0.1.9" || r.Header.Get("Idempotency-Key") == "" {
				t.Error("incorrect update confirmation or pinned release")
			}
			w.WriteHeader(202)
			io.WriteString(w, `{"id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","status":"queued"}`)
		case "/api/v1/discovered/status":
			io.WriteString(w, `{"id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","status":"succeeded","stage":"complete","finished":"2026-10-12T00:00:00Z"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer host.Close()
	base := []string{"update", "--server", host.URL, "--dev", "--state-dir", filepath.Join(t.TempDir(), "client")}
	lookup := func(string) (string, bool) { return "", false }
	for _, flags := range [][]string{{"--check"}, {}} {
		var out strings.Builder
		if err := RunWithInput(context.Background(), append(append([]string{}, base...), flags...), lookup, strings.NewReader(""), &out, io.Discard, "test"); err != nil {
			t.Fatal(err)
		}
	}
	if submissions.Load() != 1 {
		t.Fatalf("submitted %d updates", submissions.Load())
	}
	if err := RunWithInput(context.Background(), append(base, "--rollback", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"), lookup, strings.NewReader("n\n"), io.Discard, io.Discard, "test"); err == nil || !strings.Contains(err.Error(), "rollback cancelled") {
		t.Fatal("declined rollback succeeded")
	}
	if submissions.Load() != 1 {
		t.Fatal("declined rollback was submitted")
	}
	driveTerminal(t, host.URL, []tuiStep{
		{"Select:", "/client.update"}, {"Filter: client.update", "1"}, {"Arguments as JSON array", "[]"}, {"Enter to return:", ""}, {"Select:", "q"},
	})
	if submissions.Load() != 2 {
		t.Fatal("TUI did not update directly through the shared workflow")
	}
}

func TestDefaultUpdateUsesSavedServerSessionAndPrivateCAWithoutArguments(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", home)
	var submitted atomic.Int32
	host := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/auth/csrf":
			io.WriteString(w, `{"token":"fixture-csrf"}`)
		case "/api/v1/auth/login":
			http.SetCookie(w, &http.Cookie{Name: "nodedance_session", Value: "isolated-session", Path: "/", Secure: true, HttpOnly: true})
			io.WriteString(w, `{"user":{"displayName":"Fixture administrator"},"csrfToken":"fixture-csrf"}`)
		default:
			if cookie, err := r.Cookie("nodedance_session"); err != nil || cookie.Value != "isolated-session" {
				w.WriteHeader(401)
				return
			}
			switch r.URL.Path {
			case "/api/v1/operations":
				json.NewEncoder(w).Encode(control.Describe())
			case "/api/v1/updates/check":
				json.NewEncoder(w).Encode(update.Check{Component: "core", Current: "core-v0.1.8", Latest: "core-v0.1.9", Available: true, Configured: true, Compatible: true})
			case "/api/v1/updates/apply":
				submitted.Add(1)
				w.WriteHeader(202)
				io.WriteString(w, `{"id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","status":"queued"}`)
			case "/api/v1/updates/status":
				io.WriteString(w, `{"id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","status":"succeeded","finished":"2026-10-13T00:00:00Z"}`)
			default:
				http.NotFound(w, r)
			}
		}
	}))
	defer host.Close()
	ca := filepath.Join(home, "private-ca.pem")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: host.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(home, "nodedance-client")
	client, err := control.NewClient(control.ClientOptions{Server: host.URL, StateDir: state, CAFile: ca})
	if err != nil {
		t.Fatal(err)
	}
	login, _ := control.FindIn(control.Describe(), "auth.login")
	result := client.Execute(context.Background(), login, control.Input{Body: json.RawMessage(`{"username":"fixture-admin","password":"fixture-password"}`)}, nil, nil)
	client.Close()
	if err = result.Err(); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(filepath.Join(state, "session.json")); err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("saved connection lost private session permissions")
	}
	lookup := func(string) (string, bool) { return "", false }
	if err := RunWithInput(context.Background(), []string{"update"}, lookup, strings.NewReader(""), io.Discard, io.Discard, "test"); err != nil {
		t.Fatal(err)
	}
	if submitted.Load() != 1 {
		t.Fatalf("default update submitted %d jobs", submitted.Load())
	}
	// An explicit target must never receive a different server's saved cookies.
	if _, err = control.NewClient(control.ClientOptions{Server: "https://other.example", StateDir: state, UseSavedConnection: true}); err == nil {
		t.Fatal("saved administrator identity crossed server boundary")
	}
}
