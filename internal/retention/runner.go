package retention

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/daniellemccool/researchcloud-ddp-comments/internal/fourcat"
)

// The retention schedule and grace period (ADR-0027), in one place: a mark
// expires grace after the pass that made it; a pass runs at desk start,
// then interval after a good pass and retry after a failed one (4CAT's
// database may not accept connections yet when the desk starts); each call
// to 4CAT in a pass has its own callTimeout, so a blocked psql fails that
// call and never holds up the next. The interval is shorter than 4CAT's
// 30-minute expire worker, so an unmark normally lands before it acts.
const (
	grace       = 7 * 24 * time.Hour // 604800 s
	interval    = 15 * time.Minute
	retry       = 10 * time.Minute
	callTimeout = time.Minute
)

// Store is what a pass needs of 4CAT (*fourcat.Client): the read and the two
// writes, each write returning the keys it changed.
type Store interface {
	Snapshots(ctx context.Context) ([]fourcat.Snapshot, error)
	MarkExpiring(ctx context.Context, keys []string, until int64) ([]string, error)
	Unmark(ctx context.Context, keys []string) ([]string, error)
}

// Status is how the last finished pass went. At is when it started (zero:
// no pass has finished); Failed means the read or a write failed; DryRun
// means the switch was off, so the pass wrote nothing. Read is the
// snapshots read; Marked and Unmarked are the keys the writes returned, or
// in a dry run the keys Decide would have them write.
type Status struct {
	At                     time.Time
	Failed, DryRun         bool
	Read, Marked, Unmarked int
}

// Runner runs the retention pass and remembers how the last one went. Now
// and Logf are its clock and its log; a nil Logf logs nothing. Enabled is
// the switch, asked at the start of every pass: the pass writes to 4CAT only
// while it returns true; nil, or false, is a dry run.
type Runner struct {
	Store   Store
	Now     func() time.Time
	Logf    func(format string, args ...any)
	Enabled func() bool

	// timeout is each store call's deadline (callTimeout; shortened by the
	// tests).
	timeout time.Duration

	// What earlier passes of this process saw, for the lines logged only on
	// a change: the switch, and the last dry-run pass's lists. Only Once
	// uses them, and passes never overlap.
	switchSeen, wasOn  bool
	drySeen            bool
	dryMark, dryUnmark []string

	mu     sync.Mutex
	status Status
}

// Run passes now, then again after nextPass, until ctx is cancelled (also
// during a wait).
func (r *Runner) Run(ctx context.Context) {
	for ctx.Err() == nil {
		t := time.NewTimer(nextPass(r.Once(ctx)))
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-t.C:
		}
	}
}

// nextPass is the wait after a pass: interval after a good one, retry after
// a failed one.
func nextPass(s Status) time.Duration {
	if s.Failed {
		return retry
	}
	return interval
}

