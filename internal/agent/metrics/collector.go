package metrics

import (
	"context"
	"errors"
	"sort"
	"sync"
	"time"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/disk"
	"github.com/shirou/gopsutil/v4/mem"
	"github.com/shirou/gopsutil/v4/net"
)

var errInvalidData = errors.New("invalid metric data")

const (
	DefaultFastInterval = 5 * time.Second
	DefaultDiskInterval = 30 * time.Second
	defaultDiskTimeout  = 3 * time.Second
)

type collectorSource interface {
	hostname() (string, error)
	architecture(context.Context) (string, error)
	platform(context.Context) (platform, family, version string, err error)
	kernelVersion(context.Context) (string, error)
	cpuTimes(context.Context) ([]cpu.TimesStat, error)
	logicalCPUCount(context.Context) (int, error)
	memory(context.Context) (*mem.VirtualMemoryStat, error)
	networkCounters(context.Context) ([]net.IOCountersStat, error)
	networkInterfaces(context.Context) ([]net.InterfaceStat, error)
	linkMetadata(string) (linkMetadata, error)
	diskPartitions(context.Context) ([]disk.PartitionStat, error)
	diskUsage(context.Context, string) (*disk.UsageStat, error)
	uptime(context.Context) (uint64, error)
	bootID(context.Context) (string, error)
	bootTime(context.Context) (uint64, error)
}

type Collector struct {
	source collectorSource
	now    func() time.Time

	sampleMu            sync.Mutex
	stateMu             sync.Mutex
	prevCPU             *cpu.TimesStat
	prevNet             map[string]networkCounter
	prevNetSummaryNames []string
	latestDisk          DiskSnapshot

	diskPoller *diskPoller
	startOnce  sync.Once
	updates    chan Snapshot
	done       chan struct{}
}

func NewCollector() *Collector {
	return newCollector(gopsutilSource{}, time.Now, defaultDiskTimeout)
}

func newCollector(source collectorSource, now func() time.Time, diskTimeout time.Duration) *Collector {
	if now == nil {
		now = time.Now
	}
	if diskTimeout <= 0 {
		diskTimeout = defaultDiskTimeout
	}
	c := &Collector{
		source:     source,
		now:        now,
		prevNet:    make(map[string]networkCounter),
		latestDisk: DiskSnapshot{State: StateUnknown, Reason: "not_sampled"},
		done:       make(chan struct{}),
	}
	c.diskPoller = newDiskPoller(source, diskTimeout, now)
	return c
}

// Sample collects the fast host metrics synchronously. Disk sampling is
// intentionally separate; Start schedules it through a single-active-scan
// poller so a stuck statfs call cannot delay CPU, memory, network, or uptime.
func (c *Collector) Sample(ctx context.Context) Snapshot {
	c.sampleMu.Lock()
	defer c.sampleMu.Unlock()

	at := c.now()
	snapshot := Snapshot{CollectedAt: at}
	snapshot.System = c.collectSystem(ctx, at)
	snapshot.CPU = c.collectCPU(ctx, at)
	snapshot.Memory = c.collectMemory(ctx, at)
	snapshot.Network = c.collectNetwork(ctx, at)
	snapshot.Uptime = c.collectUptime(ctx, at)
	c.stateMu.Lock()
	snapshot.Disk = cloneDiskSnapshot(c.latestDisk)
	c.stateMu.Unlock()
	return snapshot
}

// SampleDisk requests one full disk scan. It waits at most the configured
// timeout and returns worker_busy if an earlier scan has not returned. A
// timed-out syscall may finish later, but no retry goroutine is created while
// that scan remains active.
func (c *Collector) SampleDisk(ctx context.Context) DiskSnapshot {
	result := c.diskPoller.sample(ctx)
	c.stateMu.Lock()
	c.latestDisk = cloneDiskSnapshot(result)
	c.stateMu.Unlock()
	return result
}

