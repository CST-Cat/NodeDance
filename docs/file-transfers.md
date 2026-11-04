# Multi-node file transfers / 多机文件对传

Core schedules regular-file transfers through `files.transfer`. Each transfer runs in the mode selected for it: Core relay (the default) or Agent direct. The selected mode never switches automatically. In relay mode, Agents run rsync through a dedicated Core WSS relay; if rsync or its confinement is unavailable, Core uses the bounded chunk relay over separate task data connections instead. In direct mode, the Agents connect to each other and a failed attempt fails the transfer. The persistent Agent connection carries control requests, progress and task outcomes; file contents use independent connections.

Core 通过 `files.transfer` 调度普通文件对传：每次传输按所选模式运行——Core 中继（默认）或 Agent 直连，所选模式不会自动切换。中继模式下经独立的 Core WSS rsync 中继传输，rsync 或其隔离不可用时改走独立连接上的分片中继；直连模式下两端 Agent 互连，尝试失败即传输失败。常驻 Agent WSS 只承载控制请求、进度和任务结果。批量中的每个文件有独立目标任务，已完成项不会因其他项失败而回滚。

## Direct connections / 直连

Direct mode tries three task-scoped paths in order: source-dials-target TCP/WSS, target-dials-source TCP/WSS, then Pion WebRTC reliable ordered DataChannel over ICE/DTLS/SCTP. Connection initiator and file sender are independent. Core forwards only task authorization, bounded SDP offers/answers, progress and outcomes; the selected direct connection carries the rsync bytes. Neither TURN candidates nor automatic Core relay are allowed in direct mode.

直连依次尝试源端连接目标、目标反向连接源端、Pion WebRTC 可靠有序 DataChannel。最后一种通过 ICE/STUN 探测局域网、IPv4/IPv6 与公网 UDP 映射并进行打洞。Core 通过现有认证控制连接转交完成候选采集的 SDP offer/answer；DTLS 验证 SDP 中的证书指纹，数据通道还校验一次性、绑定任务的凭证。每个直连通道都有缓冲上限、写入背压、连接超时与取消处理。选中路径必须不含 TURN 中继，无法直连时报告失败。

A target without a declared port listens on an operating-system-assigned TCP port. A declared listen/public port uses the provider's existing TCP forwarding; it does not create a mapping. The declared endpoint is added to interface candidates, rather than replacing them. Fixed TCP listeners are shared within the Agent control connection and route independently authenticated sessions by task identity, allowing concurrent file transfers. Both directions preserve existing Tailscale interface candidates. NodeDance does not install or configure Tailscale or change host firewall rules.

配置的监听端口与公网端口只描述已有的 TCP 端口转发，不会自动配置路由器。两端网卡候选与声明的公网候选同时保留，IPv6、同局域网及已有 Tailscale 路径仍可尝试。一个固定 TCP 端口可承载多个独立认证任务；ICE 使用自己的 UDP socket，不使用 TCP 端口转发声明推算 UDP 映射。

Agent uses `stun:stun.cloudflare.com:3478` and `stun:stun.l.google.com:19302` by default. Set the Agent service environment variable `NODEDANCE_STUN_SERVERS` to a comma-separated list of `stun:` URLs for private STUN infrastructure; an empty value or `off` disables STUN while retaining host candidates. TURN configuration is rejected. STUN services see discovery traffic and observed IP/port, not file contents. If STUN is hosted beside Core, expose its UDP listener separately; the HTTPS/WSS reverse proxy does not provide STUN service. Allow outbound UDP and its stateful replies on Agents. Each candidate-gathering phase waits up to three seconds for STUN, and direct connection setup has a twenty-second bound per Agent attempt within the task credential lifetime.

默认 STUN 服务只参与公网映射发现，不接收文件数据；可用 Agent 服务环境变量 `NODEDANCE_STUN_SERVERS` 配置自建服务，设为 `off` 或空值可关闭 STUN。普通 NAT 的打洞成功率取决于映射及过滤行为；双端对称 NAT、部分 CGNAT、禁用 UDP 或严格防火墙仍可能无法直连。这些环境应明确选择 Core 中继，或配置可达 IPv6／端口映射。已有组网通道自身也可能中继，不能仅凭虚拟网卡地址声称底层为公网直连。

