package web

import (
	"errors"
	"fmt"
	"html"
	"io"
	"log"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/daniellemccool/researchcloud-ddp-comments/internal/desklog"
	"github.com/daniellemccool/researchcloud-ddp-comments/internal/pool"
)

// maxUploadBytes is the pool upload limit.
const maxUploadBytes = 5 * 1024 * 1024

// adminCSS is the admin page's stylesheet, kept verbatim from the prototype.
const adminCSS = "body{font:14px/1.4 system-ui,sans-serif;margin:2em;max-width:70em;color:#1d2329}" +
	"h2{margin:1.6em 0 .4em;border-bottom:1px solid #e3e6ea}table{border-collapse:collapse}" +
	"td,th{padding:4px 8px;border-bottom:1px solid #e3e6ea;text-align:left;vertical-align:top}" +
	".notice{background:#fff8e6;border:1px solid #e8d9a8;padding:8px 12px;border-radius:6px}" +
	".warn{color:#b35900}form{display:inline}td form button{margin-right:4px}"

// uploadForm is the pool upload form of the admin overview, kept verbatim from the prototype.
const uploadForm = `<form method="post" action="/desk/admin/pool" enctype="multipart/form-data">` +
	`<label>Add to the pool (CSV with an id column; kind, bucket, note optional): ` +
	`<input type="file" name="file" accept=".csv,text/csv" required></label> <button>Upload</button></form>`

// adminShell wraps already-built HTML inner in an admin page; notice is plain text, shown in a box and escaped.
func adminShell(title, inner, notice string, back bool) string {
	box := ""
	if notice != "" {
		box = `<p class="notice" role="status">` + html.EscapeString(notice) + `</p>`
	}
	link := ""
	if back {
		link = `<p><a href="/desk/admin/">back to the overview</a></p>`
	}
	return `<!doctype html><html lang="en"><head><meta charset="utf-8"><title>` + html.EscapeString(title) + `</title>` +
		`<style>` + adminCSS + `</style></head><body><h1>` + html.EscapeString(title) + `</h1>` + box + inner + link + `</body></html>`
}

// setAdminHeaders sets the framing and no-store headers every admin answer
// carries (framing and no-store), including the non-admin
// 403s and the /desk/admin → /desk/admin/ redirect.
func setAdminHeaders(w http.ResponseWriter) {
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Content-Security-Policy", "frame-ancestors 'none'")
	w.Header().Set("Cache-Control", "no-store")
}

// adminHandler serves GET /desk/admin/{$} and POST /desk/admin/pool
// (plan-2a part only). It is mounted
// admin-only by the gated listener; every response carries the framing and
// no-store headers.
type adminHandler struct {
	poolPath string
	kinds    *platformKinds
	log      *desklog.Log

	addMu   sync.Mutex // held around pool.Add, whose read-modify-write contract requires it (F75)
	mu      sync.Mutex
	notices map[string]string // one-shot notice per user
}

// NewAdmin returns the admin page handler over the gated listener's Deps.
func NewAdmin(d Deps) http.Handler {
	return &adminHandler{
		poolPath: filepath.Join(d.Cfg.DeskRoot, "pool.csv"),
		kinds:    newPlatformKinds(d.Platform),
		log:      d.Log,
		notices:  make(map[string]string),
	}
}

// ServeHTTP routes the two admin routes and sets the security headers on
// every response.
func (a *adminHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	setAdminHeaders(w)
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/desk/admin/":
		a.serveOverview(w, r)
	case r.Method == http.MethodPost && r.URL.Path == "/desk/admin/pool":
		a.servePoolUpload(w, r)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

// serveOverview is GET /desk/admin/{$}: the queue line, the upload form and
// the plan-2b note, with the one-shot notice taken for this user.
func (a *adminHandler) serveOverview(w http.ResponseWriter, r *http.Request) {
	id := identity(r)
	body := a.overviewBody(a.takeNotice(id.User))
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(body))
}

// overviewBody renders the admin page with the given notice (not stored).
func (a *adminHandler) overviewBody(notice string) string {
	inner := `<h2>Queue</h2><p>` + a.poolLine() + `</p>` + uploadForm +
		`<p>Sessions, 4CAT, export and problems arrive in plan 2b.</p>`
	return adminShell("Desk admin", inner, notice, false)
}

// poolLine is the Queue line (the overview's pool line):
// the unreadable variant, else "<free> of <total> <noun>s unclaimed in the
// pool." over the pool rows this platform can open, free = not in the log's
// taken set. The line is already-escaped text.
func (a *adminHandler) poolLine() string {
	rows, err := pool.Read(a.poolPath, a.kinds.Default())
	if err != nil {
		return "pool.csv unreadable: " + html.EscapeString(err.Error())
	}
	taken := map[desklog.Key]bool{}
	if a.log != nil {
		evs, err := a.log.Read()
		if err != nil {
			log.Printf("web: admin: desk log: %v", err)
		} else {
			taken = a.log.Taken(evs)
		}
	}
	free, total := 0, 0
	for _, row := range rows {
		re, ok := a.kinds.Kind(row.Kind)
		if !ok || !re.MatchString(row.ID) {
			continue // desk.pool(): only rows this platform can open
		}
		total++
		if !taken[desklog.Key{Kind: row.Kind, ID: row.ID}] {
			free++
		}
	}
	return fmt.Sprintf("%d of %d %ss unclaimed in the pool.", free, total, html.EscapeString(a.kinds.Default()))
}

