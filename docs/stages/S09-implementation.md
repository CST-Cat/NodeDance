# S09 terminal implementation and evidence

## Scope

S09 adds short-lived host PTY and container Exec consoles over the existing Core-Agent WebSocket and browser WebSocket service. Browser requests do not carry shell command strings. A protected POST selects only `host` or an exact full container ID; the browser receives a one-use, Session-bound ticket for `/ws/v1/streams/terminal`.

The Agent opens its configured absolute shell for host sessions and `/bin/sh -i` for Docker Exec sessions. Input, output, terminal dimensions, close and exit status use the bounded terminal protocol frames. The Agent has a two-session limit per node and 30-minute idle timeout. The Core independently enforces two reserved/active streams per node, validates the current administrator Session and Origin, uses a one-minute ticket, binds node and target, bounds frames and output queues, and closes the stream on Session revocation, Agent generation change, logout or Core shutdown. The Agent terminal worker is separate from the heartbeat reader/writer path.

Audit rows contain lifecycle action/outcome, administrator actor, remote address and the selected host node or full container ID. Raw input, command text, ticket, Session token and terminal output are not audit fields.

## User interface

The node detail page has a host terminal entry. Running container rows have a console entry. The console uses the repository-locked xterm.js and fit addon, supports resize and browser keyboard input, and provides mobile-sized Tab, Ctrl+C, Ctrl+D, Esc and arrow controls. Its mobile overlay is tested at a 375 CSS-pixel viewport; no physical mobile device is required.

## Validation entry points

```bash
pnpm --dir web run build
make test-terminal-component
go test ./internal/core/server -run '^TestRealAgentBrowserTerminalLifecycle$' -count=1
NODEDANCE_S09_DIND_SOCKET="$PWD/.artifacts/dind/v29/socket/docker.sock" make test-terminal-component
make test-terminal-browser PLAYWRIGHT_BROWSERS='chromium webkit'
```

Build the embedded frontend before running Core Go tests in a clean checkout. The DIND test accepts only the repository-owned, marker-verified Engine 28/29 socket. The `S09 terminal component acceptance` GitHub Actions workflow starts the locked nested daemon for each isolated job, builds the embedded frontend, runs host PTY, Core route/queue, the live Core-Agent-browser host-terminal test, and real Docker Exec checks, runs the mobile-viewport browser tests in Chromium and WebKit, then stops only its owned test daemon. Its logs are uploaded as workflow artifacts. The stopped-container test waits for `ContainerInspect.State.Running == false` after `ContainerWait` completes before attempting terminal creation.

## Current evidence and limits

Component tests cover PTY Unicode input, resize, Ctrl+C and process reaping; Agent session cap, idle cleanup and output backpressure; Core ticket binding, one-use authorization, Origin/CSRF/Session rejection, per-node limits, queue overflow and metadata-only audit; and browser keyboard encoding, mobile helper keys, viewport width and exact container target selection. `TestRealAgentBrowserTerminalLifecycle` additionally uses the repository's local test CA and enrollment helper to run a real Agent against Core, opens the authenticated browser terminal WebSocket, checks shell input/output and `stty size` after a 42×111 resize, and verifies that ordinary stream close and API-driven Session revocation close the browser stream and reap each PTY shell. Its focused run passed with the locked Go toolchain; the S09 workflow's component selector includes this test.

`TestDINDContainerTerminalExec` is the real Engine integration check for interactive container Exec, Unicode output, terminal resize, and cleanup of a foreground `sleep 300`: closing sends Ctrl-C and a fixed `exit`, then both `Close` and an independent Docker client verify that ExecInspect is no longer running. Cleanup never signals a PID returned by Docker. `TestDINDTerminalNoShellAndStoppedContainerFailures` checks a stopped container and constructs a shell-less rootfs from the digest-locked BusyBox binary without adding a floating image dependency. The component suite passed against the repository-owned, marker-verified Engine 28 and Engine 29 DIND sockets. The local host already had the owner-controlled DIND daemons running from another worktree, so this worktree reused those exact isolated sockets; the GitHub Actions workflow starts one owned DIND engine per job.

The live Core → Agent → browser WebSocket test now covers a normal close and Session revocation. Core restart, Agent disconnect, idle expiry and forced browser disconnect are not yet covered through this live chain. The Playwright viewport test still stubs terminal transport and is evidence for UI behavior only. S09 remains **NOT_READY** until the remaining full-chain cases and outstanding stage requirements are implemented and tested; this focused live test does not mark the stage complete.
