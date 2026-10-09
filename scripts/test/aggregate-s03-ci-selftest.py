#!/usr/bin/env python3
"""Exercise S03 CI shard aggregation without GitHub or guest prerequisites."""

import copy
import contextlib
import importlib.util
import io
import json
import pathlib
import tempfile

ROOT = pathlib.Path(__file__).resolve().parents[2]
SCRIPT = ROOT / "scripts/aggregate-s03-ci.py"
SPEC = importlib.util.spec_from_file_location("nodedance_aggregate_s03_ci", SCRIPT)
AGGREGATOR = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(AGGREGATOR)

RUN_ID = "987654321"
ATTEMPT = "2"
SHA = "a" * 40


def require(condition, message):
    if not condition:
        raise AssertionError(message)


def shard(runner, architecture):
    local_run_id = f"local-{architecture}"
    evidence_root = f".artifacts/logs/acceptance-s03/{local_run_id}"
    gate = {
        "runner": runner,
        "architecture": architecture,
        "status": "PASS",
        "run_id": local_run_id,
        "evidence_root": evidence_root,
        "github_run_id": RUN_ID,
        "github_run_attempt": ATTEMPT,
        "github_sha": SHA,
    }
    required = [
        {"runner": "ubuntu-24.04", "architecture": "amd64", "status": "NOT_RUN"},
        {"runner": "ubuntu-24.04-arm", "architecture": "arm64", "status": "NOT_RUN"},
    ]
    required[0 if architecture == "amd64" else 1] = gate
    full_checks = []
    test_records = {case_id: {"status": "PASS", "local_status": "PASS", "runs": []}
                    for case_id in AGGREGATOR.CASE_IDS}
    for attempt in (1, 2, 3):
        check_results = [
            {
                "name": name,
                "status": "PASS",
                "evidence": f"{evidence_root}/attempt-{attempt}/{name}.json",
            }
            for name in AGGREGATOR.CHECK_CASES
        ]
        full_checks.append({"attempt": attempt, "results": check_results})
        for case_id in AGGREGATOR.CASE_IDS:
            supporting = [item for item in check_results
                          if case_id in AGGREGATOR.CHECK_CASES[item["name"]]]
            test_records[case_id]["runs"].append({
                "attempt": attempt,
                "status": "PASS",
                "evidence": [item["evidence"] for item in supporting],
                "partial_checks": [
                    {"name": item["name"], "status": item["status"]} for item in supporting
                ],
            })
    return {
        "schema": 1,
        "stage": "S03",
        "mode": "full",
        "run_id": local_run_id,
        "updated_at": "2026-10-08T12:05:00+00:00",
        "status": "NOT_READY",
        "verification_status": "NOT_READY",
        "local_verification_status": "PASS",
        "repeat_required": 3,
        "repeat_requested": 3,
        "evidence_root": evidence_root,
        "environment": {
            "go": "go version go1.26.8 linux/" + architecture,
            "goarch": "x86_64" if architecture == "amd64" else "aarch64",
            "guest_probe_enabled": True,
            "web_engines": ["chromium", "webkit", "firefox"],
            "ci_runner": runner,
        },
        "ci": {
            "stage": "S03",
            "provider": "github-actions",
            "job": "s03",
            "runner": runner,
            "run_id": RUN_ID,
            "run_attempt": ATTEMPT,
            "sha": SHA,
            "job_key": f"S03-{RUN_ID}-attempt-{ATTEMPT}-s03-{runner}-engine-not-applicable",
            "initialized_at": "2026-10-08T12:00:00+00:00",
        },
        "ci_gates": {"required": required},
        "checks": full_checks,
        "tests": test_records,
    }


def rejected(report, *, runner="ubuntu-24.04", run_id=RUN_ID, run_attempt=ATTEMPT, sha=SHA):
    try:
        AGGREGATOR.validate_shard(report, runner=runner, run_id=run_id,
                                  run_attempt=run_attempt, sha=sha)
    except AGGREGATOR.AggregationError:
        return
    raise AssertionError("invalid S03 shard was accepted")


