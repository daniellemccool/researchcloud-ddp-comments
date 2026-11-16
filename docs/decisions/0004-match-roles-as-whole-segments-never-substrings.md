---
status: accepted
date: "2026-10-30"
category: Access
applies_to:
    - internal/access/**/*.go
    - internal/access/roles_test.go
    - internal/web/server.go
priority: invariant
---
# Match roles as whole segments, never substrings

## Decision

The roles header is split on `;` and a role matches only when a whole segment equals the configured name. There are three standings: a collector holds the collector role, an admin the admin role, a Tech Admin the Tech Admin role; admins are collectors too, and a Tech Admin is neither unless they also hold those roles.

## Guidance

- Compare segments with equality in `internal/access`; `internal/access/roles_test.go` holds the cases, including a longer group name that extends ours, and `internal/access/gate_test.go` the standings (`TestTechAdminOnly`, `TestSameRoleForTwoStandings`, `TestAdminIsCollector`).
- Role names come from `desk.json`, validated at load against `^[A-Za-z0-9_-]+$` (any prefix; the same rule as init's). `desk_techadmin_role` is optional: a missing key means `src_co_developer`, and init does not require any member to hold it.
- A member with none of the three standings gets the 403 page naming the contact, never a collector page.
- A Tech Admin who is not a collector reaches `/fourcat/` and nothing under `/desk/`: the gate in `internal/web/server.go` refuses every other path, because the admin, screen and claim handlers do not check the collector role themselves (`TestTechAdminOnlyReachesOnlyFourcat`). `/fourcat/` admits admins and Tech Admins.
- One SRAM group may be configured for two standings; the person then has both.

## Why

A substring match lets any SRAM group whose name extends ours grant access, and a standing that implied another would hand a Tech Admin the collector pages or an admin 4CAT's users and settings.
