# shellcheck shell=sh
# Functions for deploy/init.sh. POSIX sh; sourced by init.sh, never executed.
# Globals set by init.sh before these functions run:
PARAMS_JSON=${PARAMS_JSON:-}
ERRORS=${ERRORS:-}
SYSROOT=${SYSROOT:-}
REPO_ROOT=${REPO_ROOT:-}
DESK_ROOT=
MODE=
GATE=
PG_SECRET=
CO_TOKEN=
CO_ENDPOINT=
MEMBERS_FILE=
VOLUME=
backup=
dj=
dj_keep=
dj_new=
swap_state=0
update_done=0
compose_started=0
TEMPLATE_TMP=
CLEANUP_FILES=""
CLEANUP_DIRS=""

GHCR_NS="ghcr.io/daniellemccool/researchcloud-ddp-comments"

# ADR-0020: the image and template pins come from the checked-out release
# (deploy/.env.example and platforms/<platform>/template.json), never from
# the catalog item. resolve_pins sets these globals; a component that still
# sends one of the retired names is refused.
PIN_DESK_IMAGE=
PIN_DESKPROXY_IMAGE=
PIN_FOURCAT_IMAGE=
PIN_ZSV=
PIN_TEMPLATE_SHA256=
PIN_RELEASE=

PARAM_NAMES="desk_collector_role desk_admin_role desk_platform desk_contact
desk_hide_media desk_batch_sizes desk_idle_seconds desk_timer_minutes
desk_max_sessions desk_max_running desk_storage_path fourcat_enabled
co_id co_name workspace_fqdn"

RETIRED_PARAMS="fourcat_image desk_image deskproxy_image zeeschuimer_version template_sha256"

die() {
	printf 'init: %s\n' "$*" >&2
	exit 2
}

# Collect a problem into ERRORS; the caller decides whether to continue.
collect_err() {
	ERRORS="${ERRORS}init: $1
"
}

param_err() {
	collect_err "$1: $2"
}

param() {
	printf '%s' "$PARAMS_JSON" | jq -j --arg k "$1" '.[$k]'
}

gen_hex32() {
	od -An -N16 -tx1 /dev/urandom | tr -d ' \n'
}

register_cleanup_file() {
	CLEANUP_FILES="$CLEANUP_FILES$1
"
}

register_cleanup_dir() {
	CLEANUP_DIRS="$CLEANUP_DIRS$1
"
}

cleanup() {
	# A set -e exit or a signal after the swap (swap_state != 0) but before
	# the update completed restores the previous repository copy first; the
	# kept desk.json lives outside the cleanup list, so it survives until
	# the restore has used it. update_restore runs FIRST, with no output
	# before it: a broken stderr (EIO after HUP, EPIPE on a dead pipe) must
	# not abort the restore.
	if [ "${swap_state:-0}" != 0 ] && [ "${update_done:-0}" = 0 ]; then
		update_restore
		if [ "${compose_started:-0}" = "1" ]; then
			printf 'init: update: containers may already run %s; fix the cause and re-run init.sh update %s\n' "${release:-}" "${release:-}" >&2 || :
			printf 'init: update: do not run compose up on the restored copy if the release changed FOURCAT_IMAGE (4CAT migrations may have run)\n' >&2 || :
		fi
	fi
	# Newline-separated lists, split on newlines only, so a registered path
	# containing a space is removed as one path.
	oldifs=$IFS
	IFS='
'
	# shellcheck disable=SC2086
	for f in $CLEANUP_FILES; do
		[ -n "$f" ] || continue
		rm -f "$f"
	done
	# shellcheck disable=SC2086
	for d in $CLEANUP_DIRS; do
		[ -n "$d" ] || continue
		rm -rf "$d"
	done
	IFS=$oldifs
}

parse_plugin_parameters() {
	raw=${PLUGIN_PARAMETERS:-}
	if [ -z "$raw" ]; then
		die "PLUGIN_PARAMETERS: not a JSON object"
	fi
	if printf '%s' "$raw" | jq -e 'type == "object"' >/dev/null 2>&1; then
		PARAMS_JSON=$raw
		return 0
	fi
	case $raw in
	"{"\'*)
		# Python dict representation: swap single quotes for double quotes.
		swapped=$(printf '%s' "$raw" | sed "s/'/\"/g")
		if printf '%s' "$swapped" | jq -e 'type == "object"' >/dev/null 2>&1; then
			PARAMS_JSON=$swapped
			return 0
		fi
		;;
	esac
	die "PLUGIN_PARAMETERS: not a JSON object"
}

