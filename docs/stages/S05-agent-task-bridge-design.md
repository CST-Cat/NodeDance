# S05 Core-to-Agent task bridge and dispatcher design

Status: **proposal for root review; no implementation or shared runtime changes
in this document's work item.** This bridge is a component design. It does not
complete the S05 acceptance rows or register a production wire handler.

## Goal and non-goals

Bridge a committed Core task to one authenticated Agent, durably record receipt
before the Agent acknowledges it, execute it once through the existing
container-action executor, and reconcile reports only against the exact node,
target, task, idempotency key, journal identity, and active connection
generation. The Agent taskrunner belongs to the process lifetime, not a
WebSocket session. Disconnects stop only transport work; already accepted jobs
continue to be journaled and executed. A real Agent process restart runs
`RecoverInterrupted`; it never resends or blindly replays a task that crossed
the durable delivery or execution boundary.

This proposal does not add routes, UI state, authentication, or production
WebSocket wiring. Those changes belong to the later shared Core/Agent
integration owner. It also does not broaden the allowlisted lifecycle intents
to include credentials, free-form commands, logs, or file payloads.

## Existing contracts to preserve

`core/tasks.Store.Enqueue` already commits a typed intent, request digest,
resource claim, and acceptance audit in one SQLite transaction. It checks
`NodeID + IdempotencyKey` and `TaskID` conflicts and uses the resource key
`docker-container:<64 lowercase hex ID>`. `ClaimNext` checks the active node
generation and accepted journal identity, then commits `delivery_state='sent'`,
`dispatch_journal_id`, and `delivery_committed=1` plus an audit record before
the transport can write a frame. A sent task is never automatically claimed
again. `ObserveAgentConnection` and `ReconcileAgentJournal` provide the current
journal identity barrier and complete-snapshot semantics; absent entries become
`unknown`, never queued again. `ResolveUncertainTask` and
`CompleteJournalReplacement` require explicit, proof-checked resolution.

`taskjournal.Store` has a persistent `JournalID`, a process-exclusive file lock,
deduplicating `Enqueue`, a full-ID resource claim, durable `BeginExecution`,
`RecoverInterrupted`, bounded redacted logs, and verified `Finish`. The
container-actions executor calls `Enqueue` and `BeginExecution` before any
Docker mutation, verifies a fresh Engine inspection, and exposes a no-mutation
`Reconcile` path for restart tasks with a same-boot persisted baseline.

The shared seven-state proof rules remain authoritative. `queued` is durable
receipt only. `running` needs actual Agent `ExecutionAttempted` evidence.
`succeeded` requires completed execution and a verified postcondition.
`unknown` is never replayed. A terminal report cannot contain arbitrary error
text or secret payload. Core owns `DeliveryCommitted`; an Agent report must not
be allowed to manufacture it.

## Proposed wire contract

Add typed DTOs and task message names in `internal/protocol/tasks.go`. Use the
existing version-1 envelope and payload limits, but negotiate an optional
`agent.task-bridge.v1` capability. A legacy Core filters that capability out
and the Agent sends no task-specific messages; a legacy Agent does not
advertise it and receives no task dispatches. The Agent and Core must not use
task messages until the capability is present in the authenticated Welcome.
This keeps the heartbeat/rotation contract unchanged; no unconditional new
Hello field is required.

Proposed message sequence:

1. After `Welcome`, the capable Agent sends `task_journal_hello` with its
   configured `NodeID` and the durable 64-hex `JournalID`.
2. Core calls `ObserveAgentConnection` using the authenticated node and current
   generation, then replies `task_journal_status`. It enables dispatch only
   when the reported journal is the accepted identity, no review barrier is
   active, and the connection still owns the current generation.
3. For an unchanged journal, Core requests a complete journal snapshot.
   `task_snapshot_page` messages carry one snapshot ID, contiguous page numbers,
   the same journal ID, and at most 32 reports per page. Core buffers the
   bounded snapshot and calls `ReconcileAgentJournal(..., complete=true)` only
   after the final page. It never treats an incomplete, duplicate, oversized,
   or interrupted snapshot as complete. Existing `decodeSocketPayload` limits
   a payload to 64 KiB; page encoding must remain below that bound.
