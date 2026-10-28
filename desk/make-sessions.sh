#!/usr/bin/env bash
# make-sessions.sh — render one isolated Firefox+mitmproxy session pair per desk user,
# plus the nginx gate that routes each SRAM user to their own session only.
#
#   make-sessions.sh [--desk DIR] [--repo DIR] [--admin USER]... [--up] [USER...]
#   --desk   deployment dir (default /data/onlinehate_zeeschuimer/desk)
#   --repo   this repo's checkout (default: the script's parent)
#   --admin  add USER to <desk>/admins.txt (staff: 4CAT UI, desk overview, any session's VNC)
#   --up     after rendering: per user, start mitm, wait for its CA, then start firefox
#
# USERs are appended to <desk>/sessions.tsv (username<TAB>index, append-only, so ports
# stay stable); every registered user is rendered. Session <index> gets noVNC on
# 127.0.0.1:58<index>, its own Compose network with the proxy aliased `mitm` (the policy's
# fixed `mitm:8080` reaches the right proxy), and state under state/{mitm,firefox,captures}/<user>.
# A NEW firefox state dir is cloned from <desk>/templates/firefox-profile.tar when present.
#
# Renders <desk>/nginx/desk-map.conf (http context, /etc/nginx/conf.d/),
# <desk>/nginx/desk.conf and <desk>/nginx/fourcat.conf (/etc/nginx/app-location-conf.d/).
# All per-user decisions are maps read in the content phase: `if` and `rewrite` run before
# auth_request, when $username is still empty. Anything not allowed lands on the stub (403).
set -euo pipefail
usage() { echo "usage: $0 [--desk DIR] [--repo DIR] [--admin USER]... [--up] [USER...]" >&2; exit 2; }
DESK=/data/onlinehate_zeeschuimer/desk; REPO="$(cd "$(dirname "$0")/.." && pwd)"; UP=0
NEW_USERS=(); NEW_ADMINS=()
USER_RE='^[a-z][a-z0-9._-]{0,31}$'
while [ $# -gt 0 ]; do
  case "$1" in
    --desk) DESK="$2"; shift 2;; --repo) REPO="$2"; shift 2;;
    --admin) NEW_ADMINS+=("$2"); shift 2;; --up) UP=1; shift;;
    -*) usage;; *) NEW_USERS+=("$1"); shift;;
  esac
done
for u in "${NEW_USERS[@]}" "${NEW_ADMINS[@]}"; do
  [[ "$u" =~ $USER_RE ]] || { echo "invalid username: $u" >&2; exit 2; }
done
MITM_IMAGE="mitmproxy/mitmproxy:12.2.3"; FF_IMAGE="jlesage/firefox:v26.08.3"
STUB=127.0.0.1:8110; FOURCAT=127.0.0.1:8080

mkdir -p "$DESK/firefox" "$DESK/nginx" "$DESK/mitm"
touch "$DESK/sessions.tsv" "$DESK/admins.txt"
for u in "${NEW_USERS[@]}"; do
  grep -q "^$u"$'\t' "$DESK/sessions.tsv" && continue
  n=$(( $(awk -F'\t' 'BEGIN{m=0} $2>m{m=$2} END{print m}' "$DESK/sessions.tsv") + 1 ))
  [ "$n" -le 99 ] || { echo "registry full (99 sessions)" >&2; exit 1; }
  printf '%s\t%s\n' "$u" "$n" >> "$DESK/sessions.tsv"
done
for a in "${NEW_ADMINS[@]}"; do grep -qx "$a" "$DESK/admins.txt" || echo "$a" >> "$DESK/admins.txt"; done
[ -s "$DESK/sessions.tsv" ] || usage

USERS=(); declare -A PORT
while IFS=$'\t' read -r u n; do
  [ -n "$u" ] || continue
  USERS+=("$u"); PORT[$u]="127.0.0.1:58$(printf '%02d' "$n")"
