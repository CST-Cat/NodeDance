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


def latest_agent(data, repository):
    # gh --paginate --slurp returns a list of pages, including repositories
    # whose newest Agent release is beyond the first hundred Core releases.
    entries = [entry for page in data for entry in page] if data and isinstance(data[0], list) else data
    valid = [r for r in entries if not r.get('draft') and not r.get('prerelease')
             and re.fullmatch('agent-v' + VERSION, r.get('tag_name', '')) and r.get('published_at')]
    if not valid:
        raise ValueError('publish a stable Agent release before releasing Core')
    selected = max(valid, key=lambda r: (r['published_at'], r['tag_name']))
    tag = selected['tag_name']
    return tag, f'https://github.com/{repository}/releases/tag/{tag}'


def render(component, tag, changelog, releases=None, repository=None):
    if not re.fullmatch(component + '-v' + VERSION, tag):
        raise ValueError(f'invalid {component} tag: {tag}')
    notes = extract(changelog, tag.split('-v', 1)[1])
    if component == 'core':
        if not repository or not re.fullmatch(r'[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+', repository):
            raise ValueError('valid owner/repository required')
        agent, url = latest_agent(releases or [], repository)
        notes = f'Latest Agent / 最新 Agent: [{agent}]({url})\n\n' + notes
    return notes


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument('command', choices=['render', 'promote'])
    p.add_argument('component', choices=['core', 'agent'])
    p.add_argument('tag', help='core-vX.Y.Z or agent-vX.Y.Z')
    p.add_argument('--repository')
    p.add_argument('--releases', type=Path)
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
        else:
            data = json.loads(a.releases.read_text()) if a.releases else []
            notes = render(a.component, a.tag, text, data, a.repository)
            if a.output:
                a.output.write_text(notes)
            else:
                print(notes, end='')
    except (ValueError, OSError, json.JSONDecodeError) as e:
        p.error(str(e))


if __name__ == '__main__':
    main()
