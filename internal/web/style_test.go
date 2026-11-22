package web

import (
	"net/http"
	"strings"
	"testing"
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

// Every page carries the header and the inlined stylesheet; the reading
// pages carry the band with their heading.
func TestPagesCarryTheLook(t *testing.T) {
	srv, _ := newTestServer(t)
	for _, c := range []struct{ name, user, roles, heading string }{
		{"403", "someone", "src_co_other", "Not registered"},
		{"not ready", "dmccool2", collectorRole, "Your desk"},
	} {
		b := do(t, srv, c.user, c.roles, testGate, "/desk/").Body.String()
		for _, want := range []string{`<header class="site-header">`, "--font-serif:", `<div class="band">`, `<h1 class="band__title">` + c.heading + "</h1>"} {
			if !strings.Contains(b, want) {
				t.Errorf("%s page lacks %q", c.name, want)
			}
		}
	}
	m := message("Your desk", "TikTok comment collection", "hello", 0)
	for _, want := range []string{`<header class="site-header">`, `<h1 class="band__title">Your desk</h1>`, `<p class="say">hello</p>`, "TikTok comment collection"} {
		if !strings.Contains(m, want) {
			t.Errorf("message page lacks %q", want)
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
