#!/usr/bin/env python3
"""No-Docker regression test for fail-closed DIND ownership checks."""
from __future__ import annotations

import json
import os
import pathlib
import shutil
import subprocess
import tempfile


SOURCE = pathlib.Path(__file__).resolve().parents[2]
HOST_ENDPOINT = "unix:///fake/host-docker.sock"
HOST_ID = "host-daemon-fixture-id"
CONTAINER_ID = "c" * 64
NETWORK_ID = "d" * 64


def fixture_objects(root: pathlib.Path) -> tuple[dict, dict]:
    engine = "29"
    name = f"nodedance-s00-dind-v{engine}"
    network_name = f"nodedance-s00-dind-net-v{engine}"
    image = json.loads((root / "test-images.lock.json").read_text())["images"]["engine29"]
    dir_path = root / ".artifacts/dind/v29"
    socket_dir = dir_path / "socket"
    data_dir = dir_path / "data"
    fixture_dir = root / ".artifacts/fixtures"
    container = {
        "Id": CONTAINER_ID,
        "Name": "/" + name,
        "Config": {
            "Image": image,
            "Labels": {"io.nodedance.test": "true", "io.nodedance.suite": "nodedance-s00-dind"},
        },
        "State": {"Running": True},
        "Mounts": [
            {"Type": "bind", "Source": str(socket_dir), "Destination": "/run/nodedance-socket"},
            {"Type": "bind", "Source": str(fixture_dir), "Destination": str(fixture_dir)},
            {"Type": "bind", "Source": str(data_dir), "Destination": "/var/lib/docker"},
        ],
        "NetworkSettings": {"Networks": {network_name: {"NetworkID": NETWORK_ID}}},
    }
    network = {
        "Id": NETWORK_ID,
        "Name": network_name,
        "Labels": {"io.nodedance.test": "true", "io.nodedance.suite": "nodedance-s00-dind"},
    }
    return container, network


def make_root(parent: pathlib.Path, label: str) -> tuple[pathlib.Path, pathlib.Path, pathlib.Path]:
    root = parent / label
    (root / "scripts/test").mkdir(parents=True)
    shutil.copy2(SOURCE / "test-images.lock.json", root / "test-images.lock.json")
    shutil.copy2(SOURCE / "scripts/test/dind.sh", root / "scripts/test/dind.sh")
    shutil.copy2(SOURCE / "scripts/test/dind-owner.py", root / "scripts/test/dind-owner.py")

    fake_bin = parent / f"{label}-bin"
    fake_bin.mkdir()
    docker = fake_bin / "docker"
    docker.write_text(
        "#!/usr/bin/env python3\n"
        "import json, os, pathlib, sys\n"
        "args=sys.argv[1:]\n"
        "if len(args) >= 2 and args[0] == '--host': args=args[2:]\n"
        "log=pathlib.Path(os.environ['FAKE_DOCKER_LOG'])\n"
        "with log.open('a') as f: f.write(json.dumps(args)+'\\n')\n"
        "state=json.loads(pathlib.Path(os.environ['FAKE_DOCKER_STATE']).read_text())\n"
        "if args and args[0] == 'info': print(state['host_id']); raise SystemExit(0)\n"
        "if len(args) >= 3 and args[0] == 'container' and args[1] == 'inspect':\n"
        "  obj=state.get('container')\n"
        "  if obj is None: print('Error: No such object: '+args[2], file=sys.stderr); raise SystemExit(1)\n"
        "  print(json.dumps([obj])); raise SystemExit(0)\n"
        "if len(args) >= 3 and args[0] == 'network' and args[1] == 'inspect':\n"
        "  obj=state.get('network')\n"
        "  if obj is None: print('Error response from daemon: network '+args[2]+' not found', file=sys.stderr); raise SystemExit(1)\n"
        "  print(json.dumps([obj])); raise SystemExit(0)\n"
        "print('unexpected Docker command: '+json.dumps(args), file=sys.stderr); raise SystemExit(99)\n"
    )
    docker.chmod(0o755)
    chmod = fake_bin / "chmod"
    chmod.write_text(
        "#!/usr/bin/env python3\n"
        "import json, os, sys\n"
        "with open(os.environ['FAKE_MUTATION_LOG'], 'a') as f: f.write(json.dumps(['chmod', *sys.argv[1:]])+'\\n')\n"
        "raise SystemExit(99)\n"
    )
    chmod.chmod(0o755)
    state_path = parent / f"{label}-state.json"
    return root, fake_bin, state_path


