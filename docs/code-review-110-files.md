# 110 文件深度审查与修复

本批选取上轮尚未完成全文审查的 110 个文件，逐文件读完实现、异常分支、取消/恢复路径和测试。仅搜索、局部阅读、重复阅读或新增回归测试均不计入覆盖数字。按原 331 文件清单，累计 **262 / 331**，尚余 **69**。另列的 55 个文档、锁文件及资源不计入代码覆盖数。

在 `main` 上修复，保留先前未提交修改。没有升级版本、打 release tag、推送、发布或更新实际机器。本批审查完成不代表剩余文件已审查，也不代表代码无缺陷。

## 已修复的实际问题

| 范围 | 问题和影响 | 修复与验证 |
| --- | --- | --- |
| 控制客户端 | 登录替换 `http.Client.Jar`，与并发读取产生 race。 | 保持线程安全 cookie jar，原地过期旧 cookie；并发登录/读取 race 回归。 |
| Core 契约读取 | 仅解第一段 JSON，接受尾部第二段 JSON 或超限空白。 | 有界读取并检查长度，完整 JSON 解码；两种非法输入回归。 |
| 终端重连 | 关闭不能打断正在进行的 resume HTTP 请求。 | Read 拥有可取消上下文，CloseNow 同时取消读取与连接；阻塞 resume 回归。 |
| TUI | 可选 body 的文件读取失败后仍发送空 body。 | 读取失败退出本次提交；真实 PTY 验证不会提交请求。 |
| CLI 更新观察 | 20 分钟观察上下文没有用于实际状态请求，`--timeout 0` 可越过观察截止。 | 每次状态请求以观察上下文为父上下文；共用 CLI 流程检查。 |
| 文件编辑 | ReadText 返回旧文件内容，却可能重开新路径得到新文件版本，允许覆盖未见内容。 | 对实际返回的字节计算版本；真实路径替换且保留元数据的冲突回归。 |
| 新建文本 | 超过单个 32 KiB chunk 的新文本无法创建，即使在 8 MiB 编辑上限内。 | 分块写入；完整 8 MiB 新文本创建与读回回归。 |
| 上传取消 | 空闲 `io.Pipe` 读取不响应上下文取消。 | 取消关闭读端，并按上下文返回取消结果；阻塞读取回归。 |
| 复制/归档 | 根目录 `/` 的包含关系检查拼成 `//`，遗漏输出位于源目录内部。 | 规范化源路径和分隔符；根目录自包含复制、归档拒绝回归。 |
| Web 编辑版本 | 保存后无条件采用 stat 得到的新版本，可能绑定另一写入者的新内容。 | readback 与保存内容一致才绑定新版本；替换后拒绝继续覆盖，浏览器验证。 |
| Core 下载 | Agent 错误响应不含 Entry，Core 在映射错误前当作无效元数据，丢失 404 等语义。 | 先检查响应类型/操作及错误，再要求元数据；真实 Core/root Agent 下载与预览 404。 |
| 指标合并 | Uptime 通用 merge 覆盖深拷贝结果，重新引用调用方 BootTimeUnix。 | 使用已拥有的副本；调用方修改不能改变合并后的指标。 |
| 容器日志 | 错误通道和帧关闭同时就绪，可能将截断日志误报正常结束。 | 宣布正常结束前接收尚未处理的错误；50 次损坏日志回归。 |
| Web 操作重试 | 容器、镜像、Compose 等专用界面自行随机生成幂等键，失败后重试可能新执行。 | 删除重复键参数，使用共同 operationRequest 的稳定 intent；真实浏览器模拟响应丢失验证同键。 |
| Compose 编辑 | 切换项目保留前一个项目草稿/基础摘要；旧内容验证回包可以授权新草稿。 | 按节点/项目 key 重建编辑器；验证回包同时绑定代次及内容；浏览器分别检查。 |
| 探针编辑 | 自动刷新采用新 revision，跳过冲突；原探针消失后错误走 create。 | 保存初始编辑 revision，始终更新原 ID；真实 Core 返回 409/404 并保持远端内容。 |
| 历史图表 | 加载时画布未挂载，数据 watch 在 DOM 更新前运行，首轮异步结果可能没有图。 | 保持画布宿主，更新后渲染并观察根主题；真实浏览器检查首轮实际 canvas。 |
| 主题设置 | 深色按钮实际设置 system。 | 按钮设置 dark；浏览器检查选择、保存及重载。 |
| Web 更新 | 独立 sessionStorage 失败可卡住 busy；迟到任务回包可进入另一节点视图。 | 使用共同 intent/存储回退，以页面代次隔离回包。 |
| Tailscale 展示 | 旧 peer 指纹回包可能进入新 peer；离开页面仍继续长轮询。 | 绑定请求 peer；卸载及切换观察目标后停止轮询，卸载清空机密；隔离浏览器协议夹具。 |
| 容器终端 | Docker resize 使用无限 Background 上下文。 | 加 2 秒期限；root Docker tmux 测试通过。 |

