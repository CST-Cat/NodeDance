package docker

import (
	"errors"
	"testing"
	"time"

	"github.com/CST-Cat/NodeDance/internal/protocol"
)

var (
	testIdentity = Identity{AgentID: "agent-1", NodeID: "node-1"}
	testNow      = time.Date(2026, 10, 8, 19, 0, 0, 0, time.UTC)
)

func TestUnavailableEmptySnapshotAfterAgentRestartPreservesInventory(t *testing.T) {
	store := readyStore(t, protocol.DockerContainer{ID: "id-1", Name: "important", Image: "image:v1", State: "running", Health: protocol.DockerHealthNone})
	if err := store.BindConnection(testIdentity, 2); err != nil {
		t.Fatal(err)
	}
	lease := onlineLease(2)
	before, ok := store.SnapshotAt(testIdentity.NodeID, &lease, testNow.Add(2*time.Second))
	if !ok || !before.AgentOnline || !before.DataStale || len(before.Containers) != 1 || before.Containers[0].Container.Name != "important" {
		t.Fatalf("new Agent generation did not retain stale last-known inventory: %+v", before)
	}

	unavailable := health(1, protocol.DockerAvailabilityUnavailable, false, false)
	unavailable.ErrorKind = "permission_denied"
	unavailable.Reason = "Docker socket permission denied"
	batch := snapshot(1, 0, 0, true, nil, &unavailable)
	if err := store.Accept(testIdentity, 2, 1, batch, testNow.Add(3*time.Second)); err != nil {
		t.Fatal(err)
	}
	got, ok := store.SnapshotAt(testIdentity.NodeID, &lease, testNow.Add(4*time.Second))
	if !ok || !got.AgentOnline || got.DockerAvailability != protocol.DockerAvailabilityUnavailable || !got.DataStale {
		t.Fatalf("Agent online and Docker unavailable were conflated: %+v", got)
	}
	if len(got.Containers) != 1 || got.Containers[0].Container.ID != "id-1" || !got.Containers[0].Container.Stale {
		t.Fatalf("unavailable empty snapshot erased or freshened the prior asset: %+v", got.Containers)
	}
}

func TestPersistentNodeCloneAndRestartHydrationRemainStaleUntilNewSnapshot(t *testing.T) {
	source := readyStore(t, protocol.DockerContainer{ID: "container-a", Name: "last-good", Image: "image:v1", State: "running", Health: protocol.DockerHealthNone})
	saved, ok := source.PersistentNode(testIdentity.NodeID)
	if !ok || len(saved.Containers) != 1 || saved.Generation != 1 {
		t.Fatalf("safe persistence snapshot is incomplete: %+v", saved)
	}

	candidate := source.Clone()
	if err := candidate.BindConnection(testIdentity, 2); err != nil {
		t.Fatal(err)
	}
	original, _ := source.SnapshotAt(testIdentity.NodeID, ptrLease(1), testNow.Add(time.Second))
	staged, _ := candidate.SnapshotAt(testIdentity.NodeID, ptrLease(2), testNow.Add(time.Second))
	if original.DataStale || !staged.DataStale || len(staged.Containers) != 1 || !staged.Containers[0].Container.Stale {
		t.Fatalf("candidate mutation leaked before persistence or did not preserve stale inventory: original=%+v candidate=%+v", original, staged)
	}

	restarted := NewStore()
	if err := restarted.RestoreStale(saved); err != nil {
		t.Fatal(err)
	}
	hydrated, ok := restarted.SnapshotAt(testIdentity.NodeID, ptrLease(1), testNow.Add(2*time.Second))
	if !ok || hydrated.AgentOnline || !hydrated.DataStale || len(hydrated.Containers) != 1 || !hydrated.Containers[0].Container.Stale {
		t.Fatalf("restart hydration advertised historical inventory as current: %+v", hydrated)
	}
	if err := restarted.BindConnection(testIdentity, 2); err != nil {
		t.Fatal(err)
	}
	awaiting, _ := restarted.SnapshotAt(testIdentity.NodeID, ptrLease(2), testNow.Add(3*time.Second))
	if !awaiting.AgentOnline || !awaiting.DataStale || len(awaiting.Containers) != 1 {
		t.Fatalf("new Agent connection cleared stale state without a full snapshot: %+v", awaiting)
	}
}

