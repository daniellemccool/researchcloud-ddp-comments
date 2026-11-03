---
status: accepted
date: "2026-10-30"
category: Provisioning
applies_to:
    - internal/provision/cap.go
    - internal/provision/cap_test.go
priority: default
---
# Reserve the running-session cap atomically

## Decision

A pair is started only after its slot under `desk_max_running` was reserved under the global lock, in the same critical section that counts running pairs; the per-user lock never stands in for this.

## Guidance

- `internal/provision/cap.go` reserves, then starts, then releases on failure, all against the registry's running set; `internal/provision/cap_test.go` is the rule's enforcement.
- The running count is taken from Docker by label (`desk.role=firefox`, state running) under the global lock, immediately before the start; the per-user lock is held around it.
- "Busy" (runtime cap) and "full" (registry cap) are distinct pages with distinct messages.
- The reaper's idle stop releases the slot; a crashed container is detected and released by reconcile, not left reserved forever.

## Why

Two first visits in the same second must not both pass a non-atomic count and overload the box.
