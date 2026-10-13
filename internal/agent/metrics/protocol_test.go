package metrics

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/CST-Cat/NodeDance/internal/protocol"
)

func wireTestSnapshot(at time.Time) Snapshot {
	u := unknown[string]("not_sampled", time.Time{})
	return Snapshot{
		CollectedAt: at,
		System:      SystemInfo{Hostname: u, OS: u, Architecture: u, Platform: u, PlatformFamily: u, PlatformVersion: u, KernelVersion: u},
		CPU:         CPUInfo{UsagePercent: unknown[float64]("not_sampled", time.Time{}), LogicalCores: unknown[int]("not_sampled", time.Time{})},
		Memory:      unknown[Memory]("not_sampled", time.Time{}),
		Network:     NetworkSnapshot{Summary: unknown[NetworkRate]("not_sampled", time.Time{})},
		Disk:        DiskSnapshot{State: StateUnknown, Reason: "not_sampled"},
		Uptime:      unknown[Uptime]("not_sampled", time.Time{}),
	}
}

func TestMetricsTruncationIncludesMetadataInWireLimit(t *testing.T) {
	at := time.Unix(1700000000, 0).UTC()
	for padding := 0; padding <= 128; padding++ {
		snapshot := wireTestSnapshot(at)
		for i := 0; i < protocol.MaxMetricsInterfaces; i++ {
			snapshot.Network.Interfaces = append(snapshot.Network.Interfaces, NetworkInterface{
				Name: fmt.Sprintf("eth%d", i), SummaryReason: strings.Repeat("x", padding),
				Rate: unknown[NetworkRate](strings.Repeat("r", 128), at),
			})
		}
		bounded := ToProtocolMetricsAt(snapshot, at)
		if _, err := protocol.MarshalMetricsSnapshot(bounded); err != nil {
			t.Fatalf("padding %d: retained %d interfaces, %d bytes: %v", padding, len(bounded.Network.Interfaces), metricsPayloadSize(bounded), err)
		}
		if !bounded.Network.DetailsTruncated || bounded.Network.TruncationReason != "metrics_payload_limit" {
			t.Fatalf("missing network truncation metadata at padding %d", padding)
		}
	}
}

func TestMetricsTruncationCanRemoveAllDiskDetails(t *testing.T) {
	at := time.Unix(1700000000, 0).UTC()
	snapshot := wireTestSnapshot(at)
	for i := 0; i < protocol.MaxMetricsInterfaces; i++ {
		snapshot.Network.Interfaces = append(snapshot.Network.Interfaces, NetworkInterface{
			Name: fmt.Sprintf("eth%d", i), SummaryReason: strings.Repeat("x", 128),
			Rate: unknown[NetworkRate](strings.Repeat("r", 128), at),
		})
	}
	snapshot.Disk = DiskSnapshot{State: StateOK, SampledAt: at, Mounts: []DiskMount{
		{Mountpoint: "/", Usage: known(DiskUsage{TotalBytes: 100, AvailableBytes: 50, UsedBytes: 50, UsedPercent: 50}, at)},
	}}
	bounded := ToProtocolMetricsAt(snapshot, at)
	if len(bounded.Disk.Mounts) != 0 || !bounded.Disk.DetailsTruncated {
		t.Fatal("expected omitted disk details to be marked")
	}
	if _, err := protocol.MarshalMetricsSnapshot(bounded); err != nil {
		t.Fatalf("truncation produced invalid wire state: %v", err)
	}
}

func TestMetricsTruncationRetainsFittingDiskPrefix(t *testing.T) {
	at := time.Unix(1700000000, 0).UTC()
	snapshot := wireTestSnapshot(at)
	snapshot.Disk.State, snapshot.Disk.Reason, snapshot.Disk.SampledAt = StateOK, "", at
	for i := 0; i < protocol.MaxMetricsMounts; i++ {
		snapshot.Disk.Mounts = append(snapshot.Disk.Mounts, DiskMount{
			Mountpoint: fmt.Sprintf("/%03d%s", i, strings.Repeat("a", 1000)),
			Usage:      known(DiskUsage{TotalBytes: 100, AvailableBytes: 50, UsedBytes: 50, UsedPercent: 50}, at),
		})
	}
	bounded := ToProtocolMetricsAt(snapshot, at)
	if len(bounded.Disk.Mounts) == 0 || !bounded.Disk.DetailsTruncated || bounded.Disk.Status != protocol.MetricKnown {
		t.Fatal("partial truncation unnecessarily lost known disk data")
	}
	if _, err := protocol.MarshalMetricsSnapshot(bounded); err != nil {
		t.Fatal(err)
	}
	for i, mount := range bounded.Disk.Mounts {
		if mount.Mountpoint != snapshot.Disk.Mounts[i].Mountpoint {
			t.Fatal("truncation changed disk order")
		}
	}
	// One more original detail must no longer fit, including the flags.
	next := snapshot.Disk.Mounts[len(bounded.Disk.Mounts)]
	bounded.Disk.Mounts = append(bounded.Disk.Mounts, protocol.MetricsMount{Mountpoint: next.Mountpoint, Usage: bounded.Disk.Mounts[0].Usage})
	if metricsPayloadSize(bounded) <= protocol.MaxMetricsPayloadBytes {
		t.Fatal("discarded a disk detail that still fits")
	}
}
