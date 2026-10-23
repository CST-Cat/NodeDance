package cli

import (
	"context"
	"encoding/json"
	"github.com/CST-Cat/NodeDance/internal/core/server"
	"io"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func TestLocationsCLIAndTUIUseRealCore(t *testing.T) {
	core, err := server.New("test", server.Options{DataDir: t.TempDir(), Development: true})
	if err != nil {
		t.Fatal(err)
	}
	defer core.Close()
	if err = core.BootstrapAdministrator(context.Background(), "location-admin", "location-password-strong"); err != nil {
		t.Fatal(err)
	}
	host := httptest.NewServer(core)
	defer host.Close()
	state := filepath.Join(t.TempDir(), "cli")
	invoke := func(args []string, body string) (string, error) {
		t.Helper()
		var out strings.Builder
		args = append(args, "--server", host.URL, "--dev", "--state-dir", state)
		err := RunWithInput(context.Background(), args, func(string) (string, bool) { return "", false }, strings.NewReader(body), &out, io.Discard, "test")
		return out.String(), err
	}
	must := func(args []string, body string) string {
		t.Helper()
		out, err := invoke(args, body)
		if err != nil {
			t.Fatal(err, out)
		}
		return out
	}
	must([]string{"auth", "login", "--body", "-"}, `{"username":"location-admin","password":"location-password-strong"}`)
	out := must([]string{"call", "locations.search", "--query", `{"q":"洛杉矶","countryCode":"US"}`}, "")
	if !strings.Contains(out, "Los Angeles") {
		t.Fatal(out)
	}
	out = must([]string{"call", "agents.enrollment", "--body", "-"}, `{"displayName":"location-fixture","coreUrl":"`+host.URL+`"}`)
	var enrollment struct {
		Data struct {
			NodeID string `json:"nodeId"`
		}
	}
	if err = json.Unmarshal([]byte(out), &enrollment); err != nil || enrollment.Data.NodeID == "" {
		t.Fatal(err, out)
	}
	node := enrollment.Data.NodeID
	params := `{"nodeId":"` + node + `"}`
	manual := `{"targetKind":"node","identity":"node:` + node + `","visible":true,"location":{"countryCode":"US","city":"CLI Los Angeles","region":"California","latitude":34.0522,"longitude":-118.2437}}`
	must([]string{"call", "nodes.save-preference", "--params", params, "--body", "-"}, manual)
	out = must([]string{"call", "nodes.list"}, "")
	if !strings.Contains(out, "CLI Los Angeles") || !strings.Contains(out, `"source":"manual"`) {
		t.Fatal(out)
	}
	if out, err = invoke([]string{"call", "nodes.save-preference", "--params", params, "--body", "-"}, strings.Replace(manual, "34.0522", "91", 1)); err == nil {
		t.Fatal("invalid latitude accepted", out)
	}
	// A real terminal discovers the new directory operation and submits the same
	// nested position body through the shared operation executor.
	tuiBody := strings.ReplaceAll(manual, "CLI Los Angeles", "TUI New York")
	tuiBody = strings.ReplaceAll(strings.ReplaceAll(tuiBody, "34.0522", "40.7128"), "-118.2437", "-74.006")
	driveTerminal(t, host.URL, []tuiStep{{"Select:", "/auth.login"}, {"Filter: auth.login", "1"}, {"Administrator username", "location-admin"}, {"Password (hidden)", "location-password-strong"}, {"Enter to return:", ""}, {"Select:", "/locations.get"}, {"Filter: locations.get", "1"}, {"id [", "5368361"}, {"Enter to return:", ""}, {"Select:", "/nodes.save-preference"}, {"Filter: nodes.save-preference", "1"}, {"nodeId [", node}, {"JSON body or @private-file", tuiBody}, {"Enter to return:", ""}, {"Select:", "q"}})
	out = must([]string{"call", "nodes.list"}, "")
	if !strings.Contains(out, "TUI New York") || !strings.Contains(out, "40.7128") {
		t.Fatal(out)
	}
	// JSONL dry run uses the discovered nested schema and cannot partially mutate.
	batch := `{"operation":"nodes.save-preference","params":` + params + `,"body":` + manual + `}` + "\n"
	must([]string{"batch", "--input", "-", "--dry-run"}, batch)
	out = must([]string{"call", "nodes.list"}, "")
	if !strings.Contains(out, "TUI New York") {
		t.Fatal("dry run mutated position", out)
	}
	clear := `{"targetKind":"node","identity":"node:` + node + `","visible":true,"location":null}`
	driveTerminal(t, host.URL, []tuiStep{{"Select:", "/auth.login"}, {"Filter: auth.login", "1"}, {"Administrator username", "location-admin"}, {"Password (hidden)", "location-password-strong"}, {"Enter to return:", ""}, {"Select:", "/nodes.save-preference"}, {"Filter: nodes.save-preference", "1"}, {"nodeId [", node}, {"JSON body or @private-file", clear}, {"Enter to return:", ""}, {"Select:", "q"}})
	out = must([]string{"call", "nodes.list"}, "")
	if strings.Contains(out, "TUI New York") {
		t.Fatal("TUI failed to restore automatic location", out)
	}
	must([]string{"batch", "--input", "-"}, batch)
	out = must([]string{"call", "nodes.list"}, "")
	if !strings.Contains(out, "CLI Los Angeles") {
		t.Fatal("batch did not apply location", out)
	}
}
