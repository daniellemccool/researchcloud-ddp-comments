package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/elazarl/goproxy"

	"github.com/daniellemccool/researchcloud-ddp-comments/internal/platform"
)

type lockedBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuf) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// TestLiveProxyOverHTTP drives the real goproxy handler over plain HTTP
// against an httptest upstream.
func TestLiveProxyOverHTTP(t *testing.T) {
	var gotAE string
	small := `{"cursor":5,"comments":[{"id":1,"t":"a<b"}]}`
	big := `{"comments":[` + strings.Repeat(`{"id":1},`, 20) + `{"id":2}]}`
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAE = r.Header.Get("Accept-Encoding")
		if strings.HasPrefix(r.URL.Path, "/api/cut") {
			// Announce 50 bytes, send 10, then drop the connection.
			w.Header().Set("Content-Length", "50")
			_, _ = io.WriteString(w, small[:10])
			w.(http.Flusher).Flush()
			panic(http.ErrAbortHandler)
		}
		if strings.HasPrefix(r.URL.Path, "/api/big") {
			_, _ = io.WriteString(w, big)
			return
		}
		_, _ = io.WriteString(w, small)
	}))
	defer upstream.Close()

	p := &platform.Platform{
		Name:        "test",
		DefaultKind: "video",
		Hosts:       []string{"capture.example"},
		Captures: []platform.CaptureRule{{
			Name:         "c",
			Path:         regexpMust(`^/api/`),
			Items:        "comments",
			ResponseKeep: []string{"cursor"},
			MaxBodyBytes: 60,
		}},
	}
	capPath := filepath.Join(t.TempDir(), "captures.ndjson")
	out, err := openRecordWriter(capPath)
	if err != nil {
		t.Fatal(err)
	}
	proxy := newProxy(p, Meta{User: "u", Session: "s", Platform: "test", Kind: "video"}, out, newBoundary("", newTokenFile(filepath.Join(t.TempDir(), "token")), mustURL("http://4cat-frontend:5000"), nil))
	upAddr := upstream.Listener.Addr().String()
	proxy.Tr.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, upAddr)
	}
	proxySrv := httptest.NewServer(proxy)
	defer proxySrv.Close()

	logs := &lockedBuf{}
	log.SetOutput(logs)
	defer log.SetOutput(os.Stderr)

	pu, _ := url.Parse(proxySrv.URL)
	client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(pu)}}
	get := func(path string) (string, int) {
		t.Helper()
		before := strings.Count(logs.String(), "\n")
		req, _ := http.NewRequest("GET", "http://capture.example"+path, nil)
		req.Header.Set("Accept-Encoding", "gzip, deflate, br, zstd")
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		b, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		return string(b), strings.Count(logs.String(), "\n") - before
	}

	// (a) over the cap: the browser gets every byte, one error record.
	body, lines := get("/api/big")
	if body != big {
		t.Errorf("over-cap body altered: %q", body)
	}
	if lines != 1 {
		t.Errorf("over-cap request logged %d lines", lines)
	}
	if gotAE != "gzip, deflate, br, zstd" {
		t.Errorf("upstream saw Accept-Encoding %q", gotAE)
	}

	// (b) under the cap: one item record.
	body, lines = get("/api/small")
	if body != small {
		t.Errorf("small body altered: %q", body)
	}
	if lines != 1 {
		t.Errorf("small request logged %d lines", lines)
	}

	// (d) an interrupted upstream: the "!" marker, still one line.
	if _, err := client.Get("http://capture.example/api/cut"); err == nil {
		// headers may arrive before the cut; the body read is not needed
		t.Log("cut response delivered headers")
	}
	if all := logs.String(); !strings.Contains(all, "/api/cut 200 10!\n") {
		t.Errorf("interrupted request not logged with the ! marker:\n%s", all)
	}

	// (c) a refused host logs once too.
	req, _ := http.NewRequest("GET", "http://other.desk/x", nil)
	before := strings.Count(logs.String(), "\n")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	db, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != 403 || string(db) != deniedBody {
		t.Errorf("denied: %d %q", resp.StatusCode, db)
	}
	if n := strings.Count(logs.String(), "\n") - before; n != 1 {
		t.Errorf("denied request logged %d lines", n)
	}

	data, err := os.ReadFile(capPath)
	if err != nil {
		t.Fatal(err)
	}
	recs := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(recs) != 3 {
		t.Fatalf("want 3 records, got %d: %s", len(recs), data)
	}
	if !strings.Contains(recs[0], `"error":"body too large"`) || strings.Contains(recs[0], `"item"`) {
		t.Errorf("record 0: %s", recs[0])
	}
	if !strings.Contains(recs[1], `"item":{"id":1,"t":"a<b"}`) || !strings.Contains(recs[1], `"cursor":5`) {
		t.Errorf("record 1: %s", recs[1])
	}
}

