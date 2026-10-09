package storage

import (
	"context"
	"database/sql"
	"testing"
)

func TestOpenInitializesOnlyCurrentSchemaAndDoesNotResetIt(t *testing.T) {
	ctx := context.Background()
	directory := t.TempDir()
	store, err := Open(ctx, directory)
	if err != nil {
		t.Fatalf("open current-schema database: %v", err)
	}

	var tables []string
	rows, err := store.DB.QueryContext(ctx, `SELECT name FROM sqlite_master WHERE type='table' ORDER BY name`)
	if err != nil {
		_ = store.Close()
		t.Fatal(err)
	}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			_ = rows.Close()
			_ = store.Close()
			t.Fatal(err)
		}
		tables = append(tables, name)
	}
	if err := rows.Close(); err != nil {
		_ = store.Close()
		t.Fatal(err)
	}
	if err := rows.Err(); err != nil {
		_ = store.Close()
		t.Fatal(err)
	}
	seen := make(map[string]bool, len(tables))
	for _, name := range tables {
		seen[name] = true
		if name == "schema_migrations" || len(name) >= 9 && name[:9] == "archived_" {
			t.Errorf("current schema must not create version history or archived tables: %s", name)
		}
	}
	for _, name := range []string{
		"admin_user", "nodes", "agent_devices", "docker_node_state", "docker_containers",
		"metrics_minute", "metrics_hour", "dashboard_preferences", "dashboard_settings",
		"compose_projects", "core_tasks", "core_task_resource_claims", "core_task_audit_events",
	} {
		if !seen[name] {
			t.Errorf("current schema is missing required table %q", name)
		}
	}

	for table, wantColumns := range map[string][]string{
		"dashboard_preferences": {"group_name"},
		"dashboard_settings":    {"node_group_by", "node_sort_by"},
	} {
		columns, err := tableColumns(ctx, store.DB, table)
		if err != nil {
			_ = store.Close()
			t.Fatal(err)
		}
		for _, column := range wantColumns {
			if !columns[column] {
				t.Errorf("current %s schema is missing column %q", table, column)
			}
		}
	}

	if _, err := store.DB.ExecContext(ctx, `UPDATE dashboard_settings SET featured_limit=7 WHERE id=1`); err != nil {
		_ = store.Close()
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	store, err = Open(ctx, directory)
	if err != nil {
		t.Fatalf("reopen current-schema database: %v", err)
	}
	defer store.Close()
	var featuredLimit int
	if err := store.DB.QueryRowContext(ctx, `SELECT featured_limit FROM dashboard_settings WHERE id=1`).Scan(&featuredLimit); err != nil {
		t.Fatal(err)
	}
	if featuredLimit != 7 {
		t.Fatalf("reopening current schema reset stored data: featured_limit=%d, want 7", featuredLimit)
	}
}

func tableColumns(ctx context.Context, db *sql.DB, table string) (map[string]bool, error) {
	rows, err := db.QueryContext(ctx, `PRAGMA table_info("`+table+`")`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	columns := map[string]bool{}
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, columnType string
		var defaultValue sql.NullString
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			return nil, err
		}
		columns[name] = true
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return columns, nil
}
