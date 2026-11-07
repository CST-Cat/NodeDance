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
	"github.com/CST-Cat/NodeDance/internal/protocol"
)

func newAgentRepository(t *testing.T) (*agents.Repository, *sql.DB) {
	t.Helper()
	database, err := storage.Open(context.Background(), t.TempDir())
	if err != nil {
		t.Fatalf("open Core database: %v", err)
	}
	t.Cleanup(func() { database.Close() })
	repository := agents.NewRepository(database.DB, func() time.Time { return time.Unix(1730000000, 0) })
	return repository, database.DB
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
	repository, _ := newAgentRepository(t)
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
	repository, _ := newAgentRepository(t)
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

func rotationPermissions() protocol.RuntimePermissions {
	return protocol.RuntimePermissions{OS: "linux", Architecture: "arm64"}
}

func TestCredentialRotationLifecycle(t *testing.T) {
	ctx := context.Background()
	repository, _ := newAgentRepository(t)
	enrollment, err := repository.CreateEnrollment(ctx, "Rotating VPS", "127.0.0.1", actor())
	if err != nil {
		t.Fatal(err)
	}
	oldRaw, oldDigest := deviceCredential(t)
	_ = oldRaw
	identity, err := repository.ConsumeEnrollment(ctx, auth.DigestToken(enrollment.Token), oldDigest, "request-1", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	lease, err := repository.BeginConnection(ctx, identity, oldDigest, 1, "agent-v0.0.0-test", "[]", rotationPermissions())
	if err != nil || lease.Identity.Status != "online" {
		t.Fatalf("begin connection: %+v %v", lease, err)
	}
	generation := lease.Identity.ConnectionGeneration
	if generation == 0 {
		t.Fatal("connection generation was not advanced")
	}

	requested, err := repository.RequestRotation(ctx, identity.AgentID, "127.0.0.1", actor())
	if err != nil || requested.RotationRequestedID == "" {
		t.Fatalf("request rotation: %+v %v", requested, err)
	}
	repeated, err := repository.RequestRotation(ctx, identity.AgentID, "127.0.0.1", actor())
	if err != nil || repeated.RotationRequestedID != requested.RotationRequestedID {
		t.Fatalf("rotation request must be idempotent: %+v vs %+v %v", repeated, requested, err)
	}

	newRaw, newDigest := deviceCredential(t)
	_ = newRaw
	if err := repository.PrepareRotation(ctx, identity, generation, "wrong-id", newDigest); !errors.Is(err, agents.ErrConflict) {
		t.Fatalf("prepare with foreign rotation id: %v", err)
	}
	if err := repository.PrepareRotation(ctx, identity, generation, requested.RotationRequestedID, oldDigest); !errors.Is(err, agents.ErrConflict) {
		t.Fatalf("prepare with the current credential digest: %v", err)
	}
	if err := repository.PrepareRotation(ctx, identity, generation+1, requested.RotationRequestedID, newDigest); !errors.Is(err, agents.ErrStaleConnection) {
		t.Fatalf("prepare with a stale generation: %v", err)
	}
	if err := repository.PrepareRotation(ctx, identity, generation, requested.RotationRequestedID, newDigest); err != nil {
		t.Fatalf("prepare rotation: %v", err)
	}
	if err := repository.PrepareRotation(ctx, identity, generation, requested.RotationRequestedID, newDigest); err != nil {
		t.Fatalf("prepare rotation replay must be idempotent: %v", err)
	}
	pending, err := repository.IdentityByCredential(ctx, newDigest)
	if err != nil || !pending.CredentialIsPending || pending.CredentialRotationID != requested.RotationRequestedID {
		t.Fatalf("pending credential state: %+v %v", pending, err)
	}

	committed, err := repository.BeginConnection(ctx, identity, newDigest, 1, "agent-v0.0.0-test", "[]", rotationPermissions())
	if err != nil || !committed.RotationCommitted {
		t.Fatalf("commit rotation through connection: %+v %v", committed, err)
	}
	if _, err := repository.IdentityByCredential(ctx, oldDigest); !errors.Is(err, agents.ErrUnauthorized) {
		t.Fatal("retired credential still authenticates")
	}
	active, err := repository.IdentityByCredential(ctx, newDigest)
	if err != nil || active.CredentialIsPending {
		t.Fatalf("promoted credential state: %+v %v", active, err)
	}
	fresh, err := repository.RequestRotation(ctx, identity.AgentID, "127.0.0.1", actor())
	if err != nil || fresh.RotationRequestedID == "" || fresh.RotationRequestedID == requested.RotationRequestedID {
		t.Fatalf("a committed rotation must allow a fresh request: %+v %v", fresh, err)
	}
}

func TestLeaseLifecycle(t *testing.T) {
	ctx := context.Background()
	repository, database := newAgentRepository(t)
	enrollment, err := repository.CreateEnrollment(ctx, "Leased VPS", "127.0.0.1", actor())
	if err != nil {
		t.Fatal(err)
	}
	_, digest := deviceCredential(t)
	identity, err := repository.ConsumeEnrollment(ctx, auth.DigestToken(enrollment.Token), digest, "request-1", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	lease, err := repository.BeginConnection(ctx, identity, digest, 1, "agent-v0.0.0-test", "[]", rotationPermissions())
	if err != nil {
		t.Fatal(err)
	}
	generation := lease.Identity.ConnectionGeneration

	if _, err := repository.AcceptHeartbeat(ctx, identity, generation, 0, time.Hour, nil); !errors.Is(err, agents.ErrStaleConnection) {
		t.Fatalf("heartbeat sequence 0 was accepted: %v", err)
	}
	if _, err := repository.AcceptHeartbeat(ctx, identity, generation+1, 1, time.Hour, nil); !errors.Is(err, agents.ErrStaleConnection) {
		t.Fatalf("heartbeat with a foreign generation was accepted: %v", err)
	}
	if _, err := repository.AcceptHeartbeat(ctx, identity, generation, 1, time.Hour, []string{"files.v1"}); err != nil {
		t.Fatalf("first heartbeat: %v", err)
	}
	if _, err := repository.AcceptHeartbeat(ctx, identity, generation, 1, time.Hour, nil); !errors.Is(err, agents.ErrStaleConnection) {
		t.Fatal("replayed heartbeat sequence was accepted")
	}
	if _, err := repository.AcceptHeartbeat(ctx, identity, generation, 2, time.Hour, nil); err != nil {
		t.Fatalf("monotonic heartbeat: %v", err)
	}
	if _, active, err := repository.ActiveLeaseLastSeen(ctx, identity.NodeID, generation); err != nil || !active {
		t.Fatalf("active lease lookup: %v %v", active, err)
	}

	// Backdate the lease past the offline window and sweep it.
	if _, err := database.ExecContext(ctx, `UPDATE nodes SET last_seen_at=1 WHERE id=?`, identity.NodeID); err != nil {
		t.Fatal(err)
	}
	stale, err := repository.MarkExpiredLeases(ctx, time.Hour)
	if err != nil || len(stale) != 1 || stale[0].NodeID != identity.NodeID || stale[0].Generation != generation {
		t.Fatalf("expired lease sweep: %+v %v", stale, err)
	}
	if _, active, err := repository.ActiveLeaseLastSeen(ctx, identity.NodeID, generation); err != nil || active {
		t.Fatalf("expired lease still active: %v %v", active, err)
	}
	if _, err := repository.AcceptHeartbeat(ctx, identity, generation, 3, time.Hour, nil); !errors.Is(err, agents.ErrStaleConnection) {
		t.Fatal("heartbeat after expiry was accepted")
	}
}

func TestExpireLeaseExactDeadline(t *testing.T) {
	ctx := context.Background()
	repository, database := newAgentRepository(t)
	enrollment, err := repository.CreateEnrollment(ctx, "Deadline VPS", "127.0.0.1", actor())
	if err != nil {
		t.Fatal(err)
	}
	_, digest := deviceCredential(t)
	identity, err := repository.ConsumeEnrollment(ctx, auth.DigestToken(enrollment.Token), digest, "request-1", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	lease, err := repository.BeginConnection(ctx, identity, digest, 1, "agent-v0.0.0-test", "[]", rotationPermissions())
	if err != nil {
		t.Fatal(err)
	}
	generation := lease.Identity.ConnectionGeneration
	if _, err := repository.AcceptHeartbeat(ctx, identity, generation, 1, time.Hour, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.ExpireLease(ctx, identity.NodeID, generation, -time.Hour); !errors.Is(err, agents.ErrStaleConnection) {
		t.Fatalf("non-positive offline window: %v", err)
	}
	if _, err := repository.ExpireLease(ctx, identity.NodeID, generation, time.Hour); err != nil {
		t.Fatalf("fresh lease must not expire: %v", err)
	}
	if _, err := database.ExecContext(ctx, `UPDATE nodes SET last_seen_at=1 WHERE id=?`, identity.NodeID); err != nil {
		t.Fatal(err)
	}
	expired, err := repository.ExpireLease(ctx, identity.NodeID, generation, time.Hour)
	if err != nil || !expired {
		t.Fatalf("expire backdated lease: %v %v", expired, err)
	}
	expired, err = repository.ExpireLease(ctx, identity.NodeID, generation, time.Hour)
	if err != nil || expired {
		t.Fatalf("double expiry must be a no-op: %v %v", expired, err)
	}
}

func TestRenewEnrollmentReplacesUnconsumedCredential(t *testing.T) {
	ctx := context.Background()
	repository, _ := newAgentRepository(t)
	enrollment, err := repository.CreateEnrollment(ctx, "Pending VPS", "127.0.0.1", actor())
	if err != nil {
		t.Fatal(err)
	}
	renewed, err := repository.RenewEnrollment(ctx, enrollment.NodeID, "127.0.0.1", actor())
	if err != nil {
		t.Fatalf("renew enrollment: %v", err)
	}
	if renewed.NodeID != enrollment.NodeID || renewed.DisplayName != "Pending VPS" || renewed.Token == "" {
		t.Fatalf("unexpected renewal: %+v", renewed)
	}
	if _, err := repository.ConsumeEnrollment(ctx, auth.DigestToken(enrollment.Token), digestOf(t, "stale-device"), "request-1", "127.0.0.1"); !errors.Is(err, agents.ErrEnrollmentRejected) {
		t.Fatal("renewed node still accepted the replaced credential")
	}
	identity, err := repository.ConsumeEnrollment(ctx, auth.DigestToken(renewed.Token), digestOf(t, "fresh-device"), "request-2", "127.0.0.1")
	if err != nil {
		t.Fatalf("consume renewed credential: %v", err)
	}
	if _, err := repository.RenewEnrollment(ctx, identity.NodeID, "127.0.0.1", actor()); !errors.Is(err, agents.ErrConflict) {
		t.Fatalf("renewal after consumption must conflict: %v", err)
	}
}

func TestDeleteNodeRemovesAllCoreState(t *testing.T) {
	ctx := context.Background()
	repository, _ := newAgentRepository(t)
	enrollment, err := repository.CreateEnrollment(ctx, "Doomed VPS", "127.0.0.1", actor())
	if err != nil {
		t.Fatal(err)
	}
	_, digest := deviceCredential(t)
	identity, err := repository.ConsumeEnrollment(ctx, auth.DigestToken(enrollment.Token), digest, "request-1", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.DeleteNode(ctx, identity.NodeID, "127.0.0.1", actor()); err != nil {
		t.Fatalf("delete node: %v", err)
	}
	if _, err := repository.IdentityByCredential(ctx, digest); !errors.Is(err, agents.ErrUnauthorized) {
		t.Fatal("deleted device credential still authenticates")
	}
	nodes, err := repository.ListNodes(ctx)
	if err != nil || len(nodes) != 0 {
		t.Fatalf("deleted node remained listed: %v %d", err, len(nodes))
	}
	if _, err := repository.DeleteNode(ctx, identity.NodeID, "127.0.0.1", actor()); !errors.Is(err, agents.ErrNotFound) {
		t.Fatalf("second delete must be ErrNotFound: %v", err)
	}
}

func TestEnrollmentIPUsesConsumptionAuditOnly(t *testing.T) {
	ctx := context.Background()
	repository, _ := newAgentRepository(t)
	enrollment, err := repository.CreateEnrollment(ctx, "Traced VPS", "127.0.0.1", actor())
	if err != nil {
		t.Fatal(err)
	}
	_, digest := deviceCredential(t)
	identity, err := repository.ConsumeEnrollment(ctx, auth.DigestToken(enrollment.Token), digest, "request-1", "203.0.113.9")
	if err != nil {
		t.Fatal(err)
	}
	if ip := repository.EnrollmentIP(ctx, identity.AgentID); ip != "203.0.113.9" {
		t.Fatalf("enrollment IP must come from the consume audit record, got %q", ip)
	}
	if ip := repository.EnrollmentIP(ctx, ""); ip != "" {
		t.Fatalf("empty agent id must yield an empty ip, got %q", ip)
	}
}

func TestListNodesAndAgentChannel(t *testing.T) {
	ctx := context.Background()
	repository, _ := newAgentRepository(t)
	if nodes, err := repository.ListNodes(ctx); err != nil || len(nodes) != 0 {
		t.Fatalf("empty node list: %v %d", err, len(nodes))
	}
	enrollment, err := repository.CreateEnrollment(ctx, "Listed VPS", "127.0.0.1", actor())
	if err != nil {
		t.Fatal(err)
	}
	// A pending node without a device lists with no AgentID.
	nodes, err := repository.ListNodes(ctx)
	if err != nil || len(nodes) != 1 || nodes[0].AgentID != "" || nodes[0].Status != "pending" {
		t.Fatalf("pending node list: %+v %v", nodes, err)
	}
	if _, ok, err := repository.AgentChannel(ctx, "missing"); !errors.Is(err, agents.ErrNotFound) || ok {
		t.Fatalf("unknown agent channel: %v %v", ok, err)
	}
	_, digest := deviceCredential(t)
	identity, err := repository.ConsumeEnrollment(ctx, auth.DigestToken(enrollment.Token), digest, "request-1", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.BeginConnection(ctx, identity, digest, 1, "agent-v0.0.0-test", `["files.v1"]`, rotationPermissions()); err != nil {
		t.Fatal(err)
	}
	nodes, err = repository.ListNodes(ctx)
	if err != nil || len(nodes) != 1 {
		t.Fatal(err)
	}
	node := nodes[0]
	if node.AgentID != identity.AgentID || node.Status != "online" || !node.HasLastSeen {
		t.Fatalf("connected node projection: %+v", node)
	}
	if node.Protocol != 1 || node.AgentVersion != "agent-v0.0.0-test" || len(node.Capabilities) != 1 || node.Capabilities[0] != "files.v1" {
		t.Fatalf("runtime projection: %+v", node)
	}
	if node.Permissions.OS != "linux" || node.Permissions.Architecture != "arm64" {
		t.Fatalf("permission projection: %+v", node.Permissions)
	}
	// A connected agent without rotation state reports no pending channel work.
	channel, pending, err := repository.AgentChannel(ctx, identity.AgentID)
	if err != nil || pending {
		t.Fatalf("connected agent without rotation must report no pending work: %+v pending=%v err=%v", channel, pending, err)
	}
	if channel.NodeID != identity.NodeID {
		t.Fatalf("channel identity: %+v", channel)
	}
}
