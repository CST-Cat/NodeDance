#!/usr/bin/env python3
"""Run real S03 acceptance and keep local results distinct from CI gates."""

import argparse
import datetime as dt
import json
import os
import pathlib
import platform
import re
import shutil
import subprocess
import sys
import uuid

ROOT = pathlib.Path(__file__).resolve().parents[1]
REGISTRY = json.loads((ROOT / "tests/registry.json").read_text())
STAGE = next(stage for stage in REGISTRY["stages"] if stage["id"] == "S03")
CASES = STAGE.get("tests", []) + STAGE.get("supplemental_tests", [])
CASE_IDS = [case["id"] for case in CASES]
REPORT = ROOT / "reports/stages/S03.json"
STATUSES = ROOT / "reports/status.json"
RUN_ID = dt.datetime.now(dt.timezone.utc).strftime("%Y%m%dT%H%M%SZ") + "-" + uuid.uuid4().hex[:8]
EVIDENCE_ROOT = ROOT / ".artifacts/logs/acceptance-s03" / RUN_ID
CI_RUNNERS = {"ubuntu-24.04": "amd64", "ubuntu-24.04-arm": "arm64"}
MAX_REPORT_BYTES = 1024 * 1024

CASE_REASON = {
    "S03-01": "A real Linux guest, production Agent collector, authenticated Core API/WSS, and dashboard are exercised together.",
    "S03-02": "The same guest's controlled CPU/resident-memory load and recovery are measured by Agent/Core and rendered in all three browsers.",
    "S03-03": "Real initial/reboot warm-up and same-ifindex MAC replacement are visible; the acceptance runner also forces the deterministic counter-decrease/reset formula test.",
    "S03-04": "Two real guest interfaces, non-duplicated aggregate, filesystem details, and 375/768/1440px dashboard layout are checked together.",
    "S03-05": "A restricted real Agent's permission-denied meminfo read, retained memory, continuing CPU/network samples, recovery, and visible dashboard reason are checked.",
    "S03-06": "A real Moby SDK list request is stalled and context-cancelled in the isolated guest while host metrics and the Core heartbeat lease progress; the live dashboard stale/unknown Docker presentation is checked.",
    "S03-07": "Real guest network isolation and higher-generation recovery are checked through Core and the dashboard.",
    "S03-08": "Real guest clock correction and kernel reboot, Core offset/boot/generation, and dashboard display are checked together.",
    "S03-SUP-01": "Real guest/browser sampling-to-DOM latency percentiles are collected under controlled load.",
}

# A failure in an executed component probe rejects the corresponding original
# case, while a passing partial probe never upgrades the original case.
CHECK_CASES = {
    "metrics_race": CASE_IDS,
    "guest_resource_safety": CASE_IDS,
    "linux_proc_filesystem": ["S03-01"],
    "real_core_agent_api_stream": ["S03-01", "S03-07", "S03-08"],
    "counter_reset_formula": ["S03-03"],
    "docker_sdk_stall_fixture": ["S03-06"],
    "real_browser_guest_dashboard": ["S03-01", "S03-02", "S03-03", "S03-04", "S03-05", "S03-06", "S03-07", "S03-08", "S03-SUP-01"],
    "isolated_guest_cpu_memory": ["S03-02"],
    "isolated_guest_clock_reboot": ["S03-08"],
    "isolated_guest_agent_load_response": ["S03-02"],
    "isolated_guest_clock_offset_core": ["S03-08"],
    "isolated_guest_network_stale_recovery_core": ["S03-07"],
    "isolated_guest_reboot_boot_generation_core": ["S03-08"],
    "isolated_guest_agent_first_sample_reset": ["S03-03"],
    "isolated_guest_interface_identity_replacement": ["S03-03"],
    "isolated_guest_multiple_interfaces_mounts": ["S03-04"],
    "isolated_guest_permission_partial_failure_recovery": ["S03-05"],
    "isolated_guest_docker_stall": ["S03-06"],
}


def expected_check_names(case_id):
    return {name for name, case_ids in CHECK_CASES.items() if case_id in case_ids}


