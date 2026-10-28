package storage

import (
	"context"
	"database/sql"
	_ "embed"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	coretasks "github.com/CST-Cat/NodeDance/internal/core/tasks"

	_ "modernc.org/sqlite"
)

type Store struct {
	DB     *sql.DB
	ReadDB *sql.DB
	Dir    string
}

func (s *Store) Reader() *sql.DB {
	if s != nil && s.ReadDB != nil {
		return s.ReadDB
	}
	return s.DB
}

//go:embed schema.sql
var schemaSQL string

func Open(ctx context.Context, directory string) (*Store, error) {
	if ctx == nil {
		return nil, errors.New("SQLite initialization context is required")
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
	// Apply connection-local limits through the driver DSN so replacement
	// physical connections inherit them as well. Keep FULL durability.
	query.Add("_pragma", "cache_size(-2048)")
	query.Add("_pragma", "mmap_size(0)")
	query.Add("_pragma", "temp_store(FILE)")
	query.Add("_pragma", "cache_spill(1)")
	query.Add("_pragma", "wal_autocheckpoint(256)")
	query.Add("_pragma", "journal_size_limit(1048576)")
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
	if _, err := db.ExecContext(ctx, `PRAGMA journal_mode=WAL`); err != nil {
		return fail(fmt.Errorf("enable sqlite WAL: %w", err))
	}
	if err := initializeCurrentSchema(ctx, db); err != nil {
		return fail(err)
	}
	readQuery := make(url.Values, len(query)+1)
	for key, values := range query {
		readQuery[key] = append([]string(nil), values...)
	}
	readQuery.Add("_pragma", "query_only(1)")
	readDSN := url.URL{Scheme: "file", Path: filepath.ToSlash(databasePath), RawQuery: readQuery.Encode()}
	readDB, err := sql.Open("sqlite", readDSN.String())
	if err != nil {
		return fail(fmt.Errorf("open sqlite read pool: %w", err))
	}
	readDB.SetMaxOpenConns(8)
	readDB.SetMaxIdleConns(8)
	readDB.SetConnMaxLifetime(0)
	if err := readDB.PingContext(ctx); err != nil {
		_ = readDB.Close()
		return fail(fmt.Errorf("connect sqlite read pool: %w", err))
	}
	return &Store{DB: db, ReadDB: readDB, Dir: absoluteDir}, nil
}

func initializeCurrentSchema(ctx context.Context, db *sql.DB) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin current SQLite schema initialization: %w", err)
	}
	defer tx.Rollback()
	statements := append(splitCurrentSchema(schemaSQL), coretasks.SchemaStatements()...)
	for index, statement := range statements {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("initialize current SQLite schema statement %d: %w", index+1, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit current SQLite schema initialization: %w", err)
	}
	return nil
}

func splitCurrentSchema(source string) []string {
	parts := strings.Split(source, ";\n\n")
	statements := make([]string, 0, len(parts))
	for _, part := range parts {
		statement := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(part), ";"))
		if statement != "" {
			statements = append(statements, statement)
		}
	}
	return statements
}

func (s *Store) Close() error {
	if s == nil || s.DB == nil {
		return nil
	}
	var err error
	if s.ReadDB != nil {
		err = s.ReadDB.Close()
	}
	if closeErr := s.DB.Close(); err == nil {
		err = closeErr
	}
	return err
}

// Schema returns the current storage contract for release preflight checks.
func Schema() string { return schemaSQL + strings.Join(coretasks.SchemaStatements(), "\n") }
