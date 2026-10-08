package metrics

import (
	"errors"
	"testing"
	"time"

	"github.com/CST-Cat/NodeDance/internal/protocol"
)

var testIdentity = Identity{AgentID: "agent-a", NodeID: "node-a"}

func protocolKnown[T any](value T, sampledAt time.Time, ageMillis int64) protocol.Metric[T] {
	return protocol.Metric[T]{
		Status:          protocol.MetricKnown,
		Value:           &value,
		SampledAt:       sampledAt,
		SampleAgeMillis: ageMillis,
	}
}

func validReport(collectedAt time.Time, bootID string, cpuAge, diskAge int64) protocol.MetricsSnapshot {
	sampledAt := collectedAt
	up := protocol.Uptime{Seconds: 900, BootID: bootID}
	usage := protocol.DiskUsage{TotalBytes: 1000, AvailableBytes: 400, UsedBytes: 600, UsedPercent: 60}
	rate := protocol.NetworkRate{ReceivedBytesPerSecond: 20, SentBytesPerSecond: 10}
	memory := protocol.Memory{TotalBytes: 1000, AvailableBytes: 400, UsedBytes: 600, UsedPercent: 60}
	return protocol.MetricsSnapshot{
		CollectedAt: collectedAt,
		System: protocol.MetricsSystem{
			Hostname:        protocolKnown("host-a", sampledAt, 0),
			OS:              protocolKnown("linux", sampledAt, 0),
			Architecture:    protocolKnown("amd64", sampledAt, 0),
			Platform:        protocolKnown("ubuntu", sampledAt, 0),
			PlatformFamily:  protocolKnown("debian", sampledAt, 0),
			PlatformVersion: protocolKnown("24.04", sampledAt, 0),
			KernelVersion:   protocolKnown("6.8.0", sampledAt, 0),
		},
		CPU: protocol.MetricsCPU{
			UsagePercent: protocolKnown(12.5, sampledAt, cpuAge),
			LogicalCores: protocolKnown(4, sampledAt, 0),
		},
		Memory: protocolKnown(memory, sampledAt, 0),
		Network: protocol.MetricsNetwork{
			Summary: protocolKnown(rate, sampledAt, cpuAge),
			Interfaces: []protocol.MetricsInterface{{
				Name: "eth0", IncludedInSummary: true,
				Rate: protocolKnown(rate, sampledAt, cpuAge),
			}},
		},
		Disk: protocol.MetricsDisk{
			Status: protocol.MetricKnown, SampledAt: sampledAt, SampleAgeMillis: diskAge,
			Mounts: []protocol.MetricsMount{{
				Device: "/dev/vda1", Mountpoint: "/", Filesystem: "ext4",
				Usage: protocolKnown(usage, sampledAt, diskAge),
			}},
		},
		Uptime: protocolKnown(up, sampledAt, 0),
	}
}

func activeLease(generation uint64, until time.Time) *Lease {
	return &Lease{Identity: testIdentity, Generation: generation, ValidUntil: until, Status: LeaseOnline}
}

func acceptReport(t *testing.T, store *Store, generation, sequence uint64, report protocol.MetricsSnapshot, receivedAt time.Time) {
	t.Helper()
	if err := store.Accept(testIdentity, generation, sequence, report, receivedAt); err != nil {
		t.Fatalf("Accept(sequence=%d): %v", sequence, err)
	}
}

func TestAcceptRequiresBoundIdentityGenerationAndIndependentSequence(t *testing.T) {
	store := NewStore()
	now := time.Now()
	report := validReport(time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC), "boot-a", 0, 0)
	if err := store.Accept(testIdentity, 1, 1, report, now); !errors.Is(err, ErrConnectionUnknown) {
		t.Fatalf("Accept before BindConnection error = %v, want ErrConnectionUnknown", err)
	}
	if err := store.BindConnection(testIdentity, 4); err != nil {
		t.Fatal(err)
	}
	if err := store.Accept(Identity{AgentID: "agent-other", NodeID: testIdentity.NodeID}, 4, 1, report, now); !errors.Is(err, ErrIdentityMismatch) {
		t.Fatalf("foreign identity error = %v, want ErrIdentityMismatch", err)
	}
	if err := store.Accept(testIdentity, 5, 1, report, now); !errors.Is(err, ErrGenerationMismatch) {
		t.Fatalf("unbound higher generation error = %v, want ErrGenerationMismatch", err)
	}

	acceptReport(t, store, 4, 9, report, now)
	if err := store.Accept(testIdentity, 4, 9, report, now.Add(time.Second)); !errors.Is(err, ErrStaleSequence) {
		t.Fatalf("duplicate sequence error = %v, want ErrStaleSequence", err)
	}
	if err := store.Accept(testIdentity, 4, 8, report, now.Add(time.Second)); !errors.Is(err, ErrStaleSequence) {
		t.Fatalf("lower sequence error = %v, want ErrStaleSequence", err)
	}
	if err := store.BindConnection(testIdentity, 3); !errors.Is(err, ErrStaleGeneration) {
		t.Fatalf("older binding error = %v, want ErrStaleGeneration", err)
	}
	if err := store.BindConnection(testIdentity, 5); err != nil {
		t.Fatal(err)
	}
	if err := store.Accept(testIdentity, 4, 10, report, now.Add(time.Second)); !errors.Is(err, ErrStaleGeneration) {
		t.Fatalf("old connection error = %v, want ErrStaleGeneration", err)
	}
	// Metrics sequence is reset for the new TypeMetrics connection generation.
	acceptReport(t, store, 5, 1, report, now.Add(2*time.Second))
	if store.UnbindConnection(testIdentity, 4) {
		t.Fatal("stale connection close unexpectedly unbound the current generation")
	}
	if !store.UnbindConnection(testIdentity, 5) {
		t.Fatal("current connection was not unbound")
	}
	if err := store.Accept(testIdentity, 5, 2, report, now.Add(3*time.Second)); !errors.Is(err, ErrConnectionUnknown) {
		t.Fatalf("Accept after unbind error = %v, want ErrConnectionUnknown", err)
	}
}

