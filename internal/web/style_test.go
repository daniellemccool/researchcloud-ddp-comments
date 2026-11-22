package web

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/daniellemccool/researchcloud-ddp-comments/internal/access"
	"github.com/daniellemccool/researchcloud-ddp-comments/internal/config"
)

// The faces and the band's pieces are served to a collector, behind the
// gate; the stylesheets are inlined in the pages, so they are not served.
func TestAssetsServedBehindGate(t *testing.T) {
	srv, _ := newTestServer(t)
	for path, ctype := range map[string]string{
		"fonts/NunitoSans-VariableFont_wght.woff2":        "font/woff2",
		"fonts/LibreCaslonCondensed-latin.woff2":          "font/woff2",
		"fonts/LibreCaslonCondensed-latin-ext.woff2":      "font/woff2",
		"fonts/NunitoSans-Italic-VariableFont_wght.woff2": "font/woff2",
		"zeeschuimer-64.png":                              "image/png",
		"network/consent-badge.svg":                       "image/svg+xml",
		"network/globe.svg":                               "image/svg+xml",
	} {
		rec := do(t, srv, "dmccool2", collectorRole, testGate, "/desk/assets/"+path)
		if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != ctype || rec.Body.Len() == 0 {
			t.Errorf("%s: status %d, type %q, %d bytes", path, rec.Code, rec.Header().Get("Content-Type"), rec.Body.Len())
		}
	}
	for _, path := range []string{"desk.css", "tokens.css", "missing.woff2", "fonts", "fonts/"} {
		if rec := do(t, srv, "dmccool2", collectorRole, testGate, "/desk/assets/"+path); rec.Code != http.StatusNotFound {
			t.Errorf("%s: status %d, want 404", path, rec.Code)
		}
	}
	if rec := do(t, srv, "dmccool2", collectorRole, "wrong", "/desk/assets/zeeschuimer-64.png"); rec.Code != http.StatusForbidden {
		t.Errorf("forged gate: status %d, want 403", rec.Code)
	}
}

// Every face tokens.css names is in the assets the route serves, under the
// URL the inlined stylesheet points at; Nunito, which the desk does not
// use, is the one left out.
func TestPageCSSFontsAreServed(t *testing.T) {
	if strings.Contains(pageCSS, `url("fonts/`) {
		t.Error(`pageCSS keeps a relative url("fonts/…"); it must point at /desk/assets/fonts/`)
	}
	rest := pageCSS
	for {
		i := strings.Index(rest, `url("/desk/assets/fonts/`)
		if i < 0 {
			break
		}
		rest = rest[i+len(`url("/desk/assets/`):]
		name, after, ok := strings.Cut(rest, `"`)
		if !ok {
			t.Fatalf("pageCSS has an unclosed url(\"/desk/assets/%s", rest)
		}
		rest = after
		if _, err := assetsFS.ReadFile("assets/" + name); err != nil && name != "fonts/Nunito.woff2" {
			t.Errorf("pageCSS names %s, which assets/ lacks", name)
		}
	}
}

// testProject is the test desk's co_name, the text of the message pages' band.
const testProject = "OnlineHate"

// Every page carries the header and the inlined stylesheet; the message
// pages carry the band named for the project. A message that needs
// attention is headed by the exclamation disc.
func TestPagesCarryTheLook(t *testing.T) {
	srv, _ := newTestServer(t)
	for _, c := range []struct {
		name, user, roles string
		wants             []string
	}{
		{"403", "someone", "src_co_other", []string{
			"<title>Not registered</title>",
			`<div class="stop"><span class="bang" aria-hidden="true">!</span><p class="lede">You are not registered for the desk; ask ` + testContact + `.</p></div>`,
		}},
		{"not ready", "dmccool2", collectorRole, []string{
			"<title>Your desk</title>",
			`<p class="lede">The desk is not ready yet; ask ` + testContact + `.</p>`,
		}},
	} {
		b := do(t, srv, c.user, c.roles, testGate, "/desk/").Body.String()
		for _, want := range append([]string{
			`<header class="site-header">`, "--font-serif:", `<div class="band">`,
			`<h1 class="band__title">` + testProject + `</h1>`,
		}, c.wants...) {
			if !strings.Contains(b, want) {
				t.Errorf("%s page lacks %q", c.name, want)
			}
		}
	}
}

