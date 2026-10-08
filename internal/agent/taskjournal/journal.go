// Package taskjournal persists Agent task intent and verified results.
// It does not execute tasks; callers must durably BeginExecution before
// invoking any executor.
package taskjournal

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/CST-Cat/NodeDance/internal/taskstate"
	_ "modernc.org/sqlite"
)

const MaxTaskLogBytes = 16 << 20

const (
	MaxJournalSnapshotTasks = 10000
	MaxJournalSnapshotBytes = 16 << 20
	JournalSnapshotTimeout  = 10 * time.Second
)

var (
	ErrIdempotencyConflict = errors.New("idempotency key was already used for a different request")
	ErrTaskIDConflict      = errors.New("task ID was already used for a different request")
	ErrResourceBusy        = errors.New("resource has an active or unconfirmed task")
	ErrTaskNotFound        = errors.New("task not found")
	ErrUnredactedLog       = errors.New("task log chunk must be redacted before persistence")
	ErrInvalidProgress     = errors.New("invalid task progress")
	ErrInvalidResult       = errors.New("invalid task result")
	ErrJournalInUse        = errors.New("task journal is already owned by another Agent process")
	ErrWrongNode           = errors.New("task journal belongs to another node")
	ErrInvalidBaseline     = errors.New("invalid durable execution baseline")
	ErrBaselineAlreadySet  = errors.New("task already has a durable execution baseline")
)

type Store struct {
	db        *sql.DB
	lock      *os.File
	nodeID    string
	journalID string
	now       func() time.Time
}

type EnqueueResult struct {
	Task    Snapshot
	Created bool
}

type Snapshot struct {
	TaskID            string
	NodeID            string
	TargetID          string
	ResourceKey       string
	Action            string
	Status            taskstate.Status
	CreatedAt         time.Time
	UpdatedAt         time.Time
	StartedAt         *time.Time
	FinishedAt        *time.Time
	Evidence          taskstate.Evidence
	ExecutionPhase    ExecutionPhase
	Baseline          *ExecutionBaseline
	Progress          Progress
	Result            Result
	IdempotencyKey    string
	RequestDigest     [sha256.Size]byte
	DeliveryCommitted bool
	LogBytes          int64
	LogTruncated      bool
}

type ExecutionPhase string

const (
	ExecutionPhaseNone                   ExecutionPhase = "none"
	ExecutionPhaseMutationMayHaveStarted ExecutionPhase = "mutation_may_have_started"
	ExecutionPhaseResultPersisted        ExecutionPhase = "result_persisted"
)

// ExecutionBaseline contains only the sanitized Docker facts needed to
// reconcile a lifecycle operation after an Agent crash. It intentionally has
// no raw Inspect payload, labels, environment, command, or error text.
type ExecutionBaseline struct {
	TargetID     string
	Action       string
	HostBootID   string
	StartedAt    string
	RestartCount int
	Running      bool
	Paused       bool
	Restarting   bool
}

type ProgressPhase string

const (
	PhaseAccepted    ProgressPhase = "accepted"
	PhasePreparing   ProgressPhase = "preparing"
	PhaseExecuting   ProgressPhase = "executing"
	PhaseVerifying   ProgressPhase = "verifying"
	PhaseReconciling ProgressPhase = "reconciling"
)

type Progress struct {
	Phase     ProgressPhase
	Completed uint64
	Total     uint64
}

type ResultCode string

const (
	ResultVerified  ResultCode = "verified"
	ResultFailed    ResultCode = "failed"
	ResultTimedOut  ResultCode = "timed_out"
	ResultCanceled  ResultCode = "canceled"
	ResultUncertain ResultCode = "result_pending"
)

// Result intentionally carries only typed, bounded state. It has no freeform
// error, command, or request-body field that could persist credentials.
type Result struct {
	Code             ResultCode
	ObservedState    string
	ResourceRevision string
}

type LogChunk struct {
	Bytes    []byte
	Redacted bool
}

// Open creates or opens one node's Agent-owned journal database at databasePath.
// Its dedicated parent directory must be current-user-owned mode 0700. A
// kernel flock on the database file provides single-process ownership and is
// released automatically when a process exits or is killed.
func Open(ctx context.Context, databasePath, nodeID string) (*Store, error) {
	if strings.TrimSpace(databasePath) == "" {
		return nil, errors.New("task journal path is empty")
	}
	if !validScopeID(nodeID) {
		return nil, ErrWrongNode
	}
	absolutePath, err := filepath.Abs(databasePath)
	if err != nil {
		return nil, fmt.Errorf("resolve task journal path: %w", err)
	}
	if err := ensurePrivateDirectory(filepath.Dir(absolutePath)); err != nil {
		return nil, err
	}
	if _, err := ensurePrivateRegularFile(absolutePath); err != nil {
		return nil, err
	}
	lock, err := os.OpenFile(absolutePath, os.O_RDWR, 0)
	if err != nil {
		return nil, fmt.Errorf("open task journal ownership lock: %w", err)
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = lock.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			return nil, ErrJournalInUse
		}
		return nil, fmt.Errorf("acquire task journal ownership lock: %w", err)
	}
	lockOnError := func(err error) (*Store, error) {
		_ = syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
		_ = lock.Close()
		return nil, err
	}
	for _, sidecar := range []string{absolutePath + "-wal", absolutePath + "-shm"} {
		if _, err := ensurePrivateRegularFile(sidecar); err != nil {
			return lockOnError(fmt.Errorf("secure task journal sidecar: %w", err))
		}
	}
	dsnURL := url.URL{Scheme: "file", Path: filepath.ToSlash(absolutePath)}
	query := url.Values{}
	query.Add("_pragma", "busy_timeout(5000)")
	query.Add("_pragma", "foreign_keys(1)")
	query.Add("_pragma", "synchronous(FULL)")
	dsnURL.RawQuery = query.Encode()
	db, err := sql.Open("sqlite", dsnURL.String())
	if err != nil {
		return lockOnError(fmt.Errorf("open task journal SQLite: %w", err))
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(0)
	closeOnError := func(err error) (*Store, error) {
		_ = db.Close()
		return lockOnError(err)
	}
	if err := db.PingContext(ctx); err != nil {
		return closeOnError(fmt.Errorf("connect task journal SQLite: %w", err))
	}
	var journalMode string
	if err := db.QueryRowContext(ctx, `PRAGMA journal_mode=WAL`).Scan(&journalMode); err != nil {
		return closeOnError(fmt.Errorf("enable task journal WAL: %w", err))
	}
	if !strings.EqualFold(journalMode, "wal") {
		return closeOnError(fmt.Errorf("task journal requires WAL mode, SQLite returned %q", journalMode))
	}
	journalID, err := initializeSchema(ctx, db, nodeID)
	if err != nil {
		return closeOnError(err)
	}
	if err := verifyPrivateSidecars(absolutePath); err != nil {
		return closeOnError(err)
	}
	return &Store{db: db, lock: lock, nodeID: nodeID, journalID: journalID, now: time.Now}, nil
}

