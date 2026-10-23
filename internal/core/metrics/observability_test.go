package metrics

import (
	"github.com/CST-Cat/NodeDance/internal/protocol"
	"testing"
	"time"
)

func TestEnrichmentOwnsEmptyDiagnosticMaps(t *testing.T) {
	report := protocol.MetricsSnapshot{Host: &protocol.MetricsHost{FastIntervalSeconds: 5, Observability: &protocol.Observability{SlowIntervalSeconds: 60}}}
	enrichReport(Identity{NodeID: "node", AgentID: "agent"}, 1, &report, time.Now(), Record{}, false)
	for _, key := range []string{"agent.report_interval_seconds", "agent.unobserved_intervals", "agent.sample_age_ms"} {
		if _, ok := report.Host.Observability.Values[key]; !ok {
			t.Fatal("missing Core annotation", key)
		}
	}
}
