package agents

import (
	"context"
	"database/sql"
	"errors"
	"github.com/CST-Cat/NodeDance/internal/core/audit"
	"strings"
	"time"
)

type GrantRequest struct {
	DisplayPrefix   string `json:"displayPrefix"`
	MaxUses         int    `json:"maxUses"`
	LifetimeSeconds int    `json:"lifetimeSeconds"`
	CoreURL         string `json:"coreUrl,omitempty"`
}
type Grant struct {
	ID            string    `json:"id"`
	DisplayPrefix string    `json:"displayPrefix"`
	MaxUses       int       `json:"maxUses"`
	Uses          int       `json:"uses"`
	ExpiresAt     time.Time `json:"expiresAt"`
	Revoked       bool      `json:"revoked"`
	Token         string    `json:"token,omitempty"`
}

func ValidateGrant(v GrantRequest) error {
	if strings.TrimSpace(v.DisplayPrefix) == "" || len(v.DisplayPrefix) > 60 || strings.ContainsAny(v.DisplayPrefix, "\x00\r\n") || v.MaxUses < 1 || v.MaxUses > 1000 || v.LifetimeSeconds < 60 || v.LifetimeSeconds > 86400 {
		return errors.New("grant requires a display prefix, 1..1000 uses and a 60..86400 second lifetime")
	}
	return nil
}
func (r *Repository) CreateGrant(ctx context.Context, v GrantRequest, remote string, actor sql.NullInt64) (Grant, error) {
	if err := ValidateGrant(v); err != nil {
		return Grant{}, err
	}
	id, err := newID()
	if err != nil {
		return Grant{}, err
	}
	token, digest, err := newSecret()
	if err != nil {
		return Grant{}, err
	}
	now := r.now()
	g := Grant{ID: id, DisplayPrefix: v.DisplayPrefix, MaxUses: v.MaxUses, ExpiresAt: now.Add(time.Duration(v.LifetimeSeconds) * time.Second), Token: token}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return Grant{}, err
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, `INSERT INTO agent_enrollment_grants(id,token_digest,display_prefix,max_uses,expires_at,created_at) VALUES(?,?,?,?,?,?)`, id, digest, g.DisplayPrefix, g.MaxUses, g.ExpiresAt.Unix(), now.Unix())
	if err == nil {
		err = audit.Record(ctx, tx, audit.Event{OccurredAt: now, Action: "agent_grant_create", Outcome: "succeeded", ActorID: actor, RemoteAddr: remote, Target: audit.Target{Kind: audit.TargetAgent, ID: id}})
	}
	if err != nil {
		return Grant{}, err
	}
	err = tx.Commit()
	return g, err
}
func (r *Repository) ListGrants(ctx context.Context) ([]Grant, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT id,display_prefix,max_uses,uses,expires_at,revoked_at IS NOT NULL FROM agent_enrollment_grants ORDER BY created_at DESC LIMIT 100`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	grants := []Grant{}
	for rows.Next() {
		var g Grant
		var at int64
		if err := rows.Scan(&g.ID, &g.DisplayPrefix, &g.MaxUses, &g.Uses, &at, &g.Revoked); err != nil {
			return nil, err
		}
		g.ExpiresAt = time.Unix(at, 0).UTC()
		grants = append(grants, g)
	}
	return grants, rows.Err()
}
func (r *Repository) RevokeGrant(ctx context.Context, id, remote string, actor sql.NullInt64) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE agent_enrollment_grants SET revoked_at=COALESCE(revoked_at,?) WHERE id=?`, r.now().Unix(), id)
	if err != nil {
		return err
	}
	n, _ := result.RowsAffected()
	if n != 1 {
		return ErrNotFound
	}
	err = audit.Record(ctx, tx, audit.Event{OccurredAt: r.now(), Action: "agent_grant_revoke", Outcome: "succeeded", ActorID: actor, RemoteAddr: remote, Target: audit.Target{Kind: audit.TargetAgent, ID: id}})
	if err != nil {
		return err
	}
	return tx.Commit()
}
