package retention

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/daniellemccool/researchcloud-ddp-comments/internal/fourcat"
)

// passTime is the fake clock of every pass: 2026-11-18 12:00 UTC, so a mark
// expires 2026-11-25 12:00 UTC.
var passTime = time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)

// call is one store call as the fake records it, with what its context
// looked like when the call began.
type call struct {
	op       string // "snapshots", "mark" or "unmark"
	keys     []string
	until    int64
	deadline time.Time // zero: the context had none
	expired  bool      // the context was already done
	tag      any       // the passTag value the context carries
}

// passTag marks a pass's context, so a test sees that a call's context
// derives from it.
type passTag struct{}

// fakeStore records every call in order. The read returns snaps even with
// readErr, so a runner that trusted data beside an error would write. keep
// narrows what a write returns (nil: every key it was given). readHang and
// markHang make the call wait until its context is done and return its
// error, as the docker exec does.
type fakeStore struct {
	mu                          sync.Mutex
	snaps                       []fourcat.Snapshot
	readErr, markErr, unmarkErr error
	markKeep, unmarkKeep        func([]string) []string
	readHang, markHang          bool
	calls                       []call
	// started and release, when set, hold the read until the test lets it go.
	started, release chan struct{}
}

func (f *fakeStore) record(ctx context.Context, c call) {
	c.deadline, _ = ctx.Deadline()
	c.expired = ctx.Err() != nil
	c.tag = ctx.Value(passTag{})
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, c)
}

func (f *fakeStore) Calls() []call {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.calls)
}

func (f *fakeStore) Snapshots(ctx context.Context) ([]fourcat.Snapshot, error) {
	f.record(ctx, call{op: "snapshots"})
	if f.started != nil {
		f.started <- struct{}{}
		<-f.release
	}
	if f.readHang {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return f.snaps, f.readErr
}

func (f *fakeStore) MarkExpiring(ctx context.Context, keys []string, until int64) ([]string, error) {
	f.record(ctx, call{op: "mark", keys: slices.Clone(keys), until: until})
	if f.markHang {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if f.markKeep != nil {
		return f.markKeep(keys), f.markErr
	}
	return slices.Clone(keys), f.markErr
}

func (f *fakeStore) Unmark(ctx context.Context, keys []string) ([]string, error) {
	f.record(ctx, call{op: "unmark", keys: slices.Clone(keys)})
	if f.unmarkKeep != nil {
		return f.unmarkKeep(keys), f.unmarkErr
	}
	return slices.Clone(keys), f.unmarkErr
}

// logs collects the runner's log lines.
type logs struct {
	mu    sync.Mutex
	lines []string
}

func (l *logs) logf(format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, fmt.Sprintf(format, args...))
}

func (l *logs) all() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Clone(l.lines)
}

// newRunner is a runner with marking on, as every test of a real pass needs.
func newRunner(s *fakeStore) (*Runner, *logs) {
	l := &logs{}
	return &Runner{Store: s, Now: func() time.Time { return passTime }, Logf: l.logf, Enabled: func() bool { return true }}, l
}

// The lines that say which way the switch is.
const (
	onLine  = "retention: marking is on"
	offLine = "retention: marking is off (dry run)"
)

// twoWrites is a snapshot list Decide answers with mark [1, 2, 3] and unmark
// [4, 5]: alice's 1, 2 and 3 are outside her newest two (10, 11), which have
// as many rows; bob's 4 and 5, both desk-marked, are each larger than
// everything newer.
func twoWrites() []fourcat.Snapshot {
	return []fourcat.Snapshot{
		snap(1, "alice", posts, 100, 10),
		snap(2, "alice", posts, 200, 10),
		snap(3, "alice", posts, 300, 10),
		snap(10, "alice", posts, 400, 10),
		snap(11, "alice", posts, 500, 10),
		marked(snap(4, "bob", comments, 100, 90)),
		marked(snap(5, "bob", comments, 200, 50)),
		snap(6, "bob", comments, 300, 10),
		snap(7, "bob", comments, 400, 10),
	}
}

func markLine(n int, creator, typ string) string {
	return fmt.Sprintf("retention: mark %s (%s, %s): superseded, expires 2026-11-25 12:00 UTC", key(n), creator, typ)
}

