package containerprefs

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/CST-Cat/NodeDance/internal/core/dashboard"
	"github.com/CST-Cat/NodeDance/internal/core/storage"
)

const sqlitePreferenceNodeID = "00000000-0000-4000-8000-000000000001"

func newSQLitePreferenceMigrator(t *testing.T) (*storage.Store, *SQLiteMigrator) {
	t.Helper()
	store, err := storage.Open(context.Background(), filepath.Join(t.TempDir(), "core"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.DB.Close() })
	if _, err := store.DB.Exec(`INSERT INTO nodes(id, display_name, status, created_at, updated_at)
		VALUES(?, 'test node', 'pending', 1, 1)`, sqlitePreferenceNodeID); err != nil {
		t.Fatal(err)
	}
	return store, NewSQLiteMigrator(store.DB)
}

func putSQLitePreference(t *testing.T, store *storage.Store, containerID string, value dashboard.Preference) {
	t.Helper()
	value.NodeID = sqlitePreferenceNodeID
	value.TargetKind = "container"
	value.Identity = "container:" + containerID
	if err := (dashboard.Repository{DB: store.DB}).Put(context.Background(), value); err != nil {
		t.Fatal(err)
	}
}

func getSQLitePreference(t *testing.T, store *storage.Store, containerID string) (dashboard.Preference, bool) {
	t.Helper()
	items, err := (dashboard.Repository{DB: store.DB}).List(context.Background(), sqlitePreferenceNodeID)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range items {
		if item.TargetKind == "container" && item.Identity == "container:"+containerID {
			return item, true
		}
	}
	return dashboard.Preference{}, false
}

func TestSQLiteMigratorMovesAllPreferenceFieldsAndIsIdempotent(t *testing.T) {
	store, migrator := newSQLitePreferenceMigrator(t)
	fromID := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	toID := "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	want := dashboard.Preference{Alias: "Vault", Icon: "shield", Notes: "production service",
		ServiceURL: "https://vault.example.test", SortOrder: 17, Visible: false, Pinned: true}
	putSQLitePreference(t, store, fromID, want)

	for attempt := 0; attempt < 2; attempt++ {
		if err := migrator.MigrateContainer(context.Background(), sqlitePreferenceNodeID, fromID, toID); err != nil {
			t.Fatalf("migration attempt %d: %v", attempt+1, err)
		}
	}
	got, exists := getSQLitePreference(t, store, toID)
	if !exists || got.Alias != want.Alias || got.Icon != want.Icon || got.Notes != want.Notes ||
		got.ServiceURL != want.ServiceURL || got.SortOrder != want.SortOrder || got.Visible != want.Visible || got.Pinned != want.Pinned {
		t.Fatalf("replacement preference lost fields: got=%+v exists=%t want=%+v", got, exists, want)
	}
	if old, exists := getSQLitePreference(t, store, fromID); exists {
		t.Fatalf("old identity retained preference after successful move: %+v", old)
	}
}

func TestSQLiteMigratorConflictRollsBackWithoutOverwritingEitherIdentity(t *testing.T) {
	store, migrator := newSQLitePreferenceMigrator(t)
	fromID := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	toID := "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	oldValue := dashboard.Preference{Alias: "old alias", Icon: "shield", Notes: "old note", ServiceURL: "https://old.example.test", SortOrder: 3, Visible: true, Pinned: false}
	newValue := dashboard.Preference{Alias: "new alias", Icon: "database", Notes: "new note", ServiceURL: "https://new.example.test", SortOrder: 9, Visible: false, Pinned: true}
	putSQLitePreference(t, store, fromID, oldValue)
	putSQLitePreference(t, store, toID, newValue)

	err := migrator.MigrateContainer(context.Background(), sqlitePreferenceNodeID, fromID, toID)
	if !errors.Is(err, ErrIdentityConflict) {
		t.Fatalf("migration error = %v, want ErrIdentityConflict", err)
	}
	oldGot, oldExists := getSQLitePreference(t, store, fromID)
	newGot, newExists := getSQLitePreference(t, store, toID)
	if !oldExists || oldGot.Alias != oldValue.Alias || oldGot.Icon != oldValue.Icon || oldGot.Notes != oldValue.Notes ||
		oldGot.ServiceURL != oldValue.ServiceURL || oldGot.SortOrder != oldValue.SortOrder || oldGot.Visible != oldValue.Visible || oldGot.Pinned != oldValue.Pinned {
		t.Fatalf("conflict modified the source row: got=%+v exists=%t want=%+v", oldGot, oldExists, oldValue)
	}
	if !newExists || newGot.Alias != newValue.Alias || newGot.Icon != newValue.Icon || newGot.Notes != newValue.Notes ||
		newGot.ServiceURL != newValue.ServiceURL || newGot.SortOrder != newValue.SortOrder || newGot.Visible != newValue.Visible || newGot.Pinned != newValue.Pinned {
		t.Fatalf("conflict modified the destination row: got=%+v exists=%t want=%+v", newGot, newExists, newValue)
	}
}

func TestSQLiteMigratorWithoutOldPreferenceSucceeds(t *testing.T) {
	store, migrator := newSQLitePreferenceMigrator(t)
	fromID := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	toID := "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	if err := migrator.MigrateContainer(context.Background(), sqlitePreferenceNodeID, fromID, toID); err != nil {
		t.Fatalf("migration without a source preference should be a no-op: %v", err)
	}
	if _, exists := getSQLitePreference(t, store, toID); exists {
		t.Fatal("migration without a source preference created a destination preference")
	}
}
