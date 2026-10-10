# NodeDance 强制执行开发计划与审查规则

仓库：`CST-Cat/NodeDance`
基准提交：`a17c72cc`
唯一分支：`main`

## 一、绝对执行规则

1. 全部阶段严格串行，前一阶段未 PASS，不得开发下一阶段。
2. Luna Work 负责完成全部代码修改、删除、新建、编译和功能检查。
3. 审查 Agent 逐项核对全部交付要求。缺失任意一项，直接 REJECT。
4. REJECT 必须返回同一个 Luna Work 会话继续返工，直至 PASS。
5. 所有代码直接在 `main` 修改和提交，禁止新建任何 branch。
6. 已存在的正确实现直接复用；重复实现直接删除，禁止保留两套并行机制。
7. 禁止为了代码组织、未来扩展和测试便利建设与实际业务无关的框架。
8. 禁止新建测试脚手架、测试夹具管理系统、验收报告生成器和旧阶段追踪体系。
9. 每个大模块全部开发完毕后，集中进行一次模块验收；失败后只修复并复验实际问题。
10. 不能使用虚构数据、空函数、假成功响应、未接通的前端按钮或待实现占位代码冒充功能完成。
11. 全部基础开发阶段完成之后，用户再提供 VPS。此前禁止以安装其他 VPS 的 Agent 为前提阻塞开发。
12. 本计划列出的功能属于强制交付范围，不得擅自删减、推迟或改成演示功能。

## 二、阶段 0：基础工程与错误架构修正

必须修改的文件：

| 文件 | 强制任务 |
| --- | --- |
| `Makefile` | 修复完整构建依赖，使 Web 构建、Go Core 和 Go Agent 编译形成正确顺序 |
| `web/package.json` | 确保 Vue 类型检查和正式构建命令工作正常 |
| `internal/core/webassets/assets.go` | 保持 Web 资源正确嵌入 Core |
| `internal/core/server/server.go` | 将鉴权、Agent 通信、主机监控、Docker 与告警、探测等附加能力解除错误启动依赖 |
| `internal/core/server/alerts_scheduler.go` | 告警调度失败不得阻断 Core 基础管理 |
| `internal/agent/task_bridge.go` | 基础容器启停不得被 `rebuilds.sqlite` 初始化失败阻断 |
| `internal/agent/runtime.go` | 正确组织指标、Docker、任务及附加模块的生命周期，真实报告功能可用状态 |
| `docs/architecture.md` | 更新实际运行架构 |
| `docs/plan.md` | 完全替换旧阶段安排，采用本计划 |

新建文件：无。

删除内容：删除本次修正后已经无引用的旧函数、字段、导入和错误启动路径；禁止保留新旧两种模块初始化逻辑。

阶段验收：

- `make build` 成功生成 Core 和 Agent 可执行文件。
- `make check` 完成现有前端检查和 Go 检查。
- Core 正常启动并提供 Web。
- Agent 主机监控不依赖 Docker Engine 存在。
- 重建存储故障不会使基础容器任务执行器一并失效。
- 告警、探测的独立故障不会令正常的主机监控服务退出。
- 所有删除的代码均无残留调用。

强制 REJECT：构建失败、残留死引用、重复初始化机制、严重数据库错误被吞掉、基础功能继续依赖高级功能，全部驳回。

完成后立即进入阶段 1。

## 三、阶段 1：MVP——完整主控、Agent、监控、Docker 基础管理

### Core-Agent

修改并完成：

- `cmd/nodedance/main.go`
- `cmd/nodedance-agent/main.go`
- `internal/core/cli/run.go`
- `internal/core/server/auth_api.go`
- `internal/core/server/security.go`
- `internal/core/server/agent_api.go`
- `internal/core/server/agent_websocket.go`
- `internal/core/agents/repository.go`
- `internal/agent/config.go`
- `internal/agent/registration.go`
- `internal/agent/runtime.go`
- `internal/agent/metrics/`
- `internal/protocol/metrics.go`
- `internal/protocol/version.go`

