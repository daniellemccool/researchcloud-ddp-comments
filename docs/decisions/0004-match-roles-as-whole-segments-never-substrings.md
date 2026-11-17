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

The roles header is split on `;` and a role matches only when a whole segment equals the configured name. There are three standings, each given by its own role: a Tech Admin is also an admin and a collector, and an admin is also a collector and is not a Tech Admin.

## Guidance

- Compare segments with equality in `internal/access`; `internal/access/roles_test.go` holds the cases, including a longer group name that extends ours, and `internal/access/gate_test.go` the standings (`TestTechAdminIsAdmin`, `TestSameRoleForTwoStandings`, `TestAdminIsCollector`).
- The implications between the standings are decided in `access.FromRequest` and nowhere else; every other package reads `Identity.Collector`, `Admin` and `TechAdmin` as given and never derives one from another or from the roles.
- Role names come from `desk.json`, validated at load against `^[A-Za-z0-9_-]+$` (any prefix; the same rule as init's). `desk_techadmin_role` is optional: a missing key means `src_co_developer`, and init does not require any member to hold it.
- A member with none of the three standings gets the 403 page naming the contact, never a collector page.
- The gate in `internal/web/server.go` admits every collector and has no case per path for a Tech Admin; the admin pages and `/fourcat/` admit admins, which covers Tech Admins (`TestTechAdminReachesAdminAndDesk`). Only the `techadmin` tag sent to 4CAT asks for the Tech Admin standing itself.
- One SRAM group may be configured for two standings; the person then has both.

## Why

A substring match lets any SRAM group whose name extends ours grant access. A Tech Admin holds ssh on the workspace, so keeping the desk's pages from them protects nothing, while an admin who counted as a Tech Admin would get 4CAT's users and settings.
