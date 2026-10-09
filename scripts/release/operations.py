#!/usr/bin/env python3
"""Verify and install/uninstall a signed NodeDance Linux release bundle."""

from __future__ import annotations

import argparse
import base64
import binascii
import ctypes
import errno
import grp
import hashlib
import json
import os
import platform
import pwd
import re
import shutil
import stat
import subprocess
import sys
import tarfile
import tempfile
import zlib
from pathlib import Path

MAX_BINARY = 256 * 1024 * 1024
MAX_ARCHIVE = 600 * 1024 * 1024
MAX_MANIFEST = 64 * 1024
EXPECTED_ENTRIES = {"manifest.json", "nodedance", "nodedance-agent"}
MANIFEST_FIELDS = {
    "formatVersion", "version", "os", "architecture", "coreSha256", "coreSize",
    "agentSha256", "agentSize", "signature",
}
SIGNED_FIELDS = (
    "formatVersion", "version", "os", "architecture", "coreSha256", "coreSize",
    "agentSha256", "agentSize",
)
VERSION = re.compile(r"^v?[0-9]+\.[0-9]+\.[0-9]+(?:[-+][0-9A-Za-z.-]+)?$")
SHA256 = re.compile(r"^[0-9a-f]{64}$")
SPKI_PREFIX = bytes.fromhex("302a300506032b6570032100")
DATA_DIR = Path("/var/lib/nodedance")
SERVICE_NAME = "nodedance.service"
SERVICE_USER = "nodedance"
SYSTEMD_DIR = Path("/etc/systemd/system")


class ReleaseError(Exception):
    pass


def fail(message: str) -> None:
    raise ReleaseError(message)


def object_no_duplicates(pairs):
    result = {}
    for key, value in pairs:
        if key in result:
            fail("manifest contains a duplicate JSON field")
        result[key] = value
    return result


def host_architecture() -> str:
    machine = platform.machine().lower()
    if machine in {"x86_64", "amd64"}:
        return "amd64"
    if machine in {"aarch64", "arm64"}:
        return "arm64"
    fail(f"unsupported Linux architecture: {machine}")


def load_public_key(path: Path) -> bytes:
    fd = -1
    try:
        fd = os.open(path, os.O_RDONLY | getattr(os, "O_NOFOLLOW", 0))
        info = os.fstat(fd)
        if not stat.S_ISREG(info.st_mode):
            fail("trusted public key must be a regular non-symlink file")
        if info.st_size > 4096:
            fail("trusted public key file is oversized")
        with os.fdopen(fd, "rb") as stream:
            fd = -1
            raw = stream.read(4097)
        if len(raw) > 4096:
            fail("trusted public key file is oversized")
        key = base64.b64decode(raw.strip(), validate=True)
    except (OSError, binascii.Error) as error:
        fail(f"cannot read trusted public key: {error}")
    finally:
        if fd >= 0:
            os.close(fd)
    if len(key) != 32:
        fail("trusted public key must contain a base64 Ed25519 public key")
    return key


