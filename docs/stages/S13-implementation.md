# S13 候选实现记录

**阶段状态：`NOT_READY`。** 当前提交包含可构建的 S13 候选代码、组件测试和 Docker socket 权限回归；未连接真实 Tailnet、隔离 SSH 目标或目标机 systemd Agent，因此不代表 S13 正式验收通过。

本次本机检查环境：Go `1.26.8`、Node.js `22.23.3`、pnpm `12.10.1`、Docker Engine `29.7.2`。

## 候选实现边界

- `internal/core/tailscale` 独立实现 Core 本机 `tailscale status --json` 发现、稳定 peer identity、Linux 分类、Ed25519 发布产物验签、SSH 指纹探测与 pin、一次性凭据、HTTPS 主备地址策略、SSH 预检、Agent 安装和持久任务状态。
- `internal/core/server/tailscale_api.go` 提供受现有管理员私有 API 保护的发现、SSH 指纹、手动注册和部署任务入口。凭据只交给当前部署 goroutine，不写入任务状态。SQLite migration 未改。
- `internal/core/tailscale/ssh_integration_test.go` 带有 gated real-OpenSSH 检查；新增 GitHub Actions 工作流会创建只监听 loopback 的临时 OpenSSH 测试用户，验证真实 host-key 握手、密码错误拒绝、远端命令和权限/Docker socket 预检。该工作流尚未运行，首次 CI 证据待提交后生成。
- `web/src/components/TailscaleDiscovery.vue` 提供响应式候选/不支持/已纳管节点视图、显式指纹确认、手动注册、任务状态和故障说明。
- Agent 发布包必须由受信任的 Ed25519 私钥签名；Core 配置 `NODEDANCE_AGENT_ARTIFACT_DIR` 与 `NODEDANCE_AGENT_SIGNING_PUBLIC_KEY`。缺失或验签失败时部署拒绝继续。
- SSH 安装仅从当前重新发现的 Tailscale IP 建立连接；host key 必须匹配用户当前确认指纹。已保存指纹变化时默认拒绝，只有显式重新确认并完成对新 host key 的 SSH 认证后才更新 pin。
- 备用 URL 必须是 HTTPS origin，且仅在管理员勾选 `allowFallback` 后探测。curl 不关闭 TLS 校验；证书失败会拒绝该地址。
- SSH 预检检查真实 Docker socket 的类型、mode、数字 UID/GID 和既有 Agent UID。`root:docker 0660` 会把 socket 数字 GID 配入 systemd `SupplementaryGroups`，并在安装前再次核对 socket 元数据。`0600 root:root` 等无法安全授权的配置会在创建凭据或启动服务前拒绝。owner 权限仅在已有 `nodedance-agent` UID 与 socket owner UID 相同时允许，安装脚本还会复核 UID。Docker socket 不存在时明确报告容器能力 unavailable。
- SSH 部署与本地 `install-systemd` 使用同一 Agent unit 生成器。默认 `ProtectSystem=strict`、`ProtectHome=tmpfs`，只允许写入 Agent 私有状态目录；主机文件服务默认关闭，也不声明 `agent.files.v1`。
- 管理员可以在 SSH 部署表单中填写可选绝对 file root，或本地执行 `nodedance-agent install-systemd --file-root /absolute/directory`。安装器要求目录已存在，拒绝根目录、广泛或受保护系统目录、Agent 状态目录重叠及任何符号链接路径；精确 root 通过 systemd `BindPaths` 暴露。其余 `/home` 内容保持不可见，其他文件系统仍受 strict 只读沙箱约束。SSH 表单提供单独的“即使目标机已有配置，也明确禁用”选项。
- SSH 部署在 Core 创建一次性注册凭据之前，先把已验签的目标架构 Agent 暂存到被控机，用 artifact SHA-256 复核后调用同一 `validate-file-root` Go 校验器。目标目录不存在、路径含符号链接或与实际 `/var/lib/nodedance-agent` 状态目录冲突时，预检失败并清理临时文件；Core 不创建节点身份，也不把注册凭据交给安装脚本。之后 `install-systemd` 仍会重复校验，避免仅依赖早期预检。
- SSH 请求中的目录先做绝对路径/控制字符和受保护目录检查，再以 base64 数据交给目标安装脚本；远端不把原始用户路径插入 shell 源码，而是解码到变量并作为引号包裹的 CLI 参数。实际文件写权限仍由目标机 Agent UID/GID、ACL 和挂载权限决定。
- 重装时，新显式 `fileRoot` 覆盖旧值；未传值时只从 NodeDance unit 中持久的 file-root marker 保留旧值。对应 `Environment=NODEDANCE_AGENT_FILE_ROOT=...` 行保留可读路径，便于管理员检查。`--no-file-root` 与 SSH 表单中的显式禁用选项会删除旧配置。无 marker 的旧 unit 默认不启用主机文件能力。

## S10 文件访问的 systemd 集成

