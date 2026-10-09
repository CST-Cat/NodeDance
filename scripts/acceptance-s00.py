#!/usr/bin/env python3
"""Run real S00 acceptance checks and save per-case evidence/status reports."""
import argparse
import datetime as dt
import http.client
import hashlib
import json
import os
import pathlib
import re
import shutil
import socket
import subprocess
import sys
import tempfile
import time
import urllib.error
import urllib.request
import uuid

ROOT = pathlib.Path(__file__).resolve().parents[1]
REGISTRY = json.loads((ROOT / "tests/registry.json").read_text())
S00_CASES = next(item["tests"] for item in REGISTRY["stages"] if item["id"] == "S00")
ARTIFACTS = ROOT / ".artifacts"
LOGS = ARTIFACTS / "logs" / "acceptance"
WORKTREES = ARTIFACTS / "worktrees"
WORKDATA = ARTIFACTS / "work-s00"
REPORT = ROOT / "reports" / "stages" / "S00.json"
STATUSES = ROOT / "reports" / "status.json"
RUN_ID = dt.datetime.now(dt.timezone.utc).strftime("%Y%m%dT%H%M%SZ") + "-" + uuid.uuid4().hex[:8]
CURRENT_ATTEMPT = 1


class NotReady(RuntimeError):
    """A required environment precondition is absent, so no PASS is claimed."""


def now():
    return dt.datetime.now(dt.timezone.utc).isoformat()


def attempt_dir():
    return LOGS / RUN_ID / f"attempt-{CURRENT_ATTEMPT}"


def work_attempt_dir():
    path = WORKDATA / RUN_ID / f"attempt-{CURRENT_ATTEMPT}"
    path.mkdir(parents=True, exist_ok=True, mode=0o700)
    os.chmod(path, 0o700)
    return path


def log_command(label, argv, *, env=None, cwd=ROOT, timeout=900):
    safe_label = "".join(ch if ch.isalnum() or ch in "-_" else "_" for ch in label)
    path = attempt_dir() / f"{safe_label}.log"
    path.parent.mkdir(parents=True, exist_ok=True)
    with path.open("w") as stream:
        stream.write("$ " + " ".join(str(part) for part in argv) + "\n")
        stream.flush()
        try:
            result = subprocess.run(argv, cwd=cwd, env=env, text=True, stdout=stream,
                                    stderr=subprocess.STDOUT, timeout=timeout)
            rc = result.returncode
        except subprocess.TimeoutExpired:
            stream.write(f"\nTIMEOUT after {timeout} seconds\n")
            rc = 124
    print(f"[{label}] {'PASS' if rc == 0 else 'FAIL'}; log={path.relative_to(ROOT)}", flush=True)
    return rc, path


def http_get(port, path):
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}), NoRedirect())
    request = urllib.request.Request(f"http://127.0.0.1:{port}{path}", method="GET")
    try:
        with opener.open(request, timeout=2) as response:
            return response.status, dict(response.headers), response.read()
    except urllib.error.HTTPError as error:
        return error.code, dict(error.headers), error.read()
    except urllib.error.URLError as error:
        raise RuntimeError(f"GET {path} on port {port} failed: {error}") from error


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, request, fp, code, msg, headers, new_url):
        return None


def wait_http(port, process, timeout=8):
    deadline = time.monotonic() + timeout
    last_error = None
    while time.monotonic() < deadline:
        if process.poll() is not None:
            raise RuntimeError(f"server exited early with code {process.returncode}")
        try:
            return http_get(port, "/api/v1/health")
        except Exception as error:  # connection refused until bind is ready
            last_error = error
            time.sleep(0.1)
    raise RuntimeError(f"server did not become ready: {last_error}")


def stop_process(process):
    if process.poll() is None:
        process.terminate()
        try:
            process.wait(timeout=5)
        except subprocess.TimeoutExpired:
            process.kill()
            process.wait(timeout=5)


def start_server(argv, env, log_path):
    log_path.parent.mkdir(parents=True, exist_ok=True)
    stream = log_path.open("w")
    stream.write("$ " + " ".join(str(part) for part in argv) + "\n")
    stream.flush()
    process = subprocess.Popen(argv, cwd=ROOT, env=env, stdout=stream, stderr=subprocess.STDOUT)
    process._nodedance_log_stream = stream
    return process


def close_server(process):
    stop_process(process)
    process._nodedance_log_stream.close()


