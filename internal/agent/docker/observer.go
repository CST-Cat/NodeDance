package docker

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"sync"
	"time"
)

type observerBus struct {
	observer Observer
	maxQueue int
	current  func() (uint64, uint64, []Container, Health)

	mu         sync.Mutex
	queue      []Batch
	resync     bool
	closed     bool
	wake       chan struct{}
	lastHealth *Health
}

func newObserverBus(observer Observer, queueSize int, current func() (uint64, uint64, []Container, Health)) *observerBus {
	if queueSize < 1 {
		queueSize = 1
	}
	return &observerBus{
		observer: observer, maxQueue: queueSize, current: current,
		wake: make(chan struct{}, 1),
	}
}

func (b *observerBus) run(ctx context.Context) {
	if b.observer == nil {
		<-ctx.Done()
		return
	}
	for {
		batch, needsSnapshot, ok := b.next(ctx)
		if !ok {
			return
		}
		if needsSnapshot {
			if err := b.sendSnapshot(ctx); err != nil {
				b.requestSnapshot()
				if !sleepContext(ctx, 250*time.Millisecond) {
					return
				}
			}
			continue
		}
		if err := b.observer.ApplyDockerBatch(ctx, batch); err != nil {
			b.requestSnapshot()
			if !sleepContext(ctx, 250*time.Millisecond) {
				return
			}
		}
	}
}

func (b *observerBus) next(ctx context.Context) (Batch, bool, bool) {
	for {
		b.mu.Lock()
		if b.closed {
			b.mu.Unlock()
			return Batch{}, false, false
		}
		if b.resync {
			b.resync = false
			b.mu.Unlock()
			return Batch{}, true, true
		}
		if len(b.queue) > 0 {
			batch := b.queue[0]
			copy(b.queue, b.queue[1:])
			b.queue = b.queue[:len(b.queue)-1]
			b.mu.Unlock()
			return batch, false, true
		}
		b.mu.Unlock()
		select {
		case <-ctx.Done():
			return Batch{}, false, false
		case <-b.wake:
		}
	}
}

func (b *observerBus) sendSnapshot(ctx context.Context) error {
	snapshotID, sequence, containers, health := b.current()
	groups := make([][]Change, 0, (len(containers)+MaxBatchChanges-1)/MaxBatchChanges)
	current := make([]Change, 0, MaxBatchChanges)
	for _, item := range containers {
		change := snapshotChange(sequence, item)
		candidate := append(append([]Change(nil), current...), change)
		batch := snapshotBatch(sequence, snapshotID, len(groups), false, candidate, health, len(groups) == 0)
		encoded, err := json.Marshal(batch)
		if err != nil {
			return fmt.Errorf("encode Docker snapshot batch: %w", err)
		}
		if len(candidate) > MaxBatchChanges || len(encoded) > MaxBatchBytes-128 {
			if len(current) == 0 {
				return fmt.Errorf("single Docker container cannot fit the bounded observer batch: %s", item.ID)
			}
			groups = append(groups, current)
			current = []Change{change}
			batch = snapshotBatch(sequence, snapshotID, len(groups), false, current, health, len(groups) == 0)
			encoded, err = json.Marshal(batch)
			if err != nil || len(encoded) > MaxBatchBytes-128 {
				return fmt.Errorf("single Docker container cannot fit the bounded observer batch: %s", item.ID)
			}
			continue
		}
		current = candidate
	}
	if len(current) > 0 || len(groups) == 0 {
		groups = append(groups, current)
	}
	for part, changes := range groups {
		batch := snapshotBatch(sequence, snapshotID, part, part == len(groups)-1, changes, health, part == 0)
		encoded, err := json.Marshal(batch)
		if err != nil {
			return fmt.Errorf("encode Docker snapshot batch: %w", err)
		}
		if len(encoded) > MaxBatchBytes {
			return fmt.Errorf("Docker snapshot batch exceeds %d encoded bytes", MaxBatchBytes)
		}
		if err := b.observer.ApplyDockerBatch(ctx, batch); err != nil {
			return err
		}
	}
	return nil
}

