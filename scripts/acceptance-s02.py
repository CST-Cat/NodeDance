#!/usr/bin/env python3
"""Run S02 real Core/Agent acceptance and keep evidence for each case."""

import datetime as dt
import json
import os
import pathlib
import shutil
import subprocess
import sys
import uuid

ROOT = pathlib.Path(__file__).resolve().parents[1]
REGISTRY = json.loads((ROOT / "tests/registry.json").read_text())
STAGE = next(stage for stage in REGISTRY["stages"] if stage["id"] == "S02")
CASES = STAGE.get("tests", []) + STAGE.get("supplemental_tests", [])
CASE_IDS = {case["id"] for case in CASES}
REPORT = ROOT / "reports/stages/S02.json"
STATUSES = ROOT / "reports/status.json"
RUN_ID = dt.datetime.now(dt.timezone.utc).strftime("%Y%m%dT%H%M%SZ") + "-" + uuid.uuid4().hex[:8]
ATTEMPT_ROOT = ROOT / ".artifacts/logs/acceptance-s02" / RUN_ID


def timestamp():
    return dt.datetime.now(dt.timezone.utc).isoformat()


def run_json(command, log_path, env=None, case_logs=None):
    """Run a command, store raw JSON events and split named S02 subtests."""
    case_logs = case_logs if case_logs is not None else {}
    return_codes = None
    seen = {}
    handles = {}
    log_path.parent.mkdir(parents=True, exist_ok=True)
    with log_path.open("w", encoding="utf-8") as raw:
        raw.write("$ " + " ".join(str(part) for part in command) + "\n")
        raw.flush()
        process = subprocess.Popen(command, cwd=ROOT, env=env, stdout=subprocess.PIPE,
                                   stderr=subprocess.STDOUT, text=True, bufsize=1)
        assert process.stdout is not None
        for line in process.stdout:
            raw.write(line)
            raw.flush()
            try:
                event = json.loads(line)
            except json.JSONDecodeError:
                continue
            test_name = event.get("Test", "")
            if not test_name:
                continue
            case_id = test_name.rsplit("/", 1)[-1]
            if case_id not in CASE_IDS:
                continue
            seen.setdefault(case_id, "NOT_READY")
            case_log_path = case_logs.get(case_id)
            if case_log_path is not None:
                if case_id not in handles:
                    case_log_path.parent.mkdir(parents=True, exist_ok=True)
                    handles[case_id] = case_log_path.open("w", encoding="utf-8")
                    handles[case_id].write("$ named subtest: " + test_name + "\n")
                handles[case_id].write(line)
                handles[case_id].flush()
            action = event.get("Action")
            if action == "pass":
                seen[case_id] = "PASS"
            elif action == "fail":
                seen[case_id] = "FAIL"
            elif action == "skip":
                seen[case_id] = "NOT_READY"
        return_codes = process.wait()
    for handle in handles.values():
        handle.close()
    return return_codes, seen


def copy_case_log(source, destination, note):
    destination.parent.mkdir(parents=True, exist_ok=True)
    with destination.open("w", encoding="utf-8") as output:
        output.write(note.rstrip() + "\n")
        if source.exists():
            output.write(source.read_text())


def systemd_command(attempt_dir):
    if os.environ.get("NODEDANCE_SYSTEMD_ACCEPTANCE") != "1":
        return None, None
    go = shutil.which("go")
    if not go:
        return None, "locked Go executable is unavailable"
    env = os.environ.copy()
    env["GOTOOLCHAIN"] = "local"
    env["NODEDANCE_TEST_SYSTEMD"] = "1"
    agent_binary = (ROOT / ".build/nodedance-agent").resolve()
    env["NODEDANCE_TEST_AGENT_BINARY"] = str(agent_binary)
    tests = "^(TestSystemd(ExecStartUsesLiteralSpecialPathsOnRunningManager|InstallRejectsConfigOwnedByDifferentUser)|TestSystemdAgentInstallConnectRestart)$"
    command = [go, "test", "-json", "-count=1", "./internal/agent", "./internal/core/server", "-run", tests]
    if os.geteuid() != 0:
        sudo = shutil.which("sudo")
        if not sudo:
            return None, "sudo is unavailable for the isolated systemd manager test"
        command = [sudo, "--non-interactive", "env", f"PATH={env.get('PATH', '')}",
                   "GOTOOLCHAIN=local", "NODEDANCE_TEST_SYSTEMD=1",
                   f"NODEDANCE_TEST_AGENT_BINARY={agent_binary}", go, *command[1:]]
    return command, env


def make_case_record(case):
    return {
        "status": "NOT_READY",
        "action": case["action"],
        "expected": case["expected"],
        "environment": case["environment"],
        "evidence_required": case["evidence"],
        "runs": [],
        "reason": "No current S02 attempt has completed.",
    }


