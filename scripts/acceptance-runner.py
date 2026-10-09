#!/usr/bin/env python3
"""Re-run each stage's current acceptance; never trusts historical PASS data."""
import datetime as dt
import json
import pathlib
import re
import subprocess
import sys
import uuid

ROOT = pathlib.Path(__file__).resolve().parents[1]
REGISTRY = json.loads((ROOT / "tests/registry.json").read_text())
REPORTS = ROOT / "reports" / "stages"
STATUS_FILE = ROOT / "reports" / "status.json"

statuses = {}
run_id = dt.datetime.now(dt.timezone.utc).strftime("%Y%m%dT%H%M%SZ")
for stage in REGISTRY["stages"]:
    sid = stage["id"]
    if not re.fullmatch(r"S(?:0[0-9]|1[0-7])", sid):
        raise SystemExit(f"registry contains unsafe stage ID: {sid!r}")
    command = [sys.executable, "scripts/stage-runner.py", "--stage", sid, "--mode", "full"]
    print("+", " ".join(command), flush=True)
    started_at = dt.datetime.now(dt.timezone.utc)
    result = subprocess.run(command, cwd=ROOT)
    report_path = REPORTS / f"{sid}.json"
    if not report_path.is_file():
        statuses[sid] = {"status": "FAIL", "reason": "stage runner did not write a report"}
        continue
    report = json.loads(report_path.read_text())
    expected_ids = [test["id"] for test in stage.get("tests", []) + stage.get("supplemental_tests", [])]
    actual_ids = list(report.get("tests", {}))
    updated_at = dt.datetime.fromisoformat(report.get("updated_at", "1970-01-01T00:00:00+00:00"))
    if report.get("run_id") in (None, "initial-unexecuted") or updated_at < started_at:
        statuses[sid] = {"status": "FAIL", "reason": "stage report is stale or has no fresh run identity"}
    elif set(actual_ids) != set(expected_ids) or len(actual_ids) != len(expected_ids):
        statuses[sid] = {"status": "FAIL", "reason": "stage report does not list every original required case"}
    else:
        case_errors = []
        for case_id in expected_ids:
            case = report["tests"][case_id]
            if case.get("status") not in {"PASS", "FAIL", "NOT_READY"} or not str(case.get("reason", "")).strip():
                case_errors.append(f"{case_id} lacks a valid result or reason")
                continue
            attempts = case.get("runs", [])
            if report.get("status") == "PASS":
                if case.get("status") != "PASS" or [item.get("attempt") for item in attempts] != [1, 2, 3] or any(item.get("status") != "PASS" for item in attempts):
                    case_errors.append(f"{case_id} is not PASS in all three current runs")
            elif sid == "S00" and report.get("mode") == "full" and case.get("status") != "PASS" and case.get("status") != "FAIL" and case.get("status") != "NOT_READY":
                case_errors.append(f"{case_id} has invalid S00 full-run status")
            elif sid == "S01" and report.get("mode") == "full":
                case_status = case.get("status")
                if case_status not in {"PASS", "FAIL", "NOT_READY"}:
                    case_errors.append(f"{case_id} has invalid S01 full-run status")
                elif case_status == "PASS" and ([item.get("attempt") for item in attempts] != [1, 2, 3] or any(item.get("status") != "PASS" for item in attempts)):
                    case_errors.append(f"{case_id} is PASS without three current executions")
                elif case_status in {"FAIL", "NOT_READY"} and attempts and any(item.get("status") not in {"PASS", "FAIL", "NOT_READY"} for item in attempts):
                    case_errors.append(f"{case_id} has an invalid S01 run status")
            elif sid == "S03":
                case_status = case.get("status")
                repeat_requested = report.get("repeat_requested")
                if case_status not in {"PASS", "FAIL", "NOT_READY"}:
                    case_errors.append(f"{case_id} has an invalid S03 status")
                elif not isinstance(repeat_requested, int) or not 1 <= repeat_requested <= 3:
                    case_errors.append("S03 report has an invalid repeat_requested value")
                elif [item.get("attempt") for item in attempts] != list(range(1, repeat_requested + 1)):
                    case_errors.append(f"{case_id} does not list every current S03 attempt")
                elif any(item.get("status") not in {"PASS", "FAIL", "NOT_READY"} for item in attempts):
                    case_errors.append(f"{case_id} has an invalid S03 attempt status")
                elif case_status == "PASS" and (repeat_requested != 3 or any(item.get("status") != "PASS" for item in attempts)):
                    case_errors.append(f"{case_id} is PASS without three current consecutive executions")
                elif case_status == "NOT_READY" and any(item.get("status") == "PASS" for item in attempts):
                    case_errors.append(f"{case_id} hides a current PASS execution behind NOT_READY")
            elif sid != "S00" and (case.get("status") != "NOT_READY" or attempts):
                if sid != "S01":
                    case_errors.append(f"{case_id} has an impossible non-S00/S01 result in the current implementation milestone")
        if case_errors:
            statuses[sid] = {"status": "FAIL", "reason": "; ".join(case_errors)}
        elif result.returncode == 0 and report.get("status") != "PASS":
            statuses[sid] = {"status": "FAIL", "reason": "runner returned success without a complete PASS report"}
        elif report.get("status") not in {"PASS", "FAIL", "NOT_READY"}:
            statuses[sid] = {"status": "FAIL", "reason": "invalid report status"}
        elif result.returncode != 0 and report.get("status") == "PASS":
            statuses[sid] = {"status": "FAIL", "reason": "runner returned nonzero despite a PASS report"}
        else:
            statuses[sid] = {"status": report["status"], "reason": report.get("reason", "")}

all_pass = len(statuses) == len(REGISTRY["stages"]) and all(item["status"] == "PASS" for item in statuses.values())
any_fail = any(item["status"] == "FAIL" for item in statuses.values())
summary = {
    "schema": 1, "run_id": run_id + "-" + uuid.uuid4().hex[:8], "updated_at": dt.datetime.now(dt.timezone.utc).isoformat(),
    "status": "PASS" if all_pass else "FAIL" if any_fail else "NOT_READY",
    "reason": "All original stage suites passed fresh three-run acceptance." if all_pass else
              "One or more stages are incomplete, failed, or lack executable acceptance checks.",
    "stages": statuses,
}
STATUS_FILE.parent.mkdir(parents=True, exist_ok=True)
STATUS_FILE.write_text(json.dumps(summary, ensure_ascii=False, indent=2) + "\n")
print(f"NodeDance complete acceptance: {summary['status']}")
for sid, result in statuses.items():
    print(f"{sid}: {result['status']}{': ' + result['reason'] if result['reason'] else ''}")
raise SystemExit(0 if all_pass else 1)
