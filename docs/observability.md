# Diagnostic monitoring / 诊断监控

The Agent extends the existing metrics capability; no version or capability namespace is incremented. Core owns history, alerts and analysis; Web, CLI and TUI discover every read and configuration operation from `/api/v1/operations`.

## Measurements and definitions

`metrics.catalog` publishes `definitions`, including Chinese/English name, unit, source, scope, sampling tier and denominator, and `entityDefinitions` for bounded details. The new `host.observability` contains independent `Metric<number>` readings, entity readings, detail interval and explicit truncation. Existing history and metric alert rules use the same fixed diagnostic key registry. Detailed entity identities never become permanent history labels.

- CPU user/system/steal/iowait/softirq are deltas over total CPU ticks. Guest ticks are not counted twice. Per-core usage follows the existing busy-time convention, excluding idle/iowait; at most 32 cores are shown. iowait is advisory and inherits Linux accounting limitations.
- CPU PSI uses `some`; memory/I/O use `some` and `full`, with 10/60/300-second rolling percentages. System-wide CPU `full` is intentionally absent.
- Available, dirty and Slab memory come from meminfo. Major faults, OOM kills and swap rates come from vmstat; swap pages are multiplied by the actual page size.
- Disk IOPS and time deltas use whole hardware-backed block devices, excluding partitions/stacked devices. Device disk sequence joins the identity where available. Latency is aggregate read/write time delta divided by completed operations, **not P95/P99**. Zero operations yields `no_operations`, not zero latency. Weighted I/O time / elapsed time gives mean outstanding I/O.
- TCP retransmission/connection failure/listen-overflow rates retain their kernel meanings; they are not external packet-loss percentages. Conntrack count/limit is optional when the kernel facility is absent.
- File-system inode, space and per-service status/restarts/exit status are slower details. systemd is observed only; no service is changed. Filesystem scans visit only recognised local types, at most 16 mounts, in one bounded worker. A blocked statfs cannot create an unlimited chain of workers.
- Process rankings interleave CPU/RSS/read/write leaders. CPU is a percentage of one core and may exceed 100%; identity includes PID and start time. At most 2,048 processes are scanned with a two-second budget, and 20 are returned. No command lines, environment variables, credentials or connection addresses are collected.
- Docker/containerd cgroup v2 details include CPU cores, quota, quota utilisation, throttled periods/time, memory current/limit, OOM events, PIDs and pressures. Unlimited limits stay unknown with reason `unlimited`. CPU throttling is separate from host steal. A walk visits at most 2,048 entries and returns at most 32 container leaf cgroups; it does not follow symlink directories. Docker inventory remains the source for container lifecycle state.
- Agent RSS, one-core CPU percentage and diagnostic collection durations expose observer cost. Core adds report inter-arrival time, estimated unobserved sampling intervals and Agent monotonic sample-to-serialization age. Receive clock offset is already available on the metrics view; it must not be described as pure network latency. Missing intervals are not labelled dropped packets.

`known` zero means a successful observation of zero. `unsupported`, `read_failed`, `warming_up`, `counter_reset`, `no_operations`, `sampling`, `unlimited` and source failures remain distinct reasons without a fabricated value. Fast baselines reset on boot changes and gaps longer than three configured intervals. Slow process/cgroup baselines have their own cadence and reset rules. Cached readings retain their original timestamps and expire at three sampling intervals; Core makes every diagnostic stale after lease loss.

## Configuration and application collection

Read `agents.settings`, edit the returned `settings` object, then submit it through `agents.configure`. The optional `observability` object has these defaults:

```json
{
  "intervalSeconds": 60,
  "processLimit": 10,
  "containerLimit": 16,
  "services": [],
  "applications": []
}
```

Detail interval accepts 15–300 seconds, process limit 0–20, container limit 0–32, up to 16 service names and 8 application collectors. Zero disables the corresponding detail collector. Fast collection follows the existing fast interval; detail scanning is asynchronous and never starts a second worker while the first is active. Each HTTP request has a one-second timeout, no proxy, no redirects and a 256 KiB response limit; the whole detail worker has an eight-second context budget. The wire payload additionally caps diagnostic detail to 24 KiB, sets `truncated`, and preserves aggregate readings.

Applications use `nginx` stub_status or a `prometheus` endpoint. Redis/PostgreSQL and other applications can use their existing exporter endpoint; NodeDance does not install exporters, change databases, or store database passwords. Prometheus collection selects at most 32 **unlabelled** finite nonnegative scalar samples; it does not guess aggregation across labels. Requested but missing metrics remain unknown. Units are explicitly configured. Counters are displayed as cumulative counts, not silently relabelled as rates.