4. Once synchronization succeeds, Core calls `ClaimNext` only when the Agent
   advertises free task capacity. It writes one `task_dispatch` with
   `Envelope.Generation` set to the authenticated connection generation and
   `Envelope.RequestID` equal to the task ID. The Core delivery claim is
   already committed before this write.
5. The Agent validates all bindings and the digest, reserves a bounded local
   slot, calls the journal's durable dispatch-accept operation, and queues the
   typed request. It then emits a `task_report` showing the durable `queued`
   receipt. The worker runs independently of the WebSocket session.
6. Each later `task_report` is built from the current persisted journal
   snapshot, not from an SDK error string. Core acknowledges a report only
   after its transactional validation and status update succeeds. Repeated
   reports are idempotent; a report conflict is rejected and audited.
7. A Core `task_reconcile_request` may ask the Agent to reconcile an existing
   `unknown` task. The Agent verifies the request identity and calls only the
   executor's no-mutation reconciliation method. Missing/invalid baseline,
   changed host boot ID, non-restart action, failed Inspect, or unchanged
   postcondition remains `unknown`.

Every task-bearing payload contains the explicit identity fields, even though
the envelope also carries generation and request ID:

```text
TaskDispatch {
  taskId, nodeId, journalId, idempotencyKey, requestDigest, intent
}

TaskReport {
  taskId, nodeId, journalId, targetId, idempotencyKey, requestDigest,
  status, evidence, progress, result
}
```

`requestDigest` is exactly 64 lowercase hex characters. The nested intent uses
the existing safe JSON names and shape: `action`, `container_id`, optional
`new_name`, and optional `delete_confirmed`. `DeleteConfirmationID` is not sent;
the Agent reconstructs that ephemeral binding from the full target ID after
Core has verified the administrator's explicit confirmation. No request may
contain a free-form secret or arbitrary body.

`TaskEvidence` is a tagged protocol DTO containing execution-attempted,
execution-completed, failure-confirmed, postcondition-verified,
process-terminated, actual-result-confirmed, and cancellation-confirmed. It
does not contain Core's `delivery_committed` bit. Result codes are limited to
the current safe codes (`verified`, `failed`, `timed_out`, `canceled`, and
`result_pending`); observed state and revision are bounded safe tokens only.
Queued/running reports have no terminal result. Unknown reports use
`result_pending`. The Core maps these DTOs to its existing local types and
rechecks every state transition with `taskstate.CanTransition`.

## One canonical intent and digest

Currently Core `tasks.Intent` and Agent `containeractions.safeIntent` are
separate structs with the same JSON tags and field order. Both independently
call `json.Marshal`, `taskstate.CanonicalJSON`, and `taskstate.RequestDigest`.
That happens to produce the same bytes today, but there is no shared source of
truth. The new protocol package should define the shared `TaskIntent`,
canonical intent encoder, and identity-to-digest helper. Core acceptance,
Core dispatch, Agent acceptance, and Agent lifecycle execution all use this
helper; tests compare a digest from each existing caller against the shared
golden digest.

The digest continues to cover the versioned `taskstate.Identity` envelope:
NodeID, the full target ID, `docker-container:<full target ID>`, action, and
canonical safe intent JSON. As today, `TaskID` and `IdempotencyKey` identify
the ledger entry and are checked separately rather than incorporated into the
request digest. `JournalID` is also separate from the digest: it binds this
dispatch to the exact Agent journal but does not change the user's intent.
`Envelope.Generation` binds a message to one authenticated live connection.

Before enqueue or execution, the Agent recomputes the digest from the typed
intent and checks it against the Core's digest using a fixed 32-byte decode and
constant-time equality. It also checks that the payload `NodeID` equals local
configuration, the payload `JournalID` equals `taskjournal.Store.JournalID()`,
the active session generation matches the envelope, `targetId` is exactly 64
lowercase hex characters, and the request ID equals the payload task ID. Any
mismatch produces no execution. The Core repeats the corresponding checks
inside its report transaction against the stored task and active journal.

