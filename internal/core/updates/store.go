// Package updates persists signed Agent releases and rollout state.
package updates

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	agentupdate "github.com/CST-Cat/NodeDance/internal/agent/update"
	"github.com/google/uuid"
)

var ErrConflict = errors.New("Agent update task conflicts with an active task")
var ErrNotFound = errors.New("Agent update object not found")

type Store struct {
	db  *sql.DB
	now func() time.Time
}
type Release struct {
	ID           string               `json:"id"`
	Version      string               `json:"version"`
	OS           string               `json:"os"`
	Architecture string               `json:"architecture"`
	Manifest     agentupdate.Manifest `json:"manifest"`
	CreatedAt    time.Time            `json:"createdAt"`
	artifactPath string
}
type Task struct {
	ID          string    `json:"id"`
	BatchID     string    `json:"batchId"`
	BatchNumber int       `json:"batchNumber"`
	NodeID      string    `json:"nodeId"`
	ReleaseID   string    `json:"releaseId"`
	Version     string    `json:"version,omitempty"`
	Mode        string    `json:"mode"`
	Status      string    `json:"status"`
	Reason      string    `json:"reason,omitempty"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
}
type Settings struct {
	AutoEnabled       bool   `json:"autoEnabled"`
	WindowStartMinute int    `json:"windowStartMinute"`
	WindowEndMinute   int    `json:"windowEndMinute"`
	BatchSize         int    `json:"batchSize"`
	ReleaseID         string `json:"releaseId,omitempty"`
	CampaignDate      string `json:"campaignDate,omitempty"`
	CampaignPaused    bool   `json:"campaignPaused"`
	LastError         string `json:"lastError,omitempty"`
}

func New(db *sql.DB, now func() time.Time) *Store {
	if now == nil {
		now = time.Now
	}
	return &Store{db: db, now: now}
}

func (s *Store) CreateRelease(ctx context.Context, id string, manifest agentupdate.Manifest, path string) (Release, error) {
	data, err := json.Marshal(manifest)
	if err != nil {
		return Release{}, err
	}
	at := s.now().UTC()
	_, err = s.db.ExecContext(ctx, `INSERT INTO agent_update_releases(id,version,os,architecture,manifest_json,artifact_path,created_at) VALUES(?,?,?,?,?,?,?)`, id, manifest.Version, manifest.OS, manifest.Architecture, string(data), path, at.Unix())
	if err != nil {
		return Release{}, err
	}
	return Release{ID: id, Version: manifest.Version, OS: manifest.OS, Architecture: manifest.Architecture, Manifest: manifest, CreatedAt: at, artifactPath: path}, nil
}
func (s *Store) ListReleases(ctx context.Context) ([]Release, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,manifest_json,artifact_path,created_at FROM agent_update_releases ORDER BY created_at DESC,id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []Release{}
	for rows.Next() {
		var item Release
		var raw string
		var created int64
		if err := rows.Scan(&item.ID, &raw, &item.artifactPath, &created); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(raw), &item.Manifest); err != nil {
			return nil, err
		}
		item.Version = item.Manifest.Version
		item.OS = item.Manifest.OS
		item.Architecture = item.Manifest.Architecture
		item.CreatedAt = time.Unix(created, 0).UTC()
		items = append(items, item)
	}
	return items, rows.Err()
}
func (s *Store) GetRelease(ctx context.Context, id string) (Release, error) {
	var item Release
	var raw string
	var created int64
	err := s.db.QueryRowContext(ctx, `SELECT id,manifest_json,artifact_path,created_at FROM agent_update_releases WHERE id=?`, id).Scan(&item.ID, &raw, &item.artifactPath, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return item, ErrNotFound
	}
	if err != nil {
		return item, err
	}
	if err = json.Unmarshal([]byte(raw), &item.Manifest); err != nil {
		return item, err
	}
	item.Version = item.Manifest.Version
	item.OS = item.Manifest.OS
	item.Architecture = item.Manifest.Architecture
	item.CreatedAt = time.Unix(created, 0).UTC()
	return item, nil
}

func (s *Store) CreateTask(ctx context.Context, nodeID, releaseID, mode string, batchID string, batchNumber int) (Task, error) {
	if mode != "manual" && mode != "automatic" {
		return Task{}, errors.New("invalid update mode")
	}
	if batchID == "" {
		batchID = uuid.NewString()
	}
	at := s.now().UTC()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Task{}, err
	}
	defer tx.Rollback()
	var existing Task
	var created, updated int64
	err = tx.QueryRowContext(ctx, `SELECT t.id,t.batch_id,t.batch_number,t.node_id,t.release_id,r.version,t.mode,t.status,t.reason,t.created_at,t.updated_at FROM agent_update_tasks t JOIN agent_update_releases r ON r.id=t.release_id WHERE t.node_id=? AND t.status IN ('queued','deferred','dispatched','prepared')`, nodeID).Scan(&existing.ID, &existing.BatchID, &existing.BatchNumber, &existing.NodeID, &existing.ReleaseID, &existing.Version, &existing.Mode, &existing.Status, &existing.Reason, &created, &updated)
	if err == nil {
		if existing.ReleaseID != releaseID {
			return Task{}, ErrConflict
		}
		existing.CreatedAt = time.Unix(created, 0).UTC()
		existing.UpdatedAt = time.Unix(updated, 0).UTC()
		_ = tx.Commit()
		return existing, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Task{}, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO agent_update_tasks(id,batch_id,batch_number,node_id,release_id,mode,status,created_at,updated_at) VALUES(?,?,?,?,?,?, 'queued',?,?)`, uuid.NewString(), batchID, batchNumber, nodeID, releaseID, mode, at.Unix(), at.Unix()); err != nil {
		return Task{}, err
	}
	var t Task
	var c, u int64
	if err = tx.QueryRowContext(ctx, `SELECT t.id,t.batch_id,t.batch_number,t.node_id,t.release_id,r.version,t.mode,t.status,t.reason,t.created_at,t.updated_at FROM agent_update_tasks t JOIN agent_update_releases r ON r.id=t.release_id WHERE t.node_id=? AND t.status='queued' ORDER BY t.created_at DESC LIMIT 1`, nodeID).Scan(&t.ID, &t.BatchID, &t.BatchNumber, &t.NodeID, &t.ReleaseID, &t.Version, &t.Mode, &t.Status, &t.Reason, &c, &u); err != nil {
		return Task{}, err
	}
	t.CreatedAt = time.Unix(c, 0).UTC()
	t.UpdatedAt = time.Unix(u, 0).UTC()
	if err = tx.Commit(); err != nil {
		return Task{}, err
	}
	return t, nil
}

