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
- Core 只识别原生 `nodedance.service`；Agent 识别原生 systemd 或 Docker 部署。更新请求不能指定任意服务或执行任意命令。
- 由同一个 Core／Agent 二进制按需启动临时工作进程：Core 和原生 Agent 使用临时 systemd 单元，Docker Agent 使用当前 Agent 镜像的临时容器。没有独立更新二进制、安装脚本或常驻更新服务。
- 临时工作进程独立于目标服务的生命周期，完成停止、备份、切换、启动、健康核验；失败自动恢复旧运行版本。Core 恢复更新前数据；Agent 保留当前凭据和任务状态。
- 任务日志与备份位于数据目录的同级私有目录 `.<数据目录名>-updates`，root 所有、0700；日志为普通 0600 文件。观察可在更新持锁期间继续进行，后续请求恢复没有活跃工作进程的中断任务。

Version eligibility is determined solely by the stable component tag. Protocol, API, capability and storage fingerprints never block installation; the authenticated Agent update channel also works without optional update-capability negotiation. Session generation and request identity checks remain required; release checksums, artifact architecture, runtime health and failure recovery remain required.

The installed Core or Agent binary owns the transaction through an on-demand transient systemd unit. A Docker Agent uses a temporary container made from its current Agent image. The worker survives the target restart, verifies releases, backs up private data, preserves deployment settings, checks health/reconnection and rolls back failure. It installs no separate update daemon. Root-private journals and backups live beside the data directory, outside the data backup itself. Retain rollback state and the previous Agent image until recovery is no longer needed.

## Deployment permissions / 部署权限

Core 原生安装在主控机并以 root 运行，因此可以替换自身二进制和管理 systemd 单元。Docker 仅用于各主机上的 Agent；Docker Agent 仍以 root 运行，保留 0700 状态目录、0600 凭据和文件路径保护。NodeDance 不改变 Docker socket 的权限。

Core runs natively on the controller host as root so it can replace its binary and manage `nodedance.service`. Docker is used only for per-host Agents; Docker Agents remain root-only with protected state and credentials. NodeDance does not change Docker socket permissions.

Tailscale 仍是可选能力。Core 只读取主控机现有状态；NodeDance 不安装、配置或修改 Tailscale。

Tailscale remains optional and is only queried on the deployment host. NodeDance does not install or configure it.
