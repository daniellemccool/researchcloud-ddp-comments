// Package reaper is the desk's idle stop: a loop in desk serve that stops a
// collector's pair once the collector has been idle for desk_idle_seconds,
// either without input reported by the page or without an open screen.
//
// The package sees the activity tracker, the pairs and the desk log only
// through the small interfaces on Reaper; it imports neither internal/web
// nor the Docker SDK.
package reaper

import (
	"context"
	"log"
	"sort"
	"time"

	"github.com/daniellemccool/researchcloud-ddp-comments/internal/desklog"
)

// Reaper stops idle pairs. Idle is desk_idle_seconds as a duration; Clock is
// the activity tracker; Pairs lists running pairs and stops one under its
// user lock; Log is the desk log the stop events go to; Logf writes the
// operator log line (nil means log.Printf).
type Reaper struct {
	Idle  time.Duration
	Clock interface {
		Idle(string, time.Duration) (bool, string)
		Forget(string)
	}
	Pairs interface {
		Running(context.Context) (map[string]bool, error)
		StopIf(context.Context, string, func() bool) (bool, error)
	}
	Log interface {
		Append(...desklog.Event) error
	}
	Logf func(string, ...any)
}

// codes maps the tracker's idle reason to the stop event's code.
var codes = map[string]string{
	"no input":  "idle_no_input",
	"no screen": "idle_no_screen",
}

// Pass runs one sweep: it lists the running pairs and, for each user the
// clock reports idle, stops the pair through StopIf with a condition that
// asks the clock again under the user lock. A stopped user's clocks are
// forgotten and a stop event is appended to the desk log. A Running error is
// logged and ends the pass; a stop error is logged and leaves no event.
func (r *Reaper) Pass(ctx context.Context) {
	running, err := r.Pairs.Running(ctx)
	if err != nil {
		r.logf("reaper: list running pairs: %v", err)
		return
	}
	users := make([]string, 0, len(running))
	for u, ok := range running {
		if ok {
			users = append(users, u)
		}
	}
	sort.Strings(users)
	for _, u := range users {
		if ctx.Err() != nil {
			return
		}
		if idle, _ := r.Clock.Idle(u, r.Idle); !idle {
			continue
		}
		var reason string
		stopped, err := r.Pairs.StopIf(ctx, u, func() bool {
			var idle bool
			idle, reason = r.Clock.Idle(u, r.Idle)
			return idle
		})
		if !stopped {
			continue
		}
		if err != nil {
			r.logf("reaper: stop %s (%s): %v", u, reason, err)
			continue
		}
		r.Clock.Forget(u)
		ev := desklog.Event{
			T:      time.Now().Unix(),
			User:   u,
			Action: "stop",
			Code:   codes[reason],
			IdleS:  int(r.Idle.Seconds()),
		}
		if err := r.Log.Append(ev); err != nil {
			r.logf("reaper: desk log (stop %s): %v", u, err)
		}
		r.logf("reaper: stopped %s: %s for %s", u, reason, r.Idle)
	}
}

// Run passes every period until ctx is cancelled. The first pass runs one
// period after Run starts.
func (r *Reaper) Run(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			r.Pass(ctx)
		}
	}
}

func (r *Reaper) logf(format string, args ...any) {
	if r.Logf != nil {
		r.Logf(format, args...)
		return
	}
	log.Printf(format, args...)
}
