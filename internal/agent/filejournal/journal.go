// Package filejournal stores the Agent-side, body-free execution history for
// remote filesystem writes. It is deliberately separate from the Docker task
// journal because file operations have different postconditions and recovery
// rules.
package filejournal

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	_ "modernc.org/sqlite"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"
)

var (
	ErrInvalidIntent = errors.New("invalid file journal intent")
	ErrTaskConflict  = errors.New("file task ID was reused for a different intent")
	ErrNotFound      = errors.New("file journal task not found")
	ErrTransition    = errors.New("invalid file journal transition")
	ErrCapacity      = errors.New("Agent file journal capacity is reserved by active operations")
	canonicalID      = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	sha256Digest     = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

const (
	TerminalRetention = 90 * 24 * time.Hour
	MaxRecords        = 10000
)

type Status string

const (
	Accepted  Status = "accepted"
	Running   Status = "running"
	Succeeded Status = "succeeded"
	Failed    Status = "failed"
	Canceled  Status = "canceled"
	Unknown   Status = "unknown"
)

type Intent struct {
	TaskID                   string
	Operation                string
	TargetPath               string
	NewPath                  string
	ExpectedVersion          string
	ExpectedSize             int64
	ContentSHA256            string
	BaselineCaptured         bool
	BeforeTargetExists       bool
	BeforeTargetFingerprint  string
	BeforeNewPathExists      bool
	BeforeNewPathFingerprint string
	TemporaryPath            string
	TemporaryOwned           bool
}

// Record contains only identifiers, paths, content size/digest, status and a
// fixed result code. File bytes, editor text, credentials and arbitrary error
// messages are never persisted.
type Record struct {
	Intent
	Status     Status
	ResultCode string
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

type Store struct {
	db         *sql.DB
	lock       *os.File
	now        func() time.Time
	maxRecords int
}

func Open(ctx context.Context, databasePath string) (*Store, error) {
	if strings.TrimSpace(databasePath) == "" {
		return nil, errors.New("file journal path is empty")
	}
	absolute, err := filepath.Abs(databasePath)
	if err != nil {
		return nil, fmt.Errorf("resolve file journal path: %w", err)
	}
	if err := verifyPrivateDirectory(filepath.Dir(absolute)); err != nil {
		return nil, err
	}
	if err := ensurePrivateFile(absolute); err != nil {
		return nil, err
	}
	lock, err := os.OpenFile(absolute, os.O_RDWR, 0)
	if err != nil {
		return nil, fmt.Errorf("open file journal lock: %w", err)
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = lock.Close()
		return nil, fmt.Errorf("acquire file journal lock: %w", err)
	}
	closeOnError := func(err error, db *sql.DB) (*Store, error) {
		if db != nil {
			_ = db.Close()
		}
		_ = syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
		_ = lock.Close()
		return nil, err
	}
	for _, sidecar := range []string{absolute + "-wal", absolute + "-shm"} {
		if err := ensurePrivateFile(sidecar); err != nil {
			return closeOnError(err, nil)
		}
	}
	dsn := url.URL{Scheme: "file", Path: filepath.ToSlash(absolute)}
	query := url.Values{}
	query.Add("_pragma", "busy_timeout(5000)")
	query.Add("_pragma", "synchronous(FULL)")
	dsn.RawQuery = query.Encode()
	db, err := sql.Open("sqlite", dsn.String())
	if err != nil {
		return closeOnError(fmt.Errorf("open file journal SQLite: %w", err), nil)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if err := db.PingContext(ctx); err != nil {
		return closeOnError(fmt.Errorf("connect file journal SQLite: %w", err), db)
	}
	var mode string
	if err := db.QueryRowContext(ctx, `PRAGMA journal_mode=WAL`).Scan(&mode); err != nil || !strings.EqualFold(mode, "wal") {
		if err == nil {
			err = errors.New("SQLite did not enable WAL mode")
		}
		return closeOnError(fmt.Errorf("enable file journal WAL: %w", err), db)
	}
	_, err = db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS agent_file_journal (
		task_id TEXT PRIMARY KEY,
		operation TEXT NOT NULL CHECK(operation IN ('mkdir','rename','delete','save_text','upload')),
		target_path TEXT NOT NULL,
		new_path TEXT NOT NULL,
		expected_version TEXT NOT NULL,
		expected_size INTEGER NOT NULL CHECK(expected_size >= 0),
		content_sha256 TEXT NOT NULL,
		baseline_captured INTEGER NOT NULL CHECK(baseline_captured IN (0,1)),
		before_target_exists INTEGER NOT NULL CHECK(before_target_exists IN (0,1)),
		before_target_fingerprint TEXT NOT NULL,
		before_new_path_exists INTEGER NOT NULL CHECK(before_new_path_exists IN (0,1)),
		before_new_path_fingerprint TEXT NOT NULL,
		temporary_path TEXT NOT NULL,
		temporary_owned INTEGER NOT NULL CHECK(temporary_owned IN (0,1)),
		status TEXT NOT NULL CHECK(status IN ('accepted','running','succeeded','failed','canceled','unknown')),
		result_code TEXT NOT NULL,
		created_at_ns INTEGER NOT NULL,
		updated_at_ns INTEGER NOT NULL
	)`)
	if err != nil {
		return closeOnError(fmt.Errorf("initialize file journal schema: %w", err), db)
	}
	if err := verifySidecars(absolute); err != nil {
		return closeOnError(err, db)
	}
	store := &Store{db: db, lock: lock, now: time.Now, maxRecords: MaxRecords}
	if err := store.pruneExpired(ctx, store.now().UTC()); err != nil {
		return closeOnError(fmt.Errorf("prune expired Agent file journal records: %w", err), db)
	}
	return store, nil
}

func (s *Store) Begin(ctx context.Context, intent Intent) (Record, bool, error) {
	if ctx == nil || !validIntent(intent) {
		return Record{}, false, ErrInvalidIntent
	}
	now := s.now().UTC()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Record{}, false, fmt.Errorf("begin Agent file journal write: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := pruneExpiredTx(ctx, tx, now); err != nil {
		return Record{}, false, fmt.Errorf("prune expired Agent file journal records: %w", err)
	}
	if existing, err := getRecord(ctx, tx, intent.TaskID); err == nil {
		if !sameIntent(existing.Intent, intent) {
			return Record{}, false, ErrTaskConflict
		}
		if err := tx.Commit(); err != nil {
			return Record{}, false, fmt.Errorf("commit Agent file journal replay: %w", err)
		}
		return existing, false, nil
	} else if !errors.Is(err, ErrNotFound) {
		return Record{}, false, err
	}
	if err := s.makeRoom(ctx, tx); err != nil {
		return Record{}, false, err
	}
	result, err := tx.ExecContext(ctx, `INSERT INTO agent_file_journal(task_id,operation,target_path,new_path,expected_version,expected_size,content_sha256,baseline_captured,
		before_target_exists,before_target_fingerprint,before_new_path_exists,before_new_path_fingerprint,temporary_path,temporary_owned,status,result_code,created_at_ns,updated_at_ns)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,'accepted','',?,?) ON CONFLICT(task_id) DO NOTHING`, intent.TaskID, intent.Operation, intent.TargetPath,
		intent.NewPath, intent.ExpectedVersion, intent.ExpectedSize, intent.ContentSHA256, intent.BaselineCaptured, intent.BeforeTargetExists, intent.BeforeTargetFingerprint,
		intent.BeforeNewPathExists, intent.BeforeNewPathFingerprint, intent.TemporaryPath, intent.TemporaryOwned, now.UnixNano(), now.UnixNano())
	if err != nil {
		return Record{}, false, fmt.Errorf("persist Agent file intent: %w", err)
	}
	inserted, err := result.RowsAffected()
	if err != nil {
		return Record{}, false, fmt.Errorf("read Agent file intent insert result: %w", err)
	}
	if inserted == 0 {
		existing, err := getRecord(ctx, tx, intent.TaskID)
		if err != nil {
			return Record{}, false, err
		}
		if !sameIntent(existing.Intent, intent) {
			return Record{}, false, ErrTaskConflict
		}
		if err := tx.Commit(); err != nil {
			return Record{}, false, fmt.Errorf("commit Agent file journal replay: %w", err)
		}
		return existing, false, nil
	}
	if err := tx.Commit(); err != nil {
		return Record{}, false, fmt.Errorf("commit Agent file intent: %w", err)
	}
	return Record{Intent: intent, Status: Accepted, CreatedAt: now, UpdatedAt: now}, true, nil
}

func sameIntent(existing, incoming Intent) bool {
	if existing.Operation == "upload" && incoming.Operation == "upload" {
		// The upload temporary name is Agent-local state and is regenerated
		// when Core retries the same request ID. All user-visible intent fields
		// still have to match before returning the existing task record.
		existing.TemporaryPath, incoming.TemporaryPath = "", ""
		existing.TemporaryOwned, incoming.TemporaryOwned = false, false
	}
	return existing == incoming
}

func (s *Store) StartMutation(ctx context.Context, taskID string) error {
	return s.transition(ctx, taskID, []Status{Accepted}, Running, "")
}

func (s *Store) Complete(ctx context.Context, taskID string, status Status, code string) error {
	if status != Succeeded && status != Failed && status != Canceled && status != Unknown || !validResultCode(status, code) {
		return ErrTransition
	}
	return s.transition(ctx, taskID, []Status{Accepted, Running, Unknown}, status, code)
}

func (s *Store) IncompleteUploads(ctx context.Context) ([]Record, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT task_id,operation,target_path,new_path,expected_version,expected_size,content_sha256,baseline_captured,
		before_target_exists,before_target_fingerprint,before_new_path_exists,before_new_path_fingerprint,temporary_path,temporary_owned,status,result_code,created_at_ns,updated_at_ns
		FROM agent_file_journal WHERE operation='upload' AND status IN ('accepted','running','unknown') AND temporary_owned=1 ORDER BY created_at_ns,task_id LIMIT 10000`)
	if err != nil {
		return nil, fmt.Errorf("list incomplete Agent file uploads: %w", err)
	}
	defer rows.Close()
	var records []Record
	for rows.Next() {
		var record Record
		var created, updated int64
		if err := rows.Scan(&record.TaskID, &record.Operation, &record.TargetPath, &record.NewPath, &record.ExpectedVersion, &record.ExpectedSize,
			&record.ContentSHA256, &record.BaselineCaptured, &record.BeforeTargetExists, &record.BeforeTargetFingerprint,
			&record.BeforeNewPathExists, &record.BeforeNewPathFingerprint, &record.TemporaryPath, &record.TemporaryOwned,
			&record.Status, &record.ResultCode, &created, &updated); err != nil {
			return nil, fmt.Errorf("scan incomplete Agent file upload: %w", err)
		}
		record.CreatedAt, record.UpdatedAt = time.Unix(0, created).UTC(), time.Unix(0, updated).UTC()
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read incomplete Agent file uploads: %w", err)
	}
	return records, nil
}

func (s *Store) transition(ctx context.Context, taskID string, from []Status, to Status, code string) error {
	if ctx == nil || !canonicalID.MatchString(taskID) {
		return ErrInvalidIntent
	}
	placeholders := strings.TrimRight(strings.Repeat("?,", len(from)), ",")
	args := []any{to, code, s.now().UTC().UnixNano(), taskID}
	for _, status := range from {
		args = append(args, status)
	}
	query := `UPDATE agent_file_journal SET status=?,result_code=?,updated_at_ns=? WHERE task_id=? AND status IN (` + placeholders + `)`
	result, err := s.db.ExecContext(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("update Agent file journal: %w", err)
	}
	if n, err := result.RowsAffected(); err != nil {
		return err
	} else if n == 1 {
		return nil
	}
	current, err := s.Get(ctx, taskID)
	if err != nil {
		return err
	}
	if current.Status == to && current.ResultCode == code {
		return nil
	}
	return ErrTransition
}

func (s *Store) Get(ctx context.Context, taskID string) (Record, error) {
	if ctx == nil || !canonicalID.MatchString(taskID) {
		return Record{}, ErrInvalidIntent
	}
	return getRecord(ctx, s.db, taskID)
}

type recordQueryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func getRecord(ctx context.Context, queryer recordQueryer, taskID string) (Record, error) {
	var record Record
	var created, updated int64
	err := queryer.QueryRowContext(ctx, `SELECT task_id,operation,target_path,new_path,expected_version,expected_size,content_sha256,baseline_captured,
		before_target_exists,before_target_fingerprint,before_new_path_exists,before_new_path_fingerprint,temporary_path,temporary_owned,status,result_code,created_at_ns,updated_at_ns
		FROM agent_file_journal WHERE task_id=?`, taskID).Scan(&record.TaskID, &record.Operation, &record.TargetPath, &record.NewPath,
		&record.ExpectedVersion, &record.ExpectedSize, &record.ContentSHA256, &record.BaselineCaptured, &record.BeforeTargetExists, &record.BeforeTargetFingerprint,
		&record.BeforeNewPathExists, &record.BeforeNewPathFingerprint, &record.TemporaryPath, &record.TemporaryOwned,
		&record.Status, &record.ResultCode, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return Record{}, ErrNotFound
	}
	if err != nil {
		return Record{}, fmt.Errorf("read Agent file journal: %w", err)
	}
	record.CreatedAt, record.UpdatedAt = time.Unix(0, created).UTC(), time.Unix(0, updated).UTC()
	return record, nil
}

func (s *Store) pruneExpired(ctx context.Context, now time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := pruneExpiredTx(ctx, tx, now); err != nil {
		return err
	}
	return tx.Commit()
}

func pruneExpiredTx(ctx context.Context, tx *sql.Tx, now time.Time) error {
	_, err := tx.ExecContext(ctx, `DELETE FROM agent_file_journal WHERE status IN ('succeeded','failed','canceled') AND updated_at_ns < ?`, now.Add(-TerminalRetention).UnixNano())
	return err
}

func (s *Store) makeRoom(ctx context.Context, tx *sql.Tx) error {
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM agent_file_journal`).Scan(&count); err != nil {
		return fmt.Errorf("count Agent file journal records: %w", err)
	}
	if count >= s.maxRecords {
		return ErrCapacity
	}
	return nil
}

func (s *Store) Close() error {
	if s == nil {
		return nil
	}
	var err error
	if s.db != nil {
		err = s.db.Close()
		s.db = nil
	}
	if s.lock != nil {
		_ = syscall.Flock(int(s.lock.Fd()), syscall.LOCK_UN)
		if closeErr := s.lock.Close(); err == nil {
			err = closeErr
		}
		s.lock = nil
	}
	return err
}

func validIntent(intent Intent) bool {
	if !canonicalID.MatchString(intent.TaskID) || len(intent.TargetPath) == 0 || len(intent.TargetPath) > 4096 || strings.ContainsRune(intent.TargetPath, '\x00') ||
		len(intent.NewPath) > 4096 || strings.ContainsRune(intent.NewPath, '\x00') || len(intent.ExpectedVersion) > 128 || intent.ExpectedSize < 0 || !intent.BaselineCaptured ||
		(intent.BeforeTargetExists && !sha256Digest.MatchString(intent.BeforeTargetFingerprint)) || (!intent.BeforeTargetExists && intent.BeforeTargetFingerprint != "") ||
		(intent.BeforeNewPathExists && !sha256Digest.MatchString(intent.BeforeNewPathFingerprint)) || (!intent.BeforeNewPathExists && intent.BeforeNewPathFingerprint != "") {
		return false
	}
	if !validVirtualPath(intent.TargetPath) || intent.NewPath != "" && !validVirtualPath(intent.NewPath) {
		return false
	}
	switch intent.Operation {
	case "mkdir", "delete":
		return intent.NewPath == "" && intent.ContentSHA256 == "" && intent.TemporaryPath == "" && !intent.TemporaryOwned
	case "rename":
		return intent.NewPath != "" && intent.ContentSHA256 == "" && intent.TemporaryPath == "" && !intent.TemporaryOwned
	case "save_text":
		return intent.NewPath == "" && sha256Digest.MatchString(intent.ContentSHA256) && intent.TemporaryPath == "" && !intent.TemporaryOwned
	case "upload":
		return intent.NewPath == "" && sha256Digest.MatchString(intent.ContentSHA256) && intent.TemporaryOwned && validUploadTemporary(intent.TargetPath, intent.TemporaryPath)
	default:
		return false
	}
}

func validVirtualPath(value string) bool {
	if len(value) == 0 || len(value) > 4096 || !strings.HasPrefix(value, "/") || strings.ContainsRune(value, '\x00') || path.Clean(value) != value {
		return false
	}
	for _, part := range strings.Split(value, "/") {
		if part == ".." {
			return false
		}
	}
	return true
}

func validUploadTemporary(target, temporary string) bool {
	if !validVirtualPath(temporary) || path.Dir(temporary) != path.Dir(target) {
		return false
	}
	targetBase, temporaryBase := path.Base(target), path.Base(temporary)
	prefix := "." + targetBase + ".nodedance-upload-"
	if !strings.HasPrefix(temporaryBase, prefix) || len(temporaryBase) != len(prefix)+24 {
		return false
	}
	for _, char := range strings.TrimPrefix(temporaryBase, prefix) {
		if !(char >= '0' && char <= '9' || char >= 'a' && char <= 'f') {
			return false
		}
	}
	return true
}

func validResultCode(status Status, code string) bool {
	switch status {
	case Succeeded:
		return code == "verified" || code == "state_verified"
	case Failed:
		return code == "agent_rejected" || code == "not_committed"
	case Canceled:
		return code == "cancel_confirmed"
	case Unknown:
		return code == "result_pending" || code == "mutation_uncertain"
	default:
		return false
	}
}

func verifyPrivateDirectory(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("inspect Agent state directory: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o700 {
		return errors.New("Agent file journal requires a private mode 0700 state directory")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(stat.Uid) != os.Geteuid() {
		return errors.New("Agent file journal state directory has the wrong owner")
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil || filepath.Clean(resolved) != filepath.Clean(path) {
		return errors.New("Agent file journal path contains a symbolic link")
	}
	return nil
}

func ensurePrivateFile(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		file, createErr := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
		if createErr != nil && !errors.Is(createErr, os.ErrExist) {
			return fmt.Errorf("create private Agent file journal: %w", createErr)
		}
		if file != nil {
			if syncErr := file.Sync(); syncErr != nil {
				_ = file.Close()
				return syncErr
			}
			if closeErr := file.Close(); closeErr != nil {
				return closeErr
			}
		}
		info, err = os.Lstat(path)
	}
	if err != nil {
		return fmt.Errorf("inspect Agent file journal file: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("Agent file journal must be a private mode 0600 regular file")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(stat.Uid) != os.Geteuid() || stat.Nlink != 1 {
		return errors.New("Agent file journal file has an unsafe owner or link count")
	}
	return nil
}

func verifySidecars(databasePath string) error {
	for _, path := range []string{databasePath + "-wal", databasePath + "-shm"} {
		info, err := os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
			return fmt.Errorf("Agent file journal sidecar %s is not private", filepath.Base(path))
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || int(stat.Uid) != os.Geteuid() || stat.Nlink != 1 {
			return fmt.Errorf("Agent file journal sidecar %s has an unsafe owner or link count", filepath.Base(path))
		}
	}
	return nil
}
