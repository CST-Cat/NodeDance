package containerprefs

import (
	"context"
	"errors"
	"testing"
)

func TestMemoryStoreMovesPreferencesByIdentityIdempotently(t *testing.T) {
	store := NewMemoryStore()
	from := Identity{NodeID: "node-1", ContainerID: "a000000000000000000000000000000000000000000000000000000000000000"}
	to := "b000000000000000000000000000000000000000000000000000000000000000"
	want := Preferences{Alias: "vault", Icon: "key", Note: "primary", ServiceURL: "https://vault.example.test", Pinned: true, Visible: true, Order: 3}
	if err := store.Set(context.Background(), from, want); err != nil {
		t.Fatal(err)
	}
	if err := store.MigrateContainer(context.Background(), from.NodeID, from.ContainerID, to); err != nil {
		t.Fatal(err)
	}
	if err := store.MigrateContainer(context.Background(), from.NodeID, from.ContainerID, to); err != nil {
		t.Fatalf("replaying the same task report must be idempotent: %v", err)
	}
	got, exists, err := store.Get(context.Background(), Identity{NodeID: from.NodeID, ContainerID: to})
	if err != nil || !exists || got != want {
		t.Fatalf("replacement preferences = %+v exists=%t err=%v", got, exists, err)
	}
	if _, exists, err := store.Get(context.Background(), from); err != nil || exists {
		t.Fatalf("old identity still owns moved preferences: exists=%t err=%v", exists, err)
	}
}

func TestMemoryStoreDoesNotOverwriteConflictingReplacementPreferences(t *testing.T) {
	store := NewMemoryStore()
	from := Identity{NodeID: "node-1", ContainerID: "a000000000000000000000000000000000000000000000000000000000000000"}
	to := Identity{NodeID: from.NodeID, ContainerID: "b000000000000000000000000000000000000000000000000000000000000000"}
	if err := store.Set(context.Background(), from, Preferences{Alias: "old"}); err != nil {
		t.Fatal(err)
	}
	if err := store.Set(context.Background(), to, Preferences{Alias: "new"}); err != nil {
		t.Fatal(err)
	}
	if err := store.MigrateContainer(context.Background(), from.NodeID, from.ContainerID, to.ContainerID); !errors.Is(err, ErrIdentityConflict) {
		t.Fatalf("mismatched destination should conflict, got %v", err)
	}
	old, oldExists, oldErr := store.Get(context.Background(), from)
	current, currentExists, currentErr := store.Get(context.Background(), to)
	if oldErr != nil || currentErr != nil || !oldExists || !currentExists || old.Alias != "old" || current.Alias != "new" {
		t.Fatalf("conflict changed preferences: old=%+v/%t new=%+v/%t errors=%v/%v", old, oldExists, current, currentExists, oldErr, currentErr)
	}
}
