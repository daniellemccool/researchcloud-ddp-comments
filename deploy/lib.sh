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

GHCR_NS="ghcr.io/daniellemccool/researchcloud-ddp-comments"

PARAM_NAMES="desk_collector_role desk_admin_role desk_platform desk_contact
desk_hide_media desk_batch_sizes desk_idle_seconds desk_timer_minutes
desk_max_sessions desk_max_running desk_storage_path fourcat_enabled
fourcat_image desk_image deskproxy_image zeeschuimer_version template_sha256
co_id co_name workspace_fqdn"

die() {
	printf 'init: %s\n' "$*" >&2
	exit 2
}

param_err() {
	ERRORS="${ERRORS}init: $1: $2
"
}

param() {
	printf '%s' "$PARAMS_JSON" | jq -j --arg k "$1" '.[$k]'
}

gen_hex32() {
	od -An -N16 -tx1 /dev/urandom | tr -d ' \n'
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
			printf '%s' "$v" | grep -Eq '^[0-9]+$' || param_err "$k" "must match ^[0-9]+$"
			;;
		desk_max_running)
			case $v in
			auto) ;;
			*) printf '%s' "$v" | grep -Eq '^[0-9]+$' || param_err "$k" "must be auto or a number" ;;
			esac
			;;
		desk_storage_path)
			case $v in
			auto | /*) ;;
			*) param_err "$k" "must be auto or an absolute path" ;;
			esac
			;;
		fourcat_image | desk_image | deskproxy_image)
			printf '%s' "$v" | grep -Eq '^[A-Za-z0-9][A-Za-z0-9./_-]*:[A-Za-z0-9._-]+$' ||
				param_err "$k" "must be a registry path with a tag"
			;;
		zeeschuimer_version)
			printf '%s' "$v" | grep -Eq '^[0-9]+(\.[0-9]+)*$' || param_err "$k" "must match ^[0-9]+(\.[0-9]+)*$"
			;;
		template_sha256)
			printf '%s' "$v" | grep -Eq '^[0-9a-f]{64}$' || param_err "$k" "must be 64 lowercase hex chars"
			;;
		workspace_fqdn)
			printf '%s' "$v" | grep -Eq '^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)+$' ||
				param_err "$k" "not a valid FQDN"
			;;
		esac
	done
}

read_workspace() {
	ws=$SYSROOT/etc/rsc/workspace.json
	[ -f "$ws" ] || die "workspace.json: not found at $ws"
	CO_TOKEN=$(jq -r '.co_token // empty' "$ws")
	CO_ENDPOINT=$(jq -r '.co_user_api_endpoint // empty' "$ws")
	[ -n "$CO_TOKEN" ] || die "workspace.json: missing co_token"
	[ -n "$CO_ENDPOINT" ] || die "workspace.json: missing co_user_api_endpoint"
}

load_members() {
	MEMBERS_FILE=$(mktemp)
	if [ -n "${DESK_CO_MEMBERS_FILE:-}" ]; then
		cp "$DESK_CO_MEMBERS_FILE" "$MEMBERS_FILE"
	else
		# The token goes through a curl config file, never a command line.
		curlcfg=$(mktemp)
		{
			printf 'silent\n'
			printf 'fail\n'
			printf 'header = "Authorization: %s"\n' "$CO_TOKEN"
			printf 'url = "%s"\n' "$CO_ENDPOINT"
		} >"$curlcfg"
		if ! curl --config "$curlcfg" --output "$MEMBERS_FILE"; then
			rm -f "$curlcfg"
			die "co_members: CO user API unreachable"
		fi
		rm -f "$curlcfg"
	fi
	if ! jq -e 'type == "array"' "$MEMBERS_FILE" >/dev/null 2>&1; then
		die "co_members: response is not a JSON array"
	fi
	[ "$(jq 'length' "$MEMBERS_FILE")" -gt 0 ] || die "co_members: no members returned"
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
	{
		printf 'DESK_ROOT=%s\n' "$DESK_ROOT"
		printf 'FQDN=%s\n' "$(param workspace_fqdn)"
		printf 'GHCR_NS=%s\n' "$GHCR_NS"
		printf 'DESK_IMAGE=%s\n' "$(param desk_image)"
		printf 'DESKPROXY_IMAGE=%s\n' "$(param deskproxy_image)"
		printf 'FOURCAT_IMAGE=%s\n' "$(param fourcat_image)"
		printf 'POSTGRES_PASSWORD=%s\n' "$PG_SECRET"
		printf 'REPO_ROOT=%s\n' "$REPO_ROOT"
	} >"$REPO_ROOT/deploy/.env"
	chmod 0600 "$REPO_ROOT/deploy/.env"
}

write_desk_json() {
	applied_at=$(date -u +%Y-%m-%dT%H:%M:%SZ)
	pkg=$(param desk_image)
	pkg=${pkg##*:}
	set --
	for k in $PARAM_NAMES; do
		set -- "$@" --arg "$k" "$(param "$k")"
	done
	jq -n "$@" \
		--arg desk_root "$DESK_ROOT" \
		--arg package_version "$pkg" \
		--arg applied_at "$applied_at" \
		'$ARGS.named + {layout_version: 1}' >"$DESK_ROOT/desk.json"
	chmod 0644 "$DESK_ROOT/desk.json"
}

install_nginx() {
	confdir=$SYSROOT/etc/nginx/app-location-conf.d
	mkdir -p "$confdir"
	for f in desk.conf fourcat.conf; do
		sed "s/__GATE__/$GATE/g" "$REPO_ROOT/deploy/nginx/$f" >"$confdir/$f"
		chmod 0600 "$confdir/$f"
		if grep -q __GATE__ "$confdir/$f"; then
			die "nginx: __GATE__ left in $f"
		fi
	done
	if [ "${DESK_NO_NGINX:-0}" != "1" ]; then
		nginx -t || die "nginx: configuration test failed"
		systemctl reload nginx || die "nginx: reload failed"
	fi
}

install_template() {
	platform=$(param desk_platform)
	zsv=$(param zeeschuimer_version)
	pkg=$(param desk_image)
	pkg=${pkg##*:}
	artifact="ddp-comments-template-$platform-$zsv.tar"
	url="https://github.com/daniellemccool/researchcloud-ddp-comments/releases/download/$pkg/$artifact"
	tmp=$(mktemp)
	if [ "${DESK_NO_FETCH:-0}" = "1" ]; then
		[ -n "${DESK_TEMPLATE_FILE:-}" ] || die "template: DESK_NO_FETCH=1 but DESK_TEMPLATE_FILE is unset"
		cp "$DESK_TEMPLATE_FILE" "$tmp"
	else
		curl --fail --location --silent --output "$tmp" "$url" || die "template: download failed: $url"
	fi
	got=$(sha256sum "$tmp" | awk '{print $1}')
	want=$(param template_sha256)
	if [ "$got" != "$want" ]; then
		rm -f "$tmp"
		die "template_sha256: mismatch (expected $want, got $got)"
	fi
	dest=$DESK_ROOT/templates/release/$platform-$zsv
	staging=$(mktemp -d "$DESK_ROOT/templates/release/.stage-XXXXXX")
	tar -xf "$tmp" -C "$staging"
	rm -f "$tmp"
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

pull_images() {
	if [ "${DESK_NO_DOCKER:-0}" = "1" ]; then
		return 0
	fi
	# The two fixed images are read from the compose file, not hardcoded.
	fixed=$(grep -E '^[[:space:]]+image: (postgres|memcached):[A-Za-z0-9._-]+$' "$REPO_ROOT/deploy/compose.yml" | awk '{print $2}')
	for img in "$(param desk_image)" "$(param deskproxy_image)" "$(param fourcat_image)" $fixed; do
		docker pull "$img" || die "docker pull failed: $img"
	done
}

write_wrapper() {
	mkdir -p "$SYSROOT/usr/local/bin"
	{
		printf '#!/bin/sh\n'
		printf 'exec docker compose -f %s/deploy/compose.yml exec desk desk "$@"\n' "$REPO_ROOT"
	} >"$SYSROOT/usr/local/bin/desk"
	chmod 0755 "$SYSROOT/usr/local/bin/desk"
}

platform_mode() {
	parse_plugin_parameters
	validate_params
	read_workspace
	load_members
	check_roles
	if [ -n "$ERRORS" ]; then
		printf '%s' "$ERRORS" >&2
		exit 2
	fi
	resolve_volume
	if [ -f "$DESK_ROOT/desk.json" ]; then
		MODE=reattach
		echo "init: reattach: existing desk.json found at $DESK_ROOT/desk.json"
	else
		MODE=fresh
	fi
	make_layout
	write_secrets
	write_env
	write_desk_json
	install_nginx
	install_template
	pull_images
	write_wrapper
	echo "init: mode: $MODE"
	echo "init: desk root: $DESK_ROOT"
	echo "init: .env: $REPO_ROOT/deploy/.env"
	echo "init: nginx: $SYSROOT/etc/nginx/app-location-conf.d/desk.conf $SYSROOT/etc/nginx/app-location-conf.d/fourcat.conf"
	echo "init: template: $DESK_ROOT/templates/release/$(param desk_platform)-$(param zeeschuimer_version)"
	if [ "${DESK_NO_DOCKER:-0}" = "1" ]; then
		echo "init: images: skipped (DESK_NO_DOCKER=1)"
	else
		echo "init: images: pulled"
	fi
	echo "init: wrapper: $SYSROOT/usr/local/bin/desk"
}

apply_mode() {
	# The desk root is found by the auto scan; desk.json then supplies the parameters.
	scan_single_volume
	[ -f "$DESK_ROOT/desk.json" ] || die "apply: no desk.json at $DESK_ROOT/desk.json"
	PARAMS_JSON=$(jq 'del(.desk_root, .layout_version, .package_version, .applied_at)' "$DESK_ROOT/desk.json")
	validate_params
	read_workspace
	load_members
	check_roles
	if [ -n "$ERRORS" ]; then
		printf '%s' "$ERRORS" >&2
		exit 2
	fi
	PG_SECRET=$(cat "$DESK_ROOT/secrets/4cat-postgres")
	write_env
	write_wrapper
	pull_images
	echo "docker compose -f $REPO_ROOT/deploy/compose.yml exec desk desk render"
}

update_mode() {
	release=$1
	if [ "${DESK_NO_FETCH:-0}" = "1" ]; then
		die "update: downloads are disabled (DESK_NO_FETCH=1)"
	fi
	tmp=$(mktemp -d)
	url="https://github.com/daniellemccool/researchcloud-ddp-comments/archive/refs/tags/$release.tar.gz"
	curl --fail --location --silent --output "$tmp/repo.tar.gz" "$url" || die "update: download failed: $url"
	mkdir "$tmp/x"
	tar -xzf "$tmp/repo.tar.gz" -C "$tmp/x"
	new=$(find "$tmp/x" -mindepth 1 -maxdepth 1 -type d)
	[ -n "$new" ] || die "update: archive layout unexpected"
	if [ -f "$REPO_ROOT/deploy/.env" ]; then
		cp "$REPO_ROOT/deploy/.env" "$tmp/env.keep"
	fi
	mv "$REPO_ROOT" "$REPO_ROOT.old.$$"
	mv "$new" "$REPO_ROOT"
	if [ -f "$tmp/env.keep" ]; then
		mkdir -p "$REPO_ROOT/deploy"
		cp "$tmp/env.keep" "$REPO_ROOT/deploy/.env"
	fi
	rm -rf "$REPO_ROOT.old.$$" "$tmp"
	apply_mode
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
