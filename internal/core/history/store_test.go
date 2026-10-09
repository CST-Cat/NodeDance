package history

import (
	"context"
	"database/sql"
	"math"
	"path/filepath"
	"testing"
	"time"

	"github.com/CST-Cat/NodeDance/internal/core/storage"
	"github.com/CST-Cat/NodeDance/internal/protocol"
)

func openHistoryStore(t *testing.T) (*sql.DB, Store, string) {
	t.Helper()
	opened, err := storage.Open(context.Background(), filepath.Join(t.TempDir(), "data"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = opened.Close() })
	nodeID := "00000000-0000-4000-8000-000000000001"
	now := time.Now().UnixNano()
	if _, err := opened.DB.Exec(`INSERT INTO nodes(id, display_name, status, created_at, updated_at) VALUES(?, 'history fixture', 'offline', ?, ?)`, nodeID, now, now); err != nil {
		t.Fatal(err)
	}
	return opened.DB, Store{DB: opened.DB}, nodeID
}

func TestKnownOnlyMinuteAggregationAndSampleWeightedHour(t *testing.T) {
	ctx := context.Background()
	db, store, nodeID := openHistoryStore(t)
	base := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	for index, cpu := range []float64{10, 20, 30, 100, 100, 100} {
		at := base.Add(time.Duration(index) * time.Minute)
		if err := store.AppendSnapshot(ctx, nodeID, historySnapshot(cpu, index < 3), at); err != nil {
			t.Fatal(err)
		}
	}
	// An unknown sample must leave the minute absent. A known zero, in contrast,
	// remains an explicit valid observation and is stored as zero.
	if err := store.AppendSnapshot(ctx, nodeID, historySnapshot(0, false), base.Add(7*time.Minute)); err != nil {
		t.Fatal(err)
	}
	minute, err := store.Query(ctx, nodeID, "minute", base, base.Add(8*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	cpu := findSeries(minute, "cpu.usage_percent")
	if len(cpu.Points) != 7 || cpu.Points[0].Value != 10 || cpu.Points[0].Samples != 1 || cpu.Points[6].Value != 0 {
		t.Fatalf("minute points do not preserve known values and gaps: %+v", cpu.Points)
	}
	if _, err := db.Exec(`INSERT INTO metrics_minute(node_id, metric_key, bucket_at, sample_count, sample_sum, minimum, maximum) VALUES(?, 'cpu.usage_percent', ?, 3, 300, 100, 100)`, nodeID, base.Add(8*time.Minute).Unix()); err != nil {
		t.Fatal(err)
	}
	hour, err := store.Query(ctx, nodeID, "hour", base, base.Add(59*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	point := findSeries(hour, "cpu.usage_percent").Points[0]
	// Six one-sample minute buckets, the known zero, and three more samples of
	// 100: the hour is weighted by all ten valid source observations.
	if math.Abs(point.Value-(660.0/10.0)) > 1e-9 || point.Samples != 10 {
		t.Fatalf("hourly mean=%v samples=%d; want sample-weighted 660/10 over 10 samples", point.Value, point.Samples)
	}
	if len(findSeries(minute, "memory.used_percent").Points) != 3 {
		t.Fatal("an unknown memory reading was incorrectly stored as zero or filled")
	}
}

func TestCleanupRollsUpMinuteRowsAndLeavesPreferences(t *testing.T) {
	ctx := context.Background()
	db, store, nodeID := openHistoryStore(t)
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	old := now.Add(-31 * 24 * time.Hour).Truncate(time.Minute)
	valid := now.Add(-29 * 24 * time.Hour).Truncate(time.Minute)
	if err := store.AppendSnapshot(ctx, nodeID, historySnapshot(80, false), old); err != nil {
		t.Fatal(err)
	}
	if err := store.AppendSnapshot(ctx, nodeID, historySnapshot(20, false), valid); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO dashboard_preferences(node_id,target_kind,identity_key,alias,updated_at) VALUES(?,'node',?, 'Production', 1)`, nodeID, "node:"+nodeID); err != nil {
		t.Fatal(err)
	}
	before, err := store.Query(ctx, nodeID, "hour", old, valid.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Cleanup(ctx, now); err != nil {
		t.Fatal(err)
	}
	var minuteCount, hourCount, preferenceCount int
	if err := db.QueryRow(`SELECT count(*) FROM metrics_minute WHERE node_id=?`, nodeID).Scan(&minuteCount); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM metrics_hour WHERE node_id=?`, nodeID).Scan(&hourCount); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM dashboard_preferences WHERE node_id=?`, nodeID).Scan(&preferenceCount); err != nil {
		t.Fatal(err)
	}
	if minuteCount != 1 || hourCount != 1 || preferenceCount != 1 {
		t.Fatalf("cleanup rows minute=%d hour=%d preferences=%d; want 1,1,1", minuteCount, hourCount, preferenceCount)
	}
	history, err := store.Query(ctx, nodeID, "hour", old, valid.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	points := findSeries(history, "cpu.usage_percent").Points
	beforePoints := findSeries(before, "cpu.usage_percent").Points
	if len(points) != 2 || points[0].Value != 80 || points[0].Samples != 1 || points[1].Value != 20 || points[1].Samples != 1 {
		t.Fatalf("rolled up history lost data or valid minute: %+v", points)
	}
	if len(beforePoints) != len(points) {
		t.Fatalf("hourly bucket count changed across cleanup: before=%+v after=%+v", beforePoints, points)
	}
	for i := range points {
		if points[i] != beforePoints[i] {
			t.Fatalf("hourly query duplicated or lost rolled minute data: before=%+v after=%+v", beforePoints[i], points[i])
		}
	}
}

func TestQueryUsesHalfOpenBucketRange(t *testing.T) {
	ctx := context.Background()
	_, store, nodeID := openHistoryStore(t)
	base := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	for minute, cpu := range []float64{10, 20, 30} {
		if err := store.AppendSnapshot(ctx, nodeID, historySnapshot(cpu, false), base.Add(time.Duration(minute)*time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	response, err := store.Query(ctx, nodeID, "minute", base, base.Add(2*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	points := findSeries(response, "cpu.usage_percent").Points
	if len(points) != 2 || points[0].BucketAt.Equal(base.Add(2*time.Minute)) {
		t.Fatalf("[from,to) range included the to bucket: %+v", points)
	}
}

func TestDiskSeriesIdentityAllowsColonInMountpoint(t *testing.T) {
	if !ValidKey("disk:/mnt/vol:blue:used_percent") {
		t.Fatal("mountpoint colon made a valid disk series key invalid")
	}
	if ValidKey("disk::used_percent") || ValidKey("disk:/mnt:other") {
		t.Fatal("invalid disk metric key accepted")
	}
}

func historySnapshot(cpu float64, memoryKnown bool) protocol.MetricsSnapshot {
	cpuMetric := protocol.Metric[float64]{Status: protocol.MetricKnown, Value: &cpu}
	snapshot := protocol.MetricsSnapshot{CPU: protocol.MetricsCPU{UsagePercent: cpuMetric}}
	if memoryKnown {
		memory := protocol.Memory{TotalBytes: 100, AvailableBytes: 50, UsedBytes: 50, UsedPercent: 50}
		snapshot.Memory = protocol.Metric[protocol.Memory]{Status: protocol.MetricKnown, Value: &memory}
	} else {
		snapshot.Memory = protocol.Metric[protocol.Memory]{Status: protocol.MetricUnknown, Reason: "not_sampled"}
	}
	return snapshot
}

func findSeries(response Response, key string) Series {
	for _, series := range response.Series {
		if series.Key == key {
			return series
		}
	}
	return Series{Key: key}
}
