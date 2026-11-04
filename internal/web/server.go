// Package web serves the desk's two listeners: the gated listener that
// trusts only requests carrying the gate secret, and the container listener
// that serves the health endpoint and nothing else.
package web

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/daniellemccool/researchcloud-ddp-comments/internal/access"
	"github.com/daniellemccool/researchcloud-ddp-comments/internal/config"
	"github.com/daniellemccool/researchcloud-ddp-comments/internal/desklog"
	"github.com/daniellemccool/researchcloud-ddp-comments/internal/platform"
	"github.com/daniellemccool/researchcloud-ddp-comments/internal/provision"
	"github.com/daniellemccool/researchcloud-ddp-comments/internal/session"
)

// Default addresses of the two listeners; the caller may override Addr.
const (
	defaultGatedAddr     = ":8110"
	defaultContainerAddr = ":8111"
)

// provisioner is what the home handler needs of *provision.Provisioner
// (controller ruling F54: small interfaces, so tests can use fakes).
type provisioner interface {
	Advance(ctx context.Context, user string, admin bool) provision.State
}

// sessions is what the routes need of *session.Manager.
type sessions interface {
	Start(ctx context.Context, user string) (session.StartResult, error)
	Ready(ctx context.Context, user string) bool
	OpenURL(ctx context.Context, user, url string) (int, string, error)
}

// Deps are the gated listener's dependencies. Prov and Sessions may be nil
// until a later task wires them; the home handler then answers the 503
// not-ready message. FourcatProxy, Relay and Admin are nil until a later
// task supplies them; their routes then answer 404.
type Deps struct {
	Cfg          *config.Config
	Access       access.Config
	Platform     *platform.Platform
	LogPath      string       // the access log
	Prov         provisioner  // *provision.Provisioner
	Sessions     sessions     // *session.Manager
	Log          *desklog.Log // the desk log (<desk root>/state/desk-log.ndjson)
	FourcatProxy http.Handler // nil until a later task
	Relay        http.Handler // nil until a later task
	Admin        http.Handler // nil until a later task
}

// New returns the gated listener's server. Every request goes through the
// gate first: access.FromRequest is called exactly once per request, in the
// handler below. New panics when d.Access.GateSecret is empty: an empty
// secret must fail closed, never open, so a server built without one is a
// programming error.
func New(d Deps) *http.Server {
	if d.Access.GateSecret == "" {
		panic("web: New: access.Config.GateSecret is empty; the gate must fail closed")
	}
	h := &handler{
		cfg:      d.Cfg,
		ac:       d.Access,
		platform: d.Platform,
		log:      newAccessLog(d.LogPath),
		pages:    mustLoadPages(),
		prov:     d.Prov,
		sessions: d.Sessions,
		deskLog:  d.Log,
		fourcat:  d.FourcatProxy,
		relay:    d.Relay,
		admin:    d.Admin,
		kinds:    newPlatformKinds(d.Platform),
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /desk/{$}", h.serveHome)          // the collector page, exactly /desk/
	mux.HandleFunc("GET /desk/status", h.serveStatus)     // {"ready":bool} for the page's poll
	mux.HandleFunc("POST /desk/claim", h.serveClaim)      // get more items
	mux.HandleFunc("POST /desk/open", h.serveOpen)        // open one claimed item
	mux.HandleFunc("POST /desk/problem", h.serveProblem)  // mark a problem on a claimed item
	mux.HandleFunc("GET /desk/admin/{$}", h.serveAdmin)   // nil Admin handler → 404
	mux.HandleFunc("POST /desk/admin/pool", h.serveAdmin) // nil Admin handler → 404
	mux.HandleFunc("GET /desk/admin", func(w http.ResponseWriter, r *http.Request) {
		setAdminHeaders(w)                                               // the 301 answer carries the admin headers too
		http.Redirect(w, r, "/desk/admin/", http.StatusMovedPermanently) // no-slash → 301
	})
	mux.HandleFunc("/desk/admin/", h.serveAdminTree) // admins only, else 404
	mux.HandleFunc("/desk/vnc/", h.serveVNC)         // nil Relay → 404
	mux.HandleFunc("/fourcat/", h.serveFourcat)      // admins only, nil proxy → 404
	h.routes = mux
	return &http.Server{Addr: defaultGatedAddr, Handler: h}
}

// NewContainer returns the container listener: GET /healthz answers 200
// "ok", every other path is 404, and no identity is ever read from the
// request.
func NewContainer() *http.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("ok"))
	})
	return &http.Server{Addr: defaultContainerAddr, Handler: mux}
}

