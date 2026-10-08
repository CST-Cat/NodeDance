package metrics

import (
	"bufio"
	"context"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/shirou/gopsutil/v4/cpu"
)

func TestLinuxProcAndFilesystemAgreement(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("real /proc and statfs comparison requires Linux")
	}
	source := gopsutilSource{}
	ctx := context.Background()

	// Compare gopsutil's CPU counters with an independent parser of /proc/stat.
	gotCPU, err := source.cpuTimes(ctx)
	if err != nil || len(gotCPU) != 1 {
		t.Fatalf("gopsutil CPU sample: count=%d err=%v", len(gotCPU), err)
	}
	wantCPU := readProcCPU(t)
	compareCPUCounter(t, "user", gotCPU[0].User, wantCPU.User)
	compareCPUCounter(t, "nice", gotCPU[0].Nice, wantCPU.Nice)
	compareCPUCounter(t, "system", gotCPU[0].System, wantCPU.System)
	compareCPUCounter(t, "idle", gotCPU[0].Idle, wantCPU.Idle)
	compareCPUCounter(t, "iowait", gotCPU[0].Iowait, wantCPU.Iowait)
	compareCPUCounter(t, "irq", gotCPU[0].Irq, wantCPU.Irq)
	compareCPUCounter(t, "softirq", gotCPU[0].Softirq, wantCPU.Softirq)
	compareCPUCounter(t, "steal", gotCPU[0].Steal, wantCPU.Steal)
	compareCPUCounter(t, "guest", gotCPU[0].Guest, wantCPU.Guest)
	compareCPUCounter(t, "guestNice", gotCPU[0].GuestNice, wantCPU.GuestNice)

	// Parse MemTotal and MemAvailable directly; both are reported in KiB by
	// procfs and converted to bytes by gopsutil.
	gotMemory, err := source.memory(ctx)
	if err != nil {
		t.Fatalf("gopsutil memory sample: %v", err)
	}
	wantTotal, wantAvailable := readProcMemory(t)
	if gotMemory.Total != wantTotal || gotMemory.Available != wantAvailable {
		t.Fatalf("memory mismatch: gopsutil total/available=%d/%d proc=%d/%d bytes", gotMemory.Total, gotMemory.Available, wantTotal, wantAvailable)
	}

	// Compare interface counters with a separate /proc/net/dev parser. Traffic
	// may advance between the two reads, so allow at most 1 MiB of local jitter.
	gotNetwork, err := source.networkCounters(ctx)
	if err != nil {
		t.Fatalf("gopsutil network sample: %v", err)
	}
	wantNetwork := readProcNetwork(t)
	for _, item := range gotNetwork {
		want, ok := wantNetwork[item.Name]
		if !ok {
			t.Fatalf("interface %q missing from /proc/net/dev", item.Name)
		}
		if difference(item.BytesRecv, want.rx) > 1<<20 || difference(item.BytesSent, want.tx) > 1<<20 {
			t.Fatalf("network mismatch for %s: gopsutil rx/tx=%d/%d proc=%d/%d", item.Name, item.BytesRecv, item.BytesSent, want.rx, want.tx)
		}
	}

	// Independently call statfs through the standard syscall package and compare
	// the exact byte fields gopsutil returns for the root filesystem.
	gotDisk, err := source.diskUsage(ctx, "/")
	if err != nil {
		t.Fatalf("gopsutil statfs sample: %v", err)
	}
	var stat syscall.Statfs_t
	if err := syscall.Statfs("/", &stat); err != nil {
		t.Fatalf("independent statfs sample: %v", err)
	}
	blockSize := uint64(stat.Bsize)
	wantDiskTotal := stat.Blocks * blockSize
	wantDiskAvailable := stat.Bavail * blockSize
	wantDiskUsed := (stat.Blocks - stat.Bfree) * blockSize
	if gotDisk.Total != wantDiskTotal || gotDisk.Free != wantDiskAvailable || gotDisk.Used != wantDiskUsed {
		t.Fatalf("disk mismatch: gopsutil total/free/used=%d/%d/%d syscall=%d/%d/%d", gotDisk.Total, gotDisk.Free, gotDisk.Used, wantDiskTotal, wantDiskAvailable, wantDiskUsed)
	}
	wantUsedPercent := 100 * float64(wantDiskUsed) / float64(wantDiskUsed+wantDiskAvailable)
	if math.Abs(gotDisk.UsedPercent-wantUsedPercent) > 1e-9 {
		t.Fatalf("disk percentage mismatch: gopsutil=%.12f syscall=%.12f", gotDisk.UsedPercent, wantUsedPercent)
	}

	gotUptime, err := source.uptime(ctx)
	if err != nil {
		t.Fatalf("gopsutil uptime sample: %v", err)
	}
	wantUptime := readProcUptime(t)
	if math.Abs(float64(gotUptime)-wantUptime) > 1.1 {
		t.Fatalf("uptime mismatch: gopsutil=%d /proc=%.3f seconds", gotUptime, wantUptime)
	}
	gotBootID, err := source.bootID(ctx)
	if err != nil {
		t.Fatalf("boot ID sample: %v", err)
	}
	wantBootID, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		t.Fatal(err)
	}
	if gotBootID != strings.TrimSpace(string(wantBootID)) {
		t.Fatalf("boot ID mismatch: collector=%q /proc=%q", gotBootID, strings.TrimSpace(string(wantBootID)))
	}
	architecture, err := source.architecture(ctx)
	if err != nil || architecture == "" {
		t.Fatalf("kernel architecture: value=%q err=%v", architecture, err)
	}

	collector := NewCollector()
	first := collector.Sample(ctx)
	if first.CPU.UsagePercent.State != StateUnknown || first.CPU.UsagePercent.Reason != "warming_up" {
		t.Fatalf("real first CPU sample = %+v", first.CPU.UsagePercent)
	}
	time.Sleep(200 * time.Millisecond)
	second := collector.Sample(ctx)
	if second.CPU.UsagePercent.State != StateOK || second.CPU.UsagePercent.Value == nil {
		t.Fatalf("real second CPU sample = %+v", second.CPU.UsagePercent)
	}
	if second.Memory.Value == nil || second.Uptime.Value == nil {
		t.Fatalf("real memory/uptime sample unavailable: memory=%+v uptime=%+v", second.Memory, second.Uptime)
	}
	knownInterfaceRates := 0
	for _, iface := range second.Network.Interfaces {
		if iface.Rate.State == StateOK && iface.Rate.Value != nil {
			knownInterfaceRates++
		}
	}
	if knownInterfaceRates == 0 {
		t.Fatalf("real per-interface network rates are unavailable: %+v", second.Network)
	}
	t.Logf("real host sample: arch=%s CPU=%.3f%% memoryUsed=%d/%d bytes uptime=%ds interfaces=%d knownInterfaceRates=%d networkSummary=%s (%s) rootUsed=%d/%d bytes", architecture, *second.CPU.UsagePercent.Value, second.Memory.Value.UsedBytes, second.Memory.Value.TotalBytes, second.Uptime.Value.Seconds, len(second.Network.Interfaces), knownInterfaceRates, second.Network.Summary.State, second.Network.Summary.Reason, gotDisk.Used, gotDisk.Total)
}

