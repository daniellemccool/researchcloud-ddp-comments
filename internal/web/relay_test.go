package web

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/daniellemccool/researchcloud-ddp-comments/internal/access"
	"github.com/daniellemccool/researchcloud-ddp-comments/internal/session"
)

// The real *session.Manager satisfies the relay's small interface (F54).
var _ runningChecker = (*session.Manager)(nil)

// fakeRunning is a runningChecker keyed by user.
type fakeRunning struct {
	running map[string]bool
}

func (f fakeRunning) IsRunning(_ context.Context, user string) bool {
	return f.running[user]
}

// withIdentity puts the gate-checked identity into the request context, as
// the gated listener's middleware does.
func withIdentity(r *http.Request, id access.Identity) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), ctxKey{}, id))
}

// dialRecorder records every dialled host and routes the connection to the
// matching httptest backend's real listener.
type dialRecorder struct {
	mu      sync.Mutex
	hosts   []string
	backend map[string]string // dialled host → backend listener address
}

func (d *dialRecorder) dial(ctx context.Context, network, addr string) (net.Conn, error) {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	d.mu.Lock()
	d.hosts = append(d.hosts, host)
	d.mu.Unlock()
	target, ok := d.backend[host]
	if !ok {
		return nil, fmt.Errorf("no backend for %s", host)
	}
	var nd net.Dialer
	return nd.DialContext(ctx, network, target)
}

func (d *dialRecorder) dialled() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.hosts...)
}

// screenBackend is a fake noVNC server: it counts requests, records the
// forwarded headers and answers with the owner's name.
type screenBackend struct {
	srv  *httptest.Server
	mu   sync.Mutex
	n    int
	hdrs []http.Header
}

func newScreenBackend(t *testing.T, name string) *screenBackend {
	t.Helper()
	b := &screenBackend{}
	b.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b.mu.Lock()
		b.n++
		b.hdrs = append(b.hdrs, r.Header.Clone())
		b.mu.Unlock()
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte(name + " " + r.URL.Path + "?" + r.URL.RawQuery))
	}))
	t.Cleanup(b.srv.Close)
	return b
}

func (b *screenBackend) requests() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.n
}

func (b *screenBackend) headers() []http.Header {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]http.Header(nil), b.hdrs...)
}

// assertNoIdentityHeaders fails when any recorded header set still holds one
// of the three identity headers. The names stay inside internal/access: if
// stripping changes the set, one of them was present.
func assertNoIdentityHeaders(t *testing.T, hs []http.Header) {
	t.Helper()
	for _, h := range hs {
		stripped := h.Clone()
		access.StripIdentityHeaders(stripped)
		if !reflect.DeepEqual(stripped, h) {
			t.Errorf("backend saw identity headers: %v", h)
		}
	}
}

func newTestRelay(t *testing.T, running map[string]bool, backends map[string]string) (*Relay, *dialRecorder) {
	t.Helper()
	r := NewRelay(fakeRunning{running: running})
	d := &dialRecorder{backend: backends}
	r.Dial = d.dial
	return r, d
}

func TestRelayTargetsIdentityNotPath(t *testing.T) {
	alice := newScreenBackend(t, "alice-screen")
	bob := newScreenBackend(t, "bob-screen")
	backends := map[string]string{
		"desk-firefox-alice": alice.srv.Listener.Addr().String(),
		"desk-firefox-bob":   bob.srv.Listener.Addr().String(),
	}
	relay, dials := newTestRelay(t, map[string]bool{"alice": true}, backends)
	id := access.Identity{User: "alice", Collector: true}

	// Every one of these paths must dial alice's container and never bob's:
	// the target comes from the verified identity, never from the request.
	paths := []string{
		"/desk/vnc/",
		"/desk/vnc/../bob/",
		"/desk/vnc/?user=bob",
		"/desk/vnc/bob/",
	}
	for _, p := range paths {
		req := httptest.NewRequest(http.MethodGet, p, nil)
		access.SetTestHeaders(req, "alice", collectorRole, testGate)
		rec := httptest.NewRecorder()
		relay.ServeHTTP(rec, withIdentity(req, id))
		if rec.Code != http.StatusOK {
			t.Errorf("%s: status = %d, want 200 (body %q)", p, rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), "alice-screen") {
			t.Errorf("%s: body %q is not alice's screen", p, rec.Body.String())
		}
	}
	for _, h := range dials.dialled() {
		if h != "desk-firefox-alice" {
			t.Errorf("dialled host %q, want only desk-firefox-alice", h)
		}
	}
	if n := bob.requests(); n != 0 {
		t.Errorf("bob's backend got %d requests, want 0", n)
	}
	// The gate secret and identity headers never reach the container (F74).
	assertNoIdentityHeaders(t, alice.headers())
}

