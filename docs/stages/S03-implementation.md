# S03 standalone collector implementation

This note documents the collector owned by `internal/agent/metrics`. It does not mark the S03 stage complete: the shared protocol, Agent/Core integration, live API/UI, Docker-probe isolation, network disconnect/recovery, clock-shift/reboot guest, and the complete S03 acceptance run remain outside this worktree or still need execution. The normative cases and stage report remain unchanged.

## Samples and calculations

`Collector.Sample` reads Linux host/system information, aggregate CPU counters, memory, per-interface network counters, uptime, and the kernel boot ID. It does not call Docker, Core, or browser code. `Start` publishes a fast snapshot every five seconds using a one-item newest-value channel; slow transport consumers cannot hold up sampling. Disk partitions and usage run on the separate thirty-second schedule.

Each potentially unavailable measurement has `state`, `sampledAt`, and, when unknown, a stable `reason`. Unknown measurements omit their value, so a permission or read failure cannot look like a valid zero. A failed measurement does not clear unrelated fields.

CPU usage is `100 × (Δtotal − Δidle − Δiowait) / Δtotal`, where `Δtotal` is the sum of Linux user, nice, system, idle, iowait, irq, softirq, and steal deltas. Linux guest and guestNice are validated for reset but not added again because they are already included in user and nice. The first sample, a counter reset, a changed CPU set, and a zero-length interval are unknown. Idle and iowait both count as non-busy time.

Memory used is `MemTotal − MemAvailable`, reported in bytes. Network rates are byte-counter deltas divided by elapsed seconds, in bytes per second. The first sample, a reset, an invalid interval, or a changed aggregate interface set is unknown; a reset never yields a negative rate.

Network interface details retain excluded interfaces with a reason. Aggregate selection omits loopback, known virtual devices, bridge devices, virtual upper/lower links, and bond slaves when the bond master is available. Bond master counters are counted once. If interface state or topology cannot be read safely, the aggregate is unknown rather than an incomplete or potentially duplicated sum. Interface byte counters remain available individually when topology makes only the summary unknown.

Disk usage is collected per mountpoint and not summed across mounts; there is no cross-mount disk total. Pseudo filesystems are omitted. The disk poller admits one active scan and gives its caller a three-second timeout. A concurrent scan returns `worker_busy` only while the earlier scan has not returned. Every accepted scan runs in one goroutine that exits on completion, with no parked worker between scans. This handles APIs such as statfs that may ignore context cancellation: a stuck call can occupy at most the one active scan and does not block fast CPU, memory, network, or uptime sampling. Its status remains unknown until a later scan completes.

Uptime comes from the host uptime counter and is paired with `/proc/sys/kernel/random/boot_id`; boot time is optional. This provides the sample identity needed to distinguish a reboot from a wall-clock correction. Time-offset interpretation and Core stale/recovery behavior require the later integration tests.

## Verification

Run `scripts/test/s03-collector.sh` on Linux. It requires the pinned Go 1.26.8 executable, tests and vets only this package with module files read-only, and writes logs outside the source tree. Set `NODEDANCE_GO_BIN` to the locked executable when it is not in the main checkout's `.tools` directory; set `S03_TEST_OUTPUT_DIR` to choose where logs are retained.

The package tests include deterministic CPU/memory/disk/network formula cases, initial-sample/reset behavior, multi-interface deduplication, bounded disk-scan timeout and recovery, repeated completed scans, canceled callers, and partial read failures. Linux live tests independently compare gopsutil reads with `/proc`, `syscall.Statfs`, boot ID, and uptime. A controlled CPU worker and touched 256 MiB allocation exercise real host measurements and process RSS. That load test skips when the host has under 1 GiB available or when host-wide memory does not reflect the touched allocation; any such skip is `NOT_READY` for stage acceptance and must be rerun in an isolated Linux guest. A zero exit code alone does not turn a skip into PASS. These package results are implementation evidence only; they do not substitute for the S03 integration and fault-acceptance cases.