必须完成的功能：

- 单管理员首次初始化、密码登录、Session、退出。
- 未登录不能访问管理数据及写操作。
- Agent 注册、设备凭据持久化、撤销。
- Agent 主动 WSS 连接 Core。
- 心跳、断线判断、自动重连。
- CPU、内存、磁盘、网络、运行时间的真实上报。
- Docker 不存在或无法访问时，主机监控继续工作。
- 一个 Core 能独立登记和管理多个 Agent。
- Core 与 Agent 能在同一 Linux 主机运行。

### Docker 基础管理

修改并完成：

- `internal/agent/docker/engine.go`
- `internal/agent/docker/discovery.go`
- `internal/agent/docker/observer.go`
- `internal/agent/docker/model.go`
- `internal/agent/docker/protocol.go`
- `internal/agent/docker_runtime.go`
- `internal/agent/containeractions/actions.go`
- `internal/agent/containeractions/engine.go`
- `internal/agent/taskrunner/runner.go`
- `internal/core/docker/store.go`
- `internal/core/server/docker_api.go`
- `internal/core/server/docker_integration.go`
- `internal/core/server/task_api.go`
- `internal/core/server/task_bridge.go`
- `internal/protocol/docker.go`
- `internal/protocol/tasks.go`

必须完成的功能：

- 自动发现所有现存 Docker 容器，包括停止容器。
- 同时发现独立容器和 Compose 容器。
- Docker Events 与定期全量校正。
- 展示名称、镜像、状态、Healthcheck、创建与运行时间。
- 完整展示宿主机 IP、端口、容器端口、TCP/UDP、IPv4/IPv6。
- 正确区分未发布端口、回环绑定和 Host Network。
- 容器启动、停止、重启。
- 操作绑定真实节点 ID 和容器 ID。
- 执行结果由 Agent 返回，并通过 Docker 状态核实。
- Agent 失联时，所有历史 Docker 状态标记过期。

### Web MVP

修改：

- `web/src/App.vue`
- `web/src/components/NodesDashboard.vue`
- `web/src/components/MetricsPanel.vue`
- `web/src/api.ts`
- `web/src/metrics-contract.ts`
- `web/src/style.css`

新建：`web/src/components/VpsCard.vue`

必须完成：

- 登录后直接展示所有 VPS 的监控卡片。
- 每张 VPS 卡片独立显示 CPU、内存、磁盘、网络及运行时间。
- 每张卡片直接展示所属 Docker 容器。
- 每个常用容器展示状态、IP、端口、健康状态和运行时间。
- 首页容器的启动、停止、重启按钮真实可用。
- 容器置顶、隐藏、展示数量正确生效。
- 首页操作不会错误引用 `selectedNodeID`。
- Agent 离线及 Docker 不可用状态准确显示。

删除：删除从 `NodesDashboard.vue` 抽取卡片后留下的重复模板、函数、样式和无效状态。

阶段验收：

- Core、Agent、Web 全部构建通过。
- 本地 Core 完成初始化、登录和退出。
- 本地 Agent 与 Core 真实通信并上报指标。
- 未登录管理请求被拒绝。
- Docker 不可用时主机监控持续正常。
- 全部 Docker 业务请求从 Web 贯通到 Core、Agent 和 Docker SDK。
- VPS 卡片数据、操作目标及偏好正确。
- 无虚构运行状态、假成功和空业务入口。

强制 REJECT：任一通信链路断裂、鉴权绕过、错误节点操作、假数据、假成功、监控与 Docker 异常耦合，全部驳回。

## 四、阶段 2：Komari 式监控首页与个性化

修改：

