// Package agents owns Core-side Agent enrollment, device identity and leases.
package agents

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/CST-Cat/NodeDance/internal/core/audit"
	"github.com/CST-Cat/NodeDance/internal/core/auth"
	"github.com/CST-Cat/NodeDance/internal/protocol"
)

const EnrollmentLifetime = 10 * time.Minute

var (
	ErrEnrollmentRejected = errors.New("enrollment credential rejected")
	ErrUnauthorized       = errors.New("Agent credential rejected")
	ErrRevoked            = errors.New("Agent authorization was revoked")
	ErrConflict           = errors.New("Agent state conflict")
	ErrStaleConnection    = errors.New("Agent connection generation is stale")
	ErrNotFound           = errors.New("Agent or node not found")
)

type Repository struct {
	db  *sql.DB
	now func() time.Time
}

type Enrollment struct {
	NodeID      string
	DisplayName string
	Token       string
	ExpiresAt   time.Time
}

type Identity struct {
	AgentID              string
	NodeID               string
	DisplayName          string
	Status               string
	CredentialIsPending  bool
	CredentialRotationID string
	RotationRequestedID  string
	ConnectionGeneration uint64
}

type Node struct {
	Identity
	LastSeen     time.Time
	Protocol     int
	AgentVersion string
	Capabilities []string
	Permissions  protocol.RuntimePermissions
	HasLastSeen  bool
}

type Lease struct {
	Identity
	RotationCommitted bool
	LastSeenAt        time.Time
}

type StaleLease struct {
	AgentID    string
	NodeID     string
	Generation uint64
}

func NewRepository(db *sql.DB, now func() time.Time) *Repository {
	if now == nil {
		now = time.Now
	}
	return &Repository{db: db, now: now}
}

