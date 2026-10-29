#!/usr/bin/env bash
# Local: copy the working tree to the sandbox box and run install.sh there as root.
#
#   scripts/sandbox/push.sh [--ssh-config PATH]
#
# Without --ssh-config, `ssh desk` / `scp ... desk:` use your own ssh config. With it, every
# ssh and scp call gets `-F PATH`. The tree is staged without .git, .venv, __pycache__ and
# the other local-only directories, so nothing but the code reaches the box.
set -euo pipefail

SSH=(ssh)
SCP=(scp)
while [ $# -gt 0 ]; do
  case "$1" in
    --ssh-config)
      [ $# -ge 2 ] || { echo "push.sh: --ssh-config needs a path" >&2; exit 2; }
      SSH=(ssh -F "$2"); SCP=(scp -F "$2"); shift 2 ;;
    *) echo "usage: push.sh [--ssh-config PATH]" >&2; exit 2 ;;
  esac
done

REPO="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
REMOTE=/tmp/ddp-comments-push
STAGE="$(mktemp -d)"
trap 'rm -rf "$STAGE"' EXIT

echo "== staging $REPO"
tar -C "$REPO" -cf - \
  --exclude=.git --exclude=.venv --exclude='__pycache__' --exclude='*.pyc' \
  --exclude=.claude --exclude=.superpowers --exclude=dist --exclude=state . | tar -C "$STAGE" -xf -

echo "== copying to desk:$REMOTE/"
"${SSH[@]}" desk "rm -rf $REMOTE && mkdir -p $REMOTE"
"${SCP[@]}" -r "$STAGE"/. "desk:$REMOTE/"

echo "== installing"
"${SSH[@]}" desk "sudo bash $REMOTE/scripts/sandbox/install.sh"
