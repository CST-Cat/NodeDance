package agents_test

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"testing"
	"time"

	"github.com/CST-Cat/NodeDance/internal/core/agents"
	"github.com/CST-Cat/NodeDance/internal/core/auth"
	"github.com/CST-Cat/NodeDance/internal/core/storage"
)

func newAgentRepository(t *testing.T) *agents.Repository {
	t.Helper()
	database, err := storage.Open(context.Background(), t.TempDir())
	if err != nil {
		t.Fatalf("open Core database: %v", err)
	}
	t.Cleanup(func() { database.Close() })
	return agents.NewRepository(database.DB, func() time.Time { return time.Unix(1730000000, 0) })
}

func deviceCredential(t *testing.T) (string, []byte) {
	t.Helper()
	buffer := make([]byte, 32)
	if _, err := rand.Read(buffer); err != nil {
		t.Fatal(err)
	}
	raw := hex.EncodeToString(buffer)
	return raw, auth.DigestToken(raw)
}

func TestEnrollmentCreateConsumeAndRevoke(t *testing.T) {
	ctx := context.Background()
	repository := newAgentRepository(t)
	enrollment, err := repository.CreateEnrollment(ctx, "Edge VPS", "127.0.0.1", actor())
	if err != nil {
		t.Fatalf("create enrollment: %v", err)
	}
	if enrollment.Token == "" || enrollment.NodeID == "" || enrollment.DisplayName != "Edge VPS" {
		t.Fatalf("unexpected enrollment: %+v", enrollment)
	}
	credential, deviceDigest := deviceCredential(t)
	identity, err := repository.ConsumeEnrollment(ctx, auth.DigestToken(enrollment.Token), deviceDigest, "request-1", "127.0.0.1")
	if err != nil {
		t.Fatalf("consume enrollment: %v", err)
	}
	if identity.NodeID != enrollment.NodeID || identity.AgentID == "" || identity.Status != "offline" {
		t.Fatalf("unexpected consumed identity: %+v", identity)
	}
	// The enrollment token is single-use.
	_, secondDigest := deviceCredential(t)
	if _, err := repository.ConsumeEnrollment(ctx, auth.DigestToken(enrollment.Token), secondDigest, "request-2", "127.0.0.1"); !errors.Is(err, agents.ErrEnrollmentRejected) {
		t.Fatalf("replayed enrollment token was accepted: %v", err)
	}
	// The enrolled device recovers its identity by credential; others do not.
	recovered, err := repository.IdentityByCredential(ctx, deviceDigest)
	if err != nil || recovered.AgentID != identity.AgentID {
		t.Fatalf("credential recovery failed: %+v %v", recovered, err)
	}
	if _, err := repository.IdentityByCredential(ctx, auth.DigestToken("not-enrolled")); !errors.Is(err, agents.ErrUnauthorized) {
		t.Fatalf("unknown credential was accepted: %v", err)
	}
	if _, err := repository.Revoke(ctx, identity.AgentID, "127.0.0.1", actor()); err != nil {
		t.Fatalf("revoke agent: %v", err)
	}
	if _, err := repository.IdentityByCredential(ctx, deviceDigest); !errors.Is(err, agents.ErrUnauthorized) {
		t.Fatalf("revoked credential still authenticates: %v", err)
	}
	if _, err := repository.IdentityByCredential(ctx, secondDigest); !errors.Is(err, agents.ErrUnauthorized) {
		t.Fatal("unrelated credential authenticated")
	}
	_ = credential

}

func TestEnrollmentGrantConsumption(t *testing.T) {
	ctx := context.Background()
	repository := newAgentRepository(t)
	grant, err := repository.CreateGrant(ctx, agents.GrantRequest{DisplayPrefix: "edge", MaxUses: 2, LifetimeSeconds: 3600}, "127.0.0.1", actor())
	if err != nil {
		t.Fatalf("create grant: %v", err)
	}
	digest := auth.DigestToken(grant.Token)
	first, err := repository.ConsumeEnrollment(ctx, digest, digestOf(t, "device-one"), "request-1", "127.0.0.1")
	if err != nil {
		t.Fatalf("consume by grant: %v", err)
	}
	if first.DisplayName != "edge-"+first.NodeID[:8] {
		t.Fatalf("grant display name pattern changed: %+v", first)
	}
	second, err := repository.ConsumeEnrollment(ctx, digest, digestOf(t, "device-two"), "request-2", "127.0.0.1")
	if err != nil {
		t.Fatalf("second grant use: %v", err)
	}
	if second.NodeID == first.NodeID {
		t.Fatal("grant uses must create separate identities")
	}
	if _, err := repository.ConsumeEnrollment(ctx, digest, digestOf(t, "device-three"), "request-3", "127.0.0.1"); !errors.Is(err, agents.ErrEnrollmentRejected) {
		t.Fatal("grant use beyond max_uses was accepted")
	}
	listed, err := repository.ListGrants(ctx)
	if err != nil || len(listed) != 1 || listed[0].Uses != 2 {
		t.Fatalf("grant use counter: %v %+v", err, listed)
	}
	// A revoked grant stops issuing identities immediately.
	revocable, err := repository.CreateGrant(ctx, agents.GrantRequest{DisplayPrefix: "tmp", MaxUses: 5, LifetimeSeconds: 3600}, "127.0.0.1", actor())
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.RevokeGrant(ctx, revocable.ID, "127.0.0.1", actor()); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.ConsumeEnrollment(ctx, auth.DigestToken(revocable.Token), digestOf(t, "device-four"), "request-4", "127.0.0.1"); !errors.Is(err, agents.ErrEnrollmentRejected) {
		t.Fatal("revoked grant was consumed")
	}
}

func digestOf(t *testing.T, name string) []byte {
	t.Helper()
	return auth.DigestToken(name + "-device-credential")
}

func actor() sql.NullInt64 {
	return sql.NullInt64{Int64: 1, Valid: true}
}
