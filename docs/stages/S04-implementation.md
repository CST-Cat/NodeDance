# S04 implementation and review log

This file records implementation decisions, review findings, rejected work, and
reproducible evidence. It does not override the fixed acceptance case IDs or
their machine status in [`reports/stages/S04.json`](../../reports/stages/S04.json).
No original S04 case is marked `PASS` by this module-only work.
The three-run Engine submodule checks below are historical evidence from the
former gate. The current gate is one complete normative S04 run for each
required Engine/architecture matrix, including the integrated Agent-to-Core
network path. Those historical module runs do not satisfy the current gate.
They are retained as historical evidence and are not being rerun for this
documentation change.

## Review 1 — rejected: Docker event request context ended at handshake

**Reviewer:** root stage reviewer
**Date:** 2026-10-08
**Result:** rejected; event synchronization is not accepted.

The first implementation wrapped `Engine.OpenEvents` in
`context.WithTimeout(..., RequestTimeout)` and canceled that context as soon
as the Moby SDK returned from `Client.Events`. The SDK uses the same request
context to read the streaming response body, so cancellation immediately after
response headers closes the long-lived Docker event stream. The unit fake
ignored the supplied context and therefore concealed this failure. The same
review also found that `Discoverer.Run` canceled workers without joining them
before returning or releasing the SDK client.

The initial package and race tests had passed, but they did not exercise a real
SDK stream. Those results were insufficient and did not establish event
synchronization. This rejection is retained as implementation history; the
stage remains unaccepted until the owned-DIND checks below pass and all other
stage requirements are integrated and verified.

### Required correction

1. Bind the Docker event request context to the `Discoverer.Run` lifetime.
2. Bound only the response-header/handshake wait with the SDK HTTP transport's
   `ResponseHeaderTimeout`.
3. Cancel and join all discovery, event, health, reconcile, scan, and observer
   workers before closing the Engine client and returning from `Run`.
4. Verify these behaviors against the pinned Moby SDK and a repository-owned
   Docker-in-Docker socket, not a context-ignoring mock.

### Reproduction and verification record

| Check | Result | Evidence |
|---|---|---|
| Initial SDK event stream remains live longer than `RequestTimeout` | `PASS` (module check) | Real SDK/DIND run rows below; original S04 cases remain `NOT_READY`. |
| Real Docker state change is observed after that interval | `PASS` (module check) | Real SDK/DIND run rows below; original S04 cases remain `NOT_READY`. |
| Cut the real SDK event request, change a container while disconnected, and converge after reconnect snapshot | `PASS` (module check) | Real SDK/DIND run rows below; original S04 cases remain `NOT_READY`. |
| Cancel `Run`, join workers, and release the SDK Engine | `PASS` (module check) | Real SDK/DIND run rows below; original S04 cases remain `NOT_READY`. |

## Corrected implementation and owned-DIND evidence

The implementation now passes the run context directly to `OpenEvents`. The
pinned Moby client uses that context for both the response-header wait and the
stream body, so `SDKEngine` sets an independent 8-second
`http.Transport.ResponseHeaderTimeout`; the streaming request itself remains
bound to `Discoverer.Run`. `Run` cancels its context, closes the observer bus,
joins all five workers, and only then closes the SDK client.

The first real DIND attempt exposed another integration failure: providing a
custom `http.Client` caused the SDK to infer `https` for the Unix socket. The
Engine returned plain HTTP, and the test failed with `server gave HTTP response to HTTPS client`. `NewSDKEngine` now explicitly selects the HTTP request scheme
for its local Unix socket. This failure is preserved in
`.artifacts/s04/dind-engine28-20261008T164308Z-30687.log` (ignored test output).

The first fixture-harness attempt also failed and is retained in
`.artifacts/s04/fixtures-engine28-nd-s04-20261008T165050Z-17328.log`: its run ID
included uppercase `T`/`Z`, which Compose rejects in project names, and the
developer's prior `s04real1` fixture still occupied the shared test port. The
harness now emits lowercase run IDs. The old fixture was cleaned with its exact
run ID; the partial failed fixture was removed only after checking its owner
marker and exact DIND suite label. A separate inventory assertion initially
expected dynamic IPv6 HostPort `0`, while Docker reports the dynamic request as
an empty configured port and the assigned port in `Published`; the assertion
was corrected to require that distinction.

Two following DIND runs passed but measured an apparent 10-second stop
latency because Docker's default stop grace elapsed before killing the BusyBox
shell. The fixture now uses `docker stop --time 1`, and the observed event path
is required to converge within five seconds of the command start. The adjusted
test passed against both locked Engine versions:

