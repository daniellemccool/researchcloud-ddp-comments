---
status: accepted
date: "2026-10-30"
category: Provisioning
applies_to:
    - internal/provision/steps.go
    - internal/provision/steps_test.go
priority: default
---
# Provision idempotently from the per-user step file

## Decision

First visit is an idempotent operation that advances `state/users/<user>/provision.json` step by step toward ready, resuming from the recorded steps and never from the mere existence of a container, account or file; a secret is stored before the call that creates what it protects.

## Guidance

- Each step (registry, dirs, template, fourcat_account, fourcat_token, render; no `up` or `create`, start is the page's request) is recorded with its outcome and last error in `internal/provision/steps.go`; a retry re-runs only unfinished steps; `internal/provision/steps_test.go` is the rule's enforcement.
- `applied_generation` is the template generation `<platform>-<zeeschuimer_version>`.
- The 4CAT password is written to `secrets/4cat-passwords/<user>` before `create_user`; an existing account with no stored password stops with "ask an admin to adopt it" and never resets it.
- The whole run holds the user's lock; the global lock is held only for registry allocation and cap reservation.

## Why

A crash or a reattachment that re-created accounts or reset passwords would destroy a collector's 4CAT identity and datasets.
