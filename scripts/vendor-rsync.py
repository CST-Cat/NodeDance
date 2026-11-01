#!/usr/bin/env python3
"""Verify embedded trimmed rsync assets offline against the bundle manifest.

Assets are built from the pinned upstream source by scripts/build-rsync.sh;
this script only verifies what is committed (checksums, archive shape, ELF
architecture, static linkage) and the COPYING file.
"""
import gzip
import hashlib
import io
import json
from pathlib import Path
import struct
import tarfile

ROOT = Path(__file__).resolve().parents[1]
BUNDLE = ROOT / "internal/transfer/rsyncbundle"


def main():
    manifest = json.loads((BUNDLE / "manifest.json").read_text())
    for name, spec in manifest["assets"].items():
        raw = (BUNDLE / "assets" / name).read_bytes()
        if hashlib.sha256(raw).hexdigest() != spec["sha256"]:
            raise ValueError("rsync asset checksum mismatch: " + name)
        with tarfile.open(fileobj=io.BytesIO(raw), mode="r:gz") as archive:
            members = archive.getmembers()
            if len(members) != 1 or members[0].name != "rsync" or not members[0].isfile():
                raise ValueError("unexpected rsync archive entries: " + name)
            binary = archive.extractfile(members[0]).read()
        if binary[:6] != b"\x7fELF\x02\x01" or struct.unpack_from("<H", binary, 18)[0] != spec["machine"]:
            raise ValueError("incorrect ELF architecture: " + name)
        offset = struct.unpack_from("<Q", binary, 32)[0]
        entry_size, count = struct.unpack_from("<HH", binary, 54)
        if any(struct.unpack_from("<I", binary, offset + i * entry_size)[0] == 3 for i in range(count)):
            raise ValueError("rsync requires an external ELF interpreter: " + name)
    copying = (BUNDLE / "licenses" / "COPYING").read_text()
    if "GNU GENERAL PUBLIC LICENSE" not in copying:
        raise ValueError("rsync COPYING file is missing or unexpected")
    assert ('const Version = "' + manifest["version"] + '"') in (BUNDLE / "bundle_linux.go").read_text()
    print("rsync bundle verified:", manifest["version"])


if __name__ == "__main__":
    main()
