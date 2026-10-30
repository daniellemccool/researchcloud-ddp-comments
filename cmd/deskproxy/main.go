// Command deskproxy is the desk's capture proxy: a MITM proxy that writes a
// safety copy of selected platform API responses as NDJSON records and hands
// the browser the original bytes untouched.
//
// The 4CAT allowlist, the token injection and the inward-address refusal come
// in Task 4 (ADR-0001, ADR-0009); until then every .desk host, loopback name,
// IP literal and dotless name is refused with 403 by the proxy itself.
package main

import (
	"bytes"
	"errors"
	"flag"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"

	"github.com/elazarl/goproxy"

	"github.com/daniellemccool/researchcloud-ddp-comments/internal/platform"
)

// matchedCtx is what OnRequest leaves in ctx.UserData for OnResponse.
type matchedCtx struct {
	rule *platform.CaptureRule
}

func main() {
	var (
		platformPath = flag.String("platform", "", "path to platform.json")
		user         = flag.String("user", "", "collector user name")
		session      = flag.String("session", "", "session id")
		capturesDir  = flag.String("captures", "/captures", "directory for captures.ndjson")
		caDir        = flag.String("ca", "/ca", "directory holding ca.pem and ca-key.pem")
		listen       = flag.String("listen", ":8080", "proxy listen address")
		health       = flag.String("health", ":8081", "health listen address")
	)
	flag.Parse()
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)

	if *platformPath == "" {
		log.Fatal("--platform is required")
	}
	p, err := platform.Load(*platformPath)
	if err != nil {
		log.Fatalf("load platform: %v", err)
	}
	meta := Meta{User: *user, Session: *session, Platform: p.Name, Kind: p.DefaultKind}

	certPath := filepath.Join(*caDir, "ca.pem")
	ca, err := ensureCA(certPath, filepath.Join(*caDir, "ca-key.pem"))
	if err != nil {
		log.Fatalf("CA: %v", err)
	}
	goproxy.GoproxyCa = *ca // MITM every CONNECT with our CA

	if err := os.MkdirAll(*capturesDir, 0o755); err != nil {
		log.Fatalf("captures dir: %v", err)
	}
	out, err := openRecordWriter(filepath.Join(*capturesDir, "captures.ndjson"))
	if err != nil {
		log.Fatalf("open captures: %v", err)
	}

	proxy := goproxy.NewProxyHttpServer()
	proxy.Verbose = false
	// Keep upstream bodies encoded so we decode them ourselves and the
	// browser gets the original bytes.
	proxy.Tr.DisableCompression = true

	// The browser speaks HTTP/1.1 to the proxy: goproxy's AllowHTTP2 (the
	// h2 ALPN) is left off. The spike's websocket pass-through is proven on
	// h1 only, and h2 and websockets (RFC 8441) cannot be verified in this
	// task, so Task 4's box check re-tests turning it on.
	proxy.AllowHTTP2 = false

	proxy.OnRequest().HandleConnect(goproxy.AlwaysMitm)

	proxy.OnRequest().DoFunc(func(req *http.Request, ctx *goproxy.ProxyCtx) (*http.Request, *http.Response) {
		// Fail closed: refused hosts are answered here, before any upstream
		// is contacted. CONNECT is allowed (AlwaysMitm dials nothing), so the
		// 403 is delivered inside the MITM'd TLS session.
		if resp := denyResponse(req); resp != nil {
			log.Printf("%s %s %s 403 0", req.Method, requestHost(req), req.URL.Path)
			return req, resp
		}
		if rule := matchRule(p, req); rule != nil {
			ctx.UserData = &matchedCtx{rule: rule}
		}
		return req, nil
	})

	proxy.OnResponse().DoFunc(func(resp *http.Response, ctx *goproxy.ProxyCtx) *http.Response {
		if resp == nil {
			return resp
		}
		req := ctx.Req
		if req == nil {
			req = resp.Request
		}
		if req == nil {
			return resp
		}
		nbytes := resp.ContentLength
		if m, ok := ctx.UserData.(*matchedCtx); ok && m != nil && resp.Body != nil {
			nbytes = capture(m.rule, req, resp, meta, out)
		}
		log.Printf("%s %s %s %d %d", req.Method, requestHost(req), req.URL.Path, resp.StatusCode, nbytes)
		return resp
	})

	go func() {
		mux := http.NewServeMux()
		mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, "ok\n")
		})
		log.Fatal(http.ListenAndServe(*health, mux))
	}()

	log.Printf("deskproxy platform=%s user=%s session=%s listening on %s", p.Name, *user, *session, *listen)
	log.Fatal(http.ListenAndServe(*listen, proxy))
}

// capture buffers a matching response (at most rule.MaxBodyBytes+1 bytes),
// writes its records, and puts the original bytes back on resp for the
// browser. It returns the body length it saw.
func capture(rule *platform.CaptureRule, req *http.Request, resp *http.Response, meta Meta, out *recordWriter) int64 {
	raw, err := io.ReadAll(io.LimitReader(resp.Body, rule.MaxBodyBytes+1))
	if err != nil {
		// The read failed midway: hand the browser what arrived and the error.
		resp.Body = &prefixBody{r: io.MultiReader(bytes.NewReader(raw), errReader{err}), c: resp.Body}
		log.Printf("capture %s: read: %v", rule.Name, err)
		return int64(len(raw))
	}

	var recs []Record
	if int64(len(raw)) > rule.MaxBodyBytes {
		// Too big: stream the rest through to the browser, keep nothing.
		resp.Body = &prefixBody{r: io.MultiReader(bytes.NewReader(raw), resp.Body), c: resp.Body}
		recs = []Record{errorRecord(newRecord(rule, req, resp, meta), "body too large")}
		writeRecords(out, rule, recs)
		return int64(len(raw))
	}

	_ = resp.Body.Close()
	resp.Body = io.NopCloser(bytes.NewReader(raw))
	resp.ContentLength = int64(len(raw))
	resp.Header.Set("Content-Length", strconv.Itoa(len(raw)))

	decoded, err := decodeBody(resp.Header.Get("Content-Encoding"), bytes.NewReader(raw), rule.MaxBodyBytes)
	switch {
	case errors.Is(err, ErrTooLarge):
		recs = []Record{errorRecord(newRecord(rule, req, resp, meta), "body too large")}
	case err != nil:
		recs = []Record{errorRecord(newRecord(rule, req, resp, meta), "cannot decode body: "+err.Error())}
	default:
		recs = recordsFor(rule, req, resp, decoded, meta)
	}
	writeRecords(out, rule, recs)
	return int64(len(raw))
}

func writeRecords(out *recordWriter, rule *platform.CaptureRule, recs []Record) {
	if err := out.write(recs); err != nil {
		log.Printf("capture %s: write: %v", rule.Name, err)
	}
}

// prefixBody reads r and closes the original upstream body.
type prefixBody struct {
	r io.Reader
	c io.Closer
}

func (b *prefixBody) Read(p []byte) (int, error) { return b.r.Read(p) }
func (b *prefixBody) Close() error               { return b.c.Close() }

type errReader struct{ err error }

func (e errReader) Read([]byte) (int, error) { return 0, e.err }
