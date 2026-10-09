"""Run one focused candidate target without claiming normative stage acceptance."""

from __future__ import annotations

import datetime as dt
import json
import pathlib
import subprocess
import uuid
from typing import Callable


ROOT = pathlib.Path(__file__).resolve().parents[1]
REGISTRY = json.loads((ROOT / "tests/registry.json").read_text(encoding="utf-8"))
REPORTS = pathlib.Path("reports/stages")
STATUS_FILE = pathlib.Path("reports/status.json")
CANDIDATE_TARGETS = {
    "S11": "test-candidate-s11",
    "S14": "test-candidate-s14",
    "S15": "test-candidate-s15",
    "S16": "test-candidate-s16",
}
TIMEOUT_SECONDS = 2400


def timestamp() -> str:
    return dt.datetime.now(dt.timezone.utc).isoformat()


def candidate_command(stage: str) -> list[str]:
    try:
        target = CANDIDATE_TARGETS[stage]
    except KeyError as error:
        raise ValueError(f"no candidate target is registered for {stage}") from error
    return ["make", target]


def stage_cases(stage: str, registry: dict | None = None) -> list[dict]:
    data = REGISTRY if registry is None else registry
    item = next(item for item in data["stages"] if item["id"] == stage)
    return item.get("tests", []) + item.get("supplemental_tests", [])


def _historical_snapshot(previous: dict | None) -> dict | None:
    if not previous:
        return None
    if "historical_candidate_report" in previous:
        return previous["historical_candidate_report"]
    return {
        key: value
        for key, value in previous.items()
        if key not in {"historical_candidate_report"}
    }


def build_candidate_report(
    *,
    stage: str,
    mode: str,
    run_id: str,
    updated_at: str,
    check: dict,
    previous: dict | None = None,
    registry: dict | None = None,
) -> dict:
    passed = check["status"] == "PASS"
    status = "NOT_READY" if passed else "FAIL"
    reason = (
        f"The focused {stage} candidate target passed once. Its component and responsive-browser checks "
        "do not complete every normative acceptance case; each original case remains NOT_READY pending its listed evidence."
        if passed
        else f"The focused {stage} candidate target failed; see its exact command and log. Normative cases were not reported as executed."
    )
    cases = {
        case["id"]: {
            "status": "NOT_READY",
            "action": case["action"],
            "expected": case["expected"],
            "environment": case["environment"],
            "evidence_required": case["evidence"],
            "runs": [],
            "reason": (
                f"The candidate command {'passed' if passed else 'failed'}; this run did not execute the full normative environment for this case. "
                "See candidate_checks and the preserved prior report for candidate and historical evidence."
            ),
        }
        for case in stage_cases(stage, registry)
    }
    report = {
        "schema": 1,
        "stage": stage,
        "mode": mode,
        "status": status,
        "run_id": run_id,
        "updated_at": updated_at,
        "reason": reason,
        "candidate_status": check["status"],
        "candidate_checks": [check],
        "tests": cases,
    }
    historical = _historical_snapshot(previous)
    if historical is not None:
        report["historical_candidate_report"] = historical
        report["historical_evidence_note"] = (
            "The previous stage report is preserved verbatim as historical evidence. Current normative cases above are NOT_READY until this run includes their required evidence."
        )
    if previous and previous.get("candidate_checks"):
        report["candidate_run_history"] = previous.get("candidate_run_history", []) + [
            {
                "run_id": previous.get("run_id"),
                "updated_at": previous.get("updated_at"),
                "stage_status": previous.get("status"),
                "candidate_status": previous.get("candidate_status"),
                "candidate_checks": previous["candidate_checks"],
            }
        ]
    return report


def run_candidate_stage(
    stage: str,
    mode: str,
    *,
    root: pathlib.Path = ROOT,
    registry: dict | None = None,
    command_runner: Callable = subprocess.run,
    run_id: str | None = None,
    updated_at: str | None = None,
) -> int:
    if mode not in {"full", "integration", "e2e"}:
        raise ValueError(f"invalid mode: {mode}")
    run_id = run_id or (dt.datetime.now(dt.timezone.utc).strftime("%Y%m%dT%H%M%SZ") + "-" + uuid.uuid4().hex[:8])
    updated_at = updated_at or timestamp()
    command = candidate_command(stage)
    evidence_dir = root / ".artifacts" / "logs" / f"acceptance-{stage.lower()}" / run_id
    evidence_dir.mkdir(parents=True, exist_ok=False)
    log_path = evidence_dir / "candidate.log"
    with log_path.open("w", encoding="utf-8") as stream:
        stream.write("$ " + " ".join(command) + "\n")
        stream.flush()
        try:
            result = command_runner(
                command,
                cwd=root,
                stdout=stream,
                stderr=subprocess.STDOUT,
                text=True,
                timeout=TIMEOUT_SECONDS,
            )
            exit_code = result.returncode
        except subprocess.TimeoutExpired:
            stream.write(f"\nTIMEOUT after {TIMEOUT_SECONDS}s\n")
            exit_code = 124

    check = {
        "name": f"{stage} focused candidate target",
        "status": "PASS" if exit_code == 0 else "FAIL",
        "command": command,
        "exit_code": exit_code,
        "evidence": str(log_path.relative_to(root)),
        "invocations": 1,
    }
    previous_path = root / REPORTS / f"{stage}.json"
    previous = json.loads(previous_path.read_text(encoding="utf-8")) if previous_path.is_file() else None
    report = build_candidate_report(
        stage=stage,
        mode=mode,
        run_id=run_id,
        updated_at=updated_at,
        check=check,
        previous=previous,
        registry=registry,
    )
    report_path = root / REPORTS / f"{stage}.json"
    report_path.parent.mkdir(parents=True, exist_ok=True)
    report_path.write_text(json.dumps(report, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")

    status_path = root / STATUS_FILE
    overall = json.loads(status_path.read_text(encoding="utf-8")) if status_path.exists() else {"schema": 1, "stages": {}}
    overall.setdefault("stages", {})[stage] = {
        "status": report["status"],
        "mode": mode,
        "run_id": run_id,
        "updated_at": updated_at,
        "reason": report["reason"],
    }
    overall["updated_at"] = updated_at
    status_path.parent.mkdir(parents=True, exist_ok=True)
    status_path.write_text(json.dumps(overall, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    print(f"{stage}: candidate={check['status']}, stage={report['status']}; report={report_path.relative_to(root)}")
    return 2 if report["status"] == "NOT_READY" else 1
