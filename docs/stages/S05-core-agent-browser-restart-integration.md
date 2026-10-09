# S05 Core-Agent browser restart integration

Status: **integration implementation present; formal S05 remains `NOT_READY`.**

This closure connects the existing durable Core task store, Agent task journal,
container lifecycle executor, and task runner to the authenticated Agent
WebSocket. It adds a private REST task API and a responsive container restart
control to the node dashboard. It implements the first real lifecycle closure
(`restart`) only. It does not complete the other six lifecycle actions,
container logs/stats transport, the complete S05 acceptance set, or S05 itself.

## Behavior in this integration

1. Core accepts a typed intent only through a valid administrator Session,
   Origin, and CSRF check. The route requires the complete Docker container ID
   and an `Idempotency-Key`. It derives Compose classification from the current
   fresh Core Docker inventory, never from the request body.
2. The Core task store commits the queued intent, audit event, and per-resource
   claim in one SQLite transaction. The connection may dispatch only after its
   Agent task journal has completed the full journal snapshot handshake.
3. Core commits a `sent`/`delivery_committed` claim before writing the task
   frame. A disconnect after that point is reconciled from the Agent journal;
   an uncertain outcome becomes `unknown` and is not automatically resent.
4. The Agent advertises `agent.task-bridge.v1` only when its durable journal,
   Docker SDK adapter, executor, and process-lifetime runner have started.
   The journal hello is sent only after that capability is negotiated. The
   runner and its accepted work outlive an individual WebSocket connection.
5. A task receipt is committed before Agent acknowledgement. The lifecycle
   executor records its execution baseline and running intent before calling
   Docker, performs at most one restart mutation, then requires a fresh inspect
   postcondition before reporting `succeeded`.
6. The task API exposes private, node-scoped list and result endpoints. The
   dashboard polls these endpoints while work is queued or running and displays
   queued, running, verified, failed, and unknown results. Unknown is shown as
   “结果待确认，系统不会自动重试”.
7. Idempotency lookup runs before new-task readiness checks. An identical retry
   returns its durable task while the Agent is offline or synchronizing; a
   changed request with the same key conflicts. Only a genuinely new task must
   pass a fresh trusted Docker view and synchronized-connection gate, pinned
   until its acceptance transaction commits or rolls back.

The transport frames, typed intent digest, node ID, task ID, full target ID,
idempotency key, journal ID, and connection generation are validated at the
existing component boundaries. Wire reports do not supply Core's delivery
evidence. A stale generation or journal mismatch cannot make an old task
dispatchable.

## Changed production surfaces

- `internal/core/storage/sqlite.go` registers the existing task schema as the
  next Core migration.
- `internal/core/server/server.go` owns and initializes the Core task store.
- `internal/protocol/tasks.go` defines the journal handshake, full snapshot,
  dispatch, report, and acknowledgement envelopes.
- `internal/agent/task_bridge.go` owns Agent process-lifetime journal,
  executor, and runner setup, and the per-connection protocol session.
- `internal/agent/runtime.go` negotiates the capability and routes bounded task
  messages through the existing single WebSocket reader/writer without making
  task execution depend on connection lifetime.
- `internal/core/server/agent_websocket.go` negotiates the capability and
  persists/reconciles task messages before acknowledging them.
- `internal/core/server/task_api.go` exposes private task routes.
- `web/src/api.ts` and `web/src/components/NodesDashboard.vue` add task lookup,
  progress polling, and restart UI.

The API routes added here are covered by Session, Origin, CSRF, and revocation
tests. This route check does not replace the required real Agent/Engine
authorization and failure tests.

## Tests and evidence

Pure component tests remain in the existing protocol, Core tasks, Agent
taskjournal, containeractions, and taskrunner packages. Relevant commands:

```bash
go test ./internal/protocol ./internal/agent ./internal/agent/taskrunner ./internal/agent/taskjournal ./internal/agent/containeractions ./internal/core/tasks ./internal/core/storage
go test ./internal/core/server -run '^(TestTaskAPIRequiresSessionOriginCSRFAndHonorsRevocation|TestContainerActionRouteRequiresFullContainerID)$' -count=1
go test ./internal/core/server -run '^TestRealAgentTaskJournalHandshakeDoesNotQueueWhenDockerIsUnavailable$' -count=1 -v
go test -race ./internal/core/server -run '^(TestTaskAPIRequiresSessionOriginCSRFAndHonorsRevocation|TestContainerActionRouteRequiresFullContainerID|TestRealAgentTaskJournalHandshakeDoesNotQueueWhenDockerIsUnavailable)$' -count=1
go vet ./internal/protocol ./internal/agent ./internal/agent/taskrunner ./internal/agent/taskjournal ./internal/agent/containeractions ./internal/core/tasks ./internal/core/storage ./internal/core/server
make frontend
```

