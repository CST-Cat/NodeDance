#!/usr/bin/env python3
"""Validate and render reviewed, bilingual component release notes."""
import argparse
import json
import re
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
VERSION = r'(?:0|[1-9]\d*)\.(?:0|[1-9]\d*)\.(?:0|[1-9]\d*)'


def validate_notes(body):
    headings = list(re.finditer(r'^### (English|简体中文)\s*$', body, re.M))
    if [m[1] for m in headings] != ['English', '简体中文']:
        raise ValueError('notes must contain ### English followed by ### 简体中文')
    sections = [body[headings[0].end():headings[1].start()], body[headings[1].end():]]
    if any(not re.search(r'^- \S', section, re.M) for section in sections):
        raise ValueError('both languages need at least one reviewed changelog item')
    return body.strip() + '\n'


def extract(text, version):
    entries = list(re.finditer(r'^## \[([^\]]+)\][^\n]*$', text, re.M))
    matches = [i for i, entry in enumerate(entries) if entry[1] == version]
    if len(matches) != 1:
        raise ValueError(f'exactly one changelog entry required for {version}')
    i = matches[0]
    return validate_notes(text[entries[i].end():entries[i+1].start() if i+1 < len(entries) else len(text)])


def validate(component, tag, changelog, agent_tag=None):
    if not re.fullmatch(component + '-v' + VERSION, tag):
        raise ValueError(f'invalid {component} tag: {tag}')
    notes = extract(changelog, tag.split('-v', 1)[1])
    if component == 'core':
        if not agent_tag or not re.fullmatch('agent-v' + VERSION, agent_tag):
            raise ValueError('Core requires an explicit agent-vMAJOR.MINOR.PATCH pairing')
    return notes


def render(component, tag, changelog, agent_release=None, repository=None, agent_tag=None):
    notes = validate(component, tag, changelog, agent_tag)
    if component == 'core':
        if not repository or not re.fullmatch(r'[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+', repository):
            raise ValueError('valid owner/repository required')
        if (not isinstance(agent_release, dict) or agent_release.get('tag_name') != agent_tag
                or agent_release.get('draft') is not False or agent_release.get('prerelease') is not False
                or not agent_release.get('published_at')):
            raise ValueError(f'paired Agent {agent_tag} is not a published stable release')
        url = f'https://github.com/{repository}/releases/tag/{agent_tag}'
        notes = f'Agent / 对应 Agent: [{agent_tag}]({url})\n\n' + notes
    return notes


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument('command', choices=['render', 'validate', 'promote'])
    p.add_argument('component', choices=['core', 'agent'])
    p.add_argument('tag', help='core-vX.Y.Z or agent-vX.Y.Z')
    p.add_argument('--repository')
    p.add_argument('--agent-tag', help='exact paired Agent tag for Core')
    p.add_argument('--agent-release', type=Path, help='GitHub API response for the exact paired Agent release')
    p.add_argument('--output', type=Path)
    a = p.parse_args()
    try:
        if not re.fullmatch(a.component + '-v' + VERSION, a.tag):
            raise ValueError('invalid component tag')
        path = ROOT / f'CHANGELOG_{a.component.upper()}.md'
        text = path.read_text()
        if a.command == 'promote':
            version = a.tag.split('-v', 1)[1]
            if re.search(r'^## \[' + re.escape(version) + r'\]', text, re.M):
                raise ValueError('version already exists')
            extract(text, 'Unreleased')
            if text.count('## [Unreleased]') != 1:
                raise ValueError('exactly one Unreleased heading required')
            text = text.replace('## [Unreleased]', '## [Unreleased]\n\n### English\n\n### 简体中文\n\n## [' + version + ']', 1)
            path.write_text(text)
        elif a.command == 'validate':
            validate(a.component, a.tag, text, a.agent_tag)
        else:
            data = json.loads(a.agent_release.read_text()) if a.agent_release else None
            notes = render(a.component, a.tag, text, data, a.repository, a.agent_tag)
            if a.output:
                a.output.write_text(notes)
            else:
                print(notes, end='')
    except (ValueError, OSError, json.JSONDecodeError) as e:
        p.error(str(e))


if __name__ == '__main__':
    main()
