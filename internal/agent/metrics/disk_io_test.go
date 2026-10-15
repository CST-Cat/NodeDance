package metrics

import (
	"github.com/CST-Cat/NodeDance/internal/protocol"
	"github.com/shirou/gopsutil/v4/disk"
	"testing"
	"time"
)

func TestDiskIORatesWarmResetAndDeviceChange(t *testing.T) {
	at := time.Now()
	base := diskIOBaseline{at: at, devices: map[string]disk.IOCountersStat{"vda": {ReadBytes: 100, WriteBytes: 200}}}
	next := diskIOBaseline{at: at.Add(5 * time.Second), devices: map[string]disk.IOCountersStat{"vda": {ReadBytes: 600, WriteBytes: 450}}}
	rate := diskIORate(base, next)
	if rate.Status != protocol.MetricKnown || rate.Value.ReadBytesPerSecond != 100 || rate.Value.WriteBytesPerSecond != 50 {
		t.Fatal(rate)
	}
	if diskIORate(diskIOBaseline{}, next).Reason != "warming_up" {
		t.Fatal("initial sample is not a zero rate")
	}
	if diskIORate(next, base).Reason != "invalid_interval" {
		t.Fatal("non-increasing time accepted")
	}
	reset := diskIOBaseline{at: next.at.Add(time.Second), devices: base.devices}
	if diskIORate(next, reset).Reason != "counter_reset" {
		t.Fatal("counter reset was graphed as a rate")
	}
	changed := diskIOBaseline{at: next.at, devices: map[string]disk.IOCountersStat{"vdb": {}}}
	if diskIORate(base, changed).Reason != "warming_up" {
		t.Fatal("different devices bridged")
	}
}