Each Agent embeds a trimmed static rsync 3.4.1 executable for its Linux amd64 or arm64 architecture. Runtime uses only this bundle, extracted beneath protected root Agent state; no system rsync is discovered or used. Linux Landlock ABI 3 or newer is required for the rsync availability check, and the receiver must additionally pass a runtime confinement probe. Before starting `rsync --server`, the target generates an argument template with its bundled rsync and accepts only an exact match from the source. Delete, sender, daemon, files-from, backup and redirected-log arguments are forbidden. See [bundled rsync](bundled-rsync.md) for source, checksums, architecture builds and notices.

Agent 自带对应架构的静态 rsync 3.4.1，无需在节点安装 rsync；可用性检查、参数模板和执行只使用包内文件。目标接收进程启动前，必须通过内置 rsync 运行时模板和隔离探针。接收参数只允许精确匹配该模板；Landlock 将文件读写限制到已固定句柄的私有 payload，并限制可写大小。缺少必要内核支持或探针失败时，中继模式改走分片中继、直连模式直接失败，不放宽隔离要求。

## Core relay / Core 中继

The source and target make outbound connections to `/ws/v1/files/rsync` using separate sender and receiver credentials. Core pairs the two streams and forwards rsync bytes with bounded buffers. A data disconnect before commit triggers one new relay attempt with fresh credentials and the same retained payload; persistent control connections can remain active. If rsync relay cannot complete, relay mode uses the chunk relay over `/ws/v1/files/chunks`, including upload/download acknowledgments, and never moves file chunks back onto the persistent socket. Text reads and saves use this independent data transport too; save task dispatch carries metadata and the Agent verifies the streamed body before its existing UTF-8/version-protected save.

中继连接绑定管理员会话、当前 Agent 连接、源和目标节点、目标任务、方向、路径及完整传输描述符。凭证通过请求头传递，创建后一分钟内才可消费，只能使用一次；匿名请求、管理员 Cookie 和伪造凭证均不能打开数据连接。管理员会话撤销、任务取消或 Core 停止会终止本次传输。

Core relay uses two TLS connections. Core terminates TLS and can access the forwarded rsync bytes; it is not end-to-end encryption between Agents. Direct transfers use the target's pinned task certificate. Production Agent-to-Core data connections require WSS; plain WS is allowed only for a literal loopback development Core.

## Integrity and recovery / 完整性与恢复

Core records the source node/path/version, size, SHA-256, target path and original target version in the durable task descriptor. Sources are opened through the existing file service. Target partials live under the root Agent's `0700` state directory with `0600` regular files; symlinks, hardlinks, traversal, credentials and protected host trees are rejected. The received payload is verified by SHA-256 before copying into the normal staged upload and atomically publishing the destination. Commit rechecks the original target version, so external changes are not overwritten. File modes, ownership, ACLs and extended attributes are not replicated.

失败、取消和断线保留私有 partial；新任务可以采用同一描述符的 partial，由 rsync 重跑时复用已有块。首次传输从空 payload 开始，不以已有目标文件作为增量基础；分片兜底会完整重传。已完成任务清理自己的 payload，失败 partial 没有自动过期策略，需由 root 在确认无活跃任务后清理相应私有暂存目录。

Retry uses `files.transfer-retry` on the **target node** with a fresh `Idempotency-Key`. Only confirmed `failed`, `canceled` or `timed_out` outcomes can be retried. The original source version, digest, target version and bandwidth remain binding; changed source or destination data requires an explicitly new transfer. An `unknown` result must be observed first. Agent restart never replays a started write: reconciliation verifies actual destination contents, confirms an unchanged destination as failed, or keeps an unproven outcome unknown.

对传进入提交阶段后，Core 只观察原任务，不再切换通道重复写入。取消会保留 partial，但不承诺撤销已经提交的文件。每个文件任务最多运行 30 分钟；请求中断或 Core 重启后，先通过 `tasks.get`／`tasks.observe` 确认原任务，再决定是否重试。

## Shared operations / 统一操作

