package metrics

import (
	"context"
	"errors"
	"os"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/disk"
	"github.com/shirou/gopsutil/v4/mem"
	gnet "github.com/shirou/gopsutil/v4/net"
)

type fixtureSource struct {
	mu sync.Mutex

	cpuSamples     []cpu.TimesStat
	cpuIndex       int
	cpuErr         error
	logicalCPUs    int
	memorySample   *mem.VirtualMemoryStat
	memoryErr      error
	networkSamples [][]gnet.IOCountersStat
	networkIndex   int
	networkErr     error
	interfaces     []gnet.InterfaceStat
	interfacesErr  error
	links          map[string]linkMetadata
	linkErrors     map[string]error
	partitions     []disk.PartitionStat
	partitionsErr  error
	diskUsageFunc  func(context.Context, string) (*disk.UsageStat, error)
	uptimeSeconds  uint64
	uptimeErr      error
	bootIDValue    string
	bootIDErr      error
	bootTimeValue  uint64
	bootTimeErr    error
}

func newFixtureSource() *fixtureSource {
	return &fixtureSource{
		logicalCPUs:  4,
		memorySample: &mem.VirtualMemoryStat{Total: 1000, Available: 400},
		interfaces:   []gnet.InterfaceStat{{Name: "eth0", Flags: []string{"up"}}},
		links:        map[string]linkMetadata{"eth0": {ifindex: 2, iflink: 2}},
		partitions:   []disk.PartitionStat{{Device: "/dev/test", Mountpoint: "/", Fstype: "ext4"}},
		diskUsageFunc: func(context.Context, string) (*disk.UsageStat, error) {
			return &disk.UsageStat{Total: 1000, Free: 300, Used: 700, UsedPercent: 700.0 / 1000.0 * 100}, nil
		},
		uptimeSeconds: 500,
		bootIDValue:   "test-boot-id",
		bootTimeValue: 1_700_000_000,
	}
}

func (s *fixtureSource) hostname() (string, error)                    { return "fixture", nil }
func (s *fixtureSource) architecture(context.Context) (string, error) { return "arm64", nil }
func (s *fixtureSource) platform(context.Context) (string, string, string, error) {
	return "ubuntu", "debian", "24.04", nil
}
func (s *fixtureSource) kernelVersion(context.Context) (string, error) { return "6.1", nil }
func (s *fixtureSource) cpuTimes(context.Context) ([]cpu.TimesStat, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cpuErr != nil {
		return nil, s.cpuErr
	}
	if len(s.cpuSamples) == 0 {
		return []cpu.TimesStat{{CPU: "cpu", User: 10, Idle: 10}}, nil
	}
	index := s.cpuIndex
	if index >= len(s.cpuSamples) {
		index = len(s.cpuSamples) - 1
	}
	s.cpuIndex++
	return []cpu.TimesStat{s.cpuSamples[index]}, nil
}
func (s *fixtureSource) logicalCPUCount(context.Context) (int, error) { return s.logicalCPUs, nil }
func (s *fixtureSource) memory(context.Context) (*mem.VirtualMemoryStat, error) {
	if s.memoryErr != nil {
		return nil, s.memoryErr
	}
	return s.memorySample, nil
}
func (s *fixtureSource) networkCounters(context.Context) ([]gnet.IOCountersStat, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.networkErr != nil {
		return nil, s.networkErr
	}
	if len(s.networkSamples) == 0 {
		return []gnet.IOCountersStat{{Name: "eth0", BytesRecv: 100, BytesSent: 200}}, nil
	}
	index := s.networkIndex
	if index >= len(s.networkSamples) {
		index = len(s.networkSamples) - 1
	}
	s.networkIndex++
	return append([]gnet.IOCountersStat(nil), s.networkSamples[index]...), nil
}
func (s *fixtureSource) networkInterfaces(context.Context) ([]gnet.InterfaceStat, error) {
	return s.interfaces, s.interfacesErr
}
func (s *fixtureSource) linkMetadata(name string) (linkMetadata, error) {
	if err := s.linkErrors[name]; err != nil {
		return linkMetadata{}, err
	}
	link, ok := s.links[name]
	if !ok {
		return linkMetadata{}, errors.New("missing fixture topology")
	}
	return link, nil
}
func (s *fixtureSource) diskPartitions(context.Context) ([]disk.PartitionStat, error) {
	return s.partitions, s.partitionsErr
}
func (s *fixtureSource) diskUsage(ctx context.Context, path string) (*disk.UsageStat, error) {
	return s.diskUsageFunc(ctx, path)
}
func (s *fixtureSource) uptime(context.Context) (uint64, error) { return s.uptimeSeconds, s.uptimeErr }
func (s *fixtureSource) bootID(context.Context) (string, error) { return s.bootIDValue, s.bootIDErr }
func (s *fixtureSource) bootTime(context.Context) (uint64, error) {
	return s.bootTimeValue, s.bootTimeErr
}

