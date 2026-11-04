package locks

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestUserReturnsSamePointer(t *testing.T) {
	var s Set
	first := s.User("x")
	second := s.User("x")
	if first != second {
		t.Error(`User("x") returned different pointers on two calls`)
	}
	if first == s.User("y") {
		t.Error(`User("x") and User("y") share one mutex`)
	}
}

// TestLockOrder is the desk's lock order (per-user lock, then the global
// lock, never the reverse): two goroutines taking User(a) then Global must
// never deadlock, over many iterations.
func TestLockOrder(t *testing.T) {
	var s Set
	var wg sync.WaitGroup
	var inside atomic.Int64
	for i := 0; i < 1000; i++ {
		wg.Add(2)
		for j := 0; j < 2; j++ {
			go func() {
				defer wg.Done()
				u := s.User("a")
				u.Lock()
				s.Global.Lock()
				inside.Add(1) // the critical section both locks protect
				s.Global.Unlock()
				u.Unlock()
			}()
		}
	}
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(60 * time.Second):
		t.Fatal("deadlock: user -> global lock order did not complete in 60s")
	}
	if got := inside.Load(); got != 2000 {
		t.Errorf("critical sections executed = %d, want 2000", got)
	}
}
