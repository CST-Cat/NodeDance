# Core 与三端能力收束审计 / Core convergence audit

本次检查以 `bee4368` 为审计基线，重点追踪从 Agent Dev 通道引入的 `57a2214` 到 TCP 打洞启动协调的近期变更，同时检查当前全部 Core 操作目录、本地命令分发和三端通用执行器。审计取证阶段对生产资源仅做只读目录与健康检查；测试更新请求使用隔离 HTTP/TLS 服务，未触发生产 Agent 更新、Core Dev 覆盖、备份回滚或 Devweb 启动。

Baseline: `bee4368`. The review follows recent work from Agent Dev (`57a2214`) through TCP hole-punch coordination, then checks the current Core catalogue, local command dispatch and all three generic executors. Production inspection during the audit is read-only. Update tests use isolated HTTP/TLS fixtures and perform no production update, rollback, source overwrite or preview launch.

## 确认并修复的问题 / Confirmed and repaired gaps

| 项目 | 审计前 | 修复后 |
| --- | --- | --- |
| 远端 Agent 更新 | 已有 Core API，命令却需重复组件、Core 地址与 UUID | `nodedance update --node '节点 2' --channel dev` 复用保存的连接，自动选择 Agent；名称精确匹配，重名、空目标、缺失目标拒绝提交 |
| Core Dev 参数 | 源码更新与 Agent 开发构建的通道名称和命令曾不统一 | Web 版本填 `dev`；CLI/TUI 本机与远端 Core 使用 `--channel dev`，沿用已有源码事务，无新更新器或发布标签 |
| 更新观察 | 携带 `--channel dev` 的 `--status` 会多传未声明的 `version` 查询参数 | 历史／状态请求仅发送该操作接受的目标和任务参数，不受通道选项影响 |
| 操作发现 | 未传 `--server` 的 `operations`／`describe` 使用编译时目录，即使已经保存了其他 Core 的登录 | 默认读取已连接 Core，附加 CLI/TUI 共用本地维护入口；明确离线查看改用 `--offline`，发现失败不会静默回退 |
| 本地 rsync 查询 | Agent 已有 `--rsync-version`，共享终端目录缺少入口 | `agent.rsync-version` 由 CLI/TUI 同一分发执行，保留 `nodedance agent --rsync-version` |
| Devweb 本地维护 | CLI 有命令，TUI 没有同等入口 | 共享本地目录加入 `core.devweb`；只在明确调用时运行，不自动创建或维护预览 |

The changes reuse Core's discovered `nodes.list` and `updates.*` definitions. They add no client-owned remote operation list, endpoint, permission rule, arbitrary deployment target or SSH updater. Local Core and local Agent default updates remain independent of saved remote sessions. CLI and TUI use the same workflow, including target resolution, submission, retry identity, observation and rollback confirmation.

## 近期功能的收束情况 / Recent capability coverage

| 功能 | Core 入口 | 三端结论 |
| --- | --- | --- |
| Agent Stable／Dev 手工更新、历史、回滚、备份删除 | `updates.check/apply/status/rollback/delete-backup` | 已有共享远端操作，Dev 经现有版本选择字段传递；短命令是入口简化 |
| Agent 自动更新及 Dev 跟随策略 | `agents.settings`、`agents.configure` | `autoUpdate`、`updateChannel`、更新间隔来自同一请求类型；单次手工选择通道不修改持久策略 |
| Core Stable／源码 Dev | `updates.*` | 三端均可提交；本机更新另外保留 root 维护入口 |
| 多节点文件对传、限速、完整性、取消和重试 | `files.transfer`、`tasks.get/list/cancel`、`files.transfer-retry` | 请求模式、任务及文件安全策略由 Core／Agent 执行；TCP 打洞与 ICE 是同一严格直连模式内部路径，不需要各端再实现传输策略 |
| 声明直连端口 | `nodes.asset`、`nodes.save-asset` | `directListenPort`／`directPublicPort` 在共享 NodeAsset 中；三端均可读写 |
| 直连失败诊断 | `files.transfer` 结果与 `tasks.get` 任务错误 | Agent 产生诊断，Core 传递同一结果；并非仅 Web 可见 |
| 终端能力、tmux 恢复和关闭 | `terminal.create/open/resume/connect/cancel` | 共用请求、票据及终端实现；CLI/TUI 共用交互和 JSONL 传输，Web 终端是图形呈现 |
| 主网络接口计数、节点地理位置 | `nodes.metrics`、`nodes.list`、位置与偏好操作 | 计算及公开投影在 Agent／Core，专用页面不另行产生管理行为 |
| 文件菜单、触摸长按、窄屏弹窗 | 现有 `files.*`／分享操作 | 手势和布局属于 Web；实际文件／分享变更仍调用 Core，终端无需复制图形手势 |
| Docker／Compose／镜像、主机命令、探针／告警、订阅、域名证书、分享、账号、展示设置 | 各自 Core 操作族 | 通用 CLI/TUI/Web 入口读取运行中契约；目录覆盖不等于每一项 root／Docker 副作用均已实测 |

