# Updates / 内置更新

更新已内置于 Core 和 Agent，两个本机更新入口彼此独立。root 直接运行 `nodedance update` 更新本机 Core，或 `nodedance agent update` 更新本机 Agent；均无需登录、已保存会话、Core API 连接或额外更新服务。TUI 的 `client.update` 和 `agent.update` 使用相同本机维护流程。默认 Core 更新不会读取已保存的远程连接。显式 `--server URL`（或 `NODEDANCE_SERVER`）以及远端 Agent 目标走 Core 发现契约，继续验证管理员权限和 CSRF。

Core and Agent have independent local update entrypoints. As root, run `nodedance update` for the local Core or `nodedance agent update` for the local Agent, without login, saved sessions, a Core API connection or extra updater setup. TUI exposes the same local workflows. Default Core updates ignore saved remote connections. Explicit `--server URL` (or `NODEDANCE_SERVER`) and remote Agent targets use the discovered Core contract and retain administrator authentication and CSRF checks.

```sh
nodedance update        # 更新 Core / Update Core
nodedance agent update  # 更新本机 Agent / Update the local Agent
```

`nodedance agent update` 默认更新本机 Agent，需要 root，由 `nodedance` 直接调用共用的内置更新流程。通过 Core 更新指定远端 Agent 时仍需选择节点，这是更新目标的选择，不是安装更新器：

The `nodedance agent update` command updates the local managed Agent and requires root. It runs the shared built-in transaction directly from `nodedance`. Selecting a remote Agent through Core still requires its node ID:

```sh
nodedance update --server https://core.example --component agent --node-id NODE_UUID
```

## Observation and recovery / 观察与恢复

`--check` 只检查，`--version core-vX.Y.Z`／`agent-vX.Y.Z` 可固定版本。默认等待完成；`--no-wait` 在持久接受后返回。提交前输出 `Idempotency-Key`；连接中断后用 `--idempotency-key KEY` 重试原请求，避免重复执行。`--status` 查看历史，`--status --job-id JOB_ID` 查看进度。Core 回滚使用 `--rollback JOB_ID`，交互确认后执行；Agent 本地回滚同样交互确认，`--yes` 可跳过确认。远端 Agent 的观察和回滚沿用相同的 `--component agent --node-id NODE_UUID` 目标选择。

`--check` is read-only; `--version` optionally pins a release. Updates wait by default; `--no-wait` returns after durable acceptance. Reuse the printed `--idempotency-key` after interrupted submission. `--status` lists history and `--job-id` selects a job. Rollback requires confirmation; Agent local rollback also asks for confirmation; `--yes` skips the prompt. Remote Agent observation and rollback keep the same component/node selection.

## Execution / 执行

- 从各自 `core-v*`／`agent-v*` 命名空间选择正式版本；仅比较 tag 判断是否有新版，不比较协议号、能力、API 或存储指纹，不要求 `nodedance-update.json`。
- 切换前下载并校验 SHA256；远端 Docker Agent 还会固定当前 Agent 镜像 ID。下载失败不停止旧程序。
- 原生更新按 64 KiB 分段落盘并建议内核释放已写入的文件缓存，二进制备份／替换及数据备份使用 32 KiB 读取缓冲、关闭预读并及时释放已读取的源文件缓存，不整文件载入内存；校验失败删除残留下载。Agent 临时更新进程限制为一个 Go 执行线程，使用 2 MiB Go 软内存目标（不是总内存硬上限）。下载后不再额外运行新二进制做版本预检，启动后统一核验运行版本、健康状态及重连。
- Core 只识别原生 `nodedance.service`；Agent 识别原生 systemd 或 Docker 部署。更新请求不能指定任意服务或执行任意命令。
- 由同一个 Core／Agent 二进制按需启动临时工作进程：Core 和原生 Agent 使用临时 systemd 单元，Docker Agent 使用当前 Agent 镜像的临时容器。没有独立更新二进制、安装脚本或常驻更新服务。
- 临时工作进程独立于目标服务的生命周期，完成停止、备份、切换、启动、健康核验；失败自动恢复旧运行版本。Core 恢复更新前数据；Agent 保留当前凭据和任务状态。
- 任务日志与备份位于数据目录的同级私有目录 `.<数据目录名>-updates`，root 所有、0700；日志为普通 0600 文件。观察可在更新持锁期间继续进行，后续请求恢复没有活跃工作进程的中断任务。

Version eligibility is determined solely by the stable component tag. Protocol, API, capability and storage fingerprints never block installation; the authenticated Agent update channel also works without optional update-capability negotiation. Session generation and request identity checks remain required; release checksums, artifact architecture, runtime health and failure recovery remain required.

The installed Core or Agent binary owns the transaction through an on-demand transient systemd unit. A Docker Agent uses a temporary container made from its current Agent image. The worker survives the target restart, verifies releases, backs up private data, preserves deployment settings, checks health/reconnection and rolls back failure. It installs no separate update daemon. Root-private journals and backups live beside the data directory, outside the data backup itself. Retain rollback state and the previous Agent image until recovery is no longer needed.

