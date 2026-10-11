#!/usr/bin/env python3
"""Trim verified Google Fonts Latin WOFF2 sources to NodeDance's UI profile.

Requires fonttools and brotli. Input sources stay outside the repository.
"""
import argparse
import hashlib
import json
import io
from pathlib import Path

from fontTools import subset
from fontTools.ttLib import TTFont
from fontTools.varLib.instancer import instantiateVariableFont

PROFILES = {
    'eb-garamond-400.woff2': ('88603384163d301aebd5bd769832a8bfe3c9004ec24178417b9817f0ad32c63b', 400),
    'inter-400.woff2': ('3100e775e8616cd2611beecfa23a4263d7037586789b43f035236a2e6fbd4c62', (400, 500)),
    'jetbrains-mono-400.woff2': ('14425ba9c695763c1547f48a206b7aa60350a33ae23de09f0407877f3fcd89eb', 400),
}
# Preserve all printable ASCII for dynamic names, versions, paths and shell output,
# Latin-1 accented text, and the punctuation used by both supported locales.
UNICODES = set(range(0x20, 0x100)) | {
    0x131, 0x152, 0x153, 0x2BC, 0x2010, 0x2011, 0x2013, 0x2014,
    0x2018, 0x2019, 0x201C, 0x201D, 0x2022, 0x2026, 0x2032, 0x2033,
    0x20AC, 0x2122, 0x2191, 0x2193, 0x2212,
}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--source-dir', type=Path, required=True)
    parser.add_argument('--output-dir', type=Path, default=Path(__file__).resolve().parents[1] / 'web/public/fonts')
    args = parser.parse_args()
    args.output_dir.mkdir(parents=True, exist_ok=True)
    report = []
    for name, (digest, weight) in PROFILES.items():
        source = args.source_dir / name
        data = source.read_bytes()
        if hashlib.sha256(data).hexdigest() != digest:
            raise SystemExit(f'{name}: source checksum mismatch')
        font = TTFont(source)
        if 'fvar' in font:
            instantiateVariableFont(font, {'wght': weight}, inplace=True)
        # Normalize glyph names after instancing; optimized HVAR can add fixed
        # spacing glyphs whose lazy gvar keys still refer to the source order.
        font.recalcTimestamp = False
        normalized = io.BytesIO()
        font.save(normalized)
        normalized.seek(0)
        font = TTFont(normalized)
        options = subset.Options()
        options.layout_features = ['kern', 'liga', 'clig', 'calt', 'mark', 'mkmk', 'locl', 'tnum']
        if name.startswith('jetbrains'):
            # The terminal draws individual cells and has no ligature addon.
            options.layout_features = ['kern', 'mark', 'mkmk', 'locl', 'tnum']
        options.name_IDs = [0, 1, 2, 3, 4, 5, 6, 13, 14]
        options.recalc_timestamp = False
        cutter = subset.Subsetter(options=options)
        cutter.populate(unicodes=UNICODES)
        cutter.subset(font)
        font.flavor = 'woff2'
        font.recalcTimestamp = False
        target = args.output_dir / name
        font.save(target, reorderTables=True)
        result = TTFont(target)
        expected = UNICODES & set(TTFont(source).getBestCmap())
        assert expected <= set(result.getBestCmap()), f'{name}: requested glyphs lost'
        if name.startswith('inter'):
            axis = result['fvar'].axes[0]
            assert (axis.minValue, axis.maxValue) == (400, 500)
        else:
            assert 'fvar' not in result
        report.append({'file': name, 'before': len(data), 'after': target.stat().st_size, 'glyphs': result['maxp'].numGlyphs})
    print(json.dumps(report, indent=2))


if __name__ == '__main__':
    main()
