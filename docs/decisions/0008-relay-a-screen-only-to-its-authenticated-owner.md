---
status: accepted
date: "2026-10-30"
category: Access
applies_to:
    - internal/relay/**/*.go
    - internal/relay/relay_test.go
    - internal/web/relay.go
    - internal/web/relay_test.go
priority: invariant
---
# Relay a screen only to its authenticated owner

## Decision

The desk relays a noVNC websocket only to the Firefox of the authenticated username, derived from the gate-checked identity and never from the request path or a parameter; an admin watching another screen uses a separate admin-gated route that re-checks the admin role on every connection.

## Guidance

- `internal/relay` takes the target from the identity the access package returned, so a request for another user's screen is impossible to express; the admin route takes a target and checks the admin role first; `internal/relay/relay_test.go` attempts a cross-user screen on both routes.
- The relay target is `desk-firefox-<access.Identity.User>:5800`, built from the verified identity only; no path or query parameter reaches the dial; test `internal/web/relay_test.go` (`TestRelayTargetsIdentityNotPath`).
- A relayed socket is closed when its owner or viewer is revoked or demoted; request-time checks do not cover an already open socket.
- A desk restart drops open sockets; the pair keeps running and the page reconnects.

## Why

A screen is a collector's live browser with their session inside; one mistaken route exposes it to any member.
