#!/usr/bin/env python3
"""Run S01 against real Core processes and SQLite, recording each original case."""
import argparse
import base64
import datetime as dt
import hashlib
import http.cookiejar
import json
import os
import pathlib
import pwd
import re
import shutil
import signal
import socket
import stat
import sqlite3
import subprocess
import sys
import tempfile
import time
import urllib.error
import urllib.request
import uuid

ROOT = pathlib.Path(__file__).resolve().parents[1]
REGISTRY = json.loads((ROOT / "tests/registry.json").read_text())
S01_CASES = next(item["tests"] for item in REGISTRY["stages"] if item["id"] == "S01")
REPORT = ROOT / "reports/stages/S01.json"
STATUSES = ROOT / "reports/status.json"
ARTIFACTS = ROOT / ".artifacts/logs/acceptance-s01"
WORK_ROOT = ROOT / ".artifacts/work-s01"
RUN_ID = dt.datetime.now(dt.timezone.utc).strftime("%Y%m%dT%H%M%SZ") + "-" + uuid.uuid4().hex[:8]
CURRENT_ATTEMPT = 1
ATTEMPT_DIR = ARTIFACTS / RUN_ID / "attempt-1"
WORK_DIR = WORK_ROOT / RUN_ID / "attempt-1"
HTTP_LOG = None
SENSITIVE_VALUES = set()
ALLOWED_SETUP_CREDENTIALS = {}


class NotReady(RuntimeError):
    """The required real environment is unavailable; no PASS may be claimed."""

    def __init__(self, message, detail=None):
        super().__init__(message)
        self.detail = detail or {}


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, request, fp, code, msg, headers, new_url):
        return None


def timestamp():
    return dt.datetime.now(dt.timezone.utc).isoformat()


def log_http(method, path, status, headers, body):
    if HTTP_LOG is None:
        return
    record = {
        "method": method,
        "path": path,
        "status": status,
        "cache_control": headers.get("Cache-Control", ""),
        "content_type": headers.get("Content-Type", ""),
        "body_bytes": len(body),
    }
    with HTTP_LOG.open("a") as stream:
        stream.write(json.dumps(record, ensure_ascii=False) + "\n")


def register_sensitive(value):
    if value is None:
        return
    encoded = value if isinstance(value, bytes) else str(value).encode("utf-8")
    if len(encoded) >= 8:
        SENSITIVE_VALUES.add(encoded)


class Client:
    def __init__(self, core):
        self.core = core
        self.jar = http.cookiejar.CookieJar()
        self.opener = urllib.request.build_opener(
            urllib.request.ProxyHandler({}),
            urllib.request.HTTPCookieProcessor(self.jar),
            NoRedirect(),
        )

    def cookie(self, name):
        value = next((item.value for item in self.jar if item.name == name), "")
        if name in {"nodedance_session", "nodedance_csrf"}:
            register_sensitive(value)
        return value

    def request(self, method, path, payload=None, *, origin=None, csrf=None,
                manual_cookie=None, extra_headers=None, timeout=8):
        data = None if payload is None else json.dumps(payload, ensure_ascii=False).encode()
        if isinstance(payload, dict):
            for key, value in payload.items():
                if any(part in key.lower() for part in ("password", "credential", "token", "csrf")):
                    register_sensitive(value)
        headers = {"Accept": "application/json, text/html;q=0.8", "User-Agent": "NodeDance-S01-acceptance"}
        if data is not None:
            headers["Content-Type"] = "application/json"
        if origin is not None:
            headers["Origin"] = origin
        if csrf is not None:
            headers["X-CSRF-Token"] = csrf
            register_sensitive(csrf)
        if manual_cookie is not None:
            headers["Cookie"] = manual_cookie
            for item in manual_cookie.split(";"):
                if "=" in item:
                    name, value = item.strip().split("=", 1)
                    if name in {"nodedance_session", "nodedance_csrf"}:
                        register_sensitive(value)
        if extra_headers:
            headers.update(extra_headers)
        request = urllib.request.Request(self.core.base_url + path, data=data, headers=headers, method=method)
        try:
            with self.opener.open(request, timeout=timeout) as response:
                status, response_headers, body = response.status, response.headers, response.read()
        except urllib.error.HTTPError as error:
            status, response_headers, body = error.code, error.headers, error.read()
        log_http(method, path, status, response_headers, body)
        return status, response_headers, body

    def csrf(self):
        status, headers, body = self.request("GET", "/api/v1/auth/csrf")
        if status != 200:
            raise RuntimeError(f"GET csrf challenge returned {status}")
        return json.loads(body)["token"]


class Core:
    def __init__(self, label, config=None, *, data_dir=None, production=False):
        self.label = label
        self.port = free_port()
        self.origin = ("https" if production else "http") + f"://127.0.0.1:{self.port}"
        self.base_url = f"http://127.0.0.1:{self.port}"
        self.data_dir = pathlib.Path(data_dir) if data_dir else WORK_DIR / f"{label}-data"
        self.data_dir.mkdir(parents=True, exist_ok=True, mode=0o700)
        os.chmod(self.data_dir, 0o700)
        credential_path = (self.data_dir / "setup-credential.txt").resolve()
        try:
            credential_path.relative_to((WORK_ROOT / RUN_ID).resolve())
            ALLOWED_SETUP_CREDENTIALS[credential_path] = self.data_dir.resolve()
        except ValueError:
            pass
        self.config_path = WORK_DIR / f"{label}-config.json"
        self.config_path.write_text(json.dumps(config or {}, ensure_ascii=False) + "\n")
        self.log_path = ATTEMPT_DIR / f"{label}-core.log"
        self.stream = None
        self.process = None
        self.production = production

    def command(self):
        command = [
            str(ROOT / ".build/nodedance"), "serve", "--listen", f"127.0.0.1:{self.port}",
            "--data-dir", str(self.data_dir), "--public-origin", self.origin,
            "--config", str(self.config_path),
        ]
        if not self.production:
            command.append("--dev")
        return command

    def start(self):
        binary = ROOT / ".build/nodedance"
        if not binary.is_file():
            raise NotReady(".build/nodedance is missing; run make build before S01 acceptance")
        self.log_path.parent.mkdir(parents=True, exist_ok=True)
        self.stream = self.log_path.open("w")
        self.stream.write("$ " + " ".join(self.command()) + "\n")
        self.stream.flush()
        environment = clean_environment()
        self.process = subprocess.Popen(self.command(), cwd=ROOT, env=environment,
                                        stdout=self.stream, stderr=subprocess.STDOUT)
        deadline = time.monotonic() + 10
        last_error = None
        try:
            while time.monotonic() < deadline:
                if self.process.poll() is not None:
                    raise RuntimeError(f"Core exited during startup; see {self.log_path.relative_to(ROOT)}")
                try:
                    status, _, _ = Client(self).request("GET", "/api/v1/health", timeout=1)
                    if status == 200:
                        return self
                except Exception as error:
                    last_error = error
                time.sleep(0.05)
            raise RuntimeError(f"Core did not become ready: {last_error}; see {self.log_path.relative_to(ROOT)}")
        except Exception:
            self.stop()
            raise

    def stop(self):
        if self.process is not None and self.process.poll() is None:
            self.process.send_signal(signal.SIGTERM)
            try:
                self.process.wait(timeout=8)
            except subprocess.TimeoutExpired:
                self.process.kill()
                self.process.wait(timeout=5)
        if self.stream is not None:
            self.stream.close()

    def __enter__(self):
        return self.start()

    def __exit__(self, exc_type, exc, traceback):
        self.stop()


