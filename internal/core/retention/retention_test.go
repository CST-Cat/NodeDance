package retention_test

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/CST-Cat/NodeDance/internal/core/retention"
	"github.com/CST-Cat/NodeDance/internal/core/storage"
)

const retentionNodeID = "123e4567-e89b-42d3-a456-426614174000"

type retentionFixture struct {
	database *sql.DB
	now      time.Time
	cutoff   time.Time
}

func newRetentionFixture(t *testing.T) retentionFixture {
	t.Helper()
	database, err := storage.Open(context.Background(), t.TempDir())
	if err != nil {
		t.Fatalf("open Core database: %v", err)
	}
	t.Cleanup(func() { database.Close() })
	now := time.Unix(1800000000, 0).UTC()
	if _, err := database.DB.Exec(`INSERT INTO nodes(id,display_name,status,created_at,updated_at) VALUES(?,?,?,?,1)`,
		retentionNodeID, "Retention VPS", "online", 1); err != nil {
		t.Fatal(err)
	}
	if _, err := database.DB.Exec(`INSERT INTO service_probes(id,node_id,name,kind,target,interval_seconds,timeout_seconds,enabled,revision,status,next_due_at,created_at,updated_at)
		VALUES('probe-1',?,'web','http','http://127.0.0.1:1',60,2,1,1,'unknown',0,1,1)`, retentionNodeID); err != nil {
		t.Fatal(err)
	}
	return retentionFixture{database: database.DB, now: now, cutoff: now.Add(-90 * 24 * time.Hour)}
}

func (f retentionFixture) exec(t *testing.T, query string, args ...any) {
	t.Helper()
	if _, err := f.database.Exec(query, args...); err != nil {
		t.Fatalf("seed %q: %v", query, err)
	}
}

func (f retentionFixture) count(t *testing.T, query string, args ...any) int64 {
	t.Helper()
	var count int64
	if err := f.database.QueryRow(query, args...).Scan(&count); err != nil {
		t.Fatalf("count %q: %v", query, err)
	}
	return count
}

func TestCleanupDeletesOnlyStrictlyExpiredRows(t *testing.T) {
	f := newRetentionFixture(t)
	ctx := context.Background()
	cutoffSeconds := f.cutoff.Unix()
	if f.cutoff.Nanosecond() != 0 {
		cutoffSeconds++
	}
	cutoffNanos := f.cutoff.UnixNano()

	// Audit entries: strictly older rows go, rows on the cutoff stay.
	f.exec(t, `INSERT INTO audit_entries(occurred_at,action,outcome,remote_addr) VALUES(?,?,?,?)`,
		cutoffSeconds-1, "login", "succeeded", "expired")
	f.exec(t, `INSERT INTO audit_entries(occurred_at,action,outcome,remote_addr) VALUES(?,?,?,?)`,
		cutoffSeconds, "login", "succeeded", "boundary")
	f.exec(t, `INSERT INTO audit_entries(occurred_at,action,outcome,remote_addr) VALUES(?,?,?,?)`,
		cutoffSeconds+100, "login", "succeeded", "fresh")

	result, err := retention.Cleanup(ctx, f.database, f.now, 90*24*time.Hour)
	if err != nil {
		t.Fatalf("cleanup: %v", err)
	}
	if result.AuditEntries != 1 {
		t.Fatalf("expired audit entries: %+v", result)
	}
	if f.count(t, `SELECT count(*) FROM audit_entries WHERE remote_addr='boundary'`) != 1 ||
		f.count(t, `SELECT count(*) FROM audit_entries WHERE remote_addr='fresh'`) != 1 ||
		f.count(t, `SELECT count(*) FROM audit_entries WHERE remote_addr='expired'`) != 0 {
		t.Fatal("audit cutoff boundary is not strict")
	}
	_ = cutoffNanos
}

