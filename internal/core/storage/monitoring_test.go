package storage

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
)

func TestMonitoringExpansionPreservesExistingRows(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	db, err := sql.Open("sqlite", filepath.Join(dir, "nodedance.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	before := strings.ReplaceAll(schemaSQL, ", 'icmp'", "")
	before = strings.ReplaceAll(before, ", 'asset_expiry', 'traffic_quota', 'metric', 'probe_latency', 'probe_failure'", "")
	for _, statement := range splitCurrentSchema(before) {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	_, err = db.ExecContext(ctx, `INSERT INTO nodes(id,display_name,status,created_at,updated_at) VALUES('11111111-1111-4111-8111-111111111111','kept','offline',1,1)`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.ExecContext(ctx, `INSERT INTO service_probes(id,node_id,name,kind,target,expected_http_status,interval_seconds,timeout_seconds,enabled,revision,status,next_due_at,created_at,updated_at) VALUES('22222222-2222-4222-8222-222222222222','11111111-1111-4111-8111-111111111111','existing','http','https://example.com',200,30,3,1,1,'unknown',1,1,1)`)
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	store, err := Open(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	var name string
	if err := store.DB.QueryRow(`SELECT name FROM service_probes`).Scan(&name); err != nil || name != "existing" {
		t.Fatal("probe lost", err)
	}
	_, err = store.DB.Exec(`UPDATE service_probes SET kind='icmp',target='icmp://localhost',expected_http_status=NULL`)
	if err != nil {
		t.Fatal("new ICMP constraint missing", err)
	}
	var foreignKeys int
	if err := store.DB.QueryRow(`PRAGMA foreign_keys`).Scan(&foreignKeys); err != nil || foreignKeys != 1 {
		t.Fatal("foreign keys not restored", err)
	}
	rows, err := store.DB.Query(`PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if rows.Next() {
		t.Fatal("foreign key references damaged")
	}
}
