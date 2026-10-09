#!/usr/bin/env python3
"""Redact and verify the explicit S05 evidence upload allowlist."""

from __future__ import annotations

import hashlib
import json
import pathlib
import re
import stat
import sys
import tempfile
from typing import Any


ROOT = pathlib.Path(__file__).resolve().parents[2]
REDACTION_REPORT = ROOT / "reports/evidence-s05-redaction.json"
TEXT_SUFFIXES = {".json", ".jsonl", ".log", ".txt"}
SECRET_KEYS = re.compile(r"(?i)(?:password|passwd|token|secret|api[_-]?key|private[_-]?key|credential)")
TEXT_PATTERNS = [
    re.compile(r"(?i)(authorization\s*[:=]\s*(?:bearer|basic)\s+)([^\s,;\"']+)"),
    re.compile(r"(?i)((?:password|passwd|token|secret|api[_-]?key|private[_-]?key|credential)\s*[:=]\s*)(\"[^\"]*\"|'[^']*'|[^\s,;]+)"),
    re.compile(r"(?s)(-----BEGIN (?:RSA |EC |OPENSSH )?PRIVATE KEY-----).*?(-----END (?:RSA |EC |OPENSSH )?PRIVATE KEY-----)"),
    re.compile(r"(://[^:/\s]+:)([^@/\s]+)(@)"),
]
UNREDACTED_PATTERNS = [
    re.compile(r"(?i)authorization\s*[:=]\s*(?:bearer|basic)\s+(?!\[REDACTED\])[^\s,;\"']+"),
    re.compile(r"(?i)(?:password|passwd|token|secret|api[_-]?key|private[_-]?key|credential)\s*[:=]\s*(?!\[REDACTED\])(?:\"[^\"]*\"|'[^']*'|[^\s,;]+)"),
    re.compile(r"(?s)-----BEGIN (?:RSA |EC |OPENSSH )?PRIVATE KEY-----.*?-----END (?:RSA |EC |OPENSSH )?PRIVATE KEY-----"),
    re.compile(r"://[^:/\s]+:(?!\[REDACTED\])[^@/\s]+@"),
]


def redact_object(value: Any) -> tuple[Any, int]:
    count = 0
    if isinstance(value, dict):
        cleaned = {}
        for key, item in value.items():
            if isinstance(key, str) and SECRET_KEYS.search(key):
                cleaned[key] = "[REDACTED]"
                count += 1
            else:
                cleaned[key], nested = redact_object(item)
                count += nested
        return cleaned, count
    if isinstance(value, list):
        cleaned = []
        for item in value:
            safe, nested = redact_object(item)
            cleaned.append(safe)
            count += nested
        return cleaned, count
    if isinstance(value, str):
        current = value
        for pattern in TEXT_PATTERNS:
            if pattern.groups == 2 and "PRIVATE KEY" in pattern.pattern:
                current, replacements = pattern.subn(r"\1[REDACTED]\2", current)
            elif pattern.groups == 3 and pattern.pattern.startswith("(://"):
                current, replacements = pattern.subn(r"\1[REDACTED]\3", current)
            elif pattern.groups >= 2:
                current, replacements = pattern.subn(r"\1[REDACTED]", current)
            else:
                current, replacements = pattern.subn("[REDACTED]", current)
            count += replacements
        return current, count
    return value, count


def redact_text(text: str) -> tuple[str, int]:
    current = text
    count = 0
    for pattern in TEXT_PATTERNS:
        if "PRIVATE KEY" in pattern.pattern:
            current, replacements = pattern.subn(r"\1[REDACTED]\2", current)
        elif pattern.pattern.startswith("(://"):
            current, replacements = pattern.subn(r"\1[REDACTED]\3", current)
        else:
            current, replacements = pattern.subn(r"\1[REDACTED]", current)
        count += replacements
    return current, count


def has_secret(text: str) -> bool:
    return any(pattern.search(text) for pattern in UNREDACTED_PATTERNS)


def allowed_paths() -> list[pathlib.Path]:
    return [
        ROOT / "reports/stages/S05.json",
        ROOT / "reports/status.json",
        ROOT / "reports/evidence-s05-redaction.json",
        ROOT / ".artifacts/logs/acceptance-s05",
        ROOT / ".artifacts/work-s05",
    ]