def test_aggregate_both_current_architectures():
    amd64 = shard("ubuntu-24.04", "amd64")
    arm64 = shard("ubuntu-24.04-arm", "arm64")
    original_amd64 = copy.deepcopy(amd64)
    original_arm64 = copy.deepcopy(arm64)
    result = AGGREGATOR.aggregate_reports(
        amd64, arm64, run_id=RUN_ID, run_attempt=ATTEMPT, sha=SHA,
        amd64_artifact="nodedance-s03-987654321-2-ubuntu-24.04",
        arm64_artifact="nodedance-s03-987654321-2-ubuntu-24.04-arm",
    )
    require(result["status"] == "PASS" and result["verification_status"] == "PASS",
            "two passing architecture gates did not produce the aggregate S03 PASS")
    require([item["status"] for item in result["ci_gates"]["required"]] == ["PASS", "PASS"],
            "aggregate report omitted a passing architecture gate")
    require(set(result["architecture_runs"]) == {"amd64", "arm64"},
            "aggregate report did not retain distinct architecture evidence")
    require(all(set(case["architecture_runs"]) == {"amd64", "arm64"}
                for case in result["tests"].values()),
            "aggregate case evidence omitted one architecture")
    require(amd64 == original_amd64 and arm64 == original_arm64,
            "aggregation mutated the downloaded shard reports")
    require(result["aggregation"]["github_run_id"] == RUN_ID
            and result["aggregation"]["github_run_attempt"] == ATTEMPT
            and result["aggregation"]["github_sha"] == SHA,
            "aggregate report lost its workflow identity")


def test_missing_stale_or_mismatched_shards_are_rejected():
    base = shard("ubuntu-24.04", "amd64")
    changed = copy.deepcopy(base)
    changed["ci"]["run_id"] = "old-run"
    rejected(changed)

    changed = copy.deepcopy(base)
    changed["ci"]["run_attempt"] = "1"
    rejected(changed)

    changed = copy.deepcopy(base)
    changed["ci"]["sha"] = "b" * 40
    rejected(changed)

    changed = copy.deepcopy(base)
    changed["updated_at"] = "2026-10-08T11:59:59+00:00"
    rejected(changed)

    changed = copy.deepcopy(base)
    changed["ci_gates"]["required"] = changed["ci_gates"]["required"][:1]
    rejected(changed)

    changed = copy.deepcopy(base)
    changed["ci_gates"]["required"][0]["status"] = "NOT_RUN"
    rejected(changed)

    changed = copy.deepcopy(base)
    changed["tests"][next(iter(changed["tests"]))]["runs"].pop()
    rejected(changed)

    changed = copy.deepcopy(base)
    changed["checks"][1]["results"] = [
        item for item in changed["checks"][1]["results"] if item["name"] != "metrics_race"
    ]
    rejected(changed)

    changed = copy.deepcopy(base)
    case_id = next(iter(changed["tests"]))
    changed["tests"][case_id]["runs"][1]["partial_checks"] = [
        item for item in changed["tests"][case_id]["runs"][1]["partial_checks"]
        if item["name"] != "metrics_race"
    ]
    rejected(changed)

    changed = copy.deepcopy(base)
    changed["checks"][2]["results"].append({
        "name": "unexpected_check", "status": "PASS", "evidence": ".artifacts/extra.json"
    })
    rejected(changed)

    changed = copy.deepcopy(base)
    changed["status"] = "PASS"
    rejected(changed)

    arm64 = shard("ubuntu-24.04-arm", "arm64")
    rejected(arm64, runner="ubuntu-24.04")
    try:
        AGGREGATOR.aggregate_reports(
            base, arm64, run_id=RUN_ID, run_attempt=ATTEMPT, sha=SHA,
            amd64_artifact="nodedance-s03-old-run-ubuntu-24.04",
        )
    except AGGREGATOR.AggregationError:
        pass
    else:
        raise AssertionError("aggregate accepted an artifact name from a stale workflow run")


