package compose_test

import (
	"context"
	"strings"
	"testing"
	"time"

	corecompose "github.com/CST-Cat/NodeDance/internal/core/compose"
	"github.com/CST-Cat/NodeDance/internal/core/storage"
	"github.com/CST-Cat/NodeDance/internal/protocol"
)

const composeNodeID = "123e4567-e89b-42d3-a456-426614174000"

func newComposeStore(t *testing.T) *corecompose.Store {
	t.Helper()
	database, err := storage.Open(context.Background(), t.TempDir())
	if err != nil {
		t.Fatalf("open Core database: %v", err)
	}
	t.Cleanup(func() { database.Close() })
	store, err := corecompose.NewStore(database.DB, corecompose.Options{Now: func() time.Time { return time.Unix(1730000000, 0) }})
	if err != nil {
		t.Fatalf("new compose store: %v", err)
	}
	if _, err := database.DB.Exec(`INSERT INTO nodes(id,display_name,status,created_at,updated_at) VALUES('` + composeNodeID + `','Compose VPS','online',1,1)`); err != nil {
		t.Fatalf("insert compose node: %v", err)
	}
	if _, err := database.DB.Exec(`INSERT INTO nodes(id,display_name,status,created_at,updated_at) VALUES('123e4567-e89b-42d3-a456-426614174001','Other VPS','online',1,1)`); err != nil {
		t.Fatalf("insert other node: %v", err)
	}
	return store
}

func projectRef(name, workingDirectory string) protocol.ComposeProjectRef {
	configFiles := []string{workingDirectory + "/compose.yaml"}
	return protocol.ComposeProjectRef{
		Name: name, WorkingDirectory: workingDirectory, ConfigFiles: configFiles,
		Key: protocol.ComposeProjectKey(name, workingDirectory, configFiles),
	}
}

func TestRegisterAndListComposeProjects(t *testing.T) {
	ctx := context.Background()
	store := newComposeStore(t)
	if err := store.RegisterProjects(ctx, "not-a-uuid", nil); err == nil {
		t.Fatal("an invalid node ID was accepted")
	}
	ref := projectRef("web", "/srv/web")
	if err := store.RegisterProjects(ctx, composeNodeID, []protocol.ComposeProject{{Ref: ref, ConfigAvailable: true}}); err != nil {
		t.Fatalf("register project: %v", err)
	}
	projects, err := store.Projects(ctx, composeNodeID)
	if err != nil || len(projects) != 1 {
		t.Fatalf("list projects: %v %d", err, len(projects))
	}
	if projects[0].Value.Ref.Key != ref.Key || projects[0].Value.Ref.Name != "web" {
		t.Fatalf("project identity: %+v", projects[0].Value.Ref)
	}
	if !projects[0].Value.ConfigAvailable {
		t.Fatal("config availability was not preserved")
	}
	if projects[0].DiscoveredAt.IsZero() || projects[0].LastSeenAt.IsZero() {
		t.Fatal("discovery timestamps are missing")
	}
}

func TestRegisterUpdatesLastSeenAndPreservesDiscoveredAt(t *testing.T) {
	ctx := context.Background()
	store := newComposeStore(t)
	ref := projectRef("web", "/srv/web")
	if err := store.RegisterProjects(ctx, composeNodeID, []protocol.ComposeProject{{Ref: ref, ConfigAvailable: true}}); err != nil {
		t.Fatal(err)
	}
	// A re-registration without config availability marks it missing but keeps
	// the original discovery timestamp.
	if err := store.RegisterProjects(ctx, composeNodeID, []protocol.ComposeProject{{Ref: ref}}); err != nil {
		t.Fatal(err)
	}
	projects, err := store.Projects(ctx, composeNodeID)
	if err != nil || len(projects) != 1 {
		t.Fatal(err)
	}
	if projects[0].Value.ConfigAvailable || projects[0].Value.ConfigReason != "config_missing" {
		t.Fatalf("missing config must be reported: %+v", projects[0].Value)
	}
	if projects[0].DiscoveredAt.Unix() != 1730000000 || projects[0].LastSeenAt.Unix() != 1730000000 {
		t.Fatalf("timestamps: %v %v", projects[0].DiscoveredAt, projects[0].LastSeenAt)
	}
}

func TestRegisterRejectsInvalidProjectRefs(t *testing.T) {
	ctx := context.Background()
	store := newComposeStore(t)
	bad := projectRef("web", "/srv/web")
	bad.Name = ""
	if err := store.RegisterProjects(ctx, composeNodeID, []protocol.ComposeProject{{Ref: bad}}); err == nil {
		t.Fatal("an unnamed project ref was accepted")
	}
	evil := projectRef("web", "/srv/web")
	evil.WorkingDirectory = strings.Repeat("x", 5000)
	if err := store.RegisterProjects(ctx, composeNodeID, []protocol.ComposeProject{{Ref: evil}}); err == nil {
		t.Fatal("an oversized working directory was accepted")
	}
	if projects, err := store.Projects(ctx, composeNodeID); err != nil || len(projects) != 0 {
		t.Fatalf("rejected registrations must not persist: %v %d", err, len(projects))
	}
}

func TestProjectsListIsolatedPerNode(t *testing.T) {
	ctx := context.Background()
	store := newComposeStore(t)
	other := "123e4567-e89b-42d3-a456-426614174001"
	ref := projectRef("web", "/srv/web")
	if err := store.RegisterProjects(ctx, composeNodeID, []protocol.ComposeProject{{Ref: ref}}); err != nil {
		t.Fatal(err)
	}
	projects, err := store.Projects(ctx, other)
	if err != nil || len(projects) != 0 {
		t.Fatalf("another node must see no projects: %v %d", err, len(projects))
	}
}