def verify_signature(manifest: dict, public_key: bytes) -> None:
    if set(manifest) != MANIFEST_FIELDS:
        fail("release manifest fields do not match the supported format")
    if type(manifest["formatVersion"]) is not int or manifest["formatVersion"] != 1:
        fail("unsupported release manifest format")
    version = manifest["version"]
    if not isinstance(version, str) or not VERSION.fullmatch(version):
        fail("release version is invalid")
    if (manifest["os"] != "linux" or not isinstance(manifest["architecture"], str)
            or manifest["architecture"] not in {"amd64", "arm64"}):
        fail("release target must be linux/amd64 or linux/arm64")
    for digest_field, size_field in (("coreSha256", "coreSize"), ("agentSha256", "agentSize")):
        size = manifest[size_field]
        if type(size) is not int or not 1 <= size <= MAX_BINARY:
            fail(f"{size_field} is outside the supported range")
        if not isinstance(manifest[digest_field], str) or not SHA256.fullmatch(manifest[digest_field]):
            fail(f"{digest_field} is invalid")
    try:
        signature = base64.b64decode(manifest["signature"], validate=True)
    except (TypeError, binascii.Error):
        fail("release signature is malformed")
    if len(signature) != 64:
        fail("release signature has an invalid size")
    signed = {key: manifest[key] for key in SIGNED_FIELDS}
    payload = json.dumps(signed, ensure_ascii=True, separators=(",", ":")).encode("ascii")
    pem_body = base64.b64encode(SPKI_PREFIX + public_key).decode("ascii")
    pem = "-----BEGIN PUBLIC KEY-----\n" + pem_body + "\n-----END PUBLIC KEY-----\n"
    with tempfile.TemporaryDirectory(prefix="nodedance-release-verify-") as directory:
        root = Path(directory)
        pub_path = root / "trusted.pem"
        payload_path = root / "signed-fields.json"
        signature_path = root / "signature.bin"
        pub_path.write_text(pem, encoding="ascii")
        payload_path.write_bytes(payload)
        signature_path.write_bytes(signature)
        result = subprocess.run(
            ["openssl", "pkeyutl", "-verify", "-pubin", "-inkey", str(pub_path),
             "-rawin", "-in", str(payload_path), "-sigfile", str(signature_path)],
            stdout=subprocess.DEVNULL,
            stderr=subprocess.DEVNULL,
            check=False,
        )
        if result.returncode != 0:
            fail("release signature verification failed; provide the publisher's trusted public key")


def read_bundle(bundle_path: Path, public_key_path: Path) -> tuple[dict, dict[str, bytes]]:
    fd = -1
    try:
        fd = os.open(bundle_path, os.O_RDONLY | getattr(os, "O_NOFOLLOW", 0))
        archive_info = os.fstat(fd)
        if not stat.S_ISREG(archive_info.st_mode) or archive_info.st_size > MAX_ARCHIVE:
            fail("bundle must be a regular file within the supported archive size limit")
        public_key = load_public_key(public_key_path)
        entries: dict[str, bytes] = {}
        with os.fdopen(fd, "rb") as bundle_stream:
            fd = -1
            with tarfile.open(fileobj=bundle_stream, mode="r:gz") as archive:
                for member in archive:
                    if len(entries) >= len(EXPECTED_ENTRIES):
                        fail("bundle contains unexpected or duplicate entries")
                    if member.name not in EXPECTED_ENTRIES or member.name in entries:
                        fail("bundle contains an unexpected or duplicate path")
                    if not member.isreg() or member.issym() or member.islnk() or member.pax_headers or getattr(member, "sparse", None):
                        fail("bundle entries must be regular files")
                    limit = MAX_MANIFEST if member.name == "manifest.json" else MAX_BINARY
                    if member.size < 1 or member.size > limit:
                        fail(f"bundle entry {member.name} has an invalid size")
                    stream = archive.extractfile(member)
                    if stream is None:
                        fail(f"cannot read bundle entry {member.name}")
                    with stream:
                        content = stream.read(limit + 1)
                    if len(content) != member.size or len(content) > limit:
                        fail(f"bundle entry {member.name} is truncated or oversized")
                    entries[member.name] = content
    except (OSError, tarfile.TarError, EOFError, zlib.error) as error:
        fail(f"cannot read release bundle: {error}")
    finally:
        if fd >= 0:
            os.close(fd)
    if set(entries) != EXPECTED_ENTRIES:
        fail("release bundle is missing a required file")
    try:
        manifest = json.loads(entries["manifest.json"], object_pairs_hook=object_no_duplicates)
    except (UnicodeDecodeError, json.JSONDecodeError) as error:
        fail(f"release manifest is invalid JSON: {error}")
    if not isinstance(manifest, dict):
        fail("release manifest must be a JSON object")
    verify_signature(manifest, public_key)
    for name, digest_field, size_field in (
        ("nodedance", "coreSha256", "coreSize"),
        ("nodedance-agent", "agentSha256", "agentSize"),
    ):
        contents = entries[name]
        if len(contents) != manifest[size_field] or hashlib.sha256(contents).hexdigest() != manifest[digest_field]:
            fail(f"{name} does not match its signed digest and size")
    return manifest, entries


