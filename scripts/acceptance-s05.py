#!/usr/bin/env python3
"""Run the executable S05 acceptance subset on owned Engine 28 and 29.

The report keeps every normative S05 case. Cases without an integrated
Core/Agent/Engine proof remain NOT_READY even when related component tests
pass. A full-run report is never upgraded to PASS while any case is missing.
"""

from __future__ import annotations

import argparse
import datetime as dt
import hashlib
import json
import os
import pathlib
import platform
import re
import shutil
import subprocess
import sys
import uuid


ROOT = pathlib.Path(__file__).resolve().parents[1]
REGISTRY = json.loads((ROOT / "tests/registry.json").read_text())
STAGE = next(stage for stage in REGISTRY["stages"] if stage["id"] == "S05")
CASES = STAGE.get("tests", []) + STAGE.get("supplemental_tests", [])
CASE_IDS = {case["id"] for case in CASES}
REPORT = ROOT / "reports/stages/S05.json"
STATUS_PATH = ROOT / "reports/status.json"
RUN_ID = dt.datetime.now(dt.timezone.utc).strftime("%Y%m%dT%H%M%SZ") + "-" + uuid.uuid4().hex[:8]
EVIDENCE_ROOT = ROOT / ".artifacts/logs/acceptance-s05" / RUN_ID
FORMAL_INTEGRATION_CASES = {
    "S05-01", "S05-02", "S05-03", "S05-04", "S05-05", "S05-07", "S05-08", "S05-09", "S05-10", "S05-12",
}
FORMAL_ASSERTION_MARKERS = {
    "S05-01": "S05_CASE S05-01 lifecycle=start,stop,restart,pause,resume,delete,rename verified=true",
    "S05-02": "S05_CASE S05-02 identical_submissions=10 actual_restarts=1 verified=true",
    "S05-03": "S05_CASE S05-03 same_key_different_intent=409 verified=true",
    "S05-04": "S05_CASE S05-04 in_flight_resource_conflicts=stop,restart,delete verified=true",
    "S05-05": "S05_CASE S05-05 core_agent_process_restart=before,during,after verified=true",
    "S05-07": "S05_CASE S05-07 wrong_delete_confirmation=400 running_delete=failed stopped_delete=succeeded verified=true",
    "S05-08": "S05_CASE S05-08 independent_rename=succeeded compose_rename=409 verified=true",
    "S05-09": "S05_CASE S05-09 logs=stdout+stderr+unicode+large tty=raw verified=true",
    "S05-10": "S05_CASE S05-10 stats=shared-and-last-close active_engine_stats=0 verified=true",
    "S05-12": "S05_EVIDENCE success_task_audit=true verified=true",
}
FORMAL_SUPPLEMENTAL_MARKERS = {
    "S05-06-QUEUED-CANCEL-OFFLINE": "S05_S06_CASE queued_cancel=not_dispatched repeated_delete=same_result docker_unchanged=true offline_post=503 no_rows=true reconnect_no_backlog=true delivered_timeout=unknown_result_pending no_replay=true verified=true",
}
CURRENT_CORE_GAPS = {
    "S05-06": "The current complete Core+Agent+Engine fixture suite has not run on this candidate. Its historical attempt failed during make check preflight before normative S05 cases started; timeout, cancellation, offline, and reconciliation acceptance remains pending.",
    "S05-11": "The current complete Core+Agent+Engine fixture suite has not run on this candidate. Its historical attempt failed during make check preflight before normative S05 cases started; Core task-row and Agent journal persistence-failure acceptance remains pending.",
}


def now() -> str:
    return dt.datetime.now(dt.timezone.utc).isoformat()


