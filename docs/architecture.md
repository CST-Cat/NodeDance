# NodeDance architecture

## Core

The Go Core exposes the administrator Web application and `/api/v1` HTTP API, stores administrator, Agent, task, metric, and Docker state in SQLite, and accepts authenticated outbound Agent sessions at `/ws/v1`. Browser mutations require an authenticated session and CSRF validation. Agent identity comes from registered device credentials bound to a node.

The Core task store in `internal/core/tasks/` owns remote task identity, idempotency, state transitions, delivery evidence, and audit history. Business handlers may define their operation payloads, but they share this state store and task ID semantics.

## Agent

Each Go Agent registers once, then maintains an outbound WSS connection and reports host metrics. Docker discovery is an optional capability and does not own the host metrics lifecycle. If Docker is unavailable, the Agent reports that condition while continuing host monitoring and heartbeats.

`internal/agent/docker/engine.go` owns the local Docker SDK client and its lifetime. Discovery, container actions, streaming, images, and other Docker features receive the shared client. Per-operation contexts and stream cleanup remain local to the operation that needs them.

The Agent `taskjournal` is the one durable general-purpose execution log. `taskrunner` receives task dispatches, records acceptance before acknowledgement, executes through registered business executors, and reports verified results. A business transaction may keep narrowly scoped recovery data when needed to complete or roll back that transaction; it does not replace the common task state machine.

## Web

`web/src/api.ts` is the single HTTP request implementation and handles session cookies, CSRF tokens, request deadlines, HTTP errors, and expired sessions. Business API modules call it. `web/src/streamApi.ts` owns WebSocket streaming separately.

Vite forwards API and WebSocket traffic to `VITE_CORE_TARGET` during local development. Production Web assets are built into `internal/core/webassets/dist` and embedded by the Core.

## Data and migrations

SQLite migrations are ordered and append-only so existing installations keep their migration history. Removing an active feature does not silently delete its persisted data; consolidation migrations must preserve or migrate existing records before dropping obsolete tables.