Web, CLI and TUI discover the same Core operation contract. The Web transfer dialog chooses a node and existing directory, selects the transfer mode (`relay` by default, or `direct`), accepts `bandwidthLimit` in bytes/s and displays the actual result transport. Zero means unlimited; nonzero limits must be at least 1024 B/s and no greater than the published maximum. The limit applies per file, including chunk fallback, rather than to the aggregate batch. File size uses the configured Agent/Core bound (default 1 GiB, maximum 16 GiB). Directory transfers, recursive rsync synchronization and mirrored deletion are not included.

```sh
nodedance describe files.transfer
nodedance call files.transfer --params '{"nodeId":"SOURCE_NODE_UUID"}' \
  --body '{"targetNodeId":"TARGET_NODE_UUID","paths":["/srv/data.bin"],"targetPath":"/srv/incoming","bandwidthLimit":1048576}' \
  --idempotency-key TRANSFER_KEY
nodedance call tasks.get --params '{"nodeId":"TARGET_NODE_UUID","taskId":"TASK_ID"}'
nodedance call tasks.observe --params '{"nodeId":"TARGET_NODE_UUID","taskId":"TASK_ID"}'
nodedance call tasks.cancel --params '{"nodeId":"TARGET_NODE_UUID","taskId":"TASK_ID"}'
nodedance call files.transfer-retry --params '{"nodeId":"TARGET_NODE_UUID","taskId":"TASK_ID"}' \
  --idempotency-key NEW_RETRY_KEY
```

TUI 的通用操作界面填写同样的参数和幂等键。Web 将失败目标任务保留在文件管理器的观察区；未知结果先观察，确认失败或取消后启用“重试传输”。批量返回 `transferred` 与可选 `failed`；成功项包含 `transport`、SHA-256 和目标 `taskId`，已创建任务的失败项也包含 `taskId`。单次传输请求复用原幂等键只观察／返回原任务，不自动创建新的写入。

## Verification / 验证

`internal/core/server/file_transfer_integration_test.go` starts a real isolated Core and two separate root Agent processes, with bundled rsync, an unavailable system-command PATH and disposable file/state directories. Its TLS proxy injects an unreachable direct offer, unavailable rsync data endpoints and a data-only disconnect without a product test switch. The cases cover forward TCP, reverse TCP and ICE-negotiated rsync direct transfer, WSS relay, chunk fallback, data reconnection, cancellation, retained-partial retry, consumed-credential rejection and absence of chunks on persistent control connections. Receiver and staging tests cover altered wire arguments, SHA-256 failure, target-version conflicts, unsafe partial links and recovery without replay.

```sh
make build
go test ./...
node web/scripts/audit-files.cjs
NODEDANCE_GUEST_ATTACKS=1 node web/scripts/audit-guest.cjs
node web/scripts/file-notices.cjs --check
python3 scripts/interface-coverage.py --check
```

浏览器回归使用新构建的内嵌资源和隔离 Core／root Agent，覆盖限速输入、真实取消后的未知观察和重试、结果方式、CLI 契约调用及已有文件安全边界。Go 端到端案例使用同一主机上的两个独立 Agent 进程；这不代表跨物理主机、WAN、NAT 或生产网络已经实测。测试环境不满足 root 或 Landlock 要求时，相关实测案例明确跳过，不能将跳过记为验证通过；内置包损坏或缺失属于失败。

`internal/peertransport/peer_test.go` uses Pion virtual networks with two separate port-restricted NAT routers and a STUN server. It verifies bidirectional reliable transfer without relay, and bounded failure for the tested symmetric-NAT and UDP-blocked topologies. Agent transport tests independently exercise Core-style offer/answer signaling, task authorization, duplicate-signal rejection and shared fixed TCP listeners. Virtual-network success is not evidence of live provider/CGNAT connectivity. Real rsync integration requires root and Landlock ABI >= 3; set `NODEDANCE_REQUIRE_RSYNC_TESTS=1` to fail rather than skip when unavailable, as native CI does.

新增 NAT 测试使用两个独立的虚拟 NAT 路由器，不代表生产运营商网络已经实测。运行环境不提供所需 Landlock 时，真实 rsync 用例只能记录为跳过；原生 Linux CI 必须实际运行这些用例，不能降低文件隔离要求来通过测试。
