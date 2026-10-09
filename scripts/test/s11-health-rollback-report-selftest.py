#!/usr/bin/env python3
"""Check exact S11-06 marker validation and partial-report preservation."""

from __future__ import annotations

import importlib.util
import json
import pathlib
import tempfile


ROOT = pathlib.Path(__file__).resolve().parents[2]
MODULE_PATH = ROOT / "scripts/test/s11-health-rollback-report.py"
SPEC = importlib.util.spec_from_file_location("s11_health_rollback_report", MODULE_PATH)
assert SPEC and SPEC.loader
MODULE = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(MODULE)


def fixture_marker() -> dict:
    return {
        "schema": 1,
        "case": "S11-06",
        "test": MODULE.TEST_NAME,
        "engine_version": "28.5.2",
        "project": "nd-s11-report-fixture",
        "operation_id": "op-s11-health-rollback",
        "operation_status": "failed",
        "error_code": "health_failed",
        "rollback_confirmed": True,
        "verified": False,
        "affected_services": ["web"],
        "unhealthy_replacement_id": "unhealthy-id",
        "unhealthy_replacement_health": "unhealthy",
        "unhealthy_replacement_healthcheck_configured": True,
        "source_sha256_before": "a" * 64,
        "source_sha256_after": "a" * 64,
        "image_id_before": "sha256:old-image",
        "image_id_after": "sha256:old-image",
        "web_container_id_before": "web-old",
        "web_container_id_after": "web-restored",
        "web_port_before": 18081,
        "web_port_after": 18081,
        "web_running_after": True,
        "web_healthcheck_after": False,
        "data_container_id_before": "data-stable",
        "data_container_id_after": "data-stable",
        "volume_name_before": "volume-stable",
        "volume_name_after": "volume-stable",
        "volume_marker_before": "persisted-marker",
        "volume_marker_after": "persisted-marker",
        "suite": "nodedance-s11-report-fixture",
    }


def fixture_report() -> dict:
    return {
        "schema": 1,
        "stage": "S11",
        "status": "NOT_READY",
        "reason": "existing full-stage gap",
        "tests": {
            "S11-01": {"status": "NOT_READY", "runs": [], "reason": "not executed"},
            "S11-06": {"status": "NOT_READY", "runs": [], "reason": "health rollback pending"},
        },
    }


def main() -> None:
    with tempfile.TemporaryDirectory(prefix="nodedance-s11-report-") as temporary:
        root = pathlib.Path(temporary)
        log_path = root / ".artifacts/s11/engine-28-health-rollback.log"
        log_path.parent.mkdir(parents=True)
        report_path = root / "reports/stages/S11.json"
        report_path.parent.mkdir(parents=True)
        original = fixture_report()
        report_path.write_text(json.dumps(original), encoding="utf-8")
        marker = fixture_marker()
        log_path.write_text(
            "go test output\n    test.go: S11_HEALTH_ROLLBACK_PASS "
            + json.dumps(marker, separators=(",", ":"))
            + "\n",
            encoding="utf-8",
        )

        recorded = MODULE.record_evidence(
            log_path=log_path,
            report_path=report_path,
            expected_engine="28",
            run_id="12345",
            run_attempt="1",
            commit="abc123",
            run_url="https://github.com/example/repo/actions/runs/12345",
            artifact="nodedance-s11-engine28-12345-1/engine-28-health-rollback.log",
            root=root,
        )
        assert recorded["status"] == "NOT_READY"
        assert recorded["tests"]["S11-06"]["status"] == "NOT_READY"
        assert recorded["tests"]["S11-01"] == original["tests"]["S11-01"]
        entries = recorded["tests"]["S11-06"]["health_rollback_candidate_evidence"]
        assert len(entries) == 1 and entries[0]["status"] == "PASS"
        assert entries[0]["engine"] == "28.5.2"
        assert entries[0]["evidence"] == ".artifacts/s11/engine-28-health-rollback.log"

        MODULE.record_evidence(
            log_path=log_path,
            report_path=report_path,
            expected_engine="28",
            run_id="12345",
            run_attempt="1",
            commit="abc123",
            run_url="https://github.com/example/repo/actions/runs/12345",
            artifact="nodedance-s11-engine28-12345-1/engine-28-health-rollback.log",
            root=root,
        )
        deduped = json.loads(report_path.read_text(encoding="utf-8"))
        assert len(deduped["tests"]["S11-06"]["health_rollback_candidate_evidence"]) == 1
        assert deduped["status"] == "NOT_READY"

        before = report_path.read_bytes()
        try:
            MODULE.record_evidence(
                log_path=log_path,
                report_path=report_path,
                expected_engine="29",
                run_id="12345",
                run_attempt="1",
                commit="abc123",
                run_url="https://github.com/example/repo/actions/runs/12345",
                artifact="wrong-engine",
                root=root,
            )
        except ValueError:
            pass
        else:
            raise AssertionError("a mismatched Engine marker must be rejected")
        assert report_path.read_bytes() == before, "rejected evidence changed the report"

        bad = fixture_marker()
        bad["volume_marker_after"] = "changed"
        log_path.write_text(MODULE.MARKER_PREFIX + json.dumps(bad) + "\n", encoding="utf-8")
        try:
            MODULE.record_evidence(
                log_path=log_path,
                report_path=report_path,
                expected_engine="28",
                run_id="12345",
                run_attempt="2",
                commit="def456",
                run_url="https://github.com/example/repo/actions/runs/12345",
                artifact="bad-volume",
                root=root,
            )
        except ValueError:
            pass
        else:
            raise AssertionError("a changed persistent volume marker must be rejected")
        assert report_path.read_bytes() == before, "rejected rollback proof changed the report"

        log_path.write_text("go test completed without the exact marker\n", encoding="utf-8")
        try:
            MODULE.extract_marker(log_path)
        except ValueError:
            pass
        else:
            raise AssertionError("missing marker must remain unrecorded")

    print("S11 health rollback report selftest PASS")


if __name__ == "__main__":
    main()
