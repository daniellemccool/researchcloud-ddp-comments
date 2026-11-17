---
status: accepted
date: "2026-10-30"
category: Access
applies_to:
    - internal/coapi/**/*.go
    - internal/coapi/poll_test.go
    - internal/session/revoke.go
    - internal/provision/steps.go
    - internal/web/home.go
    - cmd/desk/main.go
priority: default
---
# Revoke only on a successful poll that omits the user

## Decision

The desk polls the CO member API every minute and revokes a user only when a successful poll returns a member list without that user; a failed poll, and a successful poll with no members at all, changes nothing and shows as an alarm on the admin overview and in `desk status`.

## Guidance

- Transport errors, non-200 responses, unparsable bodies and an empty list all mean "no change" in `internal/coapi`; `internal/coapi/poll_test.go` holds each case, and only a good list reaches `session.Manager.RevokeAbsent`. An empty list is refused twice: `Poller.Poll` counts a fetch with no members as a failure even without an error, and `RevokeAbsent` selects no live row when the present set is empty.
- `Manager.Revoke` works under the user's lock in a fixed order: retire the registry row (under Global), mark the `registry` provisioning step undone, close the user's relayed screens, stop the pair, and close the screens again whatever the stop returned. Retire and the step file come before any Docker call; a failed Retire leaves a live row that the next poll selects again, and a crash or Docker error after Retire leaves a retired row that the next poll (a retired row with a running container, or with `registry` still done) or `Reconcile` finishes; never reorder them after the stop.
- With 4CAT enabled (`Manager.DropToken` set), after the stop the person's API token is deleted in 4CAT's `access_tokens`; only once that succeeded is the token file removed and the `fourcat_apitoken` step marked undone (`TestRevokeDeletesTokenAfterStop`, `TestRevokeKeepsTokenStepWhenDeleteFails`). A retired row whose token step is still done is the third sign of an unfinished revoke; it is selected only by a good, non-empty list that lacks the user (`TestRevokeAbsentFinishesTokenDelete`), so a re-added admin who opened 4CAT before `/desk/` keeps their fresh token. Coming back runs the token step again and gets a new token.
- Accepted limit (decision 2026-11-16): revocation looks through the collector seats, so an admin or Tech Admin who only ever opened 4CAT has a token but no seat and is not found; their token cannot be presented, because SRC's gate refuses a non-member and both proxies replace whatever credential a browser sends.
- Reactivation needs the poll's agreement: the registry provisioning step, the screen relay and the home page all refuse a user the last good list lacks, so a queued provisioning run or a noVNC reconnect cannot undo a revoke; a re-added member waits for the next good poll.
- Membership only: a member present without a desk role is not revoked. By decision (2026-11-11) role loss is left to the gate, which refuses every new request, and to the idle stop for a screen already open; nothing is retired and coming back needs nothing. Standing in 4CAT is sent per request from the gate's roles, so nothing stored follows roles. Profile, captures, password, the 4CAT account and its datasets survive a revoke; the API token does not.
- The poller keeps each member's roles from the last good poll (`Poller.Members`) for display on the admin page; nothing is revoked or stopped because of them, and a member whose roles the API did not give as a list is unknown, not without a role (`TestMembersKeepsRoles`).
- The last good list is kept while the API is down; the status line reads `CO poll: last good HH:MM, N failures` and is prefixed `ALARM:` after 5 consecutive failures or while no poll has succeeded since start.
- The 2026-11-05 probe found the endpoint returns one unpaginated list whose usernames equal the `REMOTE_USER` the gate sends; a paginated or renamed response needs a new probe before revocation relies on it.

## Why

An API outage must not evict every collector mid-session; an authoritative empty list cannot be right because the CO always holds the workspace's admin; and SRC-Nginx's `/validate` is the first revocation layer, so a missed poll delays revocation rather than defeating it.