func initializeSchema(ctx context.Context, db *sql.DB, nodeID string) (string, error) {
	var version int
	if err := db.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&version); err != nil {
		return "", fmt.Errorf("read task journal schema version: %w", err)
	}
	if version > 3 {
		return "", fmt.Errorf("task journal schema version %d is newer than supported version 3", version)
	}
	if version == 1 {
		journalID, err := readJournalOwner(ctx, db, nodeID)
		if err != nil {
			return "", err
		}
		if err := migrateTaskJournalV1ToV2(ctx, db); err != nil {
			return "", err
		}
		if err := migrateTaskJournalV2ToV3(ctx, db); err != nil {
			return "", err
		}
		return journalID, nil
	}
	if version == 2 {
		journalID, err := readJournalOwner(ctx, db, nodeID)
		if err != nil {
			return "", err
		}
		if err := migrateTaskJournalV2ToV3(ctx, db); err != nil {
			return "", err
		}
		return journalID, nil
	}
	if version == 3 {
		return readJournalOwner(ctx, db, nodeID)
	}
	journalID, err := newJournalID()
	if err != nil {
		return "", fmt.Errorf("generate journal identity: %w", err)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return "", fmt.Errorf("begin task journal schema creation: %w", err)
	}
	statements := []string{
		`CREATE TABLE journal_owner (
			id INTEGER PRIMARY KEY CHECK(id=1),
			node_id TEXT NOT NULL,
			journal_id TEXT NOT NULL
		)`,
		`CREATE TABLE task_journal (
			task_id TEXT PRIMARY KEY,
			node_id TEXT NOT NULL,
			idempotency_key TEXT NOT NULL,
			request_digest BLOB NOT NULL CHECK(length(request_digest)=32),
			target_id TEXT NOT NULL,
			resource_key TEXT NOT NULL,
			action TEXT NOT NULL,
			status TEXT NOT NULL CHECK(status IN ('queued','running','succeeded','failed','timed_out','canceled','unknown')),
			created_at_ns INTEGER NOT NULL,
			updated_at_ns INTEGER NOT NULL,
			started_at_ns INTEGER,
			finished_at_ns INTEGER,
			execution_attempted INTEGER NOT NULL DEFAULT 0 CHECK(execution_attempted IN (0,1)),
			execution_completed INTEGER NOT NULL DEFAULT 0 CHECK(execution_completed IN (0,1)),
			failure_confirmed INTEGER NOT NULL DEFAULT 0 CHECK(failure_confirmed IN (0,1)),
			postcondition_verified INTEGER NOT NULL DEFAULT 0 CHECK(postcondition_verified IN (0,1)),
			process_terminated INTEGER NOT NULL DEFAULT 0 CHECK(process_terminated IN (0,1)),
			actual_result_confirmed INTEGER NOT NULL DEFAULT 0 CHECK(actual_result_confirmed IN (0,1)),
			cancellation_confirmed INTEGER NOT NULL DEFAULT 0 CHECK(cancellation_confirmed IN (0,1)),
			delivery_committed INTEGER NOT NULL DEFAULT 0 CHECK(delivery_committed IN (0,1)),
			progress_phase TEXT NOT NULL DEFAULT 'accepted',
			progress_completed INTEGER NOT NULL DEFAULT 0,
			progress_total INTEGER NOT NULL DEFAULT 0,
			result_code TEXT NOT NULL DEFAULT '',
			observed_state TEXT NOT NULL DEFAULT '',
			resource_revision TEXT NOT NULL DEFAULT '',
			execution_phase TEXT NOT NULL DEFAULT 'none' CHECK(execution_phase IN ('none','mutation_may_have_started','result_persisted')),
			baseline_verified INTEGER NOT NULL DEFAULT 0 CHECK(baseline_verified IN (0,1)),
			baseline_target_id TEXT NOT NULL DEFAULT '',
			baseline_action TEXT NOT NULL DEFAULT '',
			baseline_host_boot_id TEXT NOT NULL DEFAULT '',
			baseline_started_at TEXT NOT NULL DEFAULT '',
			baseline_restart_count INTEGER NOT NULL DEFAULT -1 CHECK(baseline_restart_count >= -1),
			baseline_running INTEGER NOT NULL DEFAULT 0 CHECK(baseline_running IN (0,1)),
			baseline_paused INTEGER NOT NULL DEFAULT 0 CHECK(baseline_paused IN (0,1)),
			baseline_restarting INTEGER NOT NULL DEFAULT 0 CHECK(baseline_restarting IN (0,1)),
			task_log BLOB NOT NULL DEFAULT X'',
			log_truncated INTEGER NOT NULL DEFAULT 0 CHECK(log_truncated IN (0,1)),
			UNIQUE(node_id, idempotency_key)
		)`,
		`CREATE INDEX task_journal_resource_status ON task_journal(node_id, resource_key, status)`,
		`CREATE TABLE active_resource_claims (
			node_id TEXT NOT NULL,
			resource_key TEXT NOT NULL,
			task_id TEXT NOT NULL UNIQUE REFERENCES task_journal(task_id) ON DELETE CASCADE,
			PRIMARY KEY(node_id, resource_key)
		)`,
	}
	for _, statement := range statements {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			_ = tx.Rollback()
			return "", fmt.Errorf("create task journal schema: %w", err)
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO journal_owner(id,node_id,journal_id) VALUES(1,?,?)`, nodeID, journalID); err != nil {
		_ = tx.Rollback()
		return "", fmt.Errorf("record task journal owner: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `PRAGMA user_version=3`); err != nil {
		_ = tx.Rollback()
		return "", fmt.Errorf("set task journal schema version: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return "", fmt.Errorf("commit task journal schema: %w", err)
	}
	return journalID, nil
}

func migrateTaskJournalV2ToV3(ctx context.Context, db *sql.DB) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin task journal v2-to-v3 migration: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `ALTER TABLE task_journal ADD COLUMN delivery_committed INTEGER NOT NULL DEFAULT 0 CHECK(delivery_committed IN (0,1))`); err != nil {
		return fmt.Errorf("add durable Core delivery marker: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `PRAGMA user_version=3`); err != nil {
		return fmt.Errorf("set task journal schema version 3: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit task journal v3 migration: %w", err)
	}
	return nil
}

func readJournalOwner(ctx context.Context, db *sql.DB, nodeID string) (string, error) {
	var owner string
	var journalID string
	if err := db.QueryRowContext(ctx, `SELECT node_id,journal_id FROM journal_owner WHERE id=1`).Scan(&owner, &journalID); err != nil {
		return "", fmt.Errorf("read task journal owner: %w", err)
	}
	if owner != nodeID {
		return "", ErrWrongNode
	}
	if !validJournalID(journalID) {
		return "", errors.New("task journal ID is invalid")
	}
	return journalID, nil
}

func migrateTaskJournalV1ToV2(ctx context.Context, db *sql.DB) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin task journal v1-to-v2 migration: %w", err)
	}
	defer tx.Rollback()
	statements := []string{
		`ALTER TABLE task_journal ADD COLUMN execution_phase TEXT NOT NULL DEFAULT 'none' CHECK(execution_phase IN ('none','mutation_may_have_started','result_persisted'))`,
		`ALTER TABLE task_journal ADD COLUMN baseline_verified INTEGER NOT NULL DEFAULT 0 CHECK(baseline_verified IN (0,1))`,
		`ALTER TABLE task_journal ADD COLUMN baseline_target_id TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE task_journal ADD COLUMN baseline_action TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE task_journal ADD COLUMN baseline_host_boot_id TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE task_journal ADD COLUMN baseline_started_at TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE task_journal ADD COLUMN baseline_restart_count INTEGER NOT NULL DEFAULT -1 CHECK(baseline_restart_count >= -1)`,
		`ALTER TABLE task_journal ADD COLUMN baseline_running INTEGER NOT NULL DEFAULT 0 CHECK(baseline_running IN (0,1))`,
		`ALTER TABLE task_journal ADD COLUMN baseline_paused INTEGER NOT NULL DEFAULT 0 CHECK(baseline_paused IN (0,1))`,
		`ALTER TABLE task_journal ADD COLUMN baseline_restarting INTEGER NOT NULL DEFAULT 0 CHECK(baseline_restarting IN (0,1))`,
	}
	for _, statement := range statements {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("migrate task journal schema to v2: %w", err)
		}
	}
	if _, err := tx.ExecContext(ctx, `PRAGMA user_version=2`); err != nil {
		return fmt.Errorf("set task journal schema version 2: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit task journal v2 migration: %w", err)
	}
	return nil
}

func newJournalID() (string, error) {
	var value [32]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(value[:]), nil
}

func validJournalID(value string) bool {
	if len(value) != 64 {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == 32
}

func ensurePrivateDirectory(path string) error {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("resolve task journal directory: %w", err)
	}
	absolute = filepath.Clean(absolute)
	if _, err := os.Lstat(absolute); errors.Is(err, os.ErrNotExist) {
		parent := filepath.Dir(absolute)
		if err := verifySafeParent(parent); err != nil {
			return fmt.Errorf("task journal parent is unsafe for creating its dedicated directory: %w", err)
		}
		if err := os.Mkdir(absolute, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
			return fmt.Errorf("create dedicated task journal directory: %w", err)
		}
	} else if err != nil {
		return fmt.Errorf("inspect task journal directory: %w", err)
	}
	return verifyPrivateDirectory(absolute)
}

func verifyPrivateDirectory(path string) error {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("resolve private directory: %w", err)
	}
	absolute = filepath.Clean(absolute)
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return fmt.Errorf("resolve private directory path: %w", err)
	}
	if filepath.Clean(resolved) != absolute {
		return errors.New("task journal directory path contains a symbolic link")
	}
	if err := verifySafeParent(filepath.Dir(absolute)); err != nil {
		return fmt.Errorf("task journal directory parent is unsafe: %w", err)
	}
	info, err := os.Lstat(absolute)
	if err != nil {
		return fmt.Errorf("inspect private directory: %w", err)
	}
	if !info.IsDir() {
		return errors.New("task journal parent is not a directory")
	}
	if err := verifyOwner(info); err != nil {
		return err
	}
	if info.Mode().Perm() != 0o700 {
		return fmt.Errorf("task journal directory permissions are %04o, want 0700", info.Mode().Perm())
	}
	return nil
}

func verifySafeParent(path string) error {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("resolve task journal parent: %w", err)
	}
	absolute = filepath.Clean(absolute)
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return fmt.Errorf("resolve task journal parent path: %w", err)
	}
	if filepath.Clean(resolved) != absolute {
		return errors.New("task journal path contains a symbolic link")
	}
	info, err := os.Lstat(absolute)
	if err != nil {
		return fmt.Errorf("inspect task journal parent: %w", err)
	}
	if !info.IsDir() {
		return errors.New("task journal parent is not a directory")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return errors.New("cannot verify task journal parent owner")
	}
	if int(stat.Uid) != os.Geteuid() && stat.Uid != 0 {
		return errors.New("task journal parent is not owned by Agent or root")
	}
	if info.Mode().Perm()&0o022 != 0 && !(stat.Uid == 0 && info.Mode()&os.ModeSticky != 0) {
		return errors.New("task journal parent is writable by group or other users")
	}
	return nil
}

func ensurePrivateRegularFile(path string) (bool, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		file, createErr := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
		if createErr != nil && !errors.Is(createErr, os.ErrExist) {
			return false, fmt.Errorf("create private journal file %s: %w", filepath.Base(path), createErr)
		}
		created := createErr == nil
		if file != nil {
			if syncErr := file.Sync(); syncErr != nil {
				_ = file.Close()
				return created, fmt.Errorf("sync private journal file %s: %w", filepath.Base(path), syncErr)
			}
			if closeErr := file.Close(); closeErr != nil {
				return created, fmt.Errorf("close private journal file %s: %w", filepath.Base(path), closeErr)
			}
			if syncErr := syncDirectory(filepath.Dir(path)); syncErr != nil {
				return created, fmt.Errorf("sync private journal directory: %w", syncErr)
			}
		}
		info, err = os.Lstat(path)
	}
	if err != nil {
		return false, fmt.Errorf("inspect private journal file %s: %w", filepath.Base(path), err)
	}
	if !info.Mode().IsRegular() {
		return false, fmt.Errorf("task journal file %s must be a regular file, not a symlink or special file", filepath.Base(path))
	}
	if err := verifyOwner(info); err != nil {
		return false, err
	}
	if info.Mode().Perm() != 0o600 {
		return false, fmt.Errorf("task journal file %s permissions are %04o, want 0600", filepath.Base(path), info.Mode().Perm())
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Nlink != 1 {
		return false, fmt.Errorf("task journal file %s must have exactly one hard link", filepath.Base(path))
	}
	return false, nil
}

func verifyOwner(info os.FileInfo) error {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(stat.Uid) != os.Geteuid() {
		return errors.New("task journal path is not owned by the current Agent user")
	}
	return nil
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

func verifyPrivateSidecars(databasePath string) error {
	for _, path := range []string{databasePath + "-wal", databasePath + "-shm"} {
		info, err := os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return fmt.Errorf("inspect SQLite sidecar %s: %w", filepath.Base(path), err)
		}
		if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
			return fmt.Errorf("SQLite sidecar %s is not a private 0600 regular file", filepath.Base(path))
		}
		if err := verifyOwner(info); err != nil {
			return err
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || stat.Nlink != 1 {
			return fmt.Errorf("SQLite sidecar %s must have exactly one hard link", filepath.Base(path))
		}
	}
	return nil
}

func validScopeID(value string) bool {
	if value == "" || len(value) > taskstate.MaxIdentityBytes {
		return false
	}
	for _, r := range value {
		if r < 0x21 || r == 0x7f {
			return false
		}
	}
	return true
}

func (s *Store) Close() error {
	if s == nil {
		return nil
	}
	var dbErr error
	if s.db != nil {
		dbErr = s.db.Close()
		s.db = nil
	}
	if s.lock != nil {
		_ = syscall.Flock(int(s.lock.Fd()), syscall.LOCK_UN)
		lockErr := s.lock.Close()
		s.lock = nil
		if dbErr == nil {
			dbErr = lockErr
		}
	}
	return dbErr
}

// JournalID is a random, durable identity for this node's journal contents.
// Core can use it to distinguish the same task database after reconnect from
// an empty/recreated journal whose execution history was lost.
func (s *Store) JournalID() string {
	if s == nil {
		return ""
	}
	return s.journalID
}

// Enqueue commits the safe request summary and deduplication digest before a
// caller can dispatch it. Payload is hashed but never written to SQLite.
func (s *Store) Enqueue(ctx context.Context, identity taskstate.Identity) (EnqueueResult, error) {
	return s.enqueue(ctx, identity, false)
}

// EnqueueDelivered atomically records that this durable journal received a
// task which Core had committed for delivery. The marker is local recovery
// evidence only; it is never included in an Agent report as Core-owned proof.
func (s *Store) EnqueueDelivered(ctx context.Context, identity taskstate.Identity) (EnqueueResult, error) {
	return s.enqueue(ctx, identity, true)
}

func (s *Store) enqueue(ctx context.Context, identity taskstate.Identity, delivered bool) (EnqueueResult, error) {
	if identity.NodeID != s.nodeID {
		return EnqueueResult{}, ErrWrongNode
	}
	digest, err := taskstate.RequestDigest(identity)
	if err != nil {
		return EnqueueResult{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return EnqueueResult{}, fmt.Errorf("begin task enqueue: %w", err)
	}
	defer tx.Rollback()
	byKey, keyErr := loadByIdempotency(ctx, tx, identity.NodeID, identity.IdempotencyKey)
	if keyErr != nil && !errors.Is(keyErr, sql.ErrNoRows) {
		return EnqueueResult{}, fmt.Errorf("read idempotency key: %w", keyErr)
	}
	byTaskID, taskIDErr := loadByTaskIDAnyNode(ctx, tx, identity.TaskID)
	if taskIDErr != nil && !errors.Is(taskIDErr, sql.ErrNoRows) {
		return EnqueueResult{}, fmt.Errorf("read task ID: %w", taskIDErr)
	}
	if taskIDErr == nil {
		if byTaskID.NodeID != identity.NodeID || byTaskID.idempotencyKey != identity.IdempotencyKey || byTaskID.RequestDigest != digest {
			return EnqueueResult{}, ErrTaskIDConflict
		}
		if keyErr != nil || byKey.TaskID != byTaskID.TaskID {
			return EnqueueResult{}, ErrIdempotencyConflict
		}
		if delivered {
			if _, err := tx.ExecContext(ctx, `UPDATE task_journal SET delivery_committed=1,updated_at_ns=CASE WHEN delivery_committed=0 THEN ? ELSE updated_at_ns END WHERE node_id=? AND task_id=?`, s.now().UTC().UnixNano(), identity.NodeID, byTaskID.TaskID); err != nil {
				return EnqueueResult{}, fmt.Errorf("persist Core-delivered task marker: %w", err)
			}
			byTaskID, err = loadByTaskID(ctx, tx, identity.NodeID, byTaskID.TaskID)
			if err != nil {
				return EnqueueResult{}, fmt.Errorf("read Core-delivered task marker: %w", err)
			}
		}
		if err := tx.Commit(); err != nil {
			return EnqueueResult{}, fmt.Errorf("commit task ID lookup: %w", err)
		}
		return EnqueueResult{Task: byTaskID.Snapshot, Created: false}, nil
	}
	if keyErr == nil {
		if byKey.RequestDigest != digest {
			return EnqueueResult{}, ErrIdempotencyConflict
		}
		if delivered {
			if _, err := tx.ExecContext(ctx, `UPDATE task_journal SET delivery_committed=1,updated_at_ns=CASE WHEN delivery_committed=0 THEN ? ELSE updated_at_ns END WHERE node_id=? AND task_id=?`, s.now().UTC().UnixNano(), identity.NodeID, byKey.TaskID); err != nil {
				return EnqueueResult{}, fmt.Errorf("persist Core-delivered task marker: %w", err)
			}
			byKey, err = loadByIdempotency(ctx, tx, identity.NodeID, identity.IdempotencyKey)
			if err != nil {
				return EnqueueResult{}, fmt.Errorf("read Core-delivered task marker: %w", err)
			}
		}
		if err := tx.Commit(); err != nil {
			return EnqueueResult{}, fmt.Errorf("commit idempotent task lookup: %w", err)
		}
		return EnqueueResult{Task: byKey.Snapshot, Created: false}, nil
	}
	var owner string
	err = tx.QueryRowContext(ctx, `SELECT task_id FROM active_resource_claims WHERE node_id=? AND resource_key=?`, identity.NodeID, identity.ResourceKey).Scan(&owner)
	if err == nil {
		return EnqueueResult{}, fmt.Errorf("%w: task %s", ErrResourceBusy, owner)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return EnqueueResult{}, fmt.Errorf("check active resource claim: %w", err)
	}
	now := s.now().UTC().UnixNano()
	deliveredValue := 0
	if delivered {
		deliveredValue = 1
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO task_journal (
		task_id,node_id,idempotency_key,request_digest,target_id,resource_key,action,status,created_at_ns,updated_at_ns,delivery_committed
	) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, identity.TaskID, identity.NodeID, identity.IdempotencyKey, digest[:], identity.TargetID,
		identity.ResourceKey, identity.Action, taskstate.Queued, now, now, deliveredValue)
	if err != nil {
		return EnqueueResult{}, fmt.Errorf("persist queued task before dispatch: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO active_resource_claims(node_id,resource_key,task_id) VALUES(?,?,?)`, identity.NodeID, identity.ResourceKey, identity.TaskID); err != nil {
		return EnqueueResult{}, fmt.Errorf("claim task resource: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return EnqueueResult{}, fmt.Errorf("commit queued task: %w", err)
	}
	snapshot, err := s.Get(ctx, identity.TaskID)
	if err != nil {
		return EnqueueResult{}, fmt.Errorf("read committed task: %w", err)
	}
	return EnqueueResult{Task: snapshot, Created: true}, nil
}

