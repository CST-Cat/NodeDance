# NodeDance

NodeDance is a staged Linux server and Docker management project. The source includes the S00 foundation and the S01 Core administrator-authentication, private-session, audit, and login-appearance implementation. Agent enrollment and the later management stages remain unimplemented. Check [`reports/status.json`](reports/status.json) and the stage reports before treating any stage as accepted; source code and CI configuration alone are not acceptance evidence.

## Build

The bootstrap downloads only pinned tools into `.tools/`; it does not replace system Go, Node.js, pnpm, Docker, or the user's Docker configuration.

```bash
make bootstrap
make check
make build
```

The binaries are under `.build/`. Node.js is needed to build the Web app, not to run Core.

## First run and data location

The default Core data directory is `$XDG_DATA_HOME/nodedance`, or `~/.local/share/nodedance` when `XDG_DATA_HOME` is unset. Set it with `--data-dir`, `NODEDANCE_DATA_DIR`, or `data_dir` in the configuration file; precedence is CLI, environment, configuration, then the XDG/home default. The default config file is `$XDG_CONFIG_HOME/nodedance/config.json`, or `~/.config/nodedance/config.json`.

For a local HTTP browser session, explicitly enable development mode. It only permits a loopback listener, disables `Secure` on local cookies, and enables short timing overrides for tests:

```bash
.build/nodedance serve --dev
```

On first start, Core prints the local path to `setup-credential.txt`; the file is mode `0600` inside the data directory. Read it on the same machine and enter it in the setup page to create the single administrator. The credential is consumed and the file removed after successful setup. Public setup-status responses never return the data directory or credential path. Do not put the credential in shell arguments, environment variables, service definitions, or logs.

Core stores SQLite/WAL data, verifier-only Argon2id credentials, session-token digests, the CSRF signing key, and audit events in that data directory. The directory is restricted to `0700`; the database, signing key, and setup credential use `0600` permissions.

## Production HTTPS deployment

Production cookies always use `Secure`, `HttpOnly` for the session cookie, and `SameSite=Strict`. Serve the browser through HTTPS and configure the exact public origin. For example, when a reverse proxy on the same host forwards to the loopback Core listener:

```bash
.build/nodedance serve \
  --listen 127.0.0.1:8180 \
  --public-origin https://panel.example.test \
  --trusted-proxies 127.0.0.1
```

The proxy must terminate HTTPS and forward requests to Core. Set `public_origin` to the browser-visible HTTPS origin, including a non-default port if present. Add only the proxy's actual source IP/CIDR to `--trusted-proxies` or `trusted_proxies`; Core does not trust `X-Forwarded-For` or `X-Forwarded-Proto` by default. Do not expose the Core listener directly to the public network. WebSocket upgrades must use the same configured origin.

Example `config.json`:

```json
{
  "listen": "127.0.0.1:8180",
  "data_dir": "/var/lib/nodedance",
  "public_origin": "https://panel.example.test",
  "trusted_proxies": ["127.0.0.1"]
}
```

Configuration precedence is CLI, environment, JSON file, then default. `--config` selects the file; otherwise Core reads `NODEDANCE_CONFIG` and then the user's XDG config location. `--listen` overrides `NODEDANCE_LISTEN` and `listen`; `--public-origin` overrides `NODEDANCE_PUBLIC_ORIGIN` and `public_origin`; `--trusted-proxies` overrides `NODEDANCE_TRUSTED_PROXIES` and `trusted_proxies`.

Browser writes require a matching `Origin` and CSRF cookie/header pair (`X-CSRF-Token`). Native API clients use the same policy: obtain a challenge from `/api/v1/auth/csrf`, preserve its `nodedance_csrf` cookie, send that token in `X-CSRF-Token`, and send the configured public origin. After login, preserve the server-issued session cookie; its value is only validated against a stored digest. There is no origin-free or token-free write API.

Production session idle timeout is 12 hours, login limit is five attempts followed by a 15-minute cooldown, and WebSocket session checks run every ten seconds (always no more than 30 seconds). Short values for `session_idle_timeout`, `login_max_attempts`, `login_lockout_duration`, and `websocket_check_interval` are accepted only with `--dev` for isolated tests.

## Acceptance commands

```bash
make test-stage STAGE=S01          # one complete current acceptance run
make test-integration STAGE=S01    # real Core + SQLite HTTP and recovery checks
make test-e2e STAGE=S01            # real Core with Playwright browser projects
make test-acceptance               # reruns every stage; later stages remain NOT_READY
```

Each complete stage gate runs once and includes the full checks required for that
stage. A failure or code change calls for rerunning the affected suite and any
dependent regression checks; passing suites are not repeated to satisfy a run
count. Required architecture, Engine, browser, and real-service coverage still
has to be present in that run.

S01-12 uses real Playwright projects and a temporary Core data directory. Browser executables that are unavailable are reported `NOT_READY`; they are never counted as passed. Mobile browser testing uses responsive viewport and touch emulation rather than claiming physical iPad/Android coverage.

S00 Docker fixtures use pinned image digests and a separate Docker-in-Docker daemon/socket. They create resources with unique NodeDance labels and only clean resources carrying that exact test-run label. Existing host Docker resources are snapshotted and checked for changes. Never use global Docker pruning or stop a host service as test cleanup.

## Project documents

- [`docs/plan.md`](docs/plan.md): scope, stages, budgets, and release gates.
- [`docs/stages/S00.md`](docs/stages/S00.md): foundation implementation, examples, tests, and evidence.
- [`docs/stages/S01.md`](docs/stages/S01.md): Core storage, administrator authentication, session security, and browser acceptance.
- [`docs/architecture.md`](docs/architecture.md): module boundaries and trust model.
- [`docs/plan-amendments.md`](docs/plan-amendments.md): accepted CI and browser/device test changes.
- [`docs/requirements.json`](docs/requirements.json): P0/P1/P2 traceability.
- [`tests/registry.json`](tests/registry.json): original acceptance IDs and expected evidence.
- [`reports/status.json`](reports/status.json): current machine-readable stage status.

The GitHub Actions matrix covers Linux amd64/arm64 and isolated Docker Engine 28/29 for S00; only stages actually run by a fresh workflow can be described as CI-passed.