- `web/src/components/NodesDashboard.vue`
- `web/src/components/VpsCard.vue`
- `web/src/components/MetricsPanel.vue`
- `web/src/components/HistoricalMetrics.vue`
- `web/src/components/PreferenceEditor.vue`
- `web/src/style.css`
- `internal/core/dashboard/preferences.go`
- `internal/core/containerprefs/sqlite.go`
- `internal/core/server/dashboard_api.go`
- `internal/core/metrics/store.go`
- `internal/core/history/store.go`
- `internal/core/server/metrics_dashboard.go`

新建：`web/src/components/NodeDetail.vue`

必须完成：

1. 多 VPS 首页与单 VPS 详情彻底分离。
2. 多节点搜索、分组、筛选、排序。
3. 实时指标与 CPU、内存、磁盘、网络历史曲线。
4. 历史时间范围选择。
5. 节点别名、图标、备注、分组。
6. Docker 容器别名、图标、置顶、隐藏、拖动排序。
7. 首页容器展示数量自定义。
8. 全部设置持久化到 SQLite。
9. 刷新页面和重启 Core 后保留设置。
10. 桌面、平板、手机响应式界面全部完成。
11. Dashboard 与 NodeDetail 使用同一份节点数据，不维护重复业务状态。

删除：删除原 `NodesDashboard.vue` 中已经迁往 `NodeDetail.vue` 的重复详情逻辑和无效状态。

阶段验收：全部展示设置真实生效、刷新及重启后持久化、历史指标与实际存储一致、响应式布局正常、节点和容器身份映射正确。

强制 REJECT：设置只在前端有效、历史数据伪造、容器偏好错误继承、首页与详情重复维护业务状态、响应式布局无法正常操作，全部驳回。

## 五、阶段 3：1Panel 式 Docker 完整管理

修改：

- `internal/protocol/tasks.go`
- `internal/taskstate/state.go`
- `internal/core/server/task_api.go`
- `internal/core/tasks/tasks.go`
- `internal/agent/containeractions/actions.go`
- `internal/agent/containeractions/engine.go`
- `internal/agent/taskrunner/runner.go`
- `internal/agent/containerstreams/`
- `internal/agent/images/`
- `internal/core/server/container_streams.go`
- `internal/core/server/images_api.go`
- `web/src/api.ts`
- `web/src/components/NodeDetail.vue`
- `web/src/components/ContainerStreams.vue`
- `web/src/components/ImagesPanel.vue`

新建：`web/src/components/ContainerCreateForm.vue`

必须完成：

1. Web 直接创建 Docker 容器。
2. 创建参数包括镜像、名称、命令、环境变量、端口、挂载、网络、重启策略。
3. 创建任务使用现有 Core Task 与 Agent TaskJournal。
4. 创建成功后取得真实 Container ID。
5. 启动、停止、重启、暂停、恢复。
6. Docker 真实重命名。
7. 删除容器及危险操作确认。
8. 容器日志查看与实时跟随。
9. 容器 CPU、内存、网络统计。
10. 镜像列举、拉取、删除。
11. Docker CLI 外部变化自动同步。
12. 容器运行状态与应用健康状态分别展示。
13. 全部功能进入 NodeDetail 正式页面，不允许存在不可到达组件。

删除：删除重复参数转换、已经被统一任务机制替代的操作入口及无引用函数。

阶段验收：完整审查创建、发现、生命周期、镜像和日志的真实调用链；使用本地可运行的 Docker 执行业务检查。所有未进行的远程实机操作统一列入最终实机验收，不能报告已执行。

强制 REJECT：创建容器假成功、错误节点或容器、错误 Container ID、重复任务引擎、第二 Docker Client、镜像误删、Web 功能不可进入，全部驳回。

## 六、阶段 4：1Panel 式 Compose 与容器配置管理

修改：

