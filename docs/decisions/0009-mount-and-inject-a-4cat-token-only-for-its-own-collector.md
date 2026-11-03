---
status: accepted
date: "2026-10-30"
category: Boundary
applies_to:
    - internal/render/mounts.go
    - internal/render/mounts_test.go
    - cmd/deskproxy/**/*.go
    - cmd/deskproxy/token_test.go
    - internal/provision/token.go
priority: invariant
---
# Mount and inject a 4CAT token only for its own collector

## Decision

Each collector's 4CAT token lives in `secrets/4cat-tokens/<user>/4cat-token` (directory uid 1000 0750, file uid 1000 0600) and the **directory** is bind-mounted read-only only into that collector's proxy at `/token`, so a rotation by rename reaches the running proxy; the proxy replaces any client-supplied `Cookie` on an allowed 4CAT request with `remember_token=<that token>`. No other container, page or log ever sees a token.

## Guidance

- Rendering a pair mounts exactly one token directory, the owner's (`internal/render/mounts.go`, enforced by `internal/render/mounts_test.go`); the proxy reads the file on every allowed request so rotation needs no restart (`cmd/deskproxy/token_test.go`).
- The proxy never forwards a client cookie to 4CAT, never logs the token, and answers 403 when the token file is missing rather than forwarding unauthenticated.
- Rotation and demotion rewrite the file under the user lock in `internal/provision/token.go`; the admin page's "rotate token" is the same code path as the operator command.

## Why

The mount is the isolation boundary: a token reachable by another container lets one collector upload, read or delete under another's 4CAT identity.
