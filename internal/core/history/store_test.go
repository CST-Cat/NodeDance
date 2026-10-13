package history_test

import (
	"context"
	"testing"
	"time"

	"github.com/CST-Cat/NodeDance/internal/core/history"
	"github.com/CST-Cat/NodeDance/internal/core/storage"
)

func TestQueryHonorsFractionalTimeBounds(t *testing.T) {
	ctx := context.Background()
	db, err := storage.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = db.DB.Exec(`INSERT INTO nodes(id,display_name,status,created_at,updated_at) VALUES('node','test','offline',0,0)`); err != nil {
		t.Fatal(err)
	}
	for _, resolution := range []string{"minute", "hour"} {
		t.Run(resolution, func(t *testing.T) {
			step := time.Minute
			if resolution == "hour" {
				step = time.Hour
			}
			base := time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)
			if _, err := db.DB.Exec("DELETE FROM metrics_minute"); err != nil {
				t.Fatal(err)
			}
			for i := 0; i < 2; i++ {
				if _, err := db.DB.Exec(`INSERT INTO metrics_minute VALUES('node','cpu.usage_percent',?,1,10,10,10)`, base.Add(time.Duration(i)*step).Unix()); err != nil {
					t.Fatal(err)
				}
			}
			from, to := base.Add(time.Millisecond), base.Add(step+time.Millisecond)
			for _, selected := range []bool{false, true} {
				s := history.Store{DB: db.DB}
				var result history.Response
				var err error
				if selected {
					result, err = s.QueryKeys(ctx, "node", resolution, from, to, []string{"cpu.usage_percent"})
				} else {
					result, err = s.Query(ctx, "node", resolution, from, to)
				}
				if err != nil {
					t.Fatal(err)
				}
				if len(result.Series) != 1 || len(result.Series[0].Points) != 1 || !result.Series[0].Points[0].BucketAt.Equal(base.Add(step)) {
					t.Fatalf("selected=%v: wrong [from,to) buckets: %+v", selected, result.Series)
				}
			}
		})
	}
}
