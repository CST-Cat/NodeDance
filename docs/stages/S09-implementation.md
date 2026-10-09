# S09 terminal implementation and evidence

## Scope

S09 adds short-lived host PTY and container Exec consoles over the existing Core-Agent WebSocket and browser WebSocket service. Browser requests do not carry shell command strings. A protected POST selects only `host` or an exact full container ID; the browser receives a one-use, Session-bound ticket for `/ws/v1/streams/terminal`.

The Agent opens its configured absolute shell for host sessions and `/bin/sh -i` for Docker Exec sessions. Input, output, terminal dimensions, close and exit status use the bounded terminal protocol frames. The Agent has a two-session limit per node and 30-minute idle timeout. The Core independently enforces two reserved/active streams per node, validates the current administrator Session and Origin, uses a one-minute ticket, binds node and target, bounds frames and output queues, and closes the stream on Session revocation, Agent generation change, logout or Core shutdown. The Agent terminal worker is separate from the heartbeat reader/writer path.

Audit rows contain lifecycle action/outcome, administrator actor, remote address and the selected host node or full container ID. Raw input, command text, ticket, Session token and terminal output are not audit fields.

## User interface

The node detail page has a host terminal entry. Running container rows have a console entry. The console uses the repository-locked xterm.js and fit addon, supports resize and browser keyboard input, and provides mobile-sized Tab, Ctrl+C, Ctrl+D, Esc and arrow controls. Its mobile overlay is tested at a 375 CSS-pixel viewport; no physical mobile device is required.

## Validation entry points

```bash
make test-terminal-component
NODEDANCE_S09_DIND_SOCKET="$PWD/.artifacts/dind/v29/socket/docker.sock" make test-terminal-component
make test-terminal-browser PLAYWRIGHT_BROWSERS='chromium webkit'
```

The DIND test accepts only the repository-owned, marker-verified Engine 28/29 socket. The `S09 terminal component acceptance` GitHub Actions workflow starts the locked nested daemon for each isolated job, runs host PTY, Core route/queue and real Docker Exec checks, runs the mobile-viewport browser tests in Chromium and WebKit, then stops only its owned test daemon. Its logs are uploaded as workflow artifacts.

## Current evidence and limits

Component tests cover PTY Unicode input, resize, Ctrl+C and process reaping; Agent session cap, idle cleanup and output backpressure; Core ticket binding, one-use authorization, Origin/CSRF/Session rejection, per-node limits, queue overflow and metadata-only audit; and browser keyboard encoding, mobile helper keys, viewport width and exact container target selection.

`TestDINDContainerTerminalExec` is the real Engine integration check for interactive container Exec, Unicode output, terminal resize, and cleanup of a foreground `sleep 300`: closing sends Ctrl-C and a fixed `exit`, then both `Close` and an independent Docker client verify that ExecInspect is no longer running. Cleanup never signals a PID returned by Docker. `TestDINDTerminalNoShellAndStoppedContainerFailures` checks a stopped container and constructs a shell-less rootfs from the digest-locked BusyBox binary without adding a floating image dependency. The component suite passed against the repository-owned, marker-verified Engine 28 and Engine 29 DIND sockets. The local host already had the owner-controlled DIND daemons running from another worktree, so this worktree reused those exact isolated sockets; the GitHub Actions workflow starts one owned DIND engine per job.

The real Core → Agent → browser WebSocket chain has not yet been exercised end to end with revocation, Agent disconnect, Core restart, idle expiry and forced browser disconnect in one integration harness. The browser test stubs the network and validates UI behavior only; it is not evidence of the live terminal transport. S09 remains **NOT_READY** until those full-chain cases and three consecutive full-stage runs pass. A component or mock pass must not be reported as complete S09 acceptance.
