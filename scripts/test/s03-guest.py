#!/usr/bin/env python3
"""Run a real Linux clock-change and reboot probe in an isolated QEMU guest."""

import argparse
import datetime as dt
import gzip
import hashlib
import json
import os
import pathlib
import platform
import re
import shlex
import shutil
import signal
import socket
import stat
import subprocess
import sys
import tempfile
import time
import uuid
import urllib.error
import urllib.request

sys.path.insert(0, str(pathlib.Path(__file__).resolve().parent))
from s03_guest_resources import (  # noqa: E402
    OwnershipLost,
    ResourceBusy,
    acquire_exclusive_lock,
    assert_nbd_idle,
    assert_nbd_unmounted,
    capture_new_mount_identity,
    capture_qemu_nbd_owner,
    disconnect_owned_nbd,
    mount_identity_at,
    nbd_device_numbers,
    process_identity,
    qemu_nbd_processes,
    read_mountinfo,
    verify_mount_identity,
    verify_qemu_nbd_owner,
    unmount_owned_mount,
)


ROOT = pathlib.Path(__file__).resolve().parents[2]
WORK_ROOT = ROOT / ".artifacts" / "work-s03"
NBD_DEVICE = "/dev/nbd0"
NBD_SYSFS_DIR = pathlib.Path("/sys/block/nbd0")
NBD_LOCK = pathlib.Path(tempfile.gettempdir()) / "nodedance-s03-nbd0.lock"
RELEASE_PATH = "releases/noble/release-20260814"
RELEASE_URL = f"https://cloud-images.ubuntu.com/releases/{RELEASE_PATH}"
KEYRING = pathlib.Path("/usr/share/keyrings/ubuntu-cloudimage-keyring.gpg")
SIGNING_FINGERPRINT = "D2EB44626FDDC30B513D5BB71A5D6C4C7DB87C81"
IMAGE_LOCKS = {
    "arm64": {
        "image": "ubuntu-24.04-server-cloudimg-arm64.img",
        "sha256": "4a281a921b8d7db952895ab619736f10efe9f63e111fa5b5779ed18f023818aa",
        "qemu_binary": "qemu-system-aarch64",
        "qemu_package": "qemu-system-arm",
        "machine": "virt",
        "block_device": "virtio-blk-device",
        "console": "ttyAMA0",
        "net_device": "virtio-net-device",
    },
    "amd64": {
        "image": "ubuntu-24.04-server-cloudimg-amd64.img",
        "sha256": "6e40c07ae715f744f84af0bec76415cc1987dd115b4b8de437818561f01a3733",
        "qemu_binary": "qemu-system-x86_64",
        "qemu_package": "qemu-system-x86",
        "machine": "q35",
        "block_device": "virtio-blk-pci",
        "console": "ttyS0",
        "net_device": "virtio-net-pci",
    },
}
LOCKED_PACKAGES = {
    "qemu-utils": "1:8.2.2+ds-0ubuntu1.18",
    "ubuntu-keyring": "2023.11.28.1",
    "gpgv": "2.4.4-2ubuntu17.6",
    "cpio": "2.15+dfsg-1ubuntu2.1",
    "gzip": "1.12-1ubuntu3.2",
    "initramfs-tools-core": "0.142ubuntu25.8",
    "util-linux": "2.39.3-9ubuntu6.6",
}
QEMU_TIMEOUT_SECONDS = 360
GUEST_MEMORY_MIB = 2048


class NotReady(Exception):
    """A locked guest prerequisite is not available on this runner."""


class GuestFailure(Exception):
    """The guest started but did not meet the real clock/reboot checks."""


def utc_now():
    return dt.datetime.now(dt.timezone.utc).isoformat()


def command_output(command, *, timeout=30):
    try:
        result = subprocess.run(command, text=True, stdout=subprocess.PIPE,
                                stderr=subprocess.STDOUT, timeout=timeout, check=False)
    except (OSError, subprocess.TimeoutExpired) as error:
        raise NotReady(f"cannot run {command[0]}: {error}") from error
    if result.returncode != 0:
        raise NotReady(f"command failed ({result.returncode}): {shlex.join(command)}: {result.stdout.strip()}")
    return result.stdout.strip()


def logged_command(command, log_path, *, timeout=120, cwd=ROOT, env=None, check=True):
    log_path.parent.mkdir(parents=True, exist_ok=True)
    with log_path.open("a", encoding="utf-8") as stream:
        stream.write("\n$ " + shlex.join(map(str, command)) + "\n")
        stream.flush()
        try:
            result = subprocess.run(command, cwd=cwd, env=env, text=True,
                                    stdout=subprocess.PIPE, stderr=subprocess.STDOUT,
                                    timeout=timeout, check=False)
        except (OSError, subprocess.TimeoutExpired) as error:
            stream.write(f"{type(error).__name__}: {error}\n")
            stream.flush()
            if check:
                raise GuestFailure(f"command did not complete: {shlex.join(map(str, command))}: {error}") from error
            raise
        stream.write(result.stdout)
        if result.stdout and not result.stdout.endswith("\n"):
            stream.write("\n")
        stream.flush()
    if check and result.returncode != 0:
        raise GuestFailure(f"command failed ({result.returncode}): {shlex.join(map(str, command))}: {result.stdout.strip()}")
    return result


def package_version(package):
    output = command_output(["dpkg-query", "-W", "-f=${Version}", package])
    return output.strip()


def host_architecture():
    machine = platform.machine().lower()
    if machine in {"aarch64", "arm64"}:
        return "arm64"
    if machine in {"x86_64", "amd64"}:
        return "amd64"
    raise NotReady(f"QEMU guest probe supports locked amd64/arm64 hosts, found {machine}")


def require_package(package, expected):
    actual = package_version(package)
    if actual != expected:
        raise NotReady(f"{package} version is {actual}, expected locked {expected}")
    return actual


def preflight(arch):
    lock = IMAGE_LOCKS[arch]
    packages = dict(LOCKED_PACKAGES)
    packages[lock["qemu_package"]] = "1:8.2.2+ds-0ubuntu1.18"
    versions = {name: require_package(name, version) for name, version in packages.items()}
    qemu_binary = shutil.which(lock["qemu_binary"])
    if not qemu_binary:
        raise NotReady(f"locked QEMU binary is unavailable: {lock['qemu_binary']}")
    qemu_version = command_output([qemu_binary, "--version"]).splitlines()[0]
    if "8.2.2" not in qemu_version:
        raise NotReady(f"QEMU binary version is not locked: {qemu_version}")
    for path in (KEYRING, pathlib.Path("/dev/nbd0")):
        if not path.exists():
            raise NotReady(f"required verified key/NBD device is unavailable: {path}")
    required_tools = ("gpgv", "unmkinitramfs", "cpio", "gzip", "qemu-nbd", "partprobe",
                      "lsblk", "mount", "umount", "sudo")
    missing = [tool for tool in required_tools if not shutil.which(tool)]
    if missing:
        raise NotReady(f"required locked guest tools are unavailable: {', '.join(missing)}")
    try:
        command_output(["sudo", "-n", "true"])
    except NotReady as error:
        raise NotReady(f"passwordless sudo is required only to mount the verified image read-only: {error}") from error
    memory_bytes = os.sysconf("SC_PHYS_PAGES") * os.sysconf("SC_PAGE_SIZE")
    if memory_bytes < (GUEST_MEMORY_MIB + 512) * 1024 * 1024:
        raise NotReady(f"runner memory {memory_bytes} bytes is too small for a {GUEST_MEMORY_MIB} MiB guest")
    cgroup_limit = pathlib.Path("/sys/fs/cgroup/memory.max")
    if cgroup_limit.is_file():
        raw_limit = cgroup_limit.read_text().strip()
        if raw_limit.isdigit() and int(raw_limit) < (GUEST_MEMORY_MIB + 512) * 1024 * 1024:
            raise NotReady(f"cgroup memory limit {raw_limit} bytes is too small for the guest")
    WORK_ROOT.mkdir(parents=True, exist_ok=True)
    if shutil.disk_usage(WORK_ROOT).free < 2 * 1024 * 1024 * 1024:
        raise NotReady("less than 2 GiB of free space is available for the locked guest image and evidence")
    return {"architecture": arch, "packages": versions, "qemu_binary": qemu_binary,
            "qemu_version": qemu_version, "guest_memory_mib": GUEST_MEMORY_MIB,
            "accelerator": "tcg,thread=multi", "guest_init": "verified-image-initramfs-busybox"}


def go_binary():
    candidates = []
    configured = os.environ.get("NODEDANCE_GO_BIN")
    if configured:
        candidates.append(pathlib.Path(configured))
    candidates.extend([
        ROOT / ".tools/go1.26.8/bin/go",
        ROOT.parent / "NodeDance/.tools/go1.26.8/bin/go",
    ])
    for candidate in candidates:
        if candidate.is_file() and os.access(candidate, os.X_OK):
            version = command_output([str(candidate), "version"])
            if "go1.26.8" not in version:
                raise NotReady(f"Go toolchain is not locked to 1.26.8: {version}")
            return str(candidate)
    raise NotReady("pinned Go 1.26.8 is unavailable; set NODEDANCE_GO_BIN")


def build_probe(arch, output, log_path):
    go = go_binary()
    env = os.environ.copy()
    env.update({"GOTOOLCHAIN": "local", "GOOS": "linux", "GOARCH": arch, "CGO_ENABLED": "0"})
    logged_command([go, "build", "-mod=readonly", "-trimpath", "-ldflags=-s -w",
                    "-o", str(output), "./scripts/test/s03-guest-probe"],
                   log_path, timeout=180, env=env)
    output.chmod(0o755)
    return {"go_version": command_output([go, "version"]),
            "binary_sha256": hashlib.sha256(output.read_bytes()).hexdigest()}


def build_agent_binary(arch, output, log_path):
    go = go_binary()
    env = os.environ.copy()
    env.update({"GOTOOLCHAIN": "local", "GOOS": "linux", "GOARCH": arch, "CGO_ENABLED": "0"})
    logged_command([go, "build", "-mod=readonly", "-trimpath", "-ldflags=-s -w",
                    "-o", str(output), "./cmd/nodedance-agent"],
                   log_path, timeout=180, env=env)
    output.chmod(0o755)
    return {"go_version": command_output([go, "version"]),
            "binary_sha256": hashlib.sha256(output.read_bytes()).hexdigest(),
            "architecture": arch}


def build_privilege_helper(arch, output, log_path):
    go = go_binary()
    env = os.environ.copy()
    env.update({"GOTOOLCHAIN": "local", "GOOS": "linux", "GOARCH": arch, "CGO_ENABLED": "0"})
    logged_command([go, "build", "-mod=readonly", "-trimpath", "-ldflags=-s -w",
                    "-o", str(output), "./scripts/test/s03-privilege-helper"],
                   log_path, timeout=180, env=env)
    output.chmod(0o755)
    return {"go_version": command_output([go, "version"]),
            "binary_sha256": hashlib.sha256(output.read_bytes()).hexdigest(),
            "architecture": arch}


def build_docker_stall_fixture(arch, output, log_path):
    go = go_binary()
    env = os.environ.copy()
    env.update({"GOTOOLCHAIN": "local", "GOOS": "linux", "GOARCH": arch, "CGO_ENABLED": "0"})
    logged_command([go, "build", "-mod=readonly", "-trimpath", "-ldflags=-s -w",
                    "-o", str(output), "./scripts/test/s03-docker-stall-fixture"],
                   log_path, timeout=180, env=env)
    output.chmod(0o755)
    return {"go_version": command_output([go, "version"]),
            "binary_sha256": hashlib.sha256(output.read_bytes()).hexdigest(),
            "architecture": arch}


