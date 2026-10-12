# NodeDance architecture

## Build and embedded Web

`make build` first runs the Vue type check and production Vite build, then builds the Core and Agent Go binaries. Vite writes the production files to `internal/core/webassets/dist`; `internal/core/webassets/assets.go` embeds that directory in the Core binary. `make check` runs the same front-end checks before the Go checks.

## Core

The Go Core serves the administrator Web application, `/api/v1` HTTP API, and authenticated outbound Agent sessions at `/ws/v1/agent`, with acknowledged HTTP fallback at `/api/v1/agents/channel`. Production listeners bound beyond loopback require a configured TLS certificate and matching key; otherwise the Core CLI refuses startup. SQLite stores administrator, Agent, task, metric, and Docker state. Browser mutations require an authenticated session and CSRF validation. Agent identity comes from registered device credentials bound to a node.

Core startup treats the database, administrator security material, and Core task store as required foundations. Alert delivery and probe execution are background capabilities: alert key/store/recovery errors are reported and leave alert routes unavailable, while probe state recovery errors are logged and the scheduler retries independently. Neither scheduler owns the Core HTTP listener or host monitoring. A failure restoring cached Docker inventory is logged and does not prevent Core management from starting.

The Core task store in `internal/core/tasks/` owns remote task identity, idempotency, state transitions, delivery evidence, and audit history. Business handlers may define operation payloads, but they share this state store and task ID semantics.

## Probes and alerts

`internal/core/probes/` stores probe configuration, run history, scheduler claims, and current result state. The Core dispatches bounded HTTP(S), TCP or privileged IPv4/IPv6 ICMP checks through the enrolled node's Agent capability; the Agent performs the outbound connection. Reports are bound to the current node generation and run ID. Offline, timed-out, and unsupported probes remain unknown rather than being counted as a service failure.

`internal/core/alerts/` evaluates current Core-owned node, metric, Docker, and probe samples, stores alert transitions and maintenance windows, and queues notifications. Evaluation and delivery retry independently and report failures to the Core log. SMTP accepts STARTTLS or implicit TLS only. SMTP secrets are encrypted with a private key in the Core data directory.

## Backup and restore

`nodedance backup` uses SQLite's online backup API to snapshot committed database and WAL contents while the Core is running. The private archive includes the database, authentication and alert-encryption keys, and optional first-run or Tailscale deployment state. `nodedance restore` validates the manifest, file digests, required keys, and SQLite integrity in a private staging directory, then atomically installs the complete snapshot into an absent or empty destination on Linux. The database and matching keys move together, preserving administrator and Agent identities, preferences, tasks, probe/alert history, and notification secret access.

## Agent

The Agent starts host metric sampling before connecting to the Core. Production Core origins require HTTPS and the Agent uses authenticated WSS with the operating system certificate trust store. A custom CA file is optional for private PKI. Plain HTTP/WS is limited to a literal loopback Core address in development mode. CPU, memory, disk, network, and uptime collection has its own context and does not use Docker. A Docker SDK client is created once by `internal/agent/docker/engine.go` and shared with discovery, task actions, streams, images, Compose, and rebuild adapters. Hello advertises Docker only when that client was initialized; if the configured Docker socket cannot be used, the Agent continues host metrics and heartbeats without claiming Docker support. If a valid client cannot reach the Engine, discovery reports the unavailable Engine state while host monitoring remains active.

The Agent task journal and runner are process-owned across WebSocket reconnects. A reconnect attaches a new generation to the same runner; it does not recreate the task journal or cancel accepted work. Container lifecycle actions use the common runner and journal. Rebuild persistence in `rebuilds.sqlite` is optional for basic container actions: if it cannot be opened, start/stop/restart remain available and rebuild requests report that the rebuild capability is unavailable. Hello advertises only initialized connection-scoped capabilities. Each heartbeat reports the current set over the same WebSocket; Core accepts only unique, known capabilities that were negotiated in Hello and persists the live set. When an optional worker exits, Agent stops that worker, removes its capability from subsequent heartbeats, and keeps the host metrics and heartbeat loop active. Core disables that capability's requests and closes associated streams or transfers.

## Web

`web/src/api.ts` is the single HTTP request implementation and handles session cookies, CSRF tokens, request deadlines, HTTP errors, and expired sessions. Business API modules call it. `web/src/streamApi.ts` owns WebSocket streaming separately.

Vite forwards API and WebSocket traffic to `VITE_CORE_TARGET` during local development. Production Web assets are built into `internal/core/webassets/dist` and embedded by the Core.

## SQLite schema

Core initializes the current tables and indexes from `internal/core/storage/schema.sql` together with the shared task schema in `internal/core/tasks/schema.sql`. SQLite schema-version ledgers and historical migration files are not maintained. The application does not reset the database at startup; development databases use the current schema, and older schema layouts are not upgraded automatically.

