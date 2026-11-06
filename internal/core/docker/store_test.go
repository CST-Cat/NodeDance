package docker_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	coredocker "github.com/CST-Cat/NodeDance/internal/core/docker"
	"github.com/CST-Cat/NodeDance/internal/protocol"
)

var testNow = time.Unix(1730000000, 0)

func storeIdentity() coredocker.Identity {
	return coredocker.Identity{AgentID: "agent-1", NodeID: "node-1"}
}

func dockerContainer(idByte byte, sequence uint64) protocol.DockerChange {
	id := strings.Repeat(string(idByte), 64)
	return protocol.DockerChange{
		Sequence: sequence, Action: protocol.DockerChangeUpsert, ContainerID: id,
		Container: &protocol.DockerContainer{
			ID: id, Name: "web-" + string(idByte), Image: "nginx:1", ImageID: "sha256:" + strings.Repeat("a", 64),
			State: "running", Running: true, Health: protocol.DockerHealthNone, ObservedAt: testNow,
		},
		ObservedAt: testNow,
	}
}

func containerID(idByte byte) string {
	return strings.Repeat(string(idByte), 64)
}

func authoritativeHealth(sequence uint64) *protocol.DockerHealth {
	return &protocol.DockerHealth{Sequence: sequence, Availability: protocol.DockerAvailabilityAvailable,
		EventsConnected: true, SnapshotFresh: true, ObservedAt: testNow}
}

func activeLease(identity coredocker.Identity) *coredocker.Lease {
	return &coredocker.Lease{Identity: identity, Generation: 1, Status: coredocker.LeaseOnline, ValidUntil: testNow.Add(time.Hour)}
}

func TestDockerSnapshotSequenceMachine(t *testing.T) {
	store := coredocker.NewStore()
	identity := storeIdentity()
	valid := protocol.DockerBatch{Sequence: 1, FullSnapshot: true, SnapshotID: 7, SnapshotFinal: true,
		Health: authoritativeHealth(1), Changes: []protocol.DockerChange{dockerContainer('a', 1)}}
	if err := store.Accept(identity, 1, 1, valid, testNow); !errors.Is(err, coredocker.ErrConnectionUnknown) {
		t.Fatalf("accept before bind: %v", err)
	}
	if err := store.BindConnection(identity, 1); err != nil {
		t.Fatalf("bind connection: %v", err)
	}

	// One-chunk authoritative snapshot commits the whole inventory.
	if err := store.Accept(identity, 1, 1, valid, testNow); err != nil {
		t.Fatalf("accept first snapshot: %v", err)
	}
	view, ok := store.SnapshotAt(identity.NodeID, activeLease(identity), testNow)
	if !ok || len(view.Containers) != 1 || view.DockerAvailability != protocol.DockerAvailabilityAvailable || view.DataStale {
		t.Fatalf("snapshot view: ok=%v containers=%d availability=%v stale=%v", ok, len(view.Containers), view.DockerAvailability, view.DataStale)
	}

	// Replaying an envelope sequence is rejected even with valid content.
	if err := store.Accept(identity, 1, 1, valid, testNow); !errors.Is(err, coredocker.ErrStaleSequence) {
		t.Fatalf("replayed envelope: %v", err)
	}
	// A full snapshot from an older snapshot ID cannot replace the current one.
	regressed := protocol.DockerBatch{Sequence: 2, FullSnapshot: true, SnapshotID: 6, SnapshotFinal: true,
		Health: authoritativeHealth(2), Changes: []protocol.DockerChange{dockerContainer('b', 2)}}
	if err := store.Accept(identity, 1, 2, regressed, testNow); !errors.Is(err, coredocker.ErrInvalidSnapshot) {
		t.Fatalf("snapshot ID regression: %v", err)
	}

	// A multi-part snapshot must continue its own identity and sequence exactly.
	partOne := protocol.DockerBatch{Sequence: 3, FullSnapshot: true, SnapshotID: 8,
		Health: authoritativeHealth(3), Changes: []protocol.DockerChange{dockerContainer('c', 3)}}
	if err := store.Accept(identity, 1, 3, partOne, testNow); err != nil {
		t.Fatalf("accept snapshot part one: %v", err)
	}
	// An out-of-order part destroys the whole staged snapshot.
	mismatched := protocol.DockerBatch{Sequence: 3, FullSnapshot: true, SnapshotID: 8, SnapshotIndex: 2, SnapshotFinal: true,
		Changes: []protocol.DockerChange{dockerContainer('d', 3)}}
	if err := store.Accept(identity, 1, 4, mismatched, testNow); !errors.Is(err, coredocker.ErrInvalidSnapshot) {
		t.Fatalf("out-of-order snapshot part: %v", err)
	}
	if err := store.Accept(identity, 1, 5, partOne, testNow); err != nil {
		t.Fatalf("restage snapshot after an out-of-order part: %v", err)
	}
	partTwo := protocol.DockerBatch{Sequence: 3, FullSnapshot: true, SnapshotID: 8, SnapshotIndex: 1, SnapshotFinal: true,
		Changes: []protocol.DockerChange{dockerContainer('d', 3)}}
	if err := store.Accept(identity, 1, 6, partTwo, testNow); err != nil {
		t.Fatalf("accept snapshot part two: %v", err)
	}
	view, ok = store.SnapshotAt(identity.NodeID, activeLease(identity), testNow)
	if !ok || len(view.Containers) != 2 {
		t.Fatalf("multi-part snapshot view: ok=%v containers=%d", ok, len(view.Containers))
	}

	// A duplicate container across parts aborts the staged snapshot.
	if err := store.Accept(identity, 1, 7, protocol.DockerBatch{Sequence: 6, FullSnapshot: true, SnapshotID: 9,
		Health: authoritativeHealth(6), Changes: []protocol.DockerChange{dockerContainer('e', 6)}}, testNow); err != nil {
		t.Fatal(err)
	}
	duplicate := protocol.DockerBatch{Sequence: 6, FullSnapshot: true, SnapshotID: 9, SnapshotIndex: 1, SnapshotFinal: true,
		Changes: []protocol.DockerChange{dockerContainer('e', 6)}}
	if err := store.Accept(identity, 1, 8, duplicate, testNow); !errors.Is(err, coredocker.ErrInvalidSnapshot) {
		t.Fatalf("duplicate staged container: %v", err)
	}
	view, _ = store.SnapshotAt(identity.NodeID, activeLease(identity), testNow)
	if !ok || len(view.Containers) != 2 || !view.DataStale || view.StaleReason != "snapshot_duplicate_container" {
		t.Fatalf("aborted staging must retain the last snapshot and mark it questionable: ok=%v containers=%d stale=%v reason=%q",
			ok, len(view.Containers), view.DataStale, view.StaleReason)
	}

	// Incremental deletes apply on top of the snapshot.
	if err := store.Accept(identity, 1, 9, protocol.DockerBatch{Sequence: 8,
		Changes: []protocol.DockerChange{{Sequence: 8, Action: protocol.DockerChangeDelete, ContainerID: containerID('c'), ObservedAt: testNow}}}, testNow); err != nil {
		t.Fatalf("accept incremental delete: %v", err)
	}
	view, _ = store.SnapshotAt(identity.NodeID, activeLease(identity), testNow)
	if len(view.Containers) != 1 || view.Containers[0].Container.ID != containerID('d') {
		t.Fatalf("incremental delete did not apply: %d containers", len(view.Containers))
	}
}

