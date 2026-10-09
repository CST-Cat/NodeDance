package updates

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	agentupdate "github.com/CST-Cat/NodeDance/internal/agent/update"
	"github.com/CST-Cat/NodeDance/internal/core/storage"
)

func TestRolloutIsIdempotentAndStopsAfterFirstFailure(t *testing.T) {
	ctx := context.Background()
	database, err := storage.Open(ctx, filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	store := New(database.DB, func() time.Time { return now })
	nodes := []string{"11111111-1111-4111-8111-111111111111", "22222222-2222-4222-8222-222222222222"}
	for _, id := range nodes {
		if _, err := database.DB.Exec(`INSERT INTO nodes(id,display_name,status,created_at,updated_at) VALUES(?,?,'pending',?,?)`, id, "test node", now.Unix(), now.Unix()); err != nil {
			t.Fatal(err)
		}
	}
	manifest := agentupdate.Manifest{FormatVersion: 1, Version: "1.0.0", OS: "linux", Architecture: "amd64", SHA256: agentupdate.Digest([]byte("fixture")), Size: 7, MinProtocol: 1, MaxProtocol: 1}
	release, err := store.CreateRelease(ctx, "33333333-3333-4333-8333-333333333333", manifest, "/private/agent")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveSettings(ctx, Settings{AutoEnabled: true, WindowStartMinute: 60, WindowEndMinute: 120, BatchSize: 2, ReleaseID: release.ID}); err != nil {
		t.Fatal(err)
	}
	if err := store.StartCampaign(ctx, nodes, release.ID, "2026-10-08", 2); err != nil {
		t.Fatal(err)
	}
	first, err := store.CreateTask(ctx, nodes[0], release.ID, "manual", "", 0)
	if err != nil || first.Status != "queued" {
		t.Fatalf("first rollout task: %+v %v", first, err)
	}
	duplicate, err := store.CreateTask(ctx, nodes[0], release.ID, "manual", "", 0)
	if err != nil || duplicate.ID != first.ID {
		t.Fatalf("duplicate request did not reuse task: %+v %v", duplicate, err)
	}
	if err := store.SetTaskStatus(ctx, first.ID, "dispatched", ""); err != nil {
		t.Fatal(err)
	}
	if err := store.Report(ctx, first.ID, nodes[0], "failed", "candidate did not start"); err != nil {
		t.Fatal(err)
	}
	items, err := store.ListTasks(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("got %d rollout tasks", len(items))
	}
	for _, item := range items {
		if item.NodeID == nodes[0] && item.Status != "failed" {
			t.Errorf("failed node status=%s", item.Status)
		}
		if item.NodeID == nodes[1] && item.Status != "paused" {
			t.Errorf("later node was not paused: %s", item.Status)
		}
	}
	settings, err := store.Settings(ctx)
	if err != nil || !settings.CampaignPaused {
		t.Fatalf("campaign not paused after first failure: %+v %v", settings, err)
	}
}

func TestCoreRestartRequeuesDispatchedUpdateForReconciliation(t *testing.T) {
	ctx := context.Background()
	database, err := storage.Open(ctx, filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	now := time.Now().UTC()
	store := New(database.DB, nil)
	node := "11111111-1111-4111-8111-111111111111"
	_, err = database.DB.Exec(`INSERT INTO nodes(id,display_name,status,created_at,updated_at) VALUES(?,?,'pending',?,?)`, node, "test", now.Unix(), now.Unix())
	if err != nil {
		t.Fatal(err)
	}
	manifest := agentupdate.Manifest{FormatVersion: 1, Version: "1.0.0", OS: "linux", Architecture: "amd64", SHA256: agentupdate.Digest([]byte("fixture")), Size: 7, MinProtocol: 1, MaxProtocol: 1}
	release, err := store.CreateRelease(ctx, "33333333-3333-4333-8333-333333333333", manifest, "/private/agent")
	if err != nil {
		t.Fatal(err)
	}
	task, err := store.CreateTask(ctx, node, release.ID, "manual", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetTaskStatus(ctx, task.ID, "dispatched", ""); err != nil {
		t.Fatal(err)
	}
	if err := store.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	pending, err := store.Pending(ctx, 10)
	if err != nil || len(pending) != 1 || pending[0].Status != "deferred" {
		t.Fatalf("recovered pending task=%+v err=%v", pending, err)
	}
}

func TestAutomaticCampaignReleasesOneNodeAfterConfirmedSuccess(t *testing.T) {
	ctx := context.Background()
	database, err := storage.Open(ctx, filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	store := New(database.DB, func() time.Time { return now })
	nodes := []string{
		"11111111-1111-4111-8111-111111111111",
		"22222222-2222-4222-8222-222222222222",
		"33333333-3333-4333-8333-333333333333",
		"44444444-4444-4444-8444-444444444444",
	}
	for _, id := range nodes {
		if _, err := database.DB.Exec(`INSERT INTO nodes(id,display_name,status,created_at,updated_at) VALUES(?,?,'pending',?,?)`, id, "test node", now.Unix(), now.Unix()); err != nil {
			t.Fatal(err)
		}
	}
	manifest := agentupdate.Manifest{FormatVersion: 1, Version: "1.0.0", OS: "linux", Architecture: "amd64", SHA256: agentupdate.Digest([]byte("fixture")), Size: 7, MinProtocol: 1, MaxProtocol: 1}
	release, err := store.CreateRelease(ctx, "55555555-5555-4555-8555-555555555555", manifest, "/private/agent")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveSettings(ctx, Settings{AutoEnabled: true, WindowStartMinute: 60, WindowEndMinute: 120, BatchSize: 2, ReleaseID: release.ID}); err != nil {
		t.Fatal(err)
	}
	if err := store.StartCampaign(ctx, nodes, release.ID, "2026-10-08", 2); err != nil {
		t.Fatal(err)
	}
	assertNext := func(wantNode string, wantBatch int) Task {
		t.Helper()
		pending, err := store.Pending(ctx, 64)
		if err != nil || len(pending) != 1 {
			t.Fatalf("pending campaign tasks = %+v, %v; want exactly the next node", pending, err)
		}
		if pending[0].NodeID != wantNode || pending[0].BatchNumber != wantBatch {
			t.Fatalf("next task = node %s batch %d, want node %s batch %d", pending[0].NodeID, pending[0].BatchNumber, wantNode, wantBatch)
		}
		return pending[0]
	}
	first := assertNext(nodes[0], 0)
	if err := store.SetTaskStatus(ctx, first.ID, "dispatched", ""); err != nil {
		t.Fatal(err)
	}
	if pending, err := store.Pending(ctx, 64); err != nil || len(pending) != 0 {
		t.Fatalf("next node was dispatched before first completed: %+v %v", pending, err)
	}
	if err := store.Report(ctx, first.ID, first.NodeID, "prepared", ""); err != nil {
		t.Fatal(err)
	}
	if pending, err := store.Pending(ctx, 64); err != nil || len(pending) != 0 {
		t.Fatalf("next node was dispatched before candidate confirmation: %+v %v", pending, err)
	}
	if err := store.ReconcileVersion(ctx, first.NodeID, manifest.Version); err != nil {
		t.Fatal(err)
	}
	second := assertNext(nodes[1], 0)
	if err := store.SetTaskStatus(ctx, second.ID, "dispatched", ""); err != nil {
		t.Fatal(err)
	}
	if err := store.Report(ctx, second.ID, second.NodeID, "prepared", ""); err != nil {
		t.Fatal(err)
	}
	if err := store.ReconcileVersion(ctx, second.NodeID, manifest.Version); err != nil {
		t.Fatal(err)
	}
	third := assertNext(nodes[2], 1)
	if err := store.SetTaskStatus(ctx, third.ID, "dispatched", ""); err != nil {
		t.Fatal(err)
	}
	if err := store.Report(ctx, third.ID, third.NodeID, "failed", "candidate failed to start"); err != nil {
		t.Fatal(err)
	}
	if pending, err := store.Pending(ctx, 64); err != nil || len(pending) != 0 {
		t.Fatalf("failure did not stop later campaign nodes: %+v %v", pending, err)
	}
	tasks, err := store.ListTasks(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	for _, task := range tasks {
		if task.NodeID == nodes[3] && task.Status != "paused" {
			t.Fatalf("later node status = %q, want paused", task.Status)
		}
	}
}
