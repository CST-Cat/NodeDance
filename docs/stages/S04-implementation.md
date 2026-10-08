# S04 implementation and review log

This file records implementation decisions, review findings, rejected work, and
reproducible evidence. It does not override the fixed acceptance case IDs or
their machine status in [`reports/stages/S04.json`](../../reports/stages/S04.json).
No original S04 case is marked `PASS` by this module-only work.

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

`TestS04BoundedBatchFitsCurrentS02EnvelopeMirror` also checks a near-maximum
48 KiB Docker batch embedded in the current S02 Envelope JSON field layout:
48,281 payload bytes, 48,442 envelope bytes, below the 1 MiB frame limit. The
mirror is explicitly test-only because this worktree cannot import S02's
`internal/protocol` package from its sibling worktree. It must be replaced with
the real `protocol.Envelope` and verified over WSS after S02 is integrated; it
is not final frame or network-path acceptance evidence.

The two 100-change runs establish only the isolated Agent-module
Engine/Discoverer/observer path. They are not the full S04-SUP-01 result because
they do not traverse the integrated Agent WebSocket, Core persistence, or Core
observation path. Therefore `S04-SUP-01`, every other original S04 case, and the
whole-stage repeat gate remain outstanding, and the machine report keeps all
original S04 cases at `NOT_READY` until the full gate is completed.
