package web

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/daniellemccool/researchcloud-ddp-comments/internal/config"
	"github.com/daniellemccool/researchcloud-ddp-comments/internal/fourcat"
)

// TestUploadStateRule: uploaded means every module has a finished upload at
// or after the latest capture; the time shown is the oldest of the modules'
// latest uploads when done and the latest capture when due.
func TestUploadStateRule(t *testing.T) {
	capture := time.Unix(100, 0)
	ab := []string{"a", "b"}
	for _, c := range []struct {
		name     string
		captured bool
		byType   map[string]int64
		modules  []string
		want     UploadState
	}{
		{"nothing captured", false, map[string]int64{"a-search": 100, "b-search": 120}, ab, UploadState{Kind: uploadNone}},
		{"both at or after the capture", true, map[string]int64{"a-search": 100, "b-search": 120}, ab, UploadState{Kind: uploadDone, At: time.Unix(100, 0)}},
		{"one module never uploaded", true, map[string]int64{"a-search": 120}, ab, UploadState{Kind: uploadDue, At: capture}},
		{"one module uploaded before the capture", true, map[string]int64{"a-search": 120, "b-search": 99}, ab, UploadState{Kind: uploadDue, At: capture}},
		{"no uploads at all", true, nil, ab, UploadState{Kind: uploadDue, At: capture}},
		{"another type does not count", true, map[string]int64{"a-search": 120, "b-comments-search": 120}, ab, UploadState{Kind: uploadDue, At: capture}},
		{"no modules", true, map[string]int64{"a-search": 120}, nil, UploadState{Kind: uploadNone}},
	} {
		got := uploadState(capture, c.captured, c.byType, c.modules)
		if got.Kind != c.want.Kind || !got.At.Equal(c.want.At) {
			t.Errorf("%s: uploadState = {%d %v}, want {%d %v}", c.name, got.Kind, got.At, c.want.Kind, c.want.At)
		}
	}
}

// fakeUploads is an uploadsReader that counts its reads and notes, for the
// last one, whether its context had ended and how long it had left.
type fakeUploads struct {
	ups     fourcat.Uploads
	err     error
	reads   int
	ctxErr  error
	left    time.Duration
	limited bool
}

func (f *fakeUploads) LatestUploads(ctx context.Context) (fourcat.Uploads, error) {
	f.reads++
	f.ctxErr = ctx.Err()
	var deadline time.Time
	if deadline, f.limited = ctx.Deadline(); f.limited {
		f.left = time.Until(deadline)
	}
	return f.ups, f.err
}

// uploadsRig is an uploadStates over a temp desk root with the modules a
// and b, a clock the test moves, and a counting reader.
type uploadsRig struct {
	root   string
	clock  time.Time
	reader *fakeUploads
	states *uploadStates
}

func newUploadsRig(t *testing.T) *uploadsRig {
	t.Helper()
	plat := testPlatform()
	plat.ZeeschuimerModules = []string{"a", "b"}
	rig := &uploadsRig{root: t.TempDir(), clock: time.Unix(1_000_000, 0), reader: &fakeUploads{}}
	rig.states = newUploadStates(Deps{
		Cfg:      &config.Config{DeskRoot: rig.root},
		Platform: plat,
		Uploads:  rig.reader,
		Now:      func() time.Time { return rig.clock },
	})
	if rig.states == nil {
		t.Fatal("newUploadStates returned nil although a reader is wired")
	}
	return rig
}

// captured writes user's captures file with text and sets its modification
// time, the latest capture.
func (r *uploadsRig) captured(t *testing.T, user, text string, at time.Time) {
	t.Helper()
	path := filepath.Join(r.root, "state", "captures", user, "captures.ndjson")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, at, at); err != nil {
		t.Fatal(err)
	}
}