// ctxKey carries the identity through the routes; FromRequest is still called
// exactly once per request.
type ctxKey struct{}

// handler is the gated listener's root handler: one middleware (the gate and
// identity check) followed by the routes.
type handler struct {
	cfg      *config.Config
	ac       access.Config
	platform *platform.Platform
	log      *accessLog
	pages    *pages
	routes   *http.ServeMux

	prov     provisioner    // nil until wired: home answers 503 not-ready
	sessions sessions       // nil until wired: home answers 503 not-ready
	deskLog  *desklog.Log   // the desk log
	fourcat  http.Handler   // /fourcat/ proxy, nil → 404
	relay    http.Handler   // /desk/vnc/ relay, nil → 404
	admin    http.Handler   // /desk/admin/ pages, nil → 404
	kinds    *platformKinds // the pool.Kinds adapter over the platform

	provisioning sync.Map // users with an Advance goroutine in flight
}

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	id, err := access.FromRequest(r, h.ac)
	if err != nil {
		// Gate or identity failure: plain 403, and nothing about the
		// requester is logged (there is no trusted user).
		plain(w, http.StatusForbidden, "forbidden")
		return
	}
	h.log.note(id, r.URL.Path)

	if !id.Collector && !id.Admin {
		h.pages.render(w, http.StatusForbidden, "403", pageData{
			Title:   "Not registered",
			Contact: h.cfg.Contact,
		})
		return
	}
	h.routes.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, id)))
}

func plain(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(body))
}

// accessEntry is one line of the access log.
type accessEntry struct {
	T         int64    `json:"t"`
	User      string   `json:"user"`
	Roles     []string `json:"roles"`
	Collector bool     `json:"collector"`
	Admin     bool     `json:"admin"`
	Path      string   `json:"path"`
}

// accessLog appends one JSON line per (user, roles) change since the process
// started. Gate failures never reach it.
type accessLog struct {
	mu   sync.Mutex
	path string
	last map[string]string // user → roles signature of the last line written
}

func newAccessLog(path string) *accessLog {
	return &accessLog{path: path, last: map[string]string{}}
}

func (l *accessLog) note(id access.Identity, path string) {
	sig := strings.Join(id.Roles, "\x00")
	l.mu.Lock()
	defer l.mu.Unlock()
	// Log on the first request of a user (even with empty roles) and on
	// every roles change; a repeat with the same roles logs nothing.
	if prev, ok := l.last[id.User]; ok && prev == sig {
		return
	}
	line, err := json.Marshal(accessEntry{
		T:         time.Now().UnixMilli(),
		User:      id.User,
		Roles:     id.Roles,
		Collector: id.Collector,
		Admin:     id.Admin,
		Path:      path,
	})
	if err != nil {
		log.Printf("web: access log %s: %v", l.path, err)
		return
	}
	if err := os.MkdirAll(filepath.Dir(l.path), 0o755); err != nil {
		log.Printf("web: access log %s: %v", l.path, err)
		return
	}
	f, err := os.OpenFile(l.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		log.Printf("web: access log %s: %v", l.path, err)
		return
	}
	if _, err := f.Write(append(line, '\n')); err != nil {
		_ = f.Close()
		log.Printf("web: access log %s: %v", l.path, err)
		return
	}
	_ = f.Close()
	l.last[id.User] = sig
}
