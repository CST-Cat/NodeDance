package containerrebuild

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/CST-Cat/NodeDance/internal/protocol"
	"github.com/moby/moby/api/types/network"
	_ "modernc.org/sqlite"
)

type Phase string

const (
	PhasePreflight    Phase = "preflight"
	PhaseSnapshotting Phase = "snapshotting"
	PhaseSnapshotted  Phase = "snapshotted"
	PhaseStopping     Phase = "stopping"
	PhaseStopped      Phase = "stopped"
	PhaseRenaming     Phase = "renaming_old"
	PhaseRenamed      Phase = "renamed_old"
	PhaseDetaching    Phase = "detaching_networks"
	PhaseDetached     Phase = "detached_old"
	PhaseCreating     Phase = "creating_new"
	PhaseCreated      Phase = "created_new"
	PhaseStarting     Phase = "starting_new"
	PhaseStarted      Phase = "started_new"
	PhaseVerifying    Phase = "verifying_new"
	PhaseVerified     Phase = "verified_new"
	PhaseRollingBack  Phase = "rolling_back"
	PhaseRolledBack   Phase = "rolled_back"
	PhaseCleaning     Phase = "cleaning"
	PhaseCleaned      Phase = "cleaned"
)

type Record struct {
	TaskID          string
	NodeID          string
	OriginalID      string
	OriginalName    string
	BackupName      string
	NewID           string
	CleanupTaskID   string
	CleanupTargetID string
	CleanupPhase    Phase
	SnapshotRef     string
	SnapshotID      string
	PreviousRunning bool
	RestartPolicy   string
	Phase           Phase
	Outcome         string
	ErrorCode       string
	Spec            protocol.RebuildSpec
	Networks        map[string]*network.EndpointSettings
	UpdatedAt       time.Time
}

type Store struct{ db *sql.DB }

