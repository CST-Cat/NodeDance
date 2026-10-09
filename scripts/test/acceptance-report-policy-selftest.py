#!/usr/bin/env python3
"""Verify one-run acceptance and explicitly annotated historical report handling."""

import importlib.util
import pathlib

ROOT = pathlib.Path(__file__).resolve().parents[2]
SPEC = importlib.util.spec_from_file_location(
    "nodedance_acceptance_report_policy", ROOT / "scripts/acceptance_report_policy.py"
)
POLICY = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(POLICY)


def require(condition, message):
    if not condition:
        raise AssertionError(message)


def main():
    current = {"mode": "full", "repeat_required": 1, "repeat_requested": 1}
    require(POLICY.pass_runs_are_valid("S00", current, [{"attempt": 1, "status": "PASS"}]),
            "one complete current run was rejected")
    require(not POLICY.pass_runs_are_valid("S00", current, [
        {"attempt": 1, "status": "PASS"},
        {"attempt": 2, "status": "PASS"},
        {"attempt": 3, "status": "PASS"},
    ]), "a new report with repeated runs bypassed the single-run rule")

    historical = {
        "mode": "full",
        "repeat_required": 3,
        "repeat_requested": 3,
        "historical_acceptance_policy_note": POLICY.HISTORICAL_THREE_RUN_NOTE,
    }
    legacy_runs = [{"attempt": attempt, "status": "PASS"} for attempt in (1, 2, 3)]
    for stage in ("S00", "S01"):
        require(POLICY.pass_runs_are_valid(stage, historical, legacy_runs),
                f"annotated historical {stage} PASS was rejected")
    require(not POLICY.pass_runs_are_valid("S02", historical, legacy_runs),
            "legacy run shape was accepted for an unrelated stage")

    unannotated = dict(historical)
    unannotated.pop("historical_acceptance_policy_note")
    require(not POLICY.pass_runs_are_valid("S00", unannotated, legacy_runs),
            "unannotated legacy evidence was accepted")
    require(not POLICY.pass_runs_are_valid("S00", historical, legacy_runs[:2]),
            "incomplete historical evidence was accepted")
    failed = [{"attempt": 1, "status": "FAIL"}]
    require(not POLICY.pass_runs_are_valid("S00", current, failed),
            "a failed current run was accepted")

    partial_s06 = {
        "stage": "S06", "status": "NOT_READY", "repeat_required": 1, "repeat_requested": 1,
        "candidate_status": "PASS",
    }
    s06_tests = POLICY.S06_PARTIAL_CASE_TESTS
    s06_pass = {
        case_id: {"status": "PASS", "runs": [{"attempt": 1, "status": "PASS", "test_name": test_name}]}
        for case_id, test_name in s06_tests.items()
    }
    s06_other = {"status": "NOT_READY", "runs": []}
    for case_id, case in s06_pass.items():
        require(POLICY.s06_partial_case_is_valid(partial_s06, case_id, case),
                f"the single current {case_id} PASS was rejected")
    require(POLICY.s06_partial_case_is_valid(partial_s06, "S06-02", s06_other),
            "an unexecuted S06 case was rejected")
    require(not POLICY.s06_partial_case_is_valid(partial_s06, "S06-04", s06_pass["S06-01"]),
            "a non-eligible S06 PASS was accepted in a partial report")
    wrong_test_name = {"status": "PASS", "runs": [{"attempt": 1, "status": "PASS", "test_name": "TestS06UnrelatedMock"}]}
    require(not POLICY.s06_partial_case_is_valid(partial_s06, "S06-11", wrong_test_name),
            "unrelated test evidence was accepted for S06-11")
    three_s06_runs = {"status": "PASS", "runs": [
        {"attempt": attempt, "status": "PASS", "test_name": s06_tests["S06-03"]}
        for attempt in (1, 2, 3)
    ]}
    require(not POLICY.s06_partial_case_is_valid(partial_s06, "S06-03", three_s06_runs),
            "repeated S06-03 attempts bypassed the one-run rule")
    require(not POLICY.s06_partial_case_is_valid(dict(partial_s06, status="PASS"), "S06-03", s06_pass["S06-03"]),
            "partial S06 evidence incorrectly completed the full stage")
    failed_candidate = dict(partial_s06, status="FAIL", candidate_status="FAIL")
    require(POLICY.s06_partial_case_is_valid(failed_candidate, "S06-03", s06_pass["S06-03"]),
            "a failed responsive candidate was hidden by an S06-03 PASS")
    s06_fail = {"status": "FAIL", "runs": [{"attempt": 1, "status": "FAIL", "test_name": s06_tests["S06-11"]}]}
    failed_preference = dict(partial_s06, status="FAIL")
    require(POLICY.s06_partial_case_is_valid(failed_preference, "S06-11", s06_fail),
            "an executed S06-11 failure was rejected as partial evidence")
    require(not POLICY.s06_partial_case_is_valid(partial_s06, "S06-11", s06_fail),
            "an S06-11 failure was hidden behind NOT_READY")
    print("Acceptance report policy PASS: named one-run S06 partial cases preserve failures; other cases remain NOT_READY; annotated legacy S00/S01 evidence remains valid")


if __name__ == "__main__":
    main()
