package agents

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/CST-Cat/NodeDance/internal/core/auth"
	"github.com/CST-Cat/NodeDance/internal/core/storage"
	"github.com/CST-Cat/NodeDance/internal/protocol"
)

func testRepository(t *testing.T, now func() time.Time) (*Repository, *storage.Store) {
	t.Helper()
	store, err := storage.Open(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return NewRepository(store.DB, now), store
}

func TestEnrollmentIsBoundOneTimeAndRecoverableByDeviceCredential(t *testing.T) {
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	repo, store := testRepository(t, func() time.Time { return now })
	enrollment, err := repo.CreateEnrollment(context.Background(), "arm-node", "127.0.0.1", sql.NullInt64{Int64: 1, Valid: true})
	if err != nil {
		t.Fatal(err)
	}
	if enrollment.Token == "" || enrollment.ExpiresAt.Sub(now) != EnrollmentLifetime {
		t.Fatalf("enrollment=%+v", enrollment)
	}
	var rawMatch int
	if err := store.DB.QueryRow(`SELECT count(*) FROM agent_enrollments WHERE CAST(token_digest AS TEXT)=?`, enrollment.Token).Scan(&rawMatch); err != nil || rawMatch != 0 {
		t.Fatalf("raw enrollment token is present in database: count=%d err=%v", rawMatch, err)
	}
	deviceCredential := "pre-generated-secret-for-node-a"
	identity, err := repo.ConsumeEnrollment(context.Background(), auth.DigestToken(enrollment.Token), auth.DigestToken(deviceCredential), "attempt-node-a", "127.0.0.2")
	if err != nil {
		t.Fatal(err)
	}
	if identity.NodeID != enrollment.NodeID || identity.AgentID == "" || identity.Status != "offline" {
		t.Fatalf("identity did not use the token-bound node: %+v", identity)
	}
	recovered, err := repo.IdentityByCredential(context.Background(), auth.DigestToken(deviceCredential))
	if err != nil || recovered.AgentID != identity.AgentID || recovered.NodeID != identity.NodeID {
		t.Fatalf("device credential recovery returned %+v, %v", recovered, err)
	}
	if _, err := repo.IdentityByCredential(context.Background(), auth.DigestToken("wrong-device-secret")); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("wrong credential recovered identity: %v", err)
	}
	if _, err := repo.ConsumeEnrollment(context.Background(), auth.DigestToken(enrollment.Token), auth.DigestToken("second-device-secret"), "attempt-node-b", "127.0.0.3"); !errors.Is(err, ErrEnrollmentRejected) {
		t.Fatalf("consumed enrollment token accepted again: %v", err)
	}
	var nodes, devices int
	if err := store.DB.QueryRow(`SELECT count(*) FROM nodes`).Scan(&nodes); err != nil {
		t.Fatal(err)
	}
	if err := store.DB.QueryRow(`SELECT count(*) FROM agent_devices`).Scan(&devices); err != nil {
		t.Fatal(err)
	}
	if nodes != 1 || devices != 1 {
		t.Fatalf("replay created resources: nodes=%d devices=%d", nodes, devices)
	}
	var storedDigest []byte
	if err := store.DB.QueryRow(`SELECT credential_digest FROM agent_devices WHERE id=?`, identity.AgentID).Scan(&storedDigest); err != nil {
		t.Fatal(err)
	}
	if len(storedDigest) != 32 || string(storedDigest) == deviceCredential {
		t.Fatalf("device credential was not persisted as only a verifier: %x", storedDigest)
	}
}

