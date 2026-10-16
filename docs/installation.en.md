# Detailed setup and operations

[简体中文 README](../README.md) · [English overview](../README_EN.md)

NodeDance is a self-hosted Go Core and per-host Go Agent for managing Linux servers. The Web console provides administrator sign-in, multi-node host metrics, Docker inventory and control, Compose management, terminal and file access, service probes, alerts, and optional Tailscale-assisted Agent enrollment.

The Core owns administrator identity, Agent enrollment, task records, history, preferences, and SQLite data. Each Agent opens an outbound authenticated WebSocket connection and reports host state. Docker access is optional: an Agent without access to Docker continues to report host metrics and can provide host file and terminal features that are enabled for it.

NodeDance is currently pre-release software. The product version is `0.0.0`; build artifacts from this repository report `dev` unless a build supplies another version. Read the data compatibility note below before replacing a v0 binary.

For Agent container deployment, see [Agent Docker deployment](docker.md). Core runs natively on the controller host.

## Requirements

- Linux for the Core host and Agent hosts.
- Go `1.26.8`, Node.js `22.23.3`, and pnpm `12.10.1` for source builds; see [../.tool-versions](../.tool-versions).
- Docker Engine on an Agent host only when Docker management is needed.
- A TLS HTTPS endpoint for production browser access and Agent connections. A reverse proxy may terminate TLS and forward WebSocket connections to the Core.

Docker is not required to run the Core or collect host metrics.

## Build from source

```sh
make deps
make typecheck
make frontend
make build
```

The binaries are written to `.build/nodedance` and `.build/nodedance-agent`. `make check` runs the frontend checks and Go tests. `sudo make install` installs only the Core binary; `sudo make install-agent` explicitly installs the Agent binary. Both targets copy an existing build to `$(PREFIX)/bin` (default `/usr/local/bin`) without rebuilding as root. `DESTDIR` can stage the install for packaging.

To install the latest published Core release on Linux amd64 or arm64:

```sh
curl -fsSL https://github.com/CST-Cat/NodeDance/releases/latest/download/install-core.sh | sudo bash
```

The installer resolves the latest stable `core-vMAJOR.MINOR.PATCH` tag and verifies its architecture-specific archive against `SHA256SUMS`. It installs `/usr/local/bin/nodedance`, creates the root-owned `nodedance.service`, enables it at boot, and starts it. The service listens on `0.0.0.0:8180` and stores data under `/var/lib/nodedance`. Pass `--release-tag core-vX.Y.Z` to pin a release. Existing Core data is preserved; the installer does not install an Agent or print credentials. It refuses symlinks, directories, and units not marked as managed by NodeDance.

## Run the Core

For local development, start the Core on loopback with development cookies enabled:

```sh
go run ./cmd/nodedance serve --dev --listen 127.0.0.1:8180 --data-dir ./data
```

On the first terminal start, Core asks for an administrator username and a hidden password. Press Enter at either prompt to generate that value automatically, or enter your own. Generated usernames contain 128 random bits; generated passwords contain 256 random bits. Custom usernames must contain 3–80 ASCII letters, digits, underscores or hyphens; custom passwords must contain 12–1024 UTF-8 bytes. The login username is separate from the editable display name.

Core saves the initial login credentials in `initial-admin.json` inside its private `0700` data directory, with file permissions `0600`, and prints only the file path. Read this file locally, open `http://127.0.0.1:8180`, and sign in with both the username and password. The database stores an Argon2id password hash. Core deletes `initial-admin.json` after the first successful login. Restarting an initialized Core preserves its account and password.

When Core starts without a terminal, including the managed systemd service, it generates both values automatically without waiting for input. The release installer starts the service immediately; read the private initial credential file locally for the first login.

The release installer above creates the persistent service automatically. It uses root-owned `/var/lib/nodedance` with mode `0700`, an optional `/etc/default/nodedance` environment file for `NODEDANCE_PUBLIC_ORIGIN` and `NODEDANCE_TRUSTED_PROXIES`, and a root-managed `nodedance.service`. On first unattended start, Core generates an administrator username and password in `/var/lib/nodedance/initial-admin.json` (mode `0600`); read it locally and sign in, then Core removes the file after the first successful login. Existing accounts remain in the database across service restarts and binary upgrades.

