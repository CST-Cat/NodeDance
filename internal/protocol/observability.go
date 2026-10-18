package protocol

import (
	"errors"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// Diagnostic definitions are the shared source for collection, alerts, history
// and all three presentations. Entity identity is never a time-series label.
type MetricDefinition struct {
	Name        string `json:"name"`
	NameZH      string `json:"nameZh"`
	Unit        string `json:"unit"`
	Source      string `json:"source"`
	Scope       string `json:"scope"`
	Interval    string `json:"interval"`
	Denominator string `json:"denominator"`
}
type DiagnosticEntity struct {
	Units  map[string]string          `json:"units,omitempty"`
	Kind   string                     `json:"kind"`
	ID     string                     `json:"id"`
	Name   string                     `json:"name"`
	Values map[string]Metric[float64] `json:"values"`
}
type Observability struct {
	Values              map[string]Metric[float64] `json:"values"`
	Entities            []DiagnosticEntity         `json:"entities"`
	Truncated           bool                       `json:"truncated"`
	SlowIntervalSeconds int                        `json:"slowIntervalSeconds"`
}
type ApplicationCollector struct {
	MetricUnits map[string]string `json:"metricUnits"`
	Name        string            `json:"name"`
	Kind        string            `json:"kind"` // nginx or prometheus, including database exporters
	URL         string            `json:"url"`
	Metrics     []string          `json:"metrics"`
}
type ObservationSettings struct {
	IntervalSeconds int                    `json:"intervalSeconds"`
	ProcessLimit    int                    `json:"processLimit"`
	ContainerLimit  int                    `json:"containerLimit"`
	Services        []string               `json:"services"`
	Applications    []ApplicationCollector `json:"applications"`
}

func ObservationDefaults() ObservationSettings {
	return ObservationSettings{IntervalSeconds: 60, ProcessLimit: 10, ContainerLimit: 16, Services: []string{}, Applications: []ApplicationCollector{}}
}

var applicationMetricName = regexp.MustCompile(`^[a-zA-Z_:][a-zA-Z0-9_:]*$`)

func ValidateObservationSettings(s *ObservationSettings) error {
	if s == nil {
		return nil
	}
	if s.IntervalSeconds < 15 || s.IntervalSeconds > 300 || s.ProcessLimit < 0 || s.ProcessLimit > 20 || s.ContainerLimit < 0 || s.ContainerLimit > 32 || len(s.Services) > 16 || len(s.Applications) > 8 {
		return errors.New("invalid observability bounds")
	}
	for _, name := range s.Services {
		if len(name) == 0 || len(name) > 128 || strings.ContainsAny(name, "/\\\x00\r\n ") || strings.HasPrefix(name, "-") {
			return errors.New("invalid service name")
		}
	}
	seen := map[string]bool{}
	for _, a := range s.Applications {
		u, e := url.Parse(a.URL)
		if e != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || len(a.URL) > 2048 || len(a.Name) == 0 || len(a.Name) > 80 || strings.ContainsAny(a.Name, "\x00\r\n") || seen[a.Name] || (a.Kind != "nginx" && a.Kind != "prometheus") || len(a.Metrics) > 32 {
			return errors.New("invalid application collector")
		}
		seen[a.Name] = true
		if len(a.MetricUnits) > 32 {
			return errors.New("too many metric units")
		}
		metricNames := map[string]bool{}
		for _, key := range a.Metrics {
			if !applicationMetricName.MatchString(key) || metricNames[key] {
				return errors.New("invalid or duplicate application metric")
			}
			metricNames[key] = true
			if a.MetricUnits[key] == "" || len(a.MetricUnits[key]) > 32 {
				return errors.New("each application metric requires a unit")
			}
			if len(key) == 0 || len(key) > 128 || strings.ContainsAny(key, "{} \r\n") {
				return errors.New("invalid application metric name")
			}
		}
	}
	return nil
}

var DiagnosticDefinitions = map[string]MetricDefinition{}
var EntityDefinitions = map[string]map[string]MetricDefinition{}

func init() {
	DiagnosticDefinitions["cpu.user_percent"] = MetricDefinition{"User CPU", "用户态 CPU", "%", "/proc/stat", "host", "fast", "total CPU ticks"}
	DiagnosticDefinitions["cpu.system_percent"] = MetricDefinition{"System CPU", "内核态 CPU", "%", "/proc/stat", "host", "fast", "total CPU ticks"}
	DiagnosticDefinitions["cpu.steal_percent"] = MetricDefinition{"CPU steal", "CPU 调度等待", "%", "/proc/stat", "host", "fast", "total CPU ticks"}
	DiagnosticDefinitions["cpu.iowait_percent"] = MetricDefinition{"I/O wait", "I/O 等待", "%", "/proc/stat", "host", "fast", "total CPU ticks; kernel accounting limitations apply"}
	DiagnosticDefinitions["cpu.softirq_percent"] = MetricDefinition{"Soft IRQ", "软中断", "%", "/proc/stat", "host", "fast", "total CPU ticks"}
	DiagnosticDefinitions["cpu.running"] = MetricDefinition{"Runnable threads", "可运行线程", "count", "/proc/stat", "host", "fast", "none"}
	DiagnosticDefinitions["cpu.blocked"] = MetricDefinition{"Blocked tasks", "阻塞任务", "count", "/proc/stat", "host", "fast", "none"}
	DiagnosticDefinitions["cpu.context_switches_per_second"] = MetricDefinition{"Context switches", "上下文切换", "1/s", "/proc/stat", "host", "fast", "elapsed monotonic seconds"}
	DiagnosticDefinitions["memory.available_bytes"] = MetricDefinition{"Available memory", "可用内存", "bytes", "/proc/meminfo", "host", "fast", "none"}
	DiagnosticDefinitions["memory.dirty_bytes"] = MetricDefinition{"Dirty memory", "待写回内存", "bytes", "/proc/meminfo", "host", "fast", "none"}
	DiagnosticDefinitions["memory.slab_bytes"] = MetricDefinition{"Slab memory", "内核 Slab", "bytes", "/proc/meminfo", "host", "fast", "none"}
	DiagnosticDefinitions["memory.major_faults_per_second"] = MetricDefinition{"Major page faults", "主缺页", "1/s", "/proc/vmstat", "host", "fast", "elapsed monotonic seconds"}
	DiagnosticDefinitions["memory.oom_kills_per_second"] = MetricDefinition{"OOM kills", "OOM 终止", "1/s", "/proc/vmstat", "host", "fast", "elapsed monotonic seconds"}
	DiagnosticDefinitions["swap.in_bytes_per_second"] = MetricDefinition{"Swap in", "Swap 换入", "bytes/s", "/proc/vmstat", "host", "fast", "elapsed monotonic seconds"}
	DiagnosticDefinitions["swap.out_bytes_per_second"] = MetricDefinition{"Swap out", "Swap 换出", "bytes/s", "/proc/vmstat", "host", "fast", "elapsed monotonic seconds"}
	DiagnosticDefinitions["disk.read_iops"] = MetricDefinition{"Read IOPS", "读取 IOPS", "1/s", "/proc/diskstats", "host", "fast", "elapsed monotonic seconds"}
	DiagnosticDefinitions["disk.write_iops"] = MetricDefinition{"Write IOPS", "写入 IOPS", "1/s", "/proc/diskstats", "host", "fast", "elapsed monotonic seconds"}
	DiagnosticDefinitions["disk.read_latency_ms"] = MetricDefinition{"Mean read latency", "平均读取延迟", "ms", "/proc/diskstats", "host", "fast", "completed reads in interval"}
	DiagnosticDefinitions["disk.write_latency_ms"] = MetricDefinition{"Mean write latency", "平均写入延迟", "ms", "/proc/diskstats", "host", "fast", "completed writes in interval"}
	DiagnosticDefinitions["disk.queue"] = MetricDefinition{"Mean outstanding I/O", "平均在途 I/O", "count", "/proc/diskstats", "host", "fast", "elapsed milliseconds"}
	DiagnosticDefinitions["filesystem.inode_used_percent"] = MetricDefinition{"Maximum inode usage", "最高 inode 使用率", "%", "statfs", "host", "slow", "filesystem inode total"}
	DiagnosticDefinitions["network.tcp_retransmits_per_second"] = MetricDefinition{"TCP retransmissions", "TCP 重传", "1/s", "/proc/net/snmp", "host", "fast", "elapsed monotonic seconds"}
	DiagnosticDefinitions["network.tcp_failures_per_second"] = MetricDefinition{"TCP connection failures", "TCP 建连失败", "1/s", "/proc/net/snmp", "host", "fast", "elapsed monotonic seconds"}
	DiagnosticDefinitions["network.listen_overflows_per_second"] = MetricDefinition{"Listen queue overflow", "监听队列溢出", "1/s", "/proc/net/netstat", "host", "fast", "elapsed monotonic seconds"}
	DiagnosticDefinitions["network.conntrack_count"] = MetricDefinition{"Tracked connections", "连接跟踪已用", "count", "/proc/sys/net/netfilter", "host", "fast", "none"}
	DiagnosticDefinitions["network.conntrack_limit"] = MetricDefinition{"Conntrack limit", "连接跟踪上限", "count", "/proc/sys/net/netfilter", "host", "fast", "none"}
	DiagnosticDefinitions["network.conntrack_percent"] = MetricDefinition{"Conntrack usage", "连接跟踪使用率", "%", "/proc/sys/net/netfilter", "host", "fast", "nf_conntrack_max"}
	DiagnosticDefinitions["service.failed_count"] = MetricDefinition{"Failed systemd services", "失败服务", "count", "systemd D-Bus via systemctl", "host", "slow", "none"}
	DiagnosticDefinitions["agent.rss_bytes"] = MetricDefinition{"Agent RSS", "Agent 常驻内存", "bytes", "/proc/self/statm", "host", "fast", "none"}
	DiagnosticDefinitions["agent.cpu_percent"] = MetricDefinition{"Agent CPU", "Agent CPU", "%", "/proc/self/stat", "host", "fast", "one CPU elapsed seconds; may exceed 100"}
	DiagnosticDefinitions["agent.collection_ms"] = MetricDefinition{"Fast collection duration", "快速采集耗时", "ms", "monotonic clock", "host", "fast", "none"}
	DiagnosticDefinitions["agent.slow_collection_ms"] = MetricDefinition{"Detailed collection duration", "明细采集耗时", "ms", "monotonic clock", "host", "slow", "none"}
	DiagnosticDefinitions["pressure.cpu.some10"] = MetricDefinition{"cpu pressure some 10s", "CPU 部分阻塞 10秒", "%", "/proc/pressure/cpu", "host", "fast", "10s rolling wall-time window"}
	DiagnosticDefinitions["pressure.cpu.some60"] = MetricDefinition{"cpu pressure some 60s", "CPU 部分阻塞 60秒", "%", "/proc/pressure/cpu", "host", "fast", "60s rolling wall-time window"}
	DiagnosticDefinitions["pressure.cpu.some300"] = MetricDefinition{"cpu pressure some 300s", "CPU 部分阻塞 300秒", "%", "/proc/pressure/cpu", "host", "fast", "300s rolling wall-time window"}
	DiagnosticDefinitions["pressure.memory.some10"] = MetricDefinition{"memory pressure some 10s", "内存 部分阻塞 10秒", "%", "/proc/pressure/memory", "host", "fast", "10s rolling wall-time window"}
	DiagnosticDefinitions["pressure.memory.some60"] = MetricDefinition{"memory pressure some 60s", "内存 部分阻塞 60秒", "%", "/proc/pressure/memory", "host", "fast", "60s rolling wall-time window"}
	DiagnosticDefinitions["pressure.memory.some300"] = MetricDefinition{"memory pressure some 300s", "内存 部分阻塞 300秒", "%", "/proc/pressure/memory", "host", "fast", "300s rolling wall-time window"}
	DiagnosticDefinitions["pressure.memory.full10"] = MetricDefinition{"memory pressure full 10s", "内存 全部阻塞 10秒", "%", "/proc/pressure/memory", "host", "fast", "10s rolling wall-time window"}
	DiagnosticDefinitions["pressure.memory.full60"] = MetricDefinition{"memory pressure full 60s", "内存 全部阻塞 60秒", "%", "/proc/pressure/memory", "host", "fast", "60s rolling wall-time window"}
	DiagnosticDefinitions["pressure.memory.full300"] = MetricDefinition{"memory pressure full 300s", "内存 全部阻塞 300秒", "%", "/proc/pressure/memory", "host", "fast", "300s rolling wall-time window"}
	DiagnosticDefinitions["pressure.io.some10"] = MetricDefinition{"io pressure some 10s", "I/O 部分阻塞 10秒", "%", "/proc/pressure/io", "host", "fast", "10s rolling wall-time window"}
	DiagnosticDefinitions["pressure.io.some60"] = MetricDefinition{"io pressure some 60s", "I/O 部分阻塞 60秒", "%", "/proc/pressure/io", "host", "fast", "60s rolling wall-time window"}
	DiagnosticDefinitions["pressure.io.some300"] = MetricDefinition{"io pressure some 300s", "I/O 部分阻塞 300秒", "%", "/proc/pressure/io", "host", "fast", "300s rolling wall-time window"}
	DiagnosticDefinitions["pressure.io.full10"] = MetricDefinition{"io pressure full 10s", "I/O 全部阻塞 10秒", "%", "/proc/pressure/io", "host", "fast", "10s rolling wall-time window"}
	DiagnosticDefinitions["pressure.io.full60"] = MetricDefinition{"io pressure full 60s", "I/O 全部阻塞 60秒", "%", "/proc/pressure/io", "host", "fast", "60s rolling wall-time window"}
	DiagnosticDefinitions["pressure.io.full300"] = MetricDefinition{"io pressure full 300s", "I/O 全部阻塞 300秒", "%", "/proc/pressure/io", "host", "fast", "300s rolling wall-time window"}
	DiagnosticDefinitions["agent.report_interval_seconds"] = MetricDefinition{"Report interval", "上报间隔", "s", "Core receive monotonic clock", "agent", "fast", "none"}
	DiagnosticDefinitions["agent.sample_age_ms"] = MetricDefinition{"Sample serialization age", "采样至序列化耗时", "ms", "Agent monotonic sample age", "agent", "fast", "none"}
	DiagnosticDefinitions["agent.unobserved_intervals"] = MetricDefinition{"Unobserved sampling intervals", "未观测采样间隔", "count", "Core receive interval / configured interval", "agent", "fast", "fast interval seconds; estimate, not packet loss"}
	for key, definition := range map[string]MetricDefinition{
		"process.collected_count":      {"Ranked processes", "排行进程数量", "count", "/proc/PID", "host", "slow", "none; bounded ranking"},
		"container.collected_count":    {"Observed containers", "观测容器数量", "count", "cgroup v2", "host", "slow", "none; bounded walk"},
		"application.collectors_count": {"Configured collectors", "已配置应用采集", "count", "Agent configuration", "host", "slow", "none"},
	} {
		DiagnosticDefinitions[key] = definition
	}
	definitions := map[string]string{"usage_percent": "%", "cpu_percent": "%", "rss_bytes": "bytes", "threads": "count", "file_descriptors": "count", "read_bytes_per_second": "bytes/s", "write_bytes_per_second": "bytes/s", "cpu_cores": "cores", "cpu_limit_cores": "cores", "cpu_quota_percent": "%", "cpu_throttled_seconds_per_second": "s/s", "cpu_periods_per_second": "1/s", "cpu_throttled_periods_per_second": "1/s", "cpu_throttled_period_percent": "%", "memory_bytes": "bytes", "memory_limit_bytes": "bytes", "pids": "count", "pids_limit": "count", "oom_kills": "count", "oom_kills_per_second": "1/s", "total_bytes": "bytes", "available_bytes": "bytes", "used_bytes": "bytes", "used_percent": "%", "inode_used_percent": "%", "active": "boolean", "restarts": "count", "exit_status": "code", "collector_up": "boolean", "connections": "count", "Reading": "count", "Writing": "count", "Waiting": "count", "accepts_total": "count", "handled_total": "count", "requests_total": "count"}
	labels := map[string][2]string{
		"usage_percent":                    {"Usage", "使用率"},
		"cpu_percent":                      {"CPU", "CPU"},
		"rss_bytes":                        {"Resident memory", "常驻内存"},
		"threads":                          {"Threads", "线程"},
		"file_descriptors":                 {"File descriptors", "文件描述符"},
		"read_bytes_per_second":            {"Read", "读取"},
		"write_bytes_per_second":           {"Write", "写入"},
		"cpu_cores":                        {"CPU cores used", "已用 CPU 核数"},
		"cpu_limit_cores":                  {"CPU quota", "CPU 配额"},
		"cpu_quota_percent":                {"CPU quota usage", "CPU 配额使用率"},
		"cpu_throttled_seconds_per_second": {"Throttled time", "限流时间"},
		"cpu_periods_per_second":           {"CPU periods", "CPU 周期"},
		"cpu_throttled_periods_per_second": {"Throttled periods", "限流周期"},
		"cpu_throttled_period_percent":     {"Throttled periods", "限流周期占比"},
		"memory_bytes":                     {"Memory", "内存"},
		"memory_limit_bytes":               {"Memory limit", "内存上限"},
		"pids":                             {"Processes", "进程"},
		"pids_limit":                       {"Process limit", "进程上限"},
		"oom_kills":                        {"OOM kills", "OOM 终止次数"},
		"oom_kills_per_second":             {"OOM kills", "OOM 终止速率"},
		"total_bytes":                      {"Total space", "总空间"},
		"available_bytes":                  {"Available space", "可用空间"},
		"used_bytes":                       {"Used space", "已用空间"},
		"used_percent":                     {"Space usage", "空间使用率"},
		"inode_used_percent":               {"Inode usage", "inode 使用率"},
		"active":                           {"Active", "运行状态"},
		"restarts":                         {"Restarts", "重启次数"},
		"exit_status":                      {"Exit status", "退出状态"},
		"collector_up":                     {"Collector available", "采集可用"},
		"connections":                      {"Connections", "连接数"},
		"Reading":                          {"Reading", "读取连接"},
		"Writing":                          {"Writing", "写入连接"},
		"Waiting":                          {"Waiting", "等待连接"},
		"accepts_total":                    {"Accepted total", "累计接入"},
		"handled_total":                    {"Handled total", "累计处理"},
		"requests_total":                   {"Requests total", "累计请求"},
	}
	for kind, source := range map[string]string{"cpu": "/proc/stat", "process": "/proc/PID", "container": "cgroup v2", "filesystem": "statfs", "service": "systemd", "application": "configured application endpoint"} {
		EntityDefinitions[kind] = map[string]MetricDefinition{}
		for key, unit := range definitions {
			den := "none"
			if strings.Contains(key, "per_second") {
				den = "elapsed monotonic seconds"
			}
			if key == "cpu_percent" {
				den = "one CPU elapsed time; may exceed 100%"
			}
			EntityDefinitions[kind][key] = MetricDefinition{labels[key][0], labels[key][1], unit, source, kind, "slow", den}
		}
	}
	for key, d := range DiagnosticDefinitions {
		if strings.HasPrefix(key, "pressure.") {
			entity := d
			entity.Source = "cgroup v2 pressure"
			entity.Scope = "container"
			entity.Interval = "slow"
			EntityDefinitions["container"][key] = entity
		}
		HostMetricKeys[key] = d.Name + " (" + d.Unit + ")"
	}
}
func ValidateObservability(o *Observability, stale bool) error {
	if o == nil {
		return nil
	}
	if len(o.Values) > 128 || len(o.Entities) > 128 || o.SlowIntervalSeconds < 15 || o.SlowIntervalSeconds > 300 {
		return errors.New("invalid observability bounds")
	}
	check := func(values map[string]Metric[float64]) error {
		if len(values) > 128 {
			return errors.New("too many readings")
		}
		for k, m := range values {
			if len(k) > 128 || strings.ContainsAny(k, "\x00\r\n") {
				return errors.New("invalid reading key")
			}
			if err := validateMetric(k, m, stale); err != nil {
				return err
			}
			if m.Value != nil && !finiteNonnegative(*m.Value) {
				return errors.New("invalid diagnostic reading")
			}
		}
		return nil
	}
	if e := check(o.Values); e != nil {
		return e
	}
	for k := range o.Values {
		if _, ok := DiagnosticDefinitions[k]; !ok {
			return errors.New("unknown host diagnostic")
		}
	}
	ids := map[string]bool{}
	for _, e := range o.Entities {
		id := e.Kind + ":" + e.ID
		if ids[id] || len(e.ID) == 0 || len(e.ID) > 512 || len(e.Name) > 256 || len(e.Kind) > 32 {
			return errors.New("invalid diagnostic entity")
		}
		ids[id] = true
		if len(e.Units) > 128 {
			return errors.New("too many entity units")
		}
		for k, u := range e.Units {
			if len(k) > 128 || len(u) > 32 || strings.ContainsAny(u, "\x00\r\n") {
				return errors.New("invalid entity unit")
			}
		}
		if err := check(e.Values); err != nil {
			return err
		}
	}
	return nil
}
func CloneObservability(o *Observability) *Observability {
	if o == nil {
		return nil
	}
	out := *o
	clone := func(v map[string]Metric[float64]) map[string]Metric[float64] {
		m := map[string]Metric[float64]{}
		for k, v := range v {
			if v.Value != nil {
				n := *v.Value
				v.Value = &n
			}
			m[k] = v
		}
		return m
	}
	out.Values = clone(o.Values)
	out.Entities = append([]DiagnosticEntity{}, o.Entities...)
	for i := range out.Entities {
		out.Entities[i].Values = clone(out.Entities[i].Values)
		out.Entities[i].Units = map[string]string{}
		for k, v := range o.Entities[i].Units {
			out.Entities[i].Units[k] = v
		}
	}
	return &out
}
func VisitObservability(o *Observability, f func(*Metric[float64], time.Duration)) {
	if o == nil {
		return
	}
	for k, v := range o.Values {
		ttl := 15 * time.Second
		if d, ok := DiagnosticDefinitions[k]; ok && d.Interval == "slow" {
			ttl = time.Duration(o.SlowIntervalSeconds*3) * time.Second
		}
		f(&v, ttl)
		o.Values[k] = v
	}
	for i := range o.Entities {
		for k, v := range o.Entities[i].Values {
			ttl := time.Duration(o.SlowIntervalSeconds*3) * time.Second
			if o.Entities[i].Kind == "cpu" {
				ttl = 15 * time.Second
			}
			f(&v, ttl)
			o.Entities[i].Values[k] = v
		}
	}
}

// CatalogDefinitions includes established history keys without making them
// duplicate Agent diagnostic samples. Mount keys are derived from node mounts.
func CatalogDefinitions() map[string]MetricDefinition {
	out := map[string]MetricDefinition{}
	for k, v := range DiagnosticDefinitions {
		out[k] = v
	}
	for key, d := range map[string]MetricDefinition{
		"cpu.usage_percent":                 {"CPU usage", "CPU 使用率", "%", "/proc/stat", "host", "fast", "total CPU ticks"},
		"memory.used_percent":               {"Memory usage", "内存使用率", "%", "meminfo; configured memory mode", "host", "fast", "total physical memory"},
		"memory.used_bytes":                 {"Memory used", "已用内存", "bytes", "meminfo; configured memory mode", "host", "fast", "none"},
		"network.received_bytes_per_second": {"Network receive", "网络接收", "bytes/s", "selected interface counters", "host", "fast", "elapsed monotonic seconds"},
		"network.sent_bytes_per_second":     {"Network send", "网络发送", "bytes/s", "selected interface counters", "host", "fast", "elapsed monotonic seconds"},
		"swap.used_percent":                 {"Swap usage", "Swap 使用率", "%", "meminfo", "host", "fast", "swap total"},
		"disk.read_bytes_per_second":        {"Disk read", "磁盘读取", "bytes/s", "/proc/diskstats", "host", "fast", "elapsed monotonic seconds"},
		"disk.write_bytes_per_second":       {"Disk write", "磁盘写入", "bytes/s", "/proc/diskstats", "host", "fast", "elapsed monotonic seconds"},
	} {
		out[key] = d
	}
	return out
}
