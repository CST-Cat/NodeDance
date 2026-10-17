#!/usr/bin/env python3
"""Release rehearsal gate: verify a would-be release before its tag exists.

The rehearsal performs every validation the release workflows run in their
prepare phase (tag format, bilingual notes, exact Agent pairing) plus the
publication preconditions (no existing tag/release, pairing against the
newest stable published Agent). It never creates a tag, release or artifact;
a passing rehearsal only records that pushing the tag will publish.
"""
import argparse
import importlib.util
import json
import re
import subprocess
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
_spec = importlib.util.spec_from_file_location("release_notes", ROOT / "scripts" / "release-notes.py")
release_notes = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(release_notes)

VERSION = release_notes.VERSION
# The rehearsal record lives in version control so the release workflows can
# verify it from the tagged commit on GitHub, not from the local working tree.
REHEARSAL_MARKER = ".release-rehearsals"


class RehearsalError(Exception):
    pass

def run(command, allow_failure=False):
    result = subprocess.run(command, capture_output=True, text=True)
    if result.returncode != 0 and not allow_failure:
        raise RehearsalError(f"{' '.join(command)} failed: {result.stderr.strip() or result.stdout.strip()}")
    return result.stdout.strip()


def check_local(component, tag, agent_tag=None):
    """Validate everything decidable from the working tree."""
    if not re.fullmatch(component + "-v" + VERSION, tag):
        raise RehearsalError(f"invalid {component} tag: {tag}")
    changelog = (ROOT / f"CHANGELOG_{component.upper()}.md").read_text()
    try:
        notes = release_notes.validate(component, tag, changelog, agent_tag)
    except ValueError as reason:
        raise RehearsalError(
            f"changelog not ready for {tag}: {reason}. Add the reviewed bilingual "
            f"entry (promote from [Unreleased] first: python3 scripts/release-notes.py "
            f"promote {component} {tag})"
        ) from reason
    version = tag.split("-v", 1)[1]
    if run(["git", "status", "--porcelain", "--", f"CHANGELOG_{component.upper()}.md"]):
        raise RehearsalError("changelog has uncommitted changes; commit the reviewed notes first")
    if run(["git", "tag", "--list", tag]):
        raise RehearsalError(f"tag {tag} already exists locally; remove it before rehearsing")
    remote_tags = run(["git", "ls-remote", "--tags", "origin", f"refs/tags/{tag}"])
    if remote_tags:
        raise RehearsalError(f"tag {tag} already exists on origin")
    if component == "core":
        pinned = (ROOT / "agent-release.version").read_text().strip()
        if pinned != agent_tag:
            raise RehearsalError(f"agent-release.version pins {pinned}, rehearsal targets {agent_tag}")
        if run(["git", "status", "--porcelain", "--", "agent-release.version"]):
            raise RehearsalError("agent-release.version has uncommitted changes")
    return notes


def check_remote(component, tag, agent_tag=None):
    """Validate publication preconditions that need GitHub state."""
    repository = run(["gh", "repo", "view", "--json", "nameWithOwner", "-q", ".nameWithOwner"])
    if component == "core":
        # The pairing must already be the newest stable published Agent release.
        listing = json.loads(run(["gh", "api", "repos/{owner}/{repo}/releases?per_page=100"]))
        stable = [entry["tag_name"] for entry in listing
                  if not entry["draft"] and not entry["prerelease"]
                  and re.fullmatch("agent-v" + VERSION, entry["tag_name"])]
        newest = max(stable, key=lambda t: tuple(map(int, t.split("-v")[1].split("."))))
        if agent_tag != newest:
            raise RehearsalError(f"Core pairing must use newest stable Agent {newest}, not {agent_tag}")
    existing = run(["gh", "api", f"repos/{repository}/releases/tags/{tag}"], allow_failure=True)
    # gh prints the JSON error body to stdout even on HTTP 404 (no release).
    if existing and '"message":"Not Found"' not in existing:
        raise RehearsalError(f"release {tag} already exists")
    return repository


def record(component, tag):
    marker = ROOT / REHEARSAL_MARKER
    marker.parent.mkdir(parents=True, exist_ok=True)
    lines = []
    if marker.exists():
        lines = [line for line in marker.read_text().splitlines()
                 if line and not line.endswith(" " + tag)]
    lines.append(f"{component} {tag}")
    marker.write_text("\n".join(lines) + "\n")


def rehearse(component, tag, agent_tag=None):
    notes = check_local(component, tag, agent_tag)
    check_remote(component, tag, agent_tag)
    record(component, tag)
    print(f"Rehearsal passed for {tag}; commit {REHEARSAL_MARKER} and push the tag to publish.")
    if notes:
        print(notes.splitlines()[0])


def passed(component, tag):
    marker = ROOT / REHEARSAL_MARKER
    if not marker.exists():
        return False
    return f"{component} {tag}" in marker.read_text().splitlines()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("command", choices=["run", "check"])
    parser.add_argument("component", choices=["core", "agent"])
    parser.add_argument("tag", help="core-vX.Y.Z or agent-vX.Y.Z")
    parser.add_argument("--agent-tag", help="exact paired Agent tag, required for core")
    args = parser.parse_args()
    try:
        if args.command == "check":
            if not passed(args.component, args.tag):
                raise RehearsalError(
                    f"no passing rehearsal for {args.tag} in {REHEARSAL_MARKER}; run "
                    f"'python3 scripts/release-rehearsal.py run {args.component} {args.tag}'"
                    + (" --agent-tag <agent-vX.Y.Z>" if args.component == "core" else "")
                    + ", commit the recorded file and push it with the tag"
                )
            print(f"Rehearsal confirmed for {args.tag}")
        else:
            if args.component == "core" and not args.agent_tag:
                parser.error("core rehearsals require --agent-tag")
            rehearse(args.component, args.tag, args.agent_tag)
    except (RehearsalError, ValueError, OSError) as e:
        print(f"Rehearsal stopped: {e}", file=sys.stderr)
        sys.exit(1)


if __name__ == "__main__":
    main()
