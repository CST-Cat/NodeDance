package containerstreams

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestSDKStreamingRequestsHaveHeaderDeadlineButNoBodyDeadline(t *testing.T) {
	t.Run("inspect response body is bounded independently", func(t *testing.T) {
		fixture := newDockerStreamHTTPFixture(t, httpFixtureStallInspectBody)
		engine, err := newSDKEngine("unix://"+fixture.socket, time.Second, 150*time.Millisecond)
		if err != nil {
			t.Fatal(err)
		}
		defer engine.Close()
		started := time.Now()
		_, err = OpenLogs(context.Background(), engine, statsTestID, LogsOptions{ShowStdout: true})
		if err == nil {
			t.Fatal("OpenLogs unexpectedly succeeded after stalled Inspect body")
		}
		if elapsed := time.Since(started); elapsed > time.Second {
			t.Fatalf("Inspect body stalled for %s, exceeded the complete-request deadline", elapsed)
		}
		if fixture.logRequests.Load() != 0 {
			t.Fatal("OpenLogs requested logs after its Inspect response body stalled")
		}
	})

	t.Run("logs response headers are bounded", func(t *testing.T) {
		fixture := newDockerStreamHTTPFixture(t, httpFixtureStallLogHeaders)
		engine, err := newSDKEngine("unix://"+fixture.socket, 120*time.Millisecond, time.Second)
		if err != nil {
			t.Fatal(err)
		}
		defer engine.Close()
		started := time.Now()
		_, err = OpenLogs(context.Background(), engine, statsTestID, LogsOptions{ShowStdout: true, Follow: true})
		if err == nil {
			t.Fatal("OpenLogs unexpectedly returned a stalled response")
		}
		if elapsed := time.Since(started); elapsed > time.Second {
			t.Fatalf("SDK response headers took %s, exceeded the bounded-header budget", elapsed)
		}
	})

	t.Run("log and stats bodies outlive response header deadline", func(t *testing.T) {
		fixture := newDockerStreamHTTPFixture(t, httpFixtureLongStreams)
		engine, err := newSDKEngine("unix://"+fixture.socket, 100*time.Millisecond, time.Second)
		if err != nil {
			t.Fatal(err)
		}
		defer engine.Close()

		logs, err := OpenLogs(context.Background(), engine, statsTestID, LogsOptions{Tail: "0", Follow: true, ShowStdout: true})
		if err != nil {
			t.Fatalf("open long-follow logs: %v", err)
		}
		firstLog := nextFrame(t, logs)
		if string(firstLog.Data) != "first" {
			t.Fatalf("first log frame = %q", firstLog.Data)
		}
		secondLog := nextFrame(t, logs)
		if string(secondLog.Data) != "second" {
			t.Fatalf("second log frame = %q, expected body to remain live after response-header deadline", secondLog.Data)
		}
		if err := logs.Close(); err != nil {
			t.Fatal(err)
		}

		statsBody, err := engine.OpenStats(context.Background(), statsTestID)
		if err != nil {
			t.Fatalf("open long stats stream: %v", err)
		}
		decoder := json.NewDecoder(statsBody)
		var first map[string]any
		if err := decoder.Decode(&first); err != nil {
			t.Fatalf("read first stats sample: %v", err)
		}
		var second map[string]any
		if err := decoder.Decode(&second); err != nil {
			t.Fatalf("read second stats sample after header timeout: %v", err)
		}
		if first["marker"] != "first" || second["marker"] != "second" {
			t.Fatalf("stats stream markers = %v, %v", first["marker"], second["marker"])
		}
		if err := statsBody.Close(); err != nil {
			t.Errorf("close stats body: %v", err)
		}
	})
}