// testPlatform is the platform both live-proxy tests capture against.
func testPlatform() *platform.Platform {
	return &platform.Platform{
		Name:        "test",
		DefaultKind: "video",
		Hosts:       []string{"capture.example"},
		Captures: []platform.CaptureRule{{
			Name:         "c",
			Path:         regexpMust(`^/api/`),
			Items:        "comments",
			ResponseKeep: []string{"cursor"},
			MaxBodyBytes: 60,
		}},
	}
}

// useTestCA generates a CA in a temp dir and makes goproxy's MITM sign with
// it, so the test client can verify the proxy's per-host certificate. The
// previous GoproxyCa is restored on cleanup.
func useTestCA(t *testing.T) *x509.CertPool {
	t.Helper()
	dir := t.TempDir()
	ca, err := ensureCA(filepath.Join(dir, "ca.pem"), filepath.Join(dir, "ca-key.pem"))
	if err != nil {
		t.Fatal(err)
	}
	old := goproxy.GoproxyCa
	goproxy.GoproxyCa = *ca
	t.Cleanup(func() { goproxy.GoproxyCa = old })
	caCert, err := x509.ParseCertificate(ca.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(caCert)
	return pool
}

// TestLiveProxyHTTP2BothLegs drives the real goproxy handler through an h2
// MITM tunnel to an h2 upstream: both legs must be HTTP/2 and the capture
// hook must still fire once per stream.
func TestLiveProxyHTTP2BothLegs(t *testing.T) {
	small := `{"cursor":5,"comments":[{"id":1,"t":"a<b"}]}`
	var mu sync.Mutex
	var upstreamProto string
	upstream := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		upstreamProto = r.Proto
		mu.Unlock()
		_, _ = io.WriteString(w, small)
	}))
	upstream.EnableHTTP2 = true
	upstream.StartTLS()
	defer upstream.Close()

	capPath := filepath.Join(t.TempDir(), "captures.ndjson")
	out, err := openRecordWriter(capPath)
	if err != nil {
		t.Fatal(err)
	}
	proxy := newProxy(testPlatform(), Meta{User: "u", Session: "s", Platform: "test", Kind: "video"}, out, newBoundary("", newTokenFile(filepath.Join(t.TempDir(), "token")), mustURL("http://4cat-frontend:5000"), nil))
	caPool := useTestCA(t)

	upAddr := upstream.Listener.Addr().String()
	proxy.Tr.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, upAddr)
	}
	// The httptest certificate is issued for example.com/127.0.0.1 while the
	// request host is capture.example; this replaces only the test's trust
	// anchor and server name, not production's (newProxy verifies upstreams
	// against the system roots).
	upPool := x509.NewCertPool()
	upPool.AddCert(upstream.Certificate())
	proxy.Tr.TLSClientConfig = &tls.Config{RootCAs: upPool, ServerName: "example.com", MinVersion: tls.VersionTLS12}

	proxySrv := httptest.NewServer(proxy)
	defer proxySrv.Close()

	logs := &lockedBuf{}
	log.SetOutput(logs)
	defer log.SetOutput(os.Stderr)

	pu, _ := url.Parse(proxySrv.URL)
	client := &http.Client{Transport: &http.Transport{
		Proxy:             http.ProxyURL(pu),
		TLSClientConfig:   &tls.Config{RootCAs: caPool},
		ForceAttemptHTTP2: true,
	}}
	before := strings.Count(logs.String(), "\n")
	req, _ := http.NewRequest("GET", "https://capture.example/api/small", nil)
	req.Header.Set("Accept-Encoding", "gzip, deflate, br, zstd")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Proto != "HTTP/2.0" {
		t.Errorf("browser leg proto = %q, want HTTP/2.0", resp.Proto)
	}
	mu.Lock()
	up := upstreamProto
	mu.Unlock()
	if up != "HTTP/2.0" {
		t.Errorf("upstream leg proto = %q, want HTTP/2.0", up)
	}
	if string(b) != small {
		t.Errorf("body altered: %q", b)
	}
	if n := strings.Count(logs.String(), "\n") - before; n != 1 {
		t.Errorf("request logged %d lines, want 1:\n%s", n, logs.String())
	}

	data, err := os.ReadFile(capPath)
	if err != nil {
		t.Fatal(err)
	}
	recs := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(recs) != 1 {
		t.Fatalf("want 1 record, got %d: %s", len(recs), data)
	}
	if !strings.Contains(recs[0], `"item":{"id":1,"t":"a<b"}`) || !strings.Contains(recs[0], `"cursor":5`) {
		t.Errorf("record: %s", recs[0])
	}
}

