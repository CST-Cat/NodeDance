# NodeDance 架构与模块边界（S01）

## 目标架构

```text
桌面/移动浏览器
   ├─ HTTP：内嵌 Web 和 /api/v1/*
   └─ WebSocket：/ws/v1/dashboard 与受控流
                 │ 一个 Core 应用端口，默认 127.0.0.1:8180
                 ▼
           NodeDance Core ─── SQLite/WAL
                 ▲
                 │ Agent 主动 WebSocket；设备凭据独立于浏览器 Session
       ┌─────────┴─────────┐
       ▼                   ▼
  Linux Agent A        Linux Agent B
       ├─ 主机采集       ├─ 主机采集
       └─ Docker SDK     └─ Docker SDK / Compose CLI
```

这是一台 Core 对多个 Linux Agent 的架构。Core 托管单管理员 Web、HTTP API、WebSocket 通道、身份鉴别、资产/指标/历史存储、持久任务和审计。Agent 主动连接 Core；它在本机读取 Linux 指标、访问本机 Docker Engine，并在以后阶段执行经 Core 授权的命令。Agent 不建立管理监听端口。浏览器不能直连 Docker Socket。

## Go 包边界

| 目录 | 所属 | 责任与依赖方向 |
|---|---|---|
| `cmd/nodedance` | Core 可执行入口 | 参数/进程退出码；调用 `internal/core/cli` |
| `cmd/nodedance-agent` | Agent 可执行入口 | Agent 参数和进程入口；后续调用 Agent 模块 |
| `internal/core/config` | Core | XDG 数据/配置路径、CLI/环境/JSON/default 优先级、监听/Origin/代理/测试时间配置检查 |
| `internal/core/cli` | Core | `serve`、信号、监听器创建；组合 Core Server |
| `internal/core/server` | Core | HTTP 静态资源、管理员认证、Session/CSRF、受保护 API 和浏览器 WebSocket 会话检查 |
| `internal/core/storage` | Core | SQLite/WAL、顺序原子迁移、future ledger 拒绝和关闭 |
| `internal/core/auth` | Core | Argon2id 密码 verifier、随机 token 与单向摘要 |
| `internal/core/audit` | Core | 结构化 allowlist 审计事件，不接收任意请求字段 |
| `internal/core/webassets` | Core/Web 接口 | `go:embed dist` 嵌入生产前端；仅由 Web 构建生成 |
| `internal/protocol` | 共享协议 | Core/Agent 共用的协议版本和后续消息类型；不依赖 Core 或 Docker 实现 |
| `web/` | Web | Vue/TypeScript 单页前端，只访问 Core 同端口；构建产物不反向成为 Go 源码依赖 |

依赖方向为 `cmd → internal/core → internal/protocol`；Agent 后续可依赖 `internal/protocol`，但不能导入 Core 的数据库或服务器实现。S01 的公开端点仅包含健康、初始化状态/CSRF challenge 和登录页明确公开外观；私有 API、WebSocket 和未实现的 Agent 通道均先经服务端 Session 检查，未登录请求返回 401。登录页外观的已发布头像/背景使用固定公开路径；其他静态资源不能按任意上传 ID 匿名读取。

## HTTP 与前端生命周期

Web 由 Vite 编译到 `internal/core/webassets/dist`，Core 使用 Go `embed` 编入二进制。`dist` 是构建生成目录，加入 `.gitignore`，不作为用户手写源文件提交；干净 checkout 的 `make build` 必须先安装锁定依赖并构建前端，再编译 Core。正式运行只需一个 Core 二进制，不需 Node.js 或前端开发服务器。

`nodedance` 无参数及 `nodedance serve` 均按 `CLI > NODEDANCE_LISTEN > JSON 配置 > 127.0.0.1:8180` 解析。数据目录依次为 CLI、`NODEDANCE_DATA_DIR`、JSON 配置、XDG/home 默认值。`net.Listen` 成功之后才写启动成功日志；冲突时错误退出，不自动换端口。`--dev` 只允许回环 IP，并且只有该模式允许 HTTP Cookie 与短测试时间配置。生产 Cookie 始终 `Secure`；生产面板通过 HTTPS 反向代理访问，显式配置浏览器可见 `public_origin`，并且只有显式列入 `trusted_proxies` 的远端可以提供可信代理地址。无配置时不信任 XFF/XFP。浏览器和原生 API 写请求都必须提交 Origin 与 Session 绑定 CSRF token。

## 数据与信任边界

- Core 在 XDG 应用数据目录持久保存 SQLite/WAL、管理员 verifier、Session/CSRF 摘要、外观和审计；SQLite 迁移按顺序单事务执行，未来/非法 ledger 在启动写入前拒绝。正常退出关闭 HTTP 服务器与数据库。
- setup 状态公开响应不泄露绝对目录；首次 setup credential 只写入本机数据目录的 `0600` 文件，消费后删除。Core 日志只给出文件位置，不写入 credential 内容。
- 管理员密码使用 Argon2id、每次随机盐和固定版本化成本参数。Session Cookie 中只有随机 opaque token；数据库保存其摘要。CSRF challenge 用数据目录内 `0600` 签名密钥验证，已登录 CSRF 再与服务器 Session 摘要绑定。
- 登录限流在开始昂贵密码哈希前原子预留失败额度，限流表有容量上限并清理过期项；密码哈希并行任务有内存槽位上限。
- 审计按结构化 allowlist 只保存操作、结果、管理员 ID 和规范化远端 IP，不接受密码、token、Cookie、请求正文或任意元数据。
- 浏览器 Session 和 Agent 设备凭据是两类凭据，不可互换。
- Core 是授权决策和任务状态的持久化端；Agent 是主机、Docker Engine 和 Compose CLI 交互端。
- Docker Engine 给出真实容器状态；偏好只保存 NodeDance 展示属性。
- 流式终端、日志和文件通道需要独立限额，但仍受同端口 Session/Agent 身份和审计保护。
- 本机 Docker Socket 实际等于高权限控制面，只有未来 Agent 在目标节点持有访问权限；CI 使用锁定镜像的专属 Docker-in-Docker socket，资源带唯一 `io.nodedance.suite` 标签，根目录只在仓库 `.artifacts` 下。
- DIND 测试不修改全局 Docker daemon 配置，不停止宿主 daemon，不执行 `docker system prune`，不操作无 NodeDance 唯一标签的其他容器/卷/网络。

## 当前 S01 与未实现边界

当前实现 S00 基础与 S01 Core 管理员认证/会话/审计、登录外观和真实 SQLite 持久层。Agent 设备注册、节点/容器管理、指标、任务、Compose、文件、终端、告警和更新仍属后续 S02–S17，不能因浏览器入口或 Agent 占位程序而宣称已实现。所有未实现的私有 API/WS 仍在认证前拒绝并且认证后返回 404；Agent WebSocket 不继承管理员权限。
