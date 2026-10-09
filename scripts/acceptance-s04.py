#!/usr/bin/env python3
"""Run the complete S04 real Engine/Agent/Core acceptance with per-case evidence."""

from __future__ import annotations

import datetime as dt
import json
import os
import pathlib
import re
import shutil
import signal
import subprocess
import sys
import time
import uuid

ROOT = pathlib.Path(__file__).resolve().parents[1]
REGISTRY = json.loads((ROOT / "tests/registry.json").read_text())
STAGE = next(stage for stage in REGISTRY["stages"] if stage["id"] == "S04")
CASES = STAGE.get("tests", []) + STAGE.get("supplemental_tests", [])
CASE_BY_ID = {case["id"]: case for case in CASES}
CASE_TESTS = {
    "S04-01": ("TestDINDInventoryMatchesOwnedFixtures", "TestRealDockerAgentCoreMultiChunkSnapshotReconnect"),
    "S04-02": ("TestDINDEventLifetimeAndReconnectSnapshot", "TestRealDockerAgentCoreMultiChunkSnapshotReconnect"),
    "S04-03": ("TestDINDInventoryMatchesOwnedFixtures", "TestRealDockerAgentCoreMultiChunkSnapshotReconnect", "TestRealDockerAgentCoreBrowserP95"),
    "S04-04": ("TestDINDInventoryMatchesOwnedFixtures", "TestRealDockerAgentCoreMultiChunkSnapshotReconnect", "TestRealDockerAgentCoreBrowserP95"),
    "S04-05": ("TestDINDInventoryMatchesOwnedFixtures", "TestRealDockerAgentCoreMultiChunkSnapshotReconnect", "TestRealDockerAgentCoreBrowserP95"),
    "S04-06": ("TestDINDEventLifetimeAndReconnectSnapshot", "TestRealDockerAgentCoreMultiChunkSnapshotReconnect"),
    "S04-07": ("TestDockerSQLiteFailuresKeepLastCommittedFullAndIncrementalInventory", "TestRealAgentDockerAPIIncompatibilitySecretRedaction", "TestRealDockerAgentCoreMultiChunkSnapshotReconnect"),
    "S04-08": ("TestRealAgentDockerUnavailableKeepsHeartbeatAndHostMetrics", "TestRealAgentDockerAPIIncompatibilitySecretRedaction", "TestRealAgentDockerDINDStopAndSocketPermissionRecovery", "TestRealDockerAgentCoreBrowserP95"),
    "S04-09": ("TestLatePriorGenerationDockerFramesCannotStaleFreshServerInventory", "TestRealDockerAgentCoreMultiChunkSnapshotReconnect"),
    "S04-10": ("TestDINDInventoryMatchesOwnedFixtures", "TestRealDockerAgentCoreMultiChunkSnapshotReconnect", "TestRealDockerAgentCoreBrowserP95"),
    "S04-SUP-01": ("TestRealDockerAgentCoreBrowserP95", "TestRealDockerAgentCoreExternalStateChangeP95"),
}
AGENT_TESTS = ("TestDINDInventoryMatchesOwnedFixtures", "TestDINDEventLifetimeAndReconnectSnapshot")
SERVER_TESTS = tuple(sorted({name for names in CASE_TESTS.values() for name in names if name not in AGENT_TESTS}))
LOCKED_GO = ROOT / ".tools/go1.26.8/bin/go"
GO_COMMANDS = (
    # Inventory must run before the event-lifetime test, which deliberately
    # stops/restarts the owned Engine and therefore changes fixture states.
    ("agent-docker-inventory", "./internal/agent/docker", ("TestDINDInventoryMatchesOwnedFixtures",), "6m"),
    ("agent-docker-events", "./internal/agent/docker", ("TestDINDEventLifetimeAndReconnectSnapshot",), "6m"),
    ("internal-core-server", "./internal/core/server", SERVER_TESTS, "25m"),
)
REPORT = ROOT / "reports/stages/S04.json"
STATUS_FILE = ROOT / "reports/status.json"
RUN_ID = dt.datetime.now(dt.timezone.utc).strftime("%Y%m%dT%H%M%SZ") + "-" + uuid.uuid4().hex[:8]
ATTEMPT_ROOT = ROOT / ".artifacts/logs/acceptance-s04" / RUN_ID
SECRET_KEY = re.compile(r"token|secret|password|credential|private.?key|session|cookie|authorization|csrf", re.I)
INLINE_SECRET = re.compile(r"(?i)\b(token|secret|password|credential|authorization|cookie|csrf)(\s*[=:]\s*)([^\s,;\"']{6,})")