func TestCleanupKeepsUnresolvedAndClaimedTasks(t *testing.T) {
	f := newRetentionFixture(t)
	ctx := context.Background()
	cutoffNanos := f.cutoff.UnixNano()
	terminal := `INSERT INTO core_tasks(task_id,node_id,idempotency_key,request_digest,accepted_generation,target_id,resource_key,action,intent_json,status,delivery_state,finished_at_ns,created_at_ns,updated_at_ns)
		VALUES(?,?,?,?,1,'t','r','start','{}','succeeded','done',?,?,?)`

	f.exec(t, terminal, "task-expired", retentionNodeID, "k1", make([]byte, 32), cutoffNanos-1, cutoffNanos-2, cutoffNanos-1)
	f.exec(t, `INSERT INTO core_task_audit_events(node_id,task_id,occurred_at_ns,event) VALUES(?,?,?,'accepted')`, retentionNodeID, "task-expired", cutoffNanos-1)
	f.exec(t, terminal, "task-claimed", retentionNodeID, "k2", make([]byte, 32), cutoffNanos-1, cutoffNanos-2, cutoffNanos-1)
	f.exec(t, `INSERT INTO core_task_resource_claims(node_id,resource_key,task_id) VALUES(?,?,?)`, retentionNodeID, "r", "task-claimed")
	f.exec(t, `INSERT INTO core_tasks(task_id,node_id,idempotency_key,request_digest,accepted_generation,target_id,resource_key,action,intent_json,status,delivery_state,created_at_ns,updated_at_ns)
		VALUES('task-running',?,'k3',?,1,'t','r','start','{}','running','sent',?,?)`,
		retentionNodeID, make([]byte, 32), cutoffNanos-1, cutoffNanos-2)
	f.exec(t, `INSERT INTO core_task_audit_events(node_id,task_id,occurred_at_ns,event) VALUES(?,'task-running',?,'accepted')`, retentionNodeID, cutoffNanos-1)

	result, err := retention.Cleanup(ctx, f.database, f.now, 90*24*time.Hour)
	if err != nil {
		t.Fatalf("cleanup: %v", err)
	}
	if result.CoreTasks != 1 || result.CoreTaskEvents != 1 {
		t.Fatalf("task cleanup counts: %+v", result)
	}
	if f.count(t, `SELECT count(*) FROM core_tasks WHERE task_id='task-expired'`) != 0 {
		t.Fatal("expired terminal task survived")
	}
	if f.count(t, `SELECT count(*) FROM core_tasks WHERE task_id='task-claimed'`) != 1 {
		t.Fatal("a claimed task must be retained")
	}
	if f.count(t, `SELECT count(*) FROM core_tasks WHERE task_id='task-running'`) != 1 {
		t.Fatal("a running task must be retained")
	}
	if f.count(t, `SELECT count(*) FROM core_task_audit_events WHERE task_id='task-running'`) != 1 {
		t.Fatal("events of a retained task must stay with it")
	}
	if f.count(t, `SELECT count(*) FROM core_task_audit_events WHERE task_id='task-expired'`) != 0 {
		t.Fatal("events of an expired task must go with it")
	}
}

func TestCleanupAlertsFollowDeliveryState(t *testing.T) {
	f := newRetentionFixture(t)
	ctx := context.Background()
	cutoffNanos := f.cutoff.UnixNano()
	alert := `INSERT INTO alerts(id,fingerprint,rule_id,rule_name,node_id,node_name,subject_id,severity,status,message,first_seen_at,last_seen_at,resolved_at)
		VALUES(?,?,?,?,?,?,?,?,?,'problem',?,?,?)`
	f.exec(t, alert, "alert-resolved", "fp-resolved", "rule-1", "rule", retentionNodeID, "Retention VPS", "subject", "warning", "resolved",
		cutoffNanos-10, cutoffNanos-10, cutoffNanos-10)
	f.exec(t, `INSERT INTO alert_events(id,alert_id,kind,occurred_at,message) VALUES('event-resolved','alert-resolved','firing',?,'m')`, cutoffNanos-10)
	f.exec(t, alert, "alert-active", "fp-active", "rule-1", "rule", retentionNodeID, "Retention VPS", "subject", "critical", "active",
		cutoffNanos-10, cutoffNanos-10, nil)
	f.exec(t, `INSERT INTO alert_deliveries(id,alert_id,event_id,channel_id,channel_name,kind,payload_json,status,created_at,updated_at)
		VALUES('delivery-sent','alert-active','event-active','ch','ch','webhook','{}','sent',?,?)`, cutoffNanos-10, cutoffNanos-10)
	f.exec(t, `INSERT INTO alert_events(id,alert_id,kind,occurred_at,message) VALUES('event-active','alert-active','firing',?,'m')`, cutoffNanos-10)
	f.exec(t, `INSERT INTO alert_deliveries(id,alert_id,event_id,channel_id,channel_name,kind,payload_json,status,created_at,updated_at)
		VALUES('delivery-pending','alert-resolved','event-resolved','ch','ch','webhook','{}','retry',?,?)`, cutoffNanos-10, cutoffNanos-10)

	result, err := retention.Cleanup(ctx, f.database, f.now, 90*24*time.Hour)
	if err != nil {
		t.Fatalf("cleanup: %v", err)
	}
	// Nothing is cleanable yet: the pending retry delivery protects the resolved
	// alert, and the active alert protects its own sent delivery.
	if (result != retention.Result{}) {
		t.Fatalf("first cleanup must retain everything: %+v", result)
	}
	if f.count(t, `SELECT count(*) FROM alerts WHERE id='alert-resolved'`) != 1 {
		t.Fatal("a resolved alert with a pending delivery must be retained")
	}
	if f.count(t, `SELECT count(*) FROM alert_deliveries WHERE id='delivery-pending'`) != 1 {
		t.Fatal("a pending delivery must be retained")
	}
	if f.count(t, `SELECT count(*) FROM alerts WHERE id='alert-active'`) != 1 {
		t.Fatal("an active alert must be retained")
	}
	if f.count(t, `SELECT count(*) FROM alert_deliveries WHERE id='delivery-sent'`) != 1 {
		t.Fatal("deliveries of an active alert must be retained")
	}

	// Retire the pending delivery; the resolved alert becomes fully cleanable.
	f.exec(t, `UPDATE alert_deliveries SET status='failed' WHERE id='delivery-pending'`)
	result, err = retention.Cleanup(ctx, f.database, f.now, 90*24*time.Hour)
	if err != nil {
		t.Fatalf("second cleanup: %v", err)
	}
	if result.Alerts != 1 || result.AlertEvents != 1 || result.AlertDeliveries != 1 {
		t.Fatalf("second alert cleanup counts: %+v", result)
	}
	if f.count(t, `SELECT count(*) FROM alerts`) != 1 {
		t.Fatal("only the active alert must remain")
	}
}

