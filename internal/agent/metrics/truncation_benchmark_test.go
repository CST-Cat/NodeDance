package metrics

import (
	"encoding/json"
	"fmt"
	"github.com/CST-Cat/NodeDance/internal/protocol"
	"strings"
	"testing"
)

func denseObservability() *protocol.Observability {
	o := &protocol.Observability{SlowIntervalSeconds: 60}
	for i := 0; i < 128; i++ {
		values := map[string]protocol.Metric[float64]{}
		for j := 0; j < 16; j++ {
			values[fmt.Sprintf("reading%d", j)] = protocol.Metric[float64]{Status: protocol.MetricUnknown, Reason: strings.Repeat("x", 64)}
		}
		o.Entities = append(o.Entities, protocol.DiagnosticEntity{ID: fmt.Sprintf("process-%d", i), Kind: "process", Name: strings.Repeat("n", 128), Values: values})
	}
	return o
}

func BenchmarkBoundDenseObservability(b *testing.B) {
	original := denseObservability()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		host := &protocol.MetricsHost{Observability: protocol.CloneObservability(original)}
		boundProtocolDetails(protocol.MetricsSnapshot{Host: host})
	}
}

func TestDenseObservabilityRetainsLargestFittingPrefix(t *testing.T) {
	original := denseObservability()
	host := &protocol.MetricsHost{Observability: protocol.CloneObservability(original)}
	bounded := boundProtocolDetails(protocol.MetricsSnapshot{Host: host})
	result := bounded.Host.Observability
	if !result.Truncated || len(result.Entities) == 0 || len(result.Entities) >= len(original.Entities) {
		t.Fatal("dense readings were not bounded")
	}
	data, err := json.Marshal(result)
	if err != nil || len(data) > 24<<10 {
		t.Fatalf("oversized diagnostics: %d %v", len(data), err)
	}
	for index, entity := range result.Entities {
		if entity.ID != original.Entities[index].ID {
			t.Fatal("entity order changed")
		}
	}
	result.Entities = append(result.Entities, original.Entities[len(result.Entities)])
	data, err = json.Marshal(result)
	if err != nil || len(data) <= 24<<10 {
		t.Fatal("discarded an entity that still fits")
	}
	if len(original.Entities) != 128 || original.Truncated {
		t.Fatal("changed collector snapshot")
	}
}