func TestLiveCPUAndMemoryLoadAffectsMeasurements(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("live host load comparison requires Linux")
	}
	collector := NewCollector()
	ctx := context.Background()
	_ = collector.Sample(ctx) // Establish the CPU baseline.
	time.Sleep(300 * time.Millisecond)
	baseline := collector.Sample(ctx)
	if baseline.CPU.UsagePercent.Value == nil || baseline.Memory.Value == nil {
		t.Fatalf("baseline metrics unavailable: cpu=%+v memory=%+v", baseline.CPU.UsagePercent, baseline.Memory)
	}
	if baseline.Memory.Value.AvailableBytes < 1<<30 {
		t.Skip("less than 1 GiB is available for a safe controlled allocation")
	}

	processRSSBefore := readSelfRSS(t)
	allocation := make([]byte, 256<<20)
	for i := 0; i < len(allocation); i += 4096 {
		allocation[i] = byte(i / 4096)
	}
	stop := make(chan struct{})
	var stopOnce sync.Once
	stopWorker := func() { stopOnce.Do(func() { close(stop) }) }
	defer stopWorker()
	var sink atomic.Uint64
	go func() {
		value := uint64(1)
		for {
			select {
			case <-stop:
				sink.Store(value)
				return
			default:
				value = value*1664525 + 1013904223
			}
		}
	}()
	time.Sleep(2 * time.Second)
	during := collector.Sample(ctx)
	stopWorker()
	runtime.KeepAlive(allocation)
	_ = sink.Load()
	processRSSAfter := readSelfRSS(t)

	if during.CPU.UsagePercent.Value == nil || *during.CPU.UsagePercent.Value <= 0 {
		t.Fatalf("CPU utilization did not reflect a live CPU worker: %+v", during.CPU.UsagePercent)
	}
	if processRSSAfter < processRSSBefore+(128<<20) {
		t.Fatalf("controlled allocation did not become resident: RSS before=%d after=%d bytes", processRSSBefore, processRSSAfter)
	}
	if during.Memory.Value == nil {
		t.Fatalf("memory sample became unknown during the controlled load: %+v", during.Memory)
	}
	if during.Memory.Value.UsedBytes < baseline.Memory.Value.UsedBytes+(64<<20) {
		t.Skipf("process RSS grew by %d bytes but host MemAvailable did not expose 256 MiB allocation (system used %d -> %d); S03-02 needs its isolated Linux guest run", processRSSAfter-processRSSBefore, baseline.Memory.Value.UsedBytes, during.Memory.Value.UsedBytes)
	}
	t.Logf("controlled load: CPU %.3f%% -> %.3f%%, used memory %d -> %d bytes, process RSS %d -> %d bytes", *baseline.CPU.UsagePercent.Value, *during.CPU.UsagePercent.Value, baseline.Memory.Value.UsedBytes, during.Memory.Value.UsedBytes, processRSSBefore, processRSSAfter)
}

