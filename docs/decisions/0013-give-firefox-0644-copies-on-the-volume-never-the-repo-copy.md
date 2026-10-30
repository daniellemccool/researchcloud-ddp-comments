---
status: accepted
date: "2026-10-30"
source: docs/superpowers/specs/2026-10-29-box-first-desk-design.md
category: Browser
applies_to:
    - internal/render/policy.go
    - internal/render/modes_test.go
priority: invariant
---
# Give Firefox 0644 copies on the volume, never the repo copy

## Decision

Every file the Firefox container reads, policies, stylesheet, CA, is a copy written on the volume with mode 0644 and uid 1000 ownership; nothing under the platform's root-only repository copy is bind-mounted into a browser.

## Guidance

- `internal/render/policy.go` sets the mode and owner explicitly on every write; `internal/render/modes_test.go` is the rule's enforcement; the init script does the same for the files it places before the desk exists.
- Review rejects a bind mount whose source is under `/rsc/plugins/`.
- A rendered policy is re-read by Firefox only at start, so a changed policy means a pair restart, which the desk performs.

## Why

The repository copy is root-only, and Firefox silently ignores an unreadable policy and runs without the proxy lock, the CA or the extension.