def require_linux_arch(manifest: dict) -> None:
    if sys.platform != "linux":
        fail("release bundles can be installed only on Linux")
    actual = host_architecture()
    if manifest["architecture"] != actual:
        fail(f"release architecture linux/{manifest['architecture']} does not match this host linux/{actual}")


def write_new(path: Path, content: bytes, mode: int) -> None:
    flags = os.O_WRONLY | os.O_CREAT | os.O_EXCL
    flags |= getattr(os, "O_NOFOLLOW", 0)
    fd = os.open(path, flags, mode)
    try:
        view = memoryview(content)
        while view:
            written = os.write(fd, view)
            if written <= 0:
                raise OSError("short write")
            view = view[written:]
        os.fsync(fd)
    except BaseException:
        try:
            os.unlink(path)
        except OSError:
            pass
        raise
    finally:
        os.close(fd)


def sync_directory(path: Path) -> None:
    fd = os.open(path, os.O_RDONLY | getattr(os, "O_DIRECTORY", 0))
    try:
        os.fsync(fd)
    finally:
        os.close(fd)


def rename_directory_noreplace(source: Path, destination: Path) -> None:
    if sys.platform != "linux":
        fail("atomic no-replace directory install requires Linux renameat2")
    libc = ctypes.CDLL(None, use_errno=True)
    renameat2 = getattr(libc, "renameat2", None)
    if renameat2 is None:
        fail("this Linux system does not provide renameat2; refusing a non-atomic install")
    renameat2.argtypes = (ctypes.c_int, ctypes.c_char_p, ctypes.c_int, ctypes.c_char_p, ctypes.c_uint)
    renameat2.restype = ctypes.c_int
    at_fdcwd = -100
    rename_noreplace = 1
    result = renameat2(at_fdcwd, os.fsencode(source), at_fdcwd, os.fsencode(destination), rename_noreplace)
    if result != 0:
        error = ctypes.get_errno()
        if error == errno.EEXIST:
            fail(f"installation prefix already exists; refusing to replace unknown files: {destination}")
        raise OSError(error, os.strerror(error), str(destination))


def install(args) -> None:
    manifest, entries = read_bundle(args.bundle, args.public_key_file)
    require_linux_arch(manifest)
    prefix = validate_prefix(args.prefix)
    if not args.no_service:
        if os.geteuid() != 0:
            fail("installing the Core systemd service requires root; use --no-service for manual setups")
        check_service_install_preconditions(prefix)
        check_secure_install_parent(prefix)
        ensure_service_account_and_data()
    prefix.parent.mkdir(parents=True, exist_ok=True)
    staging = Path(tempfile.mkdtemp(prefix=".nodedance-install-", dir=prefix.parent))
    try:
        write_new(staging / "nodedance", entries["nodedance"], 0o755)
        write_new(staging / "nodedance-agent", entries["nodedance-agent"], 0o755)
        write_new(staging / "release-manifest.json", entries["manifest.json"], 0o644)
        os.chmod(staging, 0o755)
        sync_directory(staging)
        rename_directory_noreplace(staging, prefix)
        sync_directory(prefix.parent)
    except BaseException:
        shutil.rmtree(staging, ignore_errors=True)
        raise
    print(f"Installed signed NodeDance {manifest['version']} binaries in {prefix}")
    if args.no_service:
        print("Core systemd service was not installed (--no-service).")
    else:
        install_core_unit(prefix, entries)
        print(f"Enabled and started {SERVICE_NAME} on 127.0.0.1:8180.")
    print(f"Core business data is stored separately in {DATA_DIR} and is not part of uninstall.")


