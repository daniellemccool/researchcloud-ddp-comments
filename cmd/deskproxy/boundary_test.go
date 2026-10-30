package main

import (
	"context"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/daniellemccool/researchcloud-ddp-comments/internal/platform"
)

const testFQDN = "checkjefeed.example.org"

func mkReq(t *testing.T, method, rawurl string) *http.Request {
	t.Helper()
	u, err := url.Parse(rawurl)
	if err != nil {
		t.Fatal(err)
	}
	return &http.Request{Method: method, URL: u, Host: u.Host, Header: http.Header{}}
}

// TestAllowlistExact is ADR-0001's enforcement: an exact method-and-path table
// for fourcat.desk. Adding a route means adding a row here with a reason.
func TestAllowlistExact(t *testing.T) {
	for _, tc := range []struct {
		name, method, url string
		want              verdict
	}{
		{"upload route", "POST", "https://fourcat.desk/api/import-dataset/", forward4CAT},
		{"poll route", "GET", "https://fourcat.desk/api/check-query/", forward4CAT},
		{"poll route with query", "GET", "https://fourcat.desk/api/check-query/?key=abc", forward4CAT},
		{"upload route, explicit port", "POST", "https://fourcat.desk:443/api/import-dataset/", forward4CAT},
		{"upload route, upper-case host", "POST", "https://FourCat.Desk./api/import-dataset/", forward4CAT},
		{"wrong method on upload", "GET", "https://fourcat.desk/api/import-dataset/", deny},
		{"wrong method on poll", "POST", "https://fourcat.desk/api/check-query/", deny},
		{"root", "GET", "https://fourcat.desk/", deny},
		{"poll without trailing slash", "GET", "https://fourcat.desk/api/check-query", deny},
		{"upload without trailing slash", "POST", "https://fourcat.desk/api/import-dataset", deny},
		{"upload with query", "POST", "https://fourcat.desk/api/import-dataset/?x=1", deny},
		{"sub-path of poll", "GET", "https://fourcat.desk/api/check-query/x", deny},
		{"encoded slash", "GET", "https://fourcat.desk/api/check-query%2F", deny},
		{"dot segment is not normalised into the route", "GET", "https://fourcat.desk/api/../api/check-query/", deny},
		{"another 4CAT api route", "GET", "https://fourcat.desk/api/list-datasets/", deny},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := classify(mkReq(t, tc.method, tc.url), testFQDN); got != tc.want {
				t.Errorf("%s %s: got %v, want %v", tc.method, tc.url, got, tc.want)
			}
		})
	}
}

func TestInternalHostsDenied(t *testing.T) {
	denied := []string{
		"localhost", "LOCALHOST", "localhost.", "foo.localhost", "127.0.0.1", "[::1]", "[::ffff:10.0.0.1]",
		"10.0.0.5", "172.18.0.1", "frontend", "other.desk", "fourcat.desk.evil.desk", "",
	}
	allowed := []string{"www.tiktok.com", "example.org", testFQDN, strings.ToUpper(testFQDN)}

	for _, tc := range []struct {
		hosts []string
		want  verdict
	}{{denied, deny}, {allowed, allow}} {
		for _, h := range tc.hosts {
			for _, withPort := range []bool{false, true} {
				hostport := h
				if withPort {
					hostport += ":443"
				}
				req := &http.Request{
					Method: "GET",
					URL:    &url.URL{Scheme: "https", Host: hostport, Path: "/api/x"},
					Host:   hostport,
					Header: http.Header{},
				}
				if got := classify(req, testFQDN); got != tc.want {
					t.Errorf("%q: got %v, want %v", hostport, got, tc.want)
				}
			}
		}
	}

	// A request with no URL host falls back to the Host header.
	req := &http.Request{Method: "GET", URL: &url.URL{Path: "/"}, Host: "localhost:8080", Header: http.Header{}}
	if got := classify(req, testFQDN); got != deny {
		t.Errorf("Host-header localhost: got %v, want deny", got)
	}
	// The zero verdict is deny: nothing is allowed by default.
	var zero verdict
	if zero != deny {
		t.Error("zero verdict must be deny")
	}
}

func TestInwardAddr(t *testing.T) {
	for _, tc := range []struct {
		ip   string
		want bool
	}{
		{"127.0.0.1", true}, {"127.9.9.9", true}, {"::1", true},
		{"10.0.0.5", true}, {"172.16.0.1", true}, {"172.18.0.4", true}, {"172.31.255.255", true},
		{"192.168.1.1", true},
		{"169.254.169.254", true}, {"fe80::1", true},
		{"fc00::1", true}, {"fd12:3456::1", true},
		{"0.0.0.0", true}, {"::", true},
		{"::ffff:127.0.0.1", true}, {"::ffff:10.1.2.3", true}, {"::ffff:172.18.0.4", true}, {"::ffff:169.254.1.1", true},
		{"93.184.216.34", false}, {"172.15.0.1", false}, {"172.32.0.1", false}, {"192.169.0.1", false},
		{"2606:2800:220:1::1", false}, {"::ffff:93.184.216.34", false},
	} {
		ip := net.ParseIP(tc.ip)
		if ip == nil {
			t.Fatalf("bad test ip %q", tc.ip)
		}
		if got := inwardAddr(ip); got != tc.want {
			t.Errorf("inwardAddr(%s) = %v, want %v", tc.ip, got, tc.want)
		}
	}
	if !inwardAddr(nil) {
		t.Error("nil address must be refused")
	}
}