func TestSnapshotUsesLeaseStateGenerationAndMonotonicAge(t *testing.T) {
	store := NewStore()
	base := time.Now()
	if err := store.BindConnection(testIdentity, 7); err != nil {
		t.Fatal(err)
	}
	// Deliberately future wall timestamps must not freshen measurements.
	report := validReport(time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC), "boot-a", 0, 20_000)
	acceptReport(t, store, 7, 1, report, base)
	lease := activeLease(7, base.Add(2*time.Minute))

	initial, ok := store.SnapshotAt(testIdentity.NodeID, lease, base)
	if !ok || initial.NodeStatus != "online" || initial.Metrics.CPU.UsagePercent.Status != protocol.MetricKnown {
		t.Fatalf("initial snapshot did not stay online/fresh: %#v, ok=%v", initial, ok)
	}
	if initial.ClockOffsetMillis >= 0 {
		t.Fatalf("future Agent wall time should produce negative clock offset, got %f", initial.ClockOffsetMillis)
	}

	fastExpired, ok := store.SnapshotAt(testIdentity.NodeID, lease, base.Add(16*time.Second))
	if !ok {
		t.Fatal("SnapshotAt lost accepted metrics")
	}
	if fastExpired.Metrics.CPU.UsagePercent.Status != protocol.MetricStale || fastExpired.Metrics.Network.Summary.Status != protocol.MetricStale {
		t.Fatalf("fast metrics did not expire at 3x5s: cpu=%q net=%q", fastExpired.Metrics.CPU.UsagePercent.Status, fastExpired.Metrics.Network.Summary.Status)
	}
	if got := fastExpired.Metrics.Disk.Status; got != protocol.MetricKnown {
		t.Fatalf("disk should remain fresh at 20s sample age + 16s receive age, got %q", got)
	}
	if got := fastExpired.Metrics.Disk.SampleAgeMillis; got != 36_000 {
		t.Fatalf("disk sampleAgeMillis = %d, want 36000", got)
	}

	diskExpired, ok := store.SnapshotAt(testIdentity.NodeID, lease, base.Add(91*time.Second))
	if !ok || diskExpired.Metrics.Disk.Status != protocol.MetricStale {
		t.Fatalf("disk did not expire after 3x30s: %#v, ok=%v", diskExpired.Metrics.Disk, ok)
	}
	if diskExpired.Metrics.Disk.Reason != "sample_expired" {
		t.Fatalf("disk stale reason = %q, want sample_expired", diskExpired.Metrics.Disk.Reason)
	}

	// A reconnected authorized lease makes the node online immediately, while
	// values from the previous connection remain explicitly stale.
	if err := store.BindConnection(testIdentity, 8); err != nil {
		t.Fatal(err)
	}
	reconnected, ok := store.SnapshotAt(testIdentity.NodeID, activeLease(8, base.Add(2*time.Minute)), base.Add(time.Second))
	if !ok || reconnected.NodeStatus != "online" || reconnected.ActiveGeneration != 8 {
		t.Fatalf("reconnected node should be online before its first metrics report: %#v, ok=%v", reconnected, ok)
	}
	if reconnected.Generation != 7 || reconnected.Metrics.CPU.UsagePercent.Status != protocol.MetricStale || !contains(reconnected.Metrics.CPU.UsagePercent.Reason, "connection_generation_changed") {
		t.Fatalf("old-generation metrics not marked stale: generation=%d metric=%#v", reconnected.Generation, reconnected.Metrics.CPU.UsagePercent)
	}

	for name, inactive := range map[string]*Lease{
		"no active lease": nil,
		"revoked lease":   {Status: LeaseRevoked},
		"offline lease":   {Status: LeaseOffline},
		"expired lease":   activeLease(8, base),
	} {
		t.Run(name, func(t *testing.T) {
			view, ok := store.SnapshotAt(testIdentity.NodeID, inactive, base.Add(time.Second))
			if !ok || view.NodeStatus != "offline" || view.ActiveGeneration != 0 || !view.LeaseValidUntil.IsZero() {
				t.Fatalf("inactive lease exposed online state: %#v, ok=%v", view, ok)
			}
			if view.Metrics.CPU.UsagePercent.Status != protocol.MetricStale {
				t.Fatalf("inactive lease did not stale retained metrics: %#v", view.Metrics.CPU.UsagePercent)
			}
		})
	}
}

