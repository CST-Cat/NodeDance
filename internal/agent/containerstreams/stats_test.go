package containerstreams

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const statsTestID = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func TestCalculateDockerStatsFormulasAndFirstSampleUnknown(t *testing.T) {
	firstAt := time.Date(2026, 10, 8, 10, 0, 0, 0, time.UTC)
	secondAt := firstAt.Add(3 * time.Second)
	first := statsSample(firstAt)
	first.MemoryStats = memorySample(1200, 2000, map[string]uint64{"inactive_file": 200})
	first.Networks = networkSample(100, 200)
	first.BlkioStats = blockIOSample(100, 200)

	firstSnapshot := calculateStats(statsTestID, first, nil)
	assertMetricUnknown(t, firstSnapshot.CPUPercent, "warming_up")
	assertMetricUnknown(t, firstSnapshot.Network, "warming_up")
	assertMetricUnknown(t, firstSnapshot.BlockIO, "warming_up")
	if firstSnapshot.Memory.State != MetricOK || firstSnapshot.Memory.Value == nil || firstSnapshot.Memory.Value.UsedBytes != 1000 || firstSnapshot.Memory.Value.UsedPercent != 50 {
		t.Fatalf("first sample memory = %+v, want 1000 bytes / 50%%", firstSnapshot.Memory)
	}

	second := statsSample(secondAt)
	second.PreRead = timePointer(firstAt)
	second.CPUStats = &rawCPUStats{CPUUsage: &rawCPUUsage{TotalUsage: uintPointer(150), PerCPU: []uint64{100, 50}},
		SystemUsage: uintPointer(1100), OnlineCPUs: uint32Pointer(2)}
	second.PreCPUStats = &rawCPUStats{CPUUsage: &rawCPUUsage{TotalUsage: uintPointer(100)}, SystemUsage: uintPointer(1000)}
	second.MemoryStats = memorySample(1500, 3000, map[string]uint64{"total_inactive_file": 300})
	second.Networks = networkSample(160, 260)
	second.BlkioStats = blockIOSample(400, 500)

	snapshot := calculateStats(statsTestID, second, &first)
	if snapshot.CPUPercent.State != MetricOK || snapshot.CPUPercent.Value == nil || *snapshot.CPUPercent.Value != 100 {
		t.Fatalf("CPU = %+v, want 100%% from (50/100)*2*100", snapshot.CPUPercent)
	}
	if snapshot.Memory.State != MetricOK || snapshot.Memory.Value == nil || snapshot.Memory.Value.UsedBytes != 1200 || snapshot.Memory.Value.UsedPercent != 40 {
		t.Fatalf("memory = %+v, want (1500-300)/3000 = 40%%", snapshot.Memory)
	}
	if snapshot.Network.State != MetricOK || snapshot.Network.Value == nil || snapshot.Network.Value.ReceivedBytesPerSecond != 20 || snapshot.Network.Value.SentBytesPerSecond != 20 {
		t.Fatalf("network = %+v, want 20B/s in each direction", snapshot.Network)
	}
	if snapshot.BlockIO.State != MetricOK || snapshot.BlockIO.Value == nil || snapshot.BlockIO.Value.ReadBytesPerSecond != 100 || snapshot.BlockIO.Value.WriteBytesPerSecond != 100 {
		t.Fatalf("block IO = %+v, want 100B/s read and write", snapshot.BlockIO)
	}
}

func TestStatsUnknownForMissingDataAndZeroIsNotUsedAsUnknown(t *testing.T) {
	at := time.Date(2026, 10, 8, 10, 0, 0, 0, time.UTC)
	missing := calculateStats(statsTestID, rawDockerStats{Read: timePointer(at)}, nil)
	assertMetricUnknown(t, missing.CPUPercent, "warming_up")
	assertMetricUnknown(t, missing.Memory, "unavailable")
	assertMetricUnknown(t, missing.Network, "unavailable")
	assertMetricUnknown(t, missing.BlockIO, "unavailable")

	zero := rawDockerStats{Read: timePointer(at), MemoryStats: memorySample(0, 1000, map[string]uint64{"inactive_file": 0})}
	zeroMemory := calculateMemory(zero, at)
	if zeroMemory.State != MetricOK || zeroMemory.Value == nil || zeroMemory.Value.UsedBytes != 0 || zeroMemory.Value.UsedPercent != 0 {
		t.Fatalf("explicit measured zero memory must remain known: %+v", zeroMemory)
	}
	missingCache := rawDockerStats{Read: timePointer(at), MemoryStats: memorySample(100, 1000, map[string]uint64{})}
	assertMetricUnknown(t, calculateMemory(missingCache, at), "cache_unavailable")
	noLimit := rawDockerStats{Read: timePointer(at), MemoryStats: memorySample(100, 0, map[string]uint64{"cache": 0})}
	assertMetricUnknown(t, calculateMemory(noLimit, at), "limit_unavailable")
	missingTime := rawDockerStats{MemoryStats: memorySample(100, 1000, map[string]uint64{"cache": 0})}
	assertMetricUnknown(t, calculateMemory(missingTime, time.Time{}), "sample_time_unavailable")
}

