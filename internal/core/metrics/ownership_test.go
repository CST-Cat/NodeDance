package metrics

import (
	"errors"
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
func TestMergedReportOwnsNestedValues(t *testing.T) {
	store := NewStore()
	id := Identity{AgentID: "agent", NodeID: "node"}
	if err := store.BindConnection(id, 1); err != nil {
		t.Fatal(err)
	}
	at := time.Now()
	first := uint64(100)
	second := uint64(200)
	firstReport := ownershipReport(at, &first)
	if err := store.Accept(id, 1, 1, &firstReport, at); err != nil {
		t.Fatal(err)
	}
	secondReport := ownershipReport(at, &second)
	secondReport.Network.Interfaces = []protocol.MetricsInterface{{Name: "eth1", Rate: protocol.Metric[protocol.NetworkRate]{Status: protocol.MetricKnown, SampledAt: at, Value: &protocol.NetworkRate{ReceivedBytesPerSecond: 10, SentBytesPerSecond: 20}}}}
	secondReport.Disk = protocol.MetricsDisk{Status: protocol.MetricKnown, SampledAt: at, Mounts: []protocol.MetricsMount{{Mountpoint: "/", Usage: protocol.Metric[protocol.DiskUsage]{Status: protocol.MetricKnown, SampledAt: at, Value: &protocol.DiskUsage{TotalBytes: 100, UsedBytes: 50, AvailableBytes: 50, UsedPercent: 50}}}}}
	if err := store.Accept(id, 1, 2, &secondReport, at); err != nil {
		t.Fatal(err)
	}
	second = 999
	secondReport.Network.Interfaces[0].Rate.Value.ReceivedBytesPerSecond = 999
	secondReport.Disk.Mounts[0].Usage.Value.UsedBytes = 99
	view, ok := store.SnapshotAt(id.NodeID, nil, at)
	if !ok || view.Metrics.Uptime.Value == nil || *view.Metrics.Uptime.Value.BootTimeUnix != 200 {
		t.Fatal("caller changed persisted merged metric")
	}
	if view.Metrics.Network.Interfaces[0].Rate.Value.ReceivedBytesPerSecond != 10 || view.Metrics.Disk.Mounts[0].Usage.Value.UsedBytes != 50 {
		t.Fatal("caller changed persisted interface or mount metrics")
	}

}

func TestInvalidSampleCannotReplaceAcceptedMetrics(t *testing.T) {
	store := NewStore()
	identity := Identity{AgentID: "agent", NodeID: "node"}
	if err := store.BindConnection(identity, 1); err != nil {
		t.Fatal(err)
	}
	at := time.Now()
	boot := uint64(100)
	report := ownershipReport(at, &boot)
	if err := store.Accept(identity, 1, 1, &report, at); err != nil {
		t.Fatal(err)
	}
	invalid := ownershipReport(at, &boot)
	badCPU := 200.0
	invalid.CPU.UsagePercent = protocol.Metric[float64]{Status: protocol.MetricKnown, Value: &badCPU, SampledAt: at}
	if err := store.Accept(identity, 1, 2, &invalid, at); !errors.Is(err, ErrInvalidReport) {
		t.Fatalf("invalid sample accepted: %v", err)
	}
	view, ok := store.SnapshotAt(identity.NodeID, nil, at)
	if !ok || view.Sequence != 1 {
		t.Fatal("invalid sample changed latest metrics", view.Sequence)
	}
	if err := store.Accept(identity, 1, 2, &report, at); err != nil {
		t.Fatal("rejected sample consumed the sequence", err)
	}
}
