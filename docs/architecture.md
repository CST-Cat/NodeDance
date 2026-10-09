# NodeDance architecture

## Core

The Go Core exposes the administrator Web application and `/api/v1` HTTP API, stores administrator, Agent, task, metric, and Docker state in SQLite, and accepts authenticated outbound Agent sessions at `/ws/v1`. Browser mutations require an authenticated session and CSRF validation. Agent identity comes from registered device credentials bound to a node.

The Core task store in `internal/core/tasks/` owns remote task identity, idempotency, state transitions, delivery evidence, and audit history. Business handlers may define their operation payloads, but they share this state store and task ID semantics.

## Probes and alerts

`internal/core/probes/` stores probe configuration, run history, scheduler claims, and current result state. The Core dispatches bounded HTTP(S) or TCP checks through the enrolled node's negotiated Agent capability; the Agent performs the outbound connection. Reports are bound to the current node generation and run ID. Offline, timed-out, and unsupported probes remain unknown rather than being counted as a service failure.

`internal/core/alerts/` evaluates current Core-owned node, metric, Docker, and probe samples, stores alert transitions and maintenance windows, and queues notifications. Delivery runs separately from Core monitoring. SMTP accepts STARTTLS or implicit TLS only. SMTP secrets are encrypted with a private key in the Core data directory.

## Backup and restore

`nodedance backup` uses SQLite's online backup API to capture committed database state, includes the required signing/encryption keys and optional local Core state, and writes a private integrity-checked archive. `nodedance restore` verifies the archive and SQLite integrity in a private staging directory, then installs it into an absent or empty destination. A restore never overwrites non-empty data.

## Agent

Each Go Agent registers once, then maintains an outbound WSS connection and reports host metrics. Docker discovery is an optional capability and does not own the host metrics lifecycle. If Docker is unavailable, the Agent reports that condition while continuing host monitoring and heartbeats.

`internal/agent/docker/engine.go` owns the local Docker SDK client and its lifetime. Discovery, container actions, streaming, images, and other Docker features receive the shared client. Per-operation contexts and stream cleanup remain local to the operation that needs them.

The Agent `taskjournal` is the one durable general-purpose execution log. `taskrunner` receives task dispatches, records acceptance before acknowledgement, executes through registered business executors, and reports verified results. A business transaction may keep narrowly scoped recovery data when needed to complete or roll back that transaction; it does not replace the common task state machine.

## Web

`web/src/api.ts` is the single HTTP request implementation and handles session cookies, CSRF tokens, request deadlines, HTTP errors, and expired sessions. Business API modules call it. `web/src/streamApi.ts` owns WebSocket streaming separately.

Vite forwards API and WebSocket traffic to `VITE_CORE_TARGET` during local development. Production Web assets are built into `internal/core/webassets/dist` and embedded by the Core.

## SQLite schema

Core initializes the current tables and indexes from `internal/core/storage/schema.sql` together with the shared task schema in `internal/core/tasks/schema.sql`. SQLite schema-version ledgers and historical migration files are not maintained. The application does not reset the database at startup; development databases use the current schema, and older schema layouts are not upgraded automatically. The product version remains `0.0.0` independently of the database structure.
