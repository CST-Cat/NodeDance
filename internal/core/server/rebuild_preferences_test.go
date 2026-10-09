package server

import (
	"context"
	"path/filepath"
	"testing"

	coreprefs "github.com/CST-Cat/NodeDance/internal/core/containerprefs"
	"github.com/CST-Cat/NodeDance/internal/core/dashboard"
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

func TestServerDefaultsToPersistentSQLitePreferenceMigration(t *testing.T) {
	s := newTestServer(t)
	if _, ok := s.preferenceMigrator.(*coreprefs.SQLiteMigrator); !ok {
		t.Fatalf("default preference migrator = %T, want SQLiteMigrator", s.preferenceMigrator)
	}
	override := coreprefs.NewMemoryStore()
	overridden, err := New("test-explicit-preference-migrator", Options{
		DataDir: filepath.Join(t.TempDir(), "override"), Development: true, PreferenceMigrator: override,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = overridden.Close() })
	if overridden.preferenceMigrator != override {
		t.Fatalf("explicit preference migrator was not retained: got %T", overridden.preferenceMigrator)
	}

	nodeID := "00000000-0000-4000-8000-000000000001"
	if _, err := s.store.DB.Exec(`INSERT INTO nodes(id, display_name, status, created_at, updated_at)
		VALUES(?, 'preference migration test', 'pending', 1, 1)`, nodeID); err != nil {
		t.Fatal(err)
	}
	fromID := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	toID := "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	want := dashboard.Preference{NodeID: nodeID, TargetKind: "container", Identity: "container:" + fromID,
		Alias: "Vault", Icon: "shield", Notes: "production", ServiceURL: "https://vault.example.test",
		SortOrder: 5, Visible: false, Pinned: true}
	if err := s.dashboardPreferences.Put(context.Background(), want); err != nil {
		t.Fatal(err)
	}
	task := coretasks.Task{NodeID: nodeID, Status: taskstate.Succeeded,
		Intent: protocol.TaskIntent{Action: protocol.TaskRebuild, ContainerID: fromID},
		Result: coretasks.Result{ResourceRevision: toID}}
	if err := s.migrateRebuiltContainerPreferences(context.Background(), task); err != nil {
		t.Fatalf("server's default preference migration failed: %v", err)
	}
	items, err := s.dashboardPreferences.List(context.Background(), nodeID)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Identity != "container:"+toID || items[0].Alias != want.Alias || items[0].Icon != want.Icon ||
		items[0].Notes != want.Notes || items[0].ServiceURL != want.ServiceURL || items[0].SortOrder != want.SortOrder ||
		items[0].Visible != want.Visible || items[0].Pinned != want.Pinned {
		t.Fatalf("default SQLite preference migration result = %+v, want identity/fields moved to replacement", items)
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