- `internal/agent/compose/manager.go`
- `internal/agent/compose/operations.go`
- `internal/agent/compose/task_executor.go`
- `internal/agent/compose/bridge.go`
- `internal/core/compose/store.go`
- `internal/core/server/compose_api.go`
- `internal/core/server/compose_tasks_api.go`
- `internal/protocol/compose.go`
- `internal/protocol/tasks.go`
- `internal/agent/containerrebuild/`
- `internal/core/server/rebuild_api.go`
- `web/src/components/ComposeProjects.vue`
- `web/src/components/ComposeEditor.vue`
- `web/src/components/ContainerRebuildWizard.vue`
- `web/src/compose-api.ts`
- `web/src/components/NodeDetail.vue`

新建：`web/src/components/ComposeCreateForm.vue`

必须完成：

1. 识别现有 Compose 项目及对应容器。
2. 区分 Docker 容器、Compose 项目登记和配置文件。
3. 从 Web 创建新 Compose 项目。
4. 选择节点、项目名称、工作目录和 YAML。
5. Agent 验证目录与配置。
6. 执行真实 `docker compose config`。
7. 保存 YAML，执行 `docker compose up -d`。
8. 项目启动、停止、重启及重新部署。
9. Compose 配置读取、编辑、保存。
10. 修改 Compose 发布端口并重新创建受影响容器。
11. 独立容器配置变更与受控重建。
12. 保留卷、网络、环境变量和其他必要配置。
13. 失败时明确结果并执行必要恢复。
14. 新创建容器由现有 Docker Discoverer 同步。
15. Compose 页面与容器重建页面均有正式导航入口。

删除：删除已经被通用 Task 覆盖的重复任务记录、操作状态和调用路径。

阶段验收：全部 Compose 和容器配置业务链路贯通；配置校验、文件保存、部署结果核实和失败恢复逻辑正确。真实端口修改和容器替换在最终 Docker 实机验收中全部执行。

强制 REJECT：配置只写数据库、端口只改展示、错误目录操作、重复 Compose 任务引擎、重建导致数据丢失、失败冒充成功、无正式页面入口，全部驳回。

## 七、阶段 5：XPipe 式便捷连接与文件终端

修改：

- `web/src/components/VpsCard.vue`
- `web/src/components/NodeDetail.vue`
- `web/src/components/TerminalConsole.vue`
- `web/src/components/NodeFiles.vue`
- `web/src/components/PreferenceEditor.vue`
- `internal/core/server/terminal_stream.go`
- `internal/agent/terminal/manager.go`
- `internal/agent/terminal/provider_linux.go`
- `internal/core/server/files_api.go`
- `internal/core/server/files_transport.go`
- `internal/agent/files/service.go`
- `internal/core/dashboard/preferences.go`
- `internal/core/server/dashboard_api.go`
- `internal/core/storage/schema.sql`

新建文件：无。全部快捷操作进入现有卡片与详情组件。

必须完成：

1. 每张 VPS 卡片直接提供终端、文件、SSH、管理入口。
2. 一键打开所属 VPS 的 Agent 主机终端。
3. 支持 Docker 容器 Exec。
4. 终端输入、输出、窗口调整和关闭正确。
5. Web 文件浏览、上传、下载、创建、重命名、删除及文本编辑。
6. 节点配置保存 SSH Host、Port、User。
7. 生成并复制正确的 OpenSSH 命令。
8. 常用 Docker 服务入口使用用户设置的可访问地址。
9. 离线节点不能创建虚假的 Agent 会话。
10. SSH 连接信息在 Agent 离线时仍可读取和复制。
11. 文件操作遵守权限与路径安全限制。
12. 管理员退出后，关联终端会话失效。
13. 所有快捷入口绑定卡片对应节点，不依赖其他节点的选中状态。

删除：删除终端和文件功能原有的重复页面入口、重复连接方法及无效状态。

阶段验收：终端、文件、SSH 快捷命令和服务入口全部能从指定节点进入；会话绑定、节点身份、文件权限及持久化正确。

强制 REJECT：新增第二 SSH 管理后端、保存明文私钥、错误节点连接、终端权限绕过、文件越权、复制错误连接地址、入口无法打开，全部驳回。