func TestSnapshotWallClockChangesDoNotAffectFreshness(t *testing.T) {
	store := NewStore()
	base := time.Now()
	if err := store.BindConnection(testIdentity, 1); err != nil {
		t.Fatal(err)
	}
	forwards := validReport(time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC), "boot-a", 14_000, 89_000)
	acceptReport(t, store, 1, 1, forwards, base)
	lease := activeLease(1, base.Add(2*time.Minute))
	// A future Agent timestamp is not enough to make a near-expired metric fresh.
	forwardView, ok := store.SnapshotAt(testIdentity.NodeID, lease, base.Add(2*time.Second))
	if !ok || forwardView.Metrics.CPU.UsagePercent.Status != protocol.MetricStale || forwardView.Metrics.Disk.Status != protocol.MetricStale {
		t.Fatalf("future wall timestamp changed sample-age expiration: %#v, ok=%v", forwardView.Metrics, ok)
	}

	backwardStore := NewStore()
	if err := backwardStore.BindConnection(testIdentity, 1); err != nil {
		t.Fatal(err)
	}
	backwards := validReport(time.Date(1970, 1, 1, 0, 0, 0, 0, time.UTC), "boot-a", 14_000, 89_000)
	acceptReport(t, backwardStore, 1, 1, backwards, base)
	backwardView, ok := backwardStore.SnapshotAt(testIdentity.NodeID, lease, base.Add(2*time.Second))
	if !ok || backwardView.Metrics.CPU.UsagePercent.Status != protocol.MetricStale || backwardView.Metrics.Disk.Status != protocol.MetricStale {
		t.Fatalf("backward wall timestamp changed sample-age expiration: %#v, ok=%v", backwardView.Metrics, ok)
	}
}

func TestPartialFailurePreservesLastKnownValueAndBootChange(t *testing.T) {
	store := NewStore()
	base := time.Now()
	if err := store.BindConnection(testIdentity, 2); err != nil {
		t.Fatal(err)
	}
	acceptReport(t, store, 2, 1, validReport(base, "boot-a", 0, 0), base)

	partial := validReport(base.Add(time.Second), "boot-a", 0, 0)
	partial.Memory = protocol.Metric[protocol.Memory]{
		Status: protocol.MetricUnknown, Reason: "permission_denied", SampledAt: base.Add(time.Second),
	}
	acceptReport(t, store, 2, 2, partial, base.Add(time.Second))
	view, ok := store.SnapshotAt(testIdentity.NodeID, activeLease(2, base.Add(time.Minute)), base.Add(time.Second))
	if !ok || view.Metrics.Memory.Status != protocol.MetricStale || view.Metrics.Memory.Value == nil || view.Metrics.Memory.Value.UsedPercent != 60 || view.Metrics.Memory.Reason != "permission_denied" {
		t.Fatalf("partial failure did not preserve prior memory with reason: %#v, ok=%v", view.Metrics.Memory, ok)
	}

	recovery := validReport(base.Add(2*time.Second), "boot-b", 0, 0)
	acceptReport(t, store, 2, 3, recovery, base.Add(2*time.Second))
	recovered, ok := store.SnapshotAt(testIdentity.NodeID, activeLease(2, base.Add(time.Minute)), base.Add(2*time.Second))
	if !ok || recovered.Metrics.Memory.Status != protocol.MetricKnown || recovered.BootID != "boot-b" || recovered.PreviousBootID != "boot-a" || !recovered.BootIDChangedAt.Equal(base.Add(2*time.Second).UTC()) {
		t.Fatalf("recovery/boot transition not recorded: %#v, ok=%v", recovered, ok)
	}
}

func TestMetricsAcceptDoesNotRenewHeartbeatLease(t *testing.T) {
	store := NewStore()
	base := time.Now()
	if err := store.BindConnection(testIdentity, 3); err != nil {
		t.Fatal(err)
	}
	lease := activeLease(3, base.Add(30*time.Second))
	acceptReport(t, store, 3, 1, validReport(base, "boot-a", 0, 0), base)
	acceptReport(t, store, 3, 2, validReport(base.Add(29*time.Second), "boot-a", 0, 0), base.Add(29*time.Second))

	view, ok := store.SnapshotAt(testIdentity.NodeID, lease, base.Add(31*time.Second))
	if !ok || view.NodeStatus != "offline" || view.ActiveGeneration != 0 || !view.LeaseValidUntil.IsZero() {
		t.Fatalf("metrics update extended the heartbeat lease: %#v, ok=%v", view, ok)
	}
}

func contains(value, part string) bool {
	for index := 0; index+len(part) <= len(value); index++ {
		if value[index:index+len(part)] == part {
			return true
		}
	}
	return false
}
