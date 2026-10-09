package taskjournal

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/CST-Cat/NodeDance/internal/taskstate"
)

func TestRecoverInterruptedMarksUncommittedQueuedTaskUnknown(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "agent")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	store, err := Open(ctx, filepath.Join(dir, "tasks.sqlite"), "node-1")
	if err != nil {
		t.Fatalf("open task journal: %v", err)
	}
	defer store.Close()

	identity := taskstate.Identity{
		TaskID: "task-1", NodeID: "node-1", IdempotencyKey: "idempotency-key-1",
		TargetID: "container-1", ResourceKey: "container:container-1", Action: "start",
		Payload: json.RawMessage(`{"action":"start"}`),
	}
	queued, err := store.Enqueue(ctx, identity)
	if err != nil {
		t.Fatalf("enqueue task without delivery confirmation: %v", err)
	}
	if queued.Task.Status != taskstate.Queued || queued.Task.DeliveryCommitted {
		t.Fatalf("test requires an uncommitted queued row, got %#v", queued.Task)
	}

	count, err := store.RecoverInterrupted(ctx)
	if err != nil {
		t.Fatalf("recover interrupted tasks: %v", err)
	}
	if count != 1 {
		t.Fatalf("recovery changed %d task(s), want 1", count)
	}
	recovered, err := store.Get(ctx, identity.TaskID)
	if err != nil {
		t.Fatalf("read recovered task: %v", err)
	}
	if recovered.Status != taskstate.Unknown || recovered.Result.Code != ResultUncertain {
		t.Fatalf("uncommitted queued task must require reconciliation instead of replay: %#v", recovered)
	}
	if recovered.DeliveryCommitted {
		t.Fatal("recovery must not manufacture Core delivery evidence")
	}

	retry := identity
	retry.TaskID = "task-2"
	retry.IdempotencyKey = "idempotency-key-2"
	if _, err := store.Enqueue(ctx, retry); !errors.Is(err, ErrResourceBusy) {
		t.Fatalf("unresolved recovered task must keep its resource claim, got %v", err)
	}
}