done < "$DESK/sessions.tsv"
mapfile -t ADMINS < <(grep -E "$USER_RE" "$DESK/admins.txt" || true)
is_admin() { local a; for a in "${ADMINS[@]}"; do [ "$a" = "$1" ] && return 0; done; return 1; }

rm -rf "$DESK/mitm/addons"; cp -r "$REPO/desk/mitm/addons" "$DESK/mitm/addons"
cp "$REPO/desk/desk_stub.py" "$DESK/desk_stub.py"

{
  echo "# Rendered by make-sessions.sh — do not edit; add users by re-running it."
  echo "name: desk"
  echo "services:"
  for u in "${USERS[@]}"; do
    cat <<EOF
  mitm-$u:
    image: $MITM_IMAGE
    container_name: desk-mitm-$u
    command: mitmdump --set block_global=false --set stream_large_bodies=1m -s /addons/capture_comments.py
    volumes:
      - ./mitm/addons:/addons:ro
      - ./state/mitm/$u:/home/mitmproxy/.mitmproxy
      - ./state/captures/$u:/captures/$u
    environment:
      - DESK_SESSION=$u
      - DESK_USER=$u
    networks:
      net-$u:
        aliases:
          - mitm
    restart: unless-stopped
  firefox-$u:
    image: $FF_IMAGE
    container_name: desk-firefox-$u
    depends_on: [mitm-$u]
    ports:
      - "${PORT[$u]}:5800"
    volumes:
      - ./state/firefox/$u:/config
      - ./state/mitm/$u:/mitm-ca:ro
      - ./firefox/policies-$u.json:/usr/lib/firefox/distribution/policies.json:ro
    environment:
      - DISPLAY_WIDTH=1280
      - DISPLAY_HEIGHT=900
      - WEB_AUDIO=0
      - KEEP_APP_RUNNING=1
      - DARK_MODE=0
      - FF_OPEN_URL=about:blank
      - USER_ID=1000
      - GROUP_ID=1000
    shm_size: "1g"
    networks: [net-$u]
    restart: unless-stopped
EOF
  done
  echo "networks:"
  for u in "${USERS[@]}"; do echo "  net-$u: {}"; done
} > "$DESK/docker-compose.yml"

for u in "${USERS[@]}"; do
  cp "$REPO/desk/firefox/policies.json" "$DESK/firefox/policies-$u.json"
  mkdir -p "$DESK/state/mitm/$u" "$DESK/state/captures/$u"
  if [ ! -d "$DESK/state/firefox/$u" ]; then
    mkdir -p "$DESK/state/firefox/$u"
    [ -f "$DESK/templates/firefox-profile.tar" ] && tar -xf "$DESK/templates/firefox-profile.tar" -C "$DESK/state/firefox/$u"
  fi
  # Hide videos and blur images on tiktok.com (read at Firefox start; re-copied on every render).
  mkdir -p "$DESK/state/firefox/$u/profile/chrome"
  cp "$REPO/desk/firefox/userContent.css" "$DESK/state/firefox/$u/profile/chrome/userContent.css"
  chown -R 1000:1000 "$DESK/state/mitm/$u" "$DESK/state/firefox/$u" "$DESK/state/captures/$u"
done

{
  echo "# /etc/nginx/conf.d/desk-map.conf — rendered by make-sessions.sh. http context."
  echo "# \$username is set by auth_request_set in each gated location; maps are read lazily, after it."
  echo "map \$username \$desk_vnc {"; echo "    default $STUB;"
  for u in "${USERS[@]}"; do echo "    \"$u\" ${PORT[$u]};"; done; echo "}"
  echo "map \$username \$desk_admin {"; echo "    default 0;"
  for a in "${ADMINS[@]}"; do echo "    \"$a\" 1;"; done; echo "}"
  echo "map \$username \$fourcat_ui {"; echo "    default $STUB;"
  for a in "${ADMINS[@]}"; do echo "    \"$a\" $FOURCAT;"; done; echo "}"
  echo "map \$username \$fourcat_upload {"; echo "    default $STUB;"
  for u in "${USERS[@]}"; do is_admin "$u" || echo "    \"$u\" $FOURCAT;"; done
  for a in "${ADMINS[@]}"; do echo "    \"$a\" $FOURCAT;"; done; echo "}"
  echo "map \"\$desk_admin:\$desk_target\" \$desk_admin_vnc {"; echo "    default $STUB;"
  for u in "${USERS[@]}"; do echo "    \"1:$u\" ${PORT[$u]};"; done; echo "}"
} > "$DESK/nginx/desk-map.conf"

