---
status: accepted
date: "2026-10-30"
category: Deploy
priority: default
forbids:
    - "*.py"
    - cmd/**/*.py
    - internal/**/*.py
    - deploy/**/*.py
    - platforms/**/*.py
    - scripts/**/*.py
    - tests/**/*.py
    - docs/**/*.py
---
# Ship no Python outside archive/

## Decision

Nothing that runs on a workspace is Python: no `.py` file exists anywhere in the repository outside `archive/`, no image we build installs a Python interpreter for our code, and the init script is POSIX shell.

## Guidance

- `scripts/check.sh`'s shape layer is the enforcement: it fails on any tracked or untracked `*.py` outside `archive/` (ignored files such as `.venv/` excluded), and is the CI gate for this rule until `adg lean check` hard-fails a forbids match in CI; `archive/` holds only parked, unrun Playwright tests. The forbids list names every top-level tree except `archive/` because a glob cannot subtract it; add a new top-level tree here when one appears.
- The one Python dependency that existed, mitmproxy, was replaced by the Go capture proxy; a proposal to reintroduce Python must name the piece Python is actually right for.

## Why

The desk broke on the box over an interpreter version the desktop did not have; one static binary per box has no runtime to drift.