func (b *observerBus) requestSnapshot() {
	b.mu.Lock()
	if !b.closed {
		b.queue = nil
		b.resync = true
	}
	b.mu.Unlock()
	b.signal()
}

func (b *observerBus) publishChange(change Change) {
	batch := Batch{Sequence: change.Sequence, Changes: []Change{change}}
	encoded, err := json.Marshal(batch)
	if err != nil || len(encoded) > MaxBatchBytes {
		change = Change{
			Sequence: change.Sequence, Action: ChangeStale, ContainerID: change.ContainerID,
			Reason: "container update exceeds the NodeDance Docker update size limit", ObservedAt: change.ObservedAt,
		}
		batch = Batch{Sequence: change.Sequence, Changes: []Change{change}}
		encoded, err = json.Marshal(batch)
		if err != nil || len(encoded) > MaxBatchBytes {
			b.requestSnapshot()
			return
		}
	}
	b.publish(batch)
}

func (b *observerBus) publishHealth(health Health) {
	b.mu.Lock()
	if b.lastHealth != nil && equivalentHealth(*b.lastHealth, health) {
		b.mu.Unlock()
		return
	}
	copy := cloneHealth(health)
	b.lastHealth = &copy
	b.mu.Unlock()
	healthCopy := cloneHealth(health)
	b.publish(Batch{Sequence: health.Sequence, Health: &healthCopy})
}

func (b *observerBus) publish(batch Batch) {
	b.mu.Lock()
	if b.closed || b.resync {
		b.mu.Unlock()
		return
	}
	if len(b.queue) >= b.maxQueue {
		b.queue = nil
		b.resync = true
	} else {
		batch = cloneBatch(batch)
		b.queue = append(b.queue, batch)
	}
	b.mu.Unlock()
	b.signal()
}

func (b *observerBus) signal() {
	select {
	case b.wake <- struct{}{}:
	default:
	}
}

func (b *observerBus) close() {
	b.mu.Lock()
	b.closed = true
	b.queue = nil
	b.mu.Unlock()
	b.signal()
}

func cloneBatch(batch Batch) Batch {
	out := batch
	if batch.Health != nil {
		copy := cloneHealth(*batch.Health)
		out.Health = &copy
	}
	out.Changes = append([]Change(nil), batch.Changes...)
	for i := range out.Changes {
		if batch.Changes[i].Container != nil {
			copy := cloneContainer(*batch.Changes[i].Container)
			out.Changes[i].Container = &copy
		}
	}
	return out
}

func equivalentHealth(left, right Health) bool {
	left.ObservedAt = time.Time{}
	right.ObservedAt = time.Time{}
	left.LastSuccessAt = nil
	right.LastSuccessAt = nil
	left.LastSnapshotAt = nil
	right.LastSnapshotAt = nil
	return reflect.DeepEqual(left, right)
}

func snapshotChange(sequence uint64, item Container) Change {
	copy := cloneContainer(item)
	change := Change{Sequence: sequence, Action: ChangeUpsert, ContainerID: copy.ID, Container: &copy, ObservedAt: copy.ObservedAt}
	if !containerFitsBatch(copy) {
		copy = compactUnknown(copy, "container record exceeds the NodeDance Docker update size limit")
		change.Container = &copy
	}
	return change
}

func snapshotBatch(sequence, snapshotID uint64, index int, final bool, changes []Change, health Health, includeHealth bool) Batch {
	batch := Batch{
		Sequence: sequence, SnapshotID: snapshotID, FullSnapshot: true,
		SnapshotIndex: index, SnapshotFinal: final, Changes: changes,
	}
	if includeHealth {
		copy := cloneHealth(health)
		batch.Health = &copy
	}
	return batch
}

func sleepContext(ctx context.Context, duration time.Duration) bool {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
