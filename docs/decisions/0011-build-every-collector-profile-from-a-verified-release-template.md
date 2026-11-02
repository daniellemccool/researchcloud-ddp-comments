---
status: accepted
date: "2026-10-30"
category: Browser
applies_to:
    - internal/template/**/*.go
    - internal/provision/template.go
    - internal/provision/template_test.go
priority: default
---
# Build every collector profile from a verified release template

## Decision

A collector's Firefox profile is created only by unpacking the release template artifact for the workspace's platform and Zeeschuimer version, after its sha256 matched the digest pinned in the catalog item's `template_sha256` parameter; no profile is prepared by hand on a workspace for a logged-out platform.

## Guidance

- Provisioning copies `templates/release/<platform>-<version>/` into `state/firefox/<user>/` and records the template's generation in `provision.json`; a missing or mismatched artifact stops provisioning with a message naming the artifact, never a silent fallback to an empty profile (`internal/provision/template_test.go`).
- The trust root is the catalog item, not the release: the init script compares the downloaded artifact against the digest the component parameter carries, so a substituted release file cannot pass; a digest shipped beside the artifact would guard against corruption only.
- Login-required platforms get their own workspace-template mechanism; it must not reuse the release path or its verification.

## Why

A hand-prepared profile is an unrecorded hand step that a fresh launch cannot reproduce, and an unverified one may carry data or a broken extension into every session.