gate() { cat <<'EOF'
    error_page 401 = @custom_401;
    auth_request /validate;
    auth_request_set $username $upstream_http_username;
    # Always overwrite both identity headers: any location may fall through to the stub.
    proxy_set_header REMOTE_USER $username;
    proxy_set_header X-Desk-Admin $desk_admin;
EOF
}
ws() { cat <<'EOF'
    proxy_http_version 1.1;
    proxy_set_header Upgrade $http_upgrade;
    proxy_set_header Connection $connection_upgrade;
    proxy_set_header Host $host;
    proxy_buffering off;
    proxy_read_timeout 86400s;
    proxy_send_timeout 86400s;
EOF
}
{
  echo "# /etc/nginx/app-location-conf.d/desk.conf — rendered by make-sessions.sh."
  echo "# Needs conf.d/desk-map.conf and conf.d/websocket-map.conf."
  echo "location /desk/ {"; gate
  cat <<EOF
    # NO trailing slash: the stub sees the full /desk/... path.
    proxy_pass http://$STUB;
    proxy_set_header Host \$host;
}
EOF
  echo "location /desk/vnc/ {"; gate
  echo '    rewrite ^/desk/vnc/(.*)$ /$1 break;'
  echo '    proxy_pass http://$desk_vnc;'; ws; echo "}"
  echo 'location ~ ^/desk/admin/vnc/([a-z][a-z0-9._-]*)/ {'; gate
  echo '    set $desk_target $1;'
  echo '    rewrite ^/desk/admin/vnc/[^/]+/(.*)$ /$1 break;'
  echo '    proxy_pass http://$desk_admin_vnc;'; ws; echo "}"
} > "$DESK/nginx/desk.conf"

fourcat_loc() { # $1 location spec, $2 map variable
  echo "location $1 {"; gate
  cat <<EOF
    proxy_pass http://\$$2;
    proxy_http_version 1.1;
    proxy_set_header Host \$host;
    proxy_set_header X-Real-IP \$remote_addr;
    proxy_set_header X-Forwarded-For \$proxy_add_x_forwarded_for;
    proxy_set_header X-Forwarded-Proto \$scheme;
    client_max_body_size 512m;
    proxy_read_timeout 600s;
}
EOF
}
{
  echo "# /etc/nginx/app-location-conf.d/fourcat.conf — rendered by make-sessions.sh."
  echo "# 4CAT UI for admins only; desk users reach just the two calls Zeeschuimer's upload makes."
  fourcat_loc / fourcat_ui
  fourcat_loc "= /api/import-dataset/" fourcat_upload
  fourcat_loc "= /api/check-query/" fourcat_upload
} > "$DESK/nginx/fourcat.conf"
cp "$REPO/desk/nginx/websocket-map.conf" "$DESK/nginx/websocket-map.conf"

echo "rendered ${#USERS[@]} session(s) under $DESK: ${USERS[*]}"
if [ "$UP" = 1 ]; then
  cd "$DESK"
  for u in "${USERS[@]}"; do
    docker compose up -d "mitm-$u"
    for _ in $(seq 1 60); do [ -f "state/mitm/$u/mitmproxy-ca-cert.pem" ] && break; sleep 1; done
    [ -f "state/mitm/$u/mitmproxy-ca-cert.pem" ] || { echo "mitm-$u produced no CA within 60 s" >&2; exit 1; }
    docker compose up -d "firefox-$u"
  done
fi