func TestConcurrentEnrollmentTokenConsumptionCreatesExactlyOneDevice(t *testing.T) {
	repo, store := testRepository(t, time.Now)
	enrollment, err := repo.CreateEnrollment(context.Background(), "race-node", "127.0.0.1", sql.NullInt64{Int64: 1, Valid: true})
	if err != nil {
		t.Fatal(err)
	}
	const contenders = 24
	var successes atomic.Int32
	var wg sync.WaitGroup
	for index := 0; index < contenders; index++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			_, err := repo.ConsumeEnrollment(context.Background(), auth.DigestToken(enrollment.Token), auth.DigestToken(fmt.Sprintf("device-secret-%d", index)), fmt.Sprintf("request-%d", index), "127.0.0.2")
			if err == nil {
				successes.Add(1)
			} else if !errors.Is(err, ErrEnrollmentRejected) {
				t.Errorf("contender %d got unexpected error: %v", index, err)
			}
		}(index)
	}
	wg.Wait()
	var devices int
	if err := store.DB.QueryRow(`SELECT count(*) FROM agent_devices`).Scan(&devices); err != nil {
		t.Fatal(err)
	}
	if successes.Load() != 1 || devices != 1 {
		t.Fatalf("concurrent consumption succeeded %d times and created %d devices", successes.Load(), devices)
	}
}

func TestExpiredEnrollmentCredentialIsRejected(t *testing.T) {
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	repo, store := testRepository(t, func() time.Time { return now })
	enrollment, err := repo.CreateEnrollment(context.Background(), "expired-node", "127.0.0.1", sql.NullInt64{Int64: 1, Valid: true})
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(EnrollmentLifetime + time.Second)
	if _, err := repo.ConsumeEnrollment(context.Background(), auth.DigestToken(enrollment.Token), auth.DigestToken("device-secret"), "request-expired", "127.0.0.2"); !errors.Is(err, ErrEnrollmentRejected) {
		t.Fatalf("expired enrollment accepted: %v", err)
	}
	var consumed, devices int
	if err := store.DB.QueryRow(`SELECT consumed_at IS NOT NULL FROM agent_enrollments WHERE node_id=?`, enrollment.NodeID).Scan(&consumed); err != nil {
		t.Fatal(err)
	}
	if err := store.DB.QueryRow(`SELECT count(*) FROM agent_devices`).Scan(&devices); err != nil {
		t.Fatal(err)
	}
	if consumed != 0 || devices != 0 {
		t.Fatalf("expired enrollment mutated: consumed=%d devices=%d", consumed, devices)
	}
}

func TestPendingNodeCanBeListedBeforeAgentVersionExists(t *testing.T) {
	repo, _ := testRepository(t, time.Now)
	if _, err := repo.CreateEnrollment(context.Background(), "not-yet-enrolled", "127.0.0.1", sql.NullInt64{Int64: 1, Valid: true}); err != nil {
		t.Fatal(err)
	}
	nodes, err := repo.ListNodes(context.Background())
	if err != nil || len(nodes) != 1 || nodes[0].Status != "pending" || nodes[0].AgentID != "" || nodes[0].AgentVersion != "" {
		t.Fatalf("pending node could not be listed safely: %+v err=%v", nodes, err)
	}
}