def test_cli_reads_both_reports_and_writes_atomically():
    amd64 = shard("ubuntu-24.04", "amd64")
    arm64 = shard("ubuntu-24.04-arm", "arm64")
    with tempfile.TemporaryDirectory(prefix="nodedance-s03-aggregate-") as temporary:
        root = pathlib.Path(temporary)
        amd64_path = root / "amd64.json"
        arm64_path = root / "arm64.json"
        output = root / "reports/S03.json"
        amd64_path.write_text(json.dumps(amd64), encoding="utf-8")
        arm64_path.write_text(json.dumps(arm64), encoding="utf-8")
        args = [
            "--amd64-report", str(amd64_path),
            "--arm64-report", str(arm64_path),
            "--run-id", RUN_ID,
            "--run-attempt", ATTEMPT,
            "--sha", SHA,
            "--output", str(output),
        ]
        require(AGGREGATOR.main(args) == 0, "aggregate CLI rejected two valid reports")
        combined = json.loads(output.read_text(encoding="utf-8"))
        require(combined["status"] == "PASS" and combined["ci_gates"]["required"][1]["status"] == "PASS",
                "aggregate CLI output did not retain the dual-architecture PASS")
        previous = output.read_text(encoding="utf-8")
        stale_args = [
            "--amd64-report", str(amd64_path),
            "--arm64-report", str(arm64_path),
            "--run-id", "stale-run",
            "--run-attempt", ATTEMPT,
            "--sha", SHA,
            "--output", str(output),
        ]
        with contextlib.redirect_stderr(io.StringIO()):
            stale_status = AGGREGATOR.main(stale_args)
        require(stale_status == 1,
                "aggregate CLI accepted reports from a different workflow run")
        require(output.read_text(encoding="utf-8") == previous,
                "a failed aggregation overwrote the prior report")

        # A current, otherwise successful report must not survive if one full
        # attempt omits a required named check. The previously written PASS is
        # preserved byte-for-byte, rather than being replaced or recreated.
        arm64["checks"][1]["results"] = [
            item for item in arm64["checks"][1]["results"] if item["name"] != "metrics_race"
        ]
        arm64_path.write_text(json.dumps(arm64), encoding="utf-8")
        with contextlib.redirect_stderr(io.StringIO()):
            incomplete_status = AGGREGATOR.main(args)
        require(incomplete_status == 1,
                "aggregate CLI accepted an attempt missing a required named check")
        require(output.read_text(encoding="utf-8") == previous,
                "an incomplete shard overwrote the existing aggregate PASS")

        arm64 = shard("ubuntu-24.04-arm", "arm64")
        case_id = next(iter(arm64["tests"]))
        arm64["tests"][case_id]["runs"][1]["partial_checks"] = [
            item for item in arm64["tests"][case_id]["runs"][1]["partial_checks"]
            if item["name"] != "metrics_race"
        ]
        arm64_path.write_text(json.dumps(arm64), encoding="utf-8")
        with contextlib.redirect_stderr(io.StringIO()):
            missing_case_status = AGGREGATOR.main(args)
        require(missing_case_status == 1,
                "aggregate CLI accepted a case attempt missing a required partial check")
        require(output.read_text(encoding="utf-8") == previous,
                "a case with missing evidence overwrote the existing aggregate PASS")
        try:
            AGGREGATOR.read_report(str(root / "missing.json"))
        except AGGREGATOR.AggregationError:
            pass
        else:
            raise AssertionError("aggregate CLI input reader accepted a missing architecture report")


def main():
    test_aggregate_both_current_architectures()
    test_missing_stale_or_mismatched_shards_are_rejected()
    test_cli_reads_both_reports_and_writes_atomically()
    print("S03 CI aggregation safeguards PASS: same-run SHA/attempt, both unique architecture gates, complete three-run evidence, fail-closed report write")


if __name__ == "__main__":
    main()
