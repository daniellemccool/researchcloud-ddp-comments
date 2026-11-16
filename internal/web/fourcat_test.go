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
		Access:   access.Config{CollectorRole: collectorRole, AdminRole: adminRole, TechAdminRole: techAdminRole, GateSecret: testGate},
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
	hdrs  []http.Header
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
		b.hdrs = append(b.hdrs, r.Header.Clone())
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
	hdrs := append([]http.Header(nil), backend.hdrs...)
	backend.mu.Unlock()
	// The gate secret and identity headers never reach 4CAT (F74); the
	// inbound request carried them (doReq sets them via SetTestHeaders).
	assertNoIdentityHeaders(t, hdrs)

	// A backend redirect reaches the browser unmodified.
	rec = doReq(t, srv, http.MethodGet, "root", adminRole, "/fourcat/redirect", nil, nil)
	if rec.Code != http.StatusFound {
		t.Fatalf("/fourcat/redirect: status = %d, want 302", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/after-login" {
		t.Errorf("Location = %q, want /after-login (unmodified)", loc)
	}
}

// A dial failure answers the relay's plain 502, not the stdlib's empty one
// (review M6).
func TestFourcatProxyPlainBadGateway(t *testing.T) {
	fp := NewFourcatProxy("desk.example.org")
	fp.Dial = func(context.Context, string, string) (net.Conn, error) {
		return nil, errors.New("4cat-frontend is down")
	}
	srv := newTestServerWith(t, func(d *Deps) { d.FourcatProxy = fp })
	rec := doReq(t, srv, http.MethodGet, "root", adminRole, "/fourcat/", nil, nil)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", rec.Code)
	}
	if b := rec.Body.String(); b != "bad gateway" {
		t.Errorf("body = %q, want %q", b, "bad gateway")
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/plain; charset=utf-8" {
		t.Errorf("Content-Type = %q, want text/plain; charset=utf-8", ct)
	}
}

// TestTechAdminOnlyReachesOnlyFourcat: a Tech Admin who is neither collector
// nor admin gets the 403 page on every path outside /fourcat/ (the admin,
// screen and claim handlers do not check the collector role themselves) and
// reaches the 4CAT proxy.
func TestTechAdminOnlyReachesOnlyFourcat(t *testing.T) {
	var mu sync.Mutex
	reached := map[string]int{}
	stub := func(name string) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			reached[name]++
			mu.Unlock()
			_, _ = w.Write([]byte(name))
		})
	}
	srv := newTestServerWith(t, func(d *Deps) {
		d.FourcatProxy = stub("fourcat")
		d.Relay = stub("relay")
		d.Admin = stub("admin")
	})
	same := map[string]string{"Origin": "https://desk.example.org", "Sec-Fetch-Site": "same-origin"}
	for _, c := range []struct{ method, path string }{
		{http.MethodGet, "/desk/"},
		{http.MethodGet, "/desk/admin/"},
		{http.MethodGet, "/desk/vnc/"},
		{http.MethodPost, "/desk/claim"},
	} {
		rec := doReq(t, srv, c.method, "techie", techAdminRole, c.path, nil, same)
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s %s: status = %d, want 403", c.method, c.path, rec.Code)
		}
		if b := rec.Body.String(); !strings.Contains(b, testContact) {
			t.Errorf("%s %s: body %q lacks the contact", c.method, c.path, b)
		}
	}
	// A path that only starts with /fourcat/ is cleaned by the mux into a
	// redirect; it never reaches another handler.
	rec := doReq(t, srv, http.MethodGet, "techie", techAdminRole, "/fourcat/../desk/admin/", nil, same)
	if rec.Code == http.StatusOK {
		t.Errorf("/fourcat/../desk/admin/: status 200 body %q, want no handler reached", rec.Body.String())
	}
	// /fourcat without the slash gets the mux's redirect, as it does for an admin.
	rec = doReq(t, srv, http.MethodGet, "techie", techAdminRole, "/fourcat", nil, same)
	if rec.Code < 300 || rec.Code > 399 || rec.Header().Get("Location") != "/fourcat/" {
		t.Errorf("/fourcat: status %d Location %q, want a redirect to /fourcat/", rec.Code, rec.Header().Get("Location"))
	}
	rec = doReq(t, srv, http.MethodGet, "techie", techAdminRole, "/fourcat/", nil, same)
	if rec.Code != http.StatusOK || rec.Body.String() != "fourcat" {
		t.Errorf("/fourcat/: status %d body %q, want the proxy", rec.Code, rec.Body.String())
	}
	mu.Lock()
	defer mu.Unlock()
	if reached["relay"] != 0 || reached["admin"] != 0 || reached["fourcat"] != 1 {
		t.Errorf("handlers reached %v, want only fourcat once", reached)
	}
}
