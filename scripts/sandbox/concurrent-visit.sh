#!/usr/bin/env bash
# On the box: two simultaneous first visits as USER straight at the stub, with the headers
# nginx would set. Run it after `desk-sessions retire USER` (the 4CAT account survives a
# retire by design). Passes when, after both visits, there is exactly one 4CAT account, one
# fresh token file and one live sessions.tsv row for USER.
#
#   scripts/sandbox/concurrent-visit.sh USER
#
# Prints `check expected got OK|FAIL` lines; exits 1 on any FAIL or if the desk is not ready
# or failed within 180 s.
set -euo pipefail

user="${1:-}"
if ! [[ "$user" =~ ^[a-z][a-z0-9._-]{0,31}$ ]]; then
  echo "usage: concurrent-visit.sh USER   (USER matches ^[a-z][a-z0-9._-]{0,31}\$)" >&2
  exit 2
fi
DESK=/data/onlinehate_zeeschuimer/desk

# port, collector role and 4CAT db container come from desk.json (defaults as in config.py)
read -r port role dbctr < <(python3 -c '
import json, sys
c = json.load(open(sys.argv[1]))
print(c.get("stub_port", 8110), c["collector_role"], c.get("fourcat_db", "4cat_db"))' "$DESK/desk.json")

start="$(date +%s)"
visit() {
  curl -s -o /dev/null -w "visit $1: HTTP %{http_code}\n" -m 120 \
    -H "REMOTE_USER: $user" -H "X-Desk-Member: 1" -H "X-Desk-Admin: 0" \
    -H "X-Desk-Roles: $role" "http://127.0.0.1:$port/desk/"
}
visit 1 &
visit 2 &
wait

# poll until the desk page is ready (a two-pane page) or says the setup failed
deadline=$((start + 180))
while :; do
  body="$(curl -s -m 30 -H "REMOTE_USER: $user" -H "X-Desk-Member: 1" -H "X-Desk-Admin: 0" \
    -H "X-Desk-Roles: $role" "http://127.0.0.1:$port/desk/" || true)"
  case "$body" in
    *"Setup failed"*) echo "desk setup failed for $user" >&2; exit 1 ;;
    *"Setting up"*|"") ;;
    *) break ;;
  esac
  [ "$(date +%s)" -lt "$deadline" ] || { echo "timed out waiting for $user to be ready" >&2; exit 1; }
  sleep 3
done

fails=0
report() {  # name expected got
  local v=FAIL
  [ "$2" = "$3" ] && v=OK
  [ "$v" = OK ] || fails=$((fails + 1))
  echo "$1 $2 $3 $v"
}

accounts="$(sudo docker exec "$dbctr" psql -v ON_ERROR_STOP=1 -U fourcat -d fourcat -tAc \
  "SELECT count(*) FROM users WHERE name = '$user'" | tr -d '[:space:]')"
report fourcat-accounts 1 "$accounts"

tokens="$(sudo find "$DESK/secrets/4cat-tokens/$user" -type f -newermt "@$start" | wc -l | tr -d '[:space:]')"
report fresh-token-files 1 "$tokens"

live="$(sudo cat "$DESK/sessions.tsv" | awk -F'\t' -v u="$user" '$1 == u && (NF < 3 || $3 == "live")' | wc -l | tr -d '[:space:]')"
report live-session-rows 1 "$live"

[ "$fails" -eq 0 ]
