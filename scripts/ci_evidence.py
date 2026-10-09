#!/usr/bin/env python3
"""Create fresh, fail-closed stage evidence for each GitHub Actions job."""
from __future__ import annotations

import argparse
import datetime as dt
import json
import os
import pathlib
import re
import sys

ROOT = pathlib.Path(__file__).resolve().parents[1]
REGISTRY_PATH = ROOT / "tests/registry.json"
REPORT_PATH = ROOT / "reports/stages/S00.json"
STATUS_PATH = ROOT / "reports/status.json"
MARKER_PATH = ROOT / ".artifacts/ci-evidence/current-job.json"


def report_path_for(stage):
    return ROOT / f"reports/stages/{stage}.json"


def marker_path_for(stage):
    # Keep the original S00 marker location stable for existing jobs and
    # isolate every later stage so parallel jobs cannot invalidate each other.
    if stage == "S00":
        return MARKER_PATH
    return ROOT / f".artifacts/ci-evidence/{stage}/current-job.json"


def now():
    return dt.datetime.now(dt.timezone.utc).isoformat()


def metadata_from_values(*, run_id, run_attempt, sha, job, runner, engine,
                         stage="S00"):
    values = {
        "run_id": str(run_id),
        "run_attempt": str(run_attempt),
        "sha": str(sha),
        "job": str(job),
        "runner": str(runner),
        "engine": str(engine),
    }
    if any(not value.strip() for value in values.values()):
        raise ValueError("all GitHub Actions run metadata fields are required")
    runner_key = re.sub(r"[^A-Za-z0-9_.-]+", "_", values["runner"])
    job_key = re.sub(r"[^A-Za-z0-9_.-]+", "_", values["job"])
    stage_prefix = "" if stage == "S00" else f"{stage}-"
    values["job_key"] = stage_prefix + (
        f"{values['run_id']}-attempt-{values['run_attempt']}-"
        f"{job_key}-{runner_key}-engine-{values['engine']}"
    )
    values["provider"] = "github-actions"
    if stage != "S00":
        values["stage"] = stage
    values["initialized_at"] = now()
    return values


def metadata_from_environment(stage="S00"):
    return metadata_from_values(
        run_id=os.environ.get("GITHUB_RUN_ID", ""),
        run_attempt=os.environ.get("GITHUB_RUN_ATTEMPT", ""),
        sha=os.environ.get("GITHUB_SHA", ""),
        job=os.environ.get("GITHUB_JOB", ""),
        runner=os.environ.get("NODEDANCE_TEST_RUNNER", ""),
        engine=os.environ.get("NODEDANCE_TEST_ENGINE", "") or ("not-applicable" if stage != "S00" else ""),
        stage=stage,
    )


def read_json(path, fallback):
    try:
        value = json.loads(path.read_text())
        return value if isinstance(value, dict) else fallback
    except (OSError, json.JSONDecodeError):
        return fallback


def write_json(path, value):
    path.parent.mkdir(parents=True, exist_ok=True)
    temporary = path.with_name(path.name + ".tmp")
    temporary.write_text(json.dumps(value, ensure_ascii=False, indent=2) + "\n")
    temporary.replace(path)


def empty_summary(registry_path=REGISTRY_PATH):
    registry = json.loads(pathlib.Path(registry_path).read_text())
    return {
        "schema": 1,
        "stages": {
            stage["id"]: {
                "status": "NOT_READY",
                "reason": "This workflow job has not executed this stage.",
            }
            for stage in registry["stages"]
        },
    }


def stage_cases(registry_path=REGISTRY_PATH, stage="S00"):
    registry = json.loads(pathlib.Path(registry_path).read_text())
    stage_entry = next(item for item in registry["stages"] if item["id"] == stage)
    return stage_entry.get("tests", []) + stage_entry.get("supplemental_tests", [])


def not_ready_report(metadata, registry_path=REGISTRY_PATH, stage="S00"):
    reason = (
        f"GitHub Actions run {metadata['run_id']} attempt "
        f"{metadata['run_attempt']} job {metadata['job_key']} at "
        f"{metadata['sha']} was initialized; {stage} acceptance has not completed."
    )
    cases = stage_cases(registry_path, stage)
    return {
        "schema": 1,
        "stage": stage,
        "mode": "ci-full",
        "run_id": "ci-" + metadata["job_key"],
        "updated_at": now(),
        "status": "NOT_READY",
        "repeat_required": 1,
        "repeat_requested": 1,
        "verification_status": "NOT_READY",
        "reason": reason,
        "ci": metadata,
        "tests": {
            case["id"]: {
                "status": "NOT_READY",
                "action": case["action"],
                "expected": case["expected"],
                "environment": case["environment"],
                "evidence_required": case["evidence"],
                "runs": [],
                "reason": reason,
            }
            for case in cases
        },
    }