## Agent process-lifetime taskrunner

`internal/agent/taskrunner` receives an already-open journal and a constructed
container-actions executor. It owns process-scoped cancellation, a bounded
work queue, a fixed worker count (initial default: two workers), a bounded
report wake-up queue, per-task in-flight de-duplication, and the current
connection generation. The session adapter calls `Connect`, `AcceptDispatch`,
`SnapshotPages`, `Reconcile`, revision-bound `AcknowledgeReport`, and
`Disconnect`; these APIs do
not expose a WebSocket dependency.

At process startup, after the journal's single-process lock is acquired and
before any connection can accept tasks, `Start` calls `RecoverInterrupted`
exactly once. A WebSocket disconnect only detaches that generation's sender;
it never cancels worker contexts or calls recovery. Worker contexts derive
from the taskrunner process context, not the connection context. Process
shutdown cancels and joins workers with a bounded deadline. Any uncertain
in-flight operation remains journaled `unknown` with its resource claim.

Admission reserves bounded capacity before writing the Agent journal. The
dispatcher atomically stores a Core-delivery marker with the typed task
identity before reporting receipt. A journal/database failure or ambiguous
commit does not start an Engine operation. Core sends only within the Agent's
advertised capacity credits; the Agent still rejects an unexpected over-cap
dispatch before it can execute. A rejected or lost delivery never clears the
Core's committed-dispatch claim and never authorizes resend.

After successful admission, the runner's in-memory job contains the typed
request and the worker calls `containeractions.Executor.ExecuteObserved`.
After `BeginExecution` commits, the executor confirms the durable `running`
row and its real `ExecutionAttempted` evidence with a bounded journal read,
then invokes a nonblocking observer before starting Docker preflight or
mutation. A failed or mismatched read becomes `unknown` and does not issue an
Engine call. This lets a slow SDK call be reported as `running` while it is
still in progress; the runner does not invent execution evidence itself.
If a duplicate dispatch arrives in the same process, task ID/key/digest are
checked, then the in-flight map or journal status prevents a second worker. A
terminal, running, or unknown journal entry is report-only. An unknown task
never enters the work queue. If the process stops after a Core-delivered task
was durably accepted but before `BeginExecution`, startup recovery conservatively
changes that delivered queued record to `unknown`; it does not reconstruct or
replay a request from a stale connection. A task already `running` likewise
becomes `unknown`. Only completed terminal entries remain terminal.

The report queue contains task IDs as wake-up hints, not authoritative result
copies. The sender reloads the latest sanitized state from SQLite, so multiple
rapid progress updates can coalesce. On queue overflow it sets a bounded
`snapshot-dirty` flag instead of blocking workers or losing durable results;
the sender requests/emits a full snapshot when the connection permits. Each
live report has a monotonically increasing in-process `reportRevision`; its
acknowledgement echoes both task ID and revision. An older queued or running
ack cannot clear a newer terminal notification. A periodic full snapshot
repairs lost notifications on a still-live connection.
Disconnect discards only transport notifications. The journal remains the
source of truth.

`Stop` shares one join completion channel and one `WaitGroup` waiter across
all callers. Repeated short-deadline shutdown attempts cannot create an
unbounded number of waiter goroutines. A timeout still does not claim that a
blocked Docker SDK request stopped; shutdown can be retried after the request
returns, and process restart recovery remains conservative.

## Implementation status and remaining integration

