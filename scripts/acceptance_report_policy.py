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