def assert_http_bundle(port, *, expected_title=b"NodeDance", require_root=True):
    if require_root:
        status, headers, body = http_get(port, "/")
        if status != 200 or "Location" in headers:
            raise RuntimeError(f"GET / expected direct 200, got {status}, location={headers.get('Location')!r}")
        if expected_title not in body:
            raise RuntimeError(f"GET / response did not contain {expected_title!r}")
    status, _, body = http_get(port, "/api/v1/health")
    if status != 200 or b'"status":"ok"' not in body.replace(b" ", b""):
        raise RuntimeError(f"GET /api/v1/health expected 200 JSON, got {status} {body[:200]!r}")
    # S00 must not accidentally expose unauthenticated management resources.
    for path in ("/api/v1/nodes", "/api/v1/nodes/1/containers", "/ws/v1/agent", "/ws/v1/dashboard"):
        status, _, _ = http_get(port, path)
        if status != 401:
            raise RuntimeError(f"private unauthenticated route {path} must return 401, got {status}")


def case_01():
    clean = WORKTREES / RUN_ID / "clean-source"
    clean.parent.mkdir(parents=True, exist_ok=True)
    ignored = shutil.ignore_patterns(".git", ".tools", ".build", ".artifacts", "node_modules",
                                    "reports", "__pycache__", "*.pyc")
    shutil.copytree(ROOT, clean, ignore=ignored)
    generated_dirs = [path for path in clean.rglob("dist") if path.is_dir()]
    for generated_web in generated_dirs:
        shutil.rmtree(generated_web)
    forbidden = [clean / path for path in (".git", ".tools", ".build", ".artifacts", "node_modules", "web/node_modules")]
    existing = [str(path.relative_to(clean)) for path in forbidden if path.exists()]
    if existing:
        raise RuntimeError(f"clean source copy contains excluded local state: {existing}")
    retained_generated = [str(path.relative_to(clean)) for path in clean.rglob("dist") if path.is_dir()]
    if retained_generated:
        raise RuntimeError(f"clean source copy retained generated output: {retained_generated}")
    env = os.environ.copy()
    env["NODEDANCE_TOOL_ROOT"] = str(ROOT / ".tools")
    env["NODEDANCE_SKIP_BOOTSTRAP"] = "1"
    try:
        rc, path = log_command("clean-checkout-build", ["make", "build"], env=env, cwd=clean, timeout=900)
        if rc:
            raise RuntimeError(f"fresh source-copy build failed; see {path.relative_to(ROOT)}")
        for binary in ("nodedance", "nodedance-agent", "nodedance-linux-amd64", "nodedance-linux-arm64",
                       "nodedance-agent-linux-amd64", "nodedance-agent-linux-arm64"):
            if not (clean / ".build" / binary).is_file():
                raise RuntimeError(f"fresh source-copy build omitted .build/{binary}")
        return {"fresh_copy": str(clean.relative_to(ROOT)), "excluded_local_state": True,
                "all_generated_dist_dirs_removed_before_build": True,
                "build_log": str(path.relative_to(ROOT)), "binaries": 6,
                "generated_worktree_removed_after_test": True}
    finally:
        shutil.rmtree(clean.parent, ignore_errors=True)


def case_02():
    lock = json.loads((ROOT / "toolchain.lock.json").read_text())
    go = subprocess.check_output(["go", "version"], text=True).split()
    go_version = go[2] if len(go) >= 3 else ""
    node = subprocess.check_output(["node", "--version"], text=True).strip()
    pnpm_path = shutil.which("pnpm")
    if not pnpm_path:
        raise NotReady("locked pnpm executable is absent from PATH")
    pnpm = subprocess.check_output([pnpm_path, "--version"], text=True).strip()
    pnpm_binary = pathlib.Path(pnpm_path).resolve()
    with pnpm_binary.open("rb") as stream:
        if stream.read(4) != b"\x7fELF":
            raise RuntimeError(f"locked pnpm is not a directly executable native ELF: {pnpm_binary}")
    isolated_home = work_attempt_dir() / "case-02-empty-home"
    isolated_home.mkdir(parents=True, exist_ok=True)
    isolated_env = {"HOME": str(isolated_home), "XDG_DATA_HOME": str(isolated_home / ".local" / "share"),
                    "PATH": "/nonexistent"}
    isolated_pnpm = subprocess.check_output([str(pnpm_binary), "--version"], text=True, env=isolated_env).strip()
    if isolated_pnpm != lock["pnpm"]["version"]:
        raise RuntimeError(f"pnpm native executable requires Node/PATH or has wrong version: {isolated_pnpm}")
    go_toolchain = subprocess.check_output(["go", "env", "GOTOOLCHAIN"], text=True).strip()
    expected = (f"go{lock['go']['version']}", f"v{lock['node']['version']}",
                lock["pnpm"]["version"], "local")
    actual = (go_version, node, pnpm, go_toolchain)
    if actual != expected:
        raise RuntimeError(f"toolchain mismatch: expected={expected}; actual={actual}")
    rc, path = log_command("toolchain-verification", [str(ROOT / "scripts/check-tools.sh")])
    if rc:
        raise RuntimeError(f"exact toolchain/dependency verification failed; see {path.relative_to(ROOT)}")
    pnpm_sha = hashlib.sha256(pnpm_binary.read_bytes()).hexdigest()
    return {"go": go, "node": node, "pnpm": pnpm, "pnpm_native_sha256": pnpm_sha,
            "pnpm_native_elf": True, "pnpm_without_node_path": isolated_pnpm,
            "GOTOOLCHAIN": go_toolchain,
            "verification_log": str(path.relative_to(ROOT))}


