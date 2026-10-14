package protocol

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"math"
	"net"
	"net/url"
	"path/filepath"
	"strings"
	"time"
)

const (
	MaxCommandScriptBytes    = 32 * 1024
	MaxCommandRequestBytes   = 6*MaxCommandScriptBytes + 16*1024
	MinCommandTimeoutSeconds = 1
	MaxCommandTimeoutSeconds = 3600
	CapabilityHost           = "agent.host.v1"
	TypeHostRequest          = "host_request"
	TypeHostResponse         = "host_response"
)

// AgentSettings contains no enrollment or device secrets. It is the shared
// configuration contract for all administrator interfaces and the Agent.
type AgentSettings struct {
	DisableRemoteControl        bool     `json:"disableRemoteControl"`
	MemoryMode                  string   `json:"memoryMode" request:"required"`
	CustomIPv4                  string   `json:"customIpv4,omitempty"`
	CustomIPv6                  string   `json:"customIpv6,omitempty"`
	FastIntervalSeconds         int      `json:"fastIntervalSeconds" request:"required"`
	DiskIntervalSeconds         int      `json:"diskIntervalSeconds" request:"required"`
	IncludeInterfaces           []string `json:"includeInterfaces"`
	ExcludeInterfaces           []string `json:"excludeInterfaces"`
	IncludeMountpoints          []string `json:"includeMountpoints"`
	BillingDay                  int      `json:"billingDay" request:"required"`
	EnableGPU                   bool     `json:"enableGpu"`
	DNS                         string   `json:"dns,omitempty"`
	PreferIPVersion             string   `json:"preferIpVersion,omitempty"`
	Compression                 bool     `json:"compression"`
	Transport                   string   `json:"transport" request:"required"`
	PublicIPURL                 string   `json:"publicIpUrl,omitempty"`
	AutoUpdate                  bool     `json:"autoUpdate"`
	UpdateIntervalHours         int      `json:"updateIntervalHours" request:"required"`
	CommandHistoryRetentionDays int      `json:"commandHistoryRetentionDays" request:"required"`
}

func DefaultAgentSettings() AgentSettings {
	return AgentSettings{MemoryMode: "available", FastIntervalSeconds: 5, DiskIntervalSeconds: 30, BillingDay: 1, EnableGPU: true, Compression: true, Transport: "auto", UpdateIntervalHours: 6, CommandHistoryRetentionDays: 90, IncludeInterfaces: []string{}, ExcludeInterfaces: []string{}, IncludeMountpoints: []string{}}
}
func ValidateAgentSettings(s AgentSettings) error {
	if s.CommandHistoryRetentionDays < 1 || s.CommandHistoryRetentionDays > 3650 {
		return errors.New("command history retention must be between 1 and 3650 days")
	}
	if s.FastIntervalSeconds < 1 || s.FastIntervalSeconds > 60 || s.DiskIntervalSeconds < 5 || s.DiskIntervalSeconds > 300 || s.BillingDay < 1 || s.BillingDay > 28 || s.UpdateIntervalHours < 1 || s.UpdateIntervalHours > 168 {
		return errors.New("invalid sampling, billing or update interval")
	}
	if s.MemoryMode != "available" && s.MemoryMode != "raw" && s.MemoryMode != "cached" {
		return errors.New("memoryMode must be available, raw or cached")
	}
	for i, v := range []string{s.CustomIPv4, s.CustomIPv6} {
		if v != "" {
			ip := net.ParseIP(v)
			if ip == nil || (ip.To4() != nil) != (i == 0) {
				return errors.New("invalid configured IP address")
			}
		}
	}
	if s.Transport != "auto" && s.Transport != "websocket" && s.Transport != "http" {
		return errors.New("transport must be auto, websocket or http")
	}
	if s.PreferIPVersion != "" && s.PreferIPVersion != "4" && s.PreferIPVersion != "6" {
		return errors.New("IP preference must be empty, 4 or 6")
	}
	if s.DNS != "" {
		host, port, err := net.SplitHostPort(s.DNS)
		if err != nil || net.ParseIP(host) == nil || port != "53" {
			return errors.New("DNS must be a literal IP with port 53")
		}
	}
	for _, patterns := range [][]string{s.IncludeInterfaces, s.ExcludeInterfaces} {
		if len(patterns) > 32 {
			return errors.New("too many interface patterns")
		}
		for _, p := range patterns {
			if len(p) == 0 || len(p) > 128 || strings.ContainsAny(p, "/\\\x00\r\n") {
				return errors.New("invalid interface pattern")
			}
			if _, err := filepath.Match(p, "eth0"); err != nil {
				return err
			}
		}
	}
	if len(s.IncludeMountpoints) > 64 {
		return errors.New("too many mountpoints")
	}
	for _, p := range s.IncludeMountpoints {
		if !filepath.IsAbs(p) || filepath.Clean(p) != p || len(p) > 2048 || strings.ContainsAny(p, "\x00\r\n") {
			return errors.New("mountpoints must be clean absolute paths")
		}
	}
	if s.PublicIPURL != "" {
		u, e := url.Parse(s.PublicIPURL)
		if e != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Fragment != "" || len(s.PublicIPURL) > 2048 {
			return errors.New("public IP URL must be HTTPS without credentials")
		}
	}
	return nil
}

