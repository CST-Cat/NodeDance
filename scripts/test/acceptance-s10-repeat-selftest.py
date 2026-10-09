#!/usr/bin/env python3
"""Ensure S10 rejects repeat runs before invoking tools or writing reports."""

import os
import pathlib
import shutil
import subprocess
import sys
import tempfile


ROOT = pathlib.Path(__file__).resolve().parents[2]


def require(condition, message):
    if not condition:
        raise AssertionError(message)


def main():
    with tempfile.TemporaryDirectory(prefix="nodedance-s10-repeat-") as temporary:
        project = pathlib.Path(temporary) / "project"
        (project / "scripts").mkdir(parents=True)
        (project / "tests").mkdir()
        (project / "reports/stages").mkdir(parents=True)
        shutil.copy2(ROOT / "scripts/acceptance-s10.py", project / "scripts/acceptance-s10.py")
        shutil.copy2(ROOT / "tests/registry.json", project / "tests/registry.json")
        report_paths = (project / "reports/stages/S10.json", project / "reports/status.json")
        for source, destination in zip(
            (ROOT / "reports/stages/S10.json", ROOT / "reports/status.json"), report_paths
        ):
            shutil.copy2(source, destination)
        original_reports = [path.read_bytes() for path in report_paths]

        fake_bin = pathlib.Path(temporary) / "bin"
        fake_bin.mkdir()
        sentinel = pathlib.Path(temporary) / "unexpected-tool-invocation.log"
        wrapper = "#!/bin/sh\nprintf '%s\\n' \"${0##*/} $*\" >> \"$S10_POLICY_SENTINEL\"\nexit 0\n"
        for tool in ("go", "pnpm", "docker"):
            executable = fake_bin / tool
            executable.write_text(wrapper, encoding="utf-8")
            executable.chmod(0o755)

        environment = os.environ.copy()
        environment["PATH"] = os.pathsep.join((str(fake_bin), environment.get("PATH", "")))
        environment["S10_POLICY_SENTINEL"] = str(sentinel)
        result = subprocess.run(
            [sys.executable, str(project / "scripts/acceptance-s10.py"),
             "--mode", "full", "--repeat", "2"],
            cwd=project,
            env=environment,
            capture_output=True,
            text=True,
            timeout=10,
        )

        require(result.returncode == 2, f"expected argparse rejection (2), got {result.returncode}: {result.stderr}")
        require("error: --repeat must be 1" in result.stderr,
                f"repeat policy error was not reported: {result.stderr}")
        require(not sentinel.exists(), "Go, package manager, or Engine command ran before repeat rejection")
        require([path.read_bytes() for path in report_paths] == original_reports,
                "invalid repeat request changed acceptance reports")
        require(not (project / ".artifacts/logs/acceptance-s10").exists(),
                "invalid repeat request created acceptance evidence")

    print("S10 repeat policy PASS: --repeat 2 rejected before tools, reports, or evidence")


if __name__ == "__main__":
    main()
