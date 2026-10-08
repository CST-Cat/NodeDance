#!/usr/bin/env python3
"""Validate the normative requirement-to-stage-to-test trace registry."""
import collections
import json
import pathlib
import re
import sys

ROOT = pathlib.Path(__file__).resolve().parents[1]
EXPECTED_ORIGINAL_CASES = [8, 12, 11, 8, 10, 12, 12, 10, 7, 9, 11, 10, 11, 10, 7, 10, 12, 12]
KNOWN_PRIORITIES = {"P0", "P1", "P2", "BODY"}


def fail(message):
    raise SystemExit(f"requirements trace FAIL: {message}")


registry = json.loads((ROOT / "tests/registry.json").read_text())
trace = json.loads((ROOT / "docs/requirements.json").read_text())
stage_ids = [stage["id"] for stage in registry.get("stages", [])]
expected_stages = [f"S{i:02}" for i in range(18)]
if stage_ids != expected_stages:
    fail(f"stage registry must contain S00-S17 in order, got {stage_ids}")

case_by_id = {}
original_counts = []
for stage in registry["stages"]:
    original = stage.get("tests", [])
    supplemental = stage.get("supplemental_tests", [])
    original_counts.append(len(original))
    for group, cases in (("original", original), ("supplemental", supplemental)):
        for case in cases:
            case_id = case.get("id", "")
            expected_pattern = rf"{stage['id']}-(?:\d{{2}}|SUP-\d{{2}})"
            if not re.fullmatch(expected_pattern, case_id):
                fail(f"invalid {group} case ID {case_id!r} under {stage['id']}")
            if case_id in case_by_id:
                fail(f"duplicate test ID {case_id}")
            for field in ("action", "expected", "environment", "evidence"):
                if not str(case.get(field, "")).strip():
                    fail(f"{case_id} is missing required field {field}")
            case_by_id[case_id] = (stage["id"], case)
if original_counts != EXPECTED_ORIGINAL_CASES:
    fail(f"original cases must preserve counts {EXPECTED_ORIGINAL_CASES}, got {original_counts}")
if len([case_id for case_id in case_by_id if "-SUP-" not in case_id]) != 182:
    fail("registry must preserve all 182 original acceptance IDs independently")

requirements = trace.get("requirements", [])
requirement_ids = [item.get("id") for item in requirements]
if len(requirement_ids) != len(set(requirement_ids)):
    fail("duplicate requirement ID")
for priority, expected_count in (("P0", 13), ("P1", 6), ("P2", 3)):
    actual = sum(item.get("priority") == priority for item in requirements)
    if actual != expected_count:
        fail(f"{priority} trace must preserve {expected_count} original table requirements; got {actual}")
if any(item.get("priority") not in KNOWN_PRIORITIES for item in requirements):
    fail("unknown requirement priority")

references = collections.Counter()
for item in requirements:
    req_id = item.get("id", "<missing ID>")
    stages = item.get("stages", [])
    acceptance = item.get("acceptance", [])
    if not item.get("title") or not stages or not acceptance:
        fail(f"{req_id} lacks a title, stage mapping, or acceptance mapping")
    if len(acceptance) != len(set(acceptance)):
        fail(f"{req_id} duplicates an acceptance reference")
    unknown_stages = set(stages) - set(stage_ids)
    if unknown_stages:
        fail(f"{req_id} maps to unknown stage(s) {sorted(unknown_stages)}")
    for case_id in acceptance:
        if case_id not in case_by_id:
            fail(f"{req_id} references missing acceptance ID {case_id}")
        case_stage, _ = case_by_id[case_id]
        if case_stage not in stages:
            fail(f"{req_id} references {case_id} but omits its stage {case_stage}")
        references[case_id] += 1
unreferenced = sorted(set(case_by_id) - set(references))
if unreferenced:
    fail(f"acceptance cases without requirement trace: {unreferenced}")

for required_doc in ("docs/plan.md", "docs/design-source.md", "docs/plan-amendments.md", "docs/architecture.md"):
    if not (ROOT / required_doc).is_file():
        fail(f"missing plan/architecture source document {required_doc}")

for stage in registry["stages"]:
    sid = stage["id"]
    doc = ROOT / "docs/stages" / f"{sid}.md"
    if not doc.is_file():
        fail(f"missing stage document {doc.relative_to(ROOT)}")
    content = doc.read_text()
    for case in stage.get("tests", []) + stage.get("supplemental_tests", []):
        if content.count(f"`{case['id']}`") != 1:
            fail(f"{sid} documentation must list {case['id']} exactly once")

# Status is checked structurally only. A later completed stage is allowed to
# remove NOT_READY from its own documents after real implementation and tests.
status_path = ROOT / "reports/status.json"
if not status_path.is_file():
    fail("missing machine-readable reports/status.json")
status = json.loads(status_path.read_text())
statuses = status.get("stages", {})
if set(statuses) != set(stage_ids):
    fail("reports/status.json must contain one entry for every stage")
for sid, item in statuses.items():
    if item.get("status") not in {"PASS", "FAIL", "NOT_READY"}:
        fail(f"{sid} has invalid overall status {item.get('status')!r}")
    stage_report = ROOT / "reports/stages" / f"{sid}.json"
    if not stage_report.is_file():
        fail(f"missing machine-readable stage report {stage_report.relative_to(ROOT)}")
    report = json.loads(stage_report.read_text())
    if report.get("stage") != sid:
        fail(f"{sid} report stage identity mismatch")
    report_tests = report.get("tests", {})
    expected_ids = {case["id"] for case in next(s for s in registry["stages"] if s["id"] == sid).get("tests", []) + next(s for s in registry["stages"] if s["id"] == sid).get("supplemental_tests", [])}
    if set(report_tests) != expected_ids:
        fail(f"{sid} report must list all required test cases individually")
    for case_id, case_report in report_tests.items():
        if case_report.get("status") not in {"PASS", "FAIL", "NOT_READY"}:
            fail(f"{sid} {case_id} has invalid status {case_report.get('status')!r}")
        if not str(case_report.get("reason", "")).strip():
            fail(f"{sid} {case_id} has no explicit result reason")

print(f"Requirements trace PASS: 182 original checks + {len(case_by_id)-182} supplemental checks; 22 P0/P1/P2 and {sum(x['priority']=='BODY' for x in requirements)} body requirements across S00-S17")