func TestUploadCacheKeepsOneMinute(t *testing.T) {
	rig := newUploadsRig(t)
	rig.captured(t, "alice", "{}\n", time.Unix(500, 0))
	rig.reader.ups = fourcat.Uploads{"alice": {"a-search": 600, "b-search": 600}}
	ctx := context.Background()

	if st := rig.states.Of(ctx, "alice"); st.Kind != uploadDone {
		t.Fatalf("first Of = %d, want done", st.Kind)
	}
	rig.clock = rig.clock.Add(30 * time.Second)
	rig.reader.ups = fourcat.Uploads{} // 4CAT changed; the cache must not notice yet
	if st := rig.states.Of(ctx, "alice"); st.Kind != uploadDone {
		t.Errorf("Of at 30 s = %d, want the cached done", st.Kind)
	}
	if rig.reader.reads != 1 {
		t.Fatalf("reads after two calls 30 s apart = %d, want 1", rig.reader.reads)
	}
	rig.clock = rig.clock.Add(31 * time.Second)
	if st := rig.states.Of(ctx, "alice"); st.Kind != uploadDue {
		t.Errorf("Of at 61 s = %d, want due from a fresh read", st.Kind)
	}
	if rig.reader.reads != 2 {
		t.Errorf("reads after 61 s = %d, want 2", rig.reader.reads)
	}
}

// TestUploadCacheRemembersFailure: a 4CAT that is down is asked once a
// minute, not once per check-in, and the state is unknown meanwhile.
func TestUploadCacheRemembersFailure(t *testing.T) {
	rig := newUploadsRig(t)
	rig.captured(t, "alice", "{}\n", time.Unix(500, 0))
	rig.reader.err = errors.New("psql exited with status 2")
	ctx := context.Background()

	if st := rig.states.Of(ctx, "alice"); st.Kind != uploadUnknown {
		t.Fatalf("Of with a failing reader = %d, want unknown", st.Kind)
	}
	rig.clock = rig.clock.Add(59 * time.Second)
	if st := rig.states.Of(ctx, "alice"); st.Kind != uploadUnknown {
		t.Errorf("Of at 59 s = %d, want unknown", st.Kind)
	}
	if rig.reader.reads != 1 {
		t.Errorf("reads within the minute after a failure = %d, want 1", rig.reader.reads)
	}
	rig.clock = rig.clock.Add(2 * time.Second)
	rig.reader.err = nil
	rig.reader.ups = fourcat.Uploads{"alice": {"a-search": 600, "b-search": 600}}
	if st := rig.states.Of(ctx, "alice"); st.Kind != uploadDone {
		t.Errorf("Of at 61 s with 4CAT back = %d, want done", st.Kind)
	}
}

// TestUploadReadHasItsOwnTimeLimit: one read is limited to five seconds and
// does not end with the request that happened to start it, since its result
// is kept for everyone.
func TestUploadReadHasItsOwnTimeLimit(t *testing.T) {
	rig := newUploadsRig(t)
	rig.captured(t, "alice", "{}\n", time.Unix(500, 0))
	reqCtx, cancel := context.WithCancel(context.Background())
	cancel() // the student's request is already gone
	rig.states.Of(reqCtx, "alice")
	if rig.reader.reads != 1 {
		t.Fatalf("reads = %d, want 1", rig.reader.reads)
	}
	if rig.reader.ctxErr != nil {
		t.Errorf("the read's context had ended (%v) with the request's cancel", rig.reader.ctxErr)
	}
	if !rig.reader.limited {
		t.Fatal("the read has no time limit")
	}
	if d := rig.reader.left; d <= 4*time.Second || d > 5*time.Second {
		t.Errorf("the read had %v left when it started, want just under 5 s", d)
	}
}

