---
status: accepted
date: "2026-10-30"
source: docs/superpowers/specs/2026-10-29-box-first-desk-design.md
category: Boundary
applies_to:
    - cmd/deskproxy/**/*.go
    - cmd/deskproxy/boundary_test.go
priority: invariant
---
# Forward only two 4CAT API routes; 403 the rest

## Decision

The capture proxy forwards a request to the 4CAT frontend only when its host is `fourcat.desk` and it is exactly `POST /api/import-dataset/` or `GET /api/check-query/`; every other request to `fourcat.desk`, and every request whose host is `localhost`, an IP literal, a dotless name, or another `*.desk` name, is answered 403 by the proxy itself.

## Guidance

- The allowlist is an exact method-and-path table in `cmd/deskproxy`; `cmd/deskproxy/boundary_test.go` is the rule's enforcement, so adding a route means adding a row there with a reason.
- After resolving any other host, refuse a destination in a loopback, private, link-local or Docker address range, except the one 4CAT forward, and connect to the address that was checked, not to the name again.
- A forwarded request goes to `http://4cat-frontend:5000` with `Host: fourcat.desk`; 4CAT's redirects and error bodies are returned to the browser unmodified and never followed.

## Why

The browser's only way out is its proxy, so the proxy is where the 4CAT boundary is real; one extra route, or a name that resolves inward, gives a collector the whole 4CAT UI or the host under another identity.
