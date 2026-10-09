# S07 Compose implementation candidate

**Formal phase status: `NOT_READY`.** This candidate establishes the Agent
Compose protocol and manager, the Core request/status/event endpoints, a
responsive standalone Vue component, and a real Engine/Compose test harness.
It does not pass S07 or P0. The Core schema is not registered in production
SQLite migrations, the component is not mounted by the S06 application shell,
and the real Engine 28/29 acceptance has not run in this worktree.
[`reports/stages/S07.json`](../../reports/stages/S07.json) keeps all ten
normative S07 cases at `NOT_READY` and records the exact checks and blockers.

## Implemented boundaries

- `internal/protocol/compose.go` defines a negotiated Agent capability,
  source-bound project references, ordered config/environment files, profiles,
  bounded service-instance responses, and strict result validation. A project
  identity hashes its name, working directory, and ordered source files.
- `internal/agent/compose` discovers existing projects from Docker Engine
  labels and keeps each service's instances distinct. Missing source files
  disable Compose writes while preserving container inventory. Operations use
  argv execution in the original project directory, run `config --quiet`
  before mutation, and verify observed Engine state afterward. `down` never
  includes volume, image, or orphan-removal flags. `up` verification uses the
  resolved service model with the same ordered env files and enabled profiles;
  source `deploy.replicas` is authoritative, while an existing Engine replica
  count is preserved when the source omits it. This candidate does not expose
  a new `--scale` override.
- `internal/agent/compose_runtime.go` binds the Compose CLI to the same local
  Unix socket used by the SDK Engine. The runtime advertises capability only
  when the Compose plugin is available. Its bounded bridge serializes writes
  per project and keeps Compose work off the heartbeat path.
- `internal/core/compose/schema.sql` is an isolated schema fragment, with
  `Store` methods for project context, idempotent operations, status transitions,
  events, audit records, and startup recovery. Component tests apply the
  fragment explicitly; this package does not create tables in production.
- Authenticated API routes expose project discovery, idempotent action
  submission, operation status, and operation events. Agent responses are
  correlated with a connection generation. Core restart or ambiguous Agent
  disconnect changes queued/running work to `unknown/result_pending`; writes
  are never replayed. A fresh read-only inventory can resolve only provable
  `down` and `stop` postconditions. Current inventory cannot prove a restart
  occurred or enumerate every expected `up/start` service, so those outcomes
  stay pending until a stronger Agent journal/verification contract is
  integrated.
- `web/src/components/ComposeProjects.vue` is a standalone responsive project
  page with ordered source display, service instances, profile/environment-file
  inputs, validation, lifecycle actions, explicit volume-preservation copy,
  and persistent-status polling. S06 owns application navigation; this worker
  did not modify `App.vue` or `NodesDashboard.vue`.

## Reproduction checks

The following candidate-level checks completed:

```bash
GOTOOLCHAIN=local .tools/go1.26.8/bin/go test \
  ./internal/agent/compose ./internal/agent ./internal/core/compose \
  ./internal/core/server ./internal/protocol
GOTOOLCHAIN=local .tools/go1.26.8/bin/go vet \
  ./internal/agent/compose ./internal/agent ./internal/core/compose \
  ./internal/core/server ./internal/protocol
make frontend
```

The targeted Go packages and vet passed. `make frontend` passed Vue type-check
and production build using locked tools. The real DIND test is
`TestDINDComposeLifecycle`; it is designed to validate special paths, explicit
environment files, default-inactive and explicitly enabled profiles, two
replicas, `up/stop/start/restart/down/up`, and named-volume data preservation.
Its local run was skipped because there was no marked DIND socket. The added
`.github/workflows/s07-compose.yml` executes it against locked Engine 28 and 29
using the repository-owned fixture harness.

The attempted full command
`GOTOOLCHAIN=local .tools/go1.26.8/bin/go test ./...` reported a base-branch
failure in `internal/core/agents.TestMigrationVersionTwoExtendsAuditWithTypedTargets`:
the test expects migration version `3`, but the S05 baseline applies version
`4`. The server test process was still running when it was stopped to deliver
the candidate; therefore this command did not complete and must not be treated
as a full-repository pass. The candidate did not modify the storage migration
owner's `internal/core/storage/sqlite.go`.

## Required integration before S07 acceptance

1. After S06 registers migration v5, register `internal/core/compose.SchemaStatements()`
   as production migration v6. Test a clean database and an upgrade from v5;
   confirm Core startup recovery runs and the Compose API can persist operations.
2. Mount `ComposeProjects.vue` from the S06 app shell or node details navigation.
3. Run the S07 DIND workflow on Engine 28 and 29 and preserve its artifacts.
   The fixture must demonstrate that an inactive profile has zero instances and
   explicitly activating it creates its expected instance, while the data
   service keeps two Engine instances.
4. Integrate Core/Agent operation journaling and read-only reconciliation for
   actions whose outcome cannot be proved from an inventory snapshot.
5. Run the full original S07 and P0 acceptance sets once; rerun affected suites only after a failure or code change;
   component tests and DIND lifecycle checks alone cannot pass S07-10.
