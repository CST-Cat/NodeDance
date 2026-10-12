package cli

import (
	"context"
	"encoding/json"
	"github.com/CST-Cat/NodeDance/internal/control"
	"github.com/CST-Cat/NodeDance/internal/update"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

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
