package dashboard

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/CST-Cat/NodeDance/internal/core/storage"
)

func TestStableContainerAndComposeServicePreferenceIdentity(t *testing.T) {
	node := "00000000-0000-4000-8000-000000000001"
	first, ok := ContainerIdentity("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	if !ok {
		t.Fatal("full 64-character container ID was rejected")
	}
	second, ok := ContainerIdentity("bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")
	if !ok || first == second {
		t.Fatal("a same-name replacement container would inherit the old container preference")
	}
	compose, ok := ComposeServiceIdentity(node, "demo", "/srv/demo", "/srv/demo/compose.yaml", "web")
	if !ok {
		t.Fatal("valid Compose identity was rejected")
	}
	composeReplica, ok := ComposeServiceIdentity(node, "demo", "/srv/demo", "/srv/demo/compose.yaml", "web")
	if !ok || composeReplica != compose {
		t.Fatal("replicas of one node/project/service did not share the stable preference key")
	}
	for _, input := range [][5]string{
		{node, "other-project", "/srv/demo", "/srv/demo/compose.yaml", "web"},
		{node, "demo", "/srv/demo", "/srv/demo/compose.yaml", "api"},
		{"00000000-0000-4000-8000-000000000002", "demo", "/srv/demo", "/srv/demo/compose.yaml", "web"},
		{node, "demo", "/srv/second", "/srv/second/compose.yaml", "web"},
	} {
		key, valid := ComposeServiceIdentity(input[0], input[1], input[2], input[3], input[4])
		if !valid || key == compose {
			t.Fatalf("distinct node/project/service identity reused preference key %q for %v", key, input)
		}
	}
}

func TestPreferencesAndSettingsPersistAcrossCoreDatabaseReopen(t *testing.T) {
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "core-data")
	opened, err := storage.Open(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	nodeID := "00000000-0000-4000-8000-000000000001"
	now := time.Now().UnixNano()
	if _, err := opened.DB.Exec(`INSERT INTO nodes(id, display_name, status, created_at, updated_at) VALUES(?, 'node', 'offline', ?, ?)`, nodeID, now, now); err != nil {
		t.Fatal(err)
	}
	repository := Repository{DB: opened.DB, Now: func() time.Time { return time.Unix(1, 0) }}
	key, _ := ContainerIdentity("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	preference := Preference{NodeID: nodeID, TargetKind: "container", Identity: key, Alias: "Vault", Icon: "shield", Notes: "production", ServiceURL: "https://vault.example.test", SortOrder: 2, Visible: true, Pinned: true}
	if err := repository.Put(ctx, preference); err != nil {
		t.Fatal(err)
	}
	wantSettings := Settings{ViewMode: "manage", GroupBy: "compose", SortBy: "custom", FeaturedLimit: 7, CustomFields: []string{"state", "ports", "health", "image"}}
	if err := repository.PutSettings(ctx, wantSettings); err != nil {
		t.Fatal(err)
	}
	if err := opened.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := storage.Open(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	repository.DB = reopened.DB
	got, err := repository.List(ctx, nodeID)
	if err != nil || len(got) != 1 || got[0] != preference {
		t.Fatalf("persisted preference=%+v err=%v want=%+v", got, err, preference)
	}
	gotSettings, err := repository.GetSettings(ctx)
	if err != nil || gotSettings.ViewMode != wantSettings.ViewMode || gotSettings.GroupBy != wantSettings.GroupBy ||
		gotSettings.SortBy != wantSettings.SortBy || gotSettings.FeaturedLimit != wantSettings.FeaturedLimit ||
		len(gotSettings.CustomFields) != len(wantSettings.CustomFields) {
		t.Fatalf("persisted settings=%+v err=%v want=%+v", gotSettings, err, wantSettings)
	}
}

func TestPreferencesRejectUnsafeURLsIconsAndIncompleteContainerIdentity(t *testing.T) {
	nodeID := "00000000-0000-4000-8000-000000000001"
	base := Preference{NodeID: nodeID, TargetKind: "container", Identity: "container:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Visible: true}
	for name, mutate := range map[string]func(*Preference){
		"javascript URL":   func(p *Preference) { p.ServiceURL = "javascript:alert(1)" },
		"userinfo URL":     func(p *Preference) { p.ServiceURL = "https://user:password@example.test" },
		"remote icon":      func(p *Preference) { p.Icon = "https://attacker.test/icon.svg" },
		"short id":         func(p *Preference) { p.Identity = "container:aaaaaaaaaaaaaaaa" },
		"invalid node key": func(p *Preference) { p.NodeID = "not-a-uuid" },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := base
			mutate(&candidate)
			if validPreference(candidate) {
				t.Fatal("unsafe or incomplete preference was accepted")
			}
		})
	}
}