| Engine | Run | Real stop-event convergence | Long stream / event-gap snapshot / Run cancellation | Raw log |
|---|---|---:|---|---|
| Docker 28.5.2 | `165421Z-29346`, `165540Z-26229`, `165610Z-27913` | 1.152, 1.165, 1.151 s | 3 consecutive PASS; each includes real Engine outage/stale recovery | `.artifacts/s04/dind-engine28-20261008T165421Z-29346.log`, `.artifacts/s04/dind-engine28-20261008T165540Z-26229.log`, `.artifacts/s04/dind-engine28-20261008T165610Z-27913.log` |
| Docker 29.7.2 | `165445Z-31821`, `165638Z-22972`, `165710Z-15411` | 1.139, 1.155, 1.146 s | 3 consecutive PASS; each includes real Engine outage/stale recovery | `.artifacts/s04/dind-engine29-20261008T165445Z-31821.log`, `.artifacts/s04/dind-engine29-20261008T165638Z-22972.log`, `.artifacts/s04/dind-engine29-20261008T165710Z-15411.log` |

Each event run used the repository-owned `.artifacts/dind/v{28,29}/socket/docker.sock`
and a unique `io.nodedance.suite` label. The live SDK event request remained
connected for 8.5 seconds beyond the default 8-second request timeout; a real
stop event then updated the cached container in about 1.15 seconds. The test
closed only the Engine event request while leaving DIND available, stopped the
container during the 3-second reconnect backoff, and observed a complete
reconnect snapshot correct the missed state. It then canceled `Run`, waited for
it to return, and verified a new SDK client could ping that Engine. It also
stopped the owned DIND service, verified the container remained available only
as stale with its last-known state, restarted DIND, and verified a new snapshot
cleared stale state. The exact
reproduction command is `./scripts/test/s04-docker-dind.sh 28` or `... 29`.

The extended inventory test uses the repository fixture script, then checks the
`Discoverer` cache through the real SDK against all 14 owned standalone,
Compose, stopped, created-only, and extra port/health fixtures. It verifies
TCP/UDP, IPv4/IPv6 and dynamic host bindings; EXPOSE, Host Network and stopped
port distinctions; Healthy/Unhealthy/Starting/no-healthcheck states; network
IPs, bind and named-volume mounts; selected Compose identity labels; and empty
timestamps for never-started containers:

| Engine | Fixture run | Result | Raw log |
|---|---|---|---|
| Docker 28.5.2 | `nd-s04-20261008t165134z-6806` | PASS; cleanup verified | `.artifacts/s04/fixtures-engine28-nd-s04-20261008t165134z-6806.log` |
| Docker 29.7.2 | `nd-s04-20261008t165208z-19934` | PASS; cleanup verified | `.artifacts/s04/fixtures-engine29-nd-s04-20261008t165208z-19934.log` |

Reproduce with `./scripts/test/s04-docker-fixtures-dind.sh 28` or `... 29`.

## 100-change module measurement and frame-size contract test

The opt-in 100-change runner alternates 50 real `docker pause` and 50
`docker unpause` commands on one locked BusyBox fixture. Before each timing
sample, the test independently confirms the actual Engine state with
`docker inspect`; it then records command start/completion, the Docker
`TimeNano` event timestamp and receipt time, the Inspect timestamp, and the
module observer arrival time. The reported latency is nearest-rank P50/P95 and
maximum. The observer ignores initial/full-snapshot batches, so initialization
cannot satisfy an incremental-event sample. A missed event, wrong state,
missing Engine timestamp, negative clock delta, P95 over five seconds, or
unclean exact suite label fails the run. The harness requires the locked Go
1.26.8 binary and an already-running repository-owned DIND socket; it does not
start, stop, restart, or reconfigure the daemon.

| Engine | Run | Changes observed | Engine-event → observer P50 / P95 / max | Operation-start → observer P50 / P95 / max | Result and raw log |
|---|---|---:|---:|---:|---|
| Docker 28.5.2 | `nd-s04-p95-261008171052-28328` | 100/100 | 8.755 / 11.553 / 18.160 ms | 29.360 / 39.957 / 51.203 ms | PASS; exact run-label cleanup verified; `.artifacts/s04/p95-engine28-nd-s04-p95-261008171052-28328.log` |
| Docker 29.7.2 | `nd-s04-p95-261008171103-16380` | 100/100 | 8.405 / 10.446 / 12.447 ms | 28.574 / 39.240 / 45.075 ms | PASS; exact run-label cleanup verified; `.artifacts/s04/p95-engine29-nd-s04-p95-261008171103-16380.log` |

Reproduce after the owner-marked Engine is already running with
`./scripts/test/s04-docker-p95-dind.sh 28` or `... 29`. The harness retains
every run, including failed runs, under `.artifacts/s04/`.