func TestHealthKeepaliveRefreshesFreshnessWithoutContainerRevision(t *testing.T) {
	store := readyStore(t, protocol.DockerContainer{ID: "keepalive-container", Name: "steady", Image: "image:v1", State: "running", Health: protocol.DockerHealthNone})
	revision := store.Revision(testIdentity.NodeID)
	firstKeepaliveAt := testNow.Add(5 * time.Second)
	healthbeat := health(11, protocol.DockerAvailabilityAvailable, true, true)
	lastSuccess := firstKeepaliveAt
	lastSnapshot := testNow
	healthbeat.LastSuccessAt = &lastSuccess
	healthbeat.LastSnapshotAt = &lastSnapshot
	healthbeat.ObservedAt = firstKeepaliveAt
	batch := protocol.DockerBatch{Sequence: 11, Health: &healthbeat}
	if err := store.Accept(testIdentity, 1, 2, batch, firstKeepaliveAt); err != nil {
		t.Fatal(err)
	}
	if got := store.Revision(testIdentity.NodeID); got != revision {
		t.Fatalf("health keepalive changed visible inventory revision: before=%d after=%d", revision, got)
	}
	saved, ok := store.PersistentNode(testIdentity.NodeID)
	if !ok || !saved.HealthReceived.Equal(firstKeepaliveAt) || saved.Health == nil || saved.Health.Sequence != 11 {
		t.Fatalf("health keepalive was not retained independently of container revision: %+v", saved)
	}
	view, ok := store.SnapshotAt(testIdentity.NodeID, ptrLease(1), testNow.Add(20*time.Second))
	if !ok || view.DataStale || len(view.Containers) != 1 || view.Containers[0].Container.Stale {
		t.Fatalf("fresh Core receive-time lease was lost during unchanged Docker health: %+v", view)
	}
}

