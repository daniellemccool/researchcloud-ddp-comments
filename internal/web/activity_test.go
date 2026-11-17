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

// TestActivityLastInput: a user the tracker does not hold has no last input,
// and asking creates no entry, so a following Idle still starts them with
// the full grace; after Input the answer is that input's time.
func TestActivityLastInput(t *testing.T) {
	c := newFakeClock()
	a := NewActivity(c.now)
	if got, ok := a.LastInput("alice"); ok {
		t.Errorf("LastInput(alice) on an empty tracker = (%v, true), want false", got)
	}
	if ok, _, _ := activitySnapshot(a, "alice"); ok {
		t.Fatal("LastInput created an entry for a user the tracker did not hold")
	}
	// Had the read created an entry, alice would now be idle: her clocks
	// would date from before this advance.
	c.advance(testIdle + time.Minute)
	assertIdle(t, a, "alice", false, "")

	a.Input("bob") // 09:16 UTC on the fake clock
	c.advance(3 * time.Minute)
	want := time.Date(2026, 11, 5, 9, 16, 0, 0, time.UTC)
	if got, ok := a.LastInput("bob"); !ok || !got.Equal(want) {
		t.Errorf("LastInput(bob) = (%v, %v), want (%v, true)", got, ok, want)
	}
}

// TestLastInputOnlyFromInput: only a real Input sets the last input. An
// entry that Idle or an opening screen created answers false, and a Forget
// that resets the clocks of a user with a screen open does not move it: it
// still answers the time of the last Input, not the time of the Forget.
func TestLastInputOnlyFromInput(t *testing.T) {
	c := newFakeClock() // 09:00 UTC
	a := NewActivity(c.now)
	assertIdle(t, a, "alice", false, "") // Idle creates alice's entry
	if ok, _, _ := activitySnapshot(a, "alice"); !ok {
		t.Fatal("Idle created no entry for alice")
	}
	if got, ok := a.LastInput("alice"); ok {
		t.Errorf("LastInput(alice) after Idle alone = (%v, true), want false", got)
	}
	closeCarol := a.openScreen("carol", func() {})
	defer closeCarol()
	if got, ok := a.LastInput("carol"); ok {
		t.Errorf("LastInput(carol) after a screen opened = (%v, true), want false", got)
	}

	c.advance(2 * time.Minute)
	a.Input("alice") // 09:02 UTC
	want := time.Date(2026, 11, 5, 9, 2, 0, 0, time.UTC)
	if got, ok := a.LastInput("alice"); !ok || !got.Equal(want) {
		t.Errorf("LastInput(alice) after Input = (%v, %v), want (%v, true)", got, ok, want)
	}

	closeAlice := a.openScreen("alice", func() {})
	defer closeAlice()
	c.advance(5 * time.Minute)
	a.Forget("alice") // 09:07 UTC; her screen is open, so her clocks restart
	if got, ok := a.LastInput("alice"); !ok || !got.Equal(want) {
		t.Errorf("LastInput(alice) after Forget with a screen open = (%v, %v), want (%v, true): the time of the last Input, not of the Forget", got, ok, want)
	}
}