## 八、阶段 6：安装、Tailscale 与 Agent 部署

修改：

- `cmd/nodedance/main.go`
- `cmd/nodedance-agent/main.go`
- `internal/core/cli/run.go`
- `internal/agent/systemd.go`
- `internal/agent/registration.go`
- `internal/agent/config.go`
- `internal/core/tailscale/status.go`
- `internal/core/tailscale/ssh.go`
- `internal/core/tailscale/deploy.go`
- `internal/core/server/tailscale_api.go`
- `web/src/components/TailscaleDiscovery.vue`
- `Makefile`
- `README.md`

新建文件：无。

必须完成：

1. Core 安装、启动及指定监听地址。
2. Agent 手动安装、注册、启动、重启和停止。
3. systemd 常驻管理。
4. Core/Agent 同机安装配置。
5. 远程 HTTPS/WSS 连接配置。
6. Tailscale 节点发现。
7. SSH 辅助安装 Agent。
8. SSH 主机身份校验。
9. 已存在服务与配置处理。
10. Agent 凭据保护与重连。
11. 安装失败明确报错，不影响其他节点。
12. Tailscale 不存在时仍能正常运行 NodeDance。
13. README 完整记录安装与配置命令。

删除：删除失效安装命令、重复配置转换和旧部署入口。

阶段验收：安装命令、参数、配置、权限、systemd 服务定义、注册和身份验证流程全部完成并通过本地可执行检查；其他 VPS 安装在用户授权后的实机阶段统一执行。

强制 REJECT：安装覆盖无关服务、泄露凭据、强制 Tailscale、第二 Agent 身份体系、未经授权连接 VPS、手动安装不工作，全部驳回。

## 九、阶段 7：运维增强、备份恢复与正式交付

修改：

- `internal/agent/probes/`
- `internal/core/probes/store.go`
- `internal/core/server/probe_api.go`
- `internal/core/alerts/`
- `internal/core/server/alerts_api.go`
- `internal/core/server/alerts_scheduler.go`
- `internal/core/server/server.go`
- `internal/core/backup/backup.go`
- `internal/core/cli/backup.go`
- `web/src/components/NodeServiceProbes.vue`
- `web/src/components/AlertCenter.vue`
- `README.md`
- `docs/architecture.md`
- `docs/plan.md`

新建文件：无。

必须完成：

1. HTTP/TCP 服务探测。
2. Agent 执行实际探测。
3. 探测状态和历史记录。
4. 离线与未知状态正确处理。
5. 告警规则、触发、恢复与历史。
6. 通知发送和失败记录。
7. 探测、告警不阻断主机和 Docker 监控。
8. Core 数据备份。
9. Core 数据恢复。
10. 恢复后的管理员、节点、Agent 身份、偏好和任务数据保持正确。
11. Web 探测及告警界面全部接通。
12. README、架构文档与实际代码一致。
13. 正式构建完整成功。

删除：删除重复调度器、已经无业务价值的辅助实现及失效文档。

阶段验收：探测、告警、通知、备份恢复的完整业务路径均通过检查；能够实际执行的备份恢复和服务状态操作全部运行验证，外部 VPS 相关执行统一纳入最终实机验收。

强制 REJECT：错误告警、备份不能恢复、身份数据丢失、通知失败拖垮 Core、无效 UI、文档虚构功能或重新建设过度测试平台，全部驳回。

## 十、阶段 8：全部基础功能完成后的 VPS 实机验收

阶段 0～7 全部 PASS 后，才申请用户授权 VPS 部署。首先使用一台 VPS，同时运行 Core、Agent 和 Docker Engine。

当前已授予的远程权限仅限于：阶段 0～7 全部 PASS 后，在用户指定的一台已通过 Tailscale 连接的 VPS 上安装、测试并卸载 Agent。此授权不允许修改任何 Tailscale 配置，也不允许操作 Core、Docker 或执行本节其他远程验收；这些额外操作必须另行取得用户明确授权。阶段 0～7 PASS 前不得连接、扫描或登录该 VPS。