| Existing mismatch | Smallest required change | Owner boundary |
|---|---|---|
| Core `Intent` and Agent `safeIntent` duplicate canonical JSON logic. | A shared protocol intent/digest helper is used by Core task and Agent action components, with cross-package validation tests. | Implemented in isolated packages; no shared runtime integration. |
| Agent journal has no Core-delivery marker. | `EnqueueDelivered` stores delivery with the identity, digest, and resource claim; v3 migration preserves v2 as undelivered; startup changes delivered `queued` and interrupted `running` to `unknown` without releasing claims. | Implemented in `internal/agent/taskjournal`; no legacy delivery is inferred. |
| Agent journal snapshots omit idempotency key and request digest, and no bounded consistent list exists. | `SnapshotAll` returns typed identity and status in one bounded SQLite read transaction; `SnapshotPages` freezes the complete candidate before splitting it into wire pages. | Implemented in isolated journal and taskrunner packages. |
| Agent reports lack transaction-checked identity binding in `core/tasks.AgentTask`. | Core checks node, journal, target, idempotency key, and digest against the stored task inside the report transaction; Agent evidence cannot set Core-owned delivery proof. | Implemented and tested with real SQLite in the Core task-store component. It is not yet connected to Agent reports over a live WebSocket. |
| `ReconcileAgentJournal` accepts bounded summaries, while one protocol payload is capped at 64 KiB. | The Agent emits contiguous bounded snapshot pages from one frozen candidate. Core still needs a live WebSocket page assembler that validates the complete candidate before applying one final transaction. | Remaining shared Core WebSocket integration. |
| `ObserveAgentConnection` calls `cancelStaleUndelivered`; an older restored Core backup may have `delivery_committed=0` even though a post-backup dispatch ran. | The restore/bootstrap path must set a recovery barrier before any Agent observation and must not use stale `delivery_committed=0` as proof of non-delivery. Ordinary full SQLite/WAL restart and old-backup restore are distinct. | S17/Core storage integration; not solved by this Agent bridge. |
| Core task acceptance checks online lease but not the negotiated task capability; current Core and Agent WebSockets handle only heartbeat/credential rotation. | The authorized Core/Agent integration must gate task routes/`ClaimNext` on `agent.task-bridge.v1`, persist the authenticated node/generation/journal tuple, call `ObserveAgentConnection`, run the bounded publisher, validate generations on every message, and add the task message cases. Agent runtime creates one runner before its reconnect loop and attaches/detaches sessions without stopping workers. | Explicit future ownership for shared server/runtime/WebSocket files; not changed here. |
| Current Agent action `Execute` combines enqueue, begin, mutate, inspect, finish; `Reconcile` is restart-only. | The runner calls the journal's dispatched-enqueue operation, then passes the typed request to `ExecuteObserved`; the observer emits a report wake-up only after a durable, evidence-checked running read. Duplicate enqueue is idempotent. Reconcile requests remain read-only. | Implemented in the isolated Agent component. Shared runtime delivery to Core remains future work. |

Journal identity replacement remains a review barrier. If Core observes a
different `JournalID`, it disables dispatch and does not automatically apply
that snapshot as if it belonged to the old history. Existing Core tasks stay
unknown/reconciliation-required with resource claims held until the explicit
review/replacement path resolves them. The initial bridge does not invent
history or silently accept a replacement journal.

## Component acceptance after design approval

The isolated protocol tests should prove canonical byte/digest agreement,
strict payload decoding, full-target and journal binding, idempotency/task-ID
conflicts, rejected stale generations, and omission of Core-only delivery
evidence. The taskrunner tests should use real temporary SQLite journals to
prove: journal persistence precedes receipt; worker concurrency and admission
are bounded; duplicate delivery never starts a second executor; resource
claims reject conflicting tasks; disconnect leaves accepted work running;
reconnect returns a journal snapshot without replay; process startup recovers
running and delivered queued work to unknown; restart reconciliation never
mutates Docker; unknown claims remain held; failure/ambiguous journal commits
cause zero Engine calls; result-queue overflow is repaired from durable state;
and raw secrets or free-form error strings are absent from wire reports and
SQLite.

These isolated checks do not replace the later authenticated Core-Agent-Docker
full-chain, server restart, WebSocket disconnect/reconnect, stale generation,
real SQLite recovery, and browser progress tests. Until the shared integration
and those acceptance runs land, the S05 task bridge and full S05 stage remain
`NOT_READY`.
