#!/usr/bin/env python3
"""Opt-in integration test against local images on Linux rootful Docker.

Build both deploy/docker/Dockerfile.* images with :docker-test tags first.
Only disposable test containers, a volume and a temporary host directory are used.
"""
import json
import os
from pathlib import Path
import shutil
import socket
import subprocess
import tempfile
import time
import urllib.error
import urllib.parse
import urllib.request
import uuid

CORE = os.environ.get('TEST_CORE_IMAGE', 'nodedance-core:docker-test')
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
    raise AssertionError('Timed out waiting for container/API readiness')


def main():
    suffix = uuid.uuid4().hex[:12]
    core, agent, volume = [f'nodedance-docker-test-{x}-{suffix}' for x in ('core', 'agent', 'data')]
    root = Path(tempfile.mkdtemp(prefix='nodedance-docker-test-'))
    state = root / 'state'
    state.mkdir(mode=0o700)
    with socket.socket() as sock:
        sock.bind(('127.0.0.1', 0))
        port = sock.getsockname()[1]
    origin = f'http://127.0.0.1:{port}'
    cookies = {}
    csrf = ''

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
        docker('run', '-d', '--name', core, '--read-only', '--cap-drop=ALL',
               '--security-opt=no-new-privileges:true', '--tmpfs', '/tmp:mode=1777',
               '-p', f'127.0.0.1:{port}:8180', '-v', volume + ':/var/lib/nodedance', CORE)
        eventually(lambda: api('/api/v1/health').get('status') == 'ok')

    host_args = ['--privileged', '--pid=host', '--network=host', '--mount', 'type=bind,src=/,dst=/host,readonly']
    config = str(state / 'agent.json')
    (root / 'credential-link').symlink_to(config)
    try:
        assert docker('run', '--rm', AGENT, '--version').stdout.startswith('NodeDance Agent ')
        assert docker('run', '--rm', AGENT, 'run', check=False).returncode != 0
        assert docker('run', '--rm', '--user=10001:10001', AGENT, 'run', check=False).returncode != 0
        docker('volume', 'create', volume)
        start_core()
        saved = docker('exec', core, 'cat', '/var/lib/nodedance/initial-admin.json').stdout
        creds = json.loads(saved)
        permissions = docker('exec', core, 'stat', '-c', '%a:%u', '/var/lib/nodedance', '/var/lib/nodedance/initial-admin.json').stdout.splitlines()
        assert permissions == ['700:10001', '600:10001']
        logs = docker('logs', core)
        assert creds['username'] not in logs.stdout + logs.stderr and creds['password'] not in logs.stdout + logs.stderr
        assert docker('exec', core, 'id', '-u').stdout.strip() == '10001'
        assert '<html' in urllib.request.urlopen(origin, timeout=10).read().decode().lower()
        docker('stop', '-t', '10', core)
        docker('rm', core)
        start_core()
        assert docker('exec', core, 'cat', '/var/lib/nodedance/initial-admin.json').stdout == saved
        api('/api/v1/auth/csrf')
        api('/api/v1/auth/login', creds)
        assert docker('exec', core, 'test', '!', '-e', '/var/lib/nodedance/initial-admin.json', check=False).returncode == 0
        enrollment = api('/api/v1/agents/enrollments', {'displayName': 'Docker integration', 'coreUrl': 'https://docker-test.example'})
        assert 'install-agent.sh' in enrollment['installCommand']
        assert 'docker run' in enrollment['dockerInstallCommand']
        assert '--token-stdin' in enrollment['dockerInstallCommand']
        assert enrollment['token'] not in enrollment['dockerInstallCommand']
        # Root Agent state must also have protected, root-owned ancestors.
        # The temporary fixture starts out owned by the CI runner.
        docker('run', '--rm', '--user=0:0', '--entrypoint=sh', '--mount', f'type=bind,src={root},dst=/fixture', CORE,
               '-c', 'chown 0:0 /fixture /fixture/state && chmod 0700 /fixture /fixture/state')
        docker('run', '--rm', '-i', *host_args, AGENT, 'enroll', '--server', origin, '--dev',
               '--token-stdin', '--config', config, input=enrollment['token'] + '\n')
        modes = docker('run', '--rm', '--user=0:0', '--entrypoint=stat', '--mount', f'type=bind,src={state},dst=/state', CORE,
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
        # A host file is returned from the host root, not the container root.
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
        assert docker('exec', core, 'curl', '--fail', '--silent', 'http://127.0.0.1:8180/api/v1/health').returncode == 0
        print('Docker integration passed: Core non-root/read-only, Web/health, private generated credentials, volume persistence/login cleanup; Agent root enrollment, host metrics/filesystem, traversal/symlink/credential protection and reconnect.')
    finally:
        for container in (agent, core):
            docker('rm', '-f', container, check=False)
        docker('volume', 'rm', volume, check=False)
        docker('run', '--rm', '--user=0:0', '--entrypoint=sh', '--mount', f'type=bind,src={root},dst=/cleanup', CORE,
               '-c', 'rm -rf /cleanup/state && chown "$1:$2" /cleanup', 'cleanup', str(os.getuid()), str(os.getgid()), check=False)
        shutil.rmtree(root)


if __name__ == '__main__':
    main()
