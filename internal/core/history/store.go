// Package history persists sparse, known-only metric aggregates. Missing and
// failed readings create no row, so consumers can render a gap rather than a
// fabricated zero. Hourly rollups retain sample sums and counts so averages
// are weighted by the number of valid source samples.
package history

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/CST-Cat/NodeDance/internal/protocol"
)

const (
	MinuteRetention = 30 * 24 * time.Hour
	HourRetention   = 365 * 24 * time.Hour
	MinuteSeconds   = int64(time.Minute / time.Second)
	HourSeconds     = int64(time.Hour / time.Second)
)

var ErrInvalidQuery = errors.New("metric history query is invalid")

type Store struct{ DB *sql.DB }

type Point struct {
	BucketAt time.Time `json:"bucketAt"`
	Value    float64   `json:"value"`
	Samples  int64     `json:"samples"`
	Minimum  float64   `json:"minimum"`
	Maximum  float64   `json:"maximum"`
}

type Series struct {
	Key    string  `json:"key"`
	Points []Point `json:"points"`
}

type Response struct {
	NodeID     string    `json:"nodeId"`
	Resolution string    `json:"resolution"`
	From       time.Time `json:"from"`
	To         time.Time `json:"to"`
	Series     []Series  `json:"series"`
}

type value struct {
	key   string
	value float64
}

