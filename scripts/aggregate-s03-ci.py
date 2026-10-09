#!/usr/bin/env python3
"""Combine the two successful S03 architecture shards into a CI-only report."""

from __future__ import annotations

import argparse
import copy
import datetime as dt
import importlib.util
import json
import pathlib
import re
import sys

ROOT = pathlib.Path(__file__).resolve().parents[1]
REGISTRY = json.loads((ROOT / "tests/registry.json").read_text(encoding="utf-8"))
S03 = next(stage for stage in REGISTRY["stages"] if stage["id"] == "S03")
CASE_IDS = {case["id"] for case in S03.get("tests", []) + S03.get("supplemental_tests", [])}
_S03_ACCEPTANCE_SPEC = importlib.util.spec_from_file_location(
    "nodedance_acceptance_s03_contract", ROOT / "scripts/acceptance-s03.py"
)
if _S03_ACCEPTANCE_SPEC is None or _S03_ACCEPTANCE_SPEC.loader is None:
    raise RuntimeError("cannot load the S03 acceptance check contract")
_S03_ACCEPTANCE = importlib.util.module_from_spec(_S03_ACCEPTANCE_SPEC)
_S03_ACCEPTANCE_SPEC.loader.exec_module(_S03_ACCEPTANCE)
CHECK_CASES = _S03_ACCEPTANCE.CHECK_CASES
_EXPECTED_CHECK_NAMES = set(CHECK_CASES)
_EXPECTED_CASE_CHECK_NAMES = {
    case_id: _S03_ACCEPTANCE.expected_check_names(case_id) for case_id in CASE_IDS
}
RUNNERS = {
    "ubuntu-24.04": "amd64",
    "ubuntu-24.04-arm": "arm64",
}
MAX_REPORT_BYTES = 1024 * 1024


class AggregationError(ValueError):
    """A shard is missing, stale, inconsistent, or incomplete."""


def now() -> str:
    return dt.datetime.now(dt.timezone.utc).isoformat()


def _require(condition: bool, message: str) -> None:
    if not condition:
        raise AggregationError(message)


def _run_ids(value) -> list[int]:
    if not isinstance(value, list):
        return []
    return [item.get("attempt") for item in value if isinstance(item, dict)]


def _validate_attempt_checks(report: dict, *, runner: str) -> dict[int, dict[str, dict]]:
    """Require the exact checks emitted by one complete real S03 acceptance run."""
    checks = report.get("checks")
    _require(isinstance(checks, list) and len(checks) == 1
             and all(isinstance(attempt, dict) for attempt in checks)
             and _run_ids(checks) == [1],
             f"{runner} full acceptance checks are missing, duplicated, or out of order")
    by_attempt = {}
    for attempt in checks:
        attempt_number = attempt["attempt"]
        results = attempt.get("results")
        _require(isinstance(results, list) and len(results) == len(_EXPECTED_CHECK_NAMES)
                 and all(isinstance(item, dict) for item in results),
                 f"{runner} attempt {attempt_number} has an incomplete check set")
        names = [item.get("name") for item in results]
        _require(all(isinstance(name, str) for name in names)
                 and len(names) == len(set(names))
                 and set(names) == _EXPECTED_CHECK_NAMES,
                 f"{runner} attempt {attempt_number} has missing, duplicate, or unexpected checks")
        indexed = {}
        for item in results:
            _require(item.get("status") == "PASS",
                     f"{runner} attempt {attempt_number} check {item['name']} did not pass")
            evidence = item.get("evidence")
            _require(isinstance(evidence, str) and evidence.strip(),
                     f"{runner} attempt {attempt_number} check {item['name']} has no evidence")
            indexed[item["name"]] = item
        by_attempt[attempt_number] = indexed
    return by_attempt


