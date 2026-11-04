---
status: accepted
date: "2026-11-02"
category: Deploy
applies_to:
    - deploy/lib.sh
    - deploy/.env.example
    - platforms/*/template.json
    - .github/workflows/release.yml
    - scripts/check-pins.sh
priority: invariant
---

# Take every image and template pin from the checked-out release, never from the catalog item

## Decision

The init script resolves the four image references (desk, deskproxy, 4cat, firefox) from the release's own `deploy/.env.example` (the desk tag is the release tag) and the Zeeschuimer version and artifact digest from the release's `platforms/<platform>/template.json`; the catalog component declares only study parameters and the three platform-sourced ones, and a parameter carrying a pin is refused.

## Guidance

- The release workflow fails when the desk or deskproxy tag in `.env.example` differs from the tag being released.
- `update` adopts the NEW copy's pins and refuses a release whose manifest differs from `desk.json`'s recorded template until template installation at update exists.
- The digest guards the download against substitution because the manifest and the script that checks it come from the same git ref, which is the trust root.

## Why

A portal copy of a value the repository already pins adds one edit per release and a way to diverge, not trust (Danielle, 2026-11-02).
