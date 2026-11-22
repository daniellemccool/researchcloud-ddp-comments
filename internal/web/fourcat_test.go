package web

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/daniellemccool/researchcloud-ddp-comments/internal/access"
	"github.com/daniellemccool/researchcloud-ddp-comments/internal/config"
	"github.com/daniellemccool/researchcloud-ddp-comments/internal/desklog"
	"github.com/daniellemccool/researchcloud-ddp-comments/internal/provision"
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
		COName:        testProject,
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

// fourcatBackend records what the proxy forwarded. /redirect answers a 302,
// /logs/ a page that prints the request's headers (as 4CAT's logs page does),
// everything else "4cat-ui"; every answer sets a cookie.
type fourcatBackend struct {
	srv     *httptest.Server
	mu      sync.Mutex
	paths   []string
	queries []string
	hosts   []string
	xfh     []string
	xfp     []string
	hdrs    []http.Header
}

func newFourcatBackend(t *testing.T) *fourcatBackend {
	t.Helper()
	b := &fourcatBackend{}
	b.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b.mu.Lock()
		b.paths = append(b.paths, r.URL.Path)
		b.queries = append(b.queries, r.URL.RawQuery)
		b.hosts = append(b.hosts, r.Host)
		b.xfh = append(b.xfh, r.Header.Get("X-Forwarded-Host"))
		b.xfp = append(b.xfp, r.Header.Get("X-Forwarded-Proto"))
		b.hdrs = append(b.hdrs, r.Header.Clone())
		b.mu.Unlock()
		w.Header().Add("Set-Cookie", "session=4cat; Path=/")
		switch r.URL.Path {
		case "/redirect":
			http.Redirect(w, r, "/after-login", http.StatusFound)
		case "/logs/", "/other/":
			page := "<pre>Authentication: " + r.Header.Get("Authentication") +
				"\nX-4CAT-Config-Via-Proxy: " + r.Header.Get("X-4CAT-Config-Via-Proxy") + "</pre>"
			w.Header().Set("Content-Length", strconv.Itoa(len(page)))
			_, _ = w.Write([]byte(page))
		default:
			_, _ = w.Write([]byte("4cat-ui"))
		}
	}))
	t.Cleanup(b.srv.Close)
	return b
}

func (b *fourcatBackend) count() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.paths)
}

func (b *fourcatBackend) last() (http.Header, string, string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.hdrs) == 0 {
		return nil, "", ""
	}
	n := len(b.hdrs) - 1
	return b.hdrs[n], b.paths[n], b.queries[n]
}

func dialToBackend(backendAddr string) func(ctx context.Context, network, addr string) (net.Conn, error) {
	return func(ctx context.Context, network, _ string) (net.Conn, error) {
		var nd net.Dialer
		return nd.DialContext(ctx, network, backendAddr)
	}
}

const testProxySecret = "0123456789abcdef0123456789abcdef"

// fakeStudy stands in for fourcat.Study: pending is 0 (setup done), 1
// (pending) or 2 (4CAT unreachable).
type fakeStudy struct {
	ready   atomic.Bool
	pending atomic.Int32
	asks    atomic.Int32
}

func (s *fakeStudy) Ready() bool { return s.ready.Load() }
func (s *fakeStudy) SetupPending(context.Context) (bool, error) {
	s.asks.Add(1)
	switch s.pending.Load() {
	case 1:
		return true, nil
	case 2:
		return false, errors.New("4CAT unreachable")
	}
	return false, nil
}

// Secret always answers, so the tests see the proxy's own Ready check.
func (s *fakeStudy) Secret() (string, error) { return testProxySecret, nil }

// tokenFor is the fake 64-hex API token of user.
func tokenFor(user string) string { return strings.Repeat(string(user[0]), 64)[:64] }

// fourcatRig is the gated server with the 4CAT proxy over a recording
// backend, a ready study and an in-memory token store.
type fourcatRig struct {
	srv      *http.Server
	fp       *FourcatProxy
	backend  *fourcatBackend
	study    *fakeStudy
	mu       sync.Mutex
	tokens   map[string]string
	tokenErr error
	makes    []string
	makeErr  string // when set, MakeAccount records this error on the token step
}