```json
{
  "name": "redis",
  "kind": "prometheus",
  "url": "http://127.0.0.1:9121/metrics",
  "metrics": ["redis_connected_clients"],
  "metricUnits": {"redis_connected_clients": "count"}
}
```

NGINX exposes connections/reading/writing/waiting and accepts/handled/requests totals from stub_status. Richer request latency, status-code and database detail requires the relevant application instrumentation; it is never inferred from host counters. URLs must be HTTP(S), without userinfo, query credentials or fragments. Samples and bounded names are retained, never endpoint response bodies.

## Independent IPv4 / IPv6 and HTTP phases

Probe create/update/batch accept `ipFamily` (`""`, `"4"`, `"6"`). Empty follows Agent networking preference; explicit family never falls back to another family. Create two probes for independent IPv4/IPv6 results. HTTP trace measurements persist DNS, connect, TLS and first-byte timings when the phase actually occurs, plus the verified peer leaf certificate expiry when available. First-byte time is cumulative from request start; the other timings are individual phases. Literal IPs need no DNS phase. TLS verification remains enabled. History and all terminal clients receive the same measurement DTO; failed/unavailable phases are omitted rather than zero-filled.

## History, recording and analysis

- Known aggregate diagnostics join existing minute/hour histories (30 days/year). A source-timestamp cursor deduplicates cached slow samples; a repeated value from a genuinely new sample still counts.
- `nodes.recording` returns raw snapshots, diagnostic incidents and audited changes in a bounded window (maximum one hour/600 frames/8 MiB of snapshots). Default is the last ten minutes. Rolling raw retention is two hours; five minutes before/after recorded incidents are retained for seven days. Each node is capped at 10,000 raw frames, so incident storms cannot grow storage indefinitely. The response exposes truncation; expired or absent frames stay absent.
- Built-in recording triggers: memory/I/O full10 >10%, steal >20%, conntrack >90%, OOM increment, failed service count and unhealthy service probes. These are recording triggers, not replacements for configurable alert rules. Repeated triggers coalesce for 15 minutes per subject. Recordings are existing-cadence observations, not continuous packet/process traces.
- `nodes.incidents` lists up to 100 incidents from seven days. Node removal cascades every observation table. Existing retention workers prune data, including disconnected nodes.
- `nodes.analysis` examines up to 24 hours of minute means, constrained to the current observed boot epoch. It requires at least 60 points spanning one hour, at least 80% interval coverage, fresh latest data (three minutes), and no gap greater than five minutes. Baseline excludes the latest 30 minutes; recent mean uses those 30 minutes. Baseline P95 is a percentile of **minute means**, not request latency P95. Optional `changeAt` compares 30-minute before/after windows, each with at least 10 points.
- Optional `capacity` uses the metric's own unit. Forecast requires positive slope, R² >=0.8, current value below the specified capacity and horizon <=365 days. It is an estimate, not an SLA. UI offers 100 for percentages and observed totals for memory/filesystem byte usage; the bound can be edited or cleared in the selected metric’s unit. Flat/noisy/negative trends yield no exhaustion timestamp.
- `nodes.correlations` compares the same metric across up to eight explicitly selected nodes over aligned minutes from the last hour. At least 30 matching buckets and nonconstant series are required. Constant/missing histories return unknown. Pearson correlation does not establish causation or diagnose a provider.

Web keeps the existing overview cards, gauges, charts and label/value rows; new host readings and details get additional square cards using the same metric-card class, sizing, corners and spacing. The original eight overview cards retain their content; all square cards use four columns on wide screens and two on narrow screens. Existing historical monitoring displays new series with catalog labels and units. Collection configuration reuses the existing Host management schema form; recording, incidents, baseline/forecast/change comparison and node comparison use the existing discovered operation interface. No separate diagnostic panel, tabs, card system, dialog or spacing rules are introduced. The operation browser in Web/CLI/TUI provides all parameters, limits and results. Read-only users may read diagnostics and analyses; changing collection remains administrator-only. No diagnostic or probe-phase extension is added to the guest projection.

## Verification

Run the root Go suite, Web typecheck/build, isolated guest security suite, real-browser diagnostic fixtures in light/dark at wide/mobile widths, probe loopback tests, counter reset/PSI tests, retention/deletion/dedup tests and sparse-history/correlation/forecast evidence tests. Benchmark the actual host collector separately; do not promise an unmeasured universal CPU overhead.
