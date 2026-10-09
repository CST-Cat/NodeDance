#!/usr/bin/env python3
"""Run the bounded S17-07 local recovery candidate and keep S17 NOT_READY."""

from __future__ import annotations

import argparse
import datetime as dt
import json
import pathlib
import subprocess
import uuid

ROOT = pathlib.Path(__file__).resolve().parents[1]
REPORTS = ROOT / "reports/stages"
STATUS_FILE = ROOT / "reports/status.json"
COMMAND = ["make", "test-candidate-s17"]
TIMEOUT_SECONDS = 2400


def timestamp() -> str:
    return dt.datetime.now(dt.timezone.utc).isoformat()


def run(mode: str) -> int:
    if mode not in {"full", "integration", "e2e"}:
        raise ValueError(f"invalid S17 mode: {mode}")
    run_id = dt.datetime.now(dt.timezone.utc).strftime("%Y%m%dT%H%M%SZ") + "-" + uuid.uuid4().hex[:8]
    updated_at = timestamp()
    evidence_dir = ROOT / ".artifacts/logs/acceptance-s17" / run_id
    evidence_dir.mkdir(parents=True, exist_ok=False)
    log_path = evidence_dir / "candidate.log"
    with log_path.open("w", encoding="utf-8") as log:
        for probe in (["go", "version"], ["node", "--version"], ["pnpm", "--version"]):
            try:
                result = subprocess.run(probe, cwd=ROOT, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, text=True, timeout=15)
                log.write(f"$ {' '.join(probe)}\n{result.stdout.rstrip()}\n")
            except (OSError, subprocess.TimeoutExpired) as error:
                log.write(f"$ {' '.join(probe)}\nUNAVAILABLE: {error}\n")
        log.write("\n$ make test-candidate-s17\n")
        log.flush()
        try:
            result = subprocess.run(COMMAND, cwd=ROOT, stdout=log, stderr=subprocess.STDOUT, text=True, timeout=TIMEOUT_SECONDS)
            exit_code = result.returncode
        except subprocess.TimeoutExpired:
            log.write(f"\nTIMEOUT after {TIMEOUT_SECONDS}s\n")
            exit_code = 124

    candidate_status = "PASS" if exit_code == 0 else "FAIL"
    evidence = str(log_path.relative_to(ROOT))
    previous_path = REPORTS / "S17.json"
    previous = json.loads(previous_path.read_text(encoding="utf-8")) if previous_path.is_file() else None
    registry = json.loads((ROOT / "tests/registry.json").read_text(encoding="utf-8"))
    stage = next(item for item in registry["stages"] if item["id"] == "S17")
    tests = {}
    for case in stage.get("tests", []) + stage.get("supplemental_tests", []):
        tests[case["id"]] = {
            "status": "NOT_READY",
            "action": case["action"],
            "expected": case["expected"],
            "environment": case["environment"],
            "evidence_required": case["evidence"],
            "runs": [],
            "reason": "本次只执行本机候选闭环；完整规范环境及本阶段其他验收证据未完成。",
        }
    tests["S17-07"]["runs"] = [{
        "run_id": run_id,
        "attempt": 1,
        "status": candidate_status,
        "command": "make test-candidate-s17",
        "result": (
            "本机候选闭环通过：真实 Core HTTPS API 注册真实 Agent runtime；保存 dashboard 偏好、Agent 实际上报的历史指标、审计事件及加密 Webhook 配置；停止源 Core 后备份至 dataDir 外并恢复到全新临时 dataDir；核对 Node/Agent 身份、凭据验证器、偏好、历史、审计、加密密钥、CSRF 会话和文件权限；恢复后同一 Agent 自动重连，通知 Worker 使用恢复的密钥将 Webhook Secret 解密并发送到本次测试创建的 loopback receiver。"
            if candidate_status == "PASS"
            else "本机候选闭环失败；查看候选日志中的实际失败位置和清理结果。"
        ),
        "scope_limit": "仅为本机 Linux/tempdir 候选证据；未在独立新主机执行，也不代表 S17-07 或整体 S17 规范验收完成。",
        "evidence": evidence,
    }]
    tests["S17-07"]["reason"] = (
        "本机候选恢复闭环已通过，但未在独立新主机执行停服备份、全新主机恢复和恢复后 Agent 重连；S17-07 继续 NOT_READY。"
        if candidate_status == "PASS"
        else "本机候选恢复闭环失败，且独立新主机规范验收未执行；S17-07 继续 NOT_READY。"
    )
    report = {
        "schema": 1,
        "stage": "S17",
        "mode": mode,
        "status": "NOT_READY",
        "run_id": run_id,
        "updated_at": updated_at,
        "reason": (
            "S17-07 本机 Core/Agent 备份恢复候选已通过一次；独立新主机恢复、全阶段回归、双架构和72小时等规范证据尚未完成，整体 S17 继续 NOT_READY。"
            if candidate_status == "PASS"
            else "S17-07 本机 Core/Agent 备份恢复候选失败；整体 S17 继续 NOT_READY，详见原始日志。"
        ),
        "candidate_status": candidate_status,
        "candidate_checks": [{
            "name": "S17-07 local Core/Agent backup-recovery candidate",
            "status": candidate_status,
            "command": COMMAND,
            "exit_code": exit_code,
            "evidence": evidence,
            "invocations": 1,
        }],
        "tests": tests,
    }
    preflight_dir = ROOT / ".artifacts/logs/acceptance-s17/setup-preflight"
    setup_failure = preflight_dir / "missing-frontend-assets.log"
    frontend_build = preflight_dir / "frontend-build.log"
    if setup_failure.is_file() or frontend_build.is_file():
        report["setup_preflight_evidence"] = {
            "note": "The first compile-only check encountered the expected missing embedded frontend dist; the failure and subsequent frontend build logs are preserved separately.",
            "missing_assets_check": str(setup_failure.relative_to(ROOT)) if setup_failure.is_file() else None,
            "frontend_build": str(frontend_build.relative_to(ROOT)) if frontend_build.is_file() else None,
        }
    if previous:
        report["historical_stage_report"] = previous
        report["historical_evidence_note"] = "既有 S17 报告作为历史证据原样保留；本次规范用例状态仅基于本次证据。"
    REPORTS.mkdir(parents=True, exist_ok=True)
    (REPORTS / "S17.json").write_text(json.dumps(report, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    overall = json.loads(STATUS_FILE.read_text(encoding="utf-8")) if STATUS_FILE.exists() else {"schema": 1, "stages": {}}
    overall.setdefault("stages", {})["S17"] = {
        "status": "NOT_READY", "mode": mode, "run_id": run_id,
        "updated_at": updated_at, "reason": report["reason"],
    }
    overall["updated_at"] = updated_at
    STATUS_FILE.parent.mkdir(parents=True, exist_ok=True)
    STATUS_FILE.write_text(json.dumps(overall, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    print(f"S17: candidate={candidate_status}, stage=NOT_READY; report=reports/stages/S17.json; evidence={evidence}")
    return 2 if candidate_status == "PASS" else 1


if __name__ == "__main__":
    parser = argparse.ArgumentParser()
    parser.add_argument("--mode", choices=("full", "integration", "e2e"), required=True)
    raise SystemExit(run(parser.parse_args().mode))
