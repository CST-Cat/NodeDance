#!/usr/bin/env python3
"""Ensure S08 rejects repeated runs before tools, artifacts, or report writes."""

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
    with tempfile.TemporaryDirectory(prefix="nodedance-s08-repeat-") as temporary:
        project = pathlib.Path(temporary) / "project"
        (project / "scripts").mkdir(parents=True)
        (project / "tests").mkdir()
        (project / "reports/stages").mkdir(parents=True)
        shutil.copy2(ROOT / "scripts/acceptance-s08.py", project / "scripts/acceptance-s08.py")
        shutil.copy2(ROOT / "tests/registry.json", project / "tests/registry.json")
        report_paths = (project / "reports/stages/S08.json", project / "reports/status.json")
        for source, destination in zip(
            (ROOT / "reports/stages/S08.json", ROOT / "reports/status.json"), report_paths
        ):
            shutil.copy2(source, destination)
        original_reports = [path.read_bytes() for path in report_paths]

        fake_bin = pathlib.Path(temporary) / "bin"
        fake_bin.mkdir()
        sentinel = pathlib.Path(temporary) / "unexpected-tool-invocation.log"
        wrapper = "#!/bin/sh\nprintf '%s\\n' \"${0##*/} $*\" >> \"$S08_POLICY_SENTINEL\"\nexit 0\n"
        for tool in ("go", "docker"):
            executable = fake_bin / tool
            executable.write_text(wrapper, encoding="utf-8")
            executable.chmod(0o755)

        environment = os.environ.copy()
        environment["PATH"] = os.pathsep.join((str(fake_bin), environment.get("PATH", "")))
        environment["NODEDANCE_GO_BIN"] = str(fake_bin / "go")
        environment["S08_POLICY_SENTINEL"] = str(sentinel)
        result = subprocess.run(
            [sys.executable, str(project / "scripts/acceptance-s08.py"),
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
        require(not sentinel.exists(), "Go or Docker command ran before repeat rejection")
        require(not (project / ".artifacts").exists(), "invalid repeat request created acceptance artifacts")
        require([path.read_bytes() for path in report_paths] == original_reports,
                "invalid repeat request changed acceptance reports")

    print("S08 repeat policy PASS: --repeat 2 rejected before tools, reports, or artifacts")


if __name__ == "__main__":
    main()
