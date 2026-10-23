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
	"sort"
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
		if snapshot.Host != nil && snapshot.Host.Observability != nil {
			if metric, ok := snapshot.Host.Observability.Values[item.key]; ok {
				result, err := tx.ExecContext(ctx, `INSERT INTO metric_sample_cursor(node_id,metric_key,sampled_at) VALUES(?,?,?) ON CONFLICT(node_id,metric_key) DO UPDATE SET sampled_at=excluded.sampled_at WHERE excluded.sampled_at!=metric_sample_cursor.sampled_at`, nodeID, item.key, metric.SampledAt.UnixNano())
				if err != nil {
					return err
				}
				n, err := result.RowsAffected()
				if err != nil {
					return err
				}
				if n == 0 {
					continue
				}
			}
		}

		if _, err := stmt.ExecContext(ctx, nodeID, item.key, bucket, item.value, item.value, item.value); err != nil {
			return fmt.Errorf("append metric history %s: %w", item.key, err)
		}
	}
	if err := appendFine(ctx, tx, nodeID, snapshot, receivedAt); err != nil {
		return err
	}
	if err := recordSnapshot(ctx, tx, nodeID, snapshot, receivedAt); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit metric history insert: %w", err)
	}
	return nil
}

func (s Store) Cleanup(ctx context.Context, now time.Time) error {
	if s.DB == nil || now.IsZero() {
		return errors.New("metric history cleanup clock is invalid")
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin metric history cleanup: %w", err)
	}
	defer tx.Rollback()
	if err := cleanupHistory(ctx, tx, now); err != nil {
		return err
	}
	return tx.Commit()
}

