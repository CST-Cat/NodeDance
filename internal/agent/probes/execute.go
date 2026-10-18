// Package probes performs bounded, outbound service checks on the Agent's
// managed host. It does not expose a listener or accept browser-supplied data.
package probes

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/CST-Cat/NodeDance/internal/agent/networkpolicy"
	"github.com/CST-Cat/NodeDance/internal/protocol"
)

const maxProbeResponseBytes = 4096

type probeFamilyKey struct{}

type Executor struct {
	settings    func() protocol.AgentSettings
	client      *http.Client
	dialContext func(context.Context, string, string) (net.Conn, error)
}

func NewExecutor() *Executor { return NewConfiguredExecutor(protocol.DefaultAgentSettings) }
func NewConfiguredExecutor(settings func() protocol.AgentSettings) *Executor {
	dial := func(ctx context.Context, network, address string) (net.Conn, error) {
		if family, _ := ctx.Value(probeFamilyKey{}).(string); network == "tcp" && (family == "4" || family == "6") {
			network = "tcp" + family
		}
		return networkpolicy.Dial(ctx, network, address, settings())
	}
	transport := &http.Transport{
		Proxy:                 nil, // Probes always originate directly from the managed node.
		DialContext:           dial,
		TLSHandshakeTimeout:   time.Duration(protocol.MaxProbeTimeoutSeconds) * time.Second,
		ResponseHeaderTimeout: time.Duration(protocol.MaxProbeTimeoutSeconds) * time.Second,
		MaxConnsPerHost:       MaxConcurrent,
		DisableKeepAlives:     true,
	}
	return &Executor{settings: settings,
		client:      &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
		dialContext: dial,
	}
}

func (e *Executor) Execute(parent context.Context, dispatch protocol.ProbeDispatch) protocol.ProbeReport {
	report := protocol.ProbeReport{ProbeID: dispatch.ProbeID, RunID: dispatch.RunID, NodeID: dispatch.NodeID}
	if err := protocol.ValidateProbeTarget(dispatch.Kind, dispatch.Target, dispatch.ExpectedHTTPStatus); err != nil || dispatch.TimeoutSeconds < protocol.MinProbeTimeoutSeconds || dispatch.TimeoutSeconds > protocol.MaxProbeTimeoutSeconds || !protocol.ValidProbeIPFamily(dispatch.IPFamily) {
		report.Status, report.ErrorCode = protocol.ProbeResultUnknown, "invalid_dispatch"
		return report
	}
	ctx, cancel := context.WithTimeout(parent, time.Duration(dispatch.TimeoutSeconds)*time.Second)
	defer cancel()
	ctx = context.WithValue(ctx, probeFamilyKey{}, dispatch.IPFamily)
	started := time.Now()
	var httpStatus int
	var err error
	switch dispatch.Kind {
	case protocol.ProbeHTTP, protocol.ProbeHTTPS:
		httpStatus, report.Measurements, err = e.checkHTTP(ctx, dispatch.Target)
		if err == nil && httpStatus != dispatch.ExpectedHTTPStatus {
			err = errHTTPStatusMismatch
		}
	case protocol.ProbeICMP:
		err = e.checkICMP(ctx, dispatch.Target)
	case protocol.ProbeTCP:
		err = e.checkTCP(ctx, dispatch.Target)
	default:
		err = errors.New("unsupported probe kind")
	}
	report.LatencyMS = time.Since(started).Milliseconds()
	report.HTTPStatus = httpStatus
	if err == nil {
		report.Status = protocol.ProbeResultHealthy
		return report
	}
	report.Status = protocol.ProbeResultUnhealthy
	report.ErrorCode = classifyError(ctx, err)
	return report
}

var errHTTPStatusMismatch = errors.New("expected HTTP status did not match")

func (e *Executor) checkHTTP(ctx context.Context, target string) (int, *protocol.ProbeMeasurements, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return 0, nil, err
	}
	m := &protocol.ProbeMeasurements{}
	var mu sync.Mutex
	var dns, connect, handshake time.Time
	started := time.Now()
	duration := func(at time.Time) *float64 { n := float64(time.Since(at).Microseconds()) / 1000; return &n }
	trace := &httptrace.ClientTrace{
		DNSStart: func(httptrace.DNSStartInfo) { mu.Lock(); dns = time.Now(); mu.Unlock() },
		DNSDone: func(info httptrace.DNSDoneInfo) {
			mu.Lock()
			defer mu.Unlock()
			if info.Err == nil && !dns.IsZero() {
				m.DNSMS = duration(dns)
			}
		},
		ConnectStart: func(string, string) { mu.Lock(); connect = time.Now(); mu.Unlock() },
		ConnectDone: func(_, _ string, e error) {
			mu.Lock()
			defer mu.Unlock()
			if e == nil && !connect.IsZero() {
				m.ConnectMS = duration(connect)
			}
		},
		TLSHandshakeStart: func() { mu.Lock(); handshake = time.Now(); mu.Unlock() },
		TLSHandshakeDone: func(state tls.ConnectionState, e error) {
			mu.Lock()
			defer mu.Unlock()
			if e == nil && !handshake.IsZero() {
				m.TLSMS = duration(handshake)
			}
			if e == nil && len(state.PeerCertificates) > 0 {
				at := state.PeerCertificates[0].NotAfter.UTC()
				m.CertificateExpiresAt = &at
			}
		},
		GotFirstResponseByte: func() { mu.Lock(); m.FirstByteMS = duration(started); mu.Unlock() },
	}
	response, err := e.client.Do(request.WithContext(httptrace.WithClientTrace(ctx, trace)))
	if err != nil {
		mu.Lock()
		copy := *m
		mu.Unlock()
		return 0, &copy, err
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maxProbeResponseBytes))
	mu.Lock()
	copy := *m
	mu.Unlock()
	return response.StatusCode, &copy, nil
}

func (e *Executor) checkTCP(ctx context.Context, target string) error {
	endpoint := strings.TrimPrefix(target, "tcp://")
	conn, err := e.dialContext(ctx, "tcp", endpoint)
	if err != nil {
		return err
	}
	return conn.Close()
}

func classifyError(ctx context.Context, err error) string {
	var dnsError *net.DNSError
	if errors.As(err, &dnsError) {
		return "dns"
	}

	if ctx.Err() == context.DeadlineExceeded || errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	if ctx.Err() == context.Canceled || errors.Is(err, context.Canceled) {
		return "cancelled"
	}
	if errors.Is(err, errHTTPStatusMismatch) {
		return "http_status_mismatch"
	}
	var unknownAuthority x509.UnknownAuthorityError
	var invalidCertificate x509.CertificateInvalidError
	if errors.As(err, &unknownAuthority) || errors.As(err, &invalidCertificate) {
		return "tls_verification"
	}
	var hostnameError x509.HostnameError
	if errors.As(err, &hostnameError) {
		return "tls_verification"
	}
	var operationError *net.OpError
	if errors.As(err, &operationError) && (operationError.Timeout() || os.IsTimeout(err)) {
		return "timeout"
	}
	return "connection_failed"
}