## 精简设计的取舍

删除只有转发作用的 Compose writer 包装，直接使用现有 socket writer；镜像仓库认证头复用已有编码及清理函数；journal ID 只保留一份校验；完成的 Compose 轮询定时器及时移出集合；删除空生命周期回调。专用容器/Compose/探针表单移除部分独立业务校验，继续使用发现的 schema、共同执行和 Core 校验。

保留持久任务 journal、代次绑定、未知结果、内容摘要和取消证据。它们分别阻止重复执行、旧连接污染、假成功和未见内容覆盖，不能因为分支多就删除。保留源码 Docker 构建入口与原生 release 打包入口，两者用途不同，前者不是退役兼容代码。

历史 Compose 项目配置复验仍有串行 RPC 成本，Docker 暂存/持久化仍有复制成本；当前缺少规模测量，不以函数长度或局部相似作为重构依据。文件文本容量队列的高容量重试计数仍值得独立故障注入；本批没有将它标记成已复现或已修复的问题。

## 验证

- `go test -race -exec 'sudo -n' ./...`：全仓通过；新增指标所有权、损坏日志、文本替换、8 MiB 新建、空闲上传取消等回归通过。协议校验精简后再次运行 `go test -race ./internal/protocol -count=1` 通过。
- `go vet ./...`：通过；协议精简后对应 vet 再次通过。
- `pnpm --dir web typecheck`、`pnpm --dir web build`：通过，使用新 Web 资源验证。构建仍提示部分 chunk 大于 500 kB，本批没有通过调高警告阈值隐藏它。
- `node web/scripts/audit-interfaces.cjs`：通过，观察到 121 个发现的远程操作；新增回归验证主题持久化、Compose 项目隔离/旧验证/同键重试、探针 409/404、SSH 指纹绑定和卸载停止轮询。原 24 布局快照之外，新增设置／Compose／发现页面 12 个布局检查。
- `NODEDANCE_GUEST_ATTACKS=1 node web/scripts/audit-guest.cjs`：通过，本次实际发现 109 个私有操作、443 个安全用例、0 失败；20 个亮/暗/移动布局检查及新增首轮 canvas 初始化通过。数字是本次结果，不是固定覆盖断言。
- `node web/scripts/audit-files.cjs`：通过，真实 Core/root Agent、实际 VueFinder 对话框、8 MiB 编辑/CLI 读回、分页搜索、冲突、缺失下载 404、保存后替换拒绝、上传及失败恢复、亮/暗/移动布局均检查；空闲上传取消另有 Go 回归。
- `NODEDANCE_TEST_DOCKER=1 go test -race -exec 'sudo -n --preserve-env=NODEDANCE_TEST_DOCKER' ./internal/agent/terminal -run TestBundledTmuxDocker -count=1`：通过。
- `python3 scripts/vendor-tmux.py --check`、`python3 scripts/interface-coverage.py --check`、许可证比对、`git diff --check`：通过。接口文档核对结果为 121 远程 + 15 本地/工作流操作。

默认测试中的可选长时容量 soak 未启用；这次不宣称验证了大规模持续运行或正式 release 加速耗时。

Go 回归使用临时文件/隔离存储；文件浏览器使用临时 Core/root Agent，Docker 终端使用测试容器；游客仅使用协议模拟 Agent。Compose/Tailscale 浏览器响应夹具不执行实际远端变更。发布流水线优化沿用前轮实现，本批没有新的正式 GitHub release 耗时测量。

## 逐文件结论