def clean_environment():
    environment = os.environ.copy()
    for key in ("NODEDANCE_CONFIG", "NODEDANCE_DATA_DIR", "NODEDANCE_LISTEN",
                "NODEDANCE_PUBLIC_ORIGIN", "NODEDANCE_TRUSTED_PROXIES"):
        environment.pop(key, None)
    environment["HOME"] = str(WORK_DIR / "empty-home")
    pathlib.Path(environment["HOME"]).mkdir(parents=True, exist_ok=True)
    environment["XDG_CONFIG_HOME"] = str(pathlib.Path(environment["HOME"]) / ".config")
    environment["XDG_DATA_HOME"] = str(pathlib.Path(environment["HOME"]) / ".local" / "share")
    return environment


def free_port():
    with socket.socket() as listener:
        listener.bind(("127.0.0.1", 0))
        return listener.getsockname()[1]


def db_query(data_dir, sql, parameters=()):
    path = pathlib.Path(data_dir) / "nodedance.sqlite"
    connection = sqlite3.connect(f"file:{path}?mode=ro", uri=True, timeout=5)
    try:
        return connection.execute(sql, parameters).fetchall()
    finally:
        connection.close()


def setup(core, client=None, password="S01-start-password-123", display_name="NodeDance Admin"):
    client = client or Client(core)
    credential_path = core.data_dir / "setup-credential.txt"
    credential = credential_path.read_text().strip()
    register_sensitive(credential)
    register_sensitive(password)
    if credential_path.stat().st_mode & 0o777 != 0o600:
        raise RuntimeError("initial setup credential file is not mode 0600")
    csrf = client.csrf()
    status, _, body = client.request("POST", "/api/v1/auth/setup", {
        "credential": credential, "password": password, "displayName": display_name,
    }, origin=core.origin, csrf=csrf)
    if status != 201:
        raise RuntimeError(f"valid first-run setup returned {status}")
    if credential_path.exists():
        raise RuntimeError("consumed setup credential file was not removed")
    if db_query(core.data_dir, "SELECT count(*) FROM init_credential")[0][0] != 0:
        raise RuntimeError("consumed setup credential digest remains in SQLite")
    response = json.loads(body)
    if not response.get("csrfToken") or not response.get("user", {}).get("displayName"):
        raise RuntimeError("setup response omitted the user or session-bound CSRF token")
    return client, password, credential


def login(core, client, password, *, origin=None):
    register_sensitive(password)
    csrf = client.csrf()
    return client.request("POST", "/api/v1/auth/login", {"password": password},
                          origin=origin or core.origin, csrf=csrf)


def session_token(client):
    return client.cookie("nodedance_session")


def websocket_handshake(core, session, origin=None, *, timeout=5):
    connection = socket.create_connection(("127.0.0.1", core.port), timeout=timeout)
    connection.settimeout(timeout)
    key = base64.b64encode(os.urandom(16)).decode("ascii")
    request = (
        f"GET /ws/v1/dashboard HTTP/1.1\r\nHost: 127.0.0.1:{core.port}\r\n"
        "Upgrade: websocket\r\nConnection: Upgrade\r\n"
        f"Sec-WebSocket-Key: {key}\r\nSec-WebSocket-Version: 13\r\n"
        f"Origin: {origin or core.origin}\r\nCookie: nodedance_session={session}\r\n\r\n"
    ).encode("ascii")
    connection.sendall(request)
    response = bytearray()
    while b"\r\n\r\n" not in response and len(response) < 65536:
        chunk = connection.recv(4096)
        if not chunk:
            break
        response.extend(chunk)
    first = bytes(response).split(b"\r\n", 1)[0].decode("latin1")
    match = re.search(r"\s(\d{3})\s", first)
    if not match:
        connection.close()
        raise RuntimeError(f"invalid WebSocket handshake response: {first}")
    return connection, int(match.group(1)), first


def send_ws_ping(connection):
    payload = os.urandom(4)
    mask = os.urandom(4)
    masked = bytes(value ^ mask[index % 4] for index, value in enumerate(payload))
    connection.sendall(bytes((0x89, 0x80 | len(payload))) + mask + masked)


def recv_exact(connection, count):
    chunks = bytearray()
    while len(chunks) < count:
        chunk = connection.recv(count - len(chunks))
        if not chunk:
            raise EOFError("WebSocket peer closed the TCP connection")
        chunks.extend(chunk)
    return bytes(chunks)


def recv_ws_frame(connection):
    first, second = recv_exact(connection, 2)
    size = second & 0x7f
    if size == 126:
        size = int.from_bytes(recv_exact(connection, 2), "big")
    elif size == 127:
        size = int.from_bytes(recv_exact(connection, 8), "big")
    mask = recv_exact(connection, 4) if second & 0x80 else b""
    payload = recv_exact(connection, size) if size else b""
    if mask:
        payload = bytes(value ^ mask[index % 4] for index, value in enumerate(payload))
    return first & 0x0f, payload


def wait_websocket_close(connection, timeout):
    started = time.monotonic()
    deadline = started + timeout
    next_ping = 0.0
    close_payload = b""
    ping_count = 0
    pong_count = 0
    while time.monotonic() < deadline:
        now = time.monotonic()
        if now >= next_ping:
            try:
                send_ws_ping(connection)
                ping_count += 1
            except OSError:
                return round(time.monotonic() - started, 3), "tcp-closed", close_payload, ping_count, pong_count
            next_ping = now + 0.2
        connection.settimeout(min(0.25, max(0.01, deadline - now)))
        try:
            opcode, payload = recv_ws_frame(connection)
        except socket.timeout:
            continue
        except (EOFError, OSError):
            return round(time.monotonic() - started, 3), "tcp-closed", close_payload, ping_count, pong_count
        if opcode == 8:
            close_payload = payload
            return round(time.monotonic() - started, 3), "close-frame", close_payload, ping_count, pong_count
        if opcode == 10:
            pong_count += 1
    raise RuntimeError(f"WebSocket remained open for {timeout} seconds")


