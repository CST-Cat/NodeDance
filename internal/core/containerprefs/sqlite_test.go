package containerprefs_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/CST-Cat/NodeDance/internal/core/containerprefs"
	"github.com/CST-Cat/NodeDance/internal/core/dashboard"
	"github.com/CST-Cat/NodeDance/internal/core/storage"
)

func TestPreferenceMoveIsDurableIdempotentAndNodeScoped(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store, err := storage.Open(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if store != nil {
			store.Close()
		}
	}()
	node, other := "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
	for _, id := range []string{node, other} {
		if _, err := store.DB.Exec(`INSERT INTO nodes(id,display_name,status,created_at,updated_at) VALUES(?,?,'offline',1,1)`, id, id); err != nil {
			t.Fatal(err)
		}
	}
	from, to := strings.Repeat("a", 64), strings.Repeat("b", 64)
	source := dashboard.Preference{NodeID: node, TargetKind: "container", Identity: "container:" + from,
		Alias: "database", Icon: "database", Notes: "keep me", ServiceURL: "https://example.test", SortOrder: 7, Visible: true, Pinned: true}
	otherSource := source
	otherSource.NodeID = other
	repository := dashboard.Repository{DB: store.DB}
	for _, p := range []dashboard.Preference{source, otherSource} {
		if err := repository.Put(ctx, p); err != nil {
			t.Fatal(err)
		}
	}
	migrator := containerprefs.NewSQLiteMigrator(store.DB)
	for range 2 {
		if err := migrator.MigrateContainer(ctx, node, from, to); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = storage.Open(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	repository = dashboard.Repository{DB: store.DB}
	items, err := repository.List(ctx, node)
	source.Identity = "container:" + to
	source.SSHPort = 22
	if err != nil || len(items) != 1 || items[0] != source {
		t.Fatalf("moved preferences changed: %+v, %v", items, err)
	}
	items, err = repository.List(ctx, other)
	if err != nil || len(items) != 1 || items[0].Identity != otherSource.Identity {
		t.Fatalf("other node changed: %+v, %v", items, err)
	}
	// A destination already edited by the administrator must not be overwritten.
	conflicting := source
	conflicting.Identity = "container:" + from
	conflicting.Alias = "different"
	if err := repository.Put(ctx, conflicting); err != nil {
		t.Fatal(err)
	}
	migrator = containerprefs.NewSQLiteMigrator(store.DB)
	if err := migrator.MigrateContainer(ctx, node, from, to); !errors.Is(err, containerprefs.ErrIdentityConflict) {
		t.Fatalf("want conflict, got %v", err)
	}
	items, err = repository.List(ctx, node)
	if err != nil || len(items) != 2 {
		t.Fatalf("conflict lost preferences: %+v, %v", items, err)
	}
}