// CreateEnrollment creates the pending node and its one-use credential in the
// same transaction. Only the digest is persisted; the raw token is returned
// once to the authenticated administrator.
func (r *Repository) CreateEnrollment(ctx context.Context, displayName, remote string, actor sql.NullInt64) (Enrollment, error) {
	name := strings.TrimSpace(displayName)
	if name == "" || len([]rune(name)) > 80 || !validText(name) {
		return Enrollment{}, errors.New("invalid node display name")
	}
	token, digest, err := newSecret()
	if err != nil {
		return Enrollment{}, err
	}
	nodeID, err := newID()
	if err != nil {
		return Enrollment{}, err
	}
	now := r.now()
	expires := now.Add(EnrollmentLifetime)
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return Enrollment{}, fmt.Errorf("begin enrollment transaction: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT INTO nodes(id, display_name, status, created_at, updated_at)
		VALUES(?, ?, 'pending', ?, ?)`, nodeID, name, now.UnixNano(), now.UnixNano()); err != nil {
		return Enrollment{}, fmt.Errorf("create pending node: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO agent_enrollments(token_digest, node_id, expires_at, created_at)
		VALUES(?, ?, ?, ?)`, digest, nodeID, expires.Unix(), now.Unix()); err != nil {
		return Enrollment{}, fmt.Errorf("create enrollment credential: %w", err)
	}
	if err := audit.Record(ctx, tx, audit.Event{OccurredAt: now, Action: "agent_enrollment_create", Outcome: "succeeded", ActorID: actor, RemoteAddr: remote, Target: audit.Target{Kind: audit.TargetNode, ID: nodeID}}); err != nil {
		return Enrollment{}, fmt.Errorf("audit enrollment creation: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Enrollment{}, fmt.Errorf("commit enrollment: %w", err)
	}
	return Enrollment{NodeID: nodeID, DisplayName: name, Token: token, ExpiresAt: expires}, nil
}

// ConsumeEnrollment is a one-time operation. The Agent already generated and
// durably saved its own device credential before calling this method, so a
// lost HTTP response is recovered through IdentityByCredential without
// consuming the enrollment token again.
func (r *Repository) ConsumeEnrollment(ctx context.Context, enrollmentDigest, deviceDigest []byte, requestID, remote string) (Identity, error) {
	if len(enrollmentDigest) != 32 || len(deviceDigest) != 32 || strings.TrimSpace(requestID) == "" || len(requestID) > 80 {
		return Identity{}, ErrEnrollmentRejected
	}
	now := r.now()
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return Identity{}, fmt.Errorf("begin enrollment consumption: %w", err)
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE agent_enrollments
		SET consumed_at=?, request_id=?
		WHERE token_digest=? AND consumed_at IS NULL AND expires_at>?`, now.Unix(), requestID, enrollmentDigest, now.Unix())
	if err != nil {
		return Identity{}, fmt.Errorf("consume enrollment credential: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return Identity{}, fmt.Errorf("inspect enrollment consumption: %w", err)
	}
	if changed != 1 {
		return Identity{}, ErrEnrollmentRejected
	}
	var nodeID, name string
	if err := tx.QueryRowContext(ctx, `SELECT e.node_id, n.display_name FROM agent_enrollments e
		JOIN nodes n ON n.id=e.node_id WHERE e.token_digest=?`, enrollmentDigest).Scan(&nodeID, &name); err != nil {
		return Identity{}, fmt.Errorf("load enrolled node: %w", err)
	}
	var collision int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM agent_credential_verifiers WHERE digest=?`, deviceDigest).Scan(&collision); err != nil {
		return Identity{}, fmt.Errorf("check Agent credential namespace: %w", err)
	}
	if collision != 0 {
		return Identity{}, ErrEnrollmentRejected
	}
	agentID, err := newID()
	if err != nil {
		return Identity{}, fmt.Errorf("create Agent identity: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO agent_devices(id, node_id, credential_digest, created_at)
		VALUES(?, ?, ?, ?)`, agentID, nodeID, deviceDigest, now.UnixNano()); err != nil {
		return Identity{}, fmt.Errorf("create Agent device identity: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO agent_credential_verifiers(digest, agent_id, state) VALUES(?, ?, 'active')`, deviceDigest, agentID); err != nil {
		return Identity{}, ErrEnrollmentRejected
	}
	if _, err := tx.ExecContext(ctx, `UPDATE nodes SET status='offline', updated_at=? WHERE id=? AND status='pending'`, now.UnixNano(), nodeID); err != nil {
		return Identity{}, fmt.Errorf("finish node enrollment: %w", err)
	}
	if err := audit.Record(ctx, tx, audit.Event{OccurredAt: now, Action: "agent_enrollment_consume", Outcome: "succeeded", ActorID: sql.NullInt64{}, RemoteAddr: remote, Target: audit.Target{Kind: audit.TargetAgent, ID: agentID}}); err != nil {
		return Identity{}, fmt.Errorf("audit enrollment: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Identity{}, fmt.Errorf("commit Agent identity: %w", err)
	}
	return Identity{AgentID: agentID, NodeID: nodeID, DisplayName: name, Status: "offline"}, nil
}

// IdentityByCredential is intentionally read-only and gives an enrolled
// device a way to recover the Core-assigned identity after a lost enrollment
// response. A bearer for any other device returns no identity.
func (r *Repository) IdentityByCredential(ctx context.Context, digest []byte) (Identity, error) {
	return r.lookupCredential(ctx, digest)
}

func (r *Repository) lookupCredential(ctx context.Context, digest []byte) (Identity, error) {
	if len(digest) != 32 {
		return Identity{}, ErrUnauthorized
	}
	var identity Identity
	var current, pending []byte
	var pendingRotation, requested sql.NullString
	var revoked sql.NullInt64
	var credentialState string
	err := r.db.QueryRowContext(ctx, `SELECT d.id, d.node_id, n.display_name, n.status,
		d.credential_digest, d.pending_credential_digest, d.pending_rotation_id,
		d.rotation_requested_id, d.revoked_at, n.connection_generation, v.state
		FROM agent_credential_verifiers v JOIN agent_devices d ON d.id=v.agent_id
		JOIN nodes n ON n.id=d.node_id WHERE v.digest=?`, digest).
		Scan(&identity.AgentID, &identity.NodeID, &identity.DisplayName, &identity.Status,
			&current, &pending, &pendingRotation, &requested, &revoked, &identity.ConnectionGeneration, &credentialState)
	if errors.Is(err, sql.ErrNoRows) {
		return Identity{}, ErrUnauthorized
	}
	if err != nil {
		return Identity{}, fmt.Errorf("lookup Agent credential: %w", err)
	}
	if revoked.Valid || credentialState == "active" && subtle.ConstantTimeCompare(digest, current) != 1 || credentialState == "pending" && (len(pending) == 0 || subtle.ConstantTimeCompare(digest, pending) != 1) {
		return Identity{}, ErrUnauthorized
	}
	identity.CredentialIsPending = credentialState == "pending"
	if pendingRotation.Valid {
		identity.CredentialRotationID = pendingRotation.String
	}
	if requested.Valid {
		identity.RotationRequestedID = requested.String
	}
	return identity, nil
}

// BeginConnection durably advances the per-node generation before accepting
// any traffic. Using a pending rotation credential commits the rotation in the
// same transaction as the new lease and invalidates the old credential.
func (r *Repository) BeginConnection(ctx context.Context, authenticated Identity, presentedDigest []byte, protocolVersion int, agentVersion string, capabilities string, permissions protocol.RuntimePermissions) (Lease, error) {
	if len(presentedDigest) != 32 || protocolVersion < 1 || agentVersion == "" || len(agentVersion) > 80 || !validRuntimePermissions(permissions) {
		return Lease{}, ErrUnauthorized
	}
	permissionsJSON, err := json.Marshal(permissions.SupplementaryGroups)
	if err != nil {
		return Lease{}, fmt.Errorf("encode Agent runtime groups: %w", err)
	}
	now := r.now()
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return Lease{}, fmt.Errorf("begin Agent lease: %w", err)
	}
	defer tx.Rollback()
	var current, pending []byte
	var revoked sql.NullInt64
	var identity Identity
	var rotationRequested sql.NullString
	err = tx.QueryRowContext(ctx, `SELECT d.credential_digest, d.pending_credential_digest, d.revoked_at,
		d.id, d.node_id, n.display_name, n.connection_generation, d.rotation_requested_id
		FROM agent_devices d JOIN nodes n ON n.id=d.node_id WHERE d.id=?`, authenticated.AgentID).
		Scan(&current, &pending, &revoked, &identity.AgentID, &identity.NodeID, &identity.DisplayName,
			&identity.ConnectionGeneration, &rotationRequested)
	if errors.Is(err, sql.ErrNoRows) || revoked.Valid {
		return Lease{}, ErrRevoked
	}
	if err != nil {
		return Lease{}, fmt.Errorf("reload Agent lease: %w", err)
	}
	rotationCommitted := len(pending) == 32 && subtle.ConstantTimeCompare(presentedDigest, pending) == 1
	if !rotationCommitted && subtle.ConstantTimeCompare(presentedDigest, current) != 1 {
		return Lease{}, ErrUnauthorized
	}
	if rotationCommitted {
		if _, err := tx.ExecContext(ctx, `DELETE FROM agent_credential_verifiers WHERE agent_id=? AND state='active' AND digest=?`, authenticated.AgentID, current); err != nil {
			return Lease{}, fmt.Errorf("retire old Agent credential verifier: %w", err)
		}
		result, err := tx.ExecContext(ctx, `UPDATE agent_credential_verifiers SET state='active'
			WHERE agent_id=? AND state='pending' AND digest=?`, authenticated.AgentID, presentedDigest)
		if err != nil {
			return Lease{}, fmt.Errorf("promote pending Agent credential verifier: %w", err)
		}
		changed, _ := result.RowsAffected()
		if changed != 1 {
			return Lease{}, ErrConflict
		}
		if _, err := tx.ExecContext(ctx, `UPDATE agent_devices SET credential_digest=pending_credential_digest,
			pending_credential_digest=NULL, pending_rotation_id=NULL, rotation_requested_id=NULL
			WHERE id=? AND revoked_at IS NULL AND pending_credential_digest=?`, authenticated.AgentID, presentedDigest); err != nil {
			return Lease{}, fmt.Errorf("commit Agent credential rotation: %w", err)
		}
		if err := audit.Record(ctx, tx, audit.Event{OccurredAt: now, Action: "agent_rotation_commit", Outcome: "succeeded", ActorID: sql.NullInt64{}, RemoteAddr: "unknown", Target: audit.Target{Kind: audit.TargetAgent, ID: identity.AgentID}}); err != nil {
			return Lease{}, fmt.Errorf("audit Agent rotation: %w", err)
		}
	}
	result, err := tx.ExecContext(ctx, `UPDATE nodes SET status='online', connection_generation=connection_generation+1,
		last_seen_at=?, heartbeat_sequence=0, protocol_version=?, agent_version=?, capabilities_json=?, runtime_os=?,
		runtime_architecture=?, effective_uid=?, effective_gid=?, supplementary_groups_json=?, updated_at=?
		WHERE id=? AND status!='revoked'`, now.UnixNano(), protocolVersion, agentVersion, capabilities,
		permissions.OS, permissions.Architecture, permissions.EffectiveUID, permissions.EffectiveGID, string(permissionsJSON), now.UnixNano(), identity.NodeID)
	if err != nil {
		return Lease{}, fmt.Errorf("advance Agent connection generation: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil || changed != 1 {
		return Lease{}, ErrRevoked
	}
	if err := tx.QueryRowContext(ctx, `SELECT connection_generation FROM nodes WHERE id=?`, identity.NodeID).Scan(&identity.ConnectionGeneration); err != nil {
		return Lease{}, fmt.Errorf("read Agent connection generation: %w", err)
	}
	identity.Status = "online"
	if rotationRequested.Valid {
		identity.RotationRequestedID = rotationRequested.String
	}
	if rotationCommitted {
		identity.RotationRequestedID = ""
	}
	if err := tx.Commit(); err != nil {
		return Lease{}, fmt.Errorf("commit Agent lease: %w", err)
	}
	return Lease{Identity: identity, RotationCommitted: rotationCommitted, LastSeenAt: now}, nil
}

func (r *Repository) AcceptHeartbeat(ctx context.Context, identity Identity, generation, sequence uint64, offlineAfter time.Duration) (time.Time, error) {
	if generation == 0 || sequence == 0 {
		return time.Time{}, ErrStaleConnection
	}
	now := r.now()
	cutoff := now.Add(-offlineAfter).UnixNano()
	result, err := r.db.ExecContext(ctx, `UPDATE nodes SET heartbeat_sequence=?, last_seen_at=?, updated_at=?
		WHERE id=? AND connection_generation=? AND status='online' AND heartbeat_sequence<? AND last_seen_at>?`,
		sequence, now.UnixNano(), now.UnixNano(), identity.NodeID, generation, sequence, cutoff)
	if err != nil {
		return time.Time{}, fmt.Errorf("record Agent heartbeat: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil || changed != 1 {
		return time.Time{}, ErrStaleConnection
	}
	return now, nil
}

// ExpireLease is the exact-deadline path used by the per-connection timer.
// A racing heartbeat can refresh last_seen_at first, in which case this CAS
// leaves the lease online; a heartbeat arriving after the deadline cannot
// refresh it because AcceptHeartbeat applies the same cutoff.
func (r *Repository) ExpireLease(ctx context.Context, nodeID string, generation uint64, offlineAfter time.Duration) (bool, error) {
	if generation == 0 || offlineAfter <= 0 {
		return false, ErrStaleConnection
	}
	now := r.now()
	cutoff := now.Add(-offlineAfter).UnixNano()
	result, err := r.db.ExecContext(ctx, `UPDATE nodes SET status='offline', connection_generation=connection_generation+1, updated_at=?
		WHERE id=? AND connection_generation=? AND status='online' AND last_seen_at<=?`, now.UnixNano(), nodeID, generation, cutoff)
	if err != nil {
		return false, fmt.Errorf("expire Agent connection lease: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("inspect expired Agent lease: %w", err)
	}
	return changed == 1, nil
}

func (r *Repository) ActiveLeaseLastSeen(ctx context.Context, nodeID string, generation uint64) (time.Time, bool, error) {
	var lastSeen int64
	err := r.db.QueryRowContext(ctx, `SELECT last_seen_at FROM nodes WHERE id=? AND connection_generation=? AND status='online'`, nodeID, generation).Scan(&lastSeen)
	if errors.Is(err, sql.ErrNoRows) {
		return time.Time{}, false, nil
	}
	if err != nil {
		return time.Time{}, false, fmt.Errorf("read active Agent lease deadline: %w", err)
	}
	return time.Unix(0, lastSeen).UTC(), true, nil
}

// MarkExpiredLeases invalidates each expired generation before returning it to
// the WebSocket manager for closure. A frame already queued by the old reader
// then fails its SQL generation comparison even if socket closure races it.
func (r *Repository) MarkExpiredLeases(ctx context.Context, offlineAfter time.Duration) ([]StaleLease, error) {
	cutoff := r.now().Add(-offlineAfter).UnixNano()
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin offline Agent sweep: %w", err)
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT d.id, n.id, n.connection_generation
		FROM nodes n JOIN agent_devices d ON d.node_id=n.id
		WHERE n.status='online' AND n.last_seen_at<=?`, cutoff)
	if err != nil {
		return nil, fmt.Errorf("find offline Agents: %w", err)
	}
	type expired struct {
		lease StaleLease
		stale bool
	}
	var found []expired
	for rows.Next() {
		var item expired
		if err := rows.Scan(&item.lease.AgentID, &item.lease.NodeID, &item.lease.Generation); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("read offline Agent: %w", err)
		}
		found = append(found, item)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("finish offline Agent scan: %w", err)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read offline Agent scan: %w", err)
	}
	for index := range found {
		item := &found[index]
		result, err := tx.ExecContext(ctx, `UPDATE nodes SET status='offline', connection_generation=connection_generation+1,
			updated_at=? WHERE id=? AND status='online' AND connection_generation=? AND last_seen_at<=?`,
			r.now().UnixNano(), item.lease.NodeID, item.lease.Generation, cutoff)
		if err != nil {
			return nil, fmt.Errorf("expire Agent lease: %w", err)
		}
		changed, _ := result.RowsAffected()
		if changed == 1 {
			item.stale = true
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit offline Agent sweep: %w", err)
	}
	leases := make([]StaleLease, 0, len(found))
	for _, item := range found {
		if item.stale {
			leases = append(leases, item.lease)
		}
	}
	return leases, nil
}

func (r *Repository) MarkAllOffline(ctx context.Context) error {
	_, err := r.db.ExecContext(ctx, `UPDATE nodes SET status='offline', connection_generation=connection_generation+1,
		updated_at=? WHERE status='online'`, r.now().UnixNano())
	return err
}

func (r *Repository) Revoke(ctx context.Context, agentID, remote string, actor sql.NullInt64) (Identity, error) {
	now := r.now()
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return Identity{}, fmt.Errorf("begin Agent revocation: %w", err)
	}
	defer tx.Rollback()
	var identity Identity
	var revoked sql.NullInt64
	err = tx.QueryRowContext(ctx, `SELECT d.id, d.node_id, n.display_name, n.status, n.connection_generation, d.revoked_at
		FROM agent_devices d JOIN nodes n ON n.id=d.node_id WHERE d.id=?`, agentID).
		Scan(&identity.AgentID, &identity.NodeID, &identity.DisplayName, &identity.Status, &identity.ConnectionGeneration, &revoked)
	if errors.Is(err, sql.ErrNoRows) {
		return Identity{}, ErrNotFound
	}
	if err != nil {
		return Identity{}, fmt.Errorf("load Agent for revocation: %w", err)
	}
	if revoked.Valid {
		return Identity{}, ErrRevoked
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM agent_credential_verifiers WHERE agent_id=?`, agentID); err != nil {
		return Identity{}, fmt.Errorf("remove revoked Agent credential verifiers: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE agent_devices SET revoked_at=?, pending_credential_digest=NULL,
		pending_rotation_id=NULL, rotation_requested_id=NULL WHERE id=? AND revoked_at IS NULL`, now.UnixNano(), agentID); err != nil {
		return Identity{}, fmt.Errorf("revoke Agent credential: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE nodes SET status='revoked', connection_generation=connection_generation+1, updated_at=? WHERE id=?`, now.UnixNano(), identity.NodeID); err != nil {
		return Identity{}, fmt.Errorf("invalidate Agent connection: %w", err)
	}
	if err := audit.Record(ctx, tx, audit.Event{OccurredAt: now, Action: "agent_revoke", Outcome: "succeeded", ActorID: actor, RemoteAddr: remote, Target: audit.Target{Kind: audit.TargetAgent, ID: agentID}}); err != nil {
		return Identity{}, fmt.Errorf("audit Agent revocation: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Identity{}, fmt.Errorf("commit Agent revocation: %w", err)
	}
	identity.Status = "revoked"
	identity.ConnectionGeneration++
	return identity, nil
}

func (r *Repository) RequestRotation(ctx context.Context, agentID, remote string, actor sql.NullInt64) (Identity, error) {
	now := r.now()
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return Identity{}, fmt.Errorf("begin Agent rotation request: %w", err)
	}
	defer tx.Rollback()
	var identity Identity
	var revoked sql.NullInt64
	var requested, pending sql.NullString
	err = tx.QueryRowContext(ctx, `SELECT d.id, d.node_id, n.display_name, n.status, n.connection_generation,
		d.revoked_at, d.rotation_requested_id, d.pending_rotation_id
		FROM agent_devices d JOIN nodes n ON n.id=d.node_id WHERE d.id=?`, agentID).
		Scan(&identity.AgentID, &identity.NodeID, &identity.DisplayName, &identity.Status,
			&identity.ConnectionGeneration, &revoked, &requested, &pending)
	if errors.Is(err, sql.ErrNoRows) {
		return Identity{}, ErrNotFound
	}
	if err != nil {
		return Identity{}, fmt.Errorf("load Agent for rotation: %w", err)
	}
	if revoked.Valid || identity.Status == "revoked" {
		return Identity{}, ErrRevoked
	}
	if pending.Valid {
		identity.CredentialRotationID = pending.String
	} else if requested.Valid {
		identity.RotationRequestedID = requested.String
	} else {
		rotationID, err := newID()
		if err != nil {
			return Identity{}, err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE agent_devices SET rotation_requested_id=? WHERE id=? AND revoked_at IS NULL AND rotation_requested_id IS NULL AND pending_rotation_id IS NULL`, rotationID, agentID); err != nil {
			return Identity{}, fmt.Errorf("record Agent rotation request: %w", err)
		}
		identity.RotationRequestedID = rotationID
		if err := audit.Record(ctx, tx, audit.Event{OccurredAt: now, Action: "agent_rotation_request", Outcome: "succeeded", ActorID: actor, RemoteAddr: remote, Target: audit.Target{Kind: audit.TargetAgent, ID: agentID}}); err != nil {
			return Identity{}, fmt.Errorf("audit Agent rotation request: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return Identity{}, fmt.Errorf("commit Agent rotation request: %w", err)
	}
	return identity, nil
}

func (r *Repository) PrepareRotation(ctx context.Context, authenticated Identity, generation uint64, rotationID string, digest []byte) error {
	if len(digest) != 32 || rotationID == "" || len(rotationID) > 80 {
		return ErrConflict
	}
	now := r.now()
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin credential rotation: %w", err)
	}
	defer tx.Rollback()
	var current, pending []byte
	var requested, existingRotation sql.NullString
	var revoked sql.NullInt64
	var currentGeneration uint64
	var nodeStatus string
	err = tx.QueryRowContext(ctx, `SELECT n.connection_generation, n.status FROM nodes n WHERE n.id=?`, authenticated.NodeID).
		Scan(&currentGeneration, &nodeStatus)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrStaleConnection
	}
	if err != nil {
		return fmt.Errorf("read generation during rotation: %w", err)
	}
	if currentGeneration != generation || nodeStatus != "online" {
		return ErrStaleConnection
	}
	err = tx.QueryRowContext(ctx, `SELECT credential_digest, pending_credential_digest, rotation_requested_id,
		pending_rotation_id, revoked_at FROM agent_devices WHERE id=?`, authenticated.AgentID).
		Scan(&current, &pending, &requested, &existingRotation, &revoked)
	if errors.Is(err, sql.ErrNoRows) || revoked.Valid {
		return ErrRevoked
	}
	if err != nil {
		return fmt.Errorf("read rotation state: %w", err)
	}
	if subtle.ConstantTimeCompare(current, digest) == 1 {
		return ErrConflict
	}
	if len(pending) == 32 {
		if existingRotation.Valid && existingRotation.String == rotationID && subtle.ConstantTimeCompare(pending, digest) == 1 {
			return tx.Commit()
		}
		return ErrConflict
	}
	if !requested.Valid || requested.String != rotationID {
		return ErrConflict
	}
	var collision int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM agent_credential_verifiers WHERE digest=?`, digest).Scan(&collision); err != nil {
		return fmt.Errorf("check Agent credential namespace: %w", err)
	}
	if collision != 0 {
		return ErrConflict
	}
	inserted, err := tx.ExecContext(ctx, `INSERT INTO agent_credential_verifiers(digest, agent_id, state)
		VALUES(?, ?, 'pending') ON CONFLICT DO NOTHING`, digest, authenticated.AgentID)
	if err != nil {
		return fmt.Errorf("reserve pending Agent credential verifier: %w", err)
	}
	rowsInserted, err := inserted.RowsAffected()
	if err != nil {
		return fmt.Errorf("inspect pending Agent credential verifier: %w", err)
	}
	if rowsInserted != 1 {
		return ErrConflict
	}
	result, err := tx.ExecContext(ctx, `UPDATE agent_devices SET pending_credential_digest=?, pending_rotation_id=?
		WHERE id=? AND revoked_at IS NULL AND rotation_requested_id=? AND pending_rotation_id IS NULL`,
		digest, rotationID, authenticated.AgentID, rotationID)
	if err != nil {
		return fmt.Errorf("persist pending Agent credential: %w", err)
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return ErrConflict
	}
	if err := audit.Record(ctx, tx, audit.Event{OccurredAt: now, Action: "agent_rotation_prepare", Outcome: "succeeded", ActorID: sql.NullInt64{}, RemoteAddr: "unknown", Target: audit.Target{Kind: audit.TargetAgent, ID: authenticated.AgentID}}); err != nil {
		return fmt.Errorf("audit Agent credential prepare: %w", err)
	}
	return tx.Commit()
}

func (r *Repository) ListNodes(ctx context.Context) ([]Node, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT n.id, n.display_name, n.status, n.connection_generation,
		n.last_seen_at, n.protocol_version, n.agent_version, n.capabilities_json, COALESCE(d.id,''),
		n.runtime_os, n.runtime_architecture, n.effective_uid, n.effective_gid, n.supplementary_groups_json
		FROM nodes n LEFT JOIN agent_devices d ON d.node_id=n.id ORDER BY n.created_at, n.id`)
	if err != nil {
		return nil, fmt.Errorf("list managed nodes: %w", err)
	}
	defer rows.Close()
	result := []Node{}
	for rows.Next() {
		var node Node
		var lastSeen, protocolVersion, effectiveUID, effectiveGID sql.NullInt64
		var capabilities, groups string
		var osName, architecture sql.NullString
		var agentVersion sql.NullString
		if err := rows.Scan(&node.NodeID, &node.DisplayName, &node.Status, &node.ConnectionGeneration,
			&lastSeen, &protocolVersion, &agentVersion, &capabilities, &node.AgentID,
			&osName, &architecture, &effectiveUID, &effectiveGID, &groups); err != nil {
			return nil, fmt.Errorf("read managed node: %w", err)
		}
		if lastSeen.Valid {
			node.LastSeen = time.Unix(0, lastSeen.Int64).UTC()
			node.HasLastSeen = true
		}
		if protocolVersion.Valid {
			node.Protocol = int(protocolVersion.Int64)
		}
		if agentVersion.Valid {
			node.AgentVersion = agentVersion.String
		}
		node.Capabilities = decodeCapabilities(capabilities)
		if osName.Valid {
			node.Permissions.OS = osName.String
		}
		if architecture.Valid {
			node.Permissions.Architecture = architecture.String
		}
		if effectiveUID.Valid {
			node.Permissions.EffectiveUID = int(effectiveUID.Int64)
		}
		if effectiveGID.Valid {
			node.Permissions.EffectiveGID = int(effectiveGID.Int64)
		}
		if err := json.Unmarshal([]byte(groups), &node.Permissions.SupplementaryGroups); err != nil {
			node.Permissions.SupplementaryGroups = []int{}
		}
		result = append(result, node)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("finish managed node list: %w", err)
	}
	return result, nil
}

func (r *Repository) AgentChannel(ctx context.Context, agentID string) (Identity, bool, error) {
	var identity Identity
	var digest []byte
	var requested, pending sql.NullString
	var revoked sql.NullInt64
	err := r.db.QueryRowContext(ctx, `SELECT d.id, d.node_id, n.display_name, n.status,
		d.credential_digest, d.rotation_requested_id, d.pending_rotation_id, d.revoked_at,
		n.connection_generation FROM agent_devices d JOIN nodes n ON n.id=d.node_id WHERE d.id=?`, agentID).
		Scan(&identity.AgentID, &identity.NodeID, &identity.DisplayName, &identity.Status, &digest,
			&requested, &pending, &revoked, &identity.ConnectionGeneration)
	if errors.Is(err, sql.ErrNoRows) {
		return Identity{}, false, ErrNotFound
	}
	if err != nil {
		return Identity{}, false, err
	}
	if revoked.Valid {
		return Identity{}, false, ErrRevoked
	}
	identity.RotationRequestedID = requested.String
	identity.CredentialRotationID = pending.String
	return identity, requested.Valid || pending.Valid, nil
}

func (r *Repository) FindLease(ctx context.Context, agentID string) (Identity, error) {
	var identity Identity
	err := r.db.QueryRowContext(ctx, `SELECT d.id, d.node_id, n.display_name, n.status, n.connection_generation
		FROM agent_devices d JOIN nodes n ON n.id=d.node_id WHERE d.id=? AND d.revoked_at IS NULL`, agentID).
		Scan(&identity.AgentID, &identity.NodeID, &identity.DisplayName, &identity.Status, &identity.ConnectionGeneration)
	if errors.Is(err, sql.ErrNoRows) {
		return Identity{}, ErrNotFound
	}
	return identity, err
}

func newSecret() (string, []byte, error) {
	buffer := make([]byte, 32)
	if _, err := rand.Read(buffer); err != nil {
		return "", nil, err
	}
	value := hex.EncodeToString(buffer)
	return value, digestSecret(value), nil
}

func digestSecret(value string) []byte {
	// High-entropy credentials are verifier secrets; SHA-256 is appropriate for
	// these random 256-bit values and avoids storing a recoverable bearer.
	return auth.DigestToken(value)
}

func newID() (string, error) {
	buffer := make([]byte, 16)
	if _, err := rand.Read(buffer); err != nil {
		return "", err
	}
	buffer[6] = buffer[6]&0x0f | 0x40
	buffer[8] = buffer[8]&0x3f | 0x80
	encoded := hex.EncodeToString(buffer)
	return encoded[:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:], nil
}

func validText(value string) bool {
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

func decodeCapabilities(encoded string) []string {
	// Capabilities are validated before storage. This local helper intentionally
	// returns an empty list if a future/corrupt value cannot be decoded.
	var result []string
	if err := json.Unmarshal([]byte(encoded), &result); err != nil {
		return []string{}
	}
	return result
}

func validRuntimePermissions(value protocol.RuntimePermissions) bool {
	if value.OS == "" || len(value.OS) > 32 || value.Architecture == "" || len(value.Architecture) > 32 || value.EffectiveUID < 0 || value.EffectiveGID < 0 || len(value.SupplementaryGroups) > 128 {
		return false
	}
	for _, group := range value.SupplementaryGroups {
		if group < 0 {
			return false
		}
	}
	return validText(value.OS) && validText(value.Architecture)
}
