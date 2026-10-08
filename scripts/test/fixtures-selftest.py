#!/usr/bin/env python3
"""Exercise locked-digest fixtures in a dedicated Docker-in-Docker Engine."""
import json
import os
import pathlib
import subprocess
import sys

ROOT = pathlib.Path(__file__).resolve().parents[2]
HOST = os.environ.get("NODEDANCE_HOST_DOCKER_HOST", "unix:///var/run/docker.sock")
ENGINE = os.environ.get("NODEDANCE_TEST_ENGINE", "29")
RUN = sys.argv[1] if len(sys.argv) > 1 else ""
if ENGINE not in ("28", "29"):
    raise SystemExit("NODEDANCE_TEST_ENGINE must be 28 or 29")
if not RUN or len(RUN) > 32 or not RUN[0].isalnum() or any(not (c.isalnum() or c in "-_") for c in RUN):
    raise SystemExit("usage: fixtures-selftest.py UNIQUE_RUN_ID")


def run(args, *, env=None, check=True, capture=False, quiet=False):
    print("+", " ".join(str(x) for x in args), flush=True)
    result = subprocess.run(
        args, cwd=ROOT, env=env, text=True,
        stdout=subprocess.PIPE if capture else None,
        stderr=subprocess.STDOUT if capture else None,
    )
    if capture and result.stdout and not quiet:
        print(result.stdout, end="" if result.stdout.endswith("\n") else "\n", flush=True)
    if check and result.returncode:
        raise RuntimeError(f"command failed with exit {result.returncode}: {' '.join(args)}")
    return result


def host_snapshot():
    # Exclude only the exact owned nested-daemon container. Other test-labelled
    # or user containers remain in this snapshot and must be unchanged.
    result = run(
        ["docker", "--host", HOST, "ps", "--all", "--format",
         '{{.ID}}|{{.Names}}|{{.State}}|{{.Label "io.nodedance.suite"}}'],
        capture=True, quiet=True,
    )
    rows = {}
    for line in result.stdout.splitlines():
        fields = line.split("|")
        if len(fields) != 4:
            raise RuntimeError("could not parse host Docker snapshot")
        cid, name, state, suite = fields
        if suite != "nodedance-s00-dind":
            rows[cid] = {"name": name, "state": state}
    return rows


def test_docker_env():
    env = os.environ.copy()
    env["NODEDANCE_TEST_DOCKER_HOST"] = f"unix://{ROOT}/.artifacts/dind/v{ENGINE}/socket/docker.sock"
    env["DOCKER_CONFIG"] = str(ROOT / ".artifacts/docker-config")
    # Write the private CLI configuration without touching the user's Docker
    # config. Docker CLI commands below then resolve the local locked plugin.
    run(["bash", "-c", f'source "{ROOT}/scripts/docker-test-config.sh"'], env=env)
    return env


def assert_locked_compose_selected(env):
    host = env["NODEDANCE_TEST_DOCKER_HOST"]
    details = run(["docker", "--host", host, "info", "--format", "{{json .ClientInfo.Plugins}}"],
                  env=env, capture=True, quiet=True)
    plugins = json.loads(details.stdout)
    compose_plugins = [item for item in plugins if item.get("Name") == "compose"]
    lock = json.loads((ROOT / "toolchain.lock.json").read_text())
    expected_path = (ROOT / ".tools/docker/cli-plugins/docker-compose").resolve()
    if len(compose_plugins) != 1:
        raise RuntimeError(f"expected exactly one Docker Compose client plugin, found {len(compose_plugins)}")
    actual = compose_plugins[0]
    if pathlib.Path(actual["Path"]).resolve() != expected_path:
        raise RuntimeError(f"Docker CLI selected unexpected Compose plugin path: {actual['Path']}")
    expected_version = "v" + lock["compose"]["version"]
    if actual["Version"] != expected_version:
        raise RuntimeError(f"expected Compose {expected_version}, got {actual['Version']}")
    sha = subprocess.check_output(["sha256sum", str(expected_path)], text=True).split()[0]
    machine = {"x86_64": "amd64", "aarch64": "arm64"}.get(os.uname().machine)
    if machine is None:
        raise RuntimeError(f"unsupported Compose test host architecture: {os.uname().machine}")
    expected_sha = lock["compose"]["archives"][f"linux/{machine}"]["sha256"]
    if sha != expected_sha:
        raise RuntimeError(f"local Compose SHA-256 mismatch: expected {expected_sha}, got {sha}")
    engine = run(["docker", "--host", host, "version", "--format", "{{.Server.Version}}"],
                 env=env, capture=True, quiet=True).stdout.strip()
    compose_version = run(["docker", "--host", host, "compose", "version", "--short"],
                          env=env, capture=True, quiet=True).stdout.strip().removeprefix("v")
    if compose_version != lock["compose"]["version"]:
        raise RuntimeError(f"Docker CLI Compose version mismatch: {compose_version}")
    host_engine = subprocess.check_output(["docker", "--host", HOST, "version", "--format", "{{.Server.Version}}"], text=True).strip()
    docker_client = subprocess.check_output(["docker", "--host", HOST, "version", "--format", "{{.Client.Version}}"], text=True).strip()
    version_record = {"docker_cli": docker_client, "host_engine": host_engine,
                      "test_engine": engine, "compose": compose_version,
                      "compose_plugin": actual["Path"], "compose_sha256": sha}
    return version_record


