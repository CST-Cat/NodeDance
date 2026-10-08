package metrics

import (
	"math"
	"testing"
	"time"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/disk"
	"github.com/shirou/gopsutil/v4/mem"
)

func TestCPUUsagePercentLinuxGuestAndIdleRules(t *testing.T) {
	previous := cpu.TimesStat{
		CPU: "cpu", User: 100, Nice: 10, System: 50, Idle: 300,
		Iowait: 40, Irq: 1, Softirq: 2, Steal: 3, Guest: 20, GuestNice: 2,
	}
	current := cpu.TimesStat{
		CPU: "cpu", User: 101, Nice: 12, System: 53, Idle: 320,
		Iowait: 50, Irq: 2, Softirq: 3, Steal: 4, Guest: 21, GuestNice: 3,
	}
	got, reason := CPUUsagePercent(previous, current)
	if reason != "" {
		t.Fatalf("CPUUsagePercent reason = %q", reason)
	}
	// Total delta is 39; busy delta is 9. Guest deltas are already included
	// in user/nice, while idle and iowait do not count as busy.
	want := 100 * 9.0 / 39.0
	if math.Abs(got-want) > 1e-9 {
		t.Fatalf("CPUUsagePercent = %.12f, want %.12f", got, want)
	}
}

func TestCPUUsagePercentRejectsResetAndEmptyWindow(t *testing.T) {
	previous := cpu.TimesStat{CPU: "cpu", User: 10, Idle: 10}
	reset := previous
	reset.User = 9
	if _, reason := CPUUsagePercent(previous, reset); reason != "counter_reset" {
		t.Fatalf("reset reason = %q, want counter_reset", reason)
	}
	if _, reason := CPUUsagePercent(previous, previous); reason != "no_counter_progress" {
		t.Fatalf("empty-window reason = %q, want no_counter_progress", reason)
	}
	if _, reason := CPUUsagePercent(previous, cpu.TimesStat{CPU: "cpu1", User: 20}); reason != "cpu_set_changed" {
		t.Fatalf("CPU set reason = %q, want cpu_set_changed", reason)
	}
}

func TestMemoryUsageUsesTotalMinusAvailable(t *testing.T) {
	got, reason := memoryUsage(&mem.VirtualMemoryStat{Total: 1000, Available: 275, Free: 100})
	if reason != "" {
		t.Fatalf("memoryUsage reason = %q", reason)
	}
	if got.UsedBytes != 725 || got.UsedPercent != 72.5 {
		t.Fatalf("memory usage = %+v, want used 725 and 72.5%%", got)
	}
	if _, reason := memoryUsage(&mem.VirtualMemoryStat{Total: 100, Available: 101}); reason != "invalid_data" {
		t.Fatalf("invalid memory reason = %q", reason)
	}
}

func TestDiskUsagePreservesStatfsAvailablePercent(t *testing.T) {
	got, reason := diskUsage(&disk.UsageStat{Total: 1000, Free: 100, Used: 800, UsedPercent: 800.0 / 900.0 * 100})
	if reason != "" {
		t.Fatalf("diskUsage reason = %q", reason)
	}
	if got.TotalBytes != 1000 || got.AvailableBytes != 100 || got.UsedBytes != 800 || math.Abs(got.UsedPercent-88.88888888888889) > 1e-9 {
		t.Fatalf("disk usage = %+v", got)
	}
	if _, reason := diskUsage(&disk.UsageStat{Total: 100, Free: 0, Used: 0, UsedPercent: 0}); reason != "invalid_data" {
		t.Fatalf("empty disk reason = %q", reason)
	}
}

func TestNetworkRateFirstSampleResetAndUnits(t *testing.T) {
	if _, reason := networkRate(10, 10, 10, 10, 5); reason != "" {
		t.Fatalf("zero-rate sample should be valid, reason %q", reason)
	}
	got, reason := networkRate(100, 250, 600, 1250, 5)
	if reason != "" {
		t.Fatalf("networkRate reason = %q", reason)
	}
	if got.ReceivedBytesPerSecond != 100 || got.SentBytesPerSecond != 200 {
		t.Fatalf("network rate = %+v, want RX 100 and TX 200 bytes/s", got)
	}
	if _, reason := networkRate(500, 20, 10, 25, 5); reason != "counter_reset" {
		t.Fatalf("counter reset reason = %q", reason)
	}
	if _, reason := networkRate(1, 1, 2, 2, 0); reason != "invalid_interval" {
		t.Fatalf("zero interval reason = %q", reason)
	}
	if got := unknown[NetworkRate]("warming_up", time.Now()); got.Value != nil || got.State != StateUnknown {
		t.Fatalf("unknown metric must not contain a zero-valued rate: %+v", got)
	}
}
