package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"time"

	corecompose "github.com/CST-Cat/NodeDance/internal/core/compose"
	corecomposeedit "github.com/CST-Cat/NodeDance/internal/core/composeedit"
	coretasks "github.com/CST-Cat/NodeDance/internal/core/tasks"

	_ "modernc.org/sqlite"
)

type Migration struct {
	Version int
	SQL     []string
}

type Store struct {
	DB  *sql.DB
	Dir string
}

var migrations = []Migration{{
	Version: 1,
	SQL: []string{
		`CREATE TABLE admin_user (
			id INTEGER PRIMARY KEY CHECK (id = 1),
			password_hash BLOB NOT NULL,
			password_salt BLOB NOT NULL,
			password_version TEXT NOT NULL,
			display_name TEXT NOT NULL,
			updated_at INTEGER NOT NULL
		)`,
		`CREATE TABLE init_credential (
			id INTEGER PRIMARY KEY CHECK (id = 1),
			digest BLOB NOT NULL,
			created_at INTEGER NOT NULL
		)`,
		`CREATE TABLE browser_sessions (
			id TEXT PRIMARY KEY,
			token_digest BLOB NOT NULL UNIQUE,
			csrf_digest BLOB NOT NULL,
			created_at INTEGER NOT NULL,
			last_seen_at INTEGER NOT NULL,
			user_agent TEXT NOT NULL,
			remote_addr TEXT NOT NULL
		)`,
		`CREATE INDEX browser_sessions_last_seen ON browser_sessions(last_seen_at)`,
		`CREATE TABLE appearance (
			id INTEGER PRIMARY KEY CHECK (id = 1),
			display_name TEXT NOT NULL,
			theme TEXT NOT NULL,
			background_color TEXT NOT NULL,
			avatar_png BLOB,
			background_png BLOB,
			updated_at INTEGER NOT NULL
		)`,
		`INSERT INTO appearance(id, display_name, theme, background_color, updated_at)
			VALUES(1, 'NodeDance Admin', 'system', '#101827', 0)`,
		`CREATE TABLE audit_entries (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			occurred_at INTEGER NOT NULL,
			action TEXT NOT NULL,
			outcome TEXT NOT NULL,
			actor_id INTEGER,
			remote_addr TEXT NOT NULL
		)`,
	},
}, {
	Version: 2,
	SQL: []string{
		`ALTER TABLE audit_entries ADD COLUMN target_kind TEXT`,
		`ALTER TABLE audit_entries ADD COLUMN target_id TEXT`,
		`CREATE INDEX audit_entries_target ON audit_entries(target_kind, target_id)`,
		`CREATE TABLE nodes (
			id TEXT PRIMARY KEY,
			display_name TEXT NOT NULL,
			status TEXT NOT NULL CHECK (status IN ('pending', 'online', 'offline', 'revoked')),
			connection_generation INTEGER NOT NULL DEFAULT 0 CHECK (connection_generation >= 0),
			last_seen_at INTEGER,
			heartbeat_sequence INTEGER NOT NULL DEFAULT 0 CHECK (heartbeat_sequence >= 0),
			protocol_version INTEGER,
			agent_version TEXT,
			capabilities_json TEXT NOT NULL DEFAULT '[]',
			runtime_os TEXT,
			runtime_architecture TEXT,
			effective_uid INTEGER,
			effective_gid INTEGER,
			supplementary_groups_json TEXT NOT NULL DEFAULT '[]',
			created_at INTEGER NOT NULL,
			updated_at INTEGER NOT NULL
		)`,
		`CREATE TABLE agent_devices (
			id TEXT PRIMARY KEY,
			node_id TEXT NOT NULL UNIQUE REFERENCES nodes(id),
			credential_digest BLOB NOT NULL UNIQUE CHECK (length(credential_digest) = 32),
			pending_credential_digest BLOB UNIQUE CHECK (pending_credential_digest IS NULL OR length(pending_credential_digest) = 32),
			pending_rotation_id TEXT,
			rotation_requested_id TEXT,
			revoked_at INTEGER,
			created_at INTEGER NOT NULL,
			CHECK (pending_credential_digest IS NULL OR pending_credential_digest != credential_digest),
			CHECK ((pending_credential_digest IS NULL) = (pending_rotation_id IS NULL))
		)`,
		`CREATE INDEX agent_devices_node ON agent_devices(node_id)`,
		`CREATE TABLE agent_credential_verifiers (
			digest BLOB PRIMARY KEY CHECK (length(digest) = 32),
			agent_id TEXT NOT NULL REFERENCES agent_devices(id) ON DELETE CASCADE,
			state TEXT NOT NULL CHECK (state IN ('active', 'pending')),
			UNIQUE(agent_id, state)
		)`,
		`CREATE INDEX agent_credential_verifiers_agent ON agent_credential_verifiers(agent_id)`,
		`CREATE TABLE agent_enrollments (
			token_digest BLOB PRIMARY KEY CHECK (length(token_digest) = 32),
			node_id TEXT NOT NULL REFERENCES nodes(id),
			request_id TEXT,
			expires_at INTEGER NOT NULL,
			consumed_at INTEGER,
			created_at INTEGER NOT NULL
		)`,
		`CREATE INDEX agent_enrollments_node ON agent_enrollments(node_id)`,
		`CREATE INDEX nodes_status_seen ON nodes(status, last_seen_at)`,
	},
}, {
	Version: 3,
	SQL: []string{
		`CREATE TABLE docker_node_state (
			node_id TEXT PRIMARY KEY REFERENCES nodes(id) ON DELETE CASCADE,
			agent_id TEXT NOT NULL,
			generation INTEGER NOT NULL CHECK (generation > 0),
			health_json TEXT,
			health_received_at INTEGER NOT NULL,
			stale_reason TEXT NOT NULL,
			updated_at INTEGER NOT NULL
		)`,
		`CREATE TABLE docker_containers (
			node_id TEXT NOT NULL REFERENCES docker_node_state(node_id) ON DELETE CASCADE,
			container_id TEXT NOT NULL,
			record_json TEXT NOT NULL,
			PRIMARY KEY(node_id, container_id)
		)`,
		`CREATE INDEX docker_containers_node ON docker_containers(node_id, container_id)`,
	},
}, {
	Version: 4,
	SQL:     coretasks.SchemaStatements(),
}, {
	Version: 5,
	SQL: []string{
		`CREATE TABLE dashboard_preferences (
			node_id TEXT NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
			target_kind TEXT NOT NULL CHECK (target_kind IN ('node', 'container', 'compose_service')),
			identity_key TEXT NOT NULL,
			alias TEXT NOT NULL DEFAULT '',
			icon TEXT NOT NULL DEFAULT '',
			notes TEXT NOT NULL DEFAULT '',
			service_url TEXT NOT NULL DEFAULT '',
			sort_order INTEGER NOT NULL DEFAULT 0,
			visible INTEGER NOT NULL DEFAULT 1 CHECK (visible IN (0, 1)),
			pinned INTEGER NOT NULL DEFAULT 0 CHECK (pinned IN (0, 1)),
			updated_at INTEGER NOT NULL,
			PRIMARY KEY(node_id, target_kind, identity_key)
		)`,
		`CREATE TABLE dashboard_settings (
			id INTEGER PRIMARY KEY CHECK (id = 1),
			view_mode TEXT NOT NULL CHECK (view_mode IN ('monitor', 'manage')),
			group_by TEXT NOT NULL CHECK (group_by IN ('node', 'compose', 'state', 'none')),
			sort_by TEXT NOT NULL CHECK (sort_by IN ('custom', 'name', 'state')),
			featured_limit INTEGER NOT NULL CHECK (featured_limit BETWEEN 1 AND 20),
			custom_fields_json TEXT NOT NULL,
			updated_at INTEGER NOT NULL
		)`,
		`INSERT INTO dashboard_settings(id, view_mode, group_by, sort_by, featured_limit, custom_fields_json, updated_at)
			VALUES(1, 'monitor', 'node', 'custom', 4, '["state","ports","health"]', 0)`,
		`CREATE TABLE metrics_minute (
			node_id TEXT NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
			metric_key TEXT NOT NULL,
			bucket_at INTEGER NOT NULL,
			sample_count INTEGER NOT NULL CHECK (sample_count > 0),
			sample_sum REAL NOT NULL,
			minimum REAL NOT NULL,
			maximum REAL NOT NULL,
			PRIMARY KEY(node_id, metric_key, bucket_at)
		)`,
		`CREATE INDEX metrics_minute_retention ON metrics_minute(bucket_at)`,
		`CREATE TABLE metrics_hour (
			node_id TEXT NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
			metric_key TEXT NOT NULL,
			bucket_at INTEGER NOT NULL,
			sample_count INTEGER NOT NULL CHECK (sample_count > 0),
			sample_sum REAL NOT NULL,
			minimum REAL NOT NULL,
			maximum REAL NOT NULL,
			PRIMARY KEY(node_id, metric_key, bucket_at)
		)`,
		`CREATE INDEX metrics_hour_retention ON metrics_hour(bucket_at)`,
	},
}, {
	Version: 6,
	SQL:     corecompose.SchemaStatements(),
}, {
	Version: 7,
	SQL:     corecomposeedit.SchemaStatements(),
}, {
	Version: 8,
	SQL: []string{
		`CREATE TABLE service_probes (
			id TEXT PRIMARY KEY,
			node_id TEXT NOT NULL REFERENCES nodes(id),
			name TEXT NOT NULL,
			kind TEXT NOT NULL CHECK (kind IN ('http', 'https', 'tcp')),
			target TEXT NOT NULL,
			expected_http_status INTEGER,
			interval_seconds INTEGER NOT NULL CHECK (interval_seconds BETWEEN 10 AND 86400),
			timeout_seconds INTEGER NOT NULL CHECK (timeout_seconds BETWEEN 1 AND 30 AND timeout_seconds < interval_seconds),
			enabled INTEGER NOT NULL CHECK (enabled IN (0, 1)),
			revision INTEGER NOT NULL CHECK (revision > 0),
			status TEXT NOT NULL CHECK (status IN ('unknown', 'healthy', 'unhealthy')),
			last_error_code TEXT,
			consecutive_failures INTEGER NOT NULL DEFAULT 0 CHECK (consecutive_failures >= 0),
			consecutive_successes INTEGER NOT NULL DEFAULT 0 CHECK (consecutive_successes >= 0),
			last_checked_at INTEGER,
			next_due_at INTEGER NOT NULL,
			created_at INTEGER NOT NULL,
			updated_at INTEGER NOT NULL,
			deleted_at INTEGER,
			CHECK ((kind = 'tcp' AND expected_http_status IS NULL) OR (kind IN ('http', 'https') AND expected_http_status BETWEEN 100 AND 599))
		)`,
		`CREATE INDEX service_probes_due ON service_probes(enabled, deleted_at, next_due_at)`,
		`CREATE INDEX service_probes_node ON service_probes(node_id, deleted_at, id)`,
		`CREATE TABLE service_probe_runs (
			run_id TEXT PRIMARY KEY,
			probe_id TEXT NOT NULL REFERENCES service_probes(id),
			node_id TEXT NOT NULL REFERENCES nodes(id),
			probe_revision INTEGER NOT NULL,
			generation INTEGER NOT NULL DEFAULT 0,
			kind TEXT NOT NULL CHECK (kind IN ('http', 'https', 'tcp')),
			expected_http_status INTEGER,
			status TEXT NOT NULL CHECK (status IN ('pending', 'healthy', 'unhealthy', 'unknown')),
			checked_at INTEGER NOT NULL,
			completed_at INTEGER,
			latency_ms INTEGER,
			http_status INTEGER,
			error_code TEXT,
			timeout_seconds INTEGER NOT NULL,
			CHECK ((kind='tcp' AND expected_http_status IS NULL) OR (kind IN ('http','https') AND expected_http_status BETWEEN 100 AND 599))
		)`,
		`CREATE INDEX service_probe_runs_probe_time ON service_probe_runs(probe_id, checked_at DESC, run_id DESC)`,
		`CREATE INDEX service_probe_runs_pending ON service_probe_runs(status, checked_at)`,
	},
}}

