// Package probes owns Core configuration, scheduler state, and durable service
// probe history. Probe execution itself is performed by the enrolled Agent.
package probes

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/CST-Cat/NodeDance/internal/core/audit"
	"github.com/CST-Cat/NodeDance/internal/protocol"
)

var (
	ErrNotFound = errors.New("service probe not found")
	ErrConflict = errors.New("service probe configuration changed")
	ErrNotDue   = errors.New("service probe is not due")
	ErrRunState = errors.New("service probe run is not pending")
)

type Config struct {
	ID                   string `json:"id"`
	NodeID               string `json:"nodeId"`
	Name                 string `json:"name"`
	Kind                 string `json:"kind"`
	Target               string `json:"target"`
	ExpectedHTTPStatus   int    `json:"expectedHttpStatus,omitempty"`
	IntervalSeconds      int    `json:"intervalSeconds"`
	TimeoutSeconds       int    `json:"timeoutSeconds"`
	Enabled              bool   `json:"enabled"`
	Revision             int64  `json:"revision"`
	Status               string `json:"status"`
	LastErrorCode        string `json:"lastErrorCode,omitempty"`
	ConsecutiveFailures  int    `json:"consecutiveFailures"`
	ConsecutiveSuccesses int    `json:"consecutiveSuccesses"`
	LastCheckedAt        string `json:"lastCheckedAt,omitempty"`
	NextDueAt            string `json:"nextDueAt"`
	CreatedAt            string `json:"createdAt"`
	UpdatedAt            string `json:"updatedAt"`
	DeletedAt            string `json:"deletedAt,omitempty"`
}

type Run struct {
	RunID       string `json:"runId"`
	ProbeID     string `json:"probeId"`
	NodeID      string `json:"nodeId"`
	Status      string `json:"status"`
	CheckedAt   string `json:"checkedAt"`
	CompletedAt string `json:"completedAt,omitempty"`
	LatencyMS   *int64 `json:"latencyMs,omitempty"`
	HTTPStatus  *int   `json:"httpStatus,omitempty"`
	ErrorCode   string `json:"errorCode,omitempty"`
}

type Store struct {
	db  *sql.DB
	now func() time.Time
}

func New(db *sql.DB, now func() time.Time) *Store {
	if now == nil {
		now = time.Now
	}
	return &Store{db: db, now: now}
}

func NewID() (string, error) {
	var bytes [16]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return "", err
	}
	bytes[6] = (bytes[6] & 0x0f) | 0x40
	bytes[8] = (bytes[8] & 0x3f) | 0x80
	encoded := hex.EncodeToString(bytes[:])
	return encoded[:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:], nil
}

func ValidateConfig(config Config) error {
	if !validUUID(config.NodeID) || config.ID != "" && !validUUID(config.ID) {
		return errors.New("probe and node IDs must be canonical UUIDs")
	}
	name := strings.TrimSpace(config.Name)
	if name == "" || utf8.RuneCountInString(name) > 80 || strings.ContainsAny(name, "\r\n\x00") {
		return errors.New("probe name must contain 1 to 80 printable characters")
	}
	if config.IntervalSeconds < protocol.MinProbeIntervalSeconds || config.IntervalSeconds > protocol.MaxProbeIntervalSeconds ||
		config.TimeoutSeconds < protocol.MinProbeTimeoutSeconds || config.TimeoutSeconds > protocol.MaxProbeTimeoutSeconds || config.TimeoutSeconds >= config.IntervalSeconds {
		return errors.New("probe interval must be 10 to 86400 seconds and timeout 1 to 30 seconds below the interval")
	}
	if err := protocol.ValidateProbeTarget(config.Kind, config.Target, config.ExpectedHTTPStatus); err != nil {
		return err
	}
	return nil
}

