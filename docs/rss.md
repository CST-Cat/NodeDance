# Core RSS／Atom 订阅

订阅中心运行在内置 Core 中。Core 持有订阅、条目、共享阅读状态、刷新状态和通知历史；Agent 不参与。支持 RSS 和 Atom，不执行条目里的命令，不自动安装更新，不抓取文章全文、图片、外部内容地址或附件。RSS 资讯不能代替正式 Release 的签名、校验和及更新操作。

## 刷新

默认 30 分钟，最低 1 分钟，最大 10080 分钟。预设为 1、5、10、15、30、60、180、360、720、1440、10080 分钟；允许该范围内任意整数分钟。摘要通知间隔也允许 1 至 10080 分钟，默认 60 分钟。刷新加 0–9 秒偏移；失败后有界退避，处理 HTTP Retry-After，故障时不会保证每分钟成功取得内容。

后台抓取最多四个并行，同一主机名只执行一个抓取。同一订阅的手动刷新与定时刷新共用至少一分钟的启动间隔；重复手动请求合并。`queued` 等待启动，`running` 表示执行中，`succeeded`／`failed`／`canceled` 表示上次结果，`inactive` 表示停用。取消终止当前请求并放弃迟到结果；启用的订阅之后继续其定时刷新。停用则停止自动刷新。

ETag／Last-Modified 保存在 Core，条件请求的 304 不重新解析或写入条目。当前刷新受 runId、配置修订号和运行状态共同约束。修改、停用和删除后，旧抓取结果不会提交。Core 重启恢复被中断的抓取与通知；抓取只进行 GET，不重放控制操作。数据库写入失败遗留的运行状态也通过有界运行租期重新排队。

## 条目与容量

RSS guid／Atom id 优先作为身份，其次文章链接，再次确定性内容摘要；身份包含订阅 ID。guid 不自动视为可请求地址。已有条目更新内容时保留阅读状态。首次导入不通知；后续新增、匹配关键词的条目才进入通知队列。

最多 200 个订阅，每次响应解压后最多 4 MiB、1000 个条目，XML 嵌套最多 64 层。每个订阅查询最近 500 个条目，分页最多 100 项；默认 Web 每页 50 项。仅最近 500 项保留完整摘要。身份和阅读元数据保留至最后观察后的 180 天，每个订阅至多 10000 项；超出这个有界去重窗口的旧条目可能再次被识别为新增。

发布时间和首次观察时间分开保存。源未提供发布时间时界面明确显示首次发现时间，不伪造发布日期。共享阅读状态可以由 Web、CLI 和 TUI修改。

修改源 URL 会清除该订阅的旧条目、缓存验证字段与初始化状态，并重新进行静默首次导入。其它配置修改保留条目和阅读状态。所有修改使用当前 revision 防止覆盖另一界面的编辑；删除要求 revision 和 confirm=true。订阅及其条目、通知记录在同一数据库中，现有完整 Core 备份／恢复会一并保存。

## 通知

支持关闭、即时、摘要。关键词每行一个，任意关键词匹配标题或纯文本摘要即可；无关键词则接受所有新增条目。渠道使用现有启用的 SMTP／Webhook 配置，凭据继续由原有 Core 通知密钥加密。RSS 使用独立投递记录，不进入告警触发／恢复状态机。

即时通知将当前新增条目组成批次；摘要按配置间隔发送，每批最多 50 个条目。单个订阅最多 100 个待处理投递，达到上限时暂停组装新批次。失败最多自动尝试六次，按分钟指数退避；最终失败可在通知历史中显式重试。历史接口显示最近 100 项，存储保留最近 500 个记录及待处理记录，完成记录最多保留 90 天。

修改、停用或删除会撤销该订阅待发消息并取消进行中的发送。已送达外部服务的消息不能撤回。进程可能在外部接收成功、记录成功之前退出，因此网络故障或重启后存在重复投递可能；不承诺端到端 exactly-once。

## 安全与权限

所有订阅接口为管理员私有操作，经相同会话、Origin 和 CSRF 校验。游客不发布订阅名称、地址、条目或渠道；管理员 cookie 也不会改变游客投影。URL 查询参数可能包含私密令牌，URL 只出现在私有配置和管理员界面，审计不记录它。含 URL 用户凭据或片段的地址拒绝。

