package metrics

import (
	"strings"
	"time"

	"github.com/CST-Cat/NodeDance/internal/protocol"
)

// ToProtocolMetrics converts a Collector snapshot to the shared wire DTO. It
// intentionally does not add Agent identity, connection generation, or message
// sequence; those values come from the authenticated Agent runtime.
func ToProtocolMetrics(snapshot Snapshot) protocol.MetricsSnapshot {
	return ToProtocolMetricsAt(snapshot, time.Now())
}

// ToProtocolMetricsAt allows deterministic testing of monotonic sample age.
// Production callers use ToProtocolMetrics so age is captured just before the
// report is serialized and sent.
func ToProtocolMetricsAt(snapshot Snapshot, serializedAt time.Time) protocol.MetricsSnapshot {
	return protocol.MetricsSnapshot{
		CollectedAt: snapshot.CollectedAt.UTC(),
		System: protocol.MetricsSystem{
			Hostname:        toProtocolMetric(snapshot.System.Hostname, serializedAt),
			OS:              toProtocolMetric(snapshot.System.OS, serializedAt),
			Architecture:    toProtocolMetric(snapshot.System.Architecture, serializedAt),
			Platform:        toProtocolMetric(snapshot.System.Platform, serializedAt),
			PlatformFamily:  toProtocolMetric(snapshot.System.PlatformFamily, serializedAt),
			PlatformVersion: toProtocolMetric(snapshot.System.PlatformVersion, serializedAt),
			KernelVersion:   toProtocolMetric(snapshot.System.KernelVersion, serializedAt),
		},
		CPU: protocol.MetricsCPU{
			UsagePercent: toProtocolMetric(snapshot.CPU.UsagePercent, serializedAt),
			LogicalCores: toProtocolMetric(snapshot.CPU.LogicalCores, serializedAt),
		},
		Memory: toProtocolConvertedMetric(snapshot.Memory, serializedAt, func(value Memory) protocol.Memory {
			return protocol.Memory(value)
		}),
		Network: protocol.MetricsNetwork{
			Summary: toProtocolConvertedMetric(snapshot.Network.Summary, serializedAt, func(value NetworkRate) protocol.NetworkRate {
				return protocol.NetworkRate(value)
			}),
			Interfaces: mapNetworkInterfaces(snapshot.Network.Interfaces, serializedAt),
		},
		Disk: protocol.MetricsDisk{
			Status:          protocolStatus(snapshot.Disk.State, snapshot.Disk.Reason, len(snapshot.Disk.Mounts) > 0),
			Reason:          diskReason(snapshot.Disk),
			SampledAt:       snapshot.Disk.SampledAt.UTC(),
			SampleAgeMillis: sampleAgeMillis(snapshot.Disk.SampledAt, serializedAt),
			Mounts:          mapDiskMounts(snapshot.Disk.Mounts, serializedAt),
		},
		Uptime: toProtocolUptime(snapshot.Uptime, serializedAt),
	}
}

func toProtocolUptime(metric Metric[Uptime], serializedAt time.Time) protocol.Metric[protocol.Uptime] {
	out := toProtocolConvertedMetric(metric, serializedAt, func(value Uptime) protocol.Uptime {
		return protocol.Uptime(value)
	})
	if metric.Value != nil && out.Value != nil && metric.Value.BootTimeUnix != nil {
		bootTime := *metric.Value.BootTimeUnix
		out.Value.BootTimeUnix = &bootTime
	}
	return out
}

func mapNetworkInterfaces(in []NetworkInterface, serializedAt time.Time) []protocol.MetricsInterface {
	out := make([]protocol.MetricsInterface, 0, len(in))
	for _, item := range in {
		converted := protocol.MetricsInterface{
			Name:              item.Name,
			IncludedInSummary: item.IncludedInSummary,
			SummaryReason:     item.SummaryReason,
			Rate: toProtocolConvertedMetric(item.Rate, serializedAt, func(value NetworkRate) protocol.NetworkRate {
				return protocol.NetworkRate(value)
			}),
		}
		if item.Up != nil {
			up := *item.Up
			converted.Up = &up
		}
		out = append(out, converted)
	}
	return out
}

