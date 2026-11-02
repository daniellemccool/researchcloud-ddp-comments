#!/usr/bin/env bash
# retire-hand-install.sh — remove every trace of the hand-made desk install on this box.
#
# Run-once: refuses (exit 2) after the compose desk is installed, since it shares the new desk's paths.
#
# Dry-run by default: prints one line per action it WOULD take and changes nothing.
# Real run (owner only, as root):  retire-hand-install.sh --yes
#
# Paths are hardcoded: this script is for this one box only.
# The record of what the hand install consists of (the box inventory of 2026-10-29) is kept privately.

set -euo pipefail

# --- Hardcoded paths for this box -------------------------------------------

readonly STUB_UNIT=/etc/systemd/system/desk-stub.service

readonly OPT_DIR=/opt/ddp-comments
readonly -a BIN_LINKS=(
	/usr/local/bin/desk-admin
	/usr/local/bin/desk-sessions
	/usr/local/bin/desk-template
)

readonly -a NGINX_FILES=(
	/etc/nginx/app-location-conf.d/desk.conf
	/etc/nginx/app-location-conf.d/fourcat.conf
	/etc/nginx/conf.d/desk-map.conf
	/etc/nginx/conf.d/websocket-map.conf
)
# Never touched: authentication.conf, tls_main.conf, map_upgrade.conf, nginx.conf, /etc/rsc/.

readonly -a FOURCAT_COMPOSE_FILES=(
	/home/dmccool/4cat/docker-compose_public_ip.yml
	/home/dmccool/4cat/docker-compose.desk.yml
)
readonly DESK_FOURCAT_NETWORK=desk-fourcat

readonly DESK_ROOT=/data/onlinehate_zeeschuimer/desk
readonly TEMPLATE_SRC=/data/onlinehate_zeeschuimer/desk/state/firefox/_template
readonly TEMPLATE_BACKUP=/home/dmccool/_template-profile-backup.tar.gz

readonly -a HOME_LEFTOVERS=(
	/home/dmccool/4cat
	/home/dmccool/spike
	/home/dmccool/proxy-spike
	/home/dmccool/box-inventory
	/home/dmccool/vibe-probe
)

# --- Mode and privilege checks ----------------------------------------------

MODE=dry-run
case $# in
0) MODE=dry-run ;;
1)
	case $1 in
	--dry-run) MODE=dry-run ;;
	--yes) MODE=yes ;;
	*)
		echo "usage: $0 [--dry-run|--yes]" >&2
		exit 2
		;;
	esac
	;;
*)
	echo "usage: $0 [--dry-run|--yes]" >&2
	exit 2
	;;
esac

if [[ "$(id -u)" -ne 0 ]]; then
	echo "error: must run as root (try: sudo bash $0 --$MODE)" >&2
	exit 1
fi

die() {
	echo "error: $*" >&2
	exit 1
}

docker info >/dev/null 2>&1 || die "docker daemon not reachable (docker info failed)"

# Run-once guard: these are the compose desk's own paths, so any of them means the
# old hand install is already retired and a second run would wipe the new desk.
readonly -a NEW_DESK_MARKERS=(
	/rsc/plugins/ddp-comments/deploy/.env
	/data/onlinehate_zeeschuimer/desk/desk.json
	/data/onlinehate_zeeschuimer/desk/secrets/gate
)
for marker in "${NEW_DESK_MARKERS[@]}"; do
	if [[ -e $marker || -L $marker ]]; then
		echo "error: refusing to run: $marker exists, so the compose desk is installed; this script is run-once" >&2
		exit 2
	fi
done
if docker inspect desk-desk-1 >/dev/null 2>&1; then
	echo "error: refusing to run: container desk-desk-1 exists, so the compose desk is installed; this script is run-once" >&2
	exit 2
fi

# --- Helpers -----------------------------------------------------------------

state() {
	if [[ -e $1 || -L $1 ]]; then
		echo present
	else
		echo absent
	fi
}

unit_loaded() {
	systemctl cat desk-stub.service >/dev/null 2>&1
}