// BeginExecution must complete before the caller crosses into its executor.
// If its process stops after this commit, recovery changes the task to unknown.
func (s *Store) BeginExecution(ctx context.Context, taskID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin task execution record: %w", err)
	}
	defer tx.Rollback()
	var status string
	if err := tx.QueryRowContext(ctx, `SELECT status FROM task_journal WHERE node_id=? AND task_id=?`, s.nodeID, taskID).Scan(&status); err != nil {
		return taskLookupError(err)
	}
	if err := taskstate.CanTransition(taskstate.Status(status), taskstate.Running, taskstate.Evidence{ExecutionAttempted: true}); err != nil {
		return err
	}
	now := s.now().UTC().UnixNano()
	result, err := tx.ExecContext(ctx, `UPDATE task_journal SET status='running',execution_attempted=1,started_at_ns=?,updated_at_ns=?,progress_phase='executing' WHERE node_id=? AND task_id=? AND status='queued'`, now, now, s.nodeID, taskID)
	if err != nil {
		return fmt.Errorf("persist running task before executor: %w", err)
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return taskstate.ErrInvalidStatus
	}
	return tx.Commit()
}

// PrepareMutation durably binds a sanitized preflight observation to a running
// task before the caller may issue its Docker write. The persisted phase means
// a mutation may have started after this commit; it does not claim that an
// Engine request was sent or completed.
func (s *Store) PrepareMutation(ctx context.Context, taskID string, baseline ExecutionBaseline) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin durable execution baseline: %w", err)
	}
	defer tx.Rollback()
	var status, targetID, action, phase string
	if err := tx.QueryRowContext(ctx, `SELECT status,target_id,action,execution_phase FROM task_journal WHERE node_id=? AND task_id=?`, s.nodeID, taskID).
		Scan(&status, &targetID, &action, &phase); err != nil {
		return taskLookupError(err)
	}
	if taskstate.Status(status) != taskstate.Running {
		return taskstate.ErrInvalidStatus
	}
	if phase != string(ExecutionPhaseNone) {
		return ErrBaselineAlreadySet
	}
	if err := validateExecutionBaseline(targetID, action, baseline); err != nil {
		return err
	}
	now := s.now().UTC().UnixNano()
	result, err := tx.ExecContext(ctx, `UPDATE task_journal SET execution_phase=?,baseline_verified=1,
		baseline_target_id=?,baseline_action=?,baseline_host_boot_id=?,baseline_started_at=?,baseline_restart_count=?,
		baseline_running=?,baseline_paused=?,baseline_restarting=?,progress_phase=?,updated_at_ns=?
		WHERE node_id=? AND task_id=? AND status='running' AND execution_phase='none'`,
		ExecutionPhaseMutationMayHaveStarted, baseline.TargetID, baseline.Action, baseline.HostBootID, baseline.StartedAt,
		baseline.RestartCount, boolInt(baseline.Running), boolInt(baseline.Paused), boolInt(baseline.Restarting),
		PhaseExecuting, now, s.nodeID, taskID)
	if err != nil {
		return fmt.Errorf("persist durable execution baseline: %w", err)
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return ErrBaselineAlreadySet
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit durable execution baseline: %w", err)
	}
	return nil
}