def validate_prefix(prefix: Path) -> Path:
    if not prefix.is_absolute() or ".." in prefix.parts or prefix == Path("/"):
        fail("installation prefix must be an absolute non-root path without '..'")
    normalized = Path(os.path.normpath(str(prefix)))
    if not re.fullmatch(r"/[A-Za-z0-9._/-]+", str(normalized)):
        fail("installation prefix contains characters unsupported by the systemd unit")
    return normalized


def render_core_unit(prefix: Path) -> bytes:
    return (
        "[Unit]\n"
        "Description=NodeDance Core\n"
        "After=network-online.target\n"
        "Wants=network-online.target\n"
        "\n"
        "[Service]\n"
        "Type=simple\n"
        f"User={SERVICE_USER}\n"
        f"Group={SERVICE_USER}\n"
        f"WorkingDirectory={DATA_DIR}\n"
        f"Environment=HOME={DATA_DIR}\n"
        "UMask=0077\n"
        f"ExecStart={prefix}/nodedance serve --listen 127.0.0.1:8180 --data-dir {DATA_DIR}\n"
        "Restart=on-failure\n"
        "RestartSec=3s\n"
        "NoNewPrivileges=true\n"
        "PrivateTmp=true\n"
        "ProtectSystem=strict\n"
        "ProtectHome=true\n"
        f"ReadWritePaths={DATA_DIR}\n"
        "\n"
        "[Install]\n"
        "WantedBy=multi-user.target\n"
    ).encode("utf-8")


def check_service_install_preconditions(prefix: Path) -> None:
    if shutil.which("systemctl") is None:
        fail("systemctl is required for the default Core service installation")
    try:
        info = SYSTEMD_DIR.lstat()
    except OSError as error:
        fail(f"cannot inspect system unit directory: {error}")
    if not stat.S_ISDIR(info.st_mode):
        fail("system unit directory must be a real directory")
    unit_path = SYSTEMD_DIR / SERVICE_NAME
    try:
        unit_path.lstat()
    except FileNotFoundError:
        pass
    else:
        fail(f"refusing to replace an existing unit: {unit_path}")
    try:
        prefix.lstat()
    except FileNotFoundError:
        return
    fail(f"installation prefix already exists; refusing to replace unknown files: {prefix}")


def check_secure_install_parent(prefix: Path) -> None:
    try:
        info = prefix.parent.lstat()
    except OSError as error:
        fail(f"systemd installation prefix parent must already exist: {error}")
    if not stat.S_ISDIR(info.st_mode) or info.st_uid != 0 or info.st_mode & 0o022:
        fail("systemd installation prefix parent must be a root-owned directory not writable by group or others")


def ensure_service_account_and_data() -> None:
    nologin = "/usr/sbin/nologin" if Path("/usr/sbin/nologin").exists() else "/sbin/nologin"
    try:
        account = pwd.getpwnam(SERVICE_USER)
    except KeyError:
        useradd = shutil.which("useradd")
        if useradd is None:
            fail("useradd is required to create the dedicated Core service account")
        result = subprocess.run(
            [useradd, "--system", "--user-group", "--home-dir", str(DATA_DIR),
             "--shell", nologin, "--no-create-home", SERVICE_USER],
            stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, check=False,
        )
        if result.returncode != 0:
            fail("could not create the dedicated nodedance system account")
        try:
            account = pwd.getpwnam(SERVICE_USER)
        except KeyError:
            fail("useradd did not create the nodedance account")
    try:
        group = grp.getgrgid(account.pw_gid)
    except KeyError:
        fail("nodedance service account has no valid primary group")
    if (account.pw_uid == 0 or account.pw_dir != str(DATA_DIR)
            or account.pw_shell not in {"/usr/sbin/nologin", "/sbin/nologin"}
            or group.gr_name != SERVICE_USER):
        fail("existing nodedance account is not the dedicated non-root system identity required here")
    try:
        DATA_DIR.mkdir(mode=0o750)
        created = True
    except FileExistsError:
        created = False
    except OSError as error:
        fail(f"cannot create Core data directory: {error}")
    try:
        info = DATA_DIR.lstat()
    except OSError as error:
        fail(f"cannot inspect Core data directory: {error}")
    if not stat.S_ISDIR(info.st_mode):
        fail("Core data directory must be a real directory, not a symlink")
    if created:
        os.chown(DATA_DIR, account.pw_uid, account.pw_gid, follow_symlinks=False)
    elif info.st_uid != account.pw_uid or info.st_gid != account.pw_gid or info.st_mode & 0o007:
        fail("existing Core data directory must be private and owned by nodedance; it was left unchanged")


