package dashboard_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/CST-Cat/NodeDance/internal/core/dashboard"
	"github.com/CST-Cat/NodeDance/internal/core/geolocation"
	"github.com/CST-Cat/NodeDance/internal/core/storage"
)

const testNodeID = "123e4567-e89b-42d3-a456-426614174000"

func newPreferencesRepository(t *testing.T) dashboard.Repository {
	t.Helper()
	database, err := storage.Open(context.Background(), t.TempDir())
	if err != nil {
		t.Fatalf("open Core database: %v", err)
	}
	t.Cleanup(func() { database.Close() })
	if _, err := database.DB.ExecContext(context.Background(), `INSERT INTO nodes(id,display_name,status,created_at,updated_at) VALUES('`+testNodeID+`','Prefs VPS','online',1,1)`); err != nil {
		t.Fatalf("insert preference node: %v", err)
	}
	return dashboard.Repository{DB: database.DB, Now: func() time.Time { return time.Unix(1730000000, 0) }}
}

func TestIdentityHelpers(t *testing.T) {
	if dashboard.NodeIdentity(testNodeID) != "node:"+testNodeID {
		t.Fatal("node identity format changed")
	}
	if _, ok := dashboard.ContainerIdentity(strings.Repeat("a", 64)); !ok {
		t.Fatal("full lowercase container ID was rejected")
	}
	for _, invalid := range []string{strings.Repeat("A", 64), strings.Repeat("a", 63), strings.Repeat("g", 64)} {
		if _, ok := dashboard.ContainerIdentity(invalid); ok {
			t.Fatalf("invalid container ID %q was accepted", invalid)
		}
	}
	first, ok := dashboard.ComposeServiceIdentity(testNodeID, "web", "/srv/app", "/srv/app/compose.yml", "api")
	if !ok {
		t.Fatal("valid compose service identity was rejected")
	}
	again, _ := dashboard.ComposeServiceIdentity(testNodeID, "web", "/srv/app", "/srv/app/compose.yml", "api")
	if first != again {
		t.Fatal("compose service identity is not deterministic")
	}
	otherNode, _ := dashboard.ComposeServiceIdentity("123e4567-e89b-42d3-a456-426614174001", "web", "/srv/app", "/srv/app/compose.yml", "api")
	if first == otherNode {
		t.Fatal("compose service identity leaked across nodes")
	}
	if _, ok := dashboard.ComposeServiceIdentity(testNodeID, " web", "/srv/app", "", "api"); ok {
		t.Fatal("untrimmed project name was accepted")
	}
	if _, ok := dashboard.ComposeServiceIdentity(testNodeID, strings.Repeat("x", 257), "", "", "api"); ok {
		t.Fatal("oversized project name was accepted")
	}
}

func TestPutAndListNodePreferences(t *testing.T) {
	ctx := context.Background()
	repository := newPreferencesRepository(t)
	latitude, longitude := 47.6, -122.3
	if err := repository.Put(ctx, dashboard.Preference{
		NodeID: testNodeID, TargetKind: "node", Identity: dashboard.NodeIdentity(testNodeID),
		Alias: "  edge node  ", Icon: "server", Group: " edge ", SortOrder: 3, Visible: true, Pinned: true,
		CountryCode: "US", SSHHost: "edge.example.com", SSHPort: 2222, SSHUser: "deploy",
		ServiceURL: "https://edge.example.com/status",
		Location:   &geolocation.Position{CountryCode: "US", City: "Seattle", Latitude: &latitude, Longitude: &longitude},
	}); err != nil {
		t.Fatalf("save node preference: %v", err)
	}
	items, err := repository.List(ctx, testNodeID)
	if err != nil || len(items) != 1 {
		t.Fatalf("list node preferences: %v %d", err, len(items))
	}
	item := items[0]
	if item.Alias != "edge node" || item.Group != "edge" || !item.Visible || !item.Pinned {
		t.Fatalf("preference fields not preserved or trimmed: %+v", item)
	}
	if item.SSHHost != "edge.example.com" || item.SSHPort != 2222 || item.SSHUser != "deploy" {
		t.Fatalf("connection preferences were not preserved: %+v", item)
	}
	if item.Location == nil || item.Location.City != "Seattle" || item.CountryCode != "US" {
		t.Fatalf("location preferences were not preserved: %+v", item)
	}

	// SSHPort 0 means "no override" and is stored as the 22 sentinel.
	if err := repository.Put(ctx, dashboard.Preference{
		NodeID: testNodeID, TargetKind: "node", Identity: dashboard.NodeIdentity(testNodeID),
		SSHPort: 0, Visible: true,
	}); err != nil {
		t.Fatalf("save default connection preference: %v", err)
	}
	items, err = repository.List(ctx, testNodeID)
	if err != nil || len(items) != 1 {
		t.Fatalf("re-list node preferences: %v", err)
	}
	if items[0].SSHPort != 22 {
		t.Fatalf("unset SSH port must persist as the 22 sentinel, got %d", items[0].SSHPort)
	}
}