def run_collision(parent: pathlib.Path, label: str, *, with_container: bool, with_network: bool,
                  marker_mismatch: bool = False, action: str = "start", legacy_marker: bool = False) -> None:
    root, fake_bin, state_path = make_root(parent, label)
    container, network = fixture_objects(root)
    state = {
        "host_id": HOST_ID,
        "container": container if with_container else None,
        "network": network if with_network else None,
    }
    state_path.write_text(json.dumps(state))
    log_path = parent / f"{label}-docker.log"
    mutation_log = parent / f"{label}-mutations.log"
    marker_path = root / ".artifacts/dind/v29/owner.json"
    if marker_mismatch or legacy_marker:
        marker_path.parent.mkdir(parents=True)
        marker = ({
            "schema": 1,
            "suite": "nodedance-s00-dind",
            "container_name": "nodedance-s00-dind-v29",
            "image": json.loads((root / "test-images.lock.json").read_text())["images"]["engine29"],
            "host_daemon": HOST_ENDPOINT,
            "socket": str(root / ".artifacts/dind/v29/socket/docker.sock"),
            "server_version": "29.7.2",
        } if legacy_marker else {
            "schema": 2,
            "suite": "nodedance-s00-dind",
            "container_name": "nodedance-s00-dind-v29",
            "network_name": "nodedance-s00-dind-net-v29",
            "image": json.loads((root / "test-images.lock.json").read_text())["images"]["engine29"],
            "host_daemon": HOST_ENDPOINT,
            "host_daemon_id": HOST_ID,
            "socket": str(root / ".artifacts/dind/v29/socket/docker.sock"),
            "socket_dir": str(root / ".artifacts/dind/v29/socket"),
            "data_dir": str(root / ".artifacts/dind/v29/data"),
            "fixture_dir": str(root / ".artifacts/fixtures"),
            "container_id": "e" * 64,
            "network_id": NETWORK_ID,
            "server_version": "29.7.2",
        })
        marker_path.write_text(json.dumps(marker))

    env = os.environ.copy()
    env.update({
        "PATH": str(fake_bin) + os.pathsep + "/usr/bin:/bin",
        "NODEDANCE_HOST_DOCKER_HOST": HOST_ENDPOINT,
        "FAKE_DOCKER_LOG": str(log_path),
        "FAKE_DOCKER_STATE": str(state_path),
        "FAKE_MUTATION_LOG": str(mutation_log),
    })
    result = subprocess.run(
        ["bash", str(root / "scripts/test/dind.sh"), action, "29"],
        cwd=root,
        env=env,
        text=True,
        stdout=subprocess.PIPE,
        stderr=subprocess.STDOUT,
        timeout=10,
    )
    calls = [json.loads(line) for line in log_path.read_text().splitlines()]
    mutations = mutation_log.read_text().splitlines() if mutation_log.exists() else []
    def readonly(call: list[str]) -> bool:
        return bool(call) and (call[0] == "info" or call[:2] in (["container", "inspect"], ["network", "inspect"]))

    if result.returncode != 3:
        raise AssertionError(f"{label}: expected fail-closed exit 3, got {result.returncode}:\n{result.stdout}")
    if any(not readonly(call) for call in calls):
        raise AssertionError(f"{label}: fake Docker saw a non-read-only action: {calls!r}")
    if mutations:
        raise AssertionError(f"{label}: local permission mutation ran before ownership validation: {mutations!r}")
    if [call[0] for call in calls].count("info") != 1:
        raise AssertionError(f"{label}: host daemon identity should be read once: {calls!r}")
    if [call[:2] for call in calls].count(["container", "inspect"]) != 1:
        raise AssertionError(f"{label}: container name must be inspected exactly once: {calls!r}")
    if [call[:2] for call in calls].count(["network", "inspect"]) != 1:
        raise AssertionError(f"{label}: network name must be inspected exactly once: {calls!r}")
    if (root / ".artifacts/dind").exists() and not marker_mismatch and not legacy_marker:
        raise AssertionError(f"{label}: failed preflight created local owner state")
    if (marker_mismatch or legacy_marker) and marker_path.read_text() != json.dumps(marker):
        raise AssertionError(f"{label}: failed preflight altered the existing owner marker")
    print(f"PASS {label}: exit={result.returncode}; Docker calls={json.dumps(calls)}; {result.stdout.strip()}")


def main() -> None:
    with tempfile.TemporaryDirectory(prefix="nodedance-dind-owner-selftest-") as temp:
        parent = pathlib.Path(temp)
        run_collision(parent, "missing-marker-container", with_container=True, with_network=False)
        run_collision(parent, "missing-marker-network", with_container=False, with_network=True)
        run_collision(parent, "missing-marker-both", with_container=True, with_network=True)
        run_collision(parent, "marker-container-id-mismatch", with_container=True, with_network=True, marker_mismatch=True)
        run_collision(parent, "legacy-marker-stop", with_container=True, with_network=True,
                      action="stop", legacy_marker=True)
        run_collision(parent, "legacy-marker-clean", with_container=True, with_network=True,
                      action="clean", legacy_marker=True)
    print("DIND OWNER SELFTEST PASS: fail-closed checks used only the fake Docker executable; no system daemon was contacted")


if __name__ == "__main__":
    main()
