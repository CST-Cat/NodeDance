package audit

import (
	"context"
	"database/sql"
	"errors"
	"net"
	"time"
)

type Event struct {
	OccurredAt time.Time
	Action     string
	Outcome    string
	ActorID    sql.NullInt64
	RemoteAddr string
}

type Execer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

var actions = map[string]struct{}{
	"setup": {}, "login": {}, "logout": {}, "password_change": {},
	"session_revoke": {}, "appearance_update": {}, "image_upload": {},
}

var outcomes = map[string]struct{}{
	"succeeded": {}, "rejected": {}, "rate_limited": {}, "failed": {},
}

// Record writes only the typed, allowlisted audit fields. Request bodies,
// headers, cookies, tokens and arbitrary metadata are not accepted here.
func Record(ctx context.Context, execer Execer, event Event) error {
	if _, ok := actions[event.Action]; !ok {
		return errors.New("audit action is not allowlisted")
	}
	if _, ok := outcomes[event.Outcome]; !ok {
		return errors.New("audit outcome is not allowlisted")
	}
	if event.OccurredAt.IsZero() {
		event.OccurredAt = time.Now()
	}
	if ip := net.ParseIP(event.RemoteAddr); event.RemoteAddr != "unknown" && ip == nil {
		event.RemoteAddr = "unknown"
	}
	_, err := execer.ExecContext(ctx, `INSERT INTO audit_entries(occurred_at, action, outcome, actor_id, remote_addr) VALUES(?, ?, ?, ?, ?)`, event.OccurredAt.Unix(), event.Action, event.Outcome, nullableActor(event.ActorID), event.RemoteAddr)
	return err
}

func nullableActor(actor sql.NullInt64) any {
	if !actor.Valid {
		return nil
	}
	return actor.Int64
}
