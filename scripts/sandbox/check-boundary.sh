#!/usr/bin/env bash
# On the box: check what USER's Firefox container can and cannot reach through its proxy.
#
#   scripts/sandbox/check-boundary.sh USER
#
# Prints "name expected got OK|FAIL" per check; exits 1 if any FAIL.
set -euo pipefail

user="${1:-}"
if ! [[ "$user" =~ ^[a-z][a-z0-9._-]{0,31}$ ]]; then
  echo "usage: check-boundary.sh USER   (USER matches ^[a-z][a-z0-9._-]{0,31}\$)" >&2
  exit 2
fi
ctr="desk-firefox-$user"

fails=0
# probe NAME EXPECTED(403 | other = "not 403, 200 or 404") URL
probe() {
  local name="$1" want="$2" url="$3" got verdict=FAIL
  got="$(sudo docker exec "$ctr" curl -s -o /dev/null -w '%{http_code}' -m 20 \
    -x http://mitm:8080 --cacert /mitm-ca/mitmproxy-ca-cert.pem "$url" || true)"
  case "$want" in
    403) [ "$got" = 403 ] && verdict=OK ;;
    200/404) { [ "$got" = 200 ] || [ "$got" = 404 ]; } && verdict=OK ;;
  esac
  [ "$verdict" = OK ] || fails=$((fails + 1))
  echo "$name $want $got $verdict"
}

probe fourcat-root 403 https://fourcat.desk/
probe fourcat-api 200/404 "https://fourcat.desk/api/check-query/?key=x"
probe frontend 403 http://frontend:5000/
probe docker-host 403 http://172.17.0.1:8080/
probe localhost 403 http://localhost:8080/

[ "$fails" -eq 0 ]
