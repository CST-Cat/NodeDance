package metrics

import (
	"context"
	"github.com/CST-Cat/NodeDance/internal/protocol"
	"github.com/shirou/gopsutil/v4/disk"
	"os"
	"path/filepath"
	"time"
)

type diskIOBaseline struct {
	at      time.Time
	devices map[string]disk.IOCountersStat
}

// Sum whole physical/virtual hardware devices, excluding partitions and stacked
// dm/md/loop devices so the same I/O is not counted at multiple block layers.
func (h *hostCollector) collectDiskIO(ctx context.Context, at time.Time) protocol.Metric[protocol.DiskIO] {
	counters, err := disk.IOCountersWithContext(ctx)
	if err != nil {
		h.diskIO = diskIOBaseline{}
		return hostUnknown[protocol.DiskIO]("read_failed", at)
	}
	devices := map[string]disk.IOCountersStat{}
	for name, value := range counters {
		if filepath.Base(name) != name {
			continue
		}
		if _, err := os.Stat(filepath.Join("/sys/block", name, "device")); err == nil {
			devices[name] = value
		}
	}
	result := diskIORate(h.diskIO, diskIOBaseline{at: at, devices: devices})
	h.diskIO = diskIOBaseline{at: at, devices: devices}
	return result
}
func diskIORate(previous, current diskIOBaseline) protocol.Metric[protocol.DiskIO] {
	at := current.at
	if len(current.devices) == 0 {
		return hostUnknown[protocol.DiskIO]("unavailable", at)
	}
	if previous.at.IsZero() || len(previous.devices) != len(current.devices) {
		return hostUnknown[protocol.DiskIO]("warming_up", at)
	}
	seconds := at.Sub(previous.at).Seconds()
	if seconds <= 0 {
		return hostUnknown[protocol.DiskIO]("invalid_interval", at)
	}
	value := protocol.DiskIO{}
	for name, next := range current.devices {
		before, ok := previous.devices[name]
		if !ok {
			return hostUnknown[protocol.DiskIO]("warming_up", at)
		}
		if next.ReadBytes < before.ReadBytes || next.WriteBytes < before.WriteBytes {
			return hostUnknown[protocol.DiskIO]("counter_reset", at)
		}
		value.ReadBytesPerSecond += float64(next.ReadBytes-before.ReadBytes) / seconds
		value.WriteBytesPerSecond += float64(next.WriteBytes-before.WriteBytes) / seconds
	}
	return hostKnown(value, at)
}
