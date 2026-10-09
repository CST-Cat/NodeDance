package probes

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/CST-Cat/NodeDance/internal/core/audit"
	"github.com/CST-Cat/NodeDance/internal/core/storage"
	"github.com/CST-Cat/NodeDance/internal/protocol"
)

const testNodeID = "2cf3b66b-93b1-4c8d-9485-699e94da9c21"

func TestProbeConfigRunHistoryThresholdsAndRevision(t *testing.T) {
	ctx := context.Background()
	store, err := storage.Open(ctx, filepath.Join(t.TempDir(), "data"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.DB.Exec(`INSERT INTO nodes(id,display_name,status,created_at,updated_at) VALUES(?, 'probe test', 'pending', 1, 1)`, testNodeID); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	service := New(store.DB, func() time.Time { return now })
	config, err := service.Create(ctx, Config{NodeID: testNodeID, Name: "Homepage", Kind: protocol.ProbeHTTP,
		Target: "http://127.0.0.1:18080/health", ExpectedHTTPStatus: 200, IntervalSeconds: 30, TimeoutSeconds: 3, Enabled: true},
		audit.Event{Action: "probe_create", Outcome: "succeeded", RemoteAddr: "127.0.0.1"})
	if err != nil {
		t.Fatal(err)
	}
	if config.Status != protocol.ProbeResultUnknown || config.Revision != 1 || config.ID == "" {
		t.Fatalf("unexpected initial configuration: %+v", config)
	}
	runConfig, firstRun, claimed, err := service.Claim(ctx, config.ID, 9, now)
	if err != nil || !claimed || runConfig.ID != config.ID {
		t.Fatalf("claim due probe: claimed=%v config=%+v err=%v", claimed, runConfig, err)
	}
	if _, _, claimedAgain, err := service.Claim(ctx, config.ID, 9, now); err != nil || claimedAgain {
		t.Fatalf("overlapping run claimed=%v err=%v", claimedAgain, err)
	}
	changed := runConfig
	changed.Name = "Renamed"
	changed.Revision = 1
	changed.Enabled = true
	updated, err := service.Update(ctx, changed, audit.Event{Action: "probe_update", Outcome: "succeeded", RemoteAddr: "127.0.0.1"})
	if err != nil || updated.Revision != 2 {
		t.Fatalf("update probe: revision=%d err=%v", updated.Revision, err)
	}
	if err := service.Complete(ctx, protocol.ProbeReport{ProbeID: firstRun.ProbeID, RunID: firstRun.RunID, NodeID: testNodeID,
		Status: protocol.ProbeResultHealthy, HTTPStatus: 200}, 9, now); err != nil {
		t.Fatal(err)
	}
	updated, err = service.Get(ctx, config.ID)
	if err != nil || updated.Status != protocol.ProbeResultUnknown || updated.Revision != 2 {
		t.Fatalf("old-revision result mutated current configuration: %+v err=%v", updated, err)
	}

	for i := 0; i < 3; i++ {
		now = now.Add(31 * time.Second)
		_, run, claimed, err := service.Claim(ctx, config.ID, 10, now)
		if err != nil || !claimed {
			t.Fatalf("claim failure %d: claimed=%v err=%v", i+1, claimed, err)
		}
		if err := service.Complete(ctx, protocol.ProbeReport{ProbeID: config.ID, RunID: run.RunID, NodeID: testNodeID,
			Status: protocol.ProbeResultUnhealthy, ErrorCode: "connection_failed", LatencyMS: 12}, 10, now.Add(time.Millisecond)); err != nil {
			t.Fatal(err)
		}
	}
	current, err := service.Get(ctx, config.ID)
	if err != nil || current.Status != protocol.ProbeResultUnhealthy || current.ConsecutiveFailures != 3 {
		t.Fatalf("three failures did not mark probe unhealthy: %+v err=%v", current, err)
	}
	for i := 0; i < 2; i++ {
		now = now.Add(31 * time.Second)
		_, run, claimed, err := service.Claim(ctx, config.ID, 10, now)
		if err != nil || !claimed {
			t.Fatalf("claim recovery %d: claimed=%v err=%v", i+1, claimed, err)
		}
		if err := service.Complete(ctx, protocol.ProbeReport{ProbeID: config.ID, RunID: run.RunID, NodeID: testNodeID,
			Status: protocol.ProbeResultHealthy, HTTPStatus: 200, LatencyMS: 8}, 10, now.Add(time.Millisecond)); err != nil {
			t.Fatal(err)
		}
	}
	current, err = service.Get(ctx, config.ID)
	if err != nil || current.Status != protocol.ProbeResultHealthy || current.ConsecutiveSuccesses != 2 {
		t.Fatalf("two successes did not recover probe: %+v err=%v", current, err)
	}
	history, err := service.History(ctx, config.ID, 200)
	if err != nil || len(history) != 6 {
		t.Fatalf("history length=%d err=%v, want 6 (including superseded result)", len(history), err)
	}
	var auditCount int
	if err := store.DB.QueryRow(`SELECT count(*) FROM audit_entries WHERE action LIKE 'probe_%'`).Scan(&auditCount); err != nil || auditCount != 2 {
		t.Fatalf("probe config audit entries=%d err=%v", auditCount, err)
	}
}

func TestProbeDisableDeleteAndPendingRecovery(t *testing.T) {
	ctx := context.Background()
	store, err := storage.Open(ctx, filepath.Join(t.TempDir(), "data"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.DB.Exec(`INSERT INTO nodes(id,display_name,status,created_at,updated_at) VALUES(?, 'probe test', 'pending', 1, 1)`, testNodeID); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	service := New(store.DB, func() time.Time { return now })
	config, err := service.Create(ctx, Config{NodeID: testNodeID, Name: "Port", Kind: protocol.ProbeTCP,
		Target: "tcp://127.0.0.1:8080", IntervalSeconds: 30, TimeoutSeconds: 3, Enabled: true},
		audit.Event{Action: "probe_create", Outcome: "succeeded", RemoteAddr: "127.0.0.1"})
	if err != nil {
		t.Fatal(err)
	}
	_, run, claimed, err := service.Claim(ctx, config.ID, 7, now)
	if err != nil || !claimed {
		t.Fatalf("claim pending run: %v %v", claimed, err)
	}
	if err := service.RecoverPending(ctx, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := service.Complete(ctx, protocol.ProbeReport{ProbeID: run.ProbeID, RunID: run.RunID, NodeID: testNodeID,
		Status: protocol.ProbeResultHealthy}, 7, now.Add(2*time.Second)); !errors.Is(err, ErrRunState) {
		t.Fatalf("recovered run accepted late report: %v", err)
	}
	config, err = service.Get(ctx, config.ID)
	if err != nil {
		t.Fatal(err)
	}
	config.Enabled, config.Revision = false, 1
	config, err = service.Update(ctx, config, audit.Event{Action: "probe_disable", Outcome: "succeeded", RemoteAddr: "127.0.0.1"})
	if err != nil || config.Enabled || config.Revision != 2 {
		t.Fatalf("disable probe failed: %+v err=%v", config, err)
	}
	if due, err := service.Due(ctx, now.Add(24*time.Hour), 64); err != nil || len(due) != 0 {
		t.Fatalf("disabled probe scheduled: due=%+v err=%v", due, err)
	}
	if err := service.Delete(ctx, config.ID, config.Revision, audit.Event{Action: "probe_delete", Outcome: "succeeded", RemoteAddr: "127.0.0.1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Get(ctx, config.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("deleted probe remained active: %v", err)
	}
	if history, err := service.History(ctx, config.ID, 100); err != nil || len(history) != 1 || history[0].Status != protocol.ProbeResultUnknown {
		t.Fatalf("delete lost probe history: %+v err=%v", history, err)
	}
}

func TestOfflineNodeMakesProbeUnknownWithoutCountingFailure(t *testing.T) {
	ctx := context.Background()
	store, err := storage.Open(ctx, filepath.Join(t.TempDir(), "data"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.DB.Exec(`INSERT INTO nodes(id,display_name,status,created_at,updated_at) VALUES(?, 'probe test', 'pending', 1, 1)`, testNodeID); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	service := New(store.DB, func() time.Time { return now })
	config, err := service.Create(ctx, Config{NodeID: testNodeID, Name: "Health", Kind: protocol.ProbeHTTP,
		Target: "http://127.0.0.1/health", ExpectedHTTPStatus: 200, IntervalSeconds: 10, TimeoutSeconds: 3, Enabled: true},
		audit.Event{Action: "probe_create", Outcome: "succeeded", RemoteAddr: "127.0.0.1"})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if i > 0 {
			now = now.Add(11 * time.Second)
		}
		_, run, claimed, err := service.Claim(ctx, config.ID, 3, now)
		if err != nil || !claimed {
			t.Fatalf("claim healthy probe %d: claimed=%v err=%v", i+1, claimed, err)
		}
		if err := service.Complete(ctx, protocol.ProbeReport{ProbeID: config.ID, RunID: run.RunID, NodeID: testNodeID,
			Status: protocol.ProbeResultHealthy, HTTPStatus: 200}, 3, now); err != nil {
			t.Fatal(err)
		}
	}
	current, err := service.Get(ctx, config.ID)
	if err != nil || current.Status != protocol.ProbeResultHealthy {
		t.Fatalf("probe did not reach healthy state: %+v err=%v", current, err)
	}
	if err := service.MarkNodeUnknown(ctx, testNodeID, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	current, err = service.Get(ctx, config.ID)
	if err != nil || current.Status != protocol.ProbeResultUnknown || current.LastErrorCode != "node_offline" || current.ConsecutiveFailures != 0 {
		t.Fatalf("offline node was recorded as a service failure: %+v err=%v", current, err)
	}
	history, err := service.History(ctx, config.ID, 10)
	if err != nil || len(history) != 3 || history[0].Status != protocol.ProbeResultUnknown || history[0].ErrorCode != "node_offline" {
		t.Fatalf("offline state missing from history: %+v err=%v", history, err)
	}
	if err := service.MarkNodeUnknown(ctx, testNodeID, now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	current, err = service.Get(ctx, config.ID)
	if err != nil || current.Status != protocol.ProbeResultUnknown || current.LastErrorCode != "node_offline" ||
		current.ConsecutiveFailures != 0 || current.NextDueAt != now.Add(12*time.Second).Format(time.RFC3339Nano) {
		t.Fatalf("repeated offline transition was not deferred cleanly: %+v err=%v", current, err)
	}
	history, err = service.History(ctx, config.ID, 10)
	if err != nil || len(history) != 3 {
		t.Fatalf("repeated offline transition added duplicate history: %+v err=%v", history, err)
	}
}