func TestCleanupProbeRunsWindowsAndBatches(t *testing.T) {
	f := newRetentionFixture(t)
	ctx := context.Background()
	cutoffNanos := f.cutoff.UnixNano()
	run := `INSERT INTO service_probe_runs(run_id,probe_id,node_id,probe_revision,kind,status,checked_at,timeout_seconds)
		VALUES(?,?,?,?,?,?,?,2)`
	f.exec(t, run, "run-expired", "probe-1", retentionNodeID, 1, "http", "healthy", cutoffNanos-1)
	f.exec(t, run, "run-pending", "probe-1", retentionNodeID, 1, "http", "pending", cutoffNanos-1)
	f.exec(t, `INSERT INTO alert_windows(id,kind,scope_type,scope_id,starts_at,ends_at,reason,created_at,disabled_at)
		VALUES('window-old','silence','node',?,0,?,'reason',1,?)`, retentionNodeID, cutoffNanos-1, cutoffNanos-1)
	f.exec(t, `INSERT INTO alert_windows(id,kind,scope_type,scope_id,starts_at,ends_at,reason,created_at)
		VALUES('window-active','maintenance','global',NULL,0,?,'reason',1)`, cutoffNanos+3600_000_000_000)
	f.exec(t, `INSERT INTO host_batches(id,kind,request_hash,nodes_json,results_json,created_at,updated_at)
		VALUES('batch-complete','command','h','["n1"]','[{"stale":false,"job":{"status":"succeeded"}}]',?,?)`, cutoffNanos-1, cutoffNanos-1)
	f.exec(t, `INSERT INTO host_batches(id,kind,request_hash,nodes_json,results_json,created_at,updated_at)
		VALUES('batch-partial','command','h','["n1","n2"]','[{"stale":false,"job":{"status":"succeeded"}}]',?,?)`, cutoffNanos-1, cutoffNanos-1)

	result, err := retention.Cleanup(ctx, f.database, f.now, 90*24*time.Hour)
	if err != nil {
		t.Fatalf("cleanup: %v", err)
	}
	if result.ProbeRuns != 1 || result.AlertWindows != 1 || result.HostBatches != 1 {
		t.Fatalf("cleanup counts: %+v", result)
	}
	if f.count(t, `SELECT count(*) FROM service_probe_runs WHERE run_id='run-pending'`) != 1 {
		t.Fatal("pending probe runs must be retained")
	}
	if f.count(t, `SELECT count(*) FROM alert_windows WHERE id='window-active'`) != 1 {
		t.Fatal("active alert windows must be retained")
	}
	if f.count(t, `SELECT count(*) FROM host_batches WHERE id='batch-partial'`) != 1 {
		t.Fatal("incomplete host batches must be retained")
	}
}
