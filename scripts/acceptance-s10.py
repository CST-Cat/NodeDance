#!/usr/bin/env python3
"""Run S10 component checks while preserving honest formal acceptance status.

Mock/browser and package tests cannot substitute for a real Core-Agent-host
filesystem run, 100 MiB/1 GiB memory measurements, or the deployed systemd
write allowlist. Those normative cases therefore remain NOT_READY.
"""

from __future__ import annotations

import argparse
import datetime as dt
import json
import pathlib
import re
import subprocess
import sys
import uuid


ROOT = pathlib.Path(__file__).resolve().parents[1]
REGISTRY = json.loads((ROOT / "tests/registry.json").read_text(encoding="utf-8"))
STAGE = next(stage for stage in REGISTRY["stages"] if stage["id"] == "S10")
REPORT = ROOT / "reports/stages/S10.json"
STATUS = ROOT / "reports/status.json"
RUN_ID = dt.datetime.now(dt.timezone.utc).strftime("%Y%m%dT%H%M%SZ") + "-" + uuid.uuid4().hex[:8]
EVIDENCE = ROOT / ".artifacts/logs/acceptance-s10" / RUN_ID
CORE_TESTS = (
    "TestFileRoutesRequireAdministratorSession|TestLogoutCancelsSessionBoundFileTransfer|"
    "TestRevocationClosesStalledUploadBodyAndCancelsAgentTransfer|"
    "TestCanceledDispatchedFileWriteReturnsUnknownAndCancelsAgent"
)
NOT_READY = (
    "No real Core-Agent-host filesystem acceptance was executed. The 100 MiB/1 GiB RSS comparison, "
    "systemd ProtectSystem/ProtectHome write-allowlist, and durable S05 task-store integration "
    "remain unverified; component and mocked browser tests do not establish these cases."
)


def timestamp() -> str:
    return dt.datetime.now(dt.timezone.utc).isoformat()


def run(label: str, command: list[str], timeout: int = 600) -> dict[str, object]:
    safe = re.sub(r"[^A-Za-z0-9_.-]+", "_", label)
    logfile = EVIDENCE / f"{safe}.log"
    logfile.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
    with logfile.open("w", encoding="utf-8") as stream:
        stream.write("$ " + " ".join(command) + "\n")
        stream.flush()
        try:
            result = subprocess.run(command, cwd=ROOT, stdout=stream, stderr=subprocess.STDOUT,
                                    text=True, timeout=timeout)
            code = result.returncode
        except subprocess.TimeoutExpired:
            stream.write(f"\nTIMEOUT after {timeout}s\n")
            code = 124
    check = {"name": label, "status": "PASS" if code == 0 else "FAIL", "exit_code": code,
             "command": command, "evidence": str(logfile.relative_to(ROOT))}
    print(f"S10 {label}: {check['status']} exit={code} evidence={check['evidence']}", flush=True)
    return check


def save_report(checks: list[dict[str, object]]) -> None:
    failed = any(check["status"] != "PASS" for check in checks)
    stage_status = "FAIL" if failed else "NOT_READY"
    reason = "S10 component check failed; inspect the recorded log." if failed else NOT_READY
    tests = {}
    for case in STAGE.get("tests", []):
        tests[case["id"]] = {
            "status": "NOT_READY",
            "action": case["action"],
            "expected": case["expected"],
            "environment": case["environment"],
            "evidence_required": case["evidence"],
            "runs": [],
            "reason": reason if failed else NOT_READY,
        }
    report = {
        "schema": 1,
        "stage": "S10",
        "mode": "component-and-mock-e2e",
        "status": stage_status,
        "run_id": RUN_ID,
        "updated_at": timestamp(),
        "reason": reason,
        "checks": checks,
        "tests": tests,
    }
    REPORT.parent.mkdir(parents=True, exist_ok=True)
    REPORT.write_text(json.dumps(report, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    summary = json.loads(STATUS.read_text(encoding="utf-8")) if STATUS.exists() else {"schema": 1, "stages": {}}
    summary.setdefault("stages", {})["S10"] = {
        "status": stage_status, "mode": report["mode"], "run_id": RUN_ID,
        "updated_at": report["updated_at"], "reason": reason,
    }
    summary["updated_at"] = report["updated_at"]
    STATUS.parent.mkdir(parents=True, exist_ok=True)
    STATUS.write_text(json.dumps(summary, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--mode", choices=("full", "integration", "e2e"), required=True)
    parser.add_argument("--repeat", type=int, default=1)
    parser.add_argument("--project", action="append", choices=("chromium", "webkit", "firefox"))
    args = parser.parse_args()
    if not 1 <= args.repeat <= 3:
        parser.error("--repeat must be between 1 and 3")

    checks: list[dict[str, object]] = []
    if args.mode in ("full", "integration"):
        unit = ["go", "test", "-count=1", "./internal/protocol", "./internal/agent/files", "./internal/core/audit", "./internal/core/config"]
        core = ["go", "test", "./internal/core/server", "-run", f"^({CORE_TESTS})$", "-count=1"]
        for attempt in range(1, args.repeat + 1):
            checks.append(run(f"go-components-{attempt}", unit))
            checks.append(run(f"go-core-session-{attempt}", core))
            if any(check["status"] != "PASS" for check in checks[-2:]):
                break
    if args.mode in ("full", "e2e"):
        checks.append(run("web-typecheck", ["pnpm", "--dir", "web", "run", "typecheck"]))
        checks.append(run("web-build", ["pnpm", "--dir", "web", "run", "build"]))
        browser_command = ["pnpm", "--dir", "web", "exec", "playwright", "test", "--config", "playwright.s10.config.ts"]
        for project in args.project or []:
            browser_command.extend(("--project", project))
        for attempt in range(1, args.repeat + 1):
            checks.append(run(f"playwright-{attempt}", browser_command, timeout=300))
            if checks[-1]["status"] != "PASS":
                break
    save_report(checks)
    return 1 if any(check["status"] != "PASS" for check in checks) else 0


if __name__ == "__main__":
    raise SystemExit(main())
