package history

import (
	"context"
	"database/sql"
	"time"

	"github.com/CST-Cat/NodeDance/internal/protocol"
)

const FineInterval = 5 * time.Second
const FineRetention = 24 * time.Hour
const FineMaxRange = 4 * time.Hour

// Only the four rate series need high-resolution retention. Source timestamps
// deduplicate cached reports; receiver time minus sample age avoids Agent clock
// skew moving samples into the future or filling missing intervals.
func appendFine(ctx context.Context, tx *sql.Tx, node string, snapshot protocol.MetricsSnapshot, received time.Time) error {
	interval := 5
	if snapshot.Host != nil && snapshot.Host.FastIntervalSeconds > 0 {
		interval = snapshot.Host.FastIntervalSeconds
	}
	add := func(key string, status protocol.MetricStatus, sampled time.Time, age int64, value float64) error {
		if status != protocol.MetricKnown || sampled.IsZero() || age < 0 || age > int64(interval)*3000 || !finite(value) || value < 0 {
			return nil
		}
		result, err := tx.ExecContext(ctx, `INSERT INTO metric_sample_cursor(node_id,metric_key,sampled_at) VALUES(?,?,?) ON CONFLICT(node_id,metric_key) DO UPDATE SET sampled_at=excluded.sampled_at WHERE excluded.sampled_at!=metric_sample_cursor.sampled_at`, node, "5s:"+key, sampled.UnixNano())
		if err != nil {
			return err
		}
		count, err := result.RowsAffected()
		if err != nil || count == 0 {
			return err
		}
		bucket := received.Add(-time.Duration(age) * time.Millisecond).Truncate(FineInterval).Unix()
		_, err = tx.ExecContext(ctx, `INSERT INTO metrics_fine(node_id,metric_key,bucket_at,sample_count,sample_sum,minimum,maximum) VALUES(?,?,?,1,?,?,?) ON CONFLICT(node_id,metric_key,bucket_at) DO UPDATE SET sample_count=metrics_fine.sample_count+1,sample_sum=metrics_fine.sample_sum+excluded.sample_sum,minimum=MIN(metrics_fine.minimum,excluded.minimum),maximum=MAX(metrics_fine.maximum,excluded.maximum)`, node, key, bucket, value, value, value)
		return err
	}
	net := snapshot.Network.Summary
	if net.Value != nil {
		if err := add("network.received_bytes_per_second", net.Status, net.SampledAt, net.SampleAgeMillis, net.Value.ReceivedBytesPerSecond); err != nil {
			return err
		}
		if err := add("network.sent_bytes_per_second", net.Status, net.SampledAt, net.SampleAgeMillis, net.Value.SentBytesPerSecond); err != nil {
			return err
		}
	}
	if snapshot.Host != nil && snapshot.Host.DiskIO != nil {
		disk := snapshot.Host.DiskIO
		if disk.Value != nil {
			if err := add("disk.read_bytes_per_second", disk.Status, disk.SampledAt, disk.SampleAgeMillis, disk.Value.ReadBytesPerSecond); err != nil {
				return err
			}
			if err := add("disk.write_bytes_per_second", disk.Status, disk.SampledAt, disk.SampleAgeMillis, disk.Value.WriteBytesPerSecond); err != nil {
				return err
			}
		}
	}
	return nil
}
