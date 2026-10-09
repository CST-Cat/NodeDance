// Package probes performs bounded, outbound service checks on the Agent's
// managed host. It does not expose a listener or accept browser-supplied data.
package probes

import (
	"context"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/CST-Cat/NodeDance/internal/protocol"
)

const maxProbeResponseBytes = 4096

type Executor struct {
	client      *http.Client
	dialContext func(context.Context, string, string) (net.Conn, error)
}

func NewExecutor() *Executor {
	dialer := &net.Dialer{Timeout: time.Duration(protocol.MaxProbeTimeoutSeconds) * time.Second, KeepAlive: 30 * time.Second}
	transport := &http.Transport{
		Proxy:                 nil, // Probes always originate directly from the managed node.
		DialContext:           dialer.DialContext,
		TLSHandshakeTimeout:   time.Duration(protocol.MaxProbeTimeoutSeconds) * time.Second,
		ResponseHeaderTimeout: time.Duration(protocol.MaxProbeTimeoutSeconds) * time.Second,
		MaxConnsPerHost:       MaxConcurrent,
		DisableKeepAlives:     true,
	}
	return &Executor{
		client:      &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
		dialContext: dialer.DialContext,
	}
}

func (e *Executor) Execute(parent context.Context, dispatch protocol.ProbeDispatch) protocol.ProbeReport {
	report := protocol.ProbeReport{ProbeID: dispatch.ProbeID, RunID: dispatch.RunID, NodeID: dispatch.NodeID}
	if err := protocol.ValidateProbeTarget(dispatch.Kind, dispatch.Target, dispatch.ExpectedHTTPStatus); err != nil || dispatch.TimeoutSeconds < protocol.MinProbeTimeoutSeconds || dispatch.TimeoutSeconds > protocol.MaxProbeTimeoutSeconds {
		report.Status, report.ErrorCode = protocol.ProbeResultUnknown, "invalid_dispatch"
		return report
	}
	ctx, cancel := context.WithTimeout(parent, time.Duration(dispatch.TimeoutSeconds)*time.Second)
	defer cancel()
	started := time.Now()
	var httpStatus int
	var err error
	switch dispatch.Kind {
	case protocol.ProbeHTTP, protocol.ProbeHTTPS:
		httpStatus, err = e.checkHTTP(ctx, dispatch.Target)
		if err == nil && httpStatus != dispatch.ExpectedHTTPStatus {
			err = errHTTPStatusMismatch
		}
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

func (e *Executor) checkHTTP(ctx context.Context, target string) (int, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return 0, err
	}
	response, err := e.client.Do(request)
	if err != nil {
		return 0, err
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maxProbeResponseBytes))
	return response.StatusCode, nil
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
	var dnsError *net.DNSError
	if errors.As(err, &dnsError) {
		return "dns"
	}
	var operationError *net.OpError
	if errors.As(err, &operationError) && (operationError.Timeout() || os.IsTimeout(err)) {
		return "timeout"
	}
	return "connection_failed"
}