func cleanupHistory(ctx context.Context, tx *sql.Tx, now time.Time) error {
	var days int
	if err := tx.QueryRowContext(ctx, `SELECT retention_days FROM metric_history_settings WHERE id=1`).Scan(&days); err != nil {
		return err
	}
	retention := time.Duration(days) * 24 * time.Hour
	minuteCutoff := now.UTC().Add(-min(MinuteRetention, retention)).Truncate(time.Minute).Unix()
	hourCutoff := now.UTC().Add(-retention).Truncate(time.Hour).Unix()
	_, err := tx.ExecContext(ctx, `INSERT INTO metrics_hour(node_id, metric_key, bucket_at, sample_count, sample_sum, minimum, maximum)
		SELECT node_id, metric_key, (bucket_at / ?)*?, SUM(sample_count), SUM(sample_sum), MIN(minimum), MAX(maximum)
		FROM metrics_minute WHERE bucket_at < ? AND bucket_at >= ? GROUP BY node_id, metric_key, (bucket_at / ?)*?
		ON CONFLICT(node_id, metric_key, bucket_at) DO UPDATE SET
		sample_count=metrics_hour.sample_count+excluded.sample_count,
		sample_sum=metrics_hour.sample_sum+excluded.sample_sum,
		minimum=MIN(metrics_hour.minimum, excluded.minimum), maximum=MAX(metrics_hour.maximum, excluded.maximum)`,
		HourSeconds, HourSeconds, minuteCutoff, hourCutoff, HourSeconds, HourSeconds)
	if err != nil {
		return fmt.Errorf("roll up expired minute history: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM metrics_minute WHERE bucket_at < ?`, minuteCutoff); err != nil {
		return fmt.Errorf("delete expired minute history: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM metrics_fine WHERE bucket_at < ?`, now.Add(-FineRetention).Truncate(FineInterval).Unix()); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM metrics_hour WHERE bucket_at < ?`, hourCutoff); err != nil {
		return fmt.Errorf("delete expired hourly history: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM observation_incidents WHERE occurred_at<?`, now.Add(-min(7*24*time.Hour, retention)).UnixNano()); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM observation_frames WHERE received_at<? OR (received_at<? AND NOT EXISTS(SELECT 1 FROM observation_incidents i WHERE i.node_id=observation_frames.node_id AND observation_frames.received_at BETWEEN i.occurred_at-? AND i.occurred_at+?))`, now.Add(-retention).UnixNano(), now.Add(-2*time.Hour).UnixNano(), int64(5*time.Minute), int64(5*time.Minute)); err != nil {
		return err
	}

	return nil
}

func (s Store) Query(ctx context.Context, nodeID, resolution string, from, to time.Time) (Response, error) {
	return s.query(ctx, nodeID, resolution, from, to, nil)
}

// QueryKeys uses the same aggregation and bounds as Query while avoiding reads
// of unselected series for a guest projection.
func (s Store) QueryKeys(ctx context.Context, nodeID, resolution string, from, to time.Time, keys []string) (Response, error) {
	if len(keys) == 0 || len(keys) > 6 {
		return Response{}, ErrInvalidQuery
	}
	for _, key := range keys {
		if !ValidKey(key) {
			return Response{}, ErrInvalidQuery
		}
	}
	return s.query(ctx, nodeID, resolution, from, to, keys)
}
func (s Store) query(ctx context.Context, nodeID, resolution string, from, to time.Time, selected []string) (Response, error) {
	if s.DB == nil || nodeID == "" || from.IsZero() || to.IsZero() || !from.Before(to) {
		return Response{}, ErrInvalidQuery
	}
	step := MinuteSeconds
	if resolution == "hour" {
		step = HourSeconds
	} else if resolution == "5s" {
		step = int64(FineInterval / time.Second)
		if to.Sub(from) > FineMaxRange {
			return Response{}, ErrInvalidQuery
		}
	} else if resolution != "minute" {
		return Response{}, ErrInvalidQuery
	}
	from, to = from.UTC(), to.UTC()
	fromBucket := ceilBucket(from, step)
	toBucketExclusive := ceilBucket(to, step)
	filterSQL := ""
	filterArgs := []any{}
	if len(selected) > 0 {
		filterSQL = " AND metric_key IN (" + strings.TrimSuffix(strings.Repeat("?,", len(selected)), ",") + ")"
		for _, key := range selected {
			filterArgs = append(filterArgs, key)
		}
	}
	query := `SELECT metric_key, bucket_at, sample_count, sample_sum, minimum, maximum FROM metrics_minute
		WHERE node_id=? AND bucket_at>=? AND bucket_at<?` + filterSQL + ` ORDER BY metric_key, bucket_at`
	if resolution == "5s" {
		query = strings.Replace(query, "FROM metrics_minute", "FROM metrics_fine", 1)
	}
	args := append([]any{nodeID, fromBucket, toBucketExclusive}, filterArgs...)
	if resolution == "hour" {
		query = `SELECT metric_key, bucket_at, SUM(sample_count), SUM(sample_sum), MIN(minimum), MAX(maximum)
			FROM (
				SELECT metric_key, bucket_at, sample_count, sample_sum, minimum, maximum FROM metrics_hour WHERE node_id=? AND bucket_at>=? AND bucket_at<?` + filterSQL + `
				UNION ALL
				SELECT metric_key, (bucket_at / ?)*?, sample_count, sample_sum, minimum, maximum FROM metrics_minute WHERE node_id=? AND bucket_at>=? AND bucket_at<?` + filterSQL + `
			) GROUP BY metric_key, bucket_at ORDER BY metric_key, bucket_at`
		args = append([]any{nodeID, fromBucket, toBucketExclusive}, filterArgs...)
		args = append(args, HourSeconds, HourSeconds, nodeID, fromBucket, toBucketExclusive)
		args = append(args, filterArgs...)
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
	sort.Strings(keys)
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
	for key, m := range protocol.HostNumericMetrics(snapshot.Host) {
		appendKnown(key, m.Status, m.Value)
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
	if _, ok := protocol.HostMetricKeys[key]; ok {
		return true
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

func ceilBucket(at time.Time, step int64) int64 {
	interval := time.Duration(step) * time.Second
	bucket := at.Truncate(interval)
	if !bucket.Equal(at) {
		bucket = bucket.Add(interval)
	}
	return bucket.Unix()
}
