# 剩余 69 文件深度审查与修复

本批完成上一轮报告列出的全部 69 个文件。沿用原 331 文件清单，累计 **331 / 331，待审查 0**。Go/JavaScript/测试按全文检查实现、错误路径、取消、恢复、资源与权限边界；四个 JSON 资源另按完整结构、全部键值约束、占位符/许可与调用关系检查，并核对安全和恢复用词，不将其称为逐句人工翻译校对。此前另列的 55 个文档、锁文件及资源不包含在本代码覆盖数中。

直接在 main 修复，保留此前工作区修改；未创建分支、提交/推送、升级版本、发布或更新生产机器。原清单之外新增的回归文件不用于提高 331 文件覆盖数。完成覆盖不等于代码无缺陷，也不等于长期运行、amd64 发布或正式流水线耗时已有实测。

## 本批确认并修复

| 范围 | 实际问题 | 修复与证据 |
| --- | --- | --- |
| 单节点指标/探测/部署查询 | 获取一个节点时反复加载、解码全节点列表；仪表盘逐节点刷新重复这些工作。 | 指标及节点存在性按主键读取；部署在线/Docker等待共用当前 lease 和视图，拒绝未来 heartbeat、过期和零代次。新 lease 表驱动回归。 |
| Docker 视图 | 每次读取单节点视图时再遍历所有节点，检查过期 stage。 | 只检查当前节点；保留周期性的全局清理、原有 TTL 与失效原因。 |
| Docker pending queue | 每取一个 ID 都分配完整切片并排序。 | 无分配线性选择最小 ID，保留原稳定顺序和队列容量。 |
| SMTP/Webhook | TCP建立后阻塞 greeting 不响应取消；自定义 HTTP client 可绕过 Sender.Timeout。 | 总发送上下文有界，SMTP取消关闭已建立连接。两个回归修改前均失败，修改后 race 通过。 |
| 通知维护窗口 | 已排队通知在后来新增维护窗口后仍能被 claim 发出；持续抑制每次采样重复写事件。 | claim 前重新检查窗口/确认/离线抑制，抑制不消耗发送次数；只记录抑制状态转换。真实 SQLite 回归。 |
| Compose 文件写入 | 两处局部 err 遮蔽，write/fsync失败可能被报告成功；特殊FIFO可阻塞读取。 | 使用实际错误变量、nofollow+nonblock有界读取；/dev/full确定性失败回归。保存失败回滚先确认当前文件仍是本次写入内容，保护并发编辑。 |
| Compose 恢复协议 | Agent实际产生的 recovery_required/baseline_cleanup_failed 被公共结果校验拒绝。 | 当前契约补入该实际错误码；两种cleanup结果可报告，任意错误码仍拒绝。未改能力版本。 |
| 容器重建 | OperationTimeout只校验而未用于正向执行，回滚多次WithoutCancel没有总截止；空创建ID可能误认为成功。 | 正向执行及独立回滚各有总截止；拒绝无完整ID的创建结果，停止容器的重建结果保留stopped。阻塞Engine超时回归。 |
| 重建SQLite sidecar | 私有目录内预存的 -wal/-shm symlink可能在应用检查前被SQLite接触。 | 初始化SQLite前检查，打开使用nofollow/nonblock；两个sidecar符号链接回归确认目标内容不变。 |
| SSH | 握手、创建session/发送安装包发生在取消select前，沉默对端/不读取stdin可阻塞。 | 取消关闭SSH连接，覆盖握手和8MiB发送受flow control阻塞场景；发送中断仍标记结果未知。 |
| SSH shell预检 | 无Docker或非socket分支的echo docker\|absent/invalid会被shell当成pipeline。 | 引号保护字符串；真正执行两个隔离shell分支回归。 |
| Tailscale资源 | Lstat检查后重新无界读文件，signature无读取限制；状态保存缺父目录fsync。 | 同一nofollow descriptor验证/有界读取，signature 4KiB/state 4MiB，读写对称上限及rename后目录fsync；有效签名/超限/symlink/FIFO/state回归。 |
| Core消息解析 | image decoder重复公共实现；update与terminal解码未采用同一完整JSON边界。 | 复用公共有界严格decoder，拒绝未知字段/尾随JSON。真实WebSocket回归确认非法输入未转发Agent。 |
| systemd | 配置读取/decode循环重复；持久file policy绕过现有有界unit reader。 | 复用当前descriptor snapshot与nofollow有界reader，保留root、0700/0600、hardlink拒绝、unit fingerprint与Core reconnect证据。 |
| 浏览器设计审查 | 文件页仍使用退役UI选择器；导航序号漏了Guest display，Settings定位错误；普通更新断言仍要求已移除的额外确认。 | 实际VueFinder对话框、双语aria-label导航、当前普通更新契约；不删除guest/file安全脚本。 |