def write_report(report: dict[str, object]) -> None:
    REPORT.parent.mkdir(parents=True, exist_ok=True)
    REPORT.write_text(json.dumps(report, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    STATUS_PATH.parent.mkdir(parents=True, exist_ok=True)
    summary = json.loads(STATUS_PATH.read_text(encoding="utf-8")) if STATUS_PATH.exists() else {"schema": 1, "stages": {}}
    summary.setdefault("stages", {})["S05"] = {
        "status": report["status"],
        "mode": report["mode"],
        "run_id": report["run_id"],
        "updated_at": report["updated_at"],
        "reason": report.get("reason", ""),
    }
    summary["updated_at"] = report["updated_at"]
    STATUS_PATH.write_text(json.dumps(summary, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")


def run_command(label: str, command: list[str], *, env: dict[str, str], timeout: int = 180) -> tuple[int, pathlib.Path]:
    safe = re.sub(r"[^A-Za-z0-9_.-]+", "_", label)
    path = EVIDENCE_ROOT / f"{safe}.log"
    path.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
    with path.open("w", encoding="utf-8") as log:
        log.write("$ " + " ".join(command) + "\n")
        log.flush()
        try:
            result = subprocess.run(command, cwd=ROOT, env=env, stdout=log, stderr=subprocess.STDOUT,
                                    text=True, timeout=timeout)
            code = result.returncode
        except subprocess.TimeoutExpired:
            log.write(f"\nTIMEOUT after {timeout} seconds\n")
            code = 124
    state = "PASS" if code == 0 else "FAIL"
    print(f"S05 {label}: {state} exit={code} evidence={path.relative_to(ROOT)}", flush=True)
    return code, path


def locked_toolchain(go_bin: str) -> tuple[str, str]:
    result = subprocess.run([go_bin, "version"], cwd=ROOT, capture_output=True, text=True)
    if result.returncode or "go1.26.8" not in result.stdout:
        raise RuntimeError("S05 acceptance requires the locked Go 1.26.8 toolchain")
    node = subprocess.run(["node", "--version"], cwd=ROOT, capture_output=True, text=True)
    if node.returncode or node.stdout.strip() != "v22.23.3":
        raise RuntimeError("S05 acceptance requires the locked Node.js 22.23.3 toolchain")
    return result.stdout.strip(), node.stdout.strip()


def discover_dind_root() -> pathlib.Path:
    configured = os.environ.get("NODEDANCE_S05_DIND_ROOT") or os.environ.get("NODEDANCE_S04_DIND_ROOT")
    candidates = []
    if configured:
        candidates.append(pathlib.Path(configured))
    candidates.extend((ROOT, ROOT.parent / "NodeDance-s04"))
    for candidate in candidates:
        root = candidate.resolve()
        if all((root / ".artifacts/dind" / f"v{engine}" / "owner.json").is_file() for engine in (28, 29)):
            return root
    raise RuntimeError("both owner-marked v28 and v29 DIND engines are required; runner will not create, stop, or restart daemons")


def verify_engine(dind_root: pathlib.Path, engine: int) -> tuple[str, str]:
    engine_root = dind_root / ".artifacts/dind" / f"v{engine}"
    marker = json.loads((engine_root / "owner.json").read_text(encoding="utf-8"))
    socket = (engine_root / "socket/docker.sock").resolve()
    expected_socket = engine_root / "socket/docker.sock"
    if marker.get("suite") != "nodedance-s00-dind" or marker.get("socket") != str(expected_socket):
        raise RuntimeError(f"v{engine} DIND owner marker mismatch; refusing socket")
    if socket != expected_socket or not socket.is_socket():
        raise RuntimeError(f"v{engine} DIND socket is unavailable or resolves outside the owned path")
    endpoint = "unix://" + str(socket)
    docker = shutil.which("docker")
    if not docker:
        raise RuntimeError("Docker CLI is unavailable")
    result = subprocess.run([docker, "--host", endpoint, "version", "--format", "{{.Server.Version}}"],
                            cwd=ROOT, capture_output=True, text=True, timeout=15)
    version = result.stdout.strip()
    if result.returncode or not version.startswith(f"{engine}.") or version != marker.get("server_version"):
        raise RuntimeError(f"v{engine} Engine does not match its owner marker: {version!r}")
    return endpoint, str(engine_root)


def verify_fixture_manifest(log_path: pathlib.Path) -> dict[str, object]:
    text = log_path.read_text(encoding="utf-8", errors="replace")
    created = re.findall(r"S05_FIXTURE suite=(\S+) kind=(main|compose) id=([0-9a-f]{64}) name=(\S+)", text)
    cleanup = re.findall(r"S05_FIXTURE_CLEANUP suite=(\S+) (?:kind=(\S+) )?id=([0-9a-f]{64}) [^\n]*verified_absent=true", text)
    final = re.findall(r"S05_FIXTURE_MANIFEST_CLEANUP suite=(\S+) expected_ids=(\d+) remaining_ids=(\d+) verified=true", text)
    if len(created) != 2 or {kind for _, kind, _, _ in created} != {"main", "compose"}:
        return {"verified": False, "reason": "expected exactly one explicitly recorded main and Compose fixture"}
    suites = {suite for suite, _, _, _ in created}
    ids = {container_id for _, _, container_id, _ in created}
    cleanup_ids = {container_id for suite, _, container_id in cleanup if suite in suites}
    if len(suites) != 1 or cleanup_ids != ids:
        return {"verified": False, "reason": "cleanup records do not cover every created full fixture ID", "created_ids": sorted(ids), "cleanup_ids": sorted(cleanup_ids)}
    matching_final = [entry for entry in final if entry[0] in suites]
    if len(matching_final) != 1 or int(matching_final[0][1]) != len(ids) or int(matching_final[0][2]) != 0:
        return {"verified": False, "reason": "final suite inventory did not prove every manifest item absent", "created_ids": sorted(ids)}
    return {"verified": True, "suite": next(iter(suites)), "created_ids": sorted(ids),
            "cleanup_ids": sorted(cleanup_ids), "expected_ids": len(ids), "remaining_ids": 0}


def verify_process_recovery_manifest(log_path: pathlib.Path, *, core: bool) -> dict[str, object]:
    text = log_path.read_text(encoding="utf-8", errors="replace")
    prefix = "S05_CORE_RECOVERY" if core else "S05_RECOVERY"
    created = re.findall(rf"{prefix}_FIXTURE suite=(\S+) phase=(before|during|after) id=([0-9a-f]{{64}}) name=(\S+)", text)
    cleaned = re.findall(rf"{prefix}_FIXTURE_CLEANUP suite=(\S+) phase=(before|during|after) id=([0-9a-f]{{64}}) verified_absent=true", text)
    final = re.findall(rf"{prefix}_MANIFEST_CLEANUP suite=(\S+) expected_ids=(\d+) remaining_ids=(\d+) verified=true", text)
    created_ids = {container_id for _, _, container_id, _ in created}
    cleanup_ids = {container_id for _, _, container_id in cleaned}
    created_phases = {phase for _, phase, _, _ in created}
    cleanup_phases = {phase for _, phase, _ in cleaned}
    suites = {suite for suite, _, _, _ in created}
    matching_final = [entry for entry in final if entry[0] in suites]
    marker_prefix = "S05_CORE_RECOVERY_CASE" if core else "S05_RECOVERY_CASE"
    required_markers = [f"{marker_prefix} phase={phase}" for phase in ("before_mutation", "during_mutation", "after_terminal")]
    if (len(created) != 3 or created_phases != {"before", "during", "after"} or len(suites) != 1 or
            len(created_ids) != 3 or cleanup_ids != created_ids or cleanup_phases != {"before", "during", "after"}):
        return {"verified": False, "reason": "exact before/during/after fixture identities and per-ID cleanup records do not match",
                "created_ids": sorted(created_ids), "cleanup_ids": sorted(cleanup_ids)}
    if len(matching_final) != 1 or int(matching_final[0][1]) != 3 or int(matching_final[0][2]) != 0:
        return {"verified": False, "reason": "final suite inventory did not prove all three fixtures absent",
                "created_ids": sorted(created_ids)}
    if not all(marker in text for marker in required_markers):
        return {"verified": False, "reason": "one or more controlled process-kill phases lack a verified marker",
                "created_ids": sorted(created_ids)}
    return {"verified": True, "suite": next(iter(suites)), "created_ids": sorted(created_ids),
            "cleanup_ids": sorted(cleanup_ids), "expected_ids": 3, "remaining_ids": 0,
            "phases": sorted(created_phases)}


def verify_stream_fixture_manifest(log_path: pathlib.Path) -> dict[str, object]:
    text = log_path.read_text(encoding="utf-8", errors="replace")
    created = re.findall(r"S05_FIXTURE suite=(\S+) kind=(logs|tty|stats) id=([0-9a-f]{64}) name=(\S+)", text)
    cleaned = re.findall(
        r"S05_FIXTURE_CLEANUP suite=(\S+) kind=(logs|tty|stats) id=([0-9a-f]{64}) removed=true verified_absent=true", text
    )
    final = re.findall(r"S05_FIXTURE_MANIFEST_CLEANUP suite=(\S+) expected_ids=(\d+) remaining_ids=(\d+) verified=true", text)
    created_ids = {container_id for _, _, container_id, _ in created}
    cleanup_ids = {container_id for _, _, container_id in cleaned}
    suites = {suite for suite, _, _, _ in created}
    kinds = {kind for _, kind, _, _ in created}
    matching_final = [entry for entry in final if entry[0] in suites]
    if len(created) != 3 or len(created_ids) != 3 or kinds != {"logs", "tty", "stats"} or len(suites) != 1:
        return {"verified": False, "reason": "expected exactly one uniquely identified logs, tty, and stats fixture",
                "created_ids": sorted(created_ids), "cleanup_ids": sorted(cleanup_ids)}
    if cleanup_ids != created_ids:
        return {"verified": False, "reason": "per-ID cleanup records do not cover every stream fixture",
                "created_ids": sorted(created_ids), "cleanup_ids": sorted(cleanup_ids)}
    if len(matching_final) != 1 or int(matching_final[0][1]) != 3 or int(matching_final[0][2]) != 0:
        return {"verified": False, "reason": "final suite inventory did not prove all stream fixtures absent",
                "created_ids": sorted(created_ids)}
    return {"verified": True, "suite": next(iter(suites)), "created_ids": sorted(created_ids),
            "cleanup_ids": sorted(cleanup_ids), "expected_ids": 3, "remaining_ids": 0,
            "kinds": sorted(kinds)}


def verify_stream_ui_responsive(log_path: pathlib.Path) -> dict[str, object]:
    text = log_path.read_text(encoding="utf-8", errors="replace")
    for line in text.splitlines():
        if "S05_STREAM_BROWSER " not in line or " result=" not in line:
            continue
        try:
            payload = json.loads(line.split(" result=", 1)[1])
        except json.JSONDecodeError:
            return {"verified": False, "reason": "product UI browser result is not valid JSON"}
        responsive = payload.get("responsive")
        if not isinstance(responsive, dict):
            return {"verified": False, "reason": "product UI browser omitted its responsive measurement"}
        width = responsive.get("width")
        scroll_width = responsive.get("scrollWidth")
        if not isinstance(width, int) or not isinstance(scroll_width, int) or width <= 0 or scroll_width > width + 2:
            return {"verified": False, "reason": "390x844 product controls overflowed or returned invalid dimensions",
                    "width": width, "scroll_width": scroll_width}
        return {"verified": True, "viewport": {"width": 390, "height": 844},
                "controls_width": width, "controls_scroll_width": scroll_width}
    return {"verified": False, "reason": "product UI browser evidence line is missing"}


def verify_queued_cancel_offline_fixture_manifest(log_path: pathlib.Path) -> dict[str, object]:
    text = log_path.read_text(encoding="utf-8", errors="replace")
    created = re.findall(r"S05_S06_FIXTURE suite=(\S+) slot=(\d+) id=([0-9a-f]{64}) name=(\S+)", text)
    cleaned = re.findall(r"S05_S06_FIXTURE_CLEANUP suite=(\S+) id=([0-9a-f]{64}) name=(\S+) removed=true verified_absent=true", text)
    final = re.findall(r"S05_S06_FIXTURE_MANIFEST suite=(\S+) expected_ids=(\d+) remaining_ids=0 verified=true", text)
    capacities = re.findall(r"S05_S06_CAPACITY negotiated_slots=(\d+) held_slots=(\d+) queued_target_slot=(\d+) verified=true", text)
    barriers = re.findall(r"S05_S06_DISPATCH_BARRIER negotiated_slots=(\d+) delivered_slots=(\d+) agent_journal_rows=(\d+) extra_status=queued extra_delivery=ready extra_agent_journal_rows=0 verified=true", text)
    created_ids = {container_id for _, _, container_id, _ in created}
    cleanup_ids = {container_id for _, container_id, _ in cleaned}
    suites = {suite for suite, _, _, _ in created}
    if len(capacities) != 1:
        return {"verified": False, "reason": "negotiated Agent capacity marker is missing or duplicated"}
    slots, held_slots, queued_slot = (int(value) for value in capacities[0])
    expected_slots = set(range(slots + 1))
    created_slots = {int(slot) for _, slot, _, _ in created}
    if slots < 1 or held_slots != slots or queued_slot != slots or created_slots != expected_slots or len(created) != slots + 1:
        return {"verified": False, "reason": "fixture set does not match negotiated capacity plus one queued target",
                "negotiated_slots": slots, "created_slots": sorted(created_slots), "created_ids": sorted(created_ids)}
    if len(barriers) != 1 or tuple(int(value) for value in barriers[0]) != (slots, slots, slots):
        return {"verified": False, "reason": "dispatch barrier did not prove every negotiated slot delivered and the extra target still queued",
                "negotiated_slots": slots, "barriers": barriers}
    if len(suites) != 1 or len(created_ids) != slots + 1 or cleanup_ids != created_ids:
        return {"verified": False, "reason": "exact S05-06 fixture cleanup records do not cover every created full ID",
                "created_ids": sorted(created_ids), "cleanup_ids": sorted(cleanup_ids)}
    matching_final = [entry for entry in final if entry[0] in suites]
    if len(matching_final) != 1 or int(matching_final[0][1]) != slots + 1:
        return {"verified": False, "reason": "final suite inventory did not prove every exact fixture absent",
                "created_ids": sorted(created_ids)}
    marker = FORMAL_SUPPLEMENTAL_MARKERS["S05-06-QUEUED-CANCEL-OFFLINE"]
    timeout_marker = re.search(r"S05_S06_OPERATION_TIMEOUT task_status=unknown result=result_pending operation_deadline=30s delivered=true delete_status=409 engine_unchanged=true reconciliation_inspected=true proxy_forwarded_restart=0 reconnect_no_replay=true verified=true generation=(\d+)->(\d+)", text)
    reconciliation = re.search(
        r"S05_S06_RECONCILIATION agent_journal_status=unknown execution_phase=mutation_may_have_started "
        r"baseline_verified=true baseline_started_at=\S+ baseline_restart_count=(\d+) engine_restart_count=(\d+) "
        r"engine_started_at_unchanged=true process_start_count_unchanged=true engine_events=0 "
        r"agent_engine_inspect_requests=([1-9]\d*) proxy_restart_intercepts=1 proxy_restart_forwarded=0 postcondition_proven=false "
        r"ambiguity=daemon_acceptance_unproven unresolved_result=unknown verified=true",
        text,
    )
    reconciliation_verified = (reconciliation is not None and
                               reconciliation.group(1) == reconciliation.group(2) and
                               int(reconciliation.group(3)) > 0)
    if marker not in text or timeout_marker is None or int(timeout_marker.group(2)) <= int(timeout_marker.group(1)) or not reconciliation_verified:
        return {"verified": False, "reason": "safe cancellation/offline or delivered timeout/no-replay evidence is missing",
                "created_ids": sorted(created_ids)}
    return {"verified": True, "suite": next(iter(suites)), "negotiated_slots": slots,
            "created_ids": sorted(created_ids), "cleanup_ids": sorted(cleanup_ids),
            "expected_ids": slots + 1, "remaining_ids": 0}


def verify_agent_journal_failure_evidence(log_path: pathlib.Path) -> dict[str, object]:
    text = log_path.read_text(encoding="utf-8", errors="replace")
    match = re.search(
        r"S05_EVIDENCE agent_task_journal_failure=unknown_result_pending journal_rows=0 "
        r"journal_insert_attempts=1 agent_mutation_api_calls=0 engine_mutation_api_calls=0 "
        r"core_unknown_reason=delivery_committed_agent_journal_absent cross_connection_result_unproven=true "
        r"resource_claim_retained=true docker_started_at_unchanged=true "
        r"docker_start_count_unchanged=true docker_events_unchanged=true unknown_audit=true "
        r"generation=(\d+)->(\d+) verified=true",
        text,
    )
    if match is None:
        return {"verified": False, "reason": "Agent journal failure did not prove unknown audit, retained claim and zero Engine mutation"}
    previous, current = (int(value) for value in match.groups())
    if current <= previous:
        return {"verified": False, "reason": "Agent did not reconnect on a newer synchronized generation",
                "previous_generation": previous, "current_generation": current}
    return {"verified": True, "previous_generation": previous, "current_generation": current}


def source_digest() -> str:
    paths = [
        "scripts/acceptance-s05.py",
        "scripts/test/s05-task-journal.py",
        "scripts/test/s05-acceptance-selftest.py",
        "scripts/test/s05-sanitize-evidence.py",
        "scripts/stage-runner.py",
        "Makefile",
        ".github/workflows/s05-acceptance.yml",
        "internal/core/server/task_bridge.go",
        "internal/core/server/task_bridge_integration_test.go",
        "internal/core/server/task_bridge_process_recovery_test.go",
        "internal/core/server/task_core_process_restart_test.go",
        "internal/core/server/task_bridge_cancel_offline_integration_test.go",
        "internal/core/server/container_streams_integration_test.go",
        "internal/core/server/container_streams.go",
        "internal/core/server/agent_websocket.go",
        "internal/core/server/server.go",
        "internal/core/server/task_api.go",
        "internal/core/server/task_api_test.go",
        "internal/core/tasks/tasks.go",
        "internal/agent/container_stream_bridge.go",
        "internal/agent/runtime.go",
        "internal/agent/metrics_writer.go",
        "internal/protocol/container_streams.go",
        "web/src/api.ts",
        "web/src/components/NodesDashboard.vue",
        "web/tests/s05-container-actions.spec.ts",
        "web/tests/s05-task-browser.mjs",
        "web/tests/s05-container-stream-disconnect.mjs",
        "web/tests/s05-container-stream-ui.mjs",
        "web/src/components/ContainerStreams.vue",
        "web/src/streamApi.ts",
    ]
    digest = hashlib.sha256()
    for relative in paths:
        path = ROOT / relative
        digest.update(relative.encode() + b"\0" + path.read_bytes())
    return digest.hexdigest()


def case_record(case: dict[str, str]) -> dict[str, object]:
    return {
        "status": "NOT_READY",
        "action": case["action"],
        "expected": case["expected"],
        "environment": case["environment"],
        "evidence_required": case["evidence"],
        "runs": [],
        "reason": "No current final-code S05 attempt has completed.",
    }


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--mode", choices=("full", "integration", "e2e"), required=True)
    parser.add_argument("--repeat", type=int, default=1)
    args = parser.parse_args()
    if args.repeat != 1:
        parser.error("acceptance runs once; rerun the affected suite after a failure or code change")

    EVIDENCE_ROOT.mkdir(parents=True, exist_ok=False, mode=0o700)
    go_bin = os.environ.get("NODEDANCE_GO_BIN", str(ROOT / ".tools/go1.26.8/bin/go"))
    component_output = ROOT / ".artifacts/work-s05" / RUN_ID
    component_env = os.environ.copy()
    component_env["GOTOOLCHAIN"] = "local"
    component_env["NODEDANCE_GO_BIN"] = go_bin
    component_command = [sys.executable, "scripts/test/s05-task-journal.py", "--output-dir", str(component_output)]
    component_code, component_log = run_command("task-journal-standard-and-race", component_command,
                                                env=component_env, timeout=300)
    component_check = {
        "id": "S05-TASK-JOURNAL-STANDARD-RACE",
        "status": "PASS" if component_code == 0 else "FAIL",
        "command": component_command,
        "evidence": str(component_log.relative_to(ROOT)),
        "report": str((component_output / "report.json").relative_to(ROOT)),
        "exit_code": component_code,
        "formal_stage_cases_remain_independent": True,
    }
    try:
        go_version, node_version = locked_toolchain(go_bin)
        dind_root = discover_dind_root()
        engines = {number: verify_engine(dind_root, number) for number in (28, 29)}
    except (OSError, RuntimeError, json.JSONDecodeError) as error:
        report = {
            "schema": 1, "stage": "S05", "mode": args.mode, "run_id": RUN_ID, "updated_at": now(),
            "status": "FAIL" if component_code else "NOT_READY",
            "verification_status": "FAIL" if component_code else "NOT_READY", "repeat_required": 1,
            "repeat_requested": args.repeat, "reason": str(error), "tests": {case["id"]: case_record(case) for case in CASES},
            "environment": {"host": platform.platform(), "architecture": platform.machine()},
            "evidence_root": str(EVIDENCE_ROOT.relative_to(ROOT)),
            "supplemental_checks": [component_check],
        }
        write_report(report)
        print(f"S05 NOT_READY: {error}; report={REPORT.relative_to(ROOT)}", file=sys.stderr)
        return 1 if component_code else 2

    report: dict[str, object] = {
        "schema": 1, "stage": "S05", "mode": args.mode, "run_id": RUN_ID,
        "source_revision": subprocess.run(["git", "rev-parse", "HEAD"], cwd=ROOT, capture_output=True, text=True, check=True).stdout.strip(),
        "implementation_tree_sha256": source_digest(), "updated_at": now(),
        "status": "NOT_READY", "verification_status": "NOT_RUN", "repeat_required": 1,
        "repeat_requested": args.repeat, "reason": "Acceptance attempts are running.",
        "environment": {"go": go_version, "node": node_version, "architecture": platform.machine(),
                        "dind_root": str(dind_root), "engines": {str(key): engines[key][0] for key in engines}},
        "evidence_root": str(EVIDENCE_ROOT.relative_to(ROOT)),
        "tests": {case["id"]: case_record(case) for case in CASES},
        "supplemental_checks": [component_check],
    }
    write_report(report)

    playwright_env = os.environ.copy()
    playwright_env["PATH"] = str(ROOT / ".tools/node-v22.23.3/bin") + os.pathsep + str(ROOT / ".tools/pnpm/node_modules/.bin") + os.pathsep + playwright_env.get("PATH", "")
    playwright_command = ["pnpm", "--dir", "web", "exec", "playwright", "test", "--config=playwright.s03.config.ts",
                          "web/tests/s05-container-actions.spec.ts"]
    playwright_code, playwright_log = run_command("paused-ui-playwright", playwright_command, env=playwright_env, timeout=180)
    report["supplemental_checks"].append({"id": "S05-UI-PAUSE-STATE", "status": "PASS" if playwright_code == 0 else "FAIL",
                                          "command": playwright_command, "evidence": str(playwright_log.relative_to(ROOT))})

    go_common = [go_bin, "test", "-v", "-mod=readonly", "-timeout=240s", "-count=1"]
    integration_by_attempt: dict[int, dict[int, dict[str, object]]] = {}
    recovery_by_attempt: dict[int, dict[int, dict[str, object]]] = {}
    stream_by_attempt: dict[int, dict[int, dict[str, object]]] = {}
    cancel_offline_by_attempt: dict[int, dict[int, dict[str, object]]] = {}
    confirmation_by_attempt: dict[int, dict[str, object]] = {}
    any_failure = playwright_code != 0
    for attempt in range(1, args.repeat + 1):
        integration_by_attempt[attempt] = {}
        recovery_by_attempt[attempt] = {}
        stream_by_attempt[attempt] = {}
        cancel_offline_by_attempt[attempt] = {}
        confirmation_env = os.environ.copy()
        confirmation_env["GOTOOLCHAIN"] = "local"
        confirmation_command = [*go_common, "./internal/core/server", "-run", "^TestTaskDeleteRequiresExactFullTargetConfirmationBeforeQueue$"]
        confirmation_code, confirmation_log = run_command(f"attempt-{attempt}-delete-confirmation", confirmation_command,
                                                           env=confirmation_env, timeout=180)
        confirmation_by_attempt[attempt] = {"status": "PASS" if confirmation_code == 0 else "FAIL",
                                             "evidence": str(confirmation_log.relative_to(ROOT)), "exit_code": confirmation_code}
        if confirmation_code:
            any_failure = True

        for engine in (28, 29):
            endpoint, engine_root = engines[engine]
            env = os.environ.copy()
            env["GOTOOLCHAIN"] = "local"
            env["NODEDANCE_S04_DIND_HOST"] = endpoint
            env["NODEDANCE_S05_DIND_ROOT"] = engine_root
            if dind_root != ROOT:
                env["NODEDANCE_S04_DIND_ROOT"] = str(dind_root)
            command = [*go_common, "./internal/core/server", "-run", "^TestS05CoreAgentBrowserRestartIdempotencyOnOwnedDIND$"]
            label = f"attempt-{attempt}-engine-{engine}-core-agent-browser"
            code, log_path = run_command(label, command, env=env, timeout=180)
            manifest = verify_fixture_manifest(log_path)
            text = log_path.read_text(encoding="utf-8", errors="replace")
            success_marker = "--- PASS: TestS05CoreAgentBrowserRestartIdempotencyOnOwnedDIND"
            status = "PASS" if code == 0 and success_marker in text and manifest.get("verified") else "FAIL"
            if status == "FAIL":
                any_failure = True
            integration_by_attempt[attempt][engine] = {
                "status": status, "exit_code": code, "command": command,
                "evidence": str(log_path.relative_to(ROOT)), "fixture_manifest": manifest,
                "engine_version": engines[engine][0],
                "case_markers": {case_id: marker in text for case_id, marker in FORMAL_ASSERTION_MARKERS.items()},
                "supplemental_assertions": {
                    "core_task_insert_failure_without_docker_mutation": "S05_EVIDENCE core_task_insert_failure=500 task_rows_unchanged=true docker_started_at_unchanged=true docker_start_count_unchanged=true docker_events_unchanged=true verified=true" in text,
                    "agent_task_journal_failure_zero_mutation": verify_agent_journal_failure_evidence(log_path).get("verified") is True,
                    "failed_task_audit_redacted": "S05_EVIDENCE failed_task_audit=true secret_redacted=true verified=true" in text,
                    "success_task_audit": "S05_EVIDENCE success_task_audit=true verified=true" in text,
                },
            }

            stream_command = [
                *go_common, "./internal/core/server", "-run", "^TestS05CoreAgentBrowserContainerStreamsOnOwnedDIND$",
            ]
            stream_label = f"attempt-{attempt}-engine-{engine}-core-agent-browser-streams"
            stream_code, stream_log = run_command(stream_label, stream_command, env=env, timeout=240)
            stream_text = stream_log.read_text(encoding="utf-8", errors="replace")
            stream_manifest = verify_stream_fixture_manifest(stream_log)
            responsive_evidence = verify_stream_ui_responsive(stream_log)
            stream_markers = {case_id: marker in stream_text for case_id, marker in FORMAL_ASSERTION_MARKERS.items()
                              if case_id in {"S05-09", "S05-10"}}
            stream_status = "PASS" if (
                stream_code == 0 and "--- PASS: TestS05CoreAgentBrowserContainerStreamsOnOwnedDIND" in stream_text and
                stream_manifest.get("verified") and responsive_evidence.get("verified") and all(stream_markers.values())
            ) else "FAIL"
            if stream_status == "FAIL":
                any_failure = True
            stream_by_attempt[attempt][engine] = {
                "status": stream_status, "exit_code": stream_code, "command": stream_command,
                "evidence": str(stream_log.relative_to(ROOT)), "fixture_manifest": stream_manifest,
                "engine_version": engines[engine][0], "case_markers": stream_markers,
                "stats_last_subscriber_closed": "active_engine_stats=0" in stream_text,
                "responsive_product_ui": responsive_evidence,
            }

            cancel_offline_command = [
                *go_common, "./internal/core/server", "-run", "^TestS05QueuedCancelAndOfflineNoBacklogOnOwnedDIND$",
            ]
            cancel_offline_label = f"attempt-{attempt}-engine-{engine}-queued-cancel-offline"
            cancel_offline_code, cancel_offline_log = run_command(
                cancel_offline_label, cancel_offline_command, env=env, timeout=300,
            )
            cancel_offline_text = cancel_offline_log.read_text(encoding="utf-8", errors="replace")
            cancel_offline_manifest = verify_queued_cancel_offline_fixture_manifest(cancel_offline_log)
            cancel_offline_status = "PASS" if (
                cancel_offline_code == 0 and
                "--- PASS: TestS05QueuedCancelAndOfflineNoBacklogOnOwnedDIND" in cancel_offline_text and
                FORMAL_SUPPLEMENTAL_MARKERS["S05-06-QUEUED-CANCEL-OFFLINE"] in cancel_offline_text and
                cancel_offline_manifest.get("verified")
            ) else "FAIL"
            if cancel_offline_status == "FAIL":
                any_failure = True
            cancel_offline_by_attempt[attempt][engine] = {
                "status": cancel_offline_status, "exit_code": cancel_offline_code,
                "command": cancel_offline_command, "evidence": str(cancel_offline_log.relative_to(ROOT)),
                "fixture_manifest": cancel_offline_manifest, "engine_version": engines[engine][0],
                "required_marker": FORMAL_SUPPLEMENTAL_MARKERS["S05-06-QUEUED-CANCEL-OFFLINE"],
            }

            process_runs: dict[str, dict[str, object]] = {}
            for name, test_name, is_core in (
                ("agent_sigkill", "TestS05AgentProcessKillRecoveryOnOwnedDIND", False),
                ("core_sigkill", "TestS05CoreAgentProcessKillRecoveryOnOwnedDIND", True),
            ):
                process_command = [*go_common, "./internal/core/server", "-run", f"^{test_name}$"]
                process_label = f"attempt-{attempt}-engine-{engine}-{name}-recovery"
                process_code, process_log = run_command(process_label, process_command, env=env, timeout=300)
                process_text = process_log.read_text(encoding="utf-8", errors="replace")
                process_manifest = verify_process_recovery_manifest(process_log, core=is_core)
                marker_ok = FORMAL_ASSERTION_MARKERS["S05-05"] in process_text
                if is_core:
                    marker_ok = marker_ok and "S05_EVIDENCE unknown_task_audit=true agent_journal_unknown=true verified=true" in process_text
                process_status = "PASS" if process_code == 0 and marker_ok and process_manifest.get("verified") else "FAIL"
                if process_status == "FAIL":
                    any_failure = True
                process_runs[name] = {
                    "status": process_status, "exit_code": process_code, "command": process_command,
                    "evidence": str(process_log.relative_to(ROOT)), "fixture_manifest": process_manifest,
                    "engine_version": engines[engine][0], "required_marker": FORMAL_ASSERTION_MARKERS["S05-05"],
                    "unknown_audit_and_agent_journal": ("S05_EVIDENCE unknown_task_audit=true agent_journal_unknown=true verified=true" in process_text) if is_core else None,
                }
            recovery_by_attempt[attempt][engine] = process_runs

    tests = report["tests"]
    for case in CASES:
        case_id = case["id"]
        record = tests[case_id]
        if case_id in FORMAL_INTEGRATION_CASES:
            for attempt in range(1, args.repeat + 1):
                engine_runs = integration_by_attempt[attempt]
                recovery_runs = recovery_by_attempt[attempt]
                stream_runs = stream_by_attempt[attempt]
                if case_id == "S05-05":
                    passed = all(
                        recovery_runs[number]["agent_sigkill"]["status"] == "PASS" and
                        recovery_runs[number]["core_sigkill"]["status"] == "PASS"
                        for number in (28, 29)
                    )
                    status = "PASS" if passed else "FAIL"
                    evidence = {
                        str(number): {
                            name: recovery_runs[number][name]["evidence"]
                            for name in ("agent_sigkill", "core_sigkill")
                        } for number in (28, 29)
                    }
                elif case_id == "S05-12":
                    passed = True
                    for number in (28, 29):
                        primary = engine_runs[number]
                        core_recovery = recovery_runs[number]["core_sigkill"]
                        passed = passed and primary["status"] == "PASS"
                        passed = passed and primary["supplemental_assertions"]["success_task_audit"]
                        passed = passed and primary["supplemental_assertions"]["failed_task_audit_redacted"]
                        passed = passed and core_recovery["status"] == "PASS"
                        passed = passed and core_recovery["unknown_audit_and_agent_journal"] is True
                    status = "PASS" if passed else "FAIL"
                    evidence = {
                        str(number): {
                            "successful_and_failed_task_audit": engine_runs[number]["evidence"],
                            "unknown_and_agent_journal_audit": recovery_runs[number]["core_sigkill"]["evidence"],
                        } for number in (28, 29)
                    }
                elif case_id in {"S05-09", "S05-10"}:
                    marker_pass = all(stream_runs[number]["case_markers"].get(case_id, False) for number in (28, 29))
                    status = "PASS" if all(stream_runs[number]["status"] == "PASS" for number in (28, 29)) and marker_pass else "FAIL"
                    evidence = {str(number): stream_runs[number]["evidence"] for number in (28, 29)}
                else:
                    marker_pass = all(engine_runs[number]["case_markers"].get(case_id, False) for number in (28, 29))
                    status = "PASS" if all(engine_runs[number]["status"] == "PASS" for number in (28, 29)) and marker_pass else "FAIL"
                    evidence = {str(number): engine_runs[number]["evidence"] for number in (28, 29)}
                    if case_id == "S05-07" and confirmation_by_attempt[attempt]["status"] != "PASS":
                        status = "FAIL"
                    if case_id == "S05-07":
                        evidence["delete_confirmation_api"] = confirmation_by_attempt[attempt]["evidence"]
                record["runs"].append({"attempt": attempt, "status": status, "engine_runs": evidence,
                                        "reason": "" if status == "PASS" else "At least one required per-engine real integration or delete-confirmation test failed."})
            statuses = [item["status"] for item in record["runs"]]
            if statuses and all(value == "PASS" for value in statuses) and args.mode == "full":
                record["status"] = "PASS"
                record["reason"] = "The exact acceptance assertion passed on both locked owned Engines 28 and 29 in this run."
            elif "FAIL" in statuses:
                record["status"] = "FAIL"
                record["reason"] = "At least one final-code real integration attempt failed."
            else:
                record["status"] = "NOT_READY"
                record["reason"] = "The complete two-engine acceptance requirement was not met."
        else:
            reason = CURRENT_CORE_GAPS.get(case_id, "No full-scope integrated proof is currently mapped to this normative acceptance case.")
            record["reason"] = reason
            record["runs"] = [{"attempt": attempt, "status": "NOT_READY", "reason": reason}
                              for attempt in range(1, args.repeat + 1)]

    for supplemental_id, assertion_key, description in (
        ("S05-11-CORE-DB-FAILURE", "core_task_insert_failure_without_docker_mutation",
         "Real Core API rejected a task on a targeted SQLite INSERT failure, leaving task rows, target StartedAt, process start count, and Engine events unchanged."),
        ("S05-11-AGENT-JOURNAL-FAILURE", "agent_task_journal_failure_zero_mutation",
         "Real Core+Agent+Engine injection rejected Agent journal INSERT; Core retained the uncertain durable task/claim and unknown audit, Agent journal stayed empty, and Docker StartedAt/start count/events stayed unchanged across a newer synchronized connection."),
        ("S05-12-FAILED-TASK-AUDIT", "failed_task_audit_redacted",
         "Real running-container deletion failed safely and its authenticated audit history contained no request identity material."),
    ):
        for attempt in range(1, args.repeat + 1):
            engine_runs = integration_by_attempt[attempt]
            passed = all(engine_runs[number]["supplemental_assertions"][assertion_key] for number in (28, 29))
            attempt_status = "PASS" if passed and all(engine_runs[number]["status"] == "PASS" for number in (28, 29)) else "FAIL"
            report["supplemental_checks"].append({
                "id": supplemental_id, "description": description, "attempt": attempt,
                "status": attempt_status,
                "engine_runs": {str(number): engine_runs[number]["evidence"] for number in (28, 29)},
            })

    for attempt in range(1, args.repeat + 1):
        engine_runs = cancel_offline_by_attempt[attempt]
        passed = all(engine_runs[number]["status"] == "PASS" for number in (28, 29))
        report["supplemental_checks"].append({
            "id": "S05-06-QUEUED-CANCEL-OFFLINE", "attempt": attempt,
            "description": "Negotiated Agent capacity is filled behind a pre-mutation Engine barrier; a genuinely undelivered Core task is canceled twice with not_dispatched evidence and no Docker change. After the real Agent lease expires, a new write returns 503 with no Core claim/audit/task or Agent journal row, and reconnect does not replay it.",
            "status": "PASS" if passed else "FAIL",
            "marker": FORMAL_SUPPLEMENTAL_MARKERS["S05-06-QUEUED-CANCEL-OFFLINE"],
            "engine_runs": {str(number): engine_runs[number] for number in (28, 29)},
        })

    statuses = [tests[case["id"]]["status"] for case in CASES]
    if any(value == "FAIL" for value in statuses) or any(item["status"] == "FAIL" for item in report["supplemental_checks"]):
        report["status"] = "FAIL"
        report["verification_status"] = "FAIL"
        report["reason"] = "A current S05 integration or supplemental browser check failed; the failed attempt is preserved."
    elif args.mode == "full" and all(value == "PASS" for value in statuses):
        report["status"] = "PASS"
        report["verification_status"] = "PASS"
        report["reason"] = "All original S05-01 through S05-12 cases passed in this final-code run on both locked Engines."
    else:
        report["status"] = "NOT_READY"
        report["verification_status"] = "NOT_READY"
        report["reason"] = "Some original S05 cases lack full integrated coverage; component PASS does not replace those gates."
    report["updated_at"] = now()
    write_report(report)
    print(f"S05 {report['status']}: report={REPORT.relative_to(ROOT)} evidence={EVIDENCE_ROOT.relative_to(ROOT)}", flush=True)
    return 0 if report["verification_status"] == "PASS" else 1


if __name__ == "__main__":
    raise SystemExit(main())
