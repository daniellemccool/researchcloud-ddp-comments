package web

import (
	"strings"
	"testing"

	"github.com/daniellemccool/researchcloud-ddp-comments/internal/platform"
)

func TestMessageEscapesTextAndRefresh(t *testing.T) {
	got := message(`a<b> & "q"`, 3)
	if !strings.Contains(got, `<meta http-equiv="refresh" content="3">`) {
		t.Errorf("message lacks the refresh meta:\n%s", got)
	}
	if !strings.Contains(got, `a&lt;b&gt; &amp; &#34;q&#34;`) {
		t.Errorf("message does not escape the text:\n%s", got)
	}
	plain := message("plain", 0)
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
	want := `<option value="">Problem…</option>` +
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
		`<select onchange="problem(this)">` + options + `</select>`,
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
	if !strings.Contains(got, `<span id="prog">1 of 4 done</span>`) {
		t.Errorf("progress lacks the counter:\n%s", got)
	}
	if !strings.Contains(got, `style="width:25%"`) {
		t.Errorf("progress lacks the 25%% bar:\n%s", got)
	}
}

func TestDeskPageSlotsAndOnePass(t *testing.T) {
	hint := "upload them to 4CAT"
	out := deskPage(pageParams{
		User:          "alice",
		PlatformLabel: "TestTube",
		Noun:          "video",
		Seconds:       120,
		NoteCodes:     []string{"OTHER"},
		ProgressHTML:  progress(0, 1),
		Body:          `<p>%%USER%% stays literal</p>`,
		OpenHelp:      true,
		FooterHint:    &hint,
	})
	for _, want := range []string{
		"<title>TestTube desk — alice</title>",
		`<b>Your videos</b>`, // header noun slot
		`<span class="who">alice</span>`,
		`<details open>`,
		", or until the timer reaches 0:00",
		"const SECONDS=120,NOTE_CODES=[\"OTHER\"]",
		`<p>%%USER%% stays literal</p>`, // one pass: substituted text is never rescanned
		"<footer>Before you leave: click <b>Z</b> in the browser's toolbar, upload them to 4CAT.</footer>",
		`fetch('status')`,
		`f.src='/desk/vnc/'`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("page lacks %q", want)
		}
	}
	if strings.Contains(out, "<p>alice stays literal</p>") {
		t.Errorf("the body's %%USER%% was substituted; want one pass only")
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
	if !strings.Contains(out, `<span class="who">boss <a href="/desk/admin/">admin</a></span>`) {
		t.Errorf("admin page lacks the admin link after the user name")
	}
	if strings.Contains(out, "<footer>") {
		t.Errorf("page without a footer hint renders a footer")
	}
	if strings.Contains(out, "TIMER_HELP") || strings.Contains(out, "timer reaches") {
		t.Errorf("page with SECONDS=0 still has the timer help")
	}
}