func TestStatsCounterResetAndInterfaceChangesBecomeUnknown(t *testing.T) {
	firstAt := time.Date(2026, 10, 8, 10, 0, 0, 0, time.UTC)
	secondAt := firstAt.Add(time.Second)
	first := statsSample(firstAt)
	first.Networks = networkSample(100, 200)
	first.BlkioStats = blockIOSample(100, 200)
	second := statsSample(secondAt)
	second.Networks = networkSample(90, 220)
	second.BlkioStats = blockIOSample(90, 220)
	assertMetricUnknown(t, calculateNetwork(second, &first, secondAt), "counter_reset")
	assertMetricUnknown(t, calculateBlockIO(second, &first, secondAt), "counter_reset")

	second.Networks = map[string]rawNet{"eth1": {RxBytes: uintPointer(300), TxBytes: uintPointer(400)}}
	assertMetricUnknown(t, calculateNetwork(second, &first, secondAt), "interface_set_changed")

	badCPU := statsSample(secondAt)
	badCPU.PreRead = timePointer(firstAt)
	badCPU.CPUStats = &rawCPUStats{CPUUsage: &rawCPUUsage{TotalUsage: uintPointer(5)}, SystemUsage: uintPointer(1100), OnlineCPUs: uint32Pointer(1)}
	badCPU.PreCPUStats = &rawCPUStats{CPUUsage: &rawCPUUsage{TotalUsage: uintPointer(10)}, SystemUsage: uintPointer(1000)}
	assertMetricUnknown(t, calculateCPU(badCPU, secondAt), "counter_reset")
}

func TestStatsRejectCounterOverflowAndInvalidIntervals(t *testing.T) {
	at := time.Date(2026, 10, 8, 10, 0, 0, 0, time.UTC)
	previous := statsSample(at)
	previous.Networks = map[string]rawNet{
		"eth0": {RxBytes: uintPointer(0), TxBytes: uintPointer(0)},
		"eth1": {RxBytes: uintPointer(0), TxBytes: uintPointer(0)},
	}
	current := statsSample(at.Add(time.Second))
	current.Networks = map[string]rawNet{
		"eth0": {RxBytes: uintPointer(math.MaxUint64), TxBytes: uintPointer(0)},
		"eth1": {RxBytes: uintPointer(1), TxBytes: uintPointer(0)},
	}
	assertMetricUnknown(t, calculateNetwork(current, &previous, current.Read.UTC()), "invalid_data")
	current.Networks = previous.Networks
	current.Read = timePointer(at)
	assertMetricUnknown(t, calculateNetwork(current, &previous, at), "invalid_interval")
	entries := []rawBlockIOEntry{{Major: 8, Minor: 0, Op: "Read", Value: math.MaxUint64},
		{Major: 8, Minor: 0, Op: "Read", Value: 1}}
	if _, reason := blockIOCounters(&rawBlockIOStats{ServiceBytes: &entries}); reason != "invalid_data" {
		t.Fatalf("overflowing block IO counters reason=%q, want invalid_data", reason)
	}
	totalOnly := []rawBlockIOEntry{{Major: 8, Minor: 0, Op: "Total", Value: 100}}
	if _, reason := blockIOCounters(&rawBlockIOStats{ServiceBytes: &totalOnly}); reason != "unavailable" {
		t.Fatalf("Total-only block IO cannot establish read/write values: reason=%q", reason)
	}

	extremePrevious := statsSample(time.Date(2, time.January, 1, 0, 0, 0, 0, time.UTC))
	extremePrevious.Networks = networkSample(0, 0)
	extremePrevious.BlkioStats = blockIOSample(0, 0)
	extremeCurrent := statsSample(time.Date(9999, time.December, 31, 23, 59, 59, 0, time.UTC))
	extremeCurrent.Networks = networkSample(1, 1)
	extremeCurrent.BlkioStats = blockIOSample(1, 1)
	assertMetricUnknown(t, calculateNetwork(extremeCurrent, &extremePrevious, *extremeCurrent.Read), "clock_jump")
	assertMetricUnknown(t, calculateBlockIO(extremeCurrent, &extremePrevious, *extremeCurrent.Read), "clock_jump")
}