def derive_case_attempt(checks, case_id, attempt):
    """Derive one original case from all of its mapped supporting checks."""
    relevant = [item for item in checks if case_id in CHECK_CASES.get(item.get("name"), [])]
    present_names = {item.get("name") for item in relevant}
    missing_names = sorted(expected_check_names(case_id) - present_names)
    failed = sorted({item["name"] for item in relevant if item.get("status") == "FAIL"})
    pending = sorted({item["name"] for item in relevant if item.get("status") != "PASS"})
    if failed:
        status = "FAIL"
        reason = "A current supporting check for this case failed: " + ", ".join(failed)
    elif not missing_names and relevant and all(item.get("status") == "PASS" for item in relevant):
        status = "PASS"
        reason = ""
    else:
        status = "NOT_READY"
        details = []
        if missing_names:
            details.append("missing checks: " + ", ".join(missing_names))
        if pending:
            details.append("incomplete checks: " + ", ".join(pending))
        reason = "; ".join(details) or "no supporting checks were recorded"
    return {
        "attempt": attempt,
        "status": status,
        "evidence": [item["evidence"] for item in relevant if item.get("evidence")],
        "partial_checks": [{"name": item.get("name"), "status": item.get("status")} for item in relevant],
        "reason": reason,
    }


def summarize_case_runs(runs):
    statuses = [item.get("status") for item in runs]
    if "FAIL" in statuses:
        return "FAIL"
    if ([item.get("attempt") for item in runs] == [1]
            and statuses == ["PASS"]):
        return "PASS"
    return "NOT_READY"


def ci_gates_template():
    return {
        "required": [
            {"runner": runner, "architecture": arch, "status": "NOT_RUN"}
            for runner, arch in CI_RUNNERS.items()
        ]
    }


def local_architecture(machine=None):
    value = (machine or platform.machine()).lower()
    if value in {"x86_64", "amd64"}:
        return "amd64"
    if value in {"aarch64", "arm64"}:
        return "arm64"
    return value


def compact_browser_engine_records(engines, summary_path):
    """Keep reviewable aggregates in the report and point to raw ignored artifacts."""
    compact = []
    for engine in engines:
        name = engine.get("engine", "unknown")
        raw_path = summary_path.parent / f"engine-{name}.json"
        latencies = [item.get("latencyMillis") for item in engine.get("sampleToDOM", [])
                     if isinstance(item.get("latencyMillis"), (int, float))]
        latency = dict(engine.get("latencyStats", {}))
        if latencies:
            latency["minMillis"] = min(latencies)
        compact.append({
            "engine": name,
            "status": engine.get("status", "NOT_READY"),
            "reason": engine.get("reason", ""),
            "distinct_samples": engine.get("distinctSamples", 0),
            "duplicate_snapshot_deliveries": engine.get("duplicateSnapshotDeliveries", 0),
            "dom_sample_count": len(engine.get("domSamples", [])),
            "sample_to_dom_pairs": len(engine.get("sampleToDOM", [])),
            "unmatched_samples": max(0, engine.get("distinctSamples", 0) - len(engine.get("sampleToDOM", []))),
            "latency_stats": latency,
            "load_stats": engine.get("loadStats", {}),
            "viewports": engine.get("viewportEvidence", []),
            "assertions": engine.get("assertions", {}),
            "evidence": str(raw_path.relative_to(ROOT)) if raw_path.is_relative_to(ROOT) else str(raw_path),
        })
    return compact


def compact_browser_engines(browser_data, summary_path):
    return compact_browser_engine_records(browser_data.get("engines", []), summary_path)


def compact_report_checks(report):
    for attempt in report.get("checks", []):
        for check in attempt.get("results", []):
            if check.get("name") == "real_browser_guest_dashboard" and isinstance(check.get("engines"), list):
                check["engines"] = compact_browser_engine_records(
                    check["engines"], ROOT / check.get("evidence", ""))
    return report


