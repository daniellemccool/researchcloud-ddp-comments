#!/usr/bin/env bash
# Mirror this checkout with the box workspace. Run from the repo root.
#   box-sync.sh push   local -> box (deletes files on the box that are gone locally)
#   box-sync.sh pull   box -> local (never deletes)
set -euo pipefail

BOX_DIR="desk:/home/dmccool/ddp-comments/"
SSH_CMD="ssh -F $HOME/.config/claude-ssh/config"

case "${1:-}" in
push)
	rsync -az --delete --exclude .git --exclude .superpowers --exclude dist --exclude .cache \
		-e "$SSH_CMD" ./ "$BOX_DIR"
	;;
pull)
	rsync -az --exclude .git --exclude .superpowers --exclude dist --exclude .cache \
		-e "$SSH_CMD" "$BOX_DIR" ./
	;;
*)
	echo "usage: $0 push|pull" >&2
	exit 2
	;;
esac
