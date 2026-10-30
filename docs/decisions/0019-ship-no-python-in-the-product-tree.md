---
status: accepted
date: "2026-10-30"
source: docs/superpowers/specs/2026-10-29-box-first-desk-design.md
category: Deploy
priority: default
forbids:
    - cmd/**/*.py
    - internal/**/*.py
    - deploy/**/*.py
    - platforms/**/*.py
---
# Ship no Python in the product tree

## Decision

Nothing that runs on a workspace is Python: no `.py` file exists under the Go, deploy or platform trees, no image we build installs a Python interpreter for our code, and the init script is POSIX shell.

## Guidance

- `scripts/check.sh` fails on any `*.py` under `cmd/`, `internal/`, `deploy/` or `platforms/` and is the CI gate for this rule until `adg lean check` hard-fails a forbids match in CI; the legacy `desk/` and `tests/` Python trees are deleted when their Go replacement is verified on the box, and this record's forbids are widened then.
- The one Python dependency that existed, mitmproxy, was replaced by the Go capture proxy; a proposal to reintroduce Python must name the piece Python is actually right for.

## Why

The desk broke on the box over an interpreter version the desktop did not have; one static binary per box has no runtime to drift.