Configure the HTTPS reverse proxy for WebSocket upgrades on `/ws/` and forward to `127.0.0.1:8180` or the host's private address. The Core listener itself uses HTTP; use a trusted private network or terminate HTTPS at the proxy rather than exposing plain HTTP to the public Internet.

Manage the service with `sudo systemctl restart nodedance.service`, `sudo systemctl stop nodedance.service`, and `sudo journalctl -u nodedance.service`. A local Agent can run on the same Linux host. Production Agent connections require HTTPS/WSS and a certificate trusted by the Agent host; the Agent uses the system trust store, so no custom CA file is needed for ordinary public certificates. A private CA can be supplied with `--ca-file`. Plain HTTP/WS is accepted only for a literal loopback Core address in development mode. The Agent connects outbound over WSS and reconnects automatically after temporary Core or network outages.

The Core reads JSON configuration from `--config` or `NODEDANCE_CONFIG`; its default is `$XDG_CONFIG_HOME/nodedance/config.json` (or `~/.config/nodedance/config.json` on a typical Linux host). CLI flags take precedence over supported environment variables, which take precedence over the config file. Important options include:

| Setting | Purpose |
| --- | --- |
| `listen` / `--listen` / `NODEDANCE_LISTEN` | Core bind address; app fallback is `127.0.0.1:8180`, installed service uses `0.0.0.0:8180` |
| `data_dir` / `--data-dir` / `NODEDANCE_DATA_DIR` | Private Core state directory |
| `public_origin` / `--public-origin` / `NODEDANCE_PUBLIC_ORIGIN` | Browser HTTPS origin used for request-origin checks |
| `trusted_proxies` / `--trusted-proxies` / `NODEDANCE_TRUSTED_PROXIES` | Proxy IPs or CIDRs trusted for client address forwarding |
| `max_file_transfer_bytes` / `--max-file-bytes` / `NODEDANCE_MAX_FILE_BYTES` | Maximum size of one file transfer |
| `non_metric_history_retention_days` / `--history-retention-days` / `NODEDANCE_HISTORY_RETENTION_DAYS` | Retention for task, event, and audit history |

The Core data directory contains the SQLite database and authentication/encryption keys. Keep it private and include the whole directory in Core backups.

## Enroll and run an Agent

After signing in to the Core console, choose **添加 Agent**, enter a node display name, and generate a one-time enrollment. Choose the URL the target Agent can reach; it defaults to the current console origin and may be a different Core address on a private network. Native/systemd and Docker are side-by-side choices, visible before generating the command. Copy the complete command to the target Linux host and run it; it includes the short-lived token and does not prompt for it again. Expiry is displayed in Beijing time (UTC+8), while the API retains an ISO UTC timestamp.

The generated command downloads the current installation script and defaults to the latest stable published Agent at execution time, without a hardcoded tag. Python 3 parses the paginated release listing, excludes Core tags, drafts and prereleases, and compares Agent version numbers. The selected release supplies the matching binary, checksum manifest and Docker image. `agent-release.version` remains a Core publication pairing check. The installer supports Linux `amd64` and `arm64`, verifies the matching Agent binary against that release's SHA256 manifest, prepares root-owned state, enrolls through `nodedance-agent enroll --token-stdin`, then installs, enables, and starts the root systemd service or the rootful Docker container, according to the selected mode. It refuses existing Agent state, binaries, or a same-name service, and will not overwrite existing config or service files. If enrollment may have reached Core but the response was lost, it preserves the protected config and provides the recovery command.

To publish Agent artifacts, add the workflow to the tagged commit, then create and push an Agent-only tag such as `agent-v0.1.0`. Only `agent-v*` tags trigger `.github/workflows/agent-release.yml`; it builds Linux amd64/arm64 binaries, creates `SHA256SUMS`, and attaches those files plus the installer to a release with the same tag. Core release tags do not trigger this workflow. Generated commands do not specify a release tag; the installer uses Python 3 to parse the release listing and selects the newest stable Agent at execution time. If none exists, installation stops before creating credentials or starting a container.

The Agent runs as root for full host management; `enroll`, `recover`, and `run` reject non-root callers. It verifies the config remains root-owned, regular, and mode `0600` inside a `0700` state directory. Root file management exposes host paths while blocking kernel/runtime trees and Agent credentials. NodeDance does not change Docker socket permissions.