完成以下全部验收：

1. Core 初始化、登录与退出。
2. Agent 注册、连接、失联和重连。
3. 主机实时指标和历史监控。
4. Docker 独立容器自动发现。
5. Docker Compose 容器自动发现。
6. 创建、启动、停止、重启、暂停、恢复、重命名和删除容器。
7. 日志、资源统计、镜像操作。
8. 创建 Compose 项目并实际部署。
9. 修改 Compose 配置和端口。
10. 独立容器受控重建。
11. 主机终端与 Docker Exec。
12. 文件管理与传输。
13. SSH 快捷连接信息。
14. 服务探测和告警。
15. Core 备份与恢复。
16. 数据持久化与正确的失败状态。

全部结果必须与真实 Linux、Docker CLI、文件系统和数据库状态一致。单 VPS 验收完成之后，才由用户授权增加第二台 VPS。第二台 VPS 验证多 Agent 同时在线、节点隔离、跨网络通信、错节点操作保护和各节点独立断线恢复。任何真实环境验收失败，均交回同一个 Luna Work 修复。

## 十一、独立审查 Agent 的统一判定规则

### PASS 条件

- 本阶段所有规定文件已完成相应处理。
- 所有规定功能全部实现。
- 所有规定前端入口可到达。
- 完整调用链真实存在。
- 所有规定检查已执行并通过。
- 删除的重复逻辑已经彻底清理。
- 没有权限、数据安全和错误目标操作问题。
- 没有修改其他阶段的无关架构。
- 没有新建 Git 分支。

### REJECT 条件

以下任意一项成立，立即驳回：

- 缺少任何规定功能。
- 只实现前端，没有后端。
- 只实现后端，没有前端入口。
- 模块无法编译或启动。
- 业务 API 返回假成功。
- 使用静态数据冒充真实状态。
- 错误 VPS、错误容器或错误项目被操作。
- 新增重复任务状态机、Docker Client、WebSocket 或 HTTP 请求层。
- 旧实现未删除，新实现只是重复包装。
- 存在鉴权绕过或数据损坏风险。
- 使用无效测试结果冒充通过。
- 围绕测试夹具和测试框架原地打转。
- 当前阶段尚未完成就开发下一阶段。
- 创建新 Git 分支。
- 审查失败后更换 Luna Work。

驳回必须采用的格式：

> REJECT：阶段名称
>
> - 问题文件与代码位置
> - 实际错误
> - 未完成的强制功能
> - 必须删除或修改的代码
> - 修复后的验证方法
> - 返工对象：原 Luna Work 会话

不得只说“质量不好”“还需要优化”“测试不足”。修复必须围绕明确问题展开，不得自行扩大成全仓库重构。

## 十二、最终交付定义

NodeDance 的基础开发完成，意味着以下全部功能已经写成可运行的真实产品代码：

- Komari 式 Core-Agent 主从分离。
- 多 VPS 监控与历史数据。
- VPS 与 Docker 融合监控首页。
- Docker 完整生命周期和镜像管理。
- Docker Compose 创建、管理和配置修改。
- 独立容器受控重建。
- XPipe 式终端、文件和 SSH 快捷入口。
- Tailscale 发现与 Agent 安装管理。
- 服务探测、告警和备份恢复。
- 管理员鉴权及完整安全边界。
- 正式构建、安装说明和架构文档。

全部完成之后统一进行 VPS 实机验收，不允许拿“已经实现基础框架”“还有若干增强功能”“后面再补齐”作为阶段 PASS 的理由。开发始终在 `main`，按阶段串行推进。一个 Luna Work 负责到底；独立审查 Agent 发现问题就驳回给它继续完成。功能必须全部做完。测试只负责验证功能，不允许反过来主导开发。
