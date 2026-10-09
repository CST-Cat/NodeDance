# NodeDance

NodeDance is a Go Core and per-host Go Agent for managing Linux servers from a Web console. The Core owns administrator authentication, node identity, SQLite state, and the WebSocket control plane. Each Agent connects outbound to the Core and reports host metrics and Docker inventory.

The repository is being developed in the order described by [docs/plan.md](docs/plan.md). The current source is not a claim that every planned module has been delivered.

## Development

Use the Go, Node.js, and pnpm versions declared in [.tool-versions](.tool-versions). Install dependencies, then run the focused development commands:

```sh
make deps
make typecheck
make frontend
make build
```

Start a local Core with `make run`. The default listener is `127.0.0.1:8180`; configure the listen address and data directory with the Core command-line flags. Start an enrolled local Agent with `make agent`.

For Vite development, set `VITE_CORE_TARGET` to the Core URL when it differs from `http://127.0.0.1:8180`. Vite forwards `/api` and `/ws` requests to that target.

`make check` runs the frontend type check and build plus the Go unit tests. `make build` builds the Core and Agent binaries into `.build/`.