The real integration test was run as:

```bash
NODEDANCE_S04_DIND_ROOT=/path/to/NodeDance-s04 \
NODEDANCE_S04_DIND_HOST=unix:///path/to/NodeDance-s04/.artifacts/dind/v28/socket/docker.sock \
GOTOOLCHAIN=local ./.tools/go1.26.8/bin/go test -race -mod=readonly \
  -timeout=240s -count=1 -v ./internal/core/server \
  -run '^TestS05CoreAgentBrowserRestartIdempotencyOnOwnedDIND$'
```

Run the same case against the owned v29 socket as a separate invocation. The
test uses only the owner-validated S04 DIND sockets, creates one uniquely
labelled fixture, and removes that exact full ID after verifying its label. It
does not stop the daemon, access the host Docker socket, or run a global prune.
The fixture intentionally ignores `SIGTERM` so the real restart remains
observable while the API and browser show a durable `running` task. A file in
the container writable layer increments on every process start; the test
requires the browser's one restart to change its value from 1 to 2 despite ten
identical requests.

### Earlier non-DIND validation

- `make frontend`: PASS; locked Go 1.26.8, Node.js 22.23.3, and pnpm 12.10.1
  were used; Vue typecheck and production bundle build passed.
- `make build`: PASS; Core and Agent binaries built for the host plus Linux
  amd64 and arm64.
- `go test ./internal/core/server -count=1`: PASS in 84.331 seconds. This run
  had no DIND endpoint configured, so the owned-DIND cases skipped.
- Four relevant live Agent tests passed in one server-package run (111.931
  seconds): reconnect/rotation/revocation (78.38s), unavailable Docker
  (5.10s), Docker API incompatibility (10.18s), and metrics/dashboard
  (18.24s). This caught and fixed an Agent task-session argument-order bug:
  the journal ID and node ID had been passed in reverse order, which made
  every task bridge attachment fail. The detailed per-test output is in the
  current tool-run transcript; no separate raw-log file was captured.
- The private task API Session/Origin/CSRF/revocation test and full-container-ID
  route test passed. `go vet` passed for all packages listed above, and the
  new browser script passed `node --check` under the locked Node.js binary.
- `TestRealAgentTaskJournalHandshakeDoesNotQueueWhenDockerIsUnavailable`:
  PASS in 5.143 seconds against a real Core, enrolled Agent, TLS WebSocket, and
  isolated nonexistent Unix Docker socket. The journal handshake completed,
  restart was rejected with HTTP 409, no Core task row was created, and a
  subsequent heartbeat advanced. Its focused race-enabled run also passed in
  the same server-package selection (6.617 seconds total).
- A subsequent full `go test ./internal/core/server -count=1` run failed in
  the existing `TestRealAgentEnrollmentReconnectRotationAndRevocation/S02-SUP-06`
  path: after the API had reported an expired lease offline, persisted lease
  materialization was still pending at the test's 2-second deadline
  (`status="" generation=0 original=14`, package duration 89.628 seconds).
  Re-running that exact test alone passed in 55.453 seconds; it materialized
  the lease 833 microseconds after expiry. This intermittent existing S02
  lease-timing failure is retained as a caveat and was not altered here.
- A focused race-enabled batch covering the new SQLite enqueue-gate tests,
  idempotency conflicts, task API auth, no-Docker handshake, stale report ACK
  protection, and task runner report revision behavior passed.
- The first `go test -race` component batch failed once because the runner test
  observed a durable terminal SQLite row before the worker emitted its final
  report notification. The test now waits for the existing bounded
  `waitRunnerIdle` synchronization point before reconnecting. The focused test
  then passed five race-enabled repetitions, and the full seven-package race
  batch passed. The earlier failure was a test synchronization issue, not
  suppressed or retried without a source correction.

### Current integration validation
- The full affected-package race suite passed with Go 1.26.8:
  `go test -race -mod=readonly -timeout=180s ./internal/agent/... ./internal/core/tasks ./internal/core/server`
  (`internal/core/server`: 105.582 seconds). Complete output is retained in
  `.artifacts/s05-run/s05-final-race-packages.log`.
- `go vet -mod=readonly ./internal/agent/... ./internal/core/tasks ./internal/core/server`
  passed with Go 1.26.8; its command transcript is in
  `.artifacts/s05-run/s05-final-vet.log`.