def timestamp() -> str:
    return dt.datetime.now(dt.timezone.utc).isoformat()


def sanitize(value):
    if isinstance(value, dict):
        return {
            key: "[REDACTED]" if SECRET_KEY.search(str(key)) else sanitize(item)
            for key, item in value.items()
        }
    if isinstance(value, list):
        return [sanitize(item) for item in value]
    if isinstance(value, str):
        value = INLINE_SECRET.sub(r"\1\2[REDACTED]", value)
        return re.sub(r"-----BEGIN [^-]*PRIVATE KEY-----.*?-----END [^-]*PRIVATE KEY-----", "[REDACTED PRIVATE KEY]", value, flags=re.S)
    return value


def clean_env() -> dict[str, str]:
    env = os.environ.copy()
    env["GOTOOLCHAIN"] = "local"
    try:
        node_version = json.loads((ROOT / "toolchain.lock.json").read_text())["node"]["version"]
    except (OSError, json.JSONDecodeError, KeyError, TypeError):
        node_version = ""
    node_bin = ROOT / ".tools" / f"node-v{node_version}" / "bin" if node_version else None
    if node_bin is not None and node_bin.is_dir():
        env["PATH"] = str(node_bin) + os.pathsep + env.get("PATH", "")
    return env


def dind_root() -> pathlib.Path:
    configured = os.environ.get("NODEDANCE_S04_DIND_ROOT", "")
    if not configured:
        return ROOT
    root = pathlib.Path(configured).resolve()
    expected = (ROOT.parent / "NodeDance-s04").resolve()
    if root != expected:
        raise RuntimeError(f"refusing S04 DIND root outside designated sibling fixture worktree: {root}")
    return root


def write_line(handle, text: str) -> None:
    handle.write(text.rstrip("\n") + "\n")
    handle.flush()


def bounded_run(command: list[str], *, cwd: pathlib.Path, env: dict[str, str], timeout: int) -> tuple[subprocess.CompletedProcess, bool, bool]:
    """Run a process group with an outer deadline and bounded termination grace."""
    process = subprocess.Popen(command, cwd=cwd, env=env, stdout=subprocess.PIPE,
                               stderr=subprocess.STDOUT, text=True, start_new_session=True)
    try:
        output, _ = process.communicate(timeout=timeout)
        return subprocess.CompletedProcess(command, process.returncode, output), False, True
    except subprocess.TimeoutExpired:
        try:
            os.killpg(process.pid, signal.SIGTERM)
        except ProcessLookupError:
            pass
        try:
            output, _ = process.communicate(timeout=5)
        except subprocess.TimeoutExpired:
            try:
                os.killpg(process.pid, signal.SIGKILL)
            except ProcessLookupError:
                pass
            try:
                output, _ = process.communicate(timeout=5)
                reaped = True
            except subprocess.TimeoutExpired as error:
                output = error.output or ""
                reaped = False
                if process.stdout is not None:
                    process.stdout.close()
        else:
            reaped = True
        return subprocess.CompletedProcess(command, 124, output), True, reaped


def parse_timeout_seconds(value: str) -> int:
    match = re.fullmatch(r"([1-9][0-9]*)(s|m|h)", value)
    if not match:
        raise ValueError(f"invalid configured Go test timeout: {value}")
    amount = int(match.group(1))
    factor = {"s": 1, "m": 60, "h": 3600}[match.group(2)]
    return amount * factor


def output_text(value) -> str:
    if isinstance(value, bytes):
        return value.decode("utf-8", errors="replace")
    return value or ""


def preflight_cleanup_result(cleanup_code: int) -> tuple[str, tuple[bool, bool]]:
    """A missing tool is NOT_READY only when exact fixture cleanup succeeded."""
    if cleanup_code == 0:
        return "NOT_READY", (False, True)
    return "FAIL", (False, False)


def final_case_gate(status: str, cleanup_code: int, engine_after_ok: bool) -> tuple[str, list[str]]:
    reasons = []
    if cleanup_code != 0:
        status = "FAIL"
        reasons.append("exact suite fixture cleanup failed or could not be verified")
    if not engine_after_ok:
        status = "FAIL"
        reasons.append("owned Engine version/availability postcondition failed")
    return status, reasons


def cleanup_permitted(process_groups_reaped: bool) -> bool:
    """Never delete fixture resources while a timed-out child may still use them."""
    return process_groups_reaped


