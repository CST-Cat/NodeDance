// Package filetasks persists the Core-side intent and confirmed outcome of
// file mutations. It deliberately stores paths and operation names only; file
// bytes and editor text never enter this package.
package filetasks

import (
	"context"
	"database/sql"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/CST-Cat/NodeDance/internal/core/audit"
	coretasks "github.com/CST-Cat/NodeDance/internal/core/tasks"
	"github.com/CST-Cat/NodeDance/internal/taskstate"
)

const (
	OperationMkdir    = "mkdir"
	OperationRename   = "rename"
	OperationDelete   = "delete"
	OperationSaveText = "save_text"
	OperationUpload   = "upload"
	MaxPageSize       = coretasks.MaxPageSize
)

var (
	ErrInvalidRequest = errors.New("invalid durable file task request")
	ErrNotFound       = errors.New("file task not found")
	ErrStateConflict  = errors.New("file task state conflict")
	canonicalUUID     = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
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

type Store struct {
	db  *sql.DB
	now func() time.Time
}

type Task struct {
	TaskID            string
	NodeID            string
	Operation         string
	TargetPath        string
	NewPath           string
	Status            taskstate.Status
	ResultCode        string
	CreatedAt         time.Time
	UpdatedAt         time.Time
	DispatchStartedAt *time.Time
	StartedAt         *time.Time
	FinishedAt        *time.Time
}

type CreateRequest struct {
	TaskID     string
	NodeID     string
	Operation  string
	TargetPath string
	NewPath    string
	ActorID    sql.NullInt64
	RemoteAddr string
}

type Event struct {
	ID         int64
	Event      string
	FromStatus sql.NullString
	ToStatus   sql.NullString
	ActorID    sql.NullInt64
	OccurredAt time.Time
	RemoteAddr string
}

type Page struct {
	Tasks      []Task
	NextCursor *coretasks.Cursor
}

func New(db *sql.DB, now func() time.Time) (*Store, error) {
	if db == nil {
		return nil, errors.New("file task database is nil")
	}
	if now == nil {
		now = time.Now
	}
	return &Store{db: db, now: now}, nil
}

func (s *Store) Create(ctx context.Context, request CreateRequest) (Task, error) {
	if ctx == nil || !validRequest(request) {
		return Task{}, ErrInvalidRequest
	}
	now := s.now().UTC()
	paths := []string{request.TargetPath}
	if request.NewPath != "" {
		paths = append(paths, request.NewPath)
	}
	encodedTarget, err := json.Marshal(paths)
	if err != nil {
		return Task{}, ErrInvalidRequest
	}
	target := audit.Target{Kind: audit.TargetFile, ID: audit.FileTarget(request.NodeID, request.TaskID, string(encodedTarget))}
	status := taskstate.Queued
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Task{}, fmt.Errorf("begin durable file task acceptance: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT INTO file_write_tasks(task_id,node_id,operation,target_path,new_path,status,result_code,created_at_ns,updated_at_ns)
		VALUES(?,?,?,?,?,?, '',?,?)`, request.TaskID, request.NodeID, request.Operation, request.TargetPath, request.NewPath, status, now.UnixNano(), now.UnixNano()); err != nil {
		return Task{}, fmt.Errorf("persist file task intent: %w", err)
	}
	if err := insertEvent(ctx, tx, request.NodeID, request.TaskID, "accepted", sql.NullString{}, sql.NullString{String: string(status), Valid: true}, request.ActorID, request.RemoteAddr, now); err != nil {
		return Task{}, fmt.Errorf("persist file task acceptance event: %w", err)
	}
	if err := audit.Record(ctx, tx, audit.Event{OccurredAt: now, Action: auditAction(request.Operation), Outcome: "accepted",
		ActorID: request.ActorID, RemoteAddr: request.RemoteAddr, Target: target}); err != nil {
		return Task{}, fmt.Errorf("persist file task acceptance audit: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Task{}, fmt.Errorf("commit durable file task acceptance: %w", err)
	}
	return Task{TaskID: request.TaskID, NodeID: request.NodeID, Operation: request.Operation, TargetPath: request.TargetPath,
		NewPath: request.NewPath, Status: status, CreatedAt: now, UpdatedAt: now}, nil
}

// MarkDispatched records a conservative delivery boundary before Core writes
// the first request frame. It leaves status queued because the current file
// protocol has no generic Agent-start acknowledgment. A process crash after
// this marker is treated as unknown and the command is never replayed.
func (s *Store) MarkDispatched(ctx context.Context, nodeID, taskID string, actorID sql.NullInt64, remoteAddr string) error {
	if ctx == nil || !canonicalUUID.MatchString(nodeID) || !canonicalUUID.MatchString(taskID) {
		return ErrInvalidRequest
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin file task dispatch marker: %w", err)
	}
	defer tx.Rollback()
	task, err := scanTask(tx.QueryRowContext(ctx, taskSelect+` WHERE node_id=? AND task_id=?`, nodeID, taskID))
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("read file task before dispatch marker: %w", err)
	}
	if task.Status != taskstate.Queued {
		return ErrStateConflict
	}
	if task.DispatchStartedAt != nil {
		return tx.Commit()
	}
	now := s.now().UTC()
	if _, err := tx.ExecContext(ctx, `UPDATE file_write_tasks SET dispatch_started_at_ns=?,updated_at_ns=? WHERE node_id=? AND task_id=? AND status='queued' AND dispatch_started_at_ns IS NULL`,
		now.UnixNano(), now.UnixNano(), nodeID, taskID); err != nil {
		return fmt.Errorf("persist file task dispatch marker: %w", err)
	}
	if err := insertEvent(ctx, tx, nodeID, taskID, "dispatch_started", sql.NullString{String: string(taskstate.Queued), Valid: true},
		sql.NullString{String: string(taskstate.Queued), Valid: true}, actorID, remoteAddr, now); err != nil {
		return fmt.Errorf("persist file task dispatch event: %w", err)
	}
	return tx.Commit()
}

// MarkRunning may only be called after an Agent acknowledgment that it has
// begun processing. Currently upload_begin is the only file protocol step
// with such a distinct acknowledgment; simple mutations go from queued to a
// terminal result when their Agent response arrives.
func (s *Store) MarkRunning(ctx context.Context, nodeID, taskID string, actorID sql.NullInt64, remoteAddr string) error {
	return s.transition(ctx, nodeID, taskID, taskstate.Running, "agent_started", "", actorID, remoteAddr)
}

func (s *Store) Resolve(ctx context.Context, nodeID, taskID string, status taskstate.Status, resultCode string, actorID sql.NullInt64, remoteAddr string) error {
	if status != taskstate.Succeeded && status != taskstate.Failed && status != taskstate.Unknown {
		return ErrInvalidRequest
	}
	if resultCode == "" || len(resultCode) > 64 || !isResultCode(resultCode) {
		return ErrInvalidRequest
	}
	return s.transition(ctx, nodeID, taskID, status, "task_resolved", resultCode, actorID, remoteAddr)
}

func (s *Store) transition(ctx context.Context, nodeID, taskID string, to taskstate.Status, event, resultCode string, actorID sql.NullInt64, remoteAddr string) error {
	if ctx == nil || !canonicalUUID.MatchString(nodeID) || !canonicalUUID.MatchString(taskID) {
		return ErrInvalidRequest
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin file task state transition: %w", err)
	}
	defer tx.Rollback()
	task, err := scanTask(tx.QueryRowContext(ctx, taskSelect+` WHERE node_id=? AND task_id=?`, nodeID, taskID))
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("read file task before transition: %w", err)
	}
	if task.Status == to && resultCode == task.ResultCode {
		return tx.Commit()
	}
	now := s.now().UTC()
	from := task.Status
	if taskstate.IsTerminal(from) || (from != taskstate.Queued && from != taskstate.Running) {
		return ErrStateConflict
	}
	if to == taskstate.Running {
		if from != taskstate.Queued || task.DispatchStartedAt == nil || taskstate.CanTransition(from, to, taskstate.Evidence{ExecutionAttempted: true}) != nil {
			return ErrStateConflict
		}
		if err := updateTaskStatus(ctx, tx, nodeID, taskID, from, to, "", now, now.UnixNano()); err != nil {
			return err
		}
		if err := insertEvent(ctx, tx, nodeID, taskID, event, sql.NullString{String: string(from), Valid: true}, sql.NullString{String: string(to), Valid: true}, actorID, remoteAddr, now); err != nil {
			return fmt.Errorf("persist file task start event: %w", err)
		}
		return tx.Commit()
	}
	if to != taskstate.Succeeded && to != taskstate.Failed && to != taskstate.Unknown {
		return ErrInvalidRequest
	}
	if resultCode == "verified" && to != taskstate.Succeeded || (resultCode == "not_dispatched" || resultCode == "not_committed" || resultCode == "agent_rejected") && to != taskstate.Failed || resultCode == "result_pending" && to != taskstate.Unknown {
		return ErrInvalidRequest
	}
	if (to == taskstate.Succeeded || to == taskstate.Unknown) && task.DispatchStartedAt == nil {
		return ErrStateConflict
	}
	// A terminal Agent response is the first available evidence of execution
	// for one-frame mutations. Record the status history through running in the
	// same transaction; started_at is the Core receipt time of the first Agent
	// evidence, not a claimed remote process start timestamp.
	if from == taskstate.Queued && (to == taskstate.Succeeded || resultCode == "agent_rejected") {
		if task.DispatchStartedAt == nil || taskstate.CanTransition(taskstate.Queued, taskstate.Running, taskstate.Evidence{ExecutionAttempted: true}) != nil {
			return ErrStateConflict
		}
		if err := updateTaskStatus(ctx, tx, nodeID, taskID, taskstate.Queued, taskstate.Running, "", now, now.UnixNano()); err != nil {
			return err
		}
		if err := insertEvent(ctx, tx, nodeID, taskID, "agent_started", sql.NullString{String: string(taskstate.Queued), Valid: true},
			sql.NullString{String: string(taskstate.Running), Valid: true}, actorID, remoteAddr, now); err != nil {
			return fmt.Errorf("persist Agent-confirmed file task start: %w", err)
		}
		from = taskstate.Running
	}
	evidence := fileTaskEvidence(from, to, resultCode, task.DispatchStartedAt != nil)
	if err := taskstate.CanTransition(from, to, evidence); err != nil {
		return fmt.Errorf("invalid file task transition: %w", err)
	}
	if err := updateTaskStatus(ctx, tx, nodeID, taskID, from, to, resultCode, now, 0); err != nil {
		return err
	}
	if err := insertEvent(ctx, tx, nodeID, taskID, event, sql.NullString{String: string(from), Valid: true}, sql.NullString{String: string(to), Valid: true}, actorID, remoteAddr, now); err != nil {
		return fmt.Errorf("persist file task state event: %w", err)
	}
	if fileTaskTerminal(to) {
		if err := audit.Record(ctx, tx, audit.Event{OccurredAt: now, Action: auditAction(task.Operation), Outcome: string(to),
			ActorID: actorID, RemoteAddr: remoteAddr, Target: taskAuditTarget(task)}); err != nil {
			return fmt.Errorf("persist file task terminal audit: %w", err)
		}
	}
	return tx.Commit()
}

func updateTaskStatus(ctx context.Context, tx *sql.Tx, nodeID, taskID string, from, to taskstate.Status, resultCode string, now time.Time, startedAt int64) error {
	var started any
	if startedAt > 0 {
		started = startedAt
	}
	var finished any
	if fileTaskTerminal(to) {
		finished = now.UnixNano()
	}
	updated, err := tx.ExecContext(ctx, `UPDATE file_write_tasks SET status=?,result_code=?,updated_at_ns=?,started_at_ns=COALESCE(started_at_ns,?),finished_at_ns=?
		WHERE node_id=? AND task_id=? AND status=?`, to, resultCode, now.UnixNano(), started, finished, nodeID, taskID, from)
	if err != nil {
		return fmt.Errorf("update durable file task: %w", err)
	}
	if count, _ := updated.RowsAffected(); count != 1 {
		return ErrStateConflict
	}
	return nil
}

func fileTaskEvidence(from, to taskstate.Status, resultCode string, dispatched bool) taskstate.Evidence {
	switch to {
	case taskstate.Succeeded:
		return taskstate.Evidence{ExecutionAttempted: true, ExecutionCompleted: true, PostconditionVerified: true, ActualResultConfirmed: true}
	case taskstate.Failed:
		return taskstate.Evidence{ExecutionAttempted: from == taskstate.Running || resultCode == "agent_rejected" || resultCode == "not_committed",
			ExecutionCompleted: from == taskstate.Running || resultCode == "agent_rejected" || resultCode == "not_committed",
			FailureConfirmed:   true, ActualResultConfirmed: true}
	case taskstate.Unknown:
		return taskstate.Evidence{ExecutionAttempted: from == taskstate.Running, DeliveryCommitted: from == taskstate.Queued && dispatched}
	default:
		return taskstate.Evidence{}
	}
}

func fileTaskTerminal(status taskstate.Status) bool {
	return taskstate.IsTerminal(status) || status == taskstate.Unknown
}

// RecoverUnfinished never retries a file write. Queued tasks were not
// dispatched; running tasks may have crossed the Agent boundary.
func (s *Store) RecoverUnfinished(ctx context.Context) error {
	if ctx == nil {
		return ErrInvalidRequest
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+taskColumns+` FROM file_write_tasks WHERE status IN ('queued','running') ORDER BY created_at_ns,task_id`)
	if err != nil {
		return fmt.Errorf("read unfinished file tasks: %w", err)
	}
	var pending []Task
	for rows.Next() {
		task, scanErr := scanTask(rows)
		if scanErr != nil {
			_ = rows.Close()
			return scanErr
		}
		pending = append(pending, task)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return fmt.Errorf("read unfinished file tasks: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close unfinished file task query: %w", err)
	}
	for _, task := range pending {
		status, result, event := taskstate.Unknown, "result_pending", "task_resolved"
		if task.Status == taskstate.Queued && task.DispatchStartedAt == nil {
			status, result, event = taskstate.Failed, "not_dispatched", "task_recovered"
		}
		if err := s.transition(ctx, task.NodeID, task.TaskID, status, event, result, sql.NullInt64{}, "unknown"); err != nil {
			return fmt.Errorf("recover unfinished file task %s: %w", task.TaskID, err)
		}
	}
	return nil
}

func (s *Store) Get(ctx context.Context, nodeID, taskID string) (Task, error) {
	if ctx == nil || !canonicalUUID.MatchString(nodeID) || !canonicalUUID.MatchString(taskID) {
		return Task{}, ErrInvalidRequest
	}
	task, err := scanTask(s.db.QueryRowContext(ctx, taskSelect+` WHERE node_id=? AND task_id=?`, nodeID, taskID))
	if errors.Is(err, sql.ErrNoRows) {
		return Task{}, ErrNotFound
	}
	if err != nil {
		return Task{}, fmt.Errorf("read durable file task: %w", err)
	}
	return task, nil
}

func (s *Store) List(ctx context.Context, nodeID string, limit int, after *coretasks.Cursor) (Page, error) {
	if ctx == nil || !canonicalUUID.MatchString(nodeID) || limit < 1 || limit > MaxPageSize {
		return Page{}, ErrInvalidRequest
	}
	query := `SELECT ` + taskColumns + ` FROM file_write_tasks WHERE node_id=?`
	args := []any{nodeID}
	if after != nil {
		if after.CreatedAtNS <= 0 || !canonicalUUID.MatchString(after.TaskID) {
			return Page{}, ErrInvalidRequest
		}
		query += ` AND (created_at_ns < ? OR (created_at_ns = ? AND task_id < ?))`
		args = append(args, after.CreatedAtNS, after.CreatedAtNS, after.TaskID)
	}
	query += ` ORDER BY created_at_ns DESC,task_id DESC LIMIT ?`
	args = append(args, limit+1)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return Page{}, fmt.Errorf("list durable file tasks: %w", err)
	}
	defer rows.Close()
	page := Page{Tasks: make([]Task, 0, limit)}
	for rows.Next() {
		task, err := scanTask(rows)
		if err != nil {
			return Page{}, err
		}
		if len(page.Tasks) == limit {
			last := page.Tasks[len(page.Tasks)-1]
			page.NextCursor = &coretasks.Cursor{CreatedAtNS: last.CreatedAt.UnixNano(), TaskID: last.TaskID}
			break
		}
		page.Tasks = append(page.Tasks, task)
	}
	if err := rows.Err(); err != nil {
		return Page{}, fmt.Errorf("read durable file task page: %w", err)
	}
	return page, nil
}

func (s *Store) AuditEvents(ctx context.Context, nodeID, taskID string) ([]Event, error) {
	if _, err := s.Get(ctx, nodeID, taskID); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id,event,from_status,to_status,actor_id,occurred_at_ns,remote_addr
		FROM file_write_task_events WHERE node_id=? AND task_id=? ORDER BY id LIMIT 513`, nodeID, taskID)
	if err != nil {
		return nil, fmt.Errorf("read durable file task events: %w", err)
	}
	defer rows.Close()
	events := make([]Event, 0, 4)
	for rows.Next() {
		var event Event
		var at int64
		if err := rows.Scan(&event.ID, &event.Event, &event.FromStatus, &event.ToStatus, &event.ActorID, &at, &event.RemoteAddr); err != nil {
			return nil, fmt.Errorf("scan durable file task event: %w", err)
		}
		event.OccurredAt = time.Unix(0, at).UTC()
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read durable file task events: %w", err)
	}
	return events, nil
}

const taskColumns = `task_id,node_id,operation,target_path,new_path,status,result_code,created_at_ns,updated_at_ns,dispatch_started_at_ns,started_at_ns,finished_at_ns`
const taskSelect = `SELECT ` + taskColumns + ` FROM file_write_tasks`

type scanner interface{ Scan(...any) error }

func scanTask(row scanner) (Task, error) {
	var task Task
	var created, updated int64
	var dispatched, started, finished sql.NullInt64
	if err := row.Scan(&task.TaskID, &task.NodeID, &task.Operation, &task.TargetPath, &task.NewPath, &task.Status, &task.ResultCode,
		&created, &updated, &dispatched, &started, &finished); err != nil {
		return Task{}, err
	}
	task.CreatedAt, task.UpdatedAt = time.Unix(0, created).UTC(), time.Unix(0, updated).UTC()
	if dispatched.Valid {
		value := time.Unix(0, dispatched.Int64).UTC()
		task.DispatchStartedAt = &value
	}
	if started.Valid {
		value := time.Unix(0, started.Int64).UTC()
		task.StartedAt = &value
	}
	if finished.Valid {
		value := time.Unix(0, finished.Int64).UTC()
		task.FinishedAt = &value
	}
	return task, nil
}

func insertEvent(ctx context.Context, tx *sql.Tx, nodeID, taskID, event string, from, to sql.NullString, actorID sql.NullInt64, remote string, at time.Time) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO file_write_task_events(node_id,task_id,event,from_status,to_status,actor_id,occurred_at_ns,remote_addr)
		VALUES(?,?,?,?,?,?,?,?)`, nodeID, taskID, event, from, to, nullableActor(actorID), at.UnixNano(), auditRemote(remote))
	return err
}

func taskAuditTarget(task Task) audit.Target {
	paths := []string{task.TargetPath}
	if task.NewPath != "" {
		paths = append(paths, task.NewPath)
	}
	encoded, _ := json.Marshal(paths)
	return audit.Target{Kind: audit.TargetFile, ID: audit.FileTarget(task.NodeID, task.TaskID, string(encoded))}
}

func auditAction(operation string) string {
	if operation == OperationUpload {
		return "file_upload"
	}
	return "file_" + operation
}

func validRequest(request CreateRequest) bool {
	if !canonicalUUID.MatchString(request.TaskID) || !canonicalUUID.MatchString(request.NodeID) || request.Operation == "" ||
		len(request.TargetPath) == 0 || len(request.TargetPath) > 4096 || !utf8.ValidString(request.TargetPath) || strings.ContainsRune(request.TargetPath, '\x00') {
		return false
	}
	switch request.Operation {
	case OperationMkdir, OperationDelete, OperationSaveText, OperationUpload:
		return request.NewPath == ""
	case OperationRename:
		return len(request.NewPath) > 0 && len(request.NewPath) <= 4096 && utf8.ValidString(request.NewPath) && !strings.ContainsRune(request.NewPath, '\x00')
	default:
		return false
	}
}

func isResultCode(value string) bool {
	switch value {
	case "verified", "agent_rejected", "result_pending", "not_dispatched", "not_committed":
		return true
	default:
		return false
	}
}

func nullableActor(value sql.NullInt64) any {
	if !value.Valid {
		return nil
	}
	return value.Int64
}

func auditRemote(value string) string {
	if ip := net.ParseIP(value); ip == nil {
		return "unknown"
	}
	return net.ParseIP(value).String()
}
