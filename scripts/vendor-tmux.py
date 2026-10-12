#!/usr/bin/env python3
"""Verify embedded upstream tmux offline; --refresh fetches only pinned assets."""
import argparse
import hashlib
import io
import json
from pathlib import Path
import struct
import tarfile
import urllib.request

ROOT = Path(__file__).resolve().parents[1]
BUNDLE = ROOT / "internal/agent/terminal/tmuxbundle"


def verified(raw, spec):
    if hashlib.sha256(raw).hexdigest() != spec["sha256"]:
        raise ValueError("tmux upstream checksum mismatch: " + spec["name"])
    return raw


def fetch(manifest, spec):
    with urllib.request.urlopen(manifest["base"] + spec["name"], timeout=30) as response:
        raw = response.read(16 * 1024 * 1024 + 1)
    return verified(raw, spec)


def main():
    parser = argparse.ArgumentParser(__doc__)
    parser.add_argument("--refresh", action="store_true")
    parser.add_argument("--check", action="store_true", help="offline verification (default)")
    args = parser.parse_args()
    manifest = json.loads((BUNDLE / "manifest.json").read_text())
    for name, spec in manifest["assets"].items():
        path = BUNDLE / "assets" / name
        raw = fetch(manifest, spec) if args.refresh else verified(path.read_bytes(), spec)
        with tarfile.open(fileobj=io.BytesIO(raw), mode="r:gz") as archive:
            members = archive.getmembers()
            if len(members) != 1 or members[0].name != "tmux" or not members[0].isfile():
                raise ValueError("unexpected tmux archive entries")
            binary = archive.extractfile(members[0]).read()
        if binary[:6] != b"\x7fELF\x02\x01" or struct.unpack_from("<H", binary, 18)[0] != spec["machine"]:
            raise ValueError("incorrect ELF architecture")
        offset = struct.unpack_from("<Q", binary, 32)[0]
        entry_size, count = struct.unpack_from("<HH", binary, 54)
        if any(struct.unpack_from("<I", binary, offset + i * entry_size)[0] == 3 for i in range(count)):
            raise ValueError("tmux requires an external ELF interpreter")
        if args.refresh:
            path.write_bytes(raw)
    if args.refresh:
        raw = fetch(manifest, manifest["licenses"])
        with tarfile.open(fileobj=io.BytesIO(raw), mode="r:gz") as archive:
            for member in archive.getmembers():
                if member.isdir():
                    continue
                name = Path(member.name).name
                if not member.isfile() or name not in {"COPYING.tmux", "COPYING.ncurses", "COPYRIGHT.musl", "LICENSE.libevent", "LICENSE.utf8proc"}:
                    raise ValueError("unexpected license archive entry")
                (BUNDLE / "licenses" / name).write_bytes(archive.extractfile(member).read())
    assert ('const Version = "' + manifest["version"] + '"') in (BUNDLE / "bundle_linux.go").read_text()
    for filename in {"COPYING.tmux", "COPYING.ncurses", "COPYRIGHT.musl", "LICENSE.libevent", "LICENSE.utf8proc"}:
        assert (BUNDLE / "licenses" / filename).stat().st_size > 0
    for name in {"x/xterm-256color", "t/tmux-256color", "s/screen-256color"}:
        assert (BUNDLE / "assets/terminfo" / name).stat().st_size > 0
    print("Embedded tmux " + manifest["version"] + ": both static architectures, terminfo and notices verified")


if __name__ == "__main__":
    main()
