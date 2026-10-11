import importlib.util
from pathlib import Path
import unittest
import tempfile
from unittest.mock import patch

spec = importlib.util.spec_from_file_location('notes', Path(__file__).resolve().parents[1] / 'release-notes.py')
notes = importlib.util.module_from_spec(spec)
spec.loader.exec_module(notes)
BODY = '## [1.2.3]\n\n### English\n- Fix connections.\n\n### 简体中文\n- 修复连接。\n'

class ReleaseNotesTest(unittest.TestCase):
    def test_core_pins_latest_published_agent_across_pages(self):
        def release(tag, date, **kw):
            return dict(tag_name=tag, published_at=date, **kw)
        pages = [[release('core-v9.0.0', '2026-10-10'), release('agent-v2.0.0', '2026-10-11', draft=True)],
                 [release('agent-v1.1.0', '2026-10-09', prerelease=True), release('agent-v1.0.0', '2026-10-08'),
                  release('agent-v0.9.0', '2026-10-07')]]
        rendered = notes.render('core', 'core-v1.2.3', BODY, pages, 'owner/repo')
        self.assertTrue(rendered.startswith('Latest Agent / 最新 Agent: [agent-v1.0.0](https://github.com/owner/repo/releases/tag/agent-v1.0.0)\n'))
        self.assertIn('修复连接', rendered)

    def test_invalid_notes_and_missing_agent_fail(self):
        for body in ['## [Unreleased]\n', BODY.replace('- 修复连接。', ''), BODY + BODY]:
            with self.assertRaises(ValueError):
                notes.render('agent', 'agent-v1.2.3', body)
        for tag in ['v1.2.3', 'core-v1.2.3', 'agent-v01.2.3', 'agent-v1.2.3-rc1']:
            with self.assertRaises(ValueError):
                notes.render('agent', tag, BODY)
        with self.assertRaises(ValueError):
            notes.render('core', 'core-v1.2.3', BODY, [], 'owner/repo')

    def test_promote_keeps_component_history_and_resets_unreleased(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            changelog = root / 'CHANGELOG_AGENT.md'
            changelog.write_text('## [Unreleased]\n\n### English\n- New fix.\n\n### 简体中文\n- 新修复。\n\n' + BODY)
            with patch.object(notes, 'ROOT', root), patch('sys.argv', ['release-notes.py', 'promote', 'agent', 'agent-v1.2.4']):
                notes.main()
            text = changelog.read_text()
            self.assertIn('- New fix.', notes.extract(text, '1.2.4'))
            self.assertEqual(notes.extract(text, '1.2.3'), notes.extract(BODY, '1.2.3'))
            with self.assertRaises(ValueError):
                notes.extract(text, 'Unreleased')

    def test_agent_has_own_bilingual_notes(self):
        self.assertEqual(notes.render('agent', 'agent-v1.2.3', BODY), notes.extract(BODY, '1.2.3'))

if __name__ == '__main__':
    unittest.main()