// The band's text is the project's name from desk.json, and "Comment desk"
// without one; a message page escapes it, and keeps its own title for the
// tab.
func TestBandName(t *testing.T) {
	for _, c := range []struct {
		cfg  *config.Config
		want string
	}{
		{nil, "Comment desk"},
		{&config.Config{}, "Comment desk"},
		{&config.Config{COName: "  "}, "Comment desk"},
		{&config.Config{COName: " OnlineHate "}, "OnlineHate"},
	} {
		if got := bandName(c.cfg); got != c.want {
			t.Errorf("bandName(%+v) = %q, want %q", c.cfg, got, c.want)
		}
	}
	m := message("Your desk", "A<b", "TikTok comment collection", "hello", 0)
	for _, want := range []string{
		`<h1 class="band__title">A&lt;b</h1>`, "<title>Your desk</title>", `<p class="lede">hello</p>`,
		`<span class="site-header__subtitle">TikTok comment collection</span>`,
	} {
		if !strings.Contains(m, want) {
			t.Errorf("message page lacks %q", want)
		}
	}
	if strings.Contains(m, "A<b") {
		t.Error("the band's text is not escaped")
	}
	if empty := message("Your desk", "", "", "hello", 0); !strings.Contains(empty, `<h1 class="band__title">Comment desk</h1>`) {
		t.Error("a message page without a project name lacks the band's fallback")
	}
}

// The three template pages: the band is the project's name, the title is
// the tab's, and the page that leads somewhere ends in an action link.
func TestTemplatePagesUnderTheBand(t *testing.T) {
	p := mustLoadPages()
	render := func(name string, d pageData) string {
		rec := httptest.NewRecorder()
		p.render(rec, http.StatusOK, name, d)
		return rec.Body.String()
	}
	elsewhere := render("elsewhere", pageData{Title: "Open 4CAT from the desk", Band: "On<line"})
	for _, want := range []string{
		"<title>Open 4CAT from the desk</title>",
		`<h1 class="band__title">On&lt;line</h1>`,
		`<p class="lede">This link came from another website or from a mail.</p>`,
		`<p>To keep your account safe, 4CAT on this desk only opens from its own pages. Open the start page and go on from there.</p>`,
		actionLink("/", "Open the start page"),
	} {
		if !strings.Contains(elsewhere, want) {
			t.Errorf("elsewhere page lacks %q", want)
		}
	}
	nr := render("notready", pageData{Title: "Desk", Contact: "the <operator>", Detail: "template: missing"})
	for _, want := range []string{
		`<h1 class="band__title">Comment desk</h1>`, // no project name: the fallback
		`<div class="stop"><span class="bang" aria-hidden="true">!</span><p class="lede">The desk is not ready yet; ask the &lt;operator&gt;.</p></div>`,
		`<p class="muted">template: missing</p>`,
	} {
		if !strings.Contains(nr, want) {
			t.Errorf("not-ready page lacks %q", want)
		}
	}
	if plain := render("notready", pageData{Title: "Desk", Contact: "x"}); strings.Contains(plain, `class="muted"`) {
		t.Error("the not-ready page without a detail has a detail line")
	}
}

// A signed-in member without a standing still gets the look's files, so the
// 403 page shows whole (its faces, the band's pictures), and gets nothing
// else: every other path is the 403 page, as ADR-0004 says.
func TestAssetsOpenToAnySignedInMember(t *testing.T) {
	srv, _ := newTestServer(t)
	const nobody = "src_co_other"
	for path, ctype := range map[string]string{
		"zeeschuimer-64.png":                       "image/png",
		"network/globe.svg":                        "image/svg+xml",
		"network/consent-badge.svg":                "image/svg+xml",
		"fonts/NunitoSans-VariableFont_wght.woff2": "font/woff2",
	} {
		rec := do(t, srv, "someone", nobody, testGate, "/desk/assets/"+path)
		if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != ctype || rec.Body.Len() == 0 {
			t.Errorf("%s without a standing: status %d, type %q, %d bytes; want the file", path, rec.Code, rec.Header().Get("Content-Type"), rec.Body.Len())
		}
	}
	for _, path := range []string{"desk.css", "tokens.css", "missing.woff2", "fonts"} {
		if rec := do(t, srv, "someone", nobody, testGate, "/desk/assets/"+path); rec.Code != http.StatusNotFound {
			t.Errorf("%s without a standing: status %d, want 404", path, rec.Code)
		}
	}
	// Everything else stays the 403 page, whatever the path looks like.
	for _, path := range []string{
		"/desk/", "/desk/status", "/desk/admin/", "/desk/vnc/", "/fourcat/", "/desk/assets", "/desk/assets/", "/desk/assets/fonts/", "/desk/assetsx/zeeschuimer-64.png",
		"/desk/assets/../status", "/desk/assets/../admin/", "/desk/assets/%2e%2e/status", "/desk/assets/fonts/../../status",
		"/desk/assets//zeeschuimer-64.png", "/desk//assets/zeeschuimer-64.png", "/desk/assets/fonts//OFL-NunitoSans.txt",
	} {
		rec := do(t, srv, "someone", nobody, testGate, path)
		if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "You are not registered for the desk") {
			t.Errorf("%s without a standing: status %d, want the 403 page", path, rec.Code)
		}
	}
	// Only a GET: no other method reaches the files.
	for _, method := range []string{http.MethodPost, http.MethodHead, http.MethodPut, http.MethodDelete, http.MethodOptions} {
		req := httptest.NewRequest(method, "/desk/assets/zeeschuimer-64.png", nil)
		access.SetTestHeaders(req, "someone", nobody, testGate)
		rec := httptest.NewRecorder()
		srv.Handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s of a file without a standing: status %d, want 403", method, rec.Code)
		}
	}
	// The decoded path decides, never the raw one: a request whose raw path
	// names a file while its path is another page gets the 403 page.
	odd := httptest.NewRequest(http.MethodGet, "/desk/status", nil)
	odd.URL.RawPath = "/desk/assets/zeeschuimer-64.png"
	access.SetTestHeaders(odd, "someone", nobody, testGate)
	rec := httptest.NewRecorder()
	srv.Handler.ServeHTTP(rec, odd)
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "You are not registered for the desk") {
		t.Errorf("raw path names a file, path is /desk/status: status %d, want the 403 page", rec.Code)
	}
	// The gate secret still comes first (ADR-0003).
	if rec := do(t, srv, "someone", nobody, "wrong", "/desk/assets/zeeschuimer-64.png"); rec.Code != http.StatusForbidden || strings.Contains(rec.Body.String(), "PNG") {
		t.Errorf("forged gate: status %d, want a plain 403", rec.Code)
	}
}