validate_params() {
	ERRORS=""
	# Every value in the object must be a non-empty string without quotes or CR/LF.
	keys_file=$(mktemp)
	printf '%s' "$PARAMS_JSON" | jq -r 'keys[]' >"$keys_file"
	while IFS= read -r k; do
		t=$(printf '%s' "$PARAMS_JSON" | jq -r --arg k "$k" '.[$k] | type')
		if [ "$t" != "string" ]; then
			param_err "$k" "must be a string"
			continue
		fi
		v=$(param "$k")
		if [ -z "$v" ]; then
			param_err "$k" "must be a non-empty string"
			continue
		fi
		case $v in
		*'"'* | *"'"*)
			param_err "$k" "must not contain quotes"
			continue
			;;
		esac
		if printf '%s' "$PARAMS_JSON" | jq -e --arg k "$k" '(.[$k] | test("[\r\n]"))' >/dev/null 2>&1; then
			param_err "$k" "must not contain CR or LF"
			continue
		fi
	done <"$keys_file"
	rm -f "$keys_file"

	# ADR-0020: a pin sent as a parameter is refused; the release pins it.
	for k in $RETIRED_PARAMS; do
		if printf '%s' "$PARAMS_JSON" | jq -e --arg k "$k" 'has($k)' >/dev/null 2>&1; then
			param_err "$k" "no longer a parameter; the release pins it"
		fi
	done

	for k in $PARAM_NAMES; do
		if ! printf '%s' "$PARAMS_JSON" | jq -e --arg k "$k" 'has($k)' >/dev/null 2>&1; then
			param_err "$k" "missing"
			continue
		fi
		v=$(param "$k")
		case $k in
		desk_collector_role | desk_admin_role)
			if ! printf '%s' "$v" | grep -Eq '^src_co_[A-Za-z0-9_-]+$'; then
				param_err "$k" "must match ^src_co_[A-Za-z0-9_-]+$"
			fi
			case $v in
			*"<"*) param_err "$k" "must not contain a placeholder" ;;
			esac
			;;
		desk_platform)
			if ! printf '%s' "$v" | grep -Eq '^[a-z][a-z0-9_-]*$'; then
				param_err "$k" "must match ^[a-z][a-z0-9_-]*$"
			elif [ ! -f "$REPO_ROOT/platforms/$v/platform.json" ]; then
				param_err "$k" "no such platform: $v"
			fi
			;;
		desk_contact)
			case $v in
			*"<"*) param_err "$k" "must not contain a placeholder" ;;
			esac
			;;
		desk_hide_media | fourcat_enabled)
			case $v in
			true | false) ;;
			*) param_err "$k" "must be true or false" ;;
			esac
			;;
		desk_batch_sizes)
			printf '%s' "$v" | grep -Eq '^[0-9]+(,[0-9]+)*$' || param_err "$k" "must match ^[0-9]+(,[0-9]+)*$"
			;;
		desk_idle_seconds | desk_timer_minutes | desk_max_sessions)
			printf '%s' "$v" | grep -Eq '^[0-9]{1,9}$' || param_err "$k" "must be 1-9 digits"
			;;
		desk_max_running)
			case $v in
			auto) ;;
			*) printf '%s' "$v" | grep -Eq '^[0-9]{1,9}$' || param_err "$k" "must be auto or 1-9 digits" ;;
			esac
			;;
		desk_storage_path)
			tab=$(printf '\t')
			cr=$(printf '\r')
			lf=$(printf '\nX')
			lf=${lf%X}
			case $v in
			*" "* | *"$tab"* | *"$cr"* | *"$lf"*)
				param_err "$k" "must not contain whitespace"
				;;
			auto | /*) ;;
			*) param_err "$k" "must be auto or an absolute path" ;;
			esac
			;;
		workspace_fqdn)
			printf '%s' "$v" | grep -Eq '^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)+$' ||
				param_err "$k" "not a valid FQDN"
			;;
		esac
	done
}

# resolve_pins reads the release's own pins (ADR-0020): the three image
# references from <repo_root>/deploy/.env.example and the Zeeschuimer version
# plus template digest from <repo_root>/platforms/<platform>/template.json.
# It sets the PIN_* globals and collects problems with collect_err (prefix
# "pins: <KEY>" for .env.example, "template.json: <field>" for the manifest);
# returns 1 when any problem was found, 0 otherwise. It never calls die.
resolve_pins() {
	pins_repo=$1
	pins_platform=$2
	pins_env=$pins_repo/deploy/.env.example
	pins_manifest=$pins_repo/platforms/$pins_platform/template.json
	pins_rc=0
	pins_desk=""
	pins_deskproxy=""
	pins_fourcat_src=""
	for pins_key in DESK_IMAGE DESKPROXY_IMAGE FOURCAT_IMAGE; do
		pins_val=$(sed -n "s/^$pins_key=//p" "$pins_env" | head -n 1)
		if [ -z "$pins_val" ]; then
			collect_err "pins: $pins_key missing in deploy/.env.example"
			pins_rc=1
			continue
		fi
		if ! printf '%s' "$pins_val" | grep -Eq '^[A-Za-z0-9][A-Za-z0-9./_-]*:[A-Za-z0-9._-]+$'; then
			collect_err "pins: $pins_key must be a registry path with a tag"
			pins_rc=1
			continue
		fi
		case $pins_key in
		DESK_IMAGE) pins_desk=$pins_val ;;
		DESKPROXY_IMAGE) pins_deskproxy=$pins_val ;;
		FOURCAT_IMAGE) pins_fourcat_src=$pins_val ;;
		esac
	done
	if [ "$pins_rc" -eq 0 ]; then
		PIN_DESK_IMAGE=$pins_desk
		PIN_DESKPROXY_IMAGE=$pins_deskproxy
		PIN_RELEASE=${pins_desk##*:}
		if [ "${pins_deskproxy##*:}" != "$PIN_RELEASE" ]; then
			collect_err "pins: DESKPROXY_IMAGE tag differs from DESK_IMAGE tag"
			pins_rc=1
		fi
		# The regex check above ran first, so the source always has a tag.
		PIN_FOURCAT_IMAGE=$(fourcat_mirror_ref "$pins_fourcat_src")
	fi
	if [ ! -f "$pins_manifest" ]; then
		collect_err "template.json: not found at $pins_manifest"
		return 1
	fi
	# The manifest must be exactly ONE JSON object: jq judges only the last
	# value of a multi-object stream and prints one line per object, so two
	# concatenated objects would otherwise slip through the checks below.
	# Invalid JSON fails the same check; jq's own error stays off stderr.
	if ! jq -s -e 'length == 1 and (.[0] | type == "object")' "$pins_manifest" >/dev/null 2>&1; then
		collect_err "template.json: must hold exactly one JSON object"
		return 1
	fi
	if ! jq -e 'type == "object" and (keys == ["artifact","built_on","template_sha256","zeeschuimer_version"])' \
		"$pins_manifest" >/dev/null 2>&1; then
		collect_err "template.json: must hold exactly artifact, built_on, template_sha256, zeeschuimer_version"
		return 1
	fi
	pins_zsv=$(jq -r '.zeeschuimer_version' "$pins_manifest")
	pins_sha=$(jq -r '.template_sha256' "$pins_manifest")
	pins_artifact=$(jq -r '.artifact' "$pins_manifest")
	pins_built_on=$(jq -r '.built_on' "$pins_manifest")
	pins_zsv_ok=0
	for pins_field in zeeschuimer_version template_sha256 artifact built_on; do
		case $pins_field in
		zeeschuimer_version) pins_val=$pins_zsv ;;
		template_sha256) pins_val=$pins_sha ;;
		artifact) pins_val=$pins_artifact ;;
		built_on) pins_val=$pins_built_on ;;
		esac
		# A field must be a JSON string without CR or LF before any pattern is
		# applied: grep matches line by line, so a multi-line value would pass
		# whenever one of its lines fits, and jq -r stringifies non-strings.
		if ! jq -e --arg f "$pins_field" '.[$f] | type == "string" and (test("[\\r\\n]") | not)' \
			"$pins_manifest" >/dev/null 2>&1; then
			collect_err "template.json: $pins_field must be a single-line string"
			pins_rc=1
			continue
		fi
		# The value is a single line now, so -x anchors the whole value.
		case $pins_field in
		zeeschuimer_version)
			if printf '%s' "$pins_val" | grep -Eqx '[0-9]+(\.[0-9]+)*'; then
				pins_zsv_ok=1
			else
				collect_err "template.json: zeeschuimer_version must match ^[0-9]+(\.[0-9]+)*$"
				pins_rc=1
			fi
			;;
		template_sha256)
			if ! printf '%s' "$pins_val" | grep -Eqx '[0-9a-f]{64}'; then
				collect_err "template.json: template_sha256 must be 64 lowercase hex chars"
				pins_rc=1
			fi
			;;
		artifact)
			if [ "$pins_zsv_ok" = 1 ] && [ "$pins_val" != "ddp-comments-template-$pins_platform-$pins_zsv.tar" ]; then
				collect_err "template.json: artifact must be ddp-comments-template-$pins_platform-$pins_zsv.tar"
				pins_rc=1
			fi
			;;
		built_on)
			if ! printf '%s' "$pins_val" | grep -Eqx '[0-9]{4}-[0-9]{2}-[0-9]{2}'; then
				collect_err "template.json: built_on must be YYYY-MM-DD"
				pins_rc=1
			fi
			;;
		esac
	done
	if [ "$pins_rc" -eq 0 ]; then
		PIN_ZSV=$pins_zsv
		PIN_TEMPLATE_SHA256=$pins_sha
	fi
	return "$pins_rc"
}

read_workspace() {
	ws=$SYSROOT/etc/rsc/workspace.json
	if [ ! -f "$ws" ]; then
		collect_err "workspace.json: not found at $ws"
		return 1
	fi
	CO_TOKEN=$(jq -r '.co_token // empty' "$ws") || {
		collect_err "workspace.json: unreadable"
		return 1
	}
	CO_ENDPOINT=$(jq -r '.co_user_api_endpoint // empty' "$ws") || {
		collect_err "workspace.json: unreadable"
		return 1
	}
	if [ -z "$CO_TOKEN" ]; then
		collect_err "workspace.json: missing co_token"
		return 1
	fi
	if [ -z "$CO_ENDPOINT" ]; then
		collect_err "workspace.json: missing co_user_api_endpoint"
		return 1
	fi
	return 0
}

load_members() {
	MEMBERS_FILE=$(mktemp)
	register_cleanup_file "$MEMBERS_FILE"
	if [ -n "${DESK_CO_MEMBERS_FILE:-}" ]; then
		if ! cp "$DESK_CO_MEMBERS_FILE" "$MEMBERS_FILE"; then
			collect_err "co_members: cannot read $DESK_CO_MEMBERS_FILE"
			return 1
		fi
	else
		# The token goes through a curl config file, never a command line.
		curlcfg=$(mktemp)
		register_cleanup_file "$curlcfg"
		{
			printf 'silent\n'
			printf 'fail\n'
			printf 'header = "Authorization: %s"\n' "$CO_TOKEN"
			printf 'url = "%s"\n' "$CO_ENDPOINT"
		} >"$curlcfg"
		if ! curl --config "$curlcfg" --max-time 60 --connect-timeout 10 --output "$MEMBERS_FILE"; then
			collect_err "co_members: CO user API unreachable"
			return 1
		fi
	fi
	if ! jq -e 'type == "array"' "$MEMBERS_FILE" >/dev/null 2>&1; then
		collect_err "co_members: response is not a JSON array"
		return 1
	fi
	if [ "$(jq 'length' "$MEMBERS_FILE")" -eq 0 ]; then
		collect_err "co_members: no members returned"
		return 1
	fi
	return 0
}

role_held() {
	jq -e --arg r "$1" '[.[] | .roles[]?] | any(. == $r)' "$MEMBERS_FILE" >/dev/null 2>&1
}

check_roles() {
	for k in desk_collector_role desk_admin_role; do
		v=$(param "$k")
		if printf '%s' "$v" | grep -Eq '^src_co_[A-Za-z0-9_-]+$' && ! printf '%s' "$v" | grep -q '<'; then
			if ! role_held "$v"; then
				param_err "$k" "not held by any CO member"
			fi
		fi
	done
}

scan_single_volume() {
	vols=""
	for d in "$SYSROOT/data"/*/; do
		[ -d "$d" ] || continue
		name=$(basename "$d")
		if [ "$name" = "datasets" ]; then
			continue
		fi
		vols="$vols $name"
	done
	# shellcheck disable=SC2086
	set -- $vols
	if [ $# -ne 1 ]; then
		die "desk_storage_path: expected one volume under /data, found:$vols"
	fi
	VOLUME=$SYSROOT/data/$1
	DESK_ROOT=$VOLUME/desk
}

