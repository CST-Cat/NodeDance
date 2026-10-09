package retention

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/CST-Cat/NodeDance/internal/core/storage"
)

func openRetentionDB(t *testing.T) *storage.Store {
	t.Helper()
	store, err := storage.Open(context.Background(), filepath.Join(t.TempDir(), "core"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func TestCleanupExpiresOnlyTerminalFileTasksAndTheirEvents(t *testing.T) {
	ctx := context.Background()
	store := openRetentionDB(t)
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	cutoff := now.Add(-90 * 24 * time.Hour)
	old := cutoff.Add(-time.Second)
	for i := 1; i <= 5; i++ {
		nodeID := fmt.Sprintf("00000000-0000-4000-8000-%012x", i)
		if _, err := store.DB.ExecContext(ctx, `INSERT INTO nodes(id,display_name,status,created_at,updated_at) VALUES(?,?,'pending',?,?)`, nodeID, nodeID, old.Unix(), old.Unix()); err != nil {
			t.Fatal("insert node:", err)
		}
	}
	insertTask := func(id, nodeID, status string, at time.Time) {
		t.Helper()
		var finished any
		if status == "succeeded" || status == "failed" || status == "canceled" || status == "unknown" {
			finished = at.UnixNano()
		}
		if _, err := store.DB.ExecContext(ctx, `INSERT INTO file_write_tasks(task_id,node_id,operation,target_path,status,result_code,created_at_ns,updated_at_ns,finished_at_ns)
			VALUES(?,?,'mkdir','/tmp/target',?,?,?, ?,?)`, id, nodeID, status, map[string]string{"succeeded": "verified", "failed": "agent_rejected", "canceled": "cancel_confirmed", "unknown": "result_pending", "running": ""}[status], at.UnixNano(), at.UnixNano(), finished); err != nil {
			t.Fatal("insert file task", id, err)
		}
		if _, err := store.DB.ExecContext(ctx, `INSERT INTO file_write_task_events(node_id,task_id,event,to_status,occurred_at_ns) VALUES(?,?,'accepted',?,?)`, nodeID, id, status, at.UnixNano()); err != nil {
			t.Fatal("insert file task event", id, err)
		}
	}
	insertTask("00000000-0000-4000-8000-0000000000a1", "00000000-0000-4000-8000-000000000001", "succeeded", old)
	insertTask("00000000-0000-4000-8000-0000000000a2", "00000000-0000-4000-8000-000000000002", "failed", cutoff)
	insertTask("00000000-0000-4000-8000-0000000000a3", "00000000-0000-4000-8000-000000000003", "unknown", old)
	insertTask("00000000-0000-4000-8000-0000000000a4", "00000000-0000-4000-8000-000000000004", "running", old)
	insertTask("00000000-0000-4000-8000-0000000000a5", "00000000-0000-4000-8000-000000000005", "canceled", old)

	result, err := Cleanup(ctx, store.DB, now, 90*24*time.Hour)
	if err != nil {
		t.Fatal("cleanup:", err)
	}
	var got int
	for query, want := range map[string]int{
		`SELECT count(*) FROM file_write_tasks WHERE task_id='00000000-0000-4000-8000-0000000000a1'`:                                                                                    0,
		`SELECT count(*) FROM file_write_task_events WHERE task_id='00000000-0000-4000-8000-0000000000a1'`:                                                                              0,
		`SELECT count(*) FROM file_write_tasks WHERE task_id='00000000-0000-4000-8000-0000000000a5'`:                                                                                    0,
		`SELECT count(*) FROM file_write_task_events WHERE task_id='00000000-0000-4000-8000-0000000000a5'`:                                                                              0,
		`SELECT count(*) FROM file_write_tasks WHERE task_id IN ('00000000-0000-4000-8000-0000000000a2','00000000-0000-4000-8000-0000000000a3','00000000-0000-4000-8000-0000000000a4')`: 3,
	} {
		if err := store.DB.QueryRowContext(ctx, query).Scan(&got); err != nil || got != want {
			t.Fatalf("query %q count=%d err=%v want=%d", query, got, err, want)
		}
	}
	if result.FileTasks != 2 || result.FileTaskEvents != 2 {
		t.Fatalf("file retention counts=%+v want two expired terminal tasks and events", result)
	}
}

func TestCleanupExpiresOldHistoryAndRetainsAmbiguousOrActiveRows(t *testing.T) {
	ctx := context.Background()
	store := openRetentionDB(t)
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	cutoff := now.Add(-90 * 24 * time.Hour)
	old := cutoff.Add(-time.Second)
	boundary := cutoff
	recent := cutoff.Add(time.Second)
	for _, node := range []string{"node-1", "node-2", "node-3", "node-4", "node-5", "node-6", "node-7", "node-8"} {
		if _, err := store.DB.ExecContext(ctx, `INSERT INTO nodes(id,display_name,status,created_at,updated_at) VALUES(?,?,'pending',?,?)`, node, node, old.Unix(), old.Unix()); err != nil {
			t.Fatal("insert node:", err)
		}
	}
	for _, metric := range []struct {
		table string
		at    int64
	}{{"metrics_minute", old.Unix()}, {"metrics_hour", old.Add(-400 * 24 * time.Hour).Unix()}} {
		query := `INSERT INTO ` + metric.table + `(node_id,metric_key,bucket_at,sample_count,sample_sum,minimum,maximum) VALUES('node-1','cpu',?,1,1,1,1)`
		if _, err := store.DB.ExecContext(ctx, query, metric.at); err != nil {
			t.Fatal("insert metric history row:", err)
		}
	}
	for id, at := range map[string]time.Time{"audit-old": old, "audit-boundary": boundary, "audit-recent": recent} {
		if _, err := store.DB.ExecContext(ctx, `INSERT INTO audit_entries(occurred_at,action,outcome,remote_addr) VALUES(?,?,?,'unknown')`, at.Unix(), "login", "succeeded"); err != nil {
			t.Fatal("insert audit entry", id, err)
		}
	}

	insertTask := func(id, status, delivery string, finished time.Time, reconcile bool, node string, claim bool) {
		t.Helper()
		reconcileInt := 0
		if reconcile {
			reconcileInt = 1
		}
		var finish any
		if !finished.IsZero() {
			finish = finished.UnixNano()
		}
		if _, err := store.DB.ExecContext(ctx, `INSERT INTO core_tasks(
			task_id,node_id,idempotency_key,request_digest,accepted_generation,target_id,resource_key,action,intent_json,status,delivery_state,
			reconciliation_required,created_at_ns,updated_at_ns,finished_at_ns
		) VALUES(?,?,?,zeroblob(32),0,'container','docker-container:container','restart','{}',?,?,?,?,?,?)`,
			id, node, id, status, delivery, reconcileInt, old.UnixNano(), old.UnixNano(), finish); err != nil {
			t.Fatal("insert core task", id, err)
		}
		if _, err := store.DB.ExecContext(ctx, `INSERT INTO core_task_audit_events(node_id,task_id,event,to_status,occurred_at_ns) VALUES(?,?,'accepted',?,?)`, node, id, status, old.UnixNano()); err != nil {
			t.Fatal("insert core task event", id, err)
		}
		if claim {
			if _, err := store.DB.ExecContext(ctx, `INSERT INTO core_task_resource_claims(node_id,resource_key,task_id) VALUES(?,?,?)`, node, "resource:"+id, id); err != nil {
				t.Fatal("insert task resource claim", id, err)
			}
		}
	}
	insertTask("task-expired", "succeeded", "done", old, false, "node-1", false)
	insertTask("task-boundary", "succeeded", "done", boundary, false, "node-1", false)
	insertTask("task-unknown", "unknown", "needs_reconciliation", old, true, "node-2", false)
	insertTask("task-queued", "queued", "ready", time.Time{}, false, "node-3", false)
	insertTask("task-running", "running", "sent", time.Time{}, false, "node-4", false)
	insertTask("task-claimed", "succeeded", "done", old, false, "node-5", true)
	insertTask("task-reconcile", "failed", "needs_reconciliation", old, true, "node-6", false)
	if _, err := store.DB.ExecContext(ctx, `INSERT INTO core_task_audit_events(node_id,event,occurred_at_ns) VALUES('node-1','accepted',?)`, old.UnixNano()); err != nil {
		t.Fatal("insert detached task event:", err)
	}
	if _, err := store.DB.ExecContext(ctx, `INSERT INTO core_task_audit_events(node_id,event,occurred_at_ns) VALUES('node-1','accepted',?)`, boundary.UnixNano()); err != nil {
		t.Fatal("insert boundary detached task event:", err)
	}

	if _, err := store.DB.ExecContext(ctx, `INSERT INTO compose_projects(node_id,project_key,project_name,working_directory,config_files_json,config_available,discovered_at,last_seen_at) VALUES('node-1','project','project','/tmp/project','[]',1,?,?)`, old.UnixNano(), old.UnixNano()); err != nil {
		t.Fatal("insert Compose project:", err)
	}
	insertCompose := func(id, table, status string, updated time.Time) {
		t.Helper()
		switch table {
		case "compose_operations":
			if _, err := store.DB.ExecContext(ctx, `INSERT INTO compose_operations(operation_id,node_id,project_key,idempotency_key,request_digest,request_json,action,status,remote_addr,created_at,updated_at) VALUES(?,'node-1','project',?,zeroblob(32),'{}','up',?,'unknown',?,?)`, id, id, status, old.UnixNano(), updated.UnixNano()); err != nil {
				t.Fatal("insert Compose operation", id, err)
			}
			if _, err := store.DB.ExecContext(ctx, `INSERT INTO compose_operation_events(operation_id,event,to_status,remote_addr,occurred_at) VALUES(?,'accepted',?,'unknown',?)`, id, status, old.UnixNano()); err != nil {
				t.Fatal("insert Compose event", id, err)
			}
		case "compose_editor_operations":
			if _, err := store.DB.ExecContext(ctx, `INSERT INTO compose_editor_operations(operation_id,node_id,project_key,idempotency_key,request_digest,action,status,remote_addr,created_at,updated_at) VALUES(?,'node-1','project',?,zeroblob(32),'edit_apply',?,'unknown',?,?)`, id, id, status, old.UnixNano(), updated.UnixNano()); err != nil {
				t.Fatal("insert Compose editor operation", id, err)
			}
			if _, err := store.DB.ExecContext(ctx, `INSERT INTO compose_editor_events(operation_id,event,to_status,occurred_at) VALUES(?,'accepted',?,?)`, id, status, old.UnixNano()); err != nil {
				t.Fatal("insert Compose editor event", id, err)
			}
		}
	}
	insertCompose("compose-expired", "compose_operations", "succeeded", old)
	insertCompose("compose-boundary", "compose_operations", "succeeded", boundary)
	insertCompose("compose-unknown", "compose_operations", "unknown", old)
	insertCompose("compose-queued", "compose_operations", "queued", old)
	insertCompose("compose-running", "compose_operations", "running", old)
	insertCompose("editor-expired", "compose_editor_operations", "failed", old)
	insertCompose("editor-boundary", "compose_editor_operations", "succeeded", boundary)
	insertCompose("editor-unknown", "compose_editor_operations", "unknown", old)
	insertCompose("editor-queued", "compose_editor_operations", "queued", old)
	insertCompose("editor-running", "compose_editor_operations", "running", old)

	if _, err := store.DB.ExecContext(ctx, `INSERT INTO service_probes(id,node_id,name,kind,target,expected_http_status,interval_seconds,timeout_seconds,enabled,revision,status,consecutive_failures,consecutive_successes,next_due_at,created_at,updated_at)
		VALUES('probe-1','node-1','probe','http','http://localhost',200,30,3,1,1,'unknown',0,0,?,?,?)`, old.UnixNano(), old.UnixNano(), old.UnixNano()); err != nil {
		t.Fatal("insert service probe:", err)
	}
	for _, run := range []struct {
		id      string
		status  string
		checked time.Time
		end     any
	}{{"probe-expired", "healthy", old, old.UnixNano()}, {"probe-pending", "pending", old, nil}, {"probe-boundary", "healthy", boundary, boundary.UnixNano()}} {
		if _, err := store.DB.ExecContext(ctx, `INSERT INTO service_probe_runs(run_id,probe_id,node_id,probe_revision,generation,kind,expected_http_status,status,checked_at,completed_at,timeout_seconds) VALUES(?,'probe-1','node-1',1,0,'http',200,?,?,?,3)`, run.id, run.status, run.checked.UnixNano(), run.end); err != nil {
			t.Fatal("insert service probe run", run.id, err)
		}
	}

	insertAlert := func(id, status string, resolved any, first, last time.Time) {
		t.Helper()
		if _, err := store.DB.ExecContext(ctx, `INSERT INTO alerts(id,fingerprint,rule_id,rule_name,node_id,node_name,subject_id,severity,status,message,first_seen_at,last_seen_at,resolved_at) VALUES(?,?, 'rule','rule','node-1','node-1','', 'warning',?,?, ?,?,?)`, id, id, status, "message", first.UnixNano(), last.UnixNano(), resolved); err != nil {
			t.Fatal("insert alert", id, err)
		}
		if _, err := store.DB.ExecContext(ctx, `INSERT INTO alert_events(id,alert_id,kind,occurred_at,message) VALUES(?,?,'firing',?,'event')`, id+"-event", id, first.UnixNano()); err != nil {
			t.Fatal("insert alert event", id, err)
		}
	}
	insertAlert("alert-resolved-expired", "resolved", old.UnixNano(), old, old)
	insertAlert("alert-resolved-boundary", "resolved", boundary.UnixNano(), old, boundary)
	insertAlert("alert-active", "active", nil, old, old)
	insertAlert("alert-retry", "resolved", old.UnixNano(), old, old)
	insertAlert("alert-sent-recent", "resolved", old.UnixNano(), old, old)
	insertAlert("alert-event-linked", "resolved", old.UnixNano(), old, old)
	if _, err := store.DB.ExecContext(ctx, `INSERT INTO alert_events(id,alert_id,kind,occurred_at,message) VALUES('alert-boundary-event','alert-resolved-boundary','firing',?,'boundary')`, boundary.UnixNano()); err != nil {
		t.Fatal("insert boundary alert event:", err)
	}
	insertDelivery := func(id, alertID, eventID, status string, created time.Time) {
		t.Helper()
		if _, err := store.DB.ExecContext(ctx, `INSERT INTO alert_deliveries(id,alert_id,event_id,channel_id,channel_name,kind,payload_json,status,created_at,updated_at) VALUES(?,?,?,?,?,'webhook','{}',?,?,?)`, id, alertID, eventID, "channel", "channel", status, created.UnixNano(), created.UnixNano()); err != nil {
			t.Fatal("insert alert delivery", id, err)
		}
	}
	insertDelivery("delivery-active-old", "alert-active", "alert-active-event", "sent", old)
	insertDelivery("delivery-retry", "alert-retry", "alert-retry-event", "retry", old)
	insertDelivery("delivery-queued", "alert-retry", "alert-retry-event", "queued", old)
	insertDelivery("delivery-sending", "alert-retry", "alert-retry-event", "sending", old)
	insertDelivery("delivery-terminal-old", "alert-resolved-expired", "alert-resolved-expired-event", "failed", old)
	insertDelivery("delivery-terminal-boundary", "alert-resolved-boundary", "alert-resolved-boundary-event", "failed", boundary)
	insertDelivery("delivery-terminal-recent", "alert-sent-recent", "alert-sent-recent-event", "sent", recent)
	insertDelivery("delivery-event-linked", "", "alert-event-linked-event", "sent", recent)
	for _, window := range []struct {
		id       string
		start    time.Time
		end      time.Time
		disabled any
	}{{"window-expired", old.Add(-time.Second), old, nil}, {"window-boundary", old.Add(-time.Second), boundary, nil}, {"window-current", old, now.Add(time.Hour), nil}, {"window-disabled", old, now.Add(time.Hour), old.UnixNano()}} {
		if _, err := store.DB.ExecContext(ctx, `INSERT INTO alert_windows(id,kind,scope_type,starts_at,ends_at,reason,created_at,disabled_at) VALUES(?,'silence','global',?,?, 'test',?,?)`, window.id, window.start.UnixNano(), window.end.UnixNano(), window.start.UnixNano(), window.disabled); err != nil {
			t.Fatal("insert alert window", window.id, err)
		}
	}

	if _, err := store.DB.ExecContext(ctx, `INSERT INTO agent_update_releases(id,version,os,architecture,manifest_json,artifact_path,created_at) VALUES('release-1','1.0.0','linux','amd64','{}','/release/1',?)`, old.Unix()); err != nil {
		t.Fatal("insert Agent release:", err)
	}
	if _, err := store.DB.ExecContext(ctx, `UPDATE agent_update_settings SET release_id='release-1' WHERE id=1`); err != nil {
		t.Fatal("select Agent release:", err)
	}
	for i, task := range []struct {
		status string
		node   string
		at     time.Time
	}{{"succeeded", "node-1", old}, {"failed", "node-2", old}, {"paused", "node-3", old}, {"queued", "node-4", old}, {"deferred", "node-5", old}, {"prepared", "node-6", old}, {"dispatched", "node-7", old}, {"failed", "node-8", boundary}} {
		if _, err := store.DB.ExecContext(ctx, `INSERT INTO agent_update_tasks(id,batch_id,batch_number,node_id,release_id,mode,status,created_at,updated_at) VALUES(?,?,?,?,?,'manual',?,?,?)`, fmt.Sprintf("update-%d", i), "batch", i, task.node, "release-1", task.status, task.at.Unix(), task.at.Unix()); err != nil {
			t.Fatal("insert Agent update task", i, err)
		}
	}

	result, err := Cleanup(ctx, store.DB, now, 90*24*time.Hour)
	if err != nil {
		t.Fatal("cleanup:", err)
	}
	assertCount := func(query string, want int) {
		t.Helper()
		var got int
		if err := store.DB.QueryRowContext(ctx, query).Scan(&got); err != nil || got != want {
			t.Fatalf("query %q count=%d err=%v, want %d", query, got, err, want)
		}
	}
	assertCount(`SELECT count(*) FROM audit_entries`, 2) // the cutoff and recent rows remain
	assertCount(`SELECT count(*) FROM metrics_minute`, 1)
	assertCount(`SELECT count(*) FROM metrics_hour`, 1)
	assertCount(`SELECT count(*) FROM core_tasks WHERE task_id='task-expired'`, 0)
	assertCount(`SELECT count(*) FROM core_tasks WHERE task_id IN ('task-boundary','task-unknown','task-queued','task-running','task-claimed','task-reconcile')`, 6)
	assertCount(`SELECT count(*) FROM core_task_audit_events WHERE task_id='task-expired'`, 0)
	assertCount(`SELECT count(*) FROM core_task_audit_events WHERE task_id IS NULL`, 1)
	assertCount(`SELECT count(*) FROM compose_operations WHERE operation_id='compose-expired'`, 0)
	assertCount(`SELECT count(*) FROM compose_operation_events WHERE operation_id='compose-expired'`, 0)
	assertCount(`SELECT count(*) FROM compose_operations WHERE operation_id IN ('compose-boundary','compose-unknown','compose-queued','compose-running')`, 4)
	assertCount(`SELECT count(*) FROM compose_editor_operations WHERE operation_id='editor-expired'`, 0)
	assertCount(`SELECT count(*) FROM compose_editor_events WHERE operation_id='editor-expired'`, 0)
	assertCount(`SELECT count(*) FROM compose_editor_operations WHERE operation_id IN ('editor-boundary','editor-unknown','editor-queued','editor-running')`, 4)
	assertCount(`SELECT count(*) FROM service_probe_runs WHERE run_id='probe-expired'`, 0)
	assertCount(`SELECT count(*) FROM service_probe_runs WHERE run_id IN ('probe-pending','probe-boundary')`, 2)
	assertCount(`SELECT count(*) FROM alerts WHERE id='alert-resolved-expired'`, 0)
	assertCount(`SELECT count(*) FROM alert_events WHERE alert_id='alert-resolved-expired'`, 0)
	assertCount(`SELECT count(*) FROM alert_events WHERE id='alert-boundary-event'`, 1)
	assertCount(`SELECT count(*) FROM alerts WHERE id='alert-event-linked'`, 1)
	assertCount(`SELECT count(*) FROM alert_events WHERE alert_id='alert-event-linked'`, 1)
	assertCount(`SELECT count(*) FROM alert_deliveries WHERE id='delivery-terminal-old'`, 0)
	assertCount(`SELECT count(*) FROM alerts WHERE id IN ('alert-resolved-boundary','alert-active','alert-retry','alert-sent-recent','alert-event-linked')`, 5)
	assertCount(`SELECT count(*) FROM alert_deliveries WHERE id IN ('delivery-active-old','delivery-retry','delivery-queued','delivery-sending','delivery-terminal-boundary','delivery-terminal-recent','delivery-event-linked')`, 7)
	assertCount(`SELECT count(*) FROM alert_windows WHERE id IN ('window-expired','window-disabled')`, 0)
	assertCount(`SELECT count(*) FROM alert_windows WHERE id='window-boundary'`, 1)
	assertCount(`SELECT count(*) FROM alert_windows WHERE id='window-current'`, 1)
	assertCount(`SELECT count(*) FROM agent_update_tasks WHERE status IN ('succeeded','failed','paused')`, 1)
	assertCount(`SELECT count(*) FROM agent_update_tasks WHERE status IN ('queued','deferred','dispatched','prepared')`, 4)
	assertCount(`SELECT count(*) FROM agent_update_releases WHERE id='release-1'`, 1)
	if result.AuditEntries != 1 || result.CoreTasks != 1 || result.ComposeOperations != 1 || result.ComposeEditorOps != 1 || result.ProbeRuns != 1 || result.Alerts != 1 || result.AgentUpdateTasks != 3 {
		t.Fatalf("unexpected cleanup result: %+v", result)
	}
}

func TestCleanupRollsBackAllHistoryDeletesOnFailure(t *testing.T) {
	ctx := context.Background()
	store := openRetentionDB(t)
	old := time.Now().UTC().Add(-120 * 24 * time.Hour)
	if _, err := store.DB.ExecContext(ctx, `INSERT INTO audit_entries(occurred_at,action,outcome,remote_addr) VALUES(?,'login','succeeded','unknown')`, old.Unix()); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB.ExecContext(ctx, `INSERT INTO nodes(id,display_name,status,created_at,updated_at) VALUES('node','node','pending',?,?)`, old.Unix(), old.Unix()); err != nil {
		t.Fatal("insert node:", err)
	}
	if _, err := store.DB.ExecContext(ctx, `INSERT INTO compose_projects(node_id,project_key,project_name,working_directory,config_files_json,config_available,discovered_at,last_seen_at) VALUES('node','project','project','/tmp/project','[]',1,?,?)`, old.UnixNano(), old.UnixNano()); err != nil {
		t.Fatal("insert project:", err)
	}
	if _, err := store.DB.ExecContext(ctx, `INSERT INTO compose_operations(operation_id,node_id,project_key,idempotency_key,request_digest,request_json,action,status,remote_addr,created_at,updated_at) VALUES('compose-old','node','project','compose-old',zeroblob(32),'{}','up','succeeded','unknown',?,?)`, old.UnixNano(), old.UnixNano()); err != nil {
		t.Fatal("insert old Compose operation:", err)
	}
	if _, err := store.DB.ExecContext(ctx, `CREATE TRIGGER fail_compose_retention BEFORE DELETE ON compose_operations BEGIN SELECT RAISE(ABORT,'injected retention failure'); END`); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if _, err := Cleanup(ctx, store.DB, now, 90*24*time.Hour); err == nil {
		t.Fatal("cleanup with injected SQLite failure succeeded")
	}
	var auditRows int
	if err := store.DB.QueryRowContext(ctx, `SELECT count(*) FROM audit_entries`).Scan(&auditRows); err != nil || auditRows != 1 {
		t.Fatalf("earlier table delete was not rolled back: count=%d err=%v", auditRows, err)
	}
}

func TestCleanupRoundsSubsecondCutoffsUpForSecondResolutionTables(t *testing.T) {
	ctx := context.Background()
	store := openRetentionDB(t)
	now := time.Date(2026, 10, 8, 12, 0, 0, 500_000_000, time.UTC)
	cutoff := now.Add(-90 * 24 * time.Hour)
	if _, err := store.DB.ExecContext(ctx, `INSERT INTO audit_entries(occurred_at,action,outcome,remote_addr) VALUES(?,'login','succeeded','unknown')`, cutoff.Unix()); err != nil {
		t.Fatal("insert second-resolution audit row:", err)
	}
	if _, err := store.DB.ExecContext(ctx, `INSERT INTO nodes(id,display_name,status,created_at,updated_at) VALUES('node','node','pending',?,?)`, cutoff.Unix(), cutoff.Unix()); err != nil {
		t.Fatal("insert node:", err)
	}
	if _, err := store.DB.ExecContext(ctx, `INSERT INTO agent_update_releases(id,version,os,architecture,manifest_json,artifact_path,created_at) VALUES('release','1','linux','amd64','{}','/release',?)`, cutoff.Unix()); err != nil {
		t.Fatal("insert release:", err)
	}
	if _, err := store.DB.ExecContext(ctx, `INSERT INTO agent_update_tasks(id,batch_id,batch_number,node_id,release_id,mode,status,created_at,updated_at) VALUES('task','batch',0,'node','release','manual','succeeded',?,?)`, cutoff.Unix(), cutoff.Unix()); err != nil {
		t.Fatal("insert second-resolution Agent update task:", err)
	}
	if _, err := Cleanup(ctx, store.DB, now, 90*24*time.Hour); err != nil {
		t.Fatal("cleanup:", err)
	}
	for _, query := range []string{
		`SELECT count(*) FROM audit_entries`,
		`SELECT count(*) FROM agent_update_tasks`,
	} {
		var count int
		if err := store.DB.QueryRowContext(ctx, query).Scan(&count); err != nil || count != 0 {
			t.Fatalf("subsecond cutoff cleanup query %q count=%d err=%v", query, count, err)
		}
	}
}

func TestCleanupKeepsDatabaseAccountingObservableAndWALUsable(t *testing.T) {
	ctx := context.Background()
	store := openRetentionDB(t)
	old := time.Now().UTC().Add(-120 * 24 * time.Hour)
	for i := 0; i < 2000; i++ {
		if _, err := store.DB.ExecContext(ctx, `INSERT INTO audit_entries(occurred_at,action,outcome,remote_addr) VALUES(?,'login','succeeded','unknown')`, old.Unix()); err != nil {
			t.Fatal("insert bounded history sample:", err)
		}
	}
	var pagesBefore, freeBefore int64
	if err := store.DB.QueryRowContext(ctx, `PRAGMA page_count`).Scan(&pagesBefore); err != nil {
		t.Fatal(err)
	}
	if err := store.DB.QueryRowContext(ctx, `PRAGMA freelist_count`).Scan(&freeBefore); err != nil {
		t.Fatal(err)
	}
	result, err := Cleanup(ctx, store.DB, time.Now().UTC(), 90*24*time.Hour)
	if err != nil || result.AuditEntries != 2000 {
		t.Fatalf("cleanup result=%+v err=%v", result, err)
	}
	var pagesAfter, freeAfter int64
	if err := store.DB.QueryRowContext(ctx, `PRAGMA page_count`).Scan(&pagesAfter); err != nil {
		t.Fatal(err)
	}
	if err := store.DB.QueryRowContext(ctx, `PRAGMA freelist_count`).Scan(&freeAfter); err != nil {
		t.Fatal(err)
	}
	var checkpointBusy, walFrames, checkpointedFrames int64
	if err := store.DB.QueryRowContext(ctx, `PRAGMA wal_checkpoint(PASSIVE)`).Scan(&checkpointBusy, &walFrames, &checkpointedFrames); err != nil {
		t.Fatal("measure WAL checkpoint state:", err)
	}
	info, err := os.Stat(filepath.Join(store.Dir, "nodedance.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	walInfo, err := os.Stat(filepath.Join(store.Dir, "nodedance.sqlite-wal"))
	walBytes := int64(0)
	if err == nil {
		walBytes = walInfo.Size()
	} else if !os.IsNotExist(err) {
		t.Fatal("stat WAL file:", err)
	}
	if pagesAfter < pagesBefore || freeAfter <= freeBefore || info.Size() == 0 {
		t.Fatalf("SQLite cleanup accounting is not observable: pages %d->%d free %d->%d db_bytes=%d wal_bytes=%d", pagesBefore, pagesAfter, freeBefore, freeAfter, info.Size(), walBytes)
	}
	t.Logf("retention storage measurement: pages=%d->%d free_pages=%d->%d db_bytes=%d wal_bytes=%d wal_frames=%d checkpointed_frames=%d checkpoint_busy=%d",
		pagesBefore, pagesAfter, freeBefore, freeAfter, info.Size(), walBytes, walFrames, checkpointedFrames, checkpointBusy)
	var mode string
	if err := store.DB.QueryRowContext(ctx, `PRAGMA journal_mode`).Scan(&mode); err != nil || mode != "wal" {
		t.Fatalf("WAL mode after cleanup=%q err=%v (db_bytes=%d wal_bytes=%d)", mode, err, info.Size(), walBytes)
	}
	if _, err := store.DB.ExecContext(ctx, `INSERT INTO audit_entries(occurred_at,action,outcome,remote_addr) VALUES(?,'login','succeeded','unknown')`, time.Now().Unix()); err != nil {
		t.Fatalf("database was not writable after cleanup: %v", err)
	}
}
