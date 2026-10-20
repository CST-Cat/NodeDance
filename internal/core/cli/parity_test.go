package cli

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/CST-Cat/NodeDance/internal/control"
	"github.com/creack/pty"
)

type tuiStep struct{ await, input string }

func driveTerminal(t *testing.T, server string, steps []tuiStep) {
	t.Helper()
	master, slave, err := pty.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer master.Close()
	defer slave.Close()
	pty.Setsize(slave, &pty.Winsize{Rows: 30, Cols: 120})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- runTUI(ctx, []string{"--server", server, "--dev", "--state-dir", filepath.Join(t.TempDir(), "client")}, func(string) (string, bool) { return "", false }, slave, slave, slave, "parity-test")
	}()
	chunks := make(chan string, 128)
	go func() {
		buffer := make([]byte, 4096)
		for {
			n, err := master.Read(buffer)
			if n > 0 {
				select {
				case chunks <- string(buffer[:n]):
				case <-ctx.Done():
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()
	for _, step := range steps {
		var text strings.Builder
		timer := time.NewTimer(5 * time.Second)
		for !strings.Contains(text.String(), step.await) {
			select {
			case chunk := <-chunks:
				text.WriteString(chunk)
			case err := <-done:
				t.Fatalf("TUI exited before %q: %v", step.await, err)
			case <-timer.C:
				t.Fatalf("missing %q in %s", step.await, text.String())
			}
		}
		timer.Stop()
		io.WriteString(master, step.input+"\r")
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("TUI did not exit")
	}
}

func TestCLIAndTUIObserveTheSameDiscoveredTask(t *testing.T) {
	var submits, reads atomic.Int32
	o := control.Operation{Name: "future.submit", Description: "Future submission", Method: "POST", Path: "/api/v1/new-submit", Parameters: []string{}, Query: []string{}, Transport: "json", Idempotent: true,
		Completion: &control.CompletionContract{Operation: "future.observe", Params: map[string]string{"id": "data.id"}, StatusPath: "status", TerminalStatuses: []string{"succeeded", "unknown"}, SuccessStatuses: []string{"succeeded"}}}
	read := control.Operation{Name: "future.observe", Description: "Future observation", Method: "GET", Path: "/api/v1/new-observe/{id}", Parameters: []string{"id"}, Query: []string{}, Transport: "json"}
	host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/operations":
			json.NewEncoder(w).Encode(control.Catalog{Version: 1, Operations: []control.Operation{o, read}})
		case "/api/v1/auth/csrf":
			io.WriteString(w, `{"token":"test-csrf"}`)
		case o.Path:
			if r.Header.Get("Idempotency-Key") != "same-key" {
				t.Error("intent key lost")
			}
			submits.Add(1)
			io.WriteString(w, `{"id":"work","status":"queued"}`)
		case "/api/v1/new-observe/work":
			reads.Add(1)
			io.WriteString(w, `{"id":"work","status":"succeeded"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer host.Close()
	var output strings.Builder
	if err := RunWithInput(context.Background(), []string{"call", o.Name, "--wait", "--idempotency-key", "same-key", "--server", host.URL, "--dev", "--state-dir", filepath.Join(t.TempDir(), "cli")}, func(string) (string, bool) { return "", false }, strings.NewReader(""), &output, io.Discard, "test"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "queued") || !strings.Contains(output.String(), "succeeded") {
		t.Fatal("CLI lost acceptance or final result")
	}
	driveTerminal(t, host.URL, []tuiStep{
		{"Select:", "/future.submit"}, {"Filter: future.submit", "1"}, {"Idempotency key", "same-key"}, {"Wait for completion?", "y"}, {"Observation finished.", ""}, {"Select:", "q"},
	})
	if submits.Load() != 2 || reads.Load() != 2 {
		t.Fatal("terminal presentations did not share completion execution", submits.Load(), reads.Load())
	}
}

func TestTUILocalMaintenanceWorksWithoutCore(t *testing.T) {
	host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "unavailable", 503) }))
	defer host.Close()
	driveTerminal(t, host.URL, []tuiStep{
		{"Select:", "/core.version"}, {"Filter: core.version", "1"}, {"Arguments as JSON array", "[]"}, {"Enter to return:", ""}, {"Select:", "q"},
	})
}

func TestSharedTerminalCatalogueIncludesLocalWorkflows(t *testing.T) {
	catalog := control.Describe()
	catalog.Operations = append(catalog.Operations, control.Operation{Name: "future.inspect", Transport: "json"})
	seen := map[string]bool{}
	for _, o := range terminalOperations(catalog) {
		if seen[o.Name] {
			t.Fatal("ambiguous terminal operation", o.Name)
		}
		seen[o.Name] = true
	}
	for _, name := range []string{"future.inspect", "client.update", "core.backup", "core.restore", "agent.enroll", "agent.recover", "agent.tmux-version"} {
		if !seen[name] {
			t.Fatal("missing terminal capability", name)
		}
	}
}
