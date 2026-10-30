#!/usr/bin/env bash
# Run the Go tests. Usage: scripts/box-test.sh [--box] [go test args...]
# Default: natively when go is on PATH, else in the golang container against
# the local tree. --box: in the container on the box workspace, through one
# ssh line (for a human caller). All of ./... runs unless an argument names
# a package (starts with ./), in which case only the named packages run.
set -uo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")/.." || exit 2

# Must match GO_TOOLCHAIN in scripts/check.sh.
GO_TOOLCHAIN="go1.26.3"
GO_IMAGE="golang:1.26"
BOX_DIR="/home/dmccool/ddp-comments"

box=0
if [ "${1:-}" = "--box" ]; then
	box=1
	shift
fi

default_pkgs=("./...")
for a in "$@"; do
	case "$a" in
	./*) default_pkgs=() ;;
	esac
done

if [ "$box" -eq 1 ]; then
	args=""
	for a in "${default_pkgs[@]}" "$@"; do
		args="$args $(printf '%q' "$a")"
	done
	exec ssh -F "$HOME/.config/claude-ssh/config" desk \
		"cd $BOX_DIR && sudo docker run --rm -v $BOX_DIR:/src -w /src -v ddp-gomod:/go/pkg/mod $GO_IMAGE go test$args"
fi

if command -v go >/dev/null 2>&1; then
	export GOTOOLCHAIN="$GO_TOOLCHAIN"
	exec go test "${default_pkgs[@]}" "$@"
fi

exec docker run --rm -v "$PWD:/src" -w /src -v ddp-gomod:/go/pkg/mod "$GO_IMAGE" go test "${default_pkgs[@]}" "$@"