def case_03():
    rc, build_log = log_command("cross-build", ["make", "build"], timeout=900)
    if rc:
        raise RuntimeError(f"cross-build failed; see {build_log.relative_to(ROOT)}")
    expected = {
        "nodedance-linux-amd64": "Advanced Micro Devices X86-64",
        "nodedance-linux-arm64": "AArch64",
        "nodedance-agent-linux-amd64": "Advanced Micro Devices X86-64",
        "nodedance-agent-linux-arm64": "AArch64",
    }
    details = {}
    metadata_path = attempt_dir() / "cross-build-metadata.log"
    metadata_path.parent.mkdir(parents=True, exist_ok=True)
    with metadata_path.open("w") as evidence:
        evidence.write(f"$ make build\nPASS; build log: {build_log.relative_to(ROOT)}\n")
    for binary, machine in expected.items():
        path = ROOT / ".build" / binary
        output = subprocess.check_output(["readelf", "-h", str(path)], text=True)
        if machine not in output:
            raise RuntimeError(f"{binary} machine header did not contain {machine!r}")
        version = subprocess.check_output(["go", "version", "-m", str(path)], text=True)
        build_settings = [line.strip() for line in version.splitlines() if line.lstrip().startswith("build")]
        if not any(re.fullmatch(r"build\s+CGO_ENABLED=0", line) for line in build_settings):
            raise RuntimeError(f"{binary} was not built with CGO_ENABLED=0")
        digest = hashlib.sha256(path.read_bytes()).hexdigest()
        with metadata_path.open("a") as evidence:
            evidence.write(f"\n$ readelf -h .build/{binary}\n{output}")
            evidence.write(f"\n$ go version -m .build/{binary}\n{version}")
            evidence.write(f"sha256 {digest}  .build/{binary}\n")
        details[binary] = {"machine": machine, "cgo_enabled": 0, "sha256": digest}
    return {"build_log": str(build_log.relative_to(ROOT)), "artifacts": details,
            "metadata_log": str(metadata_path.relative_to(ROOT)), "cgo_disabled": True}


def case_04():
    home = work_attempt_dir() / "case-04-home"
    home.mkdir(parents=True, exist_ok=True)
    env = {"HOME": str(home), "XDG_CONFIG_HOME": str(home / ".config"),
           "XDG_DATA_HOME": str(home / ".local" / "share"), "PATH": ""}
    log = attempt_dir() / "case-04-server.log"
    process = start_server([str(ROOT / ".build/nodedance")], env, log)
    try:
        wait_http(8180, process)
        assert_http_bundle(8180)
    finally:
        close_server(process)
    return {"command": "nodedance (no args)", "listen": "127.0.0.1:8180",
            "http": "root=200/no redirect; health=200; private management and websocket routes=401 unauthenticated",
            "log": str(log.relative_to(ROOT))}


def run_bound_server(argv, env, port, label, *, expect_start=True):
    log = attempt_dir() / f"{label}.log"
    process = start_server(argv, env, log)
    if expect_start:
        try:
            wait_http(port, process)
            assert_http_bundle(port)
        finally:
            close_server(process)
        return str(log.relative_to(ROOT))
    process.wait(timeout=5)
    process._nodedance_log_stream.close()
    if process.returncode == 0:
        raise RuntimeError(f"{label} should have rejected its listen address")
    return str(log.relative_to(ROOT))