## Shared operation layer, CLI and TUI

`internal/control` owns the administrator operation catalog, discoverable input schemas, finite JSON results, the cookie/CSRF-aware HTTP client, authenticated WebSocket dialing, and the terminal authorization/open sequence. Both CLI and TUI call it; validation, execution, confirmations, idempotency and auditing remain in the existing Core/Agent handlers. Client sessions are server-bound, owner-protected, and separate from Agent credentials. `nodedance agent` invokes the separately built Agent executable without importing its runtime. See [CLI/TUI usage](cli.md).

CLI and TUI share the terminal operation registry and finite executor, including exclusive private output creation, failed-download cleanup and catalogue-driven task observation. The shared Core contract owns additional request headers, multipart fields and bounds, duplex streams and completion references; there are no independent remote client operation inventories. Core decoders and request descriptions use shared request DTOs with explicit input-presence tags, independent of JSON output omission. Core structural validation is reused by the terminal client; Web consumes the same schemas. Stateful authorization, confirmations, revisions and filesystem policy stay in Core/Agent. Local lifecycle operations remain local and accessible from both terminal presentations, even while the remote Core is unavailable. Web graphical view interactions do not create a second business implementation. [Coverage and verification](interface-coverage.md).

`internal/protocol/capabilities.go` exposes the wire descriptor and capability intersection independently of binary versions. Agent verifies that Welcome contains only offered capabilities. Production dependency-boundary tests guard the separation between Core, Agent and protocol. [The Komari study](komari-communication.md) records the upstream source revisions, adopted principles and transport decisions.

Agent enrollment returns a Core-generated installer command through `internal/installer`; Web, CLI and TUI use the same root-only command and published Agent-tag resolution.

## Extended Agent management

The `agent.host.v1` capability carries shared Agent settings, redacted diagnostics and durable root script jobs. The same operation contract exposes single-node commands, durable multi-node command/probe batches, asset settings and bounded enrollment grants. Each grant consumer receives an independent credential; its use count is claimed in the enrollment transaction. Settings, traffic and command records are stored as root-owned private JSON. Jobs persist acceptance before execution and never replay on restart.

`agent.metrics.v2` replaces the metrics capability contract; `metrics.host` carries independent metric states. GPU/public IP lookups run separately, disk mount filters apply before statfs, and Core ages metrics using the configured intervals. Extended numeric history and alert subjects come from the shared metric catalog. Probe quality separates unknown gaps from failure and ICMP loss denominators; asset expiry can be evaluated without an online lease, while quota samples require fresh traffic. HTTP and WebSocket use the same Agent protocol, identity, generation, writer and business handlers. See [Agent capabilities](agent-capabilities.md) for operation names, limits and data semantics.

## Bounded requests and durable command history

Core limits ordinary request-body reads to 15 seconds and upload/multipart reads to 30 minutes. At most 256 requests may execute concurrently (health remains reachable); saturated requests return HTTP 503 with `Retry-After: 1`. Existing per-operation body limits, transfer limits, authentication and rate limits also apply. The server options expose these resource limits for embedding and verification. WebSocket lifetimes retain their separate stream policy.

Root Agent command acceptance is written before execution. Full output remains in private JSON files; at 10,000 files, completed outcomes are compacted in batches of at least 128 into the root-owned `0600` command-history SQLite archive. Compaction preserves request hashes and job IDs, so uncertain retries still resolve to the accepted job. Lists use cached summaries and a bounded archive query instead of rereading every full output. Hourly cleanup and the discovered `commands.cleanup` operation use `commandHistoryRetentionDays` (default 90, range 1–3650). Finished known outcomes expire after that window; a key reused after expiration can execute again. Running, unknown and interrupted outcomes retain their identities. If uncertain outcomes alone fill the limit, new work is refused rather than discarding execution evidence.

Core retains partial, stale and uncertain batch records. After interruption, batch observation finds missing child results using the original node idempotency key and reads the Agent job; observation never resubmits commands. Result persistence failures report unknown outcomes; an Agent restart converts surviving running records into interrupted outcomes without replay.

During the current development phase, host management uses the existing `agent.host.v1` capability with the current settings/cleanup contract. Core and Agent should be built from the same development revision; this change does not increment product, release or capability versions.

See [verification results and reproduction](verification.md).

## Guest display

Core owns opt-in publication policy, dedicated public response types and bounded cached projections of existing metric/history/probe data. The login page exposes guest entry only when enabled; visitors receive no management capabilities. Administrator configuration and saved-policy preview use the shared discovered operation contract in all interfaces. See [guest display](guest-display.md).
