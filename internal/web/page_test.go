package web

import (
	"strings"
	"testing"

	"github.com/daniellemccool/researchcloud-ddp-comments/internal/platform"
)

func TestMessageEscapesTextAndRefresh(t *testing.T) {
	got := message("Your <desk>", "OnlineHate", "TikTok comment collection", `a<b> & "q"`, 3)
	if !strings.Contains(got, `<meta http-equiv="refresh" content="3">`) {
		t.Errorf("message lacks the refresh meta:\n%s", got)
	}
	if !strings.Contains(got, `<p class="lede">a&lt;b&gt; &amp; &#34;q&#34;</p><p class="muted">This page reloads by itself.</p>`) {
		t.Errorf("message does not escape the text, or does not say it reloads:\n%s", got)
	}
	if !strings.Contains(got, "<title>Your &lt;desk&gt;</title>") {
		t.Errorf("message does not escape its title:\n%s", got)
	}
	plain := message("Your desk", "OnlineHate", "", "plain", 0)
	if strings.Contains(plain, "refresh") {
		t.Errorf("message without refresh contains a refresh meta:\n%s", plain)
	}
	if !strings.Contains(plain, "plain") {
		t.Errorf("message lost its text:\n%s", plain)
	}
}

func TestProblemOptions(t *testing.T) {
	got := problemOptions([]platform.Problem{
		{Code: "NOT_FOUND", Label: "Not found"},
		{Code: "OTHER", Label: "Other", Note: true},
	})
	want := `<option value="">Report a problem…</option>` +
		`<option value="NOT_FOUND">Not found</option>` +
		`<option value="OTHER">Other</option>`
	if got != want {
		t.Errorf("problemOptions = %q, want %q", got, want)
	}
}

func TestRowEscapesAndMarks(t *testing.T) {
	options := problemOptions([]platform.Problem{{Code: "OTHER", Label: "Other", Note: true}})
	got := row("video", "111", "todo", false, "a<b>", "", options, true)
	for _, want := range []string{
		`<li id="r111" data-state="todo" data-kind="video" data-id="111">`,
		`onclick="open_(this.closest('li'));return false">a&lt;b&gt;</a>`,
		`<span class="timer"></span>`,
		`<select class="pick" aria-label="Report a problem with a&lt;b&gt;" onchange="problem(this)">` + options + `</select>`,
		`<div class="msg"></div>`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("row lacks %q:\n%s", want, got)
		}
	}
	done := row("video", "111", "done", true, "t", "— Other: note", options, false)
	if !strings.Contains(done, `<li id="r111" class="done" data-state="done"`) {
		t.Errorf("opened row lacks class=\"done\":\n%s", done)
	}
	if !strings.Contains(done, `<div class="msg warn">— Other: note</div>`) {
		t.Errorf("row with a message lacks the warn class:\n%s", done)
	}
	if strings.Contains(done, "timer") {
		t.Errorf("row without timer contains a timer span:\n%s", done)
	}
}

func TestProgress(t *testing.T) {
	got := progress(1, 4)
	if !strings.Contains(got, `<span id="prog" class="count__done">1 of 4 done</span><div class="count__bar"><i id="barfill" class="count__fill" style="width:25%"></i></div>`) {
		t.Errorf("progress lacks the counter:\n%s", got)
	}
	if !strings.Contains(got, `style="width:25%"`) {
		t.Errorf("progress lacks the 25%% bar:\n%s", got)
	}
}

