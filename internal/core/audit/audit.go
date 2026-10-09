package audit

import (
	"context"
	"database/sql"
	"errors"
	"net"
	"regexp"
	"time"
)

type TargetKind string

const (
	TargetNode              TargetKind = "node"
	TargetAgent             TargetKind = "agent"
	TargetTerminalHost      TargetKind = "terminal_host"
	TargetTerminalContainer TargetKind = "terminal_container"
)

type Target struct {
	Kind TargetKind
	ID   string
}

type Event struct {
	OccurredAt time.Time
	Action     string
	Outcome    string
	ActorID    sql.NullInt64
	RemoteAddr string
	Target     Target
}

type Execer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

var actions = map[string]struct{}{
	"setup": {}, "login": {}, "logout": {}, "password_change": {},
	"session_revoke": {}, "appearance_update": {}, "image_upload": {},
	"agent_enrollment_create": {}, "agent_enrollment_consume": {},
	"agent_revoke": {}, "agent_rotation_request": {},
	"agent_rotation_prepare": {}, "agent_rotation_commit": {},
	"compose_operation": {}, "terminal_start": {}, "terminal_end": {},
}

var outcomes = map[string]struct{}{
	"succeeded": {}, "rejected": {}, "rate_limited": {}, "failed": {},
	"accepted": {}, "timed_out": {}, "unknown": {},
}

var targetIDPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
var containerIDPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

// Record writes only the typed, allowlisted audit fields. Request bodies,
// headers, cookies, tokens and arbitrary metadata are not accepted here.
func Record(ctx context.Context, execer Execer, event Event) error {
	if _, ok := actions[event.Action]; !ok {
		return errors.New("audit action is not allowlisted")
	}
	if _, ok := outcomes[event.Outcome]; !ok {
		return errors.New("audit outcome is not allowlisted")
	}
	targetKind, targetID := any(nil), any(nil)
	if event.Target.Kind != "" || event.Target.ID != "" {
		switch event.Target.Kind {
		case TargetNode, TargetAgent:
			if !targetIDPattern.MatchString(event.Target.ID) {
				return errors.New("audit target ID must be a canonical UUID")
			}
			targetKind, targetID = string(event.Target.Kind), event.Target.ID
		case TargetTerminalHost:
			if !targetIDPattern.MatchString(event.Target.ID) {
				return errors.New("host terminal audit target ID must be a canonical node UUID")
			}
			targetKind, targetID = string(event.Target.Kind), event.Target.ID
		case TargetTerminalContainer:
			if !containerIDPattern.MatchString(event.Target.ID) {
				return errors.New("container terminal audit target ID must be a full Docker ID")
			}
			targetKind, targetID = string(event.Target.Kind), event.Target.ID
		default:
			return errors.New("audit target kind is not allowlisted")
		}
	}
	if event.OccurredAt.IsZero() {
		event.OccurredAt = time.Now()
	}
	if ip := net.ParseIP(event.RemoteAddr); event.RemoteAddr != "unknown" && ip == nil {
		event.RemoteAddr = "unknown"
	}
	_, err := execer.ExecContext(ctx, `INSERT INTO audit_entries(occurred_at, action, outcome, actor_id, remote_addr, target_kind, target_id)
		VALUES(?, ?, ?, ?, ?, ?, ?)`, event.OccurredAt.Unix(), event.Action, event.Outcome, nullableActor(event.ActorID), event.RemoteAddr, targetKind, targetID)
	return err
}

func nullableActor(actor sql.NullInt64) any {
	if !actor.Valid {
		return nil
	}
	return actor.Int64
}