// Once is one pass: ask the switch, read, Decide, then mark and unmark, or
// in a dry run write nothing and log what it would do. The switch line is
// logged on the first pass and whenever the switch changes. A failed read
// writes nothing and logs one line. A failed mark still unmarks, the
// protective half. Each of the three calls has its own deadline
// (callTimeout), derived from ctx. Counts and log lines come from the keys
// the writes return; the summary line only when they changed something. An
// empty list makes no write call. The status is replaced when the pass ends
// and returned.
func (r *Runner) Once(ctx context.Context) Status {
	now := r.Now()
	on := r.Enabled != nil && r.Enabled()
	if !r.switchSeen || on != r.wasOn {
		if on {
			r.logf("retention: marking is on")
		} else {
			r.logf("retention: marking is off (dry run)")
		}
		r.switchSeen, r.wasOn = true, on
	}
	cctx, cancel := r.callCtx(ctx)
	snaps, err := r.Store.Snapshots(cctx)
	cancel()
	if err != nil {
		r.logf("retention: reading snapshots failed (%v); nothing changes", err)
		return r.finish(Status{At: now, Failed: true, DryRun: !on})
	}
	byKey := make(map[string]fourcat.Snapshot, len(snaps))
	for _, s := range snaps {
		byKey[s.Key] = s
	}
	mark, unmark := Decide(snaps)
	if !on {
		r.logDryRun(byKey, len(snaps), mark, unmark)
		return r.finish(Status{At: now, DryRun: true, Read: len(snaps), Marked: len(mark), Unmarked: len(unmark)})
	}
	st := Status{At: now, Read: len(snaps)}
	if len(mark) > 0 {
		until := now.Unix() + int64(grace/time.Second)
		expires := time.Unix(until, 0).UTC().Format("2006-01-02 15:04 UTC")
		cctx, cancel := r.callCtx(ctx)
		got, err := r.Store.MarkExpiring(cctx, mark, until)
		cancel()
		for _, k := range got {
			s := byKey[k]
			r.logf("retention: mark %s (%s, %s): superseded, expires %s", k, s.Creator, s.Type, expires)
		}
		st.Marked = len(got)
		if err != nil {
			st.Failed = true
			r.logf("retention: marking failed (%v); %d of %d marked", err, len(got), len(mark))
		}
	}
	if len(unmark) > 0 {
		cctx, cancel := r.callCtx(ctx)
		got, err := r.Store.Unmark(cctx, unmark)
		cancel()
		for _, k := range got {
			s := byKey[k]
			r.logf("retention: unmark %s (%s, %s): no longer expiring", k, s.Creator, s.Type)
		}
		st.Unmarked = len(got)
		if err != nil {
			st.Failed = true
			r.logf("retention: unmarking failed (%v); %d of %d unmarked", err, len(got), len(unmark))
		}
	}
	if st.Marked+st.Unmarked > 0 {
		r.logf("retention: %d snapshots read, %d marked to expire, %d unmarked", st.Read, st.Marked, st.Unmarked)
	}
	return r.finish(st)
}

// logDryRun logs what a dry-run pass would write, one line per key and a
// summary, only when its lists differ from the last dry-run pass's in this
// process.
func (r *Runner) logDryRun(byKey map[string]fourcat.Snapshot, read int, mark, unmark []string) {
	if r.drySeen && slices.Equal(mark, r.dryMark) && slices.Equal(unmark, r.dryUnmark) {
		return
	}
	r.drySeen, r.dryMark, r.dryUnmark = true, mark, unmark
	for _, k := range mark {
		s := byKey[k]
		r.logf("retention: dry run: would mark %s (%s, %s): superseded", k, s.Creator, s.Type)
	}
	for _, k := range unmark {
		s := byKey[k]
		r.logf("retention: dry run: would unmark %s (%s, %s)", k, s.Creator, s.Type)
	}
	r.logf("retention: dry run: %d snapshots read, %d would be marked, %d would be unmarked", read, len(mark), len(unmark))
}

// callCtx is one store call's context: the pass's context with the call's
// own deadline.
func (r *Runner) callCtx(ctx context.Context) (context.Context, context.CancelFunc) {
	d := r.timeout
	if d == 0 {
		d = callTimeout
	}
	return context.WithTimeout(ctx, d)
}

// finish records st as the last pass's status and returns it.
func (r *Runner) finish(st Status) Status {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.status = st
	return st
}

// Status is a copy of the last finished pass's status.
func (r *Runner) Status() Status {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.status
}

// StatusLine is the admin page's line, counts and a date and time only (in
// the clock's zone, as the CO poll line), "(dry run)" after the name when
// the last pass was one, and whether the last pass failed.
func (r *Runner) StatusLine() (line string, failed bool) {
	const statusTime = "2006-01-02 15:04"
	s := r.Status()
	name := "Snapshot retention"
	if s.DryRun {
		name += " (dry run)"
	}
	switch {
	case s.At.IsZero():
		return "Snapshot retention: not run yet", false
	case s.Failed:
		return name + ": failed at " + s.At.Format(statusTime), true
	case s.DryRun:
		return fmt.Sprintf("%s: last run %s, %d snapshots read, %d would be marked to expire, %d would be unmarked",
			name, s.At.Format(statusTime), s.Read, s.Marked, s.Unmarked), false
	}
	return fmt.Sprintf("%s: last run %s, %d snapshots read, %d marked to expire, %d unmarked",
		name, s.At.Format(statusTime), s.Read, s.Marked, s.Unmarked), false
}

func (r *Runner) logf(format string, args ...any) {
	if r.Logf != nil {
		r.Logf(format, args...)
	}
}
