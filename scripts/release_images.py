#!/usr/bin/env python3
"""Verify Agent Docker release images contain the tested native artifact."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import platform
import shutil
import subprocess
import tempfile
import uuid

ROOT = Path(__file__).resolve().parents[1]


def docker(*args):
    return subprocess.check_output(['docker', *args], text=True).strip()


def extract(image, architecture, destination):
    container = docker('create', image)
    try:
        docker('cp', f'{container}:/usr/local/bin/nodedance-agent',
               str(destination / f'nodedance-agent-linux-{architecture}'))
    finally:
        docker('rm', '-v', container)


def verify(image, architecture, version, artifacts):
    info = json.loads(docker('image', 'inspect', image))[0]
    if info['Os'] != 'linux' or info['Architecture'] != architecture:
        raise ValueError('Agent image architecture does not match its native artifact')
    if info['Config']['User'] != '0:0':
        raise ValueError('Agent image must run as root')
    with tempfile.TemporaryDirectory(prefix='nodedance-agent-image-verify-') as directory:
        extracted = Path(directory)
        extract(image, architecture, extracted)
        expected = hashlib.sha256((artifacts / f'nodedance-agent-linux-{architecture}').read_bytes()).digest()
        actual = hashlib.sha256((extracted / f'nodedance-agent-linux-{architecture}').read_bytes()).digest()
        if actual != expected:
            raise ValueError('Agent image binary differs from the tested native artifact')
        output = docker('run', '--rm', '--entrypoint', '/usr/local/bin/nodedance-agent', image, '--version')
        if output != f'NodeDance Agent {version}':
            raise ValueError('Agent image binary version mismatch')
    if docker('run', '--rm', image, '--version') != f'NodeDance Agent {version}':
        raise ValueError('Agent entrypoint did not preserve version execution')
    print(f'Verified Agent linux/{architecture}: artifact bytes, version, root runtime')


def test(architecture, version):
    # A packaging context with no Go or Web sources proves the release image
    # contains the exact Agent binary that passed the native build and tests.
    with tempfile.TemporaryDirectory(prefix='nodedance-agent-release-images-') as directory:
        context = Path(directory)
        artifacts = context / 'dist'
        artifacts.mkdir()
        entrypoint = context / 'deploy/docker/agent-entrypoint.sh'
        entrypoint.parent.mkdir(parents=True)
        shutil.copyfile(ROOT / 'deploy/docker/agent-entrypoint.sh', entrypoint)
        source = os.environ.get('TEST_AGENT_IMAGE', 'nodedance-agent:docker-test')
        extract(source, architecture, artifacts)
        image = f'nodedance-agent-release-test:{uuid.uuid4().hex[:12]}'
        try:
            subprocess.run(['docker', 'build', '--target', 'release', '--platform', f'linux/{architecture}',
                            '-f', str(ROOT / 'deploy/docker/Dockerfile.agent'),
                            '-t', image, str(context)], check=True)
            verify(image, architecture, version, artifacts)
        finally:
            subprocess.run(['docker', 'image', 'rm', image], check=False, stdout=subprocess.DEVNULL)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('command', choices=['verify', 'test'])
    parser.add_argument('--component', choices=['agent'])
    parser.add_argument('--architecture', choices=['amd64', 'arm64'],
                        default={'x86_64': 'amd64', 'aarch64': 'arm64'}.get(platform.machine()))
    parser.add_argument('--version', default='dev')
    parser.add_argument('--image')
    parser.add_argument('--artifacts', type=Path, default=Path('dist'))
    args = parser.parse_args()
    if not args.architecture:
        parser.error('unsupported native architecture')
    if args.command == 'test':
        test(args.architecture, args.version)
    else:
        if args.component != 'agent' or not args.image:
            parser.error('verify requires --component agent and --image')
        docker('pull', args.image)
        verify(args.image, args.architecture, args.version, args.artifacts)


if __name__ == '__main__':
    main()
