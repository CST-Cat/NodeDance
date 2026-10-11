package storage

import (
	"context"
	"testing"
	"time"
)

func TestSQLiteLimitsSurviveConnectionReplacement(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	check := func() {
		t.Helper()
		for pragma, want := range map[string]int{"foreign_keys": 1, "synchronous": 2, "busy_timeout": 5000, "cache_size": -2048, "mmap_size": 0, "temp_store": 1, "wal_autocheckpoint": 256, "journal_size_limit": 1048576} {
			var got int
			if err := store.DB.QueryRowContext(ctx, "PRAGMA "+pragma).Scan(&got); err != nil {
				t.Fatal(err)
			}
			if got != want {
				t.Fatalf("%s=%d; want %d", pragma, got, want)
			}
		}
	}
	check()
	store.DB.SetConnMaxLifetime(time.Nanosecond)
	check()
	var mode string
	if err := store.DB.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&mode); err != nil || mode != "wal" {
		t.Fatalf("journal mode %s: %v", mode, err)
	}
}
