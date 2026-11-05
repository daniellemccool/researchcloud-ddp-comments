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
	"time"

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
	r := NewRelay(fakeRunning{running: running}, NewActivity(time.Now))
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
	r := NewRelay(fakeRunning{running: map[string]bool{"alice": true}}, NewActivity(time.Now))
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

// wsPipeBackend is an in-memory noVNC server behind the Dial seam: every
// dial gets one end of a net.Pipe, served by a goroutine that answers plain
// requests with keep-alive and a websocket upgrade with 101, after which it
// echoes until the relay closes the connection.
type wsPipeBackend struct {
	mu    sync.Mutex
	hosts []string
}

func (b *wsPipeBackend) dial(_ context.Context, _, addr string) (net.Conn, error) {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	b.mu.Lock()
	b.hosts = append(b.hosts, host)
	b.mu.Unlock()
	client, server := net.Pipe()
	go b.serve(server, host)
	return client, nil
}

func (b *wsPipeBackend) serve(c net.Conn, host string) {
	defer func() { _ = c.Close() }()
	br := bufio.NewReader(c)
	for {
		req, err := http.ReadRequest(br)
		if err != nil {
			return
		}
		_, _ = io.Copy(io.Discard, req.Body)
		_ = req.Body.Close()
		if strings.EqualFold(req.Header.Get("Upgrade"), "websocket") {
			_, _ = io.WriteString(c, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n\r\n")
			_, _ = io.Copy(c, br)
			return
		}
		body := host + " screen"
		_, _ = fmt.Fprintf(c, "HTTP/1.1 200 OK\r\nContent-Type: text/plain\r\nContent-Length: %d\r\n\r\n%s", len(body), body)
	}
}

// pipeRelay is a relay whose screens are wsPipeBackends and whose tracker
// runs on clock, plus one gated test server per user wrapping that relay.
func pipeRelay(t *testing.T, clock func() time.Time, users ...string) (*Relay, map[string]string) {
	t.Helper()
	running := map[string]bool{}
	for _, u := range users {
		running[u] = true
	}
	r := NewRelay(fakeRunning{running: running}, NewActivity(clock))
	r.Dial = (&wsPipeBackend{}).dial
	addrs := map[string]string{}
	for _, u := range users {
		id := access.Identity{User: u, Collector: true}
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			r.ServeHTTP(w, withIdentity(req, id))
		}))
		t.Cleanup(srv.Close)
		addrs[u] = srv.Listener.Addr().String()
	}
	return r, addrs
}

// openScreenConn opens a websocket screen through the relay at addr and
// returns the upgraded connection once the 101 head has been read.
func openScreenConn(t *testing.T, addr string) (net.Conn, *bufio.Reader) {
	t.Helper()
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	_, err = fmt.Fprintf(conn, "GET /desk/vnc/websockify HTTP/1.1\r\nHost: %s\r\nConnection: Upgrade\r\nUpgrade: WebSocket\r\n\r\n", addr)
	if err != nil {
		t.Fatalf("write upgrade: %v", err)
	}
	br := bufio.NewReader(conn)
	line, err := br.ReadString('\n')
	if err != nil {
		t.Fatalf("read status line: %v", err)
	}
	if !strings.Contains(line, "101") {
		t.Fatalf("status line %q, want 101", line)
	}
	for {
		l, err := br.ReadString('\n')
		if err != nil {
			t.Fatalf("read head: %v", err)
		}
		if l == "\r\n" {
			return conn, br
		}
	}
}

// assertScreenOpen proves the upgraded connection still carries frames.
func assertScreenOpen(t *testing.T, conn net.Conn, br *bufio.Reader) {
	t.Helper()
	frame := []byte("still-here")
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
	defer func() { _ = conn.SetDeadline(time.Time{}) }()
	if _, err := conn.Write(frame); err != nil {
		t.Fatalf("write frame: %v", err)
	}
	echo := make([]byte, len(frame))
	if _, err := io.ReadFull(br, echo); err != nil {
		t.Fatalf("read echo: %v (screen closed?)", err)
	}
}

// assertScreenEOF fails unless the connection reads EOF within 2 s.
func assertScreenEOF(t *testing.T, conn net.Conn, br *bufio.Reader) {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, err := br.ReadByte()
	if !errors.Is(err, io.EOF) {
		t.Fatalf("read after close = %v, want EOF within 2s", err)
	}
}

