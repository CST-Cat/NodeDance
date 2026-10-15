package metrics

import (
	"testing"
	"time"

	"github.com/CST-Cat/NodeDance/internal/protocol"
)

func TestDiskIOOwnershipFailureAndExpiry(t *testing.T) {
	original := &protocol.MetricsHost{FastIntervalSeconds: 5, DiskIO: &protocol.Metric[protocol.DiskIO]{Status: protocol.MetricKnown, SampledAt: time.Now(), Value: &protocol.DiskIO{ReadBytesPerSecond: 100, WriteBytesPerSecond: 50}}}
	copy := cloneHost(original)
	copy.DiskIO.Value.ReadBytesPerSecond = 999
	advanceHost(copy, 16*time.Second)
	ageHost(copy)
	if original.DiskIO.Value.ReadBytesPerSecond != 100 || original.DiskIO.SampleAgeMillis != 0 || original.DiskIO.Status != protocol.MetricKnown {
		t.Fatal("snapshot modified source metric")
	}
	if copy.DiskIO.Status != protocol.MetricStale {
		t.Fatal("expired IO remained live")
	}
	incoming := &protocol.MetricsHost{FastIntervalSeconds: 5, DiskIO: &protocol.Metric[protocol.DiskIO]{Status: protocol.MetricUnknown, Reason: "counter_reset"}}
	merged := mergeHost(original, incoming, 5*time.Second)
	if merged.DiskIO.Status != protocol.MetricStale || merged.DiskIO.Reason != "counter_reset" || merged.DiskIO.SampleAgeMillis != 5000 || merged.DiskIO.Value.ReadBytesPerSecond != 100 {
		t.Fatal("failed sample lost stale semantics", merged.DiskIO)
	}
	merged.DiskIO.Value.ReadBytesPerSecond = 999
	if original.DiskIO.Value.ReadBytesPerSecond != 100 {
		t.Fatal("merge retained source value pointer")
	}
}