type Swap struct {
	TotalBytes  uint64  `json:"totalBytes"`
	UsedBytes   uint64  `json:"usedBytes"`
	UsedPercent float64 `json:"usedPercent"`
}
type LoadAverage struct {
	One     float64 `json:"one"`
	Five    float64 `json:"five"`
	Fifteen float64 `json:"fifteen"`
}
type SocketCounts struct {
	TCP int `json:"tcp"`
	UDP int `json:"udp"`
}
type CPUAsset struct {
	Model         string `json:"model"`
	PhysicalCores int    `json:"physicalCores"`
}
type MemoryBreakdown struct {
	RawUsedBytes   uint64 `json:"rawUsedBytes"`
	AvailableBytes uint64 `json:"availableBytes"`
	FreeBytes      uint64 `json:"freeBytes"`
	CachedBytes    uint64 `json:"cachedBytes"`
	BufferedBytes  uint64 `json:"bufferedBytes"`
	SharedBytes    uint64 `json:"sharedBytes"`
}
type HostAddress struct {
	Interface string `json:"interface"`
	Address   string `json:"address"`
	Family    int    `json:"family"`
}
type GPUDevice struct {
	ID                 string   `json:"id"`
	Name               string   `json:"name"`
	Utilization        *float64 `json:"utilization,omitempty"`
	MemoryTotalBytes   *uint64  `json:"memoryTotalBytes,omitempty"`
	MemoryUsedBytes    *uint64  `json:"memoryUsedBytes,omitempty"`
	TemperatureCelsius *float64 `json:"temperatureCelsius,omitempty"`
}
type Traffic struct {
	ReceivedBytes       uint64    `json:"receivedBytes"`
	SentBytes           uint64    `json:"sentBytes"`
	PeriodReceivedBytes uint64    `json:"periodReceivedBytes"`
	PeriodSentBytes     uint64    `json:"periodSentBytes"`
	PeriodStart         time.Time `json:"periodStart"`
	CoverageStart       time.Time `json:"coverageStart"`
	LastSample          time.Time `json:"lastSample"`
	Gaps                uint64    `json:"gaps"`
}

// Host measurements fail independently; missing devices are unknown, never zero.
type MetricsHost struct {
	MemoryMode          string                  `json:"memoryMode"`
	FastIntervalSeconds int                     `json:"fastIntervalSeconds"`
	DiskIntervalSeconds int                     `json:"diskIntervalSeconds"`
	Swap                Metric[Swap]            `json:"swap"`
	Load                Metric[LoadAverage]     `json:"load"`
	Processes           Metric[int]             `json:"processes"`
	Sockets             Metric[SocketCounts]    `json:"sockets"`
	CPU                 Metric[CPUAsset]        `json:"cpu"`
	Virtualization      Metric[string]          `json:"virtualization"`
	Memory              Metric[MemoryBreakdown] `json:"memory"`
	Addresses           Metric[[]HostAddress]   `json:"addresses"`
	PublicIPv4          Metric[string]          `json:"publicIpv4"`
	PublicIPv6          Metric[string]          `json:"publicIpv6"`
	GPUs                Metric[[]GPUDevice]     `json:"gpus"`
	Traffic             Metric[Traffic]         `json:"traffic"`
}