def run_json(command: list[str], log_path: pathlib.Path, env: dict[str, str], outer_timeout: int) -> tuple[int, list[dict], bool]:
    """Capture bounded JSON Go test output, redacting secrets before evidence writes."""
    log_path.parent.mkdir(parents=True, exist_ok=True)
    events: list[dict] = []
    result, timed_out, reaped = bounded_run(command, cwd=ROOT, env=env, timeout=outer_timeout)
    with log_path.open("w", encoding="utf-8") as output:
        write_line(output, json.dumps({"record": "command", "argv": command}, ensure_ascii=False))
        for line in output_text(result.stdout).splitlines():
            try:
                event = json.loads(line)
            except json.JSONDecodeError:
                write_line(output, json.dumps({"record": "output", "text": sanitize(line)}, ensure_ascii=False))
                continue
            event = sanitize(event)
            events.append(event)
            write_line(output, json.dumps(event, ensure_ascii=False))
        code = result.returncode if result.returncode is not None else 124
        write_line(output, json.dumps({"record": "process_exit", "exit_code": code,
                                       "outer_timeout": timed_out, "process_group_reaped": reaped}))
    return code, events, reaped


def test_status(name: str, events: list[dict], code: int) -> tuple[str, str]:
    matching = [event for event in events if event.get("Test") == name or str(event.get("Test", "")).startswith(name + "/")]
    if not matching:
        return "FAIL", f"required named test {name} emitted no Go test event"
    if any(event.get("Action") == "fail" for event in matching):
        return "FAIL", f"required named test {name} or one of its subtests failed"
    if any(event.get("Action") == "skip" for event in matching):
        return "NOT_READY", f"required named test {name} or one of its subtests was skipped"
    if code != 0:
        return "FAIL", f"Go test process exited {code} after running required test {name}"
    if not any(event.get("Action") == "pass" and event.get("Test") == name for event in matching):
        return "FAIL", f"required named test {name} did not emit its final PASS event"
    return "PASS", ""


def related_events(name: str, package_logs: dict[str, tuple[pathlib.Path, list[dict]]]) -> list[dict]:
    selected = []
    for package, (path, events) in package_logs.items():
        for event in events:
            event_name = str(event.get("Test", ""))
            if event_name == name or event_name.startswith(name + "/"):
                selected.append({"record": "go_test_event", "package": package, **event})
    return selected


def save_report(report: dict) -> None:
    REPORT.parent.mkdir(parents=True, exist_ok=True)
    REPORT.write_text(json.dumps(report, ensure_ascii=False, indent=2) + "\n")
    overall = json.loads(STATUS_FILE.read_text()) if STATUS_FILE.exists() else {"schema": 1, "stages": {}}
    overall.setdefault("stages", {})["S04"] = {
        "status": report["status"], "mode": report["mode"], "run_id": report["run_id"],
        "updated_at": report["updated_at"], "reason": report.get("reason", ""),
    }
    overall["updated_at"] = report["updated_at"]
    STATUS_FILE.parent.mkdir(parents=True, exist_ok=True)
    STATUS_FILE.write_text(json.dumps(overall, ensure_ascii=False, indent=2) + "\n")


def report_case(case_id: str, attempt: int, status: str, evidence: pathlib.Path, reason: str = "") -> None:
    report = CURRENT_REPORT["tests"][case_id]
    report["runs"].append({
        "attempt": attempt,
        "status": status,
        "evidence": str(evidence.relative_to(ROOT)),
        "reason": reason,
    })
    failed_runs = [run for run in report["runs"] if run["status"] == "FAIL"]
    not_ready_runs = [run for run in report["runs"] if run["status"] == "NOT_READY"]
    if failed_runs:
        report["status"] = "FAIL"
        report["reason"] = failed_runs[0]["reason"] or "At least one acceptance attempt failed."
    elif not_ready_runs:
        report["status"] = "NOT_READY"
        report["reason"] = not_ready_runs[0]["reason"] or "At least one acceptance attempt was not ready."
    else:
        report["status"] = "PASS"
        report["reason"] = ""


def make_initial_report(mode: str, repeat: int, engine: str) -> dict:
    cases = {}
    for case in CASES:
        cases[case["id"]] = {
            "status": "NOT_READY", "action": case["action"], "expected": case["expected"],
            "environment": case["environment"], "evidence_required": case["evidence"],
            "runs": [], "reason": "No S04 acceptance attempt has completed yet.",
        }
    commit_result, commit_timed_out, commit_reaped = bounded_run(
        ["git", "rev-parse", "HEAD"], cwd=ROOT, env=clean_env(), timeout=5)
    commit = output_text(commit_result.stdout).strip() if commit_result.returncode == 0 and not commit_timed_out and commit_reaped else "unavailable"
    return {
        "schema": 1, "stage": "S04", "mode": "full" if mode == "full" else mode,
        "status": "NOT_READY", "run_id": RUN_ID, "updated_at": timestamp(),
        "repeat_required": 3, "repeat_requested": repeat,
        "verification_status": "NOT_READY", "engine": engine,
        "commit": commit,
        "tests": cases,
    }


