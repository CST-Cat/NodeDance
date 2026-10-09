#!/usr/bin/env python3
"""Record one exact Engine S11-06 health rollback marker without stage claims."""

from __future__ import annotations

import argparse
import json
import pathlib
import re
import sys


ROOT = pathlib.Path(__file__).resolve().parents[2]
MARKER_PREFIX = "S11_HEALTH_ROLLBACK_PASS "
TEST_NAME = "TestDINDComposeEditorCoreAgentHTTPS/health_rollback"
SHA256_RE = re.compile(r"^[a-f0-9]{64}$")
ENGINE_RE = re.compile(r"^(28|29)\.[0-9]+(?:\.[0-9]+)?$")


def extract_marker(log_path: pathlib.Path) -> dict:
    matches = []
    for line in log_path.read_text(encoding="utf-8", errors="replace").splitlines():
        marker_index = line.find(MARKER_PREFIX)
        if marker_index < 0:
            continue
        raw = line[marker_index + len(MARKER_PREFIX):].strip()
        try:
            value = json.loads(raw)
        except json.JSONDecodeError as error:
            raise ValueError("S11 health rollback marker is not valid JSON") from error
        if not isinstance(value, dict):
            raise ValueError("S11 health rollback marker must be a JSON object")
        matches.append(value)
    if len(matches) != 1:
        raise ValueError(f"expected exactly one S11 health rollback marker, found {len(matches)}")
    return matches[0]


def validate_marker(marker: dict, expected_engine: str) -> None:
    if expected_engine not in {"28", "29"}:
        raise ValueError("expected Engine must be 28 or 29")
    version = marker.get("engine_version")
    if not isinstance(version, str) or not ENGINE_RE.fullmatch(version) or not version.startswith(expected_engine + "."):
        raise ValueError(f"marker Engine version does not match Engine {expected_engine}")
    exact = {
        "schema": 1,
        "case": "S11-06",
        "test": TEST_NAME,
        "operation_status": "failed",
        "error_code": "health_failed",
        "rollback_confirmed": True,
        "verified": False,
        "affected_services": ["web"],
        "unhealthy_replacement_health": "unhealthy",
        "unhealthy_replacement_healthcheck_configured": True,
        "web_running_after": True,
        "web_healthcheck_after": False,
    }
    for field, wanted in exact.items():
        if marker.get(field) != wanted:
            raise ValueError(f"marker field {field} does not match its required value")
    for field in ("source_sha256_before", "source_sha256_after"):
        if not isinstance(marker.get(field), str) or not SHA256_RE.fullmatch(marker[field]):
            raise ValueError(f"marker field {field} is not a lowercase SHA-256 digest")
    if marker["source_sha256_before"] != marker["source_sha256_after"]:
        raise ValueError("Compose source hash changed after rollback")
    for before, after in (
        ("image_id_before", "image_id_after"),
        ("data_container_id_before", "data_container_id_after"),
        ("volume_name_before", "volume_name_after"),
        ("volume_marker_before", "volume_marker_after"),
    ):
        if not isinstance(marker.get(before), str) or not marker[before] or marker.get(before) != marker.get(after):
            raise ValueError(f"marker does not prove unchanged {before.removesuffix('_before')}")
    if not isinstance(marker.get("web_container_id_before"), str) or not marker["web_container_id_before"]:
        raise ValueError("marker is missing the original web container ID")
    if not isinstance(marker.get("web_container_id_after"), str) or marker["web_container_id_after"] == marker["web_container_id_before"]:
        raise ValueError("marker does not prove the failed web replacement was rolled back")
    if not isinstance(marker.get("unhealthy_replacement_id"), str) or marker["unhealthy_replacement_id"] in {
        marker["web_container_id_before"], marker["web_container_id_after"]
    }:
        raise ValueError("marker does not identify a separate unhealthy replacement container")
    if not isinstance(marker.get("web_port_before"), int) or marker["web_port_before"] < 1 or marker.get("web_port_after") != marker["web_port_before"]:
        raise ValueError("marker does not prove the prior web port was restored")
    if not isinstance(marker.get("project"), str) or not marker["project"] or not isinstance(marker.get("suite"), str) or not marker["suite"]:
        raise ValueError("marker is missing the owned Compose project identity")


def record_evidence(
    *,
    log_path: pathlib.Path,
    report_path: pathlib.Path,
    expected_engine: str,
    run_id: str,
    run_attempt: str,
    commit: str,
    run_url: str,
    artifact: str,
    root: pathlib.Path = ROOT,
) -> dict:
    marker = extract_marker(log_path)
    validate_marker(marker, expected_engine)
    relative_log = log_path.resolve().relative_to(root.resolve()).as_posix()
    report = json.loads(report_path.read_text(encoding="utf-8"))
    if report.get("stage") != "S11" or report.get("status") != "NOT_READY":
        raise ValueError("refusing to update a missing or non-NOT_READY S11 report")
    case = report.get("tests", {}).get("S11-06")
    if not isinstance(case, dict) or case.get("status") != "NOT_READY":
        raise ValueError("refusing to update a missing or already-completed S11-06 case")
    evidence = case.setdefault("health_rollback_candidate_evidence", [])
    if not isinstance(evidence, list):
        raise ValueError("S11-06 health rollback evidence field must be a list")
    entry = {
        "status": "PASS",
        "engine": marker["engine_version"],
        "workflow_run_id": run_id,
        "workflow_run_attempt": run_attempt,
        "commit": commit,
        "test": TEST_NAME,
        "job_url": run_url,
        "artifact": artifact,
        "evidence": relative_log,
        "marker": marker,
    }
    identity = (run_id, run_attempt, marker["engine_version"])
    evidence[:] = [
        old for old in evidence
        if (old.get("workflow_run_id"), old.get("workflow_run_attempt"), old.get("engine")) != identity
    ]
    evidence.append(entry)
    evidence.sort(key=lambda item: (item.get("workflow_run_id", ""), item.get("workflow_run_attempt", ""), item.get("engine", "")))
    engines = sorted({item.get("engine", "") for item in evidence if item.get("status") == "PASS"})
    case["reason"] = (
        "S11-06 real unhealthy healthcheck rollback candidate evidence is recorded for Engine "
        + ", ".join(engines)
        + "; this per-engine record does not complete S11-06 or the full S11 stage."
    )
    report_path.write_text(json.dumps(report, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    return report


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--engine", required=True, choices=("28", "29"))
    parser.add_argument("--log", required=True, type=pathlib.Path)
    parser.add_argument("--report", type=pathlib.Path, default=ROOT / "reports/stages/S11.json")
    parser.add_argument("--run-id", required=True)
    parser.add_argument("--run-attempt", required=True)
    parser.add_argument("--commit", required=True)
    parser.add_argument("--run-url", required=True)
    parser.add_argument("--artifact", required=True)
    args = parser.parse_args()
    try:
        record_evidence(
            log_path=args.log,
            report_path=args.report,
            expected_engine=args.engine,
            run_id=args.run_id,
            run_attempt=args.run_attempt,
            commit=args.commit,
            run_url=args.run_url,
            artifact=args.artifact,
        )
    except (OSError, ValueError, json.JSONDecodeError) as error:
        print(f"S11 health rollback evidence rejected: {error}", file=sys.stderr)
        return 1
    print(f"S11-06 per-engine health rollback evidence recorded: Engine {args.engine}; S11 remains NOT_READY")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