func validateExecutionBaseline(targetID, action string, baseline ExecutionBaseline) error {
	if baseline.TargetID != targetID || baseline.Action != action ||
		!safeToken(baseline.TargetID, taskstate.MaxIdentityBytes) || !safeToken(baseline.Action, taskstate.MaxActionBytes) ||
		!validBootID(baseline.HostBootID) || baseline.RestartCount < 0 ||
		(baseline.Paused && !baseline.Running) {
		return ErrInvalidBaseline
	}
	startedAt, err := time.Parse(time.RFC3339Nano, baseline.StartedAt)
	if err != nil || startedAt.UTC().Format(time.RFC3339Nano) != baseline.StartedAt {
		return ErrInvalidBaseline
	}
	return nil
}

func validBootID(value string) bool {
	if len(value) != 36 {
		return false
	}
	for index, char := range value {
		if index == 8 || index == 13 || index == 18 || index == 23 {
			if char != '-' {
				return false
			}
			continue
		}
		if !((char >= '0' && char <= '9') || (char >= 'a' && char <= 'f')) {
			return false
		}
	}
	return true
}

func (s *Store) UpdateProgress(ctx context.Context, taskID string, progress Progress) error {
	if !validProgress(progress) {
		return ErrInvalidProgress
	}
	result, err := s.db.ExecContext(ctx, `UPDATE task_journal SET progress_phase=?,progress_completed=?,progress_total=?,updated_at_ns=? WHERE node_id=? AND task_id=? AND status IN ('queued','running','unknown')`,
		progress.Phase, progress.Completed, progress.Total, s.now().UTC().UnixNano(), s.nodeID, taskID)
	if err != nil {
		return fmt.Errorf("persist task progress: %w", err)
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return taskLookupOrState(ctx, s.db, s.nodeID, taskID)
	}
	return nil
}

