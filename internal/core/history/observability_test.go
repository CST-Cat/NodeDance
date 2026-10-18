package history_test

import (
	"context"
	"github.com/CST-Cat/NodeDance/internal/core/history"
	"github.com/CST-Cat/NodeDance/internal/core/storage"
	"github.com/CST-Cat/NodeDance/internal/protocol"
	"math"
	"testing"
	"time"
)

func TestCapacityAndCorrelationRequireEvidence(t *testing.T) {
	now := time.Date(2026, 10, 18, 10, 0, 0, 0, time.UTC)
	points := []history.Point{}
	for i := 0; i < 180; i++ {
		points = append(points, history.Point{BucketAt: now.Add(time.Duration(i-180) * time.Minute), Value: 20 + float64(i)*.1, Samples: 1})
	}
	limit := 100.0
	a := history.Summarize("disk:/:used_percent", points, now, &limit, nil)
	if a.Status != "known" || a.ExhaustionAt == nil || a.Fit == nil || *a.Fit < .99 {
		t.Fatal(a)
	}
	if a = history.Summarize("x", points[:10], now, &limit, nil); a.Status != "unknown" || a.ExhaustionAt != nil {
		t.Fatal(a)
	}
	if a = history.Summarize("x", points[:90], now, &limit, nil); a.Status != "unknown" || a.Reason != "stale_history" {
		t.Fatal(a)
	}
	sparse := append(append([]history.Point{}, points[:60]...), points[90:]...)
	if a = history.Summarize("x", sparse, now, &limit, nil); a.Status != "unknown" || a.ExhaustionAt != nil {
		t.Fatal(a)
	}
	inverse := append([]history.Point{}, points...)
	for i := range inverse {
		inverse[i].Value = 100 - points[i].Value
	}
	r := history.Correlate("peer", "x", points, inverse)
	if r.Coefficient == nil || math.Abs(*r.Coefficient+1) > 1e-9 {
		t.Fatal(r)
	}
	for i := range inverse {
		inverse[i].Value = 5
	}
	if r = history.Correlate("peer", "x", points, inverse); r.Status != "unknown" || r.Reason != "constant_series" {
		t.Fatal(r)
	}
}
func TestRecordingDeduplicatesSlowSamplesAndRetainsIncidentWindow(t *testing.T) {
	ctx := context.Background()
	db, err := storage.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, err = db.DB.Exec(`INSERT INTO nodes(id,display_name,status,created_at,updated_at) VALUES('node','test','offline',0,0)`)
	if err != nil {
		t.Fatal(err)
	}
	s := history.Store{DB: db.DB}
	at := time.Now().UTC().Truncate(time.Minute).Add(-3 * time.Hour)
	value := 2.0
	snapshot := protocol.MetricsSnapshot{Host: &protocol.MetricsHost{Observability: &protocol.Observability{Values: map[string]protocol.Metric[float64]{"service.failed_count": {Status: protocol.MetricKnown, Value: &value, SampledAt: at}}, SlowIntervalSeconds: 60}}}
	for i := 0; i < 3; i++ {
		if err = s.AppendSnapshot(ctx, "node", snapshot, at.Add(time.Duration(i)*5*time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	var count int
	if err = db.DB.QueryRow(`SELECT sample_count FROM metrics_minute WHERE metric_key='service.failed_count'`).Scan(&count); err != nil || count != 1 {
		t.Fatal("repeated cached reading was counted more than once", count, err)
	}
	if err = s.Cleanup(ctx, time.Now()); err != nil {
		t.Fatal(err)
	}
	r, err := s.Recording(ctx, "node", at.Add(-time.Minute), at.Add(time.Minute))
	if err != nil || len(r.Frames) != 3 || len(r.Incidents) != 1 {
		t.Fatal(r, err)
	}
	if _, err = db.DB.Exec(`DELETE FROM nodes WHERE id='node'`); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"observation_frames", "observation_incidents", "metric_sample_cursor"} {
		if err = db.DB.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count); err != nil || count != 0 {
			t.Fatal("node deletion left diagnostic data", table, count, err)
		}
	}
}