def compose(*args, env):
    directory = f".artifacts/fixtures/{RUN}"
    return run(["docker", "--host", env["NODEDANCE_TEST_DOCKER_HOST"], "compose",
                "--env-file", f"{directory}/.env", "--project-directory", directory,
                "--project-name", f"nodedance-demo-{RUN}", "-f", f"{directory}/compose.yaml",
                "-f", f"{directory}/compose.override.yaml", *args], env=env)


def assert_persisted_markers(env, context):
    host = env["NODEDANCE_TEST_DOCKER_HOST"]
    page = run(["docker", "--host", host, "exec", f"nodedance-demo-{RUN}-web-1",
                "cat", "/usr/share/nginx/html/index.html"], capture=True, quiet=True)
    page_marker = page.stdout.strip()
    if page_marker != f"NodeDance fixture {RUN}":
        raise RuntimeError(f"{context}: bind-mounted page marker mismatch: {page_marker!r}")
    volume = run(["docker", "--host", host, "exec", f"nodedance-demo-{RUN}-data-1",
                  "cat", "/data/nodedance-marker"], capture=True, quiet=True)
    volume_marker = volume.stdout.strip()
    if volume_marker != RUN:
        raise RuntimeError(f"{context}: named-volume marker mismatch: {volume_marker!r}")
    redis = run(["docker", "--host", host, "exec", f"nodedance-demo-{RUN}-data-1",
                 "redis-cli", "GET", "nodedance_fixture"], capture=True, quiet=True)
    redis_marker = redis.stdout.strip()
    if redis_marker != RUN:
        raise RuntimeError(f"{context}: Redis persisted value mismatch: {redis_marker!r}")
    print(f"PERSISTENCE PASS ({context}): bind={page_marker!r}; named-volume={volume_marker!r}; redis={redis_marker!r}")


def assert_log_streams(env):
    host = env["NODEDANCE_TEST_DOCKER_HOST"]
    name = f"nd-log-{RUN}"
    container_id = run(["docker", "--host", host, "inspect", "--format", "{{.Id}}", name],
                       env=env, capture=True, quiet=True).stdout.strip()
    tty = run(["docker", "--host", host, "inspect", "--format", "{{.Config.Tty}}", name],
              env=env, capture=True, quiet=True).stdout.strip()
    log_path = run(["docker", "--host", host, "inspect", "--format", "{{.LogPath}}", name],
                   env=env, capture=True, quiet=True).stdout.strip()
    expected_path = f"/var/lib/docker/containers/{container_id}/{container_id}-json.log"
    if tty != "false" or log_path != expected_path:
        raise RuntimeError(f"log fixture must use non-TTY json-file logging; tty={tty!r} path={log_path!r}")

    # Read the isolated Engine's underlying raw JSON log through the NodeDance-
    # owned DIND container. This checks the actual stream metadata instead of
    # inferring stdout/stderr from merged `docker logs` output.
    dind_name = f"nodedance-s00-dind-v{ENGINE}"
    raw = run(["docker", "--host", HOST, "exec", dind_name, "cat", log_path],
              env=env, capture=True, quiet=True).stdout
    entries = [json.loads(line) for line in raw.splitlines() if line.strip()]
    stdout_entries = [entry for entry in entries if entry.get("stream") == "stdout"]
    stderr_entries = [entry for entry in entries if entry.get("stream") == "stderr"]
    stdout_text = "".join(entry.get("log", "") for entry in stdout_entries)
    stderr_text = "".join(entry.get("log", "") for entry in stderr_entries)
    stdout_marker = "stdout fixture UTF-8 你好 NodeDance"
    stderr_marker = "stderr fixture UTF-8 你好 NodeDance"
    if stdout_marker not in stdout_text:
        raise RuntimeError("raw Docker stdout log is missing the UTF-8 fixture marker")
    if stderr_marker not in stderr_text:
        raise RuntimeError("raw Docker stderr log is missing the UTF-8 fixture marker")
    stdout_sample = next(entry["log"].rstrip("\n") for entry in stdout_entries if stdout_marker in entry.get("log", ""))
    stderr_sample = next(entry["log"].rstrip("\n") for entry in stderr_entries if stderr_marker in entry.get("log", ""))

    cli_logs = run(["docker", "--host", host, "logs", name], env=env,
                   capture=True, quiet=True).stdout
    if stdout_marker not in cli_logs or stderr_marker not in cli_logs:
        raise RuntimeError("Docker logs output is missing the stdout/stderr UTF-8 fixture markers")
    evidence = {"driver": "json-file", "tty": False, "raw_path": log_path,
                "stdout_stream": "verified", "stderr_stream": "verified",
                "stdout_sample": stdout_sample, "stderr_sample": stderr_sample,
                "unicode_utf8": "你好", "cli_logs": "verified"}
    print("RAW DOCKER LOG PASS: " + json.dumps(evidence, ensure_ascii=False, sort_keys=True))
    return evidence