def setup_engine(engine: str, root: pathlib.Path, log_path: pathlib.Path) -> tuple[str | None, str]:
    socket = root / ".artifacts" / "dind" / f"v{engine}" / "socket" / "docker.sock"
    endpoint = "unix://" + str(socket)
    marker = socket.parent.parent / "owner.json"
    with log_path.open("w", encoding="utf-8") as log:
        write_line(log, f"started={timestamp()} engine={engine} dind_root={root}")
        command = [str(root / "scripts/test/dind.sh"), "start", engine]
        process, timed_out, reaped = bounded_run(command, cwd=root, env=clean_env(), timeout=180)
        write_line(log, sanitize(process.stdout or ""))
        write_line(log, json.dumps({"record": "dind_start_exit", "exit_code": process.returncode,
                                   "outer_timeout": timed_out, "process_group_reaped": reaped}))
        if process.returncode != 0 or timed_out or not reaped:
            return None, f"owned DIND Engine {engine} could not be started or verified within the bounded startup; see {log_path.relative_to(ROOT)}"
        if not marker.is_file() or not socket.is_socket():
            return None, "owned DIND marker or Unix socket is missing after start"
        try:
            owner = json.loads(marker.read_text())
        except (OSError, json.JSONDecodeError):
            return None, "owned DIND marker is unreadable"
        if owner.get("suite") != "nodedance-s00-dind" or pathlib.Path(owner.get("socket", "")).resolve() != socket.resolve():
            return None, "owned DIND marker does not match the exact S04 socket"
        docker = shutil.which("docker")
        if not docker:
            return None, "Docker CLI is unavailable in the locked test environment"
        check, check_timed_out, check_reaped = bounded_run(
            [docker, "--host", endpoint, "version", "--format", "{{.Server.Version}}"],
            cwd=ROOT, env=clean_env(), timeout=20)
        version = output_text(check.stdout).strip()
        write_line(log, json.dumps({"record": "engine_version", "version": version,
                                   "exit_code": check.returncode, "outer_timeout": check_timed_out,
                                   "process_group_reaped": check_reaped}))
        if check.returncode or check_timed_out or not check_reaped or not version.startswith(engine + ".") or version != owner.get("server_version"):
            return None, f"owned DIND Engine version did not match Engine {engine} marker"
        write_line(log, f"finished={timestamp()} result=PASS")
        return endpoint, ""


def run_fixtures(endpoint: str, droot: pathlib.Path, fixture_id: str, log_path: pathlib.Path) -> tuple[int, str, bool]:
    env = clean_env()
    env.update({"NODEDANCE_TEST_DOCKER_HOST": endpoint, "NODEDANCE_S04_DIND_ROOT": str(droot)})
    command = ["bash", str(ROOT / "scripts/test/fixtures.sh"), "create", fixture_id]
    with log_path.open("w", encoding="utf-8") as log:
        write_line(log, json.dumps({"record": "command", "argv": command}))
        process, timed_out, reaped = bounded_run(command, cwd=ROOT, env=env, timeout=180)
        output = sanitize(output_text(process.stdout))
        write_line(log, json.dumps({"record": "fixture_output", "text": output}, ensure_ascii=False))
        write_line(log, json.dumps({"record": "process_exit", "exit_code": process.returncode,
                                   "outer_timeout": timed_out, "process_group_reaped": reaped}))
    code = 0 if process.returncode == 0 and not timed_out and reaped else 124 if timed_out else process.returncode or 1
    return code, str(log_path.relative_to(ROOT)), reaped


