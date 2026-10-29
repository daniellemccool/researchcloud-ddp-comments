#!/usr/bin/env bash
# Runs every hermetic test: bash tests in order, then Python unittest discovery.
set -uo pipefail
cd "$(dirname "$0")/.."
rc=0
for t in scripts/test-*.sh; do [ "$t" = scripts/test-all.sh ] && continue; echo "== $t"; "$t" || rc=1; done
# Interpreter: the box's Python 3.12 (the deployment target) first; .venv is for Playwright only.
PY=$(cd / && uv python find 3.12 2>/dev/null)   # from /: inside the repo uv would answer with .venv || PY=""
[ -n "$PY" ] && [ -x "$PY" ] || { PY=python3; [ -x .venv/bin/python ] && PY=.venv/bin/python; }
echo "== python unittest ($PY $("$PY" -c 'import platform; print(platform.python_version())'))"
"$PY" -m unittest discover -s tests -v || rc=1
exit $rc
