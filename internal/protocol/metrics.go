package protocol

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"time"
)

const (
	// TypeMetrics is carried in an Envelope whose generation is assigned by
	// Core and whose sequence is monotonic for metrics messages only.
	TypeMetrics = "metrics"

	// CapabilityMetrics is negotiated at connection setup before Core accepts
	// TypeMetrics frames from an Agent.
	CapabilityMetrics = "agent.metrics.v1"

	// MaxMetricsPayloadBytes matches the Core Agent payload decoder bound.
	MaxMetricsPayloadBytes = 64 << 10
	MaxMetricsInterfaces   = 256
	MaxMetricsMounts       = 256
	MaxMetricSampleAgeMS   = int64((7 * 24 * time.Hour) / time.Millisecond)
)

type MetricStatus string

const (
	MetricKnown   MetricStatus = "known"
	MetricUnknown MetricStatus = "unknown"
	MetricError   MetricStatus = "error"
	MetricStale   MetricStatus = "stale"
)

// Metric preserves absence as an explicit state. A missing or failed read is
// never represented by a zero-valued Value.
type Metric[T any] struct {
	Status          MetricStatus `json:"status"`
	Value           *T           `json:"value,omitempty"`
	Reason          string       `json:"reason,omitempty"`
	SampledAt       time.Time    `json:"sampledAt"`
	SampleAgeMillis int64        `json:"sampleAgeMillis"`
}

type MetricsSnapshot struct {
	CollectedAt time.Time      `json:"collectedAt"`
	System      MetricsSystem  `json:"system"`
	CPU         MetricsCPU     `json:"cpu"`
	Memory      Metric[Memory] `json:"memory"`
	Network     MetricsNetwork `json:"network"`
	Disk        MetricsDisk    `json:"disk"`
	Uptime      Metric[Uptime] `json:"uptime"`
}

type MetricsSystem struct {
	Hostname        Metric[string] `json:"hostname"`
	OS              Metric[string] `json:"os"`
	Architecture    Metric[string] `json:"architecture"`
	Platform        Metric[string] `json:"platform"`
	PlatformFamily  Metric[string] `json:"platformFamily"`
	PlatformVersion Metric[string] `json:"platformVersion"`
	KernelVersion   Metric[string] `json:"kernelVersion"`
}

type MetricsCPU struct {
	UsagePercent Metric[float64] `json:"usagePercent"`
	LogicalCores Metric[int]     `json:"logicalCores"`
}

type Memory struct {
	TotalBytes     uint64  `json:"totalBytes"`
	AvailableBytes uint64  `json:"availableBytes"`
	UsedBytes      uint64  `json:"usedBytes"`
	UsedPercent    float64 `json:"usedPercent"`
}

type NetworkRate struct {
	ReceivedBytesPerSecond float64 `json:"receivedBytesPerSecond"`
	SentBytesPerSecond     float64 `json:"sentBytesPerSecond"`
}

type MetricsInterface struct {
	Name              string              `json:"name"`
	Up                *bool               `json:"up,omitempty"`
	IncludedInSummary bool                `json:"includedInSummary"`
	SummaryReason     string              `json:"summaryReason,omitempty"`
	Rate              Metric[NetworkRate] `json:"rate"`
}

type MetricsNetwork struct {
	Summary    Metric[NetworkRate] `json:"summary"`
	Interfaces []MetricsInterface  `json:"interfaces"`
}

type DiskUsage struct {
	TotalBytes     uint64  `json:"totalBytes"`
	AvailableBytes uint64  `json:"availableBytes"`
	UsedBytes      uint64  `json:"usedBytes"`
	UsedPercent    float64 `json:"usedPercent"`
}

type MetricsMount struct {
	Device     string            `json:"device"`
	Mountpoint string            `json:"mountpoint"`
	Filesystem string            `json:"filesystem"`
	Usage      Metric[DiskUsage] `json:"usage"`
}

type MetricsDisk struct {
	Status          MetricStatus   `json:"status"`
	Reason          string         `json:"reason,omitempty"`
	SampledAt       time.Time      `json:"sampledAt"`
	SampleAgeMillis int64          `json:"sampleAgeMillis"`
	Mounts          []MetricsMount `json:"mounts"`
}