def cleanup_fixtures(endpoint: str, droot: pathlib.Path, fixture_id: str, log_path: pathlib.Path) -> int:
    test_root = droot / ".artifacts" / "fixtures" / fixture_id
    owner_file = test_root / ".nodedance-fixture-owner"
    if not owner_file.is_file() or owner_file.read_text().strip() != fixture_id:
        env = clean_env()
        env.update({"NODEDANCE_TEST_DOCKER_HOST": endpoint, "NODEDANCE_S04_DIND_ROOT": str(droot)})
        label = f"io.nodedance.suite={fixture_id}"
        resources = {}
        clean = True
        with log_path.open("w", encoding="utf-8") as log:
            write_line(log, json.dumps({"record": "cleanup", "status": "NO_OWNER_MARKER",
                                       "reason": "do not delete resources without the exact private fixture owner marker"}))
            for kind, args in (
                ("containers", ["ps", "-aq", "--filter", f"label={label}"]),
                ("volumes", ["volume", "ls", "-q", "--filter", f"label={label}"]),
                ("networks", ["network", "ls", "-q", "--filter", f"label={label}"]),
            ):
                result, timed_out, reaped = bounded_run(
                    [shutil.which("docker") or "docker", "--host", endpoint, *args],
                    cwd=ROOT, env=env, timeout=20)
                listed = output_text(result.stdout).strip()
                resources[kind] = listed.splitlines() if listed else []
                clean = clean and result.returncode == 0 and not timed_out and reaped and not listed
                write_line(log, json.dumps({"record": "exact_labeled_resource_check", "kind": kind,
                                            "ids": resources[kind], "exit_code": result.returncode,
                                            "outer_timeout": timed_out, "process_group_reaped": reaped}))
            code = 0 if clean else 1
            write_line(log, json.dumps({"record": "process_exit", "exit_code": code,
                                        "reason": "no exact-suite resources existed" if clean else "resources remain or exact cleanup could not be verified"}))
            return code
    env = clean_env()
    env.update({"NODEDANCE_TEST_DOCKER_HOST": endpoint, "NODEDANCE_S04_DIND_ROOT": str(droot)})
    command = ["bash", str(ROOT / "scripts/test/fixtures.sh"), "clean", fixture_id]
    with log_path.open("w", encoding="utf-8") as log:
        write_line(log, json.dumps({"record": "command", "argv": command}))
        process, cleanup_timed_out, cleanup_reaped = bounded_run(command, cwd=ROOT, env=env, timeout=90)
        write_line(log, json.dumps({"record": "fixture_cleanup", "text": sanitize(output_text(process.stdout)),
                                   "exit_code": process.returncode, "outer_timeout": cleanup_timed_out,
                                   "process_group_reaped": cleanup_reaped}, ensure_ascii=False))
        remaining, inspect_timed_out, inspect_reaped = bounded_run(
            [shutil.which("docker") or "docker", "--host", endpoint, "ps", "-aq",
             "--filter", f"label=io.nodedance.suite={fixture_id}"],
            cwd=ROOT, env=env, timeout=20)
        remaining_output = output_text(remaining.stdout).strip()
        write_line(log, json.dumps({"record": "remaining_exact_suite_ids", "text": remaining_output,
                                   "exit_code": remaining.returncode, "outer_timeout": inspect_timed_out,
                                   "process_group_reaped": inspect_reaped}, ensure_ascii=False))
        code = 0 if process.returncode == 0 and not cleanup_timed_out and cleanup_reaped and remaining.returncode == 0 and not inspect_timed_out and inspect_reaped and not remaining_output else 1
        write_line(log, json.dumps({"record": "process_exit", "exit_code": code}))
        return code


def go_test_command(go: str, package: str, names: tuple[str, ...], timeout: str) -> list[str]:
    pattern = "^(" + "|".join(re.escape(name) for name in names) + ")$"
    return [go, "test", "-race", "-mod=readonly", "-json", "-timeout=" + timeout,
            "-count=1", package, "-run", pattern]


def write_case_log(case_id: str, attempt: int, engine: str, status: str, reason: str,
                   evidence_names: tuple[str, ...], package_logs: dict[str, tuple[pathlib.Path, list[dict]]],
                   attempt_dir: pathlib.Path) -> pathlib.Path:
    path = attempt_dir / f"case-{case_id}.jsonl"
    with path.open("w", encoding="utf-8") as output:
        write_line(output, json.dumps({
            "record": "case_result", "case_id": case_id, "attempt": attempt,
            "status": status, "engine": engine, "required_named_tests": evidence_names,
            "reason": reason,
        }, ensure_ascii=False))
        for name in evidence_names:
            selected = related_events(name, package_logs)
            write_line(output, json.dumps({"record": "named_test_evidence", "test": name,
                                           "events": len(selected)}, ensure_ascii=False))
            for event in selected:
                write_line(output, json.dumps(sanitize(event), ensure_ascii=False))
    return path


def report_all_cases(attempt: int, engine: str, status: str, reason: str,
                     package_logs: dict[str, tuple[pathlib.Path, list[dict]]], attempt_dir: pathlib.Path) -> None:
    for case_id in CASE_BY_ID:
        evidence = write_case_log(case_id, attempt, engine, status, reason,
                                  CASE_TESTS[case_id], package_logs, attempt_dir)
        report_case(case_id, attempt, status, evidence, reason)