func validateHost(h *MetricsHost, stale bool) error {
	if h == nil {
		return nil
	}
	if h.FastIntervalSeconds < 1 || h.FastIntervalSeconds > 60 || h.DiskIntervalSeconds < 5 || h.DiskIntervalSeconds > 300 {
		return errors.New("invalid host intervals")
	}
	checks := []error{validateMetric("host.swap", h.Swap, stale), validateMetric("host.load", h.Load, stale), validateMetric("host.processes", h.Processes, stale), validateMetric("host.sockets", h.Sockets, stale), validateMetric("host.cpu", h.CPU, stale), validateMetric("host.virtualization", h.Virtualization, stale), validateMetric("host.memory", h.Memory, stale), validateMetric("host.addresses", h.Addresses, stale), validateMetric("host.publicIpv4", h.PublicIPv4, stale), validateMetric("host.publicIpv6", h.PublicIPv6, stale), validateMetric("host.gpus", h.GPUs, stale), validateMetric("host.traffic", h.Traffic, stale)}
	for _, err := range checks {
		if err != nil {
			return err
		}
	}
	if v := h.Swap.Value; v != nil && (v.UsedBytes > v.TotalBytes || !percent(v.UsedPercent)) {
		return errors.New("invalid swap")
	}
	if v := h.Load.Value; v != nil {
		for _, n := range []float64{v.One, v.Five, v.Fifteen} {
			if !finiteNonnegative(n) {
				return errors.New("invalid load")
			}
		}
	}
	if v := h.Processes.Value; v != nil && *v < 0 {
		return errors.New("invalid process count")
	}
	if v := h.Sockets.Value; v != nil && (v.TCP < 0 || v.UDP < 0) {
		return errors.New("invalid socket counts")
	}
	if v := h.CPU.Value; v != nil && (len(v.Model) > 256 || v.PhysicalCores < 0) {
		return errors.New("invalid CPU asset")
	}
	if v := h.Virtualization.Value; v != nil && len(*v) > 256 {
		return errors.New("invalid virtualization")
	}
	for i, ip := range []Metric[string]{h.PublicIPv4, h.PublicIPv6} {
		if ip.Value != nil && (net.ParseIP(*ip.Value) == nil || (net.ParseIP(*ip.Value).To4() != nil) != (i == 0)) {
			return errors.New("invalid public address")
		}
	}
	if v := h.Addresses.Value; v != nil {
		if len(*v) > 256 {
			return errors.New("too many addresses")
		}
		for _, a := range *v {
			ip := net.ParseIP(a.Address)
			if len(a.Interface) > 128 || ip == nil || a.Family != 4 && a.Family != 6 || (ip.To4() != nil) != (a.Family == 4) {
				return errors.New("invalid interface address")
			}
		}
	}
	if v := h.GPUs.Value; v != nil {
		if len(*v) > 32 {
			return errors.New("too many GPUs")
		}
		for _, g := range *v {
			if len(g.ID) > 128 || len(g.Name) > 256 || g.Utilization != nil && !percent(*g.Utilization) || g.TemperatureCelsius != nil && (!finiteNonnegative(*g.TemperatureCelsius) || *g.TemperatureCelsius > 250) || g.MemoryTotalBytes != nil && g.MemoryUsedBytes != nil && *g.MemoryUsedBytes > *g.MemoryTotalBytes {
				return errors.New("invalid GPU")
			}
		}
	}
	if v := h.Traffic.Value; v != nil && (v.PeriodStart.IsZero() || v.CoverageStart.IsZero() || v.LastSample.IsZero() || v.PeriodReceivedBytes > v.ReceivedBytes || v.PeriodSentBytes > v.SentBytes) {
		return errors.New("invalid traffic counters")
	}
	return nil
}
func finiteNonnegative(n float64) bool { return !math.IsNaN(n) && !math.IsInf(n, 0) && n >= 0 }

type CommandSpec struct {
	Script         string `json:"script"`
	TimeoutSeconds int    `json:"timeoutSeconds"`
	Confirm        bool   `json:"confirm"`
}

func ValidateCommand(c CommandSpec) error {
	if !c.Confirm || strings.TrimSpace(c.Script) == "" || len(c.Script) > MaxCommandScriptBytes || strings.ContainsRune(c.Script, 0) || c.TimeoutSeconds < MinCommandTimeoutSeconds || c.TimeoutSeconds > MaxCommandTimeoutSeconds {
		return errors.New("command requires confirmation, a script up to 32 KiB and timeout 1..3600 seconds")
	}
	return nil
}

