package agent

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/CST-Cat/NodeDance/internal/protocol"
	"github.com/coder/websocket"
)

func punchProbeStub(t *testing.T, credential string, respond func(r *http.Request) ([]byte, bool)) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != protocol.FilePunchProbeRoute || (credential != "" && r.Header.Get("Authorization") != "Bearer "+credential) {
			http.Error(w, "Agent credential rejected", http.StatusUnauthorized)
			return
		}
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{CompressionMode: websocket.CompressionDisabled})
		if err != nil {
			return
		}
		defer conn.CloseNow()
		payload, hold := respond(r)
		if payload != nil {
			writeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			err = conn.Write(writeCtx, websocket.MessageText, payload)
			cancel()
			if err != nil {
				return
			}
		}
		if hold {
			readCtx, cancelRead := context.WithTimeout(context.Background(), 5*time.Second)
			_, _, _ = conn.Read(readCtx)
			cancelRead()
		}
	}))
}

func TestProbeCoreMappingObservesPeerAndRebindsLocalPort(t *testing.T) {
	var observed string
	server := punchProbeStub(t, "probe-credential", func(r *http.Request) ([]byte, bool) {
		observed = r.RemoteAddr
		data, _ := json.Marshal(protocol.FilePunchObservation{RemoteAddr: observed})
		return data, true
	})
	defer server.Close()
	config := Config{Server: server.URL, Development: true, Credential: "probe-credential"}
	mapping, err := probeCoreMapping(context.Background(), &config, "task-probe")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = mapping.Listener.Close()
		_ = mapping.Stream.Close()
	}()
	if mapping.Public.String() != observed {
		t.Fatalf("observed mapping %s does not match the stub peer %s", mapping.Public, observed)
	}
	local := mapping.Stream.LocalAddr().(*net.TCPAddr)
	if mapping.Local.String() != local.String() || mapping.Listener.Addr().String() != local.String() {
		t.Fatalf("punch listener did not rebind the probe source port: local=%s listener=%s", mapping.Local, mapping.Listener.Addr())
	}
}

func TestProbeCoreMappingFailureModes(t *testing.T) {
	unobservable, _ := json.Marshal(protocol.FilePunchObservation{Error: protocol.FilePunchUnobservable})
	garbage := []byte("garbage")
	for _, mode := range []struct {
		name       string
		credential string
		client     string
		payload    []byte
		contains   string
	}{
		{name: "unobservable", credential: "probe-credential", client: "probe-credential", payload: unobservable, contains: "unobservable"},
		{name: "invalid_frame", credential: "probe-credential", client: "probe-credential", payload: garbage, contains: "invalid punch probe observation"},
		{name: "rejected_credential", credential: "probe-credential", client: "wrong-credential", contains: "401"},
		{name: "missing_observation", credential: "probe-credential", client: "probe-credential", payload: nil, contains: "EOF"},
	} {
		t.Run(mode.name, func(t *testing.T) {
			server := punchProbeStub(t, mode.credential, func(*http.Request) ([]byte, bool) {
				return mode.payload, mode.payload != nil
			})
			defer server.Close()
			config := Config{Server: server.URL, Development: true, Credential: mode.client}
			_, err := probeCoreMapping(context.Background(), &config, "task-probe")
			if err == nil || !strings.Contains(err.Error(), mode.contains) {
				t.Fatalf("expected %q failure, got %v", mode.contains, err)
			}
		})
	}
}
