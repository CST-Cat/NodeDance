package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/CST-Cat/NodeDance/internal/core/config"
	agentruntime "github.com/CST-Cat/NodeDance/internal/agent"
	"github.com/CST-Cat/NodeDance/internal/core/auth"
	"github.com/CST-Cat/NodeDance/internal/protocol"
	"github.com/coder/websocket"
)

func punchProbeCredentialForTest(t *testing.T, core *Server) string {
	t.Helper()
	ctx := context.Background()
	enrollment, err := core.agents.CreateEnrollment(ctx, "punch-probe-test", "127.0.0.1", sql.NullInt64{})
	if err != nil {
		t.Fatal(err)
	}
	credential, err := agentruntime.NewCredential()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := core.agents.ConsumeEnrollment(ctx, auth.DigestToken(enrollment.Token), auth.DigestToken(credential), mustPunchProbeRequestID(t), "127.0.0.1"); err != nil {
		t.Fatal(err)
	}
	return credential
}

func mustPunchProbeRequestID(t *testing.T) string {
	t.Helper()
	requestID, err := agentruntime.NewRequestID()
	if err != nil {
		t.Fatal(err)
	}
	return requestID
}

func TestPunchProbeRejectsMissingOrInvalidCredential(t *testing.T) {
	core, err := New("test", Options{DataDir: t.TempDir(), Development: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = core.Close() })
	coreHTTP := httptest.NewServer(core)
	t.Cleanup(coreHTTP.Close)
	endpoint := "ws" + strings.TrimPrefix(coreHTTP.URL, "http") + protocol.FilePunchProbeRoute
	for _, header := range []http.Header{{}, {"Authorization": {"Bearer not-hex"}}, {"Authorization": {"Task deadbeef"}}} {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_, _, err := websocket.Dial(ctx, endpoint, &websocket.DialOptions{HTTPHeader: header})
		cancel()
		if err == nil {
			t.Fatal("punch probe accepted an unauthenticated dial")
		}
	}
}

func TestPunchProbeReportsUnobservableBehindLoopbackPeer(t *testing.T) {
	core, err := New("test", Options{DataDir: t.TempDir(), Development: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = core.Close() })
	coreHTTP := httptest.NewServer(core)
	t.Cleanup(coreHTTP.Close)
	credential := punchProbeCredentialForTest(t, core)
	endpoint := "ws" + strings.TrimPrefix(coreHTTP.URL, "http") + protocol.FilePunchProbeRoute
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, endpoint, &websocket.DialOptions{HTTPHeader: http.Header{"Authorization": {"Bearer " + credential}}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close(websocket.StatusNormalClosure, "test complete") })
	kind, raw, err := conn.Read(ctx)
	if err != nil || kind != websocket.MessageText {
		t.Fatalf("read punch probe observation: %v", err)
	}
	var observation protocol.FilePunchObservation
	if err := json.Unmarshal(raw, &observation); err != nil || observation.Error != protocol.FilePunchUnobservable {
		t.Fatalf("loopback peer was reported observable: %s", raw)
	}
	_ = conn.Close(websocket.StatusNormalClosure, "test complete")
}

func TestPunchObservationClassification(t *testing.T) {
	core := &Server{}
	t.Run("public", func(t *testing.T) {
		request := httptest.NewRequest(http.MethodGet, protocol.FilePunchProbeRoute, nil)
		request.RemoteAddr = "203.0.113.5:51024"
		observed, observable := core.punchObservation(request)
		if !observable || observed != "203.0.113.5:51024" {
			t.Fatalf("public peer not reported: %q %t", observed, observable)
		}
	})
	t.Run("private_lan", func(t *testing.T) {
		request := httptest.NewRequest(http.MethodGet, protocol.FilePunchProbeRoute, nil)
		request.RemoteAddr = "192.168.1.20:60010"
		observed, observable := core.punchObservation(request)
		if !observable || observed != "192.168.1.20:60010" {
			t.Fatalf("LAN peer not reported: %q %t", observed, observable)
		}
	})
	t.Run("loopback", func(t *testing.T) {
		request := httptest.NewRequest(http.MethodGet, protocol.FilePunchProbeRoute, nil)
		request.RemoteAddr = "127.0.0.1:44444"
		if _, observable := core.punchObservation(request); observable {
			t.Fatal("loopback peer was reported observable")
		}
	})
	t.Run("trusted_proxy", func(t *testing.T) {
		trusted, err := config.ParseTrustedProxies([]string{"203.0.113.0/24"})
		if err != nil {
			t.Fatal(err)
		}
		proxied := &Server{trusted: trusted}
		request := httptest.NewRequest(http.MethodGet, protocol.FilePunchProbeRoute, nil)
		request.RemoteAddr = "203.0.113.9:51024"
		if _, observable := proxied.punchObservation(request); observable {
			t.Fatal("trusted proxy peer was reported observable")
		}
	})
	t.Run("malformed", func(t *testing.T) {
		request := httptest.NewRequest(http.MethodGet, protocol.FilePunchProbeRoute, nil)
		request.RemoteAddr = "not-an-address"
		if _, observable := core.punchObservation(request); observable {
			t.Fatal("malformed peer was reported observable")
		}
	})
}

func TestPunchProbePerNodeLimit(t *testing.T) {
	core := &Server{punchProbes: make(map[string]int)}
	for i := 0; i < 4; i++ {
		if !core.beginPunchProbe("node-limit") {
			t.Fatalf("probe %d rejected before the limit", i+1)
		}
	}
	if core.beginPunchProbe("node-limit") {
		t.Fatal("fifth concurrent probe was accepted")
	}
	if !core.beginPunchProbe("node-other") {
		t.Fatal("another node was blocked by the first node's limit")
	}
	core.endPunchProbe("node-limit")
	if !core.beginPunchProbe("node-limit") {
		t.Fatal("released probe slot was not reusable")
	}
	for i := 0; i < 4; i++ {
		core.endPunchProbe("node-limit")
	}
	core.endPunchProbe("node-other")
	if len(core.punchProbes) != 0 {
		t.Fatalf("probe slots leaked: %v", core.punchProbes)
	}
}
