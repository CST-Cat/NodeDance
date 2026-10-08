# S05 Agent Docker logs and stats stream component

**Component status: unit/race verification and Docker 28/29 checks passed.
Formal S05 status: `NOT_READY`.** This module does not connect to Agent
runtime messages, Core authorization, browser sessions, WebSocket routing, or
temporary data channels. The original S05-09 and S05-10 acceptance rows
therefore remain `NOT_READY` until those shared integration paths are
implemented and tested.

## Docker log behavior

`OpenLogs` verifies the complete 64-character Docker ID with a fresh Inspect,
uses the actual `Config.Tty` value, and requests history with optional follow,
tail, timestamps, and time bounds. Empty output selection defaults to both
stdout and stderr; empty tail means all retained history. Bounded `Until` and
live follow cannot be combined.

Docker TTY logs are raw bytes and Docker has already merged stdout/stderr. They
are emitted as stdout chunks without parsing or altering payload bytes. For
non-TTY containers, the reader validates Docker's eight-byte multiplex header,
routes stream type 1 to stdout and 2 to stderr, and never includes header bytes
in emitted data. Malformed stream types, reserved header bytes, or truncated
frames terminate the reader with a typed error.

Each `LogFrame` contains at most 32 KiB. The output channel holds at most eight
frames; a slow consumer applies backpressure to the body reader instead of
causing unbounded memory growth. `Close` cancels the request, closes the body,
and waits up to five seconds for the reader goroutine. Parent-context
cancellation also closes the body. The module preserves UTF-8 bytes across
frames; a later browser layer must decode text across frame boundaries rather
than assuming each frame ends at a character boundary.

The SDK uses a local Unix socket and an eight-second HTTP response-header
timeout. Container Inspect also has a separate ten-second whole-request
deadline, including response-body decoding; this bounds the preflight used by
`OpenLogs` even when a daemon sends headers and then stalls. Its
`http.Client.Timeout` remains zero: after response headers arrive, follow-log
and stats bodies stay alive until the caller cancels or closes them. Unix HTTP
fixtures verify the Inspect body deadline, a stalled log response-header
deadline, and log/stats bodies that continue past the header deadline.

## On-demand shared stats

`StatsManager` has no sampler or startup poller. It makes no Docker stats call
until the first subscriber requests a container. All subscribers for the same
full container ID share one SDK stats body. Each subscriber has a one-value
channel: when it falls behind, the newest sample replaces the pending sample.
Closing one subscriber leaves a shared stream alive; closing the last one
cancels the request, closes the SDK body, and waits for the reader goroutine.
Each subscriber receives its own copy of every mutable snapshot value and
timestamp pointer. After the final unsubscribe starts closing a reader, the
container entry remains as a tombstone and a new subscription receives
`ErrStatsStreamClosing` until the old body reader has joined and the tombstone
is removed. If body close fails to stop the reader by the configured deadline,
the tombstone remains, preventing repeated SDK streams from accumulating.
Engine failures close subscribers with stable typed errors; after the reader
has joined, a later subscription can start a fresh Engine request.

Each newline-delimited stats record is limited to 1 MiB. The JSON structure is
also bounded to 64 nested containers, 16,384 tokens total, 4,096 entries per
object or array, and 64 KiB per decoded string (128 bytes per JSON number).
These limits apply per record, not to the lifetime of a healthy stats stream;
large unknown fields, excessive nesting, and oversized records close that
stream with a safe typed error.

Metric values are pointers and have an explicit `ok` or `unknown` state. A
measured numeric zero is preserved as a valid value; unavailable data is
represented by `unknown` with a reason and no value. `observed_at` and
`sampled_at` are omitted when Docker provides no usable sample time.

The Linux Docker formulas are:

| Metric | Formula and inputs | Unknown conditions |
|---|---|---|
| CPU | `100 × (current container CPU − precpu container CPU) / (current system CPU − precpu system CPU) × effective CPU count`. Effective count is positive `online_cpus`, otherwise the length of `percpu_usage`. | First sample or missing precpu fields: `warming_up`; a counter decrease: `counter_reset`; no system progress, invalid timestamps, or missing CPU count have explicit unknown reasons. |
| Memory | Used bytes = `usage − cache`; percent = `100 × used bytes / limit`. Cache uses `inactive_file`, then `total_inactive_file`, then legacy `cache`. | Missing sample time/usage/limit/cache, cache greater than usage, or zero/unavailable limit. |
| Network | Sum RX and TX byte-counter deltas across every interface, divided by the Docker sample-time interval in seconds. | First sample: `warming_up`; changed interface set: `interface_set_changed`; a counter decrease: `counter_reset`; missing counters or nonpositive interval are unknown. |
| Block I/O | Sum per-device cumulative `Read` and `Write` service bytes, ignore Docker's aggregate `Total` entries to avoid double counting, then calculate per-device deltas divided by sample-time interval. | First sample: `warming_up`; changed device set: `device_set_changed`; counter decrease: `counter_reset`; missing read/write fields, ambiguous Total-only data, or invalid interval are unknown. |

Counter baselines advance even when a metric reports a reset or interface/device
set change, so a later valid sample can recover without reporting a false
spike. The first stats sample can still provide memory while CPU, network, and
block-I/O remain unknown until their required deltas exist.

