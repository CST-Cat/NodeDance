package metrics

import (
	"github.com/CST-Cat/NodeDance/internal/protocol"
	"testing"
	"time"
)

func TestEnrichmentOwnsEmptyDiagnosticMaps(t *testing.T) {
	s := NewStore()
	report := protocol.MetricsSnapshot{Host: &protocol.MetricsHost{FastIntervalSeconds: 5, Observability: &protocol.Observability{SlowIntervalSeconds: 60}}}
	s.EnrichReport(Identity{NodeID: "node", AgentID: "agent"}, 1, &report, time.Now())
	for _, key := range []string{"agent.report_interval_seconds", "agent.unobserved_intervals", "agent.sample_age_ms"} {
		if _, ok := report.Host.Observability.Values[key]; !ok {
			t.Fatal("missing Core annotation", key)
		}
	}
}