func TestOnlyTrustedCompleteCurrentGenerationSnapshotDeletesMissingAssets(t *testing.T) {
	store := readyStore(t,
		protocol.DockerContainer{ID: "id-a", Name: "a", State: "running", Health: protocol.DockerHealthNone},
		protocol.DockerContainer{ID: "id-b", Name: "b", State: "running", Health: protocol.DockerHealthNone},
	)
	lease := onlineLease(1)
	partial := snapshot(11, 2, 0, false,
		[]protocol.DockerChange{change(11, protocol.DockerChangeUpsert, testContainer("id-a", "partial"))},
		ptrHealth(11, protocol.DockerAvailabilityAvailable, true, true))
	if err := store.Accept(testIdentity, 1, 2, partial, testNow.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	view, _ := store.SnapshotAt(testIdentity.NodeID, &lease, testNow.Add(2*time.Second))
	if len(view.Containers) != 2 {
		t.Fatalf("a half snapshot deleted assets before final: %+v", view.Containers)
	}

	staleHealth := health(12, protocol.DockerAvailabilityAvailable, false, true)
	staleEmpty := snapshot(12, 3, 0, true, nil, &staleHealth)
	if err := store.Accept(testIdentity, 1, 3, staleEmpty, testNow.Add(3*time.Second)); err != nil {
		t.Fatal(err)
	}
	view, _ = store.SnapshotAt(testIdentity.NodeID, &lease, testNow.Add(4*time.Second))
	if len(view.Containers) != 2 || !view.DataStale {
		t.Fatalf("an unsuccessful scan was used as a deletion authority: %+v", view)
	}

	goodHealth := health(13, protocol.DockerAvailabilityAvailable, true, true)
	goodEmpty := snapshot(13, 4, 0, true, nil, &goodHealth)
	if err := store.Accept(testIdentity, 1, 4, goodEmpty, testNow.Add(5*time.Second)); err != nil {
		t.Fatal(err)
	}
	view, _ = store.SnapshotAt(testIdentity.NodeID, &lease, testNow.Add(6*time.Second))
	if len(view.Containers) != 0 || view.DataStale {
		t.Fatalf("complete trusted current snapshot did not delete missing assets: %+v", view)
	}
}

func TestIncrementalsInterleavedWithSnapshotWinByWatermark(t *testing.T) {
	store := readyStore(t,
		protocol.DockerContainer{ID: "id-a", Name: "a", State: "running", Health: protocol.DockerHealthNone},
		protocol.DockerContainer{ID: "id-b", Name: "b", State: "running", Health: protocol.DockerHealthNone},
	)
	first := snapshot(20, 2, 0, false,
		[]protocol.DockerChange{change(20, protocol.DockerChangeUpsert, testContainer("id-a", "a-from-snapshot"))},
		ptrHealth(20, protocol.DockerAvailabilityAvailable, true, true))
	if err := store.Accept(testIdentity, 1, 2, first, testNow.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	deleteA := protocol.DockerBatch{Sequence: 21, Changes: []protocol.DockerChange{change(21, protocol.DockerChangeDelete, nil)}}
	deleteA.Changes[0].ContainerID = "id-a"
	if err := store.Accept(testIdentity, 1, 3, deleteA, testNow.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	createC := protocol.DockerBatch{Sequence: 22, Changes: []protocol.DockerChange{change(22, protocol.DockerChangeUpsert, testContainer("id-c", "created-after-snapshot-start"))}}
	if err := store.Accept(testIdentity, 1, 4, createC, testNow.Add(3*time.Second)); err != nil {
		t.Fatal(err)
	}
	last := snapshot(20, 2, 1, true,
		[]protocol.DockerChange{change(20, protocol.DockerChangeUpsert, testContainer("id-b", "b-from-snapshot"))}, nil)
	if err := store.Accept(testIdentity, 1, 5, last, testNow.Add(4*time.Second)); err != nil {
		t.Fatal(err)
	}
	lease := onlineLease(1)
	view, _ := store.SnapshotAt(testIdentity.NodeID, &lease, testNow.Add(5*time.Second))
	byID := map[string]protocol.DockerContainer{}
	for _, record := range view.Containers {
		byID[record.Container.ID] = record.Container
	}
	if _, exists := byID["id-a"]; exists {
		t.Fatalf("a late full-list item resurrected a newer delete: %+v", byID)
	}
	if byID["id-b"].Name != "b-from-snapshot" || byID["id-c"].Name != "created-after-snapshot-start" {
		t.Fatalf("incremental changes were lost when the snapshot committed: %+v", byID)
	}
}

func TestSnapshotInvalidChunkKeepsOldInventory(t *testing.T) {
	store := readyStore(t, protocol.DockerContainer{ID: "id-a", Name: "last-good", State: "running", Health: protocol.DockerHealthNone})
	first := snapshot(11, 2, 0, false,
		[]protocol.DockerChange{change(11, protocol.DockerChangeUpsert, testContainer("id-a", "uncommitted"))},
		ptrHealth(11, protocol.DockerAvailabilityAvailable, true, true))
	if err := store.Accept(testIdentity, 1, 2, first, testNow.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	outOfOrder := snapshot(11, 2, 2, true,
		[]protocol.DockerChange{change(11, protocol.DockerChangeUpsert, testContainer("id-b", "bad-order"))}, nil)
	if err := store.Accept(testIdentity, 1, 3, outOfOrder, testNow.Add(2*time.Second)); !errors.Is(err, ErrInvalidSnapshot) {
		t.Fatalf("out-of-order chunk error = %v", err)
	}
	lease := onlineLease(1)
	view, _ := store.SnapshotAt(testIdentity.NodeID, &lease, testNow.Add(3*time.Second))
	if len(view.Containers) != 1 || view.Containers[0].Container.Name != "last-good" {
		t.Fatalf("invalid chunk changed active inventory: %+v", view.Containers)
	}
}

func TestOlderSnapshotIDCannotResurrectPreviouslyRemovedContainer(t *testing.T) {
	store := readyStore(t, protocol.DockerContainer{ID: "id-a", Name: "a", State: "running", Health: protocol.DockerHealthNone})
	empty := snapshot(11, 2, 0, true, nil, ptrHealth(11, protocol.DockerAvailabilityAvailable, true, true))
	if err := store.Accept(testIdentity, 1, 2, empty, testNow.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	older := snapshot(12, 1, 0, true,
		[]protocol.DockerChange{change(12, protocol.DockerChangeUpsert, testContainer("id-a", "late-old-snapshot"))},
		ptrHealth(12, protocol.DockerAvailabilityAvailable, true, true))
	if err := store.Accept(testIdentity, 1, 3, older, testNow.Add(2*time.Second)); !errors.Is(err, ErrInvalidSnapshot) {
		t.Fatalf("older snapshot ID error = %v", err)
	}
	lease := onlineLease(1)
	view, _ := store.SnapshotAt(testIdentity.NodeID, &lease, testNow.Add(3*time.Second))
	if len(view.Containers) != 0 || !view.DataStale {
		t.Fatalf("older snapshot resurrected deleted inventory or was not marked stale: %+v", view)
	}
}

func TestSnapshotItemByteAndDurationBoundsPreserveInventory(t *testing.T) {
	t.Run("item limit", func(t *testing.T) {
		store := NewStoreWithOptions(Options{MaxSnapshotItems: 1})
		seedStore(t, store, protocol.DockerContainer{ID: "old", Name: "last-good", State: "running", Health: protocol.DockerHealthNone})
		first := snapshot(11, 2, 0, false,
			[]protocol.DockerChange{change(11, protocol.DockerChangeUpsert, testContainer("new-a", "a"))},
			ptrHealth(11, protocol.DockerAvailabilityAvailable, true, true))
		if err := store.Accept(testIdentity, 1, 2, first, testNow.Add(time.Second)); err != nil {
			t.Fatal(err)
		}
		last := snapshot(11, 2, 1, true,
			[]protocol.DockerChange{change(11, protocol.DockerChangeUpsert, testContainer("new-b", "b"))}, nil)
		if err := store.Accept(testIdentity, 1, 3, last, testNow.Add(2*time.Second)); !errors.Is(err, ErrSnapshotLimit) {
			t.Fatalf("item limit error = %v", err)
		}
		assertOnlyOld(t, store)
	})

	t.Run("combined snapshot and interleaved delta item limit", func(t *testing.T) {
		store := NewStoreWithOptions(Options{MaxSnapshotItems: 1})
		seedStore(t, store, protocol.DockerContainer{ID: "old", Name: "last-good", State: "running", Health: protocol.DockerHealthNone})
		first := snapshot(11, 2, 0, false,
			[]protocol.DockerChange{change(11, protocol.DockerChangeUpsert, testContainer("new-a", "a"))},
			ptrHealth(11, protocol.DockerAvailabilityAvailable, true, true))
		if err := store.Accept(testIdentity, 1, 2, first, testNow.Add(time.Second)); err != nil {
			t.Fatal(err)
		}
		interleaved := protocol.DockerBatch{Sequence: 12, Changes: []protocol.DockerChange{
			change(12, protocol.DockerChangeUpsert, testContainer("new-b", "b")),
		}}
		if err := store.Accept(testIdentity, 1, 3, interleaved, testNow.Add(2*time.Second)); !errors.Is(err, ErrSnapshotLimit) {
			t.Fatalf("combined stage item limit error = %v", err)
		}
		assertOnlyOld(t, store)
	})

	t.Run("byte limit", func(t *testing.T) {
		store := NewStore()
		seedStore(t, store, protocol.DockerContainer{ID: "old", Name: "last-good", State: "running", Health: protocol.DockerHealthNone})
		store.options.MaxSnapshotBytes = 1
		batch := snapshot(11, 2, 0, true, nil, ptrHealth(11, protocol.DockerAvailabilityAvailable, true, true))
		if err := store.Accept(testIdentity, 1, 2, batch, testNow.Add(time.Second)); !errors.Is(err, ErrSnapshotLimit) {
			t.Fatalf("byte limit error = %v", err)
		}
		assertOnlyOld(t, store)
	})

	t.Run("combined snapshot and interleaved delta byte limit", func(t *testing.T) {
		store := NewStore()
		seedStore(t, store, protocol.DockerContainer{ID: "old", Name: "last-good", State: "running", Health: protocol.DockerHealthNone})
		first := snapshot(11, 2, 0, false,
			[]protocol.DockerChange{change(11, protocol.DockerChangeUpsert, testContainer("new-a", "a"))},
			ptrHealth(11, protocol.DockerAvailabilityAvailable, true, true))
		firstPayload, err := protocol.MarshalDockerBatch(first)
		if err != nil {
			t.Fatal(err)
		}
		store.options.MaxSnapshotBytes = len(firstPayload) + 1
		if err := store.Accept(testIdentity, 1, 2, first, testNow.Add(time.Second)); err != nil {
			t.Fatal(err)
		}
		interleaved := protocol.DockerBatch{Sequence: 12, Changes: []protocol.DockerChange{
			change(12, protocol.DockerChangeUpsert, testContainer("new-b", "b")),
		}}
		if err := store.Accept(testIdentity, 1, 3, interleaved, testNow.Add(2*time.Second)); !errors.Is(err, ErrSnapshotLimit) {
			t.Fatalf("combined stage byte limit error = %v", err)
		}
		assertOnlyOld(t, store)
	})

	t.Run("duration limit", func(t *testing.T) {
		store := NewStoreWithOptions(Options{SnapshotTTL: time.Second})
		seedStore(t, store, protocol.DockerContainer{ID: "old", Name: "last-good", State: "running", Health: protocol.DockerHealthNone})
		first := snapshot(11, 2, 0, false,
			[]protocol.DockerChange{change(11, protocol.DockerChangeUpsert, testContainer("new", "uncommitted"))},
			ptrHealth(11, protocol.DockerAvailabilityAvailable, true, true))
		if err := store.Accept(testIdentity, 1, 2, first, testNow.Add(time.Second)); err != nil {
			t.Fatal(err)
		}
		if got := store.ExpireStaging(testNow.Add(3 * time.Second)); got != 1 {
			t.Fatalf("expired stages = %d, want 1", got)
		}
		last := snapshot(11, 2, 1, true,
			[]protocol.DockerChange{change(11, protocol.DockerChangeUpsert, testContainer("other", "other"))}, nil)
		if err := store.Accept(testIdentity, 1, 3, last, testNow.Add(4*time.Second)); !errors.Is(err, ErrSnapshotExpired) {
			t.Fatalf("expired continuation error = %v", err)
		}
		assertOnlyOld(t, store)
	})
}

func TestStaleSnapshotPlaceholderDoesNotEraseLastKnownFields(t *testing.T) {
	store := readyStore(t, protocol.DockerContainer{ID: "id-a", Name: "known-name", Image: "known-image", State: "running", Health: protocol.DockerHealthHealthy})
	if err := store.BindConnection(testIdentity, 2); err != nil {
		t.Fatal(err)
	}
	placeholder := protocol.DockerContainer{
		ID: "id-a", Name: "", State: "unknown", Health: protocol.DockerHealthUnknown,
		Stale: true, UnavailableReason: "record exceeds message limit", ObservedAt: testNow.Add(time.Second),
	}
	batch := snapshot(1, 1, 0, true,
		[]protocol.DockerChange{change(1, protocol.DockerChangeUpsert, &placeholder)},
		ptrHealth(1, protocol.DockerAvailabilityAvailable, true, true))
	if err := store.Accept(testIdentity, 2, 1, batch, testNow.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	lease := onlineLease(2)
	view, _ := store.SnapshotAt(testIdentity.NodeID, &lease, testNow.Add(2*time.Second))
	if len(view.Containers) != 1 {
		t.Fatalf("expected the existing container to remain: %+v", view.Containers)
	}
	got := view.Containers[0].Container
	if got.Name != "known-name" || got.Image != "known-image" || !got.Stale || got.UnavailableReason != "record exceeds message limit" {
		t.Fatalf("unknown placeholder erased last-known state: %+v", got)
	}
}

func TestAgentLeaseAndDockerHealthRemainIndependent(t *testing.T) {
	store := readyStore(t, protocol.DockerContainer{ID: "id-a", Name: "a", State: "running", Health: protocol.DockerHealthNone})
	unavailable := health(11, protocol.DockerAvailabilityUnavailable, false, true)
	unavailable.Reason = "socket permission denied"
	if err := store.Accept(testIdentity, 1, 2, protocol.DockerBatch{Sequence: 11, Health: &unavailable}, testNow.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	lease := onlineLease(1)
	view, _ := store.SnapshotAt(testIdentity.NodeID, &lease, testNow.Add(2*time.Second))
	if !view.AgentOnline || view.DockerAvailability != protocol.DockerAvailabilityUnavailable || !view.DataStale {
		t.Fatalf("Docker failure incorrectly changed Agent lease state: %+v", view)
	}
	offline := lease
	offline.Status = LeaseOffline
	view, _ = store.SnapshotAt(testIdentity.NodeID, &offline, testNow.Add(2*time.Second))
	if view.AgentOnline || view.DockerAvailability != protocol.DockerAvailabilityUnavailable || !view.DataStale {
		t.Fatalf("offline Agent state overwrote independent Docker status: %+v", view)
	}
}

func TestGenerationIdentityAndEnvelopeSequenceAreChecked(t *testing.T) {
	store := readyStore(t, protocol.DockerContainer{ID: "id-a", Name: "a", State: "running", Health: protocol.DockerHealthNone})
	batch := protocol.DockerBatch{Sequence: 11, Health: ptrHealth(11, protocol.DockerAvailabilityAvailable, true, true)}
	if err := store.Accept(Identity{AgentID: "other", NodeID: testIdentity.NodeID}, 1, 2, batch, testNow.Add(time.Second)); !errors.Is(err, ErrIdentityMismatch) {
		t.Fatalf("identity mismatch error = %v", err)
	}
	if err := store.Accept(testIdentity, 2, 2, batch, testNow.Add(time.Second)); !errors.Is(err, ErrGenerationMismatch) {
		t.Fatalf("generation mismatch error = %v", err)
	}
	if err := store.Accept(testIdentity, 1, 1, batch, testNow.Add(time.Second)); !errors.Is(err, ErrStaleSequence) {
		t.Fatalf("replayed Envelope sequence error = %v", err)
	}
}

func readyStore(t *testing.T, containers ...protocol.DockerContainer) *Store {
	t.Helper()
	store := NewStore()
	seedStore(t, store, containers...)
	return store
}

func seedStore(t *testing.T, store *Store, containers ...protocol.DockerContainer) {
	t.Helper()
	if err := store.BindConnection(testIdentity, 1); err != nil {
		t.Fatal(err)
	}
	changes := make([]protocol.DockerChange, 0, len(containers))
	for _, item := range containers {
		item.ObservedAt = testNow
		changes = append(changes, change(10, protocol.DockerChangeUpsert, &item))
	}
	batch := snapshot(10, 1, 0, true, changes, ptrHealth(10, protocol.DockerAvailabilityAvailable, true, true))
	if err := store.Accept(testIdentity, 1, 1, batch, testNow); err != nil {
		t.Fatal(err)
	}
}

func assertOnlyOld(t *testing.T, store *Store) {
	t.Helper()
	lease := onlineLease(1)
	view, ok := store.SnapshotAt(testIdentity.NodeID, &lease, testNow.Add(5*time.Second))
	if !ok || len(view.Containers) != 1 || view.Containers[0].Container.ID != "old" || view.Containers[0].Container.Name != "last-good" {
		t.Fatalf("staging failure changed active inventory: %+v", view)
	}
}

func onlineLease(generation uint64) Lease {
	return Lease{Identity: testIdentity, Generation: generation, ValidUntil: testNow.Add(time.Hour), Status: LeaseOnline}
}

func ptrLease(generation uint64) *Lease {
	lease := onlineLease(generation)
	return &lease
}

func snapshot(sequence, snapshotID uint64, index int, final bool, changes []protocol.DockerChange, health *protocol.DockerHealth) protocol.DockerBatch {
	return protocol.DockerBatch{
		Sequence: sequence, SnapshotID: snapshotID, FullSnapshot: true,
		SnapshotIndex: index, SnapshotFinal: final, Changes: changes, Health: health,
	}
}

func change(sequence uint64, action protocol.DockerChangeAction, container *protocol.DockerContainer) protocol.DockerChange {
	id := "id-a"
	if container != nil {
		id = container.ID
	}
	return protocol.DockerChange{
		Sequence: sequence, Action: action, Container: container,
		ContainerID: id, ObservedAt: testNow,
	}
}

func testContainer(id, name string) *protocol.DockerContainer {
	return &protocol.DockerContainer{
		ID: id, Name: name, Image: "image", ImageID: "sha256:abc", State: "running",
		Running: true, Health: protocol.DockerHealthNone, ObservedAt: testNow,
	}
}

func ptrHealth(sequence uint64, availability protocol.DockerAvailability, fresh, events bool) *protocol.DockerHealth {
	value := health(sequence, availability, fresh, events)
	return &value
}

func health(sequence uint64, availability protocol.DockerAvailability, fresh, events bool) protocol.DockerHealth {
	return protocol.DockerHealth{
		Sequence: sequence, Availability: availability, EventsConnected: events,
		SnapshotFresh: fresh, ObservedAt: testNow,
	}
}
