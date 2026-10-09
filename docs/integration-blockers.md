# Integration checkpoint

Branch: `codex/nodedance-integration`  
Scope: integrate the S06–S16 candidates in dependency order, preserve stage reports, and keep incomplete formal acceptance marked `NOT_READY`.

## Schema history

The integration database migrations are append-only: v1–v5 remain unchanged, Compose uses v6, Compose Editor v7, service probes v8, alerts v9, and Agent updates v10. The S16 candidate's isolated v7 number was not reused. S12's SQLite preference migrator uses the existing S06 preference schema and adds no migration.

## Candidate integration and remaining acceptance gaps

| Stage | Integrated behavior | Remaining blocker; stage remains `NOT_READY` |
|---|---|---|
| S02 | Added TLS/WSS proxy recovery and bounded Core shutdown coverage for S02-07. | No complete S02 specification run on the integration branch. |
| S04 | Added exact Engine container ID tracking through the Agent WebSocket snapshot into Core/SQLite/API/Dashboard; wired the trace test into S04-01. | Owned DIND Engine 28/29 trace was not run here; no full S04 acceptance evidence. |
| S05 | Durable task bridge is present through the S06 candidate ancestry. | The preserved candidate report records a `make check` preflight `FAIL` before any S05 case ran; the integrated branch has focused checks but no complete S05 run. |
| S06 | Dashboard candidate is present. | No real Engine/DIND S06 run, real-Core browser integration, or complete dependent S04/S05 evidence. |
| S07 | Compose project management is present on integration migration v6. | Full Engine 28/29 and complete P0/S07 acceptance evidence is outstanding. |
| S08 | Image management is present. | No controlled Registry and real Engine acceptance evidence for this integration branch. |
| S09 | Terminal component and browser work is present. | No complete live Core-Agent-browser lifecycle run covering revocation, disconnect, restart, expiry, and terminal cleanup. |
| S10 | Host file browsing and transfer work is present. | Large-file RSS comparison, systemd write allowlist, and full durable task-store integration remain unverified. |
| S11 | Compose editor is present on integration migration v7. | Candidate report lists remaining IPv6, required-env, unhealthy rollback, tag, restart injection, volume comparison, and full Core-Agent E2E gaps. |
| S12 | Rebuild preference migration is backed by SQLite storage and is wired into the default Core. | Remaining S12 port, configuration/resource preservation, failure recovery, and other report-listed cases are not complete. |
| S13 | Tailscale discovery and SSH deployment are present. | No real Tailnet, SSH host, signed production artifact, or systemd Agent installation evidence. |
| S14 | HTTP/HTTPS/TCP probes are present on integration migration v8. | Real remote Agent, DNS/TCP/certificate failures, lease recovery, and slow-probe resource evidence remain outstanding. |
| S15 | Alerts and notifications are present on integration migration v9; missing Docker healthcheck is treated as unknown for health rules. | Production SMTP/TLS and independent Agent failure/recovery/restart evidence are missing. |
| S16 | Signed updates and prepared-ACK handoff are present; update schema is integration migration v10. | Real systemd multi-Agent update/restart and production signing-key-chain evidence remain outstanding. |
| S17 | The shared application framework and stage runners are integrated. | Backup/restore and complete release acceptance are not yet implemented or validated. |

S09 remains visible through the generic stage runner, which reports `NOT_READY` until its normative acceptance suite is available. Mocked browser/component evidence is retained as component evidence and does not substitute for the live terminal transport.

## Stage runner commands

`make test-stage STAGE=S11`, `S14`, `S15`, or `S16` now dispatches one focused candidate target: `make test-candidate-s11`, `test-candidate-s14`, `test-candidate-s15`, or `test-candidate-s16`. Each target prepares the pinned frontend/browser dependencies and invokes its existing focused stage suite once. Candidate checks are recorded separately; they never turn the normative stage or its individual required cases into `PASS`. Missing real Engine, remote Agent, production SMTP, systemd, signing-key, and other case-specific evidence remains `NOT_READY`, with the previous detailed report retained under `historical_candidate_report`. Generic stages without a normative runner also keep prior detail in `historical_stage_report` when writing their current per-case `NOT_READY` result.

Per-stage execution no longer repeats repository-wide `make check` and `make build`. Those remain available as final integration checks; S00–S05, S08, and S10 continue using their existing focused acceptance runners. Stages without a normative runner, including S09, still have a callable generic command that records every required case as `NOT_READY`.

## Integration checks recorded

- `make frontend`: locked Go/Node/pnpm bootstrap, module verification, frontend typecheck, and production asset build passed.
- Focused Go suite: S16 prepared-ACK/helper paths, S12 SQLite preference migration, S15 no-healthcheck alert handling, S01 migration-version compatibility, integration schema v10, and package compilation passed.
- `go build ./cmd/nodedance ./cmd/nodedance-agent ./cmd/nodedance-release`: passed.
- Core smoke on `127.0.0.1:18180`: `/api/v1/health` and embedded `/` returned HTTP 200; Ctrl-C stopped Core and released the port. The smoke exposed a missing WaitGroup increment for `serviceProbeScheduler`; that shutdown fix is in commit `ff723cd` and the same start/health/stop path passed afterward.
- The S04 owned-DIND trace and real Core-to-Agent-to-managed-node end-to-end evidence remain outstanding, so the affected formal cases stay `NOT_READY`. Responsive layouts have browser checks at multiple CSS viewport widths; physical iPad and Android devices are not release-acceptance requirements.

The isolated stage reports remain the source of per-case evidence and are intentionally preserved. Current formal acceptance uses one meaningful complete run; previous repeated runs remain historical evidence only.
