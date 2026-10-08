#!/usr/bin/env python3
"""Run fresh stage checks; reports never allow a previous PASS to skip work."""
import argparse
import datetime as dt
import json
import pathlib
import re
import subprocess
import sys
import uuid

ROOT = pathlib.Path(__file__).resolve().parents[1]
REGISTRY_PATH = ROOT / "tests/registry.json"
REGISTRY = json.loads(REGISTRY_PATH.read_text())
REPORTS = ROOT / "reports" / "stages"
STATUS_FILE = ROOT / "reports" / "status.json"


def timestamp():
    return dt.datetime.now(dt.timezone.utc).isoformat()


def stage_tests(stage):
    item = next(item for item in REGISTRY["stages"] if item["id"] == stage)
    return item.get("tests", []) + item.get("supplemental_tests", [])


def save_report(report):
    REPORTS.mkdir(parents=True, exist_ok=True)
    path = REPORTS / f"{report['stage']}.json"
    path.write_text(json.dumps(report, ensure_ascii=False, indent=2) + "\n")
    if STATUS_FILE.exists():
        overall = json.loads(STATUS_FILE.read_text())
    else:
        overall = {"schema": 1, "stages": {}}
    overall.setdefault("stages", {})[report["stage"]] = {
        "status": report["status"], "mode": report["mode"],
        "run_id": report["run_id"], "updated_at": report["updated_at"],
        "reason": report.get("reason", ""),
    }
    overall["updated_at"] = report["updated_at"]
    STATUS_FILE.parent.mkdir(parents=True, exist_ok=True)
    STATUS_FILE.write_text(json.dumps(overall, ensure_ascii=False, indent=2) + "\n")
    return path


def not_ready_report(stage, mode, reason):
    cases = stage_tests(stage)
    report = {
        "schema": 1, "stage": stage, "mode": mode, "status": "NOT_READY",
        "run_id": uuid.uuid4().hex, "updated_at": timestamp(), "reason": reason,
        "tests": {
            case["id"]: {
                "status": "NOT_READY", "action": case["action"],
                "expected": case["expected"], "environment": case["environment"],
                "evidence_required": case["evidence"], "runs": [], "reason": reason,
            }
            for case in cases
        },
    }
    path = save_report(report)
    print(f"{stage}: NOT_READY ({len(cases)} required cases listed individually); report={path.relative_to(ROOT)}", file=sys.stderr)
    return 2


def preflight(stage, mode):
    run_id = uuid.uuid4().hex
    log_dir = ROOT / ".artifacts" / "stage-runs" / run_id
    log_dir.mkdir(parents=True, exist_ok=True)
    commands = [["make", "check"], ["make", "build"]]
    for index, command in enumerate(commands, start=1):
        log_path = log_dir / f"preflight-{index}.log"
        with log_path.open("w") as stream:
            stream.write("$ " + " ".join(command) + "\n")
            stream.flush()
            result = subprocess.run(command, cwd=ROOT, stdout=stream, stderr=subprocess.STDOUT)
        print(f"preflight {index}/{len(commands)} {'PASS' if result.returncode == 0 else 'FAIL'}: {log_path.relative_to(ROOT)}", flush=True)
        if result.returncode:
            reason = f"required preflight command failed: {' '.join(command)}; see {log_path.relative_to(ROOT)}"
            report = {
                "schema": 1, "stage": stage, "mode": mode, "status": "FAIL",
                "run_id": run_id, "updated_at": timestamp(), "reason": reason,
                "tests": {
                    case["id"]: {
                        "status": "FAIL", "action": case["action"],
                        "expected": case["expected"], "environment": case["environment"],
                        "evidence_required": case["evidence"], "runs": [],
                        "reason": "not run because a required stage preflight failed",
                    }
                    for case in stage_tests(stage)
                },
                "preflight": {"command": command, "exit_code": result.returncode,
                              "log": str(log_path.relative_to(ROOT))},
            }
            path = save_report(report)
            print(f"{stage}: FAIL; no test was reported PASS; report={path.relative_to(ROOT)}", file=sys.stderr)
            return result.returncode
    return 0


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--stage", required=True)
    parser.add_argument("--mode", choices=("full", "integration", "e2e"), required=True)
    args = parser.parse_args()
    if not re.fullmatch(r"S(?:0[0-9]|1[0-7])", args.stage):
        parser.error("--stage must be one of S00 through S17")
    if args.stage not in {stage["id"] for stage in REGISTRY["stages"]}:
        parser.error(f"{args.stage} is absent from tests/registry.json")

    if args.stage not in {"S00", "S01", "S02"}:
        return not_ready_report(
            args.stage, args.mode,
            f"{args.stage} is outside the S00 implementation milestone; its implementation and executable acceptance checks are not present. This report lists every original case individually. A previous report cannot satisfy a current run.",
        )

    if args.mode == "full" and (code := preflight(args.stage, args.mode)):
        return code
    if args.mode != "full":
        if code := preflight(args.stage, args.mode):
            return code
    repeat = 3 if args.mode == "full" else 1
    acceptance = {
        "S00": "scripts/acceptance-s00.py",
        "S01": "scripts/acceptance-s01.py",
        "S02": "scripts/acceptance-s02.py",
    }[args.stage]
    command = [sys.executable, acceptance, "--mode", args.mode, "--repeat", str(repeat)]
    return subprocess.run(command, cwd=ROOT).returncode


if __name__ == "__main__":
    raise SystemExit(main())