def case_01():
    with Core("s01-01") as core:
        client = Client(core)
        status, _, status_body = client.request("GET", "/api/v1/auth/setup/status")
        public_status = json.loads(status_body)
        if status != 200 or public_status.get("initialized") is not False or set(public_status) != {"initialized", "setupHint"}:
            raise RuntimeError("public setup status did not return only initialization state and a generic hint")
        if str(core.data_dir) in status_body.decode() or "setup-credential.txt" in status_body.decode():
            raise RuntimeError("public setup status disclosed a local credential path")
        csrf = client.csrf()
        status, _, _ = client.request("POST", "/api/v1/auth/setup", {
            "credential": "", "password": "valid-password-123", "displayName": "Admin",
        }, origin=core.origin, csrf=csrf)
        admins = db_query(core.data_dir, "SELECT count(*) FROM admin_user")[0][0]
        credential_remains = (core.data_dir / "setup-credential.txt").is_file()
        if status != 401 or admins != 0 or not credential_remains:
            raise RuntimeError(f"missing-credential setup boundary failed: HTTP={status}, admins={admins}")
        return {"public_setup_status": public_status, "missing_credential_status": status, "admin_rows_after": admins,
                "valid_local_credential_preserved": credential_remains,
                "sqlite": "isolated private work directory"}


def case_02():
    with Core("s01-02") as core:
        client, password, credential = setup(core)
        before = db_query(core.data_dir, "SELECT password_hash, password_salt, password_version FROM admin_user WHERE id=1")[0]
        repeat_client = Client(core)
        csrf = repeat_client.csrf()
        status, _, _ = repeat_client.request("POST", "/api/v1/auth/setup", {
            "credential": credential, "password": "replacement-password-456", "displayName": "Changed",
        }, origin=core.origin, csrf=csrf)
        after = db_query(core.data_dir, "SELECT password_hash, password_salt, password_version FROM admin_user WHERE id=1")[0]
        if status != 409 or before != after:
            raise RuntimeError(f"repeat setup status={status}; administrator verifier was changed")
        return {"first_setup": 201, "repeat_setup": status, "admin_verifier_unchanged": True,
                "initial_session_exists": bool(session_token(client)), "password_used": "redacted"}


def case_03():
    with Core("s01-03") as core:
        setup_client, password, _ = setup(core)
        logout_status, _, _ = setup_client.request("POST", "/api/v1/auth/logout", {},
                                                  origin=core.origin,
                                                  csrf=setup_client.cookie("nodedance_csrf"))
        if logout_status != 204:
            raise RuntimeError(f"setup session cleanup status={logout_status}")
        client = Client(core)
        status, _, _ = login(core, client, "incorrect-password-456")
        if status != 401 or session_token(client):
            raise RuntimeError(f"incorrect password login status={status}; session issued={bool(session_token(client))}")
        status, _, _ = login(core, client, "")
        if status != 401 or session_token(client):
            raise RuntimeError(f"empty password login status={status}; session issued={bool(session_token(client))}")
        status, headers, _ = login(core, client, password)
        token = session_token(client)
        cookies = list(client.jar)
        session_cookie = next((item for item in cookies if item.name == "nodedance_session"), None)
        csrf_cookie = next((item for item in cookies if item.name == "nodedance_csrf"), None)
        if status != 200 or not token or not session_cookie or "HttpOnly" not in session_cookie._rest:
            raise RuntimeError("correct login did not return an HttpOnly server session")
        if not csrf_cookie or csrf_cookie.secure or not csrf_cookie._rest.get("SameSite"):
            raise RuntimeError("development CSRF cookie did not remain readable and SameSite protected")
        rows = db_query(core.data_dir, "SELECT token_digest, csrf_digest FROM browser_sessions")
        token_digest = hashlib.sha256(token.encode()).digest()
        csrf_digest = hashlib.sha256(client.cookie("nodedance_csrf").encode()).digest()
        if len(rows) != 1 or rows[0][0] != token_digest or rows[0][1] != csrf_digest:
            raise RuntimeError("session credentials were not stored as one-way digests")
        set_cookie_headers = headers.get_all("Set-Cookie", [])
        if not any("HttpOnly" in value and "SameSite=Strict" in value for value in set_cookie_headers):
            raise RuntimeError("login response omitted required session cookie attributes")
        with Core("s01-03-production-cookie", production=True) as production_core:
            production_client = Client(production_core)
            credential_path = production_core.data_dir / "setup-credential.txt"
            credential = credential_path.read_text().strip()
            csrf = production_client.csrf()
            setup_status, setup_headers, _ = production_client.request("POST", "/api/v1/auth/setup", {
                "credential": credential, "password": "production-cookie-password-123", "displayName": "Secure Cookie Test",
            }, origin=production_core.origin, csrf=csrf, manual_cookie=f"nodedance_csrf={csrf}")
            if setup_status != 201:
                raise RuntimeError(f"production-origin setup returned {setup_status}")
            # A fresh challenge/login uses an explicit CSRF cookie because this
            # fixture's transport is loopback HTTP while the configured public
            # origin is HTTPS. The Core must still mark every cookie Secure.
            production_csrf = production_client.csrf()
            secure_status, secure_headers, _ = production_client.request("POST", "/api/v1/auth/login", {
                "password": "production-cookie-password-123",
            }, origin=production_core.origin, csrf=production_csrf,
               manual_cookie=f"nodedance_csrf={production_csrf}")
            cookie_values = secure_headers.get_all("Set-Cookie", [])
            if secure_status != 200 or len(cookie_values) < 2 or any("Secure" not in value or "SameSite=Strict" not in value for value in cookie_values):
                raise RuntimeError("production login did not set Secure; SameSite=Strict cookies")
            if not any("nodedance_session=" in value and "HttpOnly" in value for value in cookie_values):
                raise RuntimeError("production session cookie is not HttpOnly")
        return {"wrong_password": 401, "empty_password": 401, "correct_password": status,
                "session_rows": len(rows), "stored_session_and_csrf_are_digests": True,
                "session_cookie": "HttpOnly; SameSite=Strict", "csrf_cookie": "SameSite=Strict; readable for header"}


def case_04():
    with Core("s01-04") as core:
        routes = [
            "/api/v1/auth/me", "/api/v1/auth/logout", "/api/v1/auth/password",
            "/api/v1/auth/sessions", "/api/v1/auth/sessions/0123456789abcdef0123456789abcdef",
            "/api/v1/settings/appearance", "/api/v1/settings/appearance/avatar",
            "/api/v1/settings/appearance/background", "/api/v1/agents", "/api/v1/nodes",
            "/api/v1/tasks", "/api/v1/admin/private", "/api/v1/public/appearance/avatar",
            "/ws/v1/dashboard", "/ws/v1/agent", "/ws/v1/streams/logs",
        ]
        outcomes = {}
        client = Client(core)
        for path in routes:
            status, _, body = client.request("GET", path)
            expected = 404 if path == "/api/v1/public/appearance/avatar" else 401
            if status != expected:
                raise RuntimeError(f"unauthenticated {path} status={status}, expected {expected}")
            if any(marker in body.lower() for marker in (b"password_hash", b"session_digest", b"private-node")):
                raise RuntimeError(f"unauthenticated {path} leaked private response content")
            outcomes[path] = status
        status, headers, body = client.request("GET", "/settings")
        if status != 303 or headers.get("Location") != "/login":
            raise RuntimeError("private settings route did not redirect without rendering private data")
        return {"unauthenticated_private_routes": outcomes, "settings": {"status": status, "location": headers.get("Location")},
                "no_private_response_markers": True}


