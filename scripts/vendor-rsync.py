#!/usr/bin/env python3
import argparse
import gzip
import hashlib
import io
import json
import os
from pathlib import Path
import platform
import shlex
import shutil
import struct
import subprocess
import tarfile
import tempfile

ROOT = Path(__file__).resolve().parents[1]
BUNDLE = ROOT / "internal/transfer/rsyncbundle"
VERSION = "3.4.1"
SOURCE_SHA256 = "2924bcb3a1ed8b551fc101f740b9f0fe0a202b115027647cf69850d65fd88c52"
SOURCE_URL = "https://github.com/RsyncProject/rsync/releases/download/v3.4.1/rsync-3.4.1.tar.gz"
MACHINES = {"amd64": 62, "arm64": 183}
HOSTS = {"amd64": "x86_64-linux-gnu", "arm64": "aarch64-linux-gnu"}
TRIM = ["--disable-openssl", "--disable-xxhash", "--disable-zstd", "--disable-lz4", "--disable-acl-support", "--disable-xattr-support", "--disable-iconv", "--with-included-zlib", "--with-included-popt"]


def digest(raw):
    return hashlib.sha256(raw).hexdigest()


def binary_from_archive(raw, architecture):
    with tarfile.open(fileobj=io.BytesIO(raw), mode="r:gz") as archive:
        members = archive.getmembers()
        if len(members) != 1 or members[0].name != "rsync" or not members[0].isfile() or members[0].size <= 0 or members[0].size > 16 * 1024 * 1024:
            raise ValueError("unexpected rsync archive entries")
        binary = archive.extractfile(members[0]).read()
    if len(binary) < 64 or binary[:6] != b"\x7fELF\x02\x01" or struct.unpack_from("<H", binary, 18)[0] != MACHINES[architecture]:
        raise ValueError("incorrect rsync ELF architecture")
    offset = struct.unpack_from("<Q", binary, 32)[0]
    size, count = struct.unpack_from("<HH", binary, 54)
    for index in range(count):
        entry = offset + index * size
        if struct.unpack_from("<I", binary, entry)[0] == 3:
            raise ValueError("rsync requires an external ELF interpreter")
    offset = struct.unpack_from("<Q", binary, 40)[0]
    size, count = struct.unpack_from("<HH", binary, 58)
    for index in range(count):
        if struct.unpack_from("<I", binary, offset + index * size + 4)[0] == 2:
            raise ValueError("rsync executable has not been stripped")
    return binary


def record_asset(manifest, architecture, raw):
    name = f"rsync-linux-{architecture}.tar.gz"
    binary = binary_from_archive(raw, architecture)
    spec = {"machine": MACHINES[architecture], "sha256": digest(raw), "binarySHA256": digest(binary), "size": len(binary)}
    manifest["assets"][name] = spec
    text = f'''//go:build linux && {architecture}

package rsyncbundle

import _ "embed"

//go:embed assets/{name}
var archive []byte

const archiveSHA256 = "{spec['sha256']}"
const binarySHA256 = "{spec['binarySHA256']}"
const archiveMachine = {spec['machine']}
'''
    (BUNDLE / f"archive_linux_{architecture}.go").write_text(text)