func TestDeskPageSlotsAndOnePass(t *testing.T) {
	footer := "When you're done for the day: click <b>Z</b> in the browser's toolbar, upload them to 4CAT."
	out := deskPage(pageParams{
		User:          "alice",
		PlatformLabel: "TestTube",
		Noun:          "video",
		Seconds:       120,
		NoteCodes:     []string{"OTHER"},
		ProgressHTML:  progress(0, 1),
		Body:          `<p>%%USER%% stays literal</p>`,
		Footer:        &footer,
	})
	for _, want := range []string{
		"<title>TestTube desk — alice</title>",
		`<h1 class="side__title">Your queue</h1>`,
		`<span class="site-header__subtitle">TestTube comment collection · alice</span>`,
		`<details class="fold" name="fold"><summary class="site-nav__link">How to collect</summary>`,
		", or until the timer reaches 0:00",
		"const SECONDS=120,NOTE_CODES=[\"OTHER\"]",
		`<p>%%USER%% stays literal</p>`, // one pass: substituted text is never rescanned
		`<footer id="upload" class="upload">When you're done for the day: click <b>Z</b> in the browser's toolbar, upload them to 4CAT.</footer>`,
		`fetch('status')`,
		`f.src='/desk/vnc/?resize=remote'`, // the remote browser follows the frame's size
	} {
		if !strings.Contains(out, want) {
			t.Errorf("page lacks %q", want)
		}
	}
	if strings.Contains(out, `f.src='/desk/vnc/'`) {
		t.Errorf("the screen frame loads without resize=remote")
	}
	if strings.Contains(out, "<p>alice stays literal</p>") {
		t.Errorf("the body's %%USER%% was substituted; want one pass only")
	}
}

func TestDeskPageNilNoteCodesRendersEmptyArray(t *testing.T) {
	out := deskPage(pageParams{
		User:          "alice",
		PlatformLabel: "TestTube",
		Noun:          "video",
		Seconds:       60,
		NoteCodes:     nil,
		Body:          "",
	})
	if !strings.Contains(out, "NOTE_CODES=[]") {
		t.Errorf("page lacks NOTE_CODES=[]:\n%s", out)
	}
	if strings.Contains(out, "NOTE_CODES=null") {
		t.Errorf("page renders NOTE_CODES=null for a nil NoteCodes:\n%s", out)
	}
}

func TestDeskPageAdminLinkAndNoFooter(t *testing.T) {
	out := deskPage(pageParams{
		User:          "boss",
		Admin:         true,
		PlatformLabel: "TestTube",
		Noun:          "video",
		Seconds:       0,
		NoteCodes:     nil,
		Body:          "",
	})
	if !strings.Contains(out, `<li><a class="site-nav__link" href="/desk/admin/">Admin</a></li>`) {
		t.Errorf("admin page lacks the Admin link in the header")
	}
	if strings.Contains(out, "<footer") {
		t.Errorf("page without a footer renders one")
	}
	if strings.Contains(out, "TIMER_HELP") || strings.Contains(out, "timer reaches") {
		t.Errorf("page with SECONDS=0 still has the timer help")
	}
}

func TestPageHeartbeatScript(t *testing.T) {
	out := deskPage(pageParams{User: "alice", PlatformLabel: "T", Noun: "video", IdleMinutes: 15})
	for _, want := range []string{
		`fetch('active',{method:'POST',redirect:'manual'})`,
		"'pointerdown'", "'pointermove'", "'keydown'", "'wheel'", "'touchstart'",
		`id="stopped" hidden`,
		`<p class="lede">Your desk was stopped after 15 minutes without activity.</p>` + actionLink("/desk/", "Reload to continue"),
		"setInterval(",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("page lacks %q", want)
		}
	}
}

// TestPageOpenStartingMessage: a 503 from open shows the retry text in the
// row's message and does nothing else (the row is not marked done); other
// failures keep the operator message.
func TestPageOpenStartingMessage(t *testing.T) {
	out := deskPage(pageParams{User: "alice", PlatformLabel: "T", Noun: "video", IdleMinutes: 15})
	i := strings.Index(out, "async function open_(l)")
	if i < 0 {
		t.Fatal("page has no open_ function")
	}
	fn, _, found := strings.Cut(out[i:], "\nfunction openNext")
	if !found {
		t.Fatal("page has no openNext after open_")
	}
	branch := `else if(r.status===503){setMsg(l,'the browser is still starting; try again in a moment',true);}`
	if !strings.Contains(fn, branch) {
		t.Errorf("open_ lacks the 503 branch %q:\n%s", branch, fn)
	}
	if !strings.Contains(fn, "could not open; tell the operator") {
		t.Errorf("open_ lost the operator message for other failures:\n%s", fn)
	}
}