def install_core_unit(prefix: Path, entries: dict[str, bytes]) -> None:
    unit_path = SYSTEMD_DIR / SERVICE_NAME
    created = False
    try:
        write_new(unit_path, render_core_unit(prefix), 0o644)
        created = True
        run_systemctl("daemon-reload")
        run_systemctl("enable", "--now", SERVICE_NAME)
    except Exception as original:
        try:
            if created:
                rollback_service_activation()
                remove_unit_if_matches(prefix)
                run_systemctl("daemon-reload")
            cleanup_release_install(prefix, entries)
        except Exception as rollback_error:
            fail(f"Core service installation failed ({original}); rollback could not safely finish ({rollback_error}); inspect {unit_path} and {prefix}; shared data in {DATA_DIR} was preserved")
        fail(f"Core service installation failed and its new unit/binaries were rolled back; shared data in {DATA_DIR} was preserved: {original}")


def rollback_service_activation() -> None:
    # daemon-reload or enable --now may fail before systemd has loaded the new
    # unit. Attempt both cleanup operations, then query the manager so an
    # unknown unit is safe to remove while an active/enabled one is retained.
    for action in (("stop", SERVICE_NAME), ("disable", SERVICE_NAME)):
        try:
            run_systemctl(*action)
        except ReleaseError:
            pass
    active_status = query_systemctl("is-active", "--quiet", SERVICE_NAME)
    if active_status == 0:
        fail("new Core service is still active; preserving its unit and binaries")
    if active_status not in {3, 4}:
        fail(f"cannot confirm the new Core service is stopped (systemctl exit {active_status})")
    enabled_status = query_systemctl("is-enabled", "--quiet", SERVICE_NAME)
    if enabled_status == 0:
        fail("new Core service is still enabled; preserving its unit and binaries")
    if enabled_status not in {1, 4}:
        fail(f"cannot confirm the new Core service is disabled (systemctl exit {enabled_status})")


def query_systemctl(*arguments: str) -> int:
    try:
        result = subprocess.run(
            ["systemctl", *arguments], stdout=subprocess.DEVNULL,
            stderr=subprocess.DEVNULL, timeout=60, check=False,
        )
    except (OSError, subprocess.TimeoutExpired) as error:
        fail(f"cannot verify systemd service state during rollback: {error}")
    return result.returncode


def run_systemctl(*arguments: str) -> None:
    result = subprocess.run(
        ["systemctl", *arguments], stdout=subprocess.PIPE, stderr=subprocess.PIPE,
        text=True, timeout=60, check=False,
    )
    if result.returncode != 0:
        detail = (result.stderr or result.stdout).strip().replace("\n", " ")[:300]
        fail(f"systemctl {' '.join(arguments)} failed (exit {result.returncode}): {detail}")