def case_05():
    config = {"websocket_check_interval": "100ms"}
    with Core("s01-05", config) as core:
        first, password, _ = setup(core)
        second = Client(core)
        status, _, _ = login(core, second, password)
        if status != 200:
            raise RuntimeError(f"second client login status={status}")
        stale_token = session_token(second)
        if not stale_token:
            raise RuntimeError("second client did not receive a session")
        connection, ws_status, handshake = websocket_handshake(core, stale_token)
        if ws_status != 101:
            connection.close()
            raise RuntimeError(f"valid session WebSocket handshake returned {ws_status}: {handshake}")
        csrf = second.cookie("nodedance_csrf")
        logout_status, _, _ = second.request("POST", "/api/v1/auth/logout", {}, origin=core.origin, csrf=csrf)
        if logout_status != 204:
            connection.close()
            raise RuntimeError(f"logout status={logout_status}")
        stale_client = Client(core)
        status, _, _ = stale_client.request("GET", "/api/v1/auth/me", manual_cookie=f"nodedance_session={stale_token}")
        if status != 401:
            connection.close()
            raise RuntimeError(f"reused old cookie returned {status}, expected 401")
        start = time.monotonic()
        try:
            ws_elapsed, close_kind, payload, ping_count, pong_count = wait_websocket_close(connection, 3)
        finally:
            connection.close()
        elapsed = time.monotonic() - start
        if elapsed > 3 or close_kind != "close-frame":
            raise RuntimeError(f"revoked WebSocket did not close promptly: {close_kind}, {elapsed:.3f}s")
        return {"logout": logout_status, "old_cookie_api": status, "websocket_close": close_kind,
                "websocket_close_seconds": round(elapsed, 3), "close_status_code": int.from_bytes(payload[:2], "big") if len(payload) >= 2 else None,
                "heartbeat_pings": ping_count, "heartbeat_pongs": pong_count,
                "two_independent_clients": bool(session_token(first)) and bool(stale_token),
                "server_log": str(core.log_path.relative_to(ROOT))}


def case_06():
    config = {"session_idle_timeout": "3s", "websocket_check_interval": "100ms"}
    with Core("s01-06", config) as core:
        client, password, _ = setup(core)
        token = session_token(client)
        connection, ws_status, handshake = websocket_handshake(core, token)
        if ws_status != 101:
            connection.close()
            raise RuntimeError(f"WebSocket handshake returned {ws_status}: {handshake}")
        started = time.monotonic()
        ws_elapsed, close_kind, payload, ping_count, pong_count = wait_websocket_close(connection, 5)
        elapsed = time.monotonic() - started
        stale_client = Client(core)
        status, _, _ = stale_client.request("GET", "/api/v1/auth/me", manual_cookie=f"nodedance_session={token}")
        if status != 401:
            connection.close()
            raise RuntimeError(f"idle session API status={status}, expected 401")
        connection.close()
        if close_kind != "close-frame" or elapsed > 4:
            raise RuntimeError(f"idle WebSocket closure exceeded limit: {close_kind} after {elapsed:.3f}s")
        if pong_count < 1:
            raise RuntimeError("WebSocket did not acknowledge client heartbeats before idle expiry")
        if int.from_bytes(payload[:2], "big") != 1008:
            raise RuntimeError("expired WebSocket did not close with policy violation status")
        return {"configured_idle_timeout": "3s (--dev only)", "heartbeat": "client ping every 200ms",
                "expired_session_api": status, "websocket_close": close_kind,
                "close_seconds": round(elapsed, 3), "close_status_code": 1008,
                "heartbeat_pings": ping_count, "heartbeat_pongs": pong_count,
                "socket_did_not_touch_session_activity": True}


def case_07():
    with Core("s01-07", {"websocket_check_interval": "100ms"}) as core:
        primary, old_password, _ = setup(core)
        secondary = Client(core)
        status, _, _ = login(core, secondary, old_password)
        if status != 200:
            raise RuntimeError(f"secondary login status={status}")
        sessions_status, _, sessions_body = primary.request("GET", "/api/v1/auth/sessions")
        sessions = json.loads(sessions_body)["sessions"]
        if sessions_status != 200 or len(sessions) != 2:
            raise RuntimeError(f"expected two sessions before revoke, got status={sessions_status}, count={len(sessions)}")
        second_session = next(item for item in sessions if not item["current"])
        csrf = primary.cookie("nodedance_csrf")
        status, _, _ = primary.request("DELETE", f"/api/v1/auth/sessions/{second_session['id']}", {}, origin=core.origin, csrf=csrf)
        if status != 204:
            raise RuntimeError(f"revoke other device status={status}")
        revoked_status, _, _ = Client(core).request("GET", "/api/v1/auth/me", manual_cookie=f"nodedance_session={session_token(secondary)}")
        if revoked_status != 401:
            raise RuntimeError(f"revoked device reused cookie with status={revoked_status}")
        third = Client(core)
        status, _, _ = login(core, third, old_password)
        if status != 200:
            raise RuntimeError(f"third device login status={status}")
        old_primary_cookie = session_token(primary)
        old_third_cookie = session_token(third)
        if not old_primary_cookie or not old_third_cookie:
            raise RuntimeError("could not capture live session cookies before password change")
        change_csrf = primary.cookie("nodedance_csrf")
        status, _, _ = primary.request("POST", "/api/v1/auth/password", {
            "currentPassword": old_password, "newPassword": "S01-password-changed-456",
        }, origin=core.origin, csrf=change_csrf)
        if status != 204:
            raise RuntimeError(f"password change status={status}")
        all_sessions = db_query(core.data_dir, "SELECT count(*) FROM browser_sessions")[0][0]
        if all_sessions != 0:
            raise RuntimeError(f"password change left {all_sessions} browser sessions")
        old_primary = Client(core).request("GET", "/api/v1/auth/me", manual_cookie=f"nodedance_session={old_primary_cookie}")[0]
        old_third = Client(core).request("GET", "/api/v1/auth/me", manual_cookie=f"nodedance_session={old_third_cookie}")[0]
        if old_primary != 401 or old_third != 401:
            raise RuntimeError(f"password change stale sessions returned {old_primary}/{old_third}")
        return {"sessions_before": len(sessions), "other_device_revoke": 204,
                "revoked_device_api": revoked_status, "password_change": status,
                "session_rows_after_password_change": all_sessions,
                "old_primary_cookie": old_primary, "old_third_cookie": old_third}


