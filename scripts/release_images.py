#!/usr/bin/env python3
"""Verify release images contain the tested artifacts; exercise packaging locally."""
import argparse
import hashlib
import json
from pathlib import Path
import platform
import shutil
import subprocess
import tempfile
import uuid

ROOT = Path(__file__).resolve().parents[1]


def docker(*args):
    return subprocess.check_output(['docker', *args], text=True).strip()


def binaries(component):
    return ['nodedance-agent', 'nodedance-updater'] if component == 'agent' else ['nodedance']


def extract(image, component, architecture, destination):
    container = docker('create', image)
    try:
        for binary in binaries(component):
            docker('cp', f'{container}:/usr/local/bin/{binary}',
                   str(destination / f'{binary}-linux-{architecture}'))
    finally:
        docker('rm', '-v', container)


def verify(image, component, architecture, version, artifacts):
    info = json.loads(docker('image', 'inspect', image))[0]
    if info['Os'] != 'linux' or info['Architecture'] != architecture:
        raise ValueError('release image architecture does not match its native artifacts')
    expected_user = '0:0' if component == 'agent' else '10001:10001'
    if info['Config']['User'] != expected_user:
        raise ValueError('release image changed its runtime identity')
    with tempfile.TemporaryDirectory(prefix='nodedance-image-verify-') as directory:
        extracted = Path(directory)
        extract(image, component, architecture, extracted)
        for binary in binaries(component):
            name = f'{binary}-linux-{architecture}'
            expected = hashlib.sha256((artifacts / name).read_bytes()).digest()
            actual = hashlib.sha256((extracted / name).read_bytes()).digest()
            if actual != expected:
                raise ValueError(f'image binary differs from tested artifact: {name}')
            prefix = {'nodedance': 'NodeDance', 'nodedance-agent': 'NodeDance Agent',
                      'nodedance-updater': 'NodeDance Updater'}[binary]
            output = docker('run', '--rm', '--entrypoint', f'/usr/local/bin/{binary}', image, '--version')
            if output != f'{prefix} {version}':
                raise ValueError(f'image binary version mismatch: {binary}')
    if component == 'agent':
        if docker('run', '--rm', image, '--version') != f'NodeDance Agent {version}':
            raise ValueError('Agent entrypoint did not preserve version execution')
    print(f'Verified {component} linux/{architecture}: artifact bytes, version, runtime identity')


def test(architecture, version):
    # A context with no Go or Web sources proves that release packaging cannot
    # silently recompile either product. Both targets share their runtime stage.
    with tempfile.TemporaryDirectory(prefix='nodedance-release-images-') as directory:
        context = Path(directory)
        artifacts = context / 'dist'
        artifacts.mkdir()
        entrypoint = context / 'deploy/docker/agent-entrypoint.sh'
        entrypoint.parent.mkdir(parents=True)
        shutil.copyfile(ROOT / 'deploy/docker/agent-entrypoint.sh', entrypoint)
        for component in ['core', 'agent']:
            source = f'nodedance-{component}:docker-test'
            extract(source, component, architecture, artifacts)
            image = f'nodedance-release-test:{component}-{uuid.uuid4().hex[:12]}'
            try:
                subprocess.run(['docker', 'build', '--target', 'release', '--platform', f'linux/{architecture}',
                                '-f', str(ROOT / f'deploy/docker/Dockerfile.{component}'),
                                '-t', image, str(context)], check=True)
                verify(image, component, architecture, version, artifacts)
            finally:
                subprocess.run(['docker', 'image', 'rm', image], check=False, stdout=subprocess.DEVNULL)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('command', choices=['verify', 'test'])
    parser.add_argument('--component', choices=['core', 'agent'])
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
        if not args.component or not args.image:
            parser.error('verify requires --component and --image')
        # Digest-pushed images are not necessarily present in the local daemon.
        docker('pull', args.image)
        verify(args.image, args.component, args.architecture, args.version, args.artifacts)


if __name__ == '__main__':
    main()
