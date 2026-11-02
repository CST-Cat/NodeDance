#!/bin/sh
set -eu
rsync_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
exec python3 "$rsync_root/scripts/vendor-rsync.py" --build "$@"