def load_agent_manifest(path):
    manifest_path = pathlib.Path(path).resolve(strict=True)
    try:
        manifest_path.relative_to(WORK_ROOT.resolve())
    except ValueError as error:
        raise NotReady("guest Agent manifest must live under the owned .artifacts/work-s03 directory") from error
    if manifest_path.is_symlink() or manifest_path.stat().st_mode & 0o077:
        raise NotReady("guest Agent manifest must be a private regular file with mode 0600")
    try:
        value = json.loads(manifest_path.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as error:
        raise NotReady(f"cannot read the guest Agent manifest: {error}") from error
    required = ("serverUrl", "caHostFile", "controlUrl", "enrollmentToken", "nodeId")
    if any(not isinstance(value.get(key), str) or not value[key] for key in required):
        raise NotReady("guest Agent manifest is missing a required field")
    if not re.fullmatch(r"https://10\.0\.2\.2:[0-9]{1,5}", value["serverUrl"]):
        raise NotReady("guest Agent server URL is not the locked isolated QEMU host gateway")
    if not re.fullmatch(r"http://127\.0\.0\.1:[0-9]{1,5}/state", value["controlUrl"]):
        raise NotReady("guest Agent control URL must be an ephemeral loopback-only harness endpoint")
    if not re.fullmatch(r"[A-Za-z0-9_-]{32,256}", value["enrollmentToken"]):
        raise NotReady("guest Agent enrollment token has an invalid format")
    ca_path = pathlib.Path(value["caHostFile"]).resolve(strict=True)
    if ca_path.parent != manifest_path.parent or not ca_path.is_file():
        raise NotReady("guest Core CA must be a regular file beside the owned Agent manifest")
    value["caHostFile"] = str(ca_path)
    return value


def verify_image(cache_dir, arch, log_path):
    lock = IMAGE_LOCKS[arch]
    cache_dir.mkdir(parents=True, exist_ok=True)
    checksums = cache_dir / "SHA256SUMS"
    signature = cache_dir / "SHA256SUMS.gpg"
    for url, target in ((f"{RELEASE_URL}/SHA256SUMS", checksums),
                        (f"{RELEASE_URL}/SHA256SUMS.gpg", signature)):
        if not target.exists():
            logged_command(["curl", "--fail", "--location", "--retry", "2",
                            "--output", str(target), url], log_path, timeout=120)

    signature_check = logged_command([
        "gpgv", "--status-fd=1", "--keyring", str(KEYRING), str(signature), str(checksums)
    ], log_path, timeout=30)
    valid_signatures = re.findall(r"\[GNUPG:\] VALIDSIG ([0-9A-F]+)", signature_check.stdout)
    if SIGNING_FINGERPRINT not in valid_signatures:
        raise GuestFailure(f"Ubuntu image checksums were not signed by the locked key {SIGNING_FINGERPRINT}")

    line = None
    for item in checksums.read_text().splitlines():
        fields = item.split()
        if len(fields) == 2 and fields[1].lstrip("*") == lock["image"]:
            line = item
            break
    if line is None or line.split()[0] != lock["sha256"]:
        raise GuestFailure(f"official signed checksum does not match the locked {lock['image']} SHA256")

    image = cache_dir / lock["image"]
    if not image.exists():
        partial = image.with_suffix(image.suffix + ".part")
        partial.unlink(missing_ok=True)
        try:
            logged_command(["curl", "--fail", "--location", "--retry", "2",
                            "--output", str(partial), f"{RELEASE_URL}/{lock['image']}"],
                           log_path, timeout=900)
        except GuestFailure as error:
            raise NotReady(f"could not fetch the locked Ubuntu image: {error}") from error
        partial.replace(image)
    digest = hashlib.sha256()
    with image.open("rb") as stream:
        for block in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(block)
    actual = digest.hexdigest()
    if actual != lock["sha256"]:
        raise GuestFailure(f"guest image SHA256 mismatch: expected {lock['sha256']}, got {actual}")
    info = json.loads(command_output(["qemu-img", "info", "--output=json", str(image)]))
    if info.get("format") != "qcow2":
        raise GuestFailure(f"locked guest image format is {info.get('format')!r}, expected qcow2")
    return image, {"image": lock["image"], "url": f"{RELEASE_URL}/{lock['image']}",
                   "release": RELEASE_PATH, "sha256": actual,
                   "checksum_signature_fingerprint": SIGNING_FINGERPRINT,
                   "qemu_image_format": info["format"], "virtual_size": info.get("virtual-size")}


INITRAMFS_SCRIPT = r"""#!/bin/sh
set -u
BOOT_EPOCH=@BOOT_EPOCH@

BB=/bin/busybox
PROBE=/usr/local/bin/s03-probe
MARKER=/mnt/var/tmp/nodedance-s03-clock-reboot-state
say() {
  printf '%s\n' "$*"
}
wait_forever() {
  while :; do "$BB" sleep 5; done
}
fatal() {
  say "S03:ERROR $*"
  "$BB" sync
  wait_forever
}

"$BB" mkdir -p /dev /proc /sys /mnt /run
"$BB" mknod -m 600 /dev/console c 5 1 2>/dev/null || true
"$BB" mount -t proc proc /proc || fatal "cannot mount proc"
"$BB" mount -t sysfs sysfs /sys || fatal "cannot mount sysfs"
"$BB" mount -t devtmpfs devtmpfs /dev || fatal "cannot mount devtmpfs"
"$BB" mknod -m 600 /dev/console c 5 1 2>/dev/null || true
exec </dev/console >/dev/console 2>&1

run_sample() {
  tag="$1"
  destination="$2"
  if ! "$PROBE" --mode sample >"$destination" 2>/run/s03-probe-error; then
    say "S03:ERROR sample probe failed: $tag"
    "$BB" cat /run/s03-probe-error
    return 1
  fi
  printf 'S03:%s ' "$tag"
  "$BB" cat "$destination"
  printf '\n'
}
uptime_seconds() {
  "$BB" awk '{print $1}' /proc/uptime
}
start_guest_agent() {
  if [ ! -f /etc/nodedance/server-url ]; then return 0; fi
  server_url=$("$BB" cat /etc/nodedance/server-url)
  if [ -z "$server_url" ]; then fatal "guest Agent server URL is empty"; fi
  "$BB" ip link set lo up || fatal "cannot enable guest loopback"
  identify_agent_nics() {
    primary_nic=
    test_nic=
    for nic_path in /sys/class/net/*/address; do
      [ -e "$nic_path" ] || continue
      nic_name=${nic_path%/address}
      nic_name=${nic_name##*/}
      nic_mac=$($BB cat "$nic_path")
      case "$nic_mac" in
        52:54:00:00:00:01) primary_nic=$nic_name ;;
        52:54:00:00:00:02) test_nic=$nic_name ;;
      esac
    done
  }
  i=0
  while [ "$i" -lt 20 ]; do
    identify_agent_nics
    [ -n "$primary_nic" ] && [ -n "$test_nic" ] && break
    "$BB" sleep 1
    i=$((i + 1))
  done
  if [ -z "$primary_nic" ] || [ -z "$test_nic" ]; then fatal "cannot identify guest NICs by locked MAC"; fi
  printf 'S03:AGENT_NICS %s %s\n' "$primary_nic" "$test_nic"
  "$BB" ip link set "$primary_nic" up || fatal "cannot enable guest Agent network interface"
  "$BB" ip addr add 10.0.2.15/24 dev "$primary_nic" || fatal "cannot assign guest Agent address"
  "$BB" ip route replace default via 10.0.2.2 dev "$primary_nic" metric 100 || fatal "cannot route guest Agent traffic"
  "$BB" ip link set "$test_nic" up || fatal "cannot enable second guest network interface"
  "$BB" ip addr add 192.168.77.15/24 dev "$test_nic" || fatal "cannot assign second guest network address"
  "$BB" mkdir -p /mnt/var/lib/nodedance-agent || fatal "cannot create private Agent config directory"
  chmod 700 /mnt/var/lib/nodedance-agent || fatal "cannot protect Agent config directory"
  /usr/local/bin/s03-privilege-helper chown 65534 65534 \
    /mnt/var/lib/nodedance-agent /etc/nodedance/core-ca.pem /etc/nodedance/enrollment-token \
    || fatal "cannot assign guest Agent credentials to the locked service UID"
  /usr/local/bin/s03-privilege-helper assert-owner 65534 65534 700 \
    /mnt/var/lib/nodedance-agent || fatal "guest Agent config directory owner/mode is invalid"
  /usr/local/bin/s03-privilege-helper assert-owner 65534 65534 600 \
    /etc/nodedance/core-ca.pem /etc/nodedance/enrollment-token \
    || fatal "guest Agent enrollment input owner/mode is invalid"
  service_identity=$(/usr/local/bin/s03-privilege-helper exec 65534 65534 \
    /usr/local/bin/s03-privilege-helper identity) \
    || fatal "cannot verify locked guest Agent service UID"
  if [ "$service_identity" != "uid=65534 gid=65534" ]; then
    fatal "guest Agent service identity mismatch: $service_identity"
  fi
  printf 'S03:AGENT_SERVICE_IDENTITY %s\n' "$service_identity"
  agent_config=/mnt/var/lib/nodedance-agent/agent.json
  if [ ! -f "$agent_config" ]; then
    if ! /usr/local/bin/s03-privilege-helper exec 65534 65534 \
      /usr/local/bin/nodedance-agent enroll --server "$server_url" --token-stdin \
      --ca-file /etc/nodedance/core-ca.pem --config "$agent_config" \
      </etc/nodedance/enrollment-token >/run/agent-enroll.log 2>&1; then
      say "S03:AGENT_NETWORK_DIAGNOSTICS"
      "$BB" ip addr show
      "$BB" ip route show
      "$BB" cat /run/agent-enroll.log
      fatal "guest Agent enrollment failed"
    fi
    say S03:AGENT_ENROLLED
  fi
  /usr/local/bin/s03-privilege-helper assert-owner 65534 65534 600 \
    "$agent_config" || fatal "guest Agent config owner/mode is invalid"
  docker_stall_enabled=0
  if [ -f "$MARKER" ]; then
    unset DOCKER_HOST
  else
    docker_fixture=/usr/local/bin/s03-docker-stall-fixture
    [ -x "$docker_fixture" ] || fatal "locked Docker stall fixture is missing"
    /bin/busybox mkdir -m 700 -p /run/s03-docker-stall || fatal "cannot create private Docker fixture state"
    "$docker_fixture" --socket /run/s03-docker.sock --state-dir /run/s03-docker-stall \
      >/run/s03-docker-stall.log 2>&1 &
    docker_fixture_pid=$!
    i=0
    while [ ! -S /run/s03-docker.sock ] && kill -0 "$docker_fixture_pid" 2>/dev/null && [ "$i" -lt 10 ]; do
      "$BB" sleep 1
      i=$((i + 1))
    done
    if [ ! -S /run/s03-docker.sock ]; then
      "$BB" cat /run/s03-docker-stall.log
      fatal "guest-local Docker API fixture did not create its owned Unix socket"
    fi
    /usr/local/bin/s03-privilege-helper assert-owner 65534 65534 600 \
      /run/s03-docker.sock || fatal "Docker fixture socket ownership/mode is invalid"
    DOCKER_HOST=unix:///run/s03-docker.sock
    export DOCKER_HOST
    docker_stall_enabled=1
  fi
  /usr/local/bin/s03-privilege-helper exec 65534 65534 \
    /usr/local/bin/nodedance-agent run --config "$agent_config" >/run/agent.log 2>&1 &
  agent_pid=$!
  if [ -f "$MARKER" ]; then
    printf 'S03:AGENT_REBOOT_STARTED %s %s %s\n' \
      "$("$BB" cat /proc/sys/kernel/random/boot_id)" \
      "$(uptime_seconds)" "$($BB date -u +%s)"
  else
    printf 'S03:AGENT_STARTED %s %s %s\n' \
      "$("$BB" cat /proc/sys/kernel/random/boot_id)" \
      "$(uptime_seconds)" "$($BB date -u +%s)"
  fi
  i=0
  process_uid=
  process_gid=
  while [ "$i" -lt 5 ]; do
    process_uid=$($BB awk '/^Uid:/ { print $3; exit }' "/proc/$agent_pid/status" 2>/dev/null)
    process_gid=$($BB awk '/^Gid:/ { print $3; exit }' "/proc/$agent_pid/status" 2>/dev/null)
    if [ "$process_uid" = "65534" ] && [ "$process_gid" = "65534" ]; then break; fi
    "$BB" sleep 1
    i=$((i + 1))
  done
  if [ "$process_uid" != "65534" ] || [ "$process_gid" != "65534" ]; then
    fatal "guest Agent process UID/GID mismatch: uid=$process_uid gid=$process_gid"
  fi
  printf 'S03:AGENT_PROCESS_IDENTITY pid=%s uid=%s gid=%s\n' \
    "$agent_pid" "$process_uid" "$process_gid"
  if [ "$docker_stall_enabled" -eq 1 ]; then
    i=0
    while [ ! -f /run/s03-docker-stall/list-started ] && kill -0 "$agent_pid" 2>/dev/null && [ "$i" -lt 20 ]; do
      "$BB" sleep 1
      i=$((i + 1))
    done
    if [ ! -f /run/s03-docker-stall/list-started ]; then
      "$BB" cat /run/s03-docker-stall.log
      "$BB" cat /run/agent.log
      fatal "real Agent Docker SDK did not start its first container-list query"
    fi
    say "S03:AGENT_DOCKER_QUERY_STARTED $($BB date -u +%s)"
    i=0
    while [ ! -f /run/s03-docker-stall/list-cancelled ] && kill -0 "$agent_pid" 2>/dev/null && [ "$i" -lt 20 ]; do
      "$BB" sleep 1
      i=$((i + 1))
    done
    if [ ! -f /run/s03-docker-stall/list-cancelled ]; then
      "$BB" cat /run/s03-docker-stall.log
      "$BB" cat /run/agent.log
      fatal "real Docker SDK request did not observe its bounded context cancellation"
    fi
    say "S03:AGENT_DOCKER_QUERY_CANCELLED $($BB date -u +%s)"
    i=0
    while [ "$i" -lt 12 ]; do
      if ! kill -0 "$agent_pid" 2>/dev/null; then
        "$BB" cat /run/agent.log
        fatal "Agent stopped during Docker stall isolation observation"
      fi
      "$BB" sleep 1
      i=$((i + 1))
    done
    say S03:AGENT_DOCKER_QUERY_OBSERVED
  else
    i=0
    while [ "$i" -lt 12 ]; do
      if ! kill -0 "$agent_pid" 2>/dev/null; then
        "$BB" cat /run/agent.log
        fatal "guest Agent exited before initial metrics were sent"
      fi
      "$BB" sleep 1
      i=$((i + 1))
    done
  fi
  say S03:AGENT_INITIAL_WAIT_DONE
}

"$BB" mkdir -p /mnt/var/tmp
i=0
while [ ! -b /dev/vda1 ] && [ "$i" -lt 20 ]; do
  "$BB" sleep 1
  i=$((i + 1))
done
"$BB" mount -t ext4 -o rw /dev/vda1 /mnt || fatal "cannot mount guest root filesystem"
"$BB" mkdir -p /mnt/second || fatal "cannot create secondary mountpoint"
"$BB" mount -t tmpfs -o size=128m tmpfs /mnt/second || fatal "cannot mount secondary tmpfs"
if [ -f "$MARKER" ]; then
  set -- $($BB cat "$MARKER")
  if [ "$#" -ne 3 ]; then fatal "invalid reboot marker while restoring guest wall clock"; fi
  before_wall="$3"
  boot_elapsed=$($BB awk '{printf "%d", $1}' /proc/uptime)
  restored_wall=$((before_wall + boot_elapsed))
  if ! "$BB" date -u -s "@$restored_wall" >/run/s03-reboot-time.log 2>&1; then
    "$BB" cat /run/s03-reboot-time.log
    fatal "cannot restore the controlled guest-only wall clock after reboot"
  fi
  say "S03:GUEST_TIME_RESTORED $($BB date -u +%s)"
else
  if ! "$BB" date -u -s "@$BOOT_EPOCH" >/run/s03-initial-time.log 2>&1; then
    "$BB" cat /run/s03-initial-time.log
    fatal "cannot set guest-only initial wall clock"
  fi
  say "S03:GUEST_TIME_SET $($BB date -u +%s)"
fi
start_guest_agent

if [ -f "$MARKER" ]; then
  set -- $("$BB" cat "$MARKER")
  if [ "$#" -ne 3 ]; then fatal "invalid reboot marker"; fi
  before_boot_id="$1"
  before_uptime="$2"
  before_wall="$3"
  after_boot_id=$("$BB" cat /proc/sys/kernel/random/boot_id)
  after_uptime=$(uptime_seconds)
  after_wall=$("$BB" date -u +%s)
  if [ -f /etc/nodedance/server-url ]; then
    "$BB" sleep 12
    if ! kill -0 "$agent_pid" 2>/dev/null; then
      "$BB" cat /run/agent.log
      fatal "guest Agent exited after reboot"
    fi
  fi
  after_boot_id=$($BB cat /proc/sys/kernel/random/boot_id)
  after_uptime=$(uptime_seconds)
  after_wall=$($BB date -u +%s)
  printf 'S03:REBOOT_POST %s %s %s %s %s %s\n' \
    "$before_boot_id" "$before_uptime" "$before_wall" \
    "$after_boot_id" "$after_uptime" "$after_wall"
  if ! run_sample AFTER_REBOOT /run/s03-after-reboot.json; then fatal "post-reboot sample failed"; fi
  "$BB" rm -f "$MARKER"
  "$BB" sync
  "$BB" umount /mnt/second || fatal "cannot unmount secondary filesystem after reboot"
  "$BB" umount /mnt || fatal "cannot unmount guest root filesystem after reboot"
  say S03:DONE
  wait_forever
fi

load_status=0
if [ -f /etc/nodedance/server-url ]; then
  "$PROBE" --mode load --agent-marker /run/s03-agent-load-active >/run/s03-load.json 2>/run/s03-load-error &
  load_pid=$!
  i=0
  while [ ! -f /run/s03-agent-load-active ] && kill -0 "$load_pid" 2>/dev/null && [ "$i" -lt 60 ]; do
    "$BB" sleep 1
    i=$((i + 1))
  done
  if [ -f /run/s03-agent-load-active ]; then say S03:AGENT_LOAD_ACTIVE; fi
  if wait "$load_pid"; then load_status=0; else load_status=$?; fi
  "$BB" rm -f /run/s03-agent-load-active
  say S03:AGENT_LOAD_RELEASED
  "$BB" sleep 12
  say S03:AGENT_LOAD_RECOVERY_SETTLED
else
  if "$PROBE" --mode load >/run/s03-load.json 2>/run/s03-load-error; then load_status=0; else load_status=$?; fi
fi
printf 'S03:LOAD_STATUS %s\n' "$load_status"
printf 'S03:LOAD '
"$BB" cat /run/s03-load.json
printf '\n'
if [ -s /run/s03-load-error ]; then
  printf 'S03:LOAD_ERROR '
  "$BB" cat /run/s03-load-error
fi
if [ -f /etc/nodedance/server-url ]; then
  interface_before_index=$("$BB" cat "/sys/class/net/$test_nic/ifindex")
  interface_before_mac=$("$BB" cat "/sys/class/net/$test_nic/address")
  "$BB" ip link set "$test_nic" down || fatal "cannot lower the guest test interface for identity replacement"
  "$BB" ip link set "$test_nic" address 02:ff:00:00:00:03 || fatal "cannot replace guest test interface MAC"
  "$BB" ip link set "$test_nic" up || fatal "cannot restore the guest test interface after identity replacement"
  interface_after_index=$("$BB" cat "/sys/class/net/$test_nic/ifindex")
  interface_after_mac=$("$BB" cat "/sys/class/net/$test_nic/address")
  if [ "$interface_before_index" != "$interface_after_index" ] || [ "$interface_before_mac" = "$interface_after_mac" ]; then
    fatal "guest test interface did not keep its index and change MAC identity"
  fi
  printf 'S03:AGENT_INTERFACE_REPLACED %s %s %s %s %s\n' \
    "$test_nic" "$interface_before_index" "$interface_before_mac" \
    "$interface_after_index" "$interface_after_mac"
  "$BB" sleep 12
say S03:AGENT_INTERFACE_REPLACEMENT_SETTLED
  if [ -f /etc/nodedance/server-url ]; then
    "$BB" kill -TERM "$agent_pid" || fatal "cannot stop owned Agent for permission test"
    i=0
    while kill -0 "$agent_pid" 2>/dev/null && [ "$i" -lt 8 ]; do
      "$BB" sleep 1
      i=$((i + 1))
    done
    if kill -0 "$agent_pid" 2>/dev/null; then
      "$BB" kill -KILL "$agent_pid" || fatal "cannot finish stopping owned Agent for permission test"
    fi
    wait "$agent_pid" 2>/dev/null || true
    permission_proc=/run/s03-agent-proc
    "$BB" mkdir -p "$permission_proc/net" "$permission_proc/self" \
      "$permission_proc/1" \
      "$permission_proc/sys/kernel/random" || fatal "cannot create restricted proc view"
    "$BB" ln -s /proc/stat "$permission_proc/stat" || fatal "cannot link real proc stat"
    "$BB" ln -s /proc/cpuinfo "$permission_proc/cpuinfo" || fatal "cannot link real cpuinfo"
    "$BB" ln -s /proc/uptime "$permission_proc/uptime" || fatal "cannot link real uptime"
    "$BB" ln -s /proc/filesystems "$permission_proc/filesystems" || fatal "cannot link real filesystems"
    "$BB" ln -s /proc/net/dev "$permission_proc/net/dev" || fatal "cannot link real network counters"
    "$BB" ln -s /proc/self/mounts "$permission_proc/self/mounts" || fatal "cannot link real mount table"
    "$BB" ln -s /proc/1/mountinfo "$permission_proc/1/mountinfo" || fatal "cannot link real PID-1 mountinfo"
    "$BB" ln -s /proc/sys/kernel/random/boot_id \
      "$permission_proc/sys/kernel/random/boot_id" || fatal "cannot link real boot ID"
    "$BB" cp /proc/meminfo "$permission_proc/meminfo" || fatal "cannot create real meminfo permission fixture"
    chmod 000 "$permission_proc/meminfo" || fatal "cannot restrict meminfo fixture permissions"
    /usr/local/bin/s03-privilege-helper assert-owner 65534 65534 700 \
      /mnt/var/lib/nodedance-agent || fatal "Agent config directory owner changed during permission test"
    /usr/local/bin/s03-privilege-helper assert-owner 65534 65534 600 \
      "$agent_config" /etc/nodedance/core-ca.pem \
      || fatal "Agent credentials owner changed during permission test"
    say S03:AGENT_PERMISSION_FAULT
    HOST_PROC="$permission_proc" /usr/local/bin/s03-privilege-helper exec 65534 65534 \
      /usr/local/bin/nodedance-agent run --config "$agent_config" \
      >/run/agent.log 2>&1 &
    agent_pid=$!
    "$BB" sleep 12
    if ! kill -0 "$agent_pid" 2>/dev/null; then
      "$BB" cat /run/agent.log
      fatal "restricted Agent exited during permission-denial observation"
    fi
    chmod 444 "$permission_proc/meminfo" || fatal "cannot restore meminfo fixture permissions"
    say S03:AGENT_PERMISSION_RECOVERED
    "$BB" sleep 12
    if ! kill -0 "$agent_pid" 2>/dev/null; then
      "$BB" cat /run/agent.log
      fatal "restricted Agent exited after permission recovery"
    fi
  fi
fi
if ! run_sample BEFORE_CLOCK /run/s03-before-clock.json; then fatal "pre-clock sample failed"; fi

clock_before_boot=$("$BB" cat /proc/sys/kernel/random/boot_id)
clock_before_uptime=$(uptime_seconds)
clock_before_wall=$("$BB" date -u +%s)
clock_target=$((clock_before_wall + 3600))
printf 'S03:CLOCK_PRE %s %s %s %s\n' \
  "$clock_before_boot" "$clock_before_uptime" "$clock_before_wall" "$clock_target"
if ! "$BB" date -u -s "@$clock_target" >/run/s03-clock-set.log 2>&1; then
  "$BB" cat /run/s03-clock-set.log
  fatal "guest-only clock correction failed"
fi
clock_after_boot=$("$BB" cat /proc/sys/kernel/random/boot_id)
clock_after_uptime=$(uptime_seconds)
clock_after_wall=$("$BB" date -u +%s)
printf 'S03:CLOCK_POST %s %s %s\n' \
  "$clock_after_boot" "$clock_after_uptime" "$clock_after_wall"
if ! run_sample AFTER_CLOCK /run/s03-after-clock.json; then fatal "post-clock sample failed"; fi
if [ -f /etc/nodedance/server-url ]; then
  "$BB" sleep 7
  if ! kill -0 "$agent_pid" 2>/dev/null; then
    "$BB" cat /run/agent.log
    fatal "guest Agent exited after guest-only clock correction"
  fi
  say S03:AGENT_CLOCK_SETTLE_DONE
  "$BB" ip link set "$primary_nic" down || fatal "cannot isolate the guest Agent network"
  say S03:AGENT_NETWORK_ISOLATED
  "$BB" sleep 20
  "$BB" ip link set "$primary_nic" up || fatal "cannot restore the guest Agent network"
  say S03:AGENT_NETWORK_RESTORED
  "$BB" sleep 12
  if ! kill -0 "$agent_pid" 2>/dev/null; then
    "$BB" cat /run/agent.log
    fatal "guest Agent exited during network recovery"
  fi
  say S03:AGENT_NETWORK_RECOVERED
fi

reboot_before_boot=$("$BB" cat /proc/sys/kernel/random/boot_id)
reboot_before_uptime=$(uptime_seconds)
reboot_before_wall=$("$BB" date -u +%s)
printf 'S03:REBOOT_PRE %s %s %s\n' \
  "$reboot_before_boot" "$reboot_before_uptime" "$reboot_before_wall"
"$BB" printf '%s %s %s\n' "$reboot_before_boot" "$reboot_before_uptime" "$reboot_before_wall" >"$MARKER"
"$BB" sync
"$BB" umount /mnt/second || fatal "cannot unmount secondary filesystem before reboot"
"$BB" umount /mnt || fatal "cannot unmount guest root filesystem before reboot"
say S03:REBOOT_REQUESTED
"$BB" sleep 1
"$BB" reboot -f
wait_forever
"""


def guest_command(command, log_path, *, timeout=120, check=True):
    return logged_command(["sudo", "-n", *command], log_path, timeout=timeout, check=check)


def nbd_event(log_path, message):
    with log_path.open("a", encoding="utf-8") as stream:
        stream.write(f"S03 NBD: {message}\n")
        stream.flush()


def verify_nbd_device_node(device_path, sysfs_dir):
    try:
        device_stat = os.stat(device_path)
    except OSError as error:
        raise NotReady(f"cannot stat locked NBD device {device_path}: {error}") from error
    if not stat.S_ISBLK(device_stat.st_mode):
        raise NotReady(f"locked NBD path is not a block device: {device_path}")
    expected = f"{os.major(device_stat.st_rdev)}:{os.minor(device_stat.st_rdev)}"
    sysfs_identity = (sysfs_dir / "dev").read_text(encoding="ascii").strip()
    if sysfs_identity != expected:
        raise NotReady(
            f"locked NBD device identity changed: {device_path}={expected}, sysfs={sysfs_identity}")


def verify_no_nbd_mounts(sysfs_dir, mountinfo, swaps, device_path):
    try:
        assert_nbd_unmounted(sysfs_dir, mountinfo, swaps, device_path=device_path)
    except ResourceBusy as error:
        raise OwnershipLost(str(error)) from error


def verify_nbd_idle_after_disconnect(sysfs_dir, proc_root, device_path, timeout=5):
    deadline = time.monotonic() + timeout
    last_error = ""
    while time.monotonic() < deadline:
        try:
            assert_nbd_idle(sysfs_dir, read_mountinfo(),
                            pathlib.Path("/proc/swaps").read_text(encoding="utf-8"),
                            proc_root=proc_root, device_path=device_path)
            return
        except (ResourceBusy, OwnershipLost, OSError) as error:
            last_error = str(error)
            time.sleep(0.1)
    raise OwnershipLost(f"NBD device did not return to idle after disconnect: {last_error}")


def extract_kernel_and_initrd(image, round_dir, log_path):
    nbd_device = NBD_DEVICE
    sysfs_dir = NBD_SYSFS_DIR
    mount_dir = round_dir / "verified-boot-mount"
    mount_dir.mkdir(mode=0o700)
    lock = None
    owner = None
    attach_attempted = False
    mount_attempted = False
    mount_identity = None
    mount_before_mount = None
    boot_partition = None
    boot_device_number = None
    copied = False
    cleanup_errors = []
    try:
        lock = acquire_exclusive_lock(NBD_LOCK)
        verify_nbd_device_node(nbd_device, sysfs_dir)
        assert_nbd_idle(
            sysfs_dir, read_mountinfo(), pathlib.Path("/proc/swaps").read_text(encoding="utf-8"),
            device_path=nbd_device)
        if mount_identity_at(read_mountinfo(), mount_dir) is not None:
            raise ResourceBusy(f"private boot mountpoint is already mounted: {mount_dir}")
        nbd_event(log_path, f"exclusive lock acquired; verified {nbd_device} idle before attach")

        processes_before_attach = tuple(qemu_nbd_processes(device_path=nbd_device))
        if processes_before_attach:
            raise ResourceBusy(
                f"qemu-nbd process appeared before attach on {nbd_device}: "
                + ", ".join(str(process.pid) for process in processes_before_attach))
        owner_pid_file = round_dir / "nbd-launch.pid"
        if owner_pid_file.exists() or owner_pid_file.is_symlink():
            raise OwnershipLost(f"private current-run PID file already exists: {owner_pid_file}")
        attach_attempted = True
        guest_command(["qemu-nbd", f"--pid-file={owner_pid_file}",
                       "--read-only", "--format=qcow2",
            f"--connect={nbd_device}", str(image.resolve()),
        ], log_path, timeout=30)
        guest_command(["chown", f"{os.getuid()}:{os.getgid()}", str(owner_pid_file)],
                      log_path, timeout=10)
        for _ in range(50):
            try:
                owner = capture_qemu_nbd_owner(
                    nbd_device, image, sysfs_dir,
                    pid_file=owner_pid_file,
                    processes_before_attach=processes_before_attach)
                break
            except OwnershipLost:
                time.sleep(0.1)
        if owner is None:
            raise GuestFailure(
                f"qemu-nbd attached {nbd_device} but no verifiable current-run PID was found")
        owner_path = round_dir / "nbd-owner.json"
        sysfs_pid = ((sysfs_dir / "pid").read_text(encoding="ascii").strip()
                     if (sysfs_dir / "pid").exists() else None)
        sysfs_tgid = (process_identity(int(sysfs_pid)).tgid
                      if sysfs_pid is not None and int(sysfs_pid) > 0 else None)
        owner_path.write_text(json.dumps({
            "device": nbd_device, "image": str(image.resolve()), "pid": owner.pid,
            "tgid": owner.tgid, "pidFile": str(owner_pid_file),
            "processStartTime": owner.start_time, "command": owner.command,
            "sysfsPid": sysfs_pid, "sysfsPidTgid": sysfs_tgid,
            "sysfsPidStartTime": owner.sysfs_thread_start_time,
        }, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
        owner_path.chmod(0o600)
        nbd_event(log_path, f"captured current-run qemu-nbd PID={owner.pid} start={owner.start_time}")

        verify_qemu_nbd_owner(owner, nbd_device, image, sysfs_dir, pid_file=owner_pid_file)
        guest_command(["partprobe", nbd_device], log_path, timeout=10)
        labels = {}
        listing = ""
        for _ in range(50):
            verify_qemu_nbd_owner(owner, nbd_device, image, sysfs_dir, pid_file=owner_pid_file)
            listing = command_output(["lsblk", "-nrpo", "PATH,LABEL", nbd_device])
            labels = {}
            for line in listing.splitlines():
                fields = line.split(maxsplit=1)
                if len(fields) == 2:
                    labels[fields[1]] = fields[0]
            if "BOOT" in labels:
                break
            time.sleep(0.1)
        boot_partition = labels.get("BOOT")
        if not boot_partition:
            raise GuestFailure(f"verified Ubuntu BOOT partition was not enumerated after 5s: {listing.strip()}")
        partition_stat = os.stat(boot_partition)
        if not stat.S_ISBLK(partition_stat.st_mode):
            raise OwnershipLost(f"BOOT partition is not a block device: {boot_partition}")
        boot_device_number = f"{os.major(partition_stat.st_rdev)}:{os.minor(partition_stat.st_rdev)}"
        if boot_device_number not in nbd_device_numbers(sysfs_dir):
            raise OwnershipLost(
                f"BOOT partition {boot_partition} identity {boot_device_number} is not under {nbd_device}")

        expected_mount = mount_dir.resolve()
        if mount_identity_at(read_mountinfo(), expected_mount) is not None:
            raise ResourceBusy(f"private boot mountpoint became mounted before this run: {expected_mount}")
        verify_qemu_nbd_owner(owner, nbd_device, image, sysfs_dir, pid_file=owner_pid_file)
        mount_attempted = True
        mount_before_mount = read_mountinfo()
        guest_command(["mount", "-o", "ro", boot_partition, str(expected_mount)], log_path, timeout=20)
        mount_after_mount = read_mountinfo()
        mount_identity = capture_new_mount_identity(
            mount_before_mount, mount_after_mount, expected_mount, boot_device_number)
        if not verify_mount_identity(read_mountinfo(), mount_identity):
            raise OwnershipLost("boot-partition mount identity changed immediately after capture")
        nbd_event(log_path, f"captured mount id={mount_identity.mount_id} dev={mount_identity.device} target={mount_identity.target}")

        kernels = sorted(expected_mount.glob("vmlinuz-*"))
        initrds = sorted(expected_mount.glob("initrd.img-*"))
        pairs = [(kernel, expected_mount / f"initrd.img-{kernel.name.removeprefix('vmlinuz-')}")
                 for kernel in kernels]
        pairs = [(kernel, initrd) for kernel, initrd in pairs if initrd.is_file()]
        if len(pairs) != 1 or len(initrds) != 1:
            raise GuestFailure("locked Ubuntu image must contain exactly one matching kernel/initrd pair")

        kernel_source, initrd_source = pairs[0]
        kernel_copy = round_dir / "guest-kernel"
        initrd_copy = round_dir / "guest-initrd-original"
        for source, destination in ((kernel_source, kernel_copy), (initrd_source, initrd_copy)):
            verify_qemu_nbd_owner(owner, nbd_device, image, sysfs_dir, pid_file=owner_pid_file)
            if not verify_mount_identity(read_mountinfo(), mount_identity):
                raise OwnershipLost("boot mount identity changed before copying image files")
            guest_command(["cp", str(source), str(destination)], log_path, timeout=60)
            guest_command(["chown", f"{os.getuid()}:{os.getgid()}", str(destination)],
                          log_path, timeout=10)
            destination.chmod(0o444)
        copied = True
    except ResourceBusy as error:
        nbd_event(log_path, f"NOT_READY: owned NBD resource unavailable: {error}")
        raise NotReady(f"isolated guest extraction could not acquire {nbd_device}: {error}") from error
    except OwnershipLost as error:
        nbd_event(log_path, f"FAIL: NBD ownership verification failed: {error}")
        raise GuestFailure(f"isolated guest NBD ownership verification failed: {error}") from error
    finally:
        if mount_attempted and mount_identity is None:
            try:
                current_mount = mount_identity_at(read_mountinfo(), mount_dir.resolve())
                if current_mount is not None:
                    raise OwnershipLost(
                        f"mount identity was not captured by this run; refusing umount: {current_mount}")
            except (OSError, OwnershipLost) as error:
                cleanup_errors.append(f"cannot identify possible run mount: {error}")
        if mount_identity is not None:
            try:
                if owner is None:
                    raise OwnershipLost("cannot unmount because current-run qemu-nbd ownership was not verified")
                verify_qemu_nbd_owner(owner, nbd_device, image, sysfs_dir, pid_file=owner_pid_file)
                if unmount_owned_mount(
                        mount_identity,
                        get_mountinfo=read_mountinfo,
                        unmount=lambda target: guest_command(["umount", target], log_path, timeout=20)):
                    nbd_event(log_path, f"unmounted owned mount id={mount_identity.mount_id}")
                verify_no_nbd_mounts(
                    sysfs_dir, read_mountinfo(), pathlib.Path("/proc/swaps").read_text(encoding="utf-8"),
                    nbd_device)
            except Exception as error:  # Preserve a safe leak rather than disconnect an unowned device.
                cleanup_errors.append(f"owned mount cleanup failed: {error}")
                nbd_event(log_path, f"CLEANUP_FAILURE; will not disconnect NBD: {cleanup_errors[-1]}")

        if attach_attempted:
            if owner is None:
                try:
                    assert_nbd_idle(
                        sysfs_dir, read_mountinfo(), pathlib.Path("/proc/swaps").read_text(encoding="utf-8"),
                        proc_root=pathlib.Path("/proc"), device_path=nbd_device)
                except Exception as error:
                    cleanup_errors.append(
                        f"qemu-nbd PID ownership was not captured by this run; refusing disconnect: {error}")
                    nbd_event(log_path, f"CLEANUP_FAILURE: {cleanup_errors[-1]}")
            if owner is not None and not cleanup_errors:
                try:
                    disconnect_owned_nbd(
                        owner, nbd_device, image, sysfs_dir,
                        mountinfo=read_mountinfo(),
                        swaps=pathlib.Path("/proc/swaps").read_text(encoding="utf-8"),
                        disconnect=lambda: guest_command(
                            ["qemu-nbd", "--disconnect", nbd_device], log_path, timeout=20),
                        pid_file=owner_pid_file,
                    )
                    verify_nbd_idle_after_disconnect(sysfs_dir, pathlib.Path("/proc"), nbd_device)
                    nbd_event(log_path, f"disconnected owned NBD after verifying PID={owner.pid} and no mounts")
                except Exception as error:
                    cleanup_errors.append(f"owned NBD disconnect cleanup failed: {error}")
                    nbd_event(log_path, f"CLEANUP_FAILURE: {cleanup_errors[-1]}")
            elif owner is None:
                if cleanup_errors:
                    nbd_event(log_path, "CLEANUP_FAILURE; no verified qemu-nbd owner, leaving device untouched")
                else:
                    nbd_event(log_path, "attach did not establish an NBD owner; verified idle, no disconnect needed")
            elif cleanup_errors:
                nbd_event(log_path, "CLEANUP_FAILURE; mount cleanup was not verified, leaving NBD connected")

        if mount_dir.exists():
            try:
                if mount_identity_at(read_mountinfo(), mount_dir.resolve()) is not None:
                    raise OwnershipLost("refusing to remove a mountpoint with an active mount")
                mount_dir.rmdir()
            except Exception as error:
                cleanup_errors.append(f"private mountpoint cleanup failed: {error}")
                nbd_event(log_path, f"CLEANUP_FAILURE: {cleanup_errors[-1]}")
        if lock is not None:
            lock.close()
        if cleanup_errors:
            raise GuestFailure("NBD cleanup was not fully verified: " + "; ".join(cleanup_errors))
        if not copied and attach_attempted:
            nbd_event(log_path, "current-run NBD attachment was cleaned up after an incomplete extraction")

    kernel_image = round_dir / "guest-kernel-image"
    with kernel_copy.open("rb") as source:
        magic = source.read(2)
    if magic == b"\x1f\x8b":
        with gzip.open(kernel_copy, "rb") as source, kernel_image.open("wb") as destination:
            shutil.copyfileobj(source, destination)
    else:
        shutil.copyfile(kernel_copy, kernel_image)
    kernel_copy.unlink()
    return kernel_image, initrd_copy


def build_guest_initrd(arch, initrd_source, probe, round_dir, log_path,
                       agent_binary=None, agent_manifest=None, privilege_helper_binary=None,
                       docker_stall_fixture_binary=None):
    unpacked = round_dir / "unpacked-initrd"
    logged_command(["unmkinitramfs", str(initrd_source), str(unpacked)],
                   log_path, timeout=120)
    root = round_dir / "initramfs-root"
    shutil.copytree(unpacked / "main", root, symlinks=True)
    shutil.copytree(unpacked / "early", root, dirs_exist_ok=True, symlinks=True)

    busybox = root / "usr/bin/busybox"
    if not busybox.is_file():
        raise GuestFailure("verified Ubuntu initramfs has no BusyBox binary")
    busybox_info = command_output([str(busybox), "--help"]).splitlines()[0]
    if "BusyBox v1.36.1 (Ubuntu 1:1.36.1-6ubuntu3.1)" not in busybox_info:
        raise GuestFailure(f"BusyBox version from signed image is not locked: {busybox_info}")
    applets = set(command_output([str(busybox), "--list"]).splitlines())
    required_applets = {"awk", "cat", "date", "mkdir", "mknod", "mount", "printf", "reboot",
                        "rm", "sleep", "sync", "umount"}
    missing_applets = sorted(required_applets - applets)
    if missing_applets:
        raise GuestFailure(f"verified BusyBox is missing required applets: {', '.join(missing_applets)}")

    init_path = root / "init"
    init_path.write_text(INITRAMFS_SCRIPT.replace("@BOOT_EPOCH@", str(int(time.time()))), encoding="utf-8")
    init_path.chmod(0o755)
    probe_path = root / "usr/local/bin/s03-probe"
    probe_path.parent.mkdir(parents=True, exist_ok=True)
    shutil.copyfile(probe, probe_path)
    probe_path.chmod(0o755)

    if agent_binary is not None and agent_manifest is not None:
        if privilege_helper_binary is None:
            raise GuestFailure("real Agent guest integration requires the locked privilege helper")
        agent_path = root / "usr/local/bin/nodedance-agent"
        agent_path.parent.mkdir(parents=True, exist_ok=True)
        shutil.copyfile(agent_binary, agent_path)
        agent_path.chmod(0o755)
        nodedance_dir = root / "etc/nodedance"
        nodedance_dir.mkdir(parents=True, exist_ok=True)
        ca_host_path = pathlib.Path(agent_manifest["caHostFile"])
        ca_data = ca_host_path.read_bytes()
        ca_path = nodedance_dir / "core-ca.pem"
        ca_path.write_bytes(ca_data)
        ca_path.chmod(0o600)
        server_path = nodedance_dir / "server-url"
        server_path.write_text(agent_manifest["serverUrl"] + "\n", encoding="utf-8")
        server_path.chmod(0o600)
        token_path = nodedance_dir / "enrollment-token"
        token_path.write_text(agent_manifest["enrollmentToken"] + "\n", encoding="utf-8")
        token_path.chmod(0o600)
        helper_path = root / "usr/local/bin/s03-privilege-helper"
        shutil.copyfile(privilege_helper_binary, helper_path)
        helper_path.chmod(0o755)
        if docker_stall_fixture_binary is None:
            raise GuestFailure("real Agent guest integration requires the pinned Docker stall fixture")
        docker_fixture_path = root / "usr/local/bin/s03-docker-stall-fixture"
        shutil.copyfile(docker_stall_fixture_binary, docker_fixture_path)
        docker_fixture_path.chmod(0o755)

    entries = ["."]
    for current, directories, files in os.walk(root, followlinks=False):
        directories.sort()
        files.sort()
        current_path = pathlib.Path(current)
        for name in directories + files:
            entries.append(str((current_path / name).relative_to(root)))
    cpio_path = round_dir / "guest-initramfs.cpio"
    archive_input = ("\0".join(entries) + "\0").encode()
    with cpio_path.open("wb") as output:
        result = subprocess.run(
            ["cpio", "--null", "--create", "--format=newc", "--owner=0:0"],
            cwd=root, input=archive_input, stdout=output, stderr=subprocess.PIPE,
            timeout=180, check=False)
    if result.returncode:
        raise GuestFailure(f"cannot package minimal guest initramfs: {result.stderr.decode(errors='replace')}")
    final_initrd = round_dir / "guest-initramfs"
    with cpio_path.open("rb") as source, final_initrd.open("wb") as destination:
        result = subprocess.run(["gzip", "-n", "-6", "-c"], stdin=source, stdout=destination,
                                stderr=subprocess.PIPE, timeout=180, check=False)
    if result.returncode:
        raise GuestFailure(f"cannot compress minimal guest initramfs: {result.stderr.decode(errors='replace')}")
    cpio_path.unlink()
    versions = sorted(path.name for path in (root / "usr/lib/modules").iterdir()
                      if path.is_dir())
    if len(versions) != 1:
        raise GuestFailure(f"verified initramfs contains ambiguous kernel module versions: {versions}")
    return final_initrd, {
        "kernel_version": versions[0],
        "kernel_sha256": hashlib.sha256((round_dir / "guest-kernel-image").read_bytes()).hexdigest(),
        "original_initrd_sha256": hashlib.sha256(initrd_source.read_bytes()).hexdigest(),
        "test_initrd_sha256": hashlib.sha256(final_initrd.read_bytes()).hexdigest(),
        "busybox_version": busybox_info,
        "busybox_sha256": hashlib.sha256(busybox.read_bytes()).hexdigest(),
        "probe_sha256": hashlib.sha256(probe.read_bytes()).hexdigest(),
        "architecture": arch,
        "agent_sha256": hashlib.sha256(agent_binary.read_bytes()).hexdigest() if agent_binary is not None else "",
        "agent_enabled": agent_binary is not None,
    }


def monitor_command(path, command):
    client = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
    client.settimeout(3)
    try:
        client.connect(str(path))
        try:
            client.recv(4096)
        except socket.timeout:
            pass
        client.sendall((command + "\n").encode())
        try:
            client.recv(4096)
        except socket.timeout:
            pass
    finally:
        client.close()


def stop_qemu(qemu, monitor_path, log_path):
    if qemu is None or qemu.poll() is not None:
        return
    try:
        monitor_command(monitor_path, "system_powerdown")
    except OSError as error:
        with log_path.open("a", encoding="utf-8") as stream:
            stream.write(f"QEMU monitor shutdown request: {error}\n")
    try:
        qemu.wait(timeout=3)
        return
    except subprocess.TimeoutExpired:
        pass
    qemu.terminate()
    try:
        qemu.wait(timeout=10)
    except subprocess.TimeoutExpired:
        qemu.kill()
        qemu.wait(timeout=5)


def qemu_command(arch, lock, overlay, kernel, initrd, serial_log, monitor_path, qemu_binary,
                agent_enabled=False):
    kernel_arguments = f"console={lock['console']} rdinit=/init panic=10"
    command = [qemu_binary, "-name", "nodedance-s03-clock-reboot-guest",
            "-machine", lock["machine"], "-cpu", "max", "-accel", "tcg,thread=multi",
            "-smp", "2", "-m", str(GUEST_MEMORY_MIB), "-rtc", "base=utc,clock=vm",
            "-kernel", str(kernel), "-initrd", str(initrd), "-append", kernel_arguments,
            "-drive", f"if=none,id=osdisk,file={overlay},format=qcow2",
            "-device", f"{lock['block_device']},drive=osdisk",
            "-serial", f"file:{serial_log}", "-monitor",
            f"unix:{monitor_path},server=on,wait=off", "-display", "none"]
    if agent_enabled:
        command.extend(["-netdev", "user,id=net0,net=10.0.2.0/24,host=10.0.2.2",
                        "-device", f"{lock['net_device']},netdev=net0,id=guestnic0,mac=52:54:00:00:00:01",
                        "-netdev", "user,id=net1,net=192.168.77.0/24,host=192.168.77.1",
                        "-device", f"{lock['net_device']},netdev=net1,id=guestnic1,mac=52:54:00:00:00:02"])
    return command


def serial_events(serial_log):
    data = serial_log.read_text(encoding="utf-8", errors="replace").replace("\r", "\n")
    events = {}
    for line in data.splitlines():
        if not line.startswith("S03:"):
            continue
        tag, _, value = line.partition(" ")
        events[tag[4:]] = value.strip()
    return events, data


def control_state(control_url):
    request = urllib.request.Request(control_url, headers={"Accept": "application/json"})
    try:
        with urllib.request.urlopen(request, timeout=2) as response:
            return json.loads(response.read(1 << 20))
    except (OSError, urllib.error.URLError, json.JSONDecodeError):
        return None


def state_view(payload):
    if not isinstance(payload, dict):
        return None, None
    if payload.get("type") == "node_status":
        return None, payload.get("state")
    if "metrics" not in payload:
        return None, payload.get("state")
    return payload, {
        "status": payload.get("nodeStatus", "unknown"),
        "generation": payload.get("activeGeneration", 0),
        "serverTime": payload.get("serverTime", ""),
        "leaseValidUntil": payload.get("leaseValidUntil", ""),
    }


def phase_at(events):
    if "REBOOT_POST" in events or "AGENT_REBOOT_STARTED" in events:
        return "post_reboot"
    if "REBOOT_PRE" in events:
        return "pre_reboot"
    if "AGENT_NETWORK_RECOVERED" in events:
        return "network_recovered"
    if "AGENT_NETWORK_RESTORED" in events:
        return "network_recovery"
    if "AGENT_NETWORK_ISOLATED" in events:
        return "network_isolated"
    if "AGENT_PERMISSION_RECOVERED" in events:
        return "permission_recovery"
    if "AGENT_PERMISSION_FAULT" in events:
        return "permission_fault"
    if "AGENT_CLOCK_SETTLE_DONE" in events:
        return "post_clock"
    if "CLOCK_POST" in events:
        return "clock_change"
    if "AGENT_INTERFACE_REPLACEMENT_SETTLED" in events:
        return "interface_replacement_recovered"
    if "AGENT_INTERFACE_REPLACED" in events:
        return "interface_replacement"
    if "AGENT_LOAD_RELEASED" in events:
        return "load_recovery"
    if "AGENT_LOAD_ACTIVE" in events:
        return "controlled_load"
    if "AGENT_INITIAL_WAIT_DONE" in events:
        # The Docker SDK stall is deliberately the first Agent request. Keep
        # the bounded stall phase visible through its 12s observation, then
        # restore the quiet pre-load baseline phase.
        return "agent_initial"
    if "AGENT_DOCKER_QUERY_OBSERVED" in events or "AGENT_DOCKER_QUERY_CANCELLED" in events:
        return "docker_query_stalled"
    if "AGENT_DOCKER_QUERY_STARTED" in events:
        return "docker_query_stall_active"
    if "AGENT_STARTED" in events:
        return "agent_initial"
    return "before_agent"


def publish_browser_phase(serial_log, events=None, *, guest_finished=False, guest_exit_code=None):
    phase_path = os.environ.get("NODEDANCE_S03_PHASE_FILE", "").strip()
    if not phase_path:
        return
    if events is None:
        events, _ = serial_events(serial_log)
    path = pathlib.Path(phase_path)
    path.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
    payload = {
        "phase": phase_at(events),
        "events": sorted(events),
        "eventValues": events,
        "updatedAtUTC": utc_now(),
        "guestFinished": guest_finished,
    }
    if guest_exit_code is not None:
        payload["guestExitCode"] = guest_exit_code
    temporary = path.with_name(path.name + f".{os.getpid()}.tmp")
    temporary.write_text(json.dumps(payload, ensure_ascii=False) + "\n", encoding="utf-8")
    temporary.chmod(0o600)
    os.replace(temporary, path)


def wait_for_guest(qemu, serial_log, log_path, control_url=None, states_log=None):
    deadline = time.monotonic() + QEMU_TIMEOUT_SECONDS
    states = []
    next_poll = 0.0
    while time.monotonic() < deadline:
        events, _ = serial_events(serial_log)
        publish_browser_phase(serial_log, events)
        now = time.monotonic()
        if control_url and now >= next_poll:
            next_poll = now + 0.4
            payload = control_state(control_url)
            if payload is not None:
                item = {"observedAtUTC": utc_now(), "phase": phase_at(events), "api": payload}
                states.append(item)
                if states_log is not None:
                    with states_log.open("a", encoding="utf-8") as stream:
                        stream.write(json.dumps(item, ensure_ascii=False) + "\n")
        if qemu.poll() is not None:
            events, data = serial_events(serial_log)
            publish_browser_phase(serial_log, events, guest_finished=True, guest_exit_code=qemu.returncode)
            if "DONE" not in events:
                raise GuestFailure(f"guest QEMU exited before the initramfs test finished (exit {qemu.returncode}): {data[-2000:]}")
            return events, states
        events, _ = serial_events(serial_log)
        if "ERROR" in events:
            raise GuestFailure(f"guest initramfs reported an error: {events['ERROR']}")
        if "DONE" in events:
            if not control_url:
                publish_browser_phase(serial_log, events, guest_finished=True, guest_exit_code=0)
                return events, states
            reboot_fields = events.get("REBOOT_POST", "").split()
            if len(reboot_fields) == 6:
                expected_boot = reboot_fields[3]
                prior_generations = []
                recovered = False
                for item in states:
                    view, state = state_view(item.get("api"))
                    if view is None:
                        continue
                    if item.get("phase") != "post_reboot":
                        prior_generations.append(int(view.get("activeGeneration", 0) or 0))
                    metrics = view.get("metrics", {})
                    uptime = metrics.get("uptime", {}).get("value") or {}
                    recovered = (
                        state and state.get("status") == "online" and
                        view.get("bootId") == expected_boot and
                        int(view.get("activeGeneration", 0) or 0) > max(prior_generations or [0]) and
                        uptime.get("bootId") == expected_boot
                    )
                if recovered:
                    publish_browser_phase(serial_log, events, guest_finished=True, guest_exit_code=0)
                    return events, states
        time.sleep(0.2)
    events, data = serial_events(serial_log)
    publish_browser_phase(serial_log, events, guest_finished=True, guest_exit_code=124)
    raise GuestFailure(f"minimal Linux guest did not finish within {QEMU_TIMEOUT_SECONDS}s; events={sorted(events)}; serial tail={data[-2500:]}")


def analyze_guest_agent_core(events, states, round_dir):
    checks = {}
    unique = {}
    for item in states:
        view, state = state_view(item.get("api"))
        if view is None:
            continue
        key = (
            item.get("phase", "unknown"),
            view.get("nodeStatus", "unknown"),
            int(view.get("activeGeneration", 0) or 0),
            int(view.get("generation", 0) or 0),
            int(view.get("sequence", 0) or 0),
        )
        if key[3] == 0 or key[4] == 0:
            continue
        unique.setdefault(key, {**item, "view": view, "state": state})
    samples = list(unique.values())

    def view_metric(sample, section, name=None):
        metrics = sample["view"].get("metrics", {})
        value = metrics.get(section, {})
        if name is not None:
            value = value.get(name, {})
        return value if isinstance(value, dict) else {}

    def known_metric(sample, section, name=None):
        metric = view_metric(sample, section, name)
        if metric.get("status") != "known" or metric.get("value") is None:
            return None
        return metric["value"]

    # Once the isolated fixture became the first Agent Engine query, the
    # post-cancel stall-observation interval is still a quiet pre-load
    # baseline. Preserve it even though it has a dedicated Docker phase.
    initial = [sample for sample in samples if sample.get("phase") in {"agent_initial", "docker_query_stalled"}]
    baseline_cpu = [float(value) for sample in initial
                    if (value := known_metric(sample, "cpu", "usagePercent")) is not None]
    baseline_memory = [int(value["usedBytes"]) for sample in initial
                       if (value := known_metric(sample, "memory")) is not None and "usedBytes" in value]
    during = [sample for sample in samples if sample.get("phase") == "controlled_load"]
    during_cpu = [float(value) for sample in during
                  if (value := known_metric(sample, "cpu", "usagePercent")) is not None]
    during_memory = [int(value["usedBytes"]) for sample in during
                     if (value := known_metric(sample, "memory")) is not None and "usedBytes" in value]
    recovered = [sample for sample in samples if sample.get("phase") == "load_recovery"]
    recovery_cpu = [float(value) for sample in recovered
                    if (value := known_metric(sample, "cpu", "usagePercent")) is not None]
    recovery_memory = [int(value["usedBytes"]) for sample in recovered
                       if (value := known_metric(sample, "memory")) is not None and "usedBytes" in value]

    load_ok = bool(baseline_cpu and baseline_memory and during_cpu and during_memory and recovery_cpu and recovery_memory)
    load_details = {
        "status": "NOT_READY", "baseline_cpu_percent": baseline_cpu,
        "during_load_cpu_percent": during_cpu, "after_release_cpu_percent": recovery_cpu,
        "baseline_used_bytes": baseline_memory, "during_load_used_bytes": during_memory,
        "after_release_used_bytes": recovery_memory,
    }
    if load_ok:
        base_cpu = sorted(baseline_cpu)[len(baseline_cpu) // 2]
        base_memory = sorted(baseline_memory)[len(baseline_memory) // 2]
        peak_cpu = max(during_cpu)
        peak_memory = max(during_memory)
        last_recovered_cpu = recovery_cpu[-1]
        last_recovered_memory = recovery_memory[-1]
        load_details.update({
            "baseline_cpu_median_percent": base_cpu,
            "during_load_cpu_peak_percent": peak_cpu,
            "baseline_memory_median_bytes": base_memory,
            "during_load_memory_peak_bytes": peak_memory,
            "recovered_cpu_percent": last_recovered_cpu,
            "recovered_memory_bytes": last_recovered_memory,
        })
        load_pass = (peak_cpu >= base_cpu + 20 and peak_memory >= base_memory + 128 * 1024 * 1024 and
                     last_recovered_cpu <= base_cpu + 15 and
                     last_recovered_memory <= base_memory + 96 * 1024 * 1024)
        load_details["status"] = "PASS" if load_pass else "FAIL"
        if not load_pass:
            load_details["reason"] = "real Agent/Core metrics missed the controlled guest CPU/memory increase or recovery thresholds"
    else:
        load_details["reason"] = "Agent/Core API did not provide unique known samples before, during, and after controlled load"
    checks["agent_load_response"] = load_details

    clock_pre_samples = [sample for sample in samples if sample.get("phase") in {"agent_initial", "load_recovery"}]
    pre_offsets = [float(sample["view"].get("clockOffsetMs", float("nan")))
                   for sample in clock_pre_samples if sample["view"].get("clockOffsetMs") is not None]

    clock_pre_fields = events.get("CLOCK_PRE", "").split()
    clock_post_fields = events.get("CLOCK_POST", "").split()
    clock_pre_boot = clock_pre_fields[0] if len(clock_pre_fields) == 4 else ""
    clock_post_boot = clock_post_fields[0] if len(clock_post_fields) == 3 else ""
    clock_pre_unix = int(clock_pre_fields[2]) if len(clock_pre_fields) == 4 else 0
    clock_target_unix = int(clock_pre_fields[3]) if len(clock_pre_fields) == 4 else 0
    clock_post_unix = int(clock_post_fields[2]) if len(clock_post_fields) == 3 else 0
    clock_wall_delta = clock_post_unix - clock_pre_unix
    clock_raw_valid = bool(
        clock_pre_boot and clock_post_boot == clock_pre_boot and
        abs(clock_wall_delta - 3600) <= 5 and
        abs(clock_post_unix - clock_target_unix) <= 5
    )

    def timestamp_unix(value):
        if not isinstance(value, str) or not value:
            return None
        try:
            parsed = dt.datetime.fromisoformat(value.replace("Z", "+00:00"))
        except ValueError:
            return None
        if parsed.tzinfo is None:
            return None
        return parsed.timestamp()

    pre_sequences_by_generation = {}
    for sample in clock_pre_samples:
        view = sample["view"]
        generation = int(view.get("activeGeneration", 0) or 0)
        sequence = int(view.get("sequence", 0) or 0)
        if view.get("bootId") == clock_pre_boot and generation > 0 and sequence > 0:
            pre_sequences_by_generation[generation] = max(
                pre_sequences_by_generation.get(generation, 0), sequence)
    highest_pre_generation = max(pre_sequences_by_generation, default=0)

    # The guest can move from CLOCK_POST into the next fault phase before a new
    # metrics sequence arrives. Correlate the Core sample to the raw guest wall
    # clock and boot ID instead of requiring a phase label that may never own a
    # unique sequence during that short transition.
    correlated_clock_samples = []
    for sample in samples:
        view = sample["view"]
        collected_at = timestamp_unix(view.get("collectedAt"))
        if view.get("bootId") != clock_pre_boot or collected_at is None or collected_at < clock_post_unix:
            continue
        generation = int(view.get("generation", 0) or 0)
        active_generation = int(view.get("activeGeneration", 0) or 0)
        sequence = int(view.get("sequence", 0) or 0)
        if (view.get("nodeStatus") != "online" or generation <= 0 or
                active_generation != generation or sequence <= 0):
            continue
        if generation in pre_sequences_by_generation:
            if sequence <= pre_sequences_by_generation[generation]:
                continue
        elif generation <= highest_pre_generation:
            continue
        server_time = timestamp_unix(view.get("serverTime"))
        received_at = timestamp_unix(view.get("receivedAt"))
        offset = view.get("clockOffsetMs")
        if server_time is None or received_at is None or offset is None:
            continue
        measured_offset = (received_at - collected_at) * 1000
        if abs(server_time - received_at) > 60 or abs(measured_offset - float(offset)) > 5_000:
            continue
        correlated_clock_samples.append({
            "phase": sample.get("phase"),
            "bootId": view.get("bootId"),
            "generation": generation,
            "sequence": sequence,
            "collectedAt": view.get("collectedAt"),
            "receivedAt": view.get("receivedAt"),
            "serverTime": view.get("serverTime"),
            "clockOffsetMs": float(offset),
            "measuredOffsetMs": measured_offset,
        })
    post_offsets = [sample["clockOffsetMs"] for sample in correlated_clock_samples]
    clock_pass = bool(clock_raw_valid and pre_offsets and post_offsets and
                      abs(sorted(pre_offsets)[len(pre_offsets) // 2]) < 60_000 and
                      any(abs(offset + 3_600_000) < 60_000 for offset in post_offsets))
    checks["agent_clock_offset_core"] = {
        "status": "PASS" if clock_pass else "FAIL",
        "pre_clock_offset_ms": pre_offsets,
        "post_clock_offset_ms": post_offsets,
        "raw_guest_clock": {
            "boot_id_before": clock_pre_boot,
            "boot_id_after": clock_post_boot,
            "wall_before_unix": clock_pre_unix,
            "wall_target_unix": clock_target_unix,
            "wall_after_unix": clock_post_unix,
            "wall_delta_seconds": clock_wall_delta,
            "valid": clock_raw_valid,
            "highest_pre_clock_generation": highest_pre_generation,
            "correlated_core_samples": correlated_clock_samples,
        },
        "reason": "" if clock_pass else "Core did not observe the guest-only +3600s wall-clock correction while preserving the boot ID",
    }

    offline = [sample for sample in samples if sample.get("phase") == "network_isolated" and
               sample["view"].get("nodeStatus") == "offline" and
               view_metric(sample, "cpu", "usagePercent").get("status") == "stale"]
    before_isolation = [sample for sample in samples if sample.get("phase") in {"post_clock", "clock_change"} and
                        sample["view"].get("nodeStatus") == "online"]
    isolated_generation = max((int(sample["view"].get("activeGeneration", 0) or 0) for sample in before_isolation), default=0)
    recovered_online = [sample for sample in samples if sample.get("phase") in {"network_recovery", "network_recovered"} and
                        sample["view"].get("nodeStatus") == "online" and
                        int(sample["view"].get("activeGeneration", 0) or 0) > isolated_generation and
                        view_metric(sample, "cpu", "usagePercent").get("status") == "known"]
    network_pass = bool(offline and recovered_online)
    checks["agent_network_stale_recovery_core"] = {
        "status": "PASS" if network_pass else "FAIL",
        "isolated_offline_samples": len(offline),
        "generation_before_isolation": isolated_generation,
        "recovered_generations": [int(sample["view"].get("activeGeneration", 0) or 0) for sample in recovered_online],
        "recovery_sequences": [int(sample["view"].get("sequence", 0) or 0) for sample in recovered_online],
        "reason": "" if network_pass else "real guest network isolation did not produce Core stale/offline state and a higher-generation fresh recovery",
    }

    reboot_fields = events.get("REBOOT_POST", "").split()
    old_boot = reboot_fields[0] if len(reboot_fields) == 6 else ""
    new_boot = reboot_fields[3] if len(reboot_fields) == 6 else ""
    before_reboot = [sample for sample in samples if sample.get("phase") in {"network_recovered", "pre_reboot"} and
                     sample["view"].get("nodeStatus") == "online"]
    before_generation = max((int(sample["view"].get("activeGeneration", 0) or 0) for sample in before_reboot), default=0)
    post_reboot = [sample for sample in samples if sample.get("phase") == "post_reboot" and
                   sample["view"].get("nodeStatus") == "online" and sample["view"].get("bootId") == new_boot and
                   int(sample["view"].get("activeGeneration", 0) or 0) > before_generation]
    boot_change_pass = bool(old_boot and new_boot and old_boot != new_boot and post_reboot and
                            any(sample["view"].get("previousBootId") == old_boot for sample in post_reboot))
    checks["agent_reboot_boot_generation_core"] = {
        "status": "PASS" if boot_change_pass else "FAIL",
        "old_boot_id": old_boot,
        "new_boot_id": new_boot,
        "generation_before_reboot": before_generation,
        "post_reboot_generation": max((int(sample["view"].get("activeGeneration", 0) or 0) for sample in post_reboot), default=0),
        "post_reboot_sequences": [int(sample["view"].get("sequence", 0) or 0) for sample in post_reboot],
        "reason": "" if boot_change_pass else "Core did not report the new guest boot ID and prior boot ID on a higher live Agent generation",
    }

    initial_agent_fields = events.get("AGENT_STARTED", "").split()
    initial_boot_id = initial_agent_fields[0] if len(initial_agent_fields) == 3 else ""
    initial_first = [sample for sample in samples if initial_boot_id and
                     sample["view"].get("bootId") == initial_boot_id and
                     int(sample["view"].get("generation", 0) or 0) == 1 and
                     int(sample["view"].get("sequence", 0) or 0) == 1]
    restarted_first = [sample for sample in post_reboot if int(sample["view"].get("sequence", 0) or 0) == 1]
    restarted_fresh = [sample for sample in post_reboot if int(sample["view"].get("sequence", 0) or 0) > 1 and
                       view_metric(sample, "cpu", "usagePercent").get("status") == "known" and
                       view_metric(sample, "network", "summary").get("status") == "known"]

    def warming_unknown(sample, section, name=None):
        metric = view_metric(sample, section, name)
        return (metric.get("status") in {"unknown", "stale"} and
                "warming_up" in metric.get("reason", ""))

    first_sample_safe = bool(initial_first and
                             warming_unknown(initial_first[0], "cpu", "usagePercent") and
                             initial_first[0]["view"].get("metrics", {}).get("cpu", {}).get("usagePercent", {}).get("value") is None and
                             warming_unknown(initial_first[0], "network", "summary") and
                             initial_first[0]["view"].get("metrics", {}).get("network", {}).get("summary", {}).get("value") is None and
                             restarted_first and warming_unknown(restarted_first[0], "cpu", "usagePercent") and
                             warming_unknown(restarted_first[0], "network", "summary") and restarted_fresh)
    checked_rates = []
    for sample in restarted_fresh:
        interfaces = sample["view"].get("metrics", {}).get("network", {}).get("interfaces", [])
        checked_rates.extend(
            (rate.get("value") or {}).get(key)
            for interface in interfaces
            for rate in [interface.get("rate", {})]
            for key in ("receivedBytesPerSecond", "sentBytesPerSecond")
            if rate.get("status") == "known"
        )
    rates_nonnegative = bool(checked_rates) and all(
        isinstance(value, (int, float)) and value >= 0 for value in checked_rates)
    first_sample_safe = first_sample_safe and rates_nonnegative
    checks["agent_first_sample_reset"] = {
        "status": "PASS" if first_sample_safe else "FAIL",
        "initial_unknown_reports": len(initial_first),
        "restart_first_reports": len(restarted_first),
        "restart_fresh_reports": len(restarted_fresh),
        "known_network_rate_values_checked": len(checked_rates),
        "all_network_rates_nonnegative": rates_nonnegative,
        "reason": "" if first_sample_safe else "real Agent first/restart sample was not explicitly unknown/stale during warm-up or a recovered network rate was negative",
    }

    nic_roles = events.get("AGENT_NICS", "").split()
    replacement_fields = events.get("AGENT_INTERFACE_REPLACED", "").split()
    replacement_details = {
        "test_nic_from_fixed_mac": nic_roles[1] if len(nic_roles) == 2 else None,
        "event_fields": replacement_fields,
        "changed_samples": [],
        "recovered_samples": [],
    }
    replacement_event_valid = False
    replacement_name = None
    replacement_before_index = None
    replacement_before_mac = None
    replacement_after_index = None
    replacement_after_mac = None
    if len(nic_roles) == 2 and len(replacement_fields) == 5:
        replacement_name, before_index, before_mac, after_index, after_mac = replacement_fields
        replacement_before_index = before_index
        replacement_before_mac = before_mac.lower()
        replacement_after_index = after_index
        replacement_after_mac = after_mac.lower()
        replacement_event_valid = (
            replacement_name == nic_roles[1] and
            before_index == after_index and
            replacement_before_mac != replacement_after_mac
        )
    replacement_details.update({
        "replacement_interface": replacement_name,
        "ifindex_before": replacement_before_index,
        "ifindex_after": replacement_after_index,
        "mac_before": replacement_before_mac,
        "mac_after": replacement_after_mac,
        "identity_event_valid": replacement_event_valid,
    })
    for sample in samples:
        if sample.get("phase") not in {"interface_replacement", "interface_replacement_recovered"}:
            continue
        network_interfaces = sample["view"].get("metrics", {}).get("network", {}).get("interfaces", [])
        target = next((interface for interface in network_interfaces
                       if interface.get("name") == replacement_name), None)
        if target is None:
            continue
        rate = target.get("rate", {})
        sample_evidence = {
            "phase": sample.get("phase"),
            "generation": int(sample["view"].get("generation", 0) or 0),
            "sequence": int(sample["view"].get("sequence", 0) or 0),
            "rate_status": rate.get("status"),
            "rate_reason": rate.get("reason", ""),
            "rate_value": rate.get("value"),
        }
        if (sample.get("phase") == "interface_replacement" and
                rate.get("status") in {"unknown", "stale"} and
                "interface_changed" in rate.get("reason", "")):
            replacement_details["changed_samples"].append(sample_evidence)
        if (sample.get("phase") == "interface_replacement_recovered" and
                rate.get("status") == "known" and rate.get("value") is not None):
            replacement_details["recovered_samples"].append(sample_evidence)
    changed_sequences = [item["sequence"] for item in replacement_details["changed_samples"]]
    recovered_after_change = [item for item in replacement_details["recovered_samples"]
                              if item["generation"] == replacement_details["changed_samples"][-1]["generation"]
                              and item["sequence"] > max(changed_sequences, default=0)] if changed_sequences else []
    recovered_rates_valid = bool(recovered_after_change) and all(
        all(isinstance((sample.get("rate_value") or {}).get(key), (int, float)) and
            sample["rate_value"][key] >= 0
            for key in ("receivedBytesPerSecond", "sentBytesPerSecond"))
        for sample in recovered_after_change
    )
    replacement_pass = bool(replacement_event_valid and replacement_details["changed_samples"] and
                             recovered_rates_valid)
    replacement_details["recovered_after_change"] = recovered_after_change
    checks["agent_interface_identity_replacement"] = {
        "status": "PASS" if replacement_pass else "FAIL",
        **replacement_details,
        "reason": "" if replacement_pass else
        "real guest MAC identity change was not followed by a Core unknown interface_changed rate and later known nonnegative rate",
    }

    permission_fault = []
    permission_recovery = []
    permission_fault_samples = []
    for sample in samples:
        phase = sample.get("phase")
        if phase not in {"permission_fault", "permission_recovery"}:
            continue
        memory = view_metric(sample, "memory")
        cpu = view_metric(sample, "cpu", "usagePercent")
        network = view_metric(sample, "network", "summary")
        uptime = view_metric(sample, "uptime")
        common = {
            "generation": int(sample["view"].get("generation", 0) or 0),
            "activeGeneration": int(sample["view"].get("activeGeneration", 0) or 0),
            "sequence": int(sample["view"].get("sequence", 0) or 0),
            "nodeStatus": sample["view"].get("nodeStatus", "unknown"),
            "memoryStatus": memory.get("status"),
            "memoryReason": memory.get("reason", ""),
            "memoryValue": memory.get("value"),
            "memoryAgeMillis": memory.get("sampleAgeMillis"),
            "cpuStatus": cpu.get("status"),
            "networkStatus": network.get("status"),
            "uptimeStatus": uptime.get("status"),
        }
        if phase == "permission_fault" and "permission_denied" in memory.get("reason", ""):
            permission_fault.append(common)
            permission_fault_samples.append(sample)
        if phase == "permission_recovery" and memory.get("status") == "known":
            permission_recovery.append(common)

    def observed_at(sample):
        value = sample.get("observedAtUTC", "")
        try:
            parsed = dt.datetime.fromisoformat(value.replace("Z", "+00:00"))
        except (AttributeError, ValueError):
            return 0.0
        return parsed.timestamp() if parsed.tzinfo is not None else 0.0

    prior_known_memory = None
    prior_known_memory_age = None
    if permission_fault_samples:
        first_fault_time = min(observed_at(sample) for sample in permission_fault_samples)
        previous_samples = [sample for sample in samples
                            if observed_at(sample) < first_fault_time and
                            sample["view"].get("nodeStatus") == "online" and
                            view_metric(sample, "memory").get("status") == "known" and
                            view_metric(sample, "memory").get("value") is not None]
        if previous_samples:
            previous_sample = max(previous_samples, key=observed_at)
            previous_memory = view_metric(previous_sample, "memory")
            prior_known_memory = previous_memory.get("value")
            prior_known_memory_age = previous_memory.get("sampleAgeMillis")

    first_fault_sequence_by_generation = {}
    for item in permission_fault:
        generation = item["generation"]
        sequence = item["sequence"]
        if generation > 0 and sequence > 0:
            first_fault_sequence_by_generation[generation] = min(
                first_fault_sequence_by_generation.get(generation, sequence), sequence)

    def memory_fault_is_explicit(item):
        if item["memoryStatus"] == "stale":
            return (prior_known_memory is not None and
                    item["memoryValue"] == prior_known_memory and
                    "permission_denied" in item["memoryReason"])
        return False

    live_during_fault = [item for item in permission_fault
                         if memory_fault_is_explicit(item) and
                         item["generation"] == item["activeGeneration"] and
                         item["sequence"] > first_fault_sequence_by_generation.get(item["generation"], 0) and
                         item["cpuStatus"] == "known" and item["networkStatus"] == "known" and
                         item["uptimeStatus"] == "known" and item["nodeStatus"] == "online"]
    fault_memory_ages = [int(item["memoryAgeMillis"]) for item in sorted(
        live_during_fault, key=lambda item: (item["generation"], item["sequence"]))
        if isinstance(item["memoryAgeMillis"], int) and not isinstance(item["memoryAgeMillis"], bool)]
    memory_age_increased = bool(
        len(fault_memory_ages) >= 2 and
        all(fault_memory_ages[index] > fault_memory_ages[index - 1]
            for index in range(1, len(fault_memory_ages))) and
        (not isinstance(prior_known_memory_age, int) or
         fault_memory_ages[0] > prior_known_memory_age)
    )
    recovered_memory = [item for item in permission_recovery
                        if item["memoryValue"] is not None and
                        item["cpuStatus"] == "known" and item["networkStatus"] == "known" and
                        item["uptimeStatus"] == "known" and item["nodeStatus"] == "online"]
    recovery_after_fault = [item for item in recovered_memory if any(
        item["generation"] == fault["generation"] and item["sequence"] > fault["sequence"]
        for fault in live_during_fault)]
    permission_pass = bool(len(live_during_fault) >= 2 and memory_age_increased and recovery_after_fault)
    checks["agent_permission_partial_failure_recovery"] = {
        "status": "PASS" if permission_pass else "FAIL",
        "fault_reports": permission_fault,
        "last_known_memory_before_fault": prior_known_memory,
        "last_known_memory_age_millis_before_fault": prior_known_memory_age,
        "fault_memory_ages_millis": fault_memory_ages,
        "fault_memory_age_increased": memory_age_increased,
        "remaining_metrics_live_during_fault": live_during_fault,
        "recovered_memory_reports": recovery_after_fault,
        "reason": "" if permission_pass else
        "restricted real Agent did not retain exactly the last known memory as stale with permission_denied and increasing age while newer same-generation CPU/network/uptime stayed online, then recover memory on a later sequence",
    }

    multi_samples = []
    for sample in samples:
        network = sample["view"].get("metrics", {}).get("network", {})
        disk = sample["view"].get("metrics", {}).get("disk", {})
        if network.get("summary", {}).get("status") != "known" or disk.get("status") != "known":
            continue
        interfaces = network.get("interfaces", [])
        included = [interface for interface in interfaces if interface.get("includedInSummary")]
        names = {interface.get("name") for interface in included}
        summary_value = network.get("summary", {}).get("value") or {}
        sum_received = 0.0
        sum_sent = 0.0
        rates_valid = bool(included)
        for interface in included:
            rate = interface.get("rate", {})
            value = rate.get("value") or {}
            if rate.get("status") != "known":
                rates_valid = False
                break
            sum_received += float(value.get("receivedBytesPerSecond", -1))
            sum_sent += float(value.get("sentBytesPerSecond", -1))
        compare_ok = rates_valid and all(
            abs(actual - expected) <= max(1e-6, abs(expected) * 1e-6)
            for actual, expected in (
                (float(summary_value.get("receivedBytesPerSecond", -1)), sum_received),
                (float(summary_value.get("sentBytesPerSecond", -1)), sum_sent),
            )
        )
        mounts = disk.get("mounts", [])
        mount_map = {mount.get("mountpoint"): mount for mount in mounts}
        required_mounts = {"/", "/mnt", "/mnt/second"}
        mount_ok = required_mounts.issubset(mount_map) and all(
            mount_map[path].get("usage", {}).get("status") == "known" and
            (mount_map[path].get("usage", {}).get("value") or {}).get("totalBytes", 0) > 0
            for path in required_mounts if path in mount_map
        )
    agent_nic_roles = events.get("AGENT_NICS", "").split()
    multi_samples.append({
        "phase": sample.get("phase"), "generation": sample["view"].get("generation"),
        "sequence": sample["view"].get("sequence"), "interface_names": sorted(name for name in names if name),
        "primary_nic_from_mac": agent_nic_roles[0] if len(agent_nic_roles) == 2 else None,
        "test_nic_from_mac": agent_nic_roles[1] if len(agent_nic_roles) == 2 else None,
        "included_interfaces": len(included), "network_summary_matches_sum": compare_ok,
            "mountpoints": sorted(mount_map), "required_mounts_known": mount_ok,
        })
    multi_pass = any(
        {item.get("primary_nic_from_mac"), item.get("test_nic_from_mac")} - {None} <= set(item["interface_names"]) and
        item.get("primary_nic_from_mac") != item.get("test_nic_from_mac") and
        item["included_interfaces"] >= 2 and item["network_summary_matches_sum"] and
        item["required_mounts_known"]
        for item in multi_samples
    )
    checks["agent_multinet_mount_summary"] = {
        "status": "PASS" if multi_pass else "FAIL",
        "samples_checked": len(multi_samples), "sample_evidence": multi_samples,
        "reason": "" if multi_pass else "Core did not receive two live guest interfaces, non-duplicated summary totals, and all expected known mount usages",
    }

    docker_started = events.get("AGENT_DOCKER_QUERY_STARTED", "").split()
    docker_cancelled = events.get("AGENT_DOCKER_QUERY_CANCELLED", "").split()
    docker_start_unix = int(docker_started[0]) if len(docker_started) == 1 and docker_started[0].isdigit() else None
    docker_cancel_unix = int(docker_cancelled[0]) if len(docker_cancelled) == 1 and docker_cancelled[0].isdigit() else None
    docker_cancel_seconds = (docker_cancel_unix - docker_start_unix
                             if docker_start_unix is not None and docker_cancel_unix is not None else None)
    docker_active = [sample for sample in samples if sample.get("phase") == "docker_query_stall_active"]
    docker_observed = [sample for sample in samples if sample.get("phase") == "docker_query_stalled"]

    def current_online_metrics(sample):
        view = sample.get("view", {})
        generation = int(view.get("generation", 0) or 0)
        active_generation = int(view.get("activeGeneration", 0) or 0)
        cpu = view.get("metrics", {}).get("cpu", {}).get("usagePercent", {})
        return (view.get("nodeStatus") == "online" and generation > 0 and
                generation == active_generation and cpu.get("status") == "known")

    active_sequences = sorted({int(sample["view"].get("sequence", 0) or 0)
                               for sample in docker_active if current_online_metrics(sample)})
    observed_sequences = sorted({int(sample["view"].get("sequence", 0) or 0)
                                 for sample in docker_observed if current_online_metrics(sample)})
    stall_states = []
    for item in states:
        if item.get("phase") not in {"docker_query_stall_active", "docker_query_stalled"}:
            continue
        view, state = state_view(item.get("api"))
        if view is not None and state is not None:
            stall_states.append({"observedAtUTC": item.get("observedAtUTC"),
                                 "phase": item.get("phase"), "status": state.get("status"),
                                 "generation": view.get("generation"),
                                 "activeGeneration": view.get("activeGeneration"),
                                 "sequence": view.get("sequence"),
                                 "leaseValidUntil": state.get("leaseValidUntil")})
    lease_deadlines = [timestamp_unix(item.get("leaseValidUntil")) for item in stall_states
                       if item.get("status") == "online" and
                       item.get("generation") == item.get("activeGeneration")]
    lease_deadlines = [value for value in lease_deadlines if value is not None]
    heartbeat_advanced = len(lease_deadlines) >= 2 and max(lease_deadlines) > min(lease_deadlines)
    docker_stall_pass = bool(
        docker_cancel_seconds is not None and 0 < docker_cancel_seconds <= 20 and
        active_sequences and observed_sequences and
        min(active_sequences) > 0 and max(observed_sequences) > min(active_sequences) and
        len(observed_sequences) >= 2 and heartbeat_advanced and stall_states and
        all(item["status"] == "online" and item["generation"] == item["activeGeneration"]
            for item in stall_states)
    )
    checks["agent_docker_stall_isolation"] = {
        "status": "PASS" if docker_stall_pass else "FAIL",
        "real_sdk_container_list_started": len(docker_started) == 1,
        "request_cancelled_seconds_after_observed_start": docker_cancel_seconds,
        "active_stall_sequences": active_sequences,
        "post_cancel_observation_sequences": observed_sequences,
        "heartbeat_lease_advanced": heartbeat_advanced,
        "core_states_during_stall": stall_states,
        "reason": "" if docker_stall_pass else
        "the real Docker SDK list stall did not cancel within 20s while Core retained the same online Agent generation, advancing host metrics, and renewed heartbeat lease",
    }

    evidence = {
        "status": "PASS" if all(check["status"] == "PASS" for check in checks.values()) else "FAIL",
        "checks": checks,
        "states_path": "agent-core-states.jsonl",
        "raw_guest_events": {key: value for key, value in events.items()
                             if key.startswith("AGENT_") or key in {"CLOCK_PRE", "CLOCK_POST", "REBOOT_PRE", "REBOOT_POST"}},
        "unique_phase_report_observations": len(samples),
    }
    (round_dir / "agent-core-live.json").write_text(json.dumps(evidence, ensure_ascii=False, indent=2) + "\n")
    return evidence


def parse_sample_event(events, name, output_path):
    if name not in events:
        raise GuestFailure(f"guest did not emit {name} collector evidence")
    try:
        evidence = json.loads(events[name])
    except json.JSONDecodeError as error:
        raise GuestFailure(f"invalid {name} collector evidence: {error}") from error
    output_path.write_text(json.dumps(evidence, indent=2) + "\n")
    return evidence


def parse_triplet(events, name, count):
    if name not in events:
        raise GuestFailure(f"guest did not emit {name} raw evidence")
    fields = events[name].split()
    if len(fields) != count:
        raise GuestFailure(f"guest emitted invalid {name} raw evidence: {events[name]!r}")
    return fields


def check_guest_clock_and_reboot(events, round_dir):
    checks = []
    before_fields = parse_triplet(events, "CLOCK_PRE", 4)
    after_fields = parse_triplet(events, "CLOCK_POST", 3)
    reboot_fields = parse_triplet(events, "REBOOT_POST", 6)
    try:
        before_id, before_uptime, before_wall, target_wall = before_fields
        after_id, after_uptime, after_wall = after_fields
        reboot_before_id, reboot_before_uptime, reboot_before_wall, reboot_after_id, reboot_after_uptime, reboot_after_wall = reboot_fields
        before_uptime = float(before_uptime)
        after_uptime = float(after_uptime)
        before_wall = int(before_wall)
        target_wall = int(target_wall)
        after_wall = int(after_wall)
        reboot_before_uptime = float(reboot_before_uptime)
        reboot_before_wall = int(reboot_before_wall)
        reboot_after_uptime = float(reboot_after_uptime)
        reboot_after_wall = int(reboot_after_wall)
    except ValueError as error:
        raise GuestFailure(f"invalid guest clock/reboot numeric evidence: {error}") from error

    if before_id != after_id:
        checks.append("guest boot ID changed during the same-boot clock correction")
    wall_delta = after_wall - before_wall
    if abs(wall_delta - 3600) > 2 or abs(after_wall - target_wall) > 2:
        checks.append(f"guest wall-clock correction was {wall_delta}s; expected +3600s")
    uptime_delta = after_uptime - before_uptime
    if uptime_delta <= 0 or uptime_delta > 30:
        checks.append(f"guest uptime changed by {uptime_delta:.3f}s during wall-clock correction")
    if reboot_before_id != after_id or reboot_before_uptime < after_uptime:
        checks.append("guest pre-reboot boot ID/uptime did not continue from post-clock evidence")
    if reboot_after_id == reboot_before_id:
        checks.append("guest boot ID did not change after the in-guest reboot")
    if reboot_after_uptime <= 0 or reboot_after_uptime >= reboot_before_uptime:
        checks.append("guest uptime did not restart from a lower positive value after reboot")

    before_sample = parse_sample_event(events, "BEFORE_CLOCK", round_dir / "before-clock-shift.json")
    after_sample = parse_sample_event(events, "AFTER_CLOCK", round_dir / "after-clock-shift.json")
    reboot_sample = parse_sample_event(events, "AFTER_REBOOT", round_dir / "after-reboot.json")
    load_event = parse_sample_event(events, "LOAD", round_dir / "cpu-memory-load.json")
    if before_sample.get("rawBootId") != before_id or after_sample.get("rawBootId") != after_id:
        checks.append("collector boot ID did not match raw proc boot ID around clock correction")
    if reboot_sample.get("rawBootId") != reboot_after_id:
        checks.append("post-reboot collector boot ID did not match the new raw proc boot ID")

    uptime_comparisons = {}
    for name, sample in (("pre_clock", before_sample), ("post_clock", after_sample), ("post_reboot", reboot_sample)):
        metric = sample.get("snapshot", {}).get("uptime", {})
        value = metric.get("value") or {}
        try:
            collector_seconds = float(value["seconds"])
            proc_seconds = float(sample["rawUptimeSeconds"])
            difference = abs(collector_seconds - proc_seconds)
        except (KeyError, TypeError, ValueError):
            collector_seconds = None
            proc_seconds = None
            difference = None
        uptime_comparisons[name] = {
            "collectorSeconds": collector_seconds,
            "rawProcUptimeSeconds": proc_seconds,
            "absoluteDifferenceSeconds": difference,
            "maxDifferenceSeconds": 1.1,
            "passed": difference is not None and difference <= 1.1,
            "collectorSampledAt": metric.get("sampledAt", ""),
            "rawProcCaptureAt": sample.get("capturedAtUtc", ""),
            "rawProcCaptureUnixNano": sample.get("capturedUnixNano"),
        }
    for name, description in (
        ("pre_clock", "pre-clock collector uptime did not match the paired raw proc uptime"),
        ("post_clock", "post-clock collector uptime did not match the paired raw proc uptime"),
        ("post_reboot", "post-reboot collector uptime did not match the paired raw proc uptime"),
    ):
        if not uptime_comparisons[name]["passed"]:
            checks.append(description)

    clock_evidence = {
        "clock_before": {"boot_id": before_id, "uptime_seconds": before_uptime, "wall_unix_seconds": before_wall},
        "clock_after": {"boot_id": after_id, "uptime_seconds": after_uptime, "wall_unix_seconds": after_wall},
        "clock_target_unix_seconds": target_wall,
        "reboot_before": {"boot_id": reboot_before_id, "uptime_seconds": reboot_before_uptime, "wall_unix_seconds": reboot_before_wall},
        "reboot_after": {"boot_id": reboot_after_id, "uptime_seconds": reboot_after_uptime, "wall_unix_seconds": reboot_after_wall},
        "collector_proc_uptime_agreement": uptime_comparisons,
        "checks": {"passed": not checks, "failures": checks},
        "collector_evidence": {
            "before_clock": "before-clock-shift.json",
            "after_clock": "after-clock-shift.json",
            "after_reboot": "after-reboot.json",
        },
    }
    (round_dir / "guest-clock-reboot.json").write_text(json.dumps(clock_evidence, indent=2) + "\n")
    load_status = load_event.get("status", "FAIL")
    return load_event, ("PASS" if not checks else "FAIL"), checks


def run_guest_round(arch, lock, image, probe, qemu_binary, round_dir, round_number,
                    agent_manifest=None, agent_binary=None, privilege_helper_binary=None,
                    docker_stall_fixture_binary=None):
    round_dir.mkdir(parents=True, exist_ok=True)
    round_dir.chmod(0o700)
    log_path = round_dir / "commands.log"
    overlay = round_dir / "guest-overlay.qcow2"
    logged_command(["qemu-img", "create", "-f", "qcow2", "-F", "qcow2", "-b", str(image),
                    str(overlay)], log_path, timeout=60)
    kernel, original_initrd = extract_kernel_and_initrd(image, round_dir, log_path)
    initrd, kernel_info = build_guest_initrd(arch, original_initrd, probe, round_dir, log_path,
                                            agent_binary, agent_manifest, privilege_helper_binary,
                                            docker_stall_fixture_binary)
    qemu_log = round_dir / "qemu.log"
    serial_log = round_dir / "serial.log"
    serial_log.touch()
    with tempfile.TemporaryDirectory(prefix="s03-qemu-") as socket_dir:
        monitor_path = pathlib.Path(socket_dir) / "monitor.sock"
        command = qemu_command(arch, lock, overlay, kernel, initrd,
                              serial_log, monitor_path, qemu_binary,
                              agent_enabled=agent_manifest is not None)
        with qemu_log.open("w", encoding="utf-8") as output:
            qemu = subprocess.Popen(command, cwd=ROOT, stdout=output, stderr=subprocess.STDOUT)
            try:
                states_log = round_dir / "agent-core-states.jsonl" if agent_manifest is not None else None
                events, states = wait_for_guest(qemu, serial_log, log_path,
                                                agent_manifest.get("controlUrl") if agent_manifest else None,
                                                states_log)
                load_event, clock_status, clock_failures = check_guest_clock_and_reboot(events, round_dir)
                result = {
                    "round": round_number,
                    "guest_architecture": arch,
                    "guest_machine": lock["machine"],
                    "guest_kernel": kernel_info,
                    "guest_cpu_memory_probe": {
                        "status": load_event.get("status", "FAIL"),
                        "reason": load_event.get("reason", ""),
                        "evidence": "cpu-memory-load.json",
                        "load_exit_code": events.get("LOAD_STATUS", "unknown"),
                    },
                    "guest_clock_reboot_probe": {
                        "status": clock_status,
                        "reason": "; ".join(clock_failures),
                        "evidence": "guest-clock-reboot.json",
                    },
                    "full_stage_cases": {"S03-02": "NOT_READY", "S03-03": "NOT_READY",
                                         "S03-04": "NOT_READY", "S03-05": "NOT_READY",
                                         "S03-06": "NOT_READY", "S03-08": "NOT_READY"},
                    "covered_guest_behaviors": [
                        "quiet minimal Linux guest controlled CPU and resident-memory load/recovery",
                        "real pinned Moby SDK container-list request stalls on an owned guest-local Unix fixture, is context-cancelled, while Core keeps the Agent generation online, host metric sequences advance, and the heartbeat lease renews",
                        "real test-NIC MAC identity replacement on a fixed-ifindex interface; Core marked the old rate stale with interface_changed and then accepted fresh rates",
                        "real guest-only wall-clock correction while preserving boot ID and increasing uptime",
                        "real in-guest kernel reboot with boot ID change and uptime restart",
                        "collector boot ID and uptime compared with raw proc evidence before/after clock correction and reboot",
                        "restricted unprivileged real Agent denied access to a copied live /proc/meminfo view while other live metrics continue and recover",
                    ],
                    "pending_stage_behaviors": {
                        "S03-02": ["dashboard display of this guest's load/recovery and three consecutive stage runs"],
                        "S03-06": ["dashboard display of the real Docker inventory as stale/unknown while the Agent host metrics and heartbeat remain live, plus three consecutive stage runs"],
                        "S03-08": ["dashboard display of clock-offset and reboot changes, plus three consecutive stage runs"],
                    },
                }
                if agent_manifest is not None:
                    result["guest_agent_core_probe"] = analyze_guest_agent_core(events, states, round_dir)
                    result["full_stage_cases"]["S03-07"] = "NOT_READY"
                    result["pending_stage_behaviors"]["S03-07"] = ["browser stale/offline display and recovery for this isolated guest, plus three consecutive stage runs"]
                return result
            finally:
                stop_qemu(qemu, monitor_path, qemu_log)
def _interrupt_for_owned_cleanup(signum, _frame):
    raise KeyboardInterrupt(f"received signal {signum}; stopping the owned QEMU guest")


def main():
    signal.signal(signal.SIGTERM, _interrupt_for_owned_cleanup)
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--rounds", type=int, default=1, help="fresh guest overlays to test (default: 1)")
    parser.add_argument("--agent-core-manifest", help="run the locked Agent inside the guest and send its live metrics to this Core harness")
    args = parser.parse_args()
    if args.rounds < 1 or args.rounds > 10:
        parser.error("--rounds must be between 1 and 10")
    if args.agent_core_manifest and args.rounds != 1:
        parser.error("Agent/Core guest integration uses one fresh Core/enrollment per invocation; run separate harnesses for repeated attempts")

    run_id = dt.datetime.now(dt.timezone.utc).strftime("%Y%m%dT%H%M%SZ") + "-" + uuid.uuid4().hex[:8]
    run_dir = WORK_ROOT / run_id
    run_dir.mkdir(parents=True, exist_ok=False)
    run_dir.chmod(0o700)
    summary_path = run_dir / "summary.json"
    summary = {
        "schema": 1,
        "stage": "S03",
        "case": "guest_prerequisite_probe",
        "stage_status": "NOT_READY",
        "guest_probe_status": "NOT_READY",
        "run_id": run_id,
        "updated_at": utc_now(),
        "reason": "guest probe has not run",
        "component_probes": {
            "guest_cpu_memory_probe": "NOT_READY",
            "guest_clock_reboot_probe": "NOT_READY",
            "agent_load_response": "NOT_READY",
            "agent_clock_offset_core": "NOT_READY",
            "agent_network_stale_recovery_core": "NOT_READY",
            "agent_reboot_boot_generation_core": "NOT_READY",
            "agent_first_sample_reset": "NOT_READY",
            "agent_interface_identity_replacement": "NOT_READY",
            "agent_permission_partial_failure_recovery": "NOT_READY",
            "agent_multinet_mount_summary": "NOT_READY",
            "agent_docker_stall_isolation": "NOT_READY",
        },
        "stage_cases": {
            "S03-02": {
                "status": "NOT_READY",
                "covered_guest_behavior": ["controlled CPU/resident-memory increase and recovery in the minimal Linux guest, with actual Agent samples accepted by Core"],
                "pending": ["browser display of the guest load/recovery and three consecutive stage runs"],
            },
            "S03-03": {
                "status": "NOT_READY",
                "covered_guest_behavior": ["initial and rebooted Agent warm-up reports reach Core as unknown/stale before fresh nonnegative network rates"],
                "pending": ["force the deterministic counter-decrease/reset formula test in the acceptance runner; verify first-sample and same-ifindex MAC-replacement states in the live dashboard; complete three consecutive full runs"],
            },
            "S03-04": {
                "status": "NOT_READY",
                "covered_guest_behavior": ["a real guest exposes two interfaces and a tmpfs mount; Core receives interface details, aggregate sum, and filesystem stats"],
                "pending": ["browser renders the guest interface/mount details and three consecutive stage runs"],
            },
            "S03-06": {
                "status": "NOT_READY",
                "covered_guest_behavior": ["a real pinned Moby SDK Docker query is stalled on the private guest-local Unix fixture, context-cancelled, while the same Core/Agent generation stays online and its host metrics and heartbeat lease advance"],
                "pending": ["browser must show Docker inventory stale/unknown while host metrics and heartbeat remain live; three consecutive full runs and the second architecture remain"],
            },
            "S03-08": {
                "status": "NOT_READY",
                "covered_guest_behavior": ["guest-only clock correction and reboot with raw boot_id/uptime plus actual Core clock offset, boot ID, and new generation"],
                "pending": ["browser display through the clock correction/reboot and three consecutive stage runs"],
            },
            "S03-07": {
                "status": "NOT_READY",
                "covered_guest_behavior": ["real guest network isolation produced Core stale/offline; restored Agent reported a higher generation and known samples"],
                "pending": ["dashboard stale/offline/recovery display and three consecutive stage runs"],
            },
        },
        "rounds": [],
    }
    print(f"S03 guest evidence: {run_dir.relative_to(ROOT)}", flush=True)
    try:
        arch = host_architecture()
        lock = IMAGE_LOCKS[arch]
        summary["toolchain"] = preflight(arch)
        summary["host_architecture"] = arch
        agent_manifest = load_agent_manifest(args.agent_core_manifest) if args.agent_core_manifest else None
        agent_binary = None
        privilege_helper_binary = None
        docker_stall_fixture_binary = None
        image, image_info = verify_image(WORK_ROOT / "cache" / "noble-release-20260814",
                                         arch, run_dir / "setup.log")
        summary["image"] = image_info
        probe_info = build_probe(arch, run_dir / "s03-probe", run_dir / "build.log")
        summary["probe"] = probe_info
        if agent_manifest is not None:
            agent_binary = run_dir / "nodedance-agent"
            summary["agent_binary"] = build_agent_binary(arch, agent_binary, run_dir / "agent-build.log")
            privilege_helper_binary = run_dir / "s03-privilege-helper"
            summary["privilege_helper"] = build_privilege_helper(
                arch, privilege_helper_binary, run_dir / "privilege-helper-build.log")
            docker_stall_fixture_binary = run_dir / "s03-docker-stall-fixture"
            summary["docker_stall_fixture"] = build_docker_stall_fixture(
                arch, docker_stall_fixture_binary, run_dir / "docker-stall-fixture-build.log")
            summary["agent_core_target"] = {
                "server_url": agent_manifest["serverUrl"],
                "control_url": agent_manifest["controlUrl"],
                "node_id": agent_manifest["nodeId"],
            }
        host_wall_before = time.time()
        host_monotonic_before = time.monotonic()
        for round_number in range(1, args.rounds + 1):
            result = run_guest_round(arch, lock, image, run_dir / "s03-probe",
                                     summary["toolchain"]["qemu_binary"],
                                     run_dir / f"round-{round_number:02d}", round_number,
                                     agent_manifest, agent_binary, privilege_helper_binary,
                                     docker_stall_fixture_binary)
            summary["rounds"].append(result)
        host_wall_after = time.time()
        host_monotonic_after = time.monotonic()
        wall_elapsed = host_wall_after - host_wall_before
        monotonic_elapsed = host_monotonic_after - host_monotonic_before
        host_clock_jump = wall_elapsed - monotonic_elapsed
        summary["host_clock_guard"] = {
            "wall_elapsed_seconds": wall_elapsed,
            "monotonic_elapsed_seconds": monotonic_elapsed,
            "difference_seconds": host_clock_jump,
            "host_utc_before": dt.datetime.fromtimestamp(host_wall_before, dt.timezone.utc).isoformat(),
            "host_utc_after": dt.datetime.fromtimestamp(host_wall_after, dt.timezone.utc).isoformat(),
        }
        if abs(host_clock_jump) > 10:
            raise NotReady(f"host wall time drifted by {host_clock_jump:.3f}s during the guest-only test; no host time was set")
        cpu_statuses = [result["guest_cpu_memory_probe"]["status"] for result in summary["rounds"]]
        clock_statuses = [result["guest_clock_reboot_probe"]["status"] for result in summary["rounds"]]
        summary["component_probes"] = {
            "guest_cpu_memory_probe": "FAIL" if "FAIL" in cpu_statuses else "NOT_READY" if "NOT_READY" in cpu_statuses else "PASS",
            "guest_clock_reboot_probe": "FAIL" if "FAIL" in clock_statuses else "NOT_READY" if "NOT_READY" in clock_statuses else "PASS",
        }
        if agent_manifest is not None:
            check_names = ("agent_load_response", "agent_clock_offset_core",
                           "agent_network_stale_recovery_core", "agent_reboot_boot_generation_core",
                           "agent_first_sample_reset", "agent_interface_identity_replacement",
                           "agent_multinet_mount_summary", "agent_permission_partial_failure_recovery",
                           "agent_docker_stall_isolation")
            for check_name in check_names:
                statuses = [round_result.get("guest_agent_core_probe", {}).get("checks", {}).get(check_name, {}).get("status", "NOT_READY")
                            for round_result in summary["rounds"]]
                summary["component_probes"][check_name] = (
                    "FAIL" if "FAIL" in statuses else
                    "NOT_READY" if "NOT_READY" in statuses else "PASS")
        if "FAIL" in summary["component_probes"].values():
            summary["guest_probe_status"] = "FAIL"
            summary["reason"] = "one or more isolated guest component probes failed"
        elif "NOT_READY" in summary["component_probes"].values():
            summary["guest_probe_status"] = "NOT_READY"
            summary["reason"] = "one or more isolated guest component probes were not ready"
        else:
            summary["guest_probe_status"] = "PASS"
            summary["reason"] = "isolated guest component probes passed; original S03 cases remain NOT_READY pending full browser/three-run/two-architecture acceptance"
    except NotReady as error:
        summary["guest_probe_status"] = "NOT_READY"
        summary["reason"] = str(error)
    except (GuestFailure, OSError, subprocess.SubprocessError, ValueError, KeyError) as error:
        summary["guest_probe_status"] = "FAIL"
        summary["reason"] = f"{type(error).__name__}: {error}"
    except Exception as error:
        summary["guest_probe_status"] = "FAIL"
        summary["reason"] = f"unexpected {type(error).__name__}: {error}"
    summary["updated_at"] = utc_now()
    summary_path.write_text(json.dumps(summary, indent=2) + "\n")
    components = summary.get("component_probes", {})
    if components:
        print("Guest component probes: " + ", ".join(f"{name}={status}" for name, status in components.items()), flush=True)
    print(f"S03 guest prerequisite probe: {summary['guest_probe_status']}; original S03-02/S03-08 and full stage status: NOT_READY; summary={summary_path.relative_to(ROOT)}", flush=True)
    if summary["guest_probe_status"] == "PASS":
        return 0
    return 2 if summary["guest_probe_status"] == "NOT_READY" else 1


if __name__ == "__main__":
    raise SystemExit(main())