func (s Store) AppendSnapshot(ctx context.Context, nodeID string, snapshot protocol.MetricsSnapshot, receivedAt time.Time) error {
	if s.DB == nil || nodeID == "" || receivedAt.IsZero() {
		return errors.New("metric history identity or timestamp is invalid")
	}
	values := snapshotValues(snapshot)
	if len(values) == 0 {
		return nil
	}
	bucket := receivedAt.UTC().Truncate(time.Minute).Unix()
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin metric history insert: %w", err)
	}
	defer tx.Rollback()
	stmt, err := tx.PrepareContext(ctx, `INSERT INTO metrics_minute(node_id, metric_key, bucket_at, sample_count, sample_sum, minimum, maximum)
		VALUES(?, ?, ?, 1, ?, ?, ?)
		ON CONFLICT(node_id, metric_key, bucket_at) DO UPDATE SET
		sample_count=metrics_minute.sample_count+1,
		sample_sum=metrics_minute.sample_sum+excluded.sample_sum,
		minimum=MIN(metrics_minute.minimum, excluded.minimum), maximum=MAX(metrics_minute.maximum, excluded.maximum)`)
	if err != nil {
		return fmt.Errorf("prepare metric history insert: %w", err)
	}
	defer stmt.Close()
	for _, item := range values {
		if _, err := stmt.ExecContext(ctx, nodeID, item.key, bucket, item.value, item.value, item.value); err != nil {
			return fmt.Errorf("append metric history %s: %w", item.key, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit metric history insert: %w", err)
	}
	return nil
}

// Cleanup atomically folds minute buckets older than 30 days into hourly
// sums/counts and removes their minute rows, then expires hourly buckets older
// than one year. Preference tables are intentionally outside this operation.
func (s Store) Cleanup(ctx context.Context, now time.Time) error {
	if s.DB == nil || now.IsZero() {
		return errors.New("metric history cleanup clock is invalid")
	}
	minuteCutoff := now.UTC().Add(-MinuteRetention).Truncate(time.Minute).Unix()
	hourCutoff := now.UTC().Add(-HourRetention).Truncate(time.Hour).Unix()
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin metric history cleanup: %w", err)
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, `INSERT INTO metrics_hour(node_id, metric_key, bucket_at, sample_count, sample_sum, minimum, maximum)
		SELECT node_id, metric_key, (bucket_at / ?)*?, SUM(sample_count), SUM(sample_sum), MIN(minimum), MAX(maximum)
		FROM metrics_minute WHERE bucket_at < ? GROUP BY node_id, metric_key, (bucket_at / ?)*?
		ON CONFLICT(node_id, metric_key, bucket_at) DO UPDATE SET
		sample_count=metrics_hour.sample_count+excluded.sample_count,
		sample_sum=metrics_hour.sample_sum+excluded.sample_sum,
		minimum=MIN(metrics_hour.minimum, excluded.minimum), maximum=MAX(metrics_hour.maximum, excluded.maximum)`,
		HourSeconds, HourSeconds, minuteCutoff, HourSeconds, HourSeconds)
	if err != nil {
		return fmt.Errorf("roll up expired minute history: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM metrics_minute WHERE bucket_at < ?`, minuteCutoff); err != nil {
		return fmt.Errorf("delete expired minute history: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM metrics_hour WHERE bucket_at < ?`, hourCutoff); err != nil {
		return fmt.Errorf("delete expired hourly history: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit metric history cleanup: %w", err)
	}
	return nil
}

func (s Store) Query(ctx context.Context, nodeID, resolution string, from, to time.Time) (Response, error) {
	if s.DB == nil || nodeID == "" || from.IsZero() || to.IsZero() || !from.Before(to) {
		return Response{}, ErrInvalidQuery
	}
	step := MinuteSeconds
	if resolution == "hour" {
		step = HourSeconds
	} else if resolution != "minute" {
		return Response{}, ErrInvalidQuery
	}
	from, to = from.UTC(), to.UTC()
	fromBucket := ceilBucket(from.Unix(), step)
	toBucketExclusive := ceilBucket(to.Unix(), step)
	query := `SELECT metric_key, bucket_at, sample_count, sample_sum, minimum, maximum FROM metrics_minute
		WHERE node_id=? AND bucket_at>=? AND bucket_at<? ORDER BY metric_key, bucket_at`
	args := []any{nodeID, fromBucket, toBucketExclusive}
	if resolution == "hour" {
		query = `SELECT metric_key, bucket_at, SUM(sample_count), SUM(sample_sum), MIN(minimum), MAX(maximum)
			FROM (
				SELECT metric_key, bucket_at, sample_count, sample_sum, minimum, maximum FROM metrics_hour WHERE node_id=? AND bucket_at>=? AND bucket_at<?
				UNION ALL
				SELECT metric_key, (bucket_at / ?)*?, sample_count, sample_sum, minimum, maximum FROM metrics_minute WHERE node_id=? AND bucket_at>=? AND bucket_at<?
			) GROUP BY metric_key, bucket_at ORDER BY metric_key, bucket_at`
		args = []any{nodeID, fromBucket, toBucketExclusive, HourSeconds, HourSeconds, nodeID, fromBucket, toBucketExclusive}
	}
	rows, err := s.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return Response{}, fmt.Errorf("query metric history: %w", err)
	}
	defer rows.Close()
	series := make(map[string][]Point)
	for rows.Next() {
		var key string
		var bucket, count int64
		var sum, minimum, maximum float64
		if err := rows.Scan(&key, &bucket, &count, &sum, &minimum, &maximum); err != nil {
			return Response{}, fmt.Errorf("read metric history point: %w", err)
		}
		if count <= 0 || !finite(sum) || !finite(minimum) || !finite(maximum) {
			return Response{}, errors.New("stored metric history aggregate is invalid")
		}
		series[key] = append(series[key], Point{BucketAt: time.Unix(bucket, 0).UTC(), Value: sum / float64(count), Samples: count, Minimum: minimum, Maximum: maximum})
	}
	if err := rows.Err(); err != nil {
		return Response{}, fmt.Errorf("iterate metric history points: %w", err)
	}
	keys := make([]string, 0, len(series))
	for key := range series {
		keys = append(keys, key)
	}
	// Stable output simplifies the client and acceptance evidence.
	for i := 0; i < len(keys); i++ {
		for j := i + 1; j < len(keys); j++ {
			if keys[j] < keys[i] {
				keys[i], keys[j] = keys[j], keys[i]
			}
		}
	}
	out := make([]Series, 0, len(keys))
	for _, key := range keys {
		out = append(out, Series{Key: key, Points: series[key]})
	}
	return Response{NodeID: nodeID, Resolution: resolution, From: from, To: to, Series: out}, nil
}

func snapshotValues(snapshot protocol.MetricsSnapshot) []value {
	values := make([]value, 0, 8+len(snapshot.Disk.Mounts)*2)
	appendKnown := func(key string, status protocol.MetricStatus, v *float64) {
		if status == protocol.MetricKnown && v != nil && finite(*v) {
			values = append(values, value{key: key, value: *v})
		}
	}
	appendKnown("cpu.usage_percent", snapshot.CPU.UsagePercent.Status, snapshot.CPU.UsagePercent.Value)
	if snapshot.Memory.Status == protocol.MetricKnown && snapshot.Memory.Value != nil {
		usedPercent := snapshot.Memory.Value.UsedPercent
		usedBytes := float64(snapshot.Memory.Value.UsedBytes)
		if finite(usedPercent) {
			values = append(values, value{key: "memory.used_percent", value: usedPercent})
		}
		if finite(usedBytes) {
			values = append(values, value{key: "memory.used_bytes", value: usedBytes})
		}
	}
	if snapshot.Network.Summary.Status == protocol.MetricKnown && snapshot.Network.Summary.Value != nil {
		appendKnown("network.received_bytes_per_second", protocol.MetricKnown, &snapshot.Network.Summary.Value.ReceivedBytesPerSecond)
		appendKnown("network.sent_bytes_per_second", protocol.MetricKnown, &snapshot.Network.Summary.Value.SentBytesPerSecond)
	}
	seenMount := make(map[string]struct{}, len(snapshot.Disk.Mounts))
	for _, mount := range snapshot.Disk.Mounts {
		if mount.Mountpoint == "" || len(mount.Mountpoint) > 2048 || mount.Usage.Status != protocol.MetricKnown || mount.Usage.Value == nil {
			continue
		}
		if _, ok := seenMount[mount.Mountpoint]; ok {
			continue
		}
		seenMount[mount.Mountpoint] = struct{}{}
		prefix := "disk:" + mount.Mountpoint + ":"
		if finite(mount.Usage.Value.UsedPercent) {
			values = append(values, value{key: prefix + "used_percent", value: mount.Usage.Value.UsedPercent})
		}
		usedBytes := float64(mount.Usage.Value.UsedBytes)
		if finite(usedBytes) {
			values = append(values, value{key: prefix + "used_bytes", value: usedBytes})
		}
	}
	return values
}

func ValidKey(key string) bool {
	if len(key) > 4096 || strings.TrimSpace(key) != key {
		return false
	}
	switch key {
	case "cpu.usage_percent", "memory.used_percent", "memory.used_bytes", "network.received_bytes_per_second", "network.sent_bytes_per_second":
		return true
	}
	if !strings.HasPrefix(key, "disk:") {
		return false
	}
	body := strings.TrimPrefix(key, "disk:")
	for _, suffix := range []string{":used_percent", ":used_bytes"} {
		if strings.HasSuffix(body, suffix) && len(body) > len(suffix) {
			return true
		}
	}
	return false
}

func finite(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }

func ceilBucket(unix, step int64) int64 {
	if unix%step == 0 {
		return unix
	}
	return (unix/step + 1) * step
}