func (s *Store) ListTasks(ctx context.Context, limit int) ([]Task, error) {
	if limit < 1 || limit > 500 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `SELECT t.id,t.batch_id,t.batch_number,t.node_id,t.release_id,r.version,t.mode,t.status,t.reason,t.created_at,t.updated_at FROM agent_update_tasks t JOIN agent_update_releases r ON r.id=t.release_id ORDER BY t.created_at DESC,t.id LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []Task{}
	for rows.Next() {
		var t Task
		var c, u int64
		if err := rows.Scan(&t.ID, &t.BatchID, &t.BatchNumber, &t.NodeID, &t.ReleaseID, &t.Version, &t.Mode, &t.Status, &t.Reason, &c, &u); err != nil {
			return nil, err
		}
		t.CreatedAt = time.Unix(c, 0).UTC()
		t.UpdatedAt = time.Unix(u, 0).UTC()
		items = append(items, t)
	}
	return items, rows.Err()
}
func (s *Store) Pending(ctx context.Context, limit int) ([]Task, error) {
	// A campaign is a canary rollout: expose only the earliest node whose
	// predecessors have been confirmed successful. The implicit rowid records
	// insertion order even when many task timestamps share the same second.
	rows, err := s.db.QueryContext(ctx, `SELECT t.id,t.batch_id,t.batch_number,t.node_id,t.release_id,r.version,t.mode,t.status,t.reason,t.created_at,t.updated_at
		FROM agent_update_tasks t JOIN agent_update_releases r ON r.id=t.release_id
		WHERE t.status IN ('queued','deferred')
		AND NOT EXISTS (
			SELECT 1 FROM agent_update_tasks prior
			WHERE prior.batch_id=t.batch_id AND prior.rowid<t.rowid AND prior.status<>'succeeded'
		)
		ORDER BY t.created_at,t.rowid LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []Task{}
	for rows.Next() {
		var t Task
		var c, u int64
		if err := rows.Scan(&t.ID, &t.BatchID, &t.BatchNumber, &t.NodeID, &t.ReleaseID, &t.Version, &t.Mode, &t.Status, &t.Reason, &c, &u); err != nil {
			return nil, err
		}
		t.CreatedAt = time.Unix(c, 0).UTC()
		t.UpdatedAt = time.Unix(u, 0).UTC()
		items = append(items, t)
	}
	return items, rows.Err()
}
func (s *Store) SetTaskStatus(ctx context.Context, id, status, reason string) error {
	switch status {
	case "queued", "deferred", "dispatched", "prepared", "succeeded", "failed", "paused":
	default:
		return errors.New("invalid Agent update status")
	}
	_, err := s.db.ExecContext(ctx, `UPDATE agent_update_tasks SET status=?,reason=?,updated_at=? WHERE id=?`, status, reason, s.now().Unix(), id)
	return err
}

