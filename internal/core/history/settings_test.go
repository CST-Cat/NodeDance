package history_test

import (
	"context"
	"errors"
	"github.com/CST-Cat/NodeDance/internal/core/history"
	"github.com/CST-Cat/NodeDance/internal/core/storage"
	"testing"
	"time"
)

func TestRetentionSettingsExpireHistoryAtomically(t *testing.T) {
	ctx := context.Background()
	db, err := storage.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s := history.Store{DB: db.DB}
	policy, err := s.Settings(ctx)
	if err != nil || policy.RetentionDays != 365 {
		t.Fatalf("default policy: %+v %v", policy, err)
	}
	if _, err := db.DB.Exec(`INSERT INTO nodes(id,display_name,status,created_at,updated_at) VALUES('node','test','offline',0,0)`); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)
	for _, table := range []string{"metrics_minute", "metrics_hour", "metrics_fine"} {
		for _, age := range []time.Duration{time.Hour, 2 * 24 * time.Hour} {
			if _, err := db.DB.Exec(`INSERT INTO `+table+` VALUES('node','cpu.usage_percent',?,1,20,10,30)`, now.Add(-age).Unix()); err != nil {
				t.Fatal(err)
			}
		}
	}
	rollupAt := now.Add(-45 * 24 * time.Hour).Truncate(time.Hour)
	if _, err := db.DB.Exec(`INSERT INTO metrics_minute VALUES('node','cpu.usage_percent',?,2,60,20,40)`, rollupAt.Unix()); err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB.Exec(`INSERT INTO metrics_hour VALUES('node','cpu.usage_percent',?,3,90,10,50)`, rollupAt.Unix()); err != nil {
		t.Fatal(err)
	}
	for pass := 0; pass < 2; pass++ {
		if err := s.Cleanup(ctx, now); err != nil {
			t.Fatal(err)
		}
		result, err := s.Query(ctx, "node", "hour", rollupAt, rollupAt.Add(time.Hour))
		if err != nil || len(result.Series) != 1 || len(result.Series[0].Points) != 1 {
			t.Fatalf("rollup query: %+v %v", result, err)
		}
		point := result.Series[0].Points[0]
		if point.Samples != 5 || point.Value != 30 || point.Minimum != 10 || point.Maximum != 50 {
			t.Fatalf("cleanup lost weighted history or counted it twice: %+v", point)
		}
	}
	if err := s.SaveSettings(ctx, history.Settings{RetentionDays: 1}, now); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"metrics_minute", "metrics_hour", "metrics_fine"} {
		var count int
		if err := db.DB.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&count); err != nil || count != 1 {
			t.Fatalf("%s count %d: %v", table, count, err)
		}
	}
	if err := s.SaveSettings(ctx, history.Settings{RetentionDays: 0}, now); !errors.Is(err, history.ErrInvalidSettings) {
		t.Fatalf("invalid policy: %v", err)
	}
	if _, err := db.DB.Exec(`CREATE TRIGGER fail_retention BEFORE DELETE ON metrics_fine BEGIN SELECT RAISE(ABORT,'fixture'); END`); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveSettings(ctx, history.Settings{RetentionDays: 7}, now.Add(3*24*time.Hour)); err == nil {
		t.Fatal("cleanup failure was ignored")
	}
	reopened := history.Store{DB: db.DB}
	policy, err = reopened.Settings(ctx)
	if err != nil || policy.RetentionDays != 1 {
		t.Fatalf("failed save changed policy: %+v %v", policy, err)
	}
	var count int
	if err := db.DB.QueryRow(`SELECT COUNT(*) FROM metrics_minute`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("failed cleanup was not rolled back: %d %v", count, err)
	}
}
