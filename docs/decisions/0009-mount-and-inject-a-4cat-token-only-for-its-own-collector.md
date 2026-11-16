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

Each collector's 4CAT credential is their own 4CAT API token in `secrets/4cat-tokens/<user>/4cat-token`, whose directory is bind-mounted read-only only into that collector's proxy at `/token`; the proxy sends it as the `Authentication` header after removing every credential and standing header the browser sent. No other container, page or log ever sees a token.

## Guidance

- The proxy removes `Cookie`, `Authentication`, `Authorization`, `X-4CAT-Config-Via-Proxy`, `X-4CAT-Config-Tag` and the `access-token` query parameter from every allowed request before it adds the token (`clientCredentials` in `cmd/deskproxy/boundary.go`).
- The token directory is uid 1000 0750 and the file uid 1000 0600; the directory, not the file, is mounted, so a rotation by rename reaches the running proxy.
- Rendering a pair mounts exactly one token directory, the owner's (`internal/render/mounts.go`, enforced by `internal/render/mounts_test.go`); the proxy reads the file on every allowed request so rotation needs no restart (`cmd/deskproxy/token_test.go`), and `TestTokenHeaderReplacesClientCredentials` in `cmd/deskproxy/boundary_test.go` holds the stripping, including `access-token` dropped pair by pair so the poll's `key` reaches 4CAT unchanged.
- The proxy never forwards a client credential to 4CAT, never sends a standing header, never passes 4CAT's `Set-Cookie` to the browser, never logs the token, and answers 403 when the token file is missing rather than forwarding unauthenticated.
- The token is requested through 4CAT's own route (`/api/request-token/`, with a login cookie that lives only for that call and is never stored) in the provisioning step `fourcat_apitoken`, and its expiry is set to 0 ("never") in 4CAT's `access_tokens` table right after; anything but a 64-character hex token is an error and writes no file.
- The token file is rewritten only under the user lock in `internal/provision/token.go`.

## Why

The mount is the isolation boundary: a token reachable by another container lets one collector upload, read or delete under another's 4CAT identity.
