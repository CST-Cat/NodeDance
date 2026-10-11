# Komari communication study and NodeDance application

Reviewed upstream source on 2026-10-11, pinned to these revisions:

- [Komari Core protocol](https://github.com/komari-monitor/komari/blob/13cbdca545e3427f720cfe55a0db787048a0a3be/protocol/v2/jsonrpc.go)
- [Komari Agent connection and fallback](https://github.com/komari-monitor/komari-agent/blob/828afafe7fb5b1b58c1232094e1e46320719016e/server/websocket.go)
- [Komari Agent response handling](https://github.com/komari-monitor/komari-agent/blob/828afafe7fb5b1b58c1232094e1e46320719016e/server/protocol.go)

Komari maintains separate Core and Agent repositories. Its v2 contract names operations through JSON-RPC methods (`agent.report`, task/ping results, terminal and file requests), with request IDs, structured results/errors, and acknowledged events. Agent initiates its WebSocket and periodically reports metrics/basic information; Core sends work on that channel. The Agent reconnects, and its source also implements HTTP reporting/pull fallback. File metadata is separate from dedicated content transfer.

NodeDance already implements the important architectural separation: independently built Core/Agent binaries, outbound authenticated WSS, a shared wire package, explicit request IDs, connection generations, heartbeat leases, reconnect backoff/jitter, capability negotiation and process-owned task journals. The Core owns identity, desired work, history and auditing; Agent owns host execution and durable execution evidence. Docker/Compose/files/probes/terminal are optional capabilities. Core does not import Agent runtime and Agent does not import Core runtime.

This change strengthens that current design rather than replacing a working wire contract:

1. `internal/protocol/capabilities.go` centralizes supported capability names, intersection negotiation and the public wire descriptor. `/api/v1/protocol` and `nodedance core protocol` expose the contract independently of the binary build version.
2. Agent checks that Welcome enables only capabilities it actually offered. A Core cannot activate a locally disabled feature. Unknown optional capabilities in Hello are ignored by negotiation; unnegotiated messages remain rejected. Independent builds can differ in optional features while using the same wire version.
3. Dependency-boundary tests prevent production imports between Core and Agent runtime, and keep protocol independent of both. Integration tests may exercise both peers.
4. `internal/control` defines the administrator operation catalog, JSON input/result contract, and HTTP/WebSocket client. CLI and TUI share dispatch, authentication, CSRF, file transfer and terminal opening. The Core remains the authority for business validation, idempotency and audit records.
5. Core generates the Agent installer command through `internal/installer`; Web, CLI and TUI receive it in the same enrollment response. Published Agent tags and root-only security rules remain authoritative.

NodeDance retains its versioned envelope instead of adding a second JSON-RPC protocol or compatibility layer. Its generations, task journals and reconciliation already address stale connections and ambiguous execution. There is no new HTTP polling fallback: that would require an explicit delivery, acknowledgement, lease and reconciliation design across transports. Agent credentials remain in protected local state and request headers, rather than copying upstream's query-token connection scheme. CLI browser sessions are separate credentials and cannot authenticate an Agent socket.

Core and Agent can be built and released separately. Agent release tags remain `agent-vMAJOR.MINOR.PATCH`; Core release tags do not affect Agent installers. Incompatible future wire changes must explicitly advance the protocol version; independent binary versions do not imply acceptance of incompatible contracts.
