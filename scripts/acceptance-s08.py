#!/usr/bin/env python3
"""Run S08 component coverage and preserve honest real-Engine readiness.

The focused Go suite exercises the image task contract with fakes. Normative
S08 acceptance remains NOT_READY until an owned Docker Engine and controlled
Registry run records actual image and credential behavior.
"""

from __future__ import annotations

import argparse
import datetime as dt
import json
import os
import pathlib
import subprocess
import sys
import uuid

ROOT = pathlib.Path(__file__).resolve().parents[1]
REGISTRY = json.loads((ROOT / "tests/registry.json").read_text())
STAGE = next(item for item in REGISTRY["stages"] if item["id"] == "S08")
COMPONENT_TESTS = [
    "TestImageTaskCanonicalIntentExcludesOneTimeRegistryCredentials",
    "TestImageDeleteIntentRequiresFullContentAddressedID",
    "TestTaskCancelRequestIsBoundToTaskJournalAndGeneration",
    "TestPullUsesEphemeralCredentialsAndVerifiesEngineState",
    "TestInterruptedPullBecomesUnknownThenReadOnlyReconciles",
    "TestImageReferenceVerificationUsesDockerDistributionNormalization",
    "TestExplicitPullCancellationWaitsForStoppedEngineAndConfirmedState",
    "TestDeleteRefusesReferencedImageAndVerifiesSuccessfulRemoval",
    "TestCancelImagePullSignalsOnlyTheRegisteredTask",
    "TestRequestImagePullCancellationUsesLiveMatchingJournal",
    "TestImageRegistryCredentialsAreOneUseAndExpireInMemory",
]
PATTERN = "^(" + "|".join(COMPONENT_TESTS) + ")$"
PACKAGES = [
    "./internal/protocol",
    "./internal/agent/images",
    "./internal/agent/taskrunner",
    "./internal/core/server",
]


def now() -> str:
    return dt.datetime.now(dt.timezone.utc).isoformat()


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--mode", choices=("full", "integration", "e2e"), required=True)
    parser.add_argument("--repeat", type=int, default=1)
    args = parser.parse_args()
    if args.repeat != 1:
        parser.error("--repeat must be 1")

    run_id = uuid.uuid4().hex
    output_dir = ROOT / ".artifacts" / "s08" / run_id
    output_dir.mkdir(parents=True, exist_ok=False)
    go_bin = os.environ.get("NODEDANCE_GO_BIN", str(ROOT / ".tools/go1.26.8/bin/go"))
    version = subprocess.run([go_bin, "version"], cwd=ROOT, text=True, capture_output=True)
    if version.returncode or "go1.26.8" not in version.stdout:
        print("S08 component checks require the repository-pinned Go toolchain.", file=sys.stderr)
        if version.stdout:
            print(version.stdout.strip(), file=sys.stderr)
        if version.stderr:
            print(version.stderr.strip(), file=sys.stderr)
        return 2

    env = os.environ.copy()
    env["GOTOOLCHAIN"] = "local"
    command = [go_bin, "test", "-count=1", "-run", PATTERN, *PACKAGES]
    log_path = output_dir / "components-1.log"
    with log_path.open("w") as stream:
        stream.write("$ " + " ".join(command) + "\n")
        stream.flush()
        result = subprocess.run(command, cwd=ROOT, env=env, stdout=stream, stderr=subprocess.STDOUT)
    runs = [{
        "attempt": 1,
        "status": "PASS" if result.returncode == 0 else "FAIL",
        "exit_code": result.returncode,
        "log": str(log_path.relative_to(ROOT)),
    }]
    component_status = "PASS" if result.returncode == 0 else "FAIL"
    status = "FAIL" if component_status == "FAIL" else "NOT_READY"
    reason = ""
    engine_attempt_note = os.environ.get("NODEDANCE_S08_ENGINE_ATTEMPT_NOTE", "").strip()
    if component_status == "FAIL":
        reason = "S08 component tests failed; see the recorded focused Go test logs."
    else:
        reason = "Component/mocked checks passed, but no S08-owned real Docker Engine and controlled Registry acceptance evidence was provided."
        if engine_attempt_note:
            reason += " Engine fixture attempt: " + engine_attempt_note

    normative = {}
    for case in STAGE["tests"]:
        case_status = status if status == "FAIL" else "NOT_READY"
        case_reason = reason or "Requires real Docker Engine/Registry evidence; component tests alone do not satisfy the normative case."
        normative[case["id"]] = {
            "status": case_status,
            "action": case["action"],
            "expected": case["expected"],
            "environment": case["environment"],
            "evidence_required": case["evidence"],
            "runs": [],
            "reason": case_reason,
        }

    report = {
        "schema": 1,
        "stage": "S08",
        "mode": args.mode,
        "status": status,
        "run_id": run_id,
        "updated_at": now(),
        "reason": reason,
        "engine_attempt": {
            "status": "NOT_READY",
            "details": engine_attempt_note or "No S08-owned Docker Engine/Registry fixture run was recorded.",
        },
        "component_tests": {"status": component_status, "runs": runs, "test_names": COMPONENT_TESTS},
        "tests": normative,
    }
    report_path = ROOT / "reports/stages/S08.json"
    report_path.write_text(json.dumps(report, ensure_ascii=False, indent=2) + "\n")
    status_path = ROOT / "reports/status.json"
    overall = json.loads(status_path.read_text()) if status_path.exists() else {"schema": 1, "stages": {}}
    overall.setdefault("stages", {})["S08"] = {
        "status": status,
        "mode": args.mode,
        "run_id": run_id,
        "updated_at": report["updated_at"],
        "reason": reason,
    }
    overall["updated_at"] = report["updated_at"]
    status_path.write_text(json.dumps(overall, ensure_ascii=False, indent=2) + "\n")
    print(f"S08 component run 1/1: {'PASS' if result.returncode == 0 else 'FAIL'} ({log_path.relative_to(ROOT)})", flush=True)
    print(f"S08: {status}; component={component_status}; normative Engine/Registry cases remain individually reported in {report_path.relative_to(ROOT)}")
    return 0 if status == "PASS" else 1 if status == "FAIL" else 2


if __name__ == "__main__":
    raise SystemExit(main())
