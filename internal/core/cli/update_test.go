package cli

import (
	"context"
	"encoding/json"
	"github.com/CST-Cat/NodeDance/internal/control"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func TestUpdateCLIUsesDiscoveredRoutesAndConfirmation(t *testing.T) {
	submissions := 0
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
			io.WriteString(w, `{"component":"core","current":"core-v0.1.8","latest":"core-v0.1.9","available":true,"compatible":true,"configured":true}`)
		case "/api/v1/auth/csrf":
			io.WriteString(w, `{"token":"csrf"}`)
		case "/api/v1/discovered/apply":
			submissions++
			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body)
			if body["confirm"] != true || body["version"] != "core-v0.1.9" || r.Header.Get("Idempotency-Key") == "" {
				t.Error("incorrect update confirmation or pinned release")
			}
			w.WriteHeader(202)
			io.WriteString(w, `{"id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","status":"queued"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer host.Close()
	base := []string{"update", "--server", host.URL, "--dev", "--state-dir", filepath.Join(t.TempDir(), "client")}
	lookup := func(string) (string, bool) { return "", false }
	for _, flags := range [][]string{{"--check"}, {"--yes", "--no-wait"}} {
		var out strings.Builder
		if err := RunWithInput(context.Background(), append(append([]string{}, base...), flags...), lookup, strings.NewReader(""), &out, io.Discard, "test"); err != nil {
			t.Fatal(err)
		}
	}
	if submissions != 1 {
		t.Fatalf("submitted %d updates", submissions)
	}
	if err := RunWithInput(context.Background(), base, lookup, strings.NewReader("n\n"), io.Discard, io.Discard, "test"); err == nil {
		t.Fatal("declined update succeeded")
	}
	if submissions != 1 {
		t.Fatal("declined update was submitted")
	}
}
