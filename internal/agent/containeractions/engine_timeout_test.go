package containeractions

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/CST-Cat/NodeDance/internal/agent/taskjournal"
	"github.com/CST-Cat/NodeDance/internal/taskstate"
)

func TestNewSDKEngineHasWholeRequestTimeout(t *testing.T) {
	engine, err := NewSDKEngine("unix:///tmp/nodedance-does-not-connect.sock")
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	if engine.requestTimeout != defaultSDKRequestTimeout || engine.requestTimeout <= 0 {
		t.Fatalf("SDK HTTP timeout = %s, want bounded default %s", engine.requestTimeout, defaultSDKRequestTimeout)
	}
}

func TestSDKAndPreflightInspectTimeoutOnUnixHTTPHeadersAndBody(t *testing.T) {
	for _, mode := range []string{"headers", "body"} {
		t.Run(mode, func(t *testing.T) {
			socketPath, requests := startStallingDockerHTTPFixture(t, mode)
			host := "unix://" + socketPath

			// First prove the real Moby SDK HTTP client has a whole-request bound
			// even when its caller supplies context.Background.
			sdk, err := newSDKEngine(host, 150*time.Millisecond)
			if err != nil {
				t.Fatal(err)
			}
			started := time.Now()
			_, inspectErr := sdk.Inspect(context.Background(), testContainerID)
			directElapsed := time.Since(started)
			if inspectErr == nil {
				t.Fatal("stalled Unix HTTP fixture unexpectedly returned an Inspect result")
			}
			if directElapsed > 900*time.Millisecond {
				t.Fatalf("real SDK Inspect took %s, exceeded the request timeout bound", directElapsed)
			}
			if err := sdk.Close(); err != nil {
				t.Errorf("close bounded SDK: %v", err)
			}

			// Give the SDK a longer bound than the executor's preflight bound.
			// Execute(context.Background()) must still persist a confirmed
			// preflight failure instead of waiting forever or sending Start.
			boundedSDK, err := newSDKEngine(host, 5*time.Second)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := boundedSDK.Close(); err != nil {
					t.Errorf("close bounded executor SDK: %v", err)
				}
			})
			store := openTimeoutTestJournal(t)
			t.Cleanup(func() {
				if err := store.Close(); err != nil {
					t.Errorf("close timeout-test journal: %v", err)
				}
			})
			executor, err := New(boundedSDK, store, Options{
				OperationTimeout: 2 * time.Second, VerificationTimeout: time.Second,
			})
			if err != nil {
				t.Fatal(err)
			}
			req := request(ActionStart)
			started = time.Now()
			task, executeErr := executor.Execute(context.Background(), req)
			executeElapsed := time.Since(started)
			if executeErr != nil {
				t.Fatalf("Execute() error = %v", executeErr)
			}
			if executeElapsed > 2*time.Second {
				t.Fatalf("preflight inspection took %s, exceeded the one-second bound plus journal margin", executeElapsed)
			}
			if task.Status != taskstate.Failed || !task.Evidence.FailureConfirmed || !task.Evidence.ActualResultConfirmed {
				t.Fatalf("journal result = status %s evidence %+v, want confirmed preflight failure", task.Status, task.Evidence)
			}
			stored, err := store.Get(context.Background(), req.TaskID)
			if err != nil || stored.Status != taskstate.Failed {
				t.Fatalf("durable journal state = %s, error %v", stored.Status, err)
			}
			for _, request := range requests() {
				if strings.HasPrefix(request, "POST ") || strings.HasPrefix(request, "DELETE ") || strings.HasPrefix(request, "PUT ") {
					t.Fatalf("preflight timeout sent a Docker mutation: %s", request)
				}
			}
		})
	}
}

func startStallingDockerHTTPFixture(t *testing.T, mode string) (string, func() []string) {
	t.Helper()
	socketPath := filepath.Join(t.TempDir(), "docker.sock")
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	seen := make([]string, 0, 8)
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.Method+" "+r.URL.Path)
		mu.Unlock()
		if r.URL.Path == "/_ping" || strings.HasSuffix(r.URL.Path, "/_ping") {
			if mode == "headers" {
				<-r.Context().Done()
				return
			}
			w.Header().Set("API-Version", "1.45")
			w.Header().Set("OSType", "linux")
			w.WriteHeader(http.StatusOK)
			return
		}
		if strings.Contains(r.URL.Path, "/containers/") && strings.HasSuffix(r.URL.Path, "/json") {
			if mode == "headers" {
				<-r.Context().Done()
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			flusher, ok := w.(http.Flusher)
			if ok {
				flusher.Flush()
			}
			_, _ = io.WriteString(w, fmt.Sprintf(`{"Id":%q,"Name":"/stalled","State":{`, testContainerID))
			if ok {
				flusher.Flush()
			}
			<-r.Context().Done()
			return
		}
		http.NotFound(w, r)
	})}
	serverErr := make(chan error, 1)
	go func() { serverErr <- server.Serve(listener) }()
	t.Cleanup(func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := server.Shutdown(closeCtx); err != nil {
			_ = server.Close()
			t.Errorf("stop Unix HTTP fixture: %v", err)
		}
		select {
		case err := <-serverErr:
			if err != nil && err != http.ErrServerClosed {
				t.Errorf("Unix HTTP fixture server: %v", err)
			}
		case <-time.After(time.Second):
			t.Error("Unix HTTP fixture server did not stop within one second")
		}
	})
	return socketPath, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), seen...)
	}
}

func openTimeoutTestJournal(t *testing.T) *taskjournal.Store {
	t.Helper()
	parent := t.TempDir()
	if err := os.Chmod(parent, 0o700); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(parent, "journal")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	store, err := taskjournal.Open(context.Background(), filepath.Join(dir, "tasks.db"), "node-test")
	if err != nil {
		t.Fatal(err)
	}
	return store
}
