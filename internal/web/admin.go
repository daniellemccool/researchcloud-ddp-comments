package web

import (
	"context"
	"errors"
	"fmt"
	"html"
	"io"
	"log"
	"net/http"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/daniellemccool/researchcloud-ddp-comments/internal/access"
	"github.com/daniellemccool/researchcloud-ddp-comments/internal/config"
	"github.com/daniellemccool/researchcloud-ddp-comments/internal/desklog"
	"github.com/daniellemccool/researchcloud-ddp-comments/internal/platform"
	"github.com/daniellemccool/researchcloud-ddp-comments/internal/pool"
	"github.com/daniellemccool/researchcloud-ddp-comments/internal/registry"
	"github.com/daniellemccool/researchcloud-ddp-comments/internal/session"
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

// adminStatusLimit is how long the admin page waits for the session status
// (a Docker container list). Docker that does not answer in time is a
// failed status: the seats line says the running count is unavailable and
// the rest of the page renders.
const adminStatusLimit = 5 * time.Second

// adminHandler serves GET /desk/admin/{$}, POST /desk/admin/pool and POST
// /desk/admin/stop. It is mounted admin-only by the gated listener; every
// response carries the framing and no-store headers.
type adminHandler struct {
	cfg      *config.Config
	platform *platform.Platform
	poolPath string
	pool     *pool.Cache // nil → pool.csv read per view (poolSnapshot)
	kinds    *platformKinds
	log      *desklog.Log
	// members is the CO poll: its status line and its last good member
	// list; nil → no poll line and no member lists.
	members interface {
		StatusLine() string
		Members() (map[string][]string, bool)
	}
	roles    access.Config // the role names, for the member lists
	sessions adminSessions // nil → no session data
	activity *Activity     // nil → no "input N min ago"
	uploads  *uploadStates // nil → every upload state is unknown
	// statusLimit is how long the page waits for the session status
	// (adminStatusLimit; shortened by the tests).
	statusLimit time.Duration
	seats       SeatFigures
	now         func() time.Time
	records     recordCounter

	addMu   sync.Mutex // held around pool.Add, whose read-modify-write contract requires it (F75)
	mu      sync.Mutex
	notices map[string]string // one-shot notice per user
}

// NewAdmin returns the admin page handler over the gated listener's Deps.
func NewAdmin(d Deps) http.Handler {
	a := &adminHandler{
		cfg:      d.Cfg,
		platform: d.Platform,
		poolPath: filepath.Join(d.Cfg.DeskRoot, "pool.csv"),
		pool:     d.Pool,
		kinds:    newPlatformKinds(d.Platform),
		log:      d.Log,
		sessions: d.AdminSessions,
		members:  d.Members,
		roles:    d.Access,
		activity: d.Activity,
		uploads:  newUploadStates(d),
		seats:    d.Seats,
		now:      d.Now,
		notices:  make(map[string]string),

		statusLimit: adminStatusLimit,
	}
	if a.now == nil {
		a.now = time.Now
	}
	return a
}

// ServeHTTP routes the three admin routes and sets the security headers on
// every response. Without session data the stop route is 404, like an
// unknown admin path.
func (a *adminHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	setAdminHeaders(w)
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/desk/admin/":
		a.serveOverview(w, r)
	case r.Method == http.MethodPost && r.URL.Path == "/desk/admin/pool":
		a.servePoolUpload(w, r)
	case r.Method == http.MethodPost && r.URL.Path == "/desk/admin/stop" && a.sessions != nil:
		a.serveStop(w, r)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

// serveOverview is GET /desk/admin/{$}: the admin page, with the one-shot
// notice taken for this user.
func (a *adminHandler) serveOverview(w http.ResponseWriter, r *http.Request) {
	id := identity(r)
	body := a.overviewBody(r.Context(), a.takeNotice(id.User))
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(body))
}

// logUnknown is what a list built from the desk log reads when the log
// cannot be read.
const logUnknown = "unknown: the desk log cannot be read"

// overviewBody renders the admin page with the given notice (not stored):
// the sections Collection, Students, Problems and Health, in that order.
// The desk log is read once for the whole page, and the seats once
// (seatsOf). When the log cannot be read, the page starts with a warning
// and every figure from the log reads unknown; the error itself stays in
// the container log, since it can quote part of a line.
func (a *adminHandler) overviewBody(ctx context.Context, notice string) string {
	evs, err := a.readLog()
	logKnown := err == nil
	sd := a.seatsOf(ctx)
	inner := ""
	if !logKnown {
		inner = `<p class="warn">The desk log cannot be read, so the figures that come from it are unknown.</p>`
	}
	inner += a.collectionSection(evs, logKnown) + a.studentsSection(ctx, evs, logKnown, sd) + a.problemsSection(evs, logKnown) + a.healthSection(sd)
	return adminShell("Desk admin", inner, notice, false)
}

// seatData is what one page load knows of the seats: the session status
// rows, or, when the status failed, the registry's rows (no Docker), read
// only then.
type seatData struct {
	rows      []session.Row
	statusErr error
	seats     []registry.Row // read only when statusErr != nil
	seatsErr  error
}

// seatsOf asks for the session status once, under the page's time limit,
// and when it fails reads the registry once; the zero value without session
// data.
func (a *adminHandler) seatsOf(ctx context.Context) seatData {
	var sd seatData
	if a.sessions == nil {
		return sd
	}
	sctx, cancel := context.WithTimeout(ctx, a.statusLimit)
	sd.rows, sd.statusErr = a.sessions.Status(sctx)
	cancel()
	if sd.statusErr == nil {
		return sd
	}
	log.Printf("web: admin: session status: %v", sd.statusErr)
	if sd.seats, sd.seatsErr = a.sessions.Seats(); sd.seatsErr != nil {
		log.Printf("web: admin: registry: %v", sd.seatsErr)
	}
	return sd
}

// names are the users of every seat, live and retired, as far as the page
// knows them: from the status rows, else from the registry, else none.
func (sd seatData) names() []string {
	var out []string
	switch {
	case sd.statusErr == nil:
		for _, r := range sd.rows {
			out = append(out, r.User)
		}
	case sd.seatsErr == nil:
		for _, r := range sd.seats {
			out = append(out, r.User)
		}
	}
	return out
}

// deskLog is the desk log, or an empty one with the platform's default kind
// when none is wired, so its key rule (KeyOf) is always at hand.
func (a *adminHandler) deskLog() *desklog.Log {
	if a.log != nil {
		return a.log
	}
	return &desklog.Log{DefaultKind: a.kinds.Default()}
}

// readLog returns the desk log's events; nil without a log, and the error
// (logged here) when it cannot be read.
func (a *adminHandler) readLog() ([]desklog.Event, error) {
	if a.log == nil {
		return nil, nil
	}
	evs, err := a.log.Read()
	if err != nil {
		log.Printf("web: admin: desk log: %v", err)
		return nil, err
	}
	return evs, nil
}

// collectionSection is the Collection section: the queue line (or the
// unreadable-pool line), the records line and the pool upload form. With
// the desk log unknown (logKnown false), the queue's split and the collectors
// read unknown; the queue total still shows.
func (a *adminHandler) collectionSection(evs []desklog.Event, logKnown bool) string {
	queue := ""
	var c collection
	snap, err := a.poolSnapshot()
	if err != nil {
		queue = "pool.csv unreadable: " + html.EscapeString(err.Error())
		c = collect(nil, evs, a.deskLog())
	} else {
		c = collect(snap.Valid, evs, a.deskLog())
		problems := strconv.Itoa(c.Problems) + " problems"
		if c.Problems == 1 {
			problems = "1 problem"
		}
		queue = fmt.Sprintf("Queue %d %ss · unclaimed %d · claimed %d · done %d (%s) · open %d",
			c.Queue, html.EscapeString(a.kinds.Default()), c.Queue-c.Claimed, c.Claimed, c.Done, problems, c.Claimed-c.Done)
		if !logKnown {
			queue = fmt.Sprintf("Queue %d %ss · unclaimed unknown · claimed unknown · done unknown (problems unknown) · open unknown",
				c.Queue, html.EscapeString(a.kinds.Default()))
		}
	}
	collectors := strconv.Itoa(c.Collectors)
	if !logKnown {
		collectors = "unknown"
	}
	captured := "unknown"
	if n, err := a.records.Total(a.cfg.DeskRoot); err != nil {
		log.Printf("web: admin: captures: %v", err)
	} else {
		captured = strconv.FormatInt(n, 10)
	}
	return `<h2>Collection: ` + html.EscapeString(a.platform.Label) + `</h2>` +
		`<p>` + queue + `</p>` +
		`<p>Records captured ` + captured + ` · collectors who took part ` + collectors + `</p>` +
		uploadForm
}

// poolSnapshot returns the pool as it is now, through Deps.Pool (shared
// with the collector pages), or read from pool.csv on the spot when none is
// wired. Its Valid rows are the ones this platform can open (desk.pool()).
func (a *adminHandler) poolSnapshot() (*pool.Snapshot, error) {
	c := a.pool
	if c == nil {
		c = &pool.Cache{Path: a.poolPath, DefaultKind: a.kinds.Default(), Valid: a.kinds.valid}
	}
	return c.Snapshot()
}

// problemsSection is the Problems section: every problem report in the
// desk log, newest first (by time, then by place in the log), with the
// item (linked when the platform can open it), the platform's label for
// the code, the note, the student and the time. Everything from the log is
// escaped. With the desk log unknown (logKnown false), one line says so.
func (a *adminHandler) problemsSection(evs []desklog.Event, logKnown bool) string {
	if !logKnown {
		return `<h2>Problems</h2><p>` + logUnknown + `</p>`
	}
	var reports []desklog.Event
	for i := len(evs) - 1; i >= 0; i-- {
		if evs[i].Action == "problem" {
			reports = append(reports, evs[i])
		}
	}
	if len(reports) == 0 {
		return `<h2>Problems</h2><p>No problems reported.</p>`
	}
	sort.SliceStable(reports, func(i, j int) bool { return reports[i].T > reports[j].T })
	now := a.now()
	var b strings.Builder
	b.WriteString(`<h2>Problems</h2><table><tr><th>Item</th><th>Problem</th><th>Note</th><th>Student</th><th>When</th></tr>`)
	for _, e := range reports {
		b.WriteString(`<tr><td>` + a.itemCell(a.deskLog().KeyOf(e)) + `</td>` +
			`<td>` + html.EscapeString(a.problemLabel(e.Code)) + `</td>` +
			`<td>` + html.EscapeString(e.Note) + `</td>` +
			`<td>` + html.EscapeString(e.User) + `</td>` +
			`<td>` + html.EscapeString(when(time.Unix(e.T, 0), now)) + `</td></tr>`)
	}
	b.WriteString(`</table>`)
	return b.String()
}

// itemCell is an item as "<kind> <id>", linked to the item when the
// platform can open it (its id matches the kind's whole pattern and
// ItemURL builds a URL), else as text.
func (a *adminHandler) itemCell(k desklog.Key) string {
	text := html.EscapeString(k.Kind + " " + k.ID)
	re, ok := a.kinds.Kind(k.Kind)
	if !ok || !re.MatchString(k.ID) {
		return text
	}
	u, err := a.platform.ItemURL(k.Kind, k.ID)
	if err != nil {
		return text
	}
	return `<a href="` + html.EscapeString(u) + `">` + text + `</a>`
}

// problemLabel is the platform's label for a problem code, {contact}
// replaced as on the student's page; an unknown code is the code itself.
func (a *adminHandler) problemLabel(code string) string {
	for _, p := range a.platform.Problems {
		if p.Code == code {
			return strings.ReplaceAll(p.Label, "{contact}", a.cfg.Contact)
		}
	}
	return code
}

// healthSection is the Health section: the CO poll line, the seats line
// and, with 4CAT enabled, the links to 4CAT's own pages.
func (a *adminHandler) healthSection(sd seatData) string {
	s := `<h2>Health</h2>` + a.pollLine() + a.seatsLine(sd)
	if a.cfg.FourcatEnabled {
		s += `<p>4CAT: <a href="/results/">datasets</a> · <a href="/admin/">status</a> · <a href="/logs/">logs</a></p>`
	}
	return s
}

// pollLine is the CO poll's status line (ADR-0005), shown as a warning
// while it alarms; "" without a poller.
func (a *adminHandler) pollLine() string {
	if a.members == nil {
		return ""
	}
	line := a.members.StatusLine()
	class := ""
	if strings.HasPrefix(line, "ALARM") {
		class = ` class="warn"`
	}
	return `<p` + class + `>` + html.EscapeString(line) + `</p>`
}

// seatsLine is the seats line: the pairs running of the cap (Cfg.MaxRunning,
// already lowered to what memory allows), the configured value and what
// memory allows, and the live registry rows of the session limit. When the
// running count is unavailable (statusErr) it says so as a warning and
// takes the live rows from the registry alone. "" without session data.
func (a *adminHandler) seatsLine(sd seatData) string {
	if a.sessions == nil {
		return ""
	}
	memory := "unknown"
	if a.seats.MemoryAllows > 0 {
		memory = strconv.Itoa(a.seats.MemoryAllows)
	}
	figures := fmt.Sprintf("(configured %d; memory allows %s)", a.seats.Configured, memory)
	if sd.statusErr == nil {
		running, live := 0, 0
		for _, r := range sd.rows {
			if r.Running {
				running++
			}
			if r.Live {
				live++
			}
		}
		return fmt.Sprintf(`<p>Seats: %d of %d running %s · registered %d of %d</p>`,
			running, a.cfg.MaxRunning, figures, live, a.cfg.MaxSessions)
	}
	registered := "unknown"
	if sd.seatsErr == nil {
		live := 0
		for _, r := range sd.seats {
			if r.Status == "live" {
				live++
			}
		}
		registered = strconv.Itoa(live)
	}
	return fmt.Sprintf(`<p class="warn">Seats: the running count is unavailable; at most %d may run %s · registered %s of %d</p>`,
		a.cfg.MaxRunning, figures, registered, a.cfg.MaxSessions)
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
		body := a.overviewBody(r.Context(), "could not add to the pool: "+err.Error())
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(body))
		return
	}
	a.setNotice(id.User, fmt.Sprintf("added %d of %d; %s", added, len(rows)+len(rejects), rejectText(rejects)))
	a.redirect(w)
}