func TestStatsRateClockJumpBecomesUnknownThenRecovers(t *testing.T) {
	dockerBase := time.Date(2026, 10, 8, 10, 0, 0, 0, time.UTC)
	monotonicBase := time.Now()
	first := rateSample(dockerBase, monotonicBase, 100, 200, 300, 400)

	for _, test := range []struct {
		name string
		read time.Time
	}{
		{name: "forward one hour", read: dockerBase.Add(time.Hour)},
		{name: "backward one hour", read: dockerBase.Add(-time.Hour)},
	} {
		t.Run(test.name, func(t *testing.T) {
			jump := rateSample(test.read, monotonicBase.Add(time.Second), 200, 300, 400, 500)
			assertMetricUnknown(t, calculateNetwork(jump, &first, test.read), "clock_jump")
			assertMetricUnknown(t, calculateBlockIO(jump, &first, test.read), "clock_jump")

			// The anomalous sample becomes the new baseline. A following valid
			// Docker interval resumes normal rates instead of retaining stale data.
			recovery := rateSample(test.read.Add(time.Second), monotonicBase.Add(2*time.Second), 300, 400, 500, 600)
			network := calculateNetwork(recovery, &jump, *recovery.Read)
			blockIO := calculateBlockIO(recovery, &jump, *recovery.Read)
			if network.State != MetricOK || network.Value == nil || network.Value.ReceivedBytesPerSecond != 100 || network.Value.SentBytesPerSecond != 100 {
				t.Fatalf("network did not recover after %s: %+v", test.name, network)
			}
			if blockIO.State != MetricOK || blockIO.Value == nil || blockIO.Value.ReadBytesPerSecond != 100 || blockIO.Value.WriteBytesPerSecond != 100 {
				t.Fatalf("block I/O did not recover after %s: %+v", test.name, blockIO)
			}
		})
	}
}

func TestStatsRatesUseDockerIntervalForBufferedSamples(t *testing.T) {
	dockerBase := time.Date(2026, 10, 8, 10, 0, 0, 0, time.UTC)
	monotonicBase := time.Now()
	first := rateSample(dockerBase, monotonicBase, 0, 0, 0, 0)
	second := rateSample(dockerBase.Add(time.Second), monotonicBase.Add(time.Millisecond), 100, 100, 100, 100)
	third := rateSample(dockerBase.Add(2*time.Second), monotonicBase.Add(2*time.Millisecond), 200, 200, 200, 200)

	for _, pair := range [][2]rawDockerStats{{second, first}, {third, second}} {
		network := calculateNetwork(pair[0], &pair[1], *pair[0].Read)
		blockIO := calculateBlockIO(pair[0], &pair[1], *pair[0].Read)
		if network.State != MetricOK || network.Value == nil || network.Value.ReceivedBytesPerSecond != 100 || network.Value.SentBytesPerSecond != 100 {
			t.Fatalf("buffered network rate used receipt interval or became unknown: %+v", network)
		}
		if blockIO.State != MetricOK || blockIO.Value == nil || blockIO.Value.ReadBytesPerSecond != 100 || blockIO.Value.WriteBytesPerSecond != 100 {
			t.Fatalf("buffered block I/O rate used receipt interval or became unknown: %+v", blockIO)
		}
	}
}

