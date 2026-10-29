#!/usr/bin/env bash
# On the box: two simultaneous first visits as USER straight at the stub, with the headers
# nginx would set. After `desk-sessions retire USER` there must be one create_user line in the
# stub's journal and one token.
#
#   scripts/sandbox/concurrent-visit.sh USER
set -euo pipefail

user="${1:-}"
if ! [[ "$user" =~ ^[a-z][a-z0-9._-]{0,31}$ ]]; then
  echo "usage: concurrent-visit.sh USER   (USER matches ^[a-z][a-z0-9._-]{0,31}\$)" >&2
  exit 2
fi
DESK=/data/onlinehate_zeeschuimer/desk
port="$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1])).get("stub_port", 8110))' "$DESK/desk.json")"

since="$(date '+%Y-%m-%d %H:%M:%S')"
visit() {
  curl -s -o /dev/null -w "visit $1: HTTP %{http_code}\n" -m 120 \
    -H "REMOTE_USER: $user" -H "X-Desk-Member: 1" -H "X-Desk-Admin: 0" \
    -H "X-Desk-Roles: src_co_students" "http://127.0.0.1:$port/desk/status"
}
visit 1 &
visit 2 &
wait
sleep 2

echo "create_user lines in journalctl -u desk-stub since $since:"
sudo journalctl -u desk-stub --since "$since" --no-pager | grep -c create_user || true
echo "token files for $user:"
sudo ls -l "$DESK/secrets/4cat-tokens/$user/"