`TestS04BoundedBatchFitsCurrentS02Envelope` checks a near-maximum actual
`protocol.DockerBatch` payload embedded in the real S02 `protocol.Envelope`. It
uses `protocol.MarshalDockerBatch` and Agent-to-DTO conversion, then checks the
64 KiB payload bound and 1 MiB outer-frame ceiling. This is component-level
JSON framing only: it does not traverse the Agent runtime, Core persistence, or
WSS and is not final network-path acceptance evidence. The fixture measured
60,388 payload bytes and 60,508 envelope bytes.

The two 100-change runs and the three-run Engine event-stream rows above are
historical Engine submodule evidence. They establish only the isolated
Agent-module Engine/Discoverer/observer path and do not traverse the integrated
Agent WebSocket, Core persistence, or Core observation path. The current
single normative S04 run is still missing that Core-Agent network-path evidence;
the original S04 cases therefore remain `NOT_READY` until the complete required
Engine/architecture matrix is recorded.

## Shared Docker DTO and Core inventory component (historical pre-integration baseline)

This component adds the shared `TypeDocker` / `CapabilityDocker` contract, an
Agent model adapter, and an in-memory Core store. It deliberately does not edit
the S02 Envelope, Agent runtime, Core Agent WebSocket/routes, Core database
migrations, or Web application. It is not Core/Agent end-to-end integration and
does not change any original S04 machine-case result.

`protocol.DockerBatch.Sequence` is the Agent discovery-cache watermark. The
transport Envelope sequence is accepted as a separate method argument, and the
authenticated connection's identity and generation are method arguments from
Core. Docker containers contain selected Compose identity labels, normalized
port states, network/IP fields, mounts, health and timestamps; they do not carry
raw Inspect JSON, environment variables, or arbitrary Docker labels. The DTO
is strictly decoded, validates its schema and is limited to 64 KiB. A
`snapshotId` of zero is permitted only for a non-authoritative first snapshot
chunk, matching a new Agent generation whose first Engine scan cannot run.

The Core store keeps Agent lease status separate from Docker availability and
snapshot freshness. It buffers full scans until contiguous chunks and a final
chunk arrive, and replaces inventory only when the first chunk reports
`available` plus `snapshotFresh=true`. An empty or stale/unavailable report
updates the Docker health view but keeps old inventory stale. A new connection
generation also retains old inventory until that trusted scan. Per-resource
sequence checks and a committed snapshot floor prevent old incremental updates
from reviving removed IDs. Deltas arriving after a full scan's watermark are
held and applied after that scan commits, so newer create/delete events win.
Stale compact container records preserve prior known details when available.
Incomplete staging has item, byte, pending-change and duration bounds; an
overflow or invalid sequence discards only staging and leaves active inventory
untouched. Core's periodic maintenance loop must call `Store.ExpireStaging`;
`Accept` and `SnapshotAt` also perform lazy expiration.

The first Core component test run failed because the byte-limit test configured
a one-byte cap before seeding the fixture; the seed itself was correctly
rejected. The test now seeds under defaults and applies the one-byte cap only to
the candidate scan. This was a test setup defect, not an implementation failure.
The corrected component tests exercise unavailable-empty generation restart,
stale/partial snapshot preservation, successful empty replacement, staged
interleaved create/delete deltas, invalid chunk ordering, stale snapshot ID
replay, stale compact placeholder preservation, Agent/Docker status separation,
generation and Envelope replay rejection, and staging item/byte/time bounds.
`TestAgentObserverDTOAndCoreStoreRoundTrip` additionally drives the actual
Agent cache and observer: it sends a zero-ID unavailable empty snapshot, then a
65-container two-chunk fresh snapshot with cache-generated delete/create deltas
interleaved between chunks, followed by a health-only Docker failure. Every
batch traverses `ToProtocolBatch`, `MarshalDockerBatch`,
`UnmarshalDockerBatch`, and the Core Store. The test verifies that the deltas
win over the older snapshot watermark and that an old connection generation
cannot change the new generation's inventory.

Validation command and result (locked Go `1.26.8`):

```text
GOTOOLCHAIN=local .tools/go1.26.8/bin/go test -race -mod=readonly -count=1 ./internal/protocol ./internal/agent/docker ./internal/core/docker
PASS: protocol, Agent adapter, and Core inventory component packages
GOTOOLCHAIN=local .tools/go1.26.8/bin/go vet -mod=readonly ./internal/protocol ./internal/agent/docker ./internal/core/docker
PASS
```

The final targeted test, vet, and whitespace-check output is retained at
`.artifacts/s04/core-shared-store-20261008T182100Z.log`.

