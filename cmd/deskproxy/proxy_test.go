package main

import (
	"bytes"
	"context"
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
	proxy := newProxy(p, Meta{User: "u", Session: "s", Platform: "test", Kind: "video"}, out)
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
	if len(recs) != 2 {
		t.Fatalf("want 2 records, got %d: %s", len(recs), data)
	}
	if !strings.Contains(recs[0], `"error":"body too large"`) || strings.Contains(recs[0], `"item"`) {
		t.Errorf("record 0: %s", recs[0])
	}
	if !strings.Contains(recs[1], `"item":{"id":1,"t":"a<b"}`) || !strings.Contains(recs[1], `"cursor":5`) {
		t.Errorf("record 1: %s", recs[1])
	}
}
