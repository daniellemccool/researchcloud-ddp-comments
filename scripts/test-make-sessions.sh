#!/usr/bin/env bash
# test-make-sessions.sh — render-only tests (no Docker needed; `docker compose config` runs if docker is present).
HERE="$(cd "$(dirname "$0")" && pwd)"; REPO="$(dirname "$HERE")"; . "$HERE/lib.sh"
TMP="$(mktemp -d)"; trap 'rm -rf "$TMP"' EXIT
GEN="$REPO/desk/make-sessions.sh"; DESK="$TMP/desk"
export PATH="$TMP/bin:$PATH"; mkdir -p "$TMP/bin"
printf '#!/usr/bin/env bash\nexit 0\n' > "$TMP/bin/chown"; chmod +x "$TMP/bin/chown"   # no root in tests

check "render one user exits 0" "$GEN" --desk "$DESK" --repo "$REPO" --admin alice alice
C="$DESK/docker-compose.yml"
check "registry has alice at index 1" grep -qx $'alice\t1' "$DESK/sessions.tsv"
check "admins.txt has alice" grep -qx alice "$DESK/admins.txt"
check "services mitm-alice and firefox-alice" bash -c "grep -q '^  mitm-alice:' '$C' && grep -q '^  firefox-alice:' '$C'"
check "container names" bash -c "grep -q 'container_name: desk-mitm-alice' '$C' && grep -q 'container_name: desk-firefox-alice' '$C'"
check "noVNC bound to 127.0.0.1:5801" grep -q '"127.0.0.1:5801:5800"' "$C"
check "pair network with mitm alias" bash -c "grep -q 'net-alice:' '$C' && grep -q -- '- mitm$' '$C'"
check "captures mounted per user only" grep -q './state/captures/alice:/captures/alice' "$C"
check "DESK_USER set" grep -q 'DESK_USER=alice' "$C"
check "policy per user proxies to mitm:8080" grep -q '"HTTPProxy": "mitm:8080"' "$DESK/firefox/policies-alice.json"
check "addons and stub copied" test -f "$DESK/mitm/addons/capture_comments.py" -a -f "$DESK/desk_stub.py"
check "state dirs exist" test -d "$DESK/state/mitm/alice" -a -d "$DESK/state/firefox/alice" -a -d "$DESK/state/captures/alice"

M="$DESK/nginx/desk-map.conf"; D="$DESK/nginx/desk.conf"; F="$DESK/nginx/fourcat.conf"
check "map: alice's VNC port" grep -q '"alice" 127.0.0.1:5801;' "$M"
check "map: every map defaults to the stub or 0" test "$(grep -c 'default 127.0.0.1:8110;' "$M")" = 4
check "map: alice is admin" grep -q '"alice" 1;' "$M"
check "map: admin VNC keyed on admin flag and target" grep -q '"1:alice" 127.0.0.1:5801;' "$M"
check "desk: /desk/ to stub without trailing slash" grep -q 'proxy_pass http://127.0.0.1:8110;' "$D"
check "desk: stub gets REMOTE_USER and admin flag" bash -c "grep -q 'proxy_set_header REMOTE_USER \$username;' '$D' && grep -q 'proxy_set_header X-Desk-Admin \$desk_admin;' '$D'"
check "desk: VNC routed through the map" grep -q 'proxy_pass http://\$desk_vnc;' "$D"
check "desk: admin VNC routed through the map" grep -q 'proxy_pass http://\$desk_admin_vnc;' "$D"
check "fourcat: UI through admin map" grep -q 'proxy_pass http://\$fourcat_ui;' "$F"
check "fourcat: exactly two upload locations" test "$(grep -c 'proxy_pass http://\$fourcat_upload;' "$F")" = 2
check "fourcat: import and poll paths" bash -c "grep -q 'location = /api/import-dataset/' '$F' && grep -q 'location = /api/check-query/' '$F'"
check "every location overwrites both identity headers" bash -c "for f in '$D' '$F'; do n=\$(grep -c '^location' \"\$f\"); [ \"\$(grep -c 'proxy_set_header REMOTE_USER \$username;' \"\$f\")\" = \"\$n\" ] && [ \"\$(grep -c 'proxy_set_header X-Desk-Admin \$desk_admin;' \"\$f\")\" = \"\$n\" ] || exit 1; done"
check "every location is gated" bash -c "for f in '$D' '$F'; do [ \"\$(grep -c '^location' \"\$f\")\" = \"\$(grep -c 'auth_request /validate;' \"\$f\")\" ] || exit 1; done"
check "no if on \$username (runs before auth_request)" bash -c "! grep -qE 'if *\\(.*\\\$(username|desk_|fourcat_)' '$D' '$F' '$M'"

sed -n '/^  mitm-alice:/,/^  firefox-alice:/p' "$C" > "$TMP/alice-n1.txt"
check "add bob exits 0" "$GEN" --desk "$DESK" --repo "$REPO" bob
check "bob gets index 2 and port 5802" bash -c "grep -qx \$'bob\t2' '$DESK/sessions.tsv' && grep -q '\"127.0.0.1:5802:5800\"' '$C'"
check "re-adding alice keeps one registry line" bash -c "'$GEN' --desk '$DESK' --repo '$REPO' alice && [ \$(grep -c '^alice' '$DESK/sessions.tsv') = 1 ]"
sed -n '/^  mitm-alice:/,/^  firefox-alice:/p' "$C" > "$TMP/alice-n2.txt"
check "alice's block byte-identical after adding bob" cmp -s "$TMP/alice-n1.txt" "$TMP/alice-n2.txt"
check "bob is not admin" bash -c "! grep -q '\"bob\" 1;' '$M'"
check "bob may upload, not see the UI" bash -c "sed -n '/fourcat_upload/,/}/p' '$M' | grep -q '\"bob\" 127.0.0.1:8080;' && ! sed -n '/fourcat_ui/,/}/p' '$M' | grep -q '\"bob\"'"
check "no admin VNC entry for bob as admin target of a non-admin" bash -c "! grep -q '\"0:' '$M'"

check "bad username refused" bash -c "! '$GEN' --desk '$DESK' --repo '$REPO' 'Bad;user'"
check "no users at all is a usage error" bash -c "! '$GEN' --desk '$TMP/empty' --repo '$REPO'"
if command -v docker >/dev/null && docker compose version >/dev/null 2>&1; then
  check "docker compose config validates" bash -c "cd '$DESK' && docker compose config -q"
else echo "skip: docker compose not available"; fi
report
