#!/usr/bin/env python3
"""Run S05 component tests and write per-case local evidence.

The report always leaves the twelve normative S05 acceptance cases NOT_READY:
this runner exercises the state contract and SQLite journal only, without the
Core/Agent/Docker integration required by those cases.
"""

from __future__ import annotations

import argparse
import datetime as dt
import hashlib
import json
import os
import platform
import subprocess
import sys
from pathlib import Path


COMPONENT_CASES = {
    "S05J-01": {
        "title": "seven-state proof rules and canonical request digest",
        "tests": [
            "TestRequestDigestCanonicalAndScoped",
            "TestCanonicalJSONRejectsAmbiguousPayloads",
            "TestCanonicalJSONPreservesExactNumbers",
            "TestTaskStateTransitionProofs",
        ],
    },
    "S05J-02": {
        "title": "ten concurrent duplicate requests, key conflicts, and resource claims",
        "tests": [
            "TestEnqueueIdempotencyTenConcurrentRequestsAndResourceConflict",
            "TestEnqueueChecksTaskIDAndIdempotencyKeyTogether",
        ],
    },
    "S05J-03": {
        "title": "uncertain outcome retains its resource until verified resolution",
        "tests": ["TestUnknownRetainsResourceUntilVerifiedResolution"],
    },
    "S05J-04": {
        "title": "subprocess kill, SQLite restart recovery, and no blind replay",
        "tests": [
            "TestSubprocessKillAndRestartRecovery",
            "TestRecoverInterruptedIsNodeScoped",
            "TestJournalIdentityPersistsAcrossRestartAndChangesOnRecreation",
        ],
    },
    "S05J-05": {
        "title": "failed journal transaction prevents entry into the executor",
        "tests": ["TestFailedJournalWritePreventsBeginExecution"],
    },
    "S05J-06": {
        "title": "typed progress, redaction gate, 16 MiB log cap, and durable truncation",
        "tests": [
            "TestBoundedRedactedLogsAndTypedProgress",
            "TestLogTruncationAtExactLimitPersistsAcrossRestart",
        ],
    },
    "S05J-07": {
        "title": "raw request payload is absent from SQLite and WAL files",
        "tests": ["TestRequestPayloadSecretIsNeverStored"],
    },
    "S05J-08": {
        "title": "private SQLite directory, database, sidecars, and rejection of unsafe paths",
        "tests": ["TestJournalFilePermissionsAndWAL", "TestOpenRejectsInsecureExistingPathsWithoutChangingThem"],
    },
}

