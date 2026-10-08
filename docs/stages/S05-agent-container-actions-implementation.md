# S05 Agent Docker lifecycle executor component

Status: **component implemented and isolated verification passed**. This report
does not mark S05 complete. Runtime and transport integration, routes, browser
progress/result behavior, logs, and resource-stat subscriptions remain outside
this component and still require their own acceptance.

## Scope and trust boundaries

`internal/agent/containeractions` executes only these safe, typed operations:

| Action | Engine operation | Verified postcondition |
|---|---|---|
| `start` | Start | Running, not paused, not restarting |
| `stop` | Stop | Stopped, not paused, not restarting |
| `restart` | Restart | Running and not paused/restarting, with a changed valid `StartedAt` or increased `RestartCount` |
| `pause` | Pause | Running and paused |
| `resume` | Unpause | Running and not paused/restarting |
| `rename` | Rename | Fresh Inspect reports the requested name |
| `delete` | Remove | A fresh Inspect returns typed Docker `NotFound` |

The component has no HTTP or WebSocket surface and imports no Core task package.
Its typed `Request` contains the Core task identity fields and exactly the same
non-secret intent fields as `internal/core/tasks.Intent`: `action`,
`container_id`, optional `new_name`, and optional `delete_confirmed`. The
canonical intent digest therefore binds the full target ID and delete
confirmation across Core and Agent. A short ID, name, alias, or uppercase ID is
rejected.

`DeleteConfirmationID` is an ephemeral repeat binding check. A delete request
must set `DeleteConfirmed` and provide the exact full `ContainerID` in
`DeleteConfirmationID` before the component writes anything to the Agent
journal. The confirmation ID is excluded from the persisted intent because it
duplicates the target; the target and `delete_confirmed` remain in the digest.

Compose classification is derived from the actual Docker Inspect response in
the SDK adapter. Any inspected `com.docker.compose.*` label prevents rename; no
request boolean can bypass this. Deletion rejects running, paused, or restarting
containers. The operator must submit a separate stop task first. SDK removal
uses `RemoveVolumes=false` and `Force=false`; it never silently stops or forces
a container and never removes attached volumes.

## Durable execution and recovery behavior

The executor performs the following sequence for each request:

1. Validate the complete typed request and its Core-compatible canonical
   digest.
2. Call the Agent journal's transactional `Enqueue`, which durably stores the
   request summary, idempotency digest, and full-ID resource claim.
3. Commit `BeginExecution` before entering the executor.
4. Inspect the exact full container ID and validate action preconditions.
5. Read the host boot ID and atomically persist the typed, sanitized Inspect
   baseline together with `mutation_may_have_started` before a Docker write.
6. Send no more than one mutation for the task.
7. Use a fresh, bounded, cancellation-independent Inspect to verify the
   postcondition before recording `succeeded`.

An already `running` request is reported in progress and is not repeated. An
`unknown` request is returned as unknown and is never replayed; its resource
claim remains held in the journal. The caller may invoke the existing
`RecoverInterrupted` only after the former Agent process is known to have
stopped. The executor does not run recovery implicitly and does not convert
missing Engine connectivity into success.

If a mutation call times out, is canceled, returns an ambiguous error, or its
post-operation Inspect cannot complete, the executor records `unknown` when
possible. If the postcondition is visibly satisfied despite a lost mutation
acknowledgement, the executor can record `succeeded`. It does not store raw SDK
errors or Inspect payloads in the task result. Result fields contain only safe
state tokens and bounded revision tokens.

`PrepareMutation` stores only the full target ID, typed action, host boot ID,
Docker `StartedAt`, restart count, and running/paused/restarting flags. The
phase means the mutation **may** have started after that commit; it does not
prove an SDK request was sent. If baseline persistence fails or its commit
outcome is uncertain, the executor issues no Docker write and leaves the
resource claim held. A crash after the baseline commit but before the Engine
call therefore recovers as `unknown` with the unchanged resource, and the call
is not repeated.

The journal schema migrates transactionally from v1 to v2. Legacy tasks get
`execution_phase=none` and no verified baseline; recovered legacy `running`
tasks become `unknown`, retain their claims, and cannot be replayed. A failed
migration leaves schema version and existing rows unchanged, and a journal
with a newer `user_version` is refused. Normal same-database process restart
uses SQLite WAL recovery. Restoring an older Core/Agent database backup is a
separate S17 case: a row with `mutation_may_have_started` absent in that backup
does not prove that no request was sent after the backup, so restoration must
mark affected nonterminal tasks for Agent-journal/real-resource confirmation.

Preflight Inspect uses a bounded executor deadline even when the caller passed
`context.Background()`. The Moby SDK HTTP client also has a 30-second whole
request timeout, so a socket-connected but unresponsive daemon cannot hold an
HTTP exchange open forever. Journal reads performed with a detached context
also use a bounded deadline.