func TestCredentialVerifierNamespaceRejectsCrossDeviceCollisions(t *testing.T) {
	ctx := context.Background()
	repo, store := testRepository(t, time.Now)
	first, err := repo.CreateEnrollment(ctx, "first", "127.0.0.1", sql.NullInt64{Int64: 1, Valid: true})
	if err != nil {
		t.Fatal(err)
	}
	firstCredential := "11111111111111111111111111111111"
	firstIdentity, err := repo.ConsumeEnrollment(ctx, auth.DigestToken(first.Token), auth.DigestToken(firstCredential), "first-request", "127.0.0.2")
	if err != nil {
		t.Fatal(err)
	}

	second, err := repo.CreateEnrollment(ctx, "second", "127.0.0.1", sql.NullInt64{Int64: 1, Valid: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.ConsumeEnrollment(ctx, auth.DigestToken(second.Token), auth.DigestToken(firstCredential), "second-colliding-request", "127.0.0.3"); !errors.Is(err, ErrEnrollmentRejected) {
		t.Fatalf("enrollment reused another device's active credential: %v", err)
	}
	var consumed int
	if err := store.DB.QueryRow(`SELECT consumed_at IS NOT NULL FROM agent_enrollments WHERE node_id=?`, second.NodeID).Scan(&consumed); err != nil || consumed != 0 {
		t.Fatalf("collision consumed the second enrollment token: consumed=%d err=%v", consumed, err)
	}

	secondCredential := "22222222222222222222222222222222"
	secondIdentity, err := repo.ConsumeEnrollment(ctx, auth.DigestToken(second.Token), auth.DigestToken(secondCredential), "second-request", "127.0.0.3")
	if err != nil {
		t.Fatal(err)
	}
	firstLease, err := repo.BeginConnection(ctx, firstIdentity, auth.DigestToken(firstCredential), 1, "test", `[]`, protocol.RuntimePermissions{OS: "linux", Architecture: "amd64"})
	if err != nil {
		t.Fatal(err)
	}
	firstRotation, err := repo.RequestRotation(ctx, firstIdentity.AgentID, "127.0.0.1", sql.NullInt64{Int64: 1, Valid: true})
	if err != nil {
		t.Fatal(err)
	}
	firstPending := "33333333333333333333333333333333"
	if err := repo.PrepareRotation(ctx, firstIdentity, firstLease.ConnectionGeneration, firstRotation.RotationRequestedID, auth.DigestToken(firstPending)); err != nil {
		t.Fatal(err)
	}

	secondLease, err := repo.BeginConnection(ctx, secondIdentity, auth.DigestToken(secondCredential), 1, "test", `[]`, protocol.RuntimePermissions{OS: "linux", Architecture: "amd64"})
	if err != nil {
		t.Fatal(err)
	}
	secondRotation, err := repo.RequestRotation(ctx, secondIdentity.AgentID, "127.0.0.1", sql.NullInt64{Int64: 1, Valid: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, collision := range []struct {
		label      string
		credential string
	}{{"other-device-active", firstCredential}, {"other-device-pending", firstPending}} {
		if err := repo.PrepareRotation(ctx, secondIdentity, secondLease.ConnectionGeneration, secondRotation.RotationRequestedID, auth.DigestToken(collision.credential)); !errors.Is(err, ErrConflict) {
			t.Errorf("rotation colliding with %s verifier returned %v", collision.label, err)
		}
	}
	var pending []byte
	if err := store.DB.QueryRow(`SELECT pending_credential_digest FROM agent_devices WHERE id=?`, secondIdentity.AgentID).Scan(&pending); err != nil || len(pending) != 0 {
		t.Fatalf("cross-device collision mutated second Agent pending state: %x err=%v", pending, err)
	}
	if _, err := repo.IdentityByCredential(ctx, auth.DigestToken(firstCredential)); err != nil {
		t.Fatalf("cross-device collision changed first active identity: %v", err)
	}
	if identity, err := repo.IdentityByCredential(ctx, auth.DigestToken(firstPending)); err != nil || !identity.CredentialIsPending || identity.AgentID != firstIdentity.AgentID {
		t.Fatalf("cross-device collision changed first pending identity: %+v err=%v", identity, err)
	}
}

func TestGenerationCASOfflineSweepAndRevocation(t *testing.T) {
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	repo, store := testRepository(t, func() time.Time { return now })
	enrollment, err := repo.CreateEnrollment(context.Background(), "generation-node", "127.0.0.1", sql.NullInt64{Int64: 1, Valid: true})
	if err != nil {
		t.Fatal(err)
	}
	credential := "generation-device-secret"
	identity, err := repo.ConsumeEnrollment(context.Background(), auth.DigestToken(enrollment.Token), auth.DigestToken(credential), "generation-request", "127.0.0.2")
	if err != nil {
		t.Fatal(err)
	}
	permissions := protocol.RuntimePermissions{OS: "linux", Architecture: "arm64", EffectiveUID: 1000, EffectiveGID: 1001, SupplementaryGroups: []int{4, 998}}
	lease1, err := repo.BeginConnection(context.Background(), identity, auth.DigestToken(credential), 1, "test", `["agent.heartbeat.v1"]`, permissions)
	if err != nil || lease1.ConnectionGeneration != 1 {
		t.Fatalf("first lease=%+v err=%v", lease1, err)
	}
	if _, err := repo.AcceptHeartbeat(context.Background(), identity, lease1.ConnectionGeneration, 1, 30*time.Second); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.AcceptHeartbeat(context.Background(), identity, lease1.ConnectionGeneration, 1, 30*time.Second); !errors.Is(err, ErrStaleConnection) {
		t.Fatalf("duplicate sequence refreshed liveness: %v", err)
	}
	lease2, err := repo.BeginConnection(context.Background(), identity, auth.DigestToken(credential), 1, "test", `["agent.heartbeat.v1"]`, permissions)
	if err != nil || lease2.ConnectionGeneration != 2 {
		t.Fatalf("second lease=%+v err=%v", lease2, err)
	}
	if _, err := repo.AcceptHeartbeat(context.Background(), identity, lease1.ConnectionGeneration, 2, 30*time.Second); !errors.Is(err, ErrStaleConnection) {
		t.Fatalf("old generation updated status: %v", err)
	}
	if _, err := repo.AcceptHeartbeat(context.Background(), identity, lease2.ConnectionGeneration, 1, 30*time.Second); err != nil {
		t.Fatal(err)
	}
	nodes, err := repo.ListNodes(context.Background())
	if err != nil || len(nodes) != 1 || nodes[0].Permissions.OS != "linux" || nodes[0].Permissions.Architecture != "arm64" || nodes[0].Permissions.EffectiveUID != 1000 || nodes[0].Permissions.EffectiveGID != 1001 || fmt.Sprint(nodes[0].Permissions.SupplementaryGroups) != "[4 998]" {
		t.Fatalf("runtime permission snapshot was not persisted: %+v err=%v", nodes, err)
	}
	now = now.Add(30 * time.Second)
	if _, err := repo.AcceptHeartbeat(context.Background(), identity, lease2.ConnectionGeneration, 2, 30*time.Second); !errors.Is(err, ErrStaleConnection) {
		t.Fatalf("heartbeat at the offline deadline refreshed the lease: %v", err)
	}
	expired, err := repo.ExpireLease(context.Background(), identity.NodeID, lease2.ConnectionGeneration, 30*time.Second)
	if err != nil || !expired {
		t.Fatalf("exact-deadline lease expiry=%v err=%v", expired, err)
	}
	if err := repo.MarkAllOffline(context.Background()); err != nil {
		t.Fatal(err)
	}
	var generation uint64
	if err := store.DB.QueryRow(`SELECT connection_generation FROM nodes WHERE id=?`, identity.NodeID).Scan(&generation); err != nil {
		t.Fatal(err)
	}
	if generation != 3 {
		t.Fatalf("offline startup changed an already-expired generation: %d", generation)
	}
	if _, err := repo.Revoke(context.Background(), identity.AgentID, "127.0.0.1", sql.NullInt64{Int64: 1, Valid: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.IdentityByCredential(context.Background(), auth.DigestToken(credential)); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("revoked credential still resolved: %v", err)
	}
	var targetKind, targetID string
	if err := store.DB.QueryRow(`SELECT target_kind, target_id FROM audit_entries WHERE action='agent_revoke'`).Scan(&targetKind, &targetID); err != nil {
		t.Fatal(err)
	}
	if targetKind != "agent" || targetID != identity.AgentID {
		t.Fatalf("revocation audit did not identify its target: %s/%s", targetKind, targetID)
	}
}

func TestRotationRequiresNewCredentialHandshakeAndSurvivesDatabaseReopen(t *testing.T) {
	ctx := context.Background()
	directory := t.TempDir()
	store, err := storage.Open(ctx, directory)
	if err != nil {
		t.Fatal(err)
	}
	repo := NewRepository(store.DB, time.Now)
	enrollment, err := repo.CreateEnrollment(ctx, "rotation-node", "127.0.0.1", sql.NullInt64{Int64: 1, Valid: true})
	if err != nil {
		t.Fatal(err)
	}
	oldCredential := "old-rotation-device-credential"
	identity, err := repo.ConsumeEnrollment(ctx, auth.DigestToken(enrollment.Token), auth.DigestToken(oldCredential), "rotation-request", "127.0.0.2")
	if err != nil {
		t.Fatal(err)
	}
	lease, err := repo.BeginConnection(ctx, identity, auth.DigestToken(oldCredential), 1, "test", `[]`, protocol.RuntimePermissions{OS: "linux", Architecture: "amd64"})
	if err != nil {
		t.Fatal(err)
	}
	rotation, err := repo.RequestRotation(ctx, identity.AgentID, "127.0.0.1", sql.NullInt64{Int64: 1, Valid: true})
	if err != nil || rotation.RotationRequestedID == "" {
		t.Fatalf("rotation request=%+v err=%v", rotation, err)
	}
	newCredential := "new-rotation-device-credential"
	newDigest := auth.DigestToken(newCredential)
	if err := repo.PrepareRotation(ctx, identity, lease.ConnectionGeneration, rotation.RotationRequestedID, newDigest); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = storage.Open(ctx, directory)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	repo = NewRepository(store.DB, time.Now)
	pendingIdentity, err := repo.IdentityByCredential(ctx, newDigest)
	if err != nil || !pendingIdentity.CredentialIsPending || pendingIdentity.AgentID != identity.AgentID {
		t.Fatalf("pending credential lost across Core restart: %+v err=%v", pendingIdentity, err)
	}
	if _, err := repo.IdentityByCredential(ctx, auth.DigestToken(oldCredential)); err != nil {
		t.Fatalf("old credential was revoked before new credential connected: %v", err)
	}
	newLease, err := repo.BeginConnection(ctx, identity, newDigest, 1, "test", `[]`, protocol.RuntimePermissions{OS: "linux", Architecture: "amd64"})
	if err != nil || !newLease.RotationCommitted {
		t.Fatalf("new credential did not commit rotation: %+v err=%v", newLease, err)
	}
	if _, err := repo.IdentityByCredential(ctx, auth.DigestToken(oldCredential)); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("old credential remained valid after new authenticated connection: %v", err)
	}
	if recovered, err := repo.IdentityByCredential(ctx, newDigest); err != nil || recovered.CredentialIsPending || recovered.AgentID != identity.AgentID {
		t.Fatalf("new active identity=%+v err=%v", recovered, err)
	}
}

func TestMigrationVersionTwoExtendsAuditWithTypedTargets(t *testing.T) {
	_, store := testRepository(t, time.Now)
	columns := []string{}
	rows, err := store.DB.Query(`PRAGMA table_info(audit_entries)`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var cid, notNull, primary int
		var name, dataType string
		var defaultValue sql.NullString
		if err := rows.Scan(&cid, &name, &dataType, &notNull, &defaultValue, &primary); err != nil {
			t.Fatal(err)
		}
		columns = append(columns, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	want := []string{"id", "occurred_at", "action", "outcome", "actor_id", "remote_addr", "target_kind", "target_id"}
	if fmt.Sprint(columns) != fmt.Sprint(want) {
		t.Fatalf("audit columns=%v want %v", columns, want)
	}
	var version int
	if err := store.DB.QueryRow(`SELECT max(version) FROM schema_migrations`).Scan(&version); err != nil || version < 3 {
		t.Fatalf("migration version=%d err=%v", version, err)
	}
}
