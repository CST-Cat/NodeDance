import importlib.util
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location('rehearsal', Path(__file__).resolve().parents[1] / 'release-rehearsal.py')
rehearsal = importlib.util.module_from_spec(spec)
spec.loader.exec_module(rehearsal)
BODY = '## [Unreleased]\n\n### English\n- Fix.\n\n### 简体中文\n- 修复。\n\n## [1.2.3]\n\n### English\n- Fix connections.\n\n### 简体中文\n- 修复连接。\n'

class ReleaseRehearsalTest(unittest.TestCase):
    def test_gate_rejects_missing_rehearsal(self):
        with tempfile.TemporaryDirectory() as directory:
            marker = Path(directory) / 'release-rehearsal'
            marker.write_text('agent agent-v9.9.9\n')
            with patch.object(rehearsal, 'REHEARSAL_MARKER', str(marker)):
                self.assertTrue(rehearsal.passed('agent', 'agent-v9.9.9'))
                self.assertFalse(rehearsal.passed('core', 'agent-v9.9.9'))
                self.assertFalse(rehearsal.passed('agent', 'agent-v1.0.0'))
                self.assertFalse(rehearsal.passed('agent', 'agent-v9.9.9') and marker.read_text().count('\n') != 1)

    def test_check_local_validates_changelog_and_state(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / 'CHANGELOG_AGENT.md').write_text(BODY)
            with patch.object(rehearsal, 'ROOT', root), \
                 patch.object(rehearsal, 'run', side_effect=lambda command, allow_failure=False: ''):
                notes = rehearsal.check_local('agent', 'agent-v1.2.3')
                self.assertIn('修复连接', notes)
                with self.assertRaises(rehearsal.RehearsalError):
                    rehearsal.check_local('agent', 'agent-v1.2.4')
                (root / 'CHANGELOG_AGENT.md').write_text('## [1.2.3]\n\n### English\n- x.\n')
                with self.assertRaises(rehearsal.RehearsalError):
                    rehearsal.check_local('agent', 'agent-v1.2.3')
                (root / 'CHANGELOG_AGENT.md').write_text(BODY)
                with patch.object(rehearsal, 'run', side_effect=lambda command, allow_failure=False: ' M CHANGELOG_AGENT.md'):
                    with self.assertRaises(rehearsal.RehearsalError):
                        rehearsal.check_local('agent', 'agent-v1.2.3')

    def test_check_local_rejects_existing_tags_and_stale_pairing(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / 'CHANGELOG_CORE.md').write_text(BODY)
            (root / 'agent-release.version').write_text('agent-v1.0.0\n')
            def fake_run(command, allow_failure=False):
                if command[:2] == ['git', 'tag']:
                    return 'core-v1.2.3'
                if command[:2] == ['git', 'ls-remote']:
                    return 'refs/tags/core-v1.2.3'
                return ''
            with patch.object(rehearsal, 'ROOT', root), patch.object(rehearsal, 'run', side_effect=fake_run):
                with self.assertRaises(rehearsal.RehearsalError):
                    rehearsal.check_local('core', 'core-v1.2.3', 'agent-v1.0.0')
            (root / 'agent-release.version').write_text('agent-v1.0.1\n')
            with patch.object(rehearsal, 'ROOT', root), patch.object(rehearsal, 'run', side_effect=lambda command, allow_failure=False: ''):
                with self.assertRaises(rehearsal.RehearsalError):
                    rehearsal.check_local('core', 'core-v1.2.3', 'agent-v1.0.0')

    def test_check_remote_requires_newest_stable_pairing_and_missing_release(self):
        listing = [
            {'tag_name': 'agent-v1.0.0', 'draft': False, 'prerelease': False},
            {'tag_name': 'agent-v1.1.0', 'draft': False, 'prerelease': False},
            {'tag_name': 'agent-v1.2.0-rc1', 'draft': False, 'prerelease': True},
            {'tag_name': 'core-v9.0.0', 'draft': False, 'prerelease': False},
        ]
        with patch.object(rehearsal, 'run', side_effect=lambda command, allow_failure=False:
                          'owner/repo' if command[:2] == ['gh', 'repo'] else
                          ('' if allow_failure else __import__('json').dumps(listing))):
            with self.assertRaises(rehearsal.RehearsalError):
                rehearsal.check_remote('core', 'core-v1.2.3', 'agent-v1.0.0')
            self.assertEqual(rehearsal.check_remote('core', 'core-v1.2.3', 'agent-v1.1.0'), 'owner/repo')
            self.assertEqual(rehearsal.check_remote('agent', 'agent-v1.2.3'), 'owner/repo')
            with patch.object(rehearsal, 'run', side_effect=lambda command, allow_failure=False:
                              'owner/repo' if command[:2] == ['gh', 'repo'] else
                              ('{"message":"Not Found"}' if allow_failure else __import__('json').dumps(listing))):
                self.assertEqual(rehearsal.check_remote('core', 'core-v1.2.3', 'agent-v1.1.0'), 'owner/repo')
            with patch.object(rehearsal, 'run', side_effect=lambda command, allow_failure=False:
                              'owner/repo' if command[:2] == ['gh', 'repo'] else
                              ('{"tag_name":"core-v1.2.3"}' if allow_failure else __import__('json').dumps(listing))):
                with self.assertRaises(rehearsal.RehearsalError):
                    rehearsal.check_remote('core', 'core-v1.2.3', 'agent-v1.1.0')

    def test_record_and_passed_round_trip(self):
        with tempfile.TemporaryDirectory() as directory:
            marker = Path(directory) / 'nested' / 'release-rehearsal'
            with patch.object(rehearsal, 'REHEARSAL_MARKER', str(marker)):
                rehearsal.record('agent', 'agent-v1.2.3')
                rehearsal.record('core', 'core-v1.2.3')
                rehearsal.record('agent', 'agent-v1.2.3')
                lines = marker.read_text().splitlines()
                self.assertEqual(lines, ['core core-v1.2.3', 'agent agent-v1.2.3'])
                self.assertTrue(rehearsal.passed('core', 'core-v1.2.3'))

if __name__ == '__main__':
    unittest.main()
