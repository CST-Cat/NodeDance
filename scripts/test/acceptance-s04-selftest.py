#!/usr/bin/env python3
"""Check S04 named-test aggregation fails closed without touching a Docker Engine."""

import importlib.util
import json
import os
import pathlib
import time

ROOT = pathlib.Path(__file__).resolve().parents[2]
SCRIPT = ROOT / "scripts/acceptance-s04.py"
SPEC = importlib.util.spec_from_file_location("nodedance_acceptance_s04", SCRIPT)
ACCEPTANCE = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(ACCEPTANCE)


def require(condition, message):
    if not condition:
        raise AssertionError(message)


def test_named_test_process_status_is_fail_closed():
    passing = [{"Action": "run", "Test": "TestExample"},
               {"Action": "pass", "Test": "TestExample"}]
    require(ACCEPTANCE.test_status("TestExample", passing, 0)[0] == "PASS",
            "a complete named PASS event was not accepted")
    require(ACCEPTANCE.test_status("TestExample", passing, 1)[0] == "FAIL",
            "a non-zero parent process exit was ignored")
    require(ACCEPTANCE.test_status("TestMissing", passing, 0)[0] == "FAIL",
            "a missing required named test was accepted")
    skipped_child = passing + [{"Action": "skip", "Test": "TestExample/subcase"}]
    require(ACCEPTANCE.test_status("TestExample", skipped_child, 0)[0] == "NOT_READY",
            "a skipped required child test was accepted")
    failed_child = passing + [{"Action": "fail", "Test": "TestExample/subcase"}]
    require(ACCEPTANCE.test_status("TestExample", failed_child, 0)[0] == "FAIL",
            "a failed required child test was accepted")


def test_original_case_mapping_and_redaction():
    case_ids = {case["id"] for case in ACCEPTANCE.CASES}
    require(case_ids == set(ACCEPTANCE.CASE_TESTS),
            "an original S04 case lacks named real Engine/Core/Agent evidence mapping")
    require(all(names and all(name in ACCEPTANCE.AGENT_TESTS + ACCEPTANCE.SERVER_TESTS for name in names)
                for names in ACCEPTANCE.CASE_TESTS.values()),
            "an S04 case references an unexecuted named Go test")
    require("TestRealAgentDockerAPIIncompatibilitySecretRedaction" in ACCEPTANCE.CASE_TESTS["S04-08"],
            "S04-08 must run the real Agent/Core API incompatibility redaction fixture")
    require("TestRealDockerAgentCoreMultiChunkSnapshotReconnect" in ACCEPTANCE.CASE_TESTS["S04-02"],
            "S04-02 must exercise external pause/unpause/rename/delete through the real Core API")
    require("TestRealAgentDockerInventoryTraceOnOwnedDIND" in ACCEPTANCE.CASE_TESTS["S04-01"],
            "S04-01 must trace exact owned Engine IDs through Agent WSS snapshots into the final Core view")
    require("TestRealAgentDockerAPIIncompatibilitySecretRedaction" in ACCEPTANCE.CASE_TESTS["S04-07"],
            "S04-07 must include the real Agent/Core API fixture that fails after a successful Inspect")
    require("TestRealDockerAgentCoreMultiChunkSnapshotReconnect" in ACCEPTANCE.CASE_TESTS["S04-09"],
            "S04-09 must insert duplicate and older Docker frames over an authenticated WSS connection")
    integration_source = (ROOT / "internal/core/server/docker_agent_integration_test.go").read_text()
    proxy_source = (ROOT / "internal/core/server/agent_integration_test.go").read_text()
    browser_source = (ROOT / "web/tests/s04-real-browser.mjs").read_text()
    require("S04-02 external lifecycle API observed rename" in integration_source and
            "S04-02 external lifecycle API observed delete" in integration_source,
            "S04-02 evidence does not record external rename/delete convergence through the private Core API")
    require("S04-07 partial full scan failed after" in integration_source,
            "S04-07 test does not assert failure after a successful Inspect and retained inventory")
    require("replayNextDockerChangeOutOfOrderAndDuplicate" in proxy_source and
            "S04-09 authenticated WSS accepted newer frame" in integration_source,
            "S04-09 test does not inject and verify duplicate/out-of-order frames through the authenticated proxy")
    require("async function openDockerDetails()" in browser_source and
            "name: 'Docker 详情', exact: true" in browser_source and
            browser_source.count("await openDockerDetails()") == 2 and
            "Docker 详情 tab did not become the active dashboard section" in browser_source,
            "S04 real-browser flow must enter Docker details before asserting container rows, including after network restore")
    require(ACCEPTANCE.LOCKED_GO == ROOT / ".tools/go1.26.8/bin/go",
            "the S04 runner does not select the pinned Go 1.26.8 tool")
    node_version = json.loads((ROOT / "toolchain.lock.json").read_text())["node"]["version"]
    node_bin = ROOT / ".tools" / f"node-v{node_version}" / "bin"
    if node_bin.is_dir():
        require(ACCEPTANCE.clean_env()["PATH"].split(os.pathsep, 1)[0] == str(node_bin),
                "the real browser subprocess does not inherit the locked Node.js binary first on PATH")
    require([command[2] for command in ACCEPTANCE.GO_COMMANDS[:2]] == [
                ("TestDINDInventoryMatchesOwnedFixtures",),
                ("TestDINDEventLifetimeAndReconnectSnapshot",),
            ], "the external inventory fixture must be checked before the Engine outage test")
    value = ACCEPTANCE.sanitize({"password": "abc123456", "message": "authorization=BearerSecretValue"})
    require(value["password"] == "[REDACTED]" and "[REDACTED]" in value["message"],
            "S04 evidence sanitizer retained a secret field or inline secret")