func newFourcatRig(t *testing.T) *fourcatRig {
	t.Helper()
	rig := &fourcatRig{backend: newFourcatBackend(t), study: &fakeStudy{}, tokens: map[string]string{}}
	rig.study.ready.Store(true)
	rig.fp = NewFourcatProxy(FourcatDeps{
		FQDN:    "desk.example.org",
		Contact: testContact,
		Band:    testProject,
		Study:   rig.study,
		Token: func(user string) (string, error) {
			rig.mu.Lock()
			defer rig.mu.Unlock()
			if rig.tokenErr != nil {
				return "", rig.tokenErr
			}
			if tok, ok := rig.tokens[user]; ok {
				return tok, nil
			}
			return "", provision.ErrNoToken
		},
		MakeAccount: func(_ context.Context, user string) provision.State {
			rig.mu.Lock()
			defer rig.mu.Unlock()
			rig.makes = append(rig.makes, user)
			if rig.makeErr != "" {
				return provision.State{Steps: map[string]string{"fourcat_account": "done", "fourcat_apitoken": "error: " + rig.makeErr}}
			}
			rig.tokens[user] = tokenFor(user)
			return provision.State{Steps: map[string]string{"fourcat_account": "done", "fourcat_apitoken": "done"}}
		},
	})
	rig.fp.Dial = dialToBackend(rig.backend.srv.Listener.Addr().String())
	rig.srv = newTestServerWith(t, func(d *Deps) { d.FourcatProxy = rig.fp })
	return rig
}

func (r *fourcatRig) setToken(user, tok string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tokens[user] = tok
}

func (r *fourcatRig) madeFor() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.makes...)
}

// sameOrigin is what a browser sends for a request from 4CAT's own pages.
var fromOwnPages = map[string]string{"Sec-Fetch-Site": "same-origin"}

func TestFourcatProxyAdminOnly(t *testing.T) {
	rig := newFourcatRig(t)
	rig.setToken("dmccool2", tokenFor("dmccool2"))
	rec := doReq(t, rig.srv, http.MethodGet, "dmccool2", collectorRole, "/fourcat/", nil, fromOwnPages)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("collector /fourcat/: status = %d, want 403", rec.Code)
	}
	if b := rec.Body.String(); !strings.Contains(b, testContact) {
		t.Errorf("403 body %q lacks the contact %q", b, testContact)
	}
	if n := rig.backend.count(); n != 0 {
		t.Errorf("4CAT saw %d requests for a collector, want 0", n)
	}
}