def remove_unit_if_matches(prefix: Path) -> None:
    directory_fd = os.open(SYSTEMD_DIR, os.O_RDONLY | getattr(os, "O_DIRECTORY", 0) | getattr(os, "O_NOFOLLOW", 0))
    try:
        expected = render_core_unit(prefix)
        fd, original = open_regular_at(directory_fd, SERVICE_NAME, MAX_MANIFEST)
        with os.fdopen(fd, "rb") as stream:
            current = stream.read(MAX_MANIFEST + 1)
        if current != expected:
            fail("new Core unit changed during rollback; preserving it and its binary target")
        fd, opened = open_regular_at(directory_fd, SERVICE_NAME, MAX_MANIFEST)
        with os.fdopen(fd, "rb") as stream:
            fresh = stream.read(MAX_MANIFEST + 1)
        current_info = os.stat(SERVICE_NAME, dir_fd=directory_fd, follow_symlinks=False)
        if fresh != expected or not same_inode(opened, original) or not same_inode(current_info, original):
            fail("new Core unit changed during rollback; preserving it and its binary target")
        os.unlink(SERVICE_NAME, dir_fd=directory_fd)
    finally:
        os.close(directory_fd)


def cleanup_release_install(prefix: Path, entries: dict[str, bytes]) -> None:
    directory_fd = os.open(prefix, os.O_RDONLY | getattr(os, "O_DIRECTORY", 0) | getattr(os, "O_NOFOLLOW", 0))
    try:
        files = {
            "nodedance": entries["nodedance"],
            "nodedance-agent": entries["nodedance-agent"],
            "release-manifest.json": entries["manifest.json"],
        }
        for name, expected_content in files.items():
            fd, original = open_regular_at(directory_fd, name, max(MAX_BINARY, MAX_MANIFEST))
            with os.fdopen(fd, "rb") as stream:
                current = stream.read(max(MAX_BINARY, MAX_MANIFEST) + 1)
            if current != expected_content:
                fail(f"new release file {name} changed during rollback; preserving it")
            fd, opened = open_regular_at(directory_fd, name, max(MAX_BINARY, MAX_MANIFEST))
            with os.fdopen(fd, "rb") as stream:
                if stream.read(max(MAX_BINARY, MAX_MANIFEST) + 1) != expected_content:
                    fail(f"new release file {name} changed during rollback; preserving it")
            current_info = os.stat(name, dir_fd=directory_fd, follow_symlinks=False)
            if not same_inode(opened, original) or not same_inode(current_info, original):
                fail(f"new release file {name} was replaced during rollback; preserving it")
            os.unlink(name, dir_fd=directory_fd)
        os.fsync(directory_fd)
    finally:
        os.close(directory_fd)
    try:
        parent_fd = os.open(prefix.parent, os.O_RDONLY | getattr(os, "O_DIRECTORY", 0) | getattr(os, "O_NOFOLLOW", 0))
        try:
            os.rmdir(prefix.name, dir_fd=parent_fd)
            os.fsync(parent_fd)
        finally:
            os.close(parent_fd)
    except OSError as error:
        if error.errno not in {errno.ENOTEMPTY, errno.EEXIST}:
            raise


def open_regular_at(directory_fd: int, name: str, size_limit: int) -> tuple[int, os.stat_result]:
    fd = os.open(name, os.O_RDONLY | getattr(os, "O_NOFOLLOW", 0), dir_fd=directory_fd)
    try:
        info = os.fstat(fd)
        if not stat.S_ISREG(info.st_mode) or info.st_size > size_limit:
            fail(f"{name} must be a regular file within the supported size limit")
        return fd, info
    except BaseException:
        os.close(fd)
        raise


