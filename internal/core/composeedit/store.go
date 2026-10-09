// Package composeedit provides a Core-owned, content-free ledger for S11
// source transactions. Migration registration is kept in the storage
// integration layer; SchemaStatements is the migration fragment.
package composeedit

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"regexp"
	"strings"
	"time"

	"github.com/CST-Cat/NodeDance/internal/core/audit"
	"github.com/CST-Cat/NodeDance/internal/protocol"
	"github.com/CST-Cat/NodeDance/internal/taskstate"
)

//go:embed schema.sql
var schemaSQL string

func SchemaStatements() []string {
	parts := strings.Split(schemaSQL, ";\n\n")
	statements := make([]string, 0, len(parts))
	for _, part := range parts {
		statement := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(part), ";"))
		if statement != "" {
			statements = append(statements, statement)
		}
	}
	return statements
}

var (
	ErrNotFound          = errors.New("Compose editor operation not found")
	ErrConflict          = errors.New("Compose editor idempotency key conflicts with another request")
	ErrSchemaUnavailable = errors.New("Compose editor migration is not installed")
	validNodeID          = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	validOperationID     = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]{0,127}$`)
	validIdempotency     = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]{0,127}$`)
	validErrorCode       = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)
)

type Status string

const (
	StatusQueued    Status = "queued"
	StatusRunning   Status = "running"
	StatusSucceeded Status = "succeeded"
	StatusFailed    Status = "failed"
	StatusUnknown   Status = "unknown"
)

type Summary struct {
	AffectedServices  []string `json:"affectedServices,omitempty"`
	Impact            []string `json:"impact,omitempty"`
	RollbackConfirmed bool     `json:"rollbackConfirmed,omitempty"`
	DataBackup        bool     `json:"dataBackup"`
}

type Operation struct {
	ID             string            `json:"operationId"`
	NodeID         string            `json:"nodeId"`
	ProjectKey     string            `json:"projectKey"`
	IdempotencyKey string            `json:"-"`
	Digest         [sha256.Size]byte `json:"-"`
	Status         Status            `json:"status"`
	ErrorCode      string            `json:"errorCode,omitempty"`
	Verified       bool              `json:"verified"`
	Summary        Summary           `json:"summary"`
	ActorID        sql.NullInt64     `json:"-"`
	RemoteAddr     string            `json:"-"`
	CreatedAt      time.Time         `json:"createdAt"`
	UpdatedAt      time.Time         `json:"updatedAt"`
}

type Event struct {
	ID         int64     `json:"id"`
	Event      string    `json:"event"`
	FromStatus string    `json:"fromStatus,omitempty"`
	ToStatus   string    `json:"toStatus"`
	OccurredAt time.Time `json:"occurredAt"`
}

type Options struct{ Now func() time.Time }

type Store struct {
	db  *sql.DB
	now func() time.Time
}