实际 systemd 权限用例由 gated workflow 运行：选定的 `/home/<user>/...` root 必须可写、同一 home 下未选目录必须不可见，并且服务 UID 原本可写但不在 allowlist 的 host 目录必须写入失败。本机没有可控 systemd manager 时结果仍为 `NOT_READY`，不能用 `systemd-analyze verify` 代替挂载命名空间实测。

## 已执行的候选测试

| 检查 | 实际结果 | 范围 |
|---|---|---|
| `NODEDANCE_S13_TEST_DOCKER_SOCKET=/var/run/docker.sock go test ./internal/core/tailscale` | PASS | 封闭组件单测，并使用本机真实 Docker socket |
| `go test ./internal/core/server -run TestTailscale` | PASS | 实际 Core HTTP handler 的发现 JSON、私有 Session、no-store 与部署接口 Origin/CSRF 门槛；Tailscale CLI 通过测试 fixture 注入 |
| `NODEDANCE_S13_TEST_DOCKER_SOCKET=/var/run/docker.sock go test -v ./internal/core/tailscale -run 'Test(RealDockerEngineConnectsWithAuthorizedSupplementaryGroup\|FreshRemoteWithoutAgentUserUsesSupplementaryDockerGroup\|InstallRefusesDockerSocketThatCannotBeSafelyAuthorized\|DockerSocketOwnerAccessRequiresExistingMatchingServiceUID)$'` | PASS；Docker Engine `29.7.2` | 新节点 `UID=-1` 解析、systemd supplementary GID、无安全授权时拒绝、当前进程通过已授权附加 GID 连接 `/version`。此项没有安装 systemd Agent，也不替代远端 SSH 验收 |
| `go vet ./internal/core/tailscale ./internal/core/server` | PASS | S13 组件和 Core API 编译/静态检查 |
| `.github/workflows/s13-candidate.yml` | PENDING | 推送后在 GitHub Actions 创建隔离 OpenSSH target，并执行 gated real-SSH preflight、API 与 Web 检查；当前没有该远程 run 的结果 |
| `.github/workflows/s10-systemd-file-root.yml` | PENDING | 推送后在 GitHub Actions 尝试真实 systemd bind-mount 权限用例；若 runner 没有可控 system manager，会显式报告 `NOT_READY` |
| `go test -race ./internal/core/tailscale` | PASS | S13 package race 检查；该次未设置 Docker socket 环境变量，真实 Engine 用例由上方命令执行 |
| `pnpm typecheck` | PASS | 锁定 Node.js `22.23.3`、pnpm `12.10.1` |
| `pnpm build` | PASS | 生产 Web 静态资源构建 |
| `pnpm test:s13` | PASS，3/3 | Playwright 通过 Vite 页面和 stub API 验证节点分类、不自动部署、SSH 救援地址、指纹确认、fallback 关闭、凭据清空、成功/失败反馈、375/768/1440px 无横向溢出；server handler 另由上一行直接测试；不是 SSH 集成验收 |
| `go test ./...` | FAIL（既有跨阶段失败） | `internal/core/agents/TestMigrationVersionTwoExtendsAuditWithTypedTargets` 仍断言迁移版本 2，实际 schema version 为 4；`internal/core/server`、`internal/core/tailscale` 等其余已完成包测试通过。本候选未修改 storage migration |

`NODEDANCE_S13_TEST_DOCKER_SOCKET` 集成测试检查 socket 的数字 owner/group 和 mode，并由当前测试进程（已属于 Docker supplementary group）请求真实 Engine `/version`。它不声称已经验证新安装 systemd service 的真实身份；该证明仍需目标 SSH 环境。

## 正式验收仍为 `NOT_READY`

本地没有真实 Tailscale 登录环境、OpenSSH 服务或可切换 host key 节点；隔离 OpenSSH 的 Actions 工作流已添加但待运行。也没有受签名生产 Agent 产物或 amd64/arm64 被控机。以下规范用例在获得新鲜 Actions/目标系统证据前继续保持 `NOT_READY`：

- `S13-01`–`S13-02`：真实 CLI 登录状态、Tailnet 可见范围和节点分类。
- `S13-03`：组件测试验证改名/IP 变化不改变 identity；真实 Tailnet 节点变化及 Agent 偏好关联尚未执行。
- `S13-04`–`S13-05`：真实 OpenSSH 握手、错误凭据、host-key 验证由新增 Actions 测试覆盖，但当前 Actions 结果待运行；host-key 轮换及重新确认仍需独立可切 key 证据。
- `S13-06`–`S13-07`：签名生产包、双架构 Agent 安装、真实 SSH 安装中断后节点恢复和临时文件清理尚未由隔离 target 执行。真实 systemd 文件根权限另由新 gated workflow 验证，结果待首次 Actions run。
- `S13-08`–`S13-09`：真实目标机主备 HTTPS 可达性及证书验证/网络抓包。
- `S13-10`：真实部署后的连接信息、救援流程和数据库/服务日志秘密扫描。

进入正式验收前还需修复或解决全仓测试报告中提到的 migration 版本断言，执行 S13 真实 SSH/Tailscale 端到端流程，并与 S10 file-root/权限契约完成集成。正式 S13 只有逐项规定环境和证据均达成后才可改为 `PASS`。
