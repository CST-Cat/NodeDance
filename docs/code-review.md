# 重点代码审查记录

本轮在 `main` 上审查并修复，后按用户的额度要求收缩为重点审查。未升级版本、创建 release tag、发布镜像或更新机器。其他 agent 的 VueFinder 补丁复制修复已保留。

首批收尾时，逐文件审查的保守进度为 **95 / 331** 个代码、测试和主要构建配置文件；**236 个尚未完成全文深度审查**，另有 55 个文档、锁文件及资源文件需要按类型检查。后续按具体风险分批检查，不将重读或新增回归测试计入这份历史覆盖数字。全仓搜索、局部阅读和测试通过均不算完成逐文件审查。本记录是重点发现及验证结果，不是全仓无缺陷证明。

## 已修复的重点问题

最近一次核对：沿用原 331 个文件清单，已完成 **331 / 331**，尚未完成 **0 个**。最后 69 个文件的逐项结论、修复及验证见 [69 文件收尾报告](code-review-69-files.md)；此前结果见 [110 文件报告](code-review-110-files.md) 和 [50 文件报告](code-review-50-files.md)。全仓搜索、局部阅读、重读和新增回归文件不用于增加原覆盖数。JSON 资源按全部结构、键值约束及调用关系审查，不将机器校验称为逐句人工翻译审校。另列的 55 个文档、锁文件及资源仍不属于本代码清单；覆盖完成不代表代码无缺陷。

| 位置 | 原问题与影响 | 修复及验证 |
| --- | --- | --- |
| `internal/installer/command.go` | 原生安装命令尾部误拼接 Docker updater 命令，使用未设置的 `$image`，还会掩盖前面的安装退出码。 | 移除错误命令；隔离 shell 回归验证不执行 Docker，并保留安装失败退出码。 |
| `internal/agent/files/service.go` | 同时持有 `os.Root` 和原始目录描述符；保护路径校验失败时只关闭前者，泄漏描述符。备用文件访问分支在正常初始化后不可达。 | 使用单一拥有生命周期的目录句柄；初始化失败统一释放，保留 `openat2` 和符号链接拒绝策略。补充泄漏、关闭后读写拒绝及浏览器路径穿越回归。 |
| `internal/agent/probes/bridge.go` | 输入关闭、无效调度或写入失败退出时，先等待 worker，却未先取消它们；worker 的结果发送也没有取消分支。 | bridge 拥有可取消上下文，先取消再等待；结果发送可中止。覆盖输入关闭、无效消息、写失败及父上下文取消。 |
| `internal/update/manager.go` | 关闭流程没有把版本查询和任务接受阶段计入等待，可能在任务尚未退出时释放控制器锁，或关闭后仍接受新任务。 | 增加关闭状态，接受阶段纳入 worker 计数并关联关闭取消；持锁直至接受与执行均结束。回归验证关闭拒绝、锁所有权和无晚到任务。 |
| `internal/update/manager.go` | 手动回滚忽略持久化 `rolling_back` 失败，可能在无法留下恢复记录时修改运行环境。 | 记录失败立即终止；故障注入验证不执行系统命令或运行环境变更。 |
| `internal/agent/containerstreams/stats.go`、`container_stream_bridge.go` | 容器统计数据重复定义 DTO，再经 JSON 编解码转成协议类型，增加分配和契约漂移风险。 | 直接使用协议类型，保留订阅者之间的数据隔离；验证大整数、未知指标、单次共享采样和订阅者隔离。 |
| `internal/core/containerprefs` | 无调用的内存实现、空实现和注入接口与实际 SQLite 实现并存。 | 删除未使用的替代实现，直接使用 SQLite；验证持久化迁移、冲突不覆盖、幂等重试及节点隔离。 |
| `internal/core/storage` | 保留已退役监控约束的迁移代码及专属测试。 | 按仓库仅支持当前实现的规则移除；保留当前 schema 初始化及重开测试。 |

另外删除了经引用检查确认无调用的 17 个辅助函数，复用终端的统一完整容器 ID 校验，并让请求解码复用一次路由契约匹配结果。没有为精简而拆改大型运行时状态机，也没有删减原生双架构测试。

## 发布流水线优化

涉及 `.github/workflows/{agent-release,core-release,docker}.yml`、两个 Dockerfile、发布说明脚本和 `scripts/release_images.py`。