def case_08():
    with Core("s01-08", {"websocket_check_interval": "100ms"}) as core:
        client, password, _ = setup(core)
        appearance_before = client.request("GET", "/api/v1/public/appearance")[2]
        csrf = client.cookie("nodedance_csrf")
        status, _, _ = client.request("PUT", "/api/v1/settings/appearance", {
            "displayName": "Cross site", "theme": "dark", "backgroundColor": "#101827",
        }, origin="https://evil.example", csrf=csrf)
        if status != 403:
            raise RuntimeError(f"cross-site write returned {status}")
        appearance_after = client.request("GET", "/api/v1/public/appearance")[2]
        if appearance_before != appearance_after:
            raise RuntimeError("cross-site appearance write changed public login configuration")
        wrong_csrf, _, _ = client.request("PUT", "/api/v1/settings/appearance", {
            "displayName": "Bad token", "theme": "dark", "backgroundColor": "#101827",
        }, origin=core.origin, csrf="invalid-csrf-token")
        if wrong_csrf != 403:
            raise RuntimeError(f"unbound CSRF token returned {wrong_csrf}")
        connection, ws_status, handshake = websocket_handshake(core, session_token(client), "https://evil.example")
        connection.close()
        if ws_status != 403:
            raise RuntimeError(f"invalid-origin WebSocket status={ws_status}: {handshake}")
        # A valid-origin password rejection creates a structured audit event; no body is retained.
        bad_login = Client(core)
        rejected_login, _, _ = login(core, bad_login, "wrong-password-123")
        audit_rows = db_query(core.data_dir, "SELECT action, outcome, remote_addr FROM audit_entries ORDER BY id")
        allowed = {"setup", "login", "logout", "password_change", "session_revoke", "appearance_update", "image_upload"}
        if rejected_login != 401 or any(row[0] not in allowed for row in audit_rows):
            raise RuntimeError("rejected login audit was missing or used an unallowlisted action")
        if not any(row[0] == "login" and row[1] == "rejected" for row in audit_rows):
            raise RuntimeError("rejected login did not write a structured audit event")
        return {"cross_origin_write": status, "wrong_csrf": wrong_csrf,
                "invalid_origin_websocket": ws_status, "appearance_unchanged": True,
                "audit_events": [{"action": row[0], "outcome": row[1], "remote_addr": row[2]} for row in audit_rows],
                "audit_allowlist_only": True}


def case_09():
    config = {"login_max_attempts": 3, "login_lockout_duration": "4s",
              "websocket_check_interval": "100ms"}
    with Core("s01-09", config) as core:
        _, password, _ = setup(core)
        client = Client(core)
        statuses = []
        for _ in range(3):
            statuses.append(login(core, client, "incorrect-password-987")[0])
        blocked = login(core, client, password)[0]
        if statuses != [401, 401, 401] or blocked != 429:
            raise RuntimeError(f"login lockout statuses={statuses}, threshold+1={blocked}")
        time.sleep(4.2)
        recovered = login(core, client, password)[0]
        if recovered != 200:
            raise RuntimeError(f"correct password failed after cooldown: {recovered}")
        return {"failed_login_statuses": statuses, "blocked_correct_password_status": blocked,
                "attempt_threshold": 3, "cooldown_seconds": 4,
                "successful_login_after_cooldown": recovered,
                "production_default_attempts": 5, "production_default_lockout": "15m"}


def case_10():
    with Core("s01-10") as core:
        client, password, credential = setup(core, password="S01-scan-secret-384726")
        token = session_token(client)
        csrf = client.cookie("nodedance_csrf")
        private_status, private_headers, private_body = client.request("GET", "/api/v1/auth/me")
        root_status, root_headers, _ = client.request("GET", "/")
        if private_status != 200 or "no-store" not in private_headers.get("Cache-Control", ""):
            raise RuntimeError("private API response was not marked no-store")
        if root_status != 200 or "no-store" not in root_headers.get("Cache-Control", ""):
            raise RuntimeError("login shell response was not marked no-store")
        digest = hashlib.sha256(token.encode()).digest()
        stored = db_query(core.data_dir, "SELECT token_digest, csrf_digest FROM browser_sessions")[0]
        if stored[0] != digest or token.encode() in stored[0] or csrf.encode() in stored[1]:
            raise RuntimeError("raw session or CSRF token is stored in SQLite")
        audit = db_query(core.data_dir, "SELECT action, outcome, remote_addr FROM audit_entries")
        db_path = core.data_dir / "nodedance.sqlite"
        wal_path = pathlib.Path(str(db_path) + "-wal")
        static_root = ROOT / "internal/core/webassets/dist"
        if not static_root.is_dir():
            raise NotReady("embedded frontend assets are absent; make build must generate internal/core/webassets/dist")
        static_files = sorted(path for path in static_root.rglob("*") if path.is_file())
        if not static_files:
            raise RuntimeError("embedded frontend asset directory contains no generated files")
        scanned = [db_path, pathlib.Path(str(db_path) + "-shm"), wal_path, core.log_path, *static_files]
        secrets = [password.encode(), credential.encode(), token.encode(), csrf.encode(), b"private-node-canary"]
        findings = []
        for path in scanned:
            if not path.exists():
                continue
            content = path.read_bytes()
            for secret in secrets:
                if secret and secret in content:
                    findings.append(path.name)
        if findings:
            raise RuntimeError(f"sensitive plaintext found in SQLite/WAL/log/static assets: {sorted(set(findings))}")
        if b"password" in private_body.lower() and b"passwordHash" in private_body:
            raise RuntimeError("private user response disclosed a password field")
        audit_columns = [row[1] for row in db_query(core.data_dir, "PRAGMA table_info(audit_entries)")]
        if audit_columns != ["id", "occurred_at", "action", "outcome", "actor_id", "remote_addr", "target_kind", "target_id"]:
            raise RuntimeError(f"audit table has unexpected free-form fields: {audit_columns}")
        return {"scanned_files": [str(path.relative_to(ROOT)) for path in static_files],
                "private_data_scanned": ["SQLite", "SQLite WAL", "SQLite SHM", "Core log"],
                "known_plaintext_matches": [], "session_storage": "SHA-256 token digest only",
                "csrf_storage": "SHA-256 session-bound digest only", "response_cache_control": "no-store",
                "audit_fields": audit_columns, "audit_rows": len(audit)}


def has_contiguous_schema_versions(versions):
    """Check the complete applied migration ledger without pinning its length."""
    return bool(versions) and versions == list(range(1, len(versions) + 1))


