---
status: accepted
date: "2026-10-30"
category: Access
applies_to:
    - internal/web/relay.go
    - internal/web/relay_test.go
priority: invariant
---
# Relay a screen only to its authenticated owner

## Decision

The desk relays a noVNC websocket only to the Firefox of the authenticated username, derived from the gate-checked identity and never from the request path or a parameter; an admin watching another screen uses a separate admin-gated route that re-checks the admin role on every connection.

## Guidance

- The collector relay in `internal/web/relay.go` takes the target from the identity the access package returned, so a request for another user's screen is impossible to express; `TestRelayTargetsIdentityNotPath` and `TestRelayConcurrentUsersNeverCross` attempt a cross-user screen. 2b: the admin watch route, which takes a target, checks the admin role first, and gets its own cross-user test; it does not exist in 2a.
- The relay target is `desk-firefox-<access.Identity.User>:5800`, built from the verified identity only; no path or query parameter reaches the dial; test `internal/web/relay_test.go` (`TestRelayTargetsIdentityNotPath`).
- A relayed socket is closed when its owner or viewer is revoked or demoted; request-time checks do not cover an already open socket.
- A desk restart drops open sockets; the pair keeps running and the page reconnects.

## Why

A screen is a collector's live browser with their session inside; one mistaken route exposes it to any member.