func nextFrame(t *testing.T, reader *LogReader) LogFrame {
	t.Helper()
	select {
	case frame, ok := <-reader.Frames:
		if !ok {
			t.Fatal("log reader closed before expected frame")
			return LogFrame{}
		}
		return frame
	case err, ok := <-reader.Errors:
		if ok && err != nil {
			t.Fatalf("log reader error: %v", err)
		}
		t.Fatal("log reader ended before expected frame")
		return LogFrame{}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for streamed log frame")
		return LogFrame{}
	}
}

type dockerFixtureMode int

const (
	httpFixtureStallLogHeaders dockerFixtureMode = iota
	httpFixtureStallInspectBody
	httpFixtureLongStreams
)

type dockerStreamHTTPFixture struct {
	socket      string
	logRequests *atomic.Int32
}

func newDockerStreamHTTPFixture(t *testing.T, mode dockerFixtureMode) dockerStreamHTTPFixture {
	t.Helper()
	socket := filepath.Join(t.TempDir(), "docker.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	logRequests := &atomic.Int32{}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/_ping" {
			w.Header().Set("API-Version", "1.45")
			w.Header().Set("OSType", "linux")
			w.WriteHeader(http.StatusOK)
			return
		}
		if strings.Contains(r.URL.Path, "/containers/") && strings.HasSuffix(r.URL.Path, "/json") {
			w.Header().Set("Content-Type", "application/json")
			if mode == httpFixtureStallInspectBody {
				w.WriteHeader(http.StatusOK)
				if flusher, ok := w.(http.Flusher); ok {
					flusher.Flush()
				}
				<-r.Context().Done()
				return
			}
			_, _ = io.WriteString(w, `{"Id":"`+statsTestID+`","Name":"/fixture","Config":{"Tty":false}}`)
			return
		}
		if strings.Contains(r.URL.Path, "/containers/") && strings.HasSuffix(r.URL.Path, "/logs") {
			logRequests.Add(1)
			if mode == httpFixtureStallLogHeaders {
				<-r.Context().Done()
				return
			}
			w.Header().Set("Content-Type", "application/vnd.docker.raw-stream")
			w.WriteHeader(http.StatusOK)
			flusher, _ := w.(http.Flusher)
			_, _ = w.Write(fixtureLogFrame(1, []byte("first")))
			if flusher != nil {
				flusher.Flush()
			}
			select {
			case <-r.Context().Done():
				return
			case <-time.After(250 * time.Millisecond):
			}
			_, _ = w.Write(fixtureLogFrame(1, []byte("second")))
			if flusher != nil {
				flusher.Flush()
			}
			<-r.Context().Done()
			return
		}
		if strings.Contains(r.URL.Path, "/containers/") && strings.HasSuffix(r.URL.Path, "/stats") {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			flusher, _ := w.(http.Flusher)
			_, _ = io.WriteString(w, `{"marker":"first"}`+"\n")
			if flusher != nil {
				flusher.Flush()
			}
			select {
			case <-r.Context().Done():
				return
			case <-time.After(250 * time.Millisecond):
			}
			_, _ = io.WriteString(w, `{"marker":"second"}`+"\n")
			if flusher != nil {
				flusher.Flush()
			}
			<-r.Context().Done()
			return
		}
		http.NotFound(w, r)
	})}
	serverDone := make(chan error, 1)
	go func() { serverDone <- server.Serve(listener) }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := server.Shutdown(ctx); err != nil {
			_ = server.Close()
			t.Errorf("shutdown Docker API fixture: %v", err)
		}
		select {
		case err := <-serverDone:
			if err != nil && err != http.ErrServerClosed {
				t.Errorf("Docker API fixture: %v", err)
			}
		case <-time.After(time.Second):
			t.Error("Docker API fixture did not stop")
		}
	})
	return dockerStreamHTTPFixture{socket: socket, logRequests: logRequests}
}

func fixtureLogFrame(stream byte, data []byte) []byte {
	frame := make([]byte, 8+len(data))
	frame[0] = stream
	binary.BigEndian.PutUint32(frame[4:8], uint32(len(data)))
	copy(frame[8:], data)
	return frame
}