// maxStopBytes is the stop form's body limit.
const maxStopBytes = 4 * 1024

// adminStopLimit bounds an admin's stop of a pair, which runs on when the
// admin's request goes away. A normal stop waits at most 10 s for each of
// the two containers (session.stopTimeout) plus a few quick Docker calls;
// one minute leaves ample room above that and still ends a stop that hangs.
const adminStopLimit = time.Minute

// userRe is the username grammar; a name the stop form sends must match it
// before it reaches the registry, Docker or the log.
var userRe = regexp.MustCompile(`^[a-z][a-z0-9._-]{0,31}$`)

// serveStop is POST /desk/admin/stop (form field user), for a hung session;
// it is not a ban and changes nothing else. A request that is not
// same-origin, too large, names no username or names one without a registry
// row is refused with no Docker call and no log event. Otherwise the pair is
// stopped under its own time limit (adminStopLimit), whether or not the
// request is still there; on success a stop event (code admin, note
// "by <admin>") is written and the user's idle clocks restart. Either way
// the answer is the overview with a one-shot notice. A stop that worked
// stands when the log cannot be written; the notice says so.
func (a *adminHandler) serveStop(w http.ResponseWriter, r *http.Request) {
	id := identity(r)
	if !csrfOK(r) {
		a.say(w, http.StatusForbidden, "Refused: this request did not come from the admin page.")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxStopBytes)
	if err := r.ParseForm(); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			a.say(w, http.StatusRequestEntityTooLarge, "Refused: the request is too large.")
			return
		}
		a.say(w, http.StatusBadRequest, "Refused: not a form.")
		return
	}
	user := r.PostForm.Get("user")
	if !userRe.MatchString(user) {
		a.say(w, http.StatusBadRequest, "Refused: not a username.")
		return
	}
	seats, err := a.sessions.Seats()
	if err != nil {
		log.Printf("web: admin: stop %s: registry: %v", user, err)
		a.say(w, http.StatusInternalServerError, "Refused: the seat registry cannot be read.")
		return
	}
	if !slices.ContainsFunc(seats, func(s registry.Row) bool { return s.User == user }) {
		a.say(w, http.StatusBadRequest, "Refused: "+user+" has no seat.")
		return
	}
	// The stop runs to its end even when the admin's request goes away: a
	// half-done stop leaves the proxy and the network behind with no Stop
	// button to finish it.
	sctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), adminStopLimit)
	err = a.sessions.Stop(sctx, user)
	cancel()
	if err != nil {
		log.Printf("web: admin: stop %s by %s: %v", user, id.User, err)
		a.setNotice(id.User, "could not stop "+user+": "+err.Error())
		a.redirect(w)
		return
	}
	notice := "stopped " + user
	if a.log != nil {
		ev := desklog.Event{T: a.now().Unix(), User: user, Action: "stop", Code: "admin", Note: "by " + id.User}
		if err := a.log.Append(ev); err != nil {
			log.Printf("web: admin: desk log (stop %s by %s): %v", user, id.User, err)
			notice += ", but the desk log could not record the stop"
		}
	}
	if a.activity != nil {
		a.activity.Forget(user)
	}
	log.Printf("web: admin: %s stopped %s", id.User, user)
	a.setNotice(id.User, notice)
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
