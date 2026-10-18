# Diagnostic monitoring / 诊断监控

The Agent extends the existing metrics capability; no version or capability namespace is incremented. Core owns history, alerts and analysis; Web, CLI and TUI discover every read and configuration operation from `/api/v1/operations`.

## Measurements and definitions

`metrics.catalog` publishes `definitions`, including Chinese/English name, unit, source, scope, sampling tier and denominator, and `entityDefinitions` for bounded details. The new `host.observability` contains independent `Metric<number>` readings, entity readings, detail interval and explicit truncation. Existing history and metric alert rules use the same fixed diagnostic key registry. Detailed entity identities never become permanent history labels.

- CPU user/system/steal/iowait/softirq are deltas over total CPU ticks. Guest ticks are not counted twice. Per-core usage follows the existing busy-time convention, excluding idle/iowait; at most 32 cores are shown. iowait is advisory and inherits Linux accounting limitations.
- Available, dirty and Slab memory come from meminfo. Major faults and OOM kills come from vmstat.
- Disk IOPS and time deltas use whole hardware-backed block devices, excluding partitions/stacked devices. Device disk sequence joins the identity where available. Latency is aggregate read/write time delta divided by completed operations, **not P95/P99**. Zero operations yields `no_operations`, not zero latency. Weighted I/O time / elapsed time gives mean outstanding I/O.
- Process rankings interleave CPU/RSS/read/write leaders. CPU is a percentage of one core and may exceed 100%; identity includes PID and start time. At most 2,048 processes are scanned with a two-second budget, and 20 are returned. No command lines, environment variables, credentials or connection addresses are collected.
- Agent RSS, one-core CPU percentage and diagnostic collection durations expose observer cost. Core adds report inter-arrival time, estimated unobserved sampling intervals and Agent monotonic sample-to-serialization age. Receive clock offset is already available on the metrics view; it must not be described as pure network latency. Missing intervals are not labelled dropped packets.

`known` zero means a successful observation of zero. `unsupported`, `read_failed`, `warming_up`, `counter_reset`, `no_operations`, `sampling` and source failures remain distinct reasons without a fabricated value. Fast baselines reset on boot changes and gaps longer than three configured intervals. Slow process baselines have their own cadence and reset rules. Cached readings retain their original timestamps and expire at three sampling intervals; Core makes every diagnostic stale after lease loss.

## Configuration

Read `agents.settings`, edit the returned `settings` object, then submit it through `agents.configure`. The optional `observability` object has these defaults:

```json
{
  "intervalSeconds": 60,
  "processLimit": 10
}
```

Detail interval accepts 15–300 seconds, process limit 0–20. Zero disables the corresponding detail collector. Fast collection follows the existing fast interval. Process detail scanning is asynchronous and never starts a second worker while the first is active; the whole worker has an eight-second context budget. The wire payload caps diagnostic detail to 24 KiB, sets `truncated`, and preserves aggregate readings.

## Independent IPv4 / IPv6 and HTTP phases

Probe create/update/batch accept `ipFamily` (`""`, `"4"`, `"6"`). Empty follows Agent networking preference; explicit family never falls back to another family. Create two probes for independent IPv4/IPv6 results. HTTP trace measurements persist DNS, connect, TLS and first-byte timings when the phase actually occurs, plus the verified peer leaf certificate expiry when available. First-byte time is cumulative from request start; the other timings are individual phases. Literal IPs need no DNS phase. TLS verification remains enabled. History and all terminal clients receive the same measurement DTO; failed/unavailable phases are omitted rather than zero-filled.

## History, recording and analysis

- Known aggregate diagnostics join existing minute/hour histories (30 days/year). A source-timestamp cursor deduplicates cached slow samples; a repeated value from a genuinely new sample still counts.
- `nodes.recording` returns raw snapshots, diagnostic incidents and audited changes in a bounded window (maximum one hour/600 frames/8 MiB of snapshots). Default is the last ten minutes. Rolling raw retention is two hours; five minutes before/after recorded incidents are retained for seven days. Each node is capped at 10,000 raw frames, so incident storms cannot grow storage indefinitely. The response exposes truncation; expired or absent frames stay absent.
- Built-in recording triggers: steal >20%, OOM increment and unhealthy service probes. These are recording triggers, not replacements for configurable alert rules. Repeated triggers coalesce for 15 minutes per subject. Recordings are existing-cadence observations, not continuous packet/process traces.
- `nodes.incidents` lists up to 100 incidents from seven days. Node removal cascades every observation table. Existing retention workers prune data, including disconnected nodes.
- `nodes.analysis` examines up to 24 hours of minute means, constrained to the current observed boot epoch. It requires at least 60 points spanning one hour, at least 80% interval coverage, fresh latest data (three minutes), and no gap greater than five minutes. Baseline excludes the latest 30 minutes; recent mean uses those 30 minutes. Baseline P95 is a percentile of **minute means**, not request latency P95. Optional `changeAt` compares 30-minute before/after windows, each with at least 10 points.
- Optional `capacity` uses the metric's own unit. Forecast requires positive slope, R² >=0.8, current value below the specified capacity and horizon <=365 days. It is an estimate, not an SLA. UI offers 100 for percentages and observed totals for memory/filesystem byte usage; the bound can be edited or cleared in the selected metric’s unit. Flat/noisy/negative trends yield no exhaustion timestamp.
- `nodes.correlations` compares the same metric across up to eight explicitly selected nodes over aligned minutes from the last hour. At least 30 matching buckets and nonconstant series are required. Constant/missing histories return unknown. Pearson correlation does not establish causation or diagnose a provider.