func TestCollectorFirstSampleResetAndNetworkSummaryNoDuplication(t *testing.T) {
	source := newFixtureSource()
	source.cpuSamples = []cpu.TimesStat{
		{CPU: "cpu", User: 100, Idle: 300},
		{CPU: "cpu", User: 110, Idle: 310},
		{CPU: "cpu", User: 5, Idle: 5},
	}
	source.interfaces = []gnet.InterfaceStat{
		{Name: "eth0", Flags: []string{"up"}},
		{Name: "veth42", Flags: []string{"up"}},
	}
	source.links["veth42"] = linkMetadata{ifindex: 4, iflink: 9, kind: "veth"}
	source.networkSamples = [][]gnet.IOCountersStat{
		{{Name: "eth0", BytesRecv: 100, BytesSent: 100}, {Name: "veth42", BytesRecv: 1000, BytesSent: 1000}},
		{{Name: "eth0", BytesRecv: 600, BytesSent: 1100}, {Name: "veth42", BytesRecv: 9000, BytesSent: 9000}},
		{{Name: "eth0", BytesRecv: 5, BytesSent: 6}, {Name: "veth42", BytesRecv: 10000, BytesSent: 10000}},
	}
	at := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	collector := newCollector(source, func() time.Time { return at }, time.Second)
	ctx := context.Background()

	first := collector.Sample(ctx)
	if first.CPU.UsagePercent.State != StateUnknown || first.CPU.UsagePercent.Reason != "warming_up" {
		t.Fatalf("first CPU sample = %+v, want unknown warming_up", first.CPU.UsagePercent)
	}
	if first.Network.Summary.State != StateUnknown || first.Network.Summary.Reason != "warming_up" {
		t.Fatalf("first network summary = %+v, want unknown warming_up", first.Network.Summary)
	}
	if first.Uptime.Value == nil || first.Uptime.Value.BootID != "test-boot-id" || first.Uptime.Value.Seconds != 500 {
		t.Fatalf("uptime sample = %+v", first.Uptime)
	}
	if first.Disk.State != StateUnknown || first.Disk.Reason != "not_sampled" {
		t.Fatalf("Sample must not perform blocking disk work: %+v", first.Disk)
	}

	at = at.Add(5 * time.Second)
	second := collector.Sample(ctx)
	if second.CPU.UsagePercent.Value == nil || *second.CPU.UsagePercent.Value != 50 {
		t.Fatalf("second CPU sample = %+v, want 50%%", second.CPU.UsagePercent)
	}
	if second.Network.Summary.Value == nil {
		t.Fatalf("second network summary = %+v", second.Network.Summary)
	}
	if second.Network.Summary.Value.ReceivedBytesPerSecond != 100 || second.Network.Summary.Value.SentBytesPerSecond != 200 {
		t.Fatalf("network summary includes duplicate virtual traffic: %+v", second.Network.Summary.Value)
	}
	if len(second.Network.Interfaces) != 2 || second.Network.Interfaces[1].Name != "veth42" || second.Network.Interfaces[1].IncludedInSummary {
		t.Fatalf("network interface details should retain excluded veth: %+v", second.Network.Interfaces)
	}

	at = at.Add(5 * time.Second)
	third := collector.Sample(ctx)
	if third.CPU.UsagePercent.State != StateUnknown || third.CPU.UsagePercent.Reason != "counter_reset" {
		t.Fatalf("reset CPU sample = %+v", third.CPU.UsagePercent)
	}
	if third.Network.Summary.State != StateUnknown || third.Network.Summary.Reason != "counter_reset" {
		t.Fatalf("reset network sample = %+v", third.Network.Summary)
	}
}