func (s *Store) AppendLog(ctx context.Context, taskID string, chunk LogChunk) (bool, error) {
	if !chunk.Redacted {
		return false, ErrUnredactedLog
	}
	if len(chunk.Bytes) == 0 {
		return false, nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin bounded log append: %w", err)
	}
	defer tx.Rollback()
	var status string
	var current []byte
	var truncated int
	if err := tx.QueryRowContext(ctx, `SELECT status,task_log,log_truncated FROM task_journal WHERE node_id=? AND task_id=?`, s.nodeID, taskID).Scan(&status, &current, &truncated); err != nil {
		return false, taskLookupError(err)
	}
	if taskstate.Status(status) != taskstate.Running {
		return false, taskstate.ErrInvalidStatus
	}
	if len(current) > MaxTaskLogBytes {
		return false, errors.New("stored task log exceeds configured limit")
	}
	remaining := MaxTaskLogBytes - len(current)
	appendLength := len(chunk.Bytes)
	if appendLength > remaining {
		appendLength = remaining
		truncated = 1
	}
	newLog := make([]byte, len(current)+appendLength)
	copy(newLog, current)
	copy(newLog[len(current):], chunk.Bytes[:appendLength])
	if _, err := tx.ExecContext(ctx, `UPDATE task_journal SET task_log=?,log_truncated=?,updated_at_ns=? WHERE node_id=? AND task_id=? AND status='running'`, newLog, truncated, s.now().UTC().UnixNano(), s.nodeID, taskID); err != nil {
		return false, fmt.Errorf("persist bounded task log: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit bounded task log: %w", err)
	}
	return truncated != 0, nil
}

