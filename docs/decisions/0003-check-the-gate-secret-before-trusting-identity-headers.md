---
status: accepted
date: "2026-10-30"
source: docs/superpowers/specs/2026-10-29-box-first-desk-design.md
category: Access
applies_to:
    - internal/access/**/*.go
    - internal/access/gate_test.go
    - internal/arch/identity_test.go
    - deploy/nginx/**/*.conf
priority: invariant
---
# Check the gate secret before trusting identity headers

## Decision

A request on the gated listener is trusted only if it carries `X-Desk-Gate` equal to the secret the init script generated into `secrets/gate` and wrote into both nginx location files; identity headers are read in `internal/access` and nowhere else, and the container listener reads no identity at all.

## Guidance

- Check the gate secret first, then validate the username against `^[a-z][a-z0-9._-]{0,31}$`, and only then use the name in any path, container name or decision; `internal/access/gate_test.go` sends forged headers through both listeners.
- Identity is read through one function in `internal/access`; `internal/arch/identity_test.go` is the AST rule that rejects a `Header.Get` of `REMOTE_USER`, `X-Desk-Roles` or `X-Desk-Gate` anywhere else.
- Nginx sets those three headers with `proxy_set_header`, which overwrites anything the client sent.

## Why

The desk sits on every collector's Docker network, so a browser whose policy failed could reach it directly; without the secret, forged headers would impersonate any collector or admin.
