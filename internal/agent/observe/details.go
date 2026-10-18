package observe

import (
	"context"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/CST-Cat/NodeDance/internal/protocol"
	"github.com/shirou/gopsutil/v4/disk"
	"github.com/shirou/gopsutil/v4/process"
)

func (c *Collector) collectSlow(ctx context.Context, at time.Time, s protocol.ObservationSettings, boot string) *protocol.Observability {
	started := time.Now()
	if c.slowCounters == nil || c.slowBoot != boot || !c.slowCounters.last.IsZero() && at.Sub(c.slowCounters.last) > time.Duration(s.IntervalSeconds*3)*time.Second {
		c.slowCounters = &Collector{previous: map[string]counter{}}
		c.slowBoot = boot
	}
	c.slowCounters.last = at
	o := &protocol.Observability{Values: map[string]protocol.Metric[float64]{}, Entities: []protocol.DiagnosticEntity{}, SlowIntervalSeconds: s.IntervalSeconds}
	c.slowCounters.collectProcesses(ctx, at, s.ProcessLimit, o)
	c.slowCounters.collectCgroups(ctx, at, s.ContainerLimit, o)
	collectFilesystems(ctx, at, o)
	collectServices(ctx, at, s.Services, o)
	collectApplications(ctx, at, s.Applications, o)
	o.Values["agent.slow_collection_ms"] = known(float64(time.Since(started).Microseconds())/1000, at)
	return o
}

// Read comm directly: library name helpers may expand it from argv, which can
// expose credentials and exceed the entity-name wire limit.
func processName(id int32) (string, error) {
	f, err := os.Open(filepath.Join(root(), strconv.FormatInt(int64(id), 10), "comm"))
	if err != nil {
		return "", err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, 257))
	if err != nil {
		return "", err
	}
	name := strings.TrimSpace(string(data))
	if name == "" || len(name) > 256 {
		return "", fmt.Errorf("invalid process name")
	}
	return name, nil
}

