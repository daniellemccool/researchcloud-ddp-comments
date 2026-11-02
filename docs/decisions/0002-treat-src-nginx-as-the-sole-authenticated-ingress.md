---
status: accepted
date: "2026-10-30"
category: Access
applies_to:
    - deploy/nginx/**/*.conf
    - internal/access/**/*.go
priority: default
---
# Treat SRC-Nginx as the sole authenticated ingress

## Decision

The desk has no login of its own: SRC-Nginx's SRAM gate is the only authenticated way in.

## Guidance

- Every gated location lives in the two static files under `deploy/nginx/` and carries `auth_request /validate` with the username and roles forwarded as headers; the desk never issues a session cookie, password form or token of its own for people.
- Membership and revocation are the desk's own decisions in `internal/access` and `internal/coapi`, made from the forwarded identity and the polled CO member list, never delegated to the `rsc_nginx_co_role` parameter, which can hold only one role.
- Review rejects any route that authenticates a person by other means, including a 4CAT login reached from a collector's browser.

## Why

One ingress means one place where identity is established; a second login path is a second place to get it wrong and a way around the platform's own revocation.