func OpenStore(ctx context.Context, path string) (*Store, error) {
	absolute, err := filepath.Abs(path)
	if err != nil || absolute == "." {
		return nil, errors.New("container rebuild store path is invalid")
	}
	dir := filepath.Dir(absolute)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, fmt.Errorf("create private rebuild state directory: %w", err)
	}
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0077 != 0 {
		return nil, errors.New("container rebuild state directory must be a private real directory")
	}
	if file, err := os.OpenFile(absolute, os.O_CREATE|os.O_RDWR|os.O_EXCL, 0600); err == nil {
		_ = file.Close()
	} else if !errors.Is(err, os.ErrExist) {
		return nil, fmt.Errorf("create private rebuild database: %w", err)
	}
	fileInfo, err := os.Lstat(absolute)
	if err != nil || !fileInfo.Mode().IsRegular() || fileInfo.Mode().Perm()&0077 != 0 {
		return nil, errors.New("container rebuild database must be a private regular file")
	}
	dsn := url.URL{Scheme: "file", Path: filepath.ToSlash(absolute)}
	query := url.Values{}
	query.Add("_pragma", "busy_timeout(5000)")
	query.Add("_pragma", "foreign_keys(1)")
	query.Add("_pragma", "synchronous(FULL)")
	dsn.RawQuery = query.Encode()
	db, err := sql.Open("sqlite", dsn.String())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("connect rebuild database: %w", err)
	}
	var mode string
	if err := db.QueryRowContext(ctx, `PRAGMA journal_mode=WAL`).Scan(&mode); err != nil || !strings.EqualFold(mode, "wal") {
		_ = db.Close()
		return nil, errors.New("container rebuild database could not enable WAL mode")
	}
	if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS container_rebuilds (
		task_id TEXT PRIMARY KEY,
		node_id TEXT NOT NULL,
		original_id TEXT NOT NULL,
		original_name TEXT NOT NULL,
		backup_name TEXT NOT NULL,
		new_id TEXT NOT NULL DEFAULT '',
		cleanup_task_id TEXT NOT NULL DEFAULT '',
		cleanup_target_id TEXT NOT NULL DEFAULT '',
		cleanup_phase TEXT NOT NULL DEFAULT '',
		snapshot_ref TEXT NOT NULL,
		snapshot_id TEXT NOT NULL DEFAULT '',
		previous_running INTEGER NOT NULL CHECK(previous_running IN (0,1)),
		restart_policy TEXT NOT NULL,
		phase TEXT NOT NULL,
		outcome TEXT NOT NULL DEFAULT '',
		error_code TEXT NOT NULL DEFAULT '',
		spec_json TEXT NOT NULL,
		networks_json TEXT NOT NULL DEFAULT '{}',
		updated_at_ns INTEGER NOT NULL
	) WITHOUT ROWID`); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("initialize rebuild database: %w", err)
	}
	for _, sidecar := range []string{absolute + "-wal", absolute + "-shm"} {
		if err := secureSidecar(sidecar); err != nil {
			_ = db.Close()
			return nil, err
		}
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func (s *Store) PutNew(ctx context.Context, record Record) (Record, bool, error) {
	if !validRecord(record) {
		return Record{}, false, ErrRecordConflict
	}
	encoded, err := json.Marshal(record.Spec)
	if err != nil {
		return Record{}, false, err
	}
	networks, err := json.Marshal(record.Networks)
	if err != nil {
		return Record{}, false, err
	}
	result, err := s.db.ExecContext(ctx, `INSERT INTO container_rebuilds(task_id,node_id,original_id,original_name,backup_name,new_id,cleanup_task_id,cleanup_target_id,cleanup_phase,snapshot_ref,snapshot_id,previous_running,restart_policy,phase,outcome,error_code,spec_json,networks_json,updated_at_ns)
		VALUES(?,?,?,?,?,'','','','',?,'',?,?,?,'','',?,?,?) ON CONFLICT(task_id) DO NOTHING`,
		record.TaskID, record.NodeID, record.OriginalID, record.OriginalName, record.BackupName, record.SnapshotRef,
		boolInt(record.PreviousRunning), record.RestartPolicy, record.Phase, string(encoded), string(networks), time.Now().UTC().UnixNano())
	if err != nil {
		return Record{}, false, fmt.Errorf("persist initial rebuild phase: %w", err)
	}
	loaded, err := s.Get(ctx, record.TaskID)
	if err != nil {
		return Record{}, false, err
	}
	loadedSpec, _ := json.Marshal(loaded.Spec)
	if loaded.NodeID != record.NodeID || loaded.OriginalID != record.OriginalID || !bytes.Equal(loadedSpec, encoded) || loaded.BackupName != record.BackupName {
		return Record{}, false, ErrRecordConflict
	}
	created, _ := result.RowsAffected()
	return loaded, created == 1, nil
}

func (s *Store) Get(ctx context.Context, taskID string) (Record, error) {
	var record Record
	var running int
	var spec string
	var networks string
	var updated int64
	err := s.db.QueryRowContext(ctx, `SELECT task_id,node_id,original_id,original_name,backup_name,new_id,cleanup_task_id,cleanup_target_id,cleanup_phase,snapshot_ref,snapshot_id,previous_running,restart_policy,phase,outcome,error_code,spec_json,networks_json,updated_at_ns FROM container_rebuilds WHERE task_id=?`, taskID).
		Scan(&record.TaskID, &record.NodeID, &record.OriginalID, &record.OriginalName, &record.BackupName, &record.NewID, &record.CleanupTaskID, &record.CleanupTargetID, &record.CleanupPhase, &record.SnapshotRef,
			&record.SnapshotID, &running, &record.RestartPolicy, &record.Phase, &record.Outcome, &record.ErrorCode, &spec, &networks, &updated)
	if err != nil {
		return Record{}, err
	}
	if err := json.Unmarshal([]byte(spec), &record.Spec); err != nil {
		return Record{}, errors.New("stored rebuild specification is invalid")
	}
	if err := json.Unmarshal([]byte(networks), &record.Networks); err != nil {
		return Record{}, errors.New("stored rebuild network plan is invalid")
	}
	record.PreviousRunning = running != 0
	record.UpdatedAt = time.Unix(0, updated).UTC()
	if !validRecord(record) {
		return Record{}, ErrRecordConflict
	}
	return record, nil
}

func (s *Store) GetByCleanupTask(ctx context.Context, taskID string) (Record, error) {
	var rebuildTaskID string
	if err := s.db.QueryRowContext(ctx, `SELECT task_id FROM container_rebuilds WHERE cleanup_task_id=?`, taskID).Scan(&rebuildTaskID); err != nil {
		return Record{}, err
	}
	return s.Get(ctx, rebuildTaskID)
}

func (s *Store) BindCleanup(ctx context.Context, rebuildTaskID, cleanupTaskID, targetID string) (Record, error) {
	if cleanupTaskID == "" || len(cleanupTaskID) > 128 || !protocol.IsFullContainerID(targetID) {
		return Record{}, ErrRecordConflict
	}
	result, err := s.db.ExecContext(ctx, `UPDATE container_rebuilds SET cleanup_task_id=CASE WHEN cleanup_task_id='' THEN ? ELSE cleanup_task_id END,
		cleanup_target_id=CASE WHEN cleanup_target_id='' THEN ? ELSE cleanup_target_id END,cleanup_phase=?,updated_at_ns=?
		WHERE task_id=? AND outcome IN ('succeeded','failed') AND (cleanup_task_id='' OR cleanup_task_id=?) AND (cleanup_target_id='' OR cleanup_target_id=?)`,
		cleanupTaskID, targetID, PhaseCleaning, time.Now().UTC().UnixNano(), rebuildTaskID, cleanupTaskID, targetID)
	if err != nil {
		return Record{}, fmt.Errorf("bind cleanup task to rebuild record: %w", err)
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return Record{}, ErrRecordConflict
	}
	return s.Get(ctx, rebuildTaskID)
}

func (s *Store) Save(ctx context.Context, record Record) error {
	if !validRecord(record) {
		return ErrRecordConflict
	}
	encoded, err := json.Marshal(record.Spec)
	if err != nil {
		return err
	}
	networks, err := json.Marshal(record.Networks)
	if err != nil {
		return err
	}
	result, err := s.db.ExecContext(ctx, `UPDATE container_rebuilds SET new_id=?,cleanup_task_id=?,cleanup_target_id=?,cleanup_phase=?,snapshot_id=?,phase=?,outcome=?,error_code=?,updated_at_ns=?,spec_json=?,networks_json=? WHERE task_id=? AND node_id=? AND original_id=?`,
		record.NewID, record.CleanupTaskID, record.CleanupTargetID, record.CleanupPhase, record.SnapshotID, record.Phase, record.Outcome, record.ErrorCode, time.Now().UTC().UnixNano(), string(encoded), string(networks), record.TaskID, record.NodeID, record.OriginalID)
	if err != nil {
		return fmt.Errorf("persist rebuild state transition: %w", err)
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return ErrRecordConflict
	}
	return nil
}

func validRecord(record Record) bool {
	return record.TaskID != "" && len(record.TaskID) <= 128 && record.NodeID != "" && len(record.NodeID) <= 128 &&
		record.OriginalID != "" && len(record.OriginalID) == 64 && record.OriginalName != "" && record.BackupName != "" &&
		record.SnapshotRef != "" && record.Phase != "" && protocol.ValidateTaskIntent(protocol.TaskIntent{
		Action: protocol.TaskRebuild, ContainerID: record.OriginalID, Rebuild: &record.Spec,
	}) == nil
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func secureSidecar(path string) error {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return fmt.Errorf("secure rebuild database sidecar: %w", err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return errors.New("rebuild database sidecar must be a private regular file")
	}
	return nil
}