def reconcile_report(report):
    """Rebuild case results from current checks without changing their evidence."""
    compact_report_checks(report)
    attempts = report.get("checks", [])
    by_case = {case_id: [] for case_id in CASE_IDS}
    for entry in attempts:
        attempt = entry.get("attempt")
        if not isinstance(attempt, int):
            continue
        checks = entry.get("results", [])
        for case_id in CASE_IDS:
            by_case[case_id].append(derive_case_attempt(checks, case_id, attempt))

    for case_id, runs in by_case.items():
        record = report.setdefault("tests", {}).setdefault(case_id, {})
        record["runs"] = runs
        local_status = summarize_case_runs(runs)
        record["local_status"] = local_status
        record["status"] = local_status
        if local_status == "PASS":
            record["reason"] = "The current complete local acceptance run passed."
        elif local_status == "FAIL":
            record["reason"] = "At least one current supporting check for this original S03 case failed."
        else:
            record["reason"] = CASE_REASON[case_id] + " Local evidence is incomplete: " + "; ".join(
                f"attempt {run['attempt']}: {run['reason']}" for run in runs if run["status"] == "NOT_READY")
    local_statuses = [record["local_status"] for record in report.get("tests", {}).values()]
    if "FAIL" in local_statuses:
        report["local_verification_status"] = "FAIL"
    elif local_statuses and all(status == "PASS" for status in local_statuses):
        report["local_verification_status"] = "PASS"
    else:
        report["local_verification_status"] = "NOT_READY"
    return report


def finalize_report(report, *, any_failure=False, ci_runner=None):
    reconcile_report(report)
    local_status = report["local_verification_status"]
    report.setdefault("ci_gates", ci_gates_template())
    if ci_runner:
        gate = next(item for item in report["ci_gates"]["required"] if item["runner"] == ci_runner)
        gate.update({
            "status": "FAIL" if any_failure or local_status == "FAIL" else local_status,
            "run_id": report.get("run_id", ""),
            "evidence_root": report.get("evidence_root", ""),
            "github_run_id": os.environ.get("GITHUB_RUN_ID", ""),
            "github_run_attempt": os.environ.get("GITHUB_RUN_ATTEMPT", ""),
            "github_sha": os.environ.get("GITHUB_SHA", ""),
        })
    required_gates = report["ci_gates"].get("required", [])
    all_gates_pass = (
        {item.get("runner") for item in required_gates} == set(CI_RUNNERS)
        and all(item.get("status") == "PASS" for item in required_gates)
    )
    if any_failure or local_status == "FAIL":
        report["status"] = "FAIL"
        report["verification_status"] = "FAIL"
        report["reason"] = "At least one executed S03 component, Core/Agent, browser, or isolated guest check failed."
    elif local_status == "PASS" and all_gates_pass:
        report["status"] = "PASS"
        report["verification_status"] = "PASS"
        report["reason"] = (
            "All nine original and supplemental S03 cases passed in one complete real run on both "
            "required GitHub Actions architectures."
        )
    else:
        report["status"] = "NOT_READY"
        report["verification_status"] = "NOT_READY"
        if local_status == "PASS":
            pending = [item["runner"] for item in report["ci_gates"]["required"] if item.get("status") != "PASS"]
            report["reason"] = (
                "All nine original and supplemental S03 cases passed in one complete real local run; "
                "the overall stage remains NOT_READY until both architecture GitHub Actions gates pass. "
                "Pending gates: " + ", ".join(pending)
            )
        else:
            report["reason"] = (
                "S03 local acceptance is incomplete because one or more original cases lack a passing "
                "full run; both ubuntu-24.04/amd64 and ubuntu-24.04-arm/arm64 GitHub Actions gates are also required."
            )
    return report


def timestamp():
    return dt.datetime.now(dt.timezone.utc).isoformat()


def find_tool(name, pinned_relative):
    candidates = [ROOT / pinned_relative, ROOT.parent / "NodeDance" / pinned_relative]
    for candidate in candidates:
        if candidate.is_file() and os.access(candidate, os.X_OK):
            return candidate
    return pathlib.Path(shutil.which(name) or "")


def environment():
    go_bin = find_tool("go", ".tools/go1.26.8/bin/go")
    if not go_bin.is_file():
        raise RuntimeError("pinned Go 1.26.8 is unavailable")
    version = subprocess.run([str(go_bin), "version"], cwd=ROOT, capture_output=True, text=True, check=True).stdout.strip()
    if "go1.26.8" not in version:
        raise RuntimeError(f"Go toolchain is not locked to 1.26.8: {version}")
    env = os.environ.copy()
    env["GOTOOLCHAIN"] = "local"
    env["PATH"] = str(go_bin.parent) + os.pathsep + env.get("PATH", "")
    return go_bin, env, version


