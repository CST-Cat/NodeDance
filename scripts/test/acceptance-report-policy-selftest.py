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
    print("Acceptance report policy PASS: current single run and annotated legacy S00/S01 evidence")


if __name__ == "__main__":
    main()
