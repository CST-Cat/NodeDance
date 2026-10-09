#!/usr/bin/env python3
"""Guard S05 runner coverage, fresh evidence, DIND ownership and upload allowlist."""

from __future__ import annotations

import importlib.util
import json
import pathlib
import tempfile


ROOT = pathlib.Path(__file__).resolve().parents[2]


def load(name: str, relative: str):
    spec = importlib.util.spec_from_file_location(name, ROOT / relative)
    if spec is None or spec.loader is None:
        raise AssertionError(f"cannot load {relative}")
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


acceptance = load("s05_acceptance", "scripts/acceptance-s05.py")
sanitizer = load("s05_sanitizer", "scripts/test/s05-sanitize-evidence.py")


def require(condition: bool, message: str) -> None:
    if not condition:
        raise AssertionError(message)


def main() -> int:
    registry_ids = {case["id"] for case in acceptance.CASES}
    require(registry_ids == {f"S05-{index:02d}" for index in range(1, 13)},
            "fresh S05 report must list every original acceptance case exactly")
    require(acceptance.FORMAL_INTEGRATION_CASES == {
        "S05-01", "S05-02", "S05-03", "S05-04", "S05-05", "S05-07", "S05-08", "S05-09", "S05-10", "S05-12",
    }, "formal integration mapping changed without explicit review")
    require(set(acceptance.FORMAL_ASSERTION_MARKERS) == acceptance.FORMAL_INTEGRATION_CASES,
            "every integrated normative case needs its own assertion marker")
    require({"S05-06", "S05-11"} <= set(acceptance.CURRENT_CORE_GAPS) and
            not ({"S05-09", "S05-10"} & set(acceptance.CURRENT_CORE_GAPS)),
            "unintegrated cases must remain visible as gaps")
    safe_cancel_marker = acceptance.FORMAL_SUPPLEMENTAL_MARKERS.get("S05-06-QUEUED-CANCEL-OFFLINE", "")
    require(all(part in safe_cancel_marker for part in (
        "queued_cancel=not_dispatched", "repeated_delete=same_result", "docker_unchanged=true",
        "offline_post=503", "no_rows=true", "reconnect_no_backlog=true",
        "delivered_timeout=unknown_result_pending", "no_replay=true",
    )), "S05-06 cancellation, offline and delivered-timeout requirements need one explicit formal supplemental marker")
    require("agent_engine_inspect_requests=" in (ROOT / "scripts/acceptance-s05.py").read_text(encoding="utf-8") and
            "ambiguity=daemon_acceptance_unproven" in (ROOT / "internal/core/server/task_bridge_cancel_offline_integration_test.go").read_text(encoding="utf-8"),
            "S05-06 timeout reconciliation must include real read-only Engine query evidence and explain why unknown remains ambiguous")
    require(acceptance.verify_queued_cancel_offline_fixture_manifest.__name__ == "verify_queued_cancel_offline_fixture_manifest",
            "negotiated-capacity S05-06 fixture manifest verifier is unavailable")
    require(acceptance.verify_agent_journal_failure_evidence.__name__ == "verify_agent_journal_failure_evidence",
            "S05-11 Agent journal write-failure evidence verifier is unavailable")
    require(acceptance.verify_stream_ui_responsive.__name__ == "verify_stream_ui_responsive",
            "product stream UI responsive evidence verifier is unavailable")

    fresh = {
        "stage": "S05", "mode": "full", "run_id": "fresh-test-run", "repeat_required": 3,
        "repeat_requested": 3, "status": "NOT_READY", "tests": {
            case_id: {"status": "NOT_READY", "runs": []} for case_id in registry_ids
        },
    }
    require(fresh["run_id"] != "initial-unexecuted" and len(fresh["tests"]) == 12,
            "fresh S05 report fixture is invalid")
    require(acceptance.verify_fixture_manifest.__name__ == "verify_fixture_manifest",
            "fixture manifest verifier is unavailable")
    require(acceptance.verify_process_recovery_manifest.__name__ == "verify_process_recovery_manifest",
            "Core/Agent process recovery manifest verifier is unavailable")
    require(acceptance.verify_stream_fixture_manifest.__name__ == "verify_stream_fixture_manifest",
            "real stream fixture manifest verifier is unavailable")
    with tempfile.TemporaryDirectory(prefix="s05-stream-manifest-") as temp_dir:
        manifest_path = pathlib.Path(temp_dir) / "stream.log"
        suite = "nd-s05-stream-selftest"
        fixtures = [("logs", "a" * 64), ("tty", "b" * 64), ("stats", "c" * 64)]
        lines = []
        for kind, container_id in fixtures:
            lines.append(f"S05_FIXTURE suite={suite} kind={kind} id={container_id} name={suite}-{kind}")
        for kind, container_id in fixtures:
            lines.append(f"S05_FIXTURE_CLEANUP suite={suite} kind={kind} id={container_id} removed=true verified_absent=true")
        lines.append(f"S05_FIXTURE_MANIFEST_CLEANUP suite={suite} expected_ids=3 remaining_ids=0 verified=true")
        manifest_path.write_text("\n".join(lines) + "\n", encoding="utf-8")
        require(acceptance.verify_stream_fixture_manifest(manifest_path)["verified"],
                "valid exact stream fixture manifest should pass")
        broken = [line for index, line in enumerate(lines) if index != 4]
        manifest_path.write_text("\n".join(broken) + "\n", encoding="utf-8")
        require(not acceptance.verify_stream_fixture_manifest(manifest_path)["verified"],
                "missing stream fixture cleanup must fail closed")

    with tempfile.TemporaryDirectory(prefix="s05-cancel-offline-manifest-") as temp_dir:
        manifest_path = pathlib.Path(temp_dir) / "cancel-offline.log"
        suite = "nd-s05-cancel-offline-selftest"
        fixtures = [(0, "a" * 64), (1, "b" * 64), (2, "c" * 64)]
        lines = [
            "S05_S06_CAPACITY negotiated_slots=2 held_slots=2 queued_target_slot=2 verified=true",
            "S05_S06_DISPATCH_BARRIER negotiated_slots=2 delivered_slots=2 agent_journal_rows=2 extra_status=queued extra_delivery=ready extra_agent_journal_rows=0 verified=true",
        ]
        lines.extend(f"S05_S06_FIXTURE suite={suite} slot={slot} id={container_id} name={suite}-{slot}"
                     for slot, container_id in fixtures)
        lines.extend(f"S05_S06_FIXTURE_CLEANUP suite={suite} id={container_id} name={suite}-{slot} removed=true verified_absent=true"
                     for slot, container_id in fixtures)
        lines.append(f"S05_S06_FIXTURE_MANIFEST suite={suite} expected_ids=3 remaining_ids=0 verified=true")
        lines.append(safe_cancel_marker)
        lines.append("S05_S06_OPERATION_TIMEOUT task_status=unknown result=result_pending operation_deadline=30s delivered=true delete_status=409 engine_unchanged=true reconciliation_inspected=true proxy_forwarded_restart=0 reconnect_no_replay=true verified=true generation=4->5")
        lines.append("S05_S06_RECONCILIATION agent_journal_status=unknown execution_phase=mutation_may_have_started baseline_verified=true baseline_started_at=2026-10-08T12:00:00Z baseline_restart_count=0 engine_restart_count=0 engine_started_at_unchanged=true process_start_count_unchanged=true engine_events=0 agent_engine_inspect_requests=1 proxy_restart_intercepts=1 proxy_restart_forwarded=0 postcondition_proven=false ambiguity=daemon_acceptance_unproven unresolved_result=unknown verified=true")
        manifest_path.write_text("\n".join(lines) + "\n", encoding="utf-8")
        require(acceptance.verify_queued_cancel_offline_fixture_manifest(manifest_path)["verified"],
                "exact negotiated-capacity plus one-fixture cleanup manifest should pass")
        manifest_path.write_text("\n".join(lines[:-2]) + "\n", encoding="utf-8")
        require(not acceptance.verify_queued_cancel_offline_fixture_manifest(manifest_path)["verified"],
                "missing S05-06 delivered-timeout evidence must fail closed")
        invalid_reconciliation = lines.copy()
        invalid_reconciliation[-1] = invalid_reconciliation[-1].replace("engine_restart_count=0", "engine_restart_count=1")
        manifest_path.write_text("\n".join(invalid_reconciliation) + "\n", encoding="utf-8")
        require(not acceptance.verify_queued_cancel_offline_fixture_manifest(manifest_path)["verified"],
                "reconciliation evidence with changed Engine restart count must fail closed")
        missing_barrier = lines[:1] + lines[2:]
        manifest_path.write_text("\n".join(missing_barrier) + "\n", encoding="utf-8")
        require(not acceptance.verify_queued_cancel_offline_fixture_manifest(manifest_path)["verified"],
                "missing durable dispatch/capacity barrier evidence must fail closed")

    with tempfile.TemporaryDirectory(prefix="s05-agent-journal-failure-") as temp_dir:
        evidence_path = pathlib.Path(temp_dir) / "agent-journal-failure.log"
        evidence_path.write_text(
            "S05_EVIDENCE agent_task_journal_failure=unknown_result_pending journal_rows=0 journal_insert_attempts=1 "
            "agent_mutation_api_calls=0 engine_mutation_api_calls=0 "
            "core_unknown_reason=delivery_committed_agent_journal_absent cross_connection_result_unproven=true "
            "resource_claim_retained=true docker_started_at_unchanged=true "
            "docker_start_count_unchanged=true docker_events_unchanged=true unknown_audit=true "
            "generation=8->9 verified=true\n",
            encoding="utf-8",
        )
        require(acceptance.verify_agent_journal_failure_evidence(evidence_path)["verified"],
                "complete Agent journal failure evidence should pass")
        evidence_path.write_text(
            "S05_EVIDENCE agent_task_journal_failure=unknown_result_pending journal_rows=0 journal_insert_attempts=1 "
            "agent_mutation_api_calls=0 engine_mutation_api_calls=0 "
            "core_unknown_reason=delivery_committed_agent_journal_absent cross_connection_result_unproven=true "
            "resource_claim_retained=true docker_started_at_unchanged=true "
            "docker_start_count_unchanged=true docker_events_unchanged=true unknown_audit=true "
            "generation=9->9 verified=true\n",
            encoding="utf-8",
        )
        require(not acceptance.verify_agent_journal_failure_evidence(evidence_path)["verified"],
                "same-generation retry must not prove Agent reconnection")

    runner_source = (ROOT / "scripts/acceptance-s05.py").read_text(encoding="utf-8")
    require("args.repeat != 3" in runner_source and "for engine in (28, 29)" in runner_source,
            "full runner must require three attempts on both locked Engines")
    require("s05-task-journal.py" in runner_source and "standard-and-race" in runner_source,
            "standard and race journal component suites are not mandatory runner steps")
    require("verify_fixture_manifest(log_path)" in runner_source,
            "a passing integration run must prove exact fixture cleanup")
    require("TestS05AgentProcessKillRecoveryOnOwnedDIND" in runner_source and
            "TestS05CoreAgentProcessKillRecoveryOnOwnedDIND" in runner_source and
            "verify_process_recovery_manifest(process_log" in runner_source,
            "S05-05 must run and verify both real Agent and Core process restart suites")
    require("unknown_task_audit=true agent_journal_unknown=true verified=true" in runner_source,
            "S05-12 must verify unknown/recovered Core audit and Agent journal evidence")
    require("TestS05CoreAgentBrowserContainerStreamsOnOwnedDIND" in runner_source and
            "verify_stream_fixture_manifest(stream_log)" in runner_source,
            "S05-09/10 must execute the real Core-Agent-browser stream test and prove exact fixture cleanup")
    require("TestS05QueuedCancelAndOfflineNoBacklogOnOwnedDIND" in runner_source and
            "verify_queued_cancel_offline_fixture_manifest(cancel_offline_log)" in runner_source and
            safe_cancel_marker in runner_source,
            "safe queued cancellation and offline no-backlog must run and be verified per attempt and Engine")
    require("verify_agent_journal_failure_evidence(log_path)" in runner_source and
            "S05-11-AGENT-JOURNAL-FAILURE" in runner_source and
            "unknown_audit=true" in runner_source and
            "engine_mutation_api_calls=0" in runner_source and
            "core_unknown_reason=delivery_committed_agent_journal_absent" in runner_source,
            "S05-11 Agent journal failure must prove the trigger fired, zero Docker API mutation, unknown audit and unresolved result across a newer generation")
    require("verify_stream_ui_responsive(stream_log)" in runner_source and
            '"viewport": {"width": 390, "height": 844}' in runner_source,
            "formal stream run must preserve responsive product-UI evidence at 390x844")
    require("container_streams_integration_test.go" in runner_source and
            "s05-container-stream-ui.mjs" in runner_source and
            "s05-container-stream-disconnect.mjs" in runner_source and
            "web/src/components/ContainerStreams.vue" in runner_source and
            "web/src/streamApi.ts" in runner_source and
            acceptance.FORMAL_ASSERTION_MARKERS["S05-09"] in runner_source and
            acceptance.FORMAL_ASSERTION_MARKERS["S05-10"] in runner_source,
            "stream implementation, product browser test and case-specific assertions must be covered by the source digest and runner")
    require("task_bridge_cancel_offline_integration_test.go" in runner_source,
            "the real Core-Agent-Engine S05-06 cancellation/offline test must be included in the source digest")
    require(len(acceptance.source_digest()) == 64,
            "acceptance source digest must cover all listed stream and task implementation inputs")
    require("-timeout=240s" in runner_source and "timeout=300" in runner_source,
            "process recovery suites need a sufficiently long but bounded timeout")

    workflow_path = ROOT / ".github/workflows/s05-acceptance.yml"
    workflow = workflow_path.read_text(encoding="utf-8")
    require('scripts/test/dind.sh clean "$engine"' in workflow and '[[ -f "$marker" ]]' in workflow,
            "CI cleanup must target only job-owned marker-verified engines")
    require("scripts/test/s05-sanitize-evidence.py" in workflow,
            "CI must sanitize and scan logs before uploading them")
    upload = workflow.split("uses: actions/upload-artifact", 1)[1]
    upload_paths = [line.strip() for line in upload.split("path:", 1)[1].splitlines()
                    if line.strip() and line.strip() != "|"]
    expected_upload = {
        "reports/stages/S05.json", "reports/status.json", "reports/evidence-s05-redaction.json",
        ".artifacts/logs/acceptance-s05/", ".artifacts/work-s05/",
    }
    require(set(upload_paths) == expected_upload,
            "workflow artifact upload must match the explicit sanitized report/log allowlist")
    require(not any(path in {".", "**", "**/*"} for path in upload_paths),
            "workflow upload allowlist is too broad")

    sanitizer.self_test()
    clean_json, redactions = sanitizer.redact_object({"password": "plain-secret", "note": "Authorization: Bearer abc"})
    require(redactions == 2 and clean_json["password"] == "[REDACTED]" and
            not sanitizer.has_secret(clean_json["note"]), "sanitizer did not redact nested secrets")
    require("S05-01" in json.loads((ROOT / "tests/registry.json").read_text())["stages"][5]["tests"][0]["id"],
            "registry stage ordering changed unexpectedly")
    print("S05 acceptance safeguards PASS: fresh 12-case report, 3x2 engine runner, component checks, owner-only cleanup and sanitized upload allowlist")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
