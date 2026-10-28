package storage

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/CST-Cat/NodeDance/internal/core/dashboard"
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
		"users", "nodes", "agent_devices", "docker_node_state", "docker_containers",
		"metrics_fine", "metrics_minute", "metrics_hour", "dashboard_preferences", "node_connection_preferences", "node_country_preferences", "dashboard_settings",
		"compose_projects", "compose_project_preferences", "core_tasks", "core_task_resource_claims", "core_task_audit_events",
	} {
		if !seen[name] {
			t.Errorf("current schema is missing required table %q", name)
		}
	}

	for table, wantColumns := range map[string][]string{
		"dashboard_preferences": {"group_name"},
		"dashboard_settings":    {"node_group_by", "node_sort_by"},
		"appearance":            {"sharks_json"},
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

	if _, err := store.DB.ExecContext(ctx, `UPDATE dashboard_settings SET node_sort_by='name' WHERE id=1`); err != nil {
		_ = store.Close()
		t.Fatal(err)
	}
	nodeID := "11111111-1111-4111-8111-111111111111"
	if _, err := store.DB.ExecContext(ctx, `INSERT INTO nodes(id, display_name, status, created_at, updated_at) VALUES(?, ?, 'offline', 1, 1)`, nodeID, "offline host"); err != nil {
		_ = store.Close()
		t.Fatal(err)
	}
	preferences := dashboard.Repository{DB: store.DB}
	if err := preferences.Put(ctx, dashboard.Preference{NodeID: nodeID, TargetKind: "node", Identity: dashboard.NodeIdentity(nodeID),
		SSHHost: "offline.example.net", SSHPort: 2222, SSHUser: "ops", Visible: true, Pinned: true}); err != nil {
		_ = store.Close()
		t.Fatalf("save node SSH preferences: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	store, err = Open(ctx, directory)
	if err != nil {
		t.Fatalf("reopen current-schema database: %v", err)
	}
	defer store.Close()
	var nodeSortBy string
	if err := store.DB.QueryRowContext(ctx, `SELECT node_sort_by FROM dashboard_settings WHERE id=1`).Scan(&nodeSortBy); err != nil {
		t.Fatal(err)
	}
	if nodeSortBy != "name" {
		t.Fatalf("reopening current schema reset stored data: node_sort_by=%s, want name", nodeSortBy)
	}
	items, err := (dashboard.Repository{DB: store.DB}).List(ctx, nodeID)
	if err != nil || len(items) != 1 {
		t.Fatalf("read persisted node preferences: items=%#v err=%v", items, err)
	}
	if items[0].SSHHost != "offline.example.net" || items[0].SSHPort != 2222 || items[0].SSHUser != "ops" {
		t.Fatalf("reopening current schema lost SSH connection settings: %#v", items[0])
	}
}

func TestOpenAddsSharkSettingsToExistingAppearanceRow(t *testing.T) {
	ctx := context.Background()
	directory := t.TempDir()
	db, err := sql.Open("sqlite", filepath.Join(directory, "nodedance.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.ExecContext(ctx, `CREATE TABLE appearance (
		id INTEGER PRIMARY KEY CHECK (id = 1),
		display_name TEXT NOT NULL,
		theme TEXT NOT NULL,
		background_color TEXT NOT NULL,
		avatar_png BLOB,
		background_png BLOB,
		updated_at INTEGER NOT NULL
	)`)
	if err == nil {
		_, err = db.ExecContext(ctx, `INSERT INTO appearance(id, display_name, theme, background_color, updated_at) VALUES(1, 'Existing', 'system', '#101827', 1)`)
	}
	if closeErr := db.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		t.Fatal(err)
	}

	store, err := Open(ctx, directory)
	if err != nil {
		t.Fatalf("open existing appearance settings: %v", err)
	}
	defer store.Close()
	var name, sharks string
	if err := store.DB.QueryRowContext(ctx, `SELECT display_name, sharks_json FROM appearance WHERE id=1`).Scan(&name, &sharks); err != nil {
		t.Fatal(err)
	}
	if name != "Existing" || sharks != `[{"kind":"classic","size":1},{"kind":"classic","size":0.7},{"kind":"classic","size":0.84},{"kind":"classic","size":0.77}]` {
		t.Fatalf("existing appearance after adding shark settings: name=%q sharks=%s", name, sharks)
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
