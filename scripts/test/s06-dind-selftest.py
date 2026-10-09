#!/usr/bin/env python3
from pathlib import Path


ROOT = Path(__file__).resolve().parents[2]
SCRIPT = ROOT / "scripts/test/s06-dind.sh"


def main() -> None:
    source = SCRIPT.read_text(encoding="utf-8")
    start = source.split('case "$ACTION" in\n  start)', 1)[1].split("\n  clean)", 1)[0]
    clean = source.split("\n  clean)", 1)[1].split("\n  *) usage", 1)[0]

    marker_create = start.index('python3 - "$MARKER"')
    marker_publish = start.index("os.replace(tmp,path)")
    network_create = start.index("host_docker network create")
    engine_create = start.index("host_docker run --detach --privileged")
    assert marker_create < marker_publish < network_create < engine_create, (
        "the atomic owner marker must be published before creating the run network or Engine"
    )
    assert '"server_version":"starting"' in start, "partial startup marker must identify the starting state"
    assert 'data.get("server_version") in {"starting","29.7.2"}' in clean, (
        "cleanup must accept an owner-marked partially started Engine"
    )
    assert "if [[ -f \"$marker\" ]]; then scripts/test/s06-dind.sh clean \"$S06_RUN_ID\" --purge-data; fi" in (
        (ROOT / ".github/workflows/s06-candidate.yml").read_text(encoding="utf-8")
    ), "the always-run workflow cleanup must invoke the exact run cleaner when the marker exists"
    print("S06 DIND owner-marker ordering selftest PASS")


if __name__ == "__main__":
    main()
