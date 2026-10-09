package storage

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

func TestMigrationsAreOrderedAndAtomic(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "nodedance.sqlite")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE original_data(value TEXT NOT NULL); INSERT INTO original_data(value) VALUES('preserve')`); err != nil {
		t.Fatal(err)
	}
	migrations := []Migration{{Version: 1, SQL: []string{
		`CREATE TABLE migration_created(value TEXT NOT NULL)`,
		`INSERT INTO table_that_does_not_exist(value) VALUES('fail')`,
	}}}
	err = ApplyMigrations(context.Background(), db, migrations)
	if err == nil {
		t.Fatal("expected migration failure")
	}
	var original string
	if err := db.QueryRow(`SELECT value FROM original_data`).Scan(&original); err != nil || original != "preserve" {
		t.Fatalf("original row = %q, err=%v", original, err)
	}
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name='migration_created'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("partial migration table count=%d, err=%v", count, err)
	}
	var applied int
	err = db.QueryRow(`SELECT 1 FROM schema_migrations WHERE version=1`).Scan(&applied)
	if !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("failed migration was recorded: value=%d err=%v", applied, err)
	}
	_ = db.Close()
}

func TestVersionFourDatabaseUpgradesToDashboardAndHistorySchemaOnReopen(t *testing.T) {
	ctx := context.Background()
	directory := filepath.Join(t.TempDir(), "core-data")
	versionFour, err := OpenWithMigrations(ctx, directory, migrations[:4])
	if err != nil {
		t.Fatal("open v4 database:", err)
	}
	var version int
	if err := versionFour.DB.QueryRow(`SELECT MAX(version) FROM schema_migrations`).Scan(&version); err != nil || version != 4 {
		t.Fatalf("initial database migration version=%d err=%v; want v4", version, err)
	}
	if err := versionFour.Close(); err != nil {
		t.Fatal("close v4 database:", err)
	}

	upgraded, err := Open(ctx, directory)
	if err != nil {
		t.Fatal("reopen v4 database with current migrations:", err)
	}
	defer upgraded.Close()
	if err := upgraded.DB.QueryRow(`SELECT MAX(version) FROM schema_migrations`).Scan(&version); err != nil || version != 5 {
		t.Fatalf("reopened database migration version=%d err=%v; want v5", version, err)
	}
	for _, table := range []string{"dashboard_preferences", "dashboard_settings", "metrics_minute", "metrics_hour"} {
		var count int
		if err := upgraded.DB.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&count); err != nil || count != 1 {
			t.Fatalf("v5 table %q count=%d err=%v", table, count, err)
		}
	}
	var mode, grouping, sorting, fields string
	var featured int
	if err := upgraded.DB.QueryRow(`SELECT view_mode, group_by, sort_by, featured_limit, custom_fields_json FROM dashboard_settings WHERE id=1`).Scan(&mode, &grouping, &sorting, &featured, &fields); err != nil {
		t.Fatal("read migrated default dashboard settings:", err)
	}
	if mode != "monitor" || grouping != "node" || sorting != "custom" || featured != 4 || fields != `["state","ports","health"]` {
		t.Fatalf("unexpected migrated dashboard defaults: mode=%q group=%q sort=%q featured=%d fields=%q", mode, grouping, sorting, featured, fields)
	}
}

func TestMigrationListValidatedBeforeAnyDDL(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "invalid-list.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	err = ApplyMigrations(context.Background(), db, []Migration{
		{Version: 1, SQL: []string{`CREATE TABLE should_not_exist(id INTEGER)`}},
		{Version: 3, SQL: []string{`CREATE TABLE later(id INTEGER)`}},
	})
	if err == nil {
		t.Fatal("expected a non-contiguous migration list to fail")
	}
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name IN ('schema_migrations', 'should_not_exist')`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("invalid migration list created %d tables before rejection", count)
	}
}

func TestFutureOrInvalidMigrationLedgerIsRejected(t *testing.T) {
	for _, versions := range [][]int{{1, 2}, {-1}, {2}} {
		db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "future.sqlite"))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`CREATE TABLE schema_migrations(version INTEGER PRIMARY KEY, applied_at INTEGER NOT NULL)`); err != nil {
			t.Fatal(err)
		}
		for _, version := range versions {
			if _, err := db.Exec(`INSERT INTO schema_migrations(version, applied_at) VALUES(?, 1)`, version); err != nil {
				t.Fatal(err)
			}
		}
		err = ApplyMigrations(context.Background(), db, []Migration{{Version: 1, SQL: []string{`CREATE TABLE should_not_exist(id INTEGER)`}}})
		if err == nil {
			t.Errorf("ledger %v was accepted", versions)
		}
		var count int
		if scanErr := db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name='should_not_exist'`).Scan(&count); scanErr != nil {
			t.Fatal(scanErr)
		}
		if count != 0 {
			t.Errorf("ledger %v was rejected after DDL ran", versions)
		}
		_ = db.Close()
	}
}

func TestOpenRejectsFutureSchemaBeforeEnablingWAL(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "future-data")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	database := filepath.Join(dir, "nodedance.sqlite")
	db, err := sql.Open("sqlite", database)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE schema_migrations(version INTEGER PRIMARY KEY, applied_at INTEGER NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO schema_migrations(version, applied_at) VALUES(2, 1)`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenWithMigrations(context.Background(), dir, []Migration{{Version: 1, SQL: []string{`CREATE TABLE should_not_exist(id INTEGER)`}}}); err == nil {
		t.Fatal("future schema was accepted")
	}
	check, err := sql.Open("sqlite", database)
	if err != nil {
		t.Fatal(err)
	}
	defer check.Close()
	var mode string
	if err := check.QueryRow(`PRAGMA journal_mode`).Scan(&mode); err != nil {
		t.Fatal(err)
	}
	if mode != "delete" {
		t.Fatalf("rejected future database was changed to journal_mode=%q", mode)
	}
	var version int
	if err := check.QueryRow(`SELECT version FROM schema_migrations`).Scan(&version); err != nil || version != 2 {
		t.Fatalf("future ledger version=%d err=%v", version, err)
	}
}

func TestOpenEnablesWALAndRestrictsPermissions(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	store, err := Open(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	var mode string
	if err := store.DB.QueryRow(`PRAGMA journal_mode`).Scan(&mode); err != nil || mode != "wal" {
		t.Fatalf("journal_mode=%q err=%v", mode, err)
	}
	for path, want := range map[string]os.FileMode{dir: 0o700, filepath.Join(dir, "nodedance.sqlite"): 0o600} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != want {
			t.Errorf("%s permissions=%#o want %#o", path, got, want)
		}
	}
}
