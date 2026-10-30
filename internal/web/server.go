// Package web serves the desk's two listeners: the gated listener that
// trusts only requests carrying the gate secret, and the container listener
// that serves the health endpoint and nothing else.
package web

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/daniellemccool/researchcloud-ddp-comments/internal/access"
	"github.com/daniellemccool/researchcloud-ddp-comments/internal/config"
	"github.com/daniellemccool/researchcloud-ddp-comments/internal/platform"
)

// Default addresses of the two listeners; the caller may override Addr.
const (
	defaultGatedAddr     = ":8110"
	defaultContainerAddr = ":8111"
)

// New returns the gated listener's server. Every request goes through the
// gate first: access.FromRequest is called exactly once per request, in the
// handler below. logPath is the access log (<desk root>/state/access.ndjson).
func New(cfg *config.Config, ac access.Config, p *platform.Platform, logPath string) *http.Server {
	h := &handler{
		cfg:      cfg,
		ac:       ac,
		platform: p,
		log:      newAccessLog(logPath),
		pages:    mustLoadPages(),
	}
	return &http.Server{Addr: defaultGatedAddr, Handler: h}
}

// NewContainer returns the container listener: GET /healthz answers 200
// "ok", every other path is 404, and no identity is ever read from the
// request.
func NewContainer() *http.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("ok"))
	})
	return &http.Server{Addr: defaultContainerAddr, Handler: mux}
}

// handler is the gated listener's root handler: one middleware (the gate and
// identity check) followed by the routes.
type handler struct {
	cfg      *config.Config
	ac       access.Config
	platform *platform.Platform
	log      *accessLog
	pages    *pages
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

	path := r.URL.Path
	switch {
	case path == "/desk/":
		if id.Admin {
			h.pages.render(w, http.StatusOK, "admin", pageData{
				Title:    "Desk admin",
				User:     id.User,
				Platform: h.platform.Label,
			})
		} else {
			h.pages.render(w, http.StatusOK, "notready", pageData{
				Title:   "Desk",
				Contact: h.cfg.Contact,
			})
		}
	case path == "/fourcat/" || strings.HasPrefix(path, "/fourcat/"):
		plain(w, http.StatusServiceUnavailable, "4CAT access through the desk is not yet available")
	default:
		http.NotFound(w, r)
	}
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
	if l.last[id.User] == sig {
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
		return
	}
	if err := os.MkdirAll(filepath.Dir(l.path), 0o755); err != nil {
		return
	}
	f, err := os.OpenFile(l.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	if _, err := f.Write(append(line, '\n')); err != nil {
		_ = f.Close()
		return
	}
	_ = f.Close()
	l.last[id.User] = sig
}