func (s *Store) Create(ctx context.Context, config Config, event audit.Event) (Config, error) {
	if config.ID == "" {
		id, err := NewID()
		if err != nil {
			return Config{}, fmt.Errorf("generate probe ID: %w", err)
		}
		config.ID = id
	}
	if err := ValidateConfig(config); err != nil {
		return Config{}, err
	}
	now := s.now().UTC()
	config.Name = strings.TrimSpace(config.Name)
	config.Revision, config.Status = 1, protocol.ProbeResultUnknown
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Config{}, err
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, `INSERT INTO service_probes(
		id,node_id,name,kind,target,expected_http_status,interval_seconds,timeout_seconds,enabled,revision,status,next_due_at,created_at,updated_at)
		VALUES(?,?,?,?,?,?,?,?,?,1,'unknown',?,?,?)`, config.ID, config.NodeID, config.Name, config.Kind, config.Target,
		expectedStatus(config), config.IntervalSeconds, config.TimeoutSeconds, boolInt(config.Enabled), now.UnixNano(), now.UnixNano(), now.UnixNano())
	if err != nil {
		return Config{}, fmt.Errorf("insert service probe: %w", err)
	}
	if event.Target.ID == "" {
		event.Target = audit.Target{Kind: audit.TargetNode, ID: config.NodeID}
	}
	if err := audit.Record(ctx, tx, event); err != nil {
		return Config{}, fmt.Errorf("audit service probe creation: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Config{}, err
	}
	config.CreatedAt = now.Format(time.RFC3339Nano)
	config.UpdatedAt = config.CreatedAt
	config.NextDueAt = now.Format(time.RFC3339Nano)
	return config, nil
}

func (s *Store) Update(ctx context.Context, config Config, event audit.Event) (Config, error) {
	if err := ValidateConfig(config); err != nil {
		return Config{}, err
	}
	if config.Revision < 1 {
		return Config{}, errors.New("probe revision is required")
	}
	now := s.now().UTC()
	config.Name = strings.TrimSpace(config.Name)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Config{}, err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE service_probes SET name=?,kind=?,target=?,expected_http_status=?,interval_seconds=?,timeout_seconds=?,enabled=?,revision=revision+1,status='unknown',last_error_code=NULL,consecutive_failures=0,consecutive_successes=0,last_checked_at=NULL,next_due_at=?,updated_at=?
		WHERE id=? AND node_id=? AND revision=? AND deleted_at IS NULL`, config.Name, config.Kind, config.Target, expectedStatus(config), config.IntervalSeconds,
		config.TimeoutSeconds, boolInt(config.Enabled), now.UnixNano(), now.UnixNano(), config.ID, config.NodeID, config.Revision)
	if err != nil {
		return Config{}, fmt.Errorf("update service probe: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return Config{}, err
	}
	if rows == 0 {
		var exists int
		checkErr := tx.QueryRowContext(ctx, `SELECT 1 FROM service_probes WHERE id=? AND deleted_at IS NULL`, config.ID).Scan(&exists)
		if errors.Is(checkErr, sql.ErrNoRows) {
			return Config{}, ErrNotFound
		}
		return Config{}, ErrConflict
	}
	if event.Target.ID == "" {
		event.Target = audit.Target{Kind: audit.TargetNode, ID: config.NodeID}
	}
	if err := audit.Record(ctx, tx, event); err != nil {
		return Config{}, fmt.Errorf("audit service probe update: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Config{}, err
	}
	config.Revision++
	config.Status = protocol.ProbeResultUnknown
	config.ConsecutiveFailures, config.ConsecutiveSuccesses = 0, 0
	config.LastErrorCode, config.LastCheckedAt = "", ""
	config.NextDueAt, config.UpdatedAt = now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano)
	return config, nil
}

func (s *Store) Delete(ctx context.Context, id string, revision int64, event audit.Event) error {
	now := s.now().UTC()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE service_probes SET enabled=0,revision=revision+1,deleted_at=?,updated_at=? WHERE id=? AND revision=? AND deleted_at IS NULL`,
		now.UnixNano(), now.UnixNano(), id, revision)
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		var exists int
		checkErr := tx.QueryRowContext(ctx, `SELECT 1 FROM service_probes WHERE id=? AND deleted_at IS NULL`, id).Scan(&exists)
		if errors.Is(checkErr, sql.ErrNoRows) {
			return ErrNotFound
		}
		return ErrConflict
	}
	if event.Target.ID == "" {
		var nodeID string
		if err := tx.QueryRowContext(ctx, `SELECT node_id FROM service_probes WHERE id=?`, id).Scan(&nodeID); err != nil {
			return err
		}
		event.Target = audit.Target{Kind: audit.TargetNode, ID: nodeID}
	}
	if err := audit.Record(ctx, tx, event); err != nil {
		return fmt.Errorf("audit service probe deletion: %w", err)
	}
	return tx.Commit()
}

