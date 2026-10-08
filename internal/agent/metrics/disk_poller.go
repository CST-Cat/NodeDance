package metrics

import (
	"context"
	"sort"
	"sync/atomic"
	"time"

	"github.com/shirou/gopsutil/v4/disk"
)

type diskPoller struct {
	source  collectorSource
	timeout time.Duration
	now     func() time.Time
	active  atomic.Bool
}

func newDiskPoller(source collectorSource, timeout time.Duration, now func() time.Time) *diskPoller {
	return &diskPoller{source: source, timeout: timeout, now: now}
}

func (p *diskPoller) sample(parent context.Context) DiskSnapshot {
	at := p.now()
	ctx, cancel := context.WithTimeout(parent, p.timeout)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return DiskSnapshot{State: StateUnknown, Reason: reasonFromError(err), SampledAt: at}
	}
	if !p.active.CompareAndSwap(false, true) {
		return DiskSnapshot{State: StateUnknown, Reason: "worker_busy", SampledAt: at}
	}

	result := make(chan DiskSnapshot, 1)
	go func() {
		snapshot := collectDisks(ctx, p.source, at)
		// Release the slot only after all syscalls have returned. There are no
		// parked workers between scans, and a context-ignoring Statfs can leave
		// at most this one active scan behind after the caller times out.
		p.active.Store(false)
		result <- snapshot
	}()

	select {
	case snapshot := <-result:
		return snapshot
	case <-ctx.Done():
		return DiskSnapshot{State: StateUnknown, Reason: reasonFromError(ctx.Err()), SampledAt: at}
	}
}

func collectDisks(ctx context.Context, source collectorSource, at time.Time) DiskSnapshot {
	partitions, err := source.diskPartitions(ctx)
	if err != nil {
		return DiskSnapshot{State: StateUnknown, Reason: reasonFromError(err), SampledAt: at}
	}

	// Keep separately mounted filesystems, while avoiding pseudo filesystems
	// whose capacity is not disk storage. tmpfs and overlay remain visible.
	byMountpoint := make(map[string]disk.PartitionStat, len(partitions))
	for _, partition := range partitions {
		if partition.Mountpoint == "" || pseudoFilesystem(partition.Fstype) {
			continue
		}
		byMountpoint[partition.Mountpoint] = partition
	}
	mountpoints := make([]string, 0, len(byMountpoint))
	for mountpoint := range byMountpoint {
		mountpoints = append(mountpoints, mountpoint)
	}
	sort.Strings(mountpoints)
	if len(mountpoints) == 0 {
		return DiskSnapshot{State: StateUnknown, Reason: "no_mounts", SampledAt: at}
	}

	out := DiskSnapshot{State: StateOK, SampledAt: at, Mounts: make([]DiskMount, 0, len(mountpoints))}
	knownCount := 0
	unknownCount := 0
	for _, mountpoint := range mountpoints {
		partition := byMountpoint[mountpoint]
		item := DiskMount{Device: partition.Device, Mountpoint: mountpoint, Filesystem: partition.Fstype}
		if err := ctx.Err(); err != nil {
			item.Usage = unknown[DiskUsage](reasonFromError(err), at)
		} else {
			usage, usageErr := source.diskUsage(ctx, mountpoint)
			if usageErr != nil {
				item.Usage = unknown[DiskUsage](reasonFromError(usageErr), at)
			} else if ctx.Err() != nil {
				item.Usage = unknown[DiskUsage](reasonFromError(ctx.Err()), at)
			} else if value, reason := diskUsage(usage); reason != "" {
				item.Usage = unknown[DiskUsage](reason, at)
			} else {
				item.Usage = known(value, at)
			}
		}
		if item.Usage.State == StateOK {
			knownCount++
		} else {
			unknownCount++
		}
		out.Mounts = append(out.Mounts, item)
	}
	if knownCount == 0 {
		out.State = StateUnknown
		out.Reason = "all_mounts_unavailable"
	} else if unknownCount > 0 {
		out.State = StatePartial
		out.Reason = "some_mounts_unavailable"
	}
	return out
}

func pseudoFilesystem(name string) bool {
	switch name {
	case "proc", "sysfs", "devtmpfs", "devpts", "cgroup", "cgroup2", "securityfs", "debugfs", "tracefs", "pstore", "configfs", "mqueue", "hugetlbfs", "autofs":
		return true
	default:
		return false
	}
}
