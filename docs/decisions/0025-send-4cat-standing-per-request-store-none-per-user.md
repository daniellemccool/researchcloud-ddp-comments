---
status: accepted
date: "2026-11-16"
category: Access
applies_to:
    - internal/web/fourcat.go
    - internal/web/fourcat_test.go
    - internal/fourcat/study.go
    - internal/fourcat/study_test.go
priority: invariant
---

# Send 4CAT standing per request; store none per user

## Decision

A person's standing in the bundled 4CAT comes only from the gate's roles on each request, sent by the desk's `/fourcat/` proxy as 4CAT's proxy-tag headers; the desk stores no per-user standing in 4CAT, and that proxy, signed in with the person's own API token, is the only way into 4CAT's web pages.

## Guidance

- Three standings: a collector sends no tag and never reaches `/fourcat/`; an admin sends `studylead`; a Tech Admin sends `techadmin`; both roles send `studylead,techadmin`. The proxy in `internal/web/fourcat.go` sets `X-4CAT-Config-Tag` and `X-4CAT-Config-Via-Proxy: <the desk's secret>` from that request's verified identity (`TestFourcatStandingHeaders`); review rejects any code that writes a tag to 4CAT.
- The sign-in is the person's own API token, read on every request from the token file only when the step file marks `fourcat_apitoken` done (`provision.APIToken`; `TestFourcatSignsInWithOwnToken`, `TestFourcatReadsTokenEachRequest`, `TestFourcatIgnoresOldCookieFile`); an admin or Tech Admin without one gets account and token on the first visit, without a seat (`Provisioner.AdvanceFourcat`).
- No cookies either way, and no browser credential or standing header: `Cookie`, `Authentication`, `Authorization`, both proxy-tag headers, any header spelled with an underscore and the `access-token` query parameter are removed, and 4CAT's `Set-Cookie` never reaches the browser (`TestFourcatStripsClientCredentials`, `TestFourcatDropsSetCookie`).
- Nothing from another website: 4CAT changes state on plain GETs and has no form protection, so a request passes only with `Sec-Fetch-Site` `same-origin` or `none`, or, without that header, an `Origin` or `Referer` whose host is exactly ours; under `/api/` `none` is refused too, since a browser sends it for a link opened from a desktop mail or chat program as well as for a typed address (seen on the sandbox, 2026-11-16); the one exception is a GET of the bare start page, the return from the SRC sign-in. A refusal shows the "open 4CAT from the desk" page (`TestFourcatRefusesCrossSite`).
- Closed until ready: once 4CAT's first-run setup is complete, `/fourcat/` answers the not-ready page until the study settings are asserted in this run (`TestFourcatClosedUntilReady`); while 4CAT itself answers that its setup is pending, the proxy is a plain pass-through with cookies and without sign-in or standing headers, and an unreachable 4CAT never opens it (`TestFourcatPendingSetupPassesThrough`).
- The settings the desk owns are re-asserted by SQL plus a memcached key delete at every start (`fourcat.OwnedSettings` in `internal/fourcat/study.go`): the proxy secret, the `studylead` and `techadmin` privileges, `can_restart` off for the global value and the `admin`, `studylead` and `techadmin` tags, upgrade off, collectors limited to creating datasets and tokens, no access requests (`TestOwnedSettingsTable`, `TestEnsureSettingsAlwaysClearsCacheKeys`). `datasources.enabled` and the phone-home address are set once (`OnceSettings`).
- The desk's only tag write is removing `admin`, once, from accounts it made (those with a stored password), with their cached tags (`Study.Ensure`, `TestStudyEnsure`); account creation never passes `-a`.
- 4CAT's logs page prints the request's headers, so on `GET /logs/` the proxy asks for an uncompressed answer and replaces the token and the secret with `(hidden)` (`TestFourcatHidesSecretsOnLogsPage`).

## Why

Standing stored in 4CAT lingers in its cache after a group change and needs a credential that can edit users; standing sent per request follows SRAM on the next click and leaves nothing behind. Because every request through the proxy carries the person's standing, a request a hostile page can make the browser send would act with that standing, which is why the cross-site rule and the credential stripping are part of the same rule.