type CommandJob struct {
	Archived    bool       `json:"archived,omitempty"`
	ID          string     `json:"id"`
	Status      string     `json:"status"`
	StartedAt   time.Time  `json:"startedAt"`
	FinishedAt  *time.Time `json:"finishedAt,omitempty"`
	ExitCode    *int       `json:"exitCode,omitempty"`
	Output      string     `json:"output"`
	Truncated   bool       `json:"truncated"`
	Error       string     `json:"error,omitempty"`
	RequestHash string     `json:"requestHash"`
}
type HostRequest struct {
	Action         string         `json:"action"`
	Settings       *AgentSettings `json:"settings,omitempty"`
	Command        *CommandSpec   `json:"command,omitempty"`
	JobID          string         `json:"jobId,omitempty"`
	IdempotencyKey string         `json:"idempotencyKey,omitempty"`
}
type CommandCleanup struct {
	Archived int `json:"archived"`
	Expired  int `json:"expired"`
	Retained int `json:"retained"`
}
type HostResponse struct {
	Cleanup     *CommandCleanup `json:"cleanup,omitempty"`
	Status      int             `json:"status"`
	Error       string          `json:"error,omitempty"`
	Settings    *AgentSettings  `json:"settings,omitempty"`
	Job         *CommandJob     `json:"job,omitempty"`
	Jobs        []CommandJob    `json:"jobs,omitempty"`
	Diagnostics map[string]any  `json:"diagnostics,omitempty"`
}

// HostMetricKeys is shared by history, alert validation and presentation.
var HostMetricKeys = map[string]string{
	"swap.used_percent": "Swap usage (%)", "swap.used_bytes": "Swap used (bytes)",
	"load.one": "Load (1 min)", "load.five": "Load (5 min)", "load.fifteen": "Load (15 min)",
	"processes.count": "Processes", "sockets.tcp": "TCP sockets", "sockets.udp": "UDP sockets",
	"traffic.received_bytes": "Received bytes", "traffic.sent_bytes": "Sent bytes",
	"traffic.period_received_bytes": "Billing period received bytes", "traffic.period_sent_bytes": "Billing period sent bytes",
	"gpu.utilization_percent": "GPU average usage (%)", "gpu.memory_used_bytes": "GPU memory used (bytes)", "gpu.temperature_celsius": "GPU max temperature (C)",
	"memory.cached_bytes": "Cached memory (bytes)", "memory.buffered_bytes": "Buffered memory (bytes)",
}

func HostNumericMetrics(h *MetricsHost) map[string]Metric[float64] {
	out := map[string]Metric[float64]{}
	if h == nil {
		return out
	}
	add := func(key string, status MetricStatus, at time.Time, age int64, n float64) {
		out[key] = Metric[float64]{Status: status, Value: &n, SampledAt: at, SampleAgeMillis: age}
	}
	if v := h.Swap.Value; v != nil {
		add("swap.used_percent", h.Swap.Status, h.Swap.SampledAt, h.Swap.SampleAgeMillis, v.UsedPercent)
		add("swap.used_bytes", h.Swap.Status, h.Swap.SampledAt, h.Swap.SampleAgeMillis, float64(v.UsedBytes))
	}
	if v := h.Load.Value; v != nil {
		for k, n := range map[string]float64{"load.one": v.One, "load.five": v.Five, "load.fifteen": v.Fifteen} {
			add(k, h.Load.Status, h.Load.SampledAt, h.Load.SampleAgeMillis, n)
		}
	}
	if v := h.Processes.Value; v != nil {
		add("processes.count", h.Processes.Status, h.Processes.SampledAt, h.Processes.SampleAgeMillis, float64(*v))
	}
	if v := h.Sockets.Value; v != nil {
		add("sockets.tcp", h.Sockets.Status, h.Sockets.SampledAt, h.Sockets.SampleAgeMillis, float64(v.TCP))
		add("sockets.udp", h.Sockets.Status, h.Sockets.SampledAt, h.Sockets.SampleAgeMillis, float64(v.UDP))
	}
	if v := h.Memory.Value; v != nil {
		add("memory.cached_bytes", h.Memory.Status, h.Memory.SampledAt, h.Memory.SampleAgeMillis, float64(v.CachedBytes))
		add("memory.buffered_bytes", h.Memory.Status, h.Memory.SampledAt, h.Memory.SampleAgeMillis, float64(v.BufferedBytes))
	}
	if v := h.Traffic.Value; v != nil {
		for k, n := range map[string]uint64{"traffic.received_bytes": v.ReceivedBytes, "traffic.sent_bytes": v.SentBytes, "traffic.period_received_bytes": v.PeriodReceivedBytes, "traffic.period_sent_bytes": v.PeriodSentBytes} {
			add(k, h.Traffic.Status, h.Traffic.SampledAt, h.Traffic.SampleAgeMillis, float64(n))
		}
	}
	if v := h.GPUs.Value; v != nil {
		usage, memory, temp := 0.0, 0.0, 0.0
		u, m, t := 0, 0, 0
		for _, g := range *v {
			if g.Utilization != nil {
				usage += *g.Utilization
				u++
			}
			if g.MemoryUsedBytes != nil {
				memory += float64(*g.MemoryUsedBytes)
				m++
			}
			if g.TemperatureCelsius != nil {
				temp = math.Max(temp, *g.TemperatureCelsius)
				t++
			}
		}
		if u > 0 {
			add("gpu.utilization_percent", h.GPUs.Status, h.GPUs.SampledAt, h.GPUs.SampleAgeMillis, usage/float64(u))
		}
		if m > 0 {
			add("gpu.memory_used_bytes", h.GPUs.Status, h.GPUs.SampledAt, h.GPUs.SampleAgeMillis, memory)
		}
		if t > 0 {
			add("gpu.temperature_celsius", h.GPUs.Status, h.GPUs.SampledAt, h.GPUs.SampleAgeMillis, temp)
		}
	}
	return out
}

