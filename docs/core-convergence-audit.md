# Shared capability coverage / 共享能力收束

Web、CLI、TUI 以连接的 Core `/api/v1/operations` 为准；CLI/TUI 共用终端操作定义和执行器。本文记录当前入口和边界，完整生成表见 [interface coverage](interface-coverage.md)。

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

## 更新入口 / Update entrypoints

本机 Core 使用 `sudo nodedance update --channel dev`；本机 Agent 使用 `sudo nodedance agent update --channel dev`。远端 Agent 使用 `nodedance update --node NAME_OR_UUID --channel dev`；远端 Core 显式指定 `--server URL`。TUI 的 `client.update` 接收相同参数数组，Web 版本号填 `dev`。Core 从 `origin/main` 构建，Agent 拉取已发布 Dev 构建；默认不选通道仍为 Stable。

`agent.rsync-version` 与 `core.devweb` 是 CLI/TUI 共用的本机维护入口；Devweb 仅在明确调用时运行。更新观察、重试身份和回滚见 [updates](updates.md)，验证方法见 [verification](verification.md)。目录覆盖不能代替实际副作用测试。