func TestFourcatProxyHostAndPrefix(t *testing.T) {
	rig := newFourcatRig(t)
	rig.setToken("root", tokenFor("root"))
	rec := doReq(t, rig.srv, http.MethodGet, "root", adminRole, "/fourcat/", nil, fromOwnPages)
	if rec.Code != http.StatusOK {
		t.Fatalf("/fourcat/: status = %d, want 200 (body %q)", rec.Code, rec.Body.String())
	}
	if b := rec.Body.String(); b != "4cat-ui" {
		t.Errorf("body = %q, want the backend's 4cat-ui", b)
	}
	b := rig.backend
	b.mu.Lock()
	if len(b.paths) != 1 || b.paths[0] != "/" {
		t.Errorf("backend paths = %v, want [/]", b.paths)
	}
	if len(b.hosts) != 1 || b.hosts[0] != "desk.example.org" {
		t.Errorf("backend Host = %v, want desk.example.org", b.hosts)
	}
	if len(b.xfh) != 1 || b.xfh[0] != "desk.example.org" {
		t.Errorf("X-Forwarded-Host = %v, want desk.example.org", b.xfh)
	}
	if len(b.xfp) != 1 || b.xfp[0] != "https" {
		t.Errorf("X-Forwarded-Proto = %v, want https", b.xfp)
	}
	hdrs := append([]http.Header(nil), b.hdrs...)
	b.mu.Unlock()
	// The gate secret and identity headers never reach 4CAT (F74).
	assertNoIdentityHeaders(t, hdrs)

	// A backend redirect reaches the browser unmodified.
	rec = doReq(t, rig.srv, http.MethodGet, "root", adminRole, "/fourcat/redirect", nil, fromOwnPages)
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
	rig := newFourcatRig(t)
	rig.setToken("root", tokenFor("root"))
	rig.fp.Dial = func(context.Context, string, string) (net.Conn, error) {
		return nil, errors.New("4cat-frontend is down")
	}
	rec := doReq(t, rig.srv, http.MethodGet, "root", adminRole, "/fourcat/", nil, fromOwnPages)
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

func TestFourcatSignsInWithOwnToken(t *testing.T) {
	rig := newFourcatRig(t)
	rig.setToken("alice", tokenFor("alice"))
	rig.setToken("bob", tokenFor("bob"))
	rec := doReq(t, rig.srv, http.MethodGet, "alice", adminRole, "/fourcat/", nil, fromOwnPages)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	h, _, _ := rig.backend.last()
	if a := h.Values("Authentication"); len(a) != 1 || a[0] != tokenFor("alice") {
		t.Error("4CAT did not get exactly alice's own token")
	}
}

func TestFourcatStandingHeaders(t *testing.T) {
	rig := newFourcatRig(t)
	for _, c := range []struct{ roles, tags string }{
		{adminRole, "studylead"},
		{techAdminRole, "studylead,techadmin"},
		{adminRole + ";" + techAdminRole, "studylead,techadmin"},
		{collectorRole + ";" + techAdminRole, "studylead,techadmin"},
	} {
		rig.setToken("alice", tokenFor("alice"))
		rec := doReq(t, rig.srv, http.MethodGet, "alice", c.roles, "/fourcat/", nil, fromOwnPages)
		if rec.Code != http.StatusOK {
			t.Fatalf("roles %q: status = %d", c.roles, rec.Code)
		}
		h, _, _ := rig.backend.last()
		if got := h.Values("X-4CAT-Config-Tag"); len(got) != 1 || got[0] != c.tags {
			t.Errorf("roles %q: tags %q, want exactly %q", c.roles, got, c.tags)
		}
		if got := h.Values("X-4CAT-Config-Via-Proxy"); len(got) != 1 || got[0] != testProxySecret {
			t.Errorf("roles %q: the proxy secret was not sent once", c.roles)
		}
	}
}

func TestFourcatStripsClientCredentials(t *testing.T) {
	rig := newFourcatRig(t)
	rig.setToken("alice", tokenFor("alice"))
	hdrs := map[string]string{
		"Sec-Fetch-Site": "same-origin",
		"Cookie":         "session=other", "Authentication": "evil", "Authorization": "evil",
		"X-4CAT-Config-Via-Proxy": "guess", "X-4CAT-Config-Tag": "admin",
		"X_4CAT_Config_Tag": "admin", "X_4cat_config_via_proxy": "guess",
	}
	rec := doReq(t, rig.srv, http.MethodGet, "alice", adminRole, "/fourcat/results/?access-token=x&keep=1&access%2Dtoken=y", nil, hdrs)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	h, path, query := rig.backend.last()
	if path != "/results/" || query != "keep=1" {
		t.Errorf("4CAT got %q?%q, want /results/?keep=1", path, query)
	}
	if a := h.Values("Authentication"); len(a) != 1 || a[0] != tokenFor("alice") {
		t.Error("Authentication is not exactly alice's token")
	}
	if got := h.Values("X-4CAT-Config-Tag"); len(got) != 1 || got[0] != "studylead" {
		t.Errorf("tags %q, want exactly studylead", got)
	}
	if got := h.Values("X-4CAT-Config-Via-Proxy"); len(got) != 1 || got[0] != testProxySecret {
		t.Error("the browser's proxy secret reached 4CAT")
	}
	for _, name := range []string{"Cookie", "Authorization"} {
		if v := h.Values(name); len(v) != 0 {
			t.Errorf("4CAT got %s %q", name, v)
		}
	}
	for name := range h {
		if strings.Contains(name, "_") {
			t.Errorf("4CAT got the header %q, spelled with an underscore", name)
		}
	}
	assertNoIdentityHeaders(t, []http.Header{h})
}

func TestFourcatDropsSetCookie(t *testing.T) {
	rig := newFourcatRig(t)
	rig.setToken("alice", tokenFor("alice"))
	rec := doReq(t, rig.srv, http.MethodGet, "alice", adminRole, "/fourcat/", nil, fromOwnPages)
	if sc := rec.Header().Values("Set-Cookie"); rec.Code != http.StatusOK || len(sc) != 0 {
		t.Errorf("status %d, Set-Cookie %q; want 200 and none", rec.Code, sc)
	}
}

func TestFourcatRefusesCrossSite(t *testing.T) {
	const fqdn = "https://desk.example.org"
	for _, c := range []struct {
		name, method, path string
		hdrs               map[string]string
		pass               bool
	}{
		{"cross-site GET", http.MethodGet, "/fourcat/results/", map[string]string{"Sec-Fetch-Site": "cross-site"}, false},
		{"cross-site POST", http.MethodPost, "/fourcat/results/", map[string]string{"Sec-Fetch-Site": "cross-site"}, false},
		{"same-site", http.MethodGet, "/fourcat/results/", map[string]string{"Sec-Fetch-Site": "same-site"}, false},
		{"same-origin", http.MethodPost, "/fourcat/results/", map[string]string{"Sec-Fetch-Site": "same-origin"}, true},
		{"typed address", http.MethodGet, "/fourcat/results/", map[string]string{"Sec-Fetch-Site": "none"}, true},
		{"none on an api route", http.MethodGet, "/fourcat/api/delete-dataset/?key=k", map[string]string{"Sec-Fetch-Site": "none"}, false},
		{"none on the api root", http.MethodGet, "/fourcat/api/", map[string]string{"Sec-Fetch-Site": "none"}, false},
		{"same-origin on an api route", http.MethodPost, "/fourcat/api/queue-processor/", map[string]string{"Sec-Fetch-Site": "same-origin"}, true},
		{"no header, our Origin, api route", http.MethodPost, "/fourcat/api/queue-processor/", map[string]string{"Origin": fqdn}, true},
		{"no header, other Origin", http.MethodPost, "/fourcat/results/", map[string]string{"Origin": "https://evil.test"}, false},
		{"no header, our Referer", http.MethodGet, "/fourcat/results/", map[string]string{"Referer": fqdn + "/results/"}, true},
		{"no header, our Origin", http.MethodPost, "/fourcat/results/", map[string]string{"Origin": fqdn}, true},
		{"no header, lookalike Origin", http.MethodPost, "/fourcat/results/", map[string]string{"Origin": "https://desk.example.org.evil.test"}, false},
		{"no header, lookalike Referer", http.MethodGet, "/fourcat/results/", map[string]string{"Referer": "https://evil.test/?u=desk.example.org"}, false},
		{"no header at all", http.MethodGet, "/fourcat/results/", nil, false},
		{"cross-site GET of the start page", http.MethodGet, "/fourcat/", map[string]string{"Sec-Fetch-Site": "cross-site"}, true},
		{"no header GET of the start page", http.MethodGet, "/fourcat/", nil, true},
		{"cross-site start page with a query", http.MethodGet, "/fourcat/?x=1", map[string]string{"Sec-Fetch-Site": "cross-site"}, false},
		{"cross-site POST of the start page", http.MethodPost, "/fourcat/", map[string]string{"Sec-Fetch-Site": "cross-site"}, false},
		{"cross-site one level deeper", http.MethodGet, "/fourcat/a/", map[string]string{"Sec-Fetch-Site": "cross-site"}, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			rig := newFourcatRig(t)
			rig.setToken("alice", tokenFor("alice"))
			rec := doReq(t, rig.srv, c.method, "alice", adminRole, c.path, nil, c.hdrs)
			if c.pass {
				if rec.Code != http.StatusOK || rig.backend.count() != 1 {
					t.Errorf("status %d, 4CAT saw %d; want proxied", rec.Code, rig.backend.count())
				}
				return
			}
			if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "Open 4CAT from the desk") {
				t.Errorf("status %d body %q; want 403 with the elsewhere page", rec.Code, rec.Body.String())
			}
			if n := rig.backend.count(); n != 0 {
				t.Errorf("4CAT saw %d requests, want 0", n)
			}
		})
	}
}

