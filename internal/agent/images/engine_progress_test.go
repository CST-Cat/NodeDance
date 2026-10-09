package images

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/moby/moby/client"
)

func newSDKProgressTestEngine(t *testing.T, handler http.Handler) *SDKEngine {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	host := strings.TrimPrefix(server.URL, "http://")
	cli, err := client.New(client.WithHost("tcp://"+host), client.WithScheme("http"), client.WithAPIVersion("1.51"))
	if err != nil {
		t.Fatalf("create test Docker client: %v", err)
	}
	t.Cleanup(func() { _ = cli.Close() })
	return &SDKEngine{client: cli}
}

func writeSDKProgressStream(t *testing.T, handler http.HandlerFunc, body string) *SDKEngine {
	t.Helper()
	return newSDKProgressTestEngine(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1.51/images/create" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		handler(w, r)
		_, _ = fmt.Fprint(w, body)
	}))
}

func TestSDKEnginePullReportsMonotonicLayerByteProgress(t *testing.T) {
	engine := writeSDKProgressStream(t, func(http.ResponseWriter, *http.Request) {}, strings.Join([]string{
		`{"status":"Downloading","id":"layer-a","progressDetail":{"current":4,"total":10}}`,
		`{"status":"Downloading","id":"layer-b","progressDetail":{"current":3,"total":5}}`,
		`{"status":"Downloading","id":"layer-a","progressDetail":{"current":8,"total":10}}`,
		// Stale and duplicate updates must not move the aggregate backwards or
		// generate duplicate progress callbacks.
		`{"status":"Downloading","id":"layer-a","progressDetail":{"current":3,"total":10}}`,
		`{"status":"Downloading","id":"layer-a","progressDetail":{"current":8,"total":10}}`,
		// Invalid counters and status-only messages are not byte evidence.
		`{"status":"Downloading"}`,
		`{"status":"Downloading","id":"layer-b","progressDetail":{"current":20,"total":5}}`,
	}, "\n"))
	var got []PullProgress
	if err := engine.Pull(context.Background(), "alpine:latest", "", func(value PullProgress) { got = append(got, value) }); err != nil {
		t.Fatalf("consume SDK pull stream: %v", err)
	}
	want := []PullProgress{{Completed: 4, Total: 10}, {Completed: 7, Total: 15}, {Completed: 11, Total: 15}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("reported pull progress = %#v, want %#v", got, want)
	}
	for _, value := range got {
		if value.Completed > value.Total {
			t.Fatalf("completed bytes exceed total: %+v", value)
		}
	}
}

func TestSDKEnginePullWaitsForTotalBeforeReportingBytes(t *testing.T) {
	engine := writeSDKProgressStream(t, func(http.ResponseWriter, *http.Request) {}, strings.Join([]string{
		`{"status":"Downloading","id":"layer-a","progressDetail":{"current":4}}`,
		`{"status":"Downloading","id":"layer-a","progressDetail":{"current":6,"total":10}}`,
	}, "\n"))
	var got []PullProgress
	if err := engine.Pull(context.Background(), "alpine:latest", "", func(value PullProgress) { got = append(got, value) }); err != nil {
		t.Fatalf("consume SDK pull stream: %v", err)
	}
	want := []PullProgress{{Completed: 6, Total: 10}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("reported pull progress = %#v, want %#v", got, want)
	}
}

func TestSDKEnginePullReturnsStreamErrorAndCancellation(t *testing.T) {
	t.Run("malformed stream after progress", func(t *testing.T) {
		engine := writeSDKProgressStream(t, func(http.ResponseWriter, *http.Request) {}, strings.Join([]string{
			`{"status":"Downloading","id":"layer-a","progressDetail":{"current":1,"total":10}}`,
			`{"id":`,
		}, "\n"))
		var got []PullProgress
		err := engine.Pull(context.Background(), "alpine:latest", "", func(value PullProgress) { got = append(got, value) })
		if err == nil || !strings.Contains(err.Error(), "stream was interrupted") {
			t.Fatalf("malformed SDK pull stream error = %v", err)
		}
		if !reflect.DeepEqual(got, []PullProgress{{Completed: 1, Total: 10}}) {
			t.Fatalf("progress before stream error = %#v", got)
		}
	})

	t.Run("cancel while stream is open", func(t *testing.T) {
		started := make(chan struct{})
		engine := newSDKProgressTestEngine(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost || r.URL.Path != "/v1.51/images/create" {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprintln(w, `{"status":"Downloading","id":"layer-a","progressDetail":{"current":1,"total":10}}`)
			if flusher, ok := w.(http.Flusher); ok {
				flusher.Flush()
			}
			close(started)
			<-r.Context().Done()
		}))
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		progress := make(chan PullProgress, 1)
		finished := make(chan error, 1)
		go func() {
			finished <- engine.Pull(ctx, "alpine:latest", "", func(value PullProgress) { progress <- value })
		}()
		select {
		case value := <-progress:
			if value.Completed != 1 || value.Total != 10 {
				t.Fatalf("first SDK progress = %+v", value)
			}
		case <-started:
			select {
			case value := <-progress:
				if value.Completed != 1 || value.Total != 10 {
					t.Fatalf("first SDK progress = %+v", value)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("SDK did not decode the flushed progress detail")
			}
		case <-time.After(2 * time.Second):
			t.Fatal("Docker pull request did not reach the test server")
		}
		cancel()
		select {
		case err := <-finished:
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("canceled SDK pull error = %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("SDK pull did not stop after cancellation")
		}
	})
}
