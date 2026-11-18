package web

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/daniellemccool/researchcloud-ddp-comments/internal/access"
	"github.com/daniellemccool/researchcloud-ddp-comments/internal/desklog"
	"github.com/daniellemccool/researchcloud-ddp-comments/internal/platform"
	"github.com/daniellemccool/researchcloud-ddp-comments/internal/pool"
	"github.com/daniellemccool/researchcloud-ddp-comments/internal/provision"
	"github.com/daniellemccool/researchcloud-ddp-comments/internal/session"
)

// identity returns the request's identity from the context (the gate
// middleware put it there); the user never comes from the path, query or
// body.
func identity(r *http.Request) access.Identity {
	return r.Context().Value(ctxKey{}).(access.Identity)
}

// serveStatus is GET /desk/status, the check-in without input:
// {"ready":true|false} from Sessions.Ready and {"running":true|false} from
// Sessions.IsRunning, so the page shows the stopped banner only for a pair
// that is gone and not while the image's Firefox restarts, plus the display
// text a page left open keeps current ("worked", the time line).
func (h *handler) serveStatus(w http.ResponseWriter, r *http.Request) {
	user := identity(r).User
	ready, running := false, false
	if h.sessions != nil {
		running = h.sessions.IsRunning(r.Context(), user)
		ready = h.sessions.Ready(r.Context(), user)
	}
	ans := h.figures(r.Context(), user)
	ans["ready"] = ready
	ans["running"] = running
	body, err := json.Marshal(ans)
	if err != nil {
		log.Printf("web: status: %v", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

// serveActive is POST /desk/active, the page heartbeat: it records that the
// collector touched mouse or keyboard, marks the current slot as worked, and
// answers {"running":bool}, so the page can show the stopped banner once the
// pair is gone, plus the same display text as serveStatus; the time line
// already counts the slot just marked.
func (h *handler) serveActive(w http.ResponseWriter, r *http.Request) {
	user := identity(r).User
	if h.activity != nil {
		h.activity.Input(user)
	}
	h.markActive(user)
	running := false
	if h.sessions != nil {
		running = h.sessions.IsRunning(r.Context(), user)
	}
	ans := h.figures(r.Context(), user)
	ans["running"] = running
	body, err := json.Marshal(ans)
	if err != nil {
		log.Printf("web: active: %v", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

// figures is the display text both check-in answers carry, so a page left
// open stays right: "worked", the time line, and with 4CAT enabled "footer"
// (the footer's inner HTML) and "footer_warn".
func (h *handler) figures(ctx context.Context, user string) map[string]any {
	ans := map[string]any{"worked": workedLine(h.workedOf(user))}
	if footer, warn := h.footer(ctx, user, h.now()); footer != nil {
		ans["footer"], ans["footer_warn"] = *footer, warn
	}
	return ans
}

// footer is the student's upload footer and whether it warns; nil without
// 4CAT, where the page has no footer. Whether it shows at all follows the
// configuration, not whether a reader is wired: without one the state is
// unknown and the footer is the plain reminder.
func (h *handler) footer(ctx context.Context, user string, now time.Time) (*string, bool) {
	if !h.cfg.FourcatEnabled {
		return nil, false
	}
	st := h.uploads.Of(ctx, user)
	text := uploadFooter(h.platform.UploadHint, st, now)
	return &text, st.Kind == uploadDue
}

// markActive appends user's `active` mark for the current five-minute slot
// to the desk log, at most once per user per slot; time worked is read only
// from these marks (desklog.SlotsByUser). The last slot written is kept in
// memory, so a desk restart may write one duplicate, which the reader counts
// once. A failed append is logged and the slot is forgotten again: the
// check-in, and with it the idle stop, never depends on the log being
// writable. The slot is taken under slotMu and the append runs outside it,
// so slotMu is never held over a write. (The answers of both check-ins read
// the desk log for the time line, and that read waits for an append in
// progress, as the page load's read always has.)
func (h *handler) markActive(user string) {
	if h.deskLog == nil {
		return
	}
	slot := desklog.SlotStart(h.now().Unix())
	h.slotMu.Lock()
	last, marked := h.lastSlot[user]
	if marked && last == slot {
		h.slotMu.Unlock()
		return
	}
	h.lastSlot[user] = slot
	h.slotMu.Unlock()

	if err := h.deskLog.Append(desklog.Event{T: slot, User: user, Action: "active", Code: ""}); err != nil {
		log.Printf("web: desk log (active %s): %v", user, err)
		h.slotMu.Lock()
		if h.lastSlot[user] == slot { // a later slot's mark may have been taken meanwhile
			if marked {
				h.lastSlot[user] = last
			} else {
				delete(h.lastSlot, user)
			}
		}
		h.slotMu.Unlock()
	}
}

// serveClaim is POST /desk/claim: n must be ASCII
// digits and one of BatchSizes, the desk must be ready, and Log.Claim must
// not be busy or empty.
func (h *handler) serveClaim(w http.ResponseWriter, r *http.Request) {
	id := identity(r)
	nStr := r.URL.Query().Get("n")
	if nStr == "" {
		nStr = strconv.Itoa(h.cfg.BatchSizes[0])
	}
	if !asciiDigits(nStr) {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	n, err := strconv.Atoi(nStr)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	inSizes := false
	for _, b := range h.cfg.BatchSizes {
		if b == n {
			inSizes = true
			break
		}
	}
	if !inSizes {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	if !provision.Load(h.cfg, id.User).Ready() {
		w.WriteHeader(http.StatusConflict) // a hand-made POST must not claim before the desk exists
		return
	}
	_, err = h.deskLog.Claim(id.User, n, h.poolRows())
	switch {
	case errors.Is(err, desklog.ErrBusy), errors.Is(err, desklog.ErrEmpty):
		w.WriteHeader(http.StatusConflict)
	case err != nil:
		log.Printf("web: claim %s: %v", id.User, err)
		w.WriteHeader(http.StatusInternalServerError)
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}

// serveOpen is POST /desk/open: the item must be valid
// and among this user's claims; OpenURL runs, the event is appended, 204 on
// rc 0 else 500. A failed append is a 500: the Python raised and the client
// never saw success. ErrBrowserStarting (nothing was opened) is a 503 with
// the retry text and appends nothing.
func (h *handler) serveOpen(w http.ResponseWriter, r *http.Request) {
	id := identity(r)
	// Sessions is nil until a later task wires it; guard before anything else
	// (ruling F60).
	if h.sessions == nil {
		plain(w, http.StatusServiceUnavailable, "desk not ready")
		return
	}
	evs, err := h.deskLog.Read()
	if err != nil {
		log.Printf("web: desk log: %v", err)
		plain(w, http.StatusInternalServerError, "could not read the desk log")
		return
	}
	kind, itemID, ok := h.claimedItem(r, id.User, evs)
	if !ok {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	url, err := h.platform.ItemURL(kind, itemID)
	if err != nil {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	rc, stderr, err := h.sessions.OpenURL(r.Context(), id.User, url)
	if errors.Is(err, session.ErrBrowserStarting) {
		// The collector's own Firefox is not up yet, so nothing ran and
		// nothing is recorded; the page asks to try again.
		log.Printf("web: open %s: %v", id.User, err)
		plain(w, http.StatusServiceUnavailable, "the browser is still starting; try again in a moment")
		return
	}
	if err != nil {
		// The transport error's message names the docker call; it carries no
		// secret, so it is recorded as exec_err.
		log.Printf("web: open %s %s: %v", id.User, url, err)
		rc, stderr = -1, err.Error()
	}
	ev := desklog.Event{
		T:       time.Now().Unix(),
		User:    id.User,
		Action:  "open",
		Kind:    kind,
		ID:      itemID,
		Code:    "",
		ExecRC:  &rc,
		ExecErr: stderr,
	}
	if aerr := h.deskLog.Append(ev); aerr != nil {
		// The browser command has already run; log the rc too.
		log.Printf("web: desk log (open %s %s rc=%d): %v", id.User, itemID, rc, aerr)
		plain(w, http.StatusInternalServerError, "could not record")
		return
	}
	if rc == 0 {
		w.WriteHeader(http.StatusNoContent)
	} else {
		w.WriteHeader(http.StatusInternalServerError)
	}
}

// serveProblem is POST /desk/problem: the code is
// matched as given then upper-cased, the note is whitespace-collapsed and cut
// to 300 runes; unknown codes and missing required notes are 400. A failed
// append is a 500, as in serveOpen.
func (h *handler) serveProblem(w http.ResponseWriter, r *http.Request) {
	id := identity(r)
	evs, err := h.deskLog.Read()
	if err != nil {
		log.Printf("web: desk log: %v", err)
		plain(w, http.StatusInternalServerError, "could not read the desk log")
		return
	}
	kind, itemID, ok := h.claimedItem(r, id.User, evs)
	if !ok {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	q := r.URL.Query()
	code := strings.TrimSpace(q.Get("code"))
	if _, known := h.problemByCode(code); !known {
		code = strings.ToUpper(code)
	}
	note := strings.Join(strings.Fields(q.Get("note")), " ")
	if rs := []rune(note); len(rs) > 300 {
		note = string(rs[:300])
	}
	p, known := h.problemByCode(code)
	if !known || (p.Note && note == "") {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	ev := desklog.Event{
		T:      time.Now().Unix(),
		User:   id.User,
		Action: "problem",
		Kind:   kind,
		ID:     itemID,
		Code:   code,
		Note:   note,
	}
	if aerr := h.deskLog.Append(ev); aerr != nil {
		log.Printf("web: desk log (problem %s %s %s): %v", id.User, itemID, code, aerr)
		plain(w, http.StatusInternalServerError, "could not record")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// claimedItem validates the kind/id query (kind defaults to the platform's
// default kind) and reports whether the item is among the user's claims in
// evs.
func (h *handler) claimedItem(r *http.Request, user string, evs []desklog.Event) (kind, itemID string, ok bool) {
	q := r.URL.Query()
	kind = q.Get("kind")
	if kind == "" {
		kind = h.platform.DefaultKind
	}
	itemID = q.Get("id")
	if !h.valid(kind, itemID) || !claimedIn(h.deskLog.ViewOf(user, evs), kind, itemID) {
		return "", "", false
	}
	return kind, itemID, true
}

// claimedIn reports whether (kind, id) is among the view's claims.
func claimedIn(view desklog.View, kind, id string) bool {
	k := desklog.Key{Kind: kind, ID: id}
	for _, c := range view.Claims {
		if c.Key == k {
			return true
		}
	}
	return false
}

// serveAdmin is GET /desk/admin/{$} and POST /desk/admin/pool: admins only;
// with the Deps Admin handler still nil the route is 404 (a later task
// supplies it).
func (h *handler) serveAdmin(w http.ResponseWriter, r *http.Request) {
	setAdminHeaders(w)
	id := identity(r)
	if !id.Admin {
		h.forbidden(w)
		return
	}
	if h.admin == nil {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	h.admin.ServeHTTP(w, r)
}

// serveAdminTree is every other /desk/admin/ path: admins only, 404 until a
// later task supplies the handlers.
func (h *handler) serveAdminTree(w http.ResponseWriter, r *http.Request) {
	setAdminHeaders(w)
	if !identity(r).Admin {
		h.forbidden(w)
		return
	}
	w.WriteHeader(http.StatusNotFound)
}

// serveVNC is /desk/vnc/: the relay is mounted by a later task; until then
// the path is 404.
func (h *handler) serveVNC(w http.ResponseWriter, r *http.Request) {
	if h.relay == nil {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	h.relay.ServeHTTP(w, r)
}

// serveFourcat is /fourcat/: admins only, which covers Tech Admins; with the
// Deps FourcatProxy handler still nil the route is 404.
func (h *handler) serveFourcat(w http.ResponseWriter, r *http.Request) {
	if id := identity(r); !id.Admin {
		h.forbidden(w)
		return
	}
	if h.fourcat == nil {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	h.fourcat.ServeHTTP(w, r)
}

// forbidden renders the 403 page with the contact.
func (h *handler) forbidden(w http.ResponseWriter) {
	h.pages.render(w, http.StatusForbidden, "403", pageData{
		Title:   "Not registered",
		Contact: h.cfg.Contact,
	})
}

// asciiDigits reports whether s is one or more ASCII digits (Python
// re.fullmatch(r"[0-9]+", s)).
func asciiDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// platformKinds adapts *platform.Platform to pool.Kinds. The id pattern is
// compiled once per kind and anchored: Python matched ids with fullmatch,
// the pool matches with the regexp as given, so the adapter anchors it.
type platformKinds struct {
	p     *platform.Platform
	mu    sync.Mutex
	cache map[string]*regexp.Regexp
}

// The adapter satisfies pool.Kinds.
var _ pool.Kinds = (*platformKinds)(nil)

func newPlatformKinds(p *platform.Platform) *platformKinds {
	return &platformKinds{p: p, cache: make(map[string]*regexp.Regexp)}
}

// Default returns the platform's default kind.
func (k *platformKinds) Default() string { return k.p.DefaultKind }

// Kind returns the anchored id regexp of kind, compiled once per kind;
// false for an unknown kind and for a kind whose ID regexp is nil (never
// match-everything).
func (k *platformKinds) Kind(kind string) (*regexp.Regexp, bool) {
	ik, ok := k.p.ItemKinds[kind]
	if !ok || ik.ID == nil {
		return nil, false
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	if re, ok := k.cache[kind]; ok {
		return re, true
	}
	re, err := regexp.Compile(`^(?:` + ik.ID.String() + `)$`)
	if err != nil {
		return nil, false
	}
	k.cache[kind] = re
	return re, true
}

// valid reports whether kind is known and id matches its pattern, anchored
// to the whole id: a row the platform can open.
func (k *platformKinds) valid(kind, id string) bool {
	re, ok := k.Kind(kind)
	return ok && re.MatchString(id)
}