def validate_shard(report: dict, *, runner: str, run_id: str,
                   run_attempt: str, sha: str) -> dict:
    """Validate one architecture's current successful, non-aggregate report."""
    _require(runner in RUNNERS, "unexpected S03 runner")
    architecture = RUNNERS[runner]
    _require(isinstance(report, dict), f"{runner} report is not a JSON object")
    _require(report.get("stage") == "S03", f"{runner} report is not for S03")
    _require(report.get("mode") == "full", f"{runner} shard was not a full run")
    _require(report.get("status") == "NOT_READY",
             f"{runner} shard must remain NOT_READY until both architecture gates pass")
    _require(report.get("verification_status") == "NOT_READY",
             f"{runner} shard claims overall verification before aggregation")
    _require(report.get("local_verification_status") == "PASS",
             f"{runner} local full-run verification did not pass")
    _require(report.get("repeat_required") == 1 and report.get("repeat_requested") == 1,
             f"{runner} report does not describe exactly one full run")

    ci = report.get("ci")
    _require(isinstance(ci, dict), f"{runner} report has not been annotated by GitHub Actions")
    for field, expected in (
        ("stage", "S03"),
        ("provider", "github-actions"),
        ("job", "s03"),
        ("runner", runner),
        ("run_id", str(run_id)),
        ("run_attempt", str(run_attempt)),
        ("sha", str(sha)),
    ):
        _require(str(ci.get(field, "")) == expected,
                 f"{runner} shard has mismatched current-run metadata: {field}")
    _require(bool(re.fullmatch(r"[0-9a-f]{40}", str(ci.get("sha", "")), re.I)),
             f"{runner} shard SHA is malformed")

    try:
        initialized_at = dt.datetime.fromisoformat(ci["initialized_at"])
        updated_at = dt.datetime.fromisoformat(report["updated_at"])
    except (KeyError, TypeError, ValueError) as error:
        raise AggregationError(f"{runner} shard timestamps are missing or malformed") from error
    _require(updated_at >= initialized_at, f"{runner} shard predates its current CI initialization")

    environment = report.get("environment")
    _require(isinstance(environment, dict) and environment.get("ci_runner") == runner,
             f"{runner} report environment does not match its CI runner")
    machine = str(environment.get("goarch", "")).lower()
    actual_arch = "amd64" if machine in {"x86_64", "amd64"} else (
        "arm64" if machine in {"aarch64", "arm64"} else "unknown"
    )
    _require(actual_arch == architecture,
             f"{runner} report was produced on {machine or 'unknown'}, expected {architecture}")

    attempt_checks = _validate_attempt_checks(report, runner=runner)

    cases = report.get("tests")
    _require(isinstance(cases, dict) and set(cases) == CASE_IDS,
             f"{runner} shard omitted or added an S03 acceptance case")
    for case_id in sorted(CASE_IDS):
        case = cases[case_id]
        _require(isinstance(case, dict)
                 and case.get("status") == "PASS"
                 and case.get("local_status") == "PASS",
                 f"{runner} {case_id} did not pass")
        runs = case.get("runs")
        _require(isinstance(runs, list) and len(runs) == 1
                 and all(isinstance(item, dict) for item in runs)
                 and _run_ids(runs) == [1]
                 and all(item.get("status") == "PASS" for item in runs),
                 f"{runner} {case_id} lacks its current passing run")
        for item in runs:
            attempt_number = item["attempt"]
            evidence = item.get("evidence")
            checks = item.get("partial_checks")
            expected_names = _EXPECTED_CASE_CHECK_NAMES.get(case_id, set())
            _require(bool(expected_names),
                     f"S03 acceptance contract has no supporting checks for {case_id}")
            _require(isinstance(checks, list)
                     and all(isinstance(check, dict) for check in checks),
                     f"{runner} {case_id} attempt {attempt_number} has malformed partial checks")
            partial_names = [check.get("name") for check in checks]
            _require(all(isinstance(name, str) for name in partial_names)
                     and len(partial_names) == len(set(partial_names))
                     and set(partial_names) == expected_names,
                     f"{runner} {case_id} attempt {attempt_number} has missing, duplicate, or unexpected partial checks")
            _require(all(check.get("status") == "PASS" for check in checks),
                     f"{runner} {case_id} attempt {attempt_number} has a non-passing partial check")
            expected_evidence = []
            for check in attempt_checks[attempt_number].values():
                if case_id in CHECK_CASES[check["name"]]:
                    expected_evidence.append(check["evidence"])
            expected_partial = [
                name for name in attempt_checks[attempt_number]
                if case_id in CHECK_CASES[name]
            ]
            _require(partial_names == expected_partial,
                     f"{runner} {case_id} attempt {attempt_number} partial checks do not match full check order")
            _require(isinstance(evidence, list) and evidence == expected_evidence,
                     f"{runner} {case_id} attempt {item['attempt']} has no evidence links")

    required = report.get("ci_gates", {}).get("required")
    _require(isinstance(required, list) and len(required) == len(RUNNERS),
             f"{runner} shard has missing or duplicate architecture gates")
    gates_by_runner = {item.get("runner"): item for item in required if isinstance(item, dict)}
    _require(set(gates_by_runner) == set(RUNNERS),
             f"{runner} shard gate list does not contain exactly amd64 and arm64")
    for gate_runner, gate_arch in RUNNERS.items():
        gate = gates_by_runner[gate_runner]
        _require(gate.get("architecture") == gate_arch,
                 f"{runner} shard has incorrect architecture metadata for {gate_runner}")
        expected_status = "PASS" if gate_runner == runner else "NOT_RUN"
        _require(gate.get("status") == expected_status,
                 f"{runner} shard gate for {gate_runner} must be {expected_status}")
    current_gate = gates_by_runner[runner]
    for field, expected in (
        ("github_run_id", str(run_id)),
        ("github_run_attempt", str(run_attempt)),
        ("github_sha", str(sha)),
        ("run_id", str(report.get("run_id", ""))),
        ("evidence_root", str(report.get("evidence_root", ""))),
    ):
        _require(str(current_gate.get(field, "")) == expected,
                 f"{runner} gate has mismatched current-run metadata: {field}")
    return current_gate


