#!/bin/sh
# Desk init script. The platform plugin runs this once as root, from deploy/,
# before `docker compose up` on compose.yml. Modes: (none) platform path,
# `apply` re-applies desk.json, `update <release>` installs a release tag.
set -eu

self=$0
if command -v readlink >/dev/null 2>&1; then
	resolved=$(readlink -f -- "$self" 2>/dev/null || printf '%s' "$self")
	if [ -n "$resolved" ]; then
		self=$resolved
	fi
fi
case $self in
/*) ;;
*) self=$PWD/$self ;;
esac
SCRIPT_DIR=$(dirname -- "$self")
REPO_ROOT=$(CDPATH='' cd -- "$SCRIPT_DIR/.." && pwd)
SYSROOT=${DESK_SYSROOT:-}
export REPO_ROOT SYSROOT

# shellcheck source=/dev/null
. "$SCRIPT_DIR/lib.sh"

main "$@"
