<h1 align="center"><img src="web/public/logo.svg" width="32" height="32" alt="" /> NodeDance</h1>

<p align="center"><a href="README.md">简体中文</a> | <a href="README_EN.md">English</a></p>

NodeDance is a self-hosted Linux fleet monitoring and management system made up of a **Core** and an **Agent** installed on each host.

Web, CLI and TUI share the Core operation contract. CLI and TUI expose the same terminal capabilities; Web adds graphical interactions. See [interface coverage and verification](docs/interface-coverage.md) and [terminal usage](docs/cli.md).

## Install

### Core

Run this on a Linux x86_64 or ARM64 server. The installer selects the latest stable release:

```sh
curl -fsSL https://github.com/CST-Cat/NodeDance/releases/latest/download/install-core.sh | sudo bash
```

This installs the Core binary and automatically creates, enables, and starts the root-owned `nodedance.service`, bound to `0.0.0.0:8180` with data under `/var/lib/nodedance`. On first unattended startup, Core creates credentials in a private `0600` `initial-admin.json` file and removes it after the first successful login. Production deployments also need an HTTPS reverse proxy; see the [deployment guide](docs/installation.en.md#run-the-core).

On first interactive start, enter a username and password or press Enter to generate either securely. Unattended starts generate both in the private `initial-admin.json` file, removed after the first successful login.

### Agent

Sign in to Core, open **Add Agent**, enter a node name, and generate the installer command. Choose the side-by-side native/systemd or Docker option and run the complete command on the target Linux host. The enrollment token is included; no second token input is required. The command resolves the latest stable Agent release when run. Agent runs as root; expiry is shown in Beijing time (UTC+8).

Core is installed natively on the controller host. Only Agent supports [Docker deployment](docs/docker.md).

## Features

- **Host monitoring:** CPU, memory, swap, load, processes, sockets, disk, GPU, network addresses, billing traffic, and uptime across Linux hosts.
- **Docker management:** inspect and manage containers, images, and Compose projects. Docker is optional; host monitoring works without it.
- **Remote operations:** confirmed root scripts and batch execution, Agent settings/diagnostics, Web terminal and file manager, CLI, and terminal UI.
- **Probes and alerts:** HTTP(S), TCP and IPv4/IPv6 ICMP probes, multi-node targets, latency/loss quality, metric thresholds, asset expiry/traffic quota reminders, maintenance windows, and notifications.
- **Backup and restore:** preserve Core data, keys, node identities, and history.
- **File manager:** shadcn/UIArc list and grid with one shared custom context menu and command-only elFinder integration with paginated browsing/search, copy/move, ZIP, versioned editing, file/folder uploads and controlled previews over the shared Core/Agent file service. See [file management](docs/file-manager.md). Right-click file/folder sharing adds expiry, download limits, passwords, direct links, QR codes, revocation and file collection; see [file sharing](docs/file-sharing.md).
- **Terminal recovery:** Agent embeds complete tmux; Web, CLI and TUI reattach the same session after transport loss and tmux repaints the screen. Input is never replayed. See [terminal recovery](docs/cli.md#terminal-recovery--终端恢复).
- **Web navigation:** resolve the system color scheme before first paint and remember saved themes. Collapse or resize the desktop sidebar (200–400px); both persist in the current browser. Node cards hide Docker details until expanded. Click a VPS name for details; the terminal shortcut opens a modal over the current overview. Mobile navigation uses a drawer.
- **Agent enrollment:** register nodes in Core and generate one-command installers. When Tailscale is detected, NodeDance can use the existing network capability for discovery and assisted deployment; it does not install or configure Tailscale.

Agent enrollment, recovery, runtime, file service, and systemd service require root access.

## Documentation

- [Detailed setup and operations](docs/installation.en.md)
- [CLI and terminal UI](docs/cli.md)
- [Node overview, host information and memo](docs/overview.md)
- [Agent capabilities and Komari comparison](docs/agent-capabilities.md)
- [Architecture](docs/architecture.md)
- [Release notes](docs/releases.md)

正式更新 / Updates: [Web、CLI、TUI 更新、回滚与控制服务配置](docs/updates.md).

The Core includes [RSS/Atom subscriptions](docs/rss.md), with adjustable refresh intervals (minimum 1 minute, default 30 minutes), shared read state, keyword notifications and digests.

Node details expose Delete node for pending, online, offline and revoked nodes. Deletion removes Core data and credentials; it does not uninstall the remote Agent or stop dispatched work. Pending nodes expose Installation command: regenerate and repeatedly copy native/Docker commands without creating a new node. Regeneration immediately invalidates previous tokens.
