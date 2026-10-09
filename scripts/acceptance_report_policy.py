"""Validation helpers for current and explicitly historical stage reports."""

HISTORICAL_THREE_RUN_NOTE = (
    "Historical PASS recorded under the former three-consecutive-run acceptance rule. "
    "Preserved as historical evidence; not rerun under the current single-complete-run rule."
)


def pass_runs_are_valid(stage, report, runs):
    """Accept one current attempt, or explicitly annotated legacy S00/S01 evidence."""
    if not isinstance(runs, list):
        return False
    attempts = [item.get("attempt") for item in runs if isinstance(item, dict)]
    if len(attempts) != len(runs):
        return False
    statuses_pass = all(item.get("status") == "PASS" for item in runs)
    if not statuses_pass:
        return False

    historical = (
        stage in {"S00", "S01"}
        and report.get("mode") == "full"
        and report.get("historical_acceptance_policy_note") == HISTORICAL_THREE_RUN_NOTE
        and report.get("repeat_required") == 3
        and report.get("repeat_requested") == 3
    )
    if historical:
        return attempts == [1, 2, 3]

    return (
        report.get("repeat_required") == 1
        and report.get("repeat_requested") == 1
        and attempts == [1]
    )


def s06_partial_case_is_valid(report, case_id, case):
    """Validate the sole independently executable S06 case without hiding failures."""
    stage_status = report.get("status")
    candidate_status = report.get("candidate_status")
    if report.get("stage") != "S06" or stage_status not in {"NOT_READY", "FAIL"}:
        return False
    if candidate_status not in {"PASS", "FAIL"}:
        return False
    if report.get("repeat_required") != 1 or report.get("repeat_requested") != 1:
        return False
    status = case.get("status")
    runs = case.get("runs")
    if not isinstance(runs, list):
        return False
    if case_id != "S06-03":
        return status == "NOT_READY" and not runs
    if status == "PASS":
        return (
            pass_runs_are_valid("S06", report, runs)
            and (stage_status == "NOT_READY" and candidate_status == "PASS"
                 or stage_status == "FAIL" and candidate_status == "FAIL")
        )
    if status == "FAIL":
        return (
            len(runs) == 1
            and isinstance(runs[0], dict)
            and runs[0].get("attempt") == 1
            and runs[0].get("status") == "FAIL"
            and stage_status == "FAIL"
        )
    return status == "NOT_READY" and not runs and (
        stage_status == "NOT_READY" and candidate_status == "PASS"
        or stage_status == "FAIL" and candidate_status == "FAIL"
    )
