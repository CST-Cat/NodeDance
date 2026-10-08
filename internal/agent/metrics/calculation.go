package metrics

import (
	"context"
	"errors"
	"io/fs"
	"math"
	"os"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/disk"
	"github.com/shirou/gopsutil/v4/mem"
)

// CPUUsagePercent calculates aggregate Linux CPU utilization from cumulative
// /proc/stat counters. Linux guest and guestNice are already included in user
// and nice, so they are checked for resets but never added to the denominator.
// Idle and iowait are both excluded from busy time.
func CPUUsagePercent(previous, current cpu.TimesStat) (float64, string) {
	if previous.CPU != current.CPU {
		return 0, "cpu_set_changed"
	}
	old := []float64{previous.User, previous.Nice, previous.System, previous.Idle, previous.Iowait, previous.Irq, previous.Softirq, previous.Steal, previous.Guest, previous.GuestNice}
	newer := []float64{current.User, current.Nice, current.System, current.Idle, current.Iowait, current.Irq, current.Softirq, current.Steal, current.Guest, current.GuestNice}
	delta := make([]float64, len(old))
	for i := range old {
		if !finite(old[i]) || !finite(newer[i]) || old[i] < 0 || newer[i] < 0 {
			return 0, "invalid_data"
		}
		if newer[i] < old[i] {
			return 0, "counter_reset"
		}
		delta[i] = newer[i] - old[i]
	}
	// user + nice already contain guest + guestNice on Linux.
	total := delta[0] + delta[1] + delta[2] + delta[3] + delta[4] + delta[5] + delta[6] + delta[7]
	if total <= 0 || !finite(total) {
		return 0, "no_counter_progress"
	}
	busy := total - delta[3] - delta[4]
	if busy < 0 || !finite(busy) {
		return 0, "invalid_data"
	}
	percent := 100 * busy / total
	if !finite(percent) {
		return 0, "invalid_data"
	}
	return math.Max(0, math.Min(100, percent)), ""
}

func memoryUsage(sample *mem.VirtualMemoryStat) (Memory, string) {
	if sample == nil || sample.Total == 0 {
		return Memory{}, "invalid_data"
	}
	if sample.Available > sample.Total {
		return Memory{}, "invalid_data"
	}
	used := sample.Total - sample.Available
	return Memory{
		TotalBytes:     sample.Total,
		AvailableBytes: sample.Available,
		UsedBytes:      used,
		UsedPercent:    100 * float64(used) / float64(sample.Total),
	}, ""
}

func diskUsage(sample *disk.UsageStat) (DiskUsage, string) {
	if sample == nil || sample.Total == 0 || sample.Used+sample.Free == 0 {
		return DiskUsage{}, "invalid_data"
	}
	if !finite(sample.UsedPercent) || sample.UsedPercent < 0 || sample.UsedPercent > 100 {
		return DiskUsage{}, "invalid_data"
	}
	return DiskUsage{
		TotalBytes:     sample.Total,
		AvailableBytes: sample.Free,
		UsedBytes:      sample.Used,
		UsedPercent:    sample.UsedPercent,
	}, ""
}

func networkRate(previousRx, previousTx, currentRx, currentTx uint64, elapsedSeconds float64) (NetworkRate, string) {
	if !finite(elapsedSeconds) || elapsedSeconds <= 0 {
		return NetworkRate{}, "invalid_interval"
	}
	if currentRx < previousRx || currentTx < previousTx {
		return NetworkRate{}, "counter_reset"
	}
	return NetworkRate{
		ReceivedBytesPerSecond: float64(currentRx-previousRx) / elapsedSeconds,
		SentBytesPerSecond:     float64(currentTx-previousTx) / elapsedSeconds,
	}, ""
}

func reasonFromError(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, fs.ErrPermission) || errors.Is(err, os.ErrPermission) {
		return "permission_denied"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	if errors.Is(err, context.Canceled) {
		return "canceled"
	}
	if errors.Is(err, fs.ErrNotExist) || errors.Is(err, os.ErrNotExist) {
		return "unavailable"
	}
	return "read_failed"
}

func finite(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0)
}
