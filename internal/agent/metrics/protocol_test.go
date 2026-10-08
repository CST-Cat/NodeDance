package metrics

import (
	"testing"
	"time"

	"github.com/CST-Cat/NodeDance/internal/protocol"
)

func TestToProtocolMetricsComputesAgeAndPreservesUnknownReasons(t *testing.T) {
	sampledAt := time.Now()
	serializedAt := sampledAt.Add(2700 * time.Millisecond)
	usage := 31.25
	snapshot := Snapshot{
		CollectedAt: sampledAt,
		CPU:         CPUInfo{UsagePercent: known(usage, sampledAt)},
		Memory:      unknown[Memory]("permission_denied", sampledAt),
		Network: NetworkSnapshot{
			Summary: unknown[NetworkRate]("read_failed", sampledAt),
		},
	}

	wire := ToProtocolMetricsAt(snapshot, serializedAt)
	if wire.CPU.UsagePercent.Status != protocol.MetricKnown || wire.CPU.UsagePercent.Value == nil || *wire.CPU.UsagePercent.Value != usage {
		t.Fatalf("CPU conversion lost value/status: %#v", wire.CPU.UsagePercent)
	}
	if wire.CPU.UsagePercent.SampleAgeMillis != 2700 {
		t.Fatalf("CPU sample age = %d ms, want 2700", wire.CPU.UsagePercent.SampleAgeMillis)
	}
	if wire.Memory.Status != protocol.MetricUnknown || wire.Memory.Value != nil || wire.Memory.Reason != "permission_denied" {
		t.Fatalf("permission failure should remain unknown and valueless: %#v", wire.Memory)
	}
	if wire.Memory.SampleAgeMillis != 2700 {
		t.Fatalf("unknown sample age = %d ms, want 2700", wire.Memory.SampleAgeMillis)
	}
	if wire.Network.Summary.Status != protocol.MetricError || wire.Network.Summary.Value != nil || wire.Network.Summary.Reason != "read_failed" {
		t.Fatalf("read failure should remain an error without a zero rate: %#v", wire.Network.Summary)
	}
}

func TestToProtocolMetricsClampsFutureAndVeryOldSamples(t *testing.T) {
	now := time.Now()
	future := now.Add(time.Second)
	old := now.Add(-8 * 24 * time.Hour)
	snapshot := Snapshot{
		CollectedAt: now,
		CPU: CPUInfo{
			UsagePercent: known(10.0, future),
			LogicalCores: known(2, old),
		},
	}
	wire := ToProtocolMetricsAt(snapshot, now)
	if wire.CPU.UsagePercent.SampleAgeMillis != 0 {
		t.Fatalf("future sample age = %d, want clamped to zero", wire.CPU.UsagePercent.SampleAgeMillis)
	}
	if wire.CPU.LogicalCores.SampleAgeMillis != protocol.MaxMetricSampleAgeMS {
		t.Fatalf("old sample age = %d, want cap %d", wire.CPU.LogicalCores.SampleAgeMillis, protocol.MaxMetricSampleAgeMS)
	}
}
