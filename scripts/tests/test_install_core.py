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
    def install(self, body):
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
            target = root / 'installed'
            target.mkdir()
            (target / 'nodedance').write_text('previous executable')
            result = subprocess.run(['bash', str(INSTALLER), '--release-tag', 'core-v1.2.3', '--install-dir', str(target)], env={**os.environ, 'PATH': str(fakebin) + ':' + os.environ['PATH'], 'FIXTURES': str(fixtures)}, capture_output=True, text=True)
            return result.returncode, (target / 'nodedance').read_text(), result.stderr

    def test_valid_native_binary_installs(self):
        code, binary, error = self.install("echo 'NodeDance core-v1.2.3'")
        self.assertEqual(code, 0, error)
        self.assertIn('core-v1.2.3', binary)

    def test_wrong_or_unexecutable_binary_preserves_existing_install(self):
        for body in ("echo 'NodeDance core-v9.9.9'", 'exit 17'):
            with self.subTest(body=body):
                code, binary, error = self.install(body)
                self.assertNotEqual(code, 0, error)
                self.assertEqual(binary, 'previous executable')