func TestFourcatPendingSetupPassesThrough(t *testing.T) {
	rig := newFourcatRig(t)
	rig.study.ready.Store(false)
	rig.study.pending.Store(1)
	hdrs := map[string]string{
		"Sec-Fetch-Site": "same-origin", "Cookie": "session=setup",
		"Authentication": "evil", "Authorization": "evil",
		"X-4CAT-Config-Via-Proxy": "guess", "X-4CAT-Config-Tag": "admin", "X_4CAT_Config_Tag": "admin",
	}
	rec := doReq(t, rig.srv, http.MethodPost, "alice", adminRole, "/fourcat/first-run/", nil, hdrs)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want the setup form proxied", rec.Code)
	}
	h, _, _ := rig.backend.last()
	if h.Get("Cookie") != "session=setup" {
		t.Errorf("Cookie %q, want the browser's", h.Get("Cookie"))
	}
	for _, name := range []string{"Authentication", "Authorization", "X-4CAT-Config-Via-Proxy", "X-4CAT-Config-Tag"} {
		if v := h.Values(name); len(v) != 0 {
			t.Errorf("4CAT got %s during setup", name)
		}
	}
	for name := range h {
		if strings.Contains(name, "_") {
			t.Errorf("4CAT got %q during setup", name)
		}
	}
	if sc := rec.Header().Values("Set-Cookie"); len(sc) != 1 {
		t.Errorf("Set-Cookie %q, want 4CAT's passed back", sc)
	}
	if len(rig.madeFor()) != 0 {
		t.Error("an account was made during setup")
	}
	// Still no cross-site requests.
	rec = doReq(t, rig.srv, http.MethodPost, "alice", adminRole, "/fourcat/first-run/", nil, map[string]string{"Sec-Fetch-Site": "cross-site"})
	if rec.Code != http.StatusForbidden || rig.backend.count() != 1 {
		t.Errorf("cross-site during setup: status %d, 4CAT saw %d; want 403 and nothing new", rec.Code, rig.backend.count())
	}
	// 4CAT not answering: the not-ready page, never the pass-through.
	rig.study.pending.Store(2)
	rec = doReq(t, rig.srv, http.MethodGet, "alice", adminRole, "/fourcat/", nil, fromOwnPages)
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "not ready") || rig.backend.count() != 1 {
		t.Errorf("unreachable: status %d, 4CAT saw %d; want 503 not-ready and nothing new", rec.Code, rig.backend.count())
	}
	// Ready: the pass-through is never used and SetupPending not asked.
	rig.study.ready.Store(true)
	rig.study.pending.Store(1)
	asks := rig.study.asks.Load()
	rec = doReq(t, rig.srv, http.MethodGet, "alice", adminRole, "/fourcat/", nil, fromOwnPages)
	h, _, _ = rig.backend.last()
	if rec.Code != http.StatusOK || h.Get("Authentication") != tokenFor("alice") || h.Get("Cookie") != "" {
		t.Errorf("ready: status %d; want the signed-in proxy", rec.Code)
	}
	if rig.study.asks.Load() != asks {
		t.Error("SetupPending was asked although the study is ready")
	}
}