func unmarkLine(n int, creator, typ string) string {
	return fmt.Sprintf("retention: unmark %s (%s, %s): no longer expiring", key(n), creator, typ)
}

// The pass reads, then marks Decide's mark list with now + 604800, then
// unmarks Decide's unmark list, in that order, and records how it went.
func TestPassCallsInOrder(t *testing.T) {
	snaps := twoWrites()
	wantMark, wantUnmark := Decide(snaps)
	if !slices.Equal(wantMark, keys(1, 2, 3)) || !slices.Equal(wantUnmark, keys(4, 5)) {
		t.Fatalf("fixture: Decide = %v, %v", wantMark, wantUnmark)
	}
	s := &fakeStore{snaps: snaps}
	r, _ := newRunner(s)
	got := r.Once(context.Background())

	want := []call{
		{op: "snapshots"},
		{op: "mark", keys: wantMark, until: passTime.Unix() + 604800},
		{op: "unmark", keys: wantUnmark},
	}
	calls := s.Calls()
	if len(calls) != len(want) {
		t.Fatalf("calls = %+v, want %+v", calls, want)
	}
	for i := range want {
		if calls[i].op != want[i].op || !slices.Equal(calls[i].keys, want[i].keys) || calls[i].until != want[i].until {
			t.Errorf("call %d = %+v, want %+v", i, calls[i], want[i])
		}
	}
	wantStatus := Status{At: passTime, Read: 9, Marked: 3, Unmarked: 2}
	if got != wantStatus || r.Status() != wantStatus {
		t.Errorf("Once = %+v, Status = %+v, want %+v", got, r.Status(), wantStatus)
	}
}

// After the switch line of the first pass: one line per changed key, creator
// and type from this pass's read, the expiry as a UTC date and time; then one
// summary line.
func TestPassLogLines(t *testing.T) {
	s := &fakeStore{snaps: twoWrites()}
	r, l := newRunner(s)
	r.Once(context.Background())
	want := []string{
		onLine,
		markLine(1, "alice", posts),
		markLine(2, "alice", posts),
		markLine(3, "alice", posts),
		unmarkLine(4, "bob", comments),
		unmarkLine(5, "bob", comments),
		"retention: 9 snapshots read, 3 marked to expire, 2 unmarked",
	}
	if got := l.all(); !slices.Equal(got, want) {
		t.Errorf("log =\n%q\nwant\n%q", got, want)
	}
}

// TestRetentionSurvivesReadFailure: a read that fails writes nothing, even
// when data came with the error, logs one line (after the first pass's
// switch line) and records the failure.
func TestRetentionSurvivesReadFailure(t *testing.T) {
	s := &fakeStore{snaps: twoWrites(), readErr: errors.New("psql exited with status 2")}
	r, l := newRunner(s)
	got := r.Once(context.Background())
	if calls := s.Calls(); len(calls) != 1 || calls[0].op != "snapshots" {
		t.Errorf("calls = %+v, want only the read", calls)
	}
	if lines := l.all(); len(lines) != 2 || lines[0] != onLine {
		t.Errorf("log = %q, want the switch line and one line", lines)
	}
	want := Status{At: passTime, Failed: true}
	if got != want || r.Status() != want {
		t.Errorf("Once = %+v, Status = %+v, want %+v", got, r.Status(), want)
	}
}

// A failed mark does not stop the unmark, the protective half; the pass is
// failed, and what the mark did change is still logged and counted.
func TestUnmarkAfterFailedMark(t *testing.T) {
	s := &fakeStore{
		snaps:    twoWrites(),
		markErr:  errors.New("psql exited with status 1"),
		markKeep: func(k []string) []string { return k[:1] },
	}
	r, l := newRunner(s)
	got := r.Once(context.Background())
	calls := s.Calls()
	if len(calls) != 3 || calls[2].op != "unmark" || !slices.Equal(calls[2].keys, keys(4, 5)) {
		t.Fatalf("calls = %+v, want the unmark after the failed mark", calls)
	}
	want := Status{At: passTime, Failed: true, Read: 9, Marked: 1, Unmarked: 2}
	if got != want {
		t.Errorf("Once = %+v, want %+v", got, want)
	}
	lines := l.all()
	if !slices.Contains(lines, markLine(1, "alice", posts)) || slices.Contains(lines, markLine(2, "alice", posts)) {
		t.Errorf("log = %q, want the one changed key's mark line only", lines)
	}
}

