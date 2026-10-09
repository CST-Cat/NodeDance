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
	DB  *sql.DB
	Dir string
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
	return &Store{DB: db, Dir: absoluteDir}, nil
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
	return s.DB.Close()
}