def case_05():
    home = work_attempt_dir() / "case-05-home"
    config_dir = home / ".config" / "nodedance"
    config_dir.mkdir(parents=True, exist_ok=True)
    config_path = config_dir / "config.json"
    config_path.write_text(json.dumps({"listen": "127.0.0.1:18182"}))
    common = {"HOME": str(home), "XDG_CONFIG_HOME": str(home / ".config"),
              "XDG_DATA_HOME": str(home / ".local" / "share"), "PATH": os.environ["PATH"]}
    logs = []

    # CLI overrides environment and file; 18180 is the documented example.
    env = common | {"NODEDANCE_LISTEN": "127.0.0.1:18181", "NODEDANCE_CONFIG": str(config_path)}
    logs.append(run_bound_server([str(ROOT / ".build/nodedance"), "serve", "--listen", "127.0.0.1:18180"], env, 18180, "case-05-cli"))
    # Environment overrides file.
    env = common | {"NODEDANCE_LISTEN": "127.0.0.1:18181", "NODEDANCE_CONFIG": str(config_path)}
    logs.append(run_bound_server([str(ROOT / ".build/nodedance")], env, 18181, "case-05-env"))
    # File is selected when neither CLI nor environment provides a value.
    env = common | {"NODEDANCE_CONFIG": str(config_path)}
    logs.append(run_bound_server([str(ROOT / ".build/nodedance")], env, 18182, "case-05-config"))
    # Default is selected when all sources are absent (also tested by S00-04).
    default_home = work_attempt_dir() / "case-05-default-home"
    default_home.mkdir(parents=True, exist_ok=True)
    env = {"HOME": str(default_home), "XDG_CONFIG_HOME": str(default_home / ".config"),
           "XDG_DATA_HOME": str(default_home / ".local" / "share"), "PATH": os.environ["PATH"]}
    logs.append(run_bound_server([str(ROOT / ".build/nodedance")], env, 8180, "case-05-default"))
    # --dev permits loopback and rejects a wildcard/public bind.
    env = common.copy()
    logs.append(run_bound_server([str(ROOT / ".build/nodedance"), "serve", "--dev", "--listen", "127.0.0.1:18183"], env, 18183, "case-05-dev-loopback"))
    rejected_log = run_bound_server([str(ROOT / ".build/nodedance"), "serve", "--dev", "--listen", "0.0.0.0:18184"], env, 18184, "case-05-dev-public-rejected", expect_start=False)
    logs.append(rejected_log)
    return {"sources_verified": ["CLI > NODEDANCE_LISTEN > config JSON > default"],
            "custom_port": "127.0.0.1:18180", "web_api_ws_same_listener": True,
            "dev_loopback": "accepted", "dev_non_loopback": "rejected", "logs": logs}


def case_06():
    holder_code = r'''import http.server
class Handler(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        body=b"NodeDance-8180-holder\n"
        self.send_response(200); self.send_header("Content-Length", str(len(body))); self.end_headers(); self.wfile.write(body)
    def log_message(self,*args): pass
http.server.HTTPServer(("127.0.0.1",8180),Handler).serve_forever()
'''
    log = attempt_dir() / "case-06-core.log"
    holder_log = attempt_dir() / "case-06-holder.log"
    holder_log.parent.mkdir(parents=True, exist_ok=True)
    holder_stream = holder_log.open("w")
    holder = subprocess.Popen([sys.executable, "-c", holder_code], cwd=ROOT,
                              stdout=holder_stream, stderr=subprocess.STDOUT)
    try:
        deadline = time.monotonic() + 5
        while True:
            if holder.poll() is not None:
                raise NotReady("127.0.0.1:8180 was already occupied; cannot safely create the specified sentinel holder")
            try:
                status, _, body = http_get(8180, "/sentinel")
                if status == 200 and body == b"NodeDance-8180-holder\n":
                    break
            except Exception:
                if time.monotonic() >= deadline:
                    raise NotReady("could not bind/verify the isolated 8180 sentinel holder")
                time.sleep(0.05)
        core_env = os.environ.copy()
        core_home = work_attempt_dir() / "case-06-home"
        core_env["HOME"] = str(core_home)
        core_env["XDG_CONFIG_HOME"] = str(pathlib.Path(core_env["HOME"]) / ".config")
        core_env["XDG_DATA_HOME"] = str(pathlib.Path(core_env["HOME"]) / ".local" / "share")
        for key in ("NODEDANCE_CONFIG", "NODEDANCE_DATA_DIR", "NODEDANCE_LISTEN",
                    "NODEDANCE_PUBLIC_ORIGIN", "NODEDANCE_TRUSTED_PROXIES"):
            core_env.pop(key, None)
        pathlib.Path(core_env["HOME"]).mkdir(parents=True, exist_ok=True)
        process = start_server([str(ROOT / ".build/nodedance")], core_env, log)
        code = process.wait(timeout=5)
        process._nodedance_log_stream.close()
        output = log.read_text(errors="replace")
        if code == 0 or "address already in use" not in output.lower():
            raise RuntimeError(f"Core must exit with explicit port-conflict error; exit={code}")
        if "NodeDance listening on" in output:
            raise RuntimeError("Core emitted success/listening log before bind succeeded")
        status, _, body = http_get(8180, "/sentinel")
        if status != 200 or body != b"NodeDance-8180-holder\n":
            raise RuntimeError("Core port-conflict handling disturbed the existing holder")
    finally:
        stop_process(holder)
        holder_stream.close()
    return {"holder": "still served sentinel after Core exit", "core_exit": code,
            "no_success_listening_log": True, "core_log": str(log.relative_to(ROOT)),
            "holder_log": str(holder_log.relative_to(ROOT))}