func TestRelayConcurrentUsersNeverCross(t *testing.T) {
	alice := newScreenBackend(t, "alice-screen")
	bob := newScreenBackend(t, "bob-screen")
	backends := map[string]string{
		"desk-firefox-alice": alice.srv.Listener.Addr().String(),
		"desk-firefox-bob":   bob.srv.Listener.Addr().String(),
	}
	relay, dials := newTestRelay(t, map[string]bool{"alice": true, "bob": true}, backends)

	// 50 goroutines alternate alice and bob through the SAME relay; every
	// response body must be the requester's own screen. Run under -race.
	const n = 50
	var wg sync.WaitGroup
	errs := make(chan string, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			user := "alice"
			if i%2 == 1 {
				user = "bob"
			}
			req := httptest.NewRequest(http.MethodGet, "/desk/vnc/", nil)
			access.SetTestHeaders(req, user, collectorRole, testGate)
			rec := httptest.NewRecorder()
			relay.ServeHTTP(rec, withIdentity(req, access.Identity{User: user, Collector: true}))
			if rec.Code != http.StatusOK {
				errs <- fmt.Sprintf("%s: status = %d, want 200", user, rec.Code)
				return
			}
			if !strings.Contains(rec.Body.String(), user+"-screen") {
				errs <- fmt.Sprintf("%s: body %q is not the requester's screen", user, rec.Body.String())
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Error(e)
	}
	// The transport pools connections per target host, so fewer dials than
	// requests is fine; every dialled host must still be alice's or bob's.
	if got := len(dials.dialled()); got > n {
		t.Errorf("dialled %d times, want at most %d", got, n)
	}
	for _, h := range dials.dialled() {
		if h != "desk-firefox-alice" && h != "desk-firefox-bob" {
			t.Errorf("dialled host %q, want only alice's or bob's", h)
		}
	}
	assertNoIdentityHeaders(t, alice.headers())
	assertNoIdentityHeaders(t, bob.headers())
}

func TestRelayStripsPrefix(t *testing.T) {
	var mu sync.Mutex
	var seen []string
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.URL.Path+"?"+r.URL.RawQuery)
		mu.Unlock()
		_, _ = w.Write([]byte("ok"))
	}))
	t.Cleanup(backend.Close)
	relay, _ := newTestRelay(t, map[string]bool{"alice": true},
		map[string]string{"desk-firefox-alice": backend.Listener.Addr().String()})
	id := access.Identity{User: "alice", Collector: true}

	for _, p := range []string{"/desk/vnc/", "/desk/vnc/websockify?x=1"} {
		req := httptest.NewRequest(http.MethodGet, p, nil)
		rec := httptest.NewRecorder()
		relay.ServeHTTP(rec, withIdentity(req, id))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status = %d, want 200", p, rec.Code)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	want := []string{"/?", "/websockify?x=1"}
	if len(seen) != len(want) {
		t.Fatalf("backend saw %v, want %v", seen, want)
	}
	for i := range want {
		if seen[i] != want[i] {
			t.Errorf("backend saw %q, want %q", seen[i], want[i])
		}
	}
}

