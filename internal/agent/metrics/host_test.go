package metrics

import (
	"context"
	"github.com/CST-Cat/NodeDance/internal/protocol"
	"github.com/shirou/gopsutil/v4/mem"
	gnet "github.com/shirou/gopsutil/v4/net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestBillingPeriodsUTCAndGPUUnavailableFields(t *testing.T) {
	at := time.Date(2026, 3, 1, 0, 0, 0, 0, time.FixedZone("east", 8*3600))
	if got := BillingPeriod(at, 28); got.Format("2006-01-02") != "2026-02-28" || got.Location() != time.UTC {
		t.Fatal(got)
	}
	devices := parseNvidia([]byte("GPU-id, NVIDIA Test, 47, 8192, 1024, 61\nGPU-other, NVIDIA Other, N/A, N/A, N/A, N/A\n"))
	if len(devices) != 2 || devices[0].Utilization == nil || *devices[0].Utilization != 47 || devices[1].Utilization != nil {
		t.Fatalf("GPU partial fields %+v", devices)
	}
	for _, s := range []string{"NaN", "Inf", "N/A", "-1"} {
		if parseFloat(s, 1) != nil {
			t.Fatalf("invalid GPU value %s", s)
		}
	}
}

type trafficTestSource struct {
	gopsutilSource
	rx, tx uint64
	boot   string
	index  int
}

func (s *trafficTestSource) bootID(context.Context) (string, error) { return s.boot, nil }
func (s *trafficTestSource) networkCounters(context.Context) ([]gnet.IOCountersStat, error) {
	return []gnet.IOCountersStat{{Name: "eth0", BytesRecv: s.rx, BytesSent: s.tx}}, nil
}
func (s *trafficTestSource) linkMetadata(string) (linkMetadata, error) {
	return linkMetadata{ifindex: s.index}, nil
}
func TestDurableTrafficCounterResetsAndBillingRollover(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("traffic state is root-only")
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	source := &trafficTestSource{rx: 1000, tx: 2000, boot: "boot1", index: 1}
	h := &hostCollector{source: source, path: filepath.Join(dir, "traffic.json")}
	s := protocol.DefaultAgentSettings()
	interfaces := []NetworkInterface{{Name: "eth0", IncludedInSummary: true}}
	at := time.Date(2026, 9, 30, 23, 59, 50, 0, time.UTC)
	baseline := h.collectTraffic(at, s, interfaces)
	if baseline.Status != protocol.MetricKnown {
		t.Fatal(baseline)
	}
	source.rx += 100
	source.tx += 200
	next := h.collectTraffic(at.Add(5*time.Second), s, interfaces)
	if next.Value.ReceivedBytes != 100 || next.Value.PeriodSentBytes != 200 {
		t.Fatal(next)
	}
	restarted := &hostCollector{source: source, path: h.path}
	source.rx += 50
	source.tx += 80
	v := restarted.collectTraffic(at.Add(10*time.Second), s, interfaces)
	if v.Value.ReceivedBytes != 150 || v.Value.SentBytes != 280 || v.Value.PeriodReceivedBytes != 0 || v.Reason != "partial_coverage" {
		t.Fatal("lost lifetime counters or falsely exact boundary", v)
	}
	source.boot = "boot2"
	source.rx = 3
	source.tx = 5
	v = restarted.collectTraffic(at.Add(15*time.Second), s, interfaces)
	if v.Value.ReceivedBytes != 150 || v.Value.Gaps == 0 {
		t.Fatal("counter reset generated false traffic", v)
	}
	source.rx = 13
	source.tx = 25
	v = restarted.collectTraffic(at.Add(20*time.Second), s, interfaces)
	if v.Value.PeriodReceivedBytes != 10 || v.Value.PeriodSentBytes != 20 {
		t.Fatal("new baseline failed", v)
	}
}

func TestMemoryAccountingPolicies(t *testing.T) {
	sample := &mem.VirtualMemoryStat{Total: 1000, Available: 400, Free: 200, Used: 500}
	for mode, expected := range map[string]uint64{"available": 600, "raw": 500, "cached": 800} {
		v, reason := memoryUsageMode(sample, mode)
		if reason != "" || v.UsedBytes != expected || v.AvailableBytes+v.UsedBytes != v.TotalBytes {
			t.Fatalf("%s: %+v %s", mode, v, reason)
		}
	}
}

func TestAMDDriverMetricsFromSysfs(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOST_SYS", root)
	t.Setenv("PATH", root)
	dir := filepath.Join(root, "class", "drm", "card0", "device")
	if err := os.MkdirAll(filepath.Join(dir, "hwmon", "hwmon0"), 0700); err != nil {
		t.Fatal(err)
	}
	for path, text := range map[string]string{"vendor": "0x1002\n", "product_name": "AMD Test\n", "gpu_busy_percent": "67\n", "mem_info_vram_total": "2000000", "mem_info_vram_used": "1000000", "hwmon/hwmon0/temp1_input": "64000"} {
		if err := os.WriteFile(filepath.Join(dir, path), []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
	}
	devices, err := collectGPUs(context.Background())
	if err != nil || len(devices) != 1 || devices[0].Utilization == nil || *devices[0].Utilization != 67 || devices[0].TemperatureCelsius == nil || *devices[0].TemperatureCelsius != 64 {
		t.Fatal("AMD device metrics", devices, err)
	}
}
