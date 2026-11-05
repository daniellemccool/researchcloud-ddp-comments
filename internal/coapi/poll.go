package coapi

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// alarmFailures is the consecutive-failure count at which the status line alarms.
const alarmFailures = 5

// Poller keeps the last good member list, its time and the failure count.
type Poller struct {
	Fetch func(context.Context) ([]Member, error)
	Now   func() time.Time

	mu       sync.Mutex
	present  map[string]bool
	goodAt   time.Time
	hasGood  bool
	failures int
}

// Poll fetches once. On success it stores the list and time and zeroes the
// failures; on failure it counts one and keeps the last list.
func (p *Poller) Poll(ctx context.Context) (map[string]bool, bool) {
	members, err := p.Fetch(ctx)
	p.mu.Lock()
	defer p.mu.Unlock()
	if err != nil {
		p.failures++
		return nil, false
	}
	present := make(map[string]bool, len(members))
	for _, m := range members {
		present[m.Username] = true
	}
	p.present, p.goodAt, p.hasGood, p.failures = present, p.Now(), true, 0
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