只访问公开 HTTP／HTTPS 的 80／443 端口。连接时解析并检查全部 IPv4／IPv6 地址，使用已经验证的 IP 建立连接，同时保持原始主机名的 TLS 证书验证。拒绝回环、内网、链路本地、CGNAT、元数据和特殊地址；重定向最多两跳，逐跳应用相同策略。订阅抓取不采用环境代理，以免绕开地址校验。

请求有时限，响应和字段有上限，拒绝 DTD／实体声明。内容转为纯文本，Web 不渲染源站 HTML，不加载远程图片；文章链接只允许 HTTP／HTTPS，并使用 noreferrer 打开。UI、错误展示、审计和 SMTP 内容均保持不执行外部内容的边界。

## 三界面操作

所有操作从连接的 Core `/api/v1/operations` 发现：`feeds.options`、`feeds.list`、`feeds.create`、`feeds.get`、`feeds.update`、`feeds.delete`、`feeds.refresh`、`feeds.cancel`、`feeds.items`、`feeds.read`、`feeds.deliveries`、`feeds.retry-delivery`。

Web 的订阅中心提供专用界面，也可使用通用操作台。CLI 和 TUI 的通用界面自动覆盖同一操作、输入结构与执行代码。`feeds.refresh` 的 completion 指向 `feeds.get`；CLI 可用 --wait，TUI 可选择等待。取消、失败后再刷新、读取结果与通知重试都使用 Core 操作。

```sh
nodedance describe feeds.create
nodedance call feeds.create --body '{"name":"Project releases","url":"https://github.com/CST-Cat/NodeDance/releases.atom","enabled":true,"intervalMinutes":30,"keywords":[],"channelIds":[],"notificationMode":"off"}'
nodedance call feeds.items --params '{"id":"FEED_UUID"}' --query '{"offset":"0","limit":"50","unread":"true"}'
nodedance call feeds.refresh --params '{"id":"FEED_UUID"}' --wait
nodedance call feeds.cancel --params '{"id":"FEED_UUID"}'
nodedance call feeds.delete --params '{"id":"FEED_UUID"}' --body '{"revision":1,"confirm":true}'
```

## 验证入口

- `go test -race -exec 'sudo -n' ./...`：抓取、条件请求、恶意 XML、去重、通知、修订竞争、取消、恢复、真实 Core 操作契约与 CLI／PTY TUI。
- 新鲜 Web 构建后 `node web/scripts/audit-feeds.cjs`：临时真实 Core，增删、间隔、分页搜索、阅读状态、实际 SSRF 拒绝、取消、存储型脚本注入和中英文各四种明暗／移动布局。
- 新鲜 Web 构建后 `NODEDANCE_GUEST_ATTACKS=1 node web/scripts/audit-guest.cjs`：从运行 Core 发现新增操作，验证匿名拒绝、私有 RSS 投影排除及原有完整游客安全用例。

所有浏览器审查仅使用临时数据和隔离 Core。没有操作真实 Agent、远程资源或生产数据库。

### 本次验证结果

- 最终全项目 root race 测试通过：`/tmp/nodedance-feeds-delivery-all.log`；真实 Core／CLI／PTY TUI 包均通过。
- 最终 `go vet ./...` 通过：`/tmp/nodedance-feeds-complete-vet.log`。
- Web 类型、契约、1563 条双语消息、96 项文件依赖许可检查及生产构建通过。
- RSS 浏览器回归通过 11 类行为与 8 个中英文明暗／移动布局：`.build/feed-audit/report.json`。
- 新鲜构建上的完整游客安全回归通过：本次 461 个用例、零失败，动态发现 121 个私有操作；`.build/guest-audit/security-results.json`。
- 共享界面浏览器回归通过：本次发现 133 个远程操作，24 个界面布局及 12 个既有修复回归布局。

未递增产品、协议或能力版本，未执行正式发布。

The subscription list and entries load independently of interval options, notification channels and delivery history. A slow or failed history/channel read does not suppress successful entries. Initial selection reads entries once; search, unread filters and pagination do not reload delivery history, and superseded entry reads are canceled. Core still enforces subscription revisions, safe fetch targets and mutation semantics.
