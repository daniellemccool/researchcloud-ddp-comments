---
status: accepted
date: "2026-11-05"
category: Provisioning
applies_to:
    - internal/web/**/activity*.go
    - internal/web/relay.go
    - internal/reaper/**/*.go
    - internal/web/page.go
priority: default
---

# Stop a pair after idle input or no open screen

## Decision

`desk serve` runs a reaper every minute that stops a collector's pair once the page has reported no input, or no screen has been open, for longer than `desk_idle_seconds`, re-checking under the user's lock before it stops; `desk_idle_seconds = 0` turns the reaper off.

## Guidance

- Idleness comes only from the activity tracker (`internal/web/activity.go`): input is the page's heartbeat (`POST /desk/active`, sent at most once a minute after a pointer, key, wheel or touch event in the page or the screen iframe), and a screen is a relayed websocket upgrade; review rejects reading input from the VNC stream and counting plain relay requests as screens.
- The reaper (`internal/reaper`) selects idle users from `Running`, then stops each through `session.Manager.StopIf`, whose condition asks the tracker again under the user's lock, so a page load or input that lands between selection and stop keeps the pair (`TestPassRecheckUnderLock`); a stop appends `stop` with code `idle_no_input` or `idle_no_screen` and `idle_s` to the desk log, and a `Running` error ends the pass with nothing stopped.
- Every user gets the full grace: the tracker creates a missing user with all clocks at now, so clocks restart at serve start (`TestReaperGraceAfterRestart`), and `serveHome` calls `Forget` whenever `Start` returns Started, so a page load that starts or finds the pair restarts them; the reaper calls `Forget` after each stop.
- `Forget` keeps a screen that is still open and only resets its clocks, so a later `CloseUser` (revocation) can still reach it; never drop the open-screen set in `Forget`.
- A screen is closed by cancelling the context of its relay request (registered by `openScreen`); `httputil.ReverseProxy` then closes the upgraded backend connection. Do not move the close into the relay's dial seam: `http.Transport` dials with `context.WithoutCancel`, so a dialled connection never sees the request's cancel (a deviation from the design's "wrap the dialled conn").
- `internal/reaper` sees the tracker, the pairs and the desk log only through its small interfaces; it imports neither `internal/web` nor the Docker SDK, and `desk serve` starts it with a context cancelled on shutdown, only when `desk_idle_seconds > 0`.

## Why

Seats are scarce (the running cap), and a collector who walks away or closes the tab would otherwise hold one until an operator noticed. Websocket traffic is not input: noVNC keeps requesting frames with nobody at the keyboard, and telling input apart would mean parsing the VNC protocol in a relay that forwards bytes opaquely, so the page reports input itself.
