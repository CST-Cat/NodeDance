# NodeDance 架构与模块边界（S00）

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
| `internal/core/config` | Core | 配置文件读取、CLI/环境/文件/默认优先级、监听地址安全检查 |
| `internal/core/cli` | Core | `serve`、信号、监听器创建；组合 Core Server |
| `internal/core/server` | Core | HTTP 静态资源和 `/api`、`/ws` 路由入口；不包含浏览器 UI 状态 |
| `internal/core/webassets` | Core/Web 接口 | `go:embed dist` 嵌入生产前端；仅由 Web 构建生成 |
| `internal/protocol` | 共享协议 | Core/Agent 共用的协议版本和后续消息类型；不依赖 Core 或 Docker 实现 |
| `web/` | Web | Vue/TypeScript 单页前端，只访问 Core 同端口；构建产物不反向成为 Go 源码依赖 |

依赖方向为 `cmd → internal/core → internal/protocol`；Agent 后续可依赖 `internal/protocol`，但不能导入 Core 的数据库或服务器实现。S00 仅有健康端点，未实现/未鉴权的管理路径都返回 404；管理数据路由在相应鉴权阶段完成前不能临时公开。

## HTTP 与前端生命周期

Web 由 Vite 编译到 `internal/core/webassets/dist`，Core 使用 Go `embed` 编入二进制。`dist` 是构建生成目录，加入 `.gitignore`，不作为用户手写源文件提交；干净 checkout 的 `make build` 必须先安装锁定依赖并构建前端，再编译 Core。正式运行只需一个 Core 二进制，不需 Node.js 或前端开发服务器。

`nodedance` 无参数及 `nodedance serve` 均按 `CLI > NODEDANCE_LISTEN > JSON 配置 > 127.0.0.1:8180` 解析。`net.Listen` 成功之后才写启动成功日志；冲突时错误退出，不自动换端口。`--dev` 仍只允许回环 IP。HTTP、API 和所有 WebSocket 路由共用这个 listener。

## 数据与信任边界

- 生产数据将由 Core SQLite WAL 持久保存。S00 没有真实资产库、会话库、管理 API 或 WebSocket 服务端。
- 浏览器 Session 和 Agent 设备凭据是两类凭据，不可互换。
- Core 是授权决策和任务状态的持久化端；Agent 是主机、Docker Engine 和 Compose CLI 交互端。
- Docker Engine 给出真实容器状态；偏好只保存 NodeDance 展示属性。
- 流式终端、日志和文件通道需要独立限额，但仍受同端口 Session/Agent 身份和审计保护。
- 本机 Docker Socket 实际等于高权限控制面，只有未来 Agent 在目标节点持有访问权限；CI 使用锁定镜像的专属 Docker-in-Docker socket，资源带唯一 `io.nodedance.suite` 标签，根目录只在仓库 `.artifacts` 下。
- DIND 测试不修改全局 Docker daemon 配置，不停止宿主 daemon，不执行 `docker system prune`，不操作无 NodeDance 唯一标签的其他容器/卷/网络。

## 当前 S00 与未实现边界

当前有 Core CLI/配置/HTTP入口、嵌入式最小 Web、共享协议版本常量和 Agent 命令占位程序。S01–S17 的业务实现都未完成，必须保持 `NOT_READY`；占位程序会明确返回该状态。工作流文件、空模块和数据模型不能表述成未来功能已实现。接口前缀由计划保留，但 S00 未注册的路径不可匿名返回业务数据。
