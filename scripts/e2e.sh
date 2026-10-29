#!/usr/bin/env bash
# Run the e2e suite against the sandbox as IDENTITY (a state saved by tests/e2e/login.py).
#
#   E2E_BASE=https://<desk fqdn> scripts/e2e.sh IDENTITY
#
# Extra environment: E2E_ADMIN=1 for an admin identity; E2E_ADMIN_STATE=PATH to an admin's
# saved state (~/.config/ddp-comments-e2e/dmccool.json), without which the claim test skips;
# E2E_HEADED=1 to watch.
set -euo pipefail

identity="${1:-}"
if ! [[ "$identity" =~ ^[A-Za-z0-9._-]{1,64}$ ]]; then
  echo "usage: E2E_BASE=https://<desk fqdn> scripts/e2e.sh IDENTITY" >&2
  exit 2
fi
: "${E2E_BASE:?set E2E_BASE to the https URL of the desk}"

cd "$(dirname "$0")/.."
state="$HOME/.config/ddp-comments-e2e/$identity.json"
[ -f "$state" ] || { echo "e2e.sh: $state missing; run .venv/bin/python tests/e2e/login.py $identity" >&2; exit 1; }

PY=python3
[ -x .venv/bin/python ] && PY=.venv/bin/python
export E2E_STATE="$state" E2E_BASE E2E_ADMIN_STATE="${E2E_ADMIN_STATE:-}"
exec "$PY" -m unittest discover -s tests/e2e -p 'test_*.py' -v