- The current owned-DIND v28 run passed on Engine 28.5.2 in 13.877 seconds. Run
  `nd-s05-restart-1791508650341613851` created exact fixture
  `a4033983f2ef9711fede90c62de99a10e30d0cfd520463a92dbc018abb52cbf7`; task
  `ndt_b6c6e9c55f4b94ca4aa5fed678f5ea41` reached verified `succeeded`, the
  real process-start counter changed `1→2`, and accepted/delivery audits were
  each exactly one. Full output:
  `.artifacts/s05-run/s05-v28-gated-retry-final.log`.
- The current owned-DIND v29 run passed on Engine 29.7.2 in 13.859 seconds. Run
  `nd-s05-restart-1791508710602349731` created exact fixture
  `cf04b5d7150f2df2edd3ee14c63572542849c7eb23ae857dd2d3aaf8001a100d`; task
  `ndt_2f6c75723200c3e3a8f18d6a2c510a0a` reached verified `succeeded`, the
  real process-start counter changed `1→2`, and accepted/delivery audits were
  each exactly one. Full output:
  `.artifacts/s05-run/s05-v29-enqueue-gate-final.log`.
- Both runs used the owner-validated S04 DIND sockets, removed only the exact
  labelled fixture after checking its full ID and ownership label, and left
  both daemons running. A post-run exact-label query found no remaining
  fixture; no host Docker socket, daemon restart, or global prune was used.
- An earlier v29 run completed and verified the Docker restart but then
  reconnected because an older `running` report ACK arrived after the journal
  already held a newer terminal revision. The bridge had treated that stale
  ACK as fatal. The Agent now ignores a mismatched old revision without
  clearing the newer pending report; the corrected run above passed. Failed
  and corrected transcripts remain at
  `.artifacts/s05-run/s05-v29-gated-retry.log`,
  `.artifacts/s05-run/s05-v29-gated-retry-rerun.log`, and
  `.artifacts/s05-run/s05-v29-gated-retry-final.log`.
- Earlier v28 attempts exposed browser and readiness defects: the browser's
  actual TLS Origin differed from `PublicOrigin`; a refreshed browser CSRF
  token made an HTTP retry stale; then a retry was rejected while the Agent
  journal was synchronizing. The test now derives `PublicOrigin` from its
  listener and reuses the browser's current CSRF token. Store acceptance now
  looks up the key/digest before invoking the new-task gate. SQLite regression
  tests prove an offline/unsynced identical retry returns the existing task,
  changed intent conflicts without invoking the gate, and a new unsynced task
  leaves zero task/audit/claim rows. Original transcripts remain in
  `.artifacts/s05-run/s05-v28.log`, `.artifacts/s05-run/s05-v28-rerun.log`,
  `.artifacts/s05-run/s05-v28-rerun2.log`, and
  `.artifacts/s05-run/s05-v28-final.log`.

The real test skips when `NODEDANCE_S04_DIND_HOST` is absent; the runs above
explicitly set it to each owned socket. Remaining closure gates include
independent Core/Agent SIGKILL-before/after-mutation recovery, Core SQLite
read-only or ambiguous-commit zero-mutation proof, stale generation, wrong
journal and capability-negative cases, and broader lifecycle/API acceptance.
No original S05-01 through S05-12 acceptance row is marked passed here.

## Recovery boundaries and remaining work

- A complete current SQLite/WAL process restart is distinct from restoring an
  older backup. After restoring an old Core backup, `delivery_committed=0`
  cannot prove a task was never sent after that backup. Restore must put
  unresolved task records behind Agent-journal and actual-resource
  reconciliation; it must not call `cancelStaleUndelivered` as proof that a
  task was never dispatched. Backup recovery implementation remains an S17
  gate.
- A durable Agent `mutation_may_have_started` phase means the operation might
  have reached Docker. Recovered postconditions can confirm current resource
  state, but cannot prove how many external or prior attempts occurred. Audit
  records therefore describe the verified observed state and do not invent an
  execution count.
- The dashboard has a real restart control. Start, stop, pause, resume, delete,
  independent rename, images, Compose changes, file operations, terminal,
  logs, stats subscriptions, alerting, and update workflows remain outstanding.
- Browser layout is responsive by implementation; no iPad or Android device
  test is a criterion for this closure. The real browser harness uses desktop
  Playwright against the embedded production UI.
- API list cursors and task retention are bounded by the existing Core task
  store. Authentication/session revocation still needs an end-to-end task
  stream test after the future authenticated WebSocket streaming endpoints are
  integrated.

This document records an integration component only. S05 and all twelve
normative S05 cases remain `NOT_READY` until the entire original acceptance
matrix is executed and reviewed.