func Open(ctx context.Context, directory string) (*Store, error) {
	return OpenWithMigrations(ctx, directory, migrations)
}

// OpenWithMigrations is also used by storage tests to exercise rollback against
// real SQLite databases. Production callers use Open and the ordered built-in
// migration list.
func OpenWithMigrations(ctx context.Context, directory string, ordered []Migration) (*Store, error) {
	if err := validateMigrations(ordered); err != nil {
		return nil, err
	}
	if directory == "" {
		return nil, errors.New("data directory is empty")
	}
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return nil, fmt.Errorf("create data directory: %w", err)
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		return nil, fmt.Errorf("secure data directory: %w", err)
	}
	absoluteDir, err := filepath.Abs(directory)
	if err != nil {
		return nil, fmt.Errorf("resolve data directory: %w", err)
	}
	databasePath := filepath.Join(absoluteDir, "nodedance.sqlite")
	if info, statErr := os.Lstat(databasePath); statErr == nil {
		if !info.Mode().IsRegular() {
			return nil, errors.New("database path must be a regular file")
		}
		if err := os.Chmod(databasePath, 0o600); err != nil {
			return nil, fmt.Errorf("secure database file: %w", err)
		}
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return nil, fmt.Errorf("inspect database path: %w", statErr)
	}

	dsnURL := url.URL{Scheme: "file", Path: filepath.ToSlash(databasePath)}
	query := url.Values{}
	query.Add("_pragma", "busy_timeout(5000)")
	query.Add("_pragma", "foreign_keys(1)")
	query.Add("_pragma", "synchronous(FULL)")
	dsnURL.RawQuery = query.Encode()
	db, err := sql.Open("sqlite", dsnURL.String())
	if err != nil {
		return nil, fmt.Errorf("open sqlite database: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(0)
	fail := func(err error) (*Store, error) {
		_ = db.Close()
		return nil, err
	}
	if err := db.PingContext(ctx); err != nil {
		return fail(fmt.Errorf("connect sqlite database: %w", err))
	}
	if err := os.Chmod(databasePath, 0o600); err != nil {
		return fail(fmt.Errorf("secure database file: %w", err))
	}
	if _, _, err := inspectMigrationLedger(ctx, db, len(ordered)); err != nil {
		return fail(err)
	}
	if _, err := db.ExecContext(ctx, `PRAGMA journal_mode=WAL`); err != nil {
		return fail(fmt.Errorf("enable sqlite WAL: %w", err))
	}
	if err := ApplyMigrations(ctx, db, ordered); err != nil {
		return fail(err)
	}
	return &Store{DB: db, Dir: absoluteDir}, nil
}

func ApplyMigrations(ctx context.Context, db *sql.DB, ordered []Migration) error {
	if err := validateMigrations(ordered); err != nil {
		return err
	}
	ledgerExists, lastApplied, err := inspectMigrationLedger(ctx, db, len(ordered))
	if err != nil {
		return err
	}
	if !ledgerExists {
		if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
			version INTEGER PRIMARY KEY,
			applied_at INTEGER NOT NULL
		)`); err != nil {
			return fmt.Errorf("initialize migration ledger: %w", err)
		}
		lastApplied, err = readMigrationLedger(ctx, db, len(ordered))
		if err != nil {
			return err
		}
	}
	for _, migration := range ordered {
		if migration.Version <= lastApplied {
			continue
		}
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("begin migration %d: %w", migration.Version, err)
		}
		for _, statement := range migration.SQL {
			if _, err := tx.ExecContext(ctx, statement); err != nil {
				_ = tx.Rollback()
				return fmt.Errorf("apply migration %d: %w", migration.Version, err)
			}
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations(version, applied_at) VALUES(?, ?)`, migration.Version, time.Now().Unix()); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("record migration %d: %w", migration.Version, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit migration %d: %w", migration.Version, err)
		}
	}
	return nil
}