type BatchCommand struct {
	NodeIDs        []string `json:"nodeIds"`
	Script         string   `json:"script"`
	TimeoutSeconds int      `json:"timeoutSeconds"`
	Confirm        bool     `json:"confirm"`
}
type BatchProbe struct {
	NodeIDs            []string `json:"nodeIds"`
	Name               string   `json:"name"`
	Kind               string   `json:"kind"`
	Target             string   `json:"target"`
	ExpectedHTTPStatus *int     `json:"expectedHttpStatus,omitempty"`
	IntervalSeconds    int      `json:"intervalSeconds"`
	TimeoutSeconds     int      `json:"timeoutSeconds"`
	Enabled            *bool    `json:"enabled,omitempty"`
}
type NodeAsset struct {
	Price                 float64    `json:"price"`
	Currency              string     `json:"currency"`
	BillingCycleUnit      string     `json:"billingCycleUnit,omitempty"`
	BillingCycleDays      int        `json:"billingCycleDays"`
	PurchasedAt           *time.Time `json:"purchasedAt,omitempty"`
	ExpiresAt             *time.Time `json:"expiresAt,omitempty"`
	ExpiryNoticeDays      int        `json:"expiryNoticeDays"`
	TrafficLimitBytes     uint64     `json:"trafficLimitBytes"`
	TrafficAccounting     string     `json:"trafficAccounting"`
	TrafficWarningPercent float64    `json:"trafficWarningPercent"`
}

func DefaultNodeAsset() NodeAsset {
	return NodeAsset{Currency: "USD", BillingCycleDays: 30, ExpiryNoticeDays: 7, TrafficAccounting: "sum", TrafficWarningPercent: 90}
}

func ValidateNodeAsset(a NodeAsset) error {
	if !finiteNonnegative(a.Price) || len(a.Currency) < 1 || len(a.Currency) > 8 || strings.TrimSpace(a.Currency) != a.Currency || strings.ContainsAny(a.Currency, "\x00\r\n") || a.BillingCycleDays < 1 || a.BillingCycleDays > 3660 || a.ExpiryNoticeDays < 0 || a.ExpiryNoticeDays > 365 || !percent(a.TrafficWarningPercent) {
		return errors.New("invalid node asset settings")
	}
	switch a.BillingCycleUnit {
	case "", "day", "year", "month", "hour", "minute":
	default:
		return errors.New("invalid billing cycle unit")
	}
	switch a.TrafficAccounting {
	case "sum", "max", "min", "up", "down":
	default:
		return errors.New("invalid traffic accounting")
	}
	return nil
}

func CommandJobID(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])
}
