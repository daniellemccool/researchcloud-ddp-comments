package web

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/daniellemccool/researchcloud-ddp-comments/internal/access"
	"github.com/daniellemccool/researchcloud-ddp-comments/internal/config"
	"github.com/daniellemccool/researchcloud-ddp-comments/internal/desklog"
)

// newTestServerWith builds the gated server over a temp desk root with the
// test platform; fill may set the Relay/FourcatProxy/Admin handlers.
func newTestServerWith(t *testing.T, fill func(*Deps)) *http.Server {
	t.Helper()
	root := t.TempDir()
	plat := testPlatform()
	cfg := &config.Config{
		DeskRoot:      root,
		FQDN:          "desk.example.org",
		CollectorRole: collectorRole,
		AdminRole:     adminRole,
		Contact:       testContact,
		Platform:      "test",
	}
	d := Deps{
		Cfg:      cfg,
		Access:   access.Config{CollectorRole: collectorRole, AdminRole: adminRole, GateSecret: testGate},
		Platform: plat,
		LogPath:  filepath.Join(root, "state", "access.ndjson"),
		Log:      &desklog.Log{Path: filepath.Join(root, "state", "desk-log.ndjson"), DefaultKind: plat.DefaultKind},
	}
	if fill != nil {
		fill(&d)
	}
	return New(d)
}

// doReq sends one request through the gated handler with the identity
// headers and the extra headers given.
func doReq(t *testing.T, srv *http.Server, method, user, roles, path string, body io.Reader, hdrs map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, body)
	access.SetTestHeaders(req, user, roles, testGate)
	for k, v := range hdrs {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	srv.Handler.ServeHTTP(rec, req)
	return rec
}

// fourcatBackend records what the proxy forwarded.
type fourcatBackend struct {
	srv   *httptest.Server
	mu    sync.Mutex
	paths []string
	hosts []string
	xfh   []string
	xfp   []string
}

func newFourcatBackend(t *testing.T) *fourcatBackend {
	t.Helper()
	b := &fourcatBackend{}
	b.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b.mu.Lock()
		b.paths = append(b.paths, r.URL.Path)
		b.hosts = append(b.hosts, r.Host)
		b.xfh = append(b.xfh, r.Header.Get("X-Forwarded-Host"))
		b.xfp = append(b.xfp, r.Header.Get("X-Forwarded-Proto"))
		b.mu.Unlock()
		if r.URL.Path == "/redirect" {
			http.Redirect(w, r, "/after-login", http.StatusFound)
			return
		}
		_, _ = w.Write([]byte("4cat-ui"))
	}))
	t.Cleanup(b.srv.Close)
	return b
}

func dialToBackend(backendAddr string) func(ctx context.Context, network, addr string) (net.Conn, error) {
	return func(ctx context.Context, network, _ string) (net.Conn, error) {
		var nd net.Dialer
		return nd.DialContext(ctx, network, backendAddr)
	}
}

func TestFourcatProxyAdminOnly(t *testing.T) {
	var mu sync.Mutex
	dials := 0
	fp := NewFourcatProxy("desk.example.org")
	fp.Dial = func(context.Context, string, string) (net.Conn, error) {
		mu.Lock()
		dials++
		mu.Unlock()
		return nil, errors.New("must not dial for a collector")
	}
	srv := newTestServerWith(t, func(d *Deps) { d.FourcatProxy = fp })

	rec := doReq(t, srv, http.MethodGet, "dmccool2", collectorRole, "/fourcat/", nil, nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("collector /fourcat/: status = %d, want 403", rec.Code)
	}
	if b := rec.Body.String(); !strings.Contains(b, testContact) {
		t.Errorf("403 body %q lacks the contact %q", b, testContact)
	}
	mu.Lock()
	defer mu.Unlock()
	if dials != 0 {
		t.Errorf("proxy dialled %d times for a collector, want 0", dials)
	}
}

func TestFourcatProxyHostAndPrefix(t *testing.T) {
	backend := newFourcatBackend(t)
	fp := NewFourcatProxy("desk.example.org")
	fp.Dial = dialToBackend(backend.srv.Listener.Addr().String())
	srv := newTestServerWith(t, func(d *Deps) { d.FourcatProxy = fp })

	rec := doReq(t, srv, http.MethodGet, "root", adminRole, "/fourcat/", nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("/fourcat/: status = %d, want 200 (body %q)", rec.Code, rec.Body.String())
	}
	if b := rec.Body.String(); b != "4cat-ui" {
		t.Errorf("body = %q, want the backend's 4cat-ui", b)
	}
	backend.mu.Lock()
	if len(backend.paths) != 1 || backend.paths[0] != "/" {
		t.Errorf("backend paths = %v, want [/]", backend.paths)
	}
	if len(backend.hosts) != 1 || backend.hosts[0] != "desk.example.org" {
		t.Errorf("backend Host = %v, want desk.example.org", backend.hosts)
	}
	if len(backend.xfh) != 1 || backend.xfh[0] != "desk.example.org" {
		t.Errorf("X-Forwarded-Host = %v, want desk.example.org", backend.xfh)
	}
	if len(backend.xfp) != 1 || backend.xfp[0] != "https" {
		t.Errorf("X-Forwarded-Proto = %v, want https", backend.xfp)
	}
	backend.mu.Unlock()

	// A backend redirect reaches the browser unmodified.
	rec = doReq(t, srv, http.MethodGet, "root", adminRole, "/fourcat/redirect", nil, nil)
	if rec.Code != http.StatusFound {
		t.Fatalf("/fourcat/redirect: status = %d, want 302", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/after-login" {
		t.Errorf("Location = %q, want /after-login (unmodified)", loc)
	}
}
