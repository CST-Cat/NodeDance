package probes_test

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	agentprobes "github.com/CST-Cat/NodeDance/internal/agent/probes"
	"github.com/CST-Cat/NodeDance/internal/core/audit"
	coreprobes "github.com/CST-Cat/NodeDance/internal/core/probes"
	"github.com/CST-Cat/NodeDance/internal/core/storage"
	"github.com/CST-Cat/NodeDance/internal/protocol"
)

func TestAgentFailureUpdatesCoreProbeHistoryAndState(t *testing.T) {
	ctx := context.Background()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "service unavailable", http.StatusServiceUnavailable)
	}))
	defer server.Close()

	base := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	now := base
	database, err := storage.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatalf("open Core database: %v", err)
	}
	defer database.Close()
	if _, err := database.DB.ExecContext(ctx, `INSERT INTO nodes(id,display_name,status,created_at,updated_at) VALUES('123e4567-e89b-42d3-a456-426614174000','Probe VPS','online',1,1)`); err != nil {
		t.Fatalf("insert probe node: %v", err)
	}
	store := coreprobes.New(database.DB, func() time.Time { return now })
	config, err := store.Create(ctx, coreprobes.Config{
		NodeID: "123e4567-e89b-42d3-a456-426614174000", Name: "service health", Kind: protocol.ProbeHTTP,
		Target: server.URL, ExpectedHTTPStatus: http.StatusOK, IntervalSeconds: 10, TimeoutSeconds: 2, Enabled: true,
	}, newProbeAuditEvent(now))
	if err != nil {
		t.Fatalf("create probe: %v", err)
	}
	executor := agentprobes.NewExecutor()
	for attempt := 0; attempt < 3; attempt++ {
		due, err := store.Due(ctx, now, 16)
		if err != nil || len(due) != 1 || due[0].ID != config.ID {
			t.Fatalf("probe not due for attempt %d: due=%#v err=%v", attempt+1, due, err)
		}
		claimed, run, ok, err := store.Claim(ctx, config.ID, 7, now)
		if err != nil || !ok {
			t.Fatalf("claim probe attempt %d: ok=%v err=%v", attempt+1, ok, err)
		}
		report := executor.Execute(ctx, protocol.ProbeDispatch{
			ProbeID: claimed.ID, RunID: run.RunID, NodeID: claimed.NodeID, Kind: claimed.Kind,
			Target: claimed.Target, ExpectedHTTPStatus: claimed.ExpectedHTTPStatus,
			IntervalSeconds: claimed.IntervalSeconds, TimeoutSeconds: claimed.TimeoutSeconds,
		})
		if report.Status != protocol.ProbeResultUnhealthy || report.HTTPStatus != http.StatusServiceUnavailable || report.ErrorCode != "http_status_mismatch" {
			t.Fatalf("Agent did not report the real service failure: %#v", report)
		}
		if err := store.Complete(ctx, report, 7, now); err != nil {
			t.Fatalf("persist probe attempt %d: %v", attempt+1, err)
		}
		now = now.Add(10 * time.Second)
	}

	result, err := store.Get(ctx, config.ID)
	if err != nil {
		t.Fatalf("read probe result: %v", err)
	}
	if result.Status != protocol.ProbeResultUnhealthy || result.ConsecutiveFailures != 3 || result.LastErrorCode != "http_status_mismatch" {
		t.Fatalf("Core did not mark repeated real failures unhealthy: %#v", result)
	}
	history, err := store.History(ctx, config.ID, 10)
	if err != nil {
		t.Fatalf("read probe history: %v", err)
	}
	if len(history) != 3 || history[0].Status != protocol.ProbeResultUnhealthy || history[0].HTTPStatus == nil || *history[0].HTTPStatus != http.StatusServiceUnavailable {
		t.Fatalf("Core did not retain actual failure history: %#v", history)
	}
}

func newProbeAuditEvent(at time.Time) audit.Event {
	return audit.Event{OccurredAt: at, Action: "probe_create", Outcome: "succeeded", ActorID: sql.NullInt64{Int64: 1, Valid: true}}
}
