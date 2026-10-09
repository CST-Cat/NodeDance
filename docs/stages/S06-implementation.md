# S06 implementation handoff

**Implementation state:** complete as an S06 candidate. **Formal stage state:** `NOT_READY`.

This change adds the S06 dashboard preference/history foundation and browser UI on top of the existing S05 branch. It owns SQLite migration v5; no other S06 migration is registered. The migration adds per-node display preferences, singleton dashboard settings, sparse known-only minute samples and weighted hourly aggregates. A v4 database upgrade is exercised by closing the v4 store, reopening it through the production migration list, and verifying v5 defaults and tables.

The Core exposes authenticated dashboard settings, node preferences and bounded history query APIs. Metrics accepted from Agent connections are persisted using Core receipt time. Periodic retention folds expired minute buckets into weighted hourly sums/counts, removes data older than retention, leaves preference rows untouched, logs cleanup errors and joins the existing worker shutdown wait.

The browser now includes a fused host/container overview, a featured-container limit of four by default, full Docker inventory, node and container preference editors, search/group/name/state/custom ordering, persistent drag ordering, events, node settings and sparse history charts. Sorting by CPU or memory is not exposed without actual container-stat sorting. Compose service preferences use a stable node/project/configuration/service identity, while standalone preferences use the complete container ID. The inventory's Engine state remains separate from display preferences. History charts preserve unknown intervals as null gaps and disable line bridging. Existing S05 task actions and `ContainerStreams` remain in place. Responsive layouts cover 375, 768 and 1440 CSS-pixel widths.

## Executed checks

| Check | Result | Evidence scope |
|---|---|---|
| `go test -count=1 ./internal/core/server ./internal/core/history ./internal/core/dashboard ./internal/core/storage` | PASS | Real Core and SQLite code, authenticated settings/preferences/history API, range boundaries, known-only aggregation, weighted hourly values and retention behavior. |
| `go test -count=1 ./internal/core/storage -run '^TestVersionFourDatabaseUpgradesToDashboardAndHistorySchemaOnReopen$'` | PASS | Explicit production migration path from schema v4 through reopen to v5. |
| `pnpm --dir web typecheck` | PASS | Locked Node 22.23.3 and pnpm 12.10.1. |
| `pnpm --dir web build` | PASS | Vite production bundle generated. Vite emitted its existing large-chunk advisory; build succeeded. |
| `pnpm --dir web exec playwright test --config playwright.s03.config.ts tests/s06-dashboard.spec.ts` | PASS, 3 projects | Chromium, WebKit and Firefox; mocked API fixture with 40 containers; verifies four featured cards, hidden and pinned preferences, all 40 detail rows, search, touch-pointer reordering persistence through the fixture, visible custom-field/sort controls, and responsive overflow at 375/768/1440. This is component/browser evidence, not real-Core/Engine acceptance. |
| `git diff --check` | PASS | No whitespace errors. |

## Remaining S06 acceptance work

The candidate does not mark any formal S06 case `PASS`. The browser fixture stubs the API, so it does not prove browser-to-real-Core preference persistence. No isolated DIND/real Docker Engine run has been recorded for Compose replicas/recreation, external resource state, stale-node recovery, or real port/link behavior. The touch reorder browser test uses Playwright touch-mode pointer events with fixture persistence; final acceptance still needs the specified isolated real-Core and browser evidence, and the complete required run policy. S04/S05 acceptance dependencies also remain external to this branch.

`reports/stages/S06.json` therefore remains `NOT_READY` with individual reasons; implementation checks above must not be mistaken for formal stage completion.
