---
status: accepted
date: "2026-10-30"
source: docs/superpowers/specs/2026-10-29-box-first-desk-design.md
category: Boundary
applies_to:
    - deploy/compose.yml
    - internal/render/**/*.go
    - tests/box/isolation_test.go
priority: invariant
---
# Route a Firefox only through its own proxy

## Decision

Each collector network `net-<user>` is a Docker `internal` network with no gateway; the Firefox is on that network only, the proxy also joins `desk-egress` and `desk-fourcat`, so the browser's sole route to anything is its proxy regardless of its preferences.

## Guidance

- `tests/box/isolation_test.go` proves from inside a Firefox container that direct connections to the internet, the host, and the bridge gateway address all fail while the proxied path works; if the gateway address is reachable on an `internal` network, create the network with Docker's isolated gateway mode (`com.docker.network.bridge.gateway_mode_ipv4=isolated`; the sandbox runs Docker 29.1, which has it, and a launch installs current docker-ce).
- The locked proxy preference and the installed CA stay in the policy for the browser's benefit, but they are not the isolation and review does not accept them as such.
- The desk joins `net-<user>` only for the screen relay and serves nothing identity-bearing on that network.

## Why

Firefox silently ignores an unreadable policy file and runs unproxied; network isolation is the only guarantee that every collector request passes the capture proxy and the 4CAT boundary.
