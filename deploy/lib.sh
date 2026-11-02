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
update_swapped=0
CLEANUP_FILES=""
CLEANUP_DIRS=""

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
			printf 'DESK_IMAGE=%s\n' "$(param desk_image)"
			printf 'DESKPROXY_IMAGE=%s\n' "$(param deskproxy_image)"
			printf 'FOURCAT_IMAGE=%s\n' "$(param fourcat_image)"
			printf 'POSTGRES_PASSWORD=%s\n' "$PG_SECRET"
			printf 'REPO_ROOT=%s\n' "$REPO_ROOT"
		} >"$REPO_ROOT/deploy/.env"
	)
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
# It prints the verified temp path; install_template reuses that file, so the
# artifact is fetched exactly once. Called before any file is written, so a
# mismatch refuses the launch with nothing on disk.
fetch_template() {
	platform=$(param desk_platform)
	zsv=$(param zeeschuimer_version)
	pkg=$(param desk_image)
	pkg=${pkg##*:}
	artifact="ddp-comments-template-$platform-$zsv.tar"
	url="https://github.com/daniellemccool/researchcloud-ddp-comments/releases/download/$pkg/$artifact"
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
	want=$(param template_sha256)
	if [ "$got" != "$want" ]; then
		die "template_sha256: mismatch (expected $want, got $got)"
	fi
	printf '%s' "$tmp"
}

# install_template extracts the already-verified artifact ($1) into the staging
# directory and moves it into place.
install_template() {
	tmp=$1
	platform=$(param desk_platform)
	zsv=$(param zeeschuimer_version)
	dest=$DESK_ROOT/templates/release/$platform-$zsv
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

pull_images() {
	if [ "${DESK_NO_DOCKER:-0}" = "1" ]; then
		# Test override: simulate a failed pull while docker is disabled.
		if [ "${DESK_FAIL_PULL:-0}" = "1" ]; then
			die "docker pull failed: simulated failure (DESK_FAIL_PULL=1)"
		fi
		return 0
	fi
	# The two fixed images are read from the compose file, not hardcoded; a
	# compose edit that changes their count must fail loudly, not drop a pull.
	fixed=$(grep -E '^[[:space:]]+image: (postgres|memcached):[A-Za-z0-9._-]+$' "$REPO_ROOT/deploy/compose.yml" | awk '{print $2}')
	# shellcheck disable=SC2086
	set -- $fixed
	if [ $# -ne 2 ]; then
		die "compose: expected exactly 2 fixed images (postgres, memcached), found $#"
	fi
	for img in "$(param desk_image)" "$(param deskproxy_image)" "$(param fourcat_image)" "$@"; do
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
	template_tmp=$(fetch_template)
	make_layout
	write_secrets
	write_env
	write_desk_json
	install_nginx
	install_template "$template_tmp"
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

update_fail() {
	printf 'init: %s\n' "$*" >&2
	if [ "${update_swapped:-0}" = "1" ]; then
		printf 'init: update: restoring previous repository copy from %s\n' "$backup" >&2
		rm -rf "$REPO_ROOT"
		mv "$backup" "$REPO_ROOT"
		update_swapped=0
		if [ -n "${dj_keep:-}" ] && [ -f "$dj_keep" ]; then
			cp "$dj_keep" "$dj"
		fi
	else
		printf 'init: update: previous repository copy kept at %s\n' "$backup" >&2
	fi
	if [ -n "${dj_new:-}" ]; then
		rm -f "$dj_new"
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
	# the swap: the desk root is resolved from the kept copy, the new image
	# references are computed, and the images are pulled.
	resolve_update_desk_root
	dj_keep=$tmp/dj.keep
	cp "$dj" "$dj_keep"
	backup=$REPO_ROOT.old.$$
	if [ -f "$REPO_ROOT/deploy/.env" ]; then
		cp "$REPO_ROOT/deploy/.env" "$tmp/env.keep"
	fi
	desk_image=$(jq -r '.desk_image' "$dj")
	deskproxy_image=$(jq -r '.deskproxy_image' "$dj")
	new_env_example=$new/deploy/.env.example
	if [ ! -f "$new_env_example" ]; then
		die "update: the release has no deploy/.env.example"
	fi
	src_fourcat=$(sed -n 's/^FOURCAT_IMAGE=//p' "$new_env_example" | head -n 1)
	if [ -z "$src_fourcat" ]; then
		die "update: FOURCAT_IMAGE missing in the release's deploy/.env.example"
	fi
	fourcat_image=$(fourcat_mirror_ref "$src_fourcat")
	PARAMS_JSON=$(jq --arg di "${desk_image%:*}:$release" --arg dpi "${deskproxy_image%:*}:$release" \
		--arg fci "$fourcat_image" --arg release "$release" \
		'.desk_image = $di | .deskproxy_image = $dpi | .fourcat_image = $fci | .package_version = $release
		 | del(.desk_root, .layout_version, .package_version, .applied_at)' "$dj")
	pull_images
	# The swap itself: two renames on one filesystem.
	mv "$REPO_ROOT" "$backup"
	if ! mv "$new" "$REPO_ROOT"; then
		mv "$backup" "$REPO_ROOT"
		die "update: install failed, original restored"
	fi
	update_swapped=1
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
	if ! jq --arg di "${desk_image%:*}:$release" --arg dpi "${deskproxy_image%:*}:$release" \
		--arg fci "$fourcat_image" --arg release "$release" \
		'.desk_image = $di | .deskproxy_image = $dpi | .fourcat_image = $fci | .package_version = $release' \
		"$dj" >"$dj_new"; then
		update_fail "update: could not rewrite desk.json"
	fi
	mv "$dj_new" "$dj"
	dj_new=""
	chmod 0644 "$dj"
	up="docker compose -f $REPO_ROOT/deploy/compose.yml up -d"
	if [ "${DESK_NO_DOCKER:-0}" = "1" ]; then
		echo "$up"
	else
		docker compose -f "$REPO_ROOT/deploy/compose.yml" up -d ||
			update_fail "update: compose up failed"
	fi
	echo "docker compose -f $REPO_ROOT/deploy/compose.yml exec desk desk render"
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
