package web

import (
	"embed"
	"html/template"
	"net/http"
)

//go:embed templates
var templatesFS embed.FS

// pageData is the shared template data. Contact, User and Platform are
// escaped by html/template.
type pageData struct {
	Title    string
	Contact  string
	User     string
	Platform string
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
	_ = p.t.ExecuteTemplate(w, name+".html", d)
}