// Start runs one 5-second fast sampler and one 30-second disk scheduler. The
// returned channel has capacity one and keeps only the newest snapshot, so a
// slow transport consumer cannot block host sampling. Call Start once with the
// Agent lifetime context.
func (c *Collector) Start(ctx context.Context) <-chan Snapshot {
	c.startOnce.Do(func() {
		c.updates = make(chan Snapshot, 1)
		var loops sync.WaitGroup
		loops.Add(2)
		go func() {
			defer loops.Done()
			c.runFast(ctx)
		}()
		go func() {
			defer loops.Done()
			c.runDisk(ctx)
		}()
		go func() {
			loops.Wait()
			close(c.done)
		}()
	})
	return c.updates
}

// Done closes after both periodic sampler loops have exited. A timed-out
// context-ignoring disk syscall may remain isolated inside the single bounded
// disk poller, but no scheduler or fast-sampler goroutine survives shutdown.
func (c *Collector) Done() <-chan struct{} {
	return c.done
}

func (c *Collector) runFast(ctx context.Context) {
	defer close(c.updates)
	ticker := time.NewTicker(DefaultFastInterval)
	defer ticker.Stop()
	for {
		publishLatest(c.updates, c.Sample(ctx))
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (c *Collector) runDisk(ctx context.Context) {
	ticker := time.NewTicker(DefaultDiskInterval)
	defer ticker.Stop()
	for {
		c.SampleDisk(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func publishLatest(out chan Snapshot, snapshot Snapshot) {
	select {
	case out <- snapshot:
		return
	default:
	}
	select {
	case <-out:
	default:
	}
	select {
	case out <- snapshot:
	default:
	}
}

func (c *Collector) collectSystem(ctx context.Context, at time.Time) SystemInfo {
	var out SystemInfo
	hostname, err := c.source.hostname()
	if err != nil {
		out.Hostname = unknown[string](reasonFromError(err), at)
	} else {
		out.Hostname = known(hostname, at)
	}
	out.OS = known("linux", at)
	architecture, err := c.source.architecture(ctx)
	if err != nil || architecture == "" {
		if err == nil {
			err = errInvalidData
		}
		out.Architecture = unknown[string](reasonFromError(err), at)
	} else {
		out.Architecture = known(architecture, at)
	}
	platform, family, version, err := c.source.platform(ctx)
	if err != nil {
		reason := reasonFromError(err)
		out.Platform = unknown[string](reason, at)
		out.PlatformFamily = unknown[string](reason, at)
		out.PlatformVersion = unknown[string](reason, at)
	} else {
		out.Platform = stringMetric(platform, at)
		out.PlatformFamily = stringMetric(family, at)
		out.PlatformVersion = stringMetric(version, at)
	}
	kernelVersion, err := c.source.kernelVersion(ctx)
	if err != nil || kernelVersion == "" {
		if err == nil {
			err = errInvalidData
		}
		out.KernelVersion = unknown[string](reasonFromError(err), at)
	} else {
		out.KernelVersion = known(kernelVersion, at)
	}
	return out
}

func stringMetric(value string, at time.Time) Metric[string] {
	if value == "" {
		return unknown[string]("unavailable", at)
	}
	return known(value, at)
}

func (c *Collector) collectCPU(ctx context.Context, at time.Time) CPUInfo {
	var out CPUInfo
	count, err := c.source.logicalCPUCount(ctx)
	if err != nil || count <= 0 {
		if err == nil {
			err = errInvalidData
		}
		out.LogicalCores = unknown[int](reasonFromError(err), at)
	} else {
		out.LogicalCores = known(count, at)
	}

	times, err := c.source.cpuTimes(ctx)
	if err != nil {
		c.prevCPU = nil
		out.UsagePercent = unknown[float64](reasonFromError(err), at)
		return out
	}
	if len(times) != 1 || (times[0].CPU != "cpu" && times[0].CPU != "cpu-total") {
		c.prevCPU = nil
		out.UsagePercent = unknown[float64]("invalid_data", at)
		return out
	}
	current := times[0]
	if c.prevCPU == nil {
		c.prevCPU = &current
		out.UsagePercent = unknown[float64]("warming_up", at)
		return out
	}
	usage, reason := CPUUsagePercent(*c.prevCPU, current)
	c.prevCPU = &current
	if reason != "" {
		out.UsagePercent = unknown[float64](reason, at)
	} else {
		out.UsagePercent = known(usage, at)
	}
	return out
}

func (c *Collector) collectMemory(ctx context.Context, at time.Time) Metric[Memory] {
	sample, err := c.source.memory(ctx)
	if err != nil {
		return unknown[Memory](reasonFromError(err), at)
	}
	value, reason := memoryUsage(sample)
	if reason != "" {
		return unknown[Memory](reason, at)
	}
	return known(value, at)
}

type networkTopology struct {
	interfaceByName map[string]net.InterfaceStat
	factsByName     map[string]interfaceFacts
	linkByName      map[string]linkMetadata
	linkErrors      map[string]error
	masterKinds     map[string]string
	interfacesErr   error
}

func (c *Collector) readNetworkTopology(ctx context.Context) networkTopology {
	interfaces, interfacesErr := c.source.networkInterfaces(ctx)
	topology := networkTopology{
		interfaceByName: make(map[string]net.InterfaceStat, len(interfaces)),
		factsByName:     make(map[string]interfaceFacts, len(interfaces)),
		linkByName:      make(map[string]linkMetadata, len(interfaces)),
		linkErrors:      make(map[string]error, len(interfaces)),
		masterKinds:     make(map[string]string, len(interfaces)),
		interfacesErr:   interfacesErr,
	}
	for _, iface := range interfaces {
		topology.interfaceByName[iface.Name] = iface
		topology.factsByName[iface.Name] = gopsutilInterfaceFacts(iface)
		link, linkErr := c.source.linkMetadata(iface.Name)
		topology.linkByName[iface.Name] = link
		topology.linkErrors[iface.Name] = linkErr
	}
	for _, link := range topology.linkByName {
		if link.master == "" {
			continue
		}
		if master, ok := topology.linkByName[link.master]; ok {
			topology.masterKinds[link.master] = master.kind
		}
	}
	return topology
}

func (t networkTopology) identity(name string) (string, string) {
	iface, ok := t.interfaceByName[name]
	if !ok {
		return "", "interface_identity_unavailable"
	}
	return stableInterfaceIdentity(iface, t.linkByName[name])
}

func identityAcrossSample(before, after networkTopology, name string) (string, string, bool) {
	beforeID, beforeReason := before.identity(name)
	afterID, afterReason := after.identity(name)
	if beforeReason == "" && afterReason == "" {
		if beforeID != afterID {
			return "", "interface_changed_during_sample", false
		}
		return afterID, "", true
	}
	if (beforeReason == "") != (afterReason == "") {
		return "", "interface_changed_during_sample", false
	}
	if beforeReason == "interface_identity_mismatch" || afterReason == "interface_identity_mismatch" {
		return "", "interface_identity_mismatch", false
	}
	return "", "interface_identity_unavailable", false
}

func (c *Collector) collectNetwork(ctx context.Context, at time.Time) NetworkSnapshot {
	before := c.readNetworkTopology(ctx)
	counters, err := c.source.networkCounters(ctx)
	if err != nil {
		c.prevNet = make(map[string]networkCounter)
		c.prevNetSummaryNames = nil
		return NetworkSnapshot{Summary: unknown[NetworkRate](reasonFromError(err), at)}
	}
	after := c.readNetworkTopology(ctx)

	sort.Slice(counters, func(i, j int) bool { return counters[i].Name < counters[j].Name })
	current := make(map[string]networkCounter, len(counters))
	result := NetworkSnapshot{Interfaces: make([]NetworkInterface, 0, len(counters))}
	eligibleNames := make([]string, 0, len(counters))
	classificationKnown := after.interfacesErr == nil
	for _, counter := range counters {
		identity, identityReason, identityStable := identityAcrossSample(before, after, counter.Name)
		facts, factsOK := after.factsByName[counter.Name]
		link, linkOK := after.linkByName[counter.Name]
		linkErr := after.linkErrors[counter.Name]
		if !linkOK && linkErr == nil {
			linkErr = errInvalidData
		}
		included, summaryReason, classifiable := classifyInterface(counter.Name, facts, factsOK, link, linkErr, after.masterKinds[link.master])
		if !classifiable {
			classificationKnown = false
		}
		item := NetworkInterface{Name: counter.Name, IncludedInSummary: included, SummaryReason: summaryReason}
		if factsOK {
			up := facts.up
			item.Up = &up
		}
		if !identityStable {
			item.Rate = unknown[NetworkRate](identityReason, at)
		} else {
			current[counter.Name] = networkCounter{
				rx: counter.BytesRecv, tx: counter.BytesSent, at: at, identity: identity,
			}
			if previous, ok := c.prevNet[counter.Name]; !ok {
				item.Rate = unknown[NetworkRate]("warming_up", at)
			} else if previous.identity != identity {
				item.Rate = unknown[NetworkRate]("interface_changed", at)
			} else {
				elapsed := at.Sub(previous.at).Seconds()
				rate, reason := networkRate(previous.rx, previous.tx, counter.BytesRecv, counter.BytesSent, elapsed)
				if reason != "" {
					item.Rate = unknown[NetworkRate](reason, at)
				} else {
					item.Rate = known(rate, at)
				}
			}
		}
		if included {
			eligibleNames = append(eligibleNames, counter.Name)
		}
		result.Interfaces = append(result.Interfaces, item)
	}

	if after.interfacesErr != nil {
		result.Summary = unknown[NetworkRate](reasonFromError(after.interfacesErr), at)
	} else if !classificationKnown {
		result.Summary = unknown[NetworkRate]("topology_unavailable", at)
	} else if len(eligibleNames) == 0 {
		result.Summary = unknown[NetworkRate]("no_eligible_interfaces", at)
	} else if c.prevNetSummaryNames == nil {
		result.Summary = unknown[NetworkRate]("warming_up", at)
	} else if !sameStrings(c.prevNetSummaryNames, eligibleNames) {
		result.Summary = unknown[NetworkRate]("interface_set_changed", at)
	} else {
		summary := NetworkRate{}
		valid := true
		for _, item := range result.Interfaces {
			if !item.IncludedInSummary {
				continue
			}
			if item.Rate.State != StateOK || item.Rate.Value == nil {
				valid = false
				if result.Summary.Reason == "" {
					result.Summary.Reason = item.Rate.Reason
				}
				continue
			}
			summary.ReceivedBytesPerSecond += item.Rate.Value.ReceivedBytesPerSecond
			summary.SentBytesPerSecond += item.Rate.Value.SentBytesPerSecond
		}
		if !valid {
			if result.Summary.Reason == "" {
				result.Summary.Reason = "warming_up"
			}
			result.Summary.State = StateUnknown
			result.Summary.SampledAt = at
		} else if !finite(summary.ReceivedBytesPerSecond) || !finite(summary.SentBytesPerSecond) {
			result.Summary = unknown[NetworkRate]("invalid_data", at)
		} else {
			result.Summary = known(summary, at)
		}
	}

	c.prevNet = current
	if classificationKnown && after.interfacesErr == nil {
		c.prevNetSummaryNames = append([]string(nil), eligibleNames...)
	} else {
		c.prevNetSummaryNames = nil
	}
	return result
}

func (c *Collector) collectUptime(ctx context.Context, at time.Time) Metric[Uptime] {
	seconds, err := c.source.uptime(ctx)
	if err != nil {
		return unknown[Uptime](reasonFromError(err), at)
	}
	bootID, err := c.source.bootID(ctx)
	if err != nil || bootID == "" {
		if err == nil {
			err = errInvalidData
		}
		return unknown[Uptime](reasonFromError(err), at)
	}
	value := Uptime{Seconds: seconds, BootID: bootID}
	if bootTime, bootErr := c.source.bootTime(ctx); bootErr == nil {
		value.BootTimeUnix = &bootTime
	}
	return known(value, at)
}

func sameStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

func cloneDiskSnapshot(in DiskSnapshot) DiskSnapshot {
	out := in
	out.Mounts = append([]DiskMount(nil), in.Mounts...)
	for index := range out.Mounts {
		if value := out.Mounts[index].Usage.Value; value != nil {
			usageCopy := *value
			out.Mounts[index].Usage.Value = &usageCopy
		}
	}
	return out
}