Web groups load, CPU, memory, disk, Swap and billing traffic in one shared rounded rectangle, ordered in six columns on wide screens and three on narrow screens; charts and diagnostic label/value rows remain in their existing cards; CPU, memory, disk, Swap and load use equal-size enlarged percentage rings with the existing normal/warning colors; six groups of host readings and details get additional square cards using the same metric-card class, sizing, corners and spacing. The combined rectangle retains the resource readings; remaining chart and diagnostic square cards use four columns on wide screens and two on narrow screens. Existing historical monitoring displays new series with catalog labels and units. Collection configuration reuses the existing Host management schema form; recording, incidents, baseline/forecast/change comparison and node comparison use the existing discovered operation interface. No separate diagnostic panel, tabs, card system, dialog or spacing rules are introduced. The operation browser in Web/CLI/TUI provides all parameters, limits and results. Read-only users may read diagnostics and analyses; changing collection remains administrator-only. No diagnostic or probe-phase extension is added to the guest projection.

## Deployment

Publish and update the Agent before deploying Core when removing diagnostic sources. The reduced Core catalog validates the current Agent payload; retired definitions and configuration fields are not retained. / 删除诊断来源时先发布并更新 Agent，再部署 Core；Core 按当前指标目录验证上报，不保留已删除的指标定义或配置字段。

## Verification

Run the root Go suite, Web typecheck/build, isolated guest security suite, real-browser diagnostic fixtures in light/dark at wide/mobile widths, probe loopback tests, counter reset tests, retention/deletion/dedup tests and sparse-history/correlation/forecast evidence tests. Benchmark the actual host collector separately; do not promise an unmeasured universal CPU overhead.

Per-core readings reuse ResourceGauge, hide the card on single-core hosts, and adapt ring size and columns to reported core count and available card space; dense hosts retain scrollable readings at a readable minimum size. Billing traffic reuses nodes.asset quota/accounting/warning settings and shows a ring only for a positive quota. Unknown or expired traffic is not treated as zero; over-quota readings retain their actual percentage. / 每核心复用环形仪表，单核隐藏卡片，按核心数量与卡片空间调整尺寸和列数；高核心数保留最小可读尺寸并可在卡片内滚动。账期流量复用现有套餐额度、计费口径和提醒阈值，仅正额度显示环形；未知或过期数据不填零，超额保留真实百分比。

Collection/receipt timestamps, clock offset and report identity live once in the Agent collection card; uptime, OS/architecture and kernel live in Hardware and addresses. There is no separate metadata footer or divider. / 采集与接收时间、时钟偏移和上报标识统一位于 Agent 采集卡片；运行时间、系统架构和内核统一位于硬件与地址，不再重复显示底部信息栏或分割线。

Hardware and addresses uses compact two-column/one-column label-value rows. It retains both public IP families whenever a value exists, hides only empty public rows, deduplicates public/local addresses, groups addresses per interface and omits loopback/container bridges. Raw boot IDs and cache accounting details are omitted from this presentation. / 硬件与地址使用宽屏两列、窄屏一列的紧凑信息行；公网双栈有数据即显示，仅隐藏无数据空行，公网与网卡地址去重，同网卡合并，省略回环和容器网桥。此展示不再列出原始启动 ID 和缓存口径明细。

Overview network/disk-I/O mini charts and the detail network/disk-I/O charts read Core-persisted five-second rate history over a rolling four-hour window through discovered nodes.history. Load the full window once, then fetch overlapping five-second increments with at most four concurrent history requests, cancel on navigation, and preserve missing-sample gaps. Browser reloads and Agent boot/generation changes never erase saved history; offline nodes retain their past curves. Instant rate labels still use fresh metrics. The process chart retains its existing live-sample behavior. / 总览网络／磁盘 I/O 和详情网络／磁盘 I/O 曲线通过发现的 nodes.history 读取 Core 保存的最近四小时五秒均值，首次载入整段、之后每五秒增量刷新，历史请求最多四个并发，离开页面即取消；缺失采样断线，不补零。刷新浏览器或 Agent 重启不清空已保存曲线，离线节点保留历史；即时速率仍来自有效实时指标。进程曲线维持现有实时采样方式。

RateHistoryChart is shared by overview and detail cards for rates, history curves and time-range footers. The detail disk-I/O card follows Network immediately and shares the same history request. / 总览与详情复用 RateHistoryChart 的速度、历史曲线与时间范围；详情磁盘 I/O 卡片紧邻网络右侧，共享同一次历史请求。

Detail network and disk-I/O cards show four-hour estimated totals by integrating each known five-second mean over its overlap with the selected window. Missing buckets are excluded; no observations produce an unknown total, while measured zero remains zero. Totals are explicitly estimates, not raw cumulative hardware counters or billing totals. / 详情网络与磁盘 I/O 显示四小时累计估算，以有效五秒均值乘以其在窗口内的时长累加。缺失采样不计入，无观测显示未知，实测零保留为零；明确标注估算，不冒充硬件原始计数器或账单总量。

`nodes.history` accepts `resolution=5s` for the four network/disk throughput series, bounded to four-hour queries and 24-hour retention in metrics_fine. Source timestamps deduplicate cached reports; receiver time adjusted by sample age assigns buckets without trusting Agent wall-clock alignment. Samples older than three configured fast intervals are omitted. No minute buckets are expanded into invented five-second samples, and no historical backfill or migration is performed. Existing minute/hour histories remain available for longer ranges. / nodes.history 新增 resolution=5s，仅保存网络收发和磁盘读写四项速率；查询最多四小时，高频历史保留24小时。使用来源时间去重缓存上报，并按接收时间减采样年龄分桶；超过三个快速采样周期的旧值不重复写入。不将已有分钟数据插值为五秒点，不做历史回填或迁移；较长时段仍使用原有分钟／小时历史。
