package coapi

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// alarmFailures is the consecutive-failure count at which the status line alarms.
const alarmFailures = 5

// Poller keeps the last good member list with each member's roles, its time
// and the failure count.
type Poller struct {
	Fetch func(context.Context) ([]Member, error)
	Now   func() time.Time

	mu       sync.Mutex
	present  map[string]bool
	roles    map[string][]string // each member's roles in the last good list; nil: unknown
	goodAt   time.Time
	hasGood  bool
	failures int
}

// Poll fetches once. On success it stores the list, the roles and the time
// and zeroes the failures; on failure it counts one and keeps the last list.
// A Fetch that returns no members without an error is a failure too
// (ADR-0005: an empty list changes nothing).
func (p *Poller) Poll(ctx context.Context) (map[string]bool, bool) {
	members, err := p.Fetch(ctx)
	p.mu.Lock()
	defer p.mu.Unlock()
	if err != nil || len(members) == 0 {
		p.failures++
		return nil, false
	}
	present := make(map[string]bool, len(members))
	roles := make(map[string][]string, len(members))
	for _, m := range members {
		present[m.Username] = true
		roles[m.Username] = copyRoles(m.Roles)
	}
	p.present, p.roles, p.goodAt, p.hasGood, p.failures = present, roles, p.Now(), true, 0
	out := make(map[string]bool, len(present))
	for k := range present {
		out[k] = true
	}
	return out, true
}

// Absent is true only when a good list exists and lacks user.
func (p *Poller) Absent(user string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.hasGood && !p.present[user]
}

// Failures is the number of consecutive failed polls.
func (p *Poller) Failures() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.failures
}

// StatusLine is the admin-page line, prefixed ALARM after 5 consecutive
// failures or while there is no good poll yet.
func (p *Poller) StatusLine() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.hasGood {
		return fmt.Sprintf("ALARM: CO poll: no good poll yet, %d failures", p.failures)
	}
	s := fmt.Sprintf("CO poll: last good %s, %d failures", p.goodAt.Format("15:04"), p.failures)
	if p.failures >= alarmFailures {
		s = "ALARM: " + s
	}
	return s
}

// Members is a copy of each member's roles from the last good poll, for
// display on the admin page; false while no poll has succeeded. A member
// whose roles the API did not give as a list of strings maps to a nil
// slice: unknown, which is not the same as holding no role. Nothing is
// revoked or stopped because of these roles; revocation looks at membership
// alone (Absent).
func (p *Poller) Members() (map[string][]string, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.hasGood {
		return nil, false
	}
	out := make(map[string][]string, len(p.roles))
	for user, roles := range p.roles {
		out[user] = copyRoles(roles)
	}
	return out, true
}

// copyRoles copies a role list, keeping nil as nil and empty as empty.
func copyRoles(roles []string) []string {
	if roles == nil {
		return nil
	}
	return append([]string{}, roles...)
}
