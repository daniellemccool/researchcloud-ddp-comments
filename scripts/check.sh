#!/usr/bin/env bash
# Repository checks. Usage: scripts/check.sh [layer...]
# Layers: adr shell go compose shape. No argument runs all of them.
# A missing tool is a failure, never a skip. Only the go and compose layers
# skip, and only while go.mod or deploy/compose.yml does not exist yet.
set -uo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")/.." || exit 2

# Scripts of the legacy Python tree; plan 3 deletes them. Each file is named
# explicitly (no glob), and nothing under deploy/ or any new script may ever
# join this list.
LEGACY_SHELL=(scripts/e2e.sh scripts/lib.sh scripts/test-all.sh)

GOLANGCI_LINT_VERSION="v2.12.2"
GO_IMAGE="golang:1.26"
# Toolchain the native go layer pins; its major.minor must match GO_IMAGE.
GO_TOOLCHAIN="go1.26.3"
ALL_LAYERS=(adr shell go compose shape)

need() {
	if ! command -v "$1" >/dev/null 2>&1; then
		echo "$1 missing: install it" >&2
		return 1
	fi
}

layer_adr() {
	need adg || return 1
	adg lean index --model docs/decisions --root . &&
		adg lean check --model docs/decisions --root .
}

layer_shell() {
	need shellcheck || return 1
	need shfmt || return 1
	need jq || return 1
	local files=() f
	for f in scripts/*.sh deploy/*.sh tests/sh/*.sh; do
		[ -f "$f" ] || continue
		case " ${LEGACY_SHELL[*]} " in
		*" $f "*) continue ;;
		esac
		files+=("$f")
	done
	if [ "${#files[@]}" -eq 0 ]; then
		echo "no shell scripts found" >&2
		return 1
	fi
	shellcheck "${files[@]}" && shfmt -d "${files[@]}" && tests/sh/init_test.sh
}

# Run the go steps in the current environment.
go_steps() {
	local unformatted gofmt_bin
	gofmt_bin="$(go env GOROOT)/bin/gofmt"
	unformatted="$(go list -f '{{.Dir}}' ./... | xargs -r "$gofmt_bin" -l)"
	if [ -n "$unformatted" ]; then
		echo "gofmt: files need formatting:" >&2
		echo "$unformatted" >&2
		return 1
	fi
	go vet ./... && golangci-lint run && go test ./...
}

layer_go() {
	if [ ! -f go.mod ]; then
		echo "skip: go.mod does not exist yet"
		return 0
	fi
	if command -v go >/dev/null 2>&1; then
		need golangci-lint || return 1
		export GOTOOLCHAIN="$GO_TOOLCHAIN"
		go_steps
		return
	fi
	need docker || return 1
	docker run --rm -v "$PWD:/src" -w /src -v ddp-gomod:/go/pkg/mod "$GO_IMAGE" bash -c '
		set -euo pipefail
		curl -sSfL https://raw.githubusercontent.com/golangci/golangci-lint/v2.12.2/install.sh |
			sh -s -- -b /usr/local/bin '"$GOLANGCI_LINT_VERSION"'
		unformatted="$(go list -f "{{.Dir}}" ./... | xargs -r gofmt -l)"
		if [ -n "$unformatted" ]; then
			echo "gofmt: files need formatting:" >&2
			echo "$unformatted" >&2
			exit 1
		fi
		go vet ./...
		golangci-lint run
		go test ./...
	'
}

layer_compose() {
	if [ ! -f deploy/compose.yml ]; then
		echo "skip: deploy/compose.yml does not exist yet"
		return 0
	fi
	need docker || return 1
	trap 'rm -f deploy/.resolved.yml' EXIT
	trap 'exit 130' INT TERM
	local rc=0
	if docker compose -f deploy/compose.yml --env-file deploy/.env.example config >deploy/.resolved.yml; then
		if command -v go >/dev/null 2>&1; then
			GOTOOLCHAIN="$GO_TOOLCHAIN" go test -run TestCompose ./internal/arch/ || rc=1
		else
			docker run --rm -v "$PWD:/src" -w /src -v ddp-gomod:/go/pkg/mod "$GO_IMAGE" \
				go test -run TestCompose ./internal/arch/ || rc=1
		fi
	else
		rc=1
	fi
	rm -f deploy/.resolved.yml
	trap - EXIT INT TERM
	return "$rc"
}

# Mirrors ADR-0018 and ADR-0019 for environments without adg.
layer_shape() {
	local rc=0 dirs=() d py hits
	for d in cmd internal deploy platforms; do
		[ -d "$d" ] && dirs+=("$d")
	done
	if [ "${#dirs[@]}" -gt 0 ]; then
		py="$(find "${dirs[@]}" -name '*.py' -type f)"
		if [ -n "$py" ]; then
			echo "python in the product tree (ADR-0019):" >&2
			echo "$py" >&2
			rc=1
		fi
	fi
	dirs=()
	for d in cmd/desk internal; do
		[ -d "$d" ] && dirs+=("$d")
	done
	if [ "${#dirs[@]}" -gt 0 ]; then
		hits="$(grep -rEn --include='*.go' --exclude='*_test.go' --exclude-dir=arch \
			'nginx|systemctl|/etc/nginx' "${dirs[@]}" || true)"
		if [ -n "$hits" ]; then
			echo "nginx or systemctl in the running desk (ADR-0018):" >&2
			echo "$hits" >&2
			rc=1
		fi
	fi
	return "$rc"
}

main() {
	local layers=("$@") layer failed=() passed=0
	if [ "${#layers[@]}" -eq 0 ]; then
		layers=("${ALL_LAYERS[@]}")
	fi
	for layer in "${layers[@]}"; do
		case " ${ALL_LAYERS[*]} " in
		*" $layer "*) ;;
		*)
			echo "unknown layer: $layer (layers: ${ALL_LAYERS[*]})" >&2
			exit 2
			;;
		esac
	done
	for layer in "${layers[@]}"; do
		echo "== $layer"
		if "layer_$layer"; then
			passed=$((passed + 1))
		else
			failed+=("$layer")
		fi
	done
	if [ "${#failed[@]}" -gt 0 ]; then
		echo "check: FAILED (${failed[*]}); $passed passed"
		exit 1
	fi
	echo "check: ok ($passed layers passed)"
}

main "$@"