| # | 文件 | 全文审查结论 |
| --- | --- | --- |
| 1 | [internal/control/client.go](../internal/control/client.go) | 已复现并修复 CookieJar 指针替换导致的并发 race；目录读取拒绝超限和第二份 JSON。完整检查私有 session、CSRF、发现与上传/下载。 |
| 2 | [internal/control/client_test.go](../internal/control/client_test.go) | 完整审查真实会话生命周期、私有 session 文件、重定向拒绝、发现契约、WebSocket 和终端不重放回归；保留。 |
| 3 | [internal/control/operations.go](../internal/control/operations.go) | 完整审查共享 Core 操作定义、DTO schema、传输、完成契约、秘密字段标记、路径参数及 caller-owned clone；保留唯一业务操作契约。 |
| 4 | [internal/control/terminal.go](../internal/control/terminal.go) | 已复现并修复 CloseNow 无法取消在途 resume HTTP 请求；完整检查授权、重连、关闭、输入不重放和 resize。 |
| 5 | [internal/core/cli/backup.go](../internal/core/cli/backup.go) | 完整检查参数解析和备份/恢复委派，沿用共享取消和完整性实现，无独立恢复逻辑。 |
| 6 | [internal/core/cli/bootstrap.go](../internal/core/cli/bootstrap.go) | 完整检查 TTY 检测、raw 状态恢复及隐藏密码输入；无人值守不读取 stdin。 |
| 7 | [internal/core/cli/bootstrap_test.go](../internal/core/cli/bootstrap_test.go) | 完整检查真实 PTY 首次输入与密码不回显、非终端路径；保留。 |
| 8 | [internal/core/cli/control.go](../internal/core/cli/control.go) | 完整审查统一发现、流/上传/下载/交互分发与 stdin 排他；删除重复部署方法校验；JSONL 的非文件 io.Reader 不可强制中断需保留调用方所有权。 |
| 9 | [internal/core/cli/control_test.go](../internal/core/cli/control_test.go) | 完整审查真实动态路由、失败下载不留文件、部署方法和超过 4 MiB 的请求输入；保留。 |
| 10 | [internal/core/cli/run.go](../internal/core/cli/run.go) | 完整检查配置优先级、服务初始化、首登、TLS 及关闭；需纠正架构文档关于 CLI 强制外部 TLS 的描述，避免破坏现有反向代理/Docker 部署。 |
| 11 | [internal/core/cli/terminal.go](../internal/core/cli/terminal.go) | 完整检查键盘 goroutine、raw 终端上下文、取消等待和控制键；终端关闭进一步依赖共享 TerminalSession。 |
| 12 | [internal/core/cli/transport_test.go](../internal/core/cli/transport_test.go) | 完整检查 TLS 成对参数及无效 PEM；没有证明 CLI 拒绝所有 HTTP 监听，文档需与行为一致。 |
| 13 | [internal/core/cli/tui.go](../internal/core/cli/tui.go) | 修复请求文件读取失败仍提交可选空 body 的错误；完整审查输入、秘密隐藏、分页、观察取消、stream 收尾及本地共享分发。 |
| 14 | [internal/core/cli/tui_test.go](../internal/core/cli/tui_test.go) | 完整审查 PTY 登录、动态操作发现、语言切换、不回显密码与共用入网表单；保留。 |
| 15 | [internal/core/cli/update.go](../internal/core/cli/update.go) | 完整审查发现路由、明确版本、幂等 key 与等待；修复 status 请求未继承 20 分钟等待上下文。 |
| 16 | [internal/core/cli/update_test.go](../internal/core/cli/update_test.go) | 完整检查共享更新流程、路由发现与仅回滚确认，CLI/TUI 提交一致。 |
| 17 | [internal/core/cli/upload_parity_test.go](../internal/core/cli/upload_parity_test.go) | 完整检查标准输入/FIFO 真正二进制流提交、协议发现与同一内容，保留。 |
| 18 | [web/src/App.vue](../web/src/App.vue) | 完整检查 802 行认证、游客导航、历史恢复、主题和设置；修复深色按钮误选择 system，并补真实浏览器持久化验证。 |
| 19 | [web/src/api.ts](../web/src/api.ts) | 完整检查 DTO、超时/取消、CSRF、schema、机密和稳定 intent；移除专用容器/镜像 API 的重复随机幂等键路径，统一共同执行。 |
| 20 | [web/src/components/AgentEnrollment.vue](../web/src/components/AgentEnrollment.vue) | 注册和两种部署命令展示复用 Core API；Origin 表单检查有副本，后续统一发现 schema。 |
| 21 | [web/src/components/AlertCenter.vue](../web/src/components/AlertCenter.vue) | 审查规则、渠道机密、revision、窗口和轮询；所有共享变更走 Core operation。 |
| 22 | [web/src/components/ComposeCreateForm.vue](../web/src/components/ComposeCreateForm.vue) | 创建复用稳定 intent；移除独立名字/路径/大小校验副本，保留空表单提示，合法性由共同契约和 Core 判断。 |
| 23 | [web/src/components/ComposeEditor.vue](../web/src/components/ComposeEditor.vue) | 修复异步旧内容验证使新内容标记有效；保留 Core 内容摘要冲突保护。 |
| 24 | [web/src/components/ComposeProjects.vue](../web/src/components/ComposeProjects.vue) | 修复切换项目复用旧编辑状态；已触发轮询定时器从集合移除，避免持续积累。 |
| 25 | [web/src/components/ContainerCreateForm.vue](../web/src/components/ContainerCreateForm.vue) | 删除独立的端口、挂载与请求业务校验副本；专用表单构造展示输入，共同 operationRequest/schema 与 Core 执行校验。 |
| 26 | [web/src/components/ContainerRebuildWizard.vue](../web/src/components/ContainerRebuildWizard.vue) | 审查计划、目标确认、任务观察和回滚清理；提交复用稳定 intent，切换容器由父组件按 ID 重新创建，Core 仍重新检查实际目标。 |
| 27 | [web/src/components/ContainerStreams.vue](../web/src/components/ContainerStreams.vue) | 审查有界日志缓冲、跨帧 UTF-8、顺序检查、重连代次和卸载关闭。 |
| 28 | [web/src/components/ContractFields.vue](../web/src/components/ContractFields.vue) | 从 Core schema 生成字段，整数保留 BigInt 精度；需由共同序列化执行检查。 |
| 29 | [web/src/components/GuestDisplay.vue](../web/src/components/GuestDisplay.vue) | 审查公开入口、单请求刷新、失败撤回和卸载取消；沿用公开投影。 |
| 30 | [web/src/components/GuestSettings.vue](../web/src/components/GuestSettings.vue) | 共享 guest.configure 操作提交 revision；预览与保存数据源区分清晰。 |
| 31 | [web/src/components/HistoricalMetrics.vue](../web/src/components/HistoricalMetrics.vue) | 修复 loading 时 chart host 未挂载导致首轮数据无法初始化；保持画布 DOM 并于更新后渲染，监听共享根主题。 |
| 32 | [web/src/components/HostManagement.vue](../web/src/components/HostManagement.vue) | schema 生成设置/批量/授权字段，复用共同执行与稳定幂等键。 |
| 33 | [web/src/components/ImagesPanel.vue](../web/src/components/ImagesPanel.vue) | 完整审查分页、拉取密码清除、任务取消、审计和轮询；专用变更统一共同稳定 intent。 |
| 34 | [web/src/components/MetricsPanel.vue](../web/src/components/MetricsPanel.vue) | 审查单调时钟样本过期、租约过期、未知数据、指标格式及共享主题。 |
| 35 | [web/src/components/NodeDetail.vue](../web/src/components/NodeDetail.vue) | 完整检查容器操作、任务/历史、排序和编辑状态；切换重建容器时 key 重建 wizard，删除空 onMounted。 |
| 36 | [web/src/components/NodeFiles.vue](../web/src/components/NodeFiles.vue) | VueFinder 实际驱动、焦点圈定、主题和卸载资源清理逐行审查。 |
| 37 | [web/src/components/NodeServiceProbes.vue](../web/src/components/NodeServiceProbes.vue) | 复现列表刷新偷偷更新编辑 revision 及已删探针被重新创建；固定原始编辑 revision，始终更新原 ID。 |
| 38 | [web/src/components/NodesDashboard.vue](../web/src/components/NodesDashboard.vue) | 完整检查租约/代次/样本排序、断线重连、节点状态及专用任务提交；复用稳定 intent。 |
| 39 | [web/src/components/OperationConsole.vue](../web/src/components/OperationConsole.vue) | 从发现契约渲染并执行所有传输，未知提交/完成观察分离、明确新执行键和终端取消检查。 |
| 40 | [web/src/components/PreferenceEditor.vue](../web/src/components/PreferenceEditor.vue) | 保存共享偏好，专用字段为展示入口；未发现需改变存储语义的问题。 |
| 41 | [web/src/components/TailscaleDiscovery.vue](../web/src/components/TailscaleDiscovery.vue) | 修复卸载后长轮询继续私有读取，以及旧 peer SSH 指纹回包覆写当前 peer；保持单次凭据和明确 unknown 恢复。 |
| 42 | [web/src/components/TerminalConsole.vue](../web/src/components/TerminalConsole.vue) | 审查票据、准确流取消、重连与 UTF-8 解码、焦点/布局和卸载，沿用共同 Core 操作。 |
| 43 | [web/src/components/UiDialog.vue](../web/src/components/UiDialog.vue) | 原生 dialog、排队焦点和卸载取消完整。 |
| 44 | [web/src/components/UiIcon.vue](../web/src/components/UiIcon.vue) | 静态本地 SVG 路径，无动态内容执行。 |
| 45 | [web/src/components/UpdateCenter.vue](../web/src/components/UpdateCenter.vue) | 删除重复明文请求 sessionStorage/随机键实现，复用共同稳定 intent；迟到提交回包仅更新原页面代次。 |
| 46 | [web/src/components/VpsCard.vue](../web/src/components/VpsCard.vue) | 完整检查共享偏好、状态过期、Docker 展示和快捷操作；显示过滤不授予权限，实际命令走共同操作。 |
| 47 | [web/src/compose-api.ts](../web/src/compose-api.ts) | 专用 Compose 展示调用复用 Core 发现的 operationRequest，未维护路由副本。 |
| 48 | [web/src/dialogs.ts](../web/src/dialogs.ts) | 审查队列、取消及卸载清理，所有等待者均能收到结果。 |
| 49 | [web/src/file-manager.ts](../web/src/file-manager.ts) | 保存后重读实际文本，仅给匹配保存内容的 readback 绑定版本；外部替换保留原内容且要求重新打开。图片保存清除旧编辑基线。 |
| 50 | [web/src/guest.ts](../web/src/guest.ts) | 仅声明公开投影 DTO，无权限或业务分支。 |
| 51 | [web/src/i18n.ts](../web/src/i18n.ts) | 语言初始化、存储不可用、插值及展示消息重译均有校验；不对用户内容作业务转换。 |
| 52 | [web/src/main.ts](../web/src/main.ts) | 入口挂载和共享样式引用清晰，无冗余初始化。 |
| 53 | [web/src/metrics-contract.ts](../web/src/metrics-contract.ts) | 仅指标展示 DTO，状态和未知数据与当前协议一致。 |
| 54 | [web/src/navigation.ts](../web/src/navigation.ts) | 浏览器视图状态不携带权限；文件路径拒绝穿越，最终仍由 Core/Agent 验证。 |
| 55 | [web/src/streamApi.ts](../web/src/streamApi.ts) | 通过发现契约获取 WebSocket URL，增量 TextDecoder 支持跨帧 UTF-8。 |
| 56 | [web/src/vite-env.d.ts](../web/src/vite-env.d.ts) | Vite 环境类型引用，保留。 |
| 57 | [web/src/vuefinder-locales.d.ts](../web/src/vuefinder-locales.d.ts) | 锁定包语言资源声明，保留。 |
| 58 | [scripts/interface-coverage.py](../scripts/interface-coverage.py) | 从共同目录生成接口覆盖，检查传输类型和生成文件漂移。 |
| 59 | [scripts/subset-web-fonts.py](../scripts/subset-web-fonts.py) | 校验固定输入摘要、字形与字体轴，离线产物生成边界清晰。 |
| 60 | [scripts/vendor-tmux.py](../scripts/vendor-tmux.py) | 固定上游摘要和 ELF 架构、静态链接、术语及许可证检查；未执行刷新。 |
| 61 | [web/public/theme-init.js](../web/public/theme-init.js) | 首屏本地主题和 CSP 外部脚本一致，存储不可用可回退。 |
| 62 | [web/scripts/audit-files.cjs](../web/scripts/audit-files.cjs) | 完整审查真实 Core/root Agent 夹具、实际 VueFinder 对话框和四布局；新增下载错误码、保存后外部替换回归。 |
| 63 | [web/scripts/audit-guest.cjs](../web/scripts/audit-guest.cjs) | 完整审查公开投影、真实管理员会话恢复、迟到私有请求、四布局和攻击套件；新增首轮历史画布实际初始化断言。 |
| 64 | [web/scripts/audit-interfaces.cjs](../web/scripts/audit-interfaces.cjs) | 完整审查未来发现操作、失败重试、完成观察、整数精度和真实 UI；接入隔离浏览器重点回归模块。 |
| 65 | [web/scripts/check-i18n.cjs](../web/scripts/check-i18n.cjs) | 检查语言键、插值、持久化回退和动态消息；未降低断言。 |
| 66 | [web/scripts/check-operations.cjs](../web/scripts/check-operations.cjs) | 动态新增操作、路径绕过、上传、完成引用及稳定 intent 验证，保持可发现性。 |
| 67 | [web/scripts/file-notices.cjs](../web/scripts/file-notices.cjs) | 审查依赖遍历和许可证生成/比对，保留锁定依赖通知。 |
| 68 | [web/scripts/guest-attacks.cjs](../web/scripts/guest-attacks.cjs) | 完整审查发现式私有操作矩阵、伪造身份、方法/路径绕过、WebSocket、CSRF、XSS、并发及即时撤回。 |
| 69 | [internal/agent/files/archive_test.go](../internal/agent/files/archive_test.go) | 完整审查 ZIP/TAR/gzip/bzip2 独立格式、危险条目、权限、压缩校验和 8 MiB 编辑边界回归；保留。 |
| 70 | [internal/agent/files/browse.go](../internal/agent/files/browse.go) | 完整审查有界 top-page 选择、版本、payload 再截断、递归深度和 symlink/protected 路径过滤；保持分页稳定。 |
| 71 | [internal/agent/files/operations.go](../internal/agent/files/operations.go) | 完整审查复制、归档、暂存提交、目录权限及容量；复现并修复根目录/路径别名源的自包含目标检测。 |
| 72 | [internal/agent/files/operations_test.go](../internal/agent/files/operations_test.go) | 完整审查分页/搜索、复制提取、路径攻击、保护路径祖先和大转义路径响应边界；保留。 |
| 73 | [internal/agent/files/service.go](../internal/agent/files/service.go) | 完整审查 openat2、突变基线、上传和文本锁；复现并修复旧文本配新文件版本导致丢更新，以及新建8MiB文件被单chunk限制拒绝。 |
| 74 | [internal/agent/files/service_test.go](../internal/agent/files/service_test.go) | 完整审查根目录保护、句柄泄漏、关闭拒绝与symlink竞争；测试命名/文字已改为当前openat2。 |
| 75 | [internal/agent/files/task_executor.go](../internal/agent/files/task_executor.go) | 完整审查持久化执行证据、暂存恢复、上传 rendezvous 和完成/unknown；修复 ctx 取消无法打断 idle PipeReader.Read。 |
| 76 | [internal/agent/terminal/container_helper_linux.go](../internal/agent/terminal/container_helper_linux.go) | 完整审查 root-only 容器 helper、固定 shell、SIGTERM/SIGHUP、PTY 原始字节和 Exec 收尾；Resize timeout 依赖待修。 |
| 77 | [internal/agent/terminal/container_integration_test.go](../internal/agent/terminal/container_integration_test.go) | 完整审查临时 Docker容器、分离/重连 shell 变量、后台任务、新 pane 同目标与关闭后 Exec 清理；保留。 |
| 78 | [internal/agent/terminal/provider_linux.go](../internal/agent/terminal/provider_linux.go) | 完整审查 host/container 固定目标、私有工作区及 Exec 附着生命周期；给 ExecResize 增加截止时间以免关闭处理被卡住，复用完整ID校验。 |
| 79 | [internal/agent/terminal/tmux_linux.go](../internal/agent/terminal/tmux_linux.go) | 完整审查 tmux socket身份、目标 quoting、detach保留与close终止、客户端恢复及已修复 Write锁释放；保留。 |
| 80 | [internal/agent/terminal/tmux_linux_test.go](../internal/agent/terminal/tmux_linux_test.go) | 完整审查真实root tmux状态/pane/恢复测试与 pipe 阻塞写关闭回归；保留。 |
| 81 | [internal/agent/hostcontrol/control_test.go](../internal/agent/hostcontrol/control_test.go) | 完整审查持久化幂等、进程组取消/超时、输出容量、禁止远程脚本政策及私有设置别名；保留。 |
| 82 | [internal/agent/hostcontrol/history_test.go](../internal/agent/hostcontrol/history_test.go) | 完整审查容量压缩后不重放、unknown 保留与结果持久化故障后的恢复；保留。 |
| 83 | [internal/agent/images/executor.go](../internal/agent/images/executor.go) | 复用既有 EncodeRegistryCredentials 与 wipeCredentials，删除重复 JSON/base64 认证头实现；保留一次性机密与持久任务校验。 |
| 84 | [internal/agent/rebuild_adapter.go](../internal/agent/rebuild_adapter.go) | 完整审查动作 DTO 向 rebuild 执行请求转换及 Reconcile 委派；这是实际模块边界，保留。 |
| 85 | [internal/agent/compose_runtime.go](../internal/agent/compose_runtime.go) | 完整审查初始化；删除只转发 Send 的 composeWriterAdapter，直接传入现有 socketEnvelopeWriter。 |
| 86 | [internal/agent/taskrunner/runner.go](../internal/agent/taskrunner/runner.go) | 审查容量、持久确认、恢复、取消、只读协调及报告游标；保留当前有界工作与持久任务语义。 |
| 87 | [internal/agent/runtime.go](../internal/agent/runtime.go) | 完整审查握手、凭据轮换、能力协商、各桥接生命周期及队列降级；复用现有 writer，删除 Compose 专用转发包装。 |
| 88 | [internal/agent/task_bridge.go](../internal/agent/task_bridge.go) | 逐项审查持久任务、连接代次、报告确认和终止流程，执行与连接分离符合当前设计。 |
| 89 | [internal/agent/container_stream_bridge.go](../internal/agent/container_stream_bridge.go) | 复现日志错误通道和结束通道同时就绪导致损坏日志被误报正常结束；结束前先收取错误。 |
| 90 | [internal/core/compose/store.go](../internal/core/compose/store.go) | 事务保存项目引用，读取重新验证上下文；保留历史库存供离线展示，未发现需改问题。 |
| 91 | [internal/core/metrics/store.go](../internal/core/metrics/store.go) | 复现合并 uptime 后 BootTimeUnix 指针重新引用调用方的问题；改用已经深拷贝的 incoming uptime，补充所有权回归。 |
| 92 | [internal/protocol/tasks.go](../internal/protocol/tasks.go) | 完整审查 1199 行身份、动作互斥、载荷摘要、机密清除、页确认与报告验证；删除重复 journal ID 校验函数，复用当前统一规则。 |
| 93 | [internal/core/server/agent_api.go](../internal/core/server/agent_api.go) | 凭据认证、注册命令选择、撤销轮换和在线租约展示逐项审查，未发现需改变授权边界的问题。 |
| 94 | [internal/core/server/agent_http.go](../internal/core/server/agent_http.go) | 审查 gzip 双重大小界限、通道身份、序号重放、单轮询与容量回收；沿用确认式 HTTP 回退。 |
| 95 | [internal/core/server/agent_websocket.go](../internal/core/server/agent_websocket.go) | 完整审查 1118 行握手、代次替换、租约 watcher、单调能力降级、桥接响应及持久报告验证。 |
| 96 | [internal/core/server/alerts_api.go](../internal/core/server/alerts_api.go) | 审查 revision、渠道机密入参、窗口操作、审计及认证上游；保留公共存储规则。 |
| 97 | [internal/core/server/alerts_security.go](../internal/core/server/alerts_security.go) | 密钥 regular-file 检查、0600、原子硬链接和目录同步完整。 |
| 98 | [internal/core/server/auth_api.go](../internal/core/server/auth_api.go) | 审查 setup 事务、会话、CSRF、密码并发限额及密码变更期间登录事务复验；需保留真实 guest 认证回归。 |
| 99 | [internal/core/server/bootstrap.go](../internal/core/server/bootstrap.go) | 初始化管理员只允许一次；本地凭据文件与事务失败清理完整。 |
| 100 | [internal/core/server/bootstrap_test.go](../internal/core/server/bootstrap_test.go) | 逐案验证自定义/生成凭据、权限、重启、错误登录和正确登录后清理，未弱化断言。 |
| 101 | [internal/core/server/capacity_test.go](../internal/core/server/capacity_test.go) | 容量验证使用真实 Core/SQLite 和模拟 Agent，并限制节点、时长、资源增长；未误称 root/Docker 集成。 |
| 102 | [internal/core/server/compose_api.go](../internal/core/server/compose_api.go) | 审查实时与历史项目合并、配置可用性复验、有界 waiter 和响应关联。 |
| 103 | [internal/core/server/compose_tasks_api.go](../internal/core/server/compose_tasks_api.go) | 完整审查项目作用域、内容摘要、任务门控、单次内存载荷和断线处理。 |
| 104 | [internal/core/server/dashboard_api.go](../internal/core/server/dashboard_api.go) | 节点偏好绑定路径身份、历史纳秒范围与保留时限验证一致。 |
| 105 | [internal/core/server/docker_api.go](../internal/core/server/docker_api.go) | 租约与容器库存展示、推送指纹和偏好身份映射审查；路由不会把短 ID 解析为实际资源，不增加退役模式。 |
| 106 | [internal/core/server/docker_integration.go](../internal/core/server/docker_integration.go) | 有界顺序队列和持久化后提交内存状态，旧代次先拒绝，能力降级取消视为预期。 |
| 107 | [internal/core/server/docker_storage.go](../internal/core/server/docker_storage.go) | 事务替换库存与健康 keepalive 分开；读取严格 JSON 并验证键与代次。 |
| 108 | [internal/core/server/enrollment_method_test.go](../internal/core/server/enrollment_method_test.go) | 原生/Docker/默认/非法安装方法及 token 不泄露进命令均有回归。 |
| 109 | [internal/core/server/file_tasks_api.go](../internal/core/server/file_tasks_api.go) | 完整审查文本内存载荷、容量锁、上传临时文件、流确认、会话撤销及任务观察；高容量重试计数作为进一步故障注入点，未当作已复现修复。 |
| 110 | [internal/core/server/files_api.go](../internal/core/server/files_api.go) | 修复下载失败无 Entry 时提前判为 502，改为先映射 Agent 错误，保留安全预览与摘要验证。 |