func (c *Collector) collectProcesses(ctx context.Context, at time.Time, limit int, o *protocol.Observability) {
	if limit == 0 {
		o.Values["process.collected_count"] = unknown("disabled", at)
		return
	}
	ids, e := process.PidsWithContext(ctx)
	if e != nil {
		o.Values["process.collected_count"] = unknown(reason(e), at)
		return
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	entities := []protocol.DiagnosticEntity{}
	scanCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	seen := map[string]bool{}
	for i, id := range ids {
		if i >= 2048 || scanCtx.Err() != nil {
			o.Truncated = true
			break
		}
		p, e := process.NewProcessWithContext(scanCtx, id)
		if e != nil {
			continue
		}
		created, e := p.CreateTimeWithContext(scanCtx)
		if e != nil {
			continue
		}
		identity := fmt.Sprintf("%d/%d", id, created)
		name, e := processName(id)
		if e != nil {
			continue
		}
		v := map[string]protocol.Metric[float64]{}
		if m, e := p.MemoryInfoWithContext(scanCtx); e == nil {
			v["rss_bytes"] = known(float64(m.RSS), at)
		}
		if t, e := p.TimesWithContext(scanCtx); e == nil {
			key := "process/" + identity
			seen[key] = true
			r := c.rate(key, t.User+t.System, at)
			if r.Value != nil {
				*r.Value *= 100
			}
			v["cpu_percent"] = r
		}
		if n, e := p.NumThreadsWithContext(scanCtx); e == nil {
			v["threads"] = known(float64(n), at)
		}
		if n, e := p.NumFDsWithContext(scanCtx); e == nil {
			v["file_descriptors"] = known(float64(n), at)
		}
		if io, e := p.IOCountersWithContext(scanCtx); e == nil {
			for suffix, n := range map[string]uint64{"read_bytes_per_second": io.ReadBytes, "write_bytes_per_second": io.WriteBytes} {
				key := "process/" + identity + "/" + suffix
				seen[key] = true
				v[suffix] = c.rate(key, float64(n), at)
			}
		}
		entities = append(entities, protocol.DiagnosticEntity{Kind: "process", ID: identity, Name: name, Values: v})
	}
	// Union CPU, RSS and I/O rankings within the overall configured limit.
	score := func(e protocol.DiagnosticEntity, k string) float64 {
		if v := e.Values[k]; v.Status == protocol.MetricKnown && v.Value != nil {
			return *v.Value
		}
		return -1
	}
	chosen := map[string]bool{}
	for rank := 0; len(chosen) < limit && rank < len(entities); rank++ {
		for _, key := range []string{"cpu_percent", "rss_bytes", "read_bytes_per_second", "write_bytes_per_second"} {
			sort.SliceStable(entities, func(i, j int) bool {
				a, b := score(entities[i], key), score(entities[j], key)
				if a == b {
					return entities[i].ID < entities[j].ID
				}
				return a > b
			})
			if rank < len(entities) && len(chosen) < limit {
				chosen[entities[rank].ID] = true
			}
		}
	}
	sort.Slice(entities, func(i, j int) bool { return entities[i].ID < entities[j].ID })
	for _, e := range entities {
		if chosen[e.ID] {
			o.Entities = append(o.Entities, e)
		}
	}
	o.Values["process.collected_count"] = known(float64(len(chosen)), at)
	for k := range c.previous {
		if strings.HasPrefix(k, "process/") && !seen[k] {
			delete(c.previous, k)
		}
	}
}
func (c *Collector) collectCgroups(ctx context.Context, at time.Time, limit int, o *protocol.Observability) {
	if limit == 0 {
		o.Values["container.collected_count"] = unknown("disabled", at)
		return
	}
	base := "/sys/fs/cgroup"
	if _, err := os.Stat(filepath.Join(base, "cgroup.controllers")); err != nil {
		o.Values["container.collected_count"] = unknown(reason(err), at)
		return
	}
	visited, count := 0, 0
	seen := map[string]bool{}
	_ = filepath.WalkDir(base, func(path string, d os.DirEntry, err error) error {
		if ctx.Err() != nil || visited >= 2048 || count >= limit {
			o.Truncated = true
			return filepath.SkipAll
		}
		if err != nil {
			return nil
		}
		visited++
		if !d.IsDir() {
			return nil
		}
		if d.Type()&os.ModeSymlink != 0 {
			return filepath.SkipDir
		}
		rel, _ := filepath.Rel(base, path)
		if len(rel) > 512 || len(d.Name()) > 256 {
			o.Truncated = true
			return filepath.SkipDir
		}
		if strings.Count(rel, string(os.PathSeparator)) > 12 {
			return filepath.SkipDir
		}
		if !strings.Contains(d.Name(), "docker-") && !strings.Contains(d.Name(), "cri-containerd-") && !(len(d.Name()) == 64 && strings.Contains(path, "docker/")) {
			return nil
		}
		v := map[string]protocol.Metric[float64]{}
		if stat, e := fields(filepath.Join(path, "cpu.stat")); e == nil {
			for key, source := range map[string]string{"cpu_cores": "usage_usec", "cpu_throttled_seconds_per_second": "throttled_usec", "cpu_throttled_periods_per_second": "nr_throttled", "cpu_periods_per_second": "nr_periods"} {
				if n, ok := stat[source]; ok {
					identity := "cgroup/" + rel + "/" + source
					seen[identity] = true
					r := c.rate(identity, n, at)
					if r.Value != nil && (source == "usage_usec" || source == "throttled_usec") {
						*r.Value /= 1e6
					}
					v[key] = r
				}
			}
		}
		if raw, e := read(filepath.Join(path, "cpu.max")); e == nil {
			parts := strings.Fields(raw)
			if len(parts) == 2 {
				if parts[0] == "max" {
					v["cpu_limit_cores"] = unknown("unlimited", at)
				} else {
					quota, e1 := strconv.ParseFloat(parts[0], 64)
					period, e2 := strconv.ParseFloat(parts[1], 64)
					if e1 == nil && e2 == nil && period > 0 {
						v["cpu_limit_cores"] = known(quota/period, at)
						if usage := v["cpu_cores"]; usage.Value != nil && quota > 0 {
							v["cpu_quota_percent"] = known(*usage.Value/(quota/period)*100, at)
						}
					}
				}
			}
		}
		if a, b := v["cpu_throttled_periods_per_second"], v["cpu_periods_per_second"]; a.Value != nil && b.Value != nil && *b.Value > 0 {
			v["cpu_throttled_period_percent"] = known(*a.Value / *b.Value * 100, at)
		}

		for key, file := range map[string]string{"memory_bytes": "memory.current", "memory_limit_bytes": "memory.max", "pids": "pids.current", "pids_limit": "pids.max"} {
			s, e := read(filepath.Join(path, file))
			if e != nil {
				v[key] = unknown(reason(e), at)
			} else if s == "max" {
				v[key] = unknown("unlimited", at)
			} else if n, e := strconv.ParseFloat(s, 64); e == nil {
				v[key] = known(n, at)
			}
		}
		if stat, e := fields(filepath.Join(path, "memory.events")); e == nil {
			if n, ok := stat["oom_kill"]; ok {
				v["oom_kills"] = known(n, at)
				identity := "cgroup/" + rel + "/oom_kill"
				seen[identity] = true
				v["oom_kills_per_second"] = c.rate(identity, n, at)
			}
		}
		for _, resource := range []string{"cpu", "memory", "io"} {
			values := map[string]protocol.Metric[float64]{}
			for _, mode := range []string{"some", "full"} {
				if resource == "cpu" && mode == "full" {
					continue
				}
				values["pressure."+resource+"."+mode+"10"] = unknown("unsupported", at)
			}
			collectPressure(filepath.Join(path, resource+".pressure"), resource, at, values)
			for key, value := range values {
				v[key] = value
			}
		}
		count++
		o.Entities = append(o.Entities, protocol.DiagnosticEntity{Kind: "container", ID: rel, Name: d.Name(), Values: v})
		return filepath.SkipDir
	})
	o.Values["container.collected_count"] = known(float64(count), at)
	for k := range c.previous {
		if strings.HasPrefix(k, "cgroup/") && !seen[k] {
			delete(c.previous, k)
		}
	}
}
func collectFilesystems(ctx context.Context, at time.Time, o *protocol.Observability) {
	parts, e := disk.PartitionsWithContext(ctx, false)
	if e != nil {
		o.Values["filesystem.inode_used_percent"] = unknown(reason(e), at)
		return
	}
	maximum := 0.0
	found := false
	count := 0
	for _, p := range parts {
		if len(p.Mountpoint) > 256 {
			o.Truncated = true
			continue
		}
		if ctx.Err() != nil {
			o.Truncated = true
			break
		}
		switch p.Fstype {
		case "ext2", "ext3", "ext4", "xfs", "btrfs", "zfs", "f2fs", "tmpfs":
		default:
			continue
		}
		if count >= 16 {
			o.Truncated = true
			break
		}
		u, e := disk.UsageWithContext(ctx, p.Mountpoint)
		if e != nil {
			continue
		}
		count++
		v := map[string]protocol.Metric[float64]{"total_bytes": known(float64(u.Total), at), "available_bytes": known(float64(u.Free), at), "used_bytes": known(float64(u.Used), at), "used_percent": known(u.UsedPercent, at)}
		if u.InodesTotal > 0 {
			found = true
			v["inode_used_percent"] = known(u.InodesUsedPercent, at)
			maximum = max(maximum, u.InodesUsedPercent)
		} else {
			v["inode_used_percent"] = unknown("unsupported", at)
		}
		o.Entities = append(o.Entities, protocol.DiagnosticEntity{Kind: "filesystem", ID: p.Mountpoint, Name: p.Mountpoint, Values: v})
	}
	if found {
		o.Values["filesystem.inode_used_percent"] = known(maximum, at)
	} else {
		o.Values["filesystem.inode_used_percent"] = unknown("unsupported", at)
	}
}
func command(ctx context.Context, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "systemctl", args...)
	cmd.Env = []string{"PATH=/usr/bin:/bin", "LC_ALL=C", "SYSTEMD_PAGER=cat"}
	stdout, e := cmd.StdoutPipe()
	if e != nil {
		return "", e
	}
	if e = cmd.Start(); e != nil {
		return "", e
	}
	b, readErr := io.ReadAll(io.LimitReader(stdout, 64<<10))
	if len(b) >= 64<<10 {
		_ = cmd.Process.Kill()
	}
	waitErr := cmd.Wait()
	if readErr != nil {
		return "", readErr
	}
	return string(b), waitErr
}
func collectServices(ctx context.Context, at time.Time, names []string, o *protocol.Observability) {
	text, e := command(ctx, "list-units", "--failed", "--type=service", "--no-legend", "--plain", "--no-pager")
	if e != nil {
		o.Values["service.failed_count"] = unknown("unsupported", at)
	} else {
		count := 0
		for _, line := range strings.Split(strings.TrimSpace(text), "\n") {
			if strings.TrimSpace(line) != "" {
				count++
			}
		}
		o.Values["service.failed_count"] = known(float64(count), at)
	}
	for _, name := range names {
		v := map[string]protocol.Metric[float64]{"active": unknown("read_failed", at), "restarts": unknown("read_failed", at), "exit_status": unknown("read_failed", at)}
		s, e := command(ctx, "show", "--property=ActiveState,NRestarts,ExecMainStatus", "--", name)
		if e == nil {
			for _, line := range strings.Split(s, "\n") {
				pair := strings.SplitN(line, "=", 2)
				if len(pair) != 2 {
					continue
				}
				switch pair[0] {
				case "ActiveState":
					n := 0.0
					if pair[1] == "active" {
						n = 1
					}
					v["active"] = known(n, at)
				case "NRestarts", "ExecMainStatus":
					n, e := strconv.ParseFloat(pair[1], 64)
					if e == nil {
						key := "restarts"
						if pair[0] == "ExecMainStatus" {
							key = "exit_status"
						}
						v[key] = known(n, at)
					}
				}
			}
		}
		o.Entities = append(o.Entities, protocol.DiagnosticEntity{Kind: "service", ID: name, Name: name, Values: v})
	}
}
func collectApplications(ctx context.Context, at time.Time, apps []protocol.ApplicationCollector, o *protocol.Observability) {
	if o.Values == nil {
		o.Values = map[string]protocol.Metric[float64]{}
	}
	o.Values["application.collectors_count"] = known(float64(len(apps)), at)
	transport := &http.Transport{Proxy: nil, DisableKeepAlives: true}
	defer transport.CloseIdleConnections()
	client := http.Client{Transport: transport, Timeout: time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	for _, a := range apps {
		v := map[string]protocol.Metric[float64]{"collector_up": unknown("sampling", at)}
		for _, key := range a.Metrics {
			v[key] = unknown("unavailable", at)
		}
		func() {
			req, e := http.NewRequestWithContext(ctx, "GET", a.URL, nil)
			if e != nil {
				return
			}
			resp, e := client.Do(req)
			if e != nil {
				v["collector_up"] = unknown("connection_failed", at)
				return
			}
			defer resp.Body.Close()
			if resp.StatusCode != 200 {
				v["collector_up"] = unknown("http_status_mismatch", at)
				return
			}
			b, e := io.ReadAll(io.LimitReader(resp.Body, 256<<10+1))
			if e != nil || len(b) > 256<<10 {
				v["collector_up"] = unknown("response_limit", at)
				return
			}
			v["collector_up"] = known(1, at)
			if a.Kind == "nginx" {
				lines := strings.Split(string(b), "\n")
				if len(lines) >= 3 {
					nums := strings.Fields(lines[2])
					if len(nums) == 3 {
						for i, key := range []string{"accepts_total", "handled_total", "requests_total"} {
							if n, e := strconv.ParseFloat(nums[i], 64); e == nil && n >= 0 && !math.IsInf(n, 0) {
								v[key] = known(n, at)
							}
						}
					}
				}
				f := strings.Fields(string(b))
				for i, key := range f {
					if i+1 >= len(f) {
						continue
					}
					if key == "connections:" || key == "Reading:" || key == "Writing:" || key == "Waiting:" {
						n, e := strconv.ParseFloat(f[i+1], 64)
						if e == nil {
							v[strings.TrimSuffix(key, ":")] = known(n, at)
						}
					}
				}
				return
			}
			allowed := map[string]bool{}
			for _, key := range a.Metrics {
				allowed[key] = true
			}
			for _, line := range strings.Split(string(b), "\n") {
				if strings.HasPrefix(line, "#") {
					continue
				}
				f := strings.Fields(line)
				if len(f) != 2 || !allowed[f[0]] {
					continue
				}
				n, e := strconv.ParseFloat(f[1], 64)
				if e == nil && n >= 0 && !math.IsInf(n, 0) && !math.IsNaN(n) {
					v[f[0]] = known(n, at)
				}
			}
		}()
		o.Entities = append(o.Entities, protocol.DiagnosticEntity{Kind: "application", ID: a.Name, Name: a.Name, Values: v, Units: a.MetricUnits})
	}
}
