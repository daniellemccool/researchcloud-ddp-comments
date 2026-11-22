package web

import (
	"embed"
	"html"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"strings"
)

// This file is the desk's share of the D3I look (datadonation.eu's design
// system 2): assets/ holds the system's tokens.css, two of its faces with
// their licences and three pieces of its network drawing, all verbatim, and
// the desk's own stylesheet, assets/desk.css. Every page carries the
// stylesheets inline, so it renders even where a request for a stylesheet
// would be refused (the 403 page); the faces and the pieces are served from
// /desk/assets/ behind the gate like every other desk path.

//go:embed assets
var assetsFS embed.FS

// pageCSS is every page's stylesheet: the system's tokens, with their font
// URLs pointed at /desk/assets/fonts/, then desk.css.
var pageCSS = mustPageCSS()

func mustPageCSS() string {
	tokens, err := assetsFS.ReadFile("assets/tokens.css")
	if err != nil {
		panic("web: assets: " + err.Error())
	}
	desk, err := assetsFS.ReadFile("assets/desk.css")
	if err != nil {
		panic("web: assets: " + err.Error())
	}
	t := strings.ReplaceAll(string(tokens), `url("fonts/`, `url("/desk/assets/fonts/`)
	return t + "\n" + string(desk)
}

// serveAsset serves GET /desk/assets/{path...}: the faces, their licences
// and the band's pieces. The stylesheets are inlined in the pages, so they
// are not served; nor is a folder.
func serveAsset(w http.ResponseWriter, r *http.Request) {
	p := r.PathValue("path")
	if strings.HasSuffix(p, ".css") {
		http.NotFound(w, r)
		return
	}
	b, err := fs.ReadFile(assetsFS, "assets/"+p)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	ct := mime.TypeByExtension(path.Ext(p))
	if ct == "" {
		ct = "application/octet-stream"
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Cache-Control", "private, max-age=604800")
	_, _ = w.Write(b)
}

// goMark ends an action: a short dotted line and the arrow disc, as on
// datadonation.eu's "More" links. It goes last inside a button.
const goMark = `<i class="more__dots"></i><svg class="go" width="24" height="24" viewBox="-18 -18 36 36" aria-hidden="true">` +
	`<circle r="18"/><path d="M-8 0 h16 M1.5 -6.5 l6.5 6.5 l-6.5 6.5"/></svg>`

// stepSVG is the S-curve of a band's lower edge: sky above it, the blue
// line along it.
const stepSVG = `<svg viewBox="0 0 100 100" preserveAspectRatio="none">` +
	`<path class="band__step-sky" d="M0 -2 H102 V100 H100 C50 100 50 0 0 0 Z"/>` +
	`<path class="band__edge" d="M-100000 0 H0 C50 0 50 100 100 100 H100000" vector-effect="non-scaling-stroke"/></svg>`

// navLink is one link on the header's right.
type navLink struct {
	Label, Href string
	Current     bool
}

// headerHTML is the header: the desk's name over subtitle (already HTML),
// extra (already HTML, between the name and the links; the collector's
// count) and the links. Labels and hrefs are escaped.
func headerHTML(subtitle, extra string, links []navLink, folds string) string {
	var b strings.Builder
	b.WriteString(`<header class="site-header"><div class="site-header__inner">`)
	b.WriteString(`<a class="site-header__brand" href="/desk/"><span class="site-header__name">Comment desk</span>`)
	if subtitle != "" {
		b.WriteString(`<span class="site-header__subtitle">` + subtitle + `</span>`)
	}
	b.WriteString(`</a>` + extra)
	if len(links) > 0 || folds != "" {
		b.WriteString(`<nav class="site-nav" aria-label="Desk"><ul class="site-nav__list">`)
		for i, l := range links {
			cls, cur := "site-nav__item", ""
			if l.Current {
				cls, cur = "site-nav__item is-active", ` aria-current="page"`
			}
			b.WriteString(`<li class="` + cls + `"><a href="` + html.EscapeString(l.Href) + `"` + cur + `>` + html.EscapeString(l.Label) + `</a></li>`)
			if i == 0 {
				b.WriteString(folds)
			}
		}
		b.WriteString(`</ul></nav>`)
	}
	b.WriteString(`</div></header>`)
	return b.String()
}

// bandHTML is the title band, the front page's band in small: heading
// (already HTML) on a white line between two nodes, Zeeschuimer's mark in a
// white disc, the consent badge and the globe on that line, and the stepped
// lower edge. The drawing is decoration, hidden from assistive technology.
func bandHTML(heading string) string {
	return `<div class="band"><i class="band__deep" aria-hidden="true"></i><div class="band__row">` +
		`<i class="band__lead" aria-hidden="true"></i><div class="band__main"><h1>` + heading + `</h1>` +
		`<div class="band__thread" aria-hidden="true"><i class="band__line band__line--title"></i>` +
		`<svg class="band__drop" viewBox="0 0 100 100" preserveAspectRatio="none"><path d="M0 0 C50 0 50 100 100 100" vector-effect="non-scaling-stroke"/></svg>` +
		`<i class="band__line band__line--deep"></i>` +
		`<span class="band__piece band__piece--zee"><img src="/desk/assets/zeeschuimer-64.png" alt="" width="24" height="24"></span>` +
		`<img class="band__piece band__piece--badge" src="/desk/assets/network/consent-badge.svg" alt="" width="52" height="52">` +
		`<img class="band__piece band__piece--globe" src="/desk/assets/network/globe.svg" alt="" width="40" height="40">` +
		`</div></div></div><div class="band__step" aria-hidden="true">` + stepSVG + `</div></div>`
}

// pageTop opens a reading page (admin, messages): the head with title
// (plain text) and meta (already HTML), the header and the band with
// heading (plain text), then the content column. pageBottom closes it.
func pageTop(title, meta, subtitle string, links []navLink, heading string) string {
	return `<!doctype html><html lang="en"><head><meta charset="utf-8">` +
		`<meta name="viewport" content="width=device-width, initial-scale=1">` + meta +
		`<title>` + html.EscapeString(title) + `</title><style>` + pageCSS + `</style></head>` +
		`<body class="page">` + headerHTML(subtitle, "", links, "") +
		`<main>` + bandHTML(html.EscapeString(heading)) + `<div class="wrap">`
}

// pageBottom closes a page pageTop opened.
const pageBottom = `</div></main></body></html>`

// platformLine is the header's subtitle for a platform's label (plain text):
// "TikTok comment collection".
func platformLine(label string) string {
	if label == "" {
		return "Comment collection"
	}
	return html.EscapeString(label) + " comment collection"
}