type fakeResolver map[string][]string

func (f fakeResolver) LookupIPAddr(_ context.Context, host string) ([]net.IPAddr, error) {
	addrs, ok := f[host]
	if !ok {
		return nil, errors.New("no such host")
	}
	var out []net.IPAddr
	for _, a := range addrs {
		out = append(out, net.IPAddr{IP: net.ParseIP(a)})
	}
	return out, nil
}

// TestResolvedInwardDenied: every resolved address is checked and the checked
// address is the one dialled, never the name.
func TestResolvedInwardDenied(t *testing.T) {
	res := fakeResolver{
		"evil.example":  {"172.18.0.4"},
		"good.example":  {"93.184.216.34"},
		"mixed.example": {"93.184.216.34", "10.0.0.9"},
		"mapped.example": {
			"::ffff:192.168.0.1",
		},
	}
	var dialled []string
	dial := newInwardDial(res, func(_ context.Context, _, addr string) (net.Conn, error) {
		dialled = append(dialled, addr)
		c1, c2 := net.Pipe()
		_ = c2.Close()
		return c1, nil
	})

	for _, h := range []string{"evil.example", "mixed.example", "mapped.example", "127.0.0.1", "[::1]"} {
		if _, err := dial(context.Background(), "tcp", net.JoinHostPort(strings.Trim(h, "[]"), "443")); !errors.Is(err, errInward) {
			t.Errorf("%s: err %v, want errInward", h, err)
		}
	}
	if len(dialled) != 0 {
		t.Fatalf("dialled refused destinations: %v", dialled)
	}

	c, err := dial(context.Background(), "tcp", "good.example:443")
	if err != nil {
		t.Fatalf("good.example: %v", err)
	}
	_ = c.Close()
	if len(dialled) != 1 || dialled[0] != "93.184.216.34:443" {
		t.Errorf("dialled %v, want the checked address 93.184.216.34:443", dialled)
	}

	// Resolver failure denies; it never falls through to dialling the name.
	dialled = nil
	if _, err := dial(context.Background(), "tcp", "nxdomain.example:443"); err == nil || errors.Is(err, errInward) {
		t.Errorf("resolver error: got %v, want a plain error", err)
	}
	if len(dialled) != 0 {
		t.Errorf("dialled after resolver failure: %v", dialled)
	}
}

// testRig is a boundary and the capture proxy around it, listening on httptest.
type testRig struct {
	proxyURL *url.URL
	client   *http.Client
	token    string // path of the token file
	logs     *lockedBuf
}

