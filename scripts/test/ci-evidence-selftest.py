#!/usr/bin/env python3
"""Prove CI reports fail closed and uploaded evidence paths stay bounded."""
import datetime as dt
import importlib.util
import json
import pathlib
import re
import tempfile

ROOT = pathlib.Path(__file__).resolve().parents[2]
MODULE_PATH = ROOT / "scripts/ci_evidence.py"
SPEC = importlib.util.spec_from_file_location("nodedance_ci_evidence", MODULE_PATH)
EVIDENCE = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(EVIDENCE)


def require(condition, message):
    if not condition:
        raise AssertionError(message)


def test_stale_pass_is_replaced_and_current_status_is_preserved():
    metadata = EVIDENCE.metadata_from_values(
        run_id="37796591007", run_attempt="2", sha="a" * 40,
        job="s00", runner="ubuntu-24.04-arm", engine="29",
    )
    cases = EVIDENCE.stage_cases()
    with tempfile.TemporaryDirectory(prefix="nodedance-ci-evidence-") as temporary:
        root = pathlib.Path(temporary)
        report_path = root / "reports/stages/S00.json"
        status_path = root / "reports/status.json"
        marker_path = root / ".artifacts/ci-evidence/current-job.json"
        old_pass = {
            "schema": 1, "stage": "S00", "mode": "full",
            "run_id": "20261008T144457Z-743ef84a",
            "updated_at": "2026-01-01T00:00:00+00:00", "status": "PASS",
            "tests": {case["id"]: {"status": "PASS", "runs": [
                {"attempt": attempt, "status": "PASS"} for attempt in (1, 2, 3)
            ]}
                      for case in cases},
        }
        report_path.parent.mkdir(parents=True, exist_ok=True)
        report_path.write_text(json.dumps(old_pass))
        status_path.parent.mkdir(parents=True, exist_ok=True)
        status_path.write_text(json.dumps({
            "schema": 1,
            "stages": {"S00": {"status": "PASS", "run_id": "old-pass"}},
        }))

        initialized = EVIDENCE.initialize(
            report_path, status_path, marker_path, metadata=metadata,
        )
        require(initialized["status"] == "NOT_READY", "old stage PASS survived initialization")
        require(initialized["ci"] == metadata, "initial report lacks current CI metadata")
        require(set(initialized["tests"]) == {case["id"] for case in cases},
                "initial report omitted an S00 required case")
        require(all(case["status"] == "NOT_READY" and case["runs"] == []
                    for case in initialized["tests"].values()),
                "initial report contains a prior PASS or test run")
        require(all(metadata[key] in initialized["reason"]
                    for key in ("run_id", "sha", "job_key")),
                "initial report reason does not identify this CI run")

        status_path.unlink()
        EVIDENCE.initialize(report_path, status_path, marker_path, metadata=metadata)
        summary_without_checkout_file = json.loads(status_path.read_text())
        expected_stages = {
            stage["id"] for stage in json.loads((ROOT / "tests/registry.json").read_text())["stages"]
        }
        require(set(summary_without_checkout_file["stages"]) == expected_stages,
                "fresh CI status summary does not list every stage")
        require(all(item["status"] == "NOT_READY"
                    for stage_id, item in summary_without_checkout_file["stages"].items()
                    if stage_id != "S00"),
                "fresh status summary inherited a previous stage result")

        report_path.write_text(json.dumps(old_pass))
        stale_annotated = EVIDENCE.annotate(
            report_path, status_path, marker_path, metadata=metadata,
        )
        require(stale_annotated["status"] == "NOT_READY",
                "a stale PASS was accepted despite a matching current-job marker")
        require(stale_annotated["run_id"] == "ci-" + metadata["job_key"],
                "stale PASS was not replaced by this job's initial report")

        fresh = dict(initialized)
        fresh.update({
            "run_id": "fresh-stage-run", "mode": "full", "status": "PASS",
            "updated_at": (dt.datetime.fromisoformat(metadata["initialized_at"])
                           + dt.timedelta(seconds=1)).isoformat(),
        })
        fresh["tests"] = {
            case["id"]: {"status": "PASS", "runs": [
                {"attempt": attempt, "status": "PASS"} for attempt in (1, 2, 3)
            ]}
            for case in cases
        }
        report_path.write_text(json.dumps(fresh))
        annotated = EVIDENCE.annotate(
            report_path, status_path, marker_path, metadata=metadata,
        )
        require(annotated["status"] == "PASS", "annotation changed a fresh PASS result")
        require(annotated["ci"] == metadata, "fresh report lacks CI run metadata")
        require(annotated["ci"]["initialized_at"] == metadata["initialized_at"],
                "annotation changed the original initialize timestamp")
        summary = json.loads(status_path.read_text())
        require(summary["stages"]["S00"]["status"] == "PASS",
                "summary did not preserve the fresh stage result")
        require(summary["stages"]["S00"]["ci"] == metadata,
                "summary lacks current CI metadata")

        # If initialization was skipped or failed, annotate must replace an old
        # checked-in PASS rather than attach new metadata to it.
        marker_path.unlink()
        report_path.write_text(json.dumps(old_pass))
        failed_closed = EVIDENCE.annotate(
            report_path, status_path, marker_path, metadata=metadata,
        )
        require(failed_closed["status"] == "NOT_READY",
                "annotation reused a prior PASS without a current-job marker")
        require(all(case["status"] == "NOT_READY" and case["runs"] == []
                    for case in failed_closed["tests"].values()),
                "fail-closed annotation retained stale test outcomes")


def test_workflow_upload_is_hidden_file_aware_and_allowlisted():
    workflow = (ROOT / ".github/workflows/ci.yml").read_text()
    positions = [workflow.index(value) for value in (
        "rm -f reports/stages/S00.json reports/status.json && python3 scripts/ci_evidence.py initialize",
        "run: make verify-tools",
        "run: make verify-ci-evidence",
        "run: make test-stage STAGE=S00",
        "scripts/ci_evidence.py annotate",
        "uses: actions/upload-artifact@",
    )]
    require(positions == sorted(positions), "workflow CI evidence steps are out of order")
    upload = workflow[positions[-1]:]
    require(re.search(r"^\s+include-hidden-files:\s+true\s*$", upload, re.M) is not None,
            "artifact upload does not include hidden .artifacts logs")
    lines = upload.splitlines()
    path_index = next((i for i, line in enumerate(lines)
                       if re.match(r"\s+path:\s*\|\s*$", line)), None)
    require(path_index is not None, "artifact upload has no explicit path allowlist")
    paths = []
    for line in lines[path_index + 1:]:
        if not line.startswith("            "):
            break
        paths.append(line.strip())
    expected = {
        "reports/stages/S00.json",
        "reports/status.json",
        ".artifacts/logs/acceptance/",
        ".artifacts/stage-runs/",
    }
    require(set(paths) == expected and len(paths) == len(expected),
            f"artifact paths differ from the explicit evidence allowlist: {paths}")
    require(not any(".tools" in path or ".build" in path or "node_modules" in path
                    for path in paths), "artifact allowlist contains tool/build caches")


def main():
    test_stale_pass_is_replaced_and_current_status_is_preserved()
    test_workflow_upload_is_hidden_file_aware_and_allowlisted()
    print("CI evidence safeguards PASS: stale PASS invalidation, current-run metadata, hidden logs and bounded artifact paths")


if __name__ == "__main__":
    main()
