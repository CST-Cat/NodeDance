"""Run the real installer against local release fixtures; never access the network."""
import hashlib
import os
from pathlib import Path
import platform
import subprocess
import tarfile
import tempfile
import unittest

INSTALLER = Path(__file__).resolve().parents[1] / 'install-core.sh'

@unittest.skipUnless(platform.system() == 'Linux' and platform.machine() in ('aarch64', 'arm64', 'x86_64', 'amd64'), 'Linux installer')
class CoreInstallerTest(unittest.TestCase):
    def install(self, body, existing_unit=None, repeat=False, fail_start=False):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            fixtures = root / 'fixtures'
            fixtures.mkdir()
            arch = 'arm64' if platform.machine() in ('aarch64', 'arm64') else 'amd64'
            name = f'nodedance-linux-{arch}'
            binary = fixtures / name
            binary.write_text('#!/bin/sh\n' + body + '\n')
            binary.chmod(0o755)
            archive = fixtures / (name + '.tar.gz')
            with tarfile.open(archive, 'w:gz') as package:
                package.add(binary, arcname=name)
            (fixtures / 'SHA256SUMS').write_text(hashlib.sha256(archive.read_bytes()).hexdigest() + '  ' + archive.name + '\n')
            fakebin = root / 'tools'
            fakebin.mkdir()
            curl = fakebin / 'curl'
            curl.write_text('''#!/bin/bash
set -eu
output=""; url=""
while (($#)); do
 case "$1" in
 --output) output=$2; shift 2;;
 https://*) url=$1; shift;;
 *) shift;;
 esac
done
cp -- "$FIXTURES/${url##*/}" "$output"
''')
            curl.chmod(0o755)
            systemctl = fakebin / 'systemctl'
            systemctl.write_text('''#!/bin/bash
set -eu
printf '%s\\n' "$*" >> "$SYSTEMCTL_LOG"
case "$1" in
  is-active) [[ -f "$SYSTEMCTL_STATE" ]] ;;
  start|restart) [[ "${FAIL_START:-0}" != 1 ]] || exit 1; touch "$SYSTEMCTL_STATE" ;;
esac
''')
            systemctl.chmod(0o755)
            target = root / 'installed'
            target.mkdir()
            (target / 'nodedance').write_text('previous executable')
            data = root / 'data'
            data.mkdir(mode=0o700)
            (data / 'credentials.sqlite').write_text('existing account data')
            units = root / 'systemd'
            units.mkdir()
            unit = units / 'nodedance.service'
            if existing_unit is not None:
                unit.write_text(existing_unit)
            log = root / 'systemctl.log'
            state = root / 'service.active'
            if existing_unit and '# NodeDanceCoreUnit=1' in existing_unit:
                state.touch()
            command = ['bash', str(INSTALLER), '--release-tag', 'core-v1.2.3',
                       '--install-dir', str(target), '--data-dir', str(data), '--unit-dir', str(units)]
            env = {**os.environ, 'PATH': str(fakebin) + ':' + os.environ['PATH'],
                   'FIXTURES': str(fixtures), 'SYSTEMCTL_LOG': str(log),
                   'SYSTEMCTL_STATE': str(state), 'FAIL_START': '1' if fail_start else '0'}
            result = subprocess.run(command, env=env, capture_output=True, text=True)
            if repeat and result.returncode == 0:
                result = subprocess.run(command, env=env, capture_output=True, text=True)
            return {
                'code': result.returncode,
                'binary': (target / 'nodedance').read_text(),
                'error': result.stderr,
                'unit': unit.read_text() if unit.exists() else None,
                'data': (data / 'credentials.sqlite').read_text(),
                'log': log.read_text() if log.exists() else '',
                'stdout': result.stdout,
            }

    def test_valid_native_binary_installs(self):
        result = self.install("echo 'NodeDance core-v1.2.3'", repeat=True)
        self.assertEqual(result['code'], 0, result['error'])
        self.assertIn('core-v1.2.3', result['binary'])
        self.assertIn('# NodeDanceCoreUnit=1', result['unit'])
        self.assertIn('User=root\nGroup=root', result['unit'])
        self.assertIn('EnvironmentFile=-/etc/default/nodedance', result['unit'])
        self.assertIn('Environment=NODEDANCE_LISTEN=0.0.0.0:8180', result['unit'])
        self.assertIn('Environment=NODEDANCE_DATA_DIR=', result['unit'])
        self.assertIn('ExecStart=', result['unit'])
        self.assertEqual(result['data'], 'existing account data')
        self.assertEqual(result['log'].splitlines(), [
            'daemon-reload', 'enable nodedance.service', 'is-active --quiet nodedance.service', 'start nodedance.service',
            'daemon-reload', 'enable nodedance.service', 'is-active --quiet nodedance.service', 'restart nodedance.service',
        ])
        self.assertNotIn('existing account data', result['stdout'])

    def test_wrong_or_unexecutable_binary_preserves_existing_install(self):
        for body in ("echo 'NodeDance core-v9.9.9'", 'exit 17'):
            with self.subTest(body=body):
                result = self.install(body)
                self.assertNotEqual(result['code'], 0, result['error'])
                self.assertEqual(result['binary'], 'previous executable')
                self.assertIsNone(result['unit'])

    def test_refuses_unmanaged_unit_without_changing_binary_or_data(self):
        unmanaged = '[Unit]\nDescription=Some other service\n'
        result = self.install("echo 'NodeDance core-v1.2.3'", existing_unit=unmanaged)
        self.assertNotEqual(result['code'], 0)
        self.assertIn('not owned by NodeDance', result['error'])
        self.assertEqual(result['binary'], 'previous executable')
        self.assertEqual(result['unit'], unmanaged)
        self.assertEqual(result['data'], 'existing account data')

    def test_failed_service_start_is_reported_without_printing_credentials(self):
        result = self.install("echo 'NodeDance core-v1.2.3'", fail_start=True)
        self.assertNotEqual(result['code'], 0)
        self.assertIn('could not start nodedance.service', result['error'])
        self.assertNotIn('existing account data', result['stdout'])
