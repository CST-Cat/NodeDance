# NodeDance architecture

## Build and embedded Web

`make build` first runs the Vue type check and production Vite build, then builds the Core and Agent Go binaries. Vite writes the production files to `internal/core/webassets/dist`; `internal/core/webassets/assets.go` embeds that directory in the Core binary. `make check` runs the same front-end checks before the Go checks.

## Core

The Go Core serves the administrator Web application, `/api/v1` HTTP API, and authenticated outbound Agent sessions at `/ws/v1`. SQLite stores administrator, Agent, task, metric, and Docker state. Browser mutations require an authenticated session and CSRF validation. Agent identity comes from registered device credentials bound to a node.

Core startup treats the database, administrator security material, and Core task store as required foundations. Alert delivery and probe execution are background capabilities: alert key/store/recovery errors are reported and leave alert routes unavailable, while probe state recovery errors are logged and the scheduler retries independently. Neither scheduler owns the Core HTTP listener or host monitoring. A failure restoring cached Docker inventory is logged and does not prevent Core management from starting.

The Core task store in `internal/core/tasks/` owns remote task identity, idempotency, state transitions, delivery evidence, and audit history. Business handlers may define operation payloads, but they share this state store and task ID semantics.

## Probes and alerts

`internal/core/probes/` stores probe configuration, run history, scheduler claims, and current result state. The Core dispatches bounded HTTP(S) or TCP checks through the enrolled node's Agent capability; the Agent performs the outbound connection. Reports are bound to the current node generation and run ID. Offline, timed-out, and unsupported probes remain unknown rather than being counted as a service failure.

`internal/core/alerts/` evaluates current Core-owned node, metric, Docker, and probe samples, stores alert transitions and maintenance windows, and queues notifications. Evaluation and delivery retry independently and report failures to the Core log. SMTP accepts STARTTLS or implicit TLS only. SMTP secrets are encrypted with a private key in the Core data directory.

## Agent

The Agent starts host metric sampling before connecting to the Core. CPU, memory, disk, network, and uptime collection has its own context and does not use Docker. A Docker SDK client is created once by `internal/agent/docker/engine.go` and shared with discovery, task actions, streams, images, Compose, and rebuild adapters. Hello advertises Docker only when that client was initialized; if the configured Docker socket cannot be used, the Agent continues host metrics and heartbeats without claiming Docker support. If a valid client cannot reach the Engine, discovery reports the unavailable Engine state while host monitoring remains active.

The Agent task journal and runner are process-owned across WebSocket reconnects. A reconnect attaches a new generation to the same runner; it does not recreate the task journal or cancel accepted work. Container lifecycle actions use the common runner and journal. Rebuild persistence in `rebuilds.sqlite` is optional for basic container actions: if it cannot be opened, start/stop/restart remain available and rebuild requests report that the rebuild capability is unavailable. Hello advertises only initialized connection-scoped capabilities. Each heartbeat reports the current set over the same WebSocket; Core accepts only unique, known capabilities that were negotiated in Hello and persists the live set. When an optional worker exits, Agent stops that worker, removes its capability from subsequent heartbeats, and keeps the host metrics and heartbeat loop active. Core disables that capability's requests and closes associated streams or transfers.

## Web

`web/src/api.ts` is the single HTTP request implementation and handles session cookies, CSRF tokens, request deadlines, HTTP errors, and expired sessions. Business API modules call it. `web/src/streamApi.ts` owns WebSocket streaming separately.

Vite forwards API and WebSocket traffic to `VITE_CORE_TARGET` during local development. Production Web assets are built into `internal/core/webassets/dist` and embedded by the Core.

## SQLite schema

Core initializes the current tables and indexes from `internal/core/storage/schema.sql` together with the shared task schema in `internal/core/tasks/schema.sql`. SQLite schema-version ledgers and historical migration files are not maintained. The application does not reset the database at startup; development databases use the current schema, and older schema layouts are not upgraded automatically. The product version remains `0.0.0` independently of the database structure.