func NewStore(db *sql.DB, options Options) (*Store, error) {
	if db == nil {
		return nil, errors.New("Compose editor database is required")
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	return &Store{db: db, now: options.Now}, nil
}

func (s *Store) SchemaAvailable(ctx context.Context) (bool, error) {
	var exists int
	err := s.db.QueryRowContext(ctx, `SELECT 1 FROM sqlite_master WHERE type='table' AND name='compose_editor_operations'`).Scan(&exists)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

func (s *Store) RecoverUnfinished(ctx context.Context) (int64, error) {
	available, err := s.SchemaAvailable(ctx)
	if err != nil || !available {
		return 0, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT operation_id,node_id,status FROM compose_editor_operations WHERE status IN (?,?) ORDER BY created_at,operation_id`, StatusQueued, StatusRunning)
	if err != nil {
		return 0, err
	}
	type unfinished struct{ id, node, status string }
	var pending []unfinished
	for rows.Next() {
		var item unfinished
		if err := rows.Scan(&item.id, &item.node, &item.status); err != nil {
			_ = rows.Close()
			return 0, err
		}
		pending = append(pending, item)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return 0, err
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}
	now := s.now().UTC()
	for _, item := range pending {
		if _, err := tx.ExecContext(ctx, `UPDATE compose_editor_operations SET status=?,error_code='result_pending',verified=0,updated_at=? WHERE operation_id=? AND status IN (?,?)`, StatusUnknown, now.UnixNano(), item.id, StatusQueued, StatusRunning); err != nil {
			return 0, err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO compose_editor_events(operation_id,event,from_status,to_status,occurred_at) VALUES(?,?,?,?,?)`, item.id, "result_pending", item.status, StatusUnknown, now.UnixNano()); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return int64(len(pending)), nil
}

func (s *Store) Enqueue(ctx context.Context, nodeID, projectKey, idempotencyKey string, request protocol.ComposeRequest, actorID sql.NullInt64, remoteAddr string) (Operation, bool, error) {
	if !validNodeID.MatchString(nodeID) || !validIdempotency.MatchString(idempotencyKey) || !validOperationID.MatchString(request.OperationID) || request.Action != protocol.ComposeEditApply || protocol.ValidateComposeRequest(request) != nil || request.Project.Key != projectKey {
		return Operation{}, false, errors.New("invalid Compose editor operation")
	}
	available, err := s.SchemaAvailable(ctx)
	if err != nil {
		return Operation{}, false, err
	}
	if !available {
		return Operation{}, false, ErrSchemaUnavailable
	}
	identityRequest := request
	identityRequest.OperationID = ""
	encoded, err := json.Marshal(identityRequest)
	if err != nil {
		return Operation{}, false, err
	}
	canonical, err := taskstate.CanonicalJSON(encoded)
	if err != nil {
		return Operation{}, false, err
	}
	digest := sha256.Sum256(canonical)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Operation{}, false, err
	}
	defer tx.Rollback()
	existing, err := scanOperation(tx.QueryRowContext(ctx, `SELECT operation_id,node_id,project_key,idempotency_key,request_digest,status,error_code,verified,summary_json,actor_id,remote_addr,created_at,updated_at FROM compose_editor_operations WHERE node_id=? AND idempotency_key=?`, nodeID, idempotencyKey))
	if err == nil {
		if subtle.ConstantTimeCompare(existing.Digest[:], digest[:]) != 1 {
			return Operation{}, false, ErrConflict
		}
		return existing, false, tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Operation{}, false, err
	}
	now := s.now().UTC()
	cleanRemote := remoteAddr
	if net.ParseIP(cleanRemote) == nil {
		cleanRemote = "unknown"
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO compose_editor_operations(operation_id,node_id,project_key,idempotency_key,request_digest,action,status,error_code,verified,summary_json,actor_id,remote_addr,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		request.OperationID, nodeID, projectKey, idempotencyKey, digest[:], string(request.Action), StatusQueued, "", false, `{}`, nullableActor(actorID), cleanRemote, now.UnixNano(), now.UnixNano())
	if err != nil {
		return Operation{}, false, fmt.Errorf("persist Compose editor metadata: %w", err)
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO compose_editor_events(operation_id,event,from_status,to_status,occurred_at) VALUES(?,?,?,?,?)`, request.OperationID, "accepted", "", StatusQueued, now.UnixNano()); err != nil {
		return Operation{}, false, err
	}
	if err = audit.Record(ctx, tx, audit.Event{OccurredAt: now, Action: "compose_operation", Outcome: "accepted", ActorID: actorID, RemoteAddr: cleanRemote, Target: audit.Target{Kind: audit.TargetNode, ID: nodeID}}); err != nil {
		return Operation{}, false, err
	}
	if err = tx.Commit(); err != nil {
		return Operation{}, false, err
	}
	return Operation{ID: request.OperationID, NodeID: nodeID, ProjectKey: projectKey, IdempotencyKey: idempotencyKey, Digest: digest, Status: StatusQueued, ActorID: actorID, RemoteAddr: cleanRemote, Summary: Summary{DataBackup: false}, CreatedAt: now, UpdatedAt: now}, true, nil
}

func (s *Store) SetStatus(ctx context.Context, nodeID, operationID string, status Status, errorCode string, verified bool, result *protocol.ComposeEditorResult) (Operation, error) {
	if !validStatus(status) || errorCode != "" && !validErrorCode.MatchString(errorCode) || status == StatusSucceeded && (!verified || errorCode != "") || status != StatusSucceeded && verified {
		return Operation{}, errors.New("invalid Compose editor result")
	}
	available, err := s.SchemaAvailable(ctx)
	if err != nil {
		return Operation{}, err
	}
	if !available {
		return Operation{}, ErrSchemaUnavailable
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Operation{}, err
	}
	defer tx.Rollback()
	operation, err := scanOperation(tx.QueryRowContext(ctx, `SELECT operation_id,node_id,project_key,idempotency_key,request_digest,status,error_code,verified,summary_json,actor_id,remote_addr,created_at,updated_at FROM compose_editor_operations WHERE node_id=? AND operation_id=?`, nodeID, operationID))
	if errors.Is(err, sql.ErrNoRows) {
		return Operation{}, ErrNotFound
	}
	if err != nil {
		return Operation{}, err
	}
	if terminal(operation.Status) {
		if operation.Status == status && operation.ErrorCode == errorCode && operation.Verified == verified {
			return operation, tx.Commit()
		}
		if operation.Status != StatusUnknown || status != StatusSucceeded && status != StatusFailed {
			return Operation{}, errors.New("Compose editor operation result conflicts with durable state")
		}
	}
	if operation.Status == StatusQueued && status != StatusRunning && status != StatusFailed && status != StatusUnknown || (operation.Status == StatusRunning || operation.Status == StatusUnknown) && status == StatusQueued {
		return Operation{}, errors.New("invalid Compose editor status transition")
	}
	if result != nil {
		operation.Summary = Summary{AffectedServices: append([]string(nil), result.AffectedServices...), Impact: append([]string(nil), result.Impact...), RollbackConfirmed: result.RollbackConfirmed, DataBackup: result.DataBackup}
	}
	summary, _ := json.Marshal(operation.Summary)
	now := s.now().UTC()
	if _, err = tx.ExecContext(ctx, `UPDATE compose_editor_operations SET status=?,error_code=?,verified=?,summary_json=?,updated_at=? WHERE operation_id=? AND node_id=?`, status, errorCode, verified, string(summary), now.UnixNano(), operationID, nodeID); err != nil {
		return Operation{}, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO compose_editor_events(operation_id,event,from_status,to_status,occurred_at) VALUES(?,?,?,?,?)`, operationID, string(status), string(operation.Status), string(status), now.UnixNano()); err != nil {
		return Operation{}, err
	}
	if terminal(status) {
		outcome := string(status)
		if status == StatusUnknown {
			outcome = "unknown"
		}
		if err = audit.Record(ctx, tx, audit.Event{OccurredAt: now, Action: "compose_operation", Outcome: outcome, ActorID: operation.ActorID, RemoteAddr: operation.RemoteAddr, Target: audit.Target{Kind: audit.TargetNode, ID: nodeID}}); err != nil {
			return Operation{}, err
		}
	}
	if err = tx.Commit(); err != nil {
		return Operation{}, err
	}
	operation.Status = status
	operation.ErrorCode = errorCode
	operation.Verified = verified
	operation.UpdatedAt = now
	return operation, nil
}

func (s *Store) Get(ctx context.Context, nodeID, operationID string) (Operation, error) {
	available, err := s.SchemaAvailable(ctx)
	if err != nil {
		return Operation{}, err
	}
	if !available {
		return Operation{}, ErrSchemaUnavailable
	}
	operation, err := scanOperation(s.db.QueryRowContext(ctx, `SELECT operation_id,node_id,project_key,idempotency_key,request_digest,status,error_code,verified,summary_json,actor_id,remote_addr,created_at,updated_at FROM compose_editor_operations WHERE node_id=? AND operation_id=?`, nodeID, operationID))
	if errors.Is(err, sql.ErrNoRows) {
		return Operation{}, ErrNotFound
	}
	return operation, err
}

// PendingResultChecks returns the oldest ambiguous apply results for a node.
// Core and Agent reconnects may call it repeatedly; queries are read-only and
// never cause the original Compose write to be replayed.
func (s *Store) PendingResultChecks(ctx context.Context, nodeID string) ([]Operation, error) {
	if !validNodeID.MatchString(nodeID) {
		return nil, errors.New("invalid node ID")
	}
	available, err := s.SchemaAvailable(ctx)
	if err != nil {
		return nil, err
	}
	if !available {
		return nil, ErrSchemaUnavailable
	}
	rows, err := s.db.QueryContext(ctx, `SELECT operation_id,node_id,project_key,idempotency_key,request_digest,status,error_code,verified,summary_json,actor_id,remote_addr,created_at,updated_at FROM compose_editor_operations WHERE node_id=? AND status=? ORDER BY created_at,operation_id LIMIT 64`, nodeID, StatusUnknown)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var operations []Operation
	for rows.Next() {
		operation, err := scanOperation(rows)
		if err != nil {
			return nil, err
		}
		operations = append(operations, operation)
	}
	return operations, rows.Err()
}

func (s *Store) Events(ctx context.Context, nodeID, operationID string) ([]Event, error) {
	if _, err := s.Get(ctx, nodeID, operationID); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT e.id,e.event,e.from_status,e.to_status,e.occurred_at FROM compose_editor_events e JOIN compose_editor_operations o ON o.operation_id=e.operation_id WHERE o.node_id=? AND e.operation_id=? ORDER BY e.id`, nodeID, operationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var events []Event
	for rows.Next() {
		var item Event
		var at int64
		if err := rows.Scan(&item.ID, &item.Event, &item.FromStatus, &item.ToStatus, &at); err != nil {
			return nil, err
		}
		item.OccurredAt = time.Unix(0, at).UTC()
		events = append(events, item)
	}
	return events, rows.Err()
}

func scanOperation(row interface{ Scan(...any) error }) (Operation, error) {
	var item Operation
	var digest []byte
	var summary string
	var created, updated int64
	err := row.Scan(&item.ID, &item.NodeID, &item.ProjectKey, &item.IdempotencyKey, &digest, &item.Status, &item.ErrorCode, &item.Verified, &summary, &item.ActorID, &item.RemoteAddr, &created, &updated)
	if err != nil {
		return item, err
	}
	if len(digest) != sha256.Size {
		return item, errors.New("invalid Compose editor digest")
	}
	copy(item.Digest[:], digest)
	if err := json.Unmarshal([]byte(summary), &item.Summary); err != nil {
		return item, err
	}
	item.CreatedAt = time.Unix(0, created).UTC()
	item.UpdatedAt = time.Unix(0, updated).UTC()
	return item, nil
}

func validStatus(status Status) bool {
	switch status {
	case StatusQueued, StatusRunning, StatusSucceeded, StatusFailed, StatusUnknown:
		return true
	}
	return false
}
func terminal(status Status) bool {
	return status == StatusSucceeded || status == StatusFailed || status == StatusUnknown
}
func nullableActor(value sql.NullInt64) any {
	if !value.Valid {
		return nil
	}
	return value.Int64
}
