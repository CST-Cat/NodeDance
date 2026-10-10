package containerprefs

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// SQLiteMigrator moves standalone-container display preferences in the
// dashboard_preferences table. The identity key includes both the node ID
// and the full Docker container ID, so preferences cannot cross node bounds.
type SQLiteMigrator struct {
	DB *sql.DB
}

func NewSQLiteMigrator(db *sql.DB) *SQLiteMigrator {
	return &SQLiteMigrator{DB: db}
}

func (m *SQLiteMigrator) MigrateContainer(ctx context.Context, nodeID, fromID, toID string) error {
	from := Identity{NodeID: nodeID, ContainerID: fromID}
	to := Identity{NodeID: nodeID, ContainerID: toID}
	if m == nil || m.DB == nil {
		return errors.New("container preference SQLite database is unavailable")
	}
	if !validIdentity(from) || !validIdentity(to) {
		return errors.New("container preference migration identity is invalid")
	}
	if fromID == toID {
		return nil
	}

	fromKey := "container:" + fromID
	toKey := "container:" + toID
	tx, err := m.DB.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin container preference migration: %w", err)
	}
	defer tx.Rollback()

	oldValue, hasOld, err := readPreference(ctx, tx, nodeID, fromKey)
	if err != nil {
		return fmt.Errorf("read old container preference: %w", err)
	}
	if !hasOld {
		// No source preference means there is nothing to move. This also makes
		// replay after a prior successful identity move a no-op.
		return tx.Commit()
	}

	newValue, hasNew, err := readPreference(ctx, tx, nodeID, toKey)
	if err != nil {
		return fmt.Errorf("read replacement container preference: %w", err)
	}
	if hasNew {
		if newValue != oldValue {
			return ErrIdentityConflict
		}
		// A retry may observe both rows if an earlier implementation copied
		// instead of moving. Keep the replacement and remove only the identical
		// source row, in this same transaction.
		if _, err := tx.ExecContext(ctx, `DELETE FROM dashboard_preferences
			WHERE node_id=? AND target_kind='container' AND identity_key=?`, nodeID, fromKey); err != nil {
			return fmt.Errorf("remove migrated container preference source: %w", err)
		}
		return tx.Commit()
	}

	result, err := tx.ExecContext(ctx, `UPDATE dashboard_preferences SET identity_key=?
		WHERE node_id=? AND target_kind='container' AND identity_key=?`, toKey, nodeID, fromKey)
	if err != nil {
		return fmt.Errorf("move container preference identity: %w", err)
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("confirm container preference identity move: %w", err)
	}
	if updated != 1 {
		return errors.New("container preference source changed during identity move")
	}
	return tx.Commit()
}

func readPreference(ctx context.Context, tx *sql.Tx, nodeID, identityKey string) (Preferences, bool, error) {
	var value Preferences
	var visible, pinned int
	err := tx.QueryRowContext(ctx, `SELECT alias, icon, notes, service_url, sort_order, visible, pinned
		FROM dashboard_preferences WHERE node_id=? AND target_kind='container' AND identity_key=?`, nodeID, identityKey).
		Scan(&value.Alias, &value.Icon, &value.Note, &value.ServiceURL, &value.Order, &visible, &pinned)
	if errors.Is(err, sql.ErrNoRows) {
		return Preferences{}, false, nil
	}
	if err != nil {
		return Preferences{}, false, err
	}
	value.Visible = visible == 1
	value.Pinned = pinned == 1
	return value, true, nil
}
