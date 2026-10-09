#!/usr/bin/env python3
"""Focused contract tests for one-shot candidate stage runner behavior."""

from __future__ import annotations

import json
import pathlib
import runpy
import sys
import tempfile
import unittest
from contextlib import redirect_stderr
from io import StringIO
from types import SimpleNamespace

ROOT = pathlib.Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / "scripts"))

from acceptance_candidates import (  # noqa: E402
    CANDIDATE_TARGETS,
    REGISTRY,
    build_candidate_report,
    candidate_command,
    run_candidate_stage,
)
from acceptance_report_policy import s06_partial_case_is_valid  # noqa: E402


def registry_fixture() -> dict:
    return {
        "stages": [
            {
                "id": "S11",
                "tests": [
                    {
                        "id": "S11-01",
                        "action": "edit port",
                        "expected": "port applied",
                        "environment": "owned Engine",
                        "evidence": "Engine port mapping",
                    }
                ],
                "supplemental_tests": [],
            }
        ]
    }


def s06_registry_fixture() -> dict:
    return {
        "stages": [
            {
                "id": "S06",
                "tests": [
                    {
                        "id": f"S06-{number:02d}",
                        "action": "S06 acceptance action",
                        "expected": "S06 acceptance result",
                        "environment": "S06 candidate environment",
                        "evidence": "S06 acceptance evidence",
                    }
                    for number in range(1, 13)
                ],
                "supplemental_tests": [],
            }
        ]
    }