Native downloads and binary copies sync in 64 KiB increments and ask the kernel to discard written file cache, without loading whole artifacts into memory. Binary backup/install and data snapshot copies use a 32 KiB read buffer, disable read-ahead and discard consumed source cache. Failed downloads are removed. Agent workers use one Go execution thread and a 2 MiB Go soft memory target, which is not a total process memory limit. The downloaded binary is no longer launched for a separate version preflight; runtime version, health and reconnection are verified after startup. The focused large-artifact regression covers HTTPS download, SHA256 verification, backup and installation of a 48 MiB fixture and data snapshot copying under an 8 MiB cgroup limit with swap disabled. This is a transfer regression, not a complete 8 MiB VPS boot and update test; the OS, running Agent and other services also need memory. Core authentication alone uses more than 8 MiB, so this does not establish 8 MiB Core support.

上述 48 MiB 文件回归已在 8 MiB cgroup 硬上限、禁用 swap 下验证 HTTPS 下载、SHA256、二进制备份／替换及数据备份。它不等于整台 8 MiB VPS 的启动和完整更新验证；系统、现有 Agent 和其他服务仍需内存，Core 的认证本身已超过此预算。临时目录和更新目录需使用磁盘文件系统；tmpfs 中的文件仍占用内存，释放文件缓存无法消除此占用。

Use disk-backed storage for temporary/update directories: tmpfs file contents still consume memory and cannot be discarded as clean disk cache.

Reproduce the focused transfer check on an isolated Linux/systemd development host (the test uses temporary files and an HTTPS fixture, never a real service update):

```sh
go test -c -o /tmp/nodedance-update-test ./internal/update
sudo systemd-run --quiet --wait --pipe --collect \
  -p MemoryMax=8M -p MemorySwapMax=0 \
  /tmp/nodedance-update-test -test.run '^TestLargeAgentDownloadAndInstall$' -test.v
```

## Deployment permissions / 部署权限

Core 原生安装在主控机并以 root 运行，因此可以替换自身二进制和管理 systemd 单元。Docker 仅用于各主机上的 Agent；Docker Agent 仍以 root 运行，保留 0700 状态目录、0600 凭据和文件路径保护。NodeDance 不改变 Docker socket 的权限。

Core runs natively on the controller host as root so it can replace its binary and manage `nodedance.service`. Docker is used only for per-host Agents; Docker Agents remain root-only with protected state and credentials. NodeDance does not change Docker socket permissions.

Tailscale 仍是可选能力。Core 只读取主控机现有状态；NodeDance 不安装、配置或修改 Tailscale。

Tailscale remains optional and is only queried on the deployment host. NodeDance does not install or configure it.

## 发布日志与备份管理

`updates.release` 独立读取所选组件的稳定发布标签及该标签下的双语 `CHANGELOG_CORE.md`／`CHANGELOG_AGENT.md` 条目。它不执行运行环境预检、不要求 Agent 节点在线，也不依赖 GitHub REST API 配额；获取失败会返回真实错误，不能将失败伪装为空日志。Web 的更新日志与更新记录分别直接展示。

工作区构建保留真实的 `dev` 版本，不伪造发布标签。已配置的本机运行环境可以从 `dev` 检查并选择稳定发布更新；正式版本间仍只比较组件稳定标签，下载校验、架构、权限、健康检查及失败恢复要求不变。

管理员可通过发现的 `updates.delete-backup` 操作删除已完成任务的旧备份。请求包含 `component`、`jobId`、`confirm=true`，远端 Agent 另含 `nodeId`，并使用正常重试身份。Web、CLI、TUI 使用同一 Core 操作。删除保留任务日志、版本与执行状态；删除完成后不能再通过该记录回滚。活动任务、恢复失败状态、正在使用的回滚备份、符号链接工作目录及活动或名称发生变化的容器均拒绝删除。仅移除该任务的私有更新工作目录，以及名称和身份均核实的已停止 Agent 回滚容器；容器删除不得使用 force。删除意图先持久化，期间禁止回滚；失败可重试，完成状态重启后保留。

Web 将“立即更新”与“检查更新”放在同一操作行，更新按钮在前，不在操作区额外显示重启说明。回滚与备份删除均使用本地 UIArc Dialog 明确确认；取消或 Escape 不提交操作，组件默认遮挡、焦点与关闭交互保持不变。弹窗仅在更新中心局部调整分隔线与留白，保留原组件圆角。

删除成功后，记录使用项目既有 Motion 与 UIArc 动效 Token 向右滑出、淡出并收拢空位；退出期间不再接受交互，减少动态效果时直接隐藏。失败或响应未确认前保留原记录；陈旧的刷新响应不能让已清理记录重新出现。当前列表隐藏已删除备份的记录，最后一条退出后移除空列表区域，后端历史继续保留。已完成记录只显示状态胶囊，活动任务仍显示实际阶段。

`node web/scripts/audit-updates.cjs` 使用隔离 Core、协议层更新响应与真实浏览器验证上述流程，包括中英文的代表性桌面／窄屏场景、确认取消与焦点返回、失败保留和减少动态效果；同一回归模块也由完整界面测试调用，不执行真实服务更新或生产备份删除。

更新页不再在进入页面、切换目标或更新结束后自动执行运行环境预检。管理员可以主动检查，也可以直接提交更新；Core 的 apply 操作仍执行完整的稳定发布、运行环境及可更新性检查。指定版本直接来自当前输入，留空由 Core 解析最新稳定版。发布日志、节点列表和更新记录独立加载，主动检查未完成时不阻塞提交，旧检查响应不会覆盖新的目标或版本。
