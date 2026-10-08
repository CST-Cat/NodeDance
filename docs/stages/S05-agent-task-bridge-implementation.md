# S05 Agent Task Bridge Component

Status: **isolated component implemented; full S05 remains NOT_READY**.

This component adds the protocol and process-lifetime Agent dispatcher that
connects Core's durable task identity to the Agent task journal and the
verified container-actions executor. It does not register new message types
in the shared WebSocket runtime and does not provide authenticated HTTP or
WebSocket integration.

## Durable admission and recovery

The bridge is available only after `agent.task-bridge.v1` is negotiated and a
complete, acknowledged journal snapshot for the current connection generation
has completed. A dispatch must bind the exact node, journal ID, generation,
task ID, 64-character container ID, idempotency key, canonical intent, and
digest. The dispatcher reserves bounded capacity, commits `EnqueueDelivered`
to SQLite, and only then returns its receipt. Receipt/database errors,
including ambiguous commit results, do not invoke the executor. An exact retry
may inspect the durable row; a conflicting task identity or resource claim is
rejected.

The journal v3 migration does not infer prior Core delivery for v2 rows.
Process startup runs `RecoverInterrupted` before dispatch admission. Delivered
queued and interrupted running rows become `unknown`, retain their resource
claims, and are not sent to the Engine again. A connection replacement only
changes the generation-bound transport; accepted in-process work continues
under the runner's process context. Reconciliation uses the existing
read-only container-actions baseline path.

## Running reports, revisions, and shutdown

`ExecuteObserved` invokes its running observer only after `BeginExecution` has
committed and a bounded journal read confirms both `running` and the Agent's
`ExecutionAttempted` evidence. The runner turns that callback into a bounded
report hint before the executor starts Docker preflight or mutation. If the
running row cannot be confirmed, the executor records `unknown` and makes no
Engine call. A slow Engine call can therefore be drained from the runner as a
real SQLite-backed running report before it finishes. The shared runtime does
not yet deliver this report to Core over a live WebSocket.

Each immediate receipt and later live report has a monotonically increasing
in-process `reportRevision`. The acknowledgment must name the task and that
revision. An old queued or running acknowledgment returns
`ErrStaleReportAck` when a newer report is pending, so it cannot clear the
terminal notification. Complete snapshots continue to repair lost reports;
snapshot acknowledgments are guarded by the captured global revision.

`Stop` cancels process-scoped work and uses one `sync.Once` join waiter and one
shared completion channel. Repeated callers may use separate deadlines without
creating more waiters. If an executor ignores cancellation, each short Stop
can time out; after the executor returns, another Stop joins the same worker
set.

## Verification evidence

Tests use real temporary SQLite databases and transaction-backed journal
operations. The runner tests verify durable receipt ordering, duplicate
dispatch, resource conflicts, ambiguous journal commits, frozen pagination,
process restart recovery, stale generations, running notification while a
fake Engine mutation is blocked, old queued/running acknowledgment rejection
after a terminal report, and repeated timed-out Stop followed by a successful
join after releasing a blocked executor.

The Core task-store identity/audit checks pass in a separate real-SQLite
component test. They are not wired to the taskrunner over a live connection.
Snapshot tests also hold a candidate at a deterministic gate, inject a newer
notification, prove that the old snapshot ACK leaves the dirty barrier set,
then verify that a later stable snapshot clears it. The overflow test waits
for all accepted jobs to leave `activeTasks`; `runJob` emits each terminal
notification before removing its task, which is the synchronization boundary
needed to test a stable snapshot without timing guesses.

The following commands passed after the final changes:

```text
go test ./internal/protocol ./internal/core/tasks ./internal/agent/taskjournal ./internal/agent/containeractions ./internal/agent/taskrunner
go test -race ./internal/protocol ./internal/core/tasks ./internal/agent/taskjournal ./internal/agent/containeractions ./internal/agent/taskrunner
go vet ./internal/protocol ./internal/core/tasks ./internal/agent/taskjournal ./internal/agent/containeractions ./internal/agent/taskrunner
```

These are component checks. They do not validate real Docker execution through
the new task bridge, shared runtime authentication, Core-Agent HTTPS/WebSocket
delivery, or browser progress. The normative S05 cases remain `NOT_READY`
until those integrations and their full-chain evidence are completed.
