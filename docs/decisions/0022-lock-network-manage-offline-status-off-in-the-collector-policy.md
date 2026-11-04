---
status: accepted
date: "2026-11-04"
category: Browser
applies_to:
    - internal/render/policies.json.tmpl
    - internal/render/policy_test.go
priority: invariant
---

# Lock network.manage-offline-status off in the collector policy

## Decision

The collector Firefox policy locks `network.manage-offline-status` to false. The pair network has no default route by design, and with link tracking on Firefox then reports `navigator.onLine` as false, which TikTok's page treats as offline: it never requests the comments.

## Guidance

- `internal/render/policies.json.tmpl` keeps `"network.manage-offline-status": {"Value": false, "Status": "locked"}` under `Preferences`, and `internal/render/policy_test.go` asserts it in the rendered policy; review rejects removing either as unused.
- The pref changes only what Firefox reports as connectivity; it opens no route and is not isolation. A page that behaves as if offline in the pair is fixed in the policy, never by giving the Firefox a default route, a resolver or a second network.
- Manual check in a running collector Firefox: `navigator.onLine` in the console reads `true`.

## Why

Without it every TikTok video in the desk opens with an empty comment panel and nothing is captured, while the proxy, the tests and the page itself look healthy; the sandbox bisection on 2026-11-04 showed that the missing default route alone flips `navigator.onLine` and the comment request, with DNS, STUN and the proxy ruled out.