func TestNetworkSummaryCountsBondMasterOnceAndKeepsPerInterfaceRates(t *testing.T) {
	source := newFixtureSource()
	source.interfaces = []gnet.InterfaceStat{
		{Name: "bond0", Flags: []string{"up"}},
		{Name: "eth0", Flags: []string{"up"}},
		{Name: "eth1", Flags: []string{"up"}},
	}
	source.links = map[string]linkMetadata{
		"bond0": {ifindex: 7, iflink: 7, kind: "bond"},
		"eth0":  {ifindex: 2, iflink: 2, master: "bond0"},
		"eth1":  {ifindex: 3, iflink: 3, master: "bond0"},
	}
	source.networkSamples = [][]gnet.IOCountersStat{
		{
			{Name: "bond0", BytesRecv: 100, BytesSent: 200},
			{Name: "eth0", BytesRecv: 1000, BytesSent: 2000},
			{Name: "eth1", BytesRecv: 3000, BytesSent: 4000},
		},
		{
			{Name: "bond0", BytesRecv: 600, BytesSent: 1200},
			{Name: "eth0", BytesRecv: 5000, BytesSent: 10000},
			{Name: "eth1", BytesRecv: 9000, BytesSent: 12000},
		},
	}
	at := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	collector := newCollector(source, func() time.Time { return at }, time.Second)
	ctx := context.Background()
	_ = collector.Sample(ctx)
	at = at.Add(5 * time.Second)
	snapshot := collector.Sample(ctx)

	if snapshot.Network.Summary.Value == nil {
		t.Fatalf("bond summary = %+v", snapshot.Network.Summary)
	}
	if got := snapshot.Network.Summary.Value.ReceivedBytesPerSecond; got != 100 {
		t.Fatalf("bond summary RX = %.1f bytes/s, want master rate 100 without slave duplication", got)
	}
	if got := snapshot.Network.Summary.Value.SentBytesPerSecond; got != 200 {
		t.Fatalf("bond summary TX = %.1f bytes/s, want master rate 200 without slave duplication", got)
	}
	if len(snapshot.Network.Interfaces) != 3 {
		t.Fatalf("network interface details = %d, want 3", len(snapshot.Network.Interfaces))
	}
	for _, item := range snapshot.Network.Interfaces {
		wantIncluded := item.Name == "bond0"
		if item.IncludedInSummary != wantIncluded {
			t.Errorf("interface %s included=%v, want %v", item.Name, item.IncludedInSummary, wantIncluded)
		}
		if item.Rate.State != StateOK || item.Rate.Value == nil {
			t.Errorf("interface %s rate should remain available in details: %+v", item.Name, item.Rate)
		}
	}
}

func TestCollectorOneReadFailureDoesNotEraseOtherMetrics(t *testing.T) {
	source := newFixtureSource()
	source.memoryErr = os.ErrPermission
	collector := newCollector(source, time.Now, time.Second)
	snapshot := collector.Sample(context.Background())
	if snapshot.Memory.State != StateUnknown || snapshot.Memory.Reason != "permission_denied" {
		t.Fatalf("memory status = %+v", snapshot.Memory)
	}
	if snapshot.Uptime.State != StateOK || snapshot.Uptime.Value == nil {
		t.Fatalf("uptime should continue after memory read failure: %+v", snapshot.Uptime)
	}
	if snapshot.System.OS.State != StateOK || snapshot.CPU.LogicalCores.State != StateOK {
		t.Fatalf("independent system/CPU metrics were lost: system=%+v cpu=%+v", snapshot.System, snapshot.CPU)
	}
}

