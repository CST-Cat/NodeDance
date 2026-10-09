// Package compose stores the Core-owned Compose project registry and its
// durable operation ledger. Production migration registration is intentionally
// owned by the storage integration layer.
package compose

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
	ErrIdempotencyConflict = errors.New("Compose idempotency key conflicts with another request")
	ErrOperationConflict   = errors.New("Compose operation state conflicts with the durable record")
	ErrOperationNotFound   = errors.New("Compose operation not found")
	validErrorCode         = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)
)

type Status string

const (
	StatusQueued    Status = "queued"
	StatusRunning   Status = "running"
	StatusSucceeded Status = "succeeded"
	StatusFailed    Status = "failed"
	StatusTimedOut  Status = "timed_out"
	StatusUnknown   Status = "unknown"
)

type Options struct{ Now func() time.Time }

type Store struct {
	db  *sql.DB
	now func() time.Time
}

func NewStore(db *sql.DB, options Options) (*Store, error) {
	if db == nil {
		return nil, errors.New("Compose database is required")
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	return &Store{db: db, now: options.Now}, nil
}

// RecoverUnfinished marks operations that were accepted before a Core restart
// but did not reach a durable result as unknown. It never resubmits a write.
// If the later-stage Compose migration is not installed yet, it is a no-op so
// the rest of the Core remains usable while Compose routes report unavailable.
func (s *Store) RecoverUnfinished(ctx context.Context) (int64, error) {
	var exists int
	if err := s.db.QueryRowContext(ctx, `SELECT 1 FROM sqlite_master WHERE type='table' AND name='compose_operations'`).Scan(&exists); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, nil
		}
		return 0, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT operation_id, node_id, status, actor_id, remote_addr FROM compose_operations WHERE status IN (?,?) ORDER BY created_at, operation_id`, StatusQueued, StatusRunning)
	if err != nil {
		return 0, err
	}
	type unfinished struct {
		id     string
		nodeID string
		status string
		actor  sql.NullInt64
		remote string
	}
	var pending []unfinished
	for rows.Next() {
		var item unfinished
		if err := rows.Scan(&item.id, &item.nodeID, &item.status, &item.actor, &item.remote); err != nil {
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
		if _, err := tx.ExecContext(ctx, `UPDATE compose_operations SET status=?, error_code='result_pending', verified=0, updated_at=? WHERE operation_id=? AND status IN (?,?)`,
			StatusUnknown, now.UnixNano(), item.id, StatusQueued, StatusRunning); err != nil {
			return 0, err
		}
		if err := addEvent(ctx, tx, item.id, "result_pending", item.status, string(StatusUnknown), item.actor, item.remote, now); err != nil {
			return 0, err
		}
		if err := audit.Record(ctx, tx, audit.Event{OccurredAt: now, Action: "compose_operation", Outcome: string(StatusUnknown), ActorID: item.actor,
			RemoteAddr: item.remote, Target: audit.Target{Kind: audit.TargetNode, ID: item.nodeID}}); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return int64(len(pending)), nil
}

type Project struct {
	NodeID       string
	Value        protocol.ComposeProject
	DiscoveredAt time.Time
	LastSeenAt   time.Time
}

type Operation struct {
	ID             string
	NodeID         string
	ProjectKey     string
	IdempotencyKey string
	Digest         [sha256.Size]byte
	Request        protocol.ComposeRequest
	Status         Status
	ErrorCode      string
	Verified       bool
	ActorID        sql.NullInt64
	RemoteAddr     string
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

type Event struct {
	ID         int64
	Event      string
	FromStatus sql.NullString
	ToStatus   string
	ActorID    sql.NullInt64
	RemoteAddr string
	OccurredAt time.Time
}

func (s *Store) RegisterProjects(ctx context.Context, nodeID string, projects []protocol.ComposeProject) error {
	if !validUUID(nodeID) {
		return errors.New("invalid Compose node ID")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := s.now().UnixNano()
	for _, project := range projects {
		if err := protocol.ValidateComposeProjectRef(project.Ref); err != nil {
			return err
		}
		files, err := json.Marshal(project.Ref.ConfigFiles)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO compose_projects(node_id, project_key, project_name, working_directory,
			config_files_json, config_available, discovered_at, last_seen_at) VALUES(?,?,?,?,?,?,?,?)
			ON CONFLICT(node_id, project_key) DO UPDATE SET project_name=excluded.project_name,
			working_directory=excluded.working_directory, config_files_json=excluded.config_files_json,
			config_available=excluded.config_available, last_seen_at=excluded.last_seen_at`,
			nodeID, project.Ref.Key, project.Ref.Name, project.Ref.WorkingDirectory, string(files), project.ConfigAvailable, now, now)
		if err != nil {
			return fmt.Errorf("save Compose project: %w", err)
		}
	}
	return tx.Commit()
}