func TestStatsStreamUsesDockerSampleIntervalAfterBufferedDelivery(t *testing.T) {
	engine := newPipeStatsEngine()
	manager, err := NewStatsManager(engine, StatsManagerOptions{CloseTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	sub, err := manager.Subscribe(context.Background(), statsTestID)
	if err != nil {
		t.Fatal(err)
	}
	stream := waitPipeStream(t, engine)
	base := time.Date(2026, 10, 8, 10, 0, 0, 0, time.UTC)
	var buffered []byte
	for i := uint64(0); i < 3; i++ {
		buffered = append(buffered, encodeStatsSample(rateSample(base.Add(time.Duration(i)*time.Second), time.Time{}, 100*i, 100*i, 100*i, 100*i))...)
	}
	if _, err := stream.writer.Write(buffered); err != nil {
		t.Fatal(err)
	}
	snapshot := waitLatestSample(t, sub, base.Add(2*time.Second))
	if snapshot.Network.State != MetricOK || snapshot.Network.Value == nil || snapshot.Network.Value.ReceivedBytesPerSecond != 100 {
		t.Fatalf("buffered Engine stats used local read spacing instead of Docker samples: %+v", snapshot.Network)
	}
	if snapshot.BlockIO.State != MetricOK || snapshot.BlockIO.Value == nil || snapshot.BlockIO.Value.ReadBytesPerSecond != 100 {
		t.Fatalf("buffered Engine stats block I/O rate = %+v", snapshot.BlockIO)
	}
	if err := sub.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestStatsStreamAdvancesBaselineAfterDockerClockJump(t *testing.T) {
	engine := newPipeStatsEngine()
	manager, err := NewStatsManager(engine, StatsManagerOptions{CloseTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	sub, err := manager.Subscribe(context.Background(), statsTestID)
	if err != nil {
		t.Fatal(err)
	}
	stream := waitPipeStream(t, engine)
	base := time.Date(2026, 10, 8, 10, 0, 0, 0, time.UTC)
	jumped := base.Add(time.Hour)
	var records []byte
	records = append(records, encodeStatsSample(rateSample(base, time.Time{}, 0, 0, 0, 0))...)
	records = append(records, encodeStatsSample(rateSample(jumped, time.Time{}, 100, 100, 100, 100))...)
	records = append(records, encodeStatsSample(rateSample(jumped.Add(time.Second), time.Time{}, 200, 200, 200, 200))...)
	if _, err := stream.writer.Write(records); err != nil {
		t.Fatal(err)
	}
	snapshot := waitLatestSample(t, sub, jumped.Add(time.Second))
	if snapshot.Network.State != MetricOK || snapshot.Network.Value == nil || snapshot.Network.Value.ReceivedBytesPerSecond != 100 {
		t.Fatalf("stream did not recover after advancing its clock-jump baseline: %+v", snapshot.Network)
	}
	if snapshot.BlockIO.State != MetricOK || snapshot.BlockIO.Value == nil || snapshot.BlockIO.Value.ReadBytesPerSecond != 100 {
		t.Fatalf("stream block I/O did not recover after clock jump: %+v", snapshot.BlockIO)
	}
	if err := sub.Close(); err != nil {
		t.Fatal(err)
	}
}

func rateSample(read, received time.Time, rx, tx, readIO, writeIO uint64) rawDockerStats {
	sample := statsSample(read)
	sample.receivedAt = received
	sample.Networks = networkSample(rx, tx)
	sample.BlkioStats = blockIOSample(readIO, writeIO)
	return sample
}

func TestStatsManagerSharesStreamKeepsLatestAndClosesOnLastSubscriber(t *testing.T) {
	engine := newPipeStatsEngine()
	manager, err := NewStatsManager(engine, StatsManagerOptions{CloseTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	if engine.openCount() != 0 {
		t.Fatal("stats manager opened Docker stats without subscribers")
	}
	ctx := context.Background()
	first, err := manager.Subscribe(ctx, statsTestID)
	if err != nil {
		t.Fatal(err)
	}
	second, err := manager.Subscribe(ctx, statsTestID)
	if err != nil {
		t.Fatal(err)
	}
	stream := waitPipeStream(t, engine)
	if engine.openCount() != 1 {
		t.Fatalf("two subscribers opened %d Docker stats requests, want exactly one", engine.openCount())
	}
	if cap(first.updates) != 1 || cap(second.updates) != 1 {
		t.Fatal("subscriber update queues must retain at most the newest sample")
	}

	t1 := time.Date(2026, 10, 8, 10, 0, 0, 0, time.UTC)
	t2 := t1.Add(time.Second)
	if _, err := stream.writer.Write(encodeStatsSample(statsSample(t1))); err != nil {
		t.Fatal(err)
	}
	if _, err := stream.writer.Write(encodeStatsSample(statsSample(t2))); err != nil {
		t.Fatal(err)
	}
	gotFirst := waitLatestSample(t, first, t2)
	gotSecond := waitLatestSample(t, second, t2)
	if gotFirst.ObservedAt == nil || gotSecond.ObservedAt == nil || !gotFirst.ObservedAt.Equal(t2) || !gotSecond.ObservedAt.Equal(t2) {
		t.Fatalf("subscribers did not receive latest sample: first=%+v second=%+v", gotFirst, gotSecond)
	}

	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-stream.closed:
		t.Fatal("closing one of two subscribers released their shared Docker body")
	default:
	}
	if engine.openCount() != 1 {
		t.Fatalf("shared stats request was reopened: %d", engine.openCount())
	}
	if err := second.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-stream.closed:
	case <-time.After(2 * time.Second):
		t.Fatal("last unsubscribe did not close the real reader body")
	}
	select {
	case <-first.Done():
	default:
		t.Fatal("closed subscriber did not signal Done")
	}
	select {
	case <-second.Done():
	default:
		t.Fatal("closed final subscriber did not signal Done")
	}
	manager.mu.Lock()
	remaining := len(manager.streams)
	manager.mu.Unlock()
	if remaining != 0 {
		t.Fatalf("manager retained %d stats stream after final subscriber left", remaining)
	}
}

func TestStatsSubscribersReceiveIndependentSnapshotPointers(t *testing.T) {
	engine := newPipeStatsEngine()
	manager, err := NewStatsManager(engine, StatsManagerOptions{CloseTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()

	first, err := manager.Subscribe(context.Background(), statsTestID)
	if err != nil {
		t.Fatal(err)
	}
	second, err := manager.Subscribe(context.Background(), statsTestID)
	if err != nil {
		t.Fatal(err)
	}
	stream := waitPipeStream(t, engine)
	at := time.Date(2026, 10, 8, 10, 0, 0, 0, time.UTC)
	sample := statsSample(at)
	sample.MemoryStats = memorySample(1200, 2000, map[string]uint64{"inactive_file": 200})
	if _, err := stream.writer.Write(encodeStatsSample(sample)); err != nil {
		t.Fatal(err)
	}
	firstSnapshot := waitLatestSample(t, first, at)
	secondSnapshot := waitLatestSample(t, second, at)
	if firstSnapshot.ObservedAt == secondSnapshot.ObservedAt ||
		firstSnapshot.Memory.Value == secondSnapshot.Memory.Value ||
		firstSnapshot.Memory.SampledAt == secondSnapshot.Memory.SampledAt {
		t.Fatal("subscribers share mutable snapshot pointers")
	}

	start := make(chan struct{})
	mutated := make(chan struct{})
	go func() {
		close(start)
		for i := 0; i < 1000; i++ {
			firstSnapshot.Memory.Value.UsedBytes = uint64(i)
			*firstSnapshot.Memory.SampledAt = at.Add(time.Duration(i) * time.Second)
			*firstSnapshot.ObservedAt = at.Add(time.Duration(i) * time.Second)
		}
		close(mutated)
	}()
	<-start
	for i := 0; i < 1000; i++ {
		if secondSnapshot.Memory.Value.UsedBytes != 1000 ||
			!secondSnapshot.Memory.SampledAt.Equal(at) || !secondSnapshot.ObservedAt.Equal(at) {
			t.Fatalf("mutating one subscriber changed another snapshot: %+v", secondSnapshot)
		}
	}
	<-mutated
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	if err := second.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestStatsSubscriberContextCancellationReleasesStream(t *testing.T) {
	engine := newPipeStatsEngine()
	manager, err := NewStatsManager(engine, StatsManagerOptions{CloseTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	ctx, cancel := context.WithCancel(context.Background())
	sub, err := manager.Subscribe(ctx, statsTestID)
	if err != nil {
		t.Fatal(err)
	}
	stream := waitPipeStream(t, engine)
	cancel()
	select {
	case <-stream.closed:
	case <-time.After(2 * time.Second):
		t.Fatal("canceling the last subscriber did not close Docker stats body")
	}
	select {
	case <-sub.Done():
	case <-time.After(time.Second):
		t.Fatal("canceled subscriber was not closed")
	}
}

func TestStatsImmediateUnsubscribeDoesNotRaceStreamStartup(t *testing.T) {
	engine := newPipeStatsEngine()
	manager, err := NewStatsManager(engine, StatsManagerOptions{CloseTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	for i := 0; i < 50; i++ {
		sub, err := manager.Subscribe(context.Background(), statsTestID)
		if err != nil {
			t.Fatal(err)
		}
		if err := sub.Close(); err != nil {
			t.Fatalf("immediate unsubscribe #%d: %v", i, err)
		}
	}
	manager.mu.Lock()
	remaining := len(manager.streams)
	manager.mu.Unlock()
	if remaining != 0 {
		t.Fatalf("immediate unsubscribe left %d Engine stream entries", remaining)
	}
}

func TestStatsMalformedRecordClosesSubscribersAndReportsSafeError(t *testing.T) {
	engine := newPipeStatsEngine()
	manager, err := NewStatsManager(engine, StatsManagerOptions{CloseTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	sub, err := manager.Subscribe(context.Background(), statsTestID)
	if err != nil {
		t.Fatal(err)
	}
	stream := waitPipeStream(t, engine)
	_, _ = io.WriteString(stream.writer, `{"id":"attacker-secret"}`+"\n")
	select {
	case got := <-sub.Errors:
		if got != ErrStatsContainerID {
			t.Fatalf("subscriber error=%v, want typed container mismatch", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("mismatched stats record did not terminate subscriber")
	}
	select {
	case <-stream.closed:
	case <-time.After(time.Second):
		t.Fatal("mismatched stats stream body remained open")
	}
	select {
	case <-sub.Done():
	case <-time.After(time.Second):
		t.Fatal("mismatched stats stream did not finish subscriber cleanup")
	}
	select {
	case <-sub.stream.done:
	case <-time.After(time.Second):
		t.Fatal("mismatched stats reader did not join")
	}
	if strings.Contains(ErrStatsContainerID.Error(), "attacker-secret") {
		t.Fatal("typed stream error leaked untrusted record data")
	}
	recovered, err := manager.Subscribe(context.Background(), statsTestID)
	if err != nil {
		t.Fatalf("subscribe after stream failure: %v", err)
	}
	recoveredStream := waitPipeStream(t, engine)
	if err := recovered.Close(); err != nil {
		t.Fatalf("close recovered subscription: %v", err)
	}
	select {
	case <-recoveredStream.closed:
	case <-time.After(time.Second):
		t.Fatal("recovered stats stream body remained open after unsubscribe")
	}
	if engine.openCount() != 2 {
		t.Fatalf("recovery opened %d Engine streams, want original plus one replacement", engine.openCount())
	}
}

func TestStatsMissingIDMalformedRecordAndReaderFailureAreTypedAndRecoverable(t *testing.T) {
	tests := []struct {
		name       string
		write      func(*pipeStatsStream)
		want       error
		secretText string
	}{
		{
			name: "missing container ID",
			write: func(stream *pipeStatsStream) {
				_, _ = io.WriteString(stream.writer, `{"read":"2026-10-08T10:00:00Z"}`+"\n")
			},
			want: ErrStatsContainerID,
		},
		{
			name: "malformed JSON",
			write: func(stream *pipeStatsStream) {
				_, _ = io.WriteString(stream.writer, `{"id":`)
				_ = stream.writer.Close()
			},
			want: ErrStatsMalformed,
		},
		{
			name: "reader failure",
			write: func(stream *pipeStatsStream) {
				_ = stream.writer.CloseWithError(errors.New("sensitive socket detail"))
			},
			want:       ErrStatsRead,
			secretText: "sensitive socket detail",
		},
		{
			name: "oversized unknown field",
			write: func(stream *pipeStatsStream) {
				record := `{"id":"` + statsTestID + `","future":"` + strings.Repeat("x", maxStatsRecordBytes) + `"}` + "\n"
				_, _ = io.WriteString(stream.writer, record)
			},
			want: ErrStatsRecordTooLarge,
		},
		{
			name: "deeply nested unknown field",
			write: func(stream *pipeStatsStream) {
				depth := maxStatsJSONDepth + 1
				record := `{"id":"` + statsTestID + `","future":` + strings.Repeat("[", depth) + "0" + strings.Repeat("]", depth) + "}\n"
				_, _ = io.WriteString(stream.writer, record)
			},
			want: ErrStatsStructureLimit,
		},
		{
			name: "too many array entries",
			write: func(stream *pipeStatsStream) {
				entries := strings.TrimSuffix(strings.Repeat("0,", maxStatsContainerEntries), ",") + ",0"
				record := `{"id":"` + statsTestID + `","future":[` + entries + "]}\n"
				_, _ = io.WriteString(stream.writer, record)
			},
			want: ErrStatsStructureLimit,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			engine := newPipeStatsEngine()
			manager, err := NewStatsManager(engine, StatsManagerOptions{CloseTimeout: time.Second})
			if err != nil {
				t.Fatal(err)
			}
			defer manager.Close()
			sub, err := manager.Subscribe(context.Background(), statsTestID)
			if err != nil {
				t.Fatal(err)
			}
			stream := waitPipeStream(t, engine)
			test.write(stream)
			select {
			case got := <-sub.Errors:
				if got != test.want {
					t.Fatalf("subscriber error=%v, want %v", got, test.want)
				}
				if test.secretText != "" && strings.Contains(got.Error(), test.secretText) {
					t.Fatal("typed stats error leaked the underlying reader error")
				}
			case <-time.After(2 * time.Second):
				t.Fatal("failed stats stream did not report an error")
			}
			select {
			case <-sub.Done():
			case <-time.After(time.Second):
				t.Fatal("failed stats stream did not close subscriber")
			}
			select {
			case <-sub.stream.done:
			case <-time.After(time.Second):
				t.Fatal("failed stats reader did not join")
			}
			select {
			case <-stream.closed:
			case <-time.After(time.Second):
				t.Fatal("failed stats stream body remained open")
			}
			recovered, err := manager.Subscribe(context.Background(), statsTestID)
			if err != nil {
				t.Fatalf("subscribe after stream failure: %v", err)
			}
			recoveredStream := waitPipeStream(t, engine)
			if err := recovered.Close(); err != nil {
				t.Fatalf("close recovered subscription: %v", err)
			}
			select {
			case <-recoveredStream.closed:
			case <-time.After(time.Second):
				t.Fatal("recovered stats stream body remained open after unsubscribe")
			}
			if engine.openCount() != 2 {
				t.Fatalf("recovery opened %d Engine streams, want original plus one replacement", engine.openCount())
			}
		})
	}
}

func TestStatsLargeUnknownFieldWithinBoundedRecordIsIgnored(t *testing.T) {
	engine := newPipeStatsEngine()
	manager, err := NewStatsManager(engine, StatsManagerOptions{CloseTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	sub, err := manager.Subscribe(context.Background(), statsTestID)
	if err != nil {
		t.Fatal(err)
	}
	stream := waitPipeStream(t, engine)
	at := time.Date(2026, 10, 8, 10, 0, 0, 0, time.UTC)
	record := `{"id":"` + statsTestID + `","read":"` + at.Format(time.RFC3339Nano) + `","future":"` + strings.Repeat("x", 32*1024) + `"}` + "\n"
	if _, err := io.WriteString(stream.writer, record); err != nil {
		t.Fatal(err)
	}
	select {
	case snapshot := <-sub.Updates:
		if snapshot.ContainerID != statsTestID || snapshot.ObservedAt == nil || !snapshot.ObservedAt.Equal(at) {
			t.Fatalf("bounded record with unknown field produced invalid sample: %+v", snapshot)
		}
	case err := <-sub.Errors:
		t.Fatalf("bounded unknown field was rejected: %v", err)
	case <-time.After(time.Second):
		t.Fatal("bounded unknown field did not produce a stats sample")
	}
	if err := sub.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestStatsClosingStreamRejectsResubscribeUntilReaderJoins(t *testing.T) {
	engine := &closingStatsEngine{firstBody: newBlockingStatsBody()}
	manager, err := NewStatsManager(engine, StatsManagerOptions{CloseTimeout: 100 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()

	sub, err := manager.Subscribe(context.Background(), statsTestID)
	if err != nil {
		t.Fatal(err)
	}
	body := engine.firstBody
	select {
	case <-body.readStarted:
	case <-time.After(time.Second):
		t.Fatal("stats reader did not enter its blocking body")
	}
	closeResult := make(chan error, 1)
	go func() { closeResult <- sub.Close() }()
	select {
	case <-body.closeCalled:
	case <-time.After(time.Second):
		t.Fatal("unsubscribe did not close the old Engine body")
	}
	select {
	case err := <-closeResult:
		if err != ErrStatsReaderClose {
			t.Fatalf("unsubscribe error=%v, want bounded close timeout", err)
		}
	case <-time.After(time.Second):
		t.Fatal("unsubscribe did not return after its close deadline")
	}
	if _, err := manager.Subscribe(context.Background(), statsTestID); err != ErrStatsStreamClosing {
		t.Fatalf("resubscribe during stalled close error=%v, want ErrStatsStreamClosing", err)
	}
	if got := engine.opens.Load(); got != 1 {
		t.Fatalf("opened %d overlapping Engine streams while old reader was stalled", got)
	}

	close(body.releaseRead)
	select {
	case <-sub.stream.done:
	case <-time.After(time.Second):
		t.Fatal("old stats reader did not join after its body returned")
	}
	second, err := manager.Subscribe(context.Background(), statsTestID)
	if err != nil {
		t.Fatalf("resubscribe after old reader joined: %v", err)
	}
	deadline := time.Now().Add(time.Second)
	for engine.opens.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := engine.opens.Load(); got != 2 {
		t.Fatalf("opened %d Engine streams after final resubscribe, want two sequential calls", got)
	}
	select {
	case <-second.Done():
	case <-time.After(time.Second):
		t.Fatal("replacement stream did not finish its EOF cleanup")
	}
}

type pipeStatsEngine struct {
	opens atomic.Int32
	items chan *pipeStatsStream
}

type closingStatsEngine struct {
	opens     atomic.Int32
	firstBody *blockingStatsBody
}

func (e *closingStatsEngine) OpenStats(context.Context, string) (io.ReadCloser, error) {
	if e.opens.Add(1) == 1 {
		return e.firstBody, nil
	}
	return io.NopCloser(strings.NewReader("")), nil
}

type blockingStatsBody struct {
	readStarted chan struct{}
	closeCalled chan struct{}
	releaseRead chan struct{}
	readOnce    sync.Once
	closeOnce   sync.Once
}

func newBlockingStatsBody() *blockingStatsBody {
	return &blockingStatsBody{readStarted: make(chan struct{}), closeCalled: make(chan struct{}), releaseRead: make(chan struct{})}
}

func (b *blockingStatsBody) Read([]byte) (int, error) {
	b.readOnce.Do(func() { close(b.readStarted) })
	<-b.releaseRead
	return 0, io.EOF
}

func (b *blockingStatsBody) Close() error {
	b.closeOnce.Do(func() { close(b.closeCalled) })
	return nil
}

type pipeStatsStream struct {
	reader *trackedPipeReader
	writer *io.PipeWriter
	closed chan struct{}
}

type trackedPipeReader struct {
	*io.PipeReader
	once   sync.Once
	closed chan struct{}
}

func newPipeStatsEngine() *pipeStatsEngine {
	return &pipeStatsEngine{items: make(chan *pipeStatsStream, 128)}
}

func (e *pipeStatsEngine) OpenStats(ctx context.Context, _ string) (io.ReadCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	reader, writer := io.Pipe()
	closed := make(chan struct{})
	stream := &pipeStatsStream{reader: &trackedPipeReader{PipeReader: reader, closed: closed}, writer: writer, closed: closed}
	e.opens.Add(1)
	e.items <- stream
	return stream.reader, nil
}

func (e *pipeStatsEngine) openCount() int32 { return e.opens.Load() }

func (r *trackedPipeReader) Close() error {
	err := r.PipeReader.Close()
	r.once.Do(func() { close(r.closed) })
	return err
}

func waitPipeStream(t *testing.T, engine *pipeStatsEngine) *pipeStatsStream {
	t.Helper()
	select {
	case stream := <-engine.items:
		return stream
	case <-time.After(2 * time.Second):
		t.Fatal("stats manager did not open an Engine stream")
		return nil
	}
}

func encodeStatsSample(sample rawDockerStats) []byte {
	data, _ := json.Marshal(sample)
	return append(data, '\n')
}

func statsSample(at time.Time) rawDockerStats {
	return rawDockerStats{ID: statsTestID, Read: timePointer(at)}
}

func memorySample(usage, limit uint64, stats map[string]uint64) *rawMemoryStats {
	return &rawMemoryStats{Usage: uintPointer(usage), Limit: uintPointer(limit), Stats: stats}
}

func networkSample(received, sent uint64) map[string]rawNet {
	return map[string]rawNet{"eth0": {RxBytes: uintPointer(received), TxBytes: uintPointer(sent)}}
}

func blockIOSample(read, write uint64) *rawBlockIOStats {
	entries := []rawBlockIOEntry{{Major: 8, Minor: 0, Op: "Read", Value: read}, {Major: 8, Minor: 0, Op: "Write", Value: write},
		{Major: 8, Minor: 0, Op: "Total", Value: read + write}}
	return &rawBlockIOStats{ServiceBytes: &entries}
}

func uintPointer(value uint64) *uint64 { return &value }

func uint32Pointer(value uint32) *uint32 { return &value }

func timePointer(value time.Time) *time.Time { return &value }

func assertMetricUnknown[T any](t *testing.T, metric Metric[T], reason string) {
	t.Helper()
	if metric.State != MetricUnknown || metric.Value != nil || metric.Reason != reason {
		t.Fatalf("metric = %+v, want unknown reason %q with no value", metric, reason)
	}
}

func waitLatestSample(t *testing.T, sub *StatsSubscription, expected time.Time) StatsSnapshot {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		select {
		case sample, ok := <-sub.Updates:
			if !ok {
				t.Fatal("stats update stream closed before latest sample")
			}
			if sample.ObservedAt != nil && sample.ObservedAt.Equal(expected) {
				return sample
			}
		case <-deadline:
			t.Fatal("timed out waiting for latest Docker stats sample")
		}
	}
}
