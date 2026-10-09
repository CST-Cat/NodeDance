package server

import (
	"context"
	"database/sql"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/CST-Cat/NodeDance/internal/core/audit"
	"github.com/CST-Cat/NodeDance/internal/core/probes"
	"github.com/CST-Cat/NodeDance/internal/protocol"
)

func TestOfflineProbeSchedulerDefersWithoutPendingRunsOrDuplicateHistory(t *testing.T) {
	var nowNS atomic.Int64
	initial := time.Now().UTC().Truncate(time.Second)
	nowNS.Store(initial.UnixNano())
	now := func() time.Time { return time.Unix(0, nowNS.Load()).UTC() }
	core, err := New("s14-offline-scheduler", Options{DataDir: filepath.Join(t.TempDir(), "data"),
		Development: true, PublicOrigin: "https://panel.test", Now: now})
	if err != nil {
		t.Fatal(err)
	}
	defer core.Close()

	enrollment, err := core.agents.CreateEnrollment(context.Background(), "offline probe Agent", "127.0.0.1", sql.NullInt64{Int64: 1, Valid: true})
	if err != nil {
		t.Fatal(err)
	}
	config, err := core.probes.Create(context.Background(), probes.Config{NodeID: enrollment.NodeID, Name: "Loopback", Kind: protocol.ProbeHTTP,
		Target: "http://127.0.0.1:18080/health", ExpectedHTTPStatus: 200, IntervalSeconds: 30, TimeoutSeconds: 3, Enabled: true},
		audit.Event{Action: "probe_create", Outcome: "succeeded", RemoteAddr: "127.0.0.1"})
	if err != nil {
		t.Fatal(err)
	}

	waitForOffline := func(wantNextDue time.Time) []probes.Run {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			current, getErr := core.probes.Get(context.Background(), config.ID)
			if getErr == nil && current.Status == protocol.ProbeResultUnknown && current.LastErrorCode == "node_offline" &&
				current.NextDueAt == wantNextDue.Format(time.RFC3339Nano) {
				history, historyErr := core.probes.History(context.Background(), config.ID, 10)
				if historyErr != nil {
					t.Fatal(historyErr)
				}
				return history
			}
			time.Sleep(10 * time.Millisecond)
		}
		current, _ := core.probes.Get(context.Background(), config.ID)
		t.Fatalf("offline scheduler did not persist/defer probe: %+v", current)
		return nil
	}

	firstHistory := waitForOffline(initial.Add(30 * time.Second))
	if len(firstHistory) != 1 || firstHistory[0].Status != protocol.ProbeResultUnknown || firstHistory[0].ErrorCode != "node_offline" {
		t.Fatalf("offline transition history=%+v, want one node_offline unknown record", firstHistory)
	}
	var pending int
	if err := core.store.DB.QueryRow(`SELECT count(*) FROM service_probe_runs WHERE probe_id=? AND status='pending'`, config.ID).Scan(&pending); err != nil || pending != 0 {
		t.Fatalf("offline scheduler left pending runs=%d err=%v", pending, err)
	}

	nextCheck := initial.Add(30 * time.Second)
	nowNS.Store(nextCheck.UnixNano())
	if err := core.dispatchDueServiceProbes(context.Background()); err != nil {
		t.Fatal(err)
	}
	repeatedHistory := waitForOffline(nextCheck.Add(30 * time.Second))
	if len(repeatedHistory) != 1 || repeatedHistory[0].Status != protocol.ProbeResultUnknown || repeatedHistory[0].ErrorCode != "node_offline" {
		t.Fatalf("repeated offline check duplicated history: %+v", repeatedHistory)
	}
	if err := core.store.DB.QueryRow(`SELECT count(*) FROM service_probe_runs WHERE probe_id=? AND status='pending'`, config.ID).Scan(&pending); err != nil || pending != 0 {
		t.Fatalf("repeated offline scheduler left pending runs=%d err=%v", pending, err)
	}
}