type Uptime struct {
	Seconds      uint64  `json:"seconds"`
	BootID       string  `json:"bootId"`
	BootTimeUnix *uint64 `json:"bootTimeUnix,omitempty"`
}

// MarshalMetricsSnapshot validates and bounds the payload before an Agent puts
// it in an Envelope. Envelope generation and sequence are supplied separately.
func MarshalMetricsSnapshot(snapshot MetricsSnapshot) ([]byte, error) {
	if err := ValidateAgentMetricsSnapshot(snapshot); err != nil {
		return nil, err
	}
	data, err := json.Marshal(snapshot)
	if err != nil {
		return nil, err
	}
	if len(data) > MaxMetricsPayloadBytes {
		return nil, fmt.Errorf("metrics payload is %d bytes; limit is %d", len(data), MaxMetricsPayloadBytes)
	}
	return data, nil
}

// ValidateAgentMetricsSnapshot accepts known/unknown/error measurements from
// an Agent. Stale is derived by Core using its own receive time and node lease.
func ValidateAgentMetricsSnapshot(snapshot MetricsSnapshot) error {
	return validateMetricsSnapshot(snapshot, false)
}

// ValidateMetricsSnapshot also accepts Core-derived stale values for API views.
func ValidateMetricsSnapshot(snapshot MetricsSnapshot) error {
	return validateMetricsSnapshot(snapshot, true)
}

func validateMetricsSnapshot(snapshot MetricsSnapshot, allowStale bool) error {
	if snapshot.CollectedAt.IsZero() {
		return errors.New("metrics collectedAt is required")
	}
	for name, value := range map[string]Metric[string]{
		"system.hostname":        snapshot.System.Hostname,
		"system.os":              snapshot.System.OS,
		"system.architecture":    snapshot.System.Architecture,
		"system.platform":        snapshot.System.Platform,
		"system.platformFamily":  snapshot.System.PlatformFamily,
		"system.platformVersion": snapshot.System.PlatformVersion,
		"system.kernelVersion":   snapshot.System.KernelVersion,
	} {
		if err := validateMetric(name, value, allowStale); err != nil {
			return err
		}
		if value.Value != nil && len(*value.Value) > 256 {
			return fmt.Errorf("%s value exceeds 256 bytes", name)
		}
	}
	if err := validateMetric("cpu.usagePercent", snapshot.CPU.UsagePercent, allowStale); err != nil {
		return err
	}
	if snapshot.CPU.UsagePercent.Value != nil && !percent(*snapshot.CPU.UsagePercent.Value) {
		return errors.New("cpu.usagePercent must be finite and between 0 and 100")
	}
	if err := validateMetric("cpu.logicalCores", snapshot.CPU.LogicalCores, allowStale); err != nil {
		return err
	}
	if snapshot.CPU.LogicalCores.Value != nil && *snapshot.CPU.LogicalCores.Value < 1 {
		return errors.New("cpu.logicalCores must be positive")
	}
	if err := validateMetric("memory", snapshot.Memory, allowStale); err != nil {
		return err
	}
	if value := snapshot.Memory.Value; value != nil {
		if value.TotalBytes == 0 || value.AvailableBytes > value.TotalBytes || value.UsedBytes != value.TotalBytes-value.AvailableBytes || !percent(value.UsedPercent) {
			return errors.New("memory value is inconsistent")
		}
	}
	if err := validateMetric("network.summary", snapshot.Network.Summary, allowStale); err != nil {
		return err
	}
	if value := snapshot.Network.Summary.Value; value != nil && !validNetworkRate(*value) {
		return errors.New("network.summary contains an invalid rate")
	}
	if len(snapshot.Network.Interfaces) > MaxMetricsInterfaces {
		return fmt.Errorf("metrics report has more than %d network interfaces", MaxMetricsInterfaces)
	}
	for index, iface := range snapshot.Network.Interfaces {
		if iface.Name == "" || len(iface.Name) > 128 || len(iface.SummaryReason) > 128 {
			return fmt.Errorf("network.interfaces[%d] has invalid metadata", index)
		}
		if err := validateMetric("network.interfaces["+iface.Name+"].rate", iface.Rate, allowStale); err != nil {
			return err
		}
		if value := iface.Rate.Value; value != nil && !validNetworkRate(*value) {
			return fmt.Errorf("network interface %q has an invalid rate", iface.Name)
		}
	}
	if err := validateDisk(snapshot.Disk, allowStale); err != nil {
		return err
	}
	if err := validateMetric("uptime", snapshot.Uptime, allowStale); err != nil {
		return err
	}
	if value := snapshot.Uptime.Value; value != nil && (value.BootID == "" || len(value.BootID) > 128) {
		return errors.New("uptime bootId is required and limited to 128 bytes")
	}
	return nil
}

