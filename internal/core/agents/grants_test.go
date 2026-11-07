package agents_test

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/CST-Cat/NodeDance/internal/core/agents"
	"github.com/CST-Cat/NodeDance/internal/core/storage"
)

func newGrantRepository(t *testing.T) *agents.Repository {
	t.Helper()
	database, err := storage.Open(context.Background(), t.TempDir())
	if err != nil {
		t.Fatalf("open Core database: %v", err)
	}
	t.Cleanup(func() { database.Close() })
	return agents.NewRepository(database.DB, func() time.Time { return time.Unix(1730000000, 0) })
}

func TestGrantLifecycle(t *testing.T) {
	ctx := context.Background()
	repository := newGrantRepository(t)
	grant, err := repository.CreateGrant(ctx, agents.GrantRequest{DisplayPrefix: "edge ", MaxUses: 5, LifetimeSeconds: 3600}, "127.0.0.1", sql.NullInt64{Int64: 1, Valid: true})
	if err != nil {
		t.Fatalf("create grant: %v", err)
	}
	if grant.Token == "" || len(grant.Token) < 32 {
		t.Fatal("created grant did not return a usable one-time token")
	}
	if grant.Uses != 0 || grant.Revoked || grant.MaxUses != 5 {
		t.Fatalf("unexpected grant state: %+v", grant)
	}
	if !grant.ExpiresAt.After(time.Unix(1730000000, 0)) {
		t.Fatalf("grant lifetime was not applied: %v", grant.ExpiresAt)
	}
	listed, err := repository.ListGrants(ctx)
	if err != nil || len(listed) != 1 {
		t.Fatalf("list grants: %v %d", err, len(listed))
	}
	if listed[0].ID != grant.ID || listed[0].Token != "" {
		t.Fatalf("grant listing must show metadata without the secret token: %+v", listed[0])
	}
	if err := repository.RevokeGrant(ctx, grant.ID, "127.0.0.1", sql.NullInt64{Int64: 1, Valid: true}); err != nil {
		t.Fatalf("revoke grant: %v", err)
	}
	listed, err = repository.ListGrants(ctx)
	if err != nil || len(listed) != 1 || !listed[0].Revoked {
		t.Fatalf("revoked grant state: %v %+v", err, listed)
	}
	if err := repository.RevokeGrant(ctx, "00000000-0000-4000-8000-000000000000", "127.0.0.1", sql.NullInt64{Int64: 1, Valid: true}); !errors.Is(err, agents.ErrNotFound) {
		t.Fatalf("revoking an unknown grant must return ErrNotFound, got %v", err)
	}
}

func TestValidateGrantBoundaries(t *testing.T) {
	valid := []agents.GrantRequest{
		{DisplayPrefix: "edge", MaxUses: 1, LifetimeSeconds: 60},
		{DisplayPrefix: strings.Repeat("x", 60), MaxUses: 1000, LifetimeSeconds: 86400},
	}
	for _, request := range valid {
		if err := agents.ValidateGrant(request); err != nil {
			t.Fatalf("valid grant rejected: %+v: %v", request, err)
		}
	}
	invalid := []agents.GrantRequest{
		{DisplayPrefix: "  ", MaxUses: 1, LifetimeSeconds: 60},
		{DisplayPrefix: strings.Repeat("x", 61), MaxUses: 1, LifetimeSeconds: 60},
		{DisplayPrefix: "bad\nline", MaxUses: 1, LifetimeSeconds: 60},
		{DisplayPrefix: "edge", MaxUses: 0, LifetimeSeconds: 60},
		{DisplayPrefix: "edge", MaxUses: 1001, LifetimeSeconds: 60},
		{DisplayPrefix: "edge", MaxUses: 1, LifetimeSeconds: 59},
		{DisplayPrefix: "edge", MaxUses: 1, LifetimeSeconds: 86401},
	}
	for _, request := range invalid {
		if err := agents.ValidateGrant(request); err == nil {
			t.Fatalf("invalid grant was accepted: %+v", request)
		}
	}
}
