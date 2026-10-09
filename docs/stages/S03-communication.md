# S03 metrics communication

This document describes the metrics DTO/store and its current Agent, Core and
dashboard wiring. S03 remains **NOT_READY** until every original acceptance
case passes one complete real run on the required environments.

## Wire payload

`internal/protocol/metrics.go` defines the `metrics` envelope type and
`agent.metrics.v1` capability. The payload contains host/system identity, CPU,
memory, network summary and per-interface rates, disk mount details, and
uptime/boot ID. It contains neither Agent identity nor connection generation:
Core gets those from the authenticated connection context. Its sequence is
independent from heartbeat sequence and advances only for metrics messages.
Metrics acceptance never renews a heartbeat lease.

Every measurement has an explicit `known`, `unknown`, or `error` state. Unknown
and error values omit `value` and include a reason; zero is a valid measured
value only when status is `known`. Core may derive `stale`, preserving the last
known value and adding the current failure, lease, generation, or expiry reason.
Unknown/error without a previous value stay unknown/error. CPU, memory, system,
uptime, and network freshness expires after three 5-second periods (15 seconds);
disk expires after three 30-second periods (90 seconds).

`sampleAgeMillis` is calculated on the Agent from `time.Time.Sub` before wall
time serialization, so the Agent's monotonic clock contributes to age. Core
adds time elapsed since its own monotonic receipt. `sampledAt`, `collectedAt`,
and Core's `clockOffsetMs` remain wall-clock display/diagnostic fields and do
not extend freshness. The payload validator bounds age to seven days and the
serialized report to 64 KiB.

## Core store contract

The Agent keeps one Collector for its process lifetime, including socket
reconnects. A serialized writer gives heartbeat/control messages priority over
a one-entry replaceable metrics queue, and bounds telemetry writes so a slow
peer cannot hold heartbeats behind old samples. Run cancellation joins the
Collector schedules and socket writer before returning.

Core advertises metrics only when the Agent reports the capability. It calls
`BindConnection` only after authentication and lease generation assignment.
`Accept` requires that authenticated identity, connection generation and
monotonic metrics-specific sequence match the binding. A later generation can
restart its sequence at one; old sockets cannot overwrite it. On close,
`UnbindConnection(identity, generation)` cannot remove a newer generation.
Metrics acceptance checks the current heartbeat lease but never updates it.

The authenticated private API provides `GET /api/v1/nodes` for Core node and
lease state and `GET /api/v1/nodes/{nodeId}/metrics` for the selected node's
latest report. The older `/api/v1/agents` management list remains available.
An existing enrollment-pending node returns `node_status` with reason
`awaiting_agent_registration`; an unknown node ID remains `404`.
Dashboard updates use the existing session- and Origin-checked
`/ws/v1/dashboard` stream on the same Core port. Notifications coalesce by
node and each browser resolves the latest full view at send time. Session
validity is rechecked before initial and incremental writes; listing, state
lookup and each WebSocket write have bounded contexts.

`SnapshotAt` receives the trusted current lease separately from the report.
Core passes `nil` when there is no active authorized online lease, including
revocation/offline. A non-nil lease is considered online only when its status is
`online`, it matches this node and Agent, and its `ValidUntil` is in the future.
This avoids deriving online state from historical `lastSeen` alone. A new
online generation makes node status online before its first metrics report;
the retained old-generation data is marked stale. Expired or inactive leases
make node status offline immediately at read time, without a sweeper. Metrics
updates cannot keep that lease alive.

The store retains boot ID transitions, Core receipt time, Agent wall collection
time, and their offset. It merges partial failures with the last known values,
but keeps status/reason explicit. It does not aggregate disk mount rows, so a
consumer must not sum repeated filesystem/bind mounts into a second disk total.

## Dashboard clock behavior

`MetricsPanel.vue` accepts the future Core view shape from
`web/src/metrics-contract.ts`. It computes lease remaining time from the two
Core timestamps (`serverTime` and `leaseValidUntil`), then advances the
deadline with `performance.now()`. Metric freshness likewise starts with the
Core-returned `sampleAgeMillis` and advances only by the browser's monotonic
clock. The panel never compares deadlines with the user's `Date.now()`.

Without another prop update, the panel marks expired measurements stale and
the node offline while preserving displayed values and their reason. A new
view resets the monotonic baseline. UTC collection and receive timestamps are
shown separately; the reported clock offset is diagnostic only.

## Independent verification

Run the focused checks from the repository root:

```sh
go test -mod=readonly ./internal/protocol ./internal/agent/metrics ./internal/core/metrics
scripts/test/s03-metrics-web.sh
```

The browser command starts its own local Vite fixture and exercises an offset
client wall clock, metric expiry without a later update, lease expiry, recovery
on a replacement view, explicit unknown/error values, a narrow viewport, and
delayed node-list/metrics HTTP responses racing newer WebSocket data. The race
test checks that the displayed value, active generation and lease do not move
backward.
The real Core integration test starts a real Agent, waits for a second Linux
host sample with known CPU/memory and boot ID, reads the private node API,
receives the real dashboard push, verifies the Agent continues after the
browser closes, then floods a slow browser while logging out. The last local
run reached the second sample in about 5.05 seconds. These results do not cover
all S03 cases or the required complete acceptance run on each architecture;
full S03 remains NOT_READY.
