#!/usr/bin/env bash
# Run as root on the sandbox box, with desk_stub.py and this script copied to /tmp.
# Adds the SRC roles header to the desk nginx snippet (additively, idempotently),
# deploys the stub that logs it, and shows the access log.
set -euo pipefail
CONF=/etc/nginx/app-location-conf.d/desk.conf
DESK=/data/onlinehate_zeeschuimer/desk

if ! grep -q 'X-Desk-Roles' "$CONF"; then
  sed -i '/auth_request_set \$username \$upstream_http_username;/a\
    auth_request_set $src_roles $upstream_http_src_co_roles;\
    proxy_set_header X-Desk-Roles $src_roles;' "$CONF"
fi
nginx -t
systemctl reload nginx
cp "$(dirname "$0")/desk_stub.py" "$DESK/desk_stub.py"
systemctl restart desk-stub
tail -3 "$DESK/state/access.ndjson" 2>/dev/null || echo "(no access lines yet)"