desk_containers() {
	docker ps -a --format '{{.Names}}' | grep -E '^desk-(mitm|firefox)-' | sort || true
}

desk_networks() {
	docker network ls --format '{{.Name}}' | grep '^desk_net-' | sort || true
}

network_exists() {
	docker network inspect "$1" >/dev/null 2>&1
}

compose_files_exist() {
	local f
	for f in "${FOURCAT_COMPOSE_FILES[@]}"; do
		[[ -f $f ]] || return 1
	done
	return 0
}

nginx_backup_dir() {
	echo "/home/dmccool/_retired-nginx-$(date +%Y%m%d)"
}

# --- Dry run -----------------------------------------------------------------

dry_run() {
	local c n f dir

	echo "DRY RUN — no changes will be made."

	echo "would archive $TEMPLATE_SRC -> $TEMPLATE_BACKUP (source $(state "$TEMPLATE_SRC"), backup $(state "$TEMPLATE_BACKUP"))"

	if unit_loaded; then
		echo "would stop and disable desk-stub.service (unit $(state "$STUB_UNIT"), loaded)"
	else
		echo "would stop and disable desk-stub.service (unit $(state "$STUB_UNIT"), not loaded)"
	fi
	echo "would remove $STUB_UNIT ($(state "$STUB_UNIT"))"
	echo "would run systemctl daemon-reload"

	local containers
	containers="$(desk_containers)"
	if [[ -n $containers ]]; then
		while IFS= read -r c; do
			echo "would remove container $c (present)"
		done <<<"$containers"
	else
		echo "would remove desk-mitm-* / desk-firefox-* containers (absent)"
	fi

	local networks
	networks="$(desk_networks)"
	if [[ -n $networks ]]; then
		while IFS= read -r n; do
			echo "would remove network $n (present)"
		done <<<"$networks"
	else
		echo "would remove desk_net-* networks (absent)"
	fi

	if compose_files_exist; then
		echo "would run docker compose -f ${FOURCAT_COMPOSE_FILES[0]} -f ${FOURCAT_COMPOSE_FILES[1]} down -v (compose files present)"
	else
		echo "would run docker compose down -v (compose files absent, skipped)"
	fi
	if network_exists "$DESK_FOURCAT_NETWORK"; then
		echo "would remove network $DESK_FOURCAT_NETWORK (present)"
	else
		echo "would remove network $DESK_FOURCAT_NETWORK (absent)"
	fi

	echo "would remove $OPT_DIR ($(state "$OPT_DIR"))"
	for f in "${BIN_LINKS[@]}"; do
		echo "would remove symlink $f ($(state "$f"))"
	done
	dir="$(nginx_backup_dir)"
	echo "would copy the four hand nginx files to $dir ($(state "$dir")) before removing them"
	for f in "${NGINX_FILES[@]}"; do
		echo "would remove $f ($(state "$f"))"
	done

	echo "would run nginx -t, then systemctl reload nginx"

	echo "would remove $DESK_ROOT ($(state "$DESK_ROOT"))"
	for f in "${HOME_LEFTOVERS[@]}"; do
		echo "would remove $f ($(state "$f"))"
	done

	echo "DRY RUN complete — nothing was changed."
	exit 0
}

# --- Real run ----------------------------------------------------------------

