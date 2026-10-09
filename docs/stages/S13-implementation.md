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
- systemd 保持 `ProtectSystem=strict`、`ProtectHome=true`，仅允许 Agent 私有状态目录写入。这样不会为迁就文件操作而开放根文件系统写权限。

## S10 文件访问的待集成限制

当前 systemd 沙箱与 S10 主机文件服务契约尚未对齐：`ProtectSystem=strict` 只允许写 `/var/lib/nodedance-agent`，S10 文件上传、编辑和创建无法通过本服务单元写宿主机目标；`ProtectHome=true` 会隐藏 `/home`、`/root`、`/run/user`，文件浏览无法列出这些常见路径。维持安全默认。待 S10 明确 file-root、读取范围及写入授权后，再通过精确 `ReadOnlyPaths`/`ReadWritePaths` 集成；不能以无边界开放 `/` 作为修复。

## 已执行的候选测试

| 检查 | 实际结果 | 范围 |
|---|---|---|
| `NODEDANCE_S13_TEST_DOCKER_SOCKET=/var/run/docker.sock go test ./internal/core/tailscale` | PASS | 封闭组件单测，并使用本机真实 Docker socket |
| `go test ./internal/core/server -run TestTailscale` | PASS | 实际 Core HTTP handler 的发现 JSON、私有 Session、no-store 与部署接口 Origin/CSRF 门槛；Tailscale CLI 通过测试 fixture 注入 |
| `NODEDANCE_S13_TEST_DOCKER_SOCKET=/var/run/docker.sock go test -v ./internal/core/tailscale -run 'Test(RealDockerEngineConnectsWithAuthorizedSupplementaryGroup\|FreshRemoteWithoutAgentUserUsesSupplementaryDockerGroup\|InstallRefusesDockerSocketThatCannotBeSafelyAuthorized\|DockerSocketOwnerAccessRequiresExistingMatchingServiceUID)$'` | PASS；Docker Engine `29.7.2` | 新节点 `UID=-1` 解析、systemd supplementary GID、无安全授权时拒绝、当前进程通过已授权附加 GID 连接 `/version`。此项没有安装 systemd Agent，也不替代远端 SSH 验收 |
| `go vet ./internal/core/tailscale ./internal/core/server` | PASS | S13 组件和 Core API 编译/静态检查 |
| `.github/workflows/s13-candidate.yml` | PENDING | 推送后在 GitHub Actions 创建隔离 OpenSSH target，并执行 gated real-SSH preflight、API 与 Web 检查；当前没有该远程 run 的结果 |
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
- `S13-06`–`S13-07`：签名生产包、双架构 Agent 安装、真实 systemd 身份、安装中断后节点恢复和临时文件清理尚未由隔离 target 执行。
- `S13-08`–`S13-09`：真实目标机主备 HTTPS 可达性及证书验证/网络抓包。
- `S13-10`：真实部署后的连接信息、救援流程和数据库/服务日志秘密扫描。

进入正式验收前还需修复或解决全仓测试报告中提到的 migration 版本断言，执行 S13 真实 SSH/Tailscale 端到端流程，并与 S10 file-root/权限契约完成集成。正式 S13 只有逐项规定环境和证据均达成后才可改为 `PASS`。
