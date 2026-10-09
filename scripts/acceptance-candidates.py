#!/usr/bin/env python3
"""Run one focused candidate suite; preserve normative cases as NOT_READY."""

import argparse

from acceptance_candidates import CANDIDATE_TARGETS, run_candidate_stage


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--stage", required=True, choices=sorted(CANDIDATE_TARGETS))
    parser.add_argument("--mode", required=True, choices=("full", "integration", "e2e"))
    args = parser.parse_args()
    return run_candidate_stage(args.stage, args.mode)


if __name__ == "__main__":
    raise SystemExit(main())
