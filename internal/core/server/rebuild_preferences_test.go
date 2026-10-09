package server

import (
	"context"
	"testing"

	coreprefs "github.com/CST-Cat/NodeDance/internal/core/containerprefs"
	coretasks "github.com/CST-Cat/NodeDance/internal/core/tasks"
	"github.com/CST-Cat/NodeDance/internal/protocol"
	"github.com/CST-Cat/NodeDance/internal/taskstate"
)

func TestSuccessfulRebuildReportMigratesDisplayPreferenceIdentity(t *testing.T) {
	store := coreprefs.NewMemoryStore()
	fromID := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	toID := "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	want := coreprefs.Preferences{Alias: "vault", Icon: "key", Note: "primary", ServiceURL: "https://vault.example.test", Pinned: true, Visible: true, Order: 2}
	if err := store.Set(context.Background(), coreprefs.Identity{NodeID: "node-1", ContainerID: fromID}, want); err != nil {
		t.Fatal(err)
	}
	s := &Server{preferenceMigrator: store}
	task := coretasks.Task{NodeID: "node-1", Status: taskstate.Succeeded,
		Intent: protocol.TaskIntent{Action: protocol.TaskRebuild, ContainerID: fromID},
		Result: coretasks.Result{ResourceRevision: toID}}
	if err := s.migrateRebuiltContainerPreferences(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	if err := s.migrateRebuiltContainerPreferences(context.Background(), task); err != nil {
		t.Fatalf("duplicate terminal Agent report was not idempotent: %v", err)
	}
	got, exists, err := store.Get(context.Background(), coreprefs.Identity{NodeID: "node-1", ContainerID: toID})
	if err != nil || !exists || got != want {
		t.Fatalf("new replacement identity preferences = %+v exists=%t err=%v", got, exists, err)
	}
	if _, exists, err := store.Get(context.Background(), coreprefs.Identity{NodeID: "node-1", ContainerID: fromID}); err != nil || exists {
		t.Fatalf("stopped rollback identity inherited active display preferences: exists=%t err=%v", exists, err)
	}
}

func TestFailedRebuildReportDoesNotMigrateDisplayPreferenceIdentity(t *testing.T) {
	store := coreprefs.NewMemoryStore()
	fromID := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	toID := "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	want := coreprefs.Preferences{Alias: "vault", Pinned: true}
	if err := store.Set(context.Background(), coreprefs.Identity{NodeID: "node-1", ContainerID: fromID}, want); err != nil {
		t.Fatal(err)
	}
	s := &Server{preferenceMigrator: store}
	task := coretasks.Task{NodeID: "node-1", Status: taskstate.Failed,
		Intent: protocol.TaskIntent{Action: protocol.TaskRebuild, ContainerID: fromID},
		Result: coretasks.Result{ObservedState: "restored:health_check_failed", ResourceRevision: fromID}}
	if err := s.migrateRebuiltContainerPreferences(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	got, exists, err := store.Get(context.Background(), coreprefs.Identity{NodeID: "node-1", ContainerID: fromID})
	if err != nil || !exists || got != want {
		t.Fatalf("failed rebuild moved the still-active identity preferences: %+v exists=%t err=%v", got, exists, err)
	}
	if _, exists, err := store.Get(context.Background(), coreprefs.Identity{NodeID: "node-1", ContainerID: toID}); err != nil || exists {
		t.Fatalf("failed rebuild created preferences for an unverified replacement: exists=%t err=%v", exists, err)
	}
}
