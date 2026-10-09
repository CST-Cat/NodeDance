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

from acceptance_candidates import CANDIDATE_TARGETS

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
    previous_path = REPORTS / f"{stage}.json"
    previous = json.loads(previous_path.read_text()) if previous_path.is_file() else None
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
    if previous:
        report["historical_stage_report"] = previous.get("historical_stage_report", previous)
        report["historical_evidence_note"] = (
            "The previous detailed stage report is preserved verbatim as historical evidence. Current normative cases above remain NOT_READY until their required evidence is recorded by a current runner."
        )
    path = save_report(report)
    print(f"{stage}: NOT_READY ({len(cases)} required cases listed individually); report={path.relative_to(ROOT)}", file=sys.stderr)
    return 2


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--stage", required=True)
    parser.add_argument("--mode", choices=("full", "integration", "e2e"), required=True)
    args = parser.parse_args()
    if not re.fullmatch(r"S(?:0[0-9]|1[0-7])", args.stage):
        parser.error("--stage must be one of S00 through S17")
    if args.stage not in {stage["id"] for stage in REGISTRY["stages"]}:
        parser.error(f"{args.stage} is absent from tests/registry.json")

    if args.stage in CANDIDATE_TARGETS:
        command = [sys.executable, "scripts/acceptance-candidates.py", "--stage", args.stage, "--mode", args.mode]
        return subprocess.run(command, cwd=ROOT).returncode

    repeat = 1
    if args.stage == "S08":
        command = [sys.executable, "scripts/acceptance-s08.py", "--mode", args.mode, "--repeat", str(repeat)]
        return subprocess.run(command, cwd=ROOT).returncode
    if args.stage == "S05":
        command = [sys.executable, "scripts/acceptance-s05.py", "--mode", args.mode, "--repeat", str(repeat)]
        return subprocess.run(command, cwd=ROOT).returncode
    if args.stage == "S10":
        command = [sys.executable, "scripts/acceptance-s10.py", "--mode", args.mode, "--repeat", str(repeat)]
        return subprocess.run(command, cwd=ROOT).returncode
    acceptance = {
        "S00": "scripts/acceptance-s00.py",
        "S01": "scripts/acceptance-s01.py",
        "S02": "scripts/acceptance-s02.py",
        "S03": "scripts/acceptance-s03.py",
        "S04": "scripts/acceptance-s04.py",
    }.get(args.stage)
    if acceptance is None:
        return not_ready_report(
            args.stage, args.mode,
            f"{args.stage} has no runnable normative acceptance suite in this checkout. This report lists every original case individually; partial implementation or component checks do not satisfy the stage, and previous runs cannot satisfy a current run.",
        )
    command = [sys.executable, acceptance, "--mode", args.mode, "--repeat", str(repeat)]
    return subprocess.run(command, cwd=ROOT).returncode


if __name__ == "__main__":
    raise SystemExit(main())
