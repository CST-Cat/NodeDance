# Go optimization study / Go 优化学习

Reviewed Komari Core at `13cbdca545e3427f720cfe55a0db787048a0a3be` (verified upstream HEAD) and Agent at `828afafe7fb5b1b58c1232094e1e46320719016e`.

Sources / 来源：

- [Connection-local SQLite tuning](https://github.com/komari-monitor/komari/blob/13cbdca545e3427f720cfe55a0db787048a0a3be/internal/sqlitetune/connector.go), [Core baseline](https://github.com/komari-monitor/komari/blob/13cbdca545e3427f720cfe55a0db787048a0a3be/database/dbcore/dbcore.go).
- [File stream buffer pool and bounded concurrency](https://github.com/komari-monitor/komari-agent/blob/828afafe7fb5b1b58c1232094e1e46320719016e/server/file_stream.go).
- [Metric write batching, failure handling and shutdown flush](https://github.com/komari-monitor/komari/blob/13cbdca545e3427f720cfe55a0db787048a0a3be/internal/metricstore/report_batcher.go).

Applied / 已应用：

| Change / 改动 | NodeDance behavior / 本项目行为 | Evidence / 验证 |
| --- | --- | --- |
| SQLite connection limits / 连接限额 | 2 MiB page cache, mmap disabled, file-backed temp storage, spill enabled, checkpoint every 256 pages, 1 MiB post-checkpoint journal limit. DSN applies settings on replacement connections. / DSN 保证替换连接仍有这些设置。 | PRAGMA checks before and after connection replacement / 替换前后校验 |
| File buffer reuse / 文件缓冲区复用 | Downloads and task uploads reuse a 32 KiB buffer; clear it before release. Existing concurrency and file policy still apply. / 下载和任务上传复用 32 KiB 缓冲区，归还前清空内容。 | File transfer integration tests, scrub test, allocation benchmark / 传输集成、清空测试、分配基准 |
| Direct byte decoding / 直接字节解码 | Agent and Core decode bytes without constructing an intermediate string. / 避免将整个协议帧先复制为字符串。 | Existing strict frame and payload validation tests / 严格协议验证测试 |
| Indexed immutable operation contract / 操作索引 | Build schemas once; look up by name and return caller-owned copies. / 结构只构建一次，按名称索引，返回调用者独立副本。 | Mutation isolation, race checks, lookup benchmark / 副本隔离、竞态检测和查找基准 |

NodeDance already writes all valid values in one metric snapshot in one transaction with a prepared statement. It retains synchronous durable history inserts; asynchronous enqueue would change failure and durability behavior. `synchronous(FULL)` remains enabled, including audit and credentials. The journal limit applies after checkpoint; large transactions and active readers can temporarily grow WAL beyond that number. These adaptations use NodeDance's pure-Go SQLite driver and current security policy.

NodeDance 已用事务和预编译语句写入单个指标快照中的全部有效值。本次保留同步持久化，异步排队会改变错误及耐久性语义；包括审计和凭据在内，仍使用 `synchronous(FULL)`。日志限额在 checkpoint 后生效，大事务或活动读者可能使 WAL 暂时超过该值。实现使用本项目已有的纯 Go SQLite 驱动和安全策略。

Local Linux arm64 microbenchmarks / 本机 Linux arm64 微基准：

| Case / 项目 | Baseline / 原实现 | Applied / 新实现 |
| --- | --- | --- |
| `nodes.list` lookup | 287090 ns, 155547 B, 1124 allocations | 67.81 ns, 0 B, 0 allocations |
| 32 KiB transfer buffer | 7033 ns, 32768 B, 1 allocation | 775.6 ns including clear, 0 B, 0 allocations |

These measure individual functions, not total request latency; timings vary by machine. Reproduce / 仅衡量对应函数，整体请求性能需单独测量，复现命令：

```sh
go test -run '^$' -bench 'BenchmarkOperationLookup|BenchmarkTransferBuffer' -benchmem ./internal/control ./internal/agent/transferbuffer
```
