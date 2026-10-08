package protocol

import (
	"encoding/json"
	"testing"
	"time"
)

func metricKnown[T any](value T, sampledAt time.Time) Metric[T] {
	return Metric[T]{Status: MetricKnown, Value: &value, SampledAt: sampledAt}
}

func validMetricsSnapshot(at time.Time) MetricsSnapshot {
	memory := Memory{TotalBytes: 1000, AvailableBytes: 400, UsedBytes: 600, UsedPercent: 60}
	disk := DiskUsage{TotalBytes: 1000, AvailableBytes: 400, UsedBytes: 600, UsedPercent: 60}
	rate := NetworkRate{ReceivedBytesPerSecond: 10, SentBytesPerSecond: 20}
	uptime := Uptime{Seconds: 180, BootID: "boot-a"}
	return MetricsSnapshot{
		CollectedAt: at,
		System: MetricsSystem{
			Hostname:        metricKnown("host-a", at),
			OS:              metricKnown("linux", at),
			Architecture:    metricKnown("amd64", at),
			Platform:        metricKnown("ubuntu", at),
			PlatformFamily:  metricKnown("debian", at),
			PlatformVersion: metricKnown("24.04", at),
			KernelVersion:   metricKnown("6.8.0", at),
		},
		CPU: MetricsCPU{
			UsagePercent: metricKnown(12.5, at),
			LogicalCores: metricKnown(4, at),
		},
		Memory: metricKnown(memory, at),
		Network: MetricsNetwork{
			Summary: metricKnown(rate, at),
			Interfaces: []MetricsInterface{{
				Name: "eth0", IncludedInSummary: true, Rate: metricKnown(rate, at),
			}},
		},
		Disk: MetricsDisk{
			Status: MetricKnown, SampledAt: at,
			Mounts: []MetricsMount{{Device: "/dev/vda1", Mountpoint: "/", Filesystem: "ext4", Usage: metricKnown(disk, at)}},
		},
		Uptime: metricKnown(uptime, at),
	}
}

func TestMetricsSnapshotValidationPreservesExplicitFailureAndCoreStale(t *testing.T) {
	at := time.Now().UTC()
	report := validMetricsSnapshot(at)
	report.Memory = Metric[Memory]{Status: MetricUnknown, Reason: "permission_denied", SampledAt: at}
	if err := ValidateAgentMetricsSnapshot(report); err != nil {
		t.Fatalf("unknown memory with a reason should validate: %v", err)
	}
	encoded, err := MarshalMetricsSnapshot(report)
	if err != nil {
		t.Fatalf("MarshalMetricsSnapshot: %v", err)
	}
	var decoded MetricsSnapshot
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Memory.Status != MetricUnknown || decoded.Memory.Value != nil || decoded.Memory.Reason != "permission_denied" {
		t.Fatalf("round trip changed unknown state: %#v", decoded.Memory)
	}

	stale := validMetricsSnapshot(at)
	stale.CPU.UsagePercent.Status = MetricStale
	stale.CPU.UsagePercent.Reason = "sample_expired"
	if err := ValidateAgentMetricsSnapshot(stale); err == nil {
		t.Fatal("Agent report must not assign Core-derived stale state")
	}
	if err := ValidateMetricsSnapshot(stale); err != nil {
		t.Fatalf("Core API stale metric should validate: %v", err)
	}
}

func TestMetricsSampleAgeAndPayloadBounds(t *testing.T) {
	at := time.Now().UTC()
	invalidAge := validMetricsSnapshot(at)
	invalidAge.CPU.UsagePercent.SampleAgeMillis = -1
	if err := ValidateAgentMetricsSnapshot(invalidAge); err == nil {
		t.Fatal("negative sample age must be rejected")
	}

	tooManyInterfaces := validMetricsSnapshot(at)
	tooManyInterfaces.Network.Interfaces = make([]MetricsInterface, MaxMetricsInterfaces+1)
	if err := ValidateAgentMetricsSnapshot(tooManyInterfaces); err == nil {
		t.Fatal("oversized interface list must be rejected")
	}
}