func TestUploadOfReadsCapturesFile(t *testing.T) {
	ctx := context.Background()
	rig := newUploadsRig(t)

	// No file: nothing captured, and 4CAT is not even asked.
	if st := rig.states.Of(ctx, "alice"); st.Kind != uploadNone {
		t.Errorf("no captures file: %d, want none", st.Kind)
	}
	rig.captured(t, "alice", "", time.Unix(500, 0))
	if st := rig.states.Of(ctx, "alice"); st.Kind != uploadNone {
		t.Errorf("empty captures file: %d, want none", st.Kind)
	}
	if rig.reader.reads != 0 {
		t.Errorf("4CAT was read %d times for someone who captured nothing, want 0", rig.reader.reads)
	}

	// Content, and uploads older than the capture: due since the capture.
	rig.captured(t, "alice", "{}\n", time.Unix(500, 0))
	rig.reader.ups = fourcat.Uploads{"alice": {"a-search": 499, "b-search": 600}}
	if st := rig.states.Of(ctx, "alice"); st.Kind != uploadDue || !st.At.Equal(time.Unix(500, 0)) {
		t.Errorf("captures newer than an upload: {%d %v}, want due at 500", st.Kind, st.At)
	}

	// Another user's uploads never count for alice.
	rig2 := newUploadsRig(t)
	rig2.captured(t, "alice", "{}\n", time.Unix(500, 0))
	rig2.reader.ups = fourcat.Uploads{"bob": {"a-search": 600, "b-search": 600}}
	if st := rig2.states.Of(ctx, "alice"); st.Kind != uploadDue {
		t.Errorf("only bob uploaded: alice is %d, want due", st.Kind)
	}

	// Uploads later than the capture: done at the older of the two. The
	// file's time has a fraction of a second; 4CAT's times are whole
	// seconds, so an upload in the same second counts.
	rig3 := newUploadsRig(t)
	rig3.captured(t, "alice", "{}\n", time.Unix(500, 700_000_000))
	rig3.reader.ups = fourcat.Uploads{"alice": {"a-search": 500, "b-search": 620}}
	if st := rig3.states.Of(ctx, "alice"); st.Kind != uploadDone || !st.At.Equal(time.Unix(500, 0)) {
		t.Errorf("uploads at or after the capture: {%d %v}, want done at 500", st.Kind, st.At)
	}

	// No reader wired (4CAT enabled in the configuration only): unknown.
	var none *uploadStates
	if st := none.Of(ctx, "alice"); st.Kind != uploadUnknown {
		t.Errorf("nil uploadStates: %d, want unknown", st.Kind)
	}
	if newUploadStates(Deps{Cfg: &config.Config{DeskRoot: rig.root}, Platform: testPlatform()}) != nil {
		t.Error("newUploadStates without a reader is not nil")
	}

	// A platform without a Zeeschuimer module has nothing to upload: none,
	// and 4CAT is not asked.
	rig4 := newUploadsRig(t)
	rig4.states.modules = nil
	rig4.captured(t, "alice", "{}\n", time.Unix(500, 0))
	if st := rig4.states.Of(ctx, "alice"); st.Kind != uploadNone || rig4.reader.reads != 0 {
		t.Errorf("no modules: state %d after %d reads, want none without a read", st.Kind, rig4.reader.reads)
	}
}

// The clock of the footer tests stands at Tuesday 17 November 2026, 16:30
// in Amsterdam (15:30 UTC; Amsterdam is one hour ahead in November).
var tueHalfPastFour = time.Date(2026, 11, 17, 15, 30, 0, 0, time.UTC)

func TestWhen(t *testing.T) {
	now := tueHalfPastFour
	for _, c := range []struct {
		name string
		at   time.Time
		want string
	}{
		{"the same day", time.Date(2026, 11, 17, 13, 2, 0, 0, time.UTC), "14:02"},
		{"just after midnight today", time.Date(2026, 11, 16, 23, 1, 0, 0, time.UTC), "00:01"},
		{"yesterday, just before midnight", time.Date(2026, 11, 16, 22, 59, 0, 0, time.UTC), "Mon 23:59"},
		{"three days earlier", time.Date(2026, 11, 14, 13, 2, 0, 0, time.UTC), "Sat 14:02"},
		{"six days earlier", time.Date(2026, 11, 11, 13, 2, 0, 0, time.UTC), "Wed 14:02"},
		{"seven days earlier, the same weekday", time.Date(2026, 11, 10, 13, 2, 0, 0, time.UTC), "10 Nov 14:02"},
		{"twenty days earlier", time.Date(2026, 10, 28, 13, 2, 0, 0, time.UTC), "28 Oct 14:02"},
	} {
		if got := when(c.at, now); got != c.want {
			t.Errorf("%s: when = %q, want %q", c.name, got, c.want)
		}
	}
}