For restart, a final `Running` state alone is insufficient. The before and
after snapshots must show a different valid, nonzero `StartedAt`, or an
increased `RestartCount` when timestamp evidence is unavailable. The previous
time need not be earlier than the new one: a nonzero changed timestamp can
still prove a new start after wall-clock correction. Docker's zero timestamp is
valid only for a stopped, never-started container; a running or paused snapshot
with that timestamp is rejected as inconsistent. A restart with unchanged
instance evidence becomes `unknown` and keeps its resource claim.

Restart reconciliation is allowed only for an `unknown` task with a verified
baseline whose host boot ID matches the current boot. A fresh Engine Inspect
must prove the restart postcondition and show a changed nonzero `StartedAt` or
an increased `RestartCount`; otherwise the task remains unknown. A boot ID
mismatch, missing or malformed baseline, and old running records without a
baseline all fail closed. Reconciliation never calls Restart. A changed
post-baseline state proves a restart-like transition, not that this Agent sent
the original request or that exactly one Engine call occurred: an external
actor may have changed `StartedAt` or `RestartCount`. The audit only records a
verified postcondition and a typed evidence revision, never an invented call
count.

## Tests and acceptance evidence

The unit suite uses the real SQLite-backed Agent task journal, not a mocked
journal. It covers all seven operations, digest compatibility, full-ID delete
confirmation before any write, Compose rename denial, running-delete refusal,
concurrent duplicate delivery, cross-task resource locking, unknown/no-replay,
restart instance proof, lost acknowledgements, and a non-typed Inspect error
whose message contains “not found”. It also uses a real Unix HTTP server that
stalls before response headers or in a partial response body. The actual Moby
SDK call with `context.Background()` times out, while an executor configured
with a longer SDK limit is stopped by its shorter preflight deadline; both
paths verify there was no Docker mutation, and the latter reads back the real
SQLite journal's confirmed failure. That error-text case confirms that error
text alone can never prove deletion.

Journal tests use actual temporary SQLite databases for baseline persistence,
trigger-injected write failure, v1-to-v2 transactional migration rollback,
future-schema refusal, and legacy-running recovery without synthesized proof.
Bounded READY/SIGKILL subprocess tests kill the executor both after durable
baseline commit but before the Engine mutation, and after a simulated Engine
mutation but before result persistence. The unchanged pre-mutation resource
stays unknown and is never retried; the changed post-mutation resource can be
resolved from its same-boot baseline without issuing another restart. A
separate real-Docker test performs the same two process-kill boundaries against
uniquely labeled containers on each marked DIND daemon, then reopens the real
SQLite journal and checks the pre-mutation and post-mutation outcomes.

The opt-in DIND test creates unique, labeled fixtures using the digest-pinned
BusyBox image from `test-images.lock.json`. Fixture setup and cleanup use the
Docker CLI against the nested DIND socket; every lifecycle action goes through
the Moby Go SDK executor. Cleanup checks the exact test-suite label and removes
only the test's exact container IDs and volume names. It never runs a prune
command and never contacts the outer Docker API to mutate resources.

The test validates the DIND `owner.json` marker, fixed Engine container name,
version, image digest, socket path, and Unix socket before using it. Set the
environment variable to a marked repository-owned directory to run it:

```bash
NODEDANCE_S05_DIND_ROOT=/path/to/NodeDance/.artifacts/dind/v28 \
  go test -mod=readonly -count=1 -v ./internal/agent/containeractions \
  -run '^TestDINDLifecycleActions$'

NODEDANCE_S05_DIND_ROOT=/path/to/NodeDance/.artifacts/dind/v29 \
  go test -mod=readonly -count=1 -v ./internal/agent/containeractions \
  -run '^TestDINDLifecycleActions$'

NODEDANCE_S05_DIND_ROOT=/path/to/NodeDance/.artifacts/dind/v28 \
  go test -timeout=3m -mod=readonly -count=1 -v ./internal/agent/containeractions \
  -run '^TestDINDRestartSIGKILLRecoveryNeverReplays$'

NODEDANCE_S05_DIND_ROOT=/path/to/NodeDance/.artifacts/dind/v29 \
  go test -timeout=3m -mod=readonly -count=1 -v ./internal/agent/containeractions \
  -run '^TestDINDRestartSIGKILLRecoveryNeverReplays$'
```

Without that variable the DIND test is `SKIP`; it is not evidence that either
Engine was verified. The implementation was exercised on **Docker Engine
28.5.2** and **29.7.2** using the marked NodeDance test daemons in
`/home/ubuntu/codex-use/NodeDance-s04/.artifacts/dind/v28` and `v29`.
Both runs passed. Each run verified stop/start, pause/resume flags, restart
after a simulated lost acknowledgement, rename, actual Inspect-label-based
Compose rename rejection, refusal to delete a running container, separate stop
then delete, typed post-delete NotFound, and survival of a named volume.
The same runs also restart a never-started `created` container and require its
first nonzero `StartedAt` to prove the new instance. A separate live container
was passed through an adapter whose restart returned success without making a
Docker call; unchanged `StartedAt` and `RestartCount` correctly left the task
`unknown` rather than accepting `Running` alone. The exact per-run suite labels,
container IDs, volume names, and owned socket paths are printed in the verbose
test output.