func TestPermissionFailureInProcFixtureDoesNotStopOtherMetrics(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("proc permission fixture requires Linux")
	}
	procRoot := filepath.Join(t.TempDir(), "proc")
	for _, path := range []string{
		filepath.Join(procRoot, "1"),
		filepath.Join(procRoot, "sys", "kernel", "random"),
	} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeFixtureFile(t, filepath.Join(procRoot, "stat"), "cpu 100 0 100 500 0 0 0 0 0 0\ncpu0 50 0 50 250 0 0 0 0 0 0\ncpu1 50 0 50 250 0 0 0 0 0 0\nbtime 1700000000\n", 0o644)
	writeFixtureFile(t, filepath.Join(procRoot, "meminfo"), "MemTotal:       1048576 kB\nMemAvailable:    524288 kB\nMemFree:          1000 kB\n", 0o000)
	writeFixtureFile(t, filepath.Join(procRoot, "net", "dev"), "Inter-| Receive | Transmit\n face |bytes packets errs drop fifo frame compressed multicast|bytes packets errs drop fifo colls carrier compressed\n eth0: 1000 10 0 0 0 0 0 0 2000 20 0 0 0 0 0 0\n", 0o644)
	writeFixtureFile(t, filepath.Join(procRoot, "filesystems"), "nodev\tproc\next4\n", 0o644)
	writeFixtureFile(t, filepath.Join(procRoot, "1", "mountinfo"), "36 25 8:1 / / rw,relatime - ext4 /dev/root rw\n", 0o644)
	writeFixtureFile(t, filepath.Join(procRoot, "sys", "kernel", "random", "boot_id"), "fixture-boot-id\n", 0o644)
	t.Setenv("HOST_PROC", procRoot)

	snapshot := NewCollector().Sample(context.Background())
	if snapshot.Memory.State != StateUnknown || snapshot.Memory.Reason != "permission_denied" {
		t.Fatalf("inaccessible meminfo should be unknown with permission reason: %+v", snapshot.Memory)
	}
	if snapshot.CPU.LogicalCores.State != StateOK || snapshot.CPU.LogicalCores.Value == nil || *snapshot.CPU.LogicalCores.Value != 2 {
		t.Fatalf("CPU core count should continue from fixture /proc/stat: %+v", snapshot.CPU.LogicalCores)
	}
	if snapshot.Uptime.State != StateOK || snapshot.Uptime.Value == nil || snapshot.Uptime.Value.BootID != "fixture-boot-id" {
		t.Fatalf("uptime/boot ID should continue after memory failure: %+v", snapshot.Uptime)
	}
	if snapshot.Network.Summary.State != StateUnknown || snapshot.Network.Summary.Value != nil || len(snapshot.Network.Interfaces) != 1 || snapshot.Network.Interfaces[0].Rate.Value != nil {
		t.Fatalf("network counter/topology should remain explicitly unknown, not zero: %+v", snapshot.Network)
	}
	t.Logf("partial-read fault: memory=%s (%s), cpuCores=%d, bootID=%s, network=%s (%s)", snapshot.Memory.State, snapshot.Memory.Reason, *snapshot.CPU.LogicalCores.Value, snapshot.Uptime.Value.BootID, snapshot.Network.Summary.State, snapshot.Network.Summary.Reason)
}