## 验证

使用临时数据库、隔离真实Core/root Agent、协议模拟Agent和临时shell fixture，不连接生产或真实远程管理资源。

- fresh Web typecheck/build、1461双语消息检查、96包file license notices检查通过；保留构建器500kB chunk提示，未通过调整阈值隐藏。
- 新资产的guest安全浏览器套件通过：443项，0失败；109个实际发现的私有操作、240并发刷新及20个light/dark/mobile截图。数字是本次输出，不是脚本固定覆盖目标。
- 新资产的真实Core/root Agent文件套件通过：完整8MiB编辑/CLI读回、分页搜索、操作恢复、冲突、被动预览、实际VueFinder创建/编辑/上传和四种布局。
- 真实Core/Web接口检查通过：121个发现操作；24布局，另12个上一批回归布局；普通更新、rollback确认、稳定重试、项目隔离/撤销/生命周期均保留。
- 完整Web设计矩阵通过：611张双语、light/dark及1440/390/320宽度截图，包含当前VueFinder对话框、导航、更新、终端和加载/不可用状态。
- `go test -race -exec 'sudo -n' ./...`、`go vet ./...`及`git diff --check`全部通过；新增SMTP/Webhook取消、通知窗口、SSH握手/8MiB发送、sidecar拒绝、重建总超时、lease/恢复协议和真实终端非法帧回归通过。

## 逐文件结论

