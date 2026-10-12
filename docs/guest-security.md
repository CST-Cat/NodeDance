# Guest browser security verification

Run `pnpm --dir web audit:guest-security`. The script builds an isolated real Core with a temporary database and a protocol-only simulated Agent, signs in a separate administrator browser context, publishes explicit aliases/fields, and enters guest mode through the login button in Chromium. Attack requests originate from that guest page. Production servers, real Agent installations and Docker resources are not targets.

The pnpm command is the existing project convenience entry, not a requirement of the security tests. With Web dependencies and Playwright Chromium already installed, an equivalent direct run from the repository root is:

```sh
cd web
node node_modules/vite/bin/vite.js build --config vite.config.ts --outDir ../internal/core/webassets/dist --emptyOutDir
NODEDANCE_GUEST_ATTACKS=1 node scripts/audit-guest.cjs
```

If current Web assets have already been built, run `NODEDANCE_GUEST_ATTACKS=1 node web/scripts/audit-guest.cjs` from the repository root. The audit itself compiles the Core using Go and runs Chromium through Playwright.

The suite discovers private operations from the connected Core's operation contract rather than keeping another operation list. Responses, not hidden buttons, determine whether an attack succeeds. Administrator requests create the fixture and withdraw its resources; they are not counted as successful guest operations.

## Verified cases

| Group | Cases | Expected result |
| --- | ---: | --- |
| Discovered private operations, including commands/files/containers/updates/configuration | 107 | 401 despite spoofed role/mode/user headers and an anonymous CSRF token |
| Internal and opaque public IDs substituted into private resource routes | 15 | 401 |
| Public scope expansion, arbitrary history, malformed queries and parameter pollution | 12 | 400 |
| Public method changes and method override | 10 | 405 |
| Private method changes, including HEAD and OPTIONS | 21 | 401 |
| Encoded paths, traversal, NUL, repository/config and host-file paths | 12 | 401 or 404; no private data |
| Forged session cookies | 5 | 401 |
| Forged Bearer, Enrollment and Basic credentials | 3 | 401 |
| Dashboard, logs, stats, terminal-ticket and Agent WebSockets | 5 | Handshake fails; connection never opens |
| Cross-origin browser form with a real administrator session | 1 | 403 |
| HTML injection in display title, node alias and service alias | 1 | Literal text; no injected image or script execution |
| Management request during guest refresh storm | 1 | 200 within two seconds |
| Ten waves of 24 concurrent public refresh requests | 240 | 200 or bounded 503; no unselected fields/private hostname |
| Warm-cache field withdrawal, probe deletion, Agent revocation, disable with unavailable selections, global disable | 5 | Updated public scope immediately; disabled display returns 404 |
| **Total** | **438** | **No failed cases after fixes** |

The same run checks default-disabled entry, absence of display requests before guest selection, identical public projections with administrator cookies, session validation when leaving guest mode, browser-mode spoof denial, administrator save/preview, and twelve light/dark/mobile layouts. These existing checks are separate from the 438 attack cases. Counts grow when the Core operation contract grows.

## Findings and fixes

The initial run reproduced three failures: a deleted probe remained in the 15-second snapshot, a revoked node remained in that snapshot, and resource-existence validation prevented switching guest mode off while unavailable resources remained selected. These were publication withdrawal and disable problems; the private-operation matrix did not expose administrator access.

Public requests now recheck selected resource availability even when metric data is cached. The cache key includes the current available publication scope as well as the saved settings, and revoked nodes' service probes are also omitted. Database failures return an unavailable response instead of the old cached projection. Preview uses the same resource filter. Disabled drafts can retain unavailable selections, while enabling still requires available resources and valid fields. Raw query strings are rejected, including malformed queries that Go's parsed query map would omit.

Backend regression tests warm the cache, delete a probe or revoke its Agent through the actual stores, verify immediate removal and matching preview, verify that unavailable probes cannot be republished, and disable without clearing the saved selection. The browser attack run then repeats the original failures against HTTP and passes.

## Scope of the result

This is reproducible verification of the listed routes, inputs and browser flows, not proof that every possible vulnerability is absent. It does not measure distributed denial of service or test production network/TLS configuration. Previously delivered public data cannot be recalled, and requests already in flight when an administrator withdraws publication may finish with the scope they had already read.

Generated JSON results and layout screenshots live under `.build/guest-audit/` for inspection and can be removed after use. No credentials are included in the report.
