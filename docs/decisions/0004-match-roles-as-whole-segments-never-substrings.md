---
status: accepted
date: "2026-10-30"
category: Access
applies_to:
    - internal/access/**/*.go
    - internal/access/roles_test.go
priority: invariant
---
# Match roles as whole segments, never substrings

## Decision

The roles header is split on `;` and a role matches only when a whole segment equals the configured name; a collector is anyone with the collector role, an admin anyone with the admin role, and admins are collectors too.

## Guidance

- Compare segments with equality in `internal/access`; `internal/access/roles_test.go` holds the cases, including a longer group name that extends ours.
- Role names come from `desk.json`, validated at load against `^src_co_[A-Za-z0-9_-]+$`.
- A member with neither role gets the 403 page naming the contact, never a collector page.

## Why

A substring match lets any SRAM group whose name extends ours grant access.
