// Package metrics samples host statistics for the Agent.
//
// It has no dependency on the Core, browser sessions, or Docker. Consumers may
// marshal Snapshot as JSON, but transport and node identity are owned by the
// Agent protocol layer.
package metrics

import "time"

type State string

const (
	StateOK      State = "ok"
	StatePartial State = "partial"
	StateUnknown State = "unknown"
)

// Metric never uses a zero value to mean that a measurement failed. Value is
// nil when State is unknown, and Reason contains a stable machine-readable
// explanation such as permission_denied, warming_up, or counter_reset.
type Metric[T any] struct {
	State     State     `json:"state"`
	Value     *T        `json:"value,omitempty"`
	Reason    string    `json:"reason,omitempty"`
	SampledAt time.Time `json:"sampledAt"`
}

type Snapshot struct {
	CollectedAt time.Time       `json:"collectedAt"`
	System      SystemInfo      `json:"system"`
	CPU         CPUInfo         `json:"cpu"`
	Memory      Metric[Memory]  `json:"memory"`
	Network     NetworkSnapshot `json:"network"`
	Disk        DiskSnapshot    `json:"disk"`
	Uptime      Metric[Uptime]  `json:"uptime"`
}

type SystemInfo struct {
	Hostname        Metric[string] `json:"hostname"`
	OS              Metric[string] `json:"os"`
	Architecture    Metric[string] `json:"architecture"`
	Platform        Metric[string] `json:"platform"`
	PlatformFamily  Metric[string] `json:"platformFamily"`
	PlatformVersion Metric[string] `json:"platformVersion"`
	KernelVersion   Metric[string] `json:"kernelVersion"`
}

type CPUInfo struct {
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

type NetworkInterface struct {
	Name              string              `json:"name"`
	Up                *bool               `json:"up,omitempty"`
	IncludedInSummary bool                `json:"includedInSummary"`
	SummaryReason     string              `json:"summaryReason,omitempty"`
	Rate              Metric[NetworkRate] `json:"rate"`
}

type NetworkSnapshot struct {
	Summary    Metric[NetworkRate] `json:"summary"`
	Interfaces []NetworkInterface  `json:"interfaces"`
}

type DiskUsage struct {
	TotalBytes     uint64  `json:"totalBytes"`
	AvailableBytes uint64  `json:"availableBytes"`
	UsedBytes      uint64  `json:"usedBytes"`
	UsedPercent    float64 `json:"usedPercent"`
}

type DiskMount struct {
	Device     string            `json:"device"`
	Mountpoint string            `json:"mountpoint"`
	Filesystem string            `json:"filesystem"`
	Usage      Metric[DiskUsage] `json:"usage"`
}

type DiskSnapshot struct {
	State     State       `json:"state"`
	Reason    string      `json:"reason,omitempty"`
	SampledAt time.Time   `json:"sampledAt"`
	Mounts    []DiskMount `json:"mounts"`
}

type Uptime struct {
	Seconds      uint64  `json:"seconds"`
	BootID       string  `json:"bootId"`
	BootTimeUnix *uint64 `json:"bootTimeUnix,omitempty"`
}

func known[T any](value T, sampledAt time.Time) Metric[T] {
	return Metric[T]{State: StateOK, Value: &value, SampledAt: sampledAt}
}

func unknown[T any](reason string, sampledAt time.Time) Metric[T] {
	return Metric[T]{State: StateUnknown, Reason: reason, SampledAt: sampledAt}
}
