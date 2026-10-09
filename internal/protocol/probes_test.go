package protocol

import "testing"

const probeTestID = "2cf3b66b-93b1-4c8d-9485-699e94da9c21"

func TestValidateProbeTarget(t *testing.T) {
	tests := []struct {
		name, kind, target string
		status             int
		valid              bool
	}{
		{"http", ProbeHTTP, "http://127.0.0.1:18080/health", 200, true},
		{"https", ProbeHTTPS, "https://example.test/ready", 204, true},
		{"tcp", ProbeTCP, "tcp://[::1]:8080", 0, true},
		{"credentials", ProbeHTTP, "http://user:pass@example.test/", 200, false},
		{"wrong-scheme", ProbeHTTPS, "http://example.test/", 200, false},
		{"no-port", ProbeTCP, "tcp://example.test", 0, false},
		{"bad-port", ProbeTCP, "tcp://example.test:0", 0, false},
		{"tcp-http-status", ProbeTCP, "tcp://example.test:80", 200, false},
		{"bad-status", ProbeHTTP, "http://example.test", 99, false},
		{"fragment", ProbeHTTP, "http://example.test/#secret", 200, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateProbeTarget(tt.kind, tt.target, tt.status)
			if (err == nil) != tt.valid {
				t.Fatalf("ValidateProbeTarget() error=%v valid=%v", err, tt.valid)
			}
		})
	}
}

func TestValidateProbeDispatchAndReport(t *testing.T) {
	dispatch := ProbeDispatch{ProbeID: probeTestID, RunID: probeTestID, NodeID: probeTestID,
		Kind: ProbeHTTP, Target: "http://127.0.0.1/health", ExpectedHTTPStatus: 200,
		IntervalSeconds: 30, TimeoutSeconds: 3}
	envelope := Envelope{Version: CurrentVersion, Type: TypeProbeDispatch, Generation: 4, RequestID: probeTestID}
	if err := ValidateProbeDispatch(envelope, dispatch, probeTestID, 4); err != nil {
		t.Fatalf("valid dispatch rejected: %v", err)
	}
	bad := dispatch
	bad.TimeoutSeconds = bad.IntervalSeconds
	if err := ValidateProbeDispatch(envelope, bad, probeTestID, 4); err == nil {
		t.Fatal("timeout equal to interval was accepted")
	}
	report := ProbeReport{ProbeID: probeTestID, RunID: probeTestID, NodeID: probeTestID,
		Status: ProbeResultHealthy, HTTPStatus: 200, LatencyMS: 12}
	reportEnvelope := Envelope{Version: CurrentVersion, Type: TypeProbeReport, Generation: 4, RequestID: probeTestID}
	if err := ValidateProbeReport(reportEnvelope, report, probeTestID, 4); err != nil {
		t.Fatalf("valid report rejected: %v", err)
	}
	badReport := report
	badReport.Status = ProbeResultUnhealthy
	if err := ValidateProbeReport(reportEnvelope, badReport, probeTestID, 4); err == nil {
		t.Fatal("unhealthy result without reason was accepted")
	}
}
