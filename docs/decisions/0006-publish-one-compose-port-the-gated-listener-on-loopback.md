---
status: accepted
date: "2026-10-30"
category: Boundary
applies_to:
    - deploy/compose.yml
    - internal/render/**/*.go
    - internal/arch/compose_test.go
    - tests/box/ports_test.go
priority: invariant
---
# Publish one Compose port: the gated listener on loopback

## Decision

Across our compose file and every rendered per-user file, exactly one port is published, the desk's gated listener on `127.0.0.1`; 4CAT, the proxies and the browsers publish nothing. The rule is about Compose: SRC-Nginx legitimately listens on 443.

## Guidance

- `internal/arch/compose_test.go` runs `docker compose config` on `deploy/compose.yml` and on rendered files and fails on any `ports:` entry other than the gated listener, any `0.0.0.0` binding, any `network_mode: host`, and any missing restart policy; `tests/box/ports_test.go` inspects the actual bindings of running containers on the box.
- 4CAT's frontend is reachable only on the `desk-fourcat` network; the desk proxies it for admins.
- Review rejects a published port added "for debugging"; use `docker compose exec` or an ssh tunnel to a loopback port.

## Why

The sandbox's hand-run 4CAT listened on all interfaces at 8080 and 446 with only the SRC firewall in front; a published port is a second door that the SRAM gate never sees.