def build(manifest, architecture):
    host = HOSTS[architecture]
    native = {"x86_64": "amd64", "aarch64": "arm64"}.get(platform.machine())
    compiler = shlex.split(os.environ.get("RSYNC_CC", "gcc" if native == architecture else host + "-gcc"))
    strip = shlex.split(os.environ.get("RSYNC_STRIP", "strip" if native == architecture else host + "-strip"))
    with tempfile.TemporaryDirectory(prefix="nodedance-rsync-build-") as directory:
        work = Path(directory)
        source_archive = work / "rsync-3.4.1.tar.gz"
        subprocess.run(["curl", "--fail", "--location", "--retry", "3", "--connect-timeout", "20", "--max-time", "180", "--output", str(source_archive), SOURCE_URL], check=True)
        if digest(source_archive.read_bytes()) != SOURCE_SHA256:
            raise ValueError("rsync source checksum mismatch")
        source = work / "source"
        source.mkdir()
        subprocess.run(["tar", "xzf", str(source_archive), "-C", str(source), "--strip-components=1"], check=True)
        environment = dict(os.environ, CC=shlex.join(compiler), LC_ALL="C", SOURCE_DATE_EPOCH="0")
        flags = f"-Os -ffunction-sections -fdata-sections -ffile-prefix-map={source}=."
        subprocess.run(["./configure", "--host=" + host, *TRIM, "CFLAGS=" + flags, "LDFLAGS=-static -Wl,--gc-sections"], cwd=source, env=environment, check=True)
        subprocess.run(["make", "-j" + os.environ.get("RSYNC_BUILD_JOBS", "2"), "rsync"], cwd=source, env=environment, check=True)
        subprocess.run([*strip, "--strip-all", "rsync"], cwd=source, check=True)
        binary = (source / "rsync").read_bytes()
        output = io.BytesIO()
        with gzip.GzipFile(fileobj=output, mode="wb", filename="", mtime=0, compresslevel=9) as compressed:
            with tarfile.open(fileobj=compressed, mode="w", format=tarfile.GNU_FORMAT) as archive:
                entry = tarfile.TarInfo("rsync")
                entry.size, entry.mode, entry.mtime = len(binary), 0o700, 0
                archive.addfile(entry, io.BytesIO(binary))
        raw = output.getvalue()
        record_asset(manifest, architecture, raw)
        (BUNDLE / "assets" / f"rsync-linux-{architecture}.tar.gz").write_bytes(raw)
        shutil.copyfile(source / "COPYING", BUNDLE / "licenses/COPYING")
    manifest["trim"] = TRIM
    manifest["build"] = {"cflags": "-Os -ffunction-sections -fdata-sections -ffile-prefix-map=<source>=.", "ldflags": "-static -Wl,--gc-sections", "strip": "--strip-all"}


def check(manifest, architecture):
    name = f"rsync-linux-{architecture}.tar.gz"
    spec = manifest["assets"][name]
    raw = (BUNDLE / "assets" / name).read_bytes()
    if digest(raw) != spec["sha256"]:
        raise ValueError("rsync archive checksum mismatch: " + name)
    binary = binary_from_archive(raw, architecture)
    if digest(binary) != spec["binarySHA256"] or len(binary) != spec["size"] or spec["machine"] != MACHINES[architecture]:
        raise ValueError("rsync executable metadata mismatch: " + name)
    source = (BUNDLE / f"archive_linux_{architecture}.go").read_text()
    for value in [f'const archiveSHA256 = "{spec["sha256"]}"', f'const binarySHA256 = "{spec["binarySHA256"]}"', f'const archiveMachine = {spec["machine"]}', f'//go:embed assets/{name}']:
        if value not in source:
            raise ValueError("rsync embedding metadata mismatch: " + name)
    print(f"Bundled rsync {VERSION} linux/{architecture}: static, stripped, SHA-256 verified ({len(binary)} bytes)")


def main():
    parser = argparse.ArgumentParser(description="Build or verify the pinned static rsync embedded in Agent artifacts")
    mode = parser.add_mutually_exclusive_group()
    mode.add_argument("--build", action="store_true")
    mode.add_argument("--refresh", action="store_true")
    mode.add_argument("--check", action="store_true")
    parser.add_argument("--architecture", choices=MACHINES)
    args = parser.parse_args()
    manifest = json.loads((BUNDLE / "manifest.json").read_text())
    if manifest["version"] != VERSION or manifest["source"]["sha256"] != SOURCE_SHA256 or manifest["source"]["url"] != SOURCE_URL:
        raise ValueError("rsync source must remain pinned to the reviewed version and checksum")
    architectures = [args.architecture] if args.architecture else list(MACHINES)
    if args.build:
        if not args.architecture:
            parser.error("--build requires --architecture amd64 or arm64")
        build(manifest, args.architecture)
    if args.refresh:
        for architecture in architectures:
            name = f"rsync-linux-{architecture}.tar.gz"
            raw = (BUNDLE / "assets" / name).read_bytes()
            if digest(raw) != manifest["assets"][name]["sha256"]:
                raise ValueError("cannot refresh an unverified rsync archive")
            record_asset(manifest, architecture, raw)
    if args.build or args.refresh:
        manifest["licenseSHA256"] = digest((BUNDLE / "licenses/COPYING").read_bytes())
        (BUNDLE / "manifest.json").write_text(json.dumps(manifest, indent=2) + "\n")
    copying = (BUNDLE / "licenses/COPYING").read_bytes()
    if digest(copying) != manifest["licenseSHA256"] or b"GNU GENERAL PUBLIC LICENSE" not in copying:
        raise ValueError("rsync COPYING file is missing or altered")
    if f'const Version = "{VERSION}"' not in (BUNDLE / "bundle_linux.go").read_text():
        raise ValueError("rsync runtime version mismatch")
    for architecture in architectures:
        check(manifest, architecture)


if __name__ == "__main__":
    main()