run_yes() {
	local c n f count tmp dir

	echo "== 1. archive the _template firefox profile =="
	if [[ ! -e $TEMPLATE_SRC ]]; then
		if [[ -f $TEMPLATE_BACKUP ]]; then
			echo "source gone, keeping existing backup $TEMPLATE_BACKUP"
		else
			die "$TEMPLATE_SRC absent and no backup at $TEMPLATE_BACKUP; aborting, nothing else removed"
		fi
	else
		tmp="$TEMPLATE_BACKUP.tmp"
		if ! (umask 077 && tar -czf "$tmp" -C "$(dirname "$TEMPLATE_SRC")" "$(basename "$TEMPLATE_SRC")"); then
			rm -f "$tmp"
			die "could not write $tmp; aborting, nothing else removed"
		fi
		if ! tar -tzf "$tmp" >/dev/null; then
			rm -f "$tmp"
			die "archive $tmp failed verification; aborting, nothing else removed"
		fi
		mv "$tmp" "$TEMPLATE_BACKUP"
		chown dmccool:dmccool "$TEMPLATE_BACKUP"
		count="$(tar -tzf "$TEMPLATE_BACKUP" | wc -l)"
		echo "archived $TEMPLATE_SRC -> $TEMPLATE_BACKUP ($count entries)"
	fi

	echo "== 2. stop and remove desk-stub.service =="
	if unit_loaded; then
		echo "systemctl disable --now desk-stub.service"
		systemctl disable --now desk-stub.service
	else
		echo "desk-stub.service already absent"
	fi
	echo "rm -f $STUB_UNIT"
	rm -f "$STUB_UNIT"
	echo "systemctl daemon-reload"
	systemctl daemon-reload

	echo "== 3. remove desk containers and networks =="
	while IFS= read -r c; do
		[[ -n $c ]] || continue
		echo "docker rm -f $c"
		docker rm -f "$c"
	done <<<"$(desk_containers)"
	while IFS= read -r n; do
		[[ -n $n ]] || continue
		echo "docker network rm $n"
		docker network rm "$n"
	done <<<"$(desk_networks)"

	echo "== 4. take down the hand-run 4CAT =="
	if compose_files_exist; then
		echo "docker compose -f ${FOURCAT_COMPOSE_FILES[0]} -f ${FOURCAT_COMPOSE_FILES[1]} down -v"
		docker compose -f "${FOURCAT_COMPOSE_FILES[0]}" -f "${FOURCAT_COMPOSE_FILES[1]}" down -v
	else
		echo "compose files absent; skipping docker compose down -v"
	fi
	if network_exists "$DESK_FOURCAT_NETWORK"; then
		echo "docker network rm $DESK_FOURCAT_NETWORK"
		docker network rm "$DESK_FOURCAT_NETWORK"
	else
		echo "network $DESK_FOURCAT_NETWORK absent; skipping"
	fi

	echo "== 5. remove /opt/ddp-comments, symlinks, hand nginx files =="
	echo "rm -rf --one-file-system $OPT_DIR"
	rm -rf --one-file-system "$OPT_DIR"
	for f in "${BIN_LINKS[@]}"; do
		echo "rm -f $f"
		rm -f "$f"
	done
	dir="$(nginx_backup_dir)"
	echo "backing up hand nginx files to $dir"
	(umask 077 && mkdir -p "$dir")
	for f in "${NGINX_FILES[@]}"; do
		if [[ -f $f ]]; then
			cp -p "$f" "$dir/"
		fi
	done
	chown -R dmccool:dmccool "$dir"
	echo "nginx file backup in $dir"
	for f in "${NGINX_FILES[@]}"; do
		echo "rm -f $f"
		rm -f "$f"
	done

	echo "== 6. validate and reload nginx =="
	if ! nginx -t; then
		echo "error: nginx -t failed; NOT reloading nginx. The four hand nginx files were already removed; their copies are in $dir" >&2
		exit 1
	fi
	echo "systemctl reload nginx"
	systemctl reload nginx

	echo "== 7. remove the old desk root =="
	echo "rm -rf --one-file-system $DESK_ROOT"
	rm -rf --one-file-system "$DESK_ROOT"

	echo "== 8. remove home-directory leftovers =="
	for f in "${HOME_LEFTOVERS[@]}"; do
		echo "rm -rf --one-file-system $f"
		rm -rf --one-file-system "$f"
	done

	echo "== 9. what remains =="
	docker ps -a
	docker network ls
	ls /etc/nginx/app-location-conf.d /etc/nginx/conf.d /usr/local/bin /opt /home/dmccool /data/onlinehate_zeeschuimer

	echo "DONE."
}

# --- Entry point -------------------------------------------------------------

if [[ $MODE == dry-run ]]; then
	dry_run
else
	run_yes
fi
