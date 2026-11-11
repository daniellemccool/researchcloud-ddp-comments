package web

import (
	"context"
	"html"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/daniellemccool/researchcloud-ddp-comments/internal/access"
	"github.com/daniellemccool/researchcloud-ddp-comments/internal/desklog"
	"github.com/daniellemccool/researchcloud-ddp-comments/internal/fourcat"
	"github.com/daniellemccool/researchcloud-ddp-comments/internal/platform"
	"github.com/daniellemccool/researchcloud-ddp-comments/internal/pool"
	"github.com/daniellemccool/researchcloud-ddp-comments/internal/provision"
	"github.com/daniellemccool/researchcloud-ddp-comments/internal/render"
	"github.com/daniellemccool/researchcloud-ddp-comments/internal/session"
)

// serveHome is GET /desk/, in order:
// no release template and no profile → 503 not-ready; a user the last good CO
// list lacks gets the access-removed message and no kick (a revoke marked
// their registry step undone, and reactivation needs the poll's agreement);
// otherwise the provisioning state decides between the full / setup-failed /
// not-ready / setting-up messages;
// once ready, Start decides between busy / start-failed / gone / the page.
// Every not-ready state except registry-full kicks the single-flight
// background Advance (rulings F73/F79), so a failed step is retried on the
// next visit.
func (h *handler) serveHome(w http.ResponseWriter, r *http.Request) {
	// Only the desk's own origin may frame the collector page (review M2).
	w.Header().Set("Content-Security-Policy", "frame-ancestors 'self'")
	id := identity(r)
	user := id.User
	contact := h.cfg.Contact
	notReady := "The desk is not ready yet; ask " + contact + "."

	// Until Prov and Sessions are wired, the desk cannot serve anyone.
	if h.prov == nil || h.sessions == nil {
		h.say(w, http.StatusServiceUnavailable, notReady, 0)
		return
	}

	templateDir := filepath.Join(h.cfg.DeskRoot, "templates", "release", render.Generation(h.cfg))
	profile := filepath.Join(h.cfg.DeskRoot, "state", "firefox", user, "profile")
	if !isDir(templateDir) && !isDir(profile) {
		h.say(w, http.StatusServiceUnavailable, notReady, 0)
		return
	}

	st := provision.Load(h.cfg, user)
	if !st.Ready() {
		if h.members != nil && h.members.Absent(user) {
			// No kick: the registry step would refuse anyway, and a gate
			// still admitting a removed member must not make the desk
			// reactivate and revoke them every minute. A member just added
			// sees this until the next good poll lists them.
			h.say(w, http.StatusOK, "Your access to the desk was removed. If you were just re-added, try again in a minute; otherwise contact "+contact+".", 0)
			return
		}
		switch errText := st.Error(); {
		case errText == provision.RegistryFull:
			// The desk is full: no kick, no refresh — asking again changes nothing.
			h.say(w, http.StatusOK, "The desk is full; ask "+contact+".", 0)
		case errText == "error: "+fourcat.ErrSetupPending.Error():
			// 4CAT's first-run setup is pending: kick provisioning again so
			// the first visit after an admin completes the setup resumes it
			// (ruling F73), and still answer the not-ready message.
			h.kick(user, id.Admin)
			h.say(w, http.StatusServiceUnavailable, notReady, 0)
		case errText != "":
			// A failed step is retried on the next visit (ruling F79): kick
			// and refresh like the provisioning branch; the text is unchanged.
			h.kick(user, id.Admin)
			h.say(w, http.StatusOK, "Setup failed at "+st.Current()+"; ask "+contact+".", 3)
		default:
			h.kick(user, id.Admin)
			h.say(w, http.StatusOK, "Setting up your desk — "+st.Current(), 3)
		}
		return
	}

	// Restart the idle clocks before Start too: a reaper pass waiting on the
	// user lock re-checks idleness after Start releases it, and must find
	// this page load (F105).
	if h.activity != nil {
		h.activity.Forget(user)
	}
	res, err := h.sessions.Start(r.Context(), user)
	if err != nil {
		log.Printf("web: start %s: %v", user, err)
		res = session.StartFailed
	}
	switch res {
	case session.Busy:
		h.say(w, http.StatusOK, "The desk is busy, try again in a few minutes.", 0)
	case session.StartFailed:
		h.say(w, http.StatusOK, "Your browser could not be started; try again in a minute or ask "+contact+".", 0)
	case session.Gone:
		h.say(w, http.StatusOK, "Your desk is changing; this page reloads in a moment.", 3)
	default: // session.Started
		// Opening the page is input: a pair started after any stop begins
		// with both idle clocks fresh.
		if h.activity != nil {
			h.activity.Forget(user)
		}
		h.servePage(w, id)
	}
}