// ClaimTaskForDispatch atomically gives one scheduler invocation ownership of
// a queued/deferred task before it puts the command on an Agent connection.
// A stale Pending result or concurrent dispatch cannot enqueue the same update
// twice after this transition.
func (s *Store) ClaimTaskForDispatch(ctx context.Context, id string) (bool, error) {
	result, err := s.db.ExecContext(ctx, `UPDATE agent_update_tasks SET status='dispatched',reason='',updated_at=? WHERE id=? AND status IN ('queued','deferred')`, s.now().Unix(), id)
	if err != nil {
		return false, err
	}
	changed, err := result.RowsAffected()
	return changed == 1, err
}

// DeferPendingTask records a prerequisite failure only while the task remains
// queued/deferred. This prevents a stale scheduler snapshot from overwriting a
// concurrent dispatch claim.
func (s *Store) DeferPendingTask(ctx context.Context, id, reason string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE agent_update_tasks SET status='deferred',reason=?,updated_at=? WHERE id=? AND status IN ('queued','deferred') AND (status<>'deferred' OR reason<>?)`, reason, s.now().Unix(), id, reason)
	return err
}

// DeferDispatchedTask rolls back a dispatch claim when the bounded connection
// queue rejects the command. It must never regress a task that has already
// advanced after the command was accepted.
func (s *Store) DeferDispatchedTask(ctx context.Context, id, reason string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE agent_update_tasks SET status='deferred',reason=?,updated_at=? WHERE id=? AND status='dispatched'`, reason, s.now().Unix(), id)
	return err
}

func (s *Store) Report(ctx context.Context, id, nodeID, status, reason string) error {
	if status != "prepared" && status != "failed" && status != "rejected" {
		return errors.New("invalid Agent update report")
	}
	next := status
	if next == "rejected" {
		next = "failed"
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var batch, mode string
	if err = tx.QueryRowContext(ctx, `SELECT batch_id,mode FROM agent_update_tasks WHERE id=? AND node_id=? AND status IN ('dispatched','prepared')`, id, nodeID).Scan(&batch, &mode); errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	} else if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE agent_update_tasks SET status=?,reason=?,updated_at=? WHERE id=?`, next, reason, s.now().Unix(), id); err != nil {
		return err
	}
	if next == "failed" {
		if _, err = tx.ExecContext(ctx, `UPDATE agent_update_tasks SET status='paused',reason='paused after earlier node failed',updated_at=? WHERE batch_id=? AND status IN ('queued','deferred')`, s.now().Unix(), batch); err != nil {
			return err
		}
		if mode == "automatic" {
			if _, err = tx.ExecContext(ctx, `UPDATE agent_update_settings SET campaign_paused=1,last_error=?,updated_at=? WHERE id=1`, reason, s.now().Unix()); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}
func (s *Store) CompleteVersion(ctx context.Context, nodeID, version string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE agent_update_tasks SET status='succeeded',reason='',updated_at=? WHERE node_id=? AND status='prepared' AND release_id IN (SELECT id FROM agent_update_releases WHERE version=?)`, s.now().Unix(), nodeID, version)
	return err
}

