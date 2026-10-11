package storage

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// Expand the current monitoring contract in place. This is a one-time schema
// change for new capabilities; no alternate runtime schema is retained.
func expandMonitoringConstraints(ctx context.Context, db *sql.DB) error {
	names := []string{}
	for _, table := range []string{"service_probes", "service_probe_runs", "alert_rules"} {
		var source string
		err := db.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&source)
		if err == sql.ErrNoRows {
			continue
		}
		if err != nil {
			return err
		}
		marker := "'icmp'"
		if table == "alert_rules" {
			marker = "'probe_packet_loss'"
		}
		if !strings.Contains(source, marker) {
			names = append(names, table)
		}
	}
	if len(names) == 0 {
		return nil
	}
	if _, err := db.ExecContext(ctx, `PRAGMA foreign_keys=OFF`); err != nil {
		return err
	}
	defer db.ExecContext(context.Background(), `PRAGMA foreign_keys=ON`)
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, table := range names {
		var definition string
		for _, statement := range splitCurrentSchema(schemaSQL) {
			if strings.HasPrefix(statement, "CREATE TABLE IF NOT EXISTS "+table+" (") {
				definition = statement
				break
			}
		}
		if definition == "" {
			return fmt.Errorf("missing schema for %s", table)
		}
		expanded := table + "_expanded"
		definition = strings.Replace(definition, "CREATE TABLE IF NOT EXISTS "+table+" (", "CREATE TABLE "+expanded+" (", 1)
		if _, err := tx.ExecContext(ctx, definition); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO "+expanded+" SELECT * FROM "+table); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "DROP TABLE "+table); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "ALTER TABLE "+expanded+" RENAME TO "+table); err != nil {
			return err
		}
	}
	rows, err := tx.QueryContext(ctx, `PRAGMA foreign_key_check`)
	if err != nil {
		return err
	}
	bad := rows.Next()
	rows.Close()
	if bad {
		return fmt.Errorf("monitoring expansion failed foreign key validation")
	}
	return tx.Commit()
}