func TestFourcatClosedUntilReady(t *testing.T) {
	rig := newFourcatRig(t)
	rig.study.ready.Store(false)
	rig.setToken("alice", tokenFor("alice"))
	rec := doReq(t, rig.srv, http.MethodGet, "alice", adminRole, "/fourcat/", nil, fromOwnPages)
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "not ready") {
		t.Errorf("status %d body %q; want 503 with the not-ready page", rec.Code, rec.Body.String())
	}
	if n := rig.backend.count(); n != 0 {
		t.Errorf("4CAT saw %d requests, want 0", n)
	}
}

func TestFourcatMakesAccountOnFirstVisit(t *testing.T) {
	rig := newFourcatRig(t)
	rec := doReq(t, rig.srv, http.MethodGet, "alice", techAdminRole, "/fourcat/", nil, fromOwnPages)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if got := rig.madeFor(); len(got) != 1 || got[0] != "alice" {
		t.Errorf("MakeAccount calls %q, want [alice]", got)
	}
	h, _, _ := rig.backend.last()
	if h.Get("Authentication") != tokenFor("alice") {
		t.Error("the new token was not sent")
	}
	// A failed account step: the not-ready page with the step's error.
	rig2 := newFourcatRig(t)
	rig2.makeErr = "4CAT account exists; ask an admin to adopt it"
	rec = doReq(t, rig2.srv, http.MethodGet, "bob", adminRole, "/fourcat/", nil, fromOwnPages)
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "ask an admin to adopt it") {
		t.Errorf("status %d body %q; want 503 naming the step's error", rec.Code, rec.Body.String())
	}
	if n := rig2.backend.count(); n != 0 {
		t.Errorf("4CAT saw %d requests, want 0", n)
	}
	// An unreadable token file: not-ready, no account made.
	rig3 := newFourcatRig(t)
	rig3.tokenErr = errors.New("permission denied")
	rec = doReq(t, rig3.srv, http.MethodGet, "carol", adminRole, "/fourcat/", nil, fromOwnPages)
	if rec.Code != http.StatusServiceUnavailable || len(rig3.madeFor()) != 0 || rig3.backend.count() != 0 {
		t.Errorf("status %d, makes %d, 4CAT saw %d; want 503, none, none", rec.Code, len(rig3.madeFor()), rig3.backend.count())
	}
}

