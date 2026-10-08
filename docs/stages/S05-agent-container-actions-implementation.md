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
3. Commit `BeginExecution` before sending any Docker mutation.
4. Inspect the exact full container ID and validate action preconditions.
5. Send no more than one mutation for the task.
6. Use a fresh, bounded, cancellation-independent Inspect to verify the
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