class CandidateRunnerTests(unittest.TestCase):
    def test_each_candidate_maps_to_one_focused_make_target(self) -> None:
        self.assertEqual(set(CANDIDATE_TARGETS), {"S06", "S11", "S14", "S15", "S16"})
        for stage, target in CANDIDATE_TARGETS.items():
            with self.subTest(stage=stage):
                self.assertEqual(candidate_command(stage), ["make", target])

    def test_s06_candidate_runs_once_and_keeps_normative_cases_not_ready(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            root = pathlib.Path(temporary)
            calls: list[list[str]] = []

            def runner(command, *, cwd, stdout, stderr, text, timeout):
                calls.append(command)
                stdout.write(json.dumps({"Action": "run", "Test": "TestS06PreferencePersistenceAcrossCoreRestart"}) + "\n")
                stdout.write(json.dumps({"Action": "pass", "Test": "TestS06PreferencePersistenceAcrossCoreRestart", "Elapsed": 0.5}) + "\n")
                stdout.write("S06 Playwright candidate passed once\n")
                return SimpleNamespace(returncode=0)

            result = run_candidate_stage(
                "S06",
                "full",
                root=root,
                registry=s06_registry_fixture(),
                command_runner=runner,
                run_id="s06-one-run",
                updated_at="2026-10-08T00:00:00+00:00",
            )
            report = json.loads((root / "reports/stages/S06.json").read_text(encoding="utf-8"))
            self.assertEqual(result, 2)
            self.assertEqual(calls, [["make", "test-candidate-s06"]])
            self.assertEqual(report["status"], "NOT_READY")
            self.assertEqual(report["repeat_required"], 1)
            self.assertEqual(report["repeat_requested"], 1)
            self.assertEqual(report["candidate_status"], "PASS")
            self.assertEqual(len(report["candidate_checks"]), 1)
            self.assertEqual(report["candidate_checks"][0]["invocations"], 1)
            self.assertEqual(set(report["tests"]), {f"S06-{number:02d}" for number in range(1, 13)})
            self.assertEqual(report["tests"]["S06-03"]["status"], "PASS")
            self.assertEqual(report["tests"]["S06-03"]["runs"], [{
                "attempt": 1,
                "status": "PASS",
                "test_name": "TestS06PreferencePersistenceAcrossCoreRestart",
                "evidence": ".artifacts/logs/acceptance-s06/s06-one-run/candidate.log",
            }])
            other_cases = [case for case_id, case in report["tests"].items() if case_id != "S06-03"]
            self.assertEqual(len(other_cases), 11)
            self.assertTrue(all(case["status"] == "NOT_READY" and case["runs"] == [] for case in other_cases))
            self.assertTrue(all(s06_partial_case_is_valid(report, case_id, case) for case_id, case in report["tests"].items()))

    def test_s06_candidate_without_persistence_test_does_not_claim_s06_03(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            root = pathlib.Path(temporary)

            def runner(command, **kwargs):
                kwargs["stdout"].write("S06 responsive candidate passed once\n")
                return SimpleNamespace(returncode=0)

            result = run_candidate_stage(
                "S06",
                "full",
                root=root,
                registry=s06_registry_fixture(),
                command_runner=runner,
                run_id="s06-missing-preference-run",
                updated_at="2026-10-08T00:00:00+00:00",
            )
            report = json.loads((root / "reports/stages/S06.json").read_text(encoding="utf-8"))
            self.assertEqual(result, 2)
            self.assertEqual(report["status"], "NOT_READY")
            self.assertEqual(report["tests"]["S06-03"]["status"], "NOT_READY")
            self.assertEqual(report["tests"]["S06-03"]["runs"], [])
            self.assertTrue(all(case["status"] == "NOT_READY" for case_id, case in report["tests"].items() if case_id != "S06-03"))

    def test_s06_preference_failure_is_not_hidden_by_partial_stage_status(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            root = pathlib.Path(temporary)

            def runner(command, *, stdout, **kwargs):
                stdout.write(json.dumps({"Action": "run", "Test": "TestS06PreferencePersistenceAcrossCoreRestart"}) + "\n")
                stdout.write(json.dumps({"Action": "fail", "Test": "TestS06PreferencePersistenceAcrossCoreRestart", "Elapsed": 0.5}) + "\n")
                return SimpleNamespace(returncode=1)

            result = run_candidate_stage(
                "S06", "full", root=root, registry=s06_registry_fixture(), command_runner=runner,
                run_id="s06-preference-failed", updated_at="2026-10-08T00:00:00+00:00",
            )
            report = json.loads((root / "reports/stages/S06.json").read_text(encoding="utf-8"))
            self.assertEqual(result, 1)
            self.assertEqual(report["status"], "FAIL")
            self.assertEqual(report["tests"]["S06-03"]["status"], "FAIL")
            self.assertTrue(all(s06_partial_case_is_valid(report, case_id, case) for case_id, case in report["tests"].items()))

    def test_s06_browser_candidate_failure_keeps_failure_status_with_preference_pass(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            root = pathlib.Path(temporary)

            def runner(command, *, stdout, **kwargs):
                stdout.write(json.dumps({"Action": "run", "Test": "TestS06PreferencePersistenceAcrossCoreRestart"}) + "\n")
                stdout.write(json.dumps({"Action": "pass", "Test": "TestS06PreferencePersistenceAcrossCoreRestart", "Elapsed": 0.5}) + "\n")
                stdout.write("Playwright responsive candidate failed\n")
                return SimpleNamespace(returncode=1)

            result = run_candidate_stage(
                "S06", "full", root=root, registry=s06_registry_fixture(), command_runner=runner,
                run_id="s06-browser-failed", updated_at="2026-10-08T00:00:00+00:00",
            )
            report = json.loads((root / "reports/stages/S06.json").read_text(encoding="utf-8"))
            self.assertEqual(result, 1)
            self.assertEqual(report["status"], "FAIL")
            self.assertEqual(report["candidate_status"], "FAIL")
            self.assertEqual(report["tests"]["S06-03"]["status"], "PASS")
            self.assertTrue(all(s06_partial_case_is_valid(report, case_id, case) for case_id, case in report["tests"].items()))

    def test_stage_runner_routes_s06_through_candidate_reporter(self) -> None:
        stage_runner = runpy.run_path(str(ROOT / "scripts/stage-runner.py"))
        original_argv = sys.argv
        original_run = stage_runner["subprocess"].run
        calls: list[tuple[list[str], pathlib.Path]] = []

        def runner(command, *, cwd):
            calls.append((command, pathlib.Path(cwd)))
            return SimpleNamespace(returncode=2)

        try:
            sys.argv = ["stage-runner.py", "--stage", "S06", "--mode", "full"]
            stage_runner["subprocess"].run = runner
            self.assertEqual(stage_runner["main"](), 2)
        finally:
            stage_runner["subprocess"].run = original_run
            sys.argv = original_argv
        self.assertEqual(calls, [([
            sys.executable,
            "scripts/acceptance-candidates.py",
            "--stage",
            "S06",
            "--mode",
            "full",
        ], ROOT)])

    def test_all_original_candidate_cases_remain_not_ready_after_candidate_pass(self) -> None:
        for stage in CANDIDATE_TARGETS:
            report = build_candidate_report(
                stage=stage,
                mode="full",
                run_id="selftest",
                updated_at="2026-10-08T00:00:00+00:00",
                check={"status": "PASS", "exit_code": 0},
            )
            cases = next(item for item in REGISTRY["stages"] if item["id"] == stage)
            expected_ids = {
                case["id"]
                for case in cases.get("tests", []) + cases.get("supplemental_tests", [])
            }
            self.assertEqual(set(report["tests"]), expected_ids)
            self.assertEqual(report["status"], "NOT_READY")
            self.assertTrue(all(item["status"] == "NOT_READY" for item in report["tests"].values()))

    def test_passing_candidate_is_not_ready_and_runs_one_command(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            root = pathlib.Path(temporary)
            reports = root / "reports/stages"
            reports.mkdir(parents=True)
            (reports / "S11.json").write_text(
                json.dumps({
                    "stage": "S11",
                    "status": "NOT_READY",
                    "tests": {"S11-01": {"status": "PASS", "runs": [{"workflow_run_id": "old"}]}},
                    "candidate_suite": {"status": "PASS"},
                }),
                encoding="utf-8",
            )
            calls: list[list[str]] = []

            def runner(command, *, cwd, stdout, stderr, text, timeout):
                calls.append(command)
                stdout.write("focused candidate passed\n")
                return SimpleNamespace(returncode=0)

            result = run_candidate_stage(
                "S11",
                "full",
                root=root,
                registry=registry_fixture(),
                command_runner=runner,
                run_id="one-run",
                updated_at="2026-10-08T00:00:00+00:00",
            )
            report = json.loads((reports / "S11.json").read_text(encoding="utf-8"))
            self.assertEqual(result, 2)
            self.assertEqual(calls, [["make", "test-candidate-s11"]])
            self.assertEqual(report["status"], "NOT_READY")
            self.assertEqual(report["candidate_status"], "PASS")
            self.assertEqual(report["candidate_checks"][0]["invocations"], 1)
            self.assertEqual(report["tests"]["S11-01"]["status"], "NOT_READY")
            self.assertEqual(report["tests"]["S11-01"]["runs"], [])
            self.assertEqual(
                report["historical_candidate_report"]["tests"]["S11-01"]["status"],
                "PASS",
            )

    def test_generic_not_ready_run_preserves_previous_stage_report(self) -> None:
        stage_runner = runpy.run_path(str(ROOT / "scripts/stage-runner.py"))
        with tempfile.TemporaryDirectory() as temporary:
            root = pathlib.Path(temporary)
            reports = root / "reports/stages"
            reports.mkdir(parents=True)
            status_file = root / "reports/status.json"
            previous = {
                "stage": "S06",
                "status": "NOT_READY",
                "reason": "old candidate details",
                "candidate_suite": {"status": "PASS", "artifact": "old-log"},
                "tests": {"S06-01": {"status": "NOT_READY", "reason": "old evidence"}},
            }
            (reports / "S06.json").write_text(json.dumps(previous), encoding="utf-8")
            runner_globals = stage_runner["not_ready_report"].__globals__
            runner_globals["ROOT"] = root
            runner_globals["REPORTS"] = reports
            runner_globals["STATUS_FILE"] = status_file
            with redirect_stderr(StringIO()):
                result = stage_runner["not_ready_report"]("S06", "full", "current suite unavailable")
            report = json.loads((reports / "S06.json").read_text(encoding="utf-8"))
            self.assertEqual(result, 2)
            self.assertEqual(report["status"], "NOT_READY")
            self.assertTrue(all(item["status"] == "NOT_READY" for item in report["tests"].values()))
            self.assertEqual(report["historical_stage_report"], previous)

    def test_failed_candidate_is_reported_without_claiming_normative_case_ran(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            root = pathlib.Path(temporary)

            def runner(command, **kwargs):
                return SimpleNamespace(returncode=7)

            result = run_candidate_stage(
                "S11",
                "integration",
                root=root,
                registry=registry_fixture(),
                command_runner=runner,
                run_id="failed-run",
                updated_at="2026-10-08T00:00:00+00:00",
            )
            report = json.loads((root / "reports/stages/S11.json").read_text(encoding="utf-8"))
            self.assertEqual(result, 1)
            self.assertEqual(report["status"], "FAIL")
            self.assertEqual(report["candidate_checks"][0]["exit_code"], 7)
            self.assertEqual(report["tests"]["S11-01"]["status"], "NOT_READY")
            self.assertEqual(report["tests"]["S11-01"]["runs"], [])


if __name__ == "__main__":
    unittest.main(verbosity=2)