For same-host development against a loopback Core, build with `make build` and use `sudo .build/nodedance-agent enroll --dev --server http://127.0.0.1:8180 --config /var/lib/nodedance-agent/agent.json --token-stdin`. `--dev` is restricted to literal loopback HTTP. All non-development Agent endpoints require HTTPS.

The installer writes a NodeDance-managed root service without a filesystem/system write sandbox. It refuses unrelated or unmarked units, reloads systemd, and can enable/start the Agent. Inspect it with `sudo systemctl status nodedance-agent.service`; restart or stop it with `sudo systemctl restart nodedance-agent.service` or `sudo systemctl stop nodedance-agent.service`. If enrollment may have reached Core but the response was lost, retain the private config and run `sudo nodedance-agent recover --config /var/lib/nodedance-agent/agent.json`. Recovery checks the saved device credential and does not replay the one-time token. `sudo nodedance-agent uninstall-systemd` stops/removes only the current NodeDance-managed root unit; it preserves the binary and credentials.

Use `--file-root /absolute/path` to limit file-manager access to a specific directory, or `--no-file-root` to disable file management. Docker access is optional. NodeDance does not change Docker socket permissions. If Docker is absent or unavailable, host monitoring continues.

### Optional Tailscale discovery and SSH-assisted Agent installation

Tailscale is optional. Core startup does not install, configure, or require it. If the Core host has the Tailscale CLI installed and is logged in, an administrator can open **发现节点** in the console; discovery reads that host's local `tailscale status --json`. If the CLI is absent or not logged in, ordinary Core management and manual Agent enrollment continue to work.

SSH-assisted Agent deployment is available only for visible, online Linux peers and requires a signed Agent artifact on the Core. The installer creates no Agent service account: it prepares root-owned credentials and installs the same root-only systemd service used by manual enrollment. Build Linux `amd64` and/or `arm64` Agent binaries, sign each binary with an Ed25519 release key kept outside the Core host, and place each binary with its detached signature at `linux-amd64/nodedance-agent[.sig]` or `linux-arm64/nodedance-agent[.sig]` under a private artifact directory. Configure the Core service environment with the artifact directory and the base64-encoded raw Ed25519 public key, then restart Core:

```ini
Environment=NODEDANCE_AGENT_ARTIFACT_DIR=/var/lib/nodedance-agent-artifacts
Environment=NODEDANCE_AGENT_SIGNING_PUBLIC_KEY=<base64-encoded-raw-ed25519-public-key>
```

Add those lines in `sudo systemctl edit nodedance.service`, save the override, then run `sudo systemctl daemon-reload && sudo systemctl restart nodedance.service`.

The public key is not secret; keep the signing private key off the Core host. In the UI, refresh discovery, select a Linux peer, read its SSH host-key fingerprint, and verify it through an independent trusted channel before confirming. Credentials are held only for the active deployment request and are not stored in task history. The UI displays a manual SSH recovery command. If signed artifacts are not configured, use the generic **添加 Agent** enrollment flow above. Tailscale and SSH deployment should be used only when you administer those systems.

## CLI and terminal UI

All administrator operations are discoverable from `nodedance operations` as JSON, including parameter/body schemas. CLI and TUI share the same operation catalog and authenticated API client:

```sh
export NODEDANCE_SERVER=https://nodedance.example
nodedance auth login --body @/private/login.json
nodedance nodes list
nodedance describe containers.action
nodedance tui --timeout 0
```

Remote CLI operations return JSON; logs, metrics and terminal connections use JSONL. The TUI provides searchable operations, parameter forms, file transfers, paginated results and an interactive host/container shell. Local Agent lifecycle commands are available through `sudo nodedance agent ...`, which invokes the separate Agent binary. Read [CLI/TUI usage](cli.md) for authentication, task idempotency, streams and examples, and [the Komari communication study](komari-communication.md) for the protocol design.

## Use probes and alerts

Service probes are configured from a node's service-probe panel and execute from that node's Agent. Supported targets are HTTP, HTTPS, and TCP. HTTPS certificate validation is enabled; redirects are not followed. Probe timeouts are bounded, and offline or unsupported Agents produce an unknown result rather than a service failure. Three consecutive failures mark a probe unhealthy; two consecutive successes restore healthy status.