// TestUpstreamCertificateIsVerified drives the proxy against a TLS upstream
// whose certificate is not in the system roots. With newProxy's production
// TLSClientConfig the request must not yield a 200, the upstream handler must
// never run, and nothing may be captured.
func TestUpstreamCertificateIsVerified(t *testing.T) {
	small := `{"cursor":5,"comments":[{"id":1,"t":"a<b"}]}`
	var mu sync.Mutex
	reached := false
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		reached = true
		mu.Unlock()
		_, _ = io.WriteString(w, small)
	}))
	defer upstream.Close()

	capPath := filepath.Join(t.TempDir(), "captures.ndjson")
	out, err := openRecordWriter(capPath)
	if err != nil {
		t.Fatal(err)
	}
	proxy := newProxy(testPlatform(), Meta{User: "u", Session: "s", Platform: "test", Kind: "video"}, out, newBoundary("", newTokenFile(filepath.Join(t.TempDir(), "token")), mustURL("http://4cat-frontend:5000"), nil))
	caPool := useTestCA(t)

	upAddr := upstream.Listener.Addr().String()
	proxy.Tr.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, upAddr)
	}
	// proxy.Tr.TLSClientConfig is deliberately left exactly as newProxy sets
	// it: verification against the system roots, which do not hold the
	// httptest certificate.

	proxySrv := httptest.NewServer(proxy)
	defer proxySrv.Close()

	logs := &lockedBuf{}
	log.SetOutput(logs)
	defer log.SetOutput(os.Stderr)

	pu, _ := url.Parse(proxySrv.URL)
	client := &http.Client{Transport: &http.Transport{
		Proxy:             http.ProxyURL(pu),
		TLSClientConfig:   &tls.Config{RootCAs: caPool},
		ForceAttemptHTTP2: true,
	}}
	req, _ := http.NewRequest("GET", "https://capture.example/api/small", nil)
	req.Header.Set("Accept-Encoding", "gzip, deflate, br, zstd")
	resp, err := client.Do(req)
	if err == nil {
		_ = resp.Body.Close()
		if resp.StatusCode < 500 || resp.StatusCode > 599 {
			t.Errorf("request yielded status %d, want a transport error or a 5xx", resp.StatusCode)
		}
	}
	mu.Lock()
	hit := reached
	mu.Unlock()
	if hit {
		t.Error("upstream handler was reached despite the unverified certificate")
	}
	data, err := os.ReadFile(capPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(data)) != "" {
		t.Errorf("records written despite the failed request: %s", data)
	}
}

func TestWriteFailureFoldsIntoLogSuffix(t *testing.T) {
	path := filepath.Join(t.TempDir(), "captures.ndjson")
	out, err := openRecordWriter(path)
	if err != nil {
		t.Fatal(err)
	}
	_ = out.f.Close() // every write now fails
	rule := &platform.CaptureRule{Name: "c", Path: regexpMust(`^/`), Items: "comments", MaxBodyBytes: 100}
	u, _ := url.Parse("http://capture.example/api")
	req := &http.Request{Method: "GET", URL: u}
	body := `{"comments":[{"id":1}]}`
	resp := &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}
	got := capture(rule, req, resp, testMeta, out)
	if got != "23 write-error" {
		t.Errorf("got %q", got)
	}
	b, _ := io.ReadAll(resp.Body)
	if string(b) != body {
		t.Errorf("browser body %q", b)
	}
}

func mustURL(s string) *url.URL {
	u, err := url.Parse(s)
	if err != nil {
		panic(err)
	}
	return u
}
