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
- Reactivation needs the poll's agreement: the registry provisioning step, the screen relay and the home page all refuse a user the last good list lacks, so a queued provisioning run or a noVNC reconnect cannot undo a revoke; a re-added member waits for the next good poll.
- Membership only: a member present without a desk role is not revoked. By decision (2026-11-11) role loss is left to the gate, which refuses every new request, and to the idle stop for a screen already open; nothing is retired and coming back needs nothing. The 4CAT admin tag follows roles in the 4CAT-fit step; credential rotation on demotion was dropped because 4CAT's login cookie cannot be revoked that way. Profile, captures, token, password and the 4CAT account survive a revoke.
- The last good list is kept while the API is down; the status line reads `CO poll: last good HH:MM, N failures` and is prefixed `ALARM:` after 5 consecutive failures or while no poll has succeeded since start.
- The 2026-11-05 probe found the endpoint returns one unpaginated list whose usernames equal the `REMOTE_USER` the gate sends; a paginated or renamed response needs a new probe before revocation relies on it.

## Why

An API outage must not evict every collector mid-session; an authoritative empty list cannot be right because the CO always holds the workspace's admin; and SRC-Nginx's `/validate` is the first revocation layer, so a missed poll delays revocation rather than defeating it.
