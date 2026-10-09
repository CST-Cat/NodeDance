# S05 Core task persistence component

**Formal S05 status: `NOT_READY`.** This component supplies Core task persistence and state reconciliation primitives. It has no authenticated HTTP route, Agent transport integration, Docker executor, or real Engine postcondition checks. It does not pass any of the normative `S05-01` through `S05-12` cases.

## Scope and integration boundary

The implementation lives in `internal/core/tasks`. `schema.sql` is an independent migration fragment. The package exposes `SchemaSQL` and `SchemaStatements`, but `New` never creates or migrates tables. The production storage migration ledger still needs a separately reviewed migration registration.

The package takes an existing Core `*sql.DB` and writes its task, claim, and task audit rows within SQLite transactions. It does not authorize users. Future HTTP handlers must apply the existing admin Session, CSRF, node ownership, Origin, and request validation rules before calling it. The package reports only typed summaries; it does not accept command text, arbitrary JSON, credentials, or freeform errors.

The S05 `Intent` supports start, stop, restart, pause, resume, confirmed delete, and standalone rename. A Docker container target must be the full 64-character lowercase Engine ID. Names and short IDs are rejected so they cannot create alternate resource locks for the same container. `ComposeManaged` is trusted Core context supplied from the current Docker asset model; an HTTP handler must ignore any client boolean and derive the value from trusted inventory. Rename is rejected when that classification is true.

The current Core payload has no secret-bearing fields. Registry credentials and other future secrets need a separate temporary or encrypted transport contract and a keyed digest design before they can be added to persisted task intent.

## Durable acceptance and delivery

`Enqueue` validates a typed intent, hashes its canonical encoding with `taskstate.RequestDigest`, then commits the queued task, task audit event, and node/container resource claim in one transaction. It checks the current node status and heartbeat lease in that same transaction. An offline, expired, or future-dated lease cannot create a new dangerous task. A duplicate lookup returns the original task even while the Agent is offline; it creates no new work.

An exact retry with the same node, idempotency key, and digest returns the stored task. A key used with another request conflicts. A task ID reused for a different key, node, or digest conflicts, including the crossed collision where a known key is paired with another task's ID. An unused retry task ID may accompany a matching idempotent request and receives the original task ID.

Acceptance records the current authenticated connection generation. A queued task accepted online but never sent cannot silently survive an Agent reconnect and run later: `ClaimNext` selects only work accepted in that connection generation, and the next authenticated connection marks stale-generation, never-sent tasks canceled as `not_dispatched` before delivery.

`ClaimNext` requires a current online lease, matching connection generation, an observed Agent `JournalID`, and no journal replacement barrier. It commits `delivery_state='sent'`, `delivery_committed=1`, the JournalID, and a typed audit event before returning the task. The caller may write to the WebSocket only after this method returns successfully. A sent task is never selected again by `ClaimNext`.

`DeliveryCommitted` is specific evidence that Core committed an outbound delivery attempt before writing to the Agent connection. It does not assert receipt, executor entry, or Docker execution. The shared state machine permits a committed queued task to become `unknown` if the result is lost. This evidence cannot satisfy `running`, `succeeded`, or other terminal result proofs. A success still requires Agent execution attempt, completion, and a fresh verified postcondition.

An Agent `running` report must contain the Agent's durable `ExecutionAttempted` evidence. Core does not manufacture that value. If a verified terminal report arrives before the Core received the running acknowledgement, Core commits the implied queued-to-running audit transition and the terminal transition in one SQLite transaction using the report's actual evidence. Any missing proof rejects the whole transaction. A queued task can also reach a confirmed failure or cancellation before execution only when the matching shared proof rules pass.

Nonterminal Agent reports cannot carry result payloads: `queued` and `running` require an empty result; `unknown` permits only an empty result or `result_pending`, with no observed-state or resource-revision fields. This prevents untrusted freeform text or secrets from entering task result columns through progress updates.

Queued, running, and unknown tasks keep their resource claim. Only a proof-checked terminal result releases it in the same transaction that stores the result. Failed writes, failed audits, failed claims, and invalid proof leave no partially accepted task.

## Agent JournalID and missing history

The first authenticated Agent connection records the durable JournalID. A later connection with the same ID can submit typed journal summaries. A changed ID sets a pending ID and a reconciliation barrier. All sent unresolved work is blocked from further delivery; running tasks move to `unknown`, and already queued-but-sent tasks move to `unknown` using `DeliveryCommitted`, without claiming Agent execution. Claims remain held.

An incomplete Agent list makes no inference from missing entries. A complete list under the same JournalID also does not prove that an absent task never ran. Agent retention, a restored older database, or other history loss can remove records without changing the ID. A sent task missing from a complete list therefore becomes `unknown` and stays blocked. There is no automatic replay based on absence.

