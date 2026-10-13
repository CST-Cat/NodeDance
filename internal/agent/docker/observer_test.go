package docker

import (
	"context"
	"testing"
)

func TestObserverCancellationDoesNotDrainQueuedWork(t *testing.T) {
	bus := newObserverBus(nil, 2, nil)
	bus.publish(Batch{Sequence: 1})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, ok := bus.next(ctx); ok {
		t.Fatal("canceled observer continued consuming queued work")
	}
}
func TestObserverDequeuedBatchReleasesReferences(t *testing.T) {
	bus := newObserverBus(nil, 2, nil)
	bus.publish(Batch{Sequence: 1, Changes: []Change{{Container: &Container{ID: "retained"}}}})
	if _, _, ok := bus.next(context.Background()); !ok {
		t.Fatal("queued batch missing")
	}
	if slot := bus.queue[:cap(bus.queue)][0]; slot.Changes != nil {
		t.Fatal("dequeued container remains retained by queue backing array")
	}
}
