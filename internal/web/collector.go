package web

import (
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
	"github.com/daniellemccool/researchcloud-ddp-comments/internal/provision"
)

// identity returns the request's identity from the context (the gate
// middleware put it there); the user never comes from the path, query or
// body.
func identity(r *http.Request) access.Identity {
	return r.Context().Value(ctxKey{}).(access.Identity)
}

// serveStatus is GET /desk/status: {"ready":true|false} from Sessions.Ready
// (desk_stub.py do_GET).
func (h *handler) serveStatus(w http.ResponseWriter, r *http.Request) {
	ready := false
	if h.sessions != nil {
		ready = h.sessions.Ready(r.Context(), identity(r).User)
	}
	body, err := json.Marshal(map[string]bool{"ready": ready})
	if err != nil {
		log.Printf("web: status: %v", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

// serveClaim is POST /desk/claim (desk_stub.py do_POST): n must be ASCII
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

// serveOpen is POST /desk/open (desk_stub.py do_POST): the item must be valid
// and among this user's claims; OpenURL runs, the event is appended, 204 on
// rc 0 else 500.
func (h *handler) serveOpen(w http.ResponseWriter, r *http.Request) {
	id := identity(r)
	kind, itemID, ok := h.claimedItem(r, id.User)
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
	if err != nil {
		log.Printf("web: open %s %s: %v", id.User, url, err)
		rc, stderr = -1, ""
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
	if err := h.deskLog.Append(ev); err != nil {
		log.Printf("web: desk log: %v", err)
	}
	if rc == 0 {
		w.WriteHeader(http.StatusNoContent)
	} else {
		w.WriteHeader(http.StatusInternalServerError)
	}
}

// serveProblem is POST /desk/problem (desk_stub.py do_POST): the code is
// matched as given then upper-cased, the note is whitespace-collapsed and cut
// to 300 runes; unknown codes and missing required notes are 400.
func (h *handler) serveProblem(w http.ResponseWriter, r *http.Request) {
	id := identity(r)
	kind, itemID, ok := h.claimedItem(r, id.User)
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
	if err := h.deskLog.Append(ev); err != nil {
		log.Printf("web: desk log: %v", err)
	}
	w.WriteHeader(http.StatusNoContent)
}

// claimedItem validates the kind/id query (kind defaults to the platform's
// default kind) and reports whether the item is among the user's claims.
func (h *handler) claimedItem(r *http.Request, user string) (kind, itemID string, ok bool) {
	q := r.URL.Query()
	kind = q.Get("kind")
	if kind == "" {
		kind = h.platform.DefaultKind
	}
	itemID = q.Get("id")
	if !h.valid(kind, itemID) || !h.claimedBy(user, kind, itemID) {
		return "", "", false
	}
	return kind, itemID, true
}

// serveAdmin is GET /desk/admin/{$} and POST /desk/admin/pool: admins only;
// with the Deps Admin handler still nil the route is 404 (a later task
// supplies it).
func (h *handler) serveAdmin(w http.ResponseWriter, r *http.Request) {
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

// serveFourcat is /fourcat/: admins only; with the Deps FourcatProxy handler
// still nil the route is 404 (a later task supplies it).
func (h *handler) serveFourcat(w http.ResponseWriter, r *http.Request) {
	if !identity(r).Admin {
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

func newPlatformKinds(p *platform.Platform) *platformKinds {
	return &platformKinds{p: p, cache: make(map[string]*regexp.Regexp)}
}

// Default returns the platform's default kind.
func (k *platformKinds) Default() string { return k.p.DefaultKind }

// Kind returns the anchored id regexp of kind, compiled once per kind;
// false for an unknown kind.
func (k *platformKinds) Kind(kind string) (*regexp.Regexp, bool) {
	ik, ok := k.p.ItemKinds[kind]
	if !ok {
		return nil, false
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	if re, ok := k.cache[kind]; ok {
		return re, true
	}
	pattern := ".*"
	if ik.ID != nil {
		pattern = ik.ID.String()
	}
	re, err := regexp.Compile(`^(?:` + pattern + `)$`)
	if err != nil {
		return nil, false
	}
	k.cache[kind] = re
	return re, true
}
