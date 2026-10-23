#!/usr/bin/env python3
"""Build the embedded GeoNames directory from downloaded official source files.

Download cities15000.zip, admin1CodesASCII.txt and readme.txt from
https://download.geonames.org/export/dump/ before running this script. No runtime
network lookup, project version change or production access is performed here.
"""
import argparse
import datetime
import gzip
import hashlib
import json
import pathlib
import re
import zipfile

ROOT = pathlib.Path(__file__).resolve().parents[1]
parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument('cities_zip', type=pathlib.Path)
parser.add_argument('admin1', type=pathlib.Path)
parser.add_argument('readme', type=pathlib.Path)
parser.add_argument('--check', action='store_true')
args = parser.parse_args()
countries_source = (ROOT / 'internal/core/dashboard/countries.go').read_text()
codes = set(re.search(r'strings.Fields\("([A-Z ]+)"\)', countries_source)[1].split())
regions = {}
for line in args.admin1.read_text().splitlines():
    fields = line.split('\t')
    regions[fields[0]] = fields[1]
rows = []
with zipfile.ZipFile(args.cities_zip) as archive:
    for line in archive.read('cities15000.txt').decode().splitlines():
        fields = line.split('\t')
        if fields[8] not in codes:
            continue
        region = regions.get(fields[8] + '.' + fields[10], '')
        row = dict(id=fields[0], countryCode=fields[8], city=fields[1],
                   region=region, latitude=float(fields[4]), longitude=float(fields[5]),
                   search=' '.join([fields[1], fields[2], fields[3], region, fields[8]]).lower())
        rows.append((int(fields[14]), row))
rows.sort(key=lambda row: (-row[0], row[1]['id']))
payload = json.dumps([row[1] for row in rows], ensure_ascii=False,
                     separators=(',', ':')).encode()
compressed = gzip.compress(payload, mtime=0)
output = ROOT / 'internal/core/geolocation/cities.json.gz'
source_hash = hashlib.sha256(args.cities_zip.read_bytes()).hexdigest()
if args.check:
    # Compare decompressed bytes as gzip headers may differ across Python versions.
    if gzip.decompress(output.read_bytes()) != payload:
        raise SystemExit('City directory differs; rebuild with the reviewed source data.')
    notice = (ROOT / 'internal/core/geolocation/cities-NOTICE.txt').read_text()
    if source_hash not in notice or f'Records: {len(rows)}' not in notice:
        raise SystemExit('City source notice differs.')
    print(f'Verified {len(rows)} city records against the official source files.')
else:
    output.write_bytes(compressed)
    notice = (
        'GeoNames city directory, CC BY 4.0. https://www.geonames.org/\n'
        'License: https://creativecommons.org/licenses/by/4.0/\n'
        'Source: https://download.geonames.org/export/dump/cities15000.zip\n'
        'Regions: https://download.geonames.org/export/dump/admin1CodesASCII.txt\n'
        'Extracted city IDs, names, aliases, country codes, regions and WGS84 coordinates; '
        'sorted by population for bounded local search. Includes countries/regions supported by the Core country contract.\n'
        f'Retrieved {datetime.date.today().isoformat()}\n'
        f'Source ZIP SHA256: {source_hash}\nRecords: {len(rows)}\n\n'
        + args.readme.read_text())
    for path in ['internal/core/geolocation/cities-NOTICE.txt',
                 'web/public/licenses/city-locations-notices.txt']:
        (ROOT / path).write_text(notice)
    print(f'Wrote {len(rows)} city records and source notices.')
