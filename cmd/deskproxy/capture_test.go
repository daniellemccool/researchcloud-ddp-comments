package main

import (
	"bytes"
	"compress/flate"
	"compress/gzip"
	"compress/zlib"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/andybalholm/brotli"

	"github.com/daniellemccool/researchcloud-ddp-comments/internal/platform"
)

var testMeta = Meta{User: "u1", Session: "s1", Platform: "tiktok", Kind: "video"}

func loadTikTok(t *testing.T) (*platform.Platform, *platform.CaptureRule) {
	t.Helper()
	p, err := platform.Load(filepath.Join("..", "..", "platforms", "tiktok", "platform.json"))
	if err != nil {
		t.Fatal(err)
	}
	return p, &p.Captures[0]
}

func commentsRequest(t *testing.T) *http.Request {
	t.Helper()
	u, err := url.Parse("https://www.tiktok.com/api/comment/list/?aweme_id=123&cursor=0&count=20&secret=zzz")
	if err != nil {
		t.Fatal(err)
	}
	return &http.Request{Method: "GET", URL: u, Host: "www.tiktok.com"}
}

func okResponse() *http.Response {
	return &http.Response{StatusCode: 200, Header: http.Header{}}
}

func TestDecodeGzipBrDeflate(t *testing.T) {
	plain := []byte("abc")
	enc := map[string]func(io.Writer) io.WriteCloser{
		"gzip": func(w io.Writer) io.WriteCloser { return gzip.NewWriter(w) },
		"deflate": func(w io.Writer) io.WriteCloser {
			return zlib.NewWriter(w)
		},
		"br": func(w io.Writer) io.WriteCloser { return brotli.NewWriter(w) },
		"identity": func(w io.Writer) io.WriteCloser {
			return nopWriteCloser{w}
		},
	}
	for name, mk := range enc {
		var buf bytes.Buffer
		w := mk(&buf)
		if _, err := w.Write(plain); err != nil {
			t.Fatal(err)
		}
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}
		got, err := decodeBody(name, &buf, 100)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !bytes.Equal(got, plain) {
			t.Errorf("%s: got %q", name, got)
		}
	}

	// raw deflate (no zlib wrapper) is accepted too
	var buf bytes.Buffer
	fw, _ := flate.NewWriter(&buf, flate.DefaultCompression)
	_, _ = fw.Write(plain)
	_ = fw.Close()
	if got, err := decodeBody("deflate", &buf, 100); err != nil || !bytes.Equal(got, plain) {
		t.Errorf("raw deflate: %q, %v", got, err)
	}
}

type nopWriteCloser struct{ io.Writer }

func (nopWriteCloser) Close() error { return nil }

func TestDecodeTooLargeAndUnknown(t *testing.T) {
	_, err := decodeBody("identity", strings.NewReader("12345678901"), 10)
	if !errors.Is(err, ErrTooLarge) {
		t.Errorf("got %v, want ErrTooLarge", err)
	}
	// the cap applies to the decoded size
	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	_, _ = w.Write(bytes.Repeat([]byte("a"), 1000))
	_ = w.Close()
	if _, err := decodeBody("gzip", &buf, 10); !errors.Is(err, ErrTooLarge) {
		t.Errorf("gzip: got %v, want ErrTooLarge", err)
	}
	if _, err := decodeBody("zstd", strings.NewReader("x"), 10); err == nil {
		t.Error("unknown encoding must be an error")
	}
}

func TestBodyCapYieldsErrorRecord(t *testing.T) {
	_, rule := loadTikTok(t)
	r := *rule
	r.MaxBodyBytes = 10
	recs := recordsFor(&r, commentsRequest(t), okResponse(), []byte("12345678901"), testMeta)
	if len(recs) != 1 {
		t.Fatalf("got %d records", len(recs))
	}
	if recs[0].Error != "body too large" || recs[0].Body != nil || recs[0].Item != nil {
		t.Errorf("got %+v", recs[0])
	}
}

func TestItemsRecordsFromFixture(t *testing.T) {
	_, rule := loadTikTok(t)
	body, err := os.ReadFile(filepath.Join("..", "..", "platforms", "tiktok", "fixtures", "comments.json"))
	if err != nil {
		t.Fatal(err)
	}
	recs := recordsFor(rule, commentsRequest(t), okResponse(), body, testMeta)
	if len(recs) != 3 {
		t.Fatalf("got %d records", len(recs))
	}

	var top struct {
		Comments []json.RawMessage `json:"comments"`
	}
	if err := json.Unmarshal(body, &top); err != nil {
		t.Fatal(err)
	}

	for i, r := range recs {
		if r.Error != "" || r.Body != nil {
			t.Errorf("record %d: %+v", i, r)
		}
		if r.PostID != "123" {
			t.Errorf("record %d post_id %q", i, r.PostID)
		}
		if r.User != "u1" || r.Session != "s1" || r.Platform != "tiktok" || r.Kind != "video" || r.Capture != "comments" {
			t.Errorf("record %d meta: %+v", i, r)
		}
		if r.T <= 0 {
			t.Errorf("record %d t %d", i, r.T)
		}
		if r.Request.Method != "GET" || r.Request.Host != "www.tiktok.com" || r.Request.Path != "/api/comment/list/" {
			t.Errorf("record %d request: %+v", i, r.Request)
		}
		wantQ := map[string]string{"aweme_id": "123", "cursor": "0", "count": "20"}
		if !reflect.DeepEqual(r.Request.Query, wantQ) {
			t.Errorf("record %d query %v", i, r.Request.Query)
		}
		if r.Response.Status != 200 {
			t.Errorf("record %d status %d", i, r.Response.Status)
		}
		if len(r.Response.Keep) != 4 || string(r.Response.Keep["has_more"]) != "1" ||
			string(r.Response.Keep["cursor"]) != "3" || string(r.Response.Keep["total"]) != "41" ||
			string(r.Response.Keep["status_code"]) != "0" {
			t.Errorf("record %d response keep %v", i, r.Response.Keep)
		}
		if !bytes.Equal(r.Item, top.Comments[i]) {
			t.Errorf("record %d item is not the raw element", i)
		}
	}
	if got := string(recs[0].Fields["author"]); got != `"sample_user_one"` {
		t.Errorf("author field %s", got)
	}
	if got := string(recs[0].Fields["likes"]); got != "12" {
		t.Errorf("likes field %s", got)
	}
	if len(recs[0].Fields) != 9 {
		t.Errorf("fields %v", recs[0].Fields)
	}

	// the envelope marshals with the stable key names
	b, err := json.Marshal(recs[0])
	if err != nil {
		t.Fatal(err)
	}
	var env map[string]json.RawMessage
	if err := json.Unmarshal(b, &env); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"t", "user", "session", "platform", "capture", "kind", "post_id", "request", "response", "item", "fields"} {
		if _, ok := env[k]; !ok {
			t.Errorf("envelope lacks %q", k)
		}
	}
	for _, k := range []string{"body", "error"} {
		if _, ok := env[k]; ok {
			t.Errorf("items record has %q", k)
		}
	}
}