def read_installed_release(prefix: Path, public_key_file: Path) -> tuple[dict, int, dict[str, os.stat_result], dict[str, str]]:
    flags = os.O_RDONLY | getattr(os, "O_DIRECTORY", 0) | getattr(os, "O_NOFOLLOW", 0)
    try:
        directory_fd = os.open(prefix, flags)
    except OSError as error:
        fail(f"cannot open installation prefix without following symlinks: {error}")
    identities: dict[str, os.stat_result] = {}
    digests: dict[str, str] = {}
    try:
        manifest_fd, identities["release-manifest.json"] = open_regular_at(directory_fd, "release-manifest.json", MAX_MANIFEST)
        with os.fdopen(manifest_fd, "rb") as stream:
            raw = stream.read(MAX_MANIFEST + 1)
        if len(raw) > MAX_MANIFEST:
            fail("installed release manifest is oversized")
        try:
            manifest = json.loads(raw, object_pairs_hook=object_no_duplicates)
        except (UnicodeDecodeError, json.JSONDecodeError) as error:
            fail(f"installed release manifest is invalid: {error}")
        if not isinstance(manifest, dict):
            fail("installed release manifest must be a JSON object")
        verify_signature(manifest, load_public_key(public_key_file))
        require_linux_arch(manifest)
        digests["release-manifest.json"] = hashlib.sha256(raw).hexdigest()
        for filename, digest_field, size_field in (
            ("nodedance", "coreSha256", "coreSize"),
            ("nodedance-agent", "agentSha256", "agentSize"),
        ):
            file_fd, identities[filename] = open_regular_at(directory_fd, filename, MAX_BINARY)
            digest = hashlib.sha256()
            size = 0
            with os.fdopen(file_fd, "rb") as stream:
                for chunk in iter(lambda: stream.read(1024 * 1024), b""):
                    size += len(chunk)
                    digest.update(chunk)
            if size != manifest[size_field] or digest.hexdigest() != manifest[digest_field]:
                fail(f"installed {filename} was changed; refusing to remove it")
            digests[filename] = digest.hexdigest()
        return manifest, directory_fd, identities, digests
    except BaseException:
        os.close(directory_fd)
        raise


def same_inode(left: os.stat_result, right: os.stat_result) -> bool:
    return left.st_dev == right.st_dev and left.st_ino == right.st_ino and stat.S_ISREG(left.st_mode) and stat.S_ISREG(right.st_mode)


def unlink_verified_at(directory_fd: int, name: str, expected: os.stat_result, expected_digest: str, expected_size: int) -> None:
    file_fd, opened_info = open_regular_at(directory_fd, name, max(MAX_BINARY, MAX_MANIFEST))
    digest = hashlib.sha256()
    size = 0
    with os.fdopen(file_fd, "rb") as stream:
        if not same_inode(opened_info, expected):
            fail(f"{name} changed after verification; refusing to unlink it")
        for chunk in iter(lambda: stream.read(1024 * 1024), b""):
            size += len(chunk)
            digest.update(chunk)
    current_info = os.stat(name, dir_fd=directory_fd, follow_symlinks=False)
    if (not same_inode(current_info, expected) or size != expected_size
            or digest.hexdigest() != expected_digest):
        fail(f"{name} changed after verification; refusing to unlink it")
    os.unlink(name, dir_fd=directory_fd)


def remove_core_unit(prefix: Path) -> bool:
    try:
        unit_dir_fd = os.open(SYSTEMD_DIR, os.O_RDONLY | getattr(os, "O_DIRECTORY", 0) | getattr(os, "O_NOFOLLOW", 0))
    except FileNotFoundError:
        return False
    try:
        try:
            unit_fd, original_info = open_regular_at(unit_dir_fd, SERVICE_NAME, MAX_MANIFEST)
        except FileNotFoundError:
            if shutil.which("systemctl"):
                active = subprocess.run(["systemctl", "is-active", "--quiet", SERVICE_NAME],
                                        stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
                                        timeout=30, check=False)
                if active.returncode == 0:
                    fail("Core service is active but its unit file is missing; refusing to remove binaries")
            return False
        with os.fdopen(unit_fd, "rb") as stream:
            current_unit = stream.read(MAX_MANIFEST + 1)
        expected_unit = render_core_unit(prefix)
        if current_unit != expected_unit:
            fail("Core systemd unit was changed; refusing to remove an unknown unit")
        if os.geteuid() != 0:
            fail("removing the Core systemd service requires root")
        run_systemctl("disable", "--now", SERVICE_NAME)
        unit_fd, fresh_info = open_regular_at(unit_dir_fd, SERVICE_NAME, MAX_MANIFEST)
        with os.fdopen(unit_fd, "rb") as stream:
            fresh_unit = stream.read(MAX_MANIFEST + 1)
        current_info = os.stat(SERVICE_NAME, dir_fd=unit_dir_fd, follow_symlinks=False)
        if (not same_inode(fresh_info, original_info) or not same_inode(current_info, original_info)
                or fresh_unit != expected_unit):
            fail("Core systemd unit changed while stopping; refusing to unlink it")
        os.unlink(SERVICE_NAME, dir_fd=unit_dir_fd)
        run_systemctl("daemon-reload")
        return True
    finally:
        os.close(unit_dir_fd)


