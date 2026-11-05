package web

import (
	"sync"
	"testing"
	"time"
)

// fakeClock is a settable clock for the activity tracker.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{t: time.Date(2026, 11, 5, 9, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// activitySnapshot reads a user's tracked state under the tracker's lock:
// whether an entry exists, how many screens are open and the last close.
func activitySnapshot(a *Activity, user string) (ok bool, open int, lastClose time.Time) {
	a.mu.Lock()
	defer a.mu.Unlock()
	u, ok := a.users[user]
	if !ok {
		return false, 0, time.Time{}
	}
	return true, len(u.screens), u.lastScreenClose
}

const testIdle = 15 * time.Minute

func assertIdle(t *testing.T, a *Activity, user string, wantIdle bool, wantReason string) {
	t.Helper()
	idle, reason := a.Idle(user, testIdle)
	if idle != wantIdle || reason != wantReason {
		t.Errorf("Idle(%q) = (%v, %q), want (%v, %q)", user, idle, reason, wantIdle, wantReason)
	}
}

func TestIdleGraceForUnknownUser(t *testing.T) {
	c := newFakeClock()
	a := NewActivity(c.now)
	// First sight: every clock starts at now, so the user is not idle.
	assertIdle(t, a, "alice", false, "")
	// Exactly idle is not yet past idle.
	c.advance(testIdle)
	assertIdle(t, a, "alice", false, "")
	c.advance(time.Second)
	assertIdle(t, a, "alice", true, "no input")
}

func TestInputResetsIdle(t *testing.T) {
	c := newFakeClock()
	a := NewActivity(c.now)
	// A screen is open throughout, so only the input clock can make her idle.
	done := a.openScreen("alice", func() {})
	defer done()
	c.advance(testIdle - time.Second)
	a.Input("alice")
	c.advance(2 * time.Second)
	assertIdle(t, a, "alice", false, "")
	c.advance(testIdle)
	assertIdle(t, a, "alice", true, "no input")
}

func TestNoScreenIdle(t *testing.T) {
	c := newFakeClock()
	a := NewActivity(c.now)
	a.Input("alice")
	c.advance(testIdle + time.Second)
	a.Input("alice")
	assertIdle(t, a, "alice", true, "no screen")
}

func TestOpenScreenNotNoScreenIdle(t *testing.T) {
	c := newFakeClock()
	a := NewActivity(c.now)
	done := a.openScreen("alice", func() {})
	c.advance(testIdle + time.Second)
	a.Input("alice")
	assertIdle(t, a, "alice", false, "")

	// Closing the screen starts the no-screen clock at the close.
	done()
	c.advance(testIdle)
	a.Input("alice")
	assertIdle(t, a, "alice", false, "")
	c.advance(time.Second)
	a.Input("alice")
	assertIdle(t, a, "alice", true, "no screen")
}

func TestScreenCloseAfterForgetKeepsGrace(t *testing.T) {
	// A screen that closes after Forget (the proxy returns after the reaper
	// stopped the pair) must not leave a zero input clock behind: the next
	// pair gets the full grace.
	t.Run("screen still registered at Forget", func(t *testing.T) {
		c := newFakeClock()
		a := NewActivity(c.now)
		done := a.openScreen("alice", func() {})
		c.advance(2 * testIdle)
		a.Forget("alice")
		done()
		c.advance(testIdle - time.Second)
		assertIdle(t, a, "alice", false, "")
	})
	t.Run("screen already closed by CloseUser", func(t *testing.T) {
		c := newFakeClock()
		a := NewActivity(c.now)
		done := a.openScreen("alice", func() {})
		c.advance(2 * testIdle)
		if n := a.CloseUser("alice"); n != 1 {
			t.Fatalf("CloseUser = %d, want 1", n)
		}
		a.Forget("alice")
		if ok, _, _ := activitySnapshot(a, "alice"); ok {
			t.Fatal("Forget left an entry for a user with no open screen")
		}
		done()
		c.advance(testIdle - time.Second)
		assertIdle(t, a, "alice", false, "")
	})
}

func TestCloseUserCancelsOnlyThatUsersScreens(t *testing.T) {
	a := NewActivity(newFakeClock().now)
	var mu sync.Mutex
	cancelled := map[string]int{}
	cancel := func(name string) func() {
		return func() {
			mu.Lock()
			cancelled[name]++
			mu.Unlock()
		}
	}
	doneA1 := a.openScreen("alice", cancel("alice-1"))
	doneA2 := a.openScreen("alice", cancel("alice-2"))
	doneB := a.openScreen("bob", cancel("bob"))
	defer doneA1()
	defer doneA2()
	defer doneB()

	if n := a.CloseUser("alice"); n != 2 {
		t.Errorf("CloseUser(alice) = %d, want 2", n)
	}
	// A second CloseUser finds nothing left to close.
	if n := a.CloseUser("alice"); n != 0 {
		t.Errorf("second CloseUser(alice) = %d, want 0", n)
	}
	if n := a.CloseUser("nobody"); n != 0 {
		t.Errorf("CloseUser(nobody) = %d, want 0", n)
	}
	mu.Lock()
	defer mu.Unlock()
	if cancelled["alice-1"] != 1 || cancelled["alice-2"] != 1 {
		t.Errorf("alice's cancels = %v, want each called once", cancelled)
	}
	if cancelled["bob"] != 0 {
		t.Errorf("bob's cancel called %d times, want 0", cancelled["bob"])
	}
	if _, open, _ := activitySnapshot(a, "bob"); open != 1 {
		t.Errorf("bob's open screens = %d, want 1", open)
	}
}