func validateDisk(disk MetricsDisk, allowStale bool) error {
	if err := validateSampleAge("disk", disk.SampleAgeMillis); err != nil {
		return err
	}
	if err := validateStatus("disk", disk.Status, disk.Reason, disk.SampledAt, len(disk.Mounts) > 0, allowStale); err != nil {
		// `not_sampled` is the only valid zero timestamp: the Collector has not
		// completed its first asynchronous 30-second scan yet.
		if !(disk.Status == MetricUnknown && disk.Reason == "not_sampled" && disk.SampledAt.IsZero()) {
			return err
		}
	}
	if len(disk.Reason) > 128 {
		return errors.New("disk.reason exceeds 128 bytes")
	}
	if len(disk.Mounts) > MaxMetricsMounts {
		return fmt.Errorf("metrics report has more than %d disk mounts", MaxMetricsMounts)
	}
	for index, mount := range disk.Mounts {
		if len(mount.Device) > 256 || mount.Mountpoint == "" || len(mount.Mountpoint) > 1024 || len(mount.Filesystem) > 128 {
			return fmt.Errorf("disk.mounts[%d] has invalid metadata", index)
		}
		if err := validateMetric("disk.mounts["+mount.Mountpoint+"].usage", mount.Usage, allowStale); err != nil {
			return err
		}
		if value := mount.Usage.Value; value != nil {
			if value.TotalBytes == 0 || value.AvailableBytes > value.TotalBytes || value.UsedBytes > value.TotalBytes || !percent(value.UsedPercent) {
				return fmt.Errorf("disk mount %q has an invalid usage value", mount.Mountpoint)
			}
		}
	}
	return nil
}

func validateMetric[T any](name string, metric Metric[T], allowStale bool) error {
	if len(metric.Reason) > 128 {
		return fmt.Errorf("%s.reason exceeds 128 bytes", name)
	}
	if err := validateSampleAge(name, metric.SampleAgeMillis); err != nil {
		return err
	}
	return validateStatus(name, metric.Status, metric.Reason, metric.SampledAt, metric.Value != nil, allowStale)
}

func validateStatus(name string, status MetricStatus, reason string, sampledAt time.Time, hasValue, allowStale bool) error {
	if sampledAt.IsZero() && !(status == MetricUnknown && reason == "not_sampled") {
		return fmt.Errorf("%s.sampledAt is required", name)
	}
	switch status {
	case MetricKnown:
		if !hasValue {
			return fmt.Errorf("%s is known but has no value", name)
		}
	case MetricUnknown, MetricError:
		if hasValue || reason == "" {
			return fmt.Errorf("%s must provide a reason and omit value for status %q", name, status)
		}
	case MetricStale:
		if !allowStale || !hasValue || reason == "" {
			return fmt.Errorf("%s has invalid stale state", name)
		}
	default:
		return fmt.Errorf("%s has unsupported status %q", name, status)
	}
	return nil
}

func validateSampleAge(name string, sampleAgeMillis int64) error {
	if sampleAgeMillis < 0 || sampleAgeMillis > MaxMetricSampleAgeMS {
		return fmt.Errorf("%s.sampleAgeMillis must be between 0 and %d", name, MaxMetricSampleAgeMS)
	}
	return nil
}

func validNetworkRate(rate NetworkRate) bool {
	return finite(rate.ReceivedBytesPerSecond) && rate.ReceivedBytesPerSecond >= 0 &&
		finite(rate.SentBytesPerSecond) && rate.SentBytesPerSecond >= 0
}

func percent(value float64) bool {
	return finite(value) && value >= 0 && value <= 100
}

func finite(value float64) bool { return !math.IsNaN(value) && !math.IsInf(value, 0) }
