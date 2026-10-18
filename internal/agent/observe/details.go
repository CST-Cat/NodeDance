package observe

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/CST-Cat/NodeDance/internal/protocol"
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
