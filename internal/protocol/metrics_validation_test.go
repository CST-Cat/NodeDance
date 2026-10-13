package protocol

import "testing"

func TestNotSampledDiskCannotContainMountValues(t *testing.T) {
	disk := MetricsDisk{Status: MetricUnknown, Reason: "not_sampled", Mounts: []MetricsMount{{Mountpoint: "/", Usage: Metric[DiskUsage]{Status: MetricUnknown, Reason: "not_sampled"}}}}
	if err := validateDisk(disk, false); err == nil {
		t.Fatal("not_sampled disk accepted a value despite unknown state")
	}
	disk.Mounts = nil
	if err := validateDisk(disk, false); err != nil {
		t.Fatal("initial empty disk rejected", err)
	}
}
