package cli

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/CST-Cat/NodeDance/internal/control"
	"github.com/CST-Cat/NodeDance/internal/core/guest"
)

func TestCLIAndTUIPublishIdenticalDiscoveredGuestPolicy(t *testing.T) {
	op, err := control.FindIn(control.Describe(), "guest.configure")
	if err != nil {
		t.Fatal(err)
	}
	policy := guest.Defaults()
	policy.Enabled = true
	policy.Metrics = []string{"cpu.usage_percent"}
	policy.HistoryHours = 6
	policy.Nodes = []guest.Resource{{ID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", Name: strings.Repeat("🌱", 80)}}
	body, _ := json.Marshal(policy)
	var submissions atomic.Int32
	host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/operations":
			json.NewEncoder(w).Encode(control.Catalog{Version: 1, Operations: []control.Operation{op}})
		case "/api/v1/auth/csrf":
			io.WriteString(w, `{"token":"csrf"}`)
		case op.Path:
			raw, _ := io.ReadAll(r.Body)
			if string(raw) != string(body) {
				t.Errorf("presentation changed policy: %s", raw)
			}
			if r.Method != "PUT" {
				t.Error(r.Method)
			}
			submissions.Add(1)
			json.NewEncoder(w).Encode(policy)
		default:
			http.NotFound(w, r)
		}
	}))
	defer host.Close()
	if err = RunWithInput(context.Background(), []string{"call", "guest.configure", "--server", host.URL, "--dev", "--state-dir", filepath.Join(t.TempDir(), "session"), "--body", "-"}, func(string) (string, bool) { return "", false }, strings.NewReader(string(body)), io.Discard, io.Discard, "test"); err != nil {
		t.Fatal(err)
	}
	input := filepath.Join(t.TempDir(), "policy.json")
	if err = os.WriteFile(input, body, 0600); err != nil {
		t.Fatal(err)
	}
	driveTerminal(t, host.URL, []tuiStep{{"Select:", "/guest.configure"}, {"Filter: guest.configure", "1"}, {"JSON body or @private-file", "@" + input}, {"Enter: more, q: stop:", ""}, {"Enter to return:", ""}, {"Select:", "q"}})
	if submissions.Load() != 2 {
		t.Fatal("missing terminal presentation", submissions.Load())
	}
}