FORMAL_S05_CASES = [f"S05-{index:02d}" for index in range(1, 13)]


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--output-dir", type=Path)
    arguments = parser.parse_args()
    root = Path(__file__).resolve().parents[2]
    go_bin = os.environ.get("NODEDANCE_GO_BIN", "go")
    version = subprocess.run([go_bin, "version"], cwd=root, text=True, capture_output=True)
    if version.returncode != 0 or "go1.26.8" not in version.stdout:
        print("S05 component runner requires the pinned Go 1.26.8 toolchain.", file=sys.stderr)
        if version.stdout:
            print(version.stdout.strip(), file=sys.stderr)
        if version.stderr:
            print(version.stderr.strip(), file=sys.stderr)
        return 2

    run_id = dt.datetime.now(dt.timezone.utc).strftime("%Y%m%dT%H%M%SZ") + f"-{os.getpid()}"
    output = arguments.output_dir or root / ".artifacts" / "work-s05" / run_id
    output.mkdir(parents=True, exist_ok=False)
    case_dir = output / "cases"
    case_dir.mkdir()

    env = os.environ.copy()
    env["GOTOOLCHAIN"] = "local"
    suites: dict[str, dict[str, object]] = {}
    parse_errors: list[str] = []
    for mode, race_args in (("standard", []), ("race", ["-race"])):
        command = [
            go_bin,
            "test",
            "-json",
            "-count=1",
            "-mod=readonly",
            *race_args,
            "./internal/taskstate",
            "./internal/agent/taskjournal",
        ]
        proc = subprocess.run(command, cwd=root, env=env, text=True, capture_output=True)
        stdout_lines = proc.stdout.splitlines()
        (output / f"go-test-{mode}.jsonl").write_text("\n".join(stdout_lines) + "\n", encoding="utf-8")
        (output / f"stderr-{mode}.log").write_text(proc.stderr, encoding="utf-8")
        per_test: dict[str, list[dict[str, object]]] = {}
        for line_number, line in enumerate(stdout_lines, start=1):
            try:
                event = json.loads(line)
            except json.JSONDecodeError as error:
                parse_errors.append(f"{mode} line {line_number}: {error}")
                continue
            name = event.get("Test")
            if isinstance(name, str):
                root_test = name.split("/", 1)[0]
                per_test.setdefault(root_test, []).append(event)
        suites[mode] = {"command": command, "exit_code": proc.returncode, "per_test": per_test}

    component_results = []
    all_component_pass = not parse_errors
    for case_id, definition in COMPONENT_CASES.items():
        mode_results: dict[str, dict[str, str]] = {}
        selected_events: dict[str, list[dict[str, object]]] = {"standard": [], "race": []}
        case_pass = True
        case_fail = False
        for mode, suite in suites.items():
            per_test = suite["per_test"]
            assert isinstance(per_test, dict)
            results: dict[str, str] = {}
            for test_name in definition["tests"]:
                test_events = per_test.get(test_name, [])
                selected_events[mode].extend(test_events)
                terminal = [event.get("Action") for event in test_events if event.get("Action") in {"pass", "fail", "skip"}]
                if "fail" in terminal:
                    results[test_name] = "FAIL"
                elif "skip" in terminal:
                    results[test_name] = "NOT_READY"
                elif "pass" in terminal:
                    results[test_name] = "PASS"
                else:
                    results[test_name] = "FAIL" if suite["exit_code"] else "NOT_READY"
            mode_results[mode] = results
            if any(value == "FAIL" for value in results.values()) or suite["exit_code"] != 0:
                case_fail = True
            if any(value != "PASS" for value in results.values()):
                case_pass = False
            (case_dir / f"{case_id}-{mode}.jsonl").write_text(
                "".join(json.dumps(event, ensure_ascii=False, sort_keys=True) + "\n" for event in selected_events[mode]),
                encoding="utf-8",
            )
        status = "PASS" if case_pass else ("FAIL" if case_fail else "NOT_READY")
        if status != "PASS":
            all_component_pass = False
        component_results.append(
            {
                "id": case_id,
                "title": definition["title"],
                "status": status,
                "modes": mode_results,
                "logs": {mode: f"cases/{case_id}-{mode}.jsonl" for mode in suites},
            }
        )

    source_paths = [
        root / "docs/stages/S05-implementation.md",
        root / "internal/taskstate/state.go",
        root / "internal/taskstate/state_test.go",
        root / "internal/agent/taskjournal/journal.go",
        root / "internal/agent/taskjournal/journal_test.go",
        root / "scripts/test/s05-task-journal.py",
    ]
    tree_hash = hashlib.sha256()
    for path in source_paths:
        tree_hash.update(path.relative_to(root).as_posix().encode("utf-8") + b"\0")
        tree_hash.update(path.read_bytes())

    report = {
        "schema": 1,
        "run_id": run_id,
        "source_revision": subprocess.run(
            ["git", "rev-parse", "HEAD"], cwd=root, text=True, capture_output=True, check=True
        ).stdout.strip(),
        "host": {"system": platform.system(), "architecture": platform.machine()},
        "go_version": version.stdout.strip(),
        "implementation_tree_sha256": tree_hash.hexdigest(),
        "commands": {mode: {"command": suite["command"], "exit_code": suite["exit_code"]} for mode, suite in suites.items()},
        "component_status": "PASS" if all_component_pass else "FAIL",
        "component_cases": component_results,
        "formal_stage_status": "NOT_READY",
        "formal_stage_cases": [
            {
                "id": case_id,
                "status": "NOT_READY",
                "reason": "Core/Agent task delivery and real Docker execution/postcondition integration has not been implemented or tested by this component runner.",
            }
            for case_id in FORMAL_S05_CASES
        ],
        "parse_errors": parse_errors,
        "evidence": {
            "stdout": {mode: f"go-test-{mode}.jsonl" for mode in suites},
            "stderr": {mode: f"stderr-{mode}.log" for mode in suites},
        },
    }
    (output / "report.json").write_text(json.dumps(report, indent=2, ensure_ascii=False) + "\n", encoding="utf-8")
    print(json.dumps({"report": str(output / "report.json"), "component_status": report["component_status"], "formal_stage_status": "NOT_READY"}, ensure_ascii=False))
    exit_code = next((suite["exit_code"] for suite in suites.values() if suite["exit_code"] != 0), 0)
    return exit_code if exit_code else (0 if all_component_pass else 1)


if __name__ == "__main__":
    raise SystemExit(main())
