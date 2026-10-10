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

Create an enrollment credential in the Core console, then enroll the Agent on the target Linux host. The token is read from stdin; it is not placed in the command line or shell history.

If building from this checkout on that host, first run `make build` and `sudo make install`. Otherwise install a trusted release binary after verifying its release signature. Use the same `nodedance-agent` executable for enrollment, recovery, foreground execution, and systemd installation.

```sh
nodedance-agent enroll --server https://nodedance.example --token-stdin
nodedance-agent run
```

When using a terminal, paste the one-time token, press Enter, then finish stdin with Ctrl-D on Linux/macOS (or Ctrl-Z followed by Enter on Windows). For same-host development against a loopback Core, use `--dev --server http://127.0.0.1:8180`; `--dev` accepts only literal loopback HTTP addresses.

For a persistent Agent, create a dedicated unprivileged account and private state directory, enroll with an explicit config path, then install its systemd unit:

```sh
getent passwd nodedance-agent >/dev/null || sudo useradd --system --home-dir /var/lib/nodedance-agent --shell /usr/sbin/nologin nodedance-agent
sudo install -d -o nodedance-agent -g nodedance-agent -m 0700 /var/lib/nodedance-agent
sudo -u nodedance-agent nodedance-agent enroll \
  --server https://nodedance.example \
  --config /var/lib/nodedance-agent/agent.json \
  --token-stdin
sudo nodedance-agent install-systemd \
  --user nodedance-agent \
  --config /var/lib/nodedance-agent/agent.json \
  --enable
```

The installer writes a NodeDance-managed system unit, refuses an unrelated or unmarked existing unit, reloads systemd, and enables/starts the Agent. Inspect it with `sudo systemctl status nodedance-agent.service`; restart or stop it with `sudo systemctl restart nodedance-agent.service` or `sudo systemctl stop nodedance-agent.service`. If enrollment may have reached Core but the response was lost, retain the private config and run `sudo -u nodedance-agent nodedance-agent recover --config /var/lib/nodedance-agent/agent.json`. Recovery checks the saved device credential and does not replay the one-time token.

Add `--file-root /absolute/path` only when the Agent should expose that host directory in the console. Docker access is optional. If it is authorized, pass `--supplementary-group docker` when installing the unit (or use an explicitly approved group). Membership in the Docker group grants powerful control of the host. NodeDance does not change Docker socket permissions. If Docker is absent or the Agent user cannot access it, the console reports Docker as unavailable while host monitoring continues.

### Optional Tailscale discovery and SSH-assisted Agent installation

Tailscale is optional. Core startup does not install, configure, or require it. If the Core host has the Tailscale CLI installed and is logged in, an administrator can open **发现节点** in the console; discovery reads that host's local `tailscale status --json`. If the CLI is absent or not logged in, ordinary Core management and manual Agent enrollment continue to work.

SSH-assisted deployment is available only for visible, online Linux peers and requires a signed Agent artifact on the Core. Build Linux `amd64` and/or `arm64` Agent binaries, sign each binary with an Ed25519 release key kept outside the Core host, and place each binary with its detached signature at `linux-amd64/nodedance-agent[.sig]` or `linux-arm64/nodedance-agent[.sig]` under a private artifact directory. Configure the Core service environment with the artifact directory and the base64-encoded raw Ed25519 public key, then restart Core:

```ini
Environment=NODEDANCE_AGENT_ARTIFACT_DIR=/var/lib/nodedance-agent-artifacts
Environment=NODEDANCE_AGENT_SIGNING_PUBLIC_KEY=<base64-encoded-raw-ed25519-public-key>
```

Add those lines in `sudo systemctl edit nodedance.service`, save the override, then run `sudo systemctl daemon-reload && sudo systemctl restart nodedance.service`.

The public key is not secret; keep the signing private key off the Core host. In the UI, refresh discovery, select a Linux peer, read its SSH host-key fingerprint, and verify it through an independent trusted channel before confirming. Credentials are held only for the active deployment request and are not stored in task history. The UI displays a manual SSH recovery command. If signed artifacts are not configured, use the manual enrollment procedure above. Tailscale and SSH deployment should be used only when you administer those systems.

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

## Upgrade and data compatibility

Before replacing a Core binary, create a backup. Replace it only with a v0 build that is compatible with the current SQLite schema. Stop the Core, replace the binary, start it, then verify login and a connected Agent before upgrading other hosts. To roll back, stop the Core and restore the database backup created for the prior build before starting that prior binary; replacing the binary alone is not a supported rollback.

NodeDance is still at product version `0.0.0`. The current SQLite schema is initialized directly; automatic upgrades from older development schema layouts are not supported. An incompatible development database may be removed and initialized with the current schema. Normal backup/restore applies to a database created by a compatible current v0 build. Product version and SQLite structure are separate concepts.

Agent unit installation recognizes only the current NodeDance-managed unit marker. It refuses to overwrite unrelated or unmarked existing systemd units; do not expect an old unit format to be upgraded automatically. Review and resolve an unmarked existing unit explicitly before installing the current unit.

## Troubleshooting

- **Browser setup or login does not persist:** use HTTPS in production so secure session cookies are accepted. `--dev` is for loopback development only.
- **Agent remains offline:** check the Core HTTPS/WSS URL, outbound connectivity, enrollment state, and Agent service log with `systemctl status nodedance-agent` and `journalctl -u nodedance-agent`.
- **Host metrics work but Docker is unavailable:** check Docker Engine status and whether the configured Agent account can access its socket. NodeDance intentionally leaves socket permissions unchanged.
- **File access is unavailable:** verify that the Agent unit has an explicit `--file-root` and that the service account can access it. The Agent confines file operations to the configured root.
- **A probe reports unknown:** check that the node is online and negotiated the probe capability. Unknown means there is no current result; it is not treated as a failed service.
- **Restore is rejected:** use a complete private backup archive, an existing parent directory, and a destination directory that is absent or empty. Restore does not overwrite existing non-empty data.

For local API and Web debugging, set `VITE_CORE_TARGET` when the Core is not at `http://127.0.0.1:8180`; Vite forwards `/api` and `/ws` requests to that target.