def case_07():
    home = work_attempt_dir() / "case-07-home"
    empty_path = attempt_dir() / "empty-path"
    home.mkdir(parents=True, exist_ok=True)
    empty_path.mkdir(parents=True, exist_ok=True)
    log = attempt_dir() / "case-07-server.log"
    env = {"HOME": str(home), "XDG_CONFIG_HOME": str(home / ".config"),
           "XDG_DATA_HOME": str(home / ".local" / "share"), "PATH": str(empty_path)}
    process = start_server([str(ROOT / ".build/nodedance")], env, log)
    try:
        wait_http(8180, process)
        assert_http_bundle(8180)
    finally:
        close_server(process)
    return {"runtime_path": str(empty_path.relative_to(ROOT)), "node_executable_available": False,
            "root_and_health": "HTTP 200 without redirect", "log": str(log.relative_to(ROOT))}


def case_08():
    engine = os.environ.get("NODEDANCE_TEST_ENGINE", "29")
    run_id = "s00-" + RUN_ID.lower().replace("t", "-").replace("z", "")[:24]
    env = os.environ.copy()
    env["NODEDANCE_TEST_ENGINE"] = engine
    rc, path = log_command("fixture-selftest", [sys.executable, "scripts/test/fixtures-selftest.py", run_id], env=env, timeout=900)
    if rc:
        raise RuntimeError(f"isolated Docker fixture test failed; see {path.relative_to(ROOT)}")
    fixture_output = path.read_text(errors="replace")
    version_line = next((line.removeprefix("TEST ENVIRONMENT JSON: ")
                         for line in fixture_output.splitlines()
                         if line.startswith("TEST ENVIRONMENT JSON: ")), None)
    if not version_line:
        raise RuntimeError("fixture self-test passed without recording actual Docker/Compose version evidence")
    versions = json.loads(version_line)
    cleanup_rc, cleanup_path = log_command("fixture-dind-cleanup", [str(ROOT / "scripts/test/dind.sh"), "clean", engine, "--purge-data"], env=env, timeout=300)
    if cleanup_rc:
        raise RuntimeError(f"owned nested daemon cleanup failed; see {cleanup_path.relative_to(ROOT)}")
    return {"engine": engine, "run_id": run_id, "versions": versions, "fixture_log": str(path.relative_to(ROOT)),
            "cleanup_log": str(cleanup_path.relative_to(ROOT)),
            "fixture_controls": "create, health failure/recovery, daemon outage/recovery, bind/named-volume/Redis marker verification, exact-run cleanup, suite sentinel preservation, host container before/after equality"}


CASES = {
    "S00-01": case_01,
    "S00-02": case_02,
    "S00-03": case_03,
    "S00-04": case_04,
    "S00-05": case_05,
    "S00-06": case_06,
    "S00-07": case_07,
    "S00-08": case_08,
}