func TestMissingFieldPathIsAbsent(t *testing.T) {
	_, rule := loadTikTok(t)
	body := `{"comments":[{"cid":"1","text":"hi"}]}`
	recs := recordsFor(rule, commentsRequest(t), okResponse(), []byte(body), testMeta)
	if len(recs) != 1 {
		t.Fatalf("got %d", len(recs))
	}
	if _, ok := recs[0].Fields["author"]; ok {
		t.Error("missing path must leave the key absent")
	}
	if string(recs[0].Fields["text"]) != `"hi"` {
		t.Errorf("text %s", recs[0].Fields["text"])
	}
}

func TestPostIDFromPath(t *testing.T) {
	rule := &platform.CaptureRule{
		Name:   "x",
		Path:   regexpMust(`^/v/(?P<vid>[0-9]+)/c`),
		PostID: platform.PostIDSpec{From: "path", Group: "vid"},
	}
	u, _ := url.Parse("https://h.example/v/99/c")
	recs := recordsFor(rule, &http.Request{Method: "GET", URL: u}, okResponse(), []byte(`{}`), testMeta)
	if len(recs) != 1 || recs[0].PostID != "99" {
		t.Errorf("got %+v", recs)
	}
}

func TestBodyRuleStoresWholeBody(t *testing.T) {
	rule := &platform.CaptureRule{
		Name:         "whole",
		Path:         regexpMust(`^/x`),
		MaxBodyBytes: 1000,
		ResponseKeep: []string{"a"},
		Fields:       map[string]string{"f": "a"},
	}
	u, _ := url.Parse("https://h.example/x")
	body := []byte(`{ "a": 1,  "b": [1, 2] }`)
	recs := recordsFor(rule, &http.Request{Method: "GET", URL: u}, okResponse(), body, testMeta)
	if len(recs) != 1 {
		t.Fatalf("got %d", len(recs))
	}
	if !bytes.Equal(recs[0].Body, body) || recs[0].Item != nil || recs[0].Fields != nil || recs[0].Error != "" {
		t.Errorf("got %+v", recs[0])
	}
	if string(recs[0].Response.Keep["a"]) != "1" {
		t.Errorf("keep %v", recs[0].Response.Keep)
	}
}

func TestBodyRuleNonJSONBody(t *testing.T) {
	rule := &platform.CaptureRule{Name: "whole", Path: regexpMust(`^/x`), MaxBodyBytes: 1000}
	u, _ := url.Parse("https://h.example/x")
	recs := recordsFor(rule, &http.Request{Method: "GET", URL: u}, okResponse(), []byte("not json"), testMeta)
	if len(recs) != 1 || recs[0].Error == "" || recs[0].Body != nil {
		t.Errorf("got %+v", recs)
	}
}

func TestItemsNullOrNotArray(t *testing.T) {
	_, rule := loadTikTok(t)
	cases := map[string]string{
		`{"comments": null}`: `items key "comments" is null`,
		`{"comments": 1}`:    `items key "comments" is not an array`,
		`{"other": []}`:      `items key "comments" is missing`,
		`not json`:           "",
		`[1,2]`:              "",
	}
	for body, want := range cases {
		recs := recordsFor(rule, commentsRequest(t), okResponse(), []byte(body), testMeta)
		if len(recs) != 1 {
			t.Fatalf("%s: got %d records", body, len(recs))
		}
		r := recs[0]
		if r.Error == "" || r.Item != nil || r.Body != nil {
			t.Errorf("%s: got %+v", body, r)
		}
		if want != "" && r.Error != want {
			t.Errorf("%s: error %q, want %q", body, r.Error, want)
		}
	}
}

func TestEmptyItemsArrayYieldsNoRecords(t *testing.T) {
	_, rule := loadTikTok(t)
	recs := recordsFor(rule, commentsRequest(t), okResponse(), []byte(`{"comments": []}`), testMeta)
	if len(recs) != 0 {
		t.Errorf("got %d records", len(recs))
	}
}

func regexpMust(s string) *regexp.Regexp { return regexp.MustCompile(s) }