def run_attempt(attempt: int, mode: str, engine: str, droot: pathlib.Path) -> tuple[bool, bool]:
    attempt_dir = ATTEMPT_ROOT / f"attempt-{attempt}"
    attempt_dir.mkdir(parents=True, exist_ok=True, mode=0o700)
    try:
        endpoint, engine_reason = setup_engine(engine, droot, attempt_dir / "owned-dind.jsonl")
    except Exception as error:
        endpoint, engine_reason = None, f"owned DIND setup raised {type(error).__name__}; no fixture attempt started"
    if endpoint is None:
        report_all_cases(attempt, engine, "NOT_READY", engine_reason, {}, attempt_dir)
        return False, True

    fixture_id = f"s04-{uuid.uuid4().hex[:20]}"
    fixture_process_reaped = True
    try:
        fixture_code, fixture_evidence, fixture_process_reaped = run_fixtures(endpoint, droot, fixture_id, attempt_dir / "fixtures-create.jsonl")
    except Exception as error:
        fixture_code = 1
        fixture_process_reaped = False
        fixture_evidence = str((attempt_dir / "fixtures-create.jsonl").relative_to(ROOT))
        write_line_to_file(attempt_dir / "fixtures-create.jsonl", {
            "record": "fixture_runner_exception", "error_type": type(error).__name__,
        })
    if fixture_code != 0:
        reason = f"required locked Docker/Compose fixtures did not start; see {fixture_evidence}"
        if not cleanup_permitted(fixture_process_reaped):
            cleanup_code = 1
            write_line_to_file(attempt_dir / "fixtures-partial-cleanup.jsonl", {
                "record": "cleanup_blocked", "reason": "fixture creator process group was not reaped; refusing concurrent resource deletion",
            })
        else:
            try:
                cleanup_code = cleanup_fixtures(endpoint, droot, fixture_id, attempt_dir / "fixtures-partial-cleanup.jsonl")
            except Exception as error:
                cleanup_code = 1
                write_line_to_file(attempt_dir / "fixtures-partial-cleanup.jsonl", {
                    "record": "cleanup_exception", "error_type": type(error).__name__,
                })
        status = "NOT_READY" if cleanup_code == 0 else "FAIL"
        if cleanup_code:
            reason += "; exact fixture cleanup could not be verified"
        report_all_cases(attempt, engine, status, reason, {}, attempt_dir)
        write_line_to_file(attempt_dir / "attempt-summary.jsonl", {
            "record": "attempt_summary", "attempt": attempt, "engine": engine,
            "fixture_create_exit": fixture_code, "fixture_cleanup_exit": cleanup_code,
            "status": status, "reason": reason,
        })
        return False, status == "NOT_READY"

    env = clean_env()
    env.update({
        "NODEDANCE_S04_DIND_ROOT": str(droot),
        "NODEDANCE_S04_DIND_HOST": endpoint,
        "NODEDANCE_S04_DIND_ENGINE": engine,
        "NODEDANCE_S04_FIXTURE_RUN_ID": fixture_id,
        "NODEDANCE_TEST_DOCKER_HOST": endpoint,
        "DOCKER_HOST": endpoint,
        "DOCKER_CONFIG": str(ROOT / ".artifacts/docker-config"),
    })
    go = str(LOCKED_GO) if LOCKED_GO.is_file() and os.access(LOCKED_GO, os.X_OK) else ""
    package_logs: dict[str, tuple[pathlib.Path, list[dict]]] = {}
    test_results: dict[str, tuple[str, str]] = {}
    all_commands_passed = False
    all_child_processes_reaped = True
    engine_check = subprocess.CompletedProcess([], 1, "Engine postcondition was not checked")
    engine_after_timed_out = False
    engine_after_reaped = True
    engine_after_ok = False
    preflight_reason = ""
    fatal_reason = ""
    cleanup_code: int | None = None

    def cleanup_once() -> int:
        nonlocal cleanup_code
        if cleanup_code is None:
            if not cleanup_permitted(all_child_processes_reaped):
                cleanup_code = 1
                write_line_to_file(attempt_dir / "fixtures-cleanup.jsonl", {
                    "record": "cleanup_blocked", "reason": "a test process group was not reaped; refusing concurrent resource deletion",
                })
            else:
                try:
                    cleanup_code = cleanup_fixtures(endpoint, droot, fixture_id, attempt_dir / "fixtures-cleanup.jsonl")
                except Exception as error:
                    cleanup_code = 1
                    write_line_to_file(attempt_dir / "fixtures-cleanup.jsonl", {
                        "record": "cleanup_exception", "error_type": type(error).__name__,
                    })
        return cleanup_code

    try:
        if not go:
            preflight_reason = "locked Go 1.26.8 executable is unavailable"
        else:
            go_version, version_timed_out, version_reaped = bounded_run([go, "version"], cwd=ROOT, env=env, timeout=15)
            go_version_text = output_text(go_version.stdout).strip()
            if go_version.returncode != 0 or version_timed_out or not version_reaped or "go1.26.8" not in go_version_text:
                preflight_reason = "locked Go 1.26.8 executable returned an unexpected version or exceeded its 15s bound"
            else:
                all_commands_passed = True
                for index, (label, package, names, timeout) in enumerate(GO_COMMANDS, start=1):
                    log_path = attempt_dir / f"go-{index}-{label}.jsonl"
                    command = go_test_command(go, package, names, timeout)
                    outer_timeout = parse_timeout_seconds(timeout) + 120
                    code, events, reaped = run_json(command, log_path, env, outer_timeout)
                    all_child_processes_reaped = all_child_processes_reaped and reaped
                    package_logs[label] = (log_path, events)
                    if code != 0:
                        all_commands_passed = False
                    for name in names:
                        test_results[name] = test_status(name, events, code)

                engine_check, engine_after_timed_out, engine_after_reaped = bounded_run(
                    [shutil.which("docker") or "docker", "--host", endpoint,
                     "version", "--format", "{{.Server.Version}}"],
                    cwd=ROOT, env=env, timeout=20)
                engine_after_version = output_text(engine_check.stdout).strip()
                engine_after_ok = engine_check.returncode == 0 and not engine_after_timed_out and engine_after_reaped and engine_after_version.startswith(engine + ".")
    except Exception as error:
        fatal_reason = f"acceptance runner raised {type(error).__name__}; see bounded command and cleanup evidence"
    finally:
        cleanup_once()

    if preflight_reason:
        status, attempt_result = preflight_cleanup_result(cleanup_code or 0)
        reason = preflight_reason + ("; exact fixture cleanup failed" if cleanup_code else "")
        report_all_cases(attempt, engine, status, reason, package_logs, attempt_dir)
        write_line_to_file(attempt_dir / "attempt-summary.jsonl", {
            "record": "attempt_summary", "attempt": attempt, "engine": engine,
            "fixture_cleanup_exit": cleanup_code, "status": status, "reason": reason,
        })
        return attempt_result

    if fatal_reason:
        cleanup_detail = "; exact fixture cleanup failed" if cleanup_code else ""
        reason = fatal_reason + cleanup_detail
        report_all_cases(attempt, engine, "FAIL", reason, package_logs, attempt_dir)
        write_line_to_file(attempt_dir / "attempt-summary.jsonl", {
            "record": "attempt_summary", "attempt": attempt, "engine": engine,
            "go_commands_passed": False, "fixture_cleanup_exit": cleanup_code,
            "status": "FAIL", "reason": reason,
        })
        return False, False

    engine_after_version = output_text(engine_check.stdout).strip()
    attempt_ok = all_commands_passed and cleanup_code == 0 and engine_after_ok
    attempt_not_ready = any(status == "NOT_READY" for status, _ in test_results.values())

    for case_id, names in CASE_TESTS.items():
        statuses = [test_results.get(name, ("FAIL", f"required test {name} was not run")) for name in names]
        if any(status == "FAIL" for status, _ in statuses):
            status = "FAIL"
        elif any(status == "NOT_READY" for status, _ in statuses):
            status = "NOT_READY"
        elif all(status == "PASS" for status, _ in statuses):
            status = "PASS"
        else:
            status = "FAIL"
        reasons = [reason for item_status, reason in statuses if item_status != "PASS" and reason]
        status, gate_reasons = final_case_gate(status, cleanup_code, engine_after_ok)
        reasons.extend(gate_reasons)
        if not all_commands_passed and not reasons:
            status = "FAIL"
            reasons.append("a required Go parent test process failed")
        case_log = write_case_log(case_id, attempt, engine, status, "; ".join(reasons), names, package_logs, attempt_dir)
        report_case(case_id, attempt, status, case_log, "; ".join(reasons))

    write_line_to_file(attempt_dir / "attempt-summary.jsonl", {
        "record": "attempt_summary", "attempt": attempt, "engine": engine,
        "go_commands_passed": all_commands_passed, "fixture_cleanup_exit": cleanup_code,
        "engine_version_after": engine_after_version, "engine_postcondition_passed": engine_after_ok,
        "engine_postcondition_timeout": engine_after_timed_out, "engine_postcondition_reaped": engine_after_reaped,
        "status": "PASS" if attempt_ok and not attempt_not_ready else "FAIL" if not attempt_ok else "NOT_READY",
    })
    return attempt_ok, attempt_not_ready