def run_logged(name, command, log_path, *, env, timeout=240):
    log_path.parent.mkdir(parents=True, exist_ok=True)
    with log_path.open("w", encoding="utf-8") as stream:
        stream.write("$ " + " ".join(map(str, command)) + "\n")
        stream.flush()
        try:
            result = subprocess.run(command, cwd=ROOT, env=env, stdout=stream,
                                    stderr=subprocess.STDOUT, timeout=timeout, check=False)
        except subprocess.TimeoutExpired:
            stream.write(f"\nTIMEOUT after {timeout}s\n")
            result = None
    return result


def go_test_status(log_path, result, expected_test=None):
    if result is None:
        return "FAIL", "required Go test exceeded its bounded timeout"
    events = []
    for line in log_path.read_text(encoding="utf-8", errors="replace").splitlines():
        try:
            event = json.loads(line)
        except json.JSONDecodeError:
            continue
        if event.get("Test") and (expected_test is None or event.get("Test") == expected_test):
            events.append(event)
    if result.returncode:
        return "FAIL", f"Go test exited {result.returncode}; see {log_path.relative_to(ROOT)}"
    if expected_test:
        outcomes = [event.get("Action") for event in events if event.get("Action") in {"pass", "fail", "skip"}]
        if "fail" in outcomes:
            return "FAIL", f"{expected_test} emitted a failure; see {log_path.relative_to(ROOT)}"
        if "skip" in outcomes:
            return "NOT_READY", f"{expected_test} was skipped; see {log_path.relative_to(ROOT)}"
        if "pass" not in outcomes:
            return "FAIL", f"{expected_test} did not emit a named result; see {log_path.relative_to(ROOT)}"
    return "PASS", ""


def record_check(checks, name, status, evidence, reason="", **extra):
    item = {"name": name, "status": status, "evidence": str(evidence.relative_to(ROOT)), "reason": reason}
    item.update(extra)
    checks.append(item)
    print(f"S03 component check {name}: {status}" + (f" ({reason})" if reason else ""), flush=True)
    return item


