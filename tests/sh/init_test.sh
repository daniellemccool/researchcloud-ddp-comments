#!/usr/bin/env bash
# Test suite for deploy/init.sh. Run from the repository root or via
# scripts/check.sh. Each case builds a throwaway DESK_SYSROOT and runs the
# init script against it with docker, nginx and network fetches disabled.
# Every case runs in a subshell with `set -e` in effect, so a failing
# assertion aborts the case and is reported; no assertion can be inert.
set -euo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")/../.."
REPO_ROOT=$PWD

# The suite deletes deploy/.env; never run it against a deployed copy.
case $REPO_ROOT in
/rsc/*)
	echo "refusing to run from a deployed copy under /rsc/: $REPO_ROOT" >&2
	exit 2
	;;
esac

FIX=$REPO_ROOT/tests/sh/fixtures

PASS=0
FAIL=0
FAILED=()
SYSROOT=""
RC=0
OUT=""
ERR=""
CASE_ERR_FILE=""

run_init() {
	set +e
	OUT=$(sh "$REPO_ROOT/deploy/init.sh" "$@" 2>"$SYSROOT/stderr.txt")
	RC=$?
	set -e
	ERR=$(cat "$SYSROOT/stderr.txt")
}

# run_init_repo runs init.sh from a copied repository (used by the update
# cases, which swap the repository copy in place).
run_init_repo() {
	local repo=$1
	shift
	set +e
	OUT=$(sh "$repo/deploy/init.sh" "$@" 2>"$SYSROOT/stderr.txt")
	RC=$?
	set -e
	ERR=$(cat "$SYSROOT/stderr.txt")
}

a_fail() {
	printf '%s\n' "$*" >"$CASE_ERR_FILE"
	return 1
}
a_rc() {
	[ "$RC" -eq "$1" ] || a_fail "expected exit $1, got $RC (stderr: $ERR)"
}
a_err_has() {
	case $ERR in
	*"$1"*) ;;
	*) a_fail "stderr lacks [$1]: $ERR" ;;
	esac
}
a_out_has() {
	case $OUT in
	*"$1"*) ;;
	*) a_fail "stdout lacks [$1]: $OUT" ;;
	esac
}
a_file() {
	[ -f "$1" ] || a_fail "missing file: $1"
}
a_eq() {
	[ "$2" = "$3" ] || a_fail "$1: expected [$3], got [$2]"
}
a_mode() {
	local m
	m=$(stat -c %a "$2")
	[ "$m" = "$3" ] || a_fail "$1: mode $m != $3"
}

run_case() {
	local name=$1
	shift
	SYSROOT=$(mktemp -d)
	mkdir -p "$SYSROOT/etc/rsc" "$SYSROOT/data/vol1" \
		"$SYSROOT/etc/nginx/app-location-conf.d" "$SYSROOT/usr/local/bin"
	cp "$FIX/workspace.json" "$SYSROOT/etc/rsc/workspace.json"
	export DESK_SYSROOT=$SYSROOT DESK_NO_DOCKER=1 DESK_NO_NGINX=1 DESK_NO_FETCH=1
	export DESK_TEMPLATE_FILE=$FIX/template.tar DESK_CO_MEMBERS_FILE=$FIX/members.json
	PLUGIN_PARAMETERS=$(cat "$FIX/params-ok.json")
	export PLUGIN_PARAMETERS
	rm -f "$REPO_ROOT/deploy/.env"
	local errfile rc
	errfile=$(mktemp)
	# The case runs in a subshell as a plain command (not an if condition, where
	# bash would ignore set -e) with set -e in effect, so any failing assertion
	# aborts the case and is reported.
	set +e
	(
		set -e
		CASE_ERR_FILE=$errfile
		"$@"
	)
	rc=$?
	set -e
	if [ "$rc" -eq 0 ]; then
		echo "ok $name"
		PASS=$((PASS + 1))
	else
		echo "FAIL $name: $(cat "$errfile" 2>/dev/null || echo failed)"
		FAIL=$((FAIL + 1))
		FAILED+=("$name")
	fi
	rm -f "$errfile"
	rm -f "$REPO_ROOT/deploy/.env"
	rm -rf "$SYSROOT"
	SYSROOT=""
}

desk() { printf '%s/data/vol1/desk' "$SYSROOT"; }
envval() { grep "^$1=" "$REPO_ROOT/deploy/.env" | cut -d= -f2-; }

# a_nothing_written asserts that a refused run left nothing behind: no desk
# root (and so no layout directories), no deploy/.env, no nginx files.
a_nothing_written() {
	[ ! -e "$(desk)" ] || a_fail "desk root was written"
	[ ! -e "$REPO_ROOT/deploy/.env" ] || a_fail ".env was written"
	[ -z "$(ls -A "$SYSROOT/etc/nginx/app-location-conf.d")" ] ||
		a_fail "nginx files were written"
}

# make_release_tarball builds a minimal release tarball at $1: a top-level
# <name>/ holding deploy/.env.example (FOURCAT_IMAGE pin) and deploy/compose.yml.
make_release_tarball() {
	local dest=$1 rel
	rel=$(mktemp -d "$SYSROOT/rel-XXXXXX")
	mkdir -p "$rel/ddp-comments-v0.2.0-test/deploy"
	printf 'FOURCAT_IMAGE=digitalmethodsinitiative/4cat:v9.99\n' \
		>"$rel/ddp-comments-v0.2.0-test/deploy/.env.example"
	printf 'name: desk\nservices: {}\n' \
		>"$rel/ddp-comments-v0.2.0-test/deploy/compose.yml"
	tar -czf "$dest" -C "$rel" ddp-comments-v0.2.0-test
	rm -rf "$rel"
}

# make_installed_copy creates a MINIMAL "installed repository copy" at $1,
# holding only what init.sh, update_mode and the harness read from the old
# copy: deploy/ (init.sh, lib.sh, compose.yml, .env.example, the nginx
# configs) and the platform definition the parameters name. It never copies
# $REPO_ROOT itself, so editor/sandbox files in the checkout cannot break
# the test.
make_installed_copy() {
	local dest=$1
	mkdir -p "$dest/deploy/nginx" "$dest/platforms/tiktok"
	cp -a "$REPO_ROOT/deploy/init.sh" "$REPO_ROOT/deploy/lib.sh" \
		"$REPO_ROOT/deploy/compose.yml" "$REPO_ROOT/deploy/.env.example" \
		"$dest/deploy/"
	cp -a "$REPO_ROOT/deploy/nginx/desk.conf" "$REPO_ROOT/deploy/nginx/fourcat.conf" \
		"$dest/deploy/nginx/"
	cp -a "$REPO_ROOT/platforms/tiktok/platform.json" "$dest/platforms/tiktok/"
}

case_ok_fresh() {
	run_init
	a_rc 0
	local d
	d=$(desk)
	a_file "$d/desk.json"
	a_eq "desk.json key count" "$(jq 'keys | length' "$d/desk.json")" "24"
	a_eq "layout_version" "$(jq -r '.layout_version' "$d/desk.json")" "1"
	a_eq "package_version" "$(jq -r '.package_version' "$d/desk.json")" "v0.1.0-alpha.1"
	a_file "$REPO_ROOT/deploy/.env"
	a_eq "env DESK_ROOT" "$(envval DESK_ROOT)" "$d"
	a_eq "env FQDN" "$(envval FQDN)" "example.co.src.surf-hosted.nl"
	a_eq "env REPO_ROOT" "$(envval REPO_ROOT)" "$REPO_ROOT"
	a_mode "gate mode" "$d/secrets/gate" "600"
	local gate
	gate=$(cat "$d/secrets/gate")
	case $gate in
	'' | *[!0-9a-f]*) a_fail "gate not lowercase hex: [$gate]" ;;
	esac
	[ "${#gate}" -eq 32 ] || a_fail "gate length ${#gate} != 32"
	a_mode "4cat-postgres mode" "$d/secrets/4cat-postgres" "600"
	a_eq "co_token value" "$(cat "$d/secrets/co_token")" "fixture-token-not-real"
	a_mode "co_token mode" "$d/secrets/co_token" "600"
	local f
	for f in desk.conf fourcat.conf; do
		a_file "$SYSROOT/etc/nginx/app-location-conf.d/$f"
		a_mode "nginx $f mode" "$SYSROOT/etc/nginx/app-location-conf.d/$f" "600"
		grep -q "$gate" "$SYSROOT/etc/nginx/app-location-conf.d/$f" ||
			a_fail "$f does not contain the gate secret"
		if grep -q __GATE__ "$SYSROOT/etc/nginx/app-location-conf.d/$f"; then
			a_fail "$f still contains __GATE__"
		fi
	done
	a_file "$d/templates/release/tiktok-1.14.3/manifest.json"
	if [ "$(id -u)" = "0" ]; then
		a_eq "template owner uid" "$(stat -c %u "$d/templates/release/tiktok-1.14.3/manifest.json")" "1000"
	fi
	a_eq "pool.csv header" "$(head -n1 "$d/pool.csv")" "id,kind,bucket,note"
	a_file "$SYSROOT/usr/local/bin/desk"
	[ -x "$SYSROOT/usr/local/bin/desk" ] || a_fail "wrapper not executable"
	grep -q "deploy/compose.yml" "$SYSROOT/usr/local/bin/desk" ||
		a_fail "wrapper does not name the compose path"
}

case_python_repr_params() {
	PLUGIN_PARAMETERS=$(jq -r 'to_entries | map("\u0027\(.key)\u0027: \u0027\(.value)\u0027") | join(", ") | "{" + . + "}"' "$FIX/params-ok.json")
	export PLUGIN_PARAMETERS
	run_init
	a_rc 0
	local d k
	d=$(desk)
	a_file "$d/desk.json"
	a_eq "desk.json key count" "$(jq 'keys | length' "$d/desk.json")" "24"
	while IFS= read -r k; do
		a_eq "desk.json $k" \
			"$(jq -r --arg k "$k" '.[$k]' "$d/desk.json")" \
			"$(jq -r --arg k "$k" '.[$k]' "$FIX/params-ok.json")"
	done < <(jq -r 'keys[]' "$FIX/params-ok.json")
}

case_placeholder_role() {
	PLUGIN_PARAMETERS=$(jq -c '.desk_collector_role = "src_co_<fill-me-in>"' "$FIX/params-ok.json")
	export PLUGIN_PARAMETERS
	run_init
	a_rc 2
	a_err_has "desk_collector_role"
	a_nothing_written
}

case_uppercase_role() {
	PLUGIN_PARAMETERS=$(jq -c '.desk_collector_role = "Src_co_students"' "$FIX/params-ok.json")
	export PLUGIN_PARAMETERS
	run_init
	a_rc 2
	a_err_has "desk_collector_role"
	a_nothing_written
}

case_crlf_value() {
	PLUGIN_PARAMETERS=$(jq -c --arg c $'Danielle\r\n' '.desk_contact = $c' "$FIX/params-ok.json")
	export PLUGIN_PARAMETERS
	run_init
	a_rc 2
	a_err_has "desk_contact"
	a_nothing_written
}

case_trailing_lf_value() {
	PLUGIN_PARAMETERS=$(jq -c --arg c $'Danielle\n' '.desk_contact = $c' "$FIX/params-ok.json")
	export PLUGIN_PARAMETERS
	run_init
	a_rc 2
	a_err_has "desk_contact"
	a_nothing_written
}

case_storage_path_with_space() {
	PLUGIN_PARAMETERS=$(jq -c --arg sp "$SYSROOT/data/vol 1" '.desk_storage_path = $sp' "$FIX/params-ok.json")
	export PLUGIN_PARAMETERS
	run_init
	a_rc 2
	a_err_has "desk_storage_path"
	a_err_has "whitespace"
	a_nothing_written
}

case_too_many_digits() {
	PLUGIN_PARAMETERS=$(jq -c '.desk_max_sessions = "12345678901234567890"' "$FIX/params-ok.json")
	export PLUGIN_PARAMETERS
	run_init
	a_rc 2
	a_err_has "desk_max_sessions"
	a_err_has "1-9 digits"
	a_nothing_written
}

case_fourcat_mirror_ref() {
	local out
	out=$(sh -c '. "$1/deploy/lib.sh"; fourcat_mirror_ref "$2"' sh "$REPO_ROOT" 'digitalmethodsinitiative/4cat:v1.57')
	a_eq "mirror ref" "$out" "ghcr.io/daniellemccool/researchcloud-ddp-comments/4cat:v1.57"
	out=$(sh -c '. "$1/deploy/lib.sh"; fourcat_mirror_ref "$2"' sh "$REPO_ROOT" 'registry:5000/x/4cat:v2')
	a_eq "registry-port ref" "$out" "ghcr.io/daniellemccool/researchcloud-ddp-comments/4cat:v2"
}

case_quote_in_contact() {
	PLUGIN_PARAMETERS=$(jq -c --arg c "Danielle O'Brien" '.desk_contact = $c' "$FIX/params-ok.json")
	export PLUGIN_PARAMETERS
	run_init
	a_rc 2
	a_err_has "desk_contact"
}

case_two_failures() {
	PLUGIN_PARAMETERS=$(jq -c '.desk_collector_role = "src_co_<fill-me-in>" | .desk_idle_seconds = "abc"' "$FIX/params-ok.json")
	export PLUGIN_PARAMETERS
	run_init
	a_rc 2
	a_err_has "desk_collector_role"
	a_err_has "desk_idle_seconds"
}

case_role_held_by_nobody() {
	jq 'map(if .username == "dmccool" then .roles = ["src_co_admin"]
	      elif .username == "dmccool2" then .roles = []
	      else . end)' "$FIX/members.json" >"$SYSROOT/members.json"
	export DESK_CO_MEMBERS_FILE=$SYSROOT/members.json
	run_init
	a_rc 2
	a_err_has "desk_collector_role"
}

case_admin_role_held_by_nobody() {
	jq 'map(.roles |= map(select(. != "src_co_admin")))' "$FIX/members.json" >"$SYSROOT/members.json"
	export DESK_CO_MEMBERS_FILE=$SYSROOT/members.json
	run_init
	a_rc 2
	a_err_has "desk_admin_role"
}

case_two_volumes() {
	mkdir -p "$SYSROOT/data/vol2"
	run_init
	a_rc 2
	a_err_has "desk_storage_path"
	a_err_has "vol1"
	a_err_has "vol2"
}

case_reattach() {
	run_init
	a_rc 0
	local d gate1 pg1 gate2 pg2 f
	d=$(desk)
	gate1=$(cat "$d/secrets/gate")
	pg1=$(cat "$d/secrets/4cat-postgres")
	run_init
	a_rc 0
	a_out_has "reattach"
	gate2=$(cat "$d/secrets/gate")
	pg2=$(cat "$d/secrets/4cat-postgres")
	[ "$pg1" = "$pg2" ] || a_fail "4cat-postgres changed on reattach"
	[ "$gate1" != "$gate2" ] || a_fail "gate did not change on reattach"
	for f in desk.conf fourcat.conf; do
		grep -q "$gate2" "$SYSROOT/etc/nginx/app-location-conf.d/$f" ||
			a_fail "$f does not hold the new gate secret"
	done
}

case_reattach_newer_layout() {
	run_init
	a_rc 0
	local d gate1
	d=$(desk)
	gate1=$(cat "$d/secrets/gate")
	jq '.layout_version = 2' "$d/desk.json" >"$d/desk.json.new"
	mv "$d/desk.json.new" "$d/desk.json"
	run_init
	a_rc 2
	a_err_has "layout_version"
	a_eq "gate unchanged" "$(cat "$d/secrets/gate")" "$gate1"
}

case_sha_mismatch() {
	PLUGIN_PARAMETERS=$(jq -c '.template_sha256 = "0000000000000000000000000000000000000000000000000000000000000000"' "$FIX/params-ok.json")
	export PLUGIN_PARAMETERS
	run_init
	a_rc 2
	a_err_has "template_sha256"
	local d
	d=$(desk)
	if [ -e "$d/templates/release/tiktok-1.14.3" ]; then
		a_fail "template was extracted despite sha mismatch"
	fi
	[ -z "$(ls -A "$d/templates/release" 2>/dev/null)" ] ||
		a_fail "templates/release not empty after mismatch"
	# The sha256 is verified before any file is written.
	a_nothing_written
}

case_apply_mode() {
	run_init
	a_rc 0
	cp "$REPO_ROOT/deploy/.env" "$SYSROOT/env.before"
	run_init apply
	a_rc 0
	a_out_has "exec desk desk render"
	cmp -s "$SYSROOT/env.before" "$REPO_ROOT/deploy/.env" ||
		a_fail "apply modified deploy/.env"
}

case_bad_mode() {
	run_init frobnicate
	a_rc 2
	a_err_has "usage"
}

# The update cases run against a minimal installed copy inside the sysroot,
# because update swaps the repository copy in place.
case_update_ok() {
	make_installed_copy "$SYSROOT/repo"
	touch "$SYSROOT/repo/old-copy-marker"
	run_init_repo "$SYSROOT/repo"
	a_rc 0
	local d repoenv
	d=$(desk)
	repoenv=$SYSROOT/repo/deploy/.env
	a_file "$repoenv"
	make_release_tarball "$SYSROOT/release.tar.gz"
	export DESK_RELEASE_TARBALL=$SYSROOT/release.tar.gz
	run_init_repo "$SYSROOT/repo" update v0.2.0-test
	a_rc 0
	a_eq "desk_image" "$(jq -r '.desk_image' "$d/desk.json")" \
		"ghcr.io/daniellemccool/researchcloud-ddp-comments/desk:v0.2.0-test"
	a_eq "deskproxy_image" "$(jq -r '.deskproxy_image' "$d/desk.json")" \
		"ghcr.io/daniellemccool/researchcloud-ddp-comments/deskproxy:v0.2.0-test"
	a_eq "fourcat_image" "$(jq -r '.fourcat_image' "$d/desk.json")" \
		"ghcr.io/daniellemccool/researchcloud-ddp-comments/4cat:v9.99"
	a_eq "package_version" "$(jq -r '.package_version' "$d/desk.json")" "v0.2.0-test"
	a_eq "env DESK_IMAGE" "$(grep '^DESK_IMAGE=' "$repoenv" | cut -d= -f2-)" \
		"ghcr.io/daniellemccool/researchcloud-ddp-comments/desk:v0.2.0-test"
	a_eq "env DESKPROXY_IMAGE" "$(grep '^DESKPROXY_IMAGE=' "$repoenv" | cut -d= -f2-)" \
		"ghcr.io/daniellemccool/researchcloud-ddp-comments/deskproxy:v0.2.0-test"
	a_eq "env FOURCAT_IMAGE" "$(grep '^FOURCAT_IMAGE=' "$repoenv" | cut -d= -f2-)" \
		"ghcr.io/daniellemccool/researchcloud-ddp-comments/4cat:v9.99"
	# The old copy is gone: the marker file the minimal copy carried must not
	# be there any more (the release tarball holds only deploy/).
	[ ! -e "$SYSROOT/repo/old-copy-marker" ] || a_fail "old repository copy still present"
	if ls "$SYSROOT"/*.old.* >/dev/null 2>&1; then
		a_fail "a *.old.* backup remains: $(ls "$SYSROOT"/*.old.*)"
	fi
	[ ! -e "$d/desk.json.new" ] || a_fail "desk.json.new remains"
}

case_update_pull_fails() {
	make_installed_copy "$SYSROOT/repo"
	touch "$SYSROOT/repo/old-copy-marker"
	run_init_repo "$SYSROOT/repo"
	a_rc 0
	local d
	d=$(desk)
	make_release_tarball "$SYSROOT/release.tar.gz"
	export DESK_RELEASE_TARBALL=$SYSROOT/release.tar.gz DESK_FAIL_PULL=1
	run_init_repo "$SYSROOT/repo" update v0.2.0-test
	a_rc 2
	a_err_has "docker pull failed"
	# The pull happens before the swap: the old copy, desk.json and .env are
	# unchanged.
	[ -e "$SYSROOT/repo/old-copy-marker" ] || a_fail "old repository copy was replaced"
	a_eq "desk_image unchanged" "$(jq -r '.desk_image' "$d/desk.json")" \
		"ghcr.io/daniellemccool/researchcloud-ddp-comments/desk:v0.1.0-alpha.1"
	a_eq "fourcat_image unchanged" "$(jq -r '.fourcat_image' "$d/desk.json")" \
		"digitalmethodsinitiative/4cat:v1.57"
	a_eq "env FOURCAT_IMAGE unchanged" \
		"$(grep '^FOURCAT_IMAGE=' "$SYSROOT/repo/deploy/.env" | cut -d= -f2-)" \
		"digitalmethodsinitiative/4cat:v1.57"
	if ls "$SYSROOT"/*.old.* >/dev/null 2>&1; then
		a_fail "a *.old.* backup remains: $(ls "$SYSROOT"/*.old.*)"
	fi
}

case_update_explicit_storage_path() {
	mkdir -p "$SYSROOT/data/vol2"
	PLUGIN_PARAMETERS=$(jq -c --arg sp "$SYSROOT/data/vol2" '.desk_storage_path = $sp' "$FIX/params-ok.json")
	export PLUGIN_PARAMETERS
	make_installed_copy "$SYSROOT/repo"
	run_init_repo "$SYSROOT/repo"
	a_rc 0
	local d
	d=$SYSROOT/data/vol2/desk
	a_file "$d/desk.json"
	# Drop .env so update must discover desk.json and use its explicit
	# desk_storage_path despite the two volumes under the sysroot.
	rm -f "$SYSROOT/repo/deploy/.env"
	make_release_tarball "$SYSROOT/release.tar.gz"
	export DESK_RELEASE_TARBALL=$SYSROOT/release.tar.gz
	run_init_repo "$SYSROOT/repo" update v0.2.0-test
	a_rc 0
	a_eq "desk_image" "$(jq -r '.desk_image' "$d/desk.json")" \
		"ghcr.io/daniellemccool/researchcloud-ddp-comments/desk:v0.2.0-test"
	a_eq "fourcat_image" "$(jq -r '.fourcat_image' "$d/desk.json")" \
		"ghcr.io/daniellemccool/researchcloud-ddp-comments/4cat:v9.99"
}

run_case ok-fresh case_ok_fresh
run_case python-repr-params case_python_repr_params
run_case placeholder-role case_placeholder_role
run_case uppercase-role case_uppercase_role
run_case crlf-value case_crlf_value
run_case trailing-lf-value case_trailing_lf_value
run_case quote-in-contact case_quote_in_contact
run_case two-failures case_two_failures
run_case role-held-by-nobody case_role_held_by_nobody
run_case admin-role-held-by-nobody case_admin_role_held_by_nobody
run_case two-volumes case_two_volumes
run_case storage-path-with-space case_storage_path_with_space
run_case too-many-digits case_too_many_digits
run_case fourcat-mirror-ref case_fourcat_mirror_ref
run_case reattach case_reattach
run_case reattach-newer-layout case_reattach_newer_layout
run_case sha-mismatch case_sha_mismatch
run_case apply-mode case_apply_mode
run_case bad-mode case_bad_mode
run_case update-ok case_update_ok
run_case update-pull-fails case_update_pull_fails
run_case update-explicit-storage-path case_update_explicit_storage_path

echo "$PASS passed, $FAIL failed"
if [ "$FAIL" -gt 0 ]; then
	echo "failed cases: ${FAILED[*]}"
	exit 1
fi