// TestUploadFooterStates: the footer's three texts. The hint is the
// platform's own HTML and goes in as it is.
func TestUploadFooterStates(t *testing.T) {
	const hint = "upload <i>both</i> to 4CAT"
	const plain = "Before you leave: click <b>Z</b> in the browser's toolbar, upload <i>both</i> to 4CAT."
	now := tueHalfPastFour
	at := time.Date(2026, 11, 17, 13, 2, 0, 0, time.UTC) // 14:02 in Amsterdam
	for _, c := range []struct {
		name string
		st   UploadState
		want string
	}{
		{"due", UploadState{Kind: uploadDue, At: at}, "What you captured since 14:02 is not in 4CAT yet. " + plain},
		{"done", UploadState{Kind: uploadDone, At: at}, "Uploaded to 4CAT at 14:02."},
		{"nothing captured", UploadState{Kind: uploadNone}, plain},
		{"unknown", UploadState{Kind: uploadUnknown}, plain},
	} {
		if got := uploadFooter(hint, c.st, now); got != c.want {
			t.Errorf("%s: footer = %q, want %q", c.name, got, c.want)
		}
	}
}

// footerEnv is a ready collector page for alice with 4CAT enabled, the
// modules a and b, the clock at 10:00 in Amsterdam on Tuesday 17 November,
// alice's latest capture at 09:40 and the reader given.
func footerEnv(t *testing.T, reader *fakeUploads) *collectorEnv {
	t.Helper()
	e := newCollectorEnvWith(t, true, func(d *Deps) {
		d.Now = func() time.Time { return tueTenAmsterdam }
		d.Platform.ZeeschuimerModules = []string{"a", "b"}
		d.Uploads = reader
	})
	makeTemplateDir(t, e)
	writeProvisionState(t, e.root, "alice", allDoneSteps())
	e.sess.ready = true
	e.sess.running = true
	path := filepath.Join(e.root, "state", "captures", "alice", "captures.ndjson")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	captured := time.Date(2026, 11, 17, 8, 40, 0, 0, time.UTC) // 09:40 in Amsterdam
	if err := os.Chtimes(path, captured, captured); err != nil {
		t.Fatal(err)
	}
	return e
}

const (
	plainFooter = "Before you leave: click <b>Z</b> in the browser's toolbar, upload them to 4CAT."
	dueFooter   = "What you captured since 09:40 is not in 4CAT yet. " + plainFooter
	doneFooter  = "Uploaded to 4CAT at 09:45."
)

// uploadedAfter is both modules uploaded after alice's capture of 09:40:
// a at 09:45 and b at 09:50 in Amsterdam.
func uploadedAfter() fourcat.Uploads {
	return fourcat.Uploads{"alice": {
		"a-search": time.Date(2026, 11, 17, 8, 45, 0, 0, time.UTC).Unix(),
		"b-search": time.Date(2026, 11, 17, 8, 50, 0, 0, time.UTC).Unix(),
	}}
}

func TestPageFooterWarnsWhenDue(t *testing.T) {
	e := footerEnv(t, &fakeUploads{ups: fourcat.Uploads{}})
	b := e.get(t, "alice", collectorRole, "/desk/").Body.String()
	if want := `<footer id="upload" class="notice warn">` + dueFooter + `</footer>`; !strings.Contains(b, want) {
		t.Errorf("captures newer than the uploads: page lacks %q", want)
	}

	e = footerEnv(t, &fakeUploads{ups: uploadedAfter()})
	b = e.get(t, "alice", collectorRole, "/desk/").Body.String()
	if want := `<footer id="upload" class="notice">` + doneFooter + `</footer>`; !strings.Contains(b, want) {
		t.Errorf("uploads newer than the capture: page lacks %q", want)
	}

	// Without 4CAT there is no footer at all, as before.
	e = readyPageEnv(t, false)
	if b := e.get(t, "alice", collectorRole, "/desk/").Body.String(); strings.Contains(b, "<footer") {
		t.Error("page shows a footer although 4CAT is disabled")
	}
}