def run_attempt(attempt, go_bin, base_env):
    attempt_dir = EVIDENCE_ROOT / f"attempt-{attempt}"
    attempt_dir.mkdir(parents=True, exist_ok=True, mode=0o700)
    checks = []

    resource_log = attempt_dir / "guest-resource-safety.log"
    resource_command = [sys.executable, "-m", "unittest", "-v", "scripts.test.test_s03_guest_resources"]
    resource_result = run_logged("guest_resource_safety", resource_command, resource_log,
                                 env=base_env, timeout=60)
    if resource_result is None or resource_result.returncode:
        status = "FAIL"
        reason = f"guest NBD and mount ownership safety tests failed; see {resource_log.relative_to(ROOT)}"
    else:
        status, reason = "PASS", ""
    record_check(checks, "guest_resource_safety", status, resource_log, reason,
                 command=resource_command,
                 exit_code=resource_result.returncode if resource_result else None)

    metrics_log = attempt_dir / "metrics-race.jsonl"
    metrics_command = [str(go_bin), "test", "-json", "-mod=readonly", "-race", "-count=1",
                       "./internal/agent/metrics", "./internal/core/metrics", "./internal/protocol"]
    metrics_result = run_logged("metrics_race", metrics_command, metrics_log, env=base_env, timeout=360)
    status, reason = go_test_status(metrics_log, metrics_result)
    record_check(checks, "metrics_race", status, metrics_log, reason,
                 command=metrics_command, exit_code=metrics_result.returncode if metrics_result else None)

    live_env = base_env.copy()
    live_env["NODEDANCE_S03_LIVE"] = "1"
    live_log = attempt_dir / "linux-proc-filesystem.jsonl"
    live_command = [str(go_bin), "test", "-json", "-mod=readonly", "-count=1", "-v",
                    "./internal/agent/metrics", "-run", "^TestLinuxProcAndFilesystemAgreement$"]
    live_result = run_logged("linux_proc_filesystem", live_command, live_log, env=live_env, timeout=120)
    status, reason = go_test_status(live_log, live_result, "TestLinuxProcAndFilesystemAgreement")
    record_check(checks, "linux_proc_filesystem", status, live_log, reason,
                 command=live_command, exit_code=live_result.returncode if live_result else None)

    core_log = attempt_dir / "real-core-agent-api-stream.jsonl"
    core_command = [str(go_bin), "test", "-json", "-mod=readonly", "-race", "-count=1",
                    "./internal/core/server", "-run", "^TestRealAgentMetricsAPIAndDashboardStream$"]
    core_result = run_logged("real_core_agent_api_stream", core_command, core_log, env=base_env, timeout=180)
    status, reason = go_test_status(core_log, core_result, "TestRealAgentMetricsAPIAndDashboardStream")
    record_check(checks, "real_core_agent_api_stream", status, core_log, reason,
                 command=core_command, exit_code=core_result.returncode if core_result else None)

    reset_log = attempt_dir / "counter-reset-formula.jsonl"
    reset_test = "TestNetworkRateFirstSampleResetAndUnits"
    reset_command = [str(go_bin), "test", "-json", "-mod=readonly", "-count=1",
                     "./internal/agent/metrics", "-run", f"^{reset_test}$"]
    reset_result = run_logged("counter_reset_formula", reset_command, reset_log, env=base_env, timeout=120)
    status, reason = go_test_status(reset_log, reset_result, reset_test)
    record_check(checks, "counter_reset_formula", status, reset_log, reason,
                 command=reset_command, exit_code=reset_result.returncode if reset_result else None)

    docker_log = attempt_dir / "docker-sdk-stall-fixture.jsonl"
    docker_test = "TestPinnedSDKListTimeoutLeavesPingUsable"
    docker_command = [str(go_bin), "test", "-json", "-mod=readonly", "-race", "-count=1",
                      "./scripts/test/s03-docker-stall-fixture", "-run", f"^{docker_test}$"]
    docker_result = run_logged("docker_sdk_stall_fixture", docker_command, docker_log, env=base_env, timeout=120)
    status, reason = go_test_status(docker_log, docker_result, docker_test)
    record_check(checks, "docker_sdk_stall_fixture", status, docker_log, reason,
                 command=docker_command, exit_code=docker_result.returncode if docker_result else None)

    guest_log = attempt_dir / "isolated-guest.log"
    guest_command = ["bash", "scripts/test/s03-live-guest-agent.sh"]
    guest_result = run_logged("isolated_guest_and_browser", guest_command, guest_log, env=base_env, timeout=720)
    guest_text = guest_log.read_text(encoding="utf-8", errors="replace")
    guest_match = re.search(r"S03 guest evidence: (\.artifacts/work-s03/[^\s]+)", guest_text)
    guest_summary = ROOT / guest_match.group(1) / "summary.json" if guest_match else None
    guest_data = {}
    if guest_summary is not None and guest_summary.is_file():
        try:
            guest_data = json.loads(guest_summary.read_text())
        except json.JSONDecodeError:
            pass
    if guest_data.get("guest_probe_status") in {"PASS", "FAIL", "NOT_READY"}:
        guest_status = guest_data["guest_probe_status"]
        guest_reason = guest_data.get("reason", "")
    elif guest_result is None or guest_result.returncode not in {0, 2}:
        guest_status = "FAIL"
        guest_reason = f"isolated guest runner exited {guest_result.returncode if guest_result else 'after timeout'} without a readable summary"
    else:
        guest_status = "NOT_READY"
        guest_reason = f"isolated guest prerequisites were unavailable; see {guest_log.relative_to(ROOT)}"
    guest_evidence = guest_summary if guest_summary is not None and guest_summary.is_file() else guest_log

    browser_match = re.search(r"S03 browser summary: (\S+) status=(PASS|FAIL|NOT_READY)", guest_text)
    browser_summary = None
    browser_data = {}
    if browser_match:
        candidate = pathlib.Path(browser_match.group(1))
        if not candidate.is_absolute():
            candidate = ROOT / candidate
        if candidate.is_file():
            browser_summary = candidate
            try:
                browser_data = json.loads(candidate.read_text(encoding="utf-8"))
            except json.JSONDecodeError:
                browser_data = {}
    if browser_data:
        browser_status = browser_data.get("status", "FAIL")
        browser_reason = ""
        browser_engines = compact_browser_engines(browser_data, browser_summary)
        if browser_status != "PASS":
            browser_reason = "; ".join(
                f"{item.get('engine')}={item.get('status')}"
                for item in browser_engines if item.get("status") != "PASS") or "one or more browser engines did not complete live guest assertions"
    elif guest_result is None or guest_result.returncode not in {0, 2}:
        browser_status = "FAIL"
        browser_reason = f"unified live guest/browser harness failed before producing browser evidence; see {guest_log.relative_to(ROOT)}"
        browser_engines = []
    else:
        browser_status = "NOT_READY"
        browser_reason = "the unified guest/browser harness did not produce all real browser artifacts"
        browser_engines = []
    record_check(checks, "real_browser_guest_dashboard", browser_status,
                 browser_summary if browser_summary is not None else guest_log, browser_reason,
                 command=guest_command, exit_code=guest_result.returncode if guest_result else None,
                 engines=browser_engines)
    if guest_data:
        probes = guest_data.get("component_probes", {})
        for probe_name, check_name in (
                ("guest_cpu_memory_probe", "isolated_guest_cpu_memory"),
                ("guest_clock_reboot_probe", "isolated_guest_clock_reboot"),
                ("agent_load_response", "isolated_guest_agent_load_response"),
                ("agent_clock_offset_core", "isolated_guest_clock_offset_core"),
                ("agent_network_stale_recovery_core", "isolated_guest_network_stale_recovery_core"),
                ("agent_reboot_boot_generation_core", "isolated_guest_reboot_boot_generation_core"),
                ("agent_first_sample_reset", "isolated_guest_agent_first_sample_reset"),
                ("agent_interface_identity_replacement", "isolated_guest_interface_identity_replacement"),
                ("agent_multinet_mount_summary", "isolated_guest_multiple_interfaces_mounts"),
                ("agent_permission_partial_failure_recovery", "isolated_guest_permission_partial_failure_recovery"),
                ("agent_docker_stall_isolation", "isolated_guest_docker_stall")):
            probe_status = probes.get(probe_name, "NOT_READY")
            check_details = {}
            rounds = guest_data.get("rounds", [])
            if rounds:
                round_result = rounds[-1]
                if probe_name in {"agent_load_response", "agent_clock_offset_core",
                                  "agent_network_stale_recovery_core", "agent_reboot_boot_generation_core",
                                  "agent_first_sample_reset", "agent_interface_identity_replacement",
                                  "agent_multinet_mount_summary", "agent_permission_partial_failure_recovery",
                                  "agent_docker_stall_isolation"}:
                    check_details = round_result.get("guest_agent_core_probe", {}).get("checks", {}).get(probe_name, {})
                elif probe_name == "guest_cpu_memory_probe":
                    check_details = round_result.get("guest_cpu_memory_probe", {})
                elif probe_name == "guest_clock_reboot_probe":
                    check_details = round_result.get("guest_clock_reboot_probe", {})
            record_check(checks, check_name, probe_status, guest_evidence,
                         guest_data.get("reason", "") if probe_status != "PASS" else "",
                         guest_run_status=guest_status, details=check_details)
    else:
        record_check(checks, "isolated_guest_cpu_memory", guest_status, guest_evidence, guest_reason)
        record_check(checks, "isolated_guest_clock_reboot", guest_status, guest_evidence, guest_reason)

    failed_checks = {check["name"] for check in checks if check["status"] == "FAIL"}
    attempt_cases = {
        case_id: derive_case_attempt(checks, case_id, attempt)
        for case_id in CASE_IDS
    }
    return checks, attempt_cases, bool(failed_checks)


