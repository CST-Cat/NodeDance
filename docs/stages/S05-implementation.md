# S05 durable task component implementation

**Formal phase status: `NOT_READY`.** This document describes the shared state
contract and Agent SQLite journal only. Core authorization/audit and the real
Docker execution plus postcondition chain have not been integrated. None of the
original `S05-01` through `S05-12` acceptance cases is reported as passing by
the component test runner.

## Ownership and interfaces

The work is limited to `internal/taskstate`, `internal/agent/taskjournal`, the
local component tests, this document, and `scripts/test/s05-task-journal.py`.
The shared package has no transport, database, or executor dependency. The
journal does not execute commands. Core still needs its own durable task and
audit transaction before delivery, and the Agent runtime must call
`BeginExecution` before crossing into the Docker executor.

`taskstate.Identity` is the request boundary. It contains the task ID, node ID,
idempotency key, target/resource identity, action, and transient JSON payload.
`RequestDigest` validates and hashes the canonical request; the journal stores
only the digest and typed summary fields. It never stores the raw request body,
command, stdin, or freeform error. S05 lifecycle payloads must contain only
non-secret control fields. Any future secret-bearing task must define a
separate keyed-digest contract before it is integrated.

The digest format is versioned and deterministic. Object key order and
whitespace do not affect it. Duplicate keys, invalid UTF-8, trailing JSON,
payloads larger than 64 KiB, nesting deeper than 128 levels, numeric tokens
longer than 128 bytes, more than 96 numeric digits, and exponents whose
magnitude exceeds 10,000 are rejected. Numeric JSON tokens retain their exact
valid spelling, so `1` and `1.0` are distinct request digests; numbers are never
converted through `float64`.

## State and durability rules

The seven statuses are `queued`, `running`, `succeeded`, `failed`,
`timed_out`, `canceled`, and `unknown`.

- `Enqueue` commits the task row, request digest, and resource claim in one
  SQLite transaction. The caller cannot enter the executor if this write
  fails.
- Repeating the same node/idempotency key/request returns the stored task ID.
  Reusing the key with another digest returns `ErrIdempotencyConflict`.
  Reusing a task ID with a different key or digest returns `ErrTaskIDConflict`.
  Enqueue checks both supplied identities together: an unused task ID may
  accompany a retry that returns the original task, but a task ID already
  bound to another row conflicts even when the idempotency key matches an
  existing request.
- `queued`, `running`, and `unknown` tasks hold their node/resource claim.
  Another write for that resource returns `ErrResourceBusy`. A verified final
  status releases the claim in the same transaction that records the result.
- `BeginExecution` durably changes `queued` to `running` before the external
  call. A crash while still `queued` proves the executor was not entered. A
  crash after `running` is conservative: `RecoverInterrupted` changes it to
  `unknown`; neither `running` nor `unknown` can enter the executor again.
- `succeeded` requires execution-attempted, execution-completed, and
  postcondition-verified evidence. `failed` requires a confirmed failure and
  confirmed actual result. `timed_out` requires process termination and a
  confirmed actual result. A running `canceled` task requires confirmed
  cancellation, process termination, and actual-result confirmation. Missing
  evidence leaves the task nonterminal or `unknown`.
- The proof fields are typed assertions supplied by the future integration.
  The journal cannot prove a Docker postcondition by itself; formal S05 requires
  fresh Engine inspection and tests against the isolated Docker Engine.

## Agent journal ownership and privacy

`taskjournal.Open(ctx, databasePath, nodeID)` binds the database to one node.
It stores a random durable `JournalID` in owner metadata. The same database
returns the same ID after restart; recreating an empty database generates a
different ID so Core can recognize that Agent execution history was lost.
Future task delivery must carry and compare this identity before Core treats an
absent task row as proof the task never ran.
The database lives under a dedicated current-user-owned directory with mode
`0700`; its main file and SQLite WAL/SHM sidecars must be current-user-owned
regular files with mode `0600`. The open path rejects symlinks, hard-linked
files, unsafe existing directory/file permissions, and a journal already bound
to another node. It does not repair or chmod an unsafe existing path.

The process holds a nonblocking Linux `flock` on the database file for the
entire `Store` lifetime. There is no lockfile used to infer ownership. A second
live process receives `ErrJournalInUse`; the kernel releases the lock on normal
close, process crash, or kill. `RecoverInterrupted` runs only through a Store
that obtained that lock and changes only its bound node's `running` records.
Unknown resources remain claimed until actual Engine state is queried and a
verified result is recorded.

Progress is limited to typed phase and numeric counters. Results are limited to
typed status, observed state, and a bounded resource revision. Log writes
require an explicit redaction marker, are capped at 16 MiB, and expose a
truncation flag. The marker is an integration contract; callers remain
responsible for redacting before calling `AppendLog`.

## Component test evidence

Run with the repository-pinned Go binary:

```bash
NODEDANCE_GO_BIN=/path/to/go1.26.8/bin/go python3 scripts/test/s05-task-journal.py
```

The runner executes standard and race-instrumented tests and writes full
`go-test-*.jsonl`, stderr, per-component-case event logs, and `report.json` to
`.artifacts/work-s05/<run-id>/`. Its additional component IDs are `S05J-01`
through `S05J-08`; the report separately keeps all twelve normative S05 case
IDs at `NOT_READY`.

The subprocess checks use a test-only checkpoint marker to kill and restart a
real Go test process around the journal boundary. They validate queued versus
unknown recovery, lock contention/release, and resource claims against real
temporary SQLite files. They do not simulate Docker success, do not call an
executor, and do not satisfy the real Engine requirements of the normative
S05 cases.

## Review corrections and evidence

The following review findings were corrected before component approval:

- JSON decimal and exponent values remain supported. Request digests preserve
  the valid number token spelling (`1` and `1.0` are distinct), while duplicate
  object keys, oversized tokens, and out-of-bound exponents are rejected.
- The Agent journal is created only under its private `0700` directory, with
  `0600` main and sidecar files. A nonblocking OS lock on the database file
  enforces one live Agent owner and is released by process death. A random,
  durable `JournalID` remains stable on restart and changes when the database
  is recreated; an unexpected node owner is rejected.
- When a log already contains exactly 16 MiB, a subsequent byte now persists
  `log_truncated=true` even though no additional byte fits. The test closes and
  reopens the actual SQLite database before checking the marker.
- Enqueue verifies TaskID and idempotency-key bindings together. A retry with
  the same key/request and an unused TaskID returns the original task; a
  TaskID already bound to another task conflicts. A two-task cross-collision
  regression test exercises this rule.

Historical note: The component runner passed three standard and race-instrumented
runs under the former repeated-run policy; this is preserved historical evidence, not a current stage gate. `go vet -mod=readonly` passed for both owned Go packages. The latest
local evidence is `.artifacts/work-s05/20261008T172416Z-2012296/report.json`;
runner logs and reports stay in ignored local work files and are not committed.
These checks approve only the isolated component. The formal S05 phase and all
twelve normative `S05-01` through `S05-12` cases remain `NOT_READY` pending the
Core/Agent integration and real Docker execution/postcondition tests.

## Outstanding phase work

S05 still needs the Core task/audit persistence path, Agent task delivery and
journal integration, Docker lifecycle execution with fresh postcondition
inspection, timeout/cancel/process termination confirmation, and the real
Engine suites for all twelve normative cases. The current gate is one complete
full acceptance run plus regression checks for any affected suites.