def test_missing_or_wrong_go_cleanup_fails_closed():
    status, result = ACCEPTANCE.preflight_cleanup_result(0)
    require(status == "NOT_READY" and result == (False, True),
            "missing or wrong Go with successful exact cleanup must be NOT_READY")
    status, result = ACCEPTANCE.preflight_cleanup_result(1)
    require(status == "FAIL" and result == (False, False),
            "missing or wrong Go with failed exact cleanup must fail the attempt")


def test_report_reason_tracks_case_status():
    evidence = ROOT / ".artifacts" / "acceptance-s04-selftest.jsonl"
    ACCEPTANCE.CURRENT_REPORT = {"tests": {"S04-01": {
        "status": "NOT_READY", "reason": "No S04 acceptance attempt has completed yet.", "runs": [],
    }}}
    ACCEPTANCE.report_case("S04-01", 1, "PASS", evidence)
    case = ACCEPTANCE.CURRENT_REPORT["tests"]["S04-01"]
    require(case["status"] == "PASS" and case["reason"] == "",
            "a PASS case retained a stale no-attempt reason")

    ACCEPTANCE.CURRENT_REPORT = {"tests": {"S04-01": {
        "status": "NOT_READY", "reason": "No S04 acceptance attempt has completed yet.", "runs": [],
    }}}
    ACCEPTANCE.report_case("S04-01", 1, "FAIL", evidence, "required test failed")
    ACCEPTANCE.report_case("S04-01", 2, "PASS", evidence)
    case = ACCEPTANCE.CURRENT_REPORT["tests"]["S04-01"]
    require(case["status"] == "FAIL" and case["reason"] == "required test failed",
            "a later PASS hid an earlier failed attempt or its reason")


def test_cleanup_and_engine_failure_cannot_leave_a_case_passing():
    status, reasons = ACCEPTANCE.final_case_gate("PASS", cleanup_code=1, engine_after_ok=True)
    require(status == "FAIL" and reasons,
            "successful named tests passed a case despite failed exact fixture cleanup")
    status, reasons = ACCEPTANCE.final_case_gate("PASS", cleanup_code=0, engine_after_ok=False)
    require(status == "FAIL" and reasons,
            "successful named tests passed a case despite failed owned Engine postcondition")


def test_unreaped_process_group_blocks_fixture_deletion():
    require(ACCEPTANCE.cleanup_permitted(True),
            "verified-reaped process groups should permit exact fixture cleanup")
    require(not ACCEPTANCE.cleanup_permitted(False),
            "an unreaped child process group must block fixture deletion")


def test_outer_timeout_kills_and_joins_child_process_group():
    command = [ACCEPTANCE.sys.executable, "-c",
               "import subprocess,sys,time; subprocess.Popen([sys.executable,'-c','import time; time.sleep(30)']); time.sleep(30)"]
    started = time.monotonic()
    result, timed_out, reaped = ACCEPTANCE.bounded_run(
        command, cwd=ROOT, env=ACCEPTANCE.clean_env(), timeout=0.2)
    elapsed = time.monotonic() - started
    require(timed_out and result.returncode == 124,
            "outer process deadline did not produce a timeout failure result")
    require(reaped, "outer timeout did not join its process group")
    require(elapsed < 8, f"outer process timeout exceeded bounded termination grace: {elapsed:.2f}s")


def main():
    test_named_test_process_status_is_fail_closed()
    test_original_case_mapping_and_redaction()
    test_missing_or_wrong_go_cleanup_fails_closed()
    test_report_reason_tracks_case_status()
    test_cleanup_and_engine_failure_cannot_leave_a_case_passing()
    test_unreaped_process_group_blocks_fixture_deletion()
    test_outer_timeout_kills_and_joins_child_process_group()
    print("S04 acceptance safeguards PASS: required case branches mapped, named/parent/cleanup failures fail closed, bounded process groups, secrets redacted")


if __name__ == "__main__":
    main()