// TestPageStoppedBannerNeedsAStoppedPair: on a check-in without input the
// stopped banner follows the pair (running strictly false), not a screen
// that is briefly not ready; the input branch is unchanged.
func TestPageStoppedBannerNeedsAStoppedPair(t *testing.T) {
	out := deskPage(pageParams{User: "alice", PlatformLabel: "T", Noun: "video", IdleMinutes: 15})
	for _, want := range []string{
		"if(hadInput){if(!j.running)showStopped();}",
		"else if(j.ready)wasReady=true;else if(wasReady&&j.running===false)showStopped();",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("check-in lacks %q", want)
		}
	}
	if strings.Contains(out, "else if(wasReady)showStopped();") {
		t.Error("the no-input check-in still shows the banner on ready alone")
	}
}

func TestPageReattachesListenersOnIframeLoad(t *testing.T) {
	out := deskPage(pageParams{User: "alice", PlatformLabel: "T", Noun: "video", IdleMinutes: 15})
	i := strings.Index(out, `.addEventListener('load'`)
	if i < 0 {
		t.Fatal("page has no load handler on the iframe")
	}
	if !strings.Contains(out[i:], "contentDocument") {
		t.Error("the load handler does not attach the listeners to the iframe's contentDocument")
	}
}

// A refused check-in (401, 403, or a redirect to the SRC login, which needs
// redirect:'manual' to be seen at all) shows the denied banner; the contact
// in it is escaped.
func TestPageDeniedBanner(t *testing.T) {
	out := deskPage(pageParams{User: "alice", PlatformLabel: "T", Noun: "video", IdleMinutes: 15, Contact: `help <desk> & "co"`})
	for _, want := range []string{
		`id="denied" hidden`,
		`<p class="lede">You are signed out, or your access to the desk was removed.</p><p>If reloading keeps failing, contact help &lt;desk&gt; &amp; &#34;co&#34;.</p>` + actionLink("/desk/", "Reload this page"),
		`fetch('active',{method:'POST',redirect:'manual'})`,
		`fetch('status',{redirect:'manual'})`,
		"r.status===401", "r.status===403", "r.type==='opaqueredirect'",
		"function showDenied()",
		".halt[hidden] { display:none; }",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("page lacks %q", want)
		}
	}
	if strings.Contains(out, "<desk>") {
		t.Error("the contact is not escaped in the denied banner")
	}
}

// Input seen before a check-in that fails for a moment (a non-ok answer or a
// thrown fetch) is reported at the next check-in instead of being dropped.
func TestPageKeepsInputAfterFailedCheckIn(t *testing.T) {
	out := deskPage(pageParams{User: "alice", PlatformLabel: "T", Noun: "video", IdleMinutes: 15})
	i := strings.Index(out, "setInterval(")
	if i < 0 {
		t.Fatal("page has no check-in interval")
	}
	tick := out[i:]
	if n := strings.Count(tick, "if(hadInput)sawInput=true;"); n != 2 {
		t.Errorf("input is restored in %d places of the check-in, want 2 (non-ok answer and thrown fetch)", n)
	}
}

// The script finds the page's parts by these ids and by ul.rows; the look
// may change around them, they may not.
func TestDeskPageKeepsTheScriptHooks(t *testing.T) {
	footer := "f"
	options := problemOptions(nil)
	out := deskPage(pageParams{
		User: "alice", PlatformLabel: "T", Noun: "video", Seconds: 60, IdleMinutes: 15,
		ProgressHTML: progress(1, 2),
		Next:         nextAction("video", false),
		Body:         `<ul class="rows">` + row("video", "1", "done", true, "Video 1", "", options, true) + row("video", "2", "todo", false, "Video 2", "", options, true) + `</ul>`,
		Footer:       &footer,
	})
	for _, want := range []string{
		`id="prog"`, `id="barfill"`, `id="next"`, `id="worked"`, `id="upload"`,
		`id="stopped" hidden`, `id="denied" hidden`, `id="screen"`, `id="starting"`,
		`<ul class="rows">`,
		`<footer id="upload" class="upload">f</footer>`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("page lacks %q", want)
		}
	}
	// A due upload: the class the script toggles is on the footer from the start.
	due := deskPage(pageParams{User: "alice", PlatformLabel: "T", Noun: "video", Footer: &footer, FooterWarn: true})
	if want := `<footer id="upload" class="upload warn">f</footer>`; !strings.Contains(due, want) {
		t.Errorf("page with a due upload lacks %q", want)
	}
}

