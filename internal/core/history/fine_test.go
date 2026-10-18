package history_test

import (
	"context"
	"testing"
	"time"

	"github.com/CST-Cat/NodeDance/internal/core/history"
	"github.com/CST-Cat/NodeDance/internal/core/storage"
	"github.com/CST-Cat/NodeDance/internal/protocol"
)

func TestFineHistoryPersistenceGapsDedupRetentionAndDeletion(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	db, err := storage.Open(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.DB.Exec(`INSERT INTO nodes(id,display_name,status,created_at,updated_at) VALUES('fine','fine','online',0,0)`); err != nil {
		t.Fatal(err)
	}
	at := time.Now().UTC().Truncate(time.Minute).Add(-time.Minute)
	snapshot := protocol.MetricsSnapshot{Host: &protocol.MetricsHost{FastIntervalSeconds: 5}}
	store := history.Store{DB: db.DB}
	for _, second := range []int{0, 5, 20} {
		stamp := at.Add(time.Duration(second) * time.Second)
		snapshot.Network.Summary = protocol.Metric[protocol.NetworkRate]{Status: protocol.MetricKnown, SampledAt: stamp, Value: &protocol.NetworkRate{ReceivedBytesPerSecond: float64(second), SentBytesPerSecond: 100}}
		disk := protocol.Metric[protocol.DiskIO]{Status: protocol.MetricKnown, SampledAt: stamp, Value: &protocol.DiskIO{ReadBytesPerSecond: 0, WriteBytesPerSecond: 200}}
		snapshot.Host.DiskIO = &disk
		if err = store.AppendSnapshot(ctx, "fine", snapshot, stamp); err != nil {
			t.Fatal(err)
		}
		// A cached repeat must not create a second fine bucket.
		if err = store.AppendSnapshot(ctx, "fine", snapshot, stamp.Add(time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	snapshot.Network.Summary.SampleAgeMillis = 16000
	snapshot.Host.DiskIO.SampleAgeMillis = 16000
	if err = store.AppendSnapshot(ctx, "fine", snapshot, at.Add(40*time.Second)); err != nil {
		t.Fatal(err)
	}
	db.Close()
	db, err = storage.Open(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store = history.Store{DB: db.DB}
	result, err := store.Query(ctx, "fine", "5s", at, at.Add(time.Minute))
	if err != nil || len(result.Series) != 4 {
		t.Fatal(result, err)
	}
	for _, series := range result.Series {
		if len(series.Points) != 3 {
			t.Fatalf("missing intervals or stale repeats fabricated: %+v", series)
		}
		for i, p := range series.Points {
			if p.Samples != 1 || !p.BucketAt.Equal(at.Add(time.Duration([]int{0, 5, 20}[i])*time.Second)) {
				t.Fatal(p)
			}
		}
	}
	if _, err = store.Query(ctx, "fine", "5s", at, at.Add(4*time.Hour+time.Second)); err == nil {
		t.Fatal("unbounded fine query accepted")
	}
	if _, err = db.DB.Exec(`INSERT INTO metrics_fine VALUES('fine','disk.read_bytes_per_second',?,1,1,1,1)`, at.Add(-25*time.Hour).Unix()); err != nil {
		t.Fatal(err)
	}
	if err = store.Cleanup(ctx, at.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	var count int
	if err = db.DB.QueryRow(`SELECT COUNT(*) FROM metrics_fine`).Scan(&count); err != nil || count != 12 {
		t.Fatal(count, err)
	}
	if _, err = db.DB.Exec(`DELETE FROM nodes WHERE id='fine'`); err != nil {
		t.Fatal(err)
	}
	if err = db.DB.QueryRow(`SELECT COUNT(*) FROM metrics_fine`).Scan(&count); err != nil || count != 0 {
		t.Fatal(count, err)
	}
}
