# 当前实现与历史兼容残留审查

日期：2026-10-13。按用户要求在 main 上直接清理，不提升产品、协议或能力版本。本记录只描述这次增量；此前工作区已有的修改继续保留。

## 范围与判断依据

对 Agent 配置及 root 安全入口、systemd 安装、Tailscale SSH 部署的调用链进行复核，并检查更新清单解析及协议/存储一致性判定、密码格式校验、备份格式校验、Core SQLite 初始化和 Agent 本地状态解码。全仓搜索历史兼容、迁移、旧格式、废弃入口和回退线索；每项删除都结合实际引用判断，搜索无匹配不代表整个项目不存在任何遗漏。

## 已清理

| 位置 | 残留及处理 | 当前行为 |
| --- | --- | --- |
| `internal/agent/config.go` | 删除 `Schema == 0` 自动补齐；修正一个省略 schema 的 Core 集成测试配置。读取原本就不接受 schema 0，此处是写入端的隐式格式修补。 | 创建、读取和更新使用明确的 `ConfigSchema`，不隐式升级格式。生产注册流程已经明确填写该值。 |
| `internal/agent/config.go` | 删除 `loadConfigForUID`、`checkPrivateDirectoryForUID`、`checkOwnerUID` 及随当前进程 UID 改变所有权策略的包装。 | `LoadConfig`、`SaveConfig` 明确要求 root，凭据目录和文件只接受 UID 0 所有权，继续要求 0700 / 0600 和既有路径防护。 |
| `internal/agent/systemd.go` | 删除无调用的 `validateSystemdConfig`，其任意服务 UID 参数是上述泛化所有权辅助函数仅存的额外消费者。 | 活跃安装流程仍使用其现有凭据备份、验证、准备及回滚逻辑；没有删除活跃的凭据保护。 |
| `internal/agent/systemd.go` | 删除账户解析函数已忽略的 unitPath 参数，以及传给文件根目录解析的无效 User 覆写副本；修正仍提及创建服务账户的注释。 | 账户只允许 root；文件根目录选择和持久化策略不变。 |
| `internal/core/tailscale/ssh.go` | 删除全仓没有调用的 `InstallRemote` / `InstallRemoteWithFileRoot` 旧包装。将完整实现 `InstallRemoteWithFileRootOptions` 收敛为唯一 `InstallRemote`，同步部署调用和现有测试。 | 唯一入口显式接收 fileRoot 与 disableFileRoot，保持完整安装参数及执行规则。 |
| `docs/komari-communication.md` | 移除“没有 HTTP polling 回退”的过时说明与一次性变更叙述。 | 文档描述当前已实现的 WebSocket 和带确认的 HTTP 通道。 |

## 审查后保留

- 更新清单的协议/存储哈希一致性要求、备份当前格式要求和密码版本要求都用于拒绝不符合当前契约的数据，不是旧版本适配。
- SQLite 当前表及索引初始化用于新库和同格式重开；未发现 ALTER TABLE 历史升级链。不会以清理历史为由删除当前初始化或重开能力。
- 容器重建后的偏好迁移是当前资源身份变更业务，属于活跃功能。
- HTTP 传输回退、Core 地址故障回退、Docker API 协商、可选能力协商，以及任务/凭据轮换恢复均属于当前功能。没有仅因名称包含 fallback、migration 或旧凭据而删除。
- 拒绝非 root、未知格式和未协商能力的安全测试继续保留；它们不提供退役模式支持。

## 验证

- `go test -race -exec 'sudo -n' ./...`：最终修改后全项目通过，包括真实 root Agent、Core 集成和恢复测试。日志：`/tmp/nodedance-compat-final.log`。
- `go vet ./...`：最终修改后通过。日志：`/tmp/nodedance-compat-final-vet.log`。
- 普通非 root 进程运行 `go test ./internal/agent -run TestAgentHostManagementEntrypointsRejectNonRoot -v`：通过，凭据读取、凭据写入及既有管理入口均被拒绝。
- 增补配置测试：明确当前 schema 可写入并读回；缺失或不正确 schema 在创建状态目录前被拒绝。
- 删除符号全仓引用复查及 `git diff --check`：通过。

本次没有修改 Web、Core 操作契约、浏览器管理员会话或游客投影，未重新运行浏览器审查。没有执行发布、修改真实机器或递增版本。
