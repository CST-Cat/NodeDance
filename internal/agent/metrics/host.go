package metrics

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/CST-Cat/NodeDance/internal/agent/localstate"
	"github.com/CST-Cat/NodeDance/internal/agent/networkpolicy"
	"github.com/CST-Cat/NodeDance/internal/protocol"
	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/disk"
	"github.com/shirou/gopsutil/v4/host"
	"github.com/shirou/gopsutil/v4/load"
	"github.com/shirou/gopsutil/v4/mem"
)

type hostCollector struct {
	diskIO      diskIOBaseline
	source      collectorSource
	settings    func() protocol.AgentSettings
	path        string
	traffic     trafficState
	initialized bool
	slowMu      sync.Mutex
	slowRunning bool
	lastGPU     time.Time
	ipEndpoint  string
	gpu         protocol.Metric[[]protocol.GPUDevice]
	lastIP      time.Time
	ipv4        protocol.Metric[string]
	ipv6        protocol.Metric[string]
}

func (c *Collector) ConfigureHost(stateDir string, settings func() protocol.AgentSettings) {
	c.host = &hostCollector{source: c.source, path: filepath.Join(stateDir, "traffic.json"), settings: settings}
	c.diskPoller.source = configuredDiskSource{collectorSource: c.source, settings: settings}
}
func hostKnown[T any](v T, at time.Time) protocol.Metric[T] {
	return protocol.Metric[T]{Status: protocol.MetricKnown, Value: &v, SampledAt: at}
}
func hostUnknown[T any](reason string, at time.Time) protocol.Metric[T] {
	return protocol.Metric[T]{Status: protocol.MetricUnknown, Reason: reason, SampledAt: at}
}
func (h *hostCollector) collect(ctx context.Context, at time.Time, interfaces []NetworkInterface) *protocol.MetricsHost {
	s := h.settings()
	ioRate := h.collectDiskIO(ctx, at)
	v := &protocol.MetricsHost{DiskIO: &ioRate, MemoryMode: s.MemoryMode, FastIntervalSeconds: s.FastIntervalSeconds, DiskIntervalSeconds: s.DiskIntervalSeconds}
	v.Swap = hostUnknown[protocol.Swap]("read_failed", at)
	if m, e := mem.SwapMemoryWithContext(ctx); e == nil {
		v.Swap = hostKnown(protocol.Swap{TotalBytes: m.Total, UsedBytes: m.Used, UsedPercent: m.UsedPercent}, at)
	}
	v.Load = hostUnknown[protocol.LoadAverage]("read_failed", at)
	if l, e := load.AvgWithContext(ctx); e == nil {
		v.Load = hostKnown(protocol.LoadAverage{One: l.Load1, Five: l.Load5, Fifteen: l.Load15}, at)
	}
	v.Memory = hostUnknown[protocol.MemoryBreakdown]("read_failed", at)
	if m, e := mem.VirtualMemoryWithContext(ctx); e == nil {
		v.Memory = hostKnown(protocol.MemoryBreakdown{RawUsedBytes: m.Used, AvailableBytes: m.Available, FreeBytes: m.Free, CachedBytes: m.Cached, BufferedBytes: m.Buffers, SharedBytes: m.Shared}, at)
	}
	v.CPU = hostUnknown[protocol.CPUAsset]("read_failed", at)
	if info, e := cpu.InfoWithContext(ctx); e == nil && len(info) > 0 {
		cores, e := cpu.CountsWithContext(ctx, false)
		if e == nil {
			v.CPU = hostKnown(protocol.CPUAsset{Model: info[0].ModelName, PhysicalCores: cores}, at)
		}
	}
	v.Virtualization = hostUnknown[string]("read_failed", at)
	if system, role, e := host.VirtualizationWithContext(ctx); e == nil {
		if system == "" {
			system = "none"
		}
		if role != "" {
			system += "/" + role
		}
		v.Virtualization = hostKnown(system, at)
	}
	proc := os.Getenv("HOST_PROC")
	if proc == "" {
		proc = "/proc"
	}
	v.Processes = hostUnknown[int]("read_failed", at)
	if entries, e := os.ReadDir(proc); e == nil {
		n := 0
		for _, entry := range entries {
			if entry.IsDir() {
				if _, e := strconv.Atoi(entry.Name()); e == nil {
					n++
				}
			}
		}
		v.Processes = hostKnown(n, at)
	}
	v.Sockets = hostUnknown[protocol.SocketCounts]("read_failed", at)
	tcp, e1 := countSockets(proc, []string{"tcp", "tcp6"})
	udp, e2 := countSockets(proc, []string{"udp", "udp6"})
	if e1 == nil && e2 == nil {
		v.Sockets = hostKnown(protocol.SocketCounts{TCP: tcp, UDP: udp}, at)
	}
	v.Addresses = hostUnknown[[]protocol.HostAddress]("read_failed", at)
	if nics, e := net.Interfaces(); e == nil {
		addresses := []protocol.HostAddress{}
		for _, nic := range nics {
			if nic.Flags&net.FlagLoopback != 0 {
				continue
			}
			addrs, e := nic.Addrs()
			if e != nil {
				continue
			}
			for _, a := range addrs {
				ip, _, e := net.ParseCIDR(a.String())
				if e != nil || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
					continue
				}
				f := 6
				if ip.To4() != nil {
					f = 4
				}
				if len(addresses) < 256 {
					addresses = append(addresses, protocol.HostAddress{Interface: nic.Name, Address: ip.String(), Family: f})
				}
			}
		}
		v.Addresses = hostKnown(addresses, at)
	}
	h.slowMu.Lock()
	if s.PublicIPURL == "" {
		h.ipv4 = hostUnknown[string]("not_configured", at)
		h.ipv6 = hostUnknown[string]("not_configured", at)
	}
	if !s.EnableGPU {
		h.gpu = hostUnknown[[]protocol.GPUDevice]("disabled", at)
	}
	needIP := s.PublicIPURL != "" && (h.ipEndpoint != s.PublicIPURL || at.Sub(h.lastIP) >= 5*time.Minute)
	needGPU := s.EnableGPU && at.Sub(h.lastGPU) >= 30*time.Second
	if !h.slowRunning && (needIP || needGPU) {
		h.slowRunning = true
		go h.collectSlow(ctx, at, s, needIP, needGPU)
	}
	v.PublicIPv4, v.PublicIPv6, v.GPUs = h.ipv4, h.ipv6, h.gpu
	if v.PublicIPv4.Status == "" {
		v.PublicIPv4 = hostUnknown[string]("sampling", at)
	}
	if v.PublicIPv6.Status == "" {
		v.PublicIPv6 = hostUnknown[string]("sampling", at)
	}
	if v.GPUs.Status == "" {
		v.GPUs = hostUnknown[[]protocol.GPUDevice]("sampling", at)
	}
	h.slowMu.Unlock()
	if s.CustomIPv4 != "" {
		v.PublicIPv4 = hostKnown(s.CustomIPv4, at)
		v.PublicIPv4.Reason = "configured_override"
	}
	if s.CustomIPv6 != "" {
		v.PublicIPv6 = hostKnown(s.CustomIPv6, at)
		v.PublicIPv6.Reason = "configured_override"
	}
	v.Traffic = h.collectTraffic(ctx, at, s, interfaces)
	return v
}
func (h *hostCollector) collectSlow(ctx context.Context, at time.Time, s protocol.AgentSettings, ip, gpu bool) {
	var v4, v6 protocol.Metric[string]
	var g protocol.Metric[[]protocol.GPUDevice]
	if ip {
		v4, v6 = publicAddresses(ctx, s, at)
	}
	if gpu {
		g = hostUnknown[[]protocol.GPUDevice]("device_or_driver_unavailable", at)
		if values, err := collectGPUs(ctx); err == nil && len(values) > 0 {
			g = hostKnown(values, at)
		}
	}
	h.slowMu.Lock()
	defer h.slowMu.Unlock()
	if ip {
		h.ipv4, h.ipv6 = v4, v6
		h.lastIP = at
		h.ipEndpoint = s.PublicIPURL
	}
	if gpu {
		h.gpu = g
		h.lastGPU = at
	}
	h.slowRunning = false
}
func countSockets(root string, names []string) (int, error) {
	count := 0
	for _, name := range names {
		f, e := os.Open(filepath.Join(root, "net", name))
		if errors.Is(e, os.ErrNotExist) && strings.HasSuffix(name, "6") {
			continue
		}
		if e != nil {
			return 0, e
		}
		scan := bufio.NewScanner(f)
		first := true
		for scan.Scan() {
			if first {
				first = false
				continue
			}
			if strings.TrimSpace(scan.Text()) != "" {
				count++
			}
		}
		e = scan.Err()
		f.Close()
		if e != nil {
			return 0, e
		}
	}
	return count, nil
}
func publicAddresses(ctx context.Context, settings protocol.AgentSettings, at time.Time) (protocol.Metric[string], protocol.Metric[string]) {
	values := [2]protocol.Metric[string]{hostUnknown[string]("unavailable", at), hostUnknown[string]("unavailable", at)}
	for index, network := range []string{"tcp4", "tcp6"} {
		func() {
			ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
			defer cancel()
			d := net.Dialer{Resolver: networkpolicy.Resolver(settings)}
			tr := &http.Transport{DialContext: func(ctx context.Context, _ string, a string) (net.Conn, error) { return d.DialContext(ctx, network, a) }}
			defer tr.CloseIdleConnections()
			client := http.Client{Transport: tr, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
			req, e := http.NewRequestWithContext(ctx, "GET", settings.PublicIPURL, nil)
			if e != nil {
				return
			}
			resp, e := client.Do(req)
			if e != nil {
				return
			}
			defer resp.Body.Close()
			if resp.StatusCode != 200 {
				return
			}
			b, e := io.ReadAll(io.LimitReader(resp.Body, 128))
			ip := net.ParseIP(strings.TrimSpace(string(b)))
			if e == nil && ip != nil && (ip.To4() != nil) == (index == 0) {
				values[index] = hostKnown(ip.String(), at)
			}
		}()
	}
	return values[0], values[1]
}
func matches(name string, patterns []string) bool {
	for _, p := range patterns {
		if ok, _ := filepath.Match(p, name); ok {
			return true
		}
	}
	return false
}
func (c *Collector) filterInterface(name string, included bool, reason string) (bool, string) {
	if c.host == nil {
		return included, reason
	}
	s := c.host.settings()
	if matches(name, s.ExcludeInterfaces) {
		return false, "excluded_by_configuration"
	}
	if len(s.IncludeInterfaces) > 0 {
		if matches(name, s.IncludeInterfaces) && name != "lo" {
			return true, "included_by_configuration"
		}
		return false, "outside_include_configuration"
	}
	return included, reason
}

// Traffic accounting persists every accepted sample. Counter identity includes
// boot ID and ifindex; reset/recreated links never create a huge false delta.
type trafficCounter struct {
	RX       uint64 `json:"rx"`
	TX       uint64 `json:"tx"`
	Identity string `json:"identity"`
}
type trafficState struct {
	Value     protocol.Traffic          `json:"value"`
	Counters  map[string]trafficCounter `json:"counters"`
	Selection string                    `json:"selection"`
}

func BillingPeriod(at time.Time, day int) time.Time {
	at = at.UTC()
	start := time.Date(at.Year(), at.Month(), day, 0, 0, 0, 0, time.UTC)
	if at.Before(start) {
		start = start.AddDate(0, -1, 0)
	}
	return start
}
func (h *hostCollector) collectTraffic(ctx context.Context, at time.Time, s protocol.AgentSettings, interfaces []NetworkInterface) protocol.Metric[protocol.Traffic] {
	if ctx.Err() != nil {
		return hostUnknown[protocol.Traffic]("read_failed", at)
	}
	if !h.initialized {
		var loaded trafficState
		err := localstate.Read(h.path, &loaded)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return hostUnknown[protocol.Traffic]("state_unavailable", at)
		}
		h.traffic = loaded
		h.initialized = true
	}
	// Keep the last accepted baseline intact until the replacement is durable.
	previous := h.traffic.Counters
	period := BillingPeriod(at, s.BillingDay)
	v := h.traffic.Value
	selection := fmt.Sprint(s.IncludeInterfaces, s.ExcludeInterfaces)
	if v.CoverageStart.IsZero() {
		v.CoverageStart = at
	}
	periodChanged := !v.PeriodStart.Equal(period)
	selectionChanged := selection != h.traffic.Selection
	if periodChanged || selectionChanged {
		v.PeriodStart = period
		v.PeriodReceivedBytes = 0
		v.PeriodSentBytes = 0
		v.Gaps = 0
		if selectionChanged || v.LastSample.IsZero() {
			v.CoverageStart = at
			previous = nil
		} else {
			v.CoverageStart = period
			v.Gaps++
		}
	}
	if !v.LastSample.IsZero() && at.Sub(v.LastSample) > time.Duration(s.FastIntervalSeconds*3)*time.Second {
		v.Gaps++
	}
	boot, e := h.source.bootID(ctx)
	if e != nil || strings.TrimSpace(boot) == "" {
		return hostUnknown[protocol.Traffic]("boot_id_unavailable", at)
	}
	counters, e := h.source.networkCounters(ctx)
	if e != nil {
		return hostUnknown[protocol.Traffic]("read_failed", at)
	}
	allowed := map[string]bool{}
	for _, i := range interfaces {
		allowed[i.Name] = i.IncludedInSummary
	}
	current := map[string]trafficCounter{}
	for _, n := range counters {
		if !allowed[n.Name] {
			continue
		}
		meta, e := h.source.linkMetadata(n.Name)
		if e != nil {
			v.Gaps++
			continue
		}
		id := boot + "/" + strconv.Itoa(meta.ifindex)
		next := trafficCounter{RX: n.BytesRecv, TX: n.BytesSent, Identity: id}
		if prev, ok := previous[n.Name]; ok {
			if prev.Identity == id && next.RX >= prev.RX && next.TX >= prev.TX {
				rx, tx := next.RX-prev.RX, next.TX-prev.TX
				v.ReceivedBytes += rx
				v.SentBytes += tx
				if !periodChanged {
					v.PeriodReceivedBytes += rx
					v.PeriodSentBytes += tx
				}
			} else {
				v.Gaps++
			}
		}
		current[n.Name] = next
	}
	for name := range previous {
		if _, exists := current[name]; !exists {
			v.Gaps++
		}
	}
	v.LastSample = at
	next := trafficState{Value: v, Counters: current, Selection: selection}
	if ctx.Err() != nil {
		return hostUnknown[protocol.Traffic]("read_failed", at)
	}
	if e := localstate.Write(h.path, next); e != nil {
		return hostUnknown[protocol.Traffic]("state_write_failed", at)
	}
	h.traffic = next
	m := hostKnown(v, at)
	if v.CoverageStart.After(v.PeriodStart) || v.Gaps > 0 {
		m.Reason = "partial_coverage"
	}
	return m
}

// Filter before statfs: excluded network mounts cannot block an included scan.
type configuredDiskSource struct {
	collectorSource
	settings func() protocol.AgentSettings
}

func (s configuredDiskSource) diskPartitions(ctx context.Context) ([]disk.PartitionStat, error) {
	partitions, err := s.collectorSource.diskPartitions(ctx)
	if err != nil {
		return nil, err
	}
	allowed := s.settings().IncludeMountpoints
	filtered := make([]disk.PartitionStat, 0, len(partitions))
	if len(allowed) == 0 {
		for _, partition := range partitions {
			if defaultStorageMount(partition) {
				filtered = append(filtered, partition)
			}
		}
		return filtered, nil
	}
	for _, p := range partitions {
		for _, a := range allowed {
			if a == p.Mountpoint {
				filtered = append(filtered, p)
				break
			}
		}
	}
	return filtered, nil
}
