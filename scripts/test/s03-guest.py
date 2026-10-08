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
import socket
import subprocess
import sys
import tempfile
import time
import uuid


ROOT = pathlib.Path(__file__).resolve().parents[2]
WORK_ROOT = ROOT / ".artifacts" / "work-s03"
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
    },
    "amd64": {
        "image": "ubuntu-24.04-server-cloudimg-amd64.img",
        "sha256": "6e40c07ae715f744f84af0bec76415cc1987dd115b4b8de437818561f01a3733",
        "qemu_binary": "qemu-system-x86_64",
        "qemu_package": "qemu-system-x86",
        "machine": "q35",
        "block_device": "virtio-blk-pci",
        "console": "ttyS0",
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
QEMU_TIMEOUT_SECONDS = 240
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

"$BB" mkdir -p /mnt/var/tmp
i=0
while [ ! -b /dev/vda1 ] && [ "$i" -lt 20 ]; do
  "$BB" sleep 1
  i=$((i + 1))
done
"$BB" mount -t ext4 -o rw /dev/vda1 /mnt || fatal "cannot mount guest root filesystem"

if [ -f "$MARKER" ]; then
  set -- $("$BB" cat "$MARKER")
  if [ "$#" -ne 3 ]; then fatal "invalid reboot marker"; fi
  before_boot_id="$1"
  before_uptime="$2"
  before_wall="$3"
  after_boot_id=$("$BB" cat /proc/sys/kernel/random/boot_id)
  after_uptime=$(uptime_seconds)
  after_wall=$("$BB" date -u +%s)
  printf 'S03:REBOOT_POST %s %s %s %s %s %s\n' \
    "$before_boot_id" "$before_uptime" "$before_wall" \
    "$after_boot_id" "$after_uptime" "$after_wall"
  if ! run_sample AFTER_REBOOT /run/s03-after-reboot.json; then fatal "post-reboot sample failed"; fi
  "$BB" rm -f "$MARKER"
  "$BB" sync
  "$BB" umount /mnt || fatal "cannot unmount guest root filesystem after reboot"
  say S03:DONE
  wait_forever
fi

load_status=0
if "$PROBE" --mode load >/run/s03-load.json 2>/run/s03-load-error; then
  load_status=0
else
  load_status=$?
fi
printf 'S03:LOAD_STATUS %s\n' "$load_status"
printf 'S03:LOAD '
"$BB" cat /run/s03-load.json
printf '\n'
if [ -s /run/s03-load-error ]; then
  printf 'S03:LOAD_ERROR '
  "$BB" cat /run/s03-load-error
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

reboot_before_boot=$("$BB" cat /proc/sys/kernel/random/boot_id)
reboot_before_uptime=$(uptime_seconds)
reboot_before_wall=$("$BB" date -u +%s)
printf 'S03:REBOOT_PRE %s %s %s\n' \
  "$reboot_before_boot" "$reboot_before_uptime" "$reboot_before_wall"
"$BB" printf '%s %s %s\n' "$reboot_before_boot" "$reboot_before_uptime" "$reboot_before_wall" >"$MARKER"
"$BB" sync
"$BB" umount /mnt || fatal "cannot unmount guest root filesystem before reboot"
say S03:REBOOT_REQUESTED
"$BB" sleep 1
"$BB" reboot -f
wait_forever
"""


def guest_command(command, log_path, *, timeout=120, check=True):
    return logged_command(["sudo", "-n", *command], log_path, timeout=timeout, check=check)


def extract_kernel_and_initrd(image, round_dir, log_path):
    nbd_device = "/dev/nbd0"
    mount_dir = round_dir / "verified-boot-mount"
    mount_dir.mkdir()
    attached = False
    mounted = False
    try:
        guest_command(["qemu-nbd", "--read-only", "--format=qcow2",
                       f"--connect={nbd_device}", str(image)], log_path, timeout=30)
        attached = True
        guest_command(["partprobe", nbd_device], log_path, timeout=10)
        listing = command_output(["lsblk", "-nrpo", "PATH,LABEL", nbd_device])
        labels = {}
        for line in listing.splitlines():
            fields = line.split(maxsplit=1)
            if len(fields) == 2:
                labels[fields[1]] = fields[0]
        boot_partition = labels.get("BOOT")
        if not boot_partition:
            raise GuestFailure("verified Ubuntu image has no BOOT filesystem label")

        guest_command(["mount", "-o", "ro", boot_partition, str(mount_dir)], log_path, timeout=20)
        mounted = True
        kernels = sorted(mount_dir.glob("vmlinuz-*"))
        initrds = sorted(mount_dir.glob("initrd.img-*"))
        pairs = [(kernel, mount_dir / f"initrd.img-{kernel.name.removeprefix('vmlinuz-')}")
                 for kernel in kernels]
        pairs = [(kernel, initrd) for kernel, initrd in pairs if initrd.is_file()]
        if len(pairs) != 1 or len(initrds) != 1:
            raise GuestFailure("locked Ubuntu image must contain exactly one matching kernel/initrd pair")

        kernel_source, initrd_source = pairs[0]
        kernel_copy = round_dir / "guest-kernel"
        initrd_copy = round_dir / "guest-initrd-original"
        for source, destination in ((kernel_source, kernel_copy), (initrd_source, initrd_copy)):
            guest_command(["cp", str(source), str(destination)], log_path, timeout=60)
            guest_command(["chown", f"{os.getuid()}:{os.getgid()}", str(destination)],
                          log_path, timeout=10)
            destination.chmod(0o444)
    finally:
        if mounted:
            guest_command(["umount", str(mount_dir)], log_path, timeout=20, check=False)
        if attached:
            guest_command(["qemu-nbd", "--disconnect", nbd_device],
                          log_path, timeout=20, check=False)
        mount_dir.rmdir()

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


def build_guest_initrd(arch, initrd_source, probe, round_dir, log_path):
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
    init_path.write_text(INITRAMFS_SCRIPT, encoding="utf-8")
    init_path.chmod(0o755)
    probe_path = root / "usr/local/bin/s03-probe"
    probe_path.parent.mkdir(parents=True, exist_ok=True)
    shutil.copyfile(probe, probe_path)
    probe_path.chmod(0o755)

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


def qemu_command(arch, lock, overlay, kernel, initrd, serial_log, monitor_path, qemu_binary):
    kernel_arguments = f"console={lock['console']} rdinit=/init panic=10"
    return [qemu_binary, "-name", "nodedance-s03-clock-reboot-guest",
            "-machine", lock["machine"], "-cpu", "max", "-accel", "tcg,thread=multi",
            "-smp", "2", "-m", str(GUEST_MEMORY_MIB), "-rtc", "base=utc,clock=vm",
            "-kernel", str(kernel), "-initrd", str(initrd), "-append", kernel_arguments,
            "-drive", f"if=none,id=osdisk,file={overlay},format=qcow2",
            "-device", f"{lock['block_device']},drive=osdisk",
            "-serial", f"file:{serial_log}", "-monitor",
            f"unix:{monitor_path},server=on,wait=off", "-display", "none"]


def serial_events(serial_log):
    data = serial_log.read_text(encoding="utf-8", errors="replace").replace("\r", "\n")
    events = {}
    for line in data.splitlines():
        if not line.startswith("S03:"):
            continue
        tag, _, value = line.partition(" ")
        events[tag[4:]] = value.strip()
    return events, data


def wait_for_guest(qemu, serial_log, log_path):
    deadline = time.monotonic() + QEMU_TIMEOUT_SECONDS
    while time.monotonic() < deadline:
        if qemu.poll() is not None:
            events, data = serial_events(serial_log)
            if "DONE" not in events:
                raise GuestFailure(f"guest QEMU exited before the initramfs test finished (exit {qemu.returncode}): {data[-2000:]}")
            return events
        events, _ = serial_events(serial_log)
        if "DONE" in events:
            return events
        time.sleep(0.2)
    events, data = serial_events(serial_log)
    raise GuestFailure(f"minimal Linux guest did not finish within {QEMU_TIMEOUT_SECONDS}s; events={sorted(events)}; serial tail={data[-2500:]}")


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
    if abs(float(before_sample.get("rawUptimeSeconds", -999)) - before_uptime) > 2:
        checks.append("pre-clock collector uptime did not match raw proc uptime")
    if abs(float(after_sample.get("rawUptimeSeconds", -999)) - after_uptime) > 2:
        checks.append("post-clock collector uptime did not match raw proc uptime")
    if abs(float(reboot_sample.get("rawUptimeSeconds", -999)) - reboot_after_uptime) > 2:
        checks.append("post-reboot collector uptime did not match raw proc uptime")

    clock_evidence = {
        "clock_before": {"boot_id": before_id, "uptime_seconds": before_uptime, "wall_unix_seconds": before_wall},
        "clock_after": {"boot_id": after_id, "uptime_seconds": after_uptime, "wall_unix_seconds": after_wall},
        "clock_target_unix_seconds": target_wall,
        "reboot_before": {"boot_id": reboot_before_id, "uptime_seconds": reboot_before_uptime, "wall_unix_seconds": reboot_before_wall},
        "reboot_after": {"boot_id": reboot_after_id, "uptime_seconds": reboot_after_uptime, "wall_unix_seconds": reboot_after_wall},
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


def run_guest_round(arch, lock, image, probe, qemu_binary, round_dir, round_number):
    round_dir.mkdir(parents=True, exist_ok=True)
    round_dir.chmod(0o700)
    log_path = round_dir / "commands.log"
    overlay = round_dir / "guest-overlay.qcow2"
    logged_command(["qemu-img", "create", "-f", "qcow2", "-F", "qcow2", "-b", str(image),
                    str(overlay)], log_path, timeout=60)
    kernel, original_initrd = extract_kernel_and_initrd(image, round_dir, log_path)
    initrd, kernel_info = build_guest_initrd(arch, original_initrd, probe, round_dir, log_path)
    qemu_log = round_dir / "qemu.log"
    serial_log = round_dir / "serial.log"
    serial_log.touch()
    with tempfile.TemporaryDirectory(prefix="s03-qemu-") as socket_dir:
        monitor_path = pathlib.Path(socket_dir) / "monitor.sock"
        command = qemu_command(arch, lock, overlay, kernel, initrd,
                              serial_log, monitor_path, qemu_binary)
        with qemu_log.open("w", encoding="utf-8") as output:
            qemu = subprocess.Popen(command, cwd=ROOT, stdout=output, stderr=subprocess.STDOUT)
            try:
                events = wait_for_guest(qemu, serial_log, log_path)
                load_event, clock_status, clock_failures = check_guest_clock_and_reboot(events, round_dir)
                return {
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
                    "full_stage_cases": {"S03-02": "NOT_READY", "S03-08": "NOT_READY"},
                    "covered_guest_behaviors": [
                        "quiet minimal Linux guest controlled CPU and resident-memory load/recovery",
                        "real guest-only wall-clock correction while preserving boot ID and increasing uptime",
                        "real in-guest kernel reboot with boot ID change and uptime restart",
                        "collector boot ID and uptime compared with raw proc evidence before/after clock correction and reboot",
                    ],
                    "pending_stage_behaviors": {
                        "S03-02": ["Agent sampling/heartbeat integration", "end-to-end latency and recovery with Core/UI"],
                        "S03-08": ["Agent detection of clock offset and reboot generation", "stale/recovery behavior through Core/UI"],
                    },
                }
            finally:
                stop_qemu(qemu, monitor_path, qemu_log)
def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--rounds", type=int, default=1, help="fresh guest overlays to test (default: 1)")
    args = parser.parse_args()
    if args.rounds < 1 or args.rounds > 10:
        parser.error("--rounds must be between 1 and 10")

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
        },
        "stage_cases": {
            "S03-02": {
                "status": "NOT_READY",
                "covered_guest_behavior": ["controlled CPU and resident-memory increase/recovery in a minimal real Linux guest"],
                "pending": ["Agent sampling/heartbeat integration", "Core/UI end-to-end latency and recovery"],
            },
            "S03-08": {
                "status": "NOT_READY",
                "covered_guest_behavior": ["guest-only wall-clock correction and in-guest reboot with raw boot_id/uptime evidence"],
                "pending": ["Agent clock-offset and reboot-generation detection", "stale/recovery behavior through Core/UI"],
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
        image, image_info = verify_image(WORK_ROOT / "cache" / "noble-release-20260814",
                                         arch, run_dir / "setup.log")
        summary["image"] = image_info
        probe_info = build_probe(arch, run_dir / "s03-probe", run_dir / "build.log")
        summary["probe"] = probe_info
        host_wall_before = time.time()
        host_monotonic_before = time.monotonic()
        for round_number in range(1, args.rounds + 1):
            result = run_guest_round(arch, lock, image, run_dir / "s03-probe",
                                     summary["toolchain"]["qemu_binary"],
                                     run_dir / f"round-{round_number:02d}", round_number)
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
        if "FAIL" in summary["component_probes"].values():
            summary["guest_probe_status"] = "FAIL"
            summary["reason"] = "one or more isolated guest component probes failed"
        elif "NOT_READY" in summary["component_probes"].values():
            summary["guest_probe_status"] = "NOT_READY"
            summary["reason"] = "one or more isolated guest component probes were not ready"
        else:
            summary["guest_probe_status"] = "PASS"
            summary["reason"] = "isolated guest component probes passed; original S03-02/S03-08 remain NOT_READY pending Agent/Core integration"
    except NotReady as error:
        summary["guest_probe_status"] = "NOT_READY"
        summary["reason"] = str(error)
    except (GuestFailure, OSError, subprocess.SubprocessError, ValueError, KeyError) as error:
        summary["guest_probe_status"] = "FAIL"
        summary["reason"] = f"{type(error).__name__}: {error}"
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