// What /desk/assets/ serves goes to every signed-in member, whatever their
// standing, so the list is pinned: a file added under internal/web/assets
// fails here until it is added on purpose. Only public design material
// belongs in it.
func TestServedAssetsArePinned(t *testing.T) {
	served := []string{
		"fonts/LibreCaslonCondensed-latin-ext.woff2",
		"fonts/LibreCaslonCondensed-latin.woff2",
		"fonts/NunitoSans-Italic-VariableFont_wght.woff2",
		"fonts/NunitoSans-VariableFont_wght.woff2",
		"fonts/OFL-LibreCaslonCondensed.txt",
		"fonts/OFL-NunitoSans.txt",
		"network/consent-badge.svg",
		"network/globe.svg",
		"zeeschuimer-64.png",
	}
	inlined := []string{"desk.css", "tokens.css"} // in every page's <style>; the route refuses them
	var got []string
	err := fs.WalkDir(assetsFS, "assets", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			got = append(got, strings.TrimPrefix(p, "assets/"))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	want := append(append([]string{}, served...), inlined...)
	sort.Strings(got)
	sort.Strings(want)
	if !slices.Equal(got, want) {
		t.Errorf("internal/web/assets holds\n%q\nwant exactly\n%q\n(every file there but the two stylesheets is served to any signed-in member)", got, want)
	}
	srv, _ := newTestServer(t)
	for _, path := range served {
		if rec := do(t, srv, "someone", "src_co_other", testGate, "/desk/assets/"+path); rec.Code != http.StatusOK {
			t.Errorf("%s: status %d, want 200", path, rec.Code)
		}
	}
}

// The band carries its heading as the page's h1 and, in the middle of its
// line, the mark of the infrastructure the suite works with, named for
// assistive technology; the other two pictures are decoration.
func TestBandCarriesTheIntegrationMark(t *testing.T) {
	b := bandHTML("X &amp; Y") // the caller escapes, as pageTop does
	for _, want := range []string{
		`<h1 class="band__title">X &amp; Y</h1>`,
		`class="band__mark"`,
		`<img src="/desk/assets/zeeschuimer-64.png" alt="Works with Zeeschuimer"`,
		`src="/desk/assets/network/consent-badge.svg" alt=""`,
		`src="/desk/assets/network/globe.svg" alt=""`,
	} {
		if !strings.Contains(b, want) {
			t.Errorf("band lacks %q:\n%s", want, b)
		}
	}
}

// The stylesheet keys every state on a hook the page script sets, and the
// parts round 2 built by overriding one another are gone.
func TestStylesheetKnowsTheScriptHooks(t *testing.T) {
	for _, want := range []string{
		".rows li[data-state=done]", ".rows li[data-state=problem]", ".rows li.current",
		".msg.warn", ".timer.up", ".next:disabled", ".halt[hidden]", "#screen",
	} {
		if !strings.Contains(pageCSS, want) {
			t.Errorf("the stylesheet lacks %q", want)
		}
	}
	for _, gone := range []string{".notice", ".late", ".figures", ".sideband"} {
		if strings.Contains(pageCSS, gone) {
			t.Errorf("the stylesheet still names %q", gone)
		}
	}
}

// A link in running text (a name, an item, 4CAT's pages) is set in the
// system's link style, larger and bolder than the text around it, with the
// dotted underline; a link that is a part of its own (an action, a header
// link) is left to that part.
func TestRunningTextLinksTakeTheLinkStyle(t *testing.T) {
	want := `.wrap a:not([class]), .panel a:not([class]) {
  font:var(--text-link); letter-spacing:0; color:inherit; text-decoration:underline dotted var(--sky);`
	if !strings.Contains(pageCSS, want) {
		t.Errorf("the stylesheet lacks the running-text link rule:\n%s", want)
	}
}

// The arrow disc and the link that ends in it: the label and the address
// are escaped.
func TestActionLinkEscapes(t *testing.T) {
	got := actionLink(`/a?b="c"`, "Go <on>")
	want := `<a class="action" href="/a?b=&#34;c&#34;">Go &lt;on&gt;<i class="action__dots"></i>` + arrow("action__disc") + `</a>`
	if got != want {
		t.Errorf("actionLink = %q, want %q", got, want)
	}
	if a := arrow("next__disc"); !strings.HasPrefix(a, `<svg class="disc next__disc" viewBox="-18 -18 36 36" aria-hidden="true">`) {
		t.Errorf("arrow = %q", a)
	}
}

// The header's row is full width on the collector page and in the centred
// column elsewhere; the current link says so to assistive technology.
func TestHeaderLinksAndWidth(t *testing.T) {
	links := []navLink{{Label: "Your <videos>", Href: "/desk/", Current: true}, {Label: "Admin", Href: "/desk/admin/"}}
	wide := headerHTML("sub", "", links, "<li>FOLD</li>", true)
	for _, want := range []string{
		`<div class="site-header__inner site-header__inner--wide">`,
		`<li><a class="site-nav__link is-current" href="/desk/" aria-current="page">Your &lt;videos&gt;</a></li><li>FOLD</li>`,
		`<li><a class="site-nav__link" href="/desk/admin/">Admin</a></li>`,
	} {
		if !strings.Contains(wide, want) {
			t.Errorf("wide header lacks %q:\n%s", want, wide)
		}
	}
	if narrow := headerHTML("sub", "", nil, "", false); !strings.Contains(narrow, `<div class="site-header__inner">`) || strings.Contains(narrow, "<nav") {
		t.Errorf("narrow header without links = %q", narrow)
	}
}

func TestThousands(t *testing.T) {
	for n, want := range map[int64]string{0: "0", 999: "999", 1000: "1,000", 48112: "48,112", 1234567: "1,234,567", -1234: "-1,234"} {
		if got := thousands(n); got != want {
			t.Errorf("thousands(%d) = %q, want %q", n, got, want)
		}
	}
}

// The queue sits on the right of the streamed browser, beside where the
// collector works in it (a platform's comments open on the browser's right).
// The markup keeps the queue first, so it also comes first for the keyboard
// and a screen reader; the stylesheet alone puts it on the right, draws its
// dividing line on the browser's side and turns the next action's arrow to
// point at the browser.
func TestQueueSitsRightOfTheBrowser(t *testing.T) {
	for _, want := range []string{
		`.panes { flex:1; display:flex; flex-direction:row-reverse; min-height:0; }`,
		`.side { flex:none; width:var(--side); display:flex; flex-direction:column; min-height:0; border-left:1px solid var(--border); }`,
		`.next__disc { flex:none; display:block; width:var(--disc-arrow); height:var(--disc-arrow); transform:scaleX(-1); }`,
	} {
		if !strings.Contains(pageCSS, want) {
			t.Errorf("stylesheet lacks %q", want)
		}
	}
	page := deskPage(pageParams{User: "alice", PlatformLabel: "T", Noun: "video"})
	side, screen := strings.Index(page, `<aside class="side">`), strings.Index(page, `<div class="screen-slot">`)
	if side < 0 || screen < 0 || side > screen {
		t.Errorf("the queue does not come before the browser in the markup (side at %d, screen at %d)", side, screen)
	}
	if want := `It opens in the browser on the left.`; !strings.Contains(page, want) {
		t.Errorf("the help lacks %q", want)
	}
	if strings.Contains(page, "on the right") {
		t.Errorf("the help still says the browser is on the right")
	}
}

// The guide's hint and its ring are placed by the script; the stylesheet
// draws them in the look and keeps both out of the way while hidden.
func TestStylesheetDrawsTheGuide(t *testing.T) {
	for _, want := range []string{
		".guide { position:fixed;", ".guide__ring { position:fixed;", "pointer-events:none;",
		// hidden wins over the display the look gives a button
		".guide[hidden], .guide__ring[hidden], .guide [hidden] { display:none; }", ".guide__skip {", ".guide__again {",
	} {
		if !strings.Contains(pageCSS, want) {
			t.Errorf("stylesheet lacks %q", want)
		}
	}
}
