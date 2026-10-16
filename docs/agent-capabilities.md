# Agent 能力与 Komari 对照 / Agent capabilities

本次范围为 Linux Agent 能力，以及承载这些能力的 Core、Web、CLI、TUI。OAuth/OIDC、二步验证、主题和插件生态不在本次范围内。

调研依据为 [Komari Agent 源码](https://github.com/komari-monitor/komari-agent)、[Komari Core 源码](https://github.com/komari-monitor/komari)，比对快照分别为 `828afafe` 与 `13cbdca`，并检查了稳定版本 `1.5.11` 与 `1.5.1`。下面列出原有缺口的实现和入口；已经具备的 Docker、Compose、终端、文件管理、凭据轮换、通知渠道和更新回滚继续使用现有实现。上游仅声明类型、未找到执行实现的 traceroute/iperf3 不列为已实现的功能。

| 补齐的能力 | 实现及三端入口 |
| --- | --- |
| Swap、1/5/15 分钟负载、进程数、TCP/UDP socket 数 | `nodes.metrics` 的 `metrics.host`；Web 节点概览 |
| CPU 型号、物理核心、虚拟化类型、缓存/缓冲/原始内存 | 同上；内存统计支持 `available`、`raw`、`cached` |
| NVIDIA/AMD GPU 使用率、显存、温度 | NVIDIA 驱动的 `nvidia-smi`；AMD 的 sysfs；Web 概览、指标历史、指标告警 |
| 网卡 IPv4/IPv6、公网地址、自定义上报地址 | 自动读取网卡；公网查询需要配置 HTTPS 纯 IP 响应地址；支持独立 IPv4/IPv6 自定义值 |
| 累计流量、每月账期与自定义网卡筛选 | 私有持久化账本；Web 概览；`nodes.metrics` / `nodes.history` |
| 动态采样、挂载点、内存统计、DNS、IP 优先级、GPU、压缩、连接方式 | `agents.settings` / `agents.configure`；Web → 主机管理 → Agent 配置 |
| root 脚本及多主机批量执行 | `commands.submit/list/get/cancel`、`commands.batch/batches/batch-status/batch-cancel`；Web → 主机管理 → 批量脚本 |
| ICMP IPv4/IPv6、跨节点相同目标、延迟/失败/丢包/抖动统计 | `probes.create`、`probes.batch`、`probes.quality`；Web 节点探测与多节点探测 |
| 扩展指标、P95 延迟、探测失败率、ICMP 丢包率告警 | `metric`、`probe_latency`、`probe_failure`、`probe_packet_loss` 规则；Web 告警中心 |
| 费用、计费周期、到期与流量配额提醒 | `nodes.asset` / `nodes.save-asset`；`asset_expiry` / `traffic_quota` 告警规则；Web 注册命令生成、节点设置（删除节点之前）、主机管理与告警中心 |
| 共享注册密钥 | `agents.create-grant/grants/revoke-grant`；Web → 主机管理 → 批量注册；限时、限次数、可撤销，每台 Agent 独立凭据 |
| HTTP 回退与压缩 | `auto` 优先 WebSocket，握手失败后使用 HTTP；可明确选用 `http` 或 `websocket`；HTTP 使用帧序号、重试去重和轮询确认 |
| 自动更新与诊断 | `autoUpdate` 经现有更新控制服务执行；`agents.diagnostics` 返回有效配置并隐藏凭据 |
| 禁用远程终端与 root 脚本 | `disableRemoteControl`；立即拒绝新脚本并取消运行中的脚本，终端会在下次心跳重连后关闭 |

扩展指标采用新的 `agent.metrics.v2` 能力契约，控制请求采用 `agent.host.v1`。能力协商沿用统一协议，不注册旧指标契约或增加转换适配器。

Web 通用操作台、CLI 和 TUI 从连接的 Core `/api/v1/operations` 读取操作、请求结构、方法、路径和传输类型。专用页面通过同一契约调用 Core；CLI/TUI 不连接 Agent、不读取 Core 数据库。`metrics.catalog` 返回可用于扩展指标告警的指标及单位，历史图表自动显示收到的扩展序列。

## 操作示例

先通过现有 CLI 登录 Core。请求体可从私有 JSON 文件或 stdin 读取；用 `describe` 查看连接的 Core 的完整请求结构。

```sh
nodedance describe agents.configure
nodedance call agents.settings --params '{"nodeId":"NODE_UUID"}'
nodedance call agents.configure --params '{"nodeId":"NODE_UUID"}' --body @/private/agent-settings.json
nodedance call agents.diagnostics --params '{"nodeId":"NODE_UUID"}'
nodedance call metrics.catalog
nodedance call nodes.asset --params '{"nodeId":"NODE_UUID"}'
nodedance call nodes.save-asset --params '{"nodeId":"NODE_UUID"}' --body @/private/node-asset.json
nodedance call commands.batch --body @/private/batch-script.json --idempotency-key fleet-change-001
nodedance call commands.batch-status --params '{"batchId":"BATCH_ID"}'
nodedance call probes.batch --body @/private/batch-probe.json --idempotency-key fleet-probe-001
nodedance call probes.quality --params '{"id":"PROBE_UUID"}'
nodedance call agents.create-grant --body @/private/enrollment-grant.json
nodedance tui
```

TUI 搜索上述操作名，填写由 Core 提供的参数与请求体，提交后查看 JSON 结果。所有新增操作同样可在 Web 通用操作台调用。

批量脚本请求示例：

```json
{"nodeIds":["NODE_UUID"],"script":"uname -a\ndf -h","timeoutSeconds":60,"confirm":true}
```

多节点 ICMP 请求示例：

```json
{"nodeIds":["NODE_UUID"],"name":"gateway","kind":"icmp","target":"icmp://gateway.example","intervalSeconds":30,"timeoutSeconds":3,"enabled":true}
```

共享注册请求示例：

```json
{"displayPrefix":"edge","maxUses":10,"lifetimeSeconds":600,"coreUrl":"https://core.example"}
```

创建响应只展示一次密钥，并返回包含该密钥的原生和 Docker 安装命令。复制所选命令即可执行，无需另行输入注册凭据。生成命令不固定 tag，执行时自动查找最新正式 Agent release，通过 `sudo bash` 以 root 执行安装器。Docker 安装自动为注册和运行容器设置 `--user=0:0`、privileged、宿主机 PID／网络与目录挂载，无需手工追加 Docker 参数。到期或撤销共享密钥不会撤销已注册设备；管理已注册设备使用现有 Agent 撤销操作。

## 数据语义与运行边界

- 主机采样默认 5 秒，可设 1–60 秒；磁盘默认 30 秒，可设 5–300 秒。GPU 每 30 秒、公网地址每 5 分钟在独立任务采样。GPU/查询失败分别报告 unknown，不填零，不阻塞基础采样。磁盘挂载点筛选在执行 statfs 前生效。默认仅采集根目录和持久存储挂载点，排除 EFI、内存盘、内核伪文件系统及 Docker 运行时挂载；实际磁盘读取失败仍报告 unknown。显式选择挂载点可查看 EFI、tmpfs 等容量信息，内核伪文件系统始终排除。节点网络面板仅展示参与汇总的主接口（含 VPS 主网卡及 bond），默认隐藏回环、Docker 网桥、veth 和隧道接口；显式配置的接口仍遵循 Agent 筛选设置。B/s 取整数，KiB/s 及更大单位保留一位小数。
- `memoryMode=available` 使用 total − MemAvailable；`raw` 使用 gopsutil 原始 Used；`cached` 使用 total − Free。主内存指标的 availableBytes 是该策略下 total − used；`host.memory` 保留 Linux 实际 Available、原始 Used、Free、Cached、Buffers、Shared。
- 账期日为每月 UTC 1–28 日。账本持久化累计与本期观测增量。重启、网卡计数器重置、网卡身份变化、采样空档及跨账期的未分配边界区间会明确标记 `partial_coverage`。没有采样前的历史流量不会被推算。本期配额提醒按已观测流量触发，因此不完整覆盖的数值是下界。
- 流量配额支持 `sum`（上传+下载）、`max`、`min`、`up`、`down`。到期提前提醒天数和流量提醒百分比由资产设置控制；在告警中心创建相应规则、选择通知渠道。到期提醒可在节点离线时触发，离线流量保持 unknown。
- 质量查询默认最近一小时，可指定 RFC3339 `from/to`，最多 31 天和 100000 条结果。失败率以已完成的 healthy/unhealthy 为分母；离线和调度空档独立计为 unknown。ICMP 丢包率以成功 echo 与超时 echo 为分母，DNS/连接错误另列，不充当丢包。P95 使用成功探测延迟；抖动使用时间顺序上相邻成功延迟的平均绝对差。告警质量窗口为最近 15 分钟。
- 脚本最多 32 KiB、超时 1–3600 秒，每台 Agent 最多并行 4 个命令，stdout/stderr 合计保留 128 KiB。提交成功代表已受理，需查询终态。相同幂等键和请求只运行一次，键与不同请求组合被拒绝。Agent 重启时将未完成记录标为 interrupted，不重放脚本。脚本正文不写入 Core 审计；Core 仅持久化请求摘要和每节点结果摘要，完整输出由 Agent 保留。
- 终端和脚本禁用后仍是 root Agent。恢复开关在下一次心跳重连后重新协商终端能力。配置网络参数在下次连接生效；重连采用现有有界指数退避。TLS 校验、root 权限及文件保护保持现有要求，不提供跳过证书验证模式。
- state 目录必须 root-owned `0700`，所有 JSON 状态与凭据为 root-owned regular `0600`，拒绝符号链接及可替换的父目录。文件管理仍拒绝遍历、符号链接、Agent 状态/凭据路径以及保护的内核/运行时目录。
- 自动 Agent 更新默认关闭，依赖已安装并配置的更新控制服务。按 1–168 小时检查已发布 Agent tag，仍执行现有预检、备份、健康检查及回滚。新功能需要运行包含本次实现的 Core 与 Agent；已有服务不会因为修改源码自动升级。

## English overview

The added Linux Agent capabilities include extended host/GPU metrics, selectable memory accounting, interface/public/custom IP reporting, durable billing traffic, configurable sampling/networking/compression, root script jobs and batch execution, IPv4/IPv6 ICMP, shared multi-node probes, quality statistics and threshold alerts, asset expiry/traffic reminders, bounded shared enrollment, HTTP fallback, automatic Agent updates and redacted diagnostics.

Web, CLI and TUI discover the same Core operation contract. Use the commands above or the Web Host management screen; request schemas and authorization remain owned by Core and the shared protocol. Missing hardware, offline periods and incomplete traffic coverage stay explicitly unknown or partial. GPU and public address lookups run separately from the fast collector. Scripts require confirmation, have durable idempotency and bounded output, and are never replayed after a restart. `disableRemoteControl` stops root scripts and closes terminal sessions on the next heartbeat reconnect.

Enrollment grants expire, have a bounded use count and can be revoked; each enrolled Agent gets an independent device credential. Generated commands resolve the latest stable published `agent-v*` release at execution time. Runtime and private state remain root-only, TLS verification stays enabled, and the existing file protections remain enforced. Automatic updates continue to use the built-in product transaction with preflight, backup, health checks and rollback.

OAuth/OIDC, two-factor authentication, themes and plugins are excluded from this change. Windows/macOS Agents are outside NodeDance's Linux product scope. Apply the rebuilt Core/Agent or publish a release through the existing workflows to run these changes; editing this checkout does not upgrade deployed services.

Web batch command/probe forms share the generic operation intent store. An uncertain submission retains its retry key across reload or leaving the page; manually re-entering the same request reuses that key. Request bodies and scripts are not persisted. A successful submission clears its intent, so the next explicit submission can create new work.

Web 批量命令／探测表单共用通用操作的执行意图存储。结果不确定时，刷新或离开页面仍保留重试键；手动重新填写同一请求后复用原键，不保存脚本和请求正文。成功提交会清除该意图，下一次明确提交可执行新任务。

Web 的节点详情提供独立“服务探测”标签，集中管理目标、启停、结果和历史。未配置的总览不占用空面板；已配置时总览显示一次读取的简短摘要，可跳转到服务探测。切换其他标签停止界面列表轮询，不会停用 Core／Agent 已配置的探测任务。

## 注册与节点资产设置

生成 Agent 注册命令时，可填写每月流量额度（GB，1 GB = 10^9 字节）、购买日期、VPS 到期时间、续费价格及币种。周期支持年、月、小时（h）、分钟（min），现有自定义天数仍可使用。购买日期按日历日期记录（UTC 午夜存储），留空表示不记录；到期时间按浏览器本地时区输入，Core 保存 UTC；此字段与安装命令的十分钟注册凭据有效期分开。流量额度 0 表示不设额度，到期时间留空表示不设到期。流量统计周期及统计方式仍使用现有 Agent 配置和资产统计设置，价格是记录信息，不自动扣费或续费。

`agents.enrollment` 与 `nodes.renew-enrollment` 的共享请求可携带 `asset` 对象，使用 `nodes.save-asset` 的资产字段；校验、待注册节点、凭据与资产保存位于同一事务。安装和注册后仍保留这些信息。节点设置中的“流量与续费”表单位于删除节点之前，使用 `nodes.asset` / `nodes.save-asset`，离线节点也可编辑。CLI、TUI 的发现式操作界面提供相同请求和编辑能力；可用 `nodedance describe agents.enrollment` 或 `nodedance describe nodes.save-asset` 查看连接 Core 的字段和周期枚举。资产信息仅管理员可见，不进入游客投影。

SSH 复制命令通过现有 `agents.diagnostics` 共享操作自动取得连接信息，无需手填或外部聊天记忆。root Agent 读取实际 SSH 监听端点、包含 Include 文件的 sshd 配置、节点地址、本地登录账号／组和当前 SSH 会话；不读取密码、私钥或 Agent 凭据，不修改 SSH 或 Tailscale。优先使用唯一活动 SSH 账号，其次唯一普通登录账号或唯一允许账号；无法唯一选择时，命令省略用户名，使用 SSH 客户端自身的默认账号规则。手动保存的 SSH 主机、账号及端口是可选覆盖项。Web 的“复制 SSH”始终先打开共享账号选择对话框：预选已保存或已识别的账号，否则预选第一个可用账号；只有点击对话框内的“复制 SSH”动作才写入剪贴板，取消或 Escape 不复制；没有可用连接信息时先自动识别并短暂提示识别中，识别仍无候选时提示不可用而不是空对话框。CLI/TUI 可直接读取同一诊断操作的 `ssh` 结果及候选账号对应的 `commands`；Web 通过该操作采集连接信息后，按上述流程复制同一命令。SSH 监听仅允许本机访问或不可用时返回明确原因，不假定默认 22 端口。

Compose 项目删除接入共享 Task／TaskJournal：按精确项目上下文执行 down --remove-orphans，保留卷、镜像与配置，确认容器已撤下后报告 removed；命令失败或无法验证时报告不确定状态，不自动重放。

The host metric block also reports `diskIO.readBytesPerSecond` and `diskIO.writeBytesPerSecond` from whole hardware block-device counter deltas. The first sample, device-set changes and counter resets are unavailable intervals; they must not be rendered as zero throughput. Numeric history and alert keys are `disk.read_bytes_per_second` and `disk.write_bytes_per_second`. See [node overview](overview.md) for the live chart and runtime information.