func mapDiskMounts(in []DiskMount, serializedAt time.Time) []protocol.MetricsMount {
	out := make([]protocol.MetricsMount, 0, len(in))
	for _, item := range in {
		out = append(out, protocol.MetricsMount{
			Device:     item.Device,
			Mountpoint: item.Mountpoint,
			Filesystem: item.Filesystem,
			Usage: toProtocolConvertedMetric(item.Usage, serializedAt, func(value DiskUsage) protocol.DiskUsage {
				return protocol.DiskUsage(value)
			}),
		})
	}
	return out
}

func toProtocolMetric[T any](metric Metric[T], serializedAt time.Time) protocol.Metric[T] {
	status := protocolStatus(metric.State, metric.Reason, metric.Value != nil)
	out := protocol.Metric[T]{
		Status:          status,
		Reason:          metric.Reason,
		SampledAt:       metric.SampledAt.UTC(),
		SampleAgeMillis: sampleAgeMillis(metric.SampledAt, serializedAt),
	}
	if metric.Value != nil && (status == protocol.MetricKnown || status == protocol.MetricStale) {
		value := *metric.Value
		out.Value = &value
	}
	if status == protocol.MetricError && out.Reason == "" {
		out.Reason = "invalid_metric"
	}
	if status == protocol.MetricKnown {
		out.Reason = metric.Reason
	}
	return out
}

func toProtocolConvertedMetric[A, B any](metric Metric[A], serializedAt time.Time, convert func(A) B) protocol.Metric[B] {
	status := protocolStatus(metric.State, metric.Reason, metric.Value != nil)
	out := protocol.Metric[B]{
		Status:          status,
		Reason:          metric.Reason,
		SampledAt:       metric.SampledAt.UTC(),
		SampleAgeMillis: sampleAgeMillis(metric.SampledAt, serializedAt),
	}
	if metric.Value != nil && (status == protocol.MetricKnown || status == protocol.MetricStale) {
		value := convert(*metric.Value)
		out.Value = &value
	}
	if status == protocol.MetricError && out.Reason == "" {
		out.Reason = "invalid_metric"
	}
	return out
}

func diskReason(disk DiskSnapshot) string {
	if disk.Reason != "" {
		return disk.Reason
	}
	if protocolStatus(disk.State, disk.Reason, len(disk.Mounts) > 0) == protocol.MetricError {
		return "invalid_metric"
	}
	return ""
}

func protocolStatus(state State, reason string, hasValue bool) protocol.MetricStatus {
	switch state {
	case StateOK, StatePartial:
		if hasValue {
			return protocol.MetricKnown
		}
		return protocol.MetricError
	case StateUnknown:
		if hasValue {
			return protocol.MetricError
		}
		if sourceFailureReason(reason) {
			return protocol.MetricError
		}
		return protocol.MetricUnknown
	default:
		return protocol.MetricError
	}
}

func sourceFailureReason(reason string) bool {
	switch strings.TrimSpace(reason) {
	case "timeout", "unavailable", "read_failed", "invalid_data", "invalid_interval":
		return true
	default:
		// Permission denial, CPU warm-up/counter reset, unavailable topology,
		// and asynchronous disk states remain unknown with their exact reason.
		return false
	}
}

func sampleAgeMillis(sampledAt, serializedAt time.Time) int64 {
	if sampledAt.IsZero() || serializedAt.IsZero() {
		return 0
	}
	age := serializedAt.Sub(sampledAt)
	if age < 0 {
		return 0
	}
	if age > time.Duration(protocol.MaxMetricSampleAgeMS)*time.Millisecond {
		return protocol.MaxMetricSampleAgeMS
	}
	return age.Milliseconds()
}
