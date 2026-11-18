---
status: accepted
date: "2026-11-18"
category: Pool
applies_to:
    - internal/desklog/claims.go
    - internal/pool/**/*.go
    - internal/platform/**/draw*.go
    - platforms/*/draw.json
    - cmd/poolexport/**/*.go
priority: invariant
---

# Draw each claim at random, weighted by age, never by donor

## Decision

A claim draws its videos at claim time, at random without replacement, each weighted `1 / max(age, floor)` by the item's posting time. The pool, the claims and the upload files never hold anything about who donated a video.

## Guidance

- `desklog.Log.Claim` in `internal/desklog/claims.go` draws n distinct unclaimed rows weighted `w = 1 / max(age, floor)`, `age = now − posted` in seconds, comparing keys in log space (`ln(u) * age`, largest wins) so they stay finite; it never takes rows in pool-file order, and nothing it stores or logs names, groups or orders by participant.
- `posted` comes from the item kind's time rule: for TikTok's `video` kind, rule `high32`, `posted = id >> 32` in Unix seconds. A kind without a rule, an ID that does not parse, or `posted > now` gets age = floor.
- The floor is the upload cadence + 1 week: 14 days (1209600 s) for weekly uploads. It and the time rule live in `platforms/<p>/draw.json` (`{"floor_days": 14, "posted_from_id": {"video": "high32"}}`), read by `LoadDraw` in `internal/platform/draw.go`; a missing file draws uniformly at random, still never in file order.
- `draw.json` is desk-only: never put draw settings in `platform.json`, because the capture proxy loads `platform.json` strictly and an older proxy would refuse the new fields.
- `ErrBusy`, `ErrEmpty`, batch numbering and claim events stay as they were; within a batch the claim events keep the draw order.
- `cmd/poolexport` writes bare IDs only: CSV header `id`, rows shuffled with `crypto/rand`, at most 200000 rows per file. Its sent list holds bare IDs sorted ascending, written atomically only after every output file is closed. No participant identifier, export file name or row content goes into any output, report, log, test or fixture.
- Enforced by `TestDrawAgeHigh32`, `TestDrawAgeFallbacks`, `TestLoadDraw*`, `TestTiktokDrawFile`, `TestPlatformJSONUnchangedByDraw` (`internal/platform`); `TestClaimNotFileOrder`, `TestClaimWeightRatio`, `TestClaimUniformUnderFloor`, `TestClaimKeysFinite`, `TestClaimNeverRepeatsOrRetakes`, `TestClaimConcurrentDistinct` (`internal/desklog`); `TestShuffledNotInputOrder`, `TestWriteSentSorted`, `TestWriteUploadsSplits`, `TestSentListUntouchedOnWriteFailure`, `TestRunSecondRunOnlyNew` (`cmd/poolexport`).

## Why

Few people donate at a time, so a pool claimed in file order would hand a collector one person's history in (reverse) viewing order; the server is meant to hold bare video IDs only, and the floor keeps one upload's newest videos equally likely instead of favouring whoever donated last.
