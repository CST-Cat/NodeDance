# NodeDance

NodeDance is a staged Linux server and Docker management project. The current source implements the S00 foundation: a Go Core shell, embedded landing page, health endpoint, build/test tooling, and isolated Docker acceptance fixtures. **The Agent enrollment/runtime and all management features are not implemented yet. S01–S17 are `NOT_READY`; this repository is not ready to manage a server.** Check [`reports/status.json`](reports/status.json) and the stage reports for current evidence. A local S00 pass does not mean later stages or GitHub Actions have passed.

## Local setup

The bootstrap downloads only locked tool versions into `.tools/`; it does not replace system Go, Node, pnpm, Docker, or the user's Docker configuration. The pnpm package and platform-native executable are pinned in `tooling/pnpm/package-lock.json`; the Compose plugin is SHA-256 checked and selected through a NodeDance-only Docker CLI config.

```bash
make bootstrap
make check
make build
```

The resulting binaries are under `.build/`. Node.js is needed for frontend development and builds, not to run the Core binary.

## Run the S00 Core shell

No arguments start the development Core on the loopback-only default `127.0.0.1:8180`:

```bash
.build/nodedance
```

The equivalent explicit command is:

```bash
.build/nodedance serve --listen 127.0.0.1:8180
```

Check the embedded page and health endpoint:

```bash
curl -i http://127.0.0.1:8180/
curl -i http://127.0.0.1:8180/api/v1/health
```

The landing page and health endpoint are the only implemented HTTP behavior. Unimplemented API and WebSocket management routes return `404`; they do not expose unauthenticated data. Port conflicts are errors: Core will not choose another port or stop the process that owns 8180. `--dev` is restricted to loopback addresses.

## Acceptance commands

```bash
make test-stage STAGE=S00         # S00 checks, each executed three consecutive times
make test-integration STAGE=S00   # real Docker fixture and cross-build cases
make test-e2e STAGE=S00           # listener/configuration/port conflict cases
make test-acceptance              # reruns all stages and lists unfinished stages as NOT_READY
```

The test fixtures use pinned image digests and a separate Docker-in-Docker daemon/socket. They create resources with unique NodeDance labels and only clean resources carrying that exact test-run label. Existing host Docker resources are snapshotted and checked for changes. Do not use `docker system prune` as test cleanup.

## Project documents

- [`docs/plan.md`](docs/plan.md): NodeDance scope, toolchain, stages, budgets, and release gates.
- [`docs/stages/S00.md`](docs/stages/S00.md): S00 implementation steps, examples, tests, evidence, and rejection criteria.
- [`docs/architecture.md`](docs/architecture.md): module boundaries and communication model.
- [`docs/plan-amendments.md`](docs/plan-amendments.md): accepted CI/browser/device test changes.
- [`docs/requirements.json`](docs/requirements.json): P0/P1/P2 and design requirement trace.
- [`tests/registry.json`](tests/registry.json): original acceptance IDs and supplemental checks.
- [`reports/status.json`](reports/status.json): current machine-readable stage status.

Only stages with fresh, complete evidence may report `PASS`. The GitHub Actions matrix covers Linux amd64/arm64 and isolated Docker Engine 28/29; workflow configuration alone is not a CI result. The remote workflow has not been run until an authorized push starts it.