before = host_snapshot()
env = test_docker_env()
test_host = env["NODEDANCE_TEST_DOCKER_HOST"]
sentinel_name = f"nd-cleanup-sentinel-{RUN}"
started = False
sentinel_created = False
fixtures_attempted = False
versions = {}
try:
    run([str(ROOT / "scripts/test/dind.sh"), "start", ENGINE], env=os.environ.copy())
    started = True
    versions = assert_locked_compose_selected(env)
    fixtures_attempted = True
    run([str(ROOT / "scripts/test/fixtures.sh"), "create", RUN], env=env)
    assert_persisted_markers(env, "after creation")
    versions["fixture_logs"] = assert_log_streams(env)
    run([str(ROOT / "scripts/test/fixtures.sh"), "fault", RUN, "health-unhealthy"], env=env)
    run([str(ROOT / "scripts/test/fixtures.sh"), "reset", RUN, "health"], env=env)

    # Exercise a real nested-daemon outage. After the daemon returns, explicitly
    # start its Compose services and prove both the host bind and named volume
    # kept their per-run marker values.
    run([str(ROOT / "scripts/test/fixtures.sh"), "fault", RUN, "daemon-outage"], env=env)
    compose("--profile", "demo-worker", "start", env=env)
    assert_persisted_markers(env, "after isolated daemon outage and recovery")

    lock = json.loads((ROOT / "test-images.lock.json").read_text())
    busybox = lock["images"]["busybox"]
    run(["docker", "--host", test_host, "run", "--detach", "--name", sentinel_name,
         "--label", "io.nodedance.test=true", "--label", f"io.nodedance.suite=control-{RUN}",
         "--restart", "unless-stopped", busybox, "sh", "-c", "while true; do sleep 3600; done"])
    sentinel_created = True
    run([str(ROOT / "scripts/test/fixtures.sh"), "clean", RUN], env=env)
    fixtures_attempted = False
    inspected = run(["docker", "--host", test_host, "inspect", "--format",
                     '{{.State.Status}}|{{ index .Config.Labels "io.nodedance.suite" }}', sentinel_name],
                    capture=True, quiet=True).stdout.strip()
    if inspected != f"running|control-{RUN}":
        raise RuntimeError(f"cleanup changed an unrelated suite sentinel: {inspected!r}")
    print(f"FIXTURE CLEANUP PASS: unrelated suite sentinel retained on Engine {ENGINE}")
finally:
    # Failure evidence remains in the run log; cleanup always targets this
    # exact fixture run and the independently labelled control sentinel.
    if fixtures_attempted:
        run([str(ROOT / "scripts/test/fixtures.sh"), "clean", RUN], env=env,
            check=False, capture=True, quiet=True)
    if sentinel_created:
        run(["docker", "--host", test_host, "rm", "--force", sentinel_name],
            check=False, capture=True, quiet=True)
    if started:
        run([str(ROOT / "scripts/test/dind.sh"), "stop", ENGINE],
            env=os.environ.copy(), check=False)

after = host_snapshot()
if before != after:
    raise RuntimeError("host containers changed during isolated fixture lifecycle: " + json.dumps({
        "before_count": len(before), "after_count": len(after),
        "added_ids": sorted(set(after) - set(before)),
        "removed_ids": sorted(set(before) - set(after)),
    }, sort_keys=True))
print("TEST ENVIRONMENT JSON: " + json.dumps(versions, sort_keys=True))
print(f"FIXTURE SELFTEST PASS: Engine{ENGINE}; host-engine={versions['host_engine']}; Compose={versions['compose']} ({versions['compose_plugin']}); fixture run={RUN}; sentinel cleaned; host non-suite containers unchanged ({len(before)} checked)")
