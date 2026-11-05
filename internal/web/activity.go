package web

import (
	"context"
	"sync"
	"time"
)

// Activity is the per-user activity tracker the idle stop reads: the last
// page-reported input, the screens (relayed websocket upgrades) open now and
// when the last one closed. It also holds each open screen's cancel, so
// CloseUser can end a user's screens on demand.
//
// No path leaves a zero clock: every write (Input, a screen opening or
// closing) and Idle first create a missing entry with every clock at now, so
// a user the tracker has not seen, or has forgotten, always starts with the
// full grace.
type Activity struct {
	now func() time.Time

	mu    sync.Mutex
	users map[string]*userActivity
}

// userActivity is one user's clocks and open screens.
type userActivity struct {
	lastInput       time.Time
	lastScreenClose time.Time
	screens         map[*screen]struct{}
}

// screen is one open relayed websocket; its cancel ends the proxied request.
type screen struct {
	cancel context.CancelFunc
}

// NewActivity returns an empty tracker reading time from now.
func NewActivity(now func() time.Time) *Activity {
	return &Activity{now: now, users: map[string]*userActivity{}}
}

// entry returns user's state, creating it with every clock at t when it is
// missing. The caller holds a.mu.
func (a *Activity) entry(user string, t time.Time) *userActivity {
	u, ok := a.users[user]
	if !ok {
		u = &userActivity{lastInput: t, lastScreenClose: t, screens: map[*screen]struct{}{}}
		a.users[user] = u
	}
	return u
}

// Input records input from user's page now.
func (a *Activity) Input(user string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	t := a.now()
	a.entry(user, t).lastInput = t
}

// Idle reports whether user has been idle for longer than idle, and why:
// "no input" when the last input is older than idle, or "no screen" when no
// screen is open and the last one closed longer than idle ago. A user not
// seen before starts with every clock at now and is not idle.
func (a *Activity) Idle(user string, idle time.Duration) (bool, string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	t := a.now()
	u := a.entry(user, t)
	if t.Sub(u.lastInput) > idle {
		return true, "no input"
	}
	if len(u.screens) == 0 && t.Sub(u.lastScreenClose) > idle {
		return true, "no screen"
	}
	return false, ""
}

// Forget drops user's clocks, so a pair started after a stop gets the full
// grace again. A screen still registered (its proxy has not returned yet)
// stays registered, so CloseUser can still reach it; the clocks restart at
// now in that case instead of being dropped.
func (a *Activity) Forget(user string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	u, ok := a.users[user]
	if !ok {
		return
	}
	if len(u.screens) == 0 {
		delete(a.users, user)
		return
	}
	t := a.now()
	u.lastInput, u.lastScreenClose = t, t
}

// CloseUser cancels every open screen of user and returns how many it
// cancelled. Each cancelled screen leaves the open set at once; its relay
// request records the close time when the proxy returns.
func (a *Activity) CloseUser(user string) int {
	a.mu.Lock()
	defer a.mu.Unlock()
	u, ok := a.users[user]
	if !ok {
		return 0
	}
	n := len(u.screens)
	for s := range u.screens {
		s.cancel()
		delete(u.screens, s)
	}
	return n
}

// openScreen registers an open screen of user with its cancel and returns
// the function that marks it closed: it deregisters the screen (if CloseUser
// has not already) and records the close time.
func (a *Activity) openScreen(user string, cancel context.CancelFunc) (closed func()) {
	s := &screen{cancel: cancel}
	a.mu.Lock()
	a.entry(user, a.now()).screens[s] = struct{}{}
	a.mu.Unlock()
	return func() {
		a.mu.Lock()
		defer a.mu.Unlock()
		t := a.now()
		u := a.entry(user, t)
		delete(u.screens, s)
		u.lastScreenClose = t
	}
}
