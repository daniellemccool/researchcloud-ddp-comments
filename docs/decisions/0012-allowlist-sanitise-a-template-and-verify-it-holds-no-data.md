---
status: accepted
date: "2026-10-30"
category: Browser
applies_to:
    - internal/template/**/*.go
    - internal/template/release_test.go
priority: invariant
---
# Allowlist-sanitise a template and verify it holds no data

## Decision

The template release command keeps an explicit allowlist of profile files and rows, deletes everything else, and then verifies the extension's store is found with items 0, uploads 0 and navigation records 0 and settings at least 1, refusing to publish on any other count or when the store is absent.

## Guidance

- Verification runs after sanitising and reads the real IndexedDB SQLite counts; "no database found" is a failure, never a pass; `internal/template/release_test.go` holds the fixture profiles.
- The allowlist is the one in `internal/template` (filtered `prefs.js`, `extensions.json`, the extension's xpi and storage, cookies named in `keep_cookies`, the stylesheet); adding to it needs a reason in the record of the change.
- History, form data, logins, key stores, session restore, caches and crash data are always removed.

## Why

Artifacts are public release files; one captured item or cookie in the template leaks data to every workspace that uses it.
