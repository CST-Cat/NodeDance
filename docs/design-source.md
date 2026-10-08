> 来源说明：此文件保留用户最初粘贴的总体设计文本，供需求追溯。后续正式决议已将项目名统一为 NodeDance、默认端口改为 127.0.0.1:8180，并由 [plan.md](plan.md) 与 [plan-amendments.md](plan-amendments.md) 覆盖冲突内容。本文保留历史名称/示例不构成当前产品契约。

# NodeDeck 项目总体设计方案

**项目名称：NodeDeck**

**项目定位：Unified Server & Container Management Platform（服务器与容器统一管理平台）**

**正式毕设题目：**《基于 Core-Agent 架构的多节点 Linux 服务器与 Docker 容器统一管理系统的设计与实现》

NodeDeck 是一个面向多台 Linux VPS 的 Web 运维管理平台。系统以 Komari 的服务器监控与卡片展示方式为基础，结合 1Panel 的 Docker 管理能力、XPipe 的远程连接管理思路、Tailscale 的私有网络能力和 OpenSSH 的远程运维能力，实现对服务器及其 Docker 容器的集中监控与管理。

整个项目坚持四项原则：**服务器与容器同等重要、Web 界面作为统一入口、Agent 负责实际管理、功能完整但不盲目扩张。** 用户安装 Core 后，启动 Web 服务并绑定指定端口，在每台 VPS 安装 Agent，即可通过电脑、平板或手机浏览器统一管理服务器和 Docker 应用。

这是一份尚未开始开发的项目设计方案，不代表已经完成任何功能。以下区分必须实现的主体功能、建议实现的增强功能和暂不纳入开发范围的能力。

## 一、产品定位与现有软件的关系

重新核对 Komari 和 1Panel 的官方资料后，我认为 NodeDeck 应当借鉴两者各自最成熟的部分，而不是把多个产品的功能机械拼接在一起。