// TestFourcatIgnoresOldCookieFile: with the real token reader, a token file
// that still holds an older release's login cookie (and the old step name)
// is not a token: the account step runs and the cookie is never sent.
func TestFourcatIgnoresOldCookieFile(t *testing.T) {
	root := t.TempDir()
	cfg := &config.Config{DeskRoot: root}
	tokenPath := filepath.Join(root, "secrets", "4cat-tokens", "alice", "4cat-token")
	statePath := provision.StatePath(cfg, "alice")
	for path, text := range map[string]string{
		tokenPath: "alice|old-cookie-value",
		statePath: `{"steps":{"fourcat_account":"done","fourcat_token":"done"}}`,
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	rig := newFourcatRig(t)
	var made atomic.Int32
	rig.fp.deps.Token = func(user string) (string, error) { return provision.APIToken(cfg, user) }
	rig.fp.deps.MakeAccount = func(_ context.Context, user string) provision.State {
		made.Add(1)
		if err := provision.WriteToken(cfg, user, tokenFor(user)); err != nil {
			t.Error(err)
		}
		data, _ := json.Marshal(provision.State{Steps: map[string]string{"fourcat_account": "done", "fourcat_apitoken": "done"}})
		if err := os.WriteFile(statePath, data, 0o600); err != nil {
			t.Error(err)
		}
		return provision.State{Steps: map[string]string{"fourcat_account": "done", "fourcat_apitoken": "done"}}
	}
	rec := doReq(t, rig.srv, http.MethodGet, "alice", adminRole, "/fourcat/", nil, fromOwnPages)
	if rec.Code != http.StatusOK || made.Load() != 1 {
		t.Fatalf("status %d, accounts made %d; want 200 after one MakeAccount", rec.Code, made.Load())
	}
	h, _, _ := rig.backend.last()
	if strings.Contains(h.Get("Authentication"), "old-cookie-value") || h.Get("Authentication") != tokenFor("alice") {
		t.Error("the old cookie was sent, or the new token was not")
	}
}

func TestFourcatReadsTokenEachRequest(t *testing.T) {
	rig := newFourcatRig(t)
	rig.setToken("alice", strings.Repeat("1", 64))
	doReq(t, rig.srv, http.MethodGet, "alice", adminRole, "/fourcat/", nil, fromOwnPages)
	rig.setToken("alice", strings.Repeat("2", 64))
	doReq(t, rig.srv, http.MethodGet, "alice", adminRole, "/fourcat/", nil, fromOwnPages)
	h, _, _ := rig.backend.last()
	if h.Get("Authentication") != strings.Repeat("2", 64) {
		t.Error("the second request did not carry the new token")
	}
}

func TestFourcatHidesSecretsOnLogsPage(t *testing.T) {
	rig := newFourcatRig(t)
	rig.setToken("alice", tokenFor("alice"))
	rec := doReq(t, rig.srv, http.MethodGet, "alice", adminRole, "/fourcat/logs/", nil,
		map[string]string{"Sec-Fetch-Site": "same-origin", "Accept-Encoding": "gzip"})
	body := rec.Body.String()
	if rec.Code != http.StatusOK || strings.Contains(body, tokenFor("alice")) || strings.Contains(body, testProxySecret) {
		t.Errorf("logs page: status %d body %q; want both values hidden", rec.Code, body)
	}
	if strings.Count(body, "(hidden)") != 2 {
		t.Errorf("logs page body %q, want (hidden) twice", body)
	}
	if cl := rec.Header().Get("Content-Length"); cl != strconv.Itoa(len(body)) {
		t.Errorf("Content-Length %q, body is %d bytes", cl, len(body))
	}
	h, _, _ := rig.backend.last()
	if ae := h.Get("Accept-Encoding"); ae != "" && ae != "identity" {
		t.Errorf("4CAT was asked for %q on the logs page, want an uncompressed answer", ae)
	}
	// Another page's body is passed through untouched.
	rec = doReq(t, rig.srv, http.MethodGet, "alice", adminRole, "/fourcat/other/", nil, fromOwnPages)
	if !strings.Contains(rec.Body.String(), tokenFor("alice")) {
		t.Error("a page other than /logs/ was rewritten")
	}
}

// TestTechAdminReachesAdminAndDesk: a Tech Admin who holds neither the
// collector nor the admin role is an admin (ADR-0004): they reach the admin
// page, the screen relay, the collector page and the 4CAT proxy. A member
// with none of the three roles gets the 403 page on all four and reaches no
// handler.
func TestTechAdminReachesAdminAndDesk(t *testing.T) {
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
	for _, c := range []struct{ path, body string }{
		{"/desk/admin/", "admin"},
		{"/desk/vnc/", "relay"},
		{"/fourcat/", "fourcat"},
	} {
		rec := doReq(t, srv, http.MethodGet, "techie", techAdminRole, c.path, nil, same)
		if rec.Code != http.StatusOK || rec.Body.String() != c.body {
			t.Errorf("Tech Admin %s: status %d body %q, want 200 %q", c.path, rec.Code, rec.Body.String(), c.body)
		}
	}
	// The collector page: this server has no provisioner, so it answers its
	// not-ready message; what matters is that it is not the 403 page.
	rec := doReq(t, srv, http.MethodGet, "techie", techAdminRole, "/desk/", nil, same)
	if rec.Code == http.StatusForbidden {
		t.Errorf("Tech Admin /desk/: status 403 body %q, want the collector page's own answer", rec.Body.String())
	}

	mu.Lock()
	before := reached["admin"] + reached["relay"] + reached["fourcat"]
	mu.Unlock()
	for _, path := range []string{"/desk/", "/desk/admin/", "/desk/vnc/", "/fourcat/"} {
		rec := doReq(t, srv, http.MethodGet, "stranger", "other_group", path, nil, same)
		if rec.Code != http.StatusForbidden {
			t.Errorf("other_group %s: status = %d, want 403", path, rec.Code)
		}
		if b := rec.Body.String(); !strings.Contains(b, testContact) {
			t.Errorf("other_group %s: body %q lacks the contact", path, b)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if after := reached["admin"] + reached["relay"] + reached["fourcat"]; after != before {
		t.Errorf("a member with no standing reached a handler: %v", reached)
	}
}

// The proxy's three refusal pages sit under the band named for the project
// (FourcatDeps.Band); the one for a link from elsewhere leads to the start
// page with an action link.
func TestFourcatPagesCarryTheProjectName(t *testing.T) {
	band := `<h1 class="band__title">` + testProject + `</h1>`

	rig := newFourcatRig(t)
	rig.setToken("alice", tokenFor("alice"))
	elsewhere := doReq(t, rig.srv, http.MethodGet, "alice", adminRole, "/fourcat/results/", nil, map[string]string{"Sec-Fetch-Site": "cross-site"}).Body.String()
	for _, want := range []string{band, `<a class="action" href="/">Open the start page`, "<title>Open 4CAT from the desk</title>"} {
		if !strings.Contains(elsewhere, want) {
			t.Errorf("elsewhere page lacks %q", want)
		}
	}

	if b := doReq(t, rig.srv, http.MethodGet, "dmccool2", collectorRole, "/fourcat/", nil, fromOwnPages).Body.String(); !strings.Contains(b, band) {
		t.Errorf("the proxy's 403 page lacks %q", band)
	}

	rig.study.ready.Store(false)
	if b := doReq(t, rig.srv, http.MethodGet, "alice", adminRole, "/fourcat/", nil, fromOwnPages).Body.String(); !strings.Contains(b, band) {
		t.Errorf("the proxy's not-ready page lacks %q", band)
	}
}
