// Package retention deletes expired Core operational history. Metric retention
// is owned by core/history and remains independent from this package.
package retention

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

const (
	DefaultDays = 90
	MinDays     = 1
	MaxDays     = 3650
)

// Result reports rows deleted by one atomic cleanup transaction.
type Result struct {
	AuditEntries    int64
	CoreTaskEvents  int64
	CoreTasks       int64
	ProbeRuns       int64
	AlertDeliveries int64
	AlertEvents     int64
	Alerts          int64
	AlertWindows    int64
}

// Cleanup removes completed history strictly older than now-retention. Rows on
// the cutoff are retained. Related records are removed in the same transaction
// as their parent rows so an error cannot leave broken history references.
// Active or ambiguous work (including task/operation unknown states and alert
// deliveries queued, sending, or awaiting retry) is always retained.
func Cleanup(ctx context.Context, db *sql.DB, now time.Time, retention time.Duration) (Result, error) {
	if db == nil || now.IsZero() || retention <= 0 {
		return Result{}, errors.New("history retention database, clock, and duration are required")
	}
	cutoff := now.UTC().Add(-retention)
	cutoffSeconds := ceilUnixSecond(cutoff)
	cutoffNanos := cutoff.UnixNano()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return Result{}, fmt.Errorf("begin non-metric history cleanup: %w", err)
	}
	defer tx.Rollback()
	var result Result
	remove := func(label, query string, args ...any) (int64, error) {
		res, err := tx.ExecContext(ctx, query, args...)
		if err != nil {
			return 0, fmt.Errorf("delete expired %s: %w", label, err)
		}
		count, err := res.RowsAffected()
		if err != nil {
			return 0, fmt.Errorf("count expired %s: %w", label, err)
		}
		return count, nil
	}
	var n int64
	if result.AuditEntries, err = remove("audit entries", `DELETE FROM audit_entries WHERE occurred_at < ?`, cutoffSeconds); err != nil {
		return Result{}, err
	}
	// Unattached dispatch/audit facts can expire independently. Task-linked
	// events stay with their task until that terminal task is eligible below.
	if result.CoreTaskEvents, err = remove("unlinked Core task events", `DELETE FROM core_task_audit_events WHERE task_id IS NULL AND occurred_at_ns < ?`, cutoffNanos); err != nil {
		return Result{}, err
	}
	if n, err = remove("expired Core task events", `DELETE FROM core_task_audit_events WHERE task_id IN (
		SELECT t.task_id FROM core_tasks t
		WHERE t.status IN ('succeeded','failed','timed_out','canceled')
		AND t.finished_at_ns IS NOT NULL AND t.finished_at_ns < ?
		AND t.delivery_state='done' AND t.reconciliation_required=0
		AND NOT EXISTS (SELECT 1 FROM core_task_resource_claims c WHERE c.task_id=t.task_id)
	)`, cutoffNanos); err != nil {
		return Result{}, err
	}
	result.CoreTaskEvents += n
	if result.CoreTasks, err = remove("expired Core tasks", `DELETE FROM core_tasks AS t
		WHERE t.status IN ('succeeded','failed','timed_out','canceled')
		AND t.finished_at_ns IS NOT NULL AND t.finished_at_ns < ?
		AND t.delivery_state='done' AND t.reconciliation_required=0
		AND NOT EXISTS (SELECT 1 FROM core_task_resource_claims c WHERE c.task_id=t.task_id)`, cutoffNanos); err != nil {
		return Result{}, err
	}
	if result.ProbeRuns, err = remove("completed service probe runs", `DELETE FROM service_probe_runs
		WHERE status <> 'pending' AND COALESCE(completed_at, checked_at) < ?`, cutoffNanos); err != nil {
		return Result{}, err
	}
	// Deliveries use soft references. Keep all notifications for active alerts,
	// and keep every delivery that can still be sent, retried, or reconciled.
	if result.AlertDeliveries, err = remove("completed alert deliveries", `DELETE FROM alert_deliveries AS d
		WHERE d.status IN ('sent','failed','suppressed') AND d.created_at < ?
		AND NOT EXISTS (SELECT 1 FROM alerts a WHERE a.id=d.alert_id AND a.status='active')`, cutoffNanos); err != nil {
		return Result{}, err
	}
	if result.AlertEvents, err = remove("expired resolved alert events", `DELETE FROM alert_events AS e
		WHERE e.occurred_at < ?
		AND EXISTS (SELECT 1 FROM alerts a WHERE a.id=e.alert_id AND a.status='resolved')
		AND NOT EXISTS (SELECT 1 FROM alert_deliveries d WHERE d.event_id=e.id)`, cutoffNanos); err != nil {
		return Result{}, err
	}
	if n, err = remove("expired resolved alert events", `DELETE FROM alert_events WHERE alert_id IN (
		SELECT a.id FROM alerts a WHERE a.status='resolved' AND a.resolved_at IS NOT NULL AND a.resolved_at < ?
		AND NOT EXISTS (SELECT 1 FROM alert_deliveries d WHERE d.alert_id=a.id OR d.event_id IN (
			SELECT e.id FROM alert_events e WHERE e.alert_id=a.id
		))
	)`, cutoffNanos); err != nil {
		return Result{}, err
	}
	result.AlertEvents += n
	if result.Alerts, err = remove("resolved alerts", `DELETE FROM alerts AS a
		WHERE a.status='resolved' AND a.resolved_at IS NOT NULL AND a.resolved_at < ?
		AND NOT EXISTS (SELECT 1 FROM alert_deliveries d WHERE d.alert_id=a.id OR d.event_id IN (
			SELECT e.id FROM alert_events e WHERE e.alert_id=a.id
		))`, cutoffNanos); err != nil {
		return Result{}, err
	}
	if result.AlertWindows, err = remove("expired alert windows", `DELETE FROM alert_windows
		WHERE (disabled_at IS NOT NULL AND disabled_at < ?)
		OR (disabled_at IS NULL AND ends_at < ?)`, cutoffNanos, cutoffNanos); err != nil {
		return Result{}, err
	}
	if err := tx.Commit(); err != nil {
		return Result{}, fmt.Errorf("commit non-metric history cleanup: %w", err)
	}
	return result, nil
}

func ceilUnixSecond(value time.Time) int64 {
	seconds := value.Unix()
	if value.Nanosecond() != 0 {
		seconds++
	}
	return seconds
}
