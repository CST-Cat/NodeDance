"""Root installers against private fixtures; network, Docker and systemd are mocked."""
import hashlib
import os
from pathlib import Path
import platform
import subprocess
import tempfile
import unittest

SOURCE = Path(__file__).resolve().parents[1] / 'install-agent.sh'
TOKEN = 'a' * 64

@unittest.skipUnless(os.geteuid() == 0, 'root-only installer fixtures require root')
class AgentInstallerTest(unittest.TestCase):
    def run_installer(self, mode, *, bad_checksum=False, enrollment_failure=False, existing_state=False):
        with tempfile.TemporaryDirectory(prefix='nodedance-installer-test-') as directory:
            folder = Path(directory)
            fixtures, tools = folder/'fixtures', folder/'tools'
            fixtures.mkdir(); tools.mkdir()
            state, binary_dir, log = folder/'state', folder/'installed', folder/'actions'
            if existing_state:
                state.mkdir(mode=0o700); (state/'agent.json').write_text('preserve-me')
            agent = fixtures/('nodedance-agent-linux-' + ('arm64' if platform.machine() in ('aarch64','arm64') else 'amd64'))
            agent.write_text('''#!/bin/bash
set -eu
case "$1" in
 prepare-systemd-state) mkdir -m 0700 "$TEST_STATE"; printf 'created:%s\\n' "$(stat -c '%d:%i' "$TEST_STATE")";;
 enroll) read -r supplied; [[ "$supplied" == "$TEST_TOKEN" ]]; printf '{}\\n' > "$TEST_STATE/agent.json"; chmod 0600 "$TEST_STATE/agent.json"; echo enrolled >> "$TEST_LOG"; [[ "$TEST_ENROLL_FAILURE" == 0 ]] || exit 17;;
 validate-systemd-state) [[ -s "$TEST_STATE/agent.json" ]];;
 install-systemd) echo native-started >> "$TEST_LOG";;
 *) exit 19;;
esac
''')
            updater = fixtures/agent.name.replace('nodedance-agent','nodedance-updater')
            updater.write_text('#!/bin/bash\nset -eu\nprintf "updater:%s\\n" "$*" >> "$TEST_LOG"\n')
            for binary in (agent,updater): binary.chmod(0o700)
            (fixtures/'SHA256SUMS').write_text(''.join((('0'*64 if bad_checksum and binary==agent else hashlib.sha256(binary.read_bytes()).hexdigest())+'  '+binary.name+'\n') for binary in (agent,updater)))
            mocks = {
                'curl': '''#!/bin/bash
set -eu
output=""; url=""; status=0
while (($#)); do
 case "$1" in
 --output|-o) output=$2; shift 2;;
 --write-out) status=1; shift 2;;
 https://*) url=$1; shift;;
 *) shift;;
 esac
done
cp "$TEST_FIXTURES/${url##*/}" "$output"
if ((status)); then printf 200; fi
''',
                'systemctl': '''#!/bin/bash
if [[ "$1" == show ]]; then echo not-found; exit 0; fi
exit 1
''',
                'docker': '''#!/bin/bash
set -eu
case "$1" in
 info) exit 0;;
 container) exit 1;;
 pull) printf 'pull:%s\\n' "$2" >> "$TEST_LOG";;
 run)
  [[ " $* " == *" --privileged "* && " $* " == *" --pid=host "* && " $* " == *" --network=host "* ]] || exit 22
  if [[ " $* " == *" enroll "* ]]; then "$TEST_FIXTURES/nodedance-agent-linux-$TEST_ARCH" enroll;
  else echo docker-started >> "$TEST_LOG"; fi;;
 *) exit 21;;
esac
'''
            }
            for name, content in mocks.items():
                p=tools/name; p.write_text(content); p.chmod(0o700)
            text=SOURCE.read_text().replace('readonly STATE_DIR="/var/lib/nodedance-agent"',f'readonly STATE_DIR="{state}"').replace('readonly BIN_DIR="/usr/local/bin"',f'readonly BIN_DIR="{binary_dir}"')
            installer=folder/'installer.sh'; installer.write_text(text)
            env={**os.environ,'PATH':str(tools)+':/usr/bin:/bin','TEST_STATE':str(state),'TEST_LOG':str(log),'TEST_FIXTURES':str(fixtures),'TEST_TOKEN':TOKEN,'TEST_ARCH':agent.name.rsplit('-',1)[1],'TEST_ENROLL_FAILURE':str(int(enrollment_failure))}
            result=subprocess.run(['bash',str(installer),'--release-tag','agent-v1.2.3','--mode',mode,'--server','https://core.example','--token',TOKEN],env=env,stdin=subprocess.DEVNULL,capture_output=True,text=True)
            actions=log.read_text() if log.exists() else ''
            config=state/'agent.json'
            return {'code':result.returncode,'error':result.stderr,'actions':actions,'config':config.read_text() if config.exists() else None,'state_mode':state.stat().st_mode&0o777 if state.exists() else None,'config_mode':config.stat().st_mode&0o777 if config.exists() else None,'native_binary':(binary_dir/'nodedance-agent').exists()}

    def test_native_and_docker_complete_without_terminal_input(self):
        for mode in ('native','docker'):
            with self.subTest(mode=mode):
                r=self.run_installer(mode)
                self.assertEqual(r['code'],0,r['error']); self.assertEqual(r['state_mode'],0o700); self.assertEqual(r['config_mode'],0o600)
                self.assertIn('enrolled',r['actions']); self.assertNotIn(TOKEN,r['actions'])
                self.assertIn('--mode '+('systemd' if mode=='native' else 'docker'),r['actions'])
                self.assertEqual(r['native_binary'],mode=='native')
                self.assertIn('native-started' if mode=='native' else 'docker-started',r['actions'])
                if mode=='docker': self.assertIn('pull:ghcr.io/cst-cat/nodedance-agent:agent-v1.2.3',r['actions'])

    def test_checksum_failure_and_existing_state_cannot_mutate_install(self):
        for mode in ('native','docker'):
            for failure in ('bad_checksum','existing_state'):
                with self.subTest(mode=mode,failure=failure):
                    r=self.run_installer(mode,**{failure:True}); self.assertNotEqual(r['code'],0)
                    self.assertEqual(r['actions'],''); self.assertFalse(r['native_binary'])
                    self.assertEqual(r['config'],'preserve-me' if failure=='existing_state' else None)

    def test_uncertain_enrollment_keeps_protected_config_and_stops_startup(self):
        for mode in ('native','docker'):
            with self.subTest(mode=mode):
                r=self.run_installer(mode,enrollment_failure=True)
                self.assertNotEqual(r['code'],0); self.assertEqual(r['config'],'{}\n'); self.assertEqual(r['state_mode'],0o700); self.assertEqual(r['config_mode'],0o600)
                self.assertNotIn('started',r['actions']); self.assertNotIn('updater:',r['actions']); self.assertIn('preserved',r['error'])
