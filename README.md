# NodeDance

NodeDance is a self-hosted Go Core and per-host Go Agent for managing Linux servers. The Web console provides administrator sign-in, multi-node host metrics, Docker inventory and control, Compose management, terminal and file access, service probes, alerts, and optional Tailscale-assisted Agent enrollment.

The Core owns administrator identity, Agent enrollment, task records, history, preferences, and SQLite data. Each Agent opens an outbound authenticated WebSocket connection and reports host state. Docker access is optional: an Agent without access to Docker continues to report host metrics and can provide host file and terminal features that are enabled for it.

NodeDance is currently pre-release software. The product version is `0.0.0`; build artifacts from this repository report `dev` unless a build supplies another version. Read the data compatibility note below before replacing a v0 binary.

## Requirements

- Linux for the Core host and Agent hosts.
- Go `1.26.8`, Node.js `22.23.3`, and pnpm `12.10.1` for source builds; see [.tool-versions](.tool-versions).
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

The binaries are written to `.build/nodedance` and `.build/nodedance-agent`. `make check` runs the frontend checks and Go tests. To install already-built binaries, run `sudo make install`; it copies both files to `$(PREFIX)/bin` (default `/usr/local/bin`) without rebuilding as root. `DESTDIR` can stage the install for packaging, for example `make install DESTDIR=/tmp/nodedance-package`.

## Run the Core

For local development, start the Core on loopback with development cookies enabled:

```sh
go run ./cmd/nodedance serve --dev --listen 127.0.0.1:8180 --data-dir ./data
```

Open `http://127.0.0.1:8180`. On first start, the Core prints the path to a private, one-time setup credential file. Enter that credential in the first-run setup flow and choose an administrator password of at least 12 UTF-8 bytes. The Core removes the credential file after setup succeeds.

For a persistent installation, build and install the binaries, then create a dedicated Core service account and private data directory:

```sh
make build
sudo make install
getent passwd nodedance >/dev/null || sudo useradd --system --home-dir /var/lib/nodedance --shell /usr/sbin/nologin nodedance
sudo install -d -o nodedance -g nodedance -m 0700 /var/lib/nodedance
```

Create the service unit only when the destination path is absent. This command uses a temporary file and a no-clobber hard link, so an existing unit or symlink is not overwritten:

```sh
sudo sh -c 'set -eu; unit=/etc/systemd/system/nodedance.service; tmp=$(mktemp /etc/systemd/system/.nodedance.service.XXXXXX); trap "rm -f \"$tmp\"" EXIT; cat >"$tmp"; chmod 0644 "$tmp"; ln "$tmp" "$unit"; rm "$tmp"; trap - EXIT' <<'EOF'
[Unit]
Description=NodeDance Core
After=network-online.target
Wants=network-online.target

[Service]
User=nodedance
Group=nodedance
Environment=NODEDANCE_LISTEN=127.0.0.1:8180
Environment=NODEDANCE_DATA_DIR=/var/lib/nodedance
Environment=NODEDANCE_PUBLIC_ORIGIN=https://nodedance.example
ExecStart=/usr/local/bin/nodedance serve
Restart=on-failure
RestartSec=3
UMask=0077
NoNewPrivileges=true
ProtectSystem=strict
ProtectHome=true
ReadWritePaths=/var/lib/nodedance

[Install]
WantedBy=multi-user.target
EOF

sudo systemctl daemon-reload
sudo systemctl enable --now nodedance.service
sudo systemctl status nodedance.service
```

The unit listens on loopback and stores its database and keys under `/var/lib/nodedance`. Configure the reverse proxy for HTTPS, WebSocket upgrades on `/ws/`, and forwarding to `127.0.0.1:8180`. Set `NODEDANCE_TRUSTED_PROXIES` or `trusted_proxies` only to the proxy addresses that actually connect to the Core. Do not expose an unencrypted Core listener to the public Internet. On first start, read the one-time setup credential from the private file path printed in the service log (normally `sudo -u nodedance cat /var/lib/nodedance/setup-credential.txt`), then enter it in the browser setup flow. The Core removes that credential after setup succeeds.

Manage the service with `sudo systemctl restart nodedance.service`, `sudo systemctl stop nodedance.service`, and `sudo journalctl -u nodedance.service`. A local Agent can run on the same Linux host. In production, enroll it against an HTTPS origin with a valid certificate; a reverse proxy can route that origin back to the loopback Core. For local development only, use the loopback `--dev` flow below. The Agent connects outbound over WSS for HTTPS origins and reconnects automatically after temporary Core or network outages.

The Core reads JSON configuration from `--config` or `NODEDANCE_CONFIG`; its default is `$XDG_CONFIG_HOME/nodedance/config.json` (or `~/.config/nodedance/config.json` on a typical Linux host). CLI flags take precedence over supported environment variables, which take precedence over the config file. Important options include:

