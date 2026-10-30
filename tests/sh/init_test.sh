#!/usr/bin/env bash
# Test suite for deploy/init.sh. Run from the repository root or via
# scripts/check.sh. Each case builds a throwaway DESK_SYSROOT and runs the
# init script against it with docker, nginx and network fetches disabled.
set -euo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")/../.."
REPO_ROOT=$PWD
FIX=$REPO_ROOT/tests/sh/fixtures

PASS=0
FAIL=0
FAILED=()
CASE_ERR=""
SYSROOT=""
RC=0
OUT=""
ERR=""

new_sysroot() {
	SYSROOT=$(mktemp -d)
	mkdir -p "$SYSROOT/etc/rsc" "$SYSROOT/data/vol1" \
		"$SYSROOT/etc/nginx/app-location-conf.d" "$SYSROOT/usr/local/bin"
	cp "$FIX/workspace.json" "$SYSROOT/etc/rsc/workspace.json"
	export DESK_SYSROOT=$SYSROOT DESK_NO_DOCKER=1 DESK_NO_NGINX=1 DESK_NO_FETCH=1
	export DESK_TEMPLATE_FILE=$FIX/template.tar DESK_CO_MEMBERS_FILE=$FIX/members.json
	PLUGIN_PARAMETERS=$(cat "$FIX/params-ok.json")
	export PLUGIN_PARAMETERS
	rm -f "$REPO_ROOT/deploy/.env"
}

run_init() {
	set +e
	OUT=$(sh "$REPO_ROOT/deploy/init.sh" "$@" 2>"$SYSROOT/stderr.txt")
	RC=$?
	set -e
	ERR=$(cat "$SYSROOT/stderr.txt")
}

a_rc() {
	[ "$RC" -eq "$1" ] || {
		CASE_ERR="expected exit $1, got $RC (stderr: $ERR)"
		return 1
	}
}
a_err_has() {
	case $ERR in
	*"$1"*) ;;
	*)
		CASE_ERR="stderr lacks [$1]: $ERR"
		return 1
		;;
	esac
}
a_out_has() {
	case $OUT in
	*"$1"*) ;;
	*)
		CASE_ERR="stdout lacks [$1]: $OUT"
		return 1
		;;
	esac
}
a_file() {
	[ -f "$1" ] || {
		CASE_ERR="missing file: $1"
		return 1
	}
}
a_eq() {
	[ "$2" = "$3" ] || {
		CASE_ERR="$1: expected [$3], got [$2]"
		return 1
	}
}
a_mode() {
	local m
	m=$(stat -c %a "$2")
	[ "$m" = "$3" ] || {
		CASE_ERR="$1: mode $m != $3"
		return 1
	}
}

run_case() {
	local name=$1
	shift
	CASE_ERR=""
	if "$@"; then
		echo "ok $name"
		PASS=$((PASS + 1))
	else
		echo "FAIL $name: ${CASE_ERR:-failed}"
		FAIL=$((FAIL + 1))
		FAILED+=("$name")
	fi
	rm -f "$REPO_ROOT/deploy/.env"
	if [ -n "$SYSROOT" ]; then
		rm -rf "$SYSROOT"
	fi
	SYSROOT=""
}

desk() { printf '%s/data/vol1/desk' "$SYSROOT"; }
envval() { grep "^$1=" "$REPO_ROOT/deploy/.env" | cut -d= -f2-; }

case_ok_fresh() {
	new_sysroot
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
	'' | *[!0-9a-f]*)
		CASE_ERR="gate not lowercase hex: [$gate]"
		return 1
		;;
	esac
	[ "${#gate}" -eq 32 ] || {
		CASE_ERR="gate length ${#gate} != 32"
		return 1
	}
	a_mode "4cat-postgres mode" "$d/secrets/4cat-postgres" "600"
	a_eq "co_token value" "$(cat "$d/secrets/co_token")" "fixture-token-not-real"
	a_mode "co_token mode" "$d/secrets/co_token" "600"
	local f
	for f in desk.conf fourcat.conf; do
		a_file "$SYSROOT/etc/nginx/app-location-conf.d/$f"
		a_mode "nginx $f mode" "$SYSROOT/etc/nginx/app-location-conf.d/$f" "600"
		grep -q "$gate" "$SYSROOT/etc/nginx/app-location-conf.d/$f" || {
			CASE_ERR="$f does not contain the gate secret"
			return 1
		}
		if grep -q __GATE__ "$SYSROOT/etc/nginx/app-location-conf.d/$f"; then
			CASE_ERR="$f still contains __GATE__"
			return 1
		fi
	done
	a_file "$d/templates/release/tiktok-1.14.2/manifest.json"
	if [ "$(id -u)" = "0" ]; then
		a_eq "template owner uid" "$(stat -c %u "$d/templates/release/tiktok-1.14.2/manifest.json")" "1000"
	fi
	a_eq "pool.csv header" "$(head -n1 "$d/pool.csv")" "id,kind,bucket,note"
	a_file "$SYSROOT/usr/local/bin/desk"
	[ -x "$SYSROOT/usr/local/bin/desk" ] || {
		CASE_ERR="wrapper not executable"
		return 1
	}
	grep -q "deploy/compose.yml" "$SYSROOT/usr/local/bin/desk" || {
		CASE_ERR="wrapper does not name the compose path"
		return 1
	}
}