// TestCheckInCarriesFooter: both check-in answers carry the footer and
// whether it warns, so it clears within a minute of the uploads.
func TestCheckInCarriesFooter(t *testing.T) {
	reader := &fakeUploads{ups: fourcat.Uploads{}}
	e := footerEnv(t, reader)
	for _, c := range []struct{ name, method, path string }{
		{"status", http.MethodGet, "/desk/status"},
		{"active", http.MethodPost, "/desk/active"},
	} {
		ans := checkInBody(t, doMethod(t, e.srv, c.method, "alice", collectorRole, c.path))
		if ans["footer"] != dueFooter || ans["footer_warn"] != true {
			t.Errorf("%s, due: footer %v warn %v; want %q and true", c.name, ans["footer"], ans["footer_warn"], dueFooter)
		}
	}

	e = footerEnv(t, &fakeUploads{ups: uploadedAfter()})
	for _, c := range []struct{ name, method, path string }{
		{"status", http.MethodGet, "/desk/status"},
		{"active", http.MethodPost, "/desk/active"},
	} {
		ans := checkInBody(t, doMethod(t, e.srv, c.method, "alice", collectorRole, c.path))
		if ans["footer"] != doneFooter || ans["footer_warn"] != false {
			t.Errorf("%s, done: footer %v warn %v; want %q and false", c.name, ans["footer"], ans["footer_warn"], doneFooter)
		}
	}

	// Without 4CAT the answers carry no footer keys.
	e = newCollectorEnv(t, false)
	ans := checkInBody(t, e.get(t, "alice", collectorRole, "/desk/status"))
	if _, has := ans["footer"]; has {
		t.Errorf("status without 4CAT carries a footer: %v", ans)
	}
	if _, has := ans["footer_warn"]; has {
		t.Errorf("status without 4CAT carries footer_warn: %v", ans)
	}
}

// TestCheckInSurvivesUploadFailure: with 4CAT down both check-ins still
// answer 200 with ready and running, the footer is the plain text without a
// warning, and 4CAT is asked once, not once per check-in.
func TestCheckInSurvivesUploadFailure(t *testing.T) {
	reader := &fakeUploads{err: errors.New("psql exited with status 2")}
	e := footerEnv(t, reader)

	ans := checkInBody(t, e.get(t, "alice", collectorRole, "/desk/status"))
	if ans["ready"] != true || ans["footer"] != plainFooter || ans["footer_warn"] != false {
		t.Errorf("status with 4CAT down = %v; want ready, the plain footer, no warning", ans)
	}
	ans = checkInBody(t, e.post(t, "alice", collectorRole, "/desk/active"))
	if ans["running"] != true || ans["footer"] != plainFooter || ans["footer_warn"] != false {
		t.Errorf("active with 4CAT down = %v; want running, the plain footer, no warning", ans)
	}
	if reader.reads != 1 {
		t.Errorf("4CAT was asked %d times by two check-ins within a minute, want 1", reader.reads)
	}
	b := e.get(t, "alice", collectorRole, "/desk/").Body.String()
	if want := `<footer id="upload" class="notice">` + plainFooter + `</footer>`; !strings.Contains(b, want) {
		t.Errorf("page with 4CAT down lacks %q", want)
	}
}

// The check-in puts the answer's footer into the page and switches its
// warning on and off.
func TestPageCheckInUpdatesFooter(t *testing.T) {
	out := deskPage(pageParams{User: "alice", PlatformLabel: "T", Noun: "video", IdleMinutes: 15})
	for _, want := range []string{
		"getElementById('upload')",
		"innerHTML=j.footer",
		"classList.toggle('warn',j.footer_warn===true)",
		"aside > footer.warn {",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("page lacks %q", want)
		}
	}
}