## 原清单中尚未全文深审的 69 个文件

- `internal/agent/compose/manager.go`
- `internal/agent/compose/manager_test.go`
- `internal/agent/compose/operations.go`
- `internal/agent/compose/task_executor.go`
- `internal/agent/containeractions/actions.go`
- `internal/agent/containeractions/engine.go`
- `internal/agent/containerrebuild/docker_engine.go`
- `internal/agent/containerrebuild/manager.go`
- `internal/agent/containerrebuild/store.go`
- `internal/agent/containerstreams/stats.go`
- `internal/agent/docker/discovery.go`
- `internal/agent/docker/engine.go`
- `internal/agent/systemd.go`
- `internal/agent/systemd_test.go`
- `internal/agent/taskjournal/journal.go`
- `internal/agent/taskjournal/recovery_test.go`
- `internal/core/alerts/delivery.go`
- `internal/core/alerts/model.go`
- `internal/core/alerts/security_test.go`
- `internal/core/alerts/store.go`
- `internal/core/alerts/template.go`
- `internal/core/docker/store.go`
- `internal/core/probes/store.go`
- `internal/core/probes/store_integration_test.go`
- `internal/core/server/files_transport.go`
- `internal/core/server/grant_api.go`
- `internal/core/server/guest_api_test.go`
- `internal/core/server/host_api.go`
- `internal/core/server/host_batch.go`
- `internal/core/server/host_features_test.go`
- `internal/core/server/host_retention_test.go`
- `internal/core/server/images_api.go`
- `internal/core/server/metrics_dashboard.go`
- `internal/core/server/node_asset.go`
- `internal/core/server/private_api.go`
- `internal/core/server/probe_api.go`
- `internal/core/server/rebuild_api.go`
- `internal/core/server/resource_limits_test.go`
- `internal/core/server/server.go`
- `internal/core/server/tailscale_api.go`
- `internal/core/server/tailscale_api_test.go`
- `internal/core/server/tailscale_host_test.go`
- `internal/core/server/task_api.go`
- `internal/core/server/task_bridge.go`
- `internal/core/server/terminal_stream.go`
- `internal/core/server/terminal_stream_test.go`
- `internal/core/server/updates_api.go`
- `internal/core/server/updates_api_test.go`
- `internal/core/server/websocket.go`
- `internal/core/tailscale/artifacts.go`
- `internal/core/tailscale/deploy.go`
- `internal/core/tailscale/deploy_stage6_test.go`
- `internal/core/tailscale/ssh.go`
- `internal/core/tailscale/state.go`
- `internal/core/tailscale/status.go`
- `internal/core/tailscale/status_test.go`
- `internal/core/tasks/local_test.go`
- `internal/core/tasks/tasks.go`
- `internal/i18n/errors.go`
- `internal/i18n/i18n.go`
- `internal/i18n/i18n_test.go`
- `internal/i18n/zh-CN.json`
- `internal/update/releases_test.go`
- `internal/update/tailscale_test.go`
- `web/scripts/audit-design.cjs`
- `web/scripts/file-license-notices.json`
- `web/src/locales/en.json`
- `web/src/locales/zh-CN.json`
- `web/tsconfig.json`