func newRig(t *testing.T, upstream string, tokenContent *string, res resolver) *testRig {
	t.Helper()
	tokPath := filepath.Join(t.TempDir(), "token")
	if tokenContent != nil {
		if err := os.WriteFile(tokPath, []byte(*tokenContent), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	up, err := url.Parse(upstream)
	if err != nil {
		t.Fatal(err)
	}
	b := newBoundary(testFQDN, newTokenFile(tokPath), up, res)

	out, err := openRecordWriter(filepath.Join(t.TempDir(), "captures.ndjson"))
	if err != nil {
		t.Fatal(err)
	}
	p := &platform.Platform{Name: "test", DefaultKind: "video", Hosts: []string{"capture.example"}}
	proxy := newProxy(p, Meta{User: "u", Session: "s", Platform: "test", Kind: "video"}, out, b)
	srv := httptest.NewServer(proxy)
	t.Cleanup(srv.Close)

	logs := &lockedBuf{}
	log.SetOutput(logs)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	pu, _ := url.Parse(srv.URL)
	return &testRig{
		proxyURL: pu,
		client: &http.Client{
			Transport: &http.Transport{Proxy: http.ProxyURL(pu)},
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		token: tokPath,
		logs:  logs,
	}
}

func strp(s string) *string { return &s }

func (r *testRig) do(t *testing.T, method, rawurl string, hdr map[string]string, body string) (*http.Response, string) {
	t.Helper()
	req, err := http.NewRequest(method, rawurl, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := r.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp, string(b)
}

func assertDenied(t *testing.T, resp *http.Response, body string) {
	t.Helper()
	if resp.StatusCode != 403 || body != "deskproxy: denied by rule" {
		t.Errorf("got %d %q, want 403 %q", resp.StatusCode, body, "deskproxy: denied by rule")
	}
}

func TestCookieReplaced(t *testing.T) {
	var cookies []string
	var host, ae, other, gotBody, path string
	var hits atomic.Int32
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		cookies = r.Header.Values("Cookie")
		host, ae, other, path = r.Host, r.Header.Get("Accept-Encoding"), r.Header.Get("X-Other"), r.URL.RequestURI()
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(201)
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	defer up.Close()

	rig := newRig(t, up.URL, strp("abc\n"), nil)
	resp, body := rig.do(t, "POST", "http://fourcat.desk/api/import-dataset/", map[string]string{
		"Cookie": "x=y", "X-Other": "kept", "Accept-Encoding": "gzip, br",
	}, "payload")

	if resp.StatusCode != 201 || body != `{"ok":true}` || resp.Header.Get("Content-Type") != "application/json" {
		t.Errorf("response %d %q %v", resp.StatusCode, body, resp.Header)
	}
	if len(cookies) != 1 || cookies[0] != "remember_token=abc" {
		t.Errorf("upstream saw Cookie %q, want exactly remember_token=abc", cookies)
	}
	if host != "fourcat.desk" {
		t.Errorf("upstream Host %q, want fourcat.desk", host)
	}
	if other != "kept" || ae != "gzip, br" || gotBody != "payload" || path != "/api/import-dataset/" {
		t.Errorf("headers/body/path not passed through: other=%q ae=%q body=%q path=%q", other, ae, gotBody, path)
	}

	// The poll route carries its query through.
	resp, _ = rig.do(t, "GET", "http://fourcat.desk/api/check-query/?key=k1", nil, "")
	if resp.StatusCode != 201 || path != "/api/check-query/?key=k1" || len(cookies) != 1 || cookies[0] != "remember_token=abc" {
		t.Errorf("poll: %d path=%q cookies=%q", resp.StatusCode, path, cookies)
	}

	if strings.Contains(rig.logs.String(), "abc") {
		t.Errorf("token leaked into the log: %s", rig.logs.String())
	}
	if n := strings.Count(rig.logs.String(), "\n"); n != 2 {
		t.Errorf("want one log line per request (2), got %d: %q", n, rig.logs.String())
	}
}

func TestRedirectReturnedNotFollowed(t *testing.T) {
	var hits atomic.Int32
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		http.Redirect(w, r, "/login/", http.StatusFound)
	}))
	defer up.Close()

	rig := newRig(t, up.URL, strp("abc"), nil)
	resp, _ := rig.do(t, "GET", "http://fourcat.desk/api/check-query/?key=k", nil, "")
	if resp.StatusCode != 302 || resp.Header.Get("Location") != "/login/" {
		t.Errorf("got %d Location %q, want 302 /login/", resp.StatusCode, resp.Header.Get("Location"))
	}
	if hits.Load() != 1 {
		t.Errorf("upstream saw %d requests, want 1 (redirect not followed)", hits.Load())
	}
}

func TestMissingTokenIs403(t *testing.T) {
	var hits atomic.Int32
	up := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits.Add(1) }))
	defer up.Close()

	for name, content := range map[string]*string{"missing": nil, "empty": strp("\n")} {
		t.Run(name, func(t *testing.T) {
			rig := newRig(t, up.URL, content, nil)
			resp, body := rig.do(t, "POST", "http://fourcat.desk/api/import-dataset/", map[string]string{"Cookie": "x=y"}, "p")
			assertDenied(t, resp, body)
		})
	}
	if hits.Load() != 0 {
		t.Errorf("upstream reached %d times without a token", hits.Load())
	}
}

func TestUnreachableUpstreamIsNotPassedThrough(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	addr := up.URL
	up.Close() // nothing listens any more

	rig := newRig(t, addr, strp("abc"), nil)
	resp, body := rig.do(t, "GET", "http://fourcat.desk/api/check-query/?key=k", nil, "")
	if resp.StatusCode != 502 || strings.Contains(body, "abc") {
		t.Errorf("got %d %q, want 502 without the token", resp.StatusCode, body)
	}
}

// TestDeniedThroughProxy drives the denials end to end and checks that
// nothing refused reaches a listener, and that the inward-address refusal
// answers 403, not 502.
func TestDeniedThroughProxy(t *testing.T) {
	var hits atomic.Int32
	up := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits.Add(1) }))
	defer up.Close()
	upHost, upPort, _ := net.SplitHostPort(up.Listener.Addr().String())

	res := fakeResolver{"evil.example": {upHost}}
	rig := newRig(t, up.URL, strp("abc"), res)

	for _, u := range []string{
		"http://fourcat.desk/",
		"http://fourcat.desk/api/check-query",
		"http://other.desk/",
		"http://localhost:" + upPort + "/",
		"http://" + net.JoinHostPort(upHost, upPort) + "/",
		"http://evil.example:" + upPort + "/", // resolves to loopback
	} {
		resp, body := rig.do(t, "GET", u, map[string]string{"Cookie": "x=y"}, "")
		assertDenied(t, resp, body)
	}
	if hits.Load() != 0 {
		t.Errorf("refused requests reached the listener %d times", hits.Load())
	}
	if n := strings.Count(rig.logs.String(), "\n"); n != 6 {
		t.Errorf("want 6 log lines, got %d: %q", n, rig.logs.String())
	}
}