def update_summary(status_path, report, metadata, stage="S00"):
    summary = read_json(status_path, empty_summary())
    stages = summary.setdefault("stages", {})
    stages[stage] = {
        "status": report["status"],
        "mode": report["mode"],
        "run_id": report["run_id"],
        "updated_at": report["updated_at"],
        "reason": report.get("reason", ""),
        "ci": metadata,
    }
    summary.setdefault("ci_runs", {})[stage] = metadata
    summary["updated_at"] = now()
    write_json(status_path, summary)


def initialize(report_path=REPORT_PATH, status_path=STATUS_PATH,
               marker_path=MARKER_PATH, registry_path=REGISTRY_PATH,
               metadata=None, stage="S00"):
    if metadata is None:
        metadata = metadata_from_environment(stage)
    if report_path == REPORT_PATH and stage != "S00":
        report_path = report_path_for(stage)
    if marker_path == MARKER_PATH and stage != "S00":
        marker_path = marker_path_for(stage)
    report = not_ready_report(metadata, registry_path, stage)
    write_json(pathlib.Path(report_path), report)
    update_summary(pathlib.Path(status_path), report, metadata, stage)
    write_json(pathlib.Path(marker_path), metadata)
    return report


def report_is_current(report, marker, registry_path=REGISTRY_PATH, stage="S00"):
    if report.get("stage") != stage or not report.get("updated_at"):
        return False
    try:
        updated_at = dt.datetime.fromisoformat(report["updated_at"])
        initialized_at = dt.datetime.fromisoformat(marker["initialized_at"])
    except (KeyError, TypeError, ValueError):
        return False
    if updated_at < initialized_at:
        return False

    job_key = marker.get("job_key")
    initial_run_id = "ci-" + str(job_key)
    if (report.get("run_id") == initial_run_id
            and report.get("status") == "NOT_READY"
            and report.get("ci", {}).get("job_key") == job_key):
        return True

    expected_ids = {case["id"] for case in stage_cases(registry_path, stage)}
    cases = report.get("tests", {})
    if (report.get("mode") != "full"
            or report.get("run_id") in (None, "initial-unexecuted", initial_run_id)
            or set(cases) != expected_ids
            or report.get("status") not in {"PASS", "FAIL", "NOT_READY"}):
        return False
    if report["status"] == "PASS":
        return all(
            case.get("status") == "PASS"
            and [run.get("attempt") for run in case.get("runs", [])] == [1]
            and all(run.get("status") == "PASS" for run in case.get("runs", []))
            for case in cases.values()
        )
    return True


def annotate(report_path=REPORT_PATH, status_path=STATUS_PATH,
             marker_path=MARKER_PATH, registry_path=REGISTRY_PATH,
             metadata=None, stage="S00"):
    if metadata is None:
        metadata = metadata_from_environment(stage)
    if report_path == REPORT_PATH and stage != "S00":
        report_path = report_path_for(stage)
    if marker_path == MARKER_PATH and stage != "S00":
        marker_path = marker_path_for(stage)
    marker_path = pathlib.Path(marker_path)
    marker = read_json(marker_path, {})
    report_path = pathlib.Path(report_path)
    report = read_json(report_path, {})
    if (marker.get("job_key") != metadata["job_key"]
            or not report_is_current(report, marker, registry_path, stage)):
        # Fail closed if initialization was skipped or belonged to another run.
        report = initialize(report_path, status_path, marker_path,
                            registry_path, metadata, stage)
    else:
        metadata = dict(metadata)
        metadata["initialized_at"] = marker.get("initialized_at", metadata["initialized_at"])
        report["ci"] = metadata
        write_json(report_path, report)
        update_summary(pathlib.Path(status_path), report, metadata, stage)
    return report


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("command", choices=("initialize", "annotate"))
    registry = json.loads(REGISTRY_PATH.read_text())
    parser.add_argument("--stage", choices=tuple(item["id"] for item in registry["stages"]), default="S00")
    args = parser.parse_args()
    try:
        metadata = metadata_from_environment(args.stage)
        if args.command == "initialize":
            report = initialize(metadata=metadata, stage=args.stage)
        else:
            report = annotate(metadata=metadata, stage=args.stage)
    except (OSError, ValueError, KeyError, StopIteration, json.JSONDecodeError) as error:
        print(f"CI evidence {args.command} failed: {error}", file=sys.stderr)
        return 1
    print(f"{args.stage} CI report {args.command}: {report['status']} ({report['ci']['job_key']})")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