case_python_repr_params() {
	new_sysroot
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
	new_sysroot
	PLUGIN_PARAMETERS=$(jq -c '.desk_collector_role = "src_co_<fill-me-in>"' "$FIX/params-ok.json")
	export PLUGIN_PARAMETERS
	run_init
	a_rc 2
	a_err_has "desk_collector_role"
	[ ! -e "$(desk)" ] || {
		CASE_ERR="desk root was written"
		return 1
	}
	[ ! -e "$REPO_ROOT/deploy/.env" ] || {
		CASE_ERR=".env was written"
		return 1
	}
	[ -z "$(ls -A "$SYSROOT/etc/nginx/app-location-conf.d")" ] || {
		CASE_ERR="nginx files were written"
		return 1
	}
}

case_uppercase_role() {
	new_sysroot
	PLUGIN_PARAMETERS=$(jq -c '.desk_collector_role = "Src_co_students"' "$FIX/params-ok.json")
	export PLUGIN_PARAMETERS
	run_init
	a_rc 2
	a_err_has "desk_collector_role"
}

case_crlf_value() {
	new_sysroot
	PLUGIN_PARAMETERS=$(jq -c --arg c $'Danielle\r\n' '.desk_contact = $c' "$FIX/params-ok.json")
	export PLUGIN_PARAMETERS
	run_init
	a_rc 2
	a_err_has "desk_contact"
}

case_quote_in_contact() {
	new_sysroot
	PLUGIN_PARAMETERS=$(jq -c --arg c "Danielle O'Brien" '.desk_contact = $c' "$FIX/params-ok.json")
	export PLUGIN_PARAMETERS
	run_init
	a_rc 2
	a_err_has "desk_contact"
}

case_two_failures() {
	new_sysroot
	PLUGIN_PARAMETERS=$(jq -c '.desk_collector_role = "src_co_<fill-me-in>" | .desk_idle_seconds = "abc"' "$FIX/params-ok.json")
	export PLUGIN_PARAMETERS
	run_init
	a_rc 2
	a_err_has "desk_collector_role"
	a_err_has "desk_idle_seconds"
}

case_role_held_by_nobody() {
	new_sysroot
	jq 'map(if .username == "dmccool" then .roles = ["src_co_admin"]
	      elif .username == "dmccool2" then .roles = []
	      else . end)' "$FIX/members.json" >"$SYSROOT/members.json"
	export DESK_CO_MEMBERS_FILE=$SYSROOT/members.json
	run_init
	a_rc 2
	a_err_has "desk_collector_role"
}

case_admin_role_held_by_nobody() {
	new_sysroot
	jq 'map(.roles |= map(select(. != "src_co_admin")))' "$FIX/members.json" >"$SYSROOT/members.json"
	export DESK_CO_MEMBERS_FILE=$SYSROOT/members.json
	run_init
	a_rc 2
	a_err_has "desk_admin_role"
}

case_two_volumes() {
	new_sysroot
	mkdir -p "$SYSROOT/data/vol2"
	run_init
	a_rc 2
	a_err_has "desk_storage_path"
	a_err_has "vol1"
	a_err_has "vol2"
}

case_reattach() {
	new_sysroot
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
	[ "$pg1" = "$pg2" ] || {
		CASE_ERR="4cat-postgres changed on reattach"
		return 1
	}
	[ "$gate1" != "$gate2" ] || {
		CASE_ERR="gate did not change on reattach"
		return 1
	}
	for f in desk.conf fourcat.conf; do
		grep -q "$gate2" "$SYSROOT/etc/nginx/app-location-conf.d/$f" || {
			CASE_ERR="$f does not hold the new gate secret"
			return 1
		}
	done
}

case_sha_mismatch() {
	new_sysroot
	PLUGIN_PARAMETERS=$(jq -c '.template_sha256 = "0000000000000000000000000000000000000000000000000000000000000000"' "$FIX/params-ok.json")
	export PLUGIN_PARAMETERS
	run_init
	a_rc 2
	a_err_has "template_sha256"
	local d
	d=$(desk)
	if [ -e "$d/templates/release/tiktok-1.14.2" ]; then
		CASE_ERR="template was extracted despite sha mismatch"
		return 1
	fi
	[ -z "$(ls -A "$d/templates/release" 2>/dev/null)" ] || {
		CASE_ERR="templates/release not empty after mismatch"
		return 1
	}
}

case_apply_mode() {
	new_sysroot
	run_init
	a_rc 0
	rm -f "$REPO_ROOT/deploy/.env"
	run_init apply
	a_rc 0
	a_file "$REPO_ROOT/deploy/.env"
	a_eq "env DESK_ROOT after apply" "$(envval DESK_ROOT)" "$(desk)"
	a_out_has "exec desk desk render"
}

case_bad_mode() {
	new_sysroot
	run_init frobnicate
	a_rc 2
	a_err_has "usage"
}

run_case ok-fresh case_ok_fresh
run_case python-repr-params case_python_repr_params
run_case placeholder-role case_placeholder_role
run_case uppercase-role case_uppercase_role
run_case crlf-value case_crlf_value
run_case quote-in-contact case_quote_in_contact
run_case two-failures case_two_failures
run_case role-held-by-nobody case_role_held_by_nobody
run_case admin-role-held-by-nobody case_admin_role_held_by_nobody
run_case two-volumes case_two_volumes
run_case reattach case_reattach
run_case sha-mismatch case_sha_mismatch
run_case apply-mode case_apply_mode
run_case bad-mode case_bad_mode

echo "$PASS passed, $FAIL failed"
if [ "$FAIL" -gt 0 ]; then
	echo "failed cases: ${FAILED[*]}"
	exit 1
fi