// servePoolUpload is POST /desk/admin/pool : CSRF, size limits, the
// multipart file field, then ParseSelection and Add with a one-shot notice.
func (a *adminHandler) servePoolUpload(w http.ResponseWriter, r *http.Request) {
	id := identity(r)
	if !csrfOK(r) {
		a.say(w, http.StatusForbidden, "Refused: this request did not come from the admin page.")
		return
	}
	raw := r.Header.Get("Content-Length")
	if raw == "" {
		a.say(w, http.StatusLengthRequired, "Content-Length required")
		return
	}
	n, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil {
		a.say(w, http.StatusLengthRequired, "Content-Length required")
		return
	}
	if n < 0 {
		a.say(w, http.StatusBadRequest, "bad Content-Length")
		return
	}
	if n > maxUploadBytes {
		a.say(w, http.StatusRequestEntityTooLarge, "upload larger than 5 MB")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxUploadBytes)

	ctype := r.Header.Get("Content-Type")
	if !strings.HasPrefix(strings.ToLower(ctype), "multipart/form-data") {
		a.say(w, http.StatusBadRequest, "expected a multipart form")
		return
	}
	data, ok := readUpload(r)
	if !ok {
		a.say(w, http.StatusBadRequest, "no file field in the form")
		return
	}

	text := decodeReplace(string(data))
	rows, rejects, err := pool.ParseSelection(text, a.kinds)
	var selErr *pool.SelectionError
	switch {
	case errors.As(err, &selErr):
		a.setNotice(id.User, fmt.Sprintf("nothing added: line %d: %s", selErr.Line, selErr.Reason))
		a.redirect(w)
		return
	case err != nil:
		log.Printf("web: admin: parse upload: %v", err)
		a.say(w, http.StatusInternalServerError, "could not read the upload")
		return
	}
	a.addMu.Lock()
	added, _, err := pool.Add(a.poolPath, rows, a.kinds)
	a.addMu.Unlock()
	if err != nil {
		body := a.overviewBody("could not add to the pool: " + err.Error())
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(body))
		return
	}
	a.setNotice(id.User, fmt.Sprintf("added %d of %d; %s", added, len(rows)+len(rejects), rejectText(rejects)))
	a.redirect(w)
}

// decodeReplace decodes s as UTF-8, replacing each invalid byte with one
// U+FFFD, like Python's data.decode("utf-8", errors="replace") (Go's
// strings.ToValidUTF8 would collapse a run of invalid bytes into one).
func decodeReplace(s string) string {
	if utf8.ValidString(s) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for len(s) > 0 {
		r, size := utf8.DecodeRuneInString(s)
		if r == utf8.RuneError && size == 1 {
			b.WriteRune('�')
		} else {
			b.WriteString(s[:size])
		}
		s = s[size:]
	}
	return b.String()
}

// csrfOK is the CSRF check: a Sec-Fetch-Site header must say
// same-origin; without it, Origin's scheme must be http or https and its
// host part must equal Host.
func csrfOK(r *http.Request) bool {
	if vals := r.Header.Values("Sec-Fetch-Site"); len(vals) > 0 {
		return vals[0] == "same-origin"
	}
	origin := r.Header.Get("Origin")
	host := r.Host
	if host == "" {
		host = r.Header.Get("Host")
	}
	if origin == "" || host == "" {
		return false
	}
	parts := strings.SplitN(origin, "://", 2)
	return parts[len(parts)-1] == host && (parts[0] == "http" || parts[0] == "https")
}

// readUpload returns the bytes of the multipart form's first `file` part,
// or false when there is none.
func readUpload(r *http.Request) ([]byte, bool) {
	mr, err := r.MultipartReader()
	if err != nil {
		return nil, false
	}
	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			return nil, false
		}
		if err != nil {
			return nil, false
		}
		if part.FormName() != "file" {
			continue
		}
		data, err := io.ReadAll(part)
		_ = part.Close()
		if err != nil {
			return nil, false
		}
		return data, true
	}
}

// rejectText renders the reject summary: "no rejects", else the
// first 20 "<line>: <why>" pairs plus " (and <k> more)".
func rejectText(rejects []pool.Reject) string {
	if len(rejects) == 0 {
		return "no rejects"
	}
	shown := make([]string, 0, 20)
	for i, rj := range rejects {
		if i >= 20 {
			break
		}
		shown = append(shown, fmt.Sprintf("%d: %s", rj.Line, rj.Reason))
	}
	more := ""
	if len(rejects) > 20 {
		more = fmt.Sprintf(" (and %d more)", len(rejects)-20)
	}
	return "rejected lines: " + strings.Join(shown, ", ") + more
}

// say writes a one-line message page (message()) with the status.
func (a *adminHandler) say(w http.ResponseWriter, status int, text string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(message(text, 0)))
}

// redirect answers 303 to the overview (a refresh repeats the GET, not the
// upload).
func (a *adminHandler) redirect(w http.ResponseWriter) {
	w.Header().Set("Location", "/desk/admin/")
	w.WriteHeader(http.StatusSeeOther)
}

// setNotice stores the one-shot notice for user.
func (a *adminHandler) setNotice(user, text string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.notices[user] = text
}

// takeNotice returns and clears user's notice.
func (a *adminHandler) takeNotice(user string) string {
	a.mu.Lock()
	defer a.mu.Unlock()
	text := a.notices[user]
	delete(a.notices, user)
	return text
}