def uninstall(args) -> None:
    prefix = validate_prefix(args.prefix)
    manifest, directory_fd, identities, digests = read_installed_release(prefix, args.public_key_file)
    try:
        had_unit = remove_core_unit(prefix)
        for name in ("nodedance", "nodedance-agent", "release-manifest.json"):
            unlink_verified_at(directory_fd, name, identities[name], digests[name], identities[name].st_size)
        os.fsync(directory_fd)
        try:
            parent_fd = os.open(prefix.parent, os.O_RDONLY | getattr(os, "O_DIRECTORY", 0) | getattr(os, "O_NOFOLLOW", 0))
            try:
                pinned = os.fstat(directory_fd)
                current = os.stat(prefix.name, dir_fd=parent_fd, follow_symlinks=False)
                if pinned.st_dev == current.st_dev and pinned.st_ino == current.st_ino and stat.S_ISDIR(current.st_mode):
                    os.rmdir(prefix.name, dir_fd=parent_fd)
                    os.fsync(parent_fd)
            finally:
                os.close(parent_fd)
            print(f"Uninstalled NodeDance {manifest['version']} binaries" + (" and Core service" if had_unit else ""))
        except OSError:
            print(f"Uninstalled NodeDance {manifest['version']} managed files; retained {prefix} because it contains other files.")
        print(f"Preserved Core business data in {DATA_DIR} and any unrecognized prefix files.")
    finally:
        os.close(directory_fd)


def verify(args) -> None:
    manifest, _ = read_bundle(args.bundle, args.public_key_file)
    print(f"Verified NodeDance {manifest['version']} linux/{manifest['architecture']} release bundle.")


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    subparsers = parser.add_subparsers(dest="command", required=True)
    install_parser = subparsers.add_parser("install")
    install_parser.add_argument("--bundle", type=Path, required=True)
    install_parser.add_argument("--public-key-file", type=Path, required=True,
                                help="trusted Ed25519 key obtained independently of the bundle")
    install_parser.add_argument("--prefix", type=Path, default=Path("/opt/nodedance"))
    install_parser.add_argument("--no-service", action="store_true",
                                help="install binaries only for manual/non-systemd operation")
    uninstall_parser = subparsers.add_parser("uninstall")
    uninstall_parser.add_argument("--public-key-file", type=Path, required=True,
                                  help="trusted Ed25519 key obtained independently of the installation")
    uninstall_parser.add_argument("--prefix", type=Path, default=Path("/opt/nodedance"))
    verify_parser = subparsers.add_parser("verify")
    verify_parser.add_argument("--bundle", type=Path, required=True)
    verify_parser.add_argument("--public-key-file", type=Path, required=True,
                               help="trusted Ed25519 key obtained independently of the bundle")
    unit_parser = subparsers.add_parser("render-unit", help=argparse.SUPPRESS)
    unit_parser.add_argument("--prefix", type=Path, default=Path("/opt/nodedance"))
    args = parser.parse_args()
    try:
        if args.command == "install":
            install(args)
        elif args.command == "uninstall":
            uninstall(args)
        elif args.command == "verify":
            verify(args)
        else:
            sys.stdout.buffer.write(render_core_unit(validate_prefix(args.prefix)))
        return 0
    except ReleaseError as error:
        print(f"nodedance-release: {error}", file=sys.stderr)
        return 1
    except OSError as error:
        print(f"nodedance-release: filesystem operation failed: {error}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