// Round 3 changes one string of the script, the text when the time is up.
// The heartbeat of ADR-0024 is as it was.
func TestPageScriptChangesOnlyTimesUp(t *testing.T) {
	for _, want := range []string{
		`t.textContent='Time’s up. Open the next one.'`,
		`const INPUT_EVENTS=['pointerdown','pointermove','keydown','wheel','touchstart'];`,
		`fetch('active',{method:'POST',redirect:'manual'})`,
	} {
		if !strings.Contains(PAGE, want) {
			t.Errorf("PAGE lacks %q", want)
		}
	}
	if strings.Contains(PAGE, "next one'") && strings.Contains(PAGE, "time’s up —") {
		t.Error("PAGE keeps the old time's-up text")
	}
}

// The problem control of a row says which item it is about, since only the
// open row shows it beside its title.
func TestRowNamesItsProblemControl(t *testing.T) {
	opts := problemOptions(nil)
	if !strings.HasPrefix(opts, `<option value="">Report a problem…</option>`) {
		t.Errorf("problemOptions = %q", opts)
	}
	got := row("video", "1", "todo", false, "Video 4", "", opts, true)
	if want := `<select class="pick" aria-label="Report a problem with Video 4" onchange="problem(this)">`; !strings.Contains(got, want) {
		t.Errorf("row lacks %q:\n%s", want, got)
	}
}

// Every label that names the item follows the platform's noun.
func TestDeskPageFollowsTheNoun(t *testing.T) {
	out := deskPage(pageParams{User: "alice", PlatformLabel: "T", Noun: "post", Seconds: 60, Next: nextAction("post", false)})
	for _, want := range []string{
		`<h1 class="side__title">Your queue</h1>`, // the page's name is the same on every platform
		`<a class="site-nav__link is-current" href="/desk/" aria-current="page">Your queue</a>`,
		`<span class="next__label">Open next post</span>`,
		`Click <b>Open next post</b>, or a post in the list.`,
		`If the post won't show or the comments won't load, pick what happened under <b>Report a problem</b>.`,
		`When every post is done, get more.`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("page lacks %q", want)
		}
	}
	// The stylesheet's comments are not the page's words: look from <body> on.
	if _, body, ok := strings.Cut(out, "<body"); !ok || strings.Contains(body, "video") {
		t.Error("a page for posts still says video")
	}
}

// A collector with nothing claimed: no list and no next action, and the
// queue's end still offers more.
func TestDeskPageWithNothingClaimed(t *testing.T) {
	out := deskPage(pageParams{
		User: "alice", PlatformLabel: "T", Noun: "video", Guide: true,
		Body: `<div class="queue__end"><div class="queue__more" id="getmore"><button class="cmd cmd--sm cmd--main" onclick="more(5)">Get 5 more videos</button></div></div>`,
	})
	for _, gone := range []string{`id="next"`, `<ul class="rows">`, `id="prog"`} {
		if strings.Contains(out, gone) {
			t.Errorf("page with nothing claimed has %q", gone)
		}
	}
	for _, want := range []string{`id="getmore"`, `<div class="side__head"><h1 class="side__title">Your queue</h1></div>`, `<div class="count"><span class="count__time"><span id="worked">`} {
		if !strings.Contains(out, want) {
			t.Errorf("page with nothing claimed lacks %q", want)
		}
	}
}

// The next action is the arrow disc and its label; with nothing left to
// open it is disabled.
func TestNextAction(t *testing.T) {
	if got, want := nextAction("video", false), `<button id="next" class="next" onclick="openNext()">`+arrow("next__disc")+`<span class="next__label">Open next video</span></button>`; got != want {
		t.Errorf("nextAction = %q, want %q", got, want)
	}
	if got := nextAction("video", true); !strings.HasPrefix(got, `<button id="next" class="next" onclick="openNext()" disabled>`) {
		t.Errorf("disabled nextAction = %q", got)
	}
}