def write_reports(report):
    REPORT.parent.mkdir(parents=True, exist_ok=True)
    REPORT.write_text(json.dumps(report, ensure_ascii=False, indent=2) + "\n")
    current = json.loads(STATUSES.read_text()) if STATUSES.exists() else {"schema": 1, "stages": {}}
    current.setdefault("stages", {})["S00"] = {
        "status": report["status"], "mode": report["mode"], "run_id": report["run_id"],
        "updated_at": report["updated_at"], "reason": report.get("reason", ""),
    }
    current["updated_at"] = report["updated_at"]
    STATUSES.parent.mkdir(parents=True, exist_ok=True)
    STATUSES.write_text(json.dumps(current, ensure_ascii=False, indent=2) + "\n")


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--mode", choices=("full", "integration", "e2e"), required=True)
    parser.add_argument("--repeat", type=int, default=1)
    args = parser.parse_args()
    if args.repeat != 1:
        parser.error("acceptance runs once; rerun the affected suite after a failure or code change")

    selected = list(CASES)
    if args.mode == "integration":
        selected = ["S00-03", "S00-08"]
    elif args.mode == "e2e":
        selected = ["S00-04", "S00-05", "S00-06", "S00-07"]

    report = {
        "schema": 1, "stage": "S00", "mode": args.mode, "run_id": RUN_ID,
        "updated_at": now(), "status": "NOT_READY", "repeat_required": 1,
        "repeat_requested": args.repeat, "verification_status": "NOT_RUN", "tests": {},
        "evidence_root": f".artifacts/logs/acceptance/{RUN_ID}",
    }
    for case in S00_CASES:
        report["tests"][case["id"]] = {
            "status": "NOT_READY", "action": case["action"], "expected": case["expected"],
            "environment": case["environment"], "evidence_required": case["evidence"],
            "runs": [], "reason": "Not executed in this mode; a prior report cannot substitute for current execution.",
        }

    all_run_results = []
    for repeat_index in range(1, args.repeat + 1):
        global CURRENT_ATTEMPT
        CURRENT_ATTEMPT = repeat_index
        work_attempt_dir()
        print(f"S00 acceptance run {repeat_index}/{args.repeat} ({args.mode})", flush=True)
        this_run = {}
        for test_id in selected:
            started = time.monotonic()
            try:
                detail = CASES[test_id]()
                status, reason = "PASS", ""
            except NotReady as error:
                detail, status, reason = {}, "NOT_READY", str(error)
            except Exception as error:
                detail, status, reason = {}, "FAIL", f"{type(error).__name__}: {error}"
            duration = round(time.monotonic() - started, 3)
            test_record = report["tests"][test_id]
            test_record["runs"].append({"attempt": repeat_index, "status": status,
                                        "duration_seconds": duration, "details": detail,
                                        "reason": reason})
            this_run[test_id] = status
            if status == "FAIL":
                test_record["status"] = "FAIL"
                test_record["reason"] = reason
            elif status == "NOT_READY" and test_record["status"] != "FAIL":
                test_record["status"] = "NOT_READY"
                test_record["reason"] = reason
            print(f"{test_id}: {status} ({duration:.3f}s){': ' + reason if reason else ''}", flush=True)
        all_run_results.append(this_run)

    for test_id in selected:
        record = report["tests"][test_id]
        statuses = [attempt["status"] for attempt in record["runs"]]
        if statuses and all(item == "PASS" for item in statuses) and args.mode == "full":
            record["status"] = "PASS"
            record["reason"] = "The current complete full acceptance run passed."
        elif "FAIL" in statuses:
            record["status"] = "FAIL"
        elif any(item == "NOT_READY" for item in statuses):
            record["status"] = "NOT_READY"
        else:
            record["status"] = "NOT_READY"
            record["reason"] = "This mode does not execute the complete full-stage acceptance set."

    statuses = [report["tests"][test_id]["status"] for test_id in CASES]
    selected_attempts = [attempt["status"] for test_id in selected
                         for attempt in report["tests"][test_id]["runs"]]
    report["verification_status"] = "PASS" if selected_attempts and all(item == "PASS" for item in selected_attempts) else "FAIL"
    if args.mode == "full" and all(status == "PASS" for status in statuses):
        report["status"] = "PASS"
        report["reason"] = "All eight original S00 acceptance cases passed in this complete full run."
    elif "FAIL" in statuses:
        report["status"] = "FAIL"
        report["reason"] = "At least one current S00 required acceptance case failed."
    else:
        report["status"] = "NOT_READY"
        report["reason"] = "Required S00 cases were not run or one or more cases remain incomplete."
    report["updated_at"] = now()
    write_reports(report)
    print(f"S00 {report['status']}: report={REPORT.relative_to(ROOT)} evidence={report['evidence_root']}", flush=True)
    return 0 if report["verification_status"] == "PASS" else 1


if __name__ == "__main__":
    raise SystemExit(main())
