#!/usr/bin/env python3
"""Validate ownership of the shared-name DIND test resources."""
from __future__ import annotations

import json
import os
import pathlib
import sys
import tempfile


def fail(message: str) -> None:
    print(f"DIND owner check failed: {message}", file=sys.stderr)
    raise SystemExit(3)


def expected_from(argv: list[str]) -> tuple[pathlib.Path, dict[str, str]]:
    if len(argv) != 11:
        fail("internal expected-owner argument count is invalid")
    marker, suite, name, network, image, endpoint, host_id, socket, socket_dir, data_dir, fixture_dir = argv
    # Resolve paths lexically through any existing parents. The shell separately
    # rejects symlinks in these owned directories before it creates or removes.
    paths = {
        "socket": os.path.realpath(socket),
        "socket_dir": os.path.realpath(socket_dir),
        "data_dir": os.path.realpath(data_dir),
        "fixture_dir": os.path.realpath(fixture_dir),
    }
    return pathlib.Path(marker), {
        "suite": suite,
        "container_name": name,
        "network_name": network,
        "image": image,
        "host_daemon": endpoint,
        "host_daemon_id": host_id,
        **paths,
    }


def read_marker(path: pathlib.Path) -> dict | None:
    if not path.exists() and not path.is_symlink():
        return None
    if path.is_symlink() or not path.is_file():
        fail("owner marker is not a regular, non-symlink file")
    try:
        value = json.loads(path.read_text())
    except (OSError, json.JSONDecodeError) as exc:
        fail(f"owner marker cannot be read: {exc}")
    if not isinstance(value, dict):
        fail("owner marker must contain a JSON object")
    return value


def check_static(marker: dict, expected: dict[str, str]) -> None:
    if marker.get("schema") != 2:
        fail("owner marker schema is not the current identity-checked schema")
    for key, value in expected.items():
        if marker.get(key) != value:
            fail(f"owner marker {key} differs from this checkout or host daemon")


def inspect_from_env(name: str) -> dict | None:
    raw = os.environ.get(name, "")
    if not raw:
        return None
    try:
        value = json.loads(raw)
    except json.JSONDecodeError as exc:
        fail(f"Docker inspect JSON is invalid: {exc}")
    if isinstance(value, list):
        if len(value) != 1:
            fail("Docker inspect did not return exactly one object")
        value = value[0]
    if not isinstance(value, dict):
        fail("Docker inspect did not return an object")
    return value


def labels(obj: dict) -> dict:
    if "Config" in obj:
        value = obj.get("Config", {}).get("Labels")
    else:
        value = obj.get("Labels")
    return value if isinstance(value, dict) else {}


def validate_network(obj: dict, marker: dict, expected: dict[str, str]) -> str:
    network_id = obj.get("Id") or obj.get("ID")
    if not network_id or network_id != marker.get("network_id"):
        fail("live network ID differs from the owner marker")
    if obj.get("Name") != expected["network_name"]:
        fail("live network name differs from the fixed suite name")
    if labels(obj).get("io.nodedance.suite") != expected["suite"]:
        fail("live network suite label differs")
    if labels(obj).get("io.nodedance.test") != "true":
        fail("live network test label differs")
    return network_id


def validate_container(obj: dict, marker: dict, expected: dict[str, str]) -> str:
    container_id = obj.get("Id") or obj.get("ID")
    if not container_id or container_id != marker.get("container_id"):
        fail("live container ID differs from the owner marker")
    if obj.get("Name", "").lstrip("/") != expected["container_name"]:
        fail("live container name differs from the fixed suite name")
    actual_labels = labels(obj)
    if actual_labels.get("io.nodedance.suite") != expected["suite"]:
        fail("live container suite label differs")
    if actual_labels.get("io.nodedance.test") != "true":
        fail("live container test label differs")
    config = obj.get("Config") or {}
    if config.get("Image") != expected["image"]:
        fail("live container configured image differs from the locked image")

    wanted_mounts = {
        (expected["socket_dir"], "/run/nodedance-socket"),
        (expected["fixture_dir"], expected["fixture_dir"]),
        (expected["data_dir"], "/var/lib/docker"),
    }
    actual_mounts: set[tuple[str, str]] = set()
    for mount in obj.get("Mounts") or []:
        if mount.get("Type") == "bind":
            actual_mounts.add((os.path.realpath(mount.get("Source", "")), mount.get("Destination", "")))
    if actual_mounts != wanted_mounts:
        fail("live container bind mount sources or destinations differ from this checkout")

    networks = (obj.get("NetworkSettings") or {}).get("Networks") or {}
    network = networks.get(expected["network_name"])
    if not isinstance(network, dict) or network.get("NetworkID") != marker.get("network_id"):
        fail("live container is not attached to the marked network ID")
    return container_id