func TestDockerSnapshotItemLimitAbortsStaging(t *testing.T) {
	store := coredocker.NewStoreWithOptions(coredocker.Options{MaxSnapshotItems: 1})
	identity := storeIdentity()
	if err := store.BindConnection(identity, 1); err != nil {
		t.Fatal(err)
	}
	oversized := protocol.DockerBatch{Sequence: 1, FullSnapshot: true, SnapshotID: 1, SnapshotFinal: true,
		Health: authoritativeHealth(1), Changes: []protocol.DockerChange{dockerContainer('a', 1), dockerContainer('b', 1)}}
	if err := store.Accept(identity, 1, 1, oversized, testNow); !errors.Is(err, coredocker.ErrSnapshotLimit) {
		t.Fatalf("item limit: %v", err)
	}
	view, ok := store.SnapshotAt(identity.NodeID, activeLease(identity), testNow)
	if !ok || len(view.Containers) != 0 || !view.DataStale {
		t.Fatalf("aborted staging must expose no inventory: ok=%v containers=%d stale=%v", ok, len(view.Containers), view.DataStale)
	}
	// A valid single-container snapshot still lands afterwards.
	if err := store.Accept(identity, 1, 2, protocol.DockerBatch{Sequence: 2, FullSnapshot: true, SnapshotID: 2, SnapshotFinal: true,
		Health: authoritativeHealth(2), Changes: []protocol.DockerChange{dockerContainer('a', 2)}}, testNow); err != nil {
		t.Fatalf("snapshot after abort: %v", err)
	}
	if view, ok = store.SnapshotAt(identity.NodeID, activeLease(identity), testNow); !ok || len(view.Containers) != 1 {
		t.Fatalf("recovered snapshot missing: ok=%v containers=%d", ok, len(view.Containers))
	}
}

func TestDockerGenerationAndIdentityGuards(t *testing.T) {
	store := coredocker.NewStore()
	identity := storeIdentity()
	if err := store.BindConnection(identity, 1); err != nil {
		t.Fatal(err)
	}
	other := coredocker.Identity{AgentID: "agent-2", NodeID: "node-1"}
	if err := store.BindConnection(other, 2); !errors.Is(err, coredocker.ErrIdentityMismatch) {
		t.Fatalf("second agent bound to one node: %v", err)
	}
	stale := protocol.DockerBatch{Sequence: 1, FullSnapshot: true, SnapshotID: 1, SnapshotFinal: true,
		Health: authoritativeHealth(1), Changes: []protocol.DockerChange{dockerContainer('a', 1)}}
	if err := store.Accept(other, 2, 1, stale, testNow); !errors.Is(err, coredocker.ErrIdentityMismatch) {
		t.Fatalf("accept from an unbound identity: %v", err)
	}
	if err := store.Accept(identity, 2, 1, stale, testNow); !errors.Is(err, coredocker.ErrGenerationMismatch) {
		t.Fatalf("accept from an unbound generation: %v", err)
	}
	if store.UnbindConnection(identity, 2) {
		t.Fatal("generation CAS unbind must refuse a foreign generation")
	}
	if !store.UnbindConnection(identity, 1) {
		t.Fatal("unbind with the active generation failed")
	}
	if store.UnbindConnection(identity, 1) {
		t.Fatal("double unbind must be false")
	}
}
