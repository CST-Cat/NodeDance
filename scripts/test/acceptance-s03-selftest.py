#!/usr/bin/env python3
"""Self-test S03 local-run derivation, architecture gates, and compact evidence."""
import importlib.util
import json
import pathlib
import sys

ROOT = pathlib.Path(__file__).resolve().parents[2]
SCRIPT = ROOT / "scripts/acceptance-s03.py"
spec = importlib.util.spec_from_file_location("acceptance_s03", SCRIPT)
acceptance = importlib.util.module_from_spec(spec)
sys.modules[spec.name] = acceptance
spec.loader.exec_module(acceptance)


def require(condition, message):
    if not condition:
        raise AssertionError(message)


def make_checks(attempt, *, status="PASS", omitted=()):
    checks = []
    names = sorted({name for name in acceptance.CHECK_CASES if name not in omitted})
    for name in names:
        check = {"name": name, "status": status, "evidence": f".artifacts/check-{attempt}.json", "reason": ""}
        if name == "real_browser_guest_dashboard":
            check["evidence"] = ".artifacts/browser/engine-summary.json"
            check["engines"] = [{
                "engine": engine, "status": "PASS", "reason": "", "distinctSamples": 40,
                "duplicateSnapshotDeliveries": 10, "domSamples": [{"sample": i} for i in range(20)],
                "sampleToDOM": [{"latencyMillis": i * 10} for i in range(15)],
                "latencyStats": {"count": 15, "p50Millis": 70, "p95Millis": 130, "p99Millis": 140, "maxMillis": 140},
                "loadStats": {"duringCPUPeakPercent": 63},
                "viewportEvidence": [{"width": width, "scrollWidth": width, "clientWidth": width}
                                     for width in (375, 768, 1440)],
                "assertions": {"realAgentCPUAndMemory": True},
                "websocketFrames": [{"frame": i} for i in range(10000)],
            } for engine in ("chromium", "webkit", "firefox")]
        checks.append(check)
    return checks


def make_report():
    return {
        "schema": 1, "stage": "S03", "mode": "full", "run_id": "selftest",
        "repeat_required": 1, "repeat_requested": 1, "status": "NOT_READY",
        "evidence_root": ".artifacts/selftest", "tests": {
            case_id: {"status": "NOT_READY", "runs": []} for case_id in acceptance.CASE_IDS
        },
        "checks": [{"attempt": 1, "results": make_checks(1)}],
        "ci_gates": acceptance.ci_gates_template(),
    }


def test_per_attempt_and_single_run_statuses():
    passed = make_checks(1)
    for case_id in acceptance.CASE_IDS:
        result = acceptance.derive_case_attempt(passed, case_id, 1)
        require(result["status"] == "PASS", f"{case_id} did not derive a complete local PASS")
        missing = acceptance.derive_case_attempt(
            [item for item in passed if item["name"] != next(iter(acceptance.expected_check_names(case_id)))],
            case_id, 1)
        require(missing["status"] == "NOT_READY", f"{case_id} passed while a supporting check was absent")
        failed = acceptance.derive_case_attempt(
            [dict(item, status="FAIL") if item["name"] in acceptance.expected_check_names(case_id) else item
             for item in passed], case_id, 1)
        require(failed["status"] == "FAIL", f"{case_id} hid a failed supporting check")

    require(acceptance.summarize_case_runs([
        {"attempt": 1, "status": "PASS"}
    ]) == "PASS", "one complete local pass did not aggregate to PASS")
    require(acceptance.summarize_case_runs([
        {"attempt": attempt, "status": "PASS"} for attempt in (1, 2, 3)
    ]) == "NOT_READY", "repeated attempts were accepted under the single-run policy")
    require(acceptance.summarize_case_runs([]) == "NOT_READY",
            "missing current run was incorrectly promoted")
    require(acceptance.summarize_case_runs([
        {"attempt": 1, "status": "FAIL"}
    ]) == "FAIL", "a failed current run was hidden")


def test_architecture_gate_controls_overall_status():
    report = acceptance.finalize_report(make_report())
    require(report["local_verification_status"] == "PASS", "all local case runs were not recorded as passing")
    require(report["status"] == "NOT_READY", "local evidence incorrectly passed the overall stage without GitHub gates")
    require(all(case["status"] == "PASS" and len(case["runs"]) == 1
                and all(run["status"] == "PASS" for run in case["runs"])
                for case in report["tests"].values()), "original cases do not report local full-run PASS")
    require([gate["status"] for gate in report["ci_gates"]["required"]] == ["NOT_RUN", "NOT_RUN"],
            "fresh report claims an architecture gate ran")

    partial = make_report()
    acceptance.finalize_report(partial, ci_runner="ubuntu-24.04")
    require(partial["ci_gates"]["required"][0]["status"] == "PASS", "current amd64 gate was not recorded")
    require(partial["ci_gates"]["required"][1]["status"] == "NOT_RUN", "unrun arm64 gate was not preserved")
    require(partial["status"] == "NOT_READY", "one passing architecture incorrectly passed the stage")

    complete = make_report()
    for gate in complete["ci_gates"]["required"]:
        gate["status"] = "PASS"
    acceptance.finalize_report(complete)
    require(complete["status"] == "PASS" and complete["verification_status"] == "PASS",
            "both required architecture gates did not release the overall stage")


def test_browser_evidence_is_compact_and_size_bounded():
    report = acceptance.finalize_report(make_report())
    serialized = json.dumps(report, ensure_ascii=False, indent=2)
    require(len(serialized.encode("utf-8")) < 1024 * 1024, "compact S03 report exceeded the 1 MiB limit")
    browser_checks = [check for attempt in report["checks"] for check in attempt["results"]
                      if check["name"] == "real_browser_guest_dashboard"]
    require(len(browser_checks) == 1, "expected one compact browser check in the complete local run")
    for check in browser_checks:
        require(len(check["engines"]) == 3, "compact report omitted a browser engine")
        for engine in check["engines"]:
            require("websocketFrames" not in engine and "domSamples" not in engine and "sampleToDOM" not in engine,
                    "raw browser frame/sample arrays leaked into the report")
            require(engine["latency_stats"].get("minMillis") == 0,
                    "compact browser summary omitted the minimum latency")
            require(engine["unmatched_samples"] == 25, "compact browser summary misreported unmatched samples")
            require(len(engine["viewports"]) == 3
                    and pathlib.Path(engine["evidence"]).name == f"engine-{engine['engine']}.json",
                    "compact browser summary omitted viewports or raw evidence link")


if __name__ == "__main__":
    test_per_attempt_and_single_run_statuses()
    test_architecture_gate_controls_overall_status()
    test_browser_evidence_is_compact_and_size_bounded()
    print("S03 report derivation self-test: PASS")
