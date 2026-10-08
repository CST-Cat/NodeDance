# S03 metrics communication component

This document describes the independently reviewable metrics DTO/store and
dashboard panel. S03 remains **NOT_READY**: the Agent runtime does not send
`TypeMetrics`, Core has no route or WebSocket wiring for this store, and the
panel is not mounted in the application.

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

Core calls `BindConnection` only after authenticating the Agent and binding the
current lease generation. `Accept` requires the authenticated identity,
connection generation, and monotonically increasing metrics sequence to match
that binding. A later generation can restart its metrics sequence at one;
old sockets cannot overwrite it. On connection close, Core can call
`UnbindConnection(identity, generation)`; an old close cannot unbind a newer
generation.

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
on a replacement view, explicit unknown/error values, and a narrow viewport.
These checks validate only the DTO/store/panel components; they are not S03
stage acceptance and do not prove live Agent/Core delivery or the required
end-to-end latency.
