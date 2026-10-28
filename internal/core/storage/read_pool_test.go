package storage

import (
	"context"
	"testing"
)

func TestSQLiteReadPoolRejectsWritesAndSeesCommittedData(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if store.ReadDB == nil {
		t.Fatal("read pool was not opened")
	}
	if store.Reader() != store.ReadDB {
		t.Fatal("Reader() did not return the read pool")
	}
	var queryOnly int
	if err := store.Reader().QueryRowContext(ctx, "PRAGMA query_only").Scan(&queryOnly); err != nil {
		t.Fatal(err)
	}
	if queryOnly != 1 {
		t.Fatalf("query_only=%d; want 1", queryOnly)
	}
	if _, err := store.Reader().ExecContext(ctx, `CREATE TABLE read_pool_probe (id INTEGER)`); err == nil {
		t.Fatal("write through the read pool succeeded")
	}
	if _, err := store.DB.ExecContext(ctx, `CREATE TABLE read_pool_probe (id INTEGER); INSERT INTO read_pool_probe VALUES (7)`); err != nil {
		t.Fatal(err)
	}
	var value int
	if err := store.Reader().QueryRowContext(ctx, `SELECT id FROM read_pool_probe`).Scan(&value); err != nil || value != 7 {
		t.Fatalf("committed write is not visible: %d, %v", value, err)
	}
	if _, err := store.DB.ExecContext(ctx, `DELETE FROM read_pool_probe`); err != nil {
		t.Fatal(err)
	}
	if err := store.Reader().QueryRowContext(ctx, `SELECT COUNT(*) FROM read_pool_probe`).Scan(&value); err != nil || value != 0 {
		t.Fatalf("committed deletion is not visible: %d, %v", value, err)
	}
	var tables int
	if err := store.Reader().QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='nodes'`).Scan(&tables); err != nil {
		t.Fatal(err)
	}
	if tables != 1 {
		t.Fatalf("nodes table visibility through read pool: %d", tables)
	}
	var mode string
	if err := store.Reader().QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&mode); err != nil || mode != "wal" {
		t.Fatalf("read pool journal mode %s: %v", mode, err)
	}
}
