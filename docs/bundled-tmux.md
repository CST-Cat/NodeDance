# Bundled tmux / 内置 tmux

NodeDance Agent embeds **complete upstream tmux 3.7c** for Linux amd64 and arm64. No feature-level source fork is maintained. The pinned static builds come from the [upstream tmux-builds release](https://github.com/tmux/tmux-builds/releases/tag/v3.7c), linked by the [upstream installation guide](https://github.com/tmux/tmux/wiki/Installing). Each architecture adds roughly 1 MB of compressed executable data to its Agent. Static musl, libevent, ncurses and utf8proc dependencies are included; runtime package installation and download are unnecessary.

Agent 内置完整上游 tmux 3.7c，支持 Linux amd64、arm64，不维护裁剪源码分支。每种架构向 Agent 增加约 1 MB 压缩二进制，静态依赖已包含；目标主机和容器无需安装额外软件。窗口、分屏、复制模式等 tmux 功能保留，默认隐藏状态栏以适配 NodeDance 弹窗。

The archives, source URLs and SHA-256 digests are recorded in [manifest.json](../internal/agent/terminal/tmuxbundle/manifest.json). Go build constraints embed only the matching executable. `python3 scripts/vendor-tmux.py --check` verifies both archives, ELF architectures, static linkage, terminal descriptions and license presence offline. `--refresh` downloads only those exact pinned archives and verifies their digests before writing them. Updating the version requires reviewing the manifest, embedded version and matching upstream notices together; there is no `latest` download at build or runtime.

归档、来源和 SHA-256 校验值记录在 manifest 中；Go 只嵌入目标架构的二进制。`--check` 离线验证归档、架构、静态链接、终端描述和许可证。`--refresh` 仅下载固定版本并验证校验值。升级时须一起审核固定版本、校验值和上游许可证。

The embedded terminfo entries (`xterm-256color`, `tmux-256color`, `screen-256color`) and their text source come from ncurses 6.4.20240113. They can be rebuilt with `tic -x -o internal/agent/terminal/tmuxbundle/assets/terminfo internal/agent/terminal/tmuxbundle/assets/terminfo.src`. The [upstream notices](../internal/agent/terminal/tmuxbundle/licenses) for tmux and every bundled dependency are embedded and extracted beside the executable.

Runtime is root-only. Executables and notices are extracted beneath the protected `0700` Agent state directory; private socket workspaces use `/run` with `0700` permissions. This supports systems mounting `/run` with `noexec`. `TERMINFO` and `PATH` point to the private bundle inside managed host shells. Dedicated sockets and `tmux.conf` isolate NodeDance from existing system/user tmux servers and configuration. The Agent removes its workspaces after terminating their sessions.

运行时仅支持 root；二进制释放在 Agent 的 `0700` 私有状态目录下，socket 使用 `/run` 下独立的 `0700` 目录，支持 `/run` 挂载为 `noexec` 的系统。独立 socket 和配置不会影响用户已有 tmux；会话结束后 Agent 清理工作目录。文件管理继续拒绝 Agent 状态目录和受保护的运行时路径。

Native release CI checks the archives and runs real tmux session tests as root on both architectures. Tests cover shell state and background jobs surviving detach/attach, full pane support and explicit termination. Docker builds embed the same assets through the normal Go build. Installer and release checksums cover the entire Agent, including tmux, so no separate deployment or update channel is introduced. See [terminal recovery](cli.md#terminal-recovery--终端恢复) for lifecycle and authorization behavior.