1. amd64 和 arm64 分别在对应原生 runner 完成编译、执行、测试和镜像打包；发布镜像不再通过 QEMU 执行 arm64 系统包安装。
2. Docker `release` target 直接复制已经验证的二进制。Core Web 只在 prepare 构建一次。默认 Docker target 仍支持完整源码构建，并保留 VueFinder patches 安装顺序。
3. 每个原生镜像推送 digest 后，核验架构、运行用户、版本和镜像内二进制 SHA-256；两架构均通过后才合并正式多架构标签。
4. 发布镜像缓存改用各组件、各架构的 registry cache，`mode=min`。它避开 tag 对 GHA cache 的读取隔离，并避免为纯打包流程导出大量中间构建层。首次构建仍可能是冷构建，缓存导出仍有成本。
5. `agent-release.version` 明确关联 Agent，当前固定已发布的 `agent-v0.1.12`。Core 的构建不再等待 Agent release；最终发布阶段才检查指定正式版本，限时等待。今后发布新配对版本时须显式更新这个文件。两个组件的 tag 若同时触发，大部分构建可并行。

设计依据：[Docker 原生多 runner 构建示例](https://docs.docker.com/build/ci/github-actions/multi-platform/)、[registry cache](https://docs.docker.com/build/cache/backends/registry/)、[GitHub 缓存访问边界](https://docs.github.com/en/actions/reference/workflows-and-actions/dependency-caching#restrictions-for-accessing-a-cache)。

本机原生 arm64 已实际构建并验证两个 release 镜像；验证上下文没有 Go/Web 源码，且镜像内程序与提供的二进制逐字节一致。**尚未运行新的正式 GitHub release，不能给出实测总耗时，也不承诺 4–7 分钟。** amd64 原生验证由工作流执行，本轮没有本机 amd64 运行结果。

## 已完成的验证

- `go test -race ./...`、`go vet ./...`。
- root Agent/Core 集成测试及 updater race 测试；真实隔离 Docker 更新的成功、失败回滚和中断恢复场景。
- 新构建 Web 资源后的 guest 安全套件、CLI/TUI/Web 接口覆盖检查、真实 Core/root Agent 文件管理浏览器回归；保留测试脚本与断言。
- `pnpm --dir web typecheck`，文件管理依赖许可证校验。
- 工作流 `actionlint`、发布说明 Python 测试、源码 Docker 构建、原生 arm64 release 镜像打包与二进制校验。

这些验证使用隔离测试资源，没有操作生产机器。浏览器测试通过不代表所有 Web 文件都已完成逐行审查。

## 第二批：消息发送与指标截断

上轮两条线索已复现并修复，未修改操作契约或能力版本：

- `internal/agent/metrics_writer.go`：在每次发送后立即补充 Docker 数据的确定性测试中，101 次发送没有一次服务终端、日志或文件队列。原先多组 burst 分支只保障 Docker 与 metrics 之间让步。改为有配额的轮转，每个数据队列都有发送机会；每帧之前仍检查控制队列，保留 FIFO、队列上限和原有超时及失败处理。删除重复的优先级分支，发送函数比原先更短。回归同时检查持续负载下各队列进展、Docker 顺序及控制消息中途抢占。
- `internal/agent/metrics/protocol.go`：复现截断后报文仍为 65,580 字节，超过 65,536 字节限制。现在每次二分试算都包含最终截断标记，覆盖 129 组元数据长度边界，并验证磁盘明细保留尽可能多的有序前缀。
- 同一截断流程还会在移除全部磁盘明细后留下 `known` 状态，造成 `disk is known but has no value`，使整个指标报文被拒绝。现在改为 `unknown` 并明确记录 `metrics_payload_limit`；仍保留主机、CPU、内存和网络汇总。

第二批验证：root 环境下 Agent、metrics、protocol 的 race 测试及对应 `go vet` 通过；文件管理浏览器回归使用重新构建的 Web 资源与隔离的真实 Core/root Agent，通过上传、下载、8 MiB 编辑、冲突恢复和四种布局检查。

## 第三批：私有状态与流量计数恢复

检查 `internal/agent/localstate/state.go`、`internal/agent/metrics/host.go` 及对应测试，修复以下已复现问题：

- **状态读写上限不一致。** 原读取器在 4 MiB 处制造 EOF，合法 JSON 加空白填满上限后，文件尾部的第二份 JSON 会被忽略。写入端也没有大小检查，可以把原有状态覆盖成自己无法读回的文件。现在读取上限外的一个字节以识别超限，并在创建临时文件前拒绝超大的序列化结果。保留 root、0700/0600、普通文件和禁止符号链接要求。回归检查恰好 4 MiB 可读、隐藏尾部拒绝，以及超大写入失败后旧状态不变。
- **缺失启动身份仍累计流量。** 原代码忽略 boot ID 读取错误，将空身份作为有效计数基线持久化，失去跨重启识别能力。现在身份读取失败或为空时返回未知指标，不持久化这次采样；恢复后建立有效基线。回归覆盖空身份、读取失败、恢复后的首个基线和后续增量。
- **保存失败提前清空旧基线。** 修改网卡筛选配置会先改内存 counters，随后持久化失败也不恢复。故障注入复现回到原配置后漏计流量。现在候选计算仅修改局部状态，落盘成功后才替换已接受基线；状态加载失败也不会把部分解码结果写入当前状态。

流量采样同时改为传递调用方上下文；取消后不提交采样，避免使用 `context.Background()` 脱离关闭流程。新增取消回归验证上下文传递和无状态文件写入。本批没有改变操作契约、状态文件格式或产品版本。

第三批验证：root 环境下 localstate、metrics、hostcontrol 和 Core server 的 race 测试全部通过；相关 Agent 包的 `go vet` 及 diff 格式检查通过。

## 后续五批重点审查

本轮沿用前面的修改，按五批检查具体生命周期和失败路径；没有把局部检查记录成全文件审查，也没有要求每批都产生生产代码改动。

| 批次 | 检查范围 | 结果 |
| --- | --- | --- |
| 任务启动恢复与停止 | `taskrunner/runner.go` 的 Start/Stop 与 worker 接续 | 复现恢复查询尚未结束时 Stop 提前成功，且未取消恢复上下文。启动恢复现在计入等待组，Stop 取消并等待；超时如实返回，恢复退出后才能报告停止完成。 |
| 终端生命周期 | `terminal/manager.go`、`tmux_linux.go` 及端点关闭路径 | 修复父上下文取消后会话仍占用资源；修复打开途中关闭时等待一个从未启动的 reader，以及关闭后才返回的端点安装竞态；复现并修复输入写入持锁导致关闭无法中断写入的死锁，会话层和 tmux 层均释放锁后执行阻塞写入。 |
| 文件传输收尾 | `files_bridge.go` 与缓冲池释放路径 | 复现空文件下载最终帧入队失败被忽略，Core 收不到完成或失败结果。现在明确发送传输失败，并按原有 defer 释放文件、缓冲和传输槽位。 |
| Core 资源释放 | `container_streams.go`、`limiter.go` 及请求容量回归 | 未确认需要修改的生产错误。补充慢消费者隔离、迟到帧忽略、健康流不受影响及断连清空测试；既有慢请求超时释放与容量拒绝测试通过。限流容量与有界队列保留。 |
| 安装与发布校验 | 三个安装脚本、发布镜像验证脚本 | Core 安装器原先仅验证归档和 checksum；即使归档内程序版本不符或无法执行，也会覆盖旧程序。用完全离线的 release fixture 复现后，在原子替换前增加暂存程序执行及精确版本检查，失败保留旧安装。Agent tag 固定、凭据处理和镜像产物校验保持现有机制。 |

五批验证：root 下 Agent 全部包及 Core server 的 race 测试通过；终端阻塞输入、打开中止和任务启动恢复的新增回归通过；真实 tmux 的现有 root 回归通过；Core 资源隔离专项测试通过；7 个 Python 发布/安装测试通过；Shell 语法、相关 Go 静态检查和 diff 检查通过。文件传输修改还通过重新构建 Web 后的隔离真实 Core/root Agent 文件管理浏览器套件（含上传、下载、8 MiB 编辑、冲突和四种布局）。没有执行正式安装、发布或生产机器操作。

## 留待后续的检查

本批已完成完整 Agent runtime、终端生命周期、文件任务执行、重点 Core API 与 Web 专用界面。剩余重点为 Agent Compose/Docker 动作、Core 持久任务实现及相关集成测试，具体 69 文件见最新报告。后续应先追踪具体风险和跨模块生命周期，再决定是否重构；函数长或存在局部相似代码本身不足以证明设计错误。

## 历史兼容残留专项复核

2026-10-13 的增量清理见 [当前实现与历史兼容残留审查](code-review-compatibility.md)。移除旧安装包装、任意 UID 凭据辅助链、无调用的 systemd 校验、失效参数和隐式 schema 补齐。最终全项目 root race 测试、非 root 拒绝测试与 go vet 通过。