resolve_volume() {
	sp=$(param desk_storage_path)
	if [ "$sp" = "auto" ]; then
		scan_single_volume
	else
		case $sp in
		/*) ;;
		*) die "desk_storage_path: not an absolute path: $sp" ;;
		esac
		[ -d "$sp" ] || die "desk_storage_path: not a directory: $sp"
		VOLUME=$sp
		DESK_ROOT=$VOLUME/desk
	fi
}

make_layout() {
	mkdir -p "$DESK_ROOT/secrets/4cat-tokens" \
		"$DESK_ROOT/templates/release" \
		"$DESK_ROOT/state/users" "$DESK_ROOT/state/firefox" "$DESK_ROOT/state/proxy" \
		"$DESK_ROOT/state/captures" "$DESK_ROOT/state/retired" \
		"$DESK_ROOT/4cat/db" "$DESK_ROOT/4cat/data" "$DESK_ROOT/4cat/config" "$DESK_ROOT/4cat/logs" \
		"$DESK_ROOT/firefox" "$DESK_ROOT/compose" "$DESK_ROOT/locks"
	for d in "$DESK_ROOT/templates" "$DESK_ROOT/state" "$DESK_ROOT/4cat" \
		"$DESK_ROOT/state/users" "$DESK_ROOT/state/firefox" "$DESK_ROOT/state/proxy" \
		"$DESK_ROOT/state/captures" "$DESK_ROOT/state/retired" \
		"$DESK_ROOT/4cat/db" "$DESK_ROOT/4cat/data" "$DESK_ROOT/4cat/config" "$DESK_ROOT/4cat/logs" \
		"$DESK_ROOT/firefox" "$DESK_ROOT/compose" "$DESK_ROOT/locks" "$DESK_ROOT/templates/release"; do
		chmod 0755 "$d"
	done
	chmod 0700 "$DESK_ROOT/secrets" "$DESK_ROOT/secrets/4cat-tokens"
	if [ ! -f "$DESK_ROOT/pool.csv" ]; then
		printf 'id,kind,bucket,note\n' >"$DESK_ROOT/pool.csv"
	fi
	if [ ! -f "$DESK_ROOT/registry.tsv" ]; then
		: >"$DESK_ROOT/registry.tsv"
	fi
}

write_secrets() {
	GATE=$(gen_hex32)
	printf '%s\n' "$GATE" >"$DESK_ROOT/secrets/gate"
	chmod 0600 "$DESK_ROOT/secrets/gate"
	if [ ! -f "$DESK_ROOT/secrets/4cat-postgres" ]; then
		PG_SECRET=$(gen_hex32)
		printf '%s\n' "$PG_SECRET" >"$DESK_ROOT/secrets/4cat-postgres"
	fi
	PG_SECRET=$(cat "$DESK_ROOT/secrets/4cat-postgres")
	chmod 0600 "$DESK_ROOT/secrets/4cat-postgres"
	printf '%s\n' "$CO_TOKEN" >"$DESK_ROOT/secrets/co_token"
	chmod 0600 "$DESK_ROOT/secrets/co_token"
}

write_env() {
	# Written under umask 077 so the password is never world-readable, even
	# between creation and the chmod; the chmod stays as belt and braces.
	(
		umask 077
		{
			printf 'DESK_ROOT=%s\n' "$DESK_ROOT"
			printf 'FQDN=%s\n' "$(param workspace_fqdn)"
			printf 'GHCR_NS=%s\n' "$GHCR_NS"
			printf 'DESK_IMAGE=%s\n' "$PIN_DESK_IMAGE"
			printf 'DESKPROXY_IMAGE=%s\n' "$PIN_DESKPROXY_IMAGE"
			printf 'FOURCAT_IMAGE=%s\n' "$PIN_FOURCAT_IMAGE"
			printf 'POSTGRES_PASSWORD=%s\n' "$PG_SECRET"
			printf 'REPO_ROOT=%s\n' "$REPO_ROOT"
		} >"$REPO_ROOT/deploy/.env"
	)
	chmod 0600 "$REPO_ROOT/deploy/.env"
}

write_desk_json() {
	applied_at=$(date -u +%Y-%m-%dT%H:%M:%SZ)
	set --
	for k in $PARAM_NAMES; do
		set -- "$@" --arg "$k" "$(param "$k")"
	done
	# The five retired names are still recorded, from the release's pins, so
	# the key set stays at 24 for the Go side.
	jq -n "$@" \
		--arg fourcat_image "$PIN_FOURCAT_IMAGE" \
		--arg desk_image "$PIN_DESK_IMAGE" \
		--arg deskproxy_image "$PIN_DESKPROXY_IMAGE" \
		--arg zeeschuimer_version "$PIN_ZSV" \
		--arg template_sha256 "$PIN_TEMPLATE_SHA256" \
		--arg desk_root "$DESK_ROOT" \
		--arg package_version "$PIN_RELEASE" \
		--arg applied_at "$applied_at" \
		'$ARGS.named + {layout_version: 1}' >"$DESK_ROOT/desk.json"
	chmod 0644 "$DESK_ROOT/desk.json"
}

install_nginx() {
	confdir=$SYSROOT/etc/nginx/app-location-conf.d
	mkdir -p "$confdir"
	for f in desk.conf fourcat.conf; do
		# Written under umask 077 so the gate secret is never world-readable.
		(
			umask 077
			sed "s/__GATE__/$GATE/g" "$REPO_ROOT/deploy/nginx/$f" >"$confdir/$f"
		)
		chmod 0600 "$confdir/$f"
		if grep -q __GATE__ "$confdir/$f"; then
			die "nginx: __GATE__ left in $f"
		fi
	done
	if [ "${DESK_NO_NGINX:-0}" != "1" ]; then
		if ! nginx -t; then
			# nginx keeps running the old configuration; do not leave broken
			# files behind for the next reload.
			rm -f "$confdir/desk.conf" "$confdir/fourcat.conf"
			die "nginx: configuration test failed"
		fi
		systemctl reload nginx || die "nginx: reload failed"
	fi
}

# fetch_template downloads the release artifact (or copies DESK_TEMPLATE_FILE
# when DESK_NO_FETCH=1) to a temp file and verifies template_sha256 against it.
# It sets TEMPLATE_TMP to the verified temp path and prints nothing, so the
# caller invokes it directly (no command substitution): the temp file is then
# registered for cleanup in THIS shell and the EXIT trap removes it.
# install_template reuses that file, so the artifact is fetched exactly once.
# Called before any file is written, so a mismatch refuses the launch with
# nothing on disk.
fetch_template() {
	platform=$(param desk_platform)
	artifact="ddp-comments-template-$platform-$PIN_ZSV.tar"
	url="https://github.com/daniellemccool/researchcloud-ddp-comments/releases/download/$PIN_RELEASE/$artifact"
	tmp=$(mktemp)
	register_cleanup_file "$tmp"
	if [ "${DESK_NO_FETCH:-0}" = "1" ]; then
		[ -n "${DESK_TEMPLATE_FILE:-}" ] || die "template: DESK_NO_FETCH=1 but DESK_TEMPLATE_FILE is unset"
		cp "$DESK_TEMPLATE_FILE" "$tmp" || die "template: cannot copy $DESK_TEMPLATE_FILE"
	else
		curl --fail --location --silent --max-time 60 --connect-timeout 10 --output "$tmp" "$url" ||
			die "template: download failed: $url"
	fi
	got=$(sha256sum "$tmp" | awk '{print $1}')
	want=$PIN_TEMPLATE_SHA256
	if [ "$got" != "$want" ]; then
		die "template_sha256: mismatch (expected $want, got $got)"
	fi
	TEMPLATE_TMP=$tmp
}

# install_template extracts the already-verified artifact ($1) into the staging
# directory and moves it into place.
install_template() {
	tmp=$1
	platform=$(param desk_platform)
	dest=$DESK_ROOT/templates/release/$platform-$PIN_ZSV
	staging=$(mktemp -d "$DESK_ROOT/templates/release/.stage-XXXXXX")
	register_cleanup_dir "$staging"
	tar -xf "$tmp" -C "$staging"
	if [ "$(id -u)" = "0" ]; then
		chown -R 1000:1000 "$staging"
	fi
	find "$staging" -type d -exec chmod 0755 {} +
	find "$staging" -type f -exec chmod 0644 {} +
	if [ -e "$dest" ]; then
		mv "$dest" "$dest.old.$$"
	fi
	mv "$staging" "$dest"
	if [ -e "$dest.old.$$" ]; then
		rm -rf "$dest.old.$$"
	fi
}

# pull_images <compose_path> pulls the three pinned images plus the two
# fixed images (postgres, memcached) read from the given compose file, so the
# update path pre-pulls from the NEW copy's compose file.
pull_images() {
	pull_compose=$1
	# The two fixed images are read from the compose file, not hardcoded; a
	# compose edit that changes their count must fail loudly, not drop a pull.
	fixed=$(grep -E '^[[:space:]]+image: (postgres|memcached):[A-Za-z0-9._-]+$' "$pull_compose" | awk '{print $2}')
	# shellcheck disable=SC2086
	set -- $fixed
	if [ $# -ne 2 ]; then
		die "compose: expected exactly 2 fixed images (postgres, memcached), found $#"
	fi
	if [ "${DESK_NO_DOCKER:-0}" = "1" ]; then
		# Test override: simulate a failed pull while docker is disabled.
		if [ "${DESK_FAIL_PULL:-0}" = "1" ]; then
			die "docker pull failed: simulated failure (DESK_FAIL_PULL=1)"
		fi
		# Test override: list the images that would be pulled.
		if [ "${DESK_LIST_PULLS:-0}" = "1" ]; then
			for img in "$PIN_DESK_IMAGE" "$PIN_DESKPROXY_IMAGE" "$PIN_FOURCAT_IMAGE" "$@"; do
				printf 'init: would pull %s\n' "$img"
			done
		fi
		return 0
	fi
	for img in "$PIN_DESK_IMAGE" "$PIN_DESKPROXY_IMAGE" "$PIN_FOURCAT_IMAGE" "$@"; do
		docker pull "$img" || die "docker pull failed: $img"
	done
}

write_wrapper() {
	mkdir -p "$SYSROOT/usr/local/bin"
	(
		umask 077
		{
			printf '#!/bin/sh\n'
			printf 'exec docker compose -f "%s/deploy/compose.yml" exec desk desk "$@"\n' "$REPO_ROOT"
		} >"$SYSROOT/usr/local/bin/desk"
	)
	chmod 0755 "$SYSROOT/usr/local/bin/desk"
}

platform_mode() {
	parse_plugin_parameters
	validate_params
	# ADR-0020: resolve the release's pins; the ERRORS check below refuses
	# everything at once, so the return value is ignored here.
	platform=$(param desk_platform)
	if [ -f "$REPO_ROOT/platforms/$platform/platform.json" ]; then
		resolve_pins "$REPO_ROOT" "$platform" || true
	fi
	if read_workspace; then
		if load_members; then
			check_roles
		fi
	fi
	if [ -n "$ERRORS" ]; then
		printf '%s' "$ERRORS" >&2
		exit 2
	fi
	resolve_volume
	if [ -f "$DESK_ROOT/desk.json" ]; then
		# ADR-0016: gate on the existing layout version before writing anything.
		lv=$(jq -r '.layout_version // 0' "$DESK_ROOT/desk.json")
		case $lv in
		'' | *[!0-9]*) die "layout_version: desk.json has a non-integer layout_version: $lv" ;;
		esac
		if [ "$lv" -gt 1 ]; then
			die "layout_version: desk.json is layout $lv, this release knows 1"
		fi
		MODE=reattach
		echo "init: reattach: existing desk.json found at $DESK_ROOT/desk.json"
	else
		MODE=fresh
	fi
	# The template artifact is fetched and its sha256 verified before any file
	# is written, so a mismatch refuses the launch with nothing on disk.
	fetch_template
	make_layout
	write_secrets
	write_env
	write_desk_json
	install_nginx
	install_template "$TEMPLATE_TMP"
	pull_images "$REPO_ROOT/deploy/compose.yml"
	write_wrapper
	echo "init: mode: $MODE"
	echo "init: desk root: $DESK_ROOT"
	echo "init: .env: $REPO_ROOT/deploy/.env"
	echo "init: nginx: $SYSROOT/etc/nginx/app-location-conf.d/desk.conf $SYSROOT/etc/nginx/app-location-conf.d/fourcat.conf"
	echo "init: template: $DESK_ROOT/templates/release/$(param desk_platform)-$PIN_ZSV"
	if [ "${DESK_NO_DOCKER:-0}" = "1" ]; then
		echo "init: images: skipped (DESK_NO_DOCKER=1)"
	else
		echo "init: images: pulled"
	fi
	echo "init: wrapper: $SYSROOT/usr/local/bin/desk"
}

apply_mode() {
	# ADR-0017: apply only asks the running desk to re-render. It touches no
	# file: no .env rewrite, no pulls, no volume scan.
	render="docker compose -f $REPO_ROOT/deploy/compose.yml exec desk desk render"
	echo "$render"
	if [ "${DESK_NO_DOCKER:-0}" != "1" ]; then
		docker compose -f "$REPO_ROOT/deploy/compose.yml" exec desk desk render ||
			die "apply: render failed"
	fi
}

# fourcat_mirror_ref maps a 4CAT source reference (the Docker Hub image the
# release workflow mirrors from) onto the GHCR mirror, keeping only the tag:
# digitalmethodsinitiative/4cat:v1.57 -> <GHCR_NS>/4cat:v1.57. The .env.example
# value is never used verbatim; workspaces pull from the mirror.
fourcat_mirror_ref() {
	src=$1
	case $src in
	*:*) ;;
	*) die "fourcat_image: no tag in source reference: $src" ;;
	esac
	tag=${src##*:}
	printf '%s/4cat:%s\n' "$GHCR_NS" "$tag"
}

# update_restore puts the previous repository copy back according to
# swap_state (0 = not swapped, 1 = old copy moved to $backup, 2 = new copy in
# place), restores the kept desk.json, removes $dj_new and resets swap_state.
# Idempotent: a call with swap_state = 0 touches nothing. It runs with set +e
# and with HUP/INT/TERM/PIPE ignored, so one failing step, a broken stderr or
# a signal cannot abort or re-enter the rollback (the kept desk.json is
# outside the cleanup list and survives until here). State is recorded after
# each step, so a re-entry does the right thing; the new copy is moved aside,
# never rm -rf'd in place, and nothing is ever mv'd onto an existing
# $REPO_ROOT. When compose up had already started (compose_started=1), the
# moved-aside new copy is KEPT as $REPO_ROOT.failed.$$, because a running
# container may still mount it.
update_restore() {
	trap '' HUP INT TERM QUIT PIPE
	set +e
	printf 'init: update: restoring previous repository copy from %s\n' "$backup" >&2
	if [ "$swap_state" = "2" ] && [ -e "$backup" ] && [ -e "$REPO_ROOT" ]; then
		if mv "$REPO_ROOT" "$REPO_ROOT.failed.$$"; then
			swap_state=1
			if [ "${compose_started:-0}" = "1" ]; then
				printf 'init: update: the new copy is kept at %s because compose up had started\n' "$REPO_ROOT.failed.$$" >&2
			fi
		else
			printf 'init: update: could not move %s aside\n' "$REPO_ROOT" >&2
		fi
	fi
	if [ "$swap_state" -ge 1 ] && [ -e "$backup" ] && [ ! -e "$REPO_ROOT" ]; then
		if mv "$backup" "$REPO_ROOT"; then
			swap_state=0
		else
			printf 'init: update: could not move %s back\n' "$backup" >&2
		fi
	fi
	if [ "$swap_state" = "0" ] && [ -e "$REPO_ROOT.failed.$$" ] && [ "${compose_started:-0}" != "1" ]; then
		rm -rf "$REPO_ROOT.failed.$$"
	fi
	if [ -n "${dj_keep:-}" ] && [ -f "$dj_keep" ]; then
		if cp "$dj_keep" "$dj"; then
			rm -f "$dj_keep" ||
				printf 'init: update: could not remove %s\n' "$dj_keep" >&2
		else
			printf 'init: update: desk.json restore failed; the previous desk.json is kept at %s\n' "$dj_keep" >&2
		fi
	fi
	if [ -n "${dj_new:-}" ]; then
		rm -f "$dj_new"
	fi
	swap_state=0
	set -e
}

update_fail() {
	# PIPE ignored first: with a dead stderr pipe the printf below would
	# otherwise kill the shell with SIGPIPE before the restore runs.
	trap '' PIPE
	printf 'init: %s\n' "$*" >&2 || :
	update_restore
	if [ "${compose_started:-0}" = "1" ]; then
		printf 'init: update: containers may already run %s; fix the cause and re-run init.sh update %s\n' "${release:-}" "${release:-}" >&2 || :
		printf 'init: update: do not run compose up on the restored copy if the release changed FOURCAT_IMAGE (4CAT migrations may have run)\n' >&2 || :
	fi
	exit 2
}

# resolve_update_desk_root determines the desk root (and desk.json path) from
# the KEPT repository copy, before the swap: DESK_ROOT from deploy/.env, else
# desk_storage_path from the discovered desk.json, else the single-volume scan.
resolve_update_desk_root() {
	if [ -f "$REPO_ROOT/deploy/.env" ]; then
		dr=$(sed -n 's/^DESK_ROOT=//p' "$REPO_ROOT/deploy/.env" | head -n 1)
		if [ -n "$dr" ]; then
			DESK_ROOT=$dr
			dj=$DESK_ROOT/desk.json
			[ -f "$dj" ] || die "update: no desk.json at $dj"
			return 0
		fi
	fi
	found=""
	for d in "$SYSROOT/data"/*/; do
		[ -d "$d" ] || continue
		name=$(basename "$d")
		if [ "$name" = "datasets" ]; then
			continue
		fi
		if [ -f "$d/desk/desk.json" ]; then
			if [ -n "$found" ]; then
				die "update: more than one desk.json under $SYSROOT/data"
			fi
			found=$d/desk
		fi
	done
	if [ -n "$found" ]; then
		sp=$(jq -r '.desk_storage_path // empty' "$found/desk.json")
		if [ -n "$sp" ] && [ "$sp" != "auto" ]; then
			case $sp in
			/*) ;;
			*) die "update: desk_storage_path in desk.json is not an absolute path: $sp" ;;
			esac
			[ -d "$sp" ] || die "update: desk_storage_path in desk.json is not a directory: $sp"
			DESK_ROOT=$sp/desk
		else
			DESK_ROOT=$found
		fi
		dj=$DESK_ROOT/desk.json
		[ -f "$dj" ] || die "update: no desk.json at $dj"
		return 0
	fi
	scan_single_volume
	dj=$DESK_ROOT/desk.json
	[ -f "$dj" ] || die "update: no desk.json at $dj"
}

update_mode() {
	release=$1
	if ! printf '%s' "$release" | grep -Eq '^v[0-9A-Za-z._-]+$'; then
		die "update: invalid release name: $release"
	fi
	if [ -n "${DESK_RELEASE_TARBALL:-}" ]; then
		tarball=$DESK_RELEASE_TARBALL
		[ -f "$tarball" ] || die "update: DESK_RELEASE_TARBALL not found: $tarball"
	elif [ "${DESK_NO_FETCH:-0}" = "1" ]; then
		die "update: downloads are disabled (DESK_NO_FETCH=1)"
	else
		tarball=""
	fi
	# Extract under the repository's parent directory, so the swap is two
	# renames on one filesystem and a failed rename cannot nest the backup
	# inside a partial copy.
	parent=$(dirname "$REPO_ROOT")
	tmp=$(mktemp -d "$parent/.ddp-update-XXXXXX")
	register_cleanup_dir "$tmp"
	if [ -z "$tarball" ]; then
		url="https://github.com/daniellemccool/researchcloud-ddp-comments/archive/refs/tags/$release.tar.gz"
		tarball=$tmp/repo.tar.gz
		if ! curl --fail --location --silent --max-time 60 --connect-timeout 10 --output "$tarball" "$url"; then
			die "update: download failed: $url"
		fi
	fi
	mkdir "$tmp/x"
	if ! tar -xzf "$tarball" -C "$tmp/x"; then
		die "update: archive extraction failed"
	fi
	new=$(find "$tmp/x" -mindepth 1 -maxdepth 1 -type d)
	if [ -z "$new" ]; then
		die "update: archive layout unexpected"
	fi
	# Every fallible step that can leave the old copy behind happens before
	# the swap: the desk root is resolved from the kept copy, the new copy's
	# pins are resolved and checked, and the images are pulled from the new
	# copy's compose file.
	resolve_update_desk_root
	backup=$REPO_ROOT.old.$$
	if [ -e "$backup" ] || [ -e "$REPO_ROOT.failed.$$" ]; then
		die "update: stale $backup or $REPO_ROOT.failed.$$ exists; remove it first"
	fi
	if [ -f "$REPO_ROOT/deploy/.env" ]; then
		cp "$REPO_ROOT/deploy/.env" "$tmp/env.keep"
	fi
	# ADR-0020: the update adopts the NEW copy's pins (ADR-0020), so the
	# release's own .env.example and template.json are the source of truth.
	platform=$(jq -r '.desk_platform' "$dj")
	if ! resolve_pins "$new" "$platform"; then
		printf '%s' "$ERRORS" >&2
		die "update: the release's pins are invalid"
	fi
	# The desk tag IS the release tag: a tarball built for another release
	# must not be installed under this name.
	[ "$PIN_RELEASE" = "$release" ] ||
		die "update: the release's DESK_IMAGE tag $PIN_RELEASE differs from $release"
	# Installing a new template at update is not supported yet: refuse a
	# release whose manifest differs from what desk.json recorded.
	old_zsv=$(jq -r '.zeeschuimer_version' "$dj")
	old_sha=$(jq -r '.template_sha256' "$dj")
	if [ "$old_zsv" != "$PIN_ZSV" ] || [ "$old_sha" != "$PIN_TEMPLATE_SHA256" ]; then
		die "update: the release changes the template ($old_zsv -> $PIN_ZSV, sha $old_sha -> $PIN_TEMPLATE_SHA256); installing a new template at update is not supported yet, reattach the workspace on this release instead"
	fi
	# PARAMS_JSON carries desk.json's parameters (including the five retired
	# names) so param workspace_fqdn and the like keep working for write_env;
	# it is never validated, validate_params would refuse the retired names.
	PARAMS_JSON=$(jq 'del(.desk_root, .layout_version, .package_version, .applied_at)' "$dj")
	pull_images "$new/deploy/compose.yml"
	# The kept desk.json lives OUTSIDE the cleanup list ($tmp is removed by
	# cleanup), so a failed rollback cannot delete the only copy of the
	# previous desk.json. It is created only now, after every pre-swap
	# refusal, so a refused update leaves no desk.json.keep.<pid> behind.
	# Signals are ignored from the keep copy through the swap, so no signal
	# can land between the keep copy and the renames.
	trap '' HUP INT TERM QUIT
	dj_keep=$DESK_ROOT/desk.json.keep.$$
	cp "$dj" "$dj_keep"
	# The swap itself: two renames on one filesystem. Signals stay ignored
	# between them, so a HUP/INT/TERM/QUIT cannot land between the renames
	# and leave $REPO_ROOT missing; the EXIT trap still covers a set -e exit
	# in the window.
	mv "$REPO_ROOT" "$backup"
	swap_state=1
	if ! mv "$new" "$REPO_ROOT"; then
		update_restore
		if [ -e "$REPO_ROOT" ]; then
			die "update: install failed, original restored"
		else
			die "update: could not move $backup back; the previous copy is at $backup"
		fi
	fi
	swap_state=2
	trap 'exit 130' HUP INT TERM QUIT
	if [ -f "$tmp/env.keep" ]; then
		mkdir -p "$REPO_ROOT/deploy"
		cp "$tmp/env.keep" "$REPO_ROOT/deploy/.env"
	fi
	# From here on the running (old) lib.sh performs the remaining steps; the
	# new copy's own init.sh apply path is equivalent and is not re-exec'd.
	# desk.json is only moved into place once everything before compose up has
	# succeeded; every failure goes through update_fail, which restores the
	# backup and leaves desk.json/.env as they were.
	if [ ! -f "$DESK_ROOT/secrets/4cat-postgres" ]; then
		update_fail "update: no 4cat-postgres secret at $DESK_ROOT/secrets/4cat-postgres"
	fi
	PG_SECRET=$(cat "$DESK_ROOT/secrets/4cat-postgres")
	write_env
	dj_new=$dj.new
	if ! jq --arg di "$PIN_DESK_IMAGE" --arg dpi "$PIN_DESKPROXY_IMAGE" \
		--arg fci "$PIN_FOURCAT_IMAGE" --arg release "$release" \
		'.desk_image = $di | .deskproxy_image = $dpi | .fourcat_image = $fci | .package_version = $release' \
		"$dj" >"$dj_new"; then
		update_fail "update: could not rewrite desk.json"
	fi
	mv "$dj_new" "$dj"
	dj_new=""
	chmod 0644 "$dj"
	# Test hook (DESK_NO_DOCKER=1 only): a set -e exit inside the post-swap
	# window, after .env and desk.json were rewritten, to exercise the
	# EXIT-trap restore path.
	if [ "${DESK_NO_DOCKER:-0}" = "1" ] && [ "${DESK_FAIL_POSTSWAP:-0}" = "1" ]; then
		false
	fi
	up="docker compose -f $REPO_ROOT/deploy/compose.yml up -d"
	compose_started=1
	if [ "${DESK_NO_DOCKER:-0}" = "1" ]; then
		echo "$up"
		if [ "${DESK_FAIL_COMPOSE:-0}" = "1" ]; then
			update_fail "update: compose up failed"
		fi
	else
		docker compose -f "$REPO_ROOT/deploy/compose.yml" up -d ||
			update_fail "update: compose up failed"
	fi
	echo "docker compose -f $REPO_ROOT/deploy/compose.yml exec desk desk render"
	update_done=1
	rm -f "$dj_keep"
	rm -rf "$backup"
}

main() {
	case ${1:-} in
	"")
		platform_mode
		;;
	apply)
		[ $# -eq 1 ] || usage
		apply_mode
		;;
	update)
		[ $# -eq 2 ] || usage
		update_mode "$2"
		;;
	*)
		usage
		;;
	esac
}

usage() {
	echo "usage: $0 [apply | update <release>]" >&2
	exit 2
}
