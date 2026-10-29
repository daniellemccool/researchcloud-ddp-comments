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

# Poll /desk/status (the JSON the page itself polls) until it says ready: true. Each round also
# fetches /desk/, which keeps provisioning going and is where "Setup failed" shows up; the
# full / busy / not-ready pages just mean "not yet" until the timeout.
hdr=(-H "REMOTE_USER: $user" -H "X-Desk-Member: 1" -H "X-Desk-Admin: 0" -H "X-Desk-Roles: $role")
deadline=$((start + 180))
last="no answer yet"
while :; do
  page="$(curl -s -m 30 "${hdr[@]}" "http://127.0.0.1:$port/desk/" || true)"
  case "$page" in
    *"Setup failed"*)
      echo "desk setup failed for $user: $(printf '%s' "$page" | sed 's/<[^>]*>//g' | tr -s '[:space:]' ' ')" >&2
      exit 1 ;;
  esac
  code="$(curl -s -m 30 -o "${TMPDIR:-/tmp}/cv-status.$$" -w '%{http_code}' "${hdr[@]}" "http://127.0.0.1:$port/desk/status" || true)"
  if [ "$code" = 200 ] && python3 -c '
import json, sys
sys.exit(0 if json.load(open(sys.argv[1])).get("ready") is True else 1)' "${TMPDIR:-/tmp}/cv-status.$$" 2>/dev/null; then
    rm -f "${TMPDIR:-/tmp}/cv-status.$$"
    break
  fi
  last="/desk/status HTTP $code; /desk/ said: $(printf '%s' "$page" | sed 's/<[^>]*>//g' | tr -s '[:space:]' ' ' | cut -c1-100)"
  if [ "$(date +%s)" -ge "$deadline" ]; then
    rm -f "${TMPDIR:-/tmp}/cv-status.$$"
    echo "timed out after 180 s waiting for $user to be ready; last state: $last" >&2
    exit 1
  fi
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

tokens=0
if sudo test -d "$DESK/secrets/4cat-tokens/$user"; then
  tokens="$(sudo find "$DESK/secrets/4cat-tokens/$user" -type f -newermt "@$start" | wc -l | tr -d '[:space:]')"
fi
report fresh-token-files 1 "$tokens"

live="$(sudo cat "$DESK/sessions.tsv" | awk -F'\t' -v u="$user" '$1 == u && (NF < 3 || $3 == "live")' | wc -l | tr -d '[:space:]')"
report live-session-rows 1 "$live"

[ "$fails" -eq 0 ]
