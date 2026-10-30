---
status: accepted
date: "2026-10-30"
source: docs/superpowers/specs/2026-10-29-box-first-desk-design.md
category: Capture
applies_to:
    - cmd/deskproxy/**/*.go
    - cmd/deskproxy/capture_test.go
priority: default
---
# Store the raw upstream object in every safety-copy record

## Decision

A safety-copy record always contains the upstream object exactly as decoded, the whole body for a body rule or each element of the named array for an items rule, inside the stable envelope; a rule's `fields` map adds named columns beside it and never replaces it, and a body over the rule's `max_body_bytes` produces an error record with no body.

## Guidance

- Decode gzip, deflate and brotli before storing and keep the response to the browser byte-identical; `cmd/deskproxy/capture_test.go` is the rule's enforcement.
- The envelope is `t, user, session, platform, capture, kind, post_id, request{method,host,path,query_keep}, response{status,...}` plus `item` or `body`; `query_keep` lists the only query parameters stored, never headers or tokens.
- Reprocessing the safety copy must need neither the browser nor 4CAT; a change that makes a stored record depend on either is rejected.

## Why

The safety copy is the only record when an upload never happened, 4CAT is down, or a token has died; a parsed-only copy would repeat the failure it insures against.