def aggregate_reports(amd64_report: dict, arm64_report: dict, *,
                      run_id: str, run_attempt: str, sha: str,
                      amd64_artifact: str = "", arm64_artifact: str = "",
                      amd64_path: str = "", arm64_path: str = "") -> dict:
    """Validate both shards and build a CI-only S03 PASS report."""
    expected_sha = str(sha)
    _require(bool(re.fullmatch(r"[0-9a-f]{40}", expected_sha, re.I)),
             "aggregate commit SHA is malformed")
    expected_amd64_artifact = f"nodedance-s03-{run_id}-{run_attempt}-ubuntu-24.04"
    expected_arm64_artifact = f"nodedance-s03-{run_id}-{run_attempt}-ubuntu-24.04-arm"
    _require(not amd64_artifact or amd64_artifact == expected_amd64_artifact,
             "amd64 artifact name does not match the current workflow run")
    _require(not arm64_artifact or arm64_artifact == expected_arm64_artifact,
             "arm64 artifact name does not match the current workflow run")
    amd64_gate = validate_shard(
        amd64_report, runner="ubuntu-24.04", run_id=str(run_id),
        run_attempt=str(run_attempt), sha=expected_sha,
    )
    arm64_gate = validate_shard(
        arm64_report, runner="ubuntu-24.04-arm", run_id=str(run_id),
        run_attempt=str(run_attempt), sha=expected_sha,
    )

    result = copy.deepcopy(amd64_report)
    result["status"] = "PASS"
    result["verification_status"] = "PASS"
    result["local_verification_status"] = "PASS"
    result["updated_at"] = now()
    result["reason"] = (
        "All original and supplemental S03 cases passed one complete real Core-Agent-guest-browser "
        "run on both required GitHub-hosted architectures in the same current workflow run and commit."
    )
    result["ci_runner"] = None
    result["ci_architecture"] = None
    result.setdefault("environment", {})["ci_runner"] = "aggregate"
    result["ci_gates"] = {"required": []}
    aggregate_gates = []
    for report, gate, runner, architecture, artifact, path in (
        (amd64_report, amd64_gate, "ubuntu-24.04", "amd64", amd64_artifact, amd64_path),
        (arm64_report, arm64_gate, "ubuntu-24.04-arm", "arm64", arm64_artifact, arm64_path),
    ):
        merged_gate = copy.deepcopy(gate)
        merged_gate["artifact"] = artifact
        merged_gate["report_path"] = path
        result["ci_gates"]["required"].append(merged_gate)
        result.setdefault("architecture_runs", {})[architecture] = {
            "runner": runner,
            "report_run_id": report["run_id"],
            "evidence_root": report.get("evidence_root", ""),
            "artifact": artifact,
            "report_path": path,
        }
        for case_id in sorted(CASE_IDS):
            result["tests"][case_id].setdefault("architecture_runs", {})[architecture] = {
                "runner": runner,
                "runs": copy.deepcopy(report["tests"][case_id]["runs"]),
            }
    result["aggregation"] = {
        "status": "PASS",
        "github_run_id": str(run_id),
        "github_run_attempt": str(run_attempt),
        "github_sha": expected_sha,
        "required_architectures": ["amd64", "arm64"],
    }
    result["ci"] = None
    return result


def read_report(path: str) -> dict:
    try:
        value = json.loads(pathlib.Path(path).read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as error:
        raise AggregationError(f"cannot read required architecture report: {path}") from error
    _require(isinstance(value, dict), f"architecture report is not an object: {path}")
    return value


def write_report(path: str, report: dict) -> None:
    serialized = json.dumps(report, ensure_ascii=False, indent=2) + "\n"
    _require(len(serialized.encode("utf-8")) < MAX_REPORT_BYTES,
             "aggregated S03 report exceeds the 1 MiB evidence limit")
    target = pathlib.Path(path)
    target.parent.mkdir(parents=True, exist_ok=True)
    temporary = target.with_name(target.name + ".tmp")
    temporary.write_text(serialized, encoding="utf-8")
    temporary.replace(target)


def main(argv=None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--amd64-report", required=True)
    parser.add_argument("--arm64-report", required=True)
    parser.add_argument("--run-id", required=True)
    parser.add_argument("--run-attempt", required=True)
    parser.add_argument("--sha", required=True)
    parser.add_argument("--output", default="reports/stages/S03.json")
    parser.add_argument("--amd64-artifact", default="")
    parser.add_argument("--arm64-artifact", default="")
    args = parser.parse_args(argv)
    try:
        report = aggregate_reports(
            read_report(args.amd64_report), read_report(args.arm64_report),
            run_id=args.run_id, run_attempt=args.run_attempt, sha=args.sha,
            amd64_artifact=args.amd64_artifact, arm64_artifact=args.arm64_artifact,
            amd64_path=args.amd64_report, arm64_path=args.arm64_report,
        )
        write_report(args.output, report)
    except (AggregationError, OSError, ValueError) as error:
        print(f"S03 CI aggregation FAIL: {error}", file=sys.stderr)
        return 1
    print(f"S03 CI aggregation PASS: report={args.output} run={args.run_id} attempt={args.run_attempt}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
