package web

import (
	"embed"
	"html/template"
	"log"
	"net/http"
)

//go:embed templates
var templatesFS embed.FS

// pageData is the shared template data. Contact, User, Platform and Detail
// are escaped by html/template. Detail is a step's recorded error, shown by
// notready.html; it holds no secret by contract. Title names the tab; Band
// is the band's text, the project's name (bandName), "Comment desk" when
// empty.
type pageData struct {
	Title    string
	Band     string
	Contact  string
	User     string
	Platform string
	Detail   string

	Top    template.HTML // set by render: the head with Title, the header and the band with Band (pageTop)
	Bottom template.HTML // set by render: pageBottom
	Action template.HTML // set by render: the link to the start page (actionLink), for elsewhere.html
}

// pages holds the parsed HTML templates (no JavaScript, plain HTML).
type pages struct {
	t *template.Template
}

func mustLoadPages() *pages {
	t, err := template.ParseFS(templatesFS, "templates/*.html")
	if err != nil {
		panic("web: templates: " + err.Error())
	}
	return &pages{t: t}
}

// render writes the named page template (templates/<name>.html).
func (p *pages) render(w http.ResponseWriter, status int, name string, d pageData) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	if err := p.t.ExecuteTemplate(w, name+".html", chrome(d)); err != nil {
		log.Printf("web: render %s: %v", name, err)
	}
}

// chrome fills in the page's top and bottom: Title in the head, Band on the
// band, and the platform's line under the desk's name. html/template cannot
// call actionLink, so the one link a template page ends in comes from here.
func chrome(d pageData) pageData {
	d.Top = template.HTML(pageTop(d.Title, "", platformLine(d.Platform), nil, bandText(d.Band)))
	d.Bottom = template.HTML(pageBottom)
	d.Action = template.HTML(actionLink("/", "Open the start page"))
	return d
}