A broader `GOTOOLCHAIN=local .tools/go1.26.8/bin/go test -mod=readonly ./...`
was also attempted. Its Core CLI/server packages cannot compile in this clean
worktree because `internal/core/webassets` embeds `all:dist` and the Web
production `dist` directory has not been built. The targeted S04 component
packages do not depend on that generated frontend asset and pass independently.

The in-memory Core component is not yet connected to the unified SQLite
migrations or the authenticated Agent connection. It has no persisted
inventory/lease recovery test and no real Envelope/WSS test. The adapter is not
called by a running Agent. Therefore these component tests cannot establish
S04-01 through S04-10, S04-SUP-01, or one complete S04 acceptance run; original
case states stay `NOT_READY`.

## Integrated Agent, Core, SQLite, API, and dashboard path (stage not accepted)

The component described above is now connected to the Agent runtime and Core.
This records implementation scope and focused checks only; it does not change
the original S04 case results or substitute for one complete S04 acceptance run.

The Agent advertises `agent.docker.v1` only on a negotiated Core connection.
Its Docker discovery worker uses the local Engine SDK and sends bounded,
generation-bound `TypeDocker` envelopes. Snapshot chunks and event deltas use a
bounded FIFO queue, so a full-snapshot chunk cannot be coalesced away. The
single WebSocket writer keeps control traffic ahead of data and sends a pending
host-metrics sample after at most eight Docker frames. A queue overflow closes
the Agent connection so that it will reconnect and send a complete scan.
Docker unavailability is reported by the discovery worker without stopping the
Agent heartbeat or host metrics.

Core accepts Docker frames only for the authenticated Agent/Node identity and
current connection generation. It validates the generation before validating
or mutating a batch, so a queued frame from an older generation cannot stale or
replace a newer inventory. It stages multi-chunk scans privately and exposes
them only after the complete trusted snapshot is accepted. Database writes use
a candidate store and SQLite transaction before the candidate is adopted; a
failed write preserves the last committed inventory and marks it stale. Startup
hydrates the last safe inventory as stale until a new complete snapshot. Schema
migration v3 adds the Docker health and selected container-record tables. Raw
Inspect JSON, arbitrary labels, and environment values are not persisted.

The authenticated, no-touch admin GET routes are
`GET /api/v1/nodes/{node_id}/containers` and
`GET /api/v1/nodes/{node_id}/containers/{container_id}`. The authenticated
dashboard WebSocket sends an initial Docker view and later sends only when the
inventory revision, generation, Agent lease, Docker availability, event
stream, snapshot freshness, or stale state changes. It rechecks Session validity
for updates and closes revoked sessions. The dashboard keeps last-known rows
visible during Engine or Agent loss, marks them stale, and derives local lease
expiry from Core server time and the monotonic performance clock. Ping
availability is distinct from snapshot health: for example, an API-incompatible
Engine can be reachable while its inventory is stale. Engine error details use
stable allowlisted codes/reasons so response bodies and credentials are not
stored or forwarded.

Focused validation recorded for this integration:

| Check | Result | Scope |
|---|---|---|
| Core API-incompatibility secret-injection chain with real Moby SDK, Agent, TLS Core, SQLite, and authenticated API | `PASS` | Injected Authorization/token/password/Env markers were absent from persisted health/container rows, API output, and Agent stderr; Agent heartbeat and known host metrics continued to advance while the reachable Engine's snapshot was stale with `api_incompatible`. |
| Dashboard Playwright Chromium specs | `PASS`, 4/4 | Includes stale history on API incompatibility, full multi-chunk rendering, generation mismatch, port truthfulness, and responsive viewports. |
| Focused package race regression | `PASS` | `GOTOOLCHAIN=local .tools/go1.26.8/bin/go test -race -mod=readonly -count=1 ./internal/protocol ./internal/agent ./internal/agent/docker ./internal/core/docker ./internal/core/storage ./internal/core/agents ./internal/core/server`; all seven packages passed, Core server in 107.358 s. |

The safe-error test and real API-incompatibility chain are mapped into the S04
API compatibility acceptance work, but the formal runner still needs an
explicit required mapping for all of its branches. The following formal
coverage remains outstanding and must keep the original case rows at
`NOT_READY`: real external pause/unpause/rename/delete convergence through the
Core API for S04-02; a real scan failing after it has begun for S04-07; duplicate
and out-of-order insertion over the authenticated WSS path for S04-09; and one
complete S04 acceptance run with measured evidence against the required Engine
28/29 matrix. That complete run and Engine matrix evidence have not yet been
recorded.
The API-incompatibility path must also remain distinct from Engine ping
unavailability in UI and test evidence. No dashboard or component test alone
establishes the full acceptance gate.