def write_line_to_file(path: pathlib.Path, payload: dict) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    with path.open("a", encoding="utf-8") as stream:
        write_line(stream, json.dumps(sanitize(payload), ensure_ascii=False))


CURRENT_REPORT: dict


def main() -> int:
    if len(sys.argv) != 5 or sys.argv[1] != "--mode" or sys.argv[3] != "--repeat":
        print("usage: acceptance-s04.py --mode full|integration|e2e --repeat 1|3", file=sys.stderr)
        return 2
    mode, repeat_text = sys.argv[2], sys.argv[4]
    if mode not in {"full", "integration", "e2e"}:
        print("invalid mode", file=sys.stderr)
        return 2
    try:
        repeat = int(repeat_text)
    except ValueError:
        print("repeat must be 1 or 3", file=sys.stderr)
        return 2
    if repeat not in {1, 3} or (mode == "full" and repeat != 3):
        print("full mode requires exactly three attempts; integration/e2e use one", file=sys.stderr)
        return 2
    engine = os.environ.get("NODEDANCE_TEST_ENGINE", "")
    if engine not in {"28", "29"}:
        print("S04 NOT_READY: NODEDANCE_TEST_ENGINE must select locked Engine 28 or 29", file=sys.stderr)
        return 3
    try:
        root = dind_root()
    except (OSError, RuntimeError) as error:
        print(f"S04 NOT_READY: {error}", file=sys.stderr)
        return 3
    ATTEMPT_ROOT.mkdir(parents=True, exist_ok=True, mode=0o700)
    global CURRENT_REPORT
    CURRENT_REPORT = make_initial_report(mode, repeat, engine)
    CURRENT_REPORT["dind_root"] = str(root)
    go_probe, go_probe_timed_out, go_probe_reaped = bounded_run(
        [str(LOCKED_GO), "version"], cwd=ROOT, env=clean_env(), timeout=15) if LOCKED_GO.is_file() else (
            subprocess.CompletedProcess([str(LOCKED_GO), "version"], 127, "locked Go executable is unavailable"), False, True)
    CURRENT_REPORT["environment"] = {
        "runner": os.environ.get("NODEDANCE_TEST_RUNNER", "local"),
        "engine": engine,
        "go": output_text(go_probe.stdout).strip(),
        "go_probe_timeout": go_probe_timed_out,
        "go_probe_reaped": go_probe_reaped,
    }
    save_report(CURRENT_REPORT)

    attempt_failure = False
    attempt_not_ready = False
    for attempt in range(1, repeat + 1):
        print(f"S04 attempt {attempt}/{repeat}: Engine {engine}; real Agent/Core/Docker tests", flush=True)
        passed, not_ready = run_attempt(attempt, mode, engine, root)
        attempt_failure |= not passed and not not_ready
        attempt_not_ready |= not_ready
        CURRENT_REPORT["updated_at"] = timestamp()
        save_report(CURRENT_REPORT)

    all_case_runs_pass = all(
        [run.get("attempt") for run in case["runs"]] == list(range(1, repeat + 1))
        and all(run["status"] == "PASS" for run in case["runs"])
        for case in CURRENT_REPORT["tests"].values()
    )
    if attempt_failure or any(case["status"] == "FAIL" for case in CURRENT_REPORT["tests"].values()):
        CURRENT_REPORT["status"] = "FAIL"
        CURRENT_REPORT["verification_status"] = "FAIL"
        CURRENT_REPORT["reason"] = "At least one named real acceptance test, Go parent process, cleanup, or Engine postcondition failed."
    elif attempt_not_ready or not all_case_runs_pass or mode != "full":
        CURRENT_REPORT["status"] = "NOT_READY"
        CURRENT_REPORT["verification_status"] = "NOT_READY"
        CURRENT_REPORT["reason"] = "The full S04 gate requires all 11 cases to pass in three complete attempts on each required CI Engine/architecture matrix."
    else:
        CURRENT_REPORT["status"] = "PASS"
        CURRENT_REPORT["verification_status"] = "PASS"
        CURRENT_REPORT["reason"] = "All original S04 cases and S04-SUP-01 passed in three complete real Engine/Agent/Core/browser attempts."
    CURRENT_REPORT["updated_at"] = timestamp()
    save_report(CURRENT_REPORT)
    print(f"S04: {CURRENT_REPORT['status']}; report=reports/stages/S04.json; evidence={ATTEMPT_ROOT.relative_to(ROOT)}", flush=True)
    return 0 if CURRENT_REPORT["status"] == "PASS" else 1 if CURRENT_REPORT["status"] == "FAIL" else 2


if __name__ == "__main__":
    raise SystemExit(main())
