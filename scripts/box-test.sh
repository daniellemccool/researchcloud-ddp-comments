#!/usr/bin/env bash
# Run the Go tests. Usage: scripts/box-test.sh [--box] [go test args...]
# Default: natively when go is on PATH, else in the golang container against
# the local tree. --box: in the container on the box workspace, through one
# ssh line (for a human caller).
set -uo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")/.." || exit 2

# Must match GO_TOOLCHAIN in scripts/check.sh.
GO_TOOLCHAIN="go1.26.3"
GO_IMAGE="golang:1.26"
BOX_DIR="/home/dmccool/ddp-comments"

if [ "${1:-}" = "--box" ]; then
	shift
	args=""
	for a in "$@"; do
		args="$args $(printf '%q' "$a")"
	done
	exec ssh -F "$HOME/.config/claude-ssh/config" desk \
		"cd $BOX_DIR && sudo docker run --rm -v $BOX_DIR:/src -w /src -v ddp-gomod:/go/pkg/mod $GO_IMAGE go test ./...$args"
fi

if command -v go >/dev/null 2>&1; then
	export GOTOOLCHAIN="$GO_TOOLCHAIN"
	exec go test ./... "$@"
fi

exec docker run --rm -v "$PWD:/src" -w /src -v ddp-gomod:/go/pkg/mod "$GO_IMAGE" go test ./... "$@"