def files_under(path: pathlib.Path):
    if path.is_symlink():
        raise RuntimeError(f"refusing evidence symlink: {path.relative_to(ROOT)}")
    if not path.exists():
        return
    metadata = path.lstat()
    if stat.S_ISREG(metadata.st_mode):
        if path.suffix.lower() not in TEXT_SUFFIXES:
            raise RuntimeError(f"unsupported evidence file suffix: {path.relative_to(ROOT)}")
        yield path
        return
    if not stat.S_ISDIR(metadata.st_mode):
        raise RuntimeError(f"refusing non-directory evidence root: {path.relative_to(ROOT)}")
    for candidate in sorted(path.rglob("*")):
        if candidate.is_symlink():
            raise RuntimeError(f"refusing evidence symlink: {candidate.relative_to(ROOT)}")
        metadata = candidate.lstat()
        if stat.S_ISDIR(metadata.st_mode):
            continue
        if not stat.S_ISREG(metadata.st_mode):
            raise RuntimeError(f"refusing non-regular evidence file: {candidate.relative_to(ROOT)}")
        if candidate.suffix.lower() not in TEXT_SUFFIXES:
            raise RuntimeError(f"unsupported evidence file suffix: {candidate.relative_to(ROOT)}")
        if stat.S_ISREG(metadata.st_mode):
            yield candidate


def sanitize_and_verify() -> dict[str, Any]:
    required = [ROOT / "reports/stages/S05.json", ROOT / "reports/status.json"]
    missing = [path.relative_to(ROOT).as_posix() for path in required if not path.is_file()]
    if missing:
        raise RuntimeError("required report missing: " + ", ".join(missing))
    total_redactions = 0
    entries = []
    seen = set()
    for root in allowed_paths()[:2] + allowed_paths()[3:]:
        for path in files_under(root):
            relative = path.relative_to(ROOT).as_posix()
            if relative in seen or path == REDACTION_REPORT:
                continue
            seen.add(relative)
            original = read_text_file(path)
            if path.suffix.lower() == ".json":
                try:
                    decoded = json.loads(original)
                    safe, redactions = redact_object(decoded)
                    text = json.dumps(safe, ensure_ascii=False, indent=2) + "\n"
                except json.JSONDecodeError:
                    text, redactions = redact_text(original)
            else:
                text, redactions = redact_text(original)
            path.write_text(text, encoding="utf-8")
            if has_secret(text):
                raise RuntimeError(f"unredacted secret pattern remains in {relative}")
            total_redactions += redactions
            entries.append({"path": relative, "sha256": hashlib.sha256(path.read_bytes()).hexdigest(),
                            "redactions": redactions})
    report = {
        "schema": 1,
        "status": "PASS",
        "allowlist": [path.relative_to(ROOT).as_posix() for path in allowed_paths()],
        "files_scanned": len(entries),
        "redactions": total_redactions,
        "files": entries,
    }
    serialized = json.dumps(report, ensure_ascii=False, indent=2) + "\n"
    if has_secret(serialized):
        raise RuntimeError("redaction report contains an unredacted secret pattern")
    REDACTION_REPORT.write_text(serialized, encoding="utf-8")
    return report


def self_test() -> None:
    samples = [
        ('Authorization: Bearer abc.def.ghi', "Authorization: Bearer [REDACTED]"),
        ('password="value with spaces"', 'password=[REDACTED]'),
        ('https://alice:swordfish@example.test/path', 'https://alice:[REDACTED]@example.test/path'),
    ]
    for source, expected in samples:
        safe, _ = redact_text(source)
        if safe != expected or has_secret(safe):
            raise AssertionError(f"sanitizer sample failed: {safe!r}")
    safe, count = redact_object({"token": "secret-value", "message": "api_key=secret-value"})
    if count != 2 or safe["token"] != "[REDACTED]" or has_secret(safe["message"]):
        raise AssertionError("structured sanitizer sample failed")
    with tempfile.TemporaryDirectory(prefix="s05-sanitizer-selftest-", dir=ROOT) as temp_root:
        root = pathlib.Path(temp_root)
        unsupported = root / "secret.sqlite"
        unsupported.write_bytes(b"sqlite")
        try:
            list(files_under(unsupported))
        except RuntimeError as error:
            if "unsupported evidence file suffix" not in str(error):
                raise
        else:
            raise AssertionError("unknown upload suffix was silently skipped")
        binary = root / "binary.log"
        binary.write_bytes(b"\xff\x00\xfe")
        try:
            read_text_file(binary)
        except RuntimeError as error:
            if "binary" not in str(error):
                raise
        else:
            raise AssertionError("binary file with a log suffix was accepted")


def read_text_file(path: pathlib.Path) -> str:
    try:
        text = path.read_bytes().decode("utf-8")
    except UnicodeDecodeError as error:
        raise RuntimeError(f"binary or non-UTF-8 evidence is not uploadable: {path.relative_to(ROOT)}") from error
    if "\x00" in text:
        raise RuntimeError(f"binary evidence is not uploadable: {path.relative_to(ROOT)}")
    return text


def main() -> int:
    try:
        self_test()
        report = sanitize_and_verify()
    except (OSError, RuntimeError, AssertionError, json.JSONDecodeError) as error:
        print(f"S05 evidence sanitization FAIL: {error}", file=sys.stderr)
        return 1
    print(f"S05 evidence sanitization PASS: {report['files_scanned']} files scanned, {report['redactions']} redactions")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
