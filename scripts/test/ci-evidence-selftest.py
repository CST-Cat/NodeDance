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

    s01_start = workflow.index("rm -f reports/stages/S01.json reports/status.json && python3 scripts/ci_evidence.py --stage S01 initialize")
    s01_positions = [workflow.index(value, s01_start) for value in (
        "rm -f reports/stages/S01.json reports/status.json && python3 scripts/ci_evidence.py --stage S01 initialize",
        "run: make verify-tools",
        "run: make verify-ci-evidence",
        "run: make deps",
        "run: make playwright-install",
        "run: make test-stage STAGE=S01",
        "python3 scripts/ci_evidence.py --stage S01 annotate",
        "uses: actions/upload-artifact@",
    )]
    require(s01_positions == sorted(s01_positions), "S01 workflow is missing isolated fresh evidence, locked browser install, or final report steps")
    s01_upload_start = workflow.index("name: nodedance-s01-")
    s01_upload = workflow[s01_upload_start:]
    path_index = next((i for i, line in enumerate(s01_upload.splitlines())
                       if re.match(r"\s+path:\s*\|\s*$", line)), None)
    require(path_index is not None, "S01 artifact upload has no explicit path allowlist")
    s01_lines = s01_upload.splitlines()
    s01_paths = []
    for line in s01_lines[path_index + 1:]:
        if not line.startswith("            "):
            break
        s01_paths.append(line.strip())
    expected_s01 = {
        "reports/stages/S01.json",
        "reports/status.json",
        ".artifacts/logs/acceptance-s01/",
        ".artifacts/stage-runs/",
    }
    require(set(s01_paths) == expected_s01 and len(s01_paths) == len(expected_s01),
            f"S01 artifact paths differ from the explicit safe evidence allowlist: {s01_paths}")
    require(not any("work-s01" in path or "test-results" in path or "playwright-report" in path
                    or ".tools" in path or ".build" in path or "node_modules" in path
                    for path in s01_paths), "S01 artifact allowlist includes private work data or caches")
    makefile = (ROOT / "Makefile").read_text()
    require("playwright-install: deps" in makefile
            and "pnpm --dir web exec playwright install --with-deps $(PLAYWRIGHT_BROWSERS)" in makefile
            and "PLAYWRIGHT_BROWSERS ?= chromium webkit firefox" in makefile,
            "Playwright browser installation is not routed through the locked Make PATH/version")
    require(len(re.findall(r"^\s+- runner: ubuntu-24\.04$", workflow, re.M)) == 2
            and len(re.findall(r"^\s+- runner: ubuntu-24\.04-arm$", workflow, re.M)) == 2
            and "matrix:\n        include:" in workflow
            and workflow.count("engine: '28'") == 2 and workflow.count("engine: '29'") == 2,
            "S01 CI changes removed the original four S00 runner/engine combinations")


def test_s01_marker_and_report_are_isolated_from_s00():
    metadata_s00 = EVIDENCE.metadata_from_values(
        run_id="100", run_attempt="1", sha="b" * 40,
        job="s00", runner="ubuntu-24.04", engine="28",
    )
    metadata_s01 = EVIDENCE.metadata_from_values(
        run_id="100", run_attempt="1", sha="b" * 40,
        job="s01", runner="ubuntu-24.04", engine="not-applicable", stage="S01",
    )
    with tempfile.TemporaryDirectory(prefix="nodedance-ci-stage-isolation-") as temporary:
        root = pathlib.Path(temporary)
        status_path = root / "reports/status.json"
        s00_report = root / "reports/stages/S00.json"
        s01_report = root / "reports/stages/S01.json"
        s00_marker = root / ".artifacts/ci-evidence/current-job.json"
        s01_marker = root / ".artifacts/ci-evidence/S01/current-job.json"
        EVIDENCE.initialize(s00_report, status_path, s00_marker, metadata=metadata_s00)
        s00_marker_before = s00_marker.read_text()
        s01_cases = EVIDENCE.stage_cases(stage="S01")
        initialized = EVIDENCE.initialize(
            s01_report, status_path, s01_marker, metadata=metadata_s01, stage="S01",
        )
        require(initialized["stage"] == "S01" and set(initialized["tests"]) == {case["id"] for case in s01_cases},
                "S01 initialization did not list every original S01 case")
        require(initialized["status"] == "NOT_READY" and all(not item["runs"] for item in initialized["tests"].values()),
                "S01 initialization inherited test outcomes")
        require(s00_marker.read_text() == s00_marker_before and s01_marker.is_file(),
                "S01 initialization mutated or reused the S00 marker")

        stale_s01 = dict(initialized)
        stale_s01.update({
            "run_id": "stale-pass", "mode": "full", "status": "PASS",
            "updated_at": (dt.datetime.fromisoformat(metadata_s01["initialized_at"])
                           - dt.timedelta(seconds=1)).isoformat(),
        })
        stale_s01["tests"] = {
            case["id"]: {"status": "PASS", "runs": [
                {"attempt": number, "status": "PASS"} for number in (1, 2, 3)
            ]} for case in s01_cases
        }
        s01_report.write_text(json.dumps(stale_s01))
        replaced = EVIDENCE.annotate(
            s01_report, status_path, s01_marker, metadata=metadata_s01, stage="S01",
        )
        require(replaced["status"] == "NOT_READY" and replaced["run_id"] == "ci-" + metadata_s01["job_key"],
                "S01 annotate accepted a stale PASS")
        require(s00_marker.read_text() == s00_marker_before,
                "S01 annotate mutated the S00 marker")


def main():
    test_stale_pass_is_replaced_and_current_status_is_preserved()
    test_s01_marker_and_report_are_isolated_from_s00()
    test_workflow_upload_is_hidden_file_aware_and_allowlisted()
    print("CI evidence safeguards PASS: S00/S01 marker isolation, stale PASS invalidation, current-run metadata, hidden logs and bounded artifact paths")


if __name__ == "__main__":
    main()