// defaultAdvanceTimeout bounds one kicked Advance (controller ruling F89):
// a hung exec (4CAT's database starting or locked) then fails its step with
// a recorded error instead of holding the user's lock and the single-flight
// entry until the desk restarts, and the next visit retries (ruling F79).
const defaultAdvanceTimeout = 2 * time.Minute

// kick runs Prov.Advance in a goroutine unless one is already running for
// user. The goroutine's context is not cancelled by the
// request; it carries its own deadline, defaultAdvanceTimeout.
func (h *handler) kick(user string, admin bool) {
	if _, loaded := h.provisioning.LoadOrStore(user, true); loaded {
		return
	}
	timeout := h.advanceTimeout
	if timeout <= 0 {
		timeout = defaultAdvanceTimeout
	}
	go func() {
		defer h.provisioning.Delete(user)
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		_ = h.prov.Advance(ctx, user, admin)
	}()
}

// say writes a MESSAGE page with the text HTML-escaped.
func (h *handler) say(w http.ResponseWriter, status int, text string, refresh int) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(message(text, refresh)))
}

// servePage renders the collector page.
func (h *handler) servePage(w http.ResponseWriter, id access.Identity) {
	user := id.User
	evs, err := h.deskLog.Read()
	if err != nil {
		log.Printf("web: desk log: %v", err)
		evs = nil
	}
	view := h.deskLog.ViewOf(user, evs)
	rows := h.poolRows()

	notes := make(map[desklog.Key]string, len(rows))
	for _, r := range rows {
		notes[desklog.Key{Kind: r.Kind, ID: r.ID}] = r.Note
	}

	latest := 0
	for _, c := range view.Claims {
		if c.Batch > latest {
			latest = c.Batch
		}
	}
	var shown []desklog.Claimed
	for _, c := range view.Claims {
		if c.Batch == latest || !view.Done[c.Key] {
			shown = append(shown, c)
		}
	}
	earlier := len(view.Claims) - len(shown)

	noun := html.EscapeString(h.platform.DefaultKind)
	options := problemOptions(h.problems())
	timer := h.cfg.TimerMinutes > 0

	var items []string
	for n, c := range shown {
		k := c.Key
		state := "todo"
		if _, ok := view.Problems[k]; ok {
			state = "problem"
		} else if view.Opened[k] {
			state = "done"
		}
		msg := ""
		if pr, ok := view.Problems[k]; ok {
			label := pr.Code
			if p, ok := h.problemByCode(pr.Code); ok {
				label = p.Label
			}
			msg = "— " + html.EscapeString(label)
			if pr.Note != "" {
				msg += ": " + html.EscapeString(pr.Note)
			}
		}
		title := notes[k]
		if title == "" {
			title = capitalize(k.Kind) + " " + strconv.Itoa(n+1)
		}
		items = append(items, row(k.Kind, k.ID, state, view.Opened[k], title, msg, options, timer))
	}

	nDone := 0
	for _, c := range shown {
		if view.Done[c.Key] {
			nDone++
		}
	}

	var body strings.Builder
	if len(shown) > 0 {
		disabled := ""
		if nDone == len(shown) {
			disabled = " disabled"
		}
		body.WriteString(`<button id="next" class="next" onclick="openNext()"` + disabled + `>Open next ` + noun + `</button>`)
		body.WriteString(`<ul class="rows">` + strings.Join(items, "") + `</ul>`)
	}
	if earlier > 0 {
		body.WriteString(`<p class="hint">` + strconv.Itoa(earlier) + ` done earlier</p>`)
	}
	taken := h.deskLog.Taken(evs)
	unfinished := false
	for _, c := range shown {
		if !view.Done[c.Key] {
			unfinished = true
			break
		}
	}
	switch {
	case unfinished:
		body.WriteString(`<p class="hint">Finish these first: open each one, or mark a problem. Then you can get more.</p>`)
	case anyUntaken(rows, taken):
		body.WriteString(`<div class="more" id="getmore">`)
		for _, n := range h.cfg.BatchSizes {
			body.WriteString(`<button onclick="more(` + strconv.Itoa(n) + `)">Get ` + strconv.Itoa(n) + ` more ` + noun + `s</button>`)
		}
		body.WriteString(`</div>`)
	default:
		body.WriteString(`<p class="hint" id="getmore">No more ` + noun + `s right now; tell the operator.</p>`)
	}

	progressHTML := ""
	if len(shown) > 0 {
		progressHTML = progress(nDone, len(shown))
	}
	var footer *string
	if h.cfg.FourcatEnabled {
		hint := h.platform.UploadHint
		footer = &hint
	}
	noteCodes := make([]string, 0, len(h.platform.Problems))
	for _, p := range h.problems() {
		if p.Note {
			noteCodes = append(noteCodes, p.Code)
		}
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(deskPage(pageParams{
		User:          user,
		Admin:         id.Admin,
		PlatformLabel: h.platform.Label,
		Noun:          h.platform.DefaultKind,
		Seconds:       h.cfg.TimerMinutes * 60,
		NoteCodes:     noteCodes,
		ProgressHTML:  progressHTML,
		Body:          body.String(),
		OpenHelp:      len(view.Claims) == 0,
		FooterHint:    footer,
		IdleMinutes:   h.cfg.IdleSeconds / 60,
		Contact:       h.cfg.Contact,
	})))
}

