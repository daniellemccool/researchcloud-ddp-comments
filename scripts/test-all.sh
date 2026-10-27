#!/usr/bin/env bash
# Runs every hermetic test: bash tests in order, then Python unittest discovery.
set -uo pipefail
cd "$(dirname "$0")/.."
rc=0
for t in scripts/test-*.sh; do [ "$t" = scripts/test-all.sh ] && continue; echo "== $t"; "$t" || rc=1; done
echo "== python unittest"
PY=python3; [ -x .venv/bin/python ] && PY=.venv/bin/python
"$PY" -m unittest discover -s tests -v || rc=1
exit $rc