func TestDiskPollerTimeoutHasOneOutstandingScanAndRecovers(t *testing.T) {
	source := newFixtureSource()
	release := make(chan struct{})
	finished := make(chan struct{})
	var calls atomic.Int32
	source.diskUsageFunc = func(context.Context, string) (*disk.UsageStat, error) {
		call := calls.Add(1)
		if call == 1 {
			<-release // Deliberately ignore context, like a blocked Statfs syscall.
			close(finished)
		}
		return &disk.UsageStat{Total: 1000, Free: 300, Used: 700, UsedPercent: 70}, nil
	}
	collector := newCollector(source, time.Now, 30*time.Millisecond)

	first := collector.SampleDisk(context.Background())
	if first.State != StateUnknown || first.Reason != "timeout" {
		t.Fatalf("blocked disk sample = %+v, want timeout", first)
	}
	for i := 0; i < 20; i++ {
		result := collector.SampleDisk(context.Background())
		if result.State != StateUnknown || result.Reason != "worker_busy" {
			t.Fatalf("retry %d = %+v, want worker_busy", i, result)
		}
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("blocked Statfs calls = %d, want one bounded worker call", got)
	}

	close(release)
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("blocked disk fixture did not release")
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		result := collector.SampleDisk(context.Background())
		if result.State == StateOK {
			if calls.Load() != 2 || len(result.Mounts) != 1 || result.Mounts[0].Usage.State != StateOK {
				t.Fatalf("disk recovery = %+v; calls=%d", result, calls.Load())
			}
			return
		}
		if result.Reason != "worker_busy" {
			t.Fatalf("disk recovery result = %+v", result)
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("disk worker did not recover after the blocked operation returned")
}

func TestDiskSnapshotsCopyUsageValuesAcrossReturns(t *testing.T) {
	collector := newCollector(newFixtureSource(), time.Now, time.Second)
	returned := collector.SampleDisk(context.Background())
	if len(returned.Mounts) != 1 || returned.Mounts[0].Usage.Value == nil {
		t.Fatalf("fixture disk sample is unavailable: %+v", returned)
	}
	expected := *returned.Mounts[0].Usage.Value
	externalValue := returned.Mounts[0].Usage.Value

	started := make(chan struct{})
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		close(started)
		for value := uint64(1); value <= 10_000; value++ {
			externalValue.TotalBytes = value
			externalValue.AvailableBytes = value + 1
			externalValue.UsedBytes = value + 2
			externalValue.UsedPercent = float64(value)
			runtime.Gosched()
		}
	}()
	<-started
	for iteration := 0; iteration < 100; iteration++ {
		snapshot := collector.Sample(context.Background())
		if snapshot.Disk.Mounts[0].Usage.Value == nil {
			t.Fatal("collector snapshot lost a known disk usage value")
		}
	}
	<-finished

	stored := collector.Sample(context.Background()).Disk
	if got := *stored.Mounts[0].Usage.Value; got != expected {
		t.Fatalf("mutating SampleDisk's returned value changed the stored snapshot: got=%+v want=%+v", got, expected)
	}
	stored.Mounts[0].Usage.Value.TotalBytes = 0
	stored.Mounts[0].Usage.Value.AvailableBytes = 0
	stored.Mounts[0].Usage.Value.UsedBytes = 0
	stored.Mounts[0].Usage.Value.UsedPercent = 0
	if got := *collector.Sample(context.Background()).Disk.Mounts[0].Usage.Value; got != expected {
		t.Fatalf("mutating Sample's returned value changed the stored snapshot: got=%+v want=%+v", got, expected)
	}
}

func TestDiskPollerSequentialSamplesAndCanceledContextsLeaveNoIdleWorker(t *testing.T) {
	baselineGoroutines := runtime.NumGoroutine()
	for collectorNumber := 0; collectorNumber < 30; collectorNumber++ {
		collector := newCollector(newFixtureSource(), time.Now, time.Second)
		canceled, cancel := context.WithCancel(context.Background())
		cancel()
		if result := collector.SampleDisk(canceled); result.State != StateUnknown || result.Reason != "canceled" {
			t.Fatalf("canceled sample = %+v, want canceled unknown", result)
		}
		for sampleNumber := 0; sampleNumber < 5; sampleNumber++ {
			result := collector.SampleDisk(context.Background())
			if result.State != StateOK || len(result.Mounts) != 1 || result.Mounts[0].Usage.State != StateOK {
				t.Fatalf("collector %d idle sample %d = %+v, want completed scan", collectorNumber, sampleNumber, result)
			}
		}
	}

	deadline := time.Now().Add(time.Second)
	for runtime.NumGoroutine() > baselineGoroutines+2 && time.Now().Before(deadline) {
		runtime.Gosched()
		time.Sleep(time.Millisecond)
	}
	if got := runtime.NumGoroutine(); got > baselineGoroutines+2 {
		t.Fatalf("completed/canceled scans left idle goroutines: before=%d after=%d", baselineGoroutines, got)
	}
}

func TestFastSamplerRunsWhileDiskWorkerIsBlocked(t *testing.T) {
	source := newFixtureSource()
	release := make(chan struct{})
	source.diskUsageFunc = func(context.Context, string) (*disk.UsageStat, error) {
		<-release
		return &disk.UsageStat{Total: 1000, Free: 300, Used: 700, UsedPercent: 70}, nil
	}
	collector := newCollector(source, time.Now, 100*time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	updates := collector.Start(ctx)
	select {
	case snapshot := <-updates:
		if snapshot.CollectedAt.IsZero() || snapshot.Uptime.State != StateOK {
			t.Fatalf("fast sample while disk blocked = %+v", snapshot)
		}
	case <-time.After(time.Second):
		t.Fatal("fast host sampling waited for blocked disk work")
	}
	close(release)
	cancel()
	select {
	case _, open := <-updates:
		for open {
			_, open = <-updates
		}
	case <-time.After(time.Second):
		t.Fatal("collector did not stop after context cancellation")
	}
}

func TestCollectorSamplingIntervalsAreLocked(t *testing.T) {
	if DefaultFastInterval != 5*time.Second {
		t.Fatalf("fast interval = %s, want 5s", DefaultFastInterval)
	}
	if DefaultDiskInterval != 30*time.Second {
		t.Fatalf("disk interval = %s, want 30s", DefaultDiskInterval)
	}
}
