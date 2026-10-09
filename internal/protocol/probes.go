package protocol

import (
	"errors"
	"net"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

const (
	CapabilityProbes = "agent.probes.v1"

	TypeProbeDispatch = "probe_dispatch"
	TypeProbeReport   = "probe_report"

	ProbeHTTP  = "http"
	ProbeHTTPS = "https"
	ProbeTCP   = "tcp"

	ProbeResultHealthy   = "healthy"
	ProbeResultUnhealthy = "unhealthy"
	ProbeResultUnknown   = "unknown"

	MinProbeIntervalSeconds = 10
	MaxProbeIntervalSeconds = 86400
	MinProbeTimeoutSeconds  = 1
	MaxProbeTimeoutSeconds  = 30
	MaxProbeTargetBytes     = 2048
)

var probeUUIDPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

var probeErrorCodes = map[string]struct{}{
	"invalid_dispatch": {}, "node_offline": {}, "capability_unavailable": {}, "dispatch_unavailable": {},
	"dispatch_timeout": {}, "core_restarted": {}, "capacity": {}, "timeout": {}, "cancelled": {},
	"tls_verification": {}, "http_status_mismatch": {}, "dns": {}, "connection_failed": {},
}

// ProbeDispatch is Core-owned configuration copied into one scheduled run.
// Agent validates all bounds again because Core and Agent are separate trust
// boundaries even though the message is authenticated.
type ProbeDispatch struct {
	ProbeID            string `json:"probeId"`
	RunID              string `json:"runId"`
	NodeID             string `json:"nodeId"`
	Kind               string `json:"kind"`
	Target             string `json:"target"`
	ExpectedHTTPStatus int    `json:"expectedHttpStatus,omitempty"`
	IntervalSeconds    int    `json:"intervalSeconds"`
	TimeoutSeconds     int    `json:"timeoutSeconds"`
}

type ProbeReport struct {
	ProbeID    string `json:"probeId"`
	RunID      string `json:"runId"`
	NodeID     string `json:"nodeId"`
	Status     string `json:"status"`
	HTTPStatus int    `json:"httpStatus,omitempty"`
	LatencyMS  int64  `json:"latencyMs"`
	ErrorCode  string `json:"errorCode,omitempty"`
}

func ValidateProbeDispatch(envelope Envelope, dispatch ProbeDispatch, nodeID string, generation uint64) error {
	if envelope.Version != CurrentVersion || envelope.Type != TypeProbeDispatch || generation == 0 || envelope.Generation != generation ||
		!probeUUIDPattern.MatchString(envelope.RequestID) || dispatch.RunID != envelope.RequestID ||
		!probeUUIDPattern.MatchString(dispatch.ProbeID) || !probeUUIDPattern.MatchString(dispatch.RunID) || dispatch.NodeID != nodeID ||
		dispatch.TimeoutSeconds < MinProbeTimeoutSeconds || dispatch.TimeoutSeconds > MaxProbeTimeoutSeconds ||
		dispatch.IntervalSeconds < MinProbeIntervalSeconds || dispatch.IntervalSeconds > MaxProbeIntervalSeconds || dispatch.TimeoutSeconds >= dispatch.IntervalSeconds ||
		ValidateProbeTarget(dispatch.Kind, dispatch.Target, dispatch.ExpectedHTTPStatus) != nil {
		return errors.New("invalid service probe dispatch")
	}
	return nil
}

// ValidateProbeReport checks the Agent result envelope and the bounded result
// vocabulary. The Core additionally binds the run to the stored node and
// generation before accepting it.
func ValidateProbeReport(envelope Envelope, report ProbeReport, nodeID string, generation uint64) error {
	if envelope.Version != CurrentVersion || envelope.Type != TypeProbeReport || envelope.Generation != generation || generation == 0 ||
		!probeUUIDPattern.MatchString(envelope.RequestID) || report.RunID != envelope.RequestID ||
		!probeUUIDPattern.MatchString(report.RunID) || !probeUUIDPattern.MatchString(report.ProbeID) || report.NodeID != nodeID ||
		(report.Status != ProbeResultHealthy && report.Status != ProbeResultUnhealthy && report.Status != ProbeResultUnknown) ||
		report.LatencyMS < 0 || report.LatencyMS > int64(MaxProbeTimeoutSeconds*1000+1000) || len(report.ErrorCode) > 64 {
		return errors.New("invalid service probe report")
	}
	if report.Status == ProbeResultHealthy && report.ErrorCode != "" || report.Status == ProbeResultUnhealthy && report.ErrorCode == "" || report.Status == ProbeResultUnknown && report.ErrorCode == "" {
		return errors.New("service probe report status and error code disagree")
	}
	if report.ErrorCode != "" {
		if _, ok := probeErrorCodes[report.ErrorCode]; !ok {
			return errors.New("service probe report error code is not allowlisted")
		}
	}
	if report.HTTPStatus < 0 || report.HTTPStatus > 599 {
		return errors.New("invalid service probe HTTP status")
	}
	return nil
}

func ValidateProbeTarget(kind, target string, expectedHTTPStatus int) error {
	if len(target) == 0 || len(target) > MaxProbeTargetBytes || strings.TrimSpace(target) != target {
		return errors.New("probe target length or whitespace is invalid")
	}
	for _, r := range target {
		if r < 0x20 || r == 0x7f {
			return errors.New("probe target contains control characters")
		}
	}
	switch kind {
	case ProbeHTTP, ProbeHTTPS:
		u, err := url.Parse(target)
		if err != nil || u.Scheme != kind || u.Hostname() == "" || u.User != nil || u.Fragment != "" || u.Opaque != "" {
			return errors.New("HTTP probe target must be an absolute URL without credentials or fragment")
		}
		if port := u.Port(); port != "" {
			parsedPort, err := strconv.Atoi(port)
			if err != nil || parsedPort < 1 || parsedPort > 65535 {
				return errors.New("HTTP probe target port is invalid")
			}
		}
		if expectedHTTPStatus < 100 || expectedHTTPStatus > 599 {
			return errors.New("expected HTTP status must be between 100 and 599")
		}
	case ProbeTCP:
		u, err := url.Parse(target)
		if err != nil || u.Scheme != "tcp" || u.User != nil || u.Hostname() == "" || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
			return errors.New("TCP probe target must use tcp://host:port")
		}
		port, err := strconv.Atoi(u.Port())
		if err != nil || port < 1 || port > 65535 || net.ParseIP(u.Hostname()) == nil && strings.ContainsAny(u.Hostname(), "/@%") {
			return errors.New("TCP probe target port or host is invalid")
		}
		if expectedHTTPStatus != 0 {
			return errors.New("TCP probe cannot define an HTTP status")
		}
	default:
		return errors.New("unsupported service probe kind")
	}
	return nil
}
