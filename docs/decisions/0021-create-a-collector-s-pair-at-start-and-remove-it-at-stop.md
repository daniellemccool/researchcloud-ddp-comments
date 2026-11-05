---
status: accepted
date: "2026-11-03"
category: Provisioning
applies_to:
    - internal/session/**/*.go
    - internal/docker/**/*.go
    - internal/render/**/*.go
    - internal/arch/pair_test.go
priority: invariant
---

# Create a collector's pair at start and remove it at stop

## Decision

A collector's network and two containers exist only while the session runs: `start` creates `net-<user>`, `desk-proxy-<user>` and `desk-firefox-<user>` from the rendered spec through the Docker API and `stop` removes all three. Everything persistent (profile, CA, captures, token, step file) lives on the volume, so no container is ever reused across sessions and no configuration drift can exist.

## Guidance

- Every pair object carries `desk.owner=desk`, `desk.user`, `desk.role` and `desk.generation`, never a `com.docker.compose.*` label, and the desk refuses to touch an object without its owner label or with another user's.
- A leftover of the same name that is not running is removed before start; a running one is adopted, except where the memory-cap rule narrows this: `Reconcile` stops a running pair whose containers lack the spec's memory limits ("limits changed") instead of adopting it.
- `desk serve` reconciles by listing the owner label: running pairs are adopted and the desk re-joins their networks, exited pairs are stopped, a running pair whose registry row is retired is stopped (finishing a revoke; the CO poll does the same through `RevokeAbsent`), and owner-labelled objects of a user with no registry row at all are logged and left.
- The check is `internal/arch/pair_test.go` over `render.Spec` plus `internal/session` tests over the fake client's recorded call sequence.

## Why

Docker accepts a fixed address only on a self-chosen subnet and its default pools hold about 31 networks, so persistent pairs would cap registration near 28; ephemeral pairs keep networks at most equal to running sessions and remove the recreate-on-drift problem (Danielle, 2026-11-03, design P8, after the research in D§4.1).