// Finish applies a proof-checked transition. Unknown is intentionally excluded:
// use MarkUnknown or RecoverInterrupted, then reconcile against the real Engine.
func (s *Store) Finish(ctx context.Context, taskID string, status taskstate.Status, evidence taskstate.Evidence, result Result) error {
	if !taskstate.IsTerminal(status) {
		return fmt.Errorf("%w: Finish requires a terminal status", taskstate.ErrInvalidStatus)
	}
	if err := validateResult(status, result); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin task completion: %w", err)
	}
	defer tx.Rollback()
	current, oldEvidence, err := loadStatusEvidence(ctx, tx, s.nodeID, taskID)
	if err != nil {
		return taskLookupError(err)
	}
	merged := mergeEvidence(oldEvidence, evidence)
	if err := taskstate.CanTransition(current, status, merged); err != nil {
		return err
	}
	now := s.now().UTC().UnixNano()
	if _, err := tx.ExecContext(ctx, `UPDATE task_journal SET status=?,updated_at_ns=?,finished_at_ns=?,
		execution_attempted=?,execution_completed=?,failure_confirmed=?,postcondition_verified=?,process_terminated=?,actual_result_confirmed=?,cancellation_confirmed=?,
		execution_phase=?,result_code=?,observed_state=?,resource_revision=? WHERE node_id=? AND task_id=?`, status, now, now,
		boolInt(merged.ExecutionAttempted), boolInt(merged.ExecutionCompleted), boolInt(merged.FailureConfirmed), boolInt(merged.PostconditionVerified), boolInt(merged.ProcessTerminated), boolInt(merged.ActualResultConfirmed), boolInt(merged.CancellationConfirmed),
		ExecutionPhaseResultPersisted, result.Code, result.ObservedState, result.ResourceRevision, s.nodeID, taskID); err != nil {
		return fmt.Errorf("persist verified task result: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM active_resource_claims WHERE task_id=?`, taskID); err != nil {
		return fmt.Errorf("release completed task resource: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit verified task result: %w", err)
	}
	return nil
}

func (s *Store) MarkUnknown(ctx context.Context, taskID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin unknown task update: %w", err)
	}
	defer tx.Rollback()
	current, _, err := loadStatusEvidence(ctx, tx, s.nodeID, taskID)
	if err != nil {
		return taskLookupError(err)
	}
	if err := taskstate.CanTransition(current, taskstate.Unknown, taskstate.Evidence{}); err != nil {
		return err
	}
	now := s.now().UTC().UnixNano()
	if _, err := tx.ExecContext(ctx, `UPDATE task_journal SET status='unknown',updated_at_ns=?,finished_at_ns=NULL,result_code=?,progress_phase=? WHERE node_id=? AND task_id=? AND status='running'`, now, ResultUncertain, PhaseReconciling, s.nodeID, taskID); err != nil {
		return fmt.Errorf("persist unknown task state: %w", err)
	}
	return tx.Commit()
}

