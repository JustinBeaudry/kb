#!/bin/sh
# Use the full-module -coverpkg=./... profile, never summed per-package totals.
set -eu
[ "$#" -ge 1 ] && [ "$#" -le 2 ] || { echo 'usage: check-memory-coverage.sh PROFILE [MINIMUM_PERCENT]' >&2; exit 2; }
profile=$1
minimum=${2:-80}
root=$(CDPATH= cd -- "$(dirname -- "$0")/../server" && pwd)
python3 - "$profile" "$minimum" "$root" <<'PY'
import pathlib, re, sys
profile, minimum, root = pathlib.Path(sys.argv[1]), float(sys.argv[2]), pathlib.Path(sys.argv[3])
if not 80 <= minimum <= 100:
    raise SystemExit('minimum must be between 80 and 100 percent')
module = next(line.split()[1] for line in (root / 'go.mod').read_text().splitlines() if line.startswith('module '))
blocks = {}
excluded = set()
lines = profile.read_text().splitlines()
if not lines or lines[0] not in ('mode: atomic', 'mode: count', 'mode: set'):
    raise SystemExit('invalid coverage mode')
for line in lines[1:]:
    location, statements, hits = line.rsplit(' ', 2)
    filename = location.rsplit(':', 1)[0]
    prefix = module + '/'
    if not filename.startswith(prefix):
        raise SystemExit('coverage includes an unexpected module')
    source = root / filename[len(prefix):]
    if not source.is_file() or not source.resolve().is_relative_to(root.resolve()):
        raise SystemExit('coverage source is unavailable')
    # The generator declaration, rather than directories or low coverage,
    # determines the only exclusion. Maintained commands and adapters count.
    if re.search(r'^// Code generated .* DO NOT EDIT\.$', source.read_text().split('package ', 1)[0], re.M):
        excluded.add(filename)
        continue
    count, covered = int(statements), int(hits) > 0
    if count < 0 or int(hits) < 0:
        raise SystemExit('negative coverage block')
    if location in blocks and blocks[location][0] != count:
        raise SystemExit('inconsistent duplicate coverage block')
    old = blocks.get(location, (count, False))
    blocks[location] = (count, old[1] or covered)
total = sum(n for n, _ in blocks.values())
covered = sum(n for n, hit in blocks.values() if hit)
if not total:
    raise SystemExit('empty maintained coverage')
percent = 100 * covered / total
print(f'maintained coverage: {percent:.2f}% ({covered}/{total} statements, {len(blocks)} unique blocks, {len(excluded)} generated files excluded)')
if percent < minimum:
    raise SystemExit(f'coverage below required {minimum:g}%')
PY