func (s *Store) Projects(ctx context.Context, nodeID string) ([]Project, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT project_key, project_name, working_directory, config_files_json,
		config_available, discovered_at, last_seen_at FROM compose_projects WHERE node_id=? ORDER BY project_name, working_directory`, nodeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	projects := make([]Project, 0)
	for rows.Next() {
		var value protocol.ComposeProject
		var filesJSON string
		var available bool
		var discovered, seen int64
		if err := rows.Scan(&value.Ref.Key, &value.Ref.Name, &value.Ref.WorkingDirectory, &filesJSON, &available, &discovered, &seen); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(filesJSON), &value.Ref.ConfigFiles); err != nil {
			return nil, errors.New("saved Compose project context is invalid")
		}
		if protocol.ValidateComposeProjectRef(value.Ref) != nil {
			return nil, errors.New("saved Compose project identity is invalid")
		}
		value.ConfigAvailable = available
		if !available {
			value.ConfigReason = "config_missing"
		}
		projects = append(projects, Project{NodeID: nodeID, Value: value, DiscoveredAt: time.Unix(0, discovered).UTC(), LastSeenAt: time.Unix(0, seen).UTC()})
	}
	return projects, rows.Err()
}

func (s *Store) Project(ctx context.Context, nodeID, projectKey string) (Project, error) {
	var value protocol.ComposeProject
	var filesJSON string
	var available bool
	var discovered, seen int64
	err := s.db.QueryRowContext(ctx, `SELECT project_key, project_name, working_directory, config_files_json,
		config_available, discovered_at, last_seen_at FROM compose_projects WHERE node_id=? AND project_key=?`, nodeID, projectKey).
		Scan(&value.Ref.Key, &value.Ref.Name, &value.Ref.WorkingDirectory, &filesJSON, &available, &discovered, &seen)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Project{}, ErrOperationNotFound
		}
		return Project{}, err
	}
	if err := json.Unmarshal([]byte(filesJSON), &value.Ref.ConfigFiles); err != nil {
		return Project{}, errors.New("saved Compose project context is invalid")
	}
	if err := protocol.ValidateComposeProjectRef(value.Ref); err != nil {
		return Project{}, errors.New("saved Compose project identity is invalid")
	}
	value.ConfigAvailable = available
	if !available {
		value.ConfigReason = "config_missing"
	}
	return Project{NodeID: nodeID, Value: value, DiscoveredAt: time.Unix(0, discovered).UTC(), LastSeenAt: time.Unix(0, seen).UTC()}, nil
}

func (s *Store) Enqueue(ctx context.Context, nodeID, idempotencyKey string, request protocol.ComposeRequest, actorID sql.NullInt64, remoteAddr string) (Operation, bool, error) {
	return s.EnqueueWithGate(ctx, nodeID, idempotencyKey, request, actorID, remoteAddr, nil)
}

// Gate is checked after the durable idempotency lookup but before a new row is
// committed. Callers may pin the authenticated Agent connection until the
// acceptance transaction commits; a retry never requires an online Agent.
type Gate func(context.Context) (release func(), err error)

func (s *Store) EnqueueWithGate(ctx context.Context, nodeID, idempotencyKey string, request protocol.ComposeRequest, actorID sql.NullInt64, remoteAddr string, gate Gate) (Operation, bool, error) {
	if !validUUID(nodeID) || !validIdempotencyKey(idempotencyKey) || protocol.ValidateComposeRequest(request) != nil || request.Action == protocol.ComposeList {
		return Operation{}, false, errors.New("invalid Compose operation request")
	}
	digest, err := requestDigest(request)
	if err != nil {
		return Operation{}, false, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Operation{}, false, err
	}
	defer tx.Rollback()
	var existing Operation
	var rawDigest []byte
	var requestJSON string
	var createdAt, updatedAt int64
	err = tx.QueryRowContext(ctx, `SELECT operation_id, node_id, project_key, idempotency_key, request_digest, request_json,
		status, error_code, verified, actor_id, remote_addr, created_at, updated_at
		FROM compose_operations WHERE node_id=? AND idempotency_key=?`, nodeID, idempotencyKey).Scan(
		&existing.ID, &existing.NodeID, &existing.ProjectKey, &existing.IdempotencyKey, &rawDigest, &requestJSON,
		&existing.Status, &existing.ErrorCode, &existing.Verified, &existing.ActorID, &existing.RemoteAddr, &createdAt, &updatedAt)
	if err == nil {
		if len(rawDigest) != len(digest) || subtle.ConstantTimeCompare(rawDigest, digest[:]) != 1 {
			return Operation{}, false, ErrIdempotencyConflict
		}
		if err := json.Unmarshal([]byte(requestJSON), &existing.Request); err != nil {
			return Operation{}, false, errors.New("saved Compose request is invalid")
		}
		if len(rawDigest) != len(existing.Digest) {
			return Operation{}, false, errors.New("saved Compose request digest is invalid")
		}
		copy(existing.Digest[:], rawDigest)
		existing.CreatedAt, existing.UpdatedAt = time.Unix(0, createdAt).UTC(), time.Unix(0, updatedAt).UTC()
		return existing, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Operation{}, false, err
	}
	var release func()
	if gate != nil {
		release, err = gate(ctx)
		if err != nil {
			return Operation{}, false, err
		}
		defer release()
	}
	var projectExists int
	if err := tx.QueryRowContext(ctx, `SELECT 1 FROM compose_projects WHERE node_id=? AND project_key=?`, nodeID, request.Project.Key).Scan(&projectExists); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Operation{}, false, ErrOperationNotFound
		}
		return Operation{}, false, err
	}
	requestJSONBytes, err := json.Marshal(request)
	if err != nil {
		return Operation{}, false, err
	}
	now := s.now().UTC()
	created, updated := now.UnixNano(), now.UnixNano()
	if _, err := tx.ExecContext(ctx, `INSERT INTO compose_operations(operation_id, node_id, project_key, idempotency_key,
		request_digest, request_json, action, status, error_code, verified, actor_id, remote_addr, created_at, updated_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, request.OperationID, nodeID, request.Project.Key, idempotencyKey, digest[:], string(requestJSONBytes), request.Action,
		StatusQueued, "", false, nullableActor(actorID), cleanRemote(remoteAddr), created, updated); err != nil {
		return Operation{}, false, fmt.Errorf("persist Compose operation: %w", err)
	}
	if err := addEvent(ctx, tx, request.OperationID, "accepted", "", string(StatusQueued), actorID, remoteAddr, now); err != nil {
		return Operation{}, false, err
	}
	if err := audit.Record(ctx, tx, audit.Event{OccurredAt: now, Action: "compose_operation", Outcome: "accepted", ActorID: actorID,
		RemoteAddr: remoteAddr, Target: audit.Target{Kind: audit.TargetNode, ID: nodeID}}); err != nil {
		return Operation{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return Operation{}, false, err
	}
	return Operation{ID: request.OperationID, NodeID: nodeID, ProjectKey: request.Project.Key, IdempotencyKey: idempotencyKey,
		Digest: digest, Request: request, Status: StatusQueued, ActorID: actorID, RemoteAddr: cleanRemote(remoteAddr), CreatedAt: now, UpdatedAt: now}, true, nil
}

func (s *Store) SetStatus(ctx context.Context, nodeID, operationID string, status Status, errorCode string, verified bool) (Operation, error) {
	if !validStatus(status) || errorCode != "" && !validErrorCode.MatchString(errorCode) || status == StatusSucceeded && (!verified || errorCode != "") || status != StatusSucceeded && verified {
		return Operation{}, errors.New("invalid Compose operation result")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Operation{}, err
	}
	defer tx.Rollback()
	operation, err := scanOperation(tx.QueryRowContext(ctx, `SELECT operation_id, node_id, project_key, idempotency_key,
		request_digest, request_json, status, error_code, verified, actor_id, remote_addr, created_at, updated_at
		FROM compose_operations WHERE node_id=? AND operation_id=?`, nodeID, operationID))
	if errors.Is(err, sql.ErrNoRows) {
		return Operation{}, ErrOperationNotFound
	}
	if err != nil {
		return Operation{}, err
	}
	if terminal(operation.Status) {
		if operation.Status == status && operation.ErrorCode == errorCode && operation.Verified == verified {
			return operation, tx.Commit()
		}
		if operation.Status != StatusUnknown || status != StatusSucceeded && status != StatusFailed && status != StatusTimedOut {
			return Operation{}, ErrOperationConflict
		}
	}
	if operation.Status == StatusQueued && status != StatusRunning && status != StatusFailed && status != StatusUnknown ||
		(operation.Status == StatusRunning || operation.Status == StatusUnknown) && status == StatusQueued {
		return Operation{}, ErrOperationConflict
	}
	now := s.now().UTC()
	if _, err := tx.ExecContext(ctx, `UPDATE compose_operations SET status=?, error_code=?, verified=?, updated_at=? WHERE operation_id=? AND node_id=?`,
		status, errorCode, verified, now.UnixNano(), operationID, nodeID); err != nil {
		return Operation{}, err
	}
	if err := addEvent(ctx, tx, operationID, statusEvent(status), string(operation.Status), string(status), operation.ActorID, operation.RemoteAddr, now); err != nil {
		return Operation{}, err
	}
	if terminal(status) {
		if err := audit.Record(ctx, tx, audit.Event{OccurredAt: now, Action: "compose_operation", Outcome: string(status), ActorID: operation.ActorID,
			RemoteAddr: operation.RemoteAddr, Target: audit.Target{Kind: audit.TargetNode, ID: nodeID}}); err != nil {
			return Operation{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return Operation{}, err
	}
	operation.Status, operation.ErrorCode, operation.Verified, operation.UpdatedAt = status, errorCode, verified, now
	return operation, nil
}

func (s *Store) Get(ctx context.Context, nodeID, operationID string) (Operation, error) {
	operation, err := scanOperation(s.db.QueryRowContext(ctx, `SELECT operation_id, node_id, project_key, idempotency_key,
		request_digest, request_json, status, error_code, verified, actor_id, remote_addr, created_at, updated_at
		FROM compose_operations WHERE node_id=? AND operation_id=?`, nodeID, operationID))
	if errors.Is(err, sql.ErrNoRows) {
		return Operation{}, ErrOperationNotFound
	}
	return operation, err
}

// PendingResultChecks returns ambiguous operations that may be resolved from a
// fresh, read-only Engine inventory. It deliberately excludes other unknown
// results whose action-specific outcome cannot be established from inventory.
func (s *Store) PendingResultChecks(ctx context.Context, nodeID string) ([]Operation, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT operation_id, node_id, project_key, idempotency_key, request_digest, request_json,
		status, error_code, verified, actor_id, remote_addr, created_at, updated_at
		FROM compose_operations WHERE node_id=? AND status=? AND error_code='result_pending' ORDER BY created_at, operation_id LIMIT 256`, nodeID, StatusUnknown)
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
	rows, err := s.db.QueryContext(ctx, `SELECT e.id, e.event, e.from_status, e.to_status, e.actor_id, e.remote_addr, e.occurred_at
		FROM compose_operation_events e JOIN compose_operations o ON o.operation_id=e.operation_id
		WHERE o.node_id=? AND e.operation_id=? ORDER BY e.id`, nodeID, operationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var events []Event
	for rows.Next() {
		var value Event
		var at int64
		if err := rows.Scan(&value.ID, &value.Event, &value.FromStatus, &value.ToStatus, &value.ActorID, &value.RemoteAddr, &at); err != nil {
			return nil, err
		}
		value.OccurredAt = time.Unix(0, at).UTC()
		events = append(events, value)
	}
	return events, rows.Err()
}

type rowScanner interface{ Scan(...any) error }

func scanOperation(row rowScanner) (Operation, error) {
	var operation Operation
	var rawDigest []byte
	var requestJSON string
	var created, updated int64
	err := row.Scan(&operation.ID, &operation.NodeID, &operation.ProjectKey, &operation.IdempotencyKey, &rawDigest, &requestJSON,
		&operation.Status, &operation.ErrorCode, &operation.Verified, &operation.ActorID, &operation.RemoteAddr, &created, &updated)
	if err != nil {
		return Operation{}, err
	}
	if len(rawDigest) != len(operation.Digest) {
		return Operation{}, errors.New("saved Compose request digest is invalid")
	}
	copy(operation.Digest[:], rawDigest)
	if err := json.Unmarshal([]byte(requestJSON), &operation.Request); err != nil {
		return Operation{}, errors.New("saved Compose request is invalid")
	}
	operation.CreatedAt, operation.UpdatedAt = time.Unix(0, created).UTC(), time.Unix(0, updated).UTC()
	return operation, nil
}

func requestDigest(request protocol.ComposeRequest) ([sha256.Size]byte, error) {
	request.OperationID = ""
	encoded, err := json.Marshal(request)
	if err != nil {
		return [sha256.Size]byte{}, err
	}
	return sha256.Sum256(encoded), nil
}

func addEvent(ctx context.Context, tx *sql.Tx, operationID, event, from, to string, actor sql.NullInt64, remote string, at time.Time) error {
	var fromValue any
	if from != "" {
		fromValue = from
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO compose_operation_events(operation_id, event, from_status, to_status, actor_id, remote_addr, occurred_at)
		VALUES(?,?,?,?,?,?,?)`, operationID, event, fromValue, to, nullableActor(actor), cleanRemote(remote), at.UnixNano())
	return err
}

func validStatus(status Status) bool {
	switch status {
	case StatusQueued, StatusRunning, StatusSucceeded, StatusFailed, StatusTimedOut, StatusUnknown:
		return true
	}
	return false
}

func terminal(status Status) bool {
	return status == StatusSucceeded || status == StatusFailed || status == StatusTimedOut || status == StatusUnknown
}
func statusEvent(status Status) string {
	return map[Status]string{StatusRunning: "dispatched", StatusSucceeded: "verified", StatusFailed: "failed", StatusTimedOut: "timed_out", StatusUnknown: "result_pending"}[status]
}
func validIdempotencyKey(value string) bool {
	return len(value) > 0 && len(value) <= 128 && regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]{0,127}$`).MatchString(value)
}
func validUUID(value string) bool {
	return regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`).MatchString(value)
}
func nullableActor(value sql.NullInt64) any {
	if value.Valid {
		return value.Int64
	}
	return nil
}
func cleanRemote(value string) string {
	if net.ParseIP(value) == nil {
		return "unknown"
	}
	return value
}