func TestRelayPassesWebsocketUpgrade(t *testing.T) {
	// A backend that answers the upgrade with 101 and echoes one frame.
	wsBackend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hj, ok := w.(http.Hijacker)
		if !ok {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		conn, rw, err := hj.Hijack()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		_, _ = rw.WriteString("HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n\r\n")
		_ = rw.Flush()
		buf := make([]byte, 128)
		n, err := conn.Read(buf)
		if err != nil || n == 0 {
			return
		}
		_, _ = conn.Write(buf[:n])
	}))
	t.Cleanup(wsBackend.Close)

	relay, _ := newTestRelay(t, map[string]bool{"alice": true},
		map[string]string{"desk-firefox-alice": wsBackend.Listener.Addr().String()})
	id := access.Identity{User: "alice", Collector: true}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		relay.ServeHTTP(w, withIdentity(r, id))
	}))
	t.Cleanup(srv.Close)

	conn, err := net.Dial("tcp", srv.Listener.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = conn.Close() }()
	_, err = fmt.Fprintf(conn, "GET /desk/vnc/websockify HTTP/1.1\r\nHost: %s\r\nConnection: Upgrade\r\nUpgrade: websocket\r\n\r\n",
		srv.Listener.Addr().String())
	if err != nil {
		t.Fatalf("write request: %v", err)
	}
	br := bufio.NewReader(conn)
	line, err := br.ReadString('\n')
	if err != nil {
		t.Fatalf("read status line: %v", err)
	}
	if !strings.Contains(line, "101") {
		t.Fatalf("status line %q, want 101", line)
	}
	// Drain the rest of the response head.
	for {
		l, err := br.ReadString('\n')
		if err != nil {
			t.Fatalf("read head: %v", err)
		}
		if l == "\r\n" {
			break
		}
	}
	frame := []byte("one-frame")
	if _, err := conn.Write(frame); err != nil {
		t.Fatalf("write frame: %v", err)
	}
	echo := make([]byte, len(frame))
	if _, err := io.ReadFull(br, echo); err != nil {
		t.Fatalf("read echo: %v", err)
	}
	if string(echo) != string(frame) {
		t.Errorf("echo = %q, want %q", echo, frame)
	}
}

func TestRelay409WhenNotRunning(t *testing.T) {
	relay, dials := newTestRelay(t, map[string]bool{"alice": false}, map[string]string{})
	id := access.Identity{User: "alice", Collector: true}
	req := httptest.NewRequest(http.MethodGet, "/desk/vnc/", nil)
	rec := httptest.NewRecorder()
	relay.ServeHTTP(rec, withIdentity(req, id))
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", rec.Code)
	}
	if b := rec.Body.String(); b != "screen not running" {
		t.Errorf("body = %q, want %q", b, "screen not running")
	}
	if n := len(dials.dialled()); n != 0 {
		t.Errorf("dialled %d times, want 0 when the screen is not running", n)
	}
}

func TestRelay502OnDialFailure(t *testing.T) {
	r := NewRelay(fakeRunning{running: map[string]bool{"alice": true}})
	r.Dial = func(context.Context, string, string) (net.Conn, error) {
		return nil, errors.New("connection refused")
	}
	id := access.Identity{User: "alice", Collector: true}
	req := httptest.NewRequest(http.MethodGet, "/desk/vnc/", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, withIdentity(req, id))
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", rec.Code)
	}
}

// The collector's browser cookies and Authorization never reach the Firefox
// container: noVNC needs neither and the container is the least-trusted
// component (review M1).
func TestRelayDropsCookieAndAuthorization(t *testing.T) {
	alice := newScreenBackend(t, "alice-screen")
	relay, _ := newTestRelay(t, map[string]bool{"alice": true}, map[string]string{
		"desk-firefox-alice": alice.srv.Listener.Addr().String(),
	})
	req := httptest.NewRequest(http.MethodGet, "/desk/vnc/", nil)
	access.SetTestHeaders(req, "alice", collectorRole, testGate)
	req.Header.Set("Cookie", "session=sram-secret")
	req.Header.Set("Authorization", "Bearer secret")
	rec := httptest.NewRecorder()
	relay.ServeHTTP(rec, withIdentity(req, access.Identity{User: "alice", Collector: true}))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %q)", rec.Code, rec.Body.String())
	}
	hs := alice.headers()
	if len(hs) != 1 {
		t.Fatalf("backend saw %d requests, want 1", len(hs))
	}
	for _, name := range []string{"Cookie", "Authorization"} {
		if v := hs[0].Values(name); len(v) != 0 {
			t.Errorf("backend saw %s = %q, want none", name, v)
		}
	}
}
