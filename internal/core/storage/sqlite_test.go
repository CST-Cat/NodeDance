package storage

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/CST-Cat/NodeDance/internal/core/backup"
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
	if err := upgraded.DB.QueryRow(`SELECT MAX(version) FROM schema_migrations`).Scan(&version); err != nil || version != 14 {
		t.Fatalf("reopened database migration version=%d err=%v; want v14", version, err)
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

func TestCoreTaskV14MigrationPreservesRowsAndFailsClosedForAmbiguousLegacyPulls(t *testing.T) {
	ctx := context.Background()
	directory := filepath.Join(t.TempDir(), "core-data")
	v13, err := OpenWithMigrations(ctx, directory, migrations[:13])
	if err != nil {
		t.Fatal("open v13 database:", err)
	}
	const nodeID = "00000000-0000-4000-8000-000000000051"
	const taskID = "v13-existing-task"
	const targetID = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const legacyPullID = "v13-queued-image-pull"
	const legacyPullTarget = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	const sentPullID = "v13-sent-image-pull"
	if _, err := v13.DB.Exec(`INSERT INTO nodes(id,display_name,status,created_at,updated_at) VALUES(?, 'v13 migration node', 'pending', 1, 1)`, nodeID); err != nil {
		t.Fatal("insert v13 node:", err)
	}
	if _, err := v13.DB.Exec(`INSERT INTO core_tasks(task_id,node_id,idempotency_key,request_digest,accepted_generation,target_id,resource_key,action,intent_json,status,created_at_ns,updated_at_ns)
		VALUES(?,?,?,zeroblob(32),0,?,?,'restart',?,'queued',1,2)`, taskID, nodeID, "v13-task-key", targetID, "docker-container:"+targetID,
		`{"action":"restart","containerId":"`+targetID+`"}`); err != nil {
		t.Fatal("insert v13 task row:", err)
	}
	if _, err := v13.DB.Exec(`INSERT INTO core_task_resource_claims(node_id,resource_key,task_id) VALUES(?,?,?)`, nodeID, "docker-container:"+targetID, taskID); err != nil {
		t.Fatal("insert v13 resource claim:", err)
	}
	if _, err := v13.DB.Exec(`INSERT INTO core_task_audit_events(node_id,task_id,event,from_status,to_status,occurred_at_ns)
		VALUES(?,?,'accepted',NULL,'queued',1)`, nodeID, taskID); err != nil {
		t.Fatal("insert v13 task audit:", err)
	}
	for _, value := range []struct {
		taskID, key, target, resource, delivery string
		committed                               int
	}{{legacyPullID, "v13-pull-key", legacyPullTarget, "docker-image:" + legacyPullTarget, "ready", 0},
		{sentPullID, "v13-sent-pull-key", "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc", "docker-image:sent", "sent", 1}} {
		if _, err := v13.DB.Exec(`INSERT INTO core_tasks(task_id,node_id,idempotency_key,request_digest,accepted_generation,target_id,resource_key,action,intent_json,status,delivery_state,delivery_committed,created_at_ns,updated_at_ns)
			VALUES(?,?,?,zeroblob(32),0,?,?,'image_pull','{}','queued',?,?,3,4)`, value.taskID, nodeID, value.key, value.target, value.resource, value.delivery, value.committed); err != nil {
			t.Fatalf("insert v13 image-pull task %q: %v", value.taskID, err)
		}
	}
	if err := v13.Close(); err != nil {
		t.Fatal("close v13 database:", err)
	}

	upgraded, err := Open(ctx, directory)
	if err != nil {
		t.Fatal("apply v14 migration:", err)
	}
	defer upgraded.Close()
	var storedNode, storedKey, storedTarget, storedResource, status string
	var created, updated int64
	var authRequired int
	if err := upgraded.DB.QueryRow(`SELECT node_id,idempotency_key,target_id,resource_key,status,created_at_ns,updated_at_ns,registry_auth_required
		FROM core_tasks WHERE task_id=?`, taskID).Scan(&storedNode, &storedKey, &storedTarget, &storedResource, &status, &created, &updated, &authRequired); err != nil {
		t.Fatal("read preserved v13 task after migration:", err)
	}
	if storedNode != nodeID || storedKey != "v13-task-key" || storedTarget != targetID || storedResource != "docker-container:"+targetID ||
		status != "queued" || created != 1 || updated != 2 || authRequired != 0 {
		t.Fatalf("v13 task changed after migration: node=%q key=%q target=%q resource=%q status=%q times=%d/%d auth=%d", storedNode, storedKey, storedTarget, storedResource, status, created, updated, authRequired)
	}
	var claims, audits int
	if err := upgraded.DB.QueryRow(`SELECT count(*) FROM core_task_resource_claims WHERE task_id=?`, taskID).Scan(&claims); err != nil {
		t.Fatal("count preserved v13 task claim:", err)
	}
	if err := upgraded.DB.QueryRow(`SELECT count(*) FROM core_task_audit_events WHERE task_id=?`, taskID).Scan(&audits); err != nil {
		t.Fatal("count preserved v13 task audit:", err)
	}
	if claims != 1 || audits != 1 {
		t.Fatalf("v13 task claim/audit rows after migration = %d/%d, want 1/1", claims, audits)
	}
	// V13 never persisted whether these pulls had Registry credentials. The
	// queued legacy pull must remain intact but fail closed, so an old public
	// pull may need a fresh user submission after upgrade. A sent row must keep
	// its committed-delivery evidence and remain on reconciliation rules.
	var legacyStatus, legacyDelivery string
	var legacyRequired, sentRequired, sentCommitted int
	if err := upgraded.DB.QueryRow(`SELECT status,delivery_state,registry_auth_required FROM core_tasks WHERE task_id=?`, legacyPullID).
		Scan(&legacyStatus, &legacyDelivery, &legacyRequired); err != nil {
		t.Fatal("read v13 queued image pull after migration:", err)
	}
	if legacyStatus != "queued" || legacyDelivery != "ready" || legacyRequired != 1 {
		t.Fatalf("ambiguous legacy queued image pull was not preserved fail-closed: status=%q delivery=%q auth_required=%d", legacyStatus, legacyDelivery, legacyRequired)
	}
	if err := upgraded.DB.QueryRow(`SELECT registry_auth_required,delivery_committed FROM core_tasks WHERE task_id=?`, sentPullID).
		Scan(&sentRequired, &sentCommitted); err != nil {
		t.Fatal("read v13 already-committed image pull after migration:", err)
	}
	if sentRequired != 0 || sentCommitted != 1 {
		t.Fatalf("v13 already-committed image pull was altered: auth_required=%d delivery_committed=%d", sentRequired, sentCommitted)
	}
}

func TestFileTaskV13MigrationPreservesRowsAndAddsIdempotencyAndCanceled(t *testing.T) {
	ctx := context.Background()
	directory := filepath.Join(t.TempDir(), "core-data")
	v12, err := OpenWithMigrations(ctx, directory, migrations[:12])
	if err != nil {
		t.Fatal("open v12 database:", err)
	}
	const nodeID = "00000000-0000-4000-8000-000000000041"
	const taskID = "00000000-0000-4000-8000-000000000042"
	if _, err := v12.DB.Exec(`INSERT INTO nodes(id,display_name,status,created_at,updated_at) VALUES(?, 'v12 migration node', 'pending', 1, 1)`, nodeID); err != nil {
		t.Fatal("insert v12 node:", err)
	}
	if _, err := v12.DB.Exec(`INSERT INTO file_write_tasks(task_id,node_id,operation,target_path,status,result_code,created_at_ns,updated_at_ns)
		VALUES(?,?,'delete','/tmp/legacy','running','',1,1)`, taskID, nodeID); err != nil {
		t.Fatal("insert v12 file task:", err)
	}
	if _, err := v12.DB.Exec(`INSERT INTO file_write_task_events(node_id,task_id,event,to_status,occurred_at_ns)
		VALUES(?,?,'accepted','queued',1)`, nodeID, taskID); err != nil {
		t.Fatal("insert v12 task event:", err)
	}
	if err := v12.Close(); err != nil {
		t.Fatal("close v12 database:", err)
	}

	upgraded, err := Open(ctx, directory)
	if err != nil {
		t.Fatal("apply v13 file task migration:", err)
	}
	defer upgraded.Close()
	var target, status string
	if err := upgraded.DB.QueryRow(`SELECT target_path,status FROM file_write_tasks WHERE task_id=?`, taskID).Scan(&target, &status); err != nil || target != "/tmp/legacy" || status != "running" {
		t.Fatalf("v12 task after migration target=%q status=%q err=%v", target, status, err)
	}
	var events int
	if err := upgraded.DB.QueryRow(`SELECT count(*) FROM file_write_task_events WHERE task_id=?`, taskID).Scan(&events); err != nil || events != 1 {
		t.Fatalf("v12 task event count after migration=%d err=%v", events, err)
	}
	if _, err := upgraded.DB.Exec(`UPDATE file_write_tasks SET status='canceled',result_code='canceled_before_dispatch',finished_at_ns=2 WHERE task_id=?`, taskID); err != nil {
		t.Fatalf("v13 schema rejected canceled status: %v", err)
	}
	if _, err := upgraded.DB.Exec(`UPDATE file_write_tasks SET idempotency_key='same-key',request_digest=lower(hex(zeroblob(32))) WHERE task_id=?`, taskID); err != nil {
		t.Fatalf("v13 schema rejected idempotency fields: %v", err)
	}
}

func TestBackupRestoresV10DatabaseAfterV11MigrationCollision(t *testing.T) {
	ctx := context.Background()
	dataDir := filepath.Join(t.TempDir(), "pre-upgrade-core-data")
	legacy, err := OpenWithMigrations(ctx, dataDir, migrations[:10])
	if err != nil {
		t.Fatal("open v10 database:", err)
	}
	defer legacy.Close()
	if _, err := legacy.DB.ExecContext(ctx, `PRAGMA wal_autocheckpoint=0`); err != nil {
		t.Fatal("disable automatic WAL checkpoint:", err)
	}
	const nodeID = "00000000-0000-4000-8000-0000000000a1"
	if _, err := legacy.DB.ExecContext(ctx, `INSERT INTO nodes(id, display_name, status, created_at, updated_at)
		VALUES(?, 'retained recovery node', 'offline', 100, 200)`, nodeID); err != nil {
		t.Fatal("insert retained v10 row:", err)
	}
	walPath := filepath.Join(dataDir, "nodedance.sqlite-wal")
	walInfo, err := os.Stat(walPath)
	if err != nil || walInfo.Size() == 0 {
		t.Fatalf("v10 fixture did not retain committed data in SQLite WAL: info=%v err=%v", walInfo, err)
	}
	for name, contents := range map[string][]byte{
		"alert-channel-encryption.key": []byte("0123456789abcdef0123456789abcdef"),
		"csrf-signing.key":             []byte("fedcba9876543210fedcba9876543210"),
	} {
		if err := os.WriteFile(filepath.Join(dataDir, name), contents, 0o600); err != nil {
			t.Fatalf("write required backup file %s: %v", name, err)
		}
	}
	archive := filepath.Join(t.TempDir(), "pre-upgrade-core-data.backup")
	if err := backup.Create(ctx, dataDir, archive); err != nil {
		t.Fatalf("create pre-upgrade backup from live WAL database: %v", err)
	}

	// Collide with the fourth v11 index so the first three index statements
	// execute inside the migration transaction before SQLite rejects the name.
	if _, err := legacy.DB.ExecContext(ctx, `CREATE INDEX compose_operations_retention ON compose_operations(status)`); err != nil {
		t.Fatal("inject v11 index-name collision:", err)
	}
	type ledgerEntry struct {
		version   int
		appliedAt int64
	}
	readLedger := func(db *sql.DB) []ledgerEntry {
		t.Helper()
		rows, err := db.QueryContext(ctx, `SELECT version, applied_at FROM schema_migrations ORDER BY version`)
		if err != nil {
			t.Fatal("read schema migration ledger:", err)
		}
		defer rows.Close()
		var entries []ledgerEntry
		for rows.Next() {
			var entry ledgerEntry
			if err := rows.Scan(&entry.version, &entry.appliedAt); err != nil {
				t.Fatal("scan schema migration ledger:", err)
			}
			entries = append(entries, entry)
		}
		if err := rows.Err(); err != nil {
			t.Fatal("iterate schema migration ledger:", err)
		}
		return entries
	}
	ledgerBefore := readLedger(legacy.DB)
	if len(ledgerBefore) != 10 || ledgerBefore[len(ledgerBefore)-1].version != 10 {
		t.Fatalf("fixture schema ledger = %+v; want versions 1 through 10", ledgerBefore)
	}
	if err := legacy.Close(); err != nil {
		t.Fatal("close v10 store before upgrade attempt:", err)
	}

	if _, err := Open(ctx, dataDir); err == nil {
		t.Fatal("current schema unexpectedly opened despite the v11 index-name collision")
	}

	check, err := sql.Open("sqlite", filepath.Join(dataDir, "nodedance.sqlite"))
	if err != nil {
		t.Fatal("open database to inspect failed upgrade:", err)
	}
	check.SetMaxOpenConns(1)
	var retainedName string
	if err := check.QueryRowContext(ctx, `SELECT display_name FROM nodes WHERE id=?`, nodeID).Scan(&retainedName); err != nil || retainedName != "retained recovery node" {
		t.Fatalf("retained row after failed migration = %q, err=%v", retainedName, err)
	}
	ledgerAfter := readLedger(check)
	if !slices.Equal(ledgerAfter, ledgerBefore) {
		t.Fatalf("schema ledger changed after failed v11 migration: before=%+v after=%+v", ledgerBefore, ledgerAfter)
	}
	for _, index := range []string{"audit_entries_retention", "core_task_audit_events_retention", "core_tasks_retention"} {
		var count int
		if err := check.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type='index' AND name=?`, index).Scan(&count); err != nil || count != 0 {
			t.Fatalf("partial v11 index %q survived failed migration: count=%d err=%v", index, count, err)
		}
	}
	if err := check.Close(); err != nil {
		t.Fatal("close failed-upgrade inspection database:", err)
	}

	restoredDir := filepath.Join(t.TempDir(), "recovered-core-data")
	if err := backup.Restore(ctx, archive, restoredDir); err != nil {
		t.Fatalf("restore pre-upgrade backup to fresh directory: %v", err)
	}
	recovered, err := Open(ctx, restoredDir)
	if err != nil {
		t.Fatalf("open restored pre-upgrade database with current migrations: %v", err)
	}
	defer recovered.Close()
	if err := recovered.DB.QueryRowContext(ctx, `SELECT display_name FROM nodes WHERE id=?`, nodeID).Scan(&retainedName); err != nil || retainedName != "retained recovery node" {
		t.Fatalf("retained row after recovery = %q, err=%v", retainedName, err)
	}
	var currentVersion int
	if err := recovered.DB.QueryRowContext(ctx, `SELECT MAX(version) FROM schema_migrations`).Scan(&currentVersion); err != nil || currentVersion != migrations[len(migrations)-1].Version {
		t.Fatalf("restored schema version=%d err=%v; want current version %d", currentVersion, err, migrations[len(migrations)-1].Version)
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
	var schemaVersion int
	if err := store.DB.QueryRow(`SELECT max(version) FROM schema_migrations`).Scan(&schemaVersion); err != nil || schemaVersion != 14 {
		t.Fatalf("schema version=%d err=%v, want integration schema version 14", schemaVersion, err)
	}
	for _, index := range []string{"audit_entries_retention", "core_task_audit_events_retention", "core_tasks_retention", "compose_operations_retention", "compose_editor_operations_retention", "service_probe_runs_retention", "alerts_retention", "alert_events_retention", "alert_deliveries_retention", "alert_windows_retention", "agent_update_tasks_retention", "file_write_tasks_retention", "file_write_tasks_idempotency"} {
		var count int
		if err := store.DB.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='index' AND name=?`, index).Scan(&count); err != nil || count != 1 {
			t.Errorf("retention index %q present=%d err=%v", index, count, err)
		}
	}
	for _, table := range []string{"compose_projects", "compose_operations", "compose_editor_operations", "compose_editor_events", "service_probes", "service_probe_runs", "alert_rules", "alert_rule_state", "alerts", "alert_events", "alert_channels", "alert_deliveries", "alert_windows", "agent_update_releases", "agent_update_settings", "agent_update_tasks", "file_write_tasks", "file_write_task_events"} {
		var count int
		if err := store.DB.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&count); err != nil || count != 1 {
			t.Errorf("table %q present=%d err=%v", table, count, err)
		}
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