// problems returns the platform's problem codes with {contact} in the labels
// replaced.
func (h *handler) problems() []platform.Problem {
	out := make([]platform.Problem, len(h.platform.Problems))
	for i, p := range h.platform.Problems {
		p.Label = strings.ReplaceAll(p.Label, "{contact}", h.cfg.Contact)
		out[i] = p
	}
	return out
}

func (h *handler) problemByCode(code string) (platform.Problem, bool) {
	for _, p := range h.problems() {
		if p.Code == code {
			return p, true
		}
	}
	return platform.Problem{}, false
}

// poolRows returns the pool rows this platform can open, in file order:
// unknown kinds and id mismatches are dropped.
func (h *handler) poolRows() []pool.Row {
	rows, err := pool.Read(filepath.Join(h.cfg.DeskRoot, "pool.csv"), h.platform.DefaultKind)
	if err != nil {
		log.Printf("web: pool.csv: %v", err)
		return nil
	}
	out := make([]pool.Row, 0, len(rows))
	for _, r := range rows {
		if h.valid(r.Kind, r.ID) {
			out = append(out, r)
		}
	}
	return out
}

// valid reports whether kind is known and id matches its pattern, anchored
// to the whole id.
func (h *handler) valid(kind, id string) bool {
	re, ok := h.kinds.Kind(kind)
	return ok && re.MatchString(id)
}

func anyUntaken(rows []pool.Row, taken map[desklog.Key]bool) bool {
	for _, r := range rows {
		if !taken[desklog.Key{Kind: r.Kind, ID: r.ID}] {
			return true
		}
	}
	return false
}

func isDir(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.IsDir()
}

// capitalize upper-cases the first letter and lower-cases the rest, as
// Python's str.capitalize does.
func capitalize(s string) string {
	if s == "" {
		return s
	}
	r := []rune(s)
	r[0] = unicode.ToUpper(r[0])
	for i := 1; i < len(r); i++ {
		r[i] = unicode.ToLower(r[i])
	}
	return string(r)
}