// A failed unmark makes the pass failed too.
func TestFailedUnmarkFailsPass(t *testing.T) {
	s := &fakeStore{snaps: twoWrites(), unmarkErr: errors.New("psql exited with status 1"), unmarkKeep: func([]string) []string { return nil }}
	r, _ := newRunner(s)
	want := Status{At: passTime, Failed: true, Read: 9, Marked: 3}
	if got := r.Once(context.Background()); got != want {
		t.Errorf("Once = %+v, want %+v", got, want)
	}
}

// The counts and the lines come from the keys the writes return, never from
// what Decide asked for.
func TestCountsFromReturnedKeys(t *testing.T) {
	s := &fakeStore{
		snaps:      twoWrites(),
		markKeep:   func(k []string) []string { return []string{k[1]} },
		unmarkKeep: func([]string) []string { return nil },
	}
	r, l := newRunner(s)
	want := Status{At: passTime, Read: 9, Marked: 1}
	if got := r.Once(context.Background()); got != want {
		t.Errorf("Once = %+v, want %+v", got, want)
	}
	wantLines := []string{
		onLine,
		markLine(2, "alice", posts),
		"retention: 9 snapshots read, 1 marked to expire, 0 unmarked",
	}
	if got := l.all(); !slices.Equal(got, wantLines) {
		t.Errorf("log =\n%q\nwant\n%q", got, wantLines)
	}
}

// Nothing to mark or unmark: no write call at all.
func TestEmptyListsMakeNoCall(t *testing.T) {
	s := &fakeStore{snaps: []fourcat.Snapshot{snap(1, "alice", posts, 100, 10)}}
	r, _ := newRunner(s)
	want := Status{At: passTime, Read: 1}
	if got := r.Once(context.Background()); got != want {
		t.Errorf("Once = %+v, want %+v", got, want)
	}
	if calls := s.Calls(); len(calls) != 1 {
		t.Errorf("calls = %+v, want only the read", calls)
	}
}

// A real pass writes its summary line only when it changed something: not
// when there was nothing to do, and not when the writes changed no key.
func TestSummaryOnlyWhenChanged(t *testing.T) {
	s := &fakeStore{snaps: []fourcat.Snapshot{snap(1, "alice", posts, 100, 10)}}
	r, l := newRunner(s)
	r.Once(context.Background())
	r.Once(context.Background())
	if got := l.all(); !slices.Equal(got, []string{onLine}) {
		t.Errorf("log after two passes with nothing to do = %q, want only the switch line", got)
	}

	none := func([]string) []string { return nil }
	s = &fakeStore{snaps: twoWrites(), markKeep: none, unmarkKeep: none}
	r, l = newRunner(s)
	if got, want := r.Once(context.Background()), (Status{At: passTime, Read: 9}); got != want {
		t.Errorf("Once = %+v, want %+v", got, want)
	}
	if got := l.all(); !slices.Equal(got, []string{onLine}) {
		t.Errorf("log after writes that changed nothing = %q, want only the switch line", got)
	}
}

// After a good pass the next is 15 minutes later, after a failed one 10
// minutes.
func TestNextPass(t *testing.T) {
	if got := nextPass(Status{At: passTime}); got != 15*time.Minute {
		t.Errorf("after a good pass: %v, want 15m", got)
	}
	if got := nextPass(Status{At: passTime, Failed: true}); got != 10*time.Minute {
		t.Errorf("after a failed pass: %v, want 10m", got)
	}
	if got := nextPass(Status{At: passTime, DryRun: true}); got != 15*time.Minute {
		t.Errorf("after a good dry-run pass: %v, want 15m", got)
	}
	if got := nextPass(Status{At: passTime, DryRun: true, Failed: true}); got != 10*time.Minute {
		t.Errorf("after a failed dry-run pass: %v, want 10m", got)
	}
}