| Setting | Purpose |
| --- | --- |
| `listen` / `--listen` / `NODEDANCE_LISTEN` | Core bind address; default `127.0.0.1:8180` |
| `data_dir` / `--data-dir` / `NODEDANCE_DATA_DIR` | Private Core state directory |
| `public_origin` / `--public-origin` / `NODEDANCE_PUBLIC_ORIGIN` | Browser HTTPS origin used for request-origin checks |
| `trusted_proxies` / `--trusted-proxies` / `NODEDANCE_TRUSTED_PROXIES` | Proxy IPs or CIDRs trusted for client address forwarding |
| `max_file_transfer_bytes` / `--max-file-bytes` / `NODEDANCE_MAX_FILE_BYTES` | Maximum size of one file transfer |
| `non_metric_history_retention_days` / `--history-retention-days` / `NODEDANCE_HISTORY_RETENTION_DAYS` | Retention for task, event, and audit history |

The Core data directory contains the SQLite database and authentication/encryption keys. Keep it private and include the whole directory in Core backups.

## Enroll and run an Agent

After signing in to the Core console, choose **添加 Agent**, enter a node display name, and generate a one-time enrollment. The page shows the Core-bound installer command and the short-lived token separately. Copy the command to the target Linux host, run it, then paste the token only into the installer's hidden `/dev/tty` prompt. The token is never part of the command, shell history, process arguments, or environment.

The target host command is:

```bash
bash -o pipefail -c 'curl --fail --silent --show-error --location --proto =https --proto-redir =https --tlsv1.2 https://github.com/CST-Cat/NodeDance/releases/latest/download/install-agent.sh | sudo bash -s -- --server '\''https://nodedance.example'\'' --display-name '\''production-01'\'''
```

The installer supports Linux `amd64` and `arm64`. It downloads the matching Agent and the release SHA256 manifest over HTTPS, verifies the binary, prepares root-owned state, enrolls through `nodedance-agent enroll --token-stdin`, then installs, enables, and starts the root systemd service. It refuses existing Agent state, binaries, or a same-name service, and will not overwrite existing config or service files. If enrollment may have reached Core but the response was lost, it preserves the protected config and provides the recovery command.

**Release availability:** this repository currently has no published GitHub Release, so the `releases/latest/download` URL above is not usable yet. Until the first release is published, the installer detects the missing release before modifying the target host and exits with an explicit message. A maintainer triggers the release workflow by pushing a version tag after the workflow is present on the tagged commit; for example, `git tag v0.1.0` followed by `git push origin v0.1.0`. GitHub Actions builds the two Agent binaries, generates `SHA256SUMS`, attaches the installer, and creates the Release. Do not run the curl command until that workflow has completed and the release assets are available.

The Agent runs as root for full host management; `enroll`, `recover`, and `run` reject non-root callers. It verifies the config remains root-owned, regular, and mode `0600` inside a `0700` state directory. Root file management exposes host paths while blocking kernel/runtime trees and Agent credentials. NodeDance does not change Docker socket permissions.

For same-host development against a loopback Core, build with `make build` and use `sudo .build/nodedance-agent enroll --dev --server http://127.0.0.1:8180 --config /var/lib/nodedance-agent/agent.json --token-stdin`. `--dev` accepts only literal loopback HTTP addresses; production URLs must use HTTPS with a valid certificate.

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

Before replacing a Core binary, create a backup. Replace it only with a v0 build that is compatible with the current SQLite schema. Stop the Core, replace the binary, start it, then verify login and a connected Agent before upgrading other hosts. To roll back, stop the Core and restore the database backup created for the prior build before starting that prior binary; replacing the binary alone is not a supported rollback.

NodeDance is still at product version `0.0.0`. The current SQLite schema is initialized directly; automatic upgrades from older development schema layouts are not supported. An incompatible development database may be removed and initialized with the current schema. Normal backup/restore applies to a database created by a compatible current v0 build. Product version and SQLite structure are separate concepts.

Agent systemd installation is root-only. It recognizes only the current NodeDance-managed root unit and refuses to overwrite unrelated, unmarked, shadowed, or changed units. Uninstall removes that unit while preserving the Agent config and identity.

## Troubleshooting

- **Browser setup or login does not persist:** use HTTPS in production so secure session cookies are accepted. `--dev` is for loopback development only.
- **Agent remains offline:** check the Core HTTPS/WSS URL, outbound connectivity, enrollment state, and Agent service log with `systemctl status nodedance-agent` and `journalctl -u nodedance-agent`.
- **Host metrics work but Docker is unavailable:** check Docker Engine status and whether the Agent can access its socket. NodeDance intentionally leaves socket permissions unchanged.
- **File access is unavailable:** the root Agent enables full-host browsing by default, except `/proc`, `/sys`, `/dev`, `/run`, and the Agent credential state directory. Check for `NODEDANCE_AGENT_FILE_ACCESS=disabled`; `--file-root` can narrow access.
- **A probe reports unknown:** check that the node is online and negotiated the probe capability. Unknown means there is no current result; it is not treated as a failed service.
- **Restore is rejected:** use a complete private backup archive, an existing parent directory, and a destination directory that is absent or empty. Restore does not overwrite existing non-empty data.

For local API and Web debugging, set `VITE_CORE_TARGET` when the Core is not at `http://127.0.0.1:8180`; Vite forwards `/api` and `/ws` requests to that target.