// The page never reloads itself: after the last item and after getting more
// the script fetches the queue and redraws the sidebar, so the streamed
// browser stays connected and the open item keeps its timer.
func TestDeskPageRedrawsTheQueueInPlace(t *testing.T) {
	out := deskPage(pageParams{User: "alice", PlatformLabel: "T", Noun: "video", Seconds: 120})
	if strings.Contains(out, "location.reload") {
		t.Errorf("the page's script reloads the page")
	}
	for _, want := range []string{
		`fetch('queue',{redirect:'manual'})`,
		// getting more also closes the help, which a reload used to do: it
		// is open only for a collector with nothing claimed yet
		`async function more(n){try{await fetch('claim?n='+n,{method:'POST'});}catch(e){}` +
			`document.querySelectorAll('details.fold[open]').forEach(d=>{d.open=false;});redraw();}`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("page lacks %q", want)
		}
	}
}

// The guide walks along with a collector's first item: one hint at a time,
// each beside the thing to use next, moving on when the collector does it.
// It starts by itself for a collector who has never opened an item, and How
// to collect can start it again for anyone. It replaces the help that used to
// open by itself, which would now cover the queue.
func TestDeskPageCarriesTheGuide(t *testing.T) {
	first := deskPage(pageParams{User: "alice", PlatformLabel: "T", Noun: "clip", Guide: true})
	later := deskPage(pageParams{User: "alice", PlatformLabel: "T", Noun: "clip"})
	if !strings.Contains(first, `const G={"auto":true,`) {
		t.Errorf("a first-time page does not start the guide")
	}
	if !strings.Contains(later, `const G={"auto":false,`) {
		t.Errorf("a later page starts the guide by itself")
	}
	for _, want := range []string{
		`<div id="guide" class="guide" hidden>`,
		`<button id="guide-next" class="cmd cmd--sm cmd--main"></button>`,
		`<button id="guide-skip" class="guide__skip">Skip the guide</button>`,
		`<i id="guide-ring" class="guide__ring" hidden></i>`,
		// the hints follow the platform's noun
		`"more":"Start here: get your first clips."`,
		`"next":"Open the next clip here. It opens in the browser on the left."`,
		// without a hint from the platform, the plain instruction
		`"screen":"Open the comments in the browser. Then scroll the comments until no more load. Don't open replies."`,
		`"problem":"If the clip won't show or the comments won't load, pick what happened here."`,
		`"upload":"When you're done for the day, send what you captured to 4CAT:`,
		// How to collect starts it again
		`<button class="guide__again" onclick="guideStart(true)">Show me around</button>`,
		// the guide follows what the collector does
		`guideOn('drawn')`, `guideOn('opened')`,
	} {
		for name, page := range map[string]string{"first-time": first, "later": later} {
			if !strings.Contains(page, want) {
				t.Errorf("%s page lacks %q", name, want)
			}
		}
	}
	if strings.Contains(first, `name="fold" open>`) {
		t.Errorf("a fold opens by itself on a first-time page")
	}
}

// Where a platform's comments open is the platform's to say: its
// comments_hint (trusted HTML, as upload_hint is) leads the hint at the
// browser's pane. The page draws no picture of a button to look for.
func TestGuideUsesThePlatformsCommentsHint(t *testing.T) {
	out := deskPage(pageParams{User: "alice", PlatformLabel: "T", Noun: "clip",
		CommentsHint: "Click <b>Comments</b>, next to <b>You may like</b>."})
	want := `"screen":"Click \u003cb\u003eComments\u003c/b\u003e, next to \u003cb\u003eYou may like\u003c/b\u003e. Then scroll the comments until no more load. Don't open replies."`
	if !strings.Contains(out, want) {
		t.Errorf("page lacks %q", want)
	}
	for _, gone := range []string{"speech-bubble", "guide__icon"} {
		if strings.Contains(out, gone) {
			t.Errorf("page still has %q", gone)
		}
	}
}

// The guide rings the Zeeschuimer button where it sits in the streamed
// browser's toolbar: its centre is 97 px in from the pane's right edge and
// 64 px down, measured on the study desk under v1.4.0 at two window widths
// (2026-11-22). v1.4.0 had it 85 px in, 12 px off to the right.
func TestGuideRingsTheZeeschuimerButton(t *testing.T) {
	out := deskPage(pageParams{User: "alice", PlatformLabel: "T", Noun: "video"})
	if want := `if(guide.step==='upload')r={left:r.right-113,top:r.top+48,width:32,height:32};`; !strings.Contains(out, want) {
		t.Errorf("page lacks %q", want)
	}
}