def case_11():
    # Force a real DDL collision after the migration ledger has been created.
    migration_dir = WORK_DIR / "s01-11-migration-data"
    migration_dir.mkdir(parents=True, exist_ok=True, mode=0o700)
    database = migration_dir / "nodedance.sqlite"
    connection = sqlite3.connect(database)
    connection.execute("CREATE TABLE admin_user (sentinel TEXT NOT NULL)")
    connection.execute("INSERT INTO admin_user(sentinel) VALUES ('original-data-survives')")
    connection.commit()
    connection.close()
    os.chmod(database, 0o600)
    failed = Core("s01-11-migration", data_dir=migration_dir)
    failed.log_path.parent.mkdir(parents=True, exist_ok=True)
    failed.stream = failed.log_path.open("w")
    failed.stream.write("$ " + " ".join(failed.command()) + "\n")
    failed.stream.flush()
    failed.process = subprocess.Popen(failed.command(), cwd=ROOT, env=clean_environment(),
                                      stdout=failed.stream, stderr=subprocess.STDOUT)
    try:
        migration_code = failed.process.wait(timeout=10)
    except subprocess.TimeoutExpired:
        failed.process.kill()
        migration_code = failed.process.wait(timeout=5)
        failed.stream.close()
        raise RuntimeError("injected migration failure did not terminate promptly")
    failed.stream.close()
    if migration_code == 0:
        raise RuntimeError("Core accepted a real conflicting migration")
    migration_log = failed.log_path.read_text(errors="replace").lower()
    if "apply migration" not in migration_log or "already exists" not in migration_log:
        raise RuntimeError("migration fault did not produce an explicit migration error")
    preserved = db_query(migration_dir, "SELECT sentinel FROM admin_user")[0][0]
    integrity = db_query(migration_dir, "PRAGMA integrity_check")[0][0]
    if preserved != "original-data-survives" or integrity != "ok":
        raise RuntimeError("migration failure damaged original database contents")

    # Remove only the intentional conflict while preserving its row, then prove
    # the same Core data directory can complete migration and serve requests.
    recovery_db = sqlite3.connect(database)
    recovery_db.execute("ALTER TABLE admin_user RENAME TO migration_conflict_preserved")
    recovery_db.commit()
    recovery_db.close()
    with Core("s01-11-migration-recovery", data_dir=migration_dir) as recovered:
        recovery_status, _, _ = Client(recovered).request("GET", "/api/v1/health")
        recovery_versions = [row[0] for row in db_query(migration_dir, "SELECT version FROM schema_migrations ORDER BY version")]
        recovery_row = db_query(migration_dir, "SELECT sentinel FROM migration_conflict_preserved")[0][0]
        if (recovery_status != 200 or not has_contiguous_schema_versions(recovery_versions)
                or recovery_row != "original-data-survives"):
            raise RuntimeError("Core did not recover on the repaired original data directory")

    deny_parent = pathlib.Path(tempfile.mkdtemp(prefix="nodedance-s01-unwritable-"))
    os.chmod(deny_parent, 0o755)
    deny_dir = deny_parent / "data"
    deny_dir.mkdir(mode=0o755)
    os.chmod(deny_dir, 0o755)
    sentinel = deny_dir / "original.txt"
    sentinel.write_text("keep-this-original-file\n")
    os.chmod(sentinel, 0o644)
    command = [str(ROOT / ".build/nodedance"), "serve", "--dev", "--listen",
               f"127.0.0.1:{free_port()}", "--data-dir", str(deny_dir)]
    log = ATTEMPT_DIR / "s01-11-unwritable.log"
    stream = log.open("w")
    stream.write("$ " + " ".join(command) + "\n")
    stream.flush()
    process = None
    unwritable_code = None
    try:
        if os.geteuid() == 0:
            nobody = pwd.getpwnam("nobody")
            def drop_privileges():
                os.setgroups([])
                os.setgid(nobody.pw_gid)
                os.setuid(nobody.pw_uid)
            environment = clean_environment()
            environment["HOME"] = "/tmp"
            environment["XDG_CONFIG_HOME"] = "/tmp"
            environment["XDG_DATA_HOME"] = "/tmp/nodedance-s01-unwritable-xdg"
            process = subprocess.Popen(command, cwd=ROOT, env=environment,
                                       stdout=stream, stderr=subprocess.STDOUT, preexec_fn=drop_privileges)
        else:
            sudo = shutil.which("sudo")
            if not sudo:
                raise NotReady("non-root unwritable-directory check requires sudo -n -u nobody")
            probe = subprocess.run([sudo, "-n", "-u", "nobody", "--", "true"],
                                   stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
            if probe.returncode:
                raise NotReady("passwordless sudo -u nobody is unavailable for a real non-root unwritable-directory check")
            environment = clean_environment()
            environment["HOME"] = "/tmp"
            environment["XDG_CONFIG_HOME"] = "/tmp"
            environment["XDG_DATA_HOME"] = "/tmp/nodedance-s01-unwritable-xdg"
            process = subprocess.Popen([sudo, "-n", "-u", "nobody", "--", *command], cwd=ROOT,
                                       env=environment, stdout=stream, stderr=subprocess.STDOUT)
        try:
            unwritable_code = process.wait(timeout=10)
        except subprocess.TimeoutExpired:
            process.kill()
            unwritable_code = process.wait(timeout=5)
            raise RuntimeError("Core did not terminate promptly on an unwritable data directory")
        stream.close()
        output = log.read_text(errors="replace").lower()
        if unwritable_code == 0 or not any(term in output for term in ("operation not permitted", "permission denied")):
            raise RuntimeError("Core did not fail while running as a different unprivileged user")
        if sentinel.read_text() != "keep-this-original-file\n" or (deny_dir / "nodedance.sqlite").exists():
            raise RuntimeError("unprivileged database startup changed the protected directory")
        return {"migration_failure_exit": migration_code, "migration_log": str(failed.log_path.relative_to(ROOT)),
                "recovery_health": recovery_status, "recovered_schema_versions": recovery_versions,
                "preserved_original_row_after_recovery": recovery_row,
                "migration_original_row": preserved, "sqlite_integrity": integrity,
                "unwritable_core_user": "nobody", "unwritable_exit": unwritable_code,
                "protected_sentinel_unchanged": True, "unwritable_log": str(log.relative_to(ROOT))}
    finally:
        if process is not None and process.poll() is None:
            process.kill()
            process.wait(timeout=5)
        if not stream.closed:
            stream.close()
        shutil.rmtree(deny_parent, ignore_errors=True)


def case_12():
    configured = os.environ.get("NODEDANCE_PLAYWRIGHT_PROJECTS", "chromium,webkit,firefox")
    requested = [value.strip() for value in configured.split(",") if value.strip()]
    allowed = {"chromium", "webkit", "firefox"}
    if not requested or len(requested) != len(set(requested)) or any(value not in allowed for value in requested):
        raise RuntimeError(f"invalid NODEDANCE_PLAYWRIGHT_PROJECTS: {configured!r}")
    pnpm = shutil.which("pnpm")
    if not pnpm:
        raise NotReady("locked pnpm executable is absent from PATH")
    projects = {}
    for project in requested:
        config = {"websocket_check_interval": "100ms"}
        with Core(f"s01-12-{project}", config) as core:
            credential_path = core.data_dir / "setup-credential.txt"
            register_sensitive(credential_path.read_text().strip())
            initial_password = f"NodeDance-S01-{RUN_ID}-{CURRENT_ATTEMPT}-{project}-Initial42!"
            changed_password = f"NodeDance-S01-{RUN_ID}-{CURRENT_ATTEMPT}-{project}-Changed84!"
            register_sensitive(initial_password)
            register_sensitive(changed_password)
            environment = os.environ.copy()
            environment["PLAYWRIGHT_BASE_URL"] = core.base_url
            environment["NODEDANCE_TEST_DATA_DIR"] = str(core.data_dir)
            environment["NODEDANCE_TEST_INITIAL_PASSWORD"] = initial_password
            environment["NODEDANCE_TEST_CHANGED_PASSWORD"] = changed_password
            command = [pnpm, "--dir", "web", "exec", "playwright", "test",
                       "tests/s01-auth.spec.ts", f"--project={project}",
                       f"--output={WORK_DIR / f's01-12-{project}-playwright-output'}"]
            log = ATTEMPT_DIR / f"s01-12-{project}-playwright.log"
            with log.open("w") as stream:
                stream.write("$ " + " ".join(command) + "\n")
                stream.flush()
                process = subprocess.Popen(command, cwd=ROOT, env=environment, text=True,
                                           stdout=stream, stderr=subprocess.STDOUT,
                                           start_new_session=True)
                try:
                    exit_code = process.wait(timeout=240)
                except subprocess.TimeoutExpired:
                    try:
                        os.killpg(process.pid, signal.SIGTERM)
                    except ProcessLookupError:
                        pass
                    try:
                        process.wait(timeout=5)
                    except subprocess.TimeoutExpired:
                        try:
                            os.killpg(process.pid, signal.SIGKILL)
                        except ProcessLookupError:
                            pass
                        process.wait(timeout=5)
            output = log.read_text(errors="replace")
            if exit_code:
                if any(marker in output for marker in ("Executable doesn't exist", "Please run the following command to download new browsers", "browserType.launch: Executable")):
                    projects[project] = {"status": "NOT_READY", "reason": "Playwright browser executable is not installed", "log": str(log.relative_to(ROOT))}
                    continue
                projects[project] = {"status": "FAIL", "exit_code": exit_code,
                                     "log": str(log.relative_to(ROOT))}
                continue
            projects[project] = {"status": "PASS", "log": str(log.relative_to(ROOT)),
                                 "real_core": True, "real_browser": True,
                                 "direct_http_svg_and_html_upload_rejected": True}
    unrequested = sorted(allowed - set(requested))
    for project in unrequested:
        projects[project] = {"status": "NOT_READY", "reason": "not requested in this partial browser run"}
    missing = [name for name in sorted(allowed) if projects[name]["status"] != "PASS"]
    if missing:
        failed = [name for name in missing if projects[name]["status"] == "FAIL"]
        if failed:
            details = ", ".join(f"{name}={projects[name]['log']}" for name in failed)
            raise RuntimeError(f"Playwright projects failed: {details}; statuses={projects}")
        raise NotReady(f"S01-12 requires real Chromium, WebKit, and Firefox runs; incomplete projects: {', '.join(missing)}",
                       {"projects": projects, "missing_projects": missing})
    return {"projects": projects, "viewport_and_touch_emulation": "browser spec includes 390x844 responsive login backdrop",
            "ui_svg_rejection_and_direct_core_http_svg_html_rejection": True}


def scan_run_artifacts():
    roots = (
        ("acceptance_logs", ARTIFACTS / RUN_ID),
        ("uploaded_stage_logs", ROOT / ".artifacts/stage-runs"),
        ("private_work", WORK_ROOT / RUN_ID),
        ("embedded_frontend", ROOT / "internal/core/webassets/dist"),
    )
    files = []
    for category, root in roots:
        if not root.exists():
            continue
        if root.is_file():
            files.append((category, root))
        else:
            files.extend((category, path) for path in root.rglob("*") if path.is_file())
    credential_paths = []
    for category, path in files:
        if category == "private_work" and path.name == "setup-credential.txt":
            credential_paths.append(path)
    credential_values = set()
    for path in credential_paths:
        try:
            credential_values.add(path.read_bytes().strip())
        except OSError as error:
            raise RuntimeError(f"could not inspect generated setup credential: {error}") from error
    values = sorted(value for value in SENSITIVE_VALUES | credential_values if value)
    findings = []
    allowed_credentials = []
    allowed_credential_paths = set(ALLOWED_SETUP_CREDENTIALS)
    file_counts = {}
    uploaded_evidence_categories = {"acceptance_logs", "uploaded_stage_logs"}
    uploaded_evidence_matches = 0
    for category, path in files:
        file_counts[category] = file_counts.get(category, 0) + 1
        try:
            content = path.read_bytes()
        except OSError as error:
            raise RuntimeError(f"could not scan generated evidence file {path}: {error}") from error
        if path.name == "setup-credential.txt" and path.resolve() in allowed_credential_paths:
            file_info = path.lstat()
            data_dir = ALLOWED_SETUP_CREDENTIALS[path.resolve()]
            if not stat.S_ISREG(file_info.st_mode) or path.is_symlink() or file_info.st_mode & 0o777 != 0o600:
                raise RuntimeError("an allowed setup credential is not a regular 0600 file")
            if data_dir.stat().st_mode & 0o777 != 0o700:
                raise RuntimeError("an allowed setup credential is outside a 0700 data directory")
            admin_count = db_query(data_dir, "SELECT count(*) FROM admin_user")[0][0]
            init_count = db_query(data_dir, "SELECT count(*) FROM init_credential")[0][0]
            if admin_count != 0 or init_count != 1:
                raise RuntimeError("an allowed setup credential is not limited to an uninitialized Core")
            allowed_credentials.append(path)
            continue
        if any(value and value in content for value in values):
            findings.append((category, path))
            if category in uploaded_evidence_categories:
                uploaded_evidence_matches += 1
    matching_paths = [str(path.relative_to(ROOT)) for _, path in findings]
    return {"files_scanned": len(files), "sensitive_values_checked": len(values),
            "categories": sorted({category for category, _ in files}),
            "allowed_setup_credential_files": len(allowed_credentials),
            "allowed_setup_credential_paths": [str(path.relative_to(ROOT)) for path in sorted(allowed_credentials)],
            "files_scanned_by_category": file_counts,
            "uploaded_evidence_files_scanned": sum(file_counts.get(category, 0) for category in uploaded_evidence_categories),
            "uploaded_evidence_plaintext_matches": uploaded_evidence_matches,
            "plaintext_matches": len(findings), "matching_paths": matching_paths,
            "matching_categories": sorted({category for category, _ in findings})}


CASES = {
    "S01-01": case_01,
    "S01-02": case_02,
    "S01-03": case_03,
    "S01-04": case_04,
    "S01-05": case_05,
    "S01-06": case_06,
    "S01-07": case_07,
    "S01-08": case_08,
    "S01-09": case_09,
    "S01-10": case_10,
    "S01-11": case_11,
    "S01-12": case_12,
}


def write_reports(report):
    REPORT.parent.mkdir(parents=True, exist_ok=True)
    REPORT.write_text(json.dumps(report, ensure_ascii=False, indent=2) + "\n")
    current = json.loads(STATUSES.read_text()) if STATUSES.exists() else {"schema": 1, "stages": {}}
    current.setdefault("stages", {})["S01"] = {
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
    if args.repeat < 1 or args.repeat > 3:
        parser.error("--repeat must be 1..3")
    if args.mode == "full" and args.repeat != 3:
        parser.error("full stage acceptance must run three consecutive times")

    global CURRENT_ATTEMPT, ATTEMPT_DIR, WORK_DIR, HTTP_LOG
    selected = list(CASES)
    if args.mode == "integration":
        selected = [f"S01-{index:02d}" for index in range(1, 12)]
    elif args.mode == "e2e":
        selected = ["S01-12"]

    report = {
        "schema": 1, "stage": "S01", "mode": args.mode, "run_id": RUN_ID,
        "updated_at": timestamp(), "status": "NOT_READY", "repeat_required": 3,
        "repeat_requested": args.repeat, "verification_status": "NOT_RUN", "tests": {},
        "evidence_root": str((ARTIFACTS / RUN_ID).relative_to(ROOT)),
    }
    for case in S01_CASES:
        report["tests"][case["id"]] = {
            "status": "NOT_READY", "action": case["action"], "expected": case["expected"],
            "environment": case["environment"], "evidence_required": case["evidence"],
            "runs": [], "reason": "Not executed in this mode; a prior report cannot substitute for current execution.",
        }

    ARTIFACTS.mkdir(parents=True, exist_ok=True)
    WORK_ROOT.mkdir(parents=True, exist_ok=True, mode=0o700)
    os.chmod(WORK_ROOT, 0o700)
    all_run_results = []
    for attempt in range(1, args.repeat + 1):
        CURRENT_ATTEMPT = attempt
        ATTEMPT_DIR = ARTIFACTS / RUN_ID / f"attempt-{attempt}"
        WORK_DIR = WORK_ROOT / RUN_ID / f"attempt-{attempt}"
        ATTEMPT_DIR.mkdir(parents=True, exist_ok=True)
        WORK_DIR.mkdir(parents=True, exist_ok=True, mode=0o700)
        os.chmod(WORK_DIR, 0o700)
        HTTP_LOG = ATTEMPT_DIR / "http-requests.jsonl"
        HTTP_LOG.write_text("")
        print(f"S01 acceptance run {attempt}/{args.repeat} ({args.mode})", flush=True)
        this_run = {}
        for test_id in selected:
            started = time.monotonic()
            try:
                detail = CASES[test_id]()
                status, reason = "PASS", ""
            except NotReady as error:
                detail, status, reason = error.detail, "NOT_READY", str(error)
            except Exception as error:
                detail, status, reason = {}, "FAIL", f"{type(error).__name__}: {error}"
            duration = round(time.monotonic() - started, 3)
            record = report["tests"][test_id]
            record["runs"].append({"attempt": attempt, "status": status,
                                   "duration_seconds": duration, "details": detail,
                                   "reason": reason})
            this_run[test_id] = status
            if status == "FAIL":
                record["status"], record["reason"] = "FAIL", reason
            elif status == "NOT_READY" and record["status"] != "FAIL":
                record["status"], record["reason"] = "NOT_READY", reason
            print(f"{test_id}: {status} ({duration:.3f}s){': ' + reason if reason else ''}", flush=True)
        all_run_results.append(this_run)

    artifact_scan = scan_run_artifacts()
    report["artifact_scan"] = artifact_scan
    if "S01-10" in selected:
        scan_reason = "Generated acceptance logs, private work files, and embedded frontend assets were scanned for every known test secret."
        for run in report["tests"]["S01-10"]["runs"]:
            run.setdefault("details", {})["complete_run_artifact_scan"] = artifact_scan
            if artifact_scan["plaintext_matches"]:
                run["status"] = "FAIL"
                run["reason"] = "Sensitive plaintext was found in generated acceptance artifacts."
        if artifact_scan["plaintext_matches"]:
            report["tests"]["S01-10"]["status"] = "FAIL"
            report["tests"]["S01-10"]["reason"] = "Sensitive plaintext was found in generated acceptance artifacts."
        elif report["tests"]["S01-10"]["runs"]:
            report["tests"]["S01-10"]["runs"][-1]["details"]["complete_run_artifact_scan_note"] = scan_reason

    for test_id in selected:
        record = report["tests"][test_id]
        statuses = [item["status"] for item in record["runs"]]
        if statuses and all(value == "PASS" for value in statuses) and args.mode == "full" and args.repeat == 3:
            record["status"], record["reason"] = "PASS", "All three current consecutive executions passed."
        elif "FAIL" in statuses:
            record["status"] = "FAIL"
        elif "NOT_READY" in statuses:
            record["status"] = "NOT_READY"
        else:
            record["status"] = "NOT_READY"
            record["reason"] = "This mode does not satisfy the full-stage three-run requirement."

    all_required_statuses = [report["tests"][case["id"]]["status"] for case in S01_CASES]
    selected_statuses = [entry for test_id in selected for entry in (item["status"] for item in report["tests"][test_id]["runs"])]
    report["verification_status"] = "PASS" if selected_statuses and all(item == "PASS" for item in selected_statuses) else "FAIL"
    if args.mode == "full" and args.repeat == 3 and all(status == "PASS" for status in all_required_statuses):
        report["status"], report["reason"] = "PASS", "All twelve original S01 acceptance cases passed in three consecutive full runs."
    elif "FAIL" in all_required_statuses:
        report["status"], report["reason"] = "FAIL", "At least one current S01 required acceptance case failed."
    else:
        report["status"], report["reason"] = "NOT_READY", "Required S01 cases were not run or the three-consecutive full acceptance threshold was not met."
    report["updated_at"] = timestamp()
    write_reports(report)
    print(f"S01 {report['status']}: report={REPORT.relative_to(ROOT)} evidence={report['evidence_root']}", flush=True)
    return 0 if report["verification_status"] == "PASS" else 1


if __name__ == "__main__":
    raise SystemExit(main())
