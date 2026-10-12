# Engineering verification / 工程验证

This records verification of the input precision/parity, command-history lifecycle, operation-contract accuracy, resource bounds, interruption recovery and capacity changes. It is evidence for the specified scenarios, not a claim that every possible root or Docker operation has been exercised.

## Environment and isolation

Linux arm64, Go 1.26.0, Node 22.23.3. Tests use temporary Core/Agent state, loopback listeners and dedicated Docker fixtures. No production deployment is used by the capacity or browser tests. Root command tests execute only their own temporary scripts. Docker transactional tests replace and recover their own temporary containers/images and clean them up.

## Checks

- `make check`: frontend type/i18n/discovery checks, Python tests, Go suite and generated interface coverage (114 remote operations and 15 local/workflow entries).
- `go test -race ./internal/control ./internal/core/cli`: shared contracts, native integer bounds, numeric enums, completion and terminal input parity; CLI pipe and TUI FIFO upload tests send identical binary bytes to a real HTTP endpoint.
- Build `go test -c -race -o .build/hostcontrol.test ./internal/agent/hostcontrol`, then `sudo .build/hostcontrol.test -test.v`: acceptance and idempotency, cancellation/timeouts/output caps, settings policy, history compaction, result write failure, restart and no replay. The capacity fixture seeds 10,000 in-memory summaries to exercise archive pressure; it is not a 10,000-file startup benchmark.
- Build `go test -c -o .build/server.test ./internal/core/server`, then `sudo .build/server.test -test.run TestRootAgentHTTPHostControlEndToEnd -test.v`: real root Agent/Core over HTTP and automatic fallback, script/file/settings behavior, Core restart while a child result is missing, recovery of the original job without script replay and cleanup RPC.
- Build `go test -c -o .build/update.test ./internal/update`, then `sudo env NODEDANCE_TEST_DOCKER_UPDATE=1 .build/update.test -test.run TestDockerTransactionalUpdate -test.v`: real Docker replacement, health failure rollback and interrupted-transaction recovery. Release pulling is substituted with local fixture images; it does not verify a newly published release download.
- `pnpm --dir web audit:interfaces`: isolated real Core with browser checks for generic uploads, empty fields, uint64 request/response precision, uncertain key restoration after reload, observation after reload without resubmission, credential masking and discovered operation extension. Sixteen light/dark desktop/mobile snapshots cover changed Operations, Updates, Agent settings and asset forms; overflow assertions run automatically. Synthetic routes cover unavailable Agents and future task contracts. The specialized asset form reads uint64 max exactly and saves `9007199254740993` unchanged to the real Core.
- Slow JSON body and saturated request tests use real TCP to verify HTTP 408, capacity release, HTTP 503/Retry-After and reachable health checks. Batch-retention tests preserve partial, stale, unknown and interrupted results.

## 100-node simulated soak

Reproduce with:

```sh
NODEDANCE_TEST_SOAK_NODES=100 NODEDANCE_TEST_SOAK_SECONDS=600 \
  go test ./internal/core/server -run '^TestCoreCapacitySoak$' -count=1 -v
```

Observed run: 600 seconds, 100 simulated Agents, 10,800 metric reports and 32,600 measured Agent-channel HTTP requests (administrator calls are additional). Each report is checked for acknowledgment and applied node snapshot within two seconds. Real Core protocol channels, SQLite enrollment/history, periodic administrator reads and retention cleanup execute in the same run.

| Measurement | Observed |
| --- | ---: |
| HTTP request p95 | 97.783 ms |
| Test-process live heap after GC | 3,636,448 → 3,833,600 bytes |
| Goroutines | 375 → 372 |
| Open file descriptors | 55 → 53 |
| SQLite database size | 880,640 bytes |

The test passed its bounded heap/goroutine/descriptor assertions. HTTP p95 measures the simulated Agent channel requests (negotiation, heartbeat, polling and metric submission), excluding administrator calls; it is not command execution latency. Heap, goroutine and descriptor measurements belong to the whole Go test process, including Core and the load generator. Agents here are protocol simulators, not 100 real servers with Docker workloads. This demonstrates ten-minute behavior at this workload; it does not establish a maximum node count, 24-hour stability, high availability or production network performance.

## 中文说明

本轮验证覆盖输入精度与 CLI／TUI 上传对等、命令历史清理与去重、契约边界、慢请求及并发限制、写盘失败／Core 重启／Agent 重启恢复、真实 Docker 更新与回滚，以及 100 模拟节点 10 分钟运行。记录具体环境、命令及结果，区分真实 Core／root／Docker 场景与协议模拟场景。100 节点数据不能推导为真实主机的最大容量、全天稳定性或生产 SLA；不确定任务保留身份，观察恢复不重复执行。
