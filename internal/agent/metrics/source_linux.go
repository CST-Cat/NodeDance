package metrics

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/disk"
	"github.com/shirou/gopsutil/v4/host"
	"github.com/shirou/gopsutil/v4/mem"
	"github.com/shirou/gopsutil/v4/net"
)

type gopsutilSource struct{}

func (gopsutilSource) hostname() (string, error) {
	return os.Hostname()
}

func (gopsutilSource) architecture(context.Context) (string, error) {
	return host.KernelArch()
}

func (gopsutilSource) platform(ctx context.Context) (string, string, string, error) {
	return host.PlatformInformationWithContext(ctx)
}

func (gopsutilSource) kernelVersion(ctx context.Context) (string, error) {
	return host.KernelVersionWithContext(ctx)
}

func (gopsutilSource) cpuTimes(ctx context.Context) ([]cpu.TimesStat, error) {
	return cpu.TimesWithContext(ctx, false)
}

func (gopsutilSource) logicalCPUCount(ctx context.Context) (int, error) {
	return cpu.CountsWithContext(ctx, true)
}

func (gopsutilSource) memory(ctx context.Context) (*mem.VirtualMemoryStat, error) {
	return mem.VirtualMemoryWithContext(ctx)
}

func (gopsutilSource) networkCounters(ctx context.Context) ([]net.IOCountersStat, error) {
	return net.IOCountersWithContext(ctx, true)
}

func (gopsutilSource) networkInterfaces(ctx context.Context) ([]net.InterfaceStat, error) {
	return net.InterfacesWithContext(ctx)
}

func (gopsutilSource) linkMetadata(name string) (linkMetadata, error) {
	root := os.Getenv("HOST_SYS")
	if root == "" {
		root = "/sys"
	}
	path := filepath.Join(root, "class", "net", name)
	ifindex, err := readPositiveInt(filepath.Join(path, "ifindex"))
	if err != nil {
		return linkMetadata{}, err
	}
	iflink, err := readPositiveInt(filepath.Join(path, "iflink"))
	if err != nil {
		return linkMetadata{}, err
	}
	meta := linkMetadata{ifindex: ifindex, iflink: iflink}
	if masterPath, err := os.Readlink(filepath.Join(path, "master")); err == nil {
		meta.master = filepath.Base(masterPath)
	} else if !os.IsNotExist(err) {
		return linkMetadata{}, err
	}
	bridge, err := markerDirectory(filepath.Join(path, "bridge"))
	if err != nil {
		return linkMetadata{}, err
	}
	bond, err := markerDirectory(filepath.Join(path, "bonding"))
	if err != nil {
		return linkMetadata{}, err
	}
	team, err := markerDirectory(filepath.Join(path, "team"))
	if err != nil {
		return linkMetadata{}, err
	}
	tun, err := markerFile(filepath.Join(path, "tun_flags"))
	if err != nil {
		return linkMetadata{}, err
	}
	meta.kind = linkKindForPath(name, ifindex, iflink, bridge, bond || team, tun)
	entries, err := os.ReadDir(path)
	if err != nil {
		return linkMetadata{}, err
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "lower_") {
			meta.lowerLinks = append(meta.lowerLinks, strings.TrimPrefix(entry.Name(), "lower_"))
		}
		if strings.HasPrefix(entry.Name(), "upper_") {
			meta.upperLinks = append(meta.upperLinks, strings.TrimPrefix(entry.Name(), "upper_"))
		}
	}
	// ReadDir orders entries lexically, so topology slices are deterministic.
	return meta, nil
}

func (gopsutilSource) diskPartitions(ctx context.Context) ([]disk.PartitionStat, error) {
	return disk.PartitionsWithContext(ctx, true)
}

func (gopsutilSource) diskUsage(ctx context.Context, path string) (*disk.UsageStat, error) {
	return disk.UsageWithContext(ctx, path)
}

func (gopsutilSource) uptime(ctx context.Context) (uint64, error) {
	return host.UptimeWithContext(ctx)
}

func (gopsutilSource) bootID(context.Context) (string, error) {
	root := os.Getenv("HOST_PROC")
	if root == "" {
		root = "/proc"
	}
	data, err := os.ReadFile(filepath.Join(root, "sys", "kernel", "random", "boot_id"))
	if err != nil {
		return "", err
	}
	id := strings.TrimSpace(string(data))
	if id == "" {
		return "", errInvalidData
	}
	return id, nil
}

func (gopsutilSource) bootTime(ctx context.Context) (uint64, error) {
	return host.BootTimeWithContext(ctx)
}

func readPositiveInt(path string) (int, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	value, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || value <= 0 {
		return 0, errInvalidData
	}
	return value, nil
}

func markerDirectory(path string) (bool, error) {
	info, err := os.Stat(path)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return info.IsDir(), nil
}

func markerFile(path string) (bool, error) {
	_, err := os.Stat(path)
	if os.IsNotExist(err) {
		return false, nil
	}
	return err == nil, err
}
