#!/usr/bin/env python3
"""Prove S02 result aggregation fails closed on parent, missing, and skipped tests."""

import importlib.util
import pathlib

ROOT = pathlib.Path(__file__).resolve().parents[2]
SPEC = importlib.util.spec_from_file_location("nodedance_acceptance_s02", ROOT / "scripts/acceptance-s02.py")
ACCEPTANCE = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(ACCEPTANCE)


def require(condition, message):
    if not condition:
        raise AssertionError(message)


def main():
    integration_source = (ROOT / "internal/core/server/agent_integration_test.go").read_text()
    runner_source = (ROOT / "scripts/acceptance-s02.py").read_text()
    require("func TestRealAgentCoreCloseDrainsAgentProxyTunnels" in integration_source and
            't.Run("S02-07"' in integration_source,
            "S02-07 lacks a focused real Agent/Core/proxy shutdown case")
    require("closeS02CoreWithin(t, core, proxy, 5*time.Second)" in integration_source and
            "waitForAgentProxyTunnelCount(t, proxy, 0" in integration_source and
            "activeTunnels := proxy.activeTunnelCount()" in integration_source,
            "Core.Close must have a bounded wait and assert the WSS proxy copy handlers drain")
    require('"-run", "^TestRealAgent"' in runner_source,
            "the S02 runner must execute the focused real Core-close test in each attempt")

    all_pass = ["PASS", "PASS"]
    status, verification = ACCEPTANCE.evaluate_results(all_pass, [0], True, full_acceptance=True)
    require((status, verification) == ("PASS", "PASS"), "clean single-run acceptance did not pass")

    status, verification = ACCEPTANCE.evaluate_results(all_pass, [1], True, full_acceptance=True)
    require((status, verification) == ("FAIL", "FAIL"),
            "a nonzero parent process was hidden by passing named subtests")

    missing = ACCEPTANCE.case_status_for_run("missing", {}, 0)
    require(missing == "NOT_READY", "a missing required subtest was not marked NOT_READY")
    status, verification = ACCEPTANCE.evaluate_results(["PASS", missing], [0], True, full_acceptance=True)
    require((status, verification) == ("NOT_READY", "NOT_READY"),
            "a missing required case was reported as complete")

    skipped = ACCEPTANCE.case_status_for_run("skipped", {"skipped": "NOT_READY"}, 0)
    require(skipped == "NOT_READY", "a skipped required subtest was not marked NOT_READY")
    status, verification = ACCEPTANCE.evaluate_results(["PASS", skipped], [0], True, full_acceptance=True)
    require((status, verification) == ("NOT_READY", "NOT_READY"),
            "a skipped required case was reported as complete")

    status, verification = ACCEPTANCE.evaluate_results(["PASS", "FAIL"], [0], True, full_acceptance=True)
    require((status, verification) == ("FAIL", "FAIL"), "a failing named case was not rejected")

    print("S02 acceptance aggregation PASS: parent failure, missing/skip, named failure and clean full run")


if __name__ == "__main__":
    main()
