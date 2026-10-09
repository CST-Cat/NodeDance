package probes

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/CST-Cat/NodeDance/internal/protocol"
)

func TestExecutorHTTPStatusHTTPSVerificationAndTCP(t *testing.T) {
	executor := NewExecutor()
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("probe method=%s, want GET", r.Method)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer httpServer.Close()

	for _, tt := range []struct {
		name, target string
		kind         string
		wantStatus   int
		wantResult   string
		wantError    string
	}{
		{"http-expected", httpServer.URL, protocol.ProbeHTTP, http.StatusNoContent, protocol.ProbeResultHealthy, ""},
		{"http-mismatch", httpServer.URL, protocol.ProbeHTTP, http.StatusOK, protocol.ProbeResultUnhealthy, "http_status_mismatch"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dispatch := testDispatch(tt.kind, tt.target, tt.wantStatus)
			got := executor.Execute(context.Background(), dispatch)
			if got.Status != tt.wantResult || got.ErrorCode != tt.wantError || got.HTTPStatus != http.StatusNoContent {
				t.Fatalf("result=%+v, expected status=%s error=%s", got, tt.wantResult, tt.wantError)
			}
		})
	}

	tlsServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	defer tlsServer.Close()
	tlsResult := executor.Execute(context.Background(), testDispatch(protocol.ProbeHTTPS, tlsServer.URL, http.StatusOK))
	if tlsResult.Status != protocol.ProbeResultUnhealthy || tlsResult.ErrorCode != "tls_verification" {
		t.Fatalf("untrusted HTTPS certificate accepted or misclassified: %+v", tlsResult)
	}
	leaf := tlsServer.Certificate()
	trustedRoots := x509.NewCertPool()
	trustedRoots.AddCert(leaf)
	executor.client.Transport = &http.Transport{Proxy: nil, TLSClientConfig: &tls.Config{RootCAs: trustedRoots, MinVersion: tls.VersionTLS12}, DisableKeepAlives: true}
	trustedResult := executor.Execute(context.Background(), testDispatch(protocol.ProbeHTTPS, tlsServer.URL, http.StatusOK))
	if trustedResult.Status != protocol.ProbeResultHealthy || trustedResult.HTTPStatus != http.StatusOK {
		t.Fatalf("valid trusted HTTPS certificate failed: %+v", trustedResult)
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	go func() {
		conn, err := listener.Accept()
		if err == nil {
			_ = conn.Close()
		}
	}()
	tcpResult := executor.Execute(context.Background(), testDispatch(protocol.ProbeTCP, "tcp://"+listener.Addr().String(), 0))
	if tcpResult.Status != protocol.ProbeResultHealthy || tcpResult.ErrorCode != "" {
		t.Fatalf("open TCP listener did not pass: %+v", tcpResult)
	}
	closedListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	closedAddress := closedListener.Addr().String()
	_ = closedListener.Close()
	closedResult := executor.Execute(context.Background(), testDispatch(protocol.ProbeTCP, "tcp://"+closedAddress, 0))
	if closedResult.Status != protocol.ProbeResultUnhealthy || closedResult.ErrorCode == "" {
		t.Fatalf("closed TCP listener did not fail: %+v", closedResult)
	}
}

func TestExecutorIgnoresConfiguredHTTPProxies(t *testing.T) {
	var proxyRequests atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		proxyRequests.Add(1)
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer proxy.Close()
	t.Setenv("HTTP_PROXY", proxy.URL)
	t.Setenv("http_proxy", proxy.URL)
	t.Setenv("HTTPS_PROXY", proxy.URL)
	t.Setenv("https_proxy", proxy.URL)
	t.Setenv("ALL_PROXY", proxy.URL)
	t.Setenv("all_proxy", proxy.URL)
	local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	defer local.Close()
	executor := NewExecutor()
	got := executor.Execute(context.Background(), testDispatch(protocol.ProbeHTTP, local.URL, http.StatusOK))
	if got.Status != protocol.ProbeResultHealthy {
		t.Fatalf("direct node-local HTTP request did not succeed with proxy configured: %+v", got)
	}
	tlsLocal := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	defer tlsLocal.Close()
	leaf := tlsLocal.Certificate()
	trustedRoots := x509.NewCertPool()
	trustedRoots.AddCert(leaf)
	executor.client.Transport = &http.Transport{Proxy: nil, TLSClientConfig: &tls.Config{RootCAs: trustedRoots, MinVersion: tls.VersionTLS12}, DisableKeepAlives: true}
	secure := executor.Execute(context.Background(), testDispatch(protocol.ProbeHTTPS, tlsLocal.URL, http.StatusOK))
	if secure.Status != protocol.ProbeResultHealthy {
		t.Fatalf("direct node-local HTTPS request did not succeed with proxy configured: %+v", secure)
	}
	if proxyRequests.Load() != 0 {
		t.Fatalf("configured HTTP proxy received %d probe requests", proxyRequests.Load())
	}
}

func TestExecutorBoundsRequestByTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(1500 * time.Millisecond):
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer server.Close()
	executor := NewExecutor()
	// Make this executor's HTTP timeout deterministic and much shorter than
	// the production lower bound so the unit case verifies cancellation logic.
	dispatch := testDispatch(protocol.ProbeHTTP, server.URL, http.StatusOK)
	dispatch.TimeoutSeconds = 1
	start := time.Now()
	got := executor.Execute(context.Background(), dispatch)
	if got.Status != protocol.ProbeResultUnhealthy || got.ErrorCode != "timeout" || time.Since(start) >= 1400*time.Millisecond {
		t.Fatalf("request timeout was not enforced, result=%+v elapsed=%s", got, time.Since(start))
	}
}

func TestTCPDialTimeoutIsReportedAsUnknownFailureCode(t *testing.T) {
	executor := NewExecutor()
	executor.dialContext = func(ctx context.Context, _, _ string) (net.Conn, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	dispatch := testDispatch(protocol.ProbeTCP, "tcp://192.0.2.1:65000", 0)
	dispatch.TimeoutSeconds = 1
	start := time.Now()
	result := executor.Execute(context.Background(), dispatch)
	if result.Status != protocol.ProbeResultUnhealthy || result.ErrorCode != "timeout" || time.Since(start) >= 2*time.Second {
		t.Fatalf("TCP timeout not bounded/classified: result=%+v elapsed=%s", result, time.Since(start))
	}
}

func TestBridgeLimitsConcurrentRequestsAndReportsCapacityAsUnknown(t *testing.T) {
	var active, peak atomic.Int32
	release := make(chan struct{})
	started := make(chan struct{}, MaxConcurrent)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		count := active.Add(1)
		for {
			old := peak.Load()
			if count <= old || peak.CompareAndSwap(old, count) {
				break
			}
		}
		started <- struct{}{}
		<-release
		active.Add(-1)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	bridge := NewBridge(NewExecutor())
	writer := &captureWriter{reports: make(chan protocol.ProbeReport, 16)}
	commands := make(chan protocol.Envelope, 16)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- bridge.Run(ctx, writer, 3, agentProbeNodeID, commands) }()
	for i := 0; i < 8; i++ {
		runID := fmt.Sprintf("%08x-1111-4111-8111-%012x", i+1, i+1)
		dispatch := protocol.ProbeDispatch{ProbeID: agentProbeProbeID, RunID: runID, NodeID: agentProbeNodeID,
			Kind: protocol.ProbeHTTP, Target: server.URL, ExpectedHTTPStatus: http.StatusOK,
			IntervalSeconds: 10, TimeoutSeconds: 3}
		commands <- protocol.Envelope{Version: protocol.CurrentVersion, Type: protocol.TypeProbeDispatch,
			Generation: 3, RequestID: runID, Payload: mustMarshalProbe(dispatch)}
	}
	for i := 0; i < MaxConcurrent; i++ {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("probe executor did not start expected concurrent request")
		}
	}
	unknown := 0
	for unknown < 8-MaxConcurrent {
		select {
		case report := <-writer.reports:
			if report.Status == protocol.ProbeResultUnknown && report.ErrorCode == "capacity" {
				unknown++
			}
		case <-time.After(time.Second):
			t.Fatal("capacity-limited dispatches were not reported unknown")
		}
	}
	close(release)
	for i := 0; i < MaxConcurrent; i++ {
		select {
		case report := <-writer.reports:
			if report.Status != protocol.ProbeResultHealthy {
				t.Fatalf("request completed with unexpected report: %+v", report)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("probe result was not reported")
		}
	}
	if got := peak.Load(); got > MaxConcurrent || got != MaxConcurrent {
		t.Fatalf("peak concurrency=%d, want exactly %d", got, MaxConcurrent)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

type captureWriter struct{ reports chan protocol.ProbeReport }

func (w *captureWriter) Send(_ context.Context, envelope protocol.Envelope) error {
	var report protocol.ProbeReport
	if err := decodeProbePayload(envelope.Payload, &report); err != nil {
		return err
	}
	w.reports <- report
	return nil
}

func testDispatch(kind, target string, expected int) protocol.ProbeDispatch {
	return protocol.ProbeDispatch{ProbeID: agentProbeProbeID, RunID: agentProbeRunID, NodeID: agentProbeNodeID,
		Kind: kind, Target: target, ExpectedHTTPStatus: expected, IntervalSeconds: 10, TimeoutSeconds: 3}
}

const (
	agentProbeNodeID  = "2cf3b66b-93b1-4c8d-9485-699e94da9c21"
	agentProbeProbeID = "4963823e-5d7e-4cf6-9b47-32b6fb9317c2"
	agentProbeRunID   = "846b1d0e-1603-4ab3-b5b4-29db7c6dc138"
)