// RecoverInterrupted must be called only after the old Agent process is known
// to be stopped. It never replays work: running tasks and Core-delivered queued
// tasks become unknown, and their resource claims stay held until actual Engine
// state is queried and confirmed. A legacy queued row without the v3 delivery
// marker remains queued because the old journal cannot prove Core dispatch.
func (s *Store) RecoverInterrupted(ctx context.Context) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin interrupted-task recovery: %w", err)
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE task_journal SET status='unknown',result_code=?,progress_phase=?,updated_at_ns=?,finished_at_ns=NULL WHERE node_id=? AND (status='running' OR (status='queued' AND delivery_committed=1))`, ResultUncertain, PhaseReconciling, s.now().UTC().UnixNano(), s.nodeID)
	if err != nil {
		return 0, fmt.Errorf("mark interrupted tasks unknown: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("count interrupted tasks: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit interrupted-task recovery: %w", err)
	}
	return count, nil
}

// SnapshotAll reads one bounded, consistent SQLite read snapshot. It does not
// page a changing live table with LIMIT/OFFSET: callers receive a frozen
// in-memory candidate and may split it into wire pages only after this method
// completes. On any limit, deadline, decode, or commit error it returns no
// partial snapshot, so absence is never inferred from incomplete history.
func (s *Store) SnapshotAll(ctx context.Context) ([]Snapshot, error) {
	bounded, cancel := context.WithTimeout(ctx, JournalSnapshotTimeout)
	defer cancel()
	tx, err := s.db.BeginTx(bounded, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, fmt.Errorf("begin consistent task journal snapshot: %w", err)
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(bounded, `SELECT `+snapshotColumns+` FROM task_journal WHERE node_id=? ORDER BY task_id LIMIT ?`, s.nodeID, MaxJournalSnapshotTasks+1)
	if err != nil {
		return nil, fmt.Errorf("query consistent task journal snapshot: %w", err)
	}
	snapshots := make([]Snapshot, 0, 128)
	var estimatedBytes int
	for rows.Next() {
		if len(snapshots) >= MaxJournalSnapshotTasks {
			_ = rows.Close()
			return nil, errors.New("task journal snapshot exceeds record limit")
		}
		entry, err := scanLoaded(rows)
		if err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("decode task journal snapshot: %w", err)
		}
		encoded, err := json.Marshal(entry.Snapshot)
		if err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("measure task journal snapshot: %w", err)
		}
		estimatedBytes += len(encoded)
		if estimatedBytes > MaxJournalSnapshotBytes {
			_ = rows.Close()
			return nil, errors.New("task journal snapshot exceeds byte limit")
		}
		snapshots = append(snapshots, entry.Snapshot)
		if err := bounded.Err(); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("task journal snapshot deadline: %w", err)
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, fmt.Errorf("read task journal snapshot: %w", err)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("close task journal snapshot: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("finish consistent task journal snapshot: %w", err)
	}
	return snapshots, nil
}

func (s *Store) Get(ctx context.Context, taskID string) (Snapshot, error) {
	entry, err := loadByTaskID(ctx, s.db, s.nodeID, taskID)
	if err != nil {
		return Snapshot{}, taskLookupError(err)
	}
	return entry.Snapshot, nil
}

func (s *Store) GetLog(ctx context.Context, taskID string) ([]byte, bool, error) {
	var log []byte
	var truncated int
	err := s.db.QueryRowContext(ctx, `SELECT task_log,log_truncated FROM task_journal WHERE node_id=? AND task_id=?`, s.nodeID, taskID).Scan(&log, &truncated)
	if err != nil {
		return nil, false, taskLookupError(err)
	}
	if len(log) > MaxTaskLogBytes {
		return nil, false, errors.New("stored task log exceeds configured limit")
	}
	return log, truncated != 0, nil
}

type loaded struct {
	Snapshot
	RequestDigest  [sha256.Size]byte
	idempotencyKey string
}

type rowScanner interface {
	Scan(dest ...any) error
}

const snapshotColumns = `task_id,node_id,target_id,resource_key,action,status,created_at_ns,updated_at_ns,started_at_ns,finished_at_ns,
	execution_attempted,execution_completed,failure_confirmed,postcondition_verified,process_terminated,actual_result_confirmed,cancellation_confirmed,
	progress_phase,progress_completed,progress_total,result_code,observed_state,resource_revision,execution_phase,baseline_verified,
	baseline_target_id,baseline_action,baseline_host_boot_id,baseline_started_at,baseline_restart_count,baseline_running,baseline_paused,baseline_restarting,
	request_digest,length(task_log),log_truncated,idempotency_key,delivery_committed`

func scanLoaded(scanner rowScanner) (loaded, error) {
	var entry loaded
	var status string
	var createdNS, updatedNS int64
	var startedNS, finishedNS sql.NullInt64
	var attempted, completed, failed, verified, terminated, actual, canceled int
	var phase string
	var progressCompleted, progressTotal uint64
	var code, observed, revision, executionPhase string
	var baselineVerified, baselineRunning, baselinePaused, baselineRestarting, deliveryCommitted int
	var baselineTarget, baselineAction, baselineBootID, baselineStartedAt string
	var baselineRestartCount int
	var digest []byte
	if err := scanner.Scan(&entry.TaskID, &entry.NodeID, &entry.TargetID, &entry.ResourceKey, &entry.Action, &status, &createdNS, &updatedNS, &startedNS, &finishedNS,
		&attempted, &completed, &failed, &verified, &terminated, &actual, &canceled, &phase, &progressCompleted, &progressTotal, &code, &observed, &revision,
		&executionPhase, &baselineVerified, &baselineTarget, &baselineAction, &baselineBootID, &baselineStartedAt, &baselineRestartCount,
		&baselineRunning, &baselinePaused, &baselineRestarting, &digest, &entry.LogBytes, &entry.LogTruncated, &entry.idempotencyKey, &deliveryCommitted); err != nil {
		return loaded{}, err
	}
	if len(digest) != sha256.Size {
		return loaded{}, errors.New("invalid stored task request digest")
	}
	copy(entry.RequestDigest[:], digest)
	entry.Snapshot.RequestDigest = entry.RequestDigest
	entry.Snapshot.IdempotencyKey = entry.idempotencyKey
	entry.Snapshot.DeliveryCommitted = deliveryCommitted != 0
	entry.Status = taskstate.Status(status)
	entry.CreatedAt = time.Unix(0, createdNS).UTC()
	entry.UpdatedAt = time.Unix(0, updatedNS).UTC()
	if startedNS.Valid {
		started := time.Unix(0, startedNS.Int64).UTC()
		entry.StartedAt = &started
	}
	if finishedNS.Valid {
		finished := time.Unix(0, finishedNS.Int64).UTC()
		entry.FinishedAt = &finished
	}
	entry.Evidence = taskstate.Evidence{
		ExecutionAttempted: attempted != 0, ExecutionCompleted: completed != 0, FailureConfirmed: failed != 0,
		PostconditionVerified: verified != 0, ProcessTerminated: terminated != 0,
		ActualResultConfirmed: actual != 0, CancellationConfirmed: canceled != 0,
	}
	entry.Progress = Progress{Phase: ProgressPhase(phase), Completed: progressCompleted, Total: progressTotal}
	entry.Result = Result{Code: ResultCode(code), ObservedState: observed, ResourceRevision: revision}
	entry.ExecutionPhase = ExecutionPhase(executionPhase)
	if baselineVerified != 0 && (entry.ExecutionPhase == ExecutionPhaseMutationMayHaveStarted || entry.ExecutionPhase == ExecutionPhaseResultPersisted) {
		baseline := ExecutionBaseline{TargetID: baselineTarget, Action: baselineAction, HostBootID: baselineBootID,
			StartedAt: baselineStartedAt, RestartCount: baselineRestartCount,
			Running: baselineRunning != 0, Paused: baselinePaused != 0, Restarting: baselineRestarting != 0}
		if validateExecutionBaseline(entry.TargetID, entry.Action, baseline) == nil {
			entry.Baseline = &baseline
		}
	}
	return entry, nil
}

func loadByTaskID(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, nodeID, taskID string) (loaded, error) {
	return scanLoaded(q.QueryRowContext(ctx, `SELECT `+snapshotColumns+` FROM task_journal WHERE node_id=? AND task_id=?`, nodeID, taskID))
}

func loadByTaskIDAnyNode(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, taskID string) (loaded, error) {
	return scanLoaded(q.QueryRowContext(ctx, `SELECT `+snapshotColumns+` FROM task_journal WHERE task_id=?`, taskID))
}

func loadByIdempotency(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, nodeID, key string) (loaded, error) {
	return scanLoaded(q.QueryRowContext(ctx, `SELECT `+snapshotColumns+` FROM task_journal WHERE node_id=? AND idempotency_key=?`, nodeID, key))
}

func loadStatusEvidence(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, nodeID, taskID string) (taskstate.Status, taskstate.Evidence, error) {
	var status string
	var attempted, completed, failed, verified, terminated, actual, canceled int
	err := q.QueryRowContext(ctx, `SELECT status,execution_attempted,execution_completed,failure_confirmed,postcondition_verified,process_terminated,actual_result_confirmed,cancellation_confirmed FROM task_journal WHERE node_id=? AND task_id=?`, nodeID, taskID).Scan(&status, &attempted, &completed, &failed, &verified, &terminated, &actual, &canceled)
	return taskstate.Status(status), taskstate.Evidence{
		ExecutionAttempted: attempted != 0, ExecutionCompleted: completed != 0, FailureConfirmed: failed != 0,
		PostconditionVerified: verified != 0, ProcessTerminated: terminated != 0,
		ActualResultConfirmed: actual != 0, CancellationConfirmed: canceled != 0,
	}, err
}

func taskLookupError(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return ErrTaskNotFound
	}
	return err
}

func taskLookupOrState(ctx context.Context, db *sql.DB, nodeID, taskID string) error {
	var status string
	err := db.QueryRowContext(ctx, `SELECT status FROM task_journal WHERE node_id=? AND task_id=?`, nodeID, taskID).Scan(&status)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrTaskNotFound
	}
	if err != nil {
		return fmt.Errorf("read task state: %w", err)
	}
	return taskstate.ErrInvalidStatus
}

func mergeEvidence(old, next taskstate.Evidence) taskstate.Evidence {
	return taskstate.Evidence{
		ExecutionAttempted:    old.ExecutionAttempted || next.ExecutionAttempted,
		ExecutionCompleted:    old.ExecutionCompleted || next.ExecutionCompleted,
		FailureConfirmed:      old.FailureConfirmed || next.FailureConfirmed,
		PostconditionVerified: old.PostconditionVerified || next.PostconditionVerified,
		ProcessTerminated:     old.ProcessTerminated || next.ProcessTerminated,
		ActualResultConfirmed: old.ActualResultConfirmed || next.ActualResultConfirmed,
		CancellationConfirmed: old.CancellationConfirmed || next.CancellationConfirmed,
	}
}

func validProgress(progress Progress) bool {
	if progress.Total > 0 && progress.Completed > progress.Total {
		return false
	}
	switch progress.Phase {
	case PhaseAccepted, PhasePreparing, PhaseExecuting, PhaseVerifying, PhaseReconciling:
		return true
	default:
		return false
	}
}

func validateResult(status taskstate.Status, result Result) error {
	want := map[taskstate.Status]ResultCode{
		taskstate.Succeeded: ResultVerified,
		taskstate.Failed:    ResultFailed,
		taskstate.TimedOut:  ResultTimedOut,
		taskstate.Canceled:  ResultCanceled,
	}[status]
	if result.Code != want || !safeToken(string(result.Code), 32) || !safeToken(result.ObservedState, 64) || !safeToken(result.ResourceRevision, 128) {
		return ErrInvalidResult
	}
	return nil
}

func safeToken(value string, maximum int) bool {
	if value == "" {
		return true
	}
	if len(value) > maximum {
		return false
	}
	for _, r := range value {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || strings.ContainsRune("._:-", r)) {
			return false
		}
	}
	return true
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
