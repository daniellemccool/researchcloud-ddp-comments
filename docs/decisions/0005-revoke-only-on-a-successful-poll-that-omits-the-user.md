---
status: accepted
date: "2026-10-30"
source: docs/superpowers/specs/2026-10-29-box-first-desk-design.md
category: Access
applies_to:
    - internal/coapi/**/*.go
    - internal/coapi/poll_test.go
priority: default
---
# Revoke only on a successful poll that omits the user

## Decision

The desk polls the CO member API every minute and revokes a user only when a successful poll returns a member list without that user or without their role; a failed poll, and a successful poll with no members at all, changes nothing and raises an alarm on the admin page.

## Guidance

- Transport errors, non-200 responses, unparsable bodies and an empty list all mean "no change" in `internal/coapi`; `internal/coapi/poll_test.go` holds each case.
- Revocation stops the user's pair, closes every websocket relayed for or to them, retires the registry row, and for a demoted admin clears the 4CAT admin tag and rotates their credentials.
- The last good list is kept and served while the API is down, with the outage visible to admins.

## Why

An API outage must not evict every collector mid-session; an authoritative empty list cannot be right because the CO always holds the workspace's admin; and SRC-Nginx's `/validate` is the first revocation layer, so a missed poll delays revocation rather than defeating it.