Network and block-I/O rates use Docker's `Read` timestamp interval as the
denominator. The stream also records each record's local `time.Now()` monotonic
receive time. A backward/non-advancing Docker timestamp, a Docker interval over
30 seconds, or a monotonic receive gap over 30 seconds makes these rates
unknown and advances the baseline. This rejects wall-clock jumps and stale
stream gaps while allowing a buffered burst of samples to retain its Docker
sample intervals; a short receive interval is never used as a rate interval.
A subsequent valid pair recovers from the reset baseline.

## Component tests and evidence

Run standard, race, and vet checks with the repository-pinned Go binary:

```bash
GOTOOLCHAIN=local /path/to/go1.26.8/bin/go test -mod=readonly -count=1 ./internal/agent/containerstreams
GOTOOLCHAIN=local /path/to/go1.26.8/bin/go test -race -mod=readonly -count=1 ./internal/agent/containerstreams
GOTOOLCHAIN=local /path/to/go1.26.8/bin/go vet -mod=readonly ./internal/agent/containerstreams
```

The deterministic component tests cover TTY raw bytes, non-TTY stdout/stderr
demultiplexing, Unicode preservation, malformed/truncated headers, 4 MiB
chunked backpressure, Inspect-body and response-header stalls, bodies that
remain live past the header timeout, the CPU/memory/network/block-I/O formulas,
first-sample and counter-reset unknown states, counter overflow, independently
mutable subscriber snapshots, latest-value queues, cancellation, body close,
per-record byte and structure limits, malformed stats data, blocked close with
rejected overlapping resubscription, recovery after a failed stream, Docker
timestamp jumps in both directions, recovery after a jump, and buffered samples
whose monotonic receive gaps are shorter than their trusted Docker intervals.

The opt-in real Engine case validates the exact owner marker and digest-locked
BusyBox image in `.artifacts/dind/v28` or `v29`. It creates uniquely named and
labeled fixtures for historical TTY output, historical non-TTY stdout/stderr,
4 MiB output, and a live-follow/stats container. All SDK reads run against the
nested socket. Cleanup lists only the unique test suite label, rechecks that
label on every candidate, and removes exact fixture IDs. It never stops or
restarts the owned daemon, changes socket permissions, contacts the outer
Docker API for mutation, or invokes prune.

The final real Engine runs include the bounded-record parser, full Inspect
deadline, per-subscriber snapshot copies, close tombstones, and timestamp-jump
guards. Both use the repository-owned nested daemon and the digest-pinned
BusyBox fixture image. The exact command output is retained in the local
evidence files below; each post-run exact-suite-label query returned no
containers:

| Engine | Exact suite and fixture IDs | Result and evidence |
|---|---|---|
| Docker 28.5.2 | Suite `nodedance-s05-containerstreams-a41649b9e972`; plain `010276ca0cd2c3ad99baa4f706fb85dd3ddba19f756f549fd0327bffd109d577`; TTY `5c35ea9afcb6293208c50e174eb696fd9884ad44444f3ace45d2a945ec8f9315`; large output `494cd13ec531bcb6e471001062cdf0b50b4cbbb863e196ee7c5d0588046e9ec0`; follow/stats `8d33105203969a7bb4466e7b06b1cad033bfe42791bafddbac17011bb301bfe0`. | `PASS`, 2.38 s; one stats SDK open and one body close. Raw output: `.artifacts/s05-evidence/containerstreams-v28-final.txt`. |
| Docker 29.7.2 | Suite `nodedance-s05-containerstreams-11d7fc9742b3`; plain `03389d0f98e69038633be77c42fad6341cbea25f6648fbb089cb8ce9e3d57eeb`; TTY `70be87ccaf59460e5ab61487db296dedee31d0827416ee0274713f4923de9aac`; large output `ea267f6a482cc7127d0e40106e85d2d78d953bba4197267caa5d4e6c07fdc988`; follow/stats `6c1f6dfeaaf3df5de1bde0a7ac22f6b32d1fdb6f52fd676f9b8d4b15c0625ddf`. | `PASS`, 2.08 s; one stats SDK open and one body close. Raw output: `.artifacts/s05-evidence/containerstreams-v29-final.txt`. |

The v28 socket was
`/home/ubuntu/codex-use/NodeDance-s04/.artifacts/dind/v28/socket/docker.sock`;
the v29 socket was
`/home/ubuntu/codex-use/NodeDance-s04/.artifacts/dind/v29/socket/docker.sock`.
The exact-label post-run container queries were empty on both daemons. Current
pipe-backed tests additionally verify missing or mismatched IDs, malformed
JSON versus transport read failures, no raw daemon or socket error text, and
reader cleanup/recovery.

## Remaining S05 work

This component does not implement the Core task sender, browser authorization,
node/session binding, WebSocket or HTTP data channels, stream quotas tied to
Core sessions, browser backpressure, or transport disconnect integration. It
does not mark original S05-09 or S05-10 as passed, and it does not complete the
formal S05 phase. Those paths still need real Core/Agent integration tests,
including session revocation, target binding, slow-browser cancellation, and
subscriber release when the browser leaves.
