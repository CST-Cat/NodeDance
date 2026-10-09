# S10 first implementation candidate

This is an implementation candidate, not S10 acceptance. `docs/stages/S10.md`
remains `NOT_READY` until the listed real Core-Agent and target-node cases are
run and recorded.

## File root and operating-system permissions

The Agent opens the configured file root with `os.OpenRoot`, which confines
relative operations and rejects `..`/escaping symlinks. The default root is
`/`; configure a narrower root with `NODEDANCE_AGENT_FILE_ROOT`. This is a
path boundary, not an operating-system permission grant: reads, writes, owner
preservation, atomic rename, and directory creation still run with the Agent
service account's real UID/GID and are subject to mount permissions and
systemd sandbox policy.

`NODEDANCE_AGENT_MAX_FILE_BYTES` controls the per-file runtime limit, defaults
to 1 GiB, and may not exceed the protocol hard limit of 16 GiB. The Core limit
is independently configurable and should be set to the same or a lower value.
The text editor is intentionally limited to 32 KiB so Base64 text fits the
bounded 64 KiB control frame. Directory listings are capped at 100 entries and
the encoded response is checked against the control-frame bound before send.

The generated Agent systemd unit runs as the explicitly selected service user
and does not currently add a `ProtectSystem`/`ProtectHome` sandbox or a
`ReadWritePaths` allowlist. The configured file root is therefore constrained
by `os.Root` and the service account's actual filesystem permissions. Host
read/write reach, read-only filesystem behavior, and owner preservation still
require tests on a real installed target.

## API and transfer result identity

The Core file endpoints are under `/api/v1/nodes/{nodeId}/files`. Upload and
download use bounded 32 KiB chunks over the Agent's existing WebSocket
connection, with a separate bounded Core queue. The browser and Agent share
the same Core listen port. Download is spooled to a mode-0600 Core temporary
file and SHA-256-verified before any successful HTTP response is sent; this
uses bounded memory but needs temporary Core disk space up to the file size.

File write requests use their task ID as the transfer ID for compatibility
with the existing file API, and the returned task can be queried through the
existing node task endpoints. Core persists the write intent before dispatch;
Agent stores a body-free journal with a bounded terminal retention window and
does not replay non-idempotent writes. On reconnect, Core asks the Agent for
the recorded outcome; unverifiable operations remain `unknown`. Upload temp
paths are journaled before creation and cleanup removes only the exact
Agent-generated sibling path. Audit uses operation-specific actions and an
escaped target identifier containing the node UUID, task UUID, and exact path
(or source/destination pair); file contents are never recorded. If the final
audit write fails after the Agent verified success, the API reports that the
operation succeeded and that audit persistence failed; it does not silently
claim a durable audit result.

The file manager is reachable from the selected node's dashboard detail using
the “管理节点文件” button. It mounts `NodeFiles` with that selected node ID;
the return button restores the metrics and Docker detail view. The file page is
disabled until the node has a registered, online Agent.

## Candidate checks

Component tests cover root confinement, special names, upload abort/no-overwrite,
digest checks, text conflicts, mode preservation, editor limits, upload-path
ownership recovery, journal retention/capacity, and Agent journal reconciliation.
Protocol tests cover frame type, generation, transfer ID, sequence and size
bounds. Core tests cover task lookup, audit, logout cancellation, cancellation
of a stalled HTTP upload body after Session revocation, and an explicitly
`unknown` response after a dispatched write loses its browser connection. Web
Playwright tests use a mocked API and verify component behavior only. Absent
upload targets are installed using an atomic root-relative hard link that
fails on an `EEXIST` race, instead of a replace-capable rename.

The dedicated `.github/workflows/s10-files.yml` workflow runs locked component
checks and the Chromium/WebKit/Firefox mock browser suite. The local focused
Chromium suite passed. Local Firefox/WebKit startup did not complete in this
environment; their results remain pending the clean Actions runner.

The following remain `NOT_READY` unless a report records a real execution:

- Real browser-to-Core-to-Agent-to-target filesystem integration.
- 100 MiB and 1 GiB transfer SHA-256 and RSS-delta measurement (≤64 MiB).
- Real installed-Agent write permissions for the configured file root.
- Read-only filesystem and non-root owner-preservation cases on a real target.