Komari 的优势在于集中展示 VPS 的运行状态，其 Agent 支持持续上报 CPU、内存、磁盘、网络、运行时间等信息，并通过 WebSocket 与服务端通信。1Panel 则提供 Docker 容器信息、日志、控制台、资源统计和 Compose 项目管理能力。[GitHub](https://github.com/komari-monitor/komari?utm_source=chatgpt.com)

| 参考项目 | 主要借鉴内容 | NodeDeck 的具体设计 |
|---|---|---|
| **Komari** | VPS 监控卡片、实时状态、历史数据、Agent 通信 | 以 VPS 为核心组织整个 Web 界面 |
| **1Panel** | Docker、Compose、日志、容器管理 | 在每台 VPS 下直接管理 Docker 资源 |
| **XPipe** | 远程连接管理与终端体验 | 支持远程终端、节点连接信息和访问入口 |
| **Tailscale** | 私有网络、节点发现、安全连接 | 优先使用 tailnet，普通 HTTPS 作为可选方案 |
| **OpenSSH** | 远程登录、文件传输、系统维护 | 用于 Agent 首次部署和独立故障救援 |

**NodeDeck 最核心的产品区别，是将 VPS 监控与 Docker 应用管理合并到同一个服务器视图。**

传统监控系统主要回答“服务器是否正常运行”，而 NodeDeck 还要回答“这台服务器上有哪些 Docker 应用、监听哪些 IP 和端口、运行了多长时间、是否健康，以及怎样直接管理它们”。

因此，NodeDeck 的首页不应设计成普通的管理后台菜单，也不应将 Docker 隐藏在多层页面之下。**每张 VPS 卡片本身就应该包含对应 Docker 应用的状态与管理入口。**

## 二、系统总体架构

### 2.1 采用 Core-Agent 分布式架构

**架构确定为：一个中心 Core，多个被控 Agent，所有用户操作通过统一 Web 页面完成。**

```text
                      NodeDeck Web
             Windows / macOS / Linux
                  iPadOS / Android
                         │
                  HTTPS / WSS
                         │
                  NodeDeck Core
                 Go + SQLite + Web
                         │
         ┌───────────────┼───────────────┐
         │               │               │
     节点管理        状态与监控        操作任务
     用户鉴权        历史记录          审计日志
         │               │               │
         └───────────────┼───────────────┘
                         │
                WSS 双向通信通道
                         │
             Tailscale 优先连接
                         │
          ┌──────────────┼──────────────┐
          │              │              │
        VPS A          VPS B          VPS C
          │              │              │
        Agent          Agent          Agent
          │              │              │
      ┌───┴───┐      ┌───┴───┐      ┌───┴───┐
      │       │      │       │      │       │
    Linux   Docker  Linux   Docker  Linux   Docker
              │               │              │
        ┌─────┴─────┐         │          Containers
        │           │         │
    Containers    Compose   Containers
```

系统分为三个主要组成部分。

**NodeDeck Core：** 部署在一台 Linux 主控机上，提供 Web 页面、HTTP API、WebSocket 通信、管理员鉴权、节点注册、监控历史、Docker 资产信息和操作任务管理。Core 不需要安装 Docker，也不需要在本机运行被管理的业务容器。

**NodeDeck Agent：** 每台被管理的 VPS 都必须安装。Agent 由 systemd 管理，负责采集主机资源、发现 Docker 容器、查询容器状态、订阅 Docker Events、执行经过授权的远程操作，以及向 Core 上报结果。如果被控机没有 Docker，Agent 仍然正常提供 Linux 主机管理能力。

**NodeDeck Web：** 统一的浏览器管理入口，负责显示 VPS、Docker、Compose、历史监控以及管理操作。Windows、macOS、Linux、iPadOS 和 Android 均通过浏览器访问同一个服务，不单独开发桌面客户端或移动 App。

Komari 的官方 Agent 文档已验证了通过 WebSocket 持续上报主机指标、处理服务端控制消息的可行性。NodeDeck 采用相似的通信组织方式，但自行设计协议及 Docker 管理模块。[Komari Monitor](https://komari-monitor.github.io/komari-document/dev/agent.html?utm_source=chatgpt.com)

### 2.2 通信与网络设计

**Core 对外只提供一个 NodeDeck 应用服务端口。** Web 页面、REST API、浏览器 WebSocket 和 Agent WebSocket 使用同一个 HTTP 服务入口，通过路径和独立鉴权机制区分。

例如，规划以下地址结构：

| 路径 | 用途 |
|---|---|
| `/` | NodeDeck Web 页面 |
| `/api/auth/*` | 管理员登录与 Session |
| `/api/nodes/*` | VPS 节点信息 |
| `/api/containers/*` | Docker 管理 |
| `/api/compose/*` | Compose 管理 |
| `/api/tasks/*` | 远程操作任务 |
| `/ws/dashboard` | 浏览器实时状态推送 |
| `/ws/agent` | Agent 与 Core 通信 |

这些是拟定接口路径，开发前仍可调整。所有非公开接口都必须执行服务端鉴权。

**Tailscale 为优先使用的网络，但不是强制依赖。** 建议 Core 默认监听 `127.0.0.1:3080`，通过 Tailscale Serve 提供 tailnet 内的 HTTPS 入口。也支持用户自行配置 LAN 地址、反向代理或经过安全配置的公网 HTTPS。

Tailscale 官方说明，Serve 可以将 tailnet 内的 HTTPS 请求转发到本地 Web 服务，同时遵守 tailnet 访问控制。[Tailscale](https://tailscale.com/docs/features/tailscale-serve?utm_source=chatgpt.com)

Agent 的连接策略为：优先通过配置好的 Tailscale 地址或域名连接 Core；只有用户明确允许时，才通过备用 HTTPS 地址连接。如果 Tailscale 不可用，系统可以独立运行，但仍必须使用可信 TLS 和 Agent 身份认证。

由于 Agent 主动连接 Core，正常管理过程中不需要每台 VPS 额外监听一个 NodeDeck Agent 网络端口。

### 2.3 OpenSSH 的职责

OpenSSH 继续保留，但不承担日常 Docker 状态同步。

它负责首次通过 SSH 安装 Agent、管理员直接登录 VPS，以及 Agent 故障时独立救援。正常情况下，NodeDeck 的 Web 终端可以由 Agent 创建本地 PTY 并通过受控会话转发，这不等同于 SSH 协议会话。

因此，即使 Agent 出现故障，用户仍可以使用已有的 XPipe、OpenSSH 客户端或 Tailscale SSH 登录服务器。

**Agent 是 NodeDeck 的必要组件，SSH 是独立的救援与维护通道，不开发两套重复的 Docker 管理后端。**

---

## 三、Web 页面设计：Komari 风格，VPS 和 Docker 融合展示

这是整个项目最重要的产品设计部分。

### 3.1 未登录：专属登录页面

NodeDeck 默认不允许匿名查看任何监控数据。访问 Web 页面时，如果没有有效 Session，只显示：

```text
┌─────────────────────────────────────┐
│                                     │
│                                     │
│              [ 头像 ]               │
│                                     │
│           工仔小猫 GT               │
│                                     │
│             NodeDeck                │
│                                     │
│       ┌─────────────────────┐       │
│       │     输入登录密码     │       │
│       └─────────────────────┘       │
│                                     │
│              [ 登录 ]               │
│                                     │
│                                     │
└─────────────────────────────────────┘
```

头像由管理员上传，名称、头像、背景和主题颜色允许自定义。为了保持简单，整个系统仅设置一个管理员账户，默认只显示密码输入框。

未登录时，既不能进入 Dashboard，也不能通过 API、WebSocket 或静态缓存获取 VPS、容器、IP 地址、监控数据等私有信息。头像和展示名称属于管理员主动公开的登录页资源。

### 3.2 登录后：VPS 统一监控首页

首页直接借鉴 Komari 的卡片布局，而不采用 1Panel 那种以功能菜单为中心的整体页面结构。

**每一张 VPS 卡片同时展示主机监控数据和主要 Docker 应用。**

```text
NodeDeck                                  工仔小猫 GT
────────────────────────────────────────────────────
全部节点  12     在线  11     离线  1
Docker   46     运行  42     停止  4

[全部节点] [香港] [日本] [美国]           [搜索]

┌──────────────────────────────────────────────────┐
│ 🇭🇰 Hong Kong 01                       ● 在线    │
│ Debian 14 · 4 vCPU · 8 GB RAM                    │
│                                                  │
│ CPU       13%       ███░░░░░░░                   │
│ Memory    42%       █████░░░░░                   │
│ Disk      31%       ███░░░░░░░                   │
│                                                  │
│ 网络 ↑ 1.2 MB/s    ↓ 3.6 MB/s                     │
│ VPS 已运行 23 天 14 小时                          │
│                                                  │
│ Docker 运行 3 / 4                                │
│                                                  │
│ ★ 密码保险库                           ● Running │
│   vaultwarden · 已运行 12 天                      │
│   127.0.0.1:8080 → 80/TCP                        │
│   [停止] [重启] [日志]                            │
│                                                  │
│ ★ Git 服务                             ● Running │
│   gitea · 已运行 8 天                             │
│   100.101.20.3:3000 → 3000/TCP                   │
│   [停止] [重启] [日志]                            │
│                                                  │
│   Redis                                ● Running │
│   127.0.0.1:6379 → 6379/TCP                      │
│                                                  │
│   Uptime Kuma                          ○ Exited  │
│   已停止 2 小时                                   │
│   [启动] [日志]                                   │
│                                                  │
│ [查看全部 Docker] [VPS 详情] [终端]                │
└──────────────────────────────────────────────────┘
```

以上均为拟定界面和示例数据。

**首页只显示重要容器，默认展示前三至五个。** 用户可以自行决定展示哪些容器、调整顺序，以及是否在首页出现快捷操作。其余容器进入对应 VPS 页面后查看，避免一台拥有几十个容器的 VPS 将首页无限撑长。

同时提供「监控视图」和「管理视图」：前者优先展示 CPU、内存、磁盘和网络曲线，后者展示更多 Docker 信息及快捷操作。两种视图使用同一套数据，不需要分别开发两套页面。

### 3.3 VPS 详情页

点击某台 VPS 后，进入该节点的完整管理页面。顶部保持 VPS 名称、在线状态、系统版本和主要监控指标，下面通过标签切换功能。

| 页面标签 | 功能 |
|---|---|
| 总览 | CPU、内存、磁盘、网络、运行时间、历史曲线 |
| Docker | 所有容器、状态、IP、端口、日志与操作 |
| Compose | 项目列表、服务关系、配置编辑与部署 |
| 终端 | 通过 Agent 访问 VPS 终端 |
| 文件 | 文件浏览、上传下载、文本编辑 |
| 设置 | VPS 名称、分组、显示顺序、Agent 信息 |

Docker 列表必须支持搜索、筛选、排序和自定义展示字段。点击容器名称后进入容器详情，可以查看完整端口映射、资源使用情况、启动时间、退出记录、日志、网络和挂载信息。

手机端依然使用同一套 Web，但 VPS 卡片采用单列布局，Docker 操作集中到菜单或详情页，降低误触风险。响应式 Web 能覆盖各平台浏览器，但交互式终端、文件上传、触屏排序等仍需分别进行兼容性测试。

---

## 四、Docker 管理：自动发现所有现存容器

### 4.1 Docker Engine 是唯一的实时状态来源

**不能依赖 Compose 配置文件，也不能要求 Docker 容器必须由 NodeDeck 创建。**

用户通过 `docker run`、`docker compose up` 或其他 Docker API 客户端创建的容器，只要存在于目标 Docker Engine 中，Agent 都必须能够发现。

Docker Engine API 提供容器列表接口，其中 `all=true` 可以包含已停止的容器。Docker 官方 Go SDK 也可以直接查询容器、控制其生命周期和获取统计信息。[Docker Documentation](https://docs.docker.com/reference/api/engine/version/v1.51/?utm_source=chatgpt.com)

Agent 首次连接 Docker 时进行完整扫描，之后订阅 Docker Events，监听容器创建、启动、停止、退出、重命名和健康状态变化。同时定期重新扫描，以补偿断线期间遗漏的事件。Docker 官方明确提供这些事件类型，但事件历史不能作为无限期可靠存储，因此仍需要定期校验。[Docker Documentation](https://docs.docker.com/reference/cli/docker/system/events/?utm_source=chatgpt.com)

### 4.2 独立 Docker 与 Compose 容器统一展示

| 容器来源 | 是否自动发现 | 是否支持基础管理 | 是否支持编排管理 |
|---|---|---|---|
| `docker run` 创建 | 支持 | 支持 | 不适用 |
| Docker Compose 创建 | 支持 | 支持 | 配置文件可用时支持 |
| Docker API 创建 | 支持 | 支持 | 取决于所属编排方式 |
| Compose 文件已经丢失 | 支持 | 支持 | 不允许直接编辑缺失的配置 |
| 已停止但仍存在 | 支持 | 支持 | 根据类型决定 |
| 已删除容器 | 当前列表不显示 | 不可操作 | 保留已采集的历史记录 |

Agent 利用 `com.docker.compose.project` 和 `com.docker.compose.service` 标签识别 Compose 项目和服务。这两个标签由 Compose 官方定义。[Docker Documentation](https://docs.docker.com/reference/compose-file/services/?utm_source=chatgpt.com)

对于缺少 Compose 文件的容器，仍然可以管理启动、停止、重启、日志和其他独立容器级操作，但不能凭空还原完整编排配置。

### 4.3 Docker 名称：显示别名与真实名称分离

NodeDeck 支持两种不同的名称。

**显示别名：** 例如把 `vaultwarden` 显示为「密码保险库」。保存在 NodeDeck 数据库中，只影响界面，不修改 Docker。

**Docker 实际名称：** 对允许重命名的独立容器，通过 Docker Engine 执行真实重命名，修改结果应能通过 `docker ps` 查看。Docker 官方支持 `docker rename` 操作。[Docker Documentation](https://docs.docker.com/reference/cli/docker/container/rename/?utm_source=chatgpt.com)

Compose 容器不应随意修改实际名称，避免干扰 Compose 的生命周期管理。需要调整 Compose 定义时，应通过项目配置进行。

用户还可以自定义容器图标、备注、置顶状态、展示顺序和常用服务入口。这些属于 NodeDeck 自己保存的偏好，不写入 Docker 的运行配置。

### 4.4 Docker IP 和端口：必须完整、直接显示

这是 NodeDeck 的重点功能。

| 容器示例 | 宿主机绑定地址 | 端口映射 | 界面说明 |
|---|---|---|---|
| Vaultwarden | `127.0.0.1` | `8080 → 80/TCP` | 绑定回环地址 |
| Gitea | `100.101.20.3` | `3000 → 3000/TCP` | 绑定指定 Tailscale IP |
| Nginx | `0.0.0.0` | `80 → 80/TCP` | 绑定所有 IPv4 接口 |
| DNS | `0.0.0.0` | `53 → 53/UDP` | UDP 端口映射 |
| Redis | 未发布 | `6379/TCP` | 未发布宿主机端口 |

Agent 使用 Docker Inspect 获取 `NetworkSettings.Ports` 等结构化信息，兼容 TCP、UDP、IPv4、IPv6 及多个端口映射。Docker 官方也规定了 `HOST_IP:HOST_PORT:CONTAINER_PORT` 的发布方式。[Docker Documentation](https://docs.docker.com/reference/cli/docker/inspect?utm_source=chatgpt.com)

这里必须准确区分三种情况：

**已发布的端口**可以展示实际宿主机绑定地址和端口；**仅声明 EXPOSE 的端口**不代表已经发布到宿主机；**Host Network 容器**共享宿主机网络命名空间，不能按普通端口映射处理。

对已经停止的容器，可以展示保存的端口配置，但必须标明当前没有提供运行中的容器服务。NodeDeck 应分别表示「配置的端口」和「当前有效的端口映射」。

同时，网页不能假定 `127.0.0.1` 是用户当前设备可以直接访问的 VPS 地址。服务快捷入口需要依据实际可访问地址配置，避免生成错误链接。

### 4.5 Docker 运行时间与状态

NodeDeck 必须分别记录以下状态：

| 状态类型 | 需要展示的信息 |
|---|---|
| VPS / Agent | 在线、失联、最后上报时间、失联时长 |
| Docker Engine | 正常运行、停止、无法访问 |
| Docker 容器 | Running、Exited、Paused、Restarting 等 |
| Docker Healthcheck | Healthy、Unhealthy、Starting、未配置 |
| 容器时间信息 | 创建时间、启动时间、退出时间、持续运行或停止时长 |

容器运行时间依据 Docker 真实启动时间计算；容器停止时长依据最后退出时间计算。

**Agent 失联不等于 VPS 关机，容器 Running 也不等于应用健康。** 当 Agent 失联时，Core 只能展示最后一次已知的 Docker 状态，并明确标记数据已经过期，不能继续将其作为实时状态。

### 4.6 容器操作和修改端口

Docker 容器必须支持启动、停止、重启、暂停、恢复、日志查看、资源统计和删除。删除操作需要二次确认。镜像管理支持查看、拉取和删除。

端口修改则需要区别处理：

**Compose 项目：** 网页修改 Compose 文件中的端口映射，执行配置校验和备份，然后由 Agent 在正确的项目目录重新创建受影响容器，最后查询实际绑定结果。

**独立容器：** 不允许假装通过普通 Docker Update 就能直接修改端口。由于 Docker 创建后不能通过 `docker update` 直接修改发布端口，NodeDeck 需要设计受控的容器重建流程，保留原有网络、挂载、环境和启动参数，并提前提示服务中断与数据风险。[Docker Documentation](https://docs.docker.com/reference/cli/docker/container/update/?utm_source=chatgpt.com)

独立容器的一键端口修改可以列为后续开发重点，但它不能成为阻碍基础容器管理功能交付的前置条件。

### 4.7 自定义容器顺序

每台 VPS 的 Docker 展示顺序独立保存，支持拖动排序、置顶、按名称、按状态和按运行时间排序。

对于 Compose 服务，使用节点 ID、项目名称、服务名称建立稳定的展示关联，避免重新创建容器后因为 Container ID 改变而丢失设置。对外部创建的独立容器，则结合实际名称和可验证的身份信息恢复偏好，不能在无法确认身份时错误继承原容器设置。

这些排序只影响 Web 展示，不改变容器执行次序或 Compose 的服务依赖关系。

---

## 五、鉴权系统：默认完全私有，单管理员设计

NodeDeck 不采用复杂的多用户权限管理系统，但必须保证后台数据和远程操作的安全性。

### 5.1 登录机制

使用**单管理员账户、密码认证、服务端 Session** 的方案。

| 项目 | 设计方案 |
|---|---|
| 管理员数量 | 一个 |
| 登录界面 | 头像、展示名称、密码输入框 |
| 密码存储 | Argon2id 加盐哈希 |
| 登录状态 | 服务端 Session |
| 浏览器凭据 | HttpOnly、Secure、SameSite Cookie |
| Session 数据 | SQLite |
| 登录有效期 | 可配置，默认建议空闲 12 小时过期 |
| 主动退出 | 服务端立即撤销 Session |
| 登录失败保护 | 请求限速与失败次数限制 |
| OAuth / LDAP | 不实现 |
| Redis | 不需要 |
| 多用户权限管理 | 暂不实现 |

之所以不选择 JWT，是因为 NodeDeck 只有一个 Core，没有跨服务共享用户认证状态的需求。服务端 Session 更容易完成主动退出、密码修改后撤销旧会话、管理已登录设备等操作。

正常业务请求只需要验证会话，不需要重新执行 Argon2id 密码计算，因此不会因为密码哈希算法的计算成本而明显增加监控页面的运行开销。

OWASP 建议采用随机 Session 标识、服务端有效期检查和安全 Cookie 属性，并在注销时撤销服务端会话。[OWASP Cheat Sheet Series](https://cheatsheetseries.owasp.org/cheatsheets/Session_Management_Cheat_Sheet.html?utm_source=chatgpt.com)

### 5.2 浏览器认证与 Agent 认证分离

管理员通过密码登录 Web，Agent 则使用独立的设备凭据与 Core 通信。

首次安装 Agent 时，由 Core 生成具有有效期的一次性注册凭据；Agent 注册成功后获得独立设备身份，后续使用长期设备凭据连接。Core 可以撤销特定 Agent 的授权，不必修改整个系统的管理员密码。

**管理员退出网页不影响 Agent 持续监控。** Core 即使没有任何浏览器连接，也应继续接收各 VPS 的状态信息并保存历史记录。

所有 Docker 写操作必须经过 Core 的服务端授权检查。WebSocket 还需要检查来源、会话有效期和授权状态，不能因为连接已经建立就永久信任客户端。[OWASP Cheat Sheet Series](https://cheatsheetseries.owasp.org/cheatsheets/WebSocket_Security_Cheat_Sheet.html?utm_source=chatgpt.com)

### 5.3 Docker 管理权限

NodeDeck 的 Agent 需要管理本机 Docker Engine，这意味着安全设计不能只停留在登录页面。

Docker 官方明确说明，拥有 Docker daemon 完整控制能力的用户通常能够获得接近宿主机 root 的权限。即使将 Agent 放进普通用户组，只要该用户具有完整 Docker Socket 访问权，也不能将其视为真正的低权限账户。[Docker Documentation](https://docs.docker.com/engine/security/?utm_source=chatgpt.com)

因此，Agent 应将主机指标采集与 Docker 管理逻辑分开；普通监控尽可能使用低权限，而 Docker 控制能力需要明确授权。所有容器删除、Compose 部署、文件修改和终端操作都必须记录审计日志。

同时，NodeDeck 不应向公网暴露 Docker Engine API，浏览器也不能直接操作 Docker Socket。所有管理请求都经过 Core 鉴权，再由指定 Agent 在本机执行。

---

## 六、Core-Agent 数据同步机制

NodeDeck 要做到真正实用，关键在于**不管用户通过 Web、SSH、XPipe 还是其他 Docker 管理工具修改容器，NodeDeck 都能够自动同步真实状态**。

### 6.1 状态同步

建议采用三种同步方式配合：

| 同步方式 | 触发时机 | 主要内容 |
|---|---|---|
| 完整同步 | Agent 首次上线、重新连接、定期校验 | 所有现存容器及其状态 |
| 事件同步 | Docker Events 触发 | 创建、启动、停止、删除、重命名、健康变化 |
| 按需查询 | 用户打开容器详情或执行操作 | 完整配置、日志、资源使用、端口信息 |

完整同步用于建立和校验当前状态，事件同步用于减少状态变化的延迟，按需查询则避免持续传输大量不经常使用的详细信息。

例如，用户通过 SSH 执行：

```bash
docker run -d --name nginx-test -p 8080:80 nginx
```

Agent 应检测到新容器，然后自动同步容器名称、镜像、状态和端口绑定。NodeDeck 首页随即出现对应服务。

用户之后执行：

```bash
docker stop nginx-test
```

Agent 应通过 Docker Events 获取停止事件，重新查询状态，并向 Core 上报实际停止时间。网页同步更新为 Exited。

如果用户通过 NodeDeck 网页启动或停止容器，也使用同一套状态同步机制，避免出现“网页显示运行，但 Docker 实际已经停止”的情况。

### 6.2 操作任务机制

远程 Docker 操作必须具有完整的执行状态。

我建议采用：

```text
用户提交操作
      ↓
Core 验证管理员权限
      ↓
创建操作任务
      ↓
发送至指定 Agent
      ↓
Agent 调用 Docker API
      ↓
返回执行结果
      ↓
重新检查 Docker 状态
      ↓
Core 更新数据
      ↓
Web 显示最终结果
```

任务至少具有等待执行、正在执行、执行成功、执行失败、超时等状态。

对于 Docker 镜像拉取和 Compose 部署等长时间操作，可以显示进度与执行日志。对于停止、重启等操作，应在任务完成后重新查询 Docker 状态，不能把“命令已发送”当成“服务已恢复”。

Core 与 Agent 断线时，不允许将未确认执行结果的非幂等操作直接无限重试。任务需要唯一标识，Agent 应能够识别重复请求。

### 6.3 监控更新频率

建议采用以下初始参数，后续通过实测调整。

| 数据 | 建议更新方式 |
|---|---|
| CPU、内存、网络速率 | Agent 每 3～5 秒采样 |
| VPS 基础监控页面 | Web 实时接收更新 |
| 磁盘使用情况 | 每 30 秒采集 |
| Docker 容器状态 | 事件驱动 + 周期校验 |
| Docker 详细资源统计 | 打开相关页面后按需订阅 |
| 历史监控 | 按分钟聚合保存 |
| Agent 在线状态 | 心跳与连接状态共同判断 |
| 容器历史事件 | 发生变化时持久化 |

这里有一个重要设计原则：**高频数据用于实时展示，聚合数据用于长期保存。**

不需要让 Core 每秒将所有 VPS 的全部原始监控数据写入 SQLite，也不需要对每个 Docker 容器长期订阅完整资源统计。可以根据用户是否正在查看相关页面，动态调整采集与推送方式。

---

## 七、数据库设计

推荐 SQLite，配合 WAL 模式处理日常读取和写入。不引入 MySQL、PostgreSQL 或 Redis 作为 NodeDeck 的运行前置条件。

核心数据模型可以控制在以下范围：

| 数据实体 | 主要职责 |
|---|---|
| `nodes` | VPS 基本信息、分组、显示设置 |
| `agents` | Agent 身份、版本、在线状态 |
| `node_metrics` | VPS 历史性能数据 |
| `containers` | 当前 Docker 容器状态与所属节点 |
| `container_preferences` | 容器别名、图标、排序、置顶 |
| `container_events` | 容器历史事件 |
| `compose_projects` | Compose 项目与配置文件位置 |
| `tasks` | 远程管理任务及执行结果 |
| `admin` | 管理员账户及密码哈希 |
| `sessions` | 登录会话 |
| `audit_logs` | 用户操作审计 |

**NodeDeck 不应将 Docker 运行状态与个性化配置混合保存。**

Docker 状态属于 Agent 从 Docker Engine 获取的权威数据；别名、图标和排序属于 NodeDeck 自己维护的持久化配置。

当容器被删除时，当前资源记录可以转入历史状态，但不能将它继续显示成仍然存在的 Docker 容器。用户自定义展示配置可根据保留策略暂存，以便后续重新关联。

历史监控还需要设置数据保留期限和清理规则，避免长期运行导致 SQLite 数据文件无限增长。

---

## 八、技术栈确定

考虑到你的 Debian 使用习惯、单端口 Web 服务目标以及 Core-Agent 架构，我建议使用 Go 实现 Core 和 Agent，Vue 实现前端。

| 层次 | 技术选型 | 主要用途 |
|---|---|---|
| Core 后端 | Go | HTTP API、WebSocket、任务调度 |
| Agent | Go | Linux 采集、Docker SDK、远程执行 |
| 前端 | Vue 3 + TypeScript | Web 页面 |
| 前端构建 | Vite | 编译前端资源 |
| UI 组件 | Naive UI | 后台交互组件 |
| 图表 | Apache ECharts | VPS 历史监控与实时曲线 |
| Web 终端 | xterm.js | 浏览器终端 |
| 数据库 | SQLite | 资产、监控、会话、审计 |
| Docker 管理 | Docker 官方 Go SDK | 容器发现、生命周期、事件 |
| Compose 管理 | Docker Compose CLI | 项目部署与配置管理 |
| 通信 | HTTPS + WebSocket | 用户访问与 Agent 双向通信 |
| 服务管理 | systemd | Core 和 Agent 常驻运行 |
| 私有网络 | Tailscale | 可选私网连接与访问入口 |

Docker 官方提供 Go SDK，并支持 Docker Engine API 版本协商，这对管理安装不同 Docker 版本的 VPS 很有用。[Docker Documentation](https://docs.docker.com/reference/api/engine/sdk/?utm_source=chatgpt.com)

### 关于运行依赖

Core 编译后应将 Vue 前端静态资源嵌入 Go 可执行程序。用户不需要在运行环境中安装 Node.js，也不需要额外启动一个前端 Web 服务器。

Agent 则编译为独立 Linux 程序，并通过本机 Docker Unix Socket 访问 Docker Engine。Compose 管理要求目标机已安装兼容的 Docker Compose 插件。

优先支持 Linux amd64 和 arm64，其他架构根据实际编译与运行测试结果增加，不能仅凭 Go 支持某种架构就认定 Docker SDK、Compose 和全部系统采集功能已经兼容。

---

## 九、安装和使用体验

### 9.1 Core：安装后立即启动 Web

计划提供官方构建的可执行文件，以及可选的安装脚本。

首次使用的命令接口可以设计为：

```bash
nodedeck serve --listen 127.0.0.1:3080
```

这是预期使用方式，不代表现在已经有对应程序。

Core 启动后，浏览器访问：

```text
http://127.0.0.1:3080
```

首次初始化管理员密码，上传头像、设置展示名称，然后进入 NodeDeck。

正式部署时，推荐通过 Tailscale Serve 或可信反向代理提供 HTTPS。

监听地址必须由用户配置，支持回环地址、指定 LAN IP 和 Tailscale IP，不应强制写死为 `127.0.0.1`，也不应默认暴露到所有公网接口。

### 9.2 Agent：安装一次，自动上线

用户可以通过 Web 页面生成对应节点的安装命令。

```bash
nodedeck-agent enroll \
  --server https://panel.example.ts.net \
  --token <ONE_TIME_TOKEN>
```

Agent 注册后保存设备身份，创建 systemd 服务，自动启动并连接 Core。

进入 NodeDeck 后，即可看到新增 VPS 的基本信息、在线状态和 Docker 容器。

Agent 可以通过独立安装脚本部署，也可以在用户授权后由 Core 借助 SSH 安装。自动部署只是改善安装体验，不改变 Agent 必须安装的设计要求。

整个产品最终追求的部署体验是：

**Core 安装一次，每台 VPS 安装一个 Agent，然后统一从 Web 页面管理。**

---

## 十、功能范围与开发优先级

现在必须控制实际工作量，避免在尚未开发时就把项目扩展成完整的 1Panel 替代品。

### 10.1 核心交付功能

| 功能 | 优先级 | 验收标准 |
|---|---|---|
| Core-Agent 通信 | P0 | 多 VPS 正常注册、上线、离线检测 |
| Web 单管理员鉴权 | P0 | 未登录无法访问任何管理数据 |
| Komari 风格首页 | P0 | VPS 卡片与主要 Docker 同屏展示 |
| VPS 监控 | P0 | CPU、内存、磁盘、网络、运行时间 |
| Docker 全量发现 | P0 | 自动发现独立容器与 Compose 容器 |
| Docker 状态同步 | P0 | 外部命令产生的状态变化能同步显示 |
| IP 与端口展示 | P0 | 正确区分绑定地址、端口和协议 |
| 容器启停管理 | P0 | 启动、停止、重启及结果确认 |
| 容器运行时间 | P0 | 显示启动、退出和停止持续时间 |
| 自定义别名与排序 | P0 | 修改后持久保存，重启 Core 不丢失 |
| Docker 日志 | P0 | 能查看历史日志与实时输出 |
| Docker Compose | P0 | 识别项目，并管理配置可用的项目 |
| 操作审计 | P0 | 记录关键管理操作及执行结果 |

### 10.2 增强功能

| 功能 | 优先级 | 说明 |
|---|---|---|
| Compose 配置编辑与端口修改 | P1 | 校验、备份、重建及结果确认 |
| Docker 镜像管理 | P1 | 镜像查看、拉取和删除 |
| Web 终端 | P1 | Agent 转发交互式终端 |
| Web 文件管理 | P1 | 浏览、上传下载、文本编辑 |
| Tailscale 自动发现 | P1 | 发现可见节点并辅助部署 Agent |
| 独立容器重建向导 | P1 | 修改端口前检查配置与数据风险 |
| 容器服务探测 | P2 | HTTP/TCP 健康检查 |
| 监控告警 | P2 | 异常通知与告警历史 |
| Agent 自动更新 | P2 | 经过验证的更新与失败处理 |

P0 是完成整个项目主体的必要条件。P1 是后续优先完成的内容，并不要求与 P0 同时实现。P2 则属于可以根据毕设剩余时间增加的能力。

Kubernetes、Docker Swarm、多租户、插件市场、完整建站系统、证书管理平台、AI 运维决策和云厂商控制台整合，暂不纳入项目范围。

---

## 十一、测试与毕业设计验收

NodeDeck 必须通过真实的多 VPS 环境验证，而不只是通过截图展示页面。

建议至少准备三台 Linux 测试节点，其中一台拥有多个通过 `docker run` 创建的独立容器，一台拥有 Compose 项目，另一台用于模拟故障和异常情况。

测试内容包括：

| 测试场景 | 预期结果 |
|---|---|
| 全新 Agent 安装 | 正确注册并出现在 Web 首页 |
| VPS 不安装 Docker | 正常显示主机监控，Docker 标记为不可用 |
| `docker run` 创建容器 | 自动发现，不需要 Compose 文件 |
| SSH 手动停止容器 | NodeDeck 自动同步 Exited 状态 |
| SSH 手动重命名容器 | NodeDeck 同步真实名称 |
| 容器重新创建 | 合理恢复可匹配的自定义显示设置 |
| Docker daemon 停止 | Agent 仍在线，Docker 显示不可用 |
| Agent 进程停止 | 节点标记失联，历史数据标记过期 |
| Compose 端口变更 | 重建后显示正确的新绑定 |
| 浏览器未登录 | 无法访问管理 API 或 WebSocket 数据 |
| Session 过期 | 后端拒绝请求，管理页面退出 |
| Tailscale 连接中断 | 按连接策略处理，不能绕过安全限制 |
| 手机访问 | 卡片、按钮、日志和终端能够正常交互 |

测试应记录实际执行结果、失败原因和复现条件。性能方面重点观察 Agent 常驻 CPU/内存占用、多节点监控更新延迟、容器状态同步耗时和 Core 的数据库增长情况。

可以将**十至二十台 VPS、每台若干 Docker 容器**作为目标测试规模，但不要在未测试前声称系统已经达到某个并发容量或响应时间指标。

---

## 十二、与专项设计、综合拓展、毕业设计的衔接

根据你提供的课程会议记录，专项设计重点是需求分析与概要设计，综合拓展重点是详细设计与测试，并且鼓励将课程选题与毕业设计衔接。1

因此，没有必要做三个不同项目。

| 阶段 | 工作重心 | 主要成果 |
|---|---|---|
| 专项设计 | 需求分析、业务流程、概要设计 | 需求规格、用例图、数据流图、总体架构 |
| 综合拓展 | 模块详细设计、数据库、接口、测试方案 | 类图、时序图、数据表、测试用例 |
| 毕业设计 | Core、Agent、Web 实际实现与实验 | 可运行系统、功能测试、性能分析、毕业论文 |

专项设计中重点分析 VPS 监控、Docker 自动发现、容器管理、用户鉴权等需求；综合拓展再展开 Agent 通信、Docker 状态同步、容器管理任务和数据库设计。

毕业设计阶段则根据上述方案进行实现，使用真实 Debian VPS 和 Docker 容器进行测试。

这样既能够满足学校的软件工程设计要求，又能避免重复开发。

---

## 十三、最终设计结论

**NodeDeck 的整个系统可以概括为四个核心部分：**

**一个 Web 控制台：** 采用 Komari 风格的 VPS 监控页面，将服务器状态、Docker 应用、端口绑定和快捷管理操作集中展示，兼容桌面和移动浏览器。

**一个 Core-Agent 管理体系：** Core 负责统一管理、鉴权、数据库和任务调度；Agent 负责实际采集和执行，主动连接 Core，不要求在各 VPS 开放额外的 Agent 管理端口。

**一套完整的 Docker 资源管理流程：** 直接基于 Docker Engine 自动发现所有现存容器，不依赖 Compose 文件；支持独立容器、Compose 项目、端口映射、运行状态、历史事件、容器操作，以及用户自定义名称与排序。

**一套默认私有的访问机制：** 未登录只显示工仔小猫 GT 的头像和密码登录入口；登录后才允许查看基础设施数据和执行管理操作。鉴权使用单管理员 Session，不引入不必要的外部认证服务。

### 最终需要坚持的产品原则

NodeDeck 不必在功能数量上与 1Panel 竞争，也不必完全复制 Komari 的全部监控与主题功能。它应该集中解决你最关心的问题：

**打开一个 Web 页面，看见所有 VPS；看见每台 VPS 正在运行的 Docker；看见每个容器的名称、状态、时间、IP 和端口；能够快速修改、启动、停止和管理这些服务。**

此外，实际 Docker 状态必须以 Docker Engine 为准，不能由 Web 页面自行虚构；高风险修改必须有明确确认与结果校验；个人化展示配置不能干扰 Docker 的真实运行逻辑。

这份方案确定的是**项目的产品需求、总体架构、功能边界和技术路线**，不是软件已经发布后的版本规划。

**正式项目名称统一使用 NodeDeck，后续专项设计、综合拓展及毕业设计均围绕这一项目展开。**