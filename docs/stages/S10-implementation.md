# S10 first implementation candidate

This is an implementation candidate, not S10 acceptance. `docs/stages/S10.md`
remains `NOT_READY` until the listed real Core-Agent and target-node cases are
run and recorded.

## File root and operating-system permissions

The Agent opens an explicitly configured file root with `os.OpenRoot`, which
confines relative operations and rejects `..`/escaping symlinks. There is no
default host file root: when unset, the Agent does not create the file service
and does not advertise `agent.files.v1`; the Core disables the file-manager tab
and explains that a root must be configured. For a foreground Agent, set
`NODEDANCE_AGENT_FILE_ROOT` to an absolute existing directory. For a systemd
service, use `nodedance-agent install-systemd --file-root /absolute/directory`.
The root may not be `/` or `/home` itself, nor `/root` and its descendants,
`/proc`, `/sys`, `/dev`, `/run`, `/etc/systemd`, or any path overlapping the
Agent's private state directory.
The root and every component in its path must be real directories, not
symlinks. A narrow `/home/<user>/...` directory is allowed; the rest of `/home`
remains hidden from the service.

The generated service uses `ProtectSystem=strict` and `ProtectHome=tmpfs`.
`ReadWritePaths` grants writes to only the private Agent state directory;
`BindPaths` exposes the exact selected file root when configured. Every other
home directory remains inaccessible, and writes outside those explicit paths
are denied by the service mount namespace. Linux owner, group, ACL, and mount permissions still
apply: an allowlisted path is not made writable by changing its systemd mount
policy alone. The service account must already be able to read and write the
selected directory as needed. `--no-file-root` explicitly removes an existing
file root; a repeated install without a new value retains the root recorded in
the generated unit. A new `--file-root` always replaces that saved value. The
SSH form also provides an explicit checkbox to clear the saved root; leaving
the directory field blank alone does not remove persisted access.

The SSH deployment form accepts the same optional absolute file-root value and
an explicit option to disable previously configured access. It sends the path
as base64 data through the remote script and passes it to the Agent installer
as a quoted argument; the target validates canonical path,
symlink components, existing directory type, protected roots, and overlap with
Agent state before writing the systemd unit. Leaving the field empty disables
file access on a new installation and preserves only a previously persisted
NodeDance unit setting on reinstall, unless the administrator explicitly
selects the disable option.

`NODEDANCE_AGENT_MAX_FILE_BYTES` controls the per-file runtime limit, defaults
to 1 GiB, and may not exceed the protocol hard limit of 16 GiB. The Core limit
is independently configurable and should be set to the same or a lower value.
The text editor is intentionally limited to 32 KiB so Base64 text fits the
bounded 64 KiB control frame. Directory listings are capped at 100 entries and
the encoded response is checked against the control-frame bound before send.

The actual systemd mount combination is covered by a gated Linux integration
test: a selected directory below `/home` must be writable, another home path
must remain hidden, and a host path outside the selected root that would
otherwise be writable by the service UID must reject writes. This test checks
the process namespace rather than relying only on the Agent's Go path root.

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
Agent stores a body-free journal with a 90-day terminal retention window and a
10,000-record hard cap, and does not replay non-idempotent writes. Active and
unknown records are retained; expired terminal records are pruned, and a full
journal rejects new writes rather than evicting in-window results. On reconnect,
Core asks the Agent for the recorded outcome; unverifiable operations remain
`unknown`. Upload temp paths are journaled before file creation and cleanup
removes only the exact Agent-generated sibling path. Audit uses
operation-specific actions and an escaped target identifier containing the
node UUID, task UUID, and exact path (or source/destination pair); file contents
are never recorded. If the final audit write fails after the Agent verified
success, the API reports that the operation succeeded and that audit
persistence failed; it does not silently claim a durable audit result.

The file manager is reachable from the selected node's dashboard detail using
the “管理节点文件” button. It mounts `NodeFiles` with that selected node ID;
the return button restores the metrics and Docker detail view. The file page is
disabled until the node has a registered, online Agent.

## Candidate checks

Component tests cover root confinement, special names, upload abort/no-overwrite,
digest checks, text conflicts, mode preservation, editor limits, upload-path
ownership recovery, journal retention/capacity, and Agent journal reconciliation.
Protocol tests cover frame type, generation, transfer ID, sequence and size
bounds. Core tests cover task lookup and audit, logout cancellation,
cancellation of a stalled HTTP upload body after Session revocation, an
explicitly `unknown` response after a dispatched write loses its browser
connection, and reconciliation of Agent journal results after reconnect. Web
Playwright tests use a mocked API and verify component behavior only. Absent
upload targets are installed using an atomic root-relative hard link that
fails on an `EEXIST` race, instead of a replace-capable rename.

The dedicated `.github/workflows/s10-files.yml` workflow runs locked component
checks and the Chromium/WebKit/Firefox mock browser suite. The local focused
Chromium suite passed. Local Firefox/WebKit startup did not complete in this
environment; their results remain pending the clean Actions runner.

## Isolated live Core-Agent-host API slice

`TestRealAgentHostFilesAPIEndToEnd` runs the actual TLS Core, public authenticated
Agent enrollment API, Agent HTTPS enrollment, Agent WSS connection, and host
filesystem service. It uses `testing.T.TempDir()` and sets
`NODEDANCE_AGENT_FILE_ROOT` to only the child directory `host-file-root`;
Core data, Agent state, and a root-external canary are siblings outside that
file root. The test runs no real VPS, Docker daemon, systemd service, or user
business directory.

The focused command is:

```bash
go test -v -count=1 -run '^TestRealAgentHostFilesAPIEndToEnd$' ./internal/core/server
```

The run covers the protected Core API with an actual registered Agent: upload,
list, stat, rename, and download of a Chinese/spaced filename with SHA-256
comparison; text read followed by an external host write and a stale edit that
must return HTTP 409 without replacing the external bytes; `..` and an
escaping symlink that must not read the root-external canary; and exact-path
file deletion with CSRF rejection and a persisted audit target containing the
node, task, and escaped exact path. A missing CSRF header and a mismatched
confirmation leave the file intact. The follow-up also creates a non-empty
directory with a nested file, verifies both missing-CSRF and mismatched-path
requests preserve its tree and bytes, then confirms deletion through the live
Core-Agent API. It checks that the directory and nested file disappear and
that SQLite contains exactly one successful audit row whose independently
constructed target matches the node UUID, returned task ID, and exact escaped
directory path. Empty-directory deletion remains untested. The test verifies
Core download spools and Agent upload temporaries are gone, stops its Agent/Core,
removes only the test-owned TempDir, and verifies that directory is absent.
Go's own TempDir cleanup remains registered as a fallback on early test failure.

The first execution failed because the test incorrectly required an escaping
symlink to return HTTP 400. The real Agent refused the symlink, but Go's
`os.Root` confinement error currently maps to the generic file `unavailable`
code (HTTP 503). The assertion was narrowed to require a rejected response and
an unchanged outside-root canary; it did not alter production error mapping.
The focused test then passed once. The first failure and corrected pass are
both retained in the current stage report and local test logs.

The non-empty-directory follow-up added a nested file and exercised the live
delete API with an omitted CSRF header, a mismatched confirmation path, and
the exact confirmation. The first attempt compared the audit target against
the bare path, while the established contract stores a URL-escaped JSON path
list; the next attempt selected the earlier `accepted` audit row instead of
the later `succeeded` row. After correcting only those two test assertions, one
focused run passed and verified the directory and nested file were removed,
both rejected requests preserved the nested bytes, and SQLite stored exactly
one successful exact node/task/path audit target. Both assertion failures and
the passing run are retained in `.artifacts/stage-runs/`. This is partial
S10-09 evidence only: empty-directory deletion and the remaining S10-09 matrix
are still unverified.

This bounded local integration supplies live evidence for S10-01, S10-02,
and S10-07, plus partial S10-09 evidence for file and nested non-empty
directory deletion. S10-09 remains `NOT_READY` because empty-directory delete
and the full required matrix are not covered. The full stage stays `NOT_READY`:
the test uses a small fixture, runs the Agent as the current test account, and
does not cover large-transfer memory use or the systemd sandbox.

The following remain `NOT_READY` until a report records the required evidence:

- 100 MiB and 1 GiB transfer SHA-256 and RSS-delta measurement (≤64 MiB).
- Real target read/write permissions for the administrator-selected file root
  and its Linux UID/GID/ACL setup.
- Read-only filesystem and non-root owner-preservation cases on a real target.
- Full S10 browser-to-Core-to-Agent workflow and remaining negative cases.