## 仍未提供共享管理入口的配置 / Remaining configuration gap

自定义 NAT 发现服务仍依赖 Agent 服务环境变量 `NODEDANCE_STUN_SERVERS` 与 `NODEDANCE_TCP_STUN_SERVERS`。当前 `protocol.AgentSettings` 不包含对应字段，因此不能通过 `agents.configure` 从 Web、CLI、TUI 修改、读取或验证这些自定义地址。这是明确的共享配置缺口；本次未把它描述成已收束，也未通过远程脚本修改三台诊断 VPS 的服务环境。

Custom UDP/TCP STUN service selection remains Agent process configuration. `AgentSettings` has no corresponding fields, so the three remote interfaces cannot manage or inspect these values through Core. Default traversal and direct-mode operation already share Core, but custom discovery configuration does not. This audit does not change the diagnostic VPS service environments.

Core 监听／TLS／代理信任、Agent 注册凭据、systemd 安装、文件暴露根目录、文件访问开关和主机本地备份／恢复属于部署或本地恢复权限边界，不能仅为“三端相同”把私有凭据或主机引导能力开放成 Web 远程配置。已有本地维护能力须在 CLI/TUI 对等呈现。发布彩排、构建依赖和 GitHub Actions 属于开发发布流程，也不算缺失的远程管理操作。

Core listening/TLS/proxy trust, Agent credentials and systemd installation, local file-access boundaries and local backup/restore are deployment or recovery responsibilities. They do not require a Web remote equivalent. Their existing local maintenance presentations must remain equal in CLI/TUI. Build/release automation is outside the remote management contract.

## 检查方法和边界 / Verification boundaries

- 读取生产 `/api/v1/operations` 并对照共享目录和执行器；通过 TypeScript AST 检查 Web 固定操作名，未发现固定名称引用未发布操作。条件分支、变量和通用操作中心另外按执行代码核对，静态数量不代表行为覆盖率。
- 隔离 TLS fixture 验证保存的登录、Origin／私有 CA、名称及完整 ID 选择、发现的自定义路由、重名／缺失／空目标拒绝、通道参数与状态观察；PTY TUI 使用同一更新函数和连接继承。
- 本地更新 fixture 验证 Core／Agent 的 `dev` 选择经共享参数解析转为各自事务请求，冲突选项不提交；本地维护测试验证 Core 不在线时 TUI 能调用 rsync 查询和 Devweb 帮助，不执行部署。
- `internal/core/cli`、`internal/update`、`internal/control` 回归及自动生成的接口表检查负责终端／契约行为；浏览器通用操作和更新中心检查使用全新构建的内嵌 Web 与隔离 Core，不把目录表行数或静态引用数当作实机管理验证。

没有为本次入口改动升级产品版本、创建发布标签、重启远端 Agent 或改变严格直连策略。生产交付使用标准 Makefile 构建／安装／重启流程；健康、实际运行二进制和本地／公开静态资源另行核对。