func TestListUnknownNodeIsEmpty(t *testing.T) {
	items, err := newPreferencesRepository(t).List(context.Background(), testNodeID)
	if err != nil || len(items) != 0 {
		t.Fatalf("unknown node preferences: %v %d", err, len(items))
	}
}

func TestPutRejectsInvalidPreferences(t *testing.T) {
	ctx := context.Background()
	repository := newPreferencesRepository(t)
	valid := dashboard.Preference{NodeID: testNodeID, TargetKind: "node", Identity: dashboard.NodeIdentity(testNodeID), Visible: true}
	if err := repository.Put(ctx, valid); err != nil {
		t.Fatalf("baseline preference rejected: %v", err)
	}
	for name, mutate := range map[string]func(*dashboard.Preference){
		"foreign identity":    func(p *dashboard.Preference) { p.Identity = "node:00000000-0000-4000-8000-000000000000" },
		"identity whitespace": func(p *dashboard.Preference) { p.Identity = " " + p.Identity },
		"bad node id":         func(p *dashboard.Preference) { p.NodeID = "not-a-uuid" },
		"unknown icon":        func(p *dashboard.Preference) { p.Icon = "spaceship" },
		"unknown country":     func(p *dashboard.Preference) { p.CountryCode = "ZZ" },
		"bad service url":     func(p *dashboard.Preference) { p.ServiceURL = "https://edge.example.com/#fragment" },
		"userinfo in url":     func(p *dashboard.Preference) { p.ServiceURL = "https://user@edge.example.com" },
		"oversized notes":     func(p *dashboard.Preference) { p.Notes = strings.Repeat("x", 2049) },
		"ssh fields on compose": func(p *dashboard.Preference) {
			p.TargetKind = "compose_project"
			p.Identity = "compose_project:" + strings.Repeat("a", 64)
			p.SSHHost = "edge.example.com"
		},
	} {
		broken := valid
		mutate(&broken)
		if err := repository.Put(ctx, broken); err == nil {
			t.Fatalf("invalid preference was accepted: %s", name)
		}
	}
}

func TestSettingsRoundTripAndDefaults(t *testing.T) {
	ctx := context.Background()
	repository := newPreferencesRepository(t)
	if _, err := repository.GetSettings(ctx); err != nil {
		t.Fatalf("seeded settings row did not load: %v", err)
	}
	if err := repository.PutSettings(ctx, dashboard.Settings{ViewMode: "manage", GroupBy: "node", SortBy: "name", CustomFields: []string{"ports", "health"}}); err != nil {
		t.Fatalf("save settings: %v", err)
	}
	settings, err := repository.GetSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if settings.ViewMode != "manage" || settings.NodeGroupBy != "status" || settings.NodeSortBy != "custom" {
		t.Fatalf("settings defaults were not applied: %+v", settings)
	}
	if len(settings.CustomFields) != 2 || settings.CustomFields[0] != "ports" {
		t.Fatalf("custom fields did not round trip: %+v", settings.CustomFields)
	}
	for _, broken := range []dashboard.Settings{
		{ViewMode: "gallery", GroupBy: "node", SortBy: "name"},
		{ViewMode: "manage", GroupBy: "color", SortBy: "name"},
		{ViewMode: "manage", GroupBy: "node", SortBy: "name", CustomFields: []string{"ports", "ports"}},
		{ViewMode: "manage", GroupBy: "node", SortBy: "name", CustomFields: []string{"secret"}},
	} {
		if err := repository.PutSettings(ctx, broken); err == nil {
			t.Fatalf("invalid settings were accepted: %+v", broken)
		}
	}
}