| 文件 | 结论 |
| --- | --- |
| `internal/agent/compose/manager.go` | 基线 journal 与私有 cache 共用当前实现；manager 生命周期和缓存按项目 scope。 |
| `internal/agent/compose/manager_test.go` | 验证持久 Compose 基线，权限与文件边界不回退旧模式。 |
| `internal/agent/compose/operations.go` | 全文审查创建归属、恢复/重部署证据和配置安全：修复两处write/fsync错误变量遮蔽、FIFO阻塞及保存失败rollback覆盖其他编辑者的新内容。 |
| `internal/agent/compose/task_executor.go` | 部署、停止和删除逐项验证恢复证据；待核验 baseline_cleanup_failed 的协议表达。 |
| `internal/agent/containeractions/actions.go` | 执行前幂等与资源锁，持久 mutation 基线；重启和创建恢复仅做观察，不重放写操作。 |
| `internal/agent/containeractions/engine.go` | Docker 修改严格使用完整 ID，创建标签与 digest 的双重关联，close 与上下文传递完整。 |
| `internal/agent/containerrebuild/docker_engine.go` | 完整 ID 精确查询，备份归属验证和 Docker context 贯穿，避免模糊名字修改。 |
| `internal/agent/containerrebuild/manager.go` | 检查全部正向阶段、补偿恢复、snapshot/网络/端口/标签归属；补实际执行和独立回滚总超时，拒绝空 create ID。 |
| `internal/agent/containerrebuild/store.go` | 完整 WAL/FULL、阶段记录、cleanup 绑定与Spec conflict检查；修复 sidecar symlink 在SQLite初始化前拒绝，nofollow/nonblock descriptor检查保留私有regular约束。 |
| `internal/agent/containerstreams/stats.go` | 流共享单读取，最新队列、单次释放、JSON 结构界限与 monotonic gap；无证据的数值保留 unknown，不改成零。 |
| `internal/agent/docker/discovery.go` | 五 worker 联合关闭，事件/扫描 revision 防旧快照覆盖；pending queue 取最小 ID 改为无分配线性选择，保留稳定顺序。 |
| `internal/agent/docker/engine.go` | 只读 SDK transport 与完整 inspect projection，健康/动态port未知不假报；Compose override 标签必须验证 canonical ref。 |
| `internal/agent/systemd.go` | 全文检查 descriptor 权限、root身份、unit shadow 和 rollback fingerprint，持久 file policy 复用有界 nofollow reader；去重配置 decode/read循环。 |
| `internal/agent/systemd_test.go` | 全部 root权限、symlink/hardlink、并发 unit 替换、失败补偿、Core 新代次 reconnect 测试均检查；临时模拟 systemctl 不操作真实服务。 |
| `internal/agent/taskjournal/journal.go` | 完整 WAL/FULL+flock、scope/digest/claim、执行前commit、bounded consistent snapshot、Compose immutable基线和恢复未知流程；不删除必需的独立证据。 |
| `internal/agent/taskjournal/recovery_test.go` | 启动恢复不制造送达证据、不自动重放，并保留未解决资源 claim；Compose 基线不可覆盖。 |
| `internal/core/alerts/delivery.go` | SMTP 的取消不能打断无截止上下文中的阻塞 greeting，Webhook 自定义 client 也不遵守 Sender.Timeout；补取消回归修复。 |
| `internal/core/alerts/model.go` | 完整检查规则互斥、阈值、渠道 TLS 和秘密边界，DTO 保留当前单一契约。 |
| `internal/core/alerts/security_test.go` | SMTP 必须明确 TLS 模式且空模式连接前拒绝，保留安全断言。 |
| `internal/core/alerts/store.go` | 加密secret与投递有限重试、规则状态/窗口/撤销全检查；修复维护窗口重复 suppressed event、排队后新增窗口在发送前未再检查。 |
| `internal/core/alerts/template.go` | 仅允许固定字段和单次替换，拒绝不配对/未知模板，避免注入模板执行。 |
| `internal/core/docker/store.go` | 完整stage/增量/代次/lease/clone检查；单节点视图只过期自身stage，避免对每节点再扫描全表；保留独立拷贝防引用泄漏。 |
| `internal/core/probes/store.go` | revision/generation/run identity、迟到只写历史、离线去重/间隔和软删历史检查；ExpirePending 同查询读取 checked_at 消除每秒逐条重复查询。 |
| `internal/core/probes/store_integration_test.go` | 实际 HTTP 失败经 Agent 执行进入 Core 历史与连续失败状态。 |
| `internal/core/server/files_transport.go` | 锁序和关闭/撤销同步，序列、代次、允许 operation 与四槽有界；取消排队失败强制断开避免临时文件遗留。 |
| `internal/core/server/grant_api.go` | 授权操作只在已认证入口下调用，先验证 installer 再创建 token，撤销和只读列表检查。 |
| `internal/core/server/guest_api_test.go` | 完整真实Core匿名/管理员相同投影、默认opt-in、直接私有API拒绝、缓存revision、删除/撤销即时撤回及加权history测试；保留断言。 |
| `internal/core/server/host_api.go` | 有界 host waiter、代次关联、15 秒取消、单次幂等命令和共享设置校验。 |
| `internal/core/server/host_batch.go` | 最多64节点/4并发，稳定派生子任务 identity；丢响应后仅查询恢复，不再次执行命令。 |
| `internal/core/server/host_features_test.go` | 并发限额 enrollment、HTTP poll replay、防重复命令、Core重启批次恢复、root实际命令/metrics与审计检查。 |
| `internal/core/server/host_retention_test.go` | 验证只清理完成批次，保留部分、未知、中断和陈旧执行记录。 |
| `internal/core/server/images_api.go` | 一次凭证清零和派发锁序、幂等 gated acceptance、准确 page；移除独立 JSON decoder 复用公共有界严格解析。 |
| `internal/core/server/metrics_dashboard.go` | 发现每次节点指标读取重新加载全节点列表，导致仪表盘随节点数二次增长；待改节点定向查询。 |
| `internal/core/server/node_asset.go` | 节点作用域、资产校验、事务审计、uint64 流量和未知指标语义完整。 |
| `internal/core/server/private_api.go` | 完整认证后路由、密码 compare-and-swap、事务审计/撤销及终端文件 stream 收回；图片先限定像素再解码成 PNG。 |
| `internal/core/server/probe_api.go` | 共享配置验证、revision 并发保护、ownership 不可改；节点存在性查询改为主键查询以消除重复全列表。 |
| `internal/core/server/rebuild_api.go` | 规划只允许新鲜库存且准确目标，worker waiter 有界，响应目标重新核验。 |
| `internal/core/server/resource_limits_test.go` | 真实 TCP 慢请求释放容量与饱和情况下 health 独立检查。 |
| `internal/core/server/server.go` | 全初始化/关闭、共享catalog、读超时/容量、认证/Origin/CSRF 和 guest公开边界均检查；后台失败保留未知不阻塞管理。 |
| `internal/core/server/tailscale_api.go` | 部署和 resolve 共用任务契约；等待在线/Docker 改复用准确 lease 查询，拒绝未来 heartbeat 与非在线状态，不重复全列表。 |
| `internal/core/server/tailscale_api_test.go` | 部署resolve状态、关联失败不能成功、能力降级保留socket、损坏Docker历史不覆写、根Agent失败隔离与unknown刷新全检查。 |
| `internal/core/server/tailscale_host_test.go` | 部署宿主固定只读 RPC 与错误保留，不静默退回不同宿主环境。 |
| `internal/core/server/task_api.go` | 确认准确目标/当前库存，单次 secret payload 与 journal generation 绑定；断线不自动重放；任务/audit 列表有界。 |
| `internal/core/server/task_bridge.go` | snapshot 完整/唯一/有界与单代次 reconcile marker，task capacity 与终态释放完整；重新连接按持久结果恢复。 |
| `internal/core/server/terminal_stream.go` | 单次 ticket、tmux detach/resume ownership 和会话撤销同步；浏览器 frame 改公共严格 decoder 拒绝 trailing JSON。 |
| `internal/core/server/terminal_stream_test.go` | 真实 WebSocket 断线只 detach，同 stream 重附不输出 replay；跨会话/撤销/expiry 拒绝完整。新增实际WS尾随JSON回归，非法input不转发Agent。 |
| `internal/core/server/updates_api.go` | 当前组件、节点和确认约束，局部/Agent 路由及有界 waiter；响应解码可复用严格公共 decoder。 |
| `internal/core/server/updates_api_test.go` | 终端更新目标、支持平台和明确确认断言完整。 |
| `internal/core/server/websocket.go` | 会话撤销检查、Origin 拒绝、推送指纹和订阅释放完整，单节点读取成本依赖指标状态查询。 |
| `internal/core/tailscale/artifacts.go` | 签名验证使用固定架构文件，发现检查大小后无界重新读取和无界签名读取；需用已打开文件的有界读取。 |
| `internal/core/tailscale/deploy.go` | 只使用已有 Tailscale 发现；远程安装前 durable acceptance、稳定peer单执行，秘密不持久化，失败未知不自动重放。 |
| `internal/core/tailscale/deploy_stage6_test.go` | 全部远程shell和root state fixture、并发Coordinator、未知claim、preflight/rollback副作用约束审查；仅临时目录/模拟systemctl。 |
| `internal/core/tailscale/ssh.go` | 检查完整真实 SSH 和 shell 安装/rollback；修复握手及 stdin 发送取消、无 Docker 分支误把 \| 作为 pipeline；固定 fingerprint/TLS、root 服务与临时资源归属保留。 |
| `internal/core/tailscale/state.go` | 私有原子状态和旧任务不删除新关联；目录 fsync 与读取资源边界仍需加固。 |
| `internal/core/tailscale/status.go` | 只读解析 CLI 和 LocalAPI 状态，稳定 identity 与已有 Core 关联，不安装或更改 Tailscale。 |
| `internal/core/tailscale/status_test.go` | 仅查询既有 CLI/LocalAPI，区分登录失败、空 tailnet 和不可用，无配置操作。 |
| `internal/core/tasks/local_test.go` | 当前 Core 本地部署不可被 Agent claim，接受和启动事务失败可同键重试；人工确认才释放未知资源。 |
| `internal/core/tasks/tasks.go` | 全文检查接受/投递事务、幂等冲突、代次/lease/journal、丢失结果未知与人工证据、分页及 audit；共用 protocol 恢复校验自动包含当前 cleanup failure，不新增兼容层。 |
| `internal/i18n/errors.go` | 有界消息目录构建模板，只翻译展示错误并保留动态参数；不翻译任意原始输出。 |
| `internal/i18n/i18n.go` | 完整检查 locale 优先级、-- 之后不解析、透传 writer 和 errors.Is/As 语义。 |
| `internal/i18n/i18n_test.go` | 验证语言优先级、opaque 参数、格式槽及原始终端输出不被转换。 |
| `internal/i18n/zh-CN.json` | 全量结构/重复key/非空/printf槽及控制字符检查，结合CLI/TUI使用与安全恢复文案核对；不对任意用户输出翻译。 |
| `internal/update/releases_test.go` | 稳定组件 namespace、prerelease 排除及真实摘要匹配/损坏拒绝验证。 |
| `internal/update/tailscale_test.go` | 只读宿主状态 RPC 的准确输入、超时、无私有用户资料和失败语义检查。 |
| `web/scripts/audit-design.cjs` | 全文审查且更新旧文件页为实际VueFinder delete/rename/editor dialogs；导航按当前双语aria-label定位，普通更新验证当前direct apply契约，保留rollback确认验证于interface suite。 |
| `web/scripts/file-license-notices.json` | 全文license notices检查并对锁定依赖运行file-notices --check，保留MIT/ISC完整许可与copyright。 |
| `web/src/locales/en.json` | 全量key/插值/空值/字符与实际源码key检查；错误、8MiB与root当前契约用词检查，模板双花括号保留。 |
| `web/src/locales/zh-CN.json` | 全量与英文key/插值对应，权限、未知状态、恢复/8MiB说明核对；只翻译presentation不改变操作校验。 |
| `web/tsconfig.json` | 严格 TS 和 bundler 解析设置，涵盖 Vue/TS/Vite；保留无输出类型检查。 |