func (s *Store) Get(ctx context.Context, id string) (Config, error) {
	return scanConfig(s.db.QueryRowContext(ctx, probeSelect+` WHERE id=? AND deleted_at IS NULL`, id))
}

func (s *Store) List(ctx context.Context) ([]Config, error) {
	return s.list(ctx, ` WHERE deleted_at IS NULL`)
}

// ListIncludingDeleted returns retained configurations so callers can keep
// their history accessible after a soft delete. Deleted probes are never
// schedulable and their history is read-only.
func (s *Store) ListIncludingDeleted(ctx context.Context) ([]Config, error) {
	return s.list(ctx, ``)
}

func (s *Store) list(ctx context.Context, filter string) ([]Config, error) {
	rows, err := s.db.QueryContext(ctx, probeSelect+filter+` ORDER BY node_id,name,id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]Config, 0)
	for rows.Next() {
		item, err := scanConfig(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) History(ctx context.Context, id string, limit int) ([]Run, error) {
	var exists int
	if err := s.db.QueryRowContext(ctx, `SELECT 1 FROM service_probes WHERE id=?`, id).Scan(&exists); errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	} else if err != nil {
		return nil, err
	}
	if limit < 1 || limit > 200 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `SELECT run_id,probe_id,node_id,status,checked_at,completed_at,latency_ms,http_status,error_code FROM service_probe_runs WHERE probe_id=? ORDER BY checked_at DESC,run_id DESC LIMIT ?`, id, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]Run, 0)
	for rows.Next() {
		var item Run
		var checkedAt int64
		var completedAt, latency, status sql.NullInt64
		var errorCode sql.NullString
		if err := rows.Scan(&item.RunID, &item.ProbeID, &item.NodeID, &item.Status, &checkedAt, &completedAt, &latency, &status, &errorCode); err != nil {
			return nil, err
		}
		item.CheckedAt = time.Unix(0, checkedAt).UTC().Format(time.RFC3339Nano)
		if completedAt.Valid {
			item.CompletedAt = time.Unix(0, completedAt.Int64).UTC().Format(time.RFC3339Nano)
		}
		if latency.Valid {
			value := latency.Int64
			item.LatencyMS = &value
		}
		if status.Valid {
			value := int(status.Int64)
			item.HTTPStatus = &value
		}
		if errorCode.Valid {
			item.ErrorCode = errorCode.String
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) Due(ctx context.Context, at time.Time, limit int) ([]Config, error) {
	if limit < 1 || limit > 256 {
		limit = 64
	}
	rows, err := s.db.QueryContext(ctx, probeSelect+` WHERE enabled=1 AND deleted_at IS NULL AND next_due_at<=? AND NOT EXISTS (SELECT 1 FROM service_probe_runs r WHERE r.probe_id=service_probes.id AND r.status='pending') ORDER BY next_due_at,id LIMIT ?`, at.UTC().UnixNano(), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]Config, 0)
	for rows.Next() {
		item, err := scanConfig(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) Claim(ctx context.Context, probeID string, generation uint64, at time.Time) (Config, Run, bool, error) {
	now := at.UTC()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Config{}, Run{}, false, err
	}
	defer tx.Rollback()
	config, err := scanConfig(tx.QueryRowContext(ctx, probeSelect+` WHERE id=? AND enabled=1 AND deleted_at IS NULL AND next_due_at<=?`, probeID, now.UnixNano()))
	if errors.Is(err, sql.ErrNoRows) {
		return Config{}, Run{}, false, nil
	}
	if err != nil {
		return Config{}, Run{}, false, err
	}
	var pending int
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM service_probe_runs WHERE probe_id=? AND status='pending')`, probeID).Scan(&pending); err != nil {
		return Config{}, Run{}, false, err
	}
	if pending != 0 {
		return Config{}, Run{}, false, nil
	}
	runID, err := NewID()
	if err != nil {
		return Config{}, Run{}, false, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO service_probe_runs(run_id,probe_id,node_id,probe_revision,generation,kind,expected_http_status,status,checked_at,timeout_seconds) VALUES(?,?,?,?,?,?,?,'pending',?,?)`,
		runID, config.ID, config.NodeID, config.Revision, generation, config.Kind, expectedStatus(config), now.UnixNano(), config.TimeoutSeconds); err != nil {
		return Config{}, Run{}, false, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE service_probes SET next_due_at=? WHERE id=? AND revision=?`,
		now.Add(time.Duration(config.IntervalSeconds)*time.Second).UnixNano(), config.ID, config.Revision); err != nil {
		return Config{}, Run{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return Config{}, Run{}, false, err
	}
	return config, Run{RunID: runID, ProbeID: config.ID, NodeID: config.NodeID, Status: "pending", CheckedAt: now.Format(time.RFC3339Nano)}, true, nil
}

func (s *Store) Complete(ctx context.Context, report protocol.ProbeReport, generation uint64, at time.Time) error {
	now := at.UTC()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var probeID, nodeID, runStatus, kind string
	var revision, storedGeneration int64
	var expectedHTTPStatus sql.NullInt64
	if err := tx.QueryRowContext(ctx, `SELECT probe_id,node_id,probe_revision,generation,kind,expected_http_status,status FROM service_probe_runs WHERE run_id=?`, report.RunID).
		Scan(&probeID, &nodeID, &revision, &storedGeneration, &kind, &expectedHTTPStatus, &runStatus); errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	} else if err != nil {
		return err
	}
	if probeID != report.ProbeID || nodeID != report.NodeID || uint64(storedGeneration) != generation {
		return ErrConflict
	}
	if runStatus != "pending" {
		return ErrRunState
	}
	if kind == protocol.ProbeTCP {
		if report.HTTPStatus != 0 {
			return ErrConflict
		}
	} else {
		switch report.Status {
		case protocol.ProbeResultHealthy:
			if !expectedHTTPStatus.Valid || int64(report.HTTPStatus) != expectedHTTPStatus.Int64 {
				return ErrConflict
			}
		case protocol.ProbeResultUnhealthy:
			if report.HTTPStatus > 0 && report.ErrorCode != "http_status_mismatch" {
				return ErrConflict
			}
		case protocol.ProbeResultUnknown:
			if report.HTTPStatus != 0 {
				return ErrConflict
			}
		default:
			return ErrConflict
		}
	}
	var latency, status any
	if report.LatencyMS > 0 {
		latency = report.LatencyMS
	}
	if report.HTTPStatus > 0 {
		status = report.HTTPStatus
	}
	if _, err := tx.ExecContext(ctx, `UPDATE service_probe_runs SET status=?,completed_at=?,latency_ms=?,http_status=?,error_code=? WHERE run_id=? AND status='pending'`,
		report.Status, now.UnixNano(), latency, status, nullableString(report.ErrorCode), report.RunID); err != nil {
		return err
	}
	var currentRevision int64
	var enabled int
	var deleted sql.NullInt64
	if err := tx.QueryRowContext(ctx, `SELECT revision,enabled,deleted_at FROM service_probes WHERE id=?`, probeID).Scan(&currentRevision, &enabled, &deleted); err != nil {
		return err
	}
	if currentRevision == revision && enabled == 1 && !deleted.Valid {
		if err := applyStateResult(ctx, tx, probeID, report, now); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) ExpirePending(ctx context.Context, at time.Time) error {
	rows, err := s.db.QueryContext(ctx, `SELECT run_id,probe_id,node_id,generation,timeout_seconds FROM service_probe_runs WHERE status='pending'`)
	if err != nil {
		return err
	}
	type pendingRun struct {
		runID, probeID, nodeID string
		generation             uint64
		timeout                int
	}
	var pending []pendingRun
	for rows.Next() {
		var item pendingRun
		var generation int64
		if err := rows.Scan(&item.runID, &item.probeID, &item.nodeID, &generation, &item.timeout); err != nil {
			rows.Close()
			return err
		}
		item.generation = uint64(generation)
		pending = append(pending, item)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	now := at.UTC()
	for _, item := range pending {
		var checkedAt int64
		if err := s.db.QueryRowContext(ctx, `SELECT checked_at FROM service_probe_runs WHERE run_id=? AND status='pending'`, item.runID).Scan(&checkedAt); errors.Is(err, sql.ErrNoRows) {
			continue
		} else if err != nil {
			return err
		}
		if now.Sub(time.Unix(0, checkedAt)) < time.Duration(item.timeout+5)*time.Second {
			continue
		}
		report := protocol.ProbeReport{ProbeID: item.probeID, RunID: item.runID, NodeID: item.nodeID, Status: protocol.ProbeResultUnknown, ErrorCode: "dispatch_timeout"}
		if err := s.Complete(ctx, report, item.generation, now); !errors.Is(err, ErrRunState) && err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) RecoverPending(ctx context.Context, at time.Time) error {
	rows, err := s.db.QueryContext(ctx, `SELECT run_id,probe_id,node_id,generation FROM service_probe_runs WHERE status='pending'`)
	if err != nil {
		return err
	}
	type pending struct {
		runID, probeID, nodeID string
		generation             uint64
	}
	var items []pending
	for rows.Next() {
		var item pending
		var generation int64
		if err := rows.Scan(&item.runID, &item.probeID, &item.nodeID, &generation); err != nil {
			rows.Close()
			return err
		}
		item.generation = uint64(generation)
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for _, item := range items {
		report := protocol.ProbeReport{ProbeID: item.probeID, RunID: item.runID, NodeID: item.nodeID, Status: protocol.ProbeResultUnknown, ErrorCode: "core_restarted"}
		if err := s.Complete(ctx, report, item.generation, at); err != nil && !errors.Is(err, ErrRunState) {
			return err
		}
	}
	return nil
}

func (s *Store) MarkNodeUnknown(ctx context.Context, nodeID string, at time.Time) error {
	rows, err := s.db.QueryContext(ctx, `SELECT id,revision,interval_seconds FROM service_probes WHERE node_id=? AND enabled=1 AND deleted_at IS NULL`, nodeID)
	if err != nil {
		return err
	}
	type item struct {
		id       string
		revision int64
		interval int
	}
	var items []item
	for rows.Next() {
		var value item
		if err := rows.Scan(&value.id, &value.revision, &value.interval); err != nil {
			rows.Close()
			return err
		}
		items = append(items, value)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	now := at.UTC()
	for _, value := range items {
		runID, err := NewID()
		if err != nil {
			return err
		}
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		// Preserve one durable node_offline transition per probe instead of
		// appending a run on every scheduler tick while the Agent stays offline.
		// Unknown is still a meaningful transition when its previous cause was
		// a timeout, capability gap, or the initial unobserved state.
		if _, err := tx.ExecContext(ctx, `INSERT INTO service_probe_runs(run_id,probe_id,node_id,probe_revision,generation,kind,expected_http_status,status,checked_at,completed_at,error_code,timeout_seconds) SELECT ?,id,node_id,revision,0,kind,expected_http_status,'unknown',?,?, 'node_offline',timeout_seconds FROM service_probes WHERE id=? AND revision=? AND enabled=1 AND deleted_at IS NULL AND NOT (status='unknown' AND COALESCE(last_error_code,'')='node_offline')`, runID, now.UnixNano(), now.UnixNano(), value.id, value.revision); err != nil {
			_ = tx.Rollback()
			return err
		}
		// A due offline probe is deferred by its configured interval, not
		// repeatedly claimed as an executable run. This keeps the pending-run
		// table empty and avoids unbounded duplicate offline history.
		nextDue := now.Add(time.Duration(value.interval) * time.Second).UnixNano()
		if _, err := tx.ExecContext(ctx, `UPDATE service_probes SET status='unknown',last_error_code='node_offline',last_checked_at=?,next_due_at=?,consecutive_failures=0,consecutive_successes=0,updated_at=? WHERE id=? AND revision=? AND enabled=1 AND deleted_at IS NULL`, now.UnixNano(), nextDue, now.UnixNano(), value.id, value.revision); err != nil {
			_ = tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) MarkAllNodesUnknown(ctx context.Context, at time.Time) error {
	rows, err := s.db.QueryContext(ctx, `SELECT DISTINCT node_id FROM service_probes WHERE enabled=1 AND deleted_at IS NULL AND status!='unknown'`)
	if err != nil {
		return err
	}
	var nodeIDs []string
	for rows.Next() {
		var nodeID string
		if err := rows.Scan(&nodeID); err != nil {
			rows.Close()
			return err
		}
		nodeIDs = append(nodeIDs, nodeID)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for _, nodeID := range nodeIDs {
		if err := s.MarkNodeUnknown(ctx, nodeID, at); err != nil {
			return err
		}
	}
	return nil
}

func applyStateResult(ctx context.Context, tx *sql.Tx, probeID string, report protocol.ProbeReport, now time.Time) error {
	var status string
	var failures, successes int
	if err := tx.QueryRowContext(ctx, `SELECT status,consecutive_failures,consecutive_successes FROM service_probes WHERE id=?`, probeID).Scan(&status, &failures, &successes); err != nil {
		return err
	}
	nextStatus, errorCode := status, report.ErrorCode
	switch report.Status {
	case protocol.ProbeResultUnknown:
		nextStatus, failures, successes = protocol.ProbeResultUnknown, 0, 0
	case protocol.ProbeResultUnhealthy:
		failures++
		successes = 0
		if failures >= 3 {
			nextStatus = protocol.ProbeResultUnhealthy
		}
	case protocol.ProbeResultHealthy:
		failures = 0
		successes++
		if status == protocol.ProbeResultHealthy || successes >= 2 {
			nextStatus = protocol.ProbeResultHealthy
		}
		errorCode = ""
	}
	_, err := tx.ExecContext(ctx, `UPDATE service_probes SET status=?,last_error_code=?,consecutive_failures=?,consecutive_successes=?,last_checked_at=?,updated_at=? WHERE id=?`,
		nextStatus, nullableString(errorCode), failures, successes, now.UnixNano(), now.UnixNano(), probeID)
	return err
}

const probeSelect = `SELECT id,node_id,name,kind,target,expected_http_status,interval_seconds,timeout_seconds,enabled,revision,status,last_error_code,consecutive_failures,consecutive_successes,last_checked_at,next_due_at,created_at,updated_at,deleted_at FROM service_probes`

type scanner interface{ Scan(...any) error }

func scanConfig(row scanner) (Config, error) {
	var item Config
	var status, lastError sql.NullString
	var expectedStatusValue, lastChecked, deletedAt sql.NullInt64
	var enabled int
	var nextDue, created, updated int64
	err := row.Scan(&item.ID, &item.NodeID, &item.Name, &item.Kind, &item.Target, &expectedStatusValue,
		&item.IntervalSeconds, &item.TimeoutSeconds, &enabled, &item.Revision, &status, &lastError,
		&item.ConsecutiveFailures, &item.ConsecutiveSuccesses, &lastChecked, &nextDue, &created, &updated, &deletedAt)
	if err != nil {
		return Config{}, err
	}
	item.Enabled, item.Status = enabled == 1, status.String
	if expectedStatusValue.Valid {
		item.ExpectedHTTPStatus = int(expectedStatusValue.Int64)
	}
	if lastError.Valid {
		item.LastErrorCode = lastError.String
	}
	if lastChecked.Valid {
		item.LastCheckedAt = time.Unix(0, lastChecked.Int64).UTC().Format(time.RFC3339Nano)
	}
	item.NextDueAt = time.Unix(0, nextDue).UTC().Format(time.RFC3339Nano)
	item.CreatedAt = time.Unix(0, created).UTC().Format(time.RFC3339Nano)
	item.UpdatedAt = time.Unix(0, updated).UTC().Format(time.RFC3339Nano)
	if deletedAt.Valid {
		item.DeletedAt = time.Unix(0, deletedAt.Int64).UTC().Format(time.RFC3339Nano)
	}
	return item, nil
}

func expectedStatus(config Config) any {
	if config.Kind == protocol.ProbeTCP {
		return nil
	}
	return config.ExpectedHTTPStatus
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func nullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func validUUID(value string) bool {
	if len(value) != 36 || value[8] != '-' || value[13] != '-' || value[18] != '-' || value[23] != '-' || value[14] != '4' {
		return false
	}
	for i, char := range value {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			continue
		}
		if !(char >= '0' && char <= '9' || char >= 'a' && char <= 'f') {
			return false
		}
	}
	return value[19] == '8' || value[19] == '9' || value[19] == 'a' || value[19] == 'b'
}