`ResolveUncertainTask` accepts only a caller-supplied terminal result that passes the shared evidence rules. The eventual caller must first inspect the real Docker resource and provide evidence supported by that inspection; this module cannot verify the Engine. `CompleteJournalReplacement` adopts the pending JournalID only after every flagged task has been resolved. If the available evidence cannot prove a terminal result, the task and claim remain unresolved.

`ReconcileAgentJournal` accepts a bounded list of summaries, rejects duplicate task IDs, and checks the authenticated current lease generation and JournalID. A task unknown to Core, a stale connection, a changed JournalID, or a failed postcondition leaves the report uncommitted. These methods are not routes and are not a substitute for server-side authentication.

## SQLite schema and retention

The migration fragment defines `core_tasks`, `core_task_resource_claims`, `core_task_audit_events`, and `core_task_agent_state`, using foreign keys to the existing `nodes` table. All values are SQL parameters. An SQLite trigger rejects deletion of queued, running, and unknown tasks. Claims use a primary key on `(node_id, resource_key)` and a unique task reference.

There is no retention deletion function in this component. Any later 90-day pruning must select only verified terminal tasks and their corresponding audit rows. It must preserve all unresolved tasks and their evidence; it must not turn a missing task row into proof that an Agent never executed the task.

## S17 Backup-Restore Boundary

Restoring an older Core backup is different from reopening the latest SQLite database after a normal process restart. In an old backup, `delivery_committed=0` proves only that the delivery transaction had not committed at the backup point; a later live Core may have delivered and executed the task before the backup was restored. Therefore, S17 restore handling must not use `cancelStaleUndelivered` or the restored `delivery_committed=0` bit to claim `not_dispatched`. The restore path must place every nonterminal task whose delivery state may have advanced after the backup into reconciliation/unknown handling, retain its resource claim, and confirm it against the current Agent journal and actual Engine state before resolving it. A normal restart that recovers the latest consistent SQLite database/WAL keeps the existing real-time transaction proof and does not by itself require treating every never-sent queued task as backup-ambiguous. This component does not implement old-backup recovery; S17 owns that integration.

## SQLite test evidence

Tests create a real temporary SQLite database, apply the standalone SQL statements explicitly, and exercise the actual package methods. Covered checks include:

- Atomic task, audit, and resource claim creation before any delivery claim.
- Duplicate idempotency requests, different-body conflicts, task-ID/key crossed collisions, and concurrent duplicate submissions.
- Offline, expired, and future heartbeat leases; stale generations; and durable single delivery claims.
- Injected SQLite audit-write failures rolling back both acceptance and the pre-send delivery claim, in addition to a read-only database failure leaving no partial acceptance.
- An accepted but unsent task canceled at the next Agent generation, proving a stale dangerous action is not delivered after reconnect.
- Resource lock retention until proof-checked completion.
- Rejection and rollback for a running report without Agent execution evidence and a success without postcondition proof.
- Rejection and rollback of nonterminal reports carrying result/observation payloads, including a secret-string regression, while valid progress and `result_pending` reports remain accepted.
- A terminal success arriving before the Core receives the running acknowledgement, with both real evidence-backed audit transitions.
- An Agent's persisted queued record being acknowledged without marking execution or allowing a second Core delivery.
- Queued delivery loss becoming unknown using `DeliveryCommitted` while `ExecutionAttempted` stays false.
- Incomplete snapshots not being authoritative, same-JournalID history gaps not causing replay, and changed JournalID blocking dispatch until explicit resolution.
- Read-only SQLite write failure leaving no task, audit, or claim rows.
- A child Go test process committing a task, emitting `READY`, then being killed; the parent reopens SQLite and verifies the queued task, audit row, and claim. The READY wait is bounded to five seconds, the process context to fifteen seconds, and kill/wait cleanup to five seconds.
- The Agent journal's four-stage kill/reopen recovery regression also has a five-second READY deadline, a per-child process context, and immediate cleanup that kills and reaps only that test's child.
- SQLite trigger protection against pruning unresolved tasks and typed intent rejection for short IDs, unsupported actions, unconfirmed delete, invalid names, and trusted Compose-managed rename.

Run the owned component and state-machine tests with the pinned Go binary:

```bash
/path/to/go1.26.8/bin/go test -mod=readonly ./internal/taskstate ./internal/core/tasks ./internal/agent/taskjournal
/path/to/go1.26.8/bin/go test -mod=readonly -race ./internal/taskstate ./internal/core/tasks ./internal/agent/taskjournal
/path/to/go1.26.8/bin/go vet -mod=readonly ./internal/taskstate ./internal/core/tasks ./internal/agent/taskjournal
```

These component checks do not exercise a real Agent WebSocket or Docker Engine and do not count as the required complete end-to-end S05 run. The twelve normative S05 case IDs remain `NOT_READY` until the server, Agent delivery, real Docker operations, postcondition inspection, failure recovery, routes, and acceptance evidence are integrated.
