package taskjournal

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
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

func TestComposeConfigBaselinePersistsUntilExplicitlyCleared(t *testing.T) {
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

	keyDigest := sha256.Sum256([]byte("compose project"))
	key := hex.EncodeToString(keyDigest[:])
	previous := [][]byte{[]byte("services:\n  app:\n    image: app:v1\n"), []byte("name: shared\n")}
	if inserted, err := store.StoreComposeConfigBaselineIfAbsent(ctx, key, previous); err != nil || !inserted {
		t.Fatalf("store first Compose baseline: inserted=%v err=%v", inserted, err)
	}
	if inserted, err := store.StoreComposeConfigBaselineIfAbsent(ctx, key, [][]byte{[]byte("new config")}); err != nil || inserted {
		t.Fatalf("a later edit must not replace the pending rollback baseline: inserted=%v err=%v", inserted, err)
	}
	loaded, exists, err := store.LoadComposeConfigBaseline(ctx, key)
	if err != nil || !exists || len(loaded) != len(previous) {
		t.Fatalf("load Compose baseline: exists=%v len=%d err=%v", exists, len(loaded), err)
	}
	for index := range previous {
		if !bytes.Equal(loaded[index], previous[index]) {
			t.Fatalf("baseline file %d changed: got %q want %q", index, loaded[index], previous[index])
		}
	}
	if err := store.DeleteComposeConfigBaseline(ctx, key); err != nil {
		t.Fatalf("delete verified Compose baseline: %v", err)
	}
	if _, exists, err := store.LoadComposeConfigBaseline(ctx, key); err != nil || exists {
		t.Fatalf("cleared Compose baseline is still present: exists=%v err=%v", exists, err)
	}
}
