// Package observe collects bounded diagnostic measurements. Unsupported sources,
// failed reads and uninitialised/reset counters remain distinct unknown states.
package observe

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/CST-Cat/NodeDance/internal/protocol"
	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/disk"
	"github.com/shirou/gopsutil/v4/mem"
	"github.com/shirou/gopsutil/v4/process"
)

type counter struct {
	value float64
	at    time.Time
}
type Collector struct {
	settingsID    string
	cpuSelection  string
	diskSelection string
	slowCounters  *Collector
	slowBoot      string
	previous      map[string]counter
	boot          string
	last          time.Time
	mu            sync.Mutex
	running       bool
	lastSlow      time.Time
	slow          *protocol.Observability
}

func known(v float64, at time.Time) protocol.Metric[float64] {
	return protocol.Metric[float64]{Status: protocol.MetricKnown, Value: &v, SampledAt: at}
}
func unknown(reason string, at time.Time) protocol.Metric[float64] {
	return protocol.Metric[float64]{Status: protocol.MetricUnknown, Reason: reason, SampledAt: at}
}
func root() string {
	if v := os.Getenv("HOST_PROC"); v != "" {
		return v
	}
	return "/proc"
}
func read(path string) (string, error) {
	b, e := os.ReadFile(path)
	return strings.TrimSpace(string(b)), e
}
func fields(path string) (map[string]float64, error) {
	s, e := read(path)
	if e != nil {
		return nil, e
	}
	out := map[string]float64{}
	for _, line := range strings.Split(s, "\n") {
		f := strings.Fields(line)
		if len(f) >= 2 {
			v, e := strconv.ParseFloat(f[1], 64)
			if e == nil {
				out[strings.TrimSuffix(f[0], ":")] = v
			}
		}
	}
	return out, nil
}
func reason(err error) string {
	if os.IsNotExist(err) {
		return "unsupported"
	}
	return "read_failed"
}
func (c *Collector) rate(key string, n float64, at time.Time) protocol.Metric[float64] {
	prev, ok := c.previous[key]
	c.previous[key] = counter{n, at}
	if !ok {
		return unknown("warming_up", at)
	}
	if n < prev.value {
		return unknown("counter_reset", at)
	}
	seconds := at.Sub(prev.at).Seconds()
	if seconds <= 0 {
		return unknown("invalid_interval", at)
	}
	return known((n-prev.value)/seconds, at)
}
func (c *Collector) Collect(ctx context.Context, at time.Time, fast int, s *protocol.ObservationSettings) *protocol.Observability {
	started := time.Now()
	settings := protocol.ObservationDefaults()
	if s != nil {
		settings = *s
	}
	boot, _ := read(filepath.Join(root(), "sys/kernel/random/boot_id"))
	if c.previous == nil || boot == "" || boot != c.boot || !c.last.IsZero() && (at.Sub(c.last) > time.Duration(fast*3)*time.Second || !at.After(c.last)) {
		c.previous = map[string]counter{}
	}
	c.boot, c.last = boot, at
	out := &protocol.Observability{Values: map[string]protocol.Metric[float64]{}, Entities: []protocol.DiagnosticEntity{}, SlowIntervalSeconds: settings.IntervalSeconds}
	for k, d := range protocol.DiagnosticDefinitions {
		if d.Interval == "fast" {
			out.Values[k] = unknown("unsupported", at)
		}
	}
	c.collectCPU(ctx, at, out)
	for _, resource := range []string{"cpu", "memory", "io"} {
		collectPressure(filepath.Join(root(), "pressure", resource), resource, at, out.Values)
	}
	stat, e := fields(filepath.Join(root(), "stat"))
	if e == nil {
		for k, src := range map[string]string{"cpu.running": "procs_running", "cpu.blocked": "procs_blocked"} {
			if n, ok := stat[src]; ok {
				out.Values[k] = known(n, at)
			}
		}
		if n, ok := stat["ctxt"]; ok {
			out.Values["cpu.context_switches_per_second"] = c.rate("ctxt", n, at)
		}
	}
	vm, e := fields(filepath.Join(root(), "vmstat"))
	for k, src := range map[string]string{"swap.in_bytes_per_second": "pswpin", "swap.out_bytes_per_second": "pswpout", "memory.major_faults_per_second": "pgmajfault", "memory.oom_kills_per_second": "oom_kill"} {
		if e != nil {
			out.Values[k] = unknown(reason(e), at)
			continue
		}
		if n, ok := vm[src]; ok {
			if strings.HasPrefix(k, "swap.") {
				n *= float64(os.Getpagesize())
			}
			out.Values[k] = c.rate(src, n, at)
		}
	}
	if m, e := mem.VirtualMemoryWithContext(ctx); e == nil {
		out.Values["memory.available_bytes"] = known(float64(m.Available), at)
		out.Values["memory.dirty_bytes"] = known(float64(m.Dirty), at)
		out.Values["memory.slab_bytes"] = known(float64(m.Slab), at)
	}
	c.collectNetwork(at, out)
	c.collectDisk(ctx, at, out)
	if p, e := process.NewProcessWithContext(ctx, int32(os.Getpid())); e == nil {
		if m, e := p.MemoryInfoWithContext(ctx); e == nil {
			out.Values["agent.rss_bytes"] = known(float64(m.RSS), at)
		}
		if t, e := p.TimesWithContext(ctx); e == nil {
			v := c.rate("selfcpu", t.User+t.System, at)
			if v.Value != nil {
				*v.Value *= 100
			}
			out.Values["agent.cpu_percent"] = v
		}
	}
	c.mu.Lock()
	raw, _ := json.Marshal(settings)
	settingsID := string(raw)
	if settingsID != c.settingsID {
		c.settingsID = settingsID
		c.slow = nil
		c.lastSlow = time.Time{}
	}
	if c.slow != nil {
		saved := protocol.CloneObservability(c.slow)
		for k, v := range saved.Values {
			out.Values[k] = v
		}
		out.Entities = append(out.Entities, saved.Entities...)
		out.Truncated = saved.Truncated
	}
	if !c.running && at.Sub(c.lastSlow) >= time.Duration(settings.IntervalSeconds)*time.Second {
		c.running = true
		c.lastSlow = at
		go func() {
			defer func() { c.mu.Lock(); c.running = false; c.mu.Unlock() }()
			runCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
			defer cancel()
			slow := c.collectSlow(runCtx, at, settings, boot)
			c.mu.Lock()
			if c.settingsID == settingsID {
				c.slow = slow
			}
			c.mu.Unlock()
		}()
	}
	c.mu.Unlock()
	for k, d := range protocol.DiagnosticDefinitions {
		if _, ok := out.Values[k]; !ok && d.Interval == "slow" {
			out.Values[k] = unknown("sampling", at)
		}
	}
	out.Values["agent.collection_ms"] = known(float64(time.Since(started).Microseconds())/1000, at)
	return out
}
func (c *Collector) collectCPU(ctx context.Context, at time.Time, o *protocol.Observability) {
	stats, e := cpu.TimesWithContext(ctx, true)
	if e != nil {
		return
	}
	names := make([]string, 0, len(stats))
	for _, v := range stats {
		names = append(names, v.CPU)
	}
	sort.Strings(names)
	selection := strings.Join(names, "|")
	if selection != c.cpuSelection {
		for k := range c.previous {
			if strings.HasPrefix(k, "cpu/") {
				delete(c.previous, k)
			}
		}
		c.cpuSelection = selection
	}
	var user, system, steal, io, soft, total float64
	valid := true
	why := "no_counter_progress"
	for i, t := range stats {
		vals := []float64{t.User + t.Nice, t.System + t.Irq, t.Steal, t.Iowait, t.Softirq, t.Idle}
		diff := make([]float64, len(vals))
		ok := true
		for j, n := range vals {
			v := c.rate(fmt.Sprintf("cpu/%s/%d", t.CPU, j), n, at)
			if v.Value == nil {
				ok = false
				why = v.Reason
			} else {
				diff[j] = *v.Value
			}
		}
		sum := 0.0
		for _, v := range diff {
			sum += v
		}
		if !ok || sum <= 0 {
			valid = false
			continue
		}
		user += diff[0]
		system += diff[1]
		steal += diff[2]
		io += diff[3]
		soft += diff[4]
		total += sum
		if i < 32 {
			o.Entities = append(o.Entities, protocol.DiagnosticEntity{Kind: "cpu", ID: t.CPU, Name: t.CPU, Values: map[string]protocol.Metric[float64]{"usage_percent": known(100*(sum-diff[3]-diff[5])/sum, at)}})
		} else {
			o.Truncated = true
		}
	}
	for k, n := range map[string]float64{"cpu.user_percent": user, "cpu.system_percent": system, "cpu.steal_percent": steal, "cpu.iowait_percent": io, "cpu.softirq_percent": soft} {
		if valid && total > 0 {
			o.Values[k] = known(100*n/total, at)
		} else {
			o.Values[k] = unknown(why, at)
		}
	}
}
func collectPressure(path, resource string, at time.Time, values map[string]protocol.Metric[float64]) {
	s, e := read(path)
	if e != nil {
		for k := range values {
			if strings.HasPrefix(k, "pressure."+resource+".") {
				values[k] = unknown(reason(e), at)
			}
		}
		return
	}
	for _, line := range strings.Split(s, "\n") {
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}
		for _, token := range f[1:] {
			pair := strings.SplitN(token, "=", 2)
			if len(pair) != 2 || !strings.HasPrefix(pair[0], "avg") {
				continue
			}
			n, e := strconv.ParseFloat(pair[1], 64)
			if e == nil && n >= 0 && n <= 100 {
				key := "pressure." + resource + "." + f[0] + strings.TrimPrefix(pair[0], "avg")
				if _, exists := values[key]; exists {
					values[key] = known(n, at)
				}
			}
		}
	}
}
func (c *Collector) collectNetwork(at time.Time, o *protocol.Observability) {
	for file, wanted := range map[string]map[string]string{"snmp": {"RetransSegs": "network.tcp_retransmits_per_second", "AttemptFails": "network.tcp_failures_per_second"}, "netstat": {"ListenOverflows": "network.listen_overflows_per_second"}} {
		f, e := os.Open(filepath.Join(root(), "net", file))
		if e != nil {
			continue
		}
		scan := bufio.NewScanner(f)
		for scan.Scan() {
			header := strings.Fields(scan.Text())
			if !scan.Scan() {
				break
			}
			nums := strings.Fields(scan.Text())
			if len(header) != len(nums) || len(header) == 0 || header[0] != nums[0] {
				continue
			}
			for i, name := range header {
				if key, ok := wanted[name]; ok {
					n, e := strconv.ParseFloat(nums[i], 64)
					if e == nil {
						o.Values[key] = c.rate(key, n, at)
					}
				}
			}
		}
		f.Close()
	}
	count, e1 := read(filepath.Join(root(), "sys/net/netfilter/nf_conntrack_count"))
	limit, e2 := read(filepath.Join(root(), "sys/net/netfilter/nf_conntrack_max"))
	a, e3 := strconv.ParseFloat(count, 64)
	b, e4 := strconv.ParseFloat(limit, 64)
	if e1 == nil && e2 == nil && e3 == nil && e4 == nil && b > 0 {
		o.Values["network.conntrack_count"] = known(a, at)
		o.Values["network.conntrack_limit"] = known(b, at)
		o.Values["network.conntrack_percent"] = known(a/b*100, at)
	}
}
func (c *Collector) collectDisk(ctx context.Context, at time.Time, o *protocol.Observability) {
	stats, e := disk.IOCountersWithContext(ctx)
	if e != nil {
		return
	}
	keys := make([]string, 0, len(stats))
	for k := range stats {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	identities := []string{}
	for _, name := range keys {
		if _, e := os.Stat(filepath.Join("/sys/block", name, "device")); e == nil {
			seq, _ := read(filepath.Join("/sys/block", name, "diskseq"))
			identities = append(identities, name+"/"+seq)
		}
	}
	selection := strings.Join(identities, "|")
	if selection != c.diskSelection {
		for k := range c.previous {
			if strings.HasPrefix(k, "disk/") {
				delete(c.previous, k)
			}
		}
		c.diskSelection = selection
	}

	var reads, writes, readms, writems, queue float64
	valid := true
	why := "warming_up"
	count := 0
	for _, name := range keys {
		if _, e := os.Stat(filepath.Join("/sys/block", name, "device")); e != nil {
			continue
		}
		count++
		d := stats[name]
		identity, _ := read(filepath.Join("/sys/block", name, "diskseq"))
		name = name + "/" + identity
		rates := []protocol.Metric[float64]{c.rate("disk/"+name+"/reads", float64(d.ReadCount), at), c.rate("disk/"+name+"/writes", float64(d.WriteCount), at), c.rate("disk/"+name+"/readms", float64(d.ReadTime), at), c.rate("disk/"+name+"/writems", float64(d.WriteTime), at), c.rate("disk/"+name+"/weighted", float64(d.WeightedIO), at)}
		for _, r := range rates {
			if r.Value == nil {
				valid = false
				why = r.Reason
			}
		}
		if !valid {
			continue
		}
		reads += *rates[0].Value
		writes += *rates[1].Value
		readms += *rates[2].Value
		writems += *rates[3].Value
		queue += *rates[4].Value / 1000
	}
	if count == 0 {
		return
	}
	if !valid {
		for _, k := range []string{"disk.read_iops", "disk.write_iops", "disk.read_latency_ms", "disk.write_latency_ms", "disk.queue"} {
			o.Values[k] = unknown(why, at)
		}
		return
	}
	o.Values["disk.read_iops"] = known(reads, at)
	o.Values["disk.write_iops"] = known(writes, at)
	o.Values["disk.queue"] = known(queue, at)
	if reads > 0 {
		o.Values["disk.read_latency_ms"] = known(readms/reads, at)
	} else {
		o.Values["disk.read_latency_ms"] = unknown("no_operations", at)
	}
	if writes > 0 {
		o.Values["disk.write_latency_ms"] = known(writems/writes, at)
	} else {
		o.Values["disk.write_latency_ms"] = unknown("no_operations", at)
	}
}
