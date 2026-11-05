package reaper_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/daniellemccool/researchcloud-ddp-comments/internal/desklog"
	"github.com/daniellemccool/researchcloud-ddp-comments/internal/reaper"
	"github.com/daniellemccool/researchcloud-ddp-comments/internal/web"
)

const idle = 900 * time.Second

// trace is the ordered record every fake writes to, so a test can check
// that Forget follows the stop.
type trace struct {
	mu    sync.Mutex
	lines []string
}

func (t *trace) add(format string, args ...any) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.lines = append(t.lines, fmt.Sprintf(format, args...))
}

func (t *trace) all() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]string(nil), t.lines...)
}

// fakeClock answers Idle from answer, given the user and the 1-based number
// of Idle calls for that user so far.
type fakeClock struct {
	tr     *trace
	answer func(user string, call int) (bool, string)
	calls  map[string]int
}

func (c *fakeClock) Idle(user string, d time.Duration) (bool, string) {
	if d != idle {
		panic(fmt.Sprintf("Idle called with %v, want %v", d, idle))
	}
	if c.calls == nil {
		c.calls = map[string]int{}
	}
	c.calls[user]++
	c.tr.add("idle %s", user)
	return c.answer(user, c.calls[user])
}

func (c *fakeClock) Forget(user string) { c.tr.add("forget %s", user) }

// fakePairs runs the cond StopIf is given and records a stop when it holds.
// A stopped user leaves the running set.
type fakePairs struct {
	tr         *trace
	mu         sync.Mutex
	running    map[string]bool
	runningErr error
	stopErr    error
	listed     int
}

func (p *fakePairs) Running(context.Context) (map[string]bool, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.listed++
	if p.runningErr != nil {
		return nil, p.runningErr
	}
	out := make(map[string]bool, len(p.running))
	for u, r := range p.running {
		out[u] = r
	}
	return out, nil
}

func (p *fakePairs) StopIf(_ context.Context, user string, cond func() bool) (bool, error) {
	p.tr.add("stopif %s", user)
	if !cond() {
		return false, nil
	}
	p.tr.add("stop %s", user)
	if p.stopErr != nil {
		return true, p.stopErr
	}
	p.mu.Lock()
	delete(p.running, user)
	p.mu.Unlock()
	return true, nil
}

func (p *fakePairs) listCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.listed
}

type fakeLog struct {
	evs []desklog.Event
}

func (l *fakeLog) Append(evs ...desklog.Event) error {
	l.evs = append(l.evs, evs...)
	return nil
}

// world is one test's reaper with its fakes.
type world struct {
	tr    *trace
	clock *fakeClock
	pairs *fakePairs
	log   *fakeLog
	logf  []string
	r     *reaper.Reaper
}

func newWorld(answer func(user string, call int) (bool, string), running ...string) *world {
	w := &world{tr: &trace{}, log: &fakeLog{}}
	w.clock = &fakeClock{tr: w.tr, answer: answer}
	w.pairs = &fakePairs{tr: w.tr, running: map[string]bool{}}
	for _, u := range running {
		w.pairs.running[u] = true
	}
	w.r = &reaper.Reaper{
		Idle:  idle,
		Clock: w.clock,
		Pairs: w.pairs,
		Log:   w.log,
		Logf:  func(format string, args ...any) { w.logf = append(w.logf, fmt.Sprintf(format, args...)) },
	}
	return w
}

// stripT drops the timestamps so events compare by content.
func stripT(evs []desklog.Event) []desklog.Event {
	out := make([]desklog.Event, len(evs))
	for i, e := range evs {
		if e.T == 0 {
			panic("event without a timestamp")
		}
		e.T = 0
		out[i] = e
	}
	return out
}

func TestPassStopsIdlePair(t *testing.T) {
	for _, tc := range []struct {
		reason, code string
	}{
		{"no input", "idle_no_input"},
		{"no screen", "idle_no_screen"},
	} {
		t.Run(tc.code, func(t *testing.T) {
			w := newWorld(func(string, int) (bool, string) { return true, tc.reason }, "alice")
			w.r.Pass(context.Background())

			// Selection, then the re-check inside StopIf's cond, then the
			// stop, then Forget.
			want := []string{"idle alice", "stopif alice", "idle alice", "stop alice", "forget alice"}
			if got := w.tr.all(); !reflect.DeepEqual(got, want) {
				t.Fatalf("trace = %v, want %v", got, want)
			}
			wantEv := []desklog.Event{{User: "alice", Action: "stop", Code: tc.code, IdleS: 900}}
			if got := stripT(w.log.evs); !reflect.DeepEqual(got, wantEv) {
				t.Fatalf("events = %+v, want %+v", got, wantEv)
			}
		})
	}
}

