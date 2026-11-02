---
status: accepted
date: "2026-10-30"
category: Provisioning
applies_to:
    - deploy/init.sh
    - internal/provision/reattach.go
    - internal/provision/reattach_test.go
priority: default
---
# Reattach by preserving data and regenerating workspace state

## Decision

When the init script finds `desk.json` on the volume it switches to reattach mode: 4CAT data, the Postgres password, accounts, registry and captures are preserved, and everything bound to the old workspace is regenerated, the CO token, FQDN, gate secret, networks, rendered files and runtime state.

## Guidance

- Reattach creates no 4CAT account and resets no password; per-user steps that reference containers or networks are re-run from `provision.json` by `internal/provision/reattach.go`; `internal/provision/reattach_test.go` is the rule's enforcement.
- The FQDN flows into `.env`, 4CAT's `SERVER_NAME` and rendered policies from the new workspace, never from the stored `desk.json`.
- A layout version in `desk.json` gates migrations; an unknown higher version stops with a message instead of guessing.

## Why

The volume is what survives a workspace, so a relaunch on the same volume must bring the study back intact without carrying the dead workspace's secrets and names.
