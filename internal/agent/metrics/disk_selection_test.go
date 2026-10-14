package metrics

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/CST-Cat/NodeDance/internal/protocol"
	"github.com/shirou/gopsutil/v4/disk"
)

type storageSource struct {
	gopsutilSource
	partitions []disk.PartitionStat
	visited    []string
	fail       string
}

func (s *storageSource) diskPartitions(context.Context) ([]disk.PartitionStat, error) {
	return s.partitions, nil
}
func (s *storageSource) diskUsage(_ context.Context, mount string) (*disk.UsageStat, error) {
	s.visited = append(s.visited, mount)
	if mount == s.fail {
		return nil, errors.New("disk unavailable")
	}
	return &disk.UsageStat{Total: 100, Used: 40, Free: 60, UsedPercent: 40}, nil
}
func TestDefaultDisksExcludeRuntimeBeforeReading(t *testing.T) {
	s := &storageSource{partitions: []disk.PartitionStat{
		{Mountpoint: "/", Fstype: "ext4"}, {Mountpoint: "/data", Fstype: "xfs"}, {Mountpoint: "/archive", Fstype: "nfs"},
		{Mountpoint: "/boot", Fstype: "ext4"}, {Mountpoint: "/boot/efi", Fstype: "vfat"},
		{Mountpoint: "/dev/shm", Fstype: "tmpfs"}, {Mountpoint: "/run", Fstype: "tmpfs"},
		{Mountpoint: "/run/docker/netns/fixture", Fstype: "nsfs"}, {Mountpoint: "/proc/sys/fs/binfmt_misc", Fstype: "binfmt_misc"},
		{Mountpoint: "/var/lib/docker/overlay2/fixture/merged", Fstype: "overlay"}, {Mountpoint: "/snap/fixture", Fstype: "squashfs"},
	}}
	settings := protocol.DefaultAgentSettings()
	source := configuredDiskSource{collectorSource: s, settings: func() protocol.AgentSettings { return settings }}
	snapshot := collectDisks(context.Background(), source, time.Now())
	if snapshot.State != StateOK || !reflect.DeepEqual(s.visited, []string{"/", "/archive", "/boot", "/data"}) {
		t.Fatalf("unexpected scan: %+v %v", snapshot, s.visited)
	}
	s.visited = nil
	s.fail = "/data"
	if got := collectDisks(context.Background(), source, time.Now()); got.State != StatePartial || got.Reason != "some_mounts_unavailable" {
		t.Fatalf("real storage errors hidden: %+v", got)
	}
	s.visited = nil
	settings.IncludeMountpoints = []string{"/boot/efi", "/dev/shm", "/run/docker/netns/fixture"}
	collectDisks(context.Background(), source, time.Now())
	if !reflect.DeepEqual(s.visited, []string{"/boot/efi", "/dev/shm"}) {
		t.Fatalf("explicit selection lost or pseudo mount read: %v", s.visited)
	}
	if !defaultStorageMount(disk.PartitionStat{Mountpoint: "/", Fstype: "overlay"}) {
		t.Fatal("container root disappeared")
	}
}