The latest lifecycle runs, after baseline persistence was added, reported these
exact fixture identities:

| Engine | Suite and exact fixture IDs | Result and evidence |
|---|---|---|
| Docker 28.5.2 | Suite `nodedance-s05-container-actions-cceacdfe380e`; main `e50bcc40f86209de0f31fe0536be632f84233066494136d93c83b3fa5a7a33b8`; Compose-managed `dc7753cadbae05fbb6087a3ca270baab355b6b4e5cd8d2b44b592b1517ee5306`; never-started `31c0917ac60e72690b7fc4bbfa1d00d9e32202bb8745d0b4dc5954119e232bd0`; no-op restart `c2a077c5616609eecbc96f9cddabbeaa46a0fd8a054e983bbb2d319b55894bca`; volume `nd-s05-28-cceacdfe380e-data`. | `PASS`, 32.28 s. Raw output: `.artifacts/s05-evidence/containeractions-lifecycle-v28-final.txt`. |
| Docker 29.7.2 | Suite `nodedance-s05-container-actions-e1d00f760a24`; main `a411aa8e75b932f3bb9ecd9428e1e0a7900c996b4db9b9c5e5ce2f0aca5cfd69`; Compose-managed `78d1f83b99a37d7e7e8f61e099da4cd01f83c91a783a31148c5add5cd50a9b8a`; never-started `fa251aa702f686cd9f8efd5d5849819308c2a1ee069dec0fbd26fe1931716fe0`; no-op restart `5fa9c5c3b677249006d0dc5d50a4934d15f8a4092c62007587a49a22430c4144`; volume `nd-s05-29-e1d00f760a24-data`. | `PASS`, 32.31 s. Raw output: `.artifacts/s05-evidence/containeractions-lifecycle-v29-final.txt`. |

The real-Docker SIGKILL runs both passed. Before the SDK mutation, each task
recovered as `unknown`, remained unchanged, and refused a duplicate request;
after the real restart mutation but before Finish, each task reconciled from
the durable same-boot baseline to `succeeded` without another restart:

| Engine | Suite and exact fixture IDs | Result and evidence |
|---|---|---|
| Docker 28.5.2 | Suite `nodedance-s05-action-sigkill-5bab0d5179ba`; before mutation `15575cfe242b1f1e28d2abe8649c500864e766586b97e4059715e5f1c00e62fc`; after mutation `77931786d20ce32109a5588473de098a64119d63950757e8db8258bc189f58e5`. | `PASS`, 10.95 s. Raw output: `.artifacts/s05-evidence/containeractions-sigkill-v28-final.txt`. |
| Docker 29.7.2 | Suite `nodedance-s05-action-sigkill-0d817f8086b1`; before mutation `8e93fd9d021a31b58c19f7e3692c5559a63a929bce286d15ade1d0b57871dbd5`; after mutation `25e9789446a31d82c39a78e0ab697b9949bc413dacfe79290afcf4e841de06d7`. | `PASS`, 15.41 s. Raw output: `.artifacts/s05-evidence/containeractions-sigkill-v29-final.txt`. |

The test initially used an eight-second READY deadline. The retained failed
v28 output at `.artifacts/s05-evidence/containeractions-sigkill-v28-ready8-failure.txt`
shows the second checkpoint timed out at 8.80 seconds. In the passing runs the
real restart returned after 10.217 seconds on v28 and 10.259 seconds on v29;
the test does not assert whether BusyBox PID 1 handled SIGTERM. The Moby SDK
HTTP client has a 30-second whole-request bound (the executor operation
context is 45 seconds), so the READY deadline is 40 seconds, leaving ten
seconds for test startup and preflight. Each `CommandContext` child is limited
to 50 seconds, the two-stage test context to 150 seconds, and the documented
`go test` command to three minutes. Cleanup still kills and waits at most five
seconds and joins the READY reader within one second.

Unit and race commands:

```bash
go test -mod=readonly -count=1 ./internal/agent/containeractions
go test -race -mod=readonly -count=1 ./internal/agent/containeractions
go vet -mod=readonly ./internal/agent/containeractions
```

## Known integration work

This component does not yet connect Core's durable task sender to the Agent
journal, provide authenticated task routes, reconcile Core's task state from
Agent journal IDs, or expose browser progress/results. It does not implement
logs or opt-in resource statistics. Its verified state is therefore limited to
the isolated lifecycle executor and the concrete DIND/unit cases above; the
original full S05 acceptance set remains pending.

The isolated executor now persists its restart baseline and supports same-boot
postcondition reconciliation. Runtime/transport integration still must bind
that reconciliation to an authenticated task and the correct journal ID. A
real-Docker SIGKILL-at-restart-boundary test passed on both marked DIND
versions, but it proves only the isolated Agent component. The deterministic
subprocess test also covers both pre-mutation and post-mutation kill points.
The baseline cannot prove causality when an external actor restarts the same
container, and a changed boot ID always leaves the task unknown.