// Run passes at once and returns when its context is cancelled during the
// wait that follows.
func TestRunStopsOnCancel(t *testing.T) {
	s := &fakeStore{snaps: twoWrites()}
	r, _ := newRunner(s)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { r.Run(ctx); close(done) }()
	deadline := time.Now().Add(5 * time.Second)
	for r.Status().At.IsZero() {
		if time.Now().After(deadline) {
			t.Fatal("no pass at start")
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
	if n := len(s.Calls()); n != 3 {
		t.Errorf("calls = %d, want one pass of 3", n)
	}
}

// The status reads while a pass runs: zero until the pass finishes (run
// with -race).
func TestStatusDuringPass(t *testing.T) {
	s := &fakeStore{snaps: twoWrites(), started: make(chan struct{}), release: make(chan struct{})}
	r, _ := newRunner(s)
	done := make(chan Status)
	go func() { done <- r.Once(context.Background()) }()
	<-s.started
	if got := r.Status(); got != (Status{}) {
		t.Errorf("Status during the first pass = %+v, want zero", got)
	}
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() { defer wg.Done(); _ = r.Status(); _, _ = r.StatusLine() }()
	}
	close(s.release)
	wg.Wait()
	if got := <-done; got != r.Status() {
		t.Errorf("Status after the pass = %+v, want %+v", r.Status(), got)
	}
}

// The admin page's line in its five states, marking on and in a dry run:
// counts and a time only.
func TestStatusLine(t *testing.T) {
	on := func() bool { return true }
	off := func() bool { return false }
	for _, c := range []struct {
		name       string
		store      *fakeStore
		enabled    func() bool
		pass       bool
		wantLine   string
		wantFailed bool
	}{
		{"before a pass", &fakeStore{}, on, false,
			"Snapshot retention: not run yet", false},
		{"good pass", &fakeStore{snaps: twoWrites()}, on, true,
			"Snapshot retention: last run 2026-11-18 12:00, 9 snapshots read, 3 marked to expire, 2 unmarked", false},
		{"failed pass", &fakeStore{readErr: errors.New("down")}, on, true,
			"Snapshot retention: failed at 2026-11-18 12:00", true},
		{"good dry-run pass", &fakeStore{snaps: twoWrites()}, off, true,
			"Snapshot retention (dry run): last run 2026-11-18 12:00, 9 snapshots read, 3 would be marked to expire, 2 would be unmarked", false},
		{"failed dry-run pass", &fakeStore{readErr: errors.New("down")}, off, true,
			"Snapshot retention (dry run): failed at 2026-11-18 12:00", true},
	} {
		r := &Runner{Store: c.store, Now: func() time.Time { return passTime }, Enabled: c.enabled}
		if c.pass {
			r.Once(context.Background())
		}
		if line, failed := r.StatusLine(); line != c.wantLine || failed != c.wantFailed {
			t.Errorf("%s: %q, %v; want %q, %v", c.name, line, failed, c.wantLine, c.wantFailed)
		}
	}
}

// switchFunc is a switch the test turns: Enabled returns *on and counts how
// often it was asked.
func switchFunc(on *bool, asked *int) func() bool {
	return func() bool { *asked++; return *on }
}

func dryMarkLine(n int, creator, typ string) string {
	return fmt.Sprintf("retention: dry run: would mark %s (%s, %s): superseded", key(n), creator, typ)
}

func dryUnmarkLine(n int, creator, typ string) string {
	return fmt.Sprintf("retention: dry run: would unmark %s (%s, %s)", key(n), creator, typ)
}

// TestDryRunWritesNothing: switched off, a pass reads and decides but makes
// no write call, though Decide has keys for both, and its status counts what
// it would do.
func TestDryRunWritesNothing(t *testing.T) {
	s := &fakeStore{snaps: twoWrites()}
	r, _ := newRunner(s)
	r.Enabled = func() bool { return false }
	got := r.Once(context.Background())
	if calls := s.Calls(); len(calls) != 1 || calls[0].op != "snapshots" {
		t.Errorf("calls = %+v, want only the read", calls)
	}
	want := Status{At: passTime, DryRun: true, Read: 9, Marked: 3, Unmarked: 2}
	if got != want || r.Status() != want {
		t.Errorf("Once = %+v, Status = %+v, want %+v", got, r.Status(), want)
	}
}

// A runner without a switch is a dry run.
func TestNilEnabledIsDryRun(t *testing.T) {
	s := &fakeStore{snaps: twoWrites()}
	r, l := newRunner(s)
	r.Enabled = nil
	got := r.Once(context.Background())
	if calls := s.Calls(); len(calls) != 1 || calls[0].op != "snapshots" {
		t.Errorf("calls = %+v, want only the read", calls)
	}
	if !got.DryRun {
		t.Errorf("Once = %+v, want a dry run", got)
	}
	if lines := l.all(); len(lines) == 0 || lines[0] != offLine {
		t.Errorf("log = %q, want %q first", lines, offLine)
	}
}

// The switch is asked once at the start of every pass, so off, on, off
// across three passes of one runner writes only in the second; each change,
// and the first pass, logs the switch line first.
func TestSwitchReadEveryPass(t *testing.T) {
	s := &fakeStore{snaps: twoWrites()}
	r, l := newRunner(s)
	on, asked := false, 0
	r.Enabled = switchFunc(&on, &asked)

	ops := func() []string {
		var out []string
		for _, c := range s.Calls() {
			out = append(out, c.op)
		}
		return out
	}
	for i, c := range []struct {
		on       bool
		wantOps  []string
		wantLine string
	}{
		{false, []string{"snapshots"}, offLine},
		{true, []string{"snapshots", "snapshots", "mark", "unmark"}, onLine},
		{false, []string{"snapshots", "snapshots", "mark", "unmark", "snapshots"}, offLine},
	} {
		on = c.on
		before := len(l.all())
		st := r.Once(context.Background())
		if asked != i+1 {
			t.Errorf("pass %d: switch asked %d times in all, want %d", i+1, asked, i+1)
		}
		if st.DryRun == c.on {
			t.Errorf("pass %d: DryRun = %v with the switch %v", i+1, st.DryRun, c.on)
		}
		if got := ops(); !slices.Equal(got, c.wantOps) {
			t.Errorf("pass %d: calls = %v, want %v", i+1, got, c.wantOps)
		}
		if lines := l.all(); len(lines) <= before || lines[before] != c.wantLine {
			t.Errorf("pass %d: log = %q, want %q first", i+1, lines[before:], c.wantLine)
		}
	}

	// The same way twice: no switch line.
	before := len(l.all())
	r.Once(context.Background())
	if lines := l.all()[before:]; slices.Contains(lines, onLine) || slices.Contains(lines, offLine) {
		t.Errorf("pass with the switch unchanged: log = %q, want no switch line", lines)
	}
}

// The dry-run lines: one per key Decide would mark or unmark, then a
// summary, on the first dry-run pass; nothing on an identical pass; all
// again when the decision changes.
func TestDryRunLines(t *testing.T) {
	s := &fakeStore{snaps: twoWrites()}
	r, l := newRunner(s)
	r.Enabled = func() bool { return false }

	r.Once(context.Background())
	want := []string{
		offLine,
		dryMarkLine(1, "alice", posts),
		dryMarkLine(2, "alice", posts),
		dryMarkLine(3, "alice", posts),
		dryUnmarkLine(4, "bob", comments),
		dryUnmarkLine(5, "bob", comments),
		"retention: dry run: 9 snapshots read, 3 would be marked, 2 would be unmarked",
	}
	if got := l.all(); !slices.Equal(got, want) {
		t.Errorf("first pass: log =\n%q\nwant\n%q", got, want)
	}

	r.Once(context.Background())
	if got := l.all()[len(want):]; len(got) != 0 {
		t.Errorf("identical second pass: log = %q, want nothing", got)
	}

	// A newer, larger upload of alice's: her 10 is now superseded too.
	s.snaps = append(twoWrites(), snap(12, "alice", posts, 600, 20))
	r.Once(context.Background())
	want2 := []string{
		dryMarkLine(1, "alice", posts),
		dryMarkLine(2, "alice", posts),
		dryMarkLine(3, "alice", posts),
		dryMarkLine(10, "alice", posts),
		dryUnmarkLine(4, "bob", comments),
		dryUnmarkLine(5, "bob", comments),
		"retention: dry run: 10 snapshots read, 4 would be marked, 2 would be unmarked",
	}
	if got := l.all()[len(want):]; !slices.Equal(got, want2) {
		t.Errorf("pass after a change: log =\n%q\nwant\n%q", got, want2)
	}
}

// A dry-run pass whose read fails is handled as in a real pass: one line,
// the pass failed, the retry after 10 minutes, and no dry-run lines.
func TestDryRunReadFailure(t *testing.T) {
	s := &fakeStore{snaps: twoWrites(), readErr: errors.New("psql exited with status 2")}
	r, l := newRunner(s)
	r.Enabled = func() bool { return false }
	got := r.Once(context.Background())
	if calls := s.Calls(); len(calls) != 1 {
		t.Errorf("calls = %+v, want only the read", calls)
	}
	if lines := l.all(); len(lines) != 2 || lines[0] != offLine {
		t.Errorf("log = %q, want the switch line and one line", lines)
	}
	if want := (Status{At: passTime, Failed: true, DryRun: true}); got != want {
		t.Errorf("Once = %+v, want %+v", got, want)
	}
	if nextPass(got) != 10*time.Minute {
		t.Errorf("next pass after a failed dry-run read: %v, want 10m", nextPass(got))
	}
}

// Each of the three store calls gets its own one-minute deadline, derived
// from the pass's context.
func TestStoreCallDeadlines(t *testing.T) {
	s := &fakeStore{snaps: twoWrites()}
	r, _ := newRunner(s)
	ctx := context.WithValue(context.Background(), passTag{}, "pass")
	before := time.Now()
	r.Once(ctx)
	after := time.Now()
	calls := s.Calls()
	if len(calls) != 3 {
		t.Fatalf("calls = %+v, want the read and both writes", calls)
	}
	for _, c := range calls {
		if c.deadline.IsZero() || c.deadline.Before(before.Add(time.Minute)) || c.deadline.After(after.Add(time.Minute)) {
			t.Errorf("%s: deadline %v, want one minute from the call (between %v and %v)",
				c.op, c.deadline, before.Add(time.Minute), after.Add(time.Minute))
		}
		if c.tag != "pass" {
			t.Errorf("%s: context does not derive from the pass's context", c.op)
		}
	}
}

// A mark that runs out of time is a failed mark; the unmark still runs,
// with a fresh deadline of its own.
func TestUnmarkAfterMarkTimeout(t *testing.T) {
	s := &fakeStore{snaps: twoWrites(), markHang: true}
	r, l := newRunner(s)
	r.timeout = 20 * time.Millisecond
	// The pass's own 5-second limit only keeps a broken runner from hanging
	// the test; a runner that shared one deadline would pass it on expired.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	got := r.Once(ctx)
	calls := s.Calls()
	if len(calls) != 3 || calls[2].op != "unmark" {
		t.Fatalf("calls = %+v, want the unmark after the timed-out mark", calls)
	}
	if calls[2].expired || calls[2].deadline.IsZero() || !calls[2].deadline.After(time.Now()) {
		t.Errorf("unmark context: expired %v, deadline %v; want a fresh, live deadline", calls[2].expired, calls[2].deadline)
	}
	want := Status{At: passTime, Failed: true, Read: 9, Unmarked: 2}
	if got != want {
		t.Errorf("Once = %+v, want %+v", got, want)
	}
	if lines := l.all(); slices.Contains(lines, markLine(1, "alice", posts)) {
		t.Errorf("log = %q, want no mark line after a timed-out mark", lines)
	}
}

// A read that runs out of time is a failed read: nothing is written and
// the next pass is the retry.
func TestReadTimeoutFails(t *testing.T) {
	s := &fakeStore{snaps: twoWrites(), readHang: true}
	r, l := newRunner(s)
	r.timeout = 20 * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	start := time.Now()
	got := r.Once(ctx)
	if d := time.Since(start); d > 2*time.Second {
		t.Errorf("the read waited %v, want its own short deadline", d)
	}
	if calls := s.Calls(); len(calls) != 1 {
		t.Errorf("calls = %+v, want only the read", calls)
	}
	if lines := l.all(); len(lines) != 2 || lines[0] != onLine {
		t.Errorf("log = %q, want the switch line and one line", lines)
	}
	if want := (Status{At: passTime, Failed: true}); got != want {
		t.Errorf("Once = %+v, want %+v", got, want)
	}
	if nextPass(got) != 10*time.Minute {
		t.Errorf("next pass after a timed-out read: %v, want 10m", nextPass(got))
	}
}
