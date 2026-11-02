#!/usr/bin/env bash
# Pin guard for a release tag. Usage: scripts/check-pins.sh <tag>
# Exits 0 when deploy/.env.example pins DESK_IMAGE and DESKPROXY_IMAGE to
# <tag>, FOURCAT_IMAGE carries a tag, and every platforms/*/template.json is
# a well-formed manifest; otherwise one line per failure on stderr, exit 1.
# No argument: usage, exit 2. CHECK_PINS_ROOT overrides the repository root
# (default: the checkout this script lives in); the tests use it.
set -euo pipefail

if [ -n "${CHECK_PINS_ROOT:-}" ]; then
	cd "$CHECK_PINS_ROOT"
else
	cd "$(dirname "${BASH_SOURCE[0]}")/.."
fi

if [ "$#" -ne 1 ]; then
	echo "usage: scripts/check-pins.sh <tag>" >&2
	exit 2
fi

tag=$1
failed=0

fail() {
	echo "check-pins: $*" >&2
	failed=1
}

env_example=deploy/.env.example

# env_value reads KEY the way deploy/lib.sh does: the FIRST matching line.
env_value() {
	sed -n "s/^$1=//p" "$env_example" | head -n 1
}

# env_count counts the KEY= lines; a duplicate is a failure of its own.
env_count() {
	grep -c "^$1=" "$env_example" || true
}

for key in DESK_IMAGE DESKPROXY_IMAGE FOURCAT_IMAGE; do
	count=$(env_count "$key")
	if [ "$count" -eq 0 ]; then
		fail "$key missing in $env_example"
		continue
	fi
	if [ "$count" -gt 1 ]; then
		fail "$key appears more than once in $env_example"
	fi
	value=$(env_value "$key")
	# Same regex as deploy/lib.sh; printf '%s' adds no newline, so a trailing
	# CR cannot hide behind line matching.
	if ! printf '%s' "$value" | grep -Eqx '^[A-Za-z0-9][A-Za-z0-9./_-]*:[A-Za-z0-9._-]+$'; then
		fail "$key must be a registry path with a tag"
		continue
	fi
	case $key in
	DESK_IMAGE | DESKPROXY_IMAGE)
		if [ "${value##*:}" != "$tag" ]; then
			fail "$key tag ${value##*:} differs from $tag"
		fi
		;;
	esac
done

shopt -s nullglob
manifests=(platforms/*/template.json)
if [ "${#manifests[@]}" -eq 0 ]; then
	fail "no platform manifest found"
fi

for m in "${manifests[@]}"; do
	p=${m#platforms/}
	p=${p%/template.json}
	pfx="platforms/$p/template.json"
	if ! jq -se 'length == 1 and (.[0] | type == "object")' "$m" >/dev/null 2>&1; then
		fail "$pfx: must hold exactly one JSON object"
		continue
	fi
	keys=$(jq -r 'keys | sort | join(",")' "$m")
	if [ "$keys" != "artifact,built_on,template_sha256,zeeschuimer_version" ]; then
		fail "$pfx: keys must be exactly artifact,built_on,template_sha256,zeeschuimer_version (got $keys)"
		continue
	fi
	zsv=$(jq -r '.zeeschuimer_version | if type == "string" then . else "" end' "$m")
	if [[ ! "$zsv" =~ ^[0-9]+(\.[0-9]+)*$ ]]; then
		fail "$pfx: zeeschuimer_version must match ^[0-9]+(\.[0-9]+)*$"
	fi
	sha=$(jq -r '.template_sha256 | if type == "string" then . else "" end' "$m")
	if [[ ! "$sha" =~ ^[0-9a-f]{64}$ ]]; then
		fail "$pfx: template_sha256 must match ^[0-9a-f]{64}$"
	fi
	built=$(jq -r '.built_on | if type == "string" then . else "" end' "$m")
	if [[ ! "$built" =~ ^[0-9]{4}-[0-9]{2}-[0-9]{2}$ ]]; then
		fail "$pfx: built_on must match ^[0-9]{4}-[0-9]{2}-[0-9]{2}$"
	fi
	artifact=$(jq -r '.artifact | if type == "string" then . else "" end' "$m")
	expected="ddp-comments-template-$p-$zsv.tar"
	if [ "$artifact" != "$expected" ]; then
		fail "$pfx: artifact must be $expected (got $artifact)"
	fi
done

if [ "$failed" -ne 0 ]; then
	exit 1
fi
