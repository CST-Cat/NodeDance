#!/usr/bin/env python3
"""Opt-in integration test for a native Core with a root Docker Agent.

Build the native Core binary and the Agent image first. Only a disposable Core
state directory, Agent container, and temporary host directory are used.
"""
import json
import os
from pathlib import Path
import shutil
import socket
import stat
import subprocess
import tempfile
import time
import urllib.error
import urllib.parse
import urllib.request
import uuid

CORE_BINARY = Path(os.environ.get('TEST_CORE_BINARY', '.build/nodedance')).resolve()
AGENT = os.environ.get('TEST_AGENT_IMAGE', 'nodedance-agent:docker-test')


def docker(*args, input=None, check=True):
    result = subprocess.run(['docker', *args], input=input, text=True, capture_output=True)
    if check and result.returncode:
        raise RuntimeError('Docker command failed: ' + result.stderr.strip())
    return result


def eventually(fn, seconds=60):
    deadline = time.monotonic() + seconds
    while time.monotonic() < deadline:
        try:
            result = fn()
            if result:
                return result
        except (OSError, urllib.error.URLError, ValueError):
            pass
        time.sleep(0.25)
    raise AssertionError('Timed out waiting for Core/API readiness')


def main():
    if not CORE_BINARY.is_file():
        raise SystemExit(f'native Core binary not found: {CORE_BINARY}')

    suffix = uuid.uuid4().hex[:12]
    agent = f'nodedance-agent-test-{suffix}'
    root = Path(tempfile.mkdtemp(prefix='nodedance-agent-docker-test-'))
    state = root / 'agent-state'
    state.mkdir(mode=0o700)
    core_data = root / 'core-data'
    core_data.mkdir(mode=0o700)
    core_log_path = root / 'core.log'
    core_log = core_log_path.open('w')
    with socket.socket() as sock:
        sock.bind(('127.0.0.1', 0))
        port = sock.getsockname()[1]
    origin = f'http://127.0.0.1:{port}'
    cookies = {}
    csrf = ''
    core_process = None

    def api(path, body=None):
        nonlocal csrf
        headers = {'Origin': origin, 'Cookie': '; '.join(f'{k}={v}' for k, v in cookies.items())}
        if body is not None:
            headers.update({'Content-Type': 'application/json', 'X-CSRF-Token': csrf})
        request = urllib.request.Request(origin + path, headers=headers,
                                        data=json.dumps(body).encode() if body is not None else None)
        with urllib.request.urlopen(request, timeout=10) as response:
            for cookie in response.headers.get_all('Set-Cookie', []):
                name, value = cookie.split(';', 1)[0].split('=', 1)
                cookies[name] = value
            result = json.load(response)
        csrf = result.get('csrfToken', result.get('token', csrf))
        return result

    def start_core():
        nonlocal core_process
        env = {**os.environ, 'NODEDANCE_PUBLIC_ORIGIN': origin, 'NODEDANCE_TRUSTED_PROXIES': ''}
        core_process = subprocess.Popen(
            [str(CORE_BINARY), 'serve', '--dev', '--listen', f'127.0.0.1:{port}', '--data-dir', str(core_data)],
            env=env, stdout=core_log, stderr=subprocess.STDOUT,
        )
        eventually(lambda: core_process.poll() is None and api('/api/v1/health').get('status') == 'ok')

    def stop_core():
        nonlocal core_process
        if core_process is not None:
            core_process.terminate()
            try:
                core_process.wait(timeout=10)
            except subprocess.TimeoutExpired:
                core_process.kill()
                core_process.wait(timeout=5)
            core_process = None

    host_args = ['--privileged', '--pid=host', '--network=host', '--mount', 'type=bind,src=/,dst=/host,readonly']
    config = str(state / 'agent.json')
    (root / 'credential-link').symlink_to(config)
    try:
        assert docker('run', '--rm', AGENT, '--version').stdout.startswith('NodeDance Agent ')
        assert docker('run', '--rm', AGENT, 'run', check=False).returncode != 0
        assert docker('run', '--rm', '--user=10001:10001', AGENT, 'run', check=False).returncode != 0
        assert docker('run', '--rm', '--user=10001:10001', '--entrypoint=/usr/local/bin/nodedance-agent', AGENT, 'update', check=False).returncode != 0

        start_core()
        credential_path = core_data / 'initial-admin.json'
        saved = credential_path.read_text()
        creds = json.loads(saved)
        assert stat.S_IMODE(core_data.stat().st_mode) == 0o700
        assert stat.S_IMODE(credential_path.stat().st_mode) == 0o600
        assert credential_path.stat().st_uid == os.getuid()
        assert '<html' in urllib.request.urlopen(origin, timeout=10).read().decode().lower()
        stop_core()
        start_core()
        assert credential_path.read_text() == saved
        api('/api/v1/auth/csrf')
        api('/api/v1/auth/login', creds)
        assert not credential_path.exists()

        enrollment = api('/api/v1/agents/enrollments', {'displayName': 'Docker integration', 'coreUrl': origin})
        for field, mode in [('installCommand', 'native'), ('dockerInstallCommand', 'docker')]:
            command = enrollment[field]
            assert 'raw.githubusercontent.com/CST-Cat/NodeDance/main/scripts/install-agent.sh' in command
            assert '--release-tag' not in command
            assert 'agent-v' not in command
            assert f'--mode {mode}' in command
            assert f'--token {enrollment["token"]}' in command
            assert f'--server {origin}' in command
            assert 'sudo bash' in command

        # The production Agent refuses state directories not already root-owned.
        docker('run', '--rm', '--user=0:0', '--entrypoint=sh', '--mount', f'type=bind,src={root},dst=/fixture', AGENT,
               '-c', 'chown 0:0 /fixture /fixture/agent-state && chmod 0700 /fixture /fixture/agent-state')
        docker('run', '--rm', '-i', *host_args, AGENT, 'enroll', '--server', origin, '--dev',
               '--token-stdin', '--config', config, input=enrollment['token'] + '\n')
        modes = docker('run', '--rm', '--user=0:0', '--entrypoint=stat', '--mount', f'type=bind,src={state},dst=/state', AGENT,
                       '-c', '%a:%u', '/state', '/state/agent.json').stdout.splitlines()
        assert modes == ['700:0', '600:0']
        docker('run', '-d', '--name', agent, *host_args, AGENT, 'run', '--config', config)
        node_id = enrollment['nodeId']

        def online():
            nodes = api('/api/v1/nodes')['nodes']
            return next((n for n in nodes if n['nodeId'] == node_id and n['status'] == 'online'), None)

        node = eventually(online)
        assert node['runtimePermissions']['effectiveUid'] == 0
        host_metric = eventually(lambda: api(f'/api/v1/nodes/{node_id}/metrics').get('metrics', {}).get('system', {}).get('hostname', {}).get('value'))
        assert host_metric == socket.gethostname()
        hostname = api(f'/api/v1/nodes/{node_id}/files/text?path=/etc/hostname')['text'].strip()
        assert hostname == Path('/etc/hostname').read_text().strip()
        for protected in (config, str(root / 'credential-link'), '/../etc/passwd', '/proc/1/status', '/sys', '/run'):
            try:
                operation = 'text' if protected == str(root / 'credential-link') else 'stat'
                api(f'/api/v1/nodes/{node_id}/files/{operation}?path=' + urllib.parse.quote(protected))
                raise AssertionError('Protected host path was exposed: ' + protected)
            except urllib.error.HTTPError as error:
                assert error.code in (400, 403), error.code
        docker('restart', '-t', '10', agent)
        eventually(lambda: (n := online()) and n['generation'] > node['generation'])
        assert api('/api/v1/health').get('status') == 'ok'
        print('Agent Docker integration passed against native Core: protected state, enrollment, host metrics/files, path protection, and reconnect.')
    finally:
        docker('rm', '-f', agent, check=False)
        stop_core()
        core_log.close()
        # The Agent fixture deliberately owns its config as root. Restore access
        # to this test's disposable directory before removing it as the runner.
        docker('run', '--rm', '--user=0:0', '--entrypoint=sh', '--mount', f'type=bind,src={root},dst=/cleanup', AGENT,
               '-c', 'chmod -R u+rwX /cleanup && chown -R "$1:$2" /cleanup', 'cleanup', str(os.getuid()), str(os.getgid()), check=False)
        shutil.rmtree(root, ignore_errors=True)


if __name__ == '__main__':
    main()