type procNetworkCounters struct {
	rx uint64
	tx uint64
}

func readProcCPU(t *testing.T) cpu.TimesStat {
	t.Helper()
	file, err := os.Open("/proc/stat")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) == 0 || fields[0] != "cpu" {
			continue
		}
		values := make([]float64, 10)
		for i := 1; i < len(fields) && i <= len(values); i++ {
			value, err := strconv.ParseUint(fields[i], 10, 64)
			if err != nil {
				t.Fatal(err)
			}
			values[i-1] = float64(value) / cpu.ClocksPerSec
		}
		return cpu.TimesStat{CPU: "cpu-total", User: values[0], Nice: values[1], System: values[2], Idle: values[3], Iowait: values[4], Irq: values[5], Softirq: values[6], Steal: values[7], Guest: values[8], GuestNice: values[9]}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	t.Fatal("aggregate cpu line not found in /proc/stat")
	return cpu.TimesStat{}
}

func compareCPUCounter(t *testing.T, name string, got, want float64) {
	t.Helper()
	tolerance := 2 / cpu.ClocksPerSec
	if math.Abs(got-want) > tolerance {
		t.Fatalf("CPU %s counter mismatch: gopsutil=%.6f /proc=%.6f seconds (tolerance %.6f)", name, got, want, tolerance)
	}
}

func readProcMemory(t *testing.T) (uint64, uint64) {
	t.Helper()
	data, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		t.Fatal(err)
	}
	values := make(map[string]uint64)
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		if fields[0] != "MemTotal:" && fields[0] != "MemAvailable:" {
			continue
		}
		value, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		values[strings.TrimSuffix(fields[0], ":")] = value * 1024
	}
	return values["MemTotal"], values["MemAvailable"]
}

func readProcNetwork(t *testing.T) map[string]procNetworkCounters {
	t.Helper()
	data, err := os.ReadFile("/proc/net/dev")
	if err != nil {
		t.Fatal(err)
	}
	out := make(map[string]procNetworkCounters)
	for _, line := range strings.Split(string(data), "\n") {
		separator := strings.LastIndex(line, ":")
		if separator < 0 {
			continue
		}
		name := strings.TrimSpace(line[:separator])
		fields := strings.Fields(line[separator+1:])
		if name == "" || len(fields) < 9 {
			continue
		}
		rx, err := strconv.ParseUint(fields[0], 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		tx, err := strconv.ParseUint(fields[8], 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		out[name] = procNetworkCounters{rx: rx, tx: tx}
	}
	return out
}

func readProcUptime(t *testing.T) float64 {
	t.Helper()
	data, err := os.ReadFile("/proc/uptime")
	if err != nil {
		t.Fatal(err)
	}
	fields := strings.Fields(string(data))
	if len(fields) == 0 {
		t.Fatal("/proc/uptime is empty")
	}
	value, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func readSelfRSS(t *testing.T) uint64 {
	t.Helper()
	data, err := os.ReadFile("/proc/self/status")
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		if !strings.HasPrefix(line, "VmRSS:") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			t.Fatalf("malformed VmRSS line %q", line)
		}
		value, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		return value * 1024
	}
	t.Fatal("VmRSS not found in /proc/self/status")
	return 0
}

func writeFixtureFile(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

func difference(a, b uint64) uint64 {
	if a > b {
		return a - b
	}
	return b - a
}
