package docker

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	coredocker "github.com/CST-Cat/NodeDance/internal/core/docker"
	"github.com/CST-Cat/NodeDance/internal/protocol"
)

type collectingObserver struct {
	batches []Batch
}

func (o *collectingObserver) ApplyDockerBatch(_ context.Context, batch Batch) error {
	o.batches = append(o.batches, cloneBatch(batch))
	return nil
}

func TestAgentObserverDTOAndCoreStoreRoundTrip(t *testing.T) {
	now := time.Date(2026, 10, 8, 20, 0, 0, 0, time.UTC)
	identity := coredocker.Identity{AgentID: "agent-integration", NodeID: "node-integration"}
	store := coredocker.NewStore()
	if err := store.BindConnection(identity, 1); err != nil {
		t.Fatal(err)
	}
	envelopeSequence := uint64(0)

	oldCache := newStateCache(func() time.Time { return now })
	oldCache.setEventStatus(true, nil)
	oldCache.applySnapshot(0, []Container{baseContainer("old-id", "old", "running")})
	oldObserver := &collectingObserver{}
	oldBus := newObserverBus(oldObserver, 8, oldCache.currentBatch)
	if err := oldBus.sendSnapshot(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(oldObserver.batches) != 1 {
		t.Fatalf("seed inventory snapshot chunks = %d, want 1", len(oldObserver.batches))
	}
	envelopeSequence++
	if err := roundTripBatch(t, store, identity, 1, envelopeSequence, oldObserver.batches[0], now); err != nil {
		t.Fatal(err)
	}
	if err := store.BindConnection(identity, 2); err != nil {
		t.Fatal(err)
	}

	// A fresh Agent generation may start with an empty cache and no usable
	// Docker Engine. Exercise the real Observer -> DTO -> codec -> Core path for
	// snapshotId=0; this report must mark old assets stale without replacing them.
	unavailableCache := newStateCache(func() time.Time { return now })
	unavailableCache.setSnapshotFailure(errors.New("permission denied"))
	unavailableObserver := &collectingObserver{}
	unavailableBus := newObserverBus(unavailableObserver, 8, unavailableCache.currentBatch)
	if err := unavailableBus.sendSnapshot(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(unavailableObserver.batches) != 1 || unavailableObserver.batches[0].SnapshotID != 0 || len(unavailableObserver.batches[0].Changes) != 0 {
		t.Fatalf("initial unavailable Engine report did not use the empty zero-ID snapshot contract: %+v", unavailableObserver.batches)
	}
	envelopeSequence = 1
	if err := roundTripBatch(t, store, identity, 2, envelopeSequence, unavailableObserver.batches[0], now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	lease := coredocker.Lease{
		Identity: identity, Generation: 2, ValidUntil: now.Add(time.Hour), Status: coredocker.LeaseOnline,
	}
	unavailableView, ok := store.SnapshotAt(identity.NodeID, &lease, now.Add(2*time.Second))
	if !ok || !unavailableView.AgentOnline || unavailableView.DockerAvailability != protocol.DockerAvailabilityUnavailable ||
		len(unavailableView.Containers) != 1 || unavailableView.Containers[0].Container.ID != "old-id" || !unavailableView.Containers[0].Container.Stale {
		t.Fatalf("initial unavailable Engine report deleted old-generation inventory: %+v", unavailableView)
	}

	newCache := newStateCache(func() time.Time { return now })
	newCache.setEventStatus(true, nil)
	containers := make([]Container, 65)
	for index := range containers {
		containers[index] = baseContainer(fmt.Sprintf("fixture-%02d", index), fmt.Sprintf("service-%02d", index), "running")
	}
	newCache.applySnapshot(0, containers)
	newObserver := &collectingObserver{}
	newBus := newObserverBus(newObserver, 8, newCache.currentBatch)
	if err := newBus.sendSnapshot(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(newObserver.batches) != 2 || newObserver.batches[0].SnapshotIndex != 0 || newObserver.batches[1].SnapshotIndex != 1 || !newObserver.batches[1].SnapshotFinal {
		t.Fatalf("Observer did not generate a two-chunk full snapshot: %+v", newObserver.batches)
	}

	// The Core must retain its prior generation's inventory while the first
	// chunk of the new generation is only staged.
	envelopeSequence++
	if err := roundTripBatch(t, store, identity, 2, envelopeSequence, newObserver.batches[0], now.Add(3*time.Second)); err != nil {
		t.Fatal(err)
	}
	stagedView, ok := store.SnapshotAt(identity.NodeID, &lease, now.Add(4*time.Second))
	if !ok || len(stagedView.Containers) != 1 || stagedView.Containers[0].Container.ID != "old-id" || !stagedView.Containers[0].Container.Stale {
		t.Fatalf("partial new-generation snapshot damaged the last-known inventory: %+v", stagedView)
	}

	// Produce real Agent-cache delete/create events after the full image's
	// watermark, route them through observerBus, the shared DTO codec, and Core,
	// then deliver the second (older-watermark) chunk. The deltas must win.
	now = now.Add(3 * time.Second)
	deletedRevision := newCache.observeEvent(Event{ContainerID: "fixture-00", Action: "destroy"})
	deleteChange, changed := newCache.applyEventDelete("fixture-00", deletedRevision, now)
	if !changed {
		t.Fatal("Agent cache did not produce the expected delete event")
	}
	newBus.publishChange(deleteChange)
	deleteBatch := nextObservedBatch(t, newBus)
	envelopeSequence++
	if err := roundTripBatch(t, store, identity, 2, envelopeSequence, deleteBatch, now); err != nil {
		t.Fatal(err)
	}

	created := baseContainer("created-after-snapshot", "created-after-snapshot", "running")
	created.ObservedAt = now
	createdRevision := newCache.observeEvent(Event{ContainerID: created.ID, Action: "create"})
	createChange, changed := newCache.applyEventUpsert(created.ID, createdRevision, created)
	if !changed {
		t.Fatal("Agent cache did not produce the expected create event")
	}
	newBus.publishChange(createChange)
	createBatch := nextObservedBatch(t, newBus)
	envelopeSequence++
	if err := roundTripBatch(t, store, identity, 2, envelopeSequence, createBatch, now); err != nil {
		t.Fatal(err)
	}

	// The captured final chunk represents the earlier scan watermark. A stale
	// asset in it may not undo the later delete, and its absence may not undo
	// the later create.
	envelopeSequence++
	if err := roundTripBatch(t, store, identity, 2, envelopeSequence, newObserver.batches[1], now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	view, ok := store.SnapshotAt(identity.NodeID, &lease, now.Add(2*time.Second))
	if !ok || !view.AgentOnline || view.DataStale || len(view.Containers) != 65 {
		t.Fatalf("multi-chunk full snapshot did not commit with newer deltas: %+v", view)
	}
	ids := make(map[string]struct{}, len(view.Containers))
	for _, record := range view.Containers {
		ids[record.Container.ID] = struct{}{}
	}
	if _, exists := ids["fixture-00"]; exists {
		t.Fatal("older full snapshot resurrected a container deleted after its watermark")
	}
	if _, exists := ids["created-after-snapshot"]; !exists {
		t.Fatal("older full snapshot erased a container created after its watermark")
	}

	// Health-only updates use the same Agent observer bus and DTO pipeline and
	// cannot erase the current inventory or change Core's Agent lease.
	now = now.Add(3 * time.Second)
	newCache.setPingResult(errors.New("permission denied"))
	newBus.publishHealth(newCache.healthSnapshot())
	healthBatch := nextObservedBatch(t, newBus)
	envelopeSequence++
	if err := roundTripBatch(t, store, identity, 2, envelopeSequence, healthBatch, now); err != nil {
		t.Fatal(err)
	}
	view, ok = store.SnapshotAt(identity.NodeID, &lease, now.Add(time.Second))
	if !ok || !view.AgentOnline || view.DockerAvailability != protocol.DockerAvailabilityUnavailable || !view.DataStale || len(view.Containers) != 65 {
		t.Fatalf("health-only Docker failure did not preserve inventory independently of lease: %+v", view)
	}

	oldGeneration := protocol.DockerBatch{Sequence: 99, Health: &protocol.DockerHealth{
		Sequence: 99, Availability: protocol.DockerAvailabilityAvailable, EventsConnected: true,
		SnapshotFresh: true, ObservedAt: now,
	}}
	if err := store.Accept(identity, 1, 100, oldGeneration, now.Add(2*time.Second)); !errors.Is(err, coredocker.ErrStaleGeneration) {
		t.Fatalf("old socket accepted a post-reconnect report: %v", err)
	}
	view, ok = store.SnapshotAt(identity.NodeID, &lease, now.Add(3*time.Second))
	if !ok || view.ActiveGeneration != 2 || !view.AgentOnline || len(view.Containers) != 65 {
		t.Fatalf("old generation altered new-generation state: %+v", view)
	}
}

func nextObservedBatch(t *testing.T, bus *observerBus) Batch {
	t.Helper()
	batch, needsSnapshot, ok := bus.next(context.Background())
	if !ok || needsSnapshot {
		t.Fatalf("observer queue did not return an incremental batch: ok=%v snapshot=%v", ok, needsSnapshot)
	}
	return batch
}

func roundTripBatch(t *testing.T, store *coredocker.Store, identity coredocker.Identity, generation, envelopeSequence uint64, batch Batch, receivedAt time.Time) error {
	t.Helper()
	dto, err := ToProtocolBatch(batch)
	if err != nil {
		return err
	}
	payload, err := protocol.MarshalDockerBatch(dto)
	if err != nil {
		return err
	}
	decoded, err := protocol.UnmarshalDockerBatch(payload)
	if err != nil {
		return err
	}
	return store.Accept(identity, generation, envelopeSequence, decoded, receivedAt)
}
