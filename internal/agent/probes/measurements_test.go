package probes

import (
	"context"
	"github.com/CST-Cat/NodeDance/internal/protocol"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHTTPPhasesAndForcedIPFamily(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) }))
	defer server.Close()
	executor := NewExecutor()
	executor.client = server.Client()
	dispatch := protocol.ProbeDispatch{Kind: protocol.ProbeHTTPS, Target: server.URL, ExpectedHTTPStatus: 200, TimeoutSeconds: 2}
	r := executor.Execute(context.Background(), dispatch)
	if r.Status != protocol.ProbeResultHealthy || r.Measurements == nil || r.Measurements.FirstByteMS == nil || r.Measurements.CertificateExpiresAt == nil {
		t.Fatal(r)
	}
	// A literal IPv4 endpoint cannot silently satisfy an IPv6-only probe.
	executor = NewExecutor()
	dispatch.Kind = protocol.ProbeTCP
	dispatch.Target = "tcp://" + strings.TrimPrefix(server.URL, "https://")
	dispatch.ExpectedHTTPStatus = 0
	dispatch.IPFamily = "6"
	r = executor.Execute(context.Background(), dispatch)
	if r.Status == protocol.ProbeResultHealthy {
		t.Fatal("IPv6 probe fell back to IPv4")
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	dispatch.IPFamily = "4"
	dispatch.Target = "tcp://" + listener.Addr().String()
	r = executor.Execute(context.Background(), dispatch)
	if r.Status != protocol.ProbeResultHealthy {
		t.Fatal(r)
	}
}