func validateMigrations(ordered []Migration) error {
	if len(ordered) == 0 {
		return errors.New("migration list must not be empty")
	}
	for index, migration := range ordered {
		want := index + 1
		if migration.Version != want {
			return fmt.Errorf("migration versions must be ordered and contiguous from 1: position %d has version %d", want, migration.Version)
		}
		if len(migration.SQL) == 0 {
			return fmt.Errorf("migration %d has no statements", migration.Version)
		}
	}
	return nil
}

func inspectMigrationLedger(ctx context.Context, db *sql.DB, supported int) (bool, int, error) {
	var objectType string
	err := db.QueryRowContext(ctx, `SELECT type FROM sqlite_master WHERE name='schema_migrations'`).Scan(&objectType)
	if errors.Is(err, sql.ErrNoRows) {
		return false, 0, nil
	}
	if err != nil {
		return false, 0, fmt.Errorf("inspect migration ledger: %w", err)
	}
	if objectType != "table" {
		return false, 0, fmt.Errorf("invalid migration ledger: schema_migrations is a %s, not a table", objectType)
	}
	last, err := readMigrationLedger(ctx, db, supported)
	if err != nil {
		return true, 0, err
	}
	return true, last, nil
}

func readMigrationLedger(ctx context.Context, db *sql.DB, supported int) (int, error) {
	rows, err := db.QueryContext(ctx, `SELECT version FROM schema_migrations ORDER BY version`)
	if err != nil {
		return 0, fmt.Errorf("read migration ledger: %w", err)
	}
	defer rows.Close()
	last := 0
	for rows.Next() {
		var version int
		if err := rows.Scan(&version); err != nil {
			return 0, fmt.Errorf("read migration ledger entry: %w", err)
		}
		if version != last+1 {
			return 0, fmt.Errorf("invalid migration ledger: expected version %d, found %d", last+1, version)
		}
		if version > supported {
			return 0, fmt.Errorf("database schema version %d is newer than this binary supports (%d)", version, supported)
		}
		last = version
	}
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("read migration ledger: %w", err)
	}
	return last, nil
}

func (s *Store) Close() error {
	if s == nil || s.DB == nil {
		return nil
	}
	return s.DB.Close()
}