def save_report(report):
    REPORT.parent.mkdir(parents=True, exist_ok=True)
    REPORT.write_text(json.dumps(report, ensure_ascii=False, indent=2) + "\n")
    overall = json.loads(STATUSES.read_text()) if STATUSES.exists() else {"schema": 1, "stages": {}}
    overall.setdefault("stages", {})["S02"] = {
        "status": report["status"], "mode": report["mode"], "run_id": report["run_id"],
        "updated_at": report["updated_at"], "reason": report.get("reason", ""),
    }
    overall["updated_at"] = report["updated_at"]
    STATUSES.parent.mkdir(parents=True, exist_ok=True)
    STATUSES.write_text(json.dumps(overall, ensure_ascii=False, indent=2) + "\n")


def evaluate_results(case_statuses, process_exit_codes, race_pass, full_acceptance=False):
    """Fail the stage if the parent Go process failed after named tests passed."""
    process_failed = any(code != 0 for code in process_exit_codes)
    case_failed = any(status == "FAIL" for status in case_statuses)
    if process_failed or case_failed or not race_pass:
        return "FAIL", "FAIL"
    verification = "PASS" if case_statuses and all(status == "PASS" for status in case_statuses) else "NOT_READY"
    if full_acceptance and verification == "PASS":
        return "PASS", verification
    return "NOT_READY", verification


def case_status_for_run(case_id, seen, process_exit_code):
    """Classify an absent required subtest without confusing it with a pass."""
    if case_id in seen:
        return seen[case_id]
    return "FAIL" if process_exit_code else "NOT_READY"


def run_attempt(attempt, report, mode):
    attempt_dir = ATTEMPT_ROOT / f"attempt-{attempt}"
    attempt_dir.mkdir(parents=True, exist_ok=True, mode=0o700)
    all_case_logs = {case_id: attempt_dir / f"case-{case_id}.jsonl" for case_id in CASE_IDS}
    server_log = attempt_dir / "real-core-agent.jsonl"
    command = ["go", "test", "-json", "-count=1", "./internal/core/server", "-run", "^TestRealAgent"]
    code, seen = run_json(command, server_log, case_logs=all_case_logs)

    for case_id in CASE_IDS - {"S02-SUP-04", "S02-SUP-07"}:
        status = case_status_for_run(case_id, seen, code)
        reason = ""
        if status == "NOT_READY":
            reason = "The required named test was skipped or unavailable."
        elif status == "FAIL":
            reason = "The real Core/Agent test failed or did not reach this named assertion."
        report["tests"][case_id]["runs"].append({
            "attempt": attempt, "status": status,
            "evidence": str(all_case_logs[case_id].relative_to(ROOT)) if all_case_logs[case_id].exists() else str(server_log.relative_to(ROOT)),
            "reason": reason,
        })

    # SUP-07 is deliberately performed inside S02-01's three-node enrollment:
    # the first committed enrollment response is dropped and device-credential
    # recovery must succeed before the subtest can pass.
    source = all_case_logs["S02-01"]
    copy_case_log(source, all_case_logs["S02-SUP-07"], "S02-SUP-07 evidence is the first Agent enrollment assertion inside S02-01.")
    supplement_status = case_status_for_run("S02-01", seen, code)
    report["tests"]["S02-SUP-07"]["runs"].append({
        "attempt": attempt, "status": supplement_status,
        "evidence": str(all_case_logs["S02-SUP-07"].relative_to(ROOT)),
        "reason": "" if supplement_status == "PASS" else "Lost enrollment response recovery is part of the S02-01 three-Agent real API flow.",
    })

    sys_command, sys_env_or_reason = systemd_command(attempt_dir)
    systemd_id = "S02-SUP-04"
    systemd_log = attempt_dir / "systemd-manager.jsonl"
    systemd_case_logs = {systemd_id: all_case_logs[systemd_id]}
    if sys_command is None:
        status = "NOT_READY"
        reason = sys_env_or_reason or "NODEDANCE_SYSTEMD_ACCEPTANCE=1 is required to execute real systemd acceptance."
        systemd_log.write_text("NOT_RUN: " + reason + "\n")
    else:
        sys_env = sys_env_or_reason
        sys_env.update({"GOTOOLCHAIN": "local", "NODEDANCE_TEST_SYSTEMD": "1"})
        # The subprocess command is run with root only for this disposable CI
        # manager test. Normal Core/Agent acceptance stays unprivileged.
        status_code, sys_seen = run_json(sys_command, systemd_log, env=sys_env, case_logs=systemd_case_logs)
        required_systemd = {
            "TestSystemdExecStartUsesLiteralSpecialPathsOnRunningManager",
            "TestSystemdInstallRejectsConfigOwnedByDifferentUser",
            "TestSystemdAgentInstallConnectRestart",
        }
        # These package tests do not carry S02 names, so also inspect their JSON
        # events from the preserved raw log.
        passed = set()
        failed = set()
        for line in systemd_log.read_text().splitlines():
            try:
                event = json.loads(line)
            except json.JSONDecodeError:
                continue
            if event.get("Action") == "pass" and event.get("Test") in required_systemd:
                passed.add(event["Test"])
            elif event.get("Action") in {"fail", "skip"} and event.get("Test") in required_systemd:
                failed.add(event["Test"])
        if status_code == 0 and passed == required_systemd and not failed:
            status, reason = "PASS", ""
            all_case_logs[systemd_id].write_text("Actual systemd manager run and explicit-user ownership test passed.\n" + systemd_log.read_text())
        elif failed or status_code != 0:
            status, reason = "FAIL", "Actual systemd manager, service-user, or Core-connected Agent restart verification failed."
        else:
            status, reason = "NOT_READY", "Actual systemd manager tests were skipped or did not emit all required results."
    report["tests"][systemd_id]["runs"].append({
        "attempt": attempt, "status": status,
        "evidence": str((all_case_logs[systemd_id] if all_case_logs[systemd_id].exists() else systemd_log).relative_to(ROOT)),
        "reason": reason,
    })
    print(f"S02 attempt {attempt}: real Core/Agent go test exit={code}; systemd={status}", flush=True)
    return code


