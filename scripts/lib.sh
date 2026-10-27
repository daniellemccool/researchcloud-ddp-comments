# Sourced by every scripts/test-*.sh. Counts failures; never `set -e` in tests.
set -uo pipefail
FAIL=0
check() { local d="$1"; shift; if "$@" >/dev/null 2>&1; then echo "ok: $d"; else echo "FAIL: $d"; FAIL=$((FAIL+1)); fi; }
report() { echo; if [ "$FAIL" -eq 0 ]; then echo "all checks passed: $(basename "$0")"; else echo "$FAIL check(s) failed: $(basename "$0")"; exit 1; fi; }