func TestPassSkipsActive(t *testing.T) {
	w := newWorld(func(user string, _ int) (bool, string) {
		if user == "bob" {
			return true, "no screen"
		}
		return false, ""
	}, "alice", "bob")
	w.r.Pass(context.Background())

	want := []string{"idle alice", "idle bob", "stopif bob", "idle bob", "stop bob", "forget bob"}
	if got := w.tr.all(); !reflect.DeepEqual(got, want) {
		t.Fatalf("trace = %v, want %v", got, want)
	}
	if len(w.log.evs) != 1 || w.log.evs[0].User != "bob" {
		t.Fatalf("events = %+v, want only bob's stop", w.log.evs)
	}
}

func TestPassRecheckUnderLock(t *testing.T) {
	// Idle at selection, active by the time the user lock is held (the page
	// reported input in between): no stop, no event, no Forget.
	w := newWorld(func(_ string, call int) (bool, string) {
		if call == 1 {
			return true, "no input"
		}
		return false, ""
	}, "alice")
	w.r.Pass(context.Background())

	want := []string{"idle alice", "stopif alice", "idle alice"}
	if got := w.tr.all(); !reflect.DeepEqual(got, want) {
		t.Fatalf("trace = %v, want %v", got, want)
	}
	if len(w.log.evs) != 0 {
		t.Fatalf("events = %+v, want none", w.log.evs)
	}
}

func TestPassRunningErrorStopsNothing(t *testing.T) {
	w := newWorld(func(string, int) (bool, string) { return true, "no input" }, "alice")
	w.pairs.runningErr = errors.New("docker list failed")
	w.r.Pass(context.Background())

	if got := w.tr.all(); len(got) != 0 {
		t.Fatalf("trace = %v, want nothing after a Running error", got)
	}
	if len(w.log.evs) != 0 {
		t.Fatalf("events = %+v, want none", w.log.evs)
	}
	if len(w.logf) != 1 || !strings.Contains(w.logf[0], "docker list failed") {
		t.Fatalf("logf = %v, want one line naming the error", w.logf)
	}
}

func TestPassStopErrorLogsNoEvent(t *testing.T) {
	w := newWorld(func(string, int) (bool, string) { return true, "no input" }, "alice")
	w.pairs.stopErr = errors.New("remove net-alice: boom")
	w.r.Pass(context.Background())

	want := []string{"idle alice", "stopif alice", "idle alice", "stop alice"}
	if got := w.tr.all(); !reflect.DeepEqual(got, want) {
		t.Fatalf("trace = %v, want %v (no Forget after a failed stop)", got, want)
	}
	if len(w.log.evs) != 0 {
		t.Fatalf("events = %+v, want none after a failed stop", w.log.evs)
	}
	if len(w.logf) != 1 || !strings.Contains(w.logf[0], "boom") || !strings.Contains(w.logf[0], "alice") {
		t.Fatalf("logf = %v, want one line naming alice and the error", w.logf)
	}
}

// TestReaperGraceAfterRestart: after a desk restart the tracker is empty and
// two adopted pairs are running. Nobody is stopped before a full idle period
// has passed since the first pass saw them; one pass after that stops both.
func TestReaperGraceAfterRestart(t *testing.T) {
	t0 := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	now := t0
	activity := web.NewActivity(func() time.Time { return now })
	tr := &trace{}
	pairs := &fakePairs{tr: tr, running: map[string]bool{"alice": true, "bob": true}}
	dlog := &fakeLog{}
	r := &reaper.Reaper{Idle: idle, Clock: activity, Pairs: pairs, Log: dlog, Logf: func(string, ...any) {}}

	for _, at := range []time.Duration{0, idle - time.Second} {
		now = t0.Add(at)
		r.Pass(context.Background())
		if len(dlog.evs) != 0 {
			t.Fatalf("pass at t=%v stopped %+v, want nothing", at, dlog.evs)
		}
	}
	now = t0.Add(idle + 61*time.Second)
	r.Pass(context.Background())
	want := []desklog.Event{
		{User: "alice", Action: "stop", Code: "idle_no_input", IdleS: 900},
		{User: "bob", Action: "stop", Code: "idle_no_input", IdleS: 900},
	}
	if got := stripT(dlog.evs); !reflect.DeepEqual(got, want) {
		t.Fatalf("events at t=idle+61s = %+v, want %+v", got, want)
	}
	// Forget gave both users fresh clocks: a pair started now has the full
	// grace again.
	if stale, why := activity.Idle("alice", idle); stale {
		t.Fatalf("alice idle right after the stop (%s), want fresh clocks", why)
	}
}

// TestRunPassesUntilCancelled: Run passes every period and returns once its
// context is cancelled.
func TestRunPassesUntilCancelled(t *testing.T) {
	w := newWorld(func(string, int) (bool, string) { return false, "" }, "alice")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		w.r.Run(ctx, 5*time.Millisecond)
		close(done)
	}()
	deadline := time.Now().Add(2 * time.Second)
	for w.pairs.listCount() < 2 {
		if time.Now().After(deadline) {
			cancel()
			t.Fatalf("Run made %d passes in 2 s, want at least 2", w.pairs.listCount())
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
}
