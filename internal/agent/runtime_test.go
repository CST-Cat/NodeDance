package agent

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/CST-Cat/NodeDance/internal/protocol"
	"github.com/coder/websocket"
)

func TestRuntimeHelloOffersUpdateCapability(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("Agent runtime requires root")
	}
	hellos := make(chan protocol.Hello, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.CloseNow()
		_, raw, err := conn.Read(r.Context())
		if err != nil {
			t.Error(err)
			return
		}
		var envelope protocol.Envelope
		var hello protocol.Hello
		if json.Unmarshal(raw, &envelope) != nil || envelope.Type != protocol.TypeHello || json.Unmarshal(envelope.Payload, &hello) != nil {
			t.Error("invalid runtime hello")
			return
		}
		hellos <- hello
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// Capture the real runtime handshake, then let the mock close before welcome.
	_ = runConnection(ctx, filepath.Join(t.TempDir(), "agent.json"), Config{Server: server.URL, Development: true, Credential: "test-credential", AgentID: "test-agent", NodeID: "test-node"}, "agent-v0.1.7", nil, nil, nil, nil, nil, nil)
	select {
	case hello := <-hellos:
		if !containsCapability(protocol.NegotiateCapabilities(hello.Capabilities), protocol.CapabilityUpdate) {
			t.Fatal("runtime update requests cannot be enabled without advertising the update capability")
		}
	case <-ctx.Done():
		t.Fatal("runtime did not send hello")
	}
}

func TestAgentRootRequirement(t *testing.T) {
	for _, operation := range []string{"enrollment", "recovery", "runtime"} {
		if err := agentRootRequirement(0, operation); err != nil {
			t.Errorf("root %s was rejected: %v", operation, err)
		}
		if err := agentRootRequirement(1000, operation); err == nil || !strings.Contains(err.Error(), "requires root privileges") {
			t.Errorf("non-root %s was accepted: %v", operation, err)
		}
	}
}

func TestAgentHostManagementEntrypointsRejectNonRoot(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("non-root entrypoint checks require a non-root test process")
	}
	configPath := filepath.Join(t.TempDir(), "missing-agent.json")
	cases := []struct {
		name string
		call func() error
	}{
		{
			name: "enroll",
			call: func() error {
				return Enroll(context.Background(), "http://127.0.0.1:8180", "", true, strings.NewReader(""), configPath)
			},
		},
		{
			name: "recover",
			call: func() error { return Recover(context.Background(), configPath) },
		},
		{
			name: "run",
			call: func() error { return Run(context.Background(), configPath, "test", io.Discard) },
		},
		{
			name: "file service",
			call: func() error {
				service, err := openAgentFileService(configPath)
				if service != nil {
					_ = service.Close()
				}
				return err
			},
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if err := testCase.call(); err == nil || !strings.Contains(err.Error(), "requires root privileges") {
				t.Fatalf("non-root %s entrypoint was accepted: %v", testCase.name, err)
			}
		})
	}
}

func TestAgentWebSocketURLMapsCoreOriginScheme(t *testing.T) {
	tests := []struct {
		server string
		want   string
	}{
		{server: "https://core.example:8443", want: "wss://core.example:8443/ws/v1/agent"},
		{server: "http://127.0.0.1:8180", want: "ws://127.0.0.1:8180/ws/v1/agent"},
	}
	for _, test := range tests {
		got, err := agentWebSocketURL(test.server)
		if err != nil {
			t.Fatalf("agentWebSocketURL(%q): %v", test.server, err)
		}
		if got != test.want {
			t.Errorf("agentWebSocketURL(%q) = %q, want %q", test.server, got, test.want)
		}
	}
}