def save_report(report):
    serialized = json.dumps(report, ensure_ascii=False, indent=2) + "\n"
    if len(serialized.encode("utf-8")) >= MAX_REPORT_BYTES:
        raise ValueError(f"S03 report exceeds the {MAX_REPORT_BYTES}-byte reviewable evidence limit")
    REPORT.parent.mkdir(parents=True, exist_ok=True)
    REPORT.write_text(serialized)
    overall = json.loads(STATUSES.read_text()) if STATUSES.exists() else {"schema": 1, "stages": {}}
    overall.setdefault("stages", {})["S03"] = {
        "status": report["status"], "mode": report["mode"], "run_id": report["run_id"],
        "updated_at": report["updated_at"], "reason": report.get("reason", ""),
    }
    overall["updated_at"] = report["updated_at"]
    STATUSES.parent.mkdir(parents=True, exist_ok=True)
    STATUSES.write_text(json.dumps(overall, ensure_ascii=False, indent=2) + "\n")


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--mode", choices=("full", "integration", "e2e"), required=True)
    parser.add_argument("--repeat", type=int, default=1)
    parser.add_argument("--ci-runner", choices=tuple(CI_RUNNERS),
                        help="record this full-run result as one required GitHub architecture gate")
    args = parser.parse_args()
    if args.repeat != 1:
        parser.error("acceptance runs once; rerun the affected suite after a failure or code change")
    if args.ci_runner and args.mode != "full":
        parser.error("--ci-runner requires --mode full")
    if args.ci_runner:
        configured_runner = os.environ.get("NODEDANCE_TEST_RUNNER", "").strip()
        if configured_runner and configured_runner != args.ci_runner:
            parser.error(f"NODEDANCE_TEST_RUNNER={configured_runner} does not match --ci-runner {args.ci_runner}")
        actual_arch = local_architecture()
        expected_arch = CI_RUNNERS[args.ci_runner]
        if actual_arch != expected_arch:
            parser.error(f"--ci-runner {args.ci_runner} requires {expected_arch}, found {actual_arch}")

    try:
        go_bin, env, go_version = environment()
    except (RuntimeError, subprocess.SubprocessError) as error:
        print(f"S03 NOT_READY: {error}", file=sys.stderr)
        return 2

    EVIDENCE_ROOT.mkdir(parents=True, exist_ok=True, mode=0o700)
    report = {
        "schema": 1, "stage": "S03", "mode": args.mode, "run_id": RUN_ID,
        "updated_at": timestamp(), "status": "NOT_READY", "reason": "required end-to-end S03 cases are incomplete",
        "repeat_required": 1, "repeat_requested": args.repeat, "verification_status": "NOT_READY",
        "local_verification_status": "NOT_READY", "ci_gates": ci_gates_template(),
        "evidence_root": str(EVIDENCE_ROOT.relative_to(ROOT)),
        "environment": {"go": go_version, "goarch": platform.machine(),
                        "guest_probe_enabled": True, "web_engines": ["chromium", "webkit", "firefox"],
                        "ci_runner": args.ci_runner},
        "checks": [],
        "tests": {case["id"]: {
            "status": "NOT_READY", "action": case["action"], "expected": case["expected"],
            "environment": case["environment"], "evidence_required": case["evidence"], "runs": [],
            "reason": CASE_REASON[case["id"]],
        } for case in CASES},
    }

    any_failure = False
    for attempt in range(1, args.repeat + 1):
        print(f"S03 acceptance attempt {attempt}/{args.repeat} ({args.mode})", flush=True)
        checks, case_results, failed = run_attempt(attempt, go_bin, env)
        report["checks"].append({"attempt": attempt, "results": checks})
        any_failure = any_failure or failed
        for case_id in CASE_IDS:
            report["tests"][case_id]["runs"].append(case_results[case_id])

    finalize_report(report, any_failure=any_failure, ci_runner=args.ci_runner)
    local_status = report["local_verification_status"]
    report["updated_at"] = timestamp()
    save_report(report)
    print(f"S03 {report['status']}: report={REPORT.relative_to(ROOT)} evidence={report['evidence_root']}", flush=True)
    if report["status"] == "FAIL":
        return 1
    if args.ci_runner and local_status == "PASS":
        return 0
    return 2


if __name__ == "__main__":
    raise SystemExit(main())
