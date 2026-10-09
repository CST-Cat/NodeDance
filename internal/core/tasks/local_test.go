package tasks_test

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/CST-Cat/NodeDance/internal/core/storage"
	coretasks "github.com/CST-Cat/NodeDance/internal/core/tasks"
	"github.com/CST-Cat/NodeDance/internal/protocol"
	"github.com/CST-Cat/NodeDance/internal/taskstate"
)

func TestLocalAgentDeploymentIsIdempotentAndNeverAgentClaimed(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC()
	database, err := storage.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	store, err := coretasks.New(database.DB, coretasks.Options{Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.DB.ExecContext(ctx, `INSERT INTO nodes(id,display_name,status,created_at,updated_at) VALUES('node-local','Local VPS','pending',1,1)`); err != nil {
		t.Fatal(err)
	}
	intent := localDeploymentIntent()
	request := coretasks.EnqueueRequest{NodeID: "node-local", IdempotencyKey: "deployment-key-1", Intent: intent}
	accepted, err := store.EnqueueAndStartLocal(ctx, request)
	if err != nil {
		t.Fatalf("enqueue local deployment: %v", err)
	}
	if !accepted.Created || accepted.Task.Status != taskstate.Running || accepted.Task.DeliveryState != "done" {
		t.Fatalf("local task is not running outside Agent delivery: %#v", accepted)
	}
	retry, found, err := store.FindLocalByIdempotency(ctx, request.IdempotencyKey, intent)
	if err != nil || !found || retry.TaskID != accepted.Task.TaskID {
		t.Fatalf("same intent retry did not find the original task: task=%#v found=%t err=%v", retry, found, err)
	}
	changedIntent := intent
	changedSpec := *intent.AgentDeploy
	changedSpec.CoreURL = "https://other.example"
	changedIntent.AgentDeploy = &changedSpec
	if _, found, err := store.FindLocalByIdempotency(ctx, request.IdempotencyKey, changedIntent); found || !errors.Is(err, coretasks.ErrIdempotencyConflict) {
		t.Fatalf("same key with changed non-secret intent must conflict: found=%t err=%v", found, err)
	}
	if _, err := store.Enqueue(ctx, request); !errors.Is(err, coretasks.ErrInvalidRequest) {
		t.Fatalf("Agent-delivered Enqueue must reject Core-local action: %v", err)
	}

	if _, err := database.DB.ExecContext(ctx, `UPDATE nodes SET status='online',connection_generation=1,last_seen_at=? WHERE id='node-local'`, now.UnixNano()); err != nil {
		t.Fatal(err)
	}
	connection := coretasks.AgentConnection{NodeID: "node-local", ConnectionGeneration: 1, JournalID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}
	if _, err := store.ObserveAgentConnection(ctx, connection); err != nil {
		t.Fatalf("observe newly online Agent journal: %v", err)
	}
	if _, claimed, err := store.ClaimNext(ctx, connection, sql.NullInt64{}, "127.0.0.1"); err != nil || claimed {
		t.Fatalf("new Agent must not claim Core-local deployment task: claimed=%t err=%v", claimed, err)
	}
	finished, err := store.CompleteLocal(ctx, "node-local", accepted.Task.TaskID, taskstate.Succeeded, "absent", sql.NullInt64{}, "127.0.0.1")
	if err != nil || finished.Status != taskstate.Succeeded || finished.Result.ObservedState != "absent" {
		t.Fatalf("complete local execution: task=%#v err=%v", finished, err)
	}
}

func TestAgentDeployIntentCannotBeAttachedToOtherTaskActions(t *testing.T) {
	intent := localDeploymentIntent()
	intent.Action = protocol.TaskStart
	intent.ContainerID = strings.Repeat("a", 64)
	if err := protocol.ValidateTaskIntent(intent); err == nil {
		t.Fatal("container task with an attached AgentDeploy spec must be rejected")
	}
}

func TestLocalDeploymentStartFailureRollsBackAcceptanceAndRetryCanStart(t *testing.T) {
	ctx := context.Background()
	database, err := storage.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	store, err := coretasks.New(database.DB, coretasks.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.DB.ExecContext(ctx, `INSERT INTO nodes(id,display_name,status,created_at,updated_at) VALUES('node-local','Local VPS','pending',1,1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := database.DB.ExecContext(ctx, `CREATE TRIGGER fail_local_task_start BEFORE INSERT ON core_task_audit_events
		WHEN NEW.event='delivery_claimed' AND NEW.from_status='queued' AND NEW.to_status='running'
		BEGIN SELECT RAISE(ABORT,'injected local task start failure'); END`); err != nil {
		t.Fatal(err)
	}
	request := coretasks.EnqueueRequest{NodeID: "node-local", IdempotencyKey: "retry-after-start-failure", Intent: localDeploymentIntent()}
	if _, err := store.EnqueueAndStartLocal(ctx, request); err == nil {
		t.Fatal("injected Core task start failure unexpectedly succeeded")
	}
	if _, found, err := store.FindLocalByIdempotency(ctx, request.IdempotencyKey, request.Intent); err != nil || found {
		t.Fatalf("failed acceptance left an idempotent task behind: found=%t err=%v", found, err)
	}
	var claims int
	if err := database.DB.QueryRowContext(ctx, `SELECT count(*) FROM core_task_resource_claims`).Scan(&claims); err != nil || claims != 0 {
		t.Fatalf("failed acceptance left a resource claim: count=%d err=%v", claims, err)
	}
	if _, err := database.DB.ExecContext(ctx, `DROP TRIGGER fail_local_task_start`); err != nil {
		t.Fatal(err)
	}
	retry, err := store.EnqueueAndStartLocal(ctx, request)
	if err != nil || !retry.Created || retry.Task.Status != taskstate.Running {
		t.Fatalf("same-key retry did not start cleanly after rollback: task=%#v err=%v", retry, err)
	}
}

func TestRecoverInterruptedLocalAgentDeployments(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC()
	database, err := storage.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	store, err := coretasks.New(database.DB, coretasks.Options{Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	for _, nodeID := range []string{"node-queued", "node-running"} {
		if _, err := database.DB.ExecContext(ctx, `INSERT INTO nodes(id,display_name,status,created_at,updated_at) VALUES(?,?,'pending',1,1)`, nodeID, nodeID); err != nil {
			t.Fatal(err)
		}
	}
	queued, err := store.EnqueueAndStartLocal(ctx, coretasks.EnqueueRequest{NodeID: "node-queued", IdempotencyKey: "queued-key", Intent: localDeploymentIntent()})
	if err != nil {
		t.Fatal(err)
	}
	running, err := store.EnqueueAndStartLocal(ctx, coretasks.EnqueueRequest{NodeID: "node-running", IdempotencyKey: "running-key", Intent: localDeploymentIntentFor("peer-running")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.DB.ExecContext(ctx, `UPDATE core_tasks SET status='queued',execution_attempted=0,started_at_ns=NULL,progress_phase='accepted' WHERE task_id=?`, queued.Task.TaskID); err != nil {
		t.Fatal(err)
	}
	if err := store.RecoverLocalTasks(ctx); err != nil {
		t.Fatalf("recover interrupted Core-local tasks: %v", err)
	}
	queuedAfter, err := store.Get(ctx, "node-queued", queued.Task.TaskID)
	if err != nil || queuedAfter.Status != taskstate.Canceled || queuedAfter.Result.ObservedState != "not_started" {
		t.Fatalf("unstarted task should be safely canceled: task=%#v err=%v", queuedAfter, err)
	}
	runningAfter, err := store.Get(ctx, "node-running", running.Task.TaskID)
	if err != nil || runningAfter.Status != taskstate.Unknown || runningAfter.Result.Code != coretasks.ResultUncertain || runningAfter.DeliveryState != "done" {
		t.Fatalf("interrupted execution should remain unknown: task=%#v err=%v", runningAfter, err)
	}
	confirmedEvidence := coretasks.Evidence{ExecutionAttempted: true, ExecutionCompleted: true, ActualResultConfirmed: true, PostconditionVerified: true}
	confirmedResult := coretasks.Result{Code: coretasks.ResultVerified, ObservedState: "unavailable"}
	if err := store.ResolveUncertainTask(ctx, "node-running", running.Task.TaskID, taskstate.Succeeded, confirmedEvidence, confirmedResult, sql.NullInt64{Int64: 1, Valid: true}, "127.0.0.1"); err != nil {
		t.Fatalf("administrator-confirmed deployment resolution: %v", err)
	}
	if err := store.ResolveUncertainTask(ctx, "node-running", running.Task.TaskID, taskstate.Succeeded, confirmedEvidence, confirmedResult, sql.NullInt64{Int64: 1, Valid: true}, "127.0.0.1"); err != nil {
		t.Fatalf("repeated same deployment resolution should be idempotent: %v", err)
	}
	newTask, err := store.EnqueueAndStartLocal(ctx, coretasks.EnqueueRequest{NodeID: "node-running", IdempotencyKey: "running-key-next", Intent: localDeploymentIntentFor("peer-running")})
	if err != nil || !newTask.Created || newTask.Task.Status != taskstate.Running {
		t.Fatalf("confirmed resolution should release the peer for a new deployment: task=%#v err=%v", newTask, err)
	}
}

func localDeploymentIntent() protocol.TaskIntent {
	return localDeploymentIntentFor("peer-local")
}

func localDeploymentIntentFor(peerIdentity string) protocol.TaskIntent {
	return protocol.TaskIntent{
		Action: protocol.TaskAgentDeploy, ContainerID: "tailscale-peer:" + peerIdentity,
		AgentDeploy: &protocol.AgentDeployTaskSpec{
			PeerIdentity: peerIdentity, PeerName: "Local VPS", CoreURL: "https://core.example",
			HostFingerprint: "SHA256:abcdefghijklmnopqrstuvwxyz0123456789", SSHUser: "root", Authentication: "password",
		},
	}
}