func (s *Store) ReconcileVersion(ctx context.Context, nodeID, version string, pendingTaskIDs ...string) error {
	pendingTaskID := ""
	if len(pendingTaskIDs) > 0 {
		pendingTaskID = pendingTaskIDs[0]
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if pendingTaskID != "" {
		// A staged Agent journal survives a Core restart. Recover() moves the
		// former dispatch to deferred; restore that exact task to dispatched
		// before the connection scheduler runs so the Agent's retried prepared
		// report can persist without sending the download command a second time.
		if _, err := tx.ExecContext(ctx, `UPDATE agent_update_tasks SET status='dispatched',reason='',updated_at=? WHERE id=? AND node_id=? AND status IN ('queued','deferred')`, s.now().Unix(), pendingTaskID, nodeID); err != nil {
			return err
		}
	}
	rows, err := tx.QueryContext(ctx, `SELECT t.id,t.batch_id,r.version FROM agent_update_tasks t JOIN agent_update_releases r ON r.id=t.release_id WHERE t.node_id=? AND t.status='prepared'`, nodeID)
	if err != nil {
		return err
	}
	type pending struct{ id, batch, want string }
	items := []pending{}
	for rows.Next() {
		var p pending
		if err := rows.Scan(&p.id, &p.batch, &p.want); err != nil {
			rows.Close()
			return err
		}
		items = append(items, p)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, p := range items {
		if p.want == version {
			_, err = tx.ExecContext(ctx, `UPDATE agent_update_tasks SET status='succeeded',reason='',updated_at=? WHERE id=? AND status='prepared'`, s.now().Unix(), p.id)
		} else if p.id == pendingTaskID {
			// The Agent reports a durable staged journal in Hello when Core's
			// prepared acknowledgement may have been lost. Preserve the task so
			// it can resend its report and receive the acknowledgement again.
			continue
		} else {
			_, err = tx.ExecContext(ctx, `UPDATE agent_update_tasks SET status='failed',reason='Agent returned on the previous version after update attempt',updated_at=? WHERE id=? AND status='prepared'`, s.now().Unix(), p.id)
			if err == nil {
				_, err = tx.ExecContext(ctx, `UPDATE agent_update_tasks SET status='paused',reason='paused after earlier node failed',updated_at=? WHERE batch_id=? AND status IN ('queued','deferred')`, s.now().Unix(), p.batch)
			}
			if err == nil {
				_, err = tx.ExecContext(ctx, `UPDATE agent_update_settings SET campaign_paused=1,last_error='Agent returned on the previous version after update attempt',updated_at=? WHERE id=1`, s.now().Unix())
			}
		}
		if err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) Settings(ctx context.Context) (Settings, error) {
	var v Settings
	var enabled, paused int
	var release sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT auto_enabled,window_start_minute,window_end_minute,batch_size,release_id,campaign_date,campaign_paused,last_error FROM agent_update_settings WHERE id=1`).Scan(&enabled, &v.WindowStartMinute, &v.WindowEndMinute, &v.BatchSize, &release, &v.CampaignDate, &paused, &v.LastError)
	if err != nil {
		return v, err
	}
	v.AutoEnabled = enabled == 1
	v.CampaignPaused = paused == 1
	if release.Valid {
		v.ReleaseID = release.String
	}
	return v, nil
}
func (s *Store) SaveSettings(ctx context.Context, v Settings) error {
	if v.WindowStartMinute < 0 || v.WindowStartMinute > 1439 || v.WindowEndMinute < 0 || v.WindowEndMinute > 1439 || v.BatchSize < 1 || v.BatchSize > 100 {
		return errors.New("invalid Agent update schedule")
	}
	if v.AutoEnabled && v.WindowStartMinute == v.WindowEndMinute {
		return errors.New("automatic update window must have a non-zero duration")
	}
	enabled := 0
	if v.AutoEnabled {
		enabled = 1
	}
	var release any
	if v.ReleaseID != "" {
		release = v.ReleaseID
	}
	_, err := s.db.ExecContext(ctx, `UPDATE agent_update_settings SET auto_enabled=?,window_start_minute=?,window_end_minute=?,batch_size=?,release_id=?,campaign_date='',campaign_paused=0,last_error='',updated_at=? WHERE id=1`, enabled, v.WindowStartMinute, v.WindowEndMinute, v.BatchSize, release, s.now().Unix())
	return err
}

func (s *Store) StartCampaign(ctx context.Context, nodes []string, releaseID, date string, batchSize int) error {
	if len(nodes) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var current string
	var enabled, paused int
	if err = tx.QueryRowContext(ctx, `SELECT campaign_date,auto_enabled,campaign_paused FROM agent_update_settings WHERE id=1`).Scan(&current, &enabled, &paused); err != nil {
		return err
	}
	if enabled != 1 || paused == 1 || current == date {
		return nil
	}
	batch := uuid.NewString()
	at := s.now().Unix()
	for i, node := range nodes {
		if _, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO agent_update_tasks(id,batch_id,batch_number,node_id,release_id,mode,status,created_at,updated_at) VALUES(?,?,?,?,?,'automatic','queued',?,?)`, uuid.NewString(), batch, i/batchSize, node, releaseID, at, at); err != nil {
			return err
		}
	}
	if _, err = tx.ExecContext(ctx, `UPDATE agent_update_settings SET campaign_date=?,campaign_paused=0,last_error='',updated_at=? WHERE id=1`, date, at); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) FailTask(ctx context.Context, id, reason string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var batch, mode string
	if err = tx.QueryRowContext(ctx, `SELECT batch_id,mode FROM agent_update_tasks WHERE id=?`, id).Scan(&batch, &mode); errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	} else if err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `UPDATE agent_update_tasks SET status='failed',reason=?,updated_at=? WHERE id=? AND status IN ('queued','deferred')`, reason, s.now().Unix(), id)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed == 0 {
		return tx.Commit()
	}
	if _, err = tx.ExecContext(ctx, `UPDATE agent_update_tasks SET status='paused',reason='paused after earlier node failed',updated_at=? WHERE batch_id=? AND status IN ('queued','deferred')`, s.now().Unix(), batch); err != nil {
		return err
	}
	if mode == "automatic" {
		if _, err = tx.ExecContext(ctx, `UPDATE agent_update_settings SET campaign_paused=1,last_error=?,updated_at=? WHERE id=1`, reason, s.now().Unix()); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) RecordCampaignFailure(ctx context.Context, reason string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE agent_update_settings SET campaign_paused=1,last_error=?,updated_at=? WHERE id=1`, reason, s.now().Unix())
	return err
}
func (s *Store) ArtifactsPath(ctx context.Context, id string) (string, error) {
	var path string
	err := s.db.QueryRowContext(ctx, `SELECT artifact_path FROM agent_update_releases WHERE id=?`, id).Scan(&path)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return path, err
}
func (s *Store) ReleaseVersion(ctx context.Context, id string) (string, error) {
	var value string
	err := s.db.QueryRowContext(ctx, `SELECT version FROM agent_update_releases WHERE id=?`, id).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return value, err
}
func (s *Store) UpdateCampaignDate(ctx context.Context, date string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE agent_update_settings SET campaign_date=?,updated_at=? WHERE id=1`, date, s.now().Unix())
	return err
}

func (s *Store) Recover(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `UPDATE agent_update_tasks SET status='deferred',reason='Core restarted; waiting for Agent state reconciliation',updated_at=? WHERE status='dispatched'`, s.now().Unix())
	return err
}

func (s *Store) ResumeCampaign(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `UPDATE agent_update_settings SET campaign_paused=0,last_error='',campaign_date='',updated_at=? WHERE id=1`, s.now().Unix())
	return err
}

func (s *Store) String() string { return fmt.Sprintf("Agent update store (%p)", s.db) }
