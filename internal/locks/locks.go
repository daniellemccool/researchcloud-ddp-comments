// Package locks holds the desk's in-process locks: one mutex per user plus a
// single global mutex. Exactly one process mutates desk state, so no file
// locks are needed. The order is always per-user lock first, then the global
// lock, never the reverse; a run that needs both (provisioning's registry
// step) takes the user lock for the whole run and the global lock inside it.
package locks

import "sync"

// Set is the desk's lock set. The zero value is ready to use.
type Set struct {
	Global sync.Mutex
	mu     sync.Mutex
	users  map[string]*sync.Mutex
}

// User returns the mutex serialising one user's operations, creating it on
// first use; the same pointer is returned every time.
func (s *Set) User(name string) *sync.Mutex {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.users == nil {
		s.users = make(map[string]*sync.Mutex)
	}
	m, ok := s.users[name]
	if !ok {
		m = &sync.Mutex{}
		s.users[name] = m
	}
	return m
}