def write_marker(path: pathlib.Path, expected: dict[str, str], container_id: str, network_id: str, version: str) -> None:
    if not container_id and not network_id:
        # Empty IDs are an intentional provisioning intent only before Docker
        # resources have been created. A later inspect finding an object while
        # its ID is empty fails closed instead of adopting by label/name.
        pass
    marker = {"schema": 2, **expected, "container_id": container_id, "network_id": network_id,
              "socket": expected["socket"], "server_version": version}
    path.parent.mkdir(parents=True, exist_ok=True)
    fd, temporary = tempfile.mkstemp(prefix=".owner-", dir=path.parent)
    try:
        with os.fdopen(fd, "w") as stream:
            json.dump(marker, stream, indent=2)
            stream.write("\n")
            stream.flush()
            os.fsync(stream.fileno())
        os.chmod(temporary, 0o600)
        os.replace(temporary, path)
    finally:
        try:
            os.unlink(temporary)
        except FileNotFoundError:
            pass


def command_validate(mode: str, path: pathlib.Path, expected: dict[str, str]) -> None:
    marker = read_marker(path)
    container = inspect_from_env("ND_DIND_CONTAINER_JSON")
    network = inspect_from_env("ND_DIND_NETWORK_JSON")
    if marker is None:
        if mode == "start" and container is None and network is None:
            print("|")
            return
        fail("a fixed-name Docker resource exists without this checkout's owner marker")

    check_static(marker, expected)
    if not isinstance(marker.get("container_id"), str) or not isinstance(marker.get("network_id"), str):
        fail("owner marker has malformed resource IDs")
    if container is not None:
        validate_container(container, marker, expected)
    if network is not None:
        validate_network(network, marker, expected)
    if container is not None and network is None:
        fail("marked container exists but its marked network is missing")
    if mode == "stop" and (container is None or network is None):
        fail("stop requires both marked Docker resources to exist")
    print(f"{marker['container_id']}|{marker['network_id']}")


def command_attest_network(path: pathlib.Path, expected: dict[str, str], candidate_id: str) -> None:
    marker = read_marker(path)
    if marker is None:
        fail("cannot attest a new network without a local owner marker")
    check_static(marker, expected)
    obj = inspect_from_env("ND_DIND_NETWORK_JSON")
    if obj is None:
        fail("new network has no inspect result")
    if obj.get("Id") != candidate_id or obj.get("Name") != expected["network_name"]:
        fail("new network inspect identity differs from the create result")
    if labels(obj).get("io.nodedance.suite") != expected["suite"] or labels(obj).get("io.nodedance.test") != "true":
        fail("new network labels do not prove suite ownership")
    prior_id = marker.get("network_id", "")
    if prior_id and prior_id != candidate_id:
        fail("new network ID differs from the already marked network")
    write_marker(path, expected, marker.get("container_id", ""), candidate_id, marker.get("server_version", "starting"))
    print(candidate_id)


def command_attest_container(path: pathlib.Path, expected: dict[str, str], candidate_id: str) -> None:
    marker = read_marker(path)
    if marker is None:
        fail("cannot attest a new container without a local owner marker")
    check_static(marker, expected)
    container = inspect_from_env("ND_DIND_CONTAINER_JSON")
    network = inspect_from_env("ND_DIND_NETWORK_JSON")
    if container is None or network is None:
        fail("new container or marked network has no inspect result")
    if container.get("Id") != candidate_id:
        fail("new container inspect ID differs from the create result")
    validate_network(network, {**marker, "network_id": marker.get("network_id", "")}, expected)
    probe_marker = {**marker, "container_id": candidate_id}
    validate_container(container, probe_marker, expected)
    prior_id = marker.get("container_id", "")
    if prior_id and prior_id != candidate_id:
        fail("new container ID differs from the already marked container")
    write_marker(path, expected, candidate_id, marker["network_id"], marker.get("server_version", "starting"))
    print(candidate_id)


def command_write(path: pathlib.Path, expected: dict[str, str], container_id: str, network_id: str, version: str) -> None:
    existing = read_marker(path)
    if existing is not None:
        check_static(existing, expected)
        if existing.get("container_id") not in ("", container_id):
            fail("container ID changed while updating the owner marker")
        if existing.get("network_id") not in ("", network_id):
            fail("network ID changed while updating the owner marker")
    write_marker(path, expected, container_id, network_id, version)


def main(argv: list[str]) -> None:
    if len(argv) < 2:
        fail("missing subcommand")
    command = argv[0]
    if command == "validate":
        if len(argv) != 13:
            fail("validate requires mode plus expected-owner arguments")
        mode = argv[1]
        if mode not in ("start", "stop", "clean"):
            fail("unknown validation mode")
        path, expected = expected_from(argv[2:])
        command_validate(mode, path, expected)
    elif command == "write":
        if len(argv) != 15:
            fail("write requires expected-owner arguments and three marker values")
        path, expected = expected_from(argv[1:12])
        command_write(path, expected, argv[12], argv[13], argv[14])
    elif command == "attest-network":
        if len(argv) != 13:
            fail("attest-network requires expected-owner arguments and candidate ID")
        path, expected = expected_from(argv[1:12])
        command_attest_network(path, expected, argv[12])
    elif command == "attest-container":
        if len(argv) != 13:
            fail("attest-container requires expected-owner arguments and candidate ID")
        path, expected = expected_from(argv[1:12])
        command_attest_container(path, expected, argv[12])
    else:
        fail(f"unknown subcommand: {command}")


if __name__ == "__main__":
    main(sys.argv[1:])