The Alert Center manages rules, maintenance windows, notification channels, and delivery history. Webhooks use administrator-configured HTTP(S) URLs. SMTP requires STARTTLS or implicit TLS; plaintext SMTP is rejected, including on loopback. Notification secrets are encrypted in the Core database using a key stored in the private data directory.

## Back up and restore Core data

Create a consistent SQLite snapshot and a private archive with:

```sh
nodedance backup --data-dir /var/lib/nodedance --output /secure/backups/nodedance-2026-10-09.tar
```

The output path must not already exist and must be outside the Core data directory. The backup archive contains the SQLite database and required authentication/encryption keys, plus optional first-run credential or Tailscale state files when present; it is created with mode `0600`. Store it in a restricted location, and protect any off-host copy as sensitive data.

Restore to a new or empty directory on Linux:

```sh
nodedance restore \
  --input /secure/backups/nodedance-2026-10-09.tar \
  --data-dir /var/lib/nodedance-restored
```

Restore validates the archive, file hashes, required keys, and SQLite integrity before atomically installing the data. It refuses a non-empty destination. Stop the Core before switching it to the restored directory. Verify administrator login, node identities, and required notification settings before returning the restored Core to service.

The database snapshot keeps administrator and Agent credentials, node identities, dashboard preferences, tasks, probes, alerts, notification history, and their related records together. The alert-channel encryption key is included so restored notification secrets remain decryptable.

## Upgrade and data compatibility

Run `nodedance update` to select the newest stable Core tag. Protocol, capability, API and SQLite fingerprints do not gate version selection. The built-in transaction verifies the release artifact, preserves data for recovery, switches the runtime and checks its health; failed startup restores the previous runtime and pre-update data. A raw binary replacement alone does not restore data changed by the newer runtime. See [updates](updates.md).

NodeDance is still at product version `0.0.0`. The current SQLite schema is initialized directly; automatic upgrades from older development schema layouts are not supported. An incompatible development database may be removed and initialized with the current schema. Normal backup/restore applies to a database created by a compatible current v0 build. Product version and SQLite structure are separate concepts.

Agent systemd installation is root-only. It recognizes only the current NodeDance-managed root unit and refuses to overwrite unrelated, unmarked, shadowed, or changed units. Uninstall removes that unit while preserving the Agent config and identity.

## Troubleshooting

- **Browser setup or login does not persist:** use HTTPS in production so secure session cookies are accepted. `--dev` is for loopback development only.
- **Agent remains offline:** check the configured Core HTTPS/WSS URL, outbound connectivity, enrollment state, and Agent service log with `systemctl status nodedance-agent` and `journalctl -u nodedance-agent`.
- **Host metrics work but Docker is unavailable:** check Docker Engine status and whether the Agent can access its socket. NodeDance intentionally leaves socket permissions unchanged.
- **File access is unavailable:** the root Agent enables full-host browsing by default, except `/proc`, `/sys`, `/dev`, `/run`, and the Agent credential state directory. Check for `NODEDANCE_AGENT_FILE_ACCESS=disabled`; `--file-root` can narrow access.
- **A probe reports unknown:** check that the node is online and negotiated the probe capability. Unknown means there is no current result; it is not treated as a failed service.
- **Restore is rejected:** use a complete private backup archive, an existing parent directory, and a destination directory that is absent or empty. Restore does not overwrite existing non-empty data.

For local API and Web debugging, set `VITE_CORE_TARGET` when the Core is not at `http://127.0.0.1:8180`; Vite forwards `/api` and `/ws` requests to that target.

## Language and releases / 语言与发布

支持 English 和简体中文。Web 顶部切换语言；CLI 使用 `--lang en|zh-CN`，TUI 还可按 `l` 切换。Core 使用 `core-v*`，Agent 使用 `agent-v*` 独立发布，发布说明提供中英双语。

[语言约定](i18n.md) · [发布流程](releases.md) · [Komari 优化学习](komari-optimizations.md)

Pending nodes retain an Installation command entry on their detail page. Reopening it lets you regenerate a fresh ten-minute credential for the same node and repeatedly copy either deployment command. Core stores only token digests, so regeneration invalidates previously issued commands. Once registered, enrollment regeneration is rejected. Delete node is located at the bottom of the Web Node settings tab and is available in every node state and removes Core-owned records and credentials, without uninstalling the remote Agent or claiming to stop dispatched work.
