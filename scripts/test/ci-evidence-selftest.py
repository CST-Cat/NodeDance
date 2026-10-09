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


def workflow_command_position(workflow, alternatives, message):
    positions = [position for command in alternatives
                 if (position := workflow.find(command)) >= 0]
    require(bool(positions), message)
    return min(positions)


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
                {"attempt": attempt, "status": "PASS"} for attempt in (1,)
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
                {"attempt": attempt, "status": "PASS"} for attempt in (1,)
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


def test_stage_docs_list_each_registered_case_once():
    registry = json.loads((ROOT / "tests/registry.json").read_text())
    for stage in registry["stages"]:
        document = (ROOT / "docs/stages" / f"{stage['id']}.md").read_text()
        for case in stage.get("tests", []) + stage.get("supplemental_tests", []):
            marker = f"`{case['id']}`"
            require(document.count(marker) == 1,
                    f"{stage['id']} documentation must list {case['id']} exactly once")


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
    s01_end = workflow.index("  s02:", s01_start)
    s01_job = workflow[s01_start:s01_end]
    s01_acceptance_position = s01_job.index("run: make test-stage STAGE=S01")
    s01_before_acceptance = s01_job[:s01_acceptance_position]
    if "make build" in s01_before_acceptance:
        s01_build_position = s01_before_acceptance.index("make build")
    else:
        s01_frontend_position = workflow_command_position(
            s01_before_acceptance, ("make frontend",),
            "S01 workflow does not build embedded frontend assets before acceptance",
        )
        s01_core_binary_position = workflow_command_position(
            s01_before_acceptance, (".build/nodedance",),
            "S01 workflow does not build the local Core executable before acceptance",
        )
        require(s01_frontend_position < s01_core_binary_position,
                "S01 embedded frontend must be built before the local Core executable")
        s01_build_position = s01_frontend_position
    s01_browser_position = workflow_command_position(
        s01_before_acceptance,
        ("pnpm --dir web exec playwright install --with-deps chromium webkit firefox",
         "make playwright-install"),
        "S01 workflow does not install the locked Playwright browsers before acceptance",
    )
    s01_positions = [s01_job.index(value) for value in (
        "rm -f reports/stages/S01.json reports/status.json && python3 scripts/ci_evidence.py --stage S01 initialize",
        "make verify-tools",
        "run: make verify-ci-evidence",
    )] + [s01_build_position, s01_browser_position, s01_acceptance_position] + [s01_job.index(value) for value in (
        "python3 scripts/ci_evidence.py --stage S01 annotate",
        "uses: actions/upload-artifact@",
    )]
    require(s01_positions == sorted(s01_positions), "S01 workflow is missing isolated fresh evidence, embedded/Core build, locked browser install, acceptance, or final report steps")
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

    s05_workflow = (ROOT / ".github/workflows/s05-acceptance.yml").read_text()
    s05_start = s05_workflow.index("rm -f reports/stages/S05.json && python3 scripts/ci_evidence.py --stage S05 initialize")
    s05_job = s05_workflow[s05_start:]
    s05_acceptance_position = s05_job.index("run: make test-stage STAGE=S05")
    s05_before_acceptance = s05_job[:s05_acceptance_position]
    s05_frontend_position = workflow_command_position(
        s05_before_acceptance, ("make frontend", "make build"),
        "S05 workflow does not build the locked embedded frontend before acceptance",
    )
    s05_browser_position = workflow_command_position(
        s05_before_acceptance,
        ("pnpm --dir web exec playwright install --with-deps chromium webkit firefox",
         "make playwright-install"),
        "S05 workflow does not install the locked Playwright browsers before acceptance",
    )
    s05_positions = [s05_job.index(value) for value in (
        "rm -f reports/stages/S05.json && python3 scripts/ci_evidence.py --stage S05 initialize",
        "make verify-tools",
        "run: make verify-ci-evidence",
    )] + [s05_frontend_position, s05_browser_position] + [s05_job.index(value) for value in (
        "scripts/test/dind.sh start 28",
        "scripts/test/dind.sh start 29",
    )] + [s05_acceptance_position] + [s05_job.index(value) for value in (
        "python3 scripts/ci_evidence.py --stage S05 annotate",
        "python3 scripts/test/s05-sanitize-evidence.py",
        "uses: actions/upload-artifact@",
    )]
    require(s05_positions == sorted(s05_positions), "S05 workflow is missing isolated fresh evidence, frontend/browser prerequisites, owned Engine setup, acceptance, sanitization, or final report steps")
    s05_upload_start = s05_workflow.index("name: nodedance-s05-")
    s05_upload = s05_workflow[s05_upload_start:]
    require(re.search(r"^\s+include-hidden-files:\s+true\s*$", s05_upload, re.M) is not None,
            "S05 artifact upload does not include hidden .artifacts evidence")
    s05_lines = s05_upload.splitlines()
    s05_path_index = next((i for i, line in enumerate(s05_lines)
                           if re.match(r"\s+path:\s*\|\s*$", line)), None)
    require(s05_path_index is not None, "S05 artifact upload has no explicit path allowlist")
    s05_paths = []
    for line in s05_lines[s05_path_index + 1:]:
        if not line.startswith("            "):
            break
        s05_paths.append(line.strip())
    expected_s05 = {
        "reports/stages/S05.json",
        "reports/status.json",
        "reports/evidence-s05-redaction.json",
        ".artifacts/logs/acceptance-s05/",
        ".artifacts/work-s05/",
    }
    require(set(s05_paths) == expected_s05 and len(s05_paths) == len(expected_s05),
            f"S05 artifact paths differ from the explicit evidence allowlist: {s05_paths}")
    require(not any(".tools" in path or ".build" in path or "node_modules" in path
                    for path in s05_paths), "S05 artifact allowlist contains tool/build caches")

    s02_start = workflow.index("name: S02 /")
    s02_positions = [workflow.index(value, s02_start) for value in (
        "name: S02 /",
        "rm -f reports/stages/S02.json && python3 scripts/ci_evidence.py --stage S02 initialize",
        "run: make verify-tools",
        "run: make verify-ci-evidence",
        "run: make build",
        "run: make test-stage STAGE=S02",
        "python3 scripts/ci_evidence.py --stage S02 annotate",
        "uses: actions/upload-artifact@",
    )]
    require(s02_positions == sorted(s02_positions), "S02 workflow is missing fresh evidence, locked tools, full acceptance, or final report steps")
    s02_job = workflow[s02_start:]
    require("ubuntu-24.04" in s02_job and "ubuntu-24.04-arm" in s02_job
            and "NODEDANCE_SYSTEMD_ACCEPTANCE: '1'" in s02_job,
            "S02 CI must exercise both GitHub-hosted Linux architectures and the disposable systemd manager test")
    s02_path_index = next((i for i, line in enumerate(s02_job.splitlines())
                           if re.match(r"\s+path:\s*\|\s*$", line)), None)
    require(s02_path_index is not None, "S02 artifact upload has no explicit path allowlist")
    s02_lines = s02_job.splitlines()
    s02_paths = []
    for line in s02_lines[s02_path_index + 1:]:
        if not line.startswith("            "):
            break
        s02_paths.append(line.strip())
    expected_s02 = {
        "reports/stages/S02.json",
        "reports/status.json",
        ".artifacts/logs/acceptance-s02/",
        ".artifacts/stage-runs/",
    }
    require(set(s02_paths) == expected_s02 and len(s02_paths) == len(expected_s02),
            f"S02 artifact paths differ from the explicit evidence allowlist: {s02_paths}")
    require(not any("work-s02" in path or ".tools" in path or ".build" in path or "node_modules" in path
                    for path in s02_paths), "S02 artifact allowlist includes private work data or caches")
    makefile = (ROOT / "Makefile").read_text()
    require("build: frontend" in makefile
            and "-o .build/nodedance-agent ./cmd/nodedance-agent" in makefile,
            "S02 prerequisite must build the runner-native Agent CLI consumed by the real systemd acceptance")
    require("frontend: deps" in makefile and "pnpm --dir web run build" in makefile,
            "S02 workflow prerequisite must generate the embedded frontend assets before Go tests")
    require("playwright-install: deps" in makefile
            and "pnpm --dir web exec playwright install --with-deps $(PLAYWRIGHT_BROWSERS)" in makefile
            and "PLAYWRIGHT_BROWSERS ?= chromium webkit firefox" in makefile,
            "Playwright browser installation is not routed through the locked Make PATH/version")
    s00_workflow = workflow[workflow.index("jobs:"):workflow.index("  s01:")]
    require(len(re.findall(r"^\s+- runner: ubuntu-24\.04$", s00_workflow, re.M)) == 2
            and len(re.findall(r"^\s+- runner: ubuntu-24\.04-arm$", s00_workflow, re.M)) == 2
            and "matrix:\n        include:" in s00_workflow
            and s00_workflow.count("engine: '28'") == 2 and s00_workflow.count("engine: '29'") == 2,
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
                {"attempt": number, "status": "PASS"} for number in (1,)
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


def test_s02_marker_and_report_are_isolated_from_s00_s01():
    metadata_s02 = EVIDENCE.metadata_from_values(
        run_id="101", run_attempt="1", sha="c" * 40,
        job="s02", runner="ubuntu-24.04-arm", engine="not-applicable", stage="S02",
    )
    with tempfile.TemporaryDirectory(prefix="nodedance-ci-s02-isolation-") as temporary:
        root = pathlib.Path(temporary)
        status_path = root / "reports/status.json"
        s02_report = root / "reports/stages/S02.json"
        s02_marker = root / ".artifacts/ci-evidence/S02/current-job.json"
        s02_cases = EVIDENCE.stage_cases(stage="S02")
        initialized = EVIDENCE.initialize(
            s02_report, status_path, s02_marker, metadata=metadata_s02, stage="S02",
        )
        require(initialized["stage"] == "S02" and set(initialized["tests"]) == {case["id"] for case in s02_cases},
                "S02 initialization omitted an original or supplemental acceptance case")
        require(initialized["status"] == "NOT_READY" and all(not item["runs"] for item in initialized["tests"].values()),
                "S02 initialization inherited previous test results")
        stale = dict(initialized)
        stale.update({"run_id": "stale-s02-pass", "mode": "full", "status": "PASS",
                      "updated_at": (dt.datetime.fromisoformat(metadata_s02["initialized_at"])
                                     - dt.timedelta(seconds=1)).isoformat()})
        stale["tests"] = {case["id"]: {"status": "PASS", "runs": [
            {"attempt": number, "status": "PASS"} for number in (1,)
        ]} for case in s02_cases}
        s02_report.write_text(json.dumps(stale))
        replaced = EVIDENCE.annotate(s02_report, status_path, s02_marker, metadata=metadata_s02, stage="S02")
        require(replaced["status"] == "NOT_READY" and replaced["run_id"] == "ci-" + metadata_s02["job_key"],
                "S02 annotate accepted a stale PASS")


def test_s04_marker_and_report_are_isolated_from_s00_s01_s02():
    metadata_s04 = EVIDENCE.metadata_from_values(
        run_id="102", run_attempt="1", sha="d" * 40,
        job="s04", runner="ubuntu-24.04-arm", engine="29", stage="S04",
    )
    with tempfile.TemporaryDirectory(prefix="nodedance-ci-s04-isolation-") as temporary:
        root = pathlib.Path(temporary)
        status_path = root / "reports/status.json"
        s00_report, s01_report, s02_report = (root / f"reports/stages/{stage}.json"
                                              for stage in ("S00", "S01", "S02"))
        s04_report = root / "reports/stages/S04.json"
        s00_marker = root / ".artifacts/ci-evidence/current-job.json"
        s01_marker = root / ".artifacts/ci-evidence/S01/current-job.json"
        s02_marker = root / ".artifacts/ci-evidence/S02/current-job.json"
        s04_marker = root / ".artifacts/ci-evidence/S04/current-job.json"
        metadata_s00 = EVIDENCE.metadata_from_values(
            run_id="102", run_attempt="1", sha="d" * 40,
            job="s00", runner="ubuntu-24.04", engine="28",
        )
        metadata_s01 = EVIDENCE.metadata_from_values(
            run_id="102", run_attempt="1", sha="d" * 40,
            job="s01", runner="ubuntu-24.04", engine="not-applicable", stage="S01",
        )
        metadata_s02 = EVIDENCE.metadata_from_values(
            run_id="102", run_attempt="1", sha="d" * 40,
            job="s02", runner="ubuntu-24.04", engine="not-applicable", stage="S02",
        )
        for stage_report, marker, metadata, stage in (
            (s00_report, s00_marker, metadata_s00, "S00"),
            (s01_report, s01_marker, metadata_s01, "S01"),
            (s02_report, s02_marker, metadata_s02, "S02"),
        ):
            EVIDENCE.initialize(stage_report, status_path, marker, metadata=metadata, stage=stage)
        markers_before = [path.read_text() for path in (s00_marker, s01_marker, s02_marker)]
        cases = EVIDENCE.stage_cases(stage="S04")
        initialized = EVIDENCE.initialize(
            s04_report, status_path, s04_marker, metadata=metadata_s04, stage="S04",
        )
        require(initialized["stage"] == "S04" and set(initialized["tests"]) == {case["id"] for case in cases},
                "S04 initialization omitted an original or supplemental case")
        require(initialized["status"] == "NOT_READY" and all(not item["runs"] for item in initialized["tests"].values()),
                "S04 initialization inherited test outcomes")
        require([path.read_text() for path in (s00_marker, s01_marker, s02_marker)] == markers_before
                and s04_marker.is_file(), "S04 initialization changed or reused an earlier-stage marker")
        stale = dict(initialized)
        stale.update({"run_id": "stale-s04-pass", "mode": "full", "status": "PASS",
                      "updated_at": (dt.datetime.fromisoformat(metadata_s04["initialized_at"])
                                     - dt.timedelta(seconds=1)).isoformat()})
        stale["tests"] = {case["id"]: {"status": "PASS", "runs": [
            {"attempt": number, "status": "PASS"} for number in (1,)
        ]} for case in cases}
        s04_report.write_text(json.dumps(stale))
        replaced = EVIDENCE.annotate(s04_report, status_path, s04_marker,
                                     metadata=metadata_s04, stage="S04")
        require(replaced["status"] == "NOT_READY" and replaced["run_id"] == "ci-" + metadata_s04["job_key"],
                "S04 annotate accepted a stale PASS")
        require([path.read_text() for path in (s00_marker, s01_marker, s02_marker)] == markers_before,
                "S04 annotate mutated an earlier-stage marker")


def test_s04_workflow_matrix_and_artifact_allowlist():
    workflow = (ROOT / ".github/workflows/ci.yml").read_text()
    s04_start = workflow.index("name: S04 /")
    s04_job = workflow[s04_start:]
    positions = [s04_job.index(value) for value in (
        "rm -f reports/stages/S04.json && python3 scripts/ci_evidence.py --stage S04 initialize",
        "run: make verify-tools",
        "Prepare isolated S04 DIND sibling fixture worktree",
        'git worktree add --detach "$fixture_root" "$GITHUB_SHA"',
        '[[ "$actual_root" == "$expected_root" && "$actual_sha" == "$GITHUB_SHA" ]]',
        'printf \'NODEDANCE_S04_DIND_ROOT=%s\\n\' "$fixture_root" >> "$GITHUB_ENV"',
        "run: make verify-ci-evidence",
        "run: make frontend",
        "make playwright-install PLAYWRIGHT_BROWSERS=chromium",
        "run: make test-stage STAGE=S04",
        "python3 scripts/ci_evidence.py --stage S04 annotate",
        "uses: actions/upload-artifact@",
    )]
    require(positions == sorted(positions), "S04 CI workflow is missing isolated evidence, locked tools, real browser install, full acceptance, or final report step")
    makefile = (ROOT / "Makefile").read_text()
    require("frontend: deps" in makefile and "pnpm --dir web run build" in makefile,
            "S04 workflow prerequisite must generate the embedded frontend assets before Go tests")
    web_build = json.loads((ROOT / "web/package.json").read_text())["scripts"]["build"]
    require("../internal/core/webassets/dist" in web_build,
            "S04 frontend build does not produce the Go-embedded webassets/dist directory")
    require('fixture_root="$(dirname "$GITHUB_WORKSPACE")/NodeDance-s04"' in s04_job
            and 'expected_root="$(realpath -m "$GITHUB_WORKSPACE/../NodeDance-s04")"' in s04_job
            and '[[ "$fixture_root" == "$expected_root" ]]' in s04_job,
            "S04 DIND worktree is not constrained to the exact sibling fixture path")
    require('actual_sha="$(git -C "$fixture_root" rev-parse HEAD)"' in s04_job
            and '[[ "$actual_root" == "$expected_root" && "$actual_sha" == "$GITHUB_SHA" ]]' in s04_job,
            "S04 DIND worktree is not verified against the current checkout SHA")
    require('compose_bin="$GITHUB_WORKSPACE/.tools/docker/cli-plugins/docker-compose"' in s04_job
            and 'expected_compose="$(cat "$GITHUB_WORKSPACE/docker-compose.version")"' in s04_job,
            "S04 fixtures do not explicitly verify the locked Compose tool from the main checkout")
    require("ubuntu-24.04" in s04_job and "ubuntu-24.04-arm" in s04_job
            and "engine: '28'" in s04_job and "engine: '29'" in s04_job,
            "S04 CI must cover GitHub-hosted amd64/arm64 and Docker Engine 28/29")
    upload_start = s04_job.index("name: nodedance-s04-")
    upload = s04_job[upload_start:]
    lines = upload.splitlines()
    path_index = next((index for index, line in enumerate(lines)
                       if re.match(r"\s+path:\s*\|\s*$", line)), None)
    require(path_index is not None, "S04 artifact upload has no explicit path allowlist")
    paths = []
    for line in lines[path_index + 1:]:
        if not line.startswith("            "):
            break
        paths.append(line.strip())
    expected = {
        "reports/stages/S04.json", "reports/status.json",
        ".artifacts/logs/acceptance-s04/", ".artifacts/stage-runs/",
    }
    require(set(paths) == expected and len(paths) == len(expected),
            f"S04 artifact paths differ from the explicit safe evidence allowlist: {paths}")
    require(not any("work-s04" in path or ".tools" in path or ".build" in path or "node_modules" in path
                    for path in paths), "S04 artifact allowlist includes private test work data or build caches")


def test_s08_image_engine_builds_embedded_assets_before_core_test():
    workflow = (ROOT / ".github/workflows/s08-image-engine.yml").read_text()
    verify_tools_position = workflow_command_position(
        workflow, ("run: make verify-tools",),
        "S08 image Engine workflow does not verify the locked toolchain",
    )
    frontend_position = workflow_command_position(
        workflow, ("run: make frontend",),
        "S08 image Engine workflow does not build locked embedded Web assets",
    )
    core_test_position = workflow_command_position(
        workflow,
        ("run: NODEDANCE_S08_TEST_PACKAGE=./internal/core/server scripts/test/s08-image-engine-dind.sh",),
        "S08 image Engine workflow does not run the Core API integration test",
    )
    require(verify_tools_position < frontend_position < core_test_position,
            "S08 Core package test must run after the locked frontend build creates webassets/dist")
    makefile = (ROOT / "Makefile").read_text()
    require("frontend: deps" in makefile and "pnpm --dir web run build" in makefile,
            "S08 frontend prerequisite must use the locked frontend build target")
    web_build = json.loads((ROOT / "web/package.json").read_text())["scripts"]["build"]
    require("../internal/core/webassets/dist" in web_build,
            "S08 frontend build does not produce the Go-embedded webassets/dist directory")
    runner = (ROOT / "scripts/test/s08-image-engine-dind.sh").read_text()
    require('ARTIFACT_STEM="image"' in runner
            and 'ARTIFACT="$ARTIFACT_DIR/${ARTIFACT_STEM}-engine${ENGINE}-${RUN_ID}.log"' in runner,
            "S08 Agent image test producer does not use the image-engine<version>-<run>.log naming contract")
    require('test -s ".artifacts/s08/image-engine${{ matrix.engine }}-$S08_RUN_ID.log"' in workflow,
            "S08 workflow does not verify the producer's image-engine<version>-<run>.log artifact")


def test_s03_workflow_matrix_and_artifact_allowlist():
    workflow = (ROOT / ".github/workflows/ci.yml").read_text()
    s03_job = workflow[workflow.index("  s03:"):workflow.index("  s04:")]
    positions = [s03_job.index(value) for value in (
        "rm -f reports/stages/S03.json && python3 scripts/ci_evidence.py --stage S03 initialize",
        "run: make verify-tools",
        "run: make verify-ci-evidence",
        "run: make deps",
        "run: make playwright-install",
        "qemu-utils=1:8.2.2+ds-0ubuntu1.18",
        "sudo modprobe nbd max_part=8",
        "run: |\n          make check\n          make build",
        "scripts/acceptance-s03.py --mode full --repeat 1 --ci-runner",
        "python3 scripts/ci_evidence.py --stage S03 annotate",
        "uses: actions/upload-artifact@",
    )]
    require(positions == sorted(positions),
            "S03 workflow is missing the locked guest/browser prerequisites, one complete real acceptance run, or final report upload")
    require("ubuntu-24.04" in s03_job and "ubuntu-24.04-arm" in s03_job
            and "qemu-package: qemu-system-x86" in s03_job
            and "qemu-package: qemu-system-arm" in s03_job,
            "S03 CI must cover amd64/arm64 GitHub-hosted runners with matching QEMU")
    require("timeout-minutes: 360" in s03_job and "fail-fast: false" in s03_job
            and "--allow-downgrades" in s03_job,
            "S03 CI must keep both architecture shards independent and install the exact locked package versions")
    upload_start = s03_job.index("name: nodedance-s03-")
    upload = s03_job[upload_start:]
    lines = upload.splitlines()
    path_index = next((index for index, line in enumerate(lines)
                       if re.match(r"\s+path:\s*\|\s*$", line)), None)
    require(path_index is not None, "S03 artifact upload has no explicit path allowlist")
    paths = []
    for line in lines[path_index + 1:]:
        if not line.startswith("            "):
            break
        paths.append(line.strip())
    expected = {
        "reports/stages/S03.json", "reports/status.json",
        ".artifacts/logs/acceptance-s03/",
        ".artifacts/work-s03/guest-agent/**/browser/engine-*.json",
        ".artifacts/work-s03/guest-agent/**/browser/guest-phase.json",
        ".artifacts/work-s03/guest-agent/**/browser/playwright.log",
        ".artifacts/work-s03/*/summary.json",
        ".artifacts/work-s03/*/round-*/serial.log",
        ".artifacts/work-s03/*/round-*/commands.log",
        ".artifacts/work-s03/*/round-*/agent-core-states.jsonl",
        ".artifacts/work-s03/*/round-*/guest-clock-reboot.json",
        ".artifacts/work-s03/*/round-*/cpu-memory-load.json",
    }
    require(set(paths) == expected and len(paths) == len(expected),
            f"S03 artifact paths differ from the explicit evidence allowlist: {paths}")
    require("include-hidden-files: true" in upload,
            "S03 evidence upload must include hidden evidence directories")
    require(not any("/core/" in path or "manifest" in path or "binary" in path
                    or "overlay" in path or "image" in path or "key" in path
                    for path in paths),
            "S03 artifact allowlist includes private harness files, binaries, images, overlays, or keys")


def test_s03_aggregate_job_requires_both_current_shards():
    workflow = (ROOT / ".github/workflows/ci.yml").read_text()
    aggregate = workflow[workflow.index("  s03-aggregate:"):workflow.index("  s04:")]
    positions = [aggregate.index(value) for value in (
        "needs: s03",
        "python3 scripts/ci_evidence.py --stage S03 initialize",
        "nodedance-s03-${{ github.run_id }}-${{ github.run_attempt }}-ubuntu-24.04",
        "nodedance-s03-${{ github.run_id }}-${{ github.run_attempt }}-ubuntu-24.04-arm",
        "python3 scripts/test/aggregate-s03-ci-selftest.py",
        "python3 scripts/aggregate-s03-ci.py",
        "python3 scripts/ci_evidence.py --stage S03 annotate",
        "uses: actions/upload-artifact@",
    )]
    require(positions == sorted(positions),
            "S03 aggregate job does not wait for both shards, validate their reports, and publish the final result")
    require(aggregate.count("uses: actions/download-artifact@d3f86a106a0bac45b974a628896c90dbdf5c8093") == 2
            and "--run-id '${{ github.run_id }}'" in aggregate
            and "--run-attempt '${{ github.run_attempt }}'" in aggregate
            and "--sha '${{ github.sha }}'" in aggregate,
            "S03 aggregation must download both current-run shards and validate the same run, attempt, and commit")
    upload_start = aggregate.index("name: nodedance-s03-aggregate-")
    upload = aggregate[upload_start:]
    lines = upload.splitlines()
    path_index = next((index for index, line in enumerate(lines)
                       if re.match(r"\s+path:\s*\|\s*$", line)), None)
    require(path_index is not None, "S03 aggregate artifact has no explicit report allowlist")
    paths = []
    for line in lines[path_index + 1:]:
        if not line.startswith("            "):
            break
        paths.append(line.strip())
    expected = {
        "reports/stages/S03.json", "reports/status.json",
        ".artifacts/s03-ci-shards/amd64/reports/stages/S03.json",
        ".artifacts/s03-ci-shards/arm64/reports/stages/S03.json",
    }
    require(set(paths) == expected and len(paths) == len(expected),
            "S03 aggregate artifact must contain the final report and both input shard reports only")


def main():
    test_stale_pass_is_replaced_and_current_status_is_preserved()
    test_stage_docs_list_each_registered_case_once()
    test_s01_marker_and_report_are_isolated_from_s00()
    test_s02_marker_and_report_are_isolated_from_s00_s01()
    test_s04_marker_and_report_are_isolated_from_s00_s01_s02()
    test_workflow_upload_is_hidden_file_aware_and_allowlisted()
    test_s03_workflow_matrix_and_artifact_allowlist()
    test_s03_aggregate_job_requires_both_current_shards()
    test_s04_workflow_matrix_and_artifact_allowlist()
    test_s08_image_engine_builds_embedded_assets_before_core_test()
    print("CI evidence safeguards PASS: S00/S01/S02/S03/S04/S05/S08 workflow order, marker isolation, stale PASS invalidation, current-run metadata, hidden logs and bounded artifact paths")


if __name__ == "__main__":
    main()