def run_race_check(report):
    evidence_dir = ATTEMPT_ROOT / "race"
    evidence_dir.mkdir(parents=True, exist_ok=True, mode=0o700)
    commands = [
        ["go", "test", "-race", "-count=1", "./internal/agent", "./internal/core/agents"],
        ["go", "test", "-race", "-count=1", "./internal/core/server", "-run", "^TestRealAgent"],
    ]
    results = []
    for index, command in enumerate(commands, start=1):
        path = evidence_dir / f"race-{index}.log"
        with path.open("w", encoding="utf-8") as log:
            log.write("$ " + " ".join(command) + "\n")
            log.flush()
            result = subprocess.run(command, cwd=ROOT, stdout=log, stderr=subprocess.STDOUT)
        results.append({"command": command, "status": "PASS" if result.returncode == 0 else "FAIL",
                        "exit_code": result.returncode, "evidence": str(path.relative_to(ROOT))})
        print(f"S02 race check {index}/{len(commands)} {'PASS' if result.returncode == 0 else 'FAIL'}: {path.relative_to(ROOT)}", flush=True)
        if result.returncode:
            break
    report["race_checks"] = results
    return all(item["status"] == "PASS" for item in results) and len(results) == len(commands)


def main():
    import argparse

    parser = argparse.ArgumentParser()
    parser.add_argument("--mode", choices=("full", "integration", "e2e"), required=True)
    parser.add_argument("--repeat", type=int, default=1)
    args = parser.parse_args()
    if args.repeat != 1:
        parser.error("acceptance runs once; rerun the affected suite after a failure or code change")

    report = {
        "schema": 1, "stage": "S02", "mode": args.mode,
        "run_id": RUN_ID, "updated_at": timestamp(), "status": "NOT_READY",
        "repeat_required": 1, "repeat_requested": args.repeat,
        "verification_status": "NOT_RUN", "tests": {case["id"]: make_case_record(case) for case in CASES},
        "evidence_root": str(ATTEMPT_ROOT.relative_to(ROOT)),
        "environment": {"go": subprocess.run(["go", "version"], cwd=ROOT, capture_output=True, text=True).stdout.strip(),
                        "goarch": os.uname().machine if hasattr(os, "uname") else "unknown",
                        "systemd_acceptance_enabled": os.environ.get("NODEDANCE_SYSTEMD_ACCEPTANCE") == "1"},
    }
    ATTEMPT_ROOT.mkdir(parents=True, exist_ok=True, mode=0o700)
    all_codes = []
    for attempt in range(1, args.repeat + 1):
        print(f"S02 real acceptance run {attempt}/{args.repeat} ({args.mode})", flush=True)
        all_codes.append(run_attempt(attempt, report, args.mode))
    race_pass = True
    if args.mode == "full":
        race_pass = run_race_check(report)

    for case in CASES:
        record = report["tests"][case["id"]]
        statuses = [item["status"] for item in record["runs"]]
        if len(statuses) == 1 and all(item == "PASS" for item in statuses):
            record["status"], record["reason"] = "PASS", "The current complete real acceptance run passed."
        elif "FAIL" in statuses:
            record["status"], record["reason"] = "FAIL", "At least one current S02 acceptance execution failed."
        else:
            record["status"], record["reason"] = "NOT_READY", "Required S02 evidence is missing, skipped, or incomplete."
    case_statuses = [report["tests"][case["id"]]["status"] for case in CASES]
    full_acceptance = args.mode == "full" and race_pass
    report["status"], report["verification_status"] = evaluate_results(
        case_statuses, all_codes, race_pass, full_acceptance=full_acceptance)
    if report["status"] == "PASS":
        report["reason"] = "All original and supplemental S02 cases passed in this complete real run; parent test processes exited successfully and race checks passed."
    elif report["status"] == "FAIL":
        report["reason"] = "At least one required case, parent test process, or race check failed; passing subtests do not override a failing parent process."
    else:
        report["reason"] = "A required case or systemd check is missing, skipped, or incomplete."
    report["updated_at"] = timestamp()
    save_report(report)
    print(f"S02 {report['status']}: report={REPORT.relative_to(ROOT)} evidence={report['evidence_root']}", flush=True)
    if report["verification_status"] != "PASS":
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
