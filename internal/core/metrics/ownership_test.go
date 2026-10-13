package metrics

import (
	"github.com/CST-Cat/NodeDance/internal/protocol"
	"testing"
	"time"
)

func ownershipReport(at time.Time, boot *uint64) protocol.MetricsSnapshot {
	u := protocol.Metric[string]{Status: protocol.MetricUnknown, Reason: "not_sampled"}
	return protocol.MetricsSnapshot{CollectedAt: at,
		System: protocol.MetricsSystem{Hostname: u, OS: u, Architecture: u, Platform: u, PlatformFamily: u, PlatformVersion: u, KernelVersion: u},
		CPU:    protocol.MetricsCPU{UsagePercent: protocol.Metric[float64]{Status: protocol.MetricUnknown, Reason: "not_sampled"}, LogicalCores: protocol.Metric[int]{Status: protocol.MetricUnknown, Reason: "not_sampled"}},
		Memory: protocol.Metric[protocol.Memory]{Status: protocol.MetricUnknown, Reason: "not_sampled"}, Network: protocol.MetricsNetwork{Summary: protocol.Metric[protocol.NetworkRate]{Status: protocol.MetricUnknown, Reason: "not_sampled"}},
		Disk:   protocol.MetricsDisk{Status: protocol.MetricUnknown, Reason: "not_sampled"},
		Uptime: protocol.Metric[protocol.Uptime]{Status: protocol.MetricKnown, SampledAt: at, Value: &protocol.Uptime{BootID: "boot", BootTimeUnix: boot}},
	}
}
func TestMergedReportDoesNotRetainCallerBootTimePointer(t *testing.T) {
	store := NewStore()
	id := Identity{AgentID: "agent", NodeID: "node"}
	if err := store.BindConnection(id, 1); err != nil {
		t.Fatal(err)
	}
	at := time.Now()
	first := uint64(100)
	second := uint64(200)
	if err := store.Accept(id, 1, 1, ownershipReport(at, &first), at); err != nil {
		t.Fatal(err)
	}
	if err := store.Accept(id, 1, 2, ownershipReport(at, &second), at); err != nil {
		t.Fatal(err)
	}
	second = 999
	view, ok := store.SnapshotAt(id.NodeID, nil, at)
	if !ok || view.Metrics.Uptime.Value == nil || *view.Metrics.Uptime.Value.BootTimeUnix != 200 {
		t.Fatal("caller changed persisted merged metric")
	}
}
