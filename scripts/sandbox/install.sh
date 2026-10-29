#!/usr/bin/env bash
# On the sandbox box, as root: install the code pushed to /tmp/ddp-comments-push/.
# Idempotent: on an installed box a re-run changes nothing (the stub is restarted only when
# the code or the unit changed).
set -euo pipefail

SRC=/tmp/ddp-comments-push
DEST=/opt/ddp-comments
DESK=/data/onlinehate_zeeschuimer/desk
FOURCAT_DIR=/home/dmccool/4cat
UNIT=/etc/systemd/system/desk-stub.service

[ "$(id -u)" -eq 0 ] || { echo "install.sh: run as root (sudo bash $0)" >&2; exit 1; }
[ -d "$SRC" ] || { echo "install.sh: $SRC missing; run push.sh first" >&2; exit 1; }

restart=0

echo "== code: $SRC -> $DEST"
mkdir -p "$DEST"
changed="$(rsync -ai --delete "$SRC"/ "$DEST"/ | grep -c . || true)"
[ "$changed" -eq 0 ] || restart=1
echo "$changed changed path(s)"

echo "== bin symlinks in /usr/local/bin"
for f in "$DEST"/bin/*; do
  ln -sfn "$f" "/usr/local/bin/$(basename "$f")"
done

echo "== docker network desk-fourcat"
docker network inspect desk-fourcat >/dev/null 2>&1 || docker network create desk-fourcat

echo "== 4CAT frontend on desk-fourcat"
override="$FOURCAT_DIR/docker-compose.desk.yml"
want="$(mktemp)"
trap 'rm -f "$want"' EXIT
cat >"$want" <<'EOF'
services:
  frontend:
    networks: [default, desk-fourcat]
networks:
  desk-fourcat:
    external: true
EOF
if ! cmp -s "$want" "$override" 2>/dev/null; then
  cp "$want" "$override"
  echo "wrote $override"
fi
(cd "$FOURCAT_DIR" && docker compose -f docker-compose_public_ip.yml -f docker-compose.desk.yml up -d frontend)

echo "== desk.json"
mkdir -p "$DESK"
if [ ! -f "$DESK/desk.json" ]; then
  desk-sessions init --desk-root "$DESK" --repo "$DEST" --platform tiktok \
    --collector-role src_co_students --admin-role src_co_admin --contact Danielle
else
  echo "present, left alone"
fi

echo "== render (compose, policies, nginx -t and reload)"
desk-sessions render --desk-root "$DESK"

echo "== desk-stub.service"
if ! cmp -s "$DEST/desk/desk-stub.service" "$UNIT" 2>/dev/null; then
  cp "$DEST/desk/desk-stub.service" "$UNIT"
  restart=1
fi
systemctl daemon-reload
systemctl enable desk-stub >/dev/null 2>&1 || true
if [ "$restart" -eq 1 ] || ! systemctl is-active --quiet desk-stub; then
  systemctl restart desk-stub
fi
echo "desk-stub: $(systemctl is-active desk-stub)"

echo "== sessions"
desk-sessions status --desk-root "$DESK"