// waitOpen waits up to 2 s for user's open-screen count to reach want.
func waitOpen(t *testing.T, a *Activity, user string, want int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		_, open, _ := activitySnapshot(a, user)
		if open == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s open screens = %d, want %d", user, open, want)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestCloseUserEndsOpenScreen(t *testing.T) {
	relay, addrs := pipeRelay(t, time.Now, "alice")
	conn, br := openScreenConn(t, addrs["alice"])
	assertScreenOpen(t, conn, br)
	if n := relay.activity.CloseUser("alice"); n != 1 {
		t.Fatalf("CloseUser(alice) = %d, want 1", n)
	}
	assertScreenEOF(t, conn, br)
	waitOpen(t, relay.activity, "alice", 0)
}

func TestCloseUserLeavesOtherUsers(t *testing.T) {
	relay, addrs := pipeRelay(t, time.Now, "alice", "bob")
	aConn, aBR := openScreenConn(t, addrs["alice"])
	bConn, bBR := openScreenConn(t, addrs["bob"])
	if n := relay.activity.CloseUser("alice"); n != 1 {
		t.Fatalf("CloseUser(alice) = %d, want 1", n)
	}
	assertScreenEOF(t, aConn, aBR)
	assertScreenOpen(t, bConn, bBR)
	if _, open, _ := activitySnapshot(relay.activity, "bob"); open != 1 {
		t.Errorf("bob open screens = %d, want 1", open)
	}
}

func TestOnlyUpgradesCountAsScreens(t *testing.T) {
	c := newFakeClock()
	relay, addrs := pipeRelay(t, c.now, "alice")

	// A plain asset GET over a kept-alive client connection is not a screen,
	// neither while the request runs nor while the connection idles.
	client := &http.Client{Transport: &http.Transport{}}
	t.Cleanup(client.CloseIdleConnections)
	for i := 0; i < 2; i++ {
		resp, err := client.Get("http://" + addrs["alice"] + "/desk/vnc/vnc.html")
		if err != nil {
			t.Fatalf("plain GET: %v", err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("plain GET status = %d, want 200", resp.StatusCode)
		}
	}
	if _, open, _ := activitySnapshot(relay.activity, "alice"); open != 0 {
		t.Fatalf("open screens after plain GETs = %d, want 0", open)
	}

	// An upgrade is a screen; its close lowers the count and stamps the close.
	conn, br := openScreenConn(t, addrs["alice"])
	assertScreenOpen(t, conn, br)
	waitOpen(t, relay.activity, "alice", 1)
	c.advance(time.Hour)
	closedAt := c.now()
	_ = conn.Close()
	waitOpen(t, relay.activity, "alice", 0)
	if _, _, last := activitySnapshot(relay.activity, "alice"); !last.Equal(closedAt) {
		t.Errorf("lastScreenClose = %v, want %v", last, closedAt)
	}
}

// absentView is the relay's poll view in the tests.
type absentView map[string]bool

func (a absentView) Absent(user string) bool { return a[user] }

// A user absent from the last good CO list gets 403 before the running
// check, so noVNC's auto-reconnect cannot reopen a screen during a revoke.
func TestRelayRefusesAbsentUser(t *testing.T) {
	alice := newScreenBackend(t, "alice-screen")
	relay, dials := newTestRelay(t, map[string]bool{"alice": true, "bob": true},
		map[string]string{"desk-firefox-alice": alice.srv.Listener.Addr().String()})
	relay.Members = absentView{"alice": true}
	req := httptest.NewRequest(http.MethodGet, "/desk/vnc/", nil)
	rec := httptest.NewRecorder()
	relay.ServeHTTP(rec, withIdentity(req, access.Identity{User: "alice", Collector: true}))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
	if n := len(dials.dialled()); n != 0 {
		t.Errorf("dialled %d times, want 0 for an absent user", n)
	}
	if n := alice.requests(); n != 0 {
		t.Errorf("alice's backend got %d requests, want 0", n)
	}
	if n := relay.activity.CloseUser("alice"); n != 0 {
		t.Errorf("an absent user's refused request left %d open screens", n)
	}
}
