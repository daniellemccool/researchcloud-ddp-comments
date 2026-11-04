// Command deskproxy is the desk's capture proxy: a MITM proxy that writes a
// safety copy of selected platform API responses as NDJSON records and hands
// the browser the original bytes untouched.
//
// The boundary (boundary.go) comes first: only the two 4CAT routes are
// forwarded, with the collector's token as the Cookie (ADR-0001, ADR-0009);
// loopback names, IP literals, dotless names, every other .desk host and any
// destination that resolves to an inward address are refused with 403 by the
// proxy itself.
package main

import (
	"bytes"
	"crypto/tls"
	"errors"
	"flag"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
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
		listenCIDR   = flag.String("listen-cidr", "", "bind the listeners to this network's address only")
		tokenPath    = flag.String("token-file", "/token", "this collector's 4CAT token file")
		upstream     = flag.String("fourcat-upstream", "http://4cat-frontend:5000", "where fourcat.desk is forwarded")
		fqdn         = flag.String("fqdn", "", "the workspace's own host name, allowed through")
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
	up, err := url.Parse(*upstream)
	if err != nil || up.Host == "" || (up.Scheme != "http" && up.Scheme != "https") {
		log.Fatalf("--fourcat-upstream %q is not an http(s) URL", *upstream)
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

	proxy := newProxy(p, meta, out, newBoundary(*fqdn, newTokenFile(*tokenPath), up, nil))

	// ADR-0007: with --listen-cidr both listeners bind to this container's
	// address on that network only, so the shared bridges carry no listener.
	// Resolved before the first ListenAndServe; no address inside the CIDR is
	// fatal before anything is served.
	listenAddr, healthAddr := *listen, *health
	if *listenCIDR != "" {
		var addrs []net.Addr
		addrs, err = localAddrs()
		if err != nil {
			log.Fatalf("list interface addresses: %v", err)
		}
		listenAddr, err = bindAddr(*listenCIDR, *listen, addrs)
		if err != nil {
			log.Fatal(err)
		}
		healthAddr, err = bindAddr(*listenCIDR, *health, addrs)
		if err != nil {
			log.Fatal(err)
		}
	}

	go func() {
		mux := http.NewServeMux()
		mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, "ok\n")
		})
		log.Fatal(http.ListenAndServe(healthAddr, mux))
	}()

	cidrField := ""
	if *listenCIDR != "" {
		cidrField = " cidr=" + *listenCIDR
	}
	log.Printf("deskproxy platform=%s user=%s session=%s listening on %s%s", p.Name, *user, *session, listenAddr, cidrField)
	log.Fatal(http.ListenAndServe(listenAddr, proxy))
}

// newProxy builds the capture proxy handler.
//
// Log format, exactly one stderr line per request, written when the response
// is handed back: "method host path status bytes". bytes is the body length
// delivered to the browser; "N+" means N bytes were buffered and more were
// streamed through (over-cap body); "N!" means the upstream read failed after
// N bytes and the browser gets those N bytes and then the error; "-" means
// the length is unknown (unmatched chunked response). " write-error" is
// appended when the record could not be written, so it still is one line.
// goproxy's own WARN: lines (its logger, independent of Verbose) can appear in
// addition, for instance when a browser aborts an h2 stream; they are not
// response lines.
func newProxy(p *platform.Platform, meta Meta, out *recordWriter, b *boundary) *goproxy.ProxyHttpServer {
	proxy := goproxy.NewProxyHttpServer()
	proxy.Verbose = false
	// The browser's own Accept-Encoding is forwarded unchanged (goproxy
	// would otherwise strip it, and upstream would see a different client
	// than Firefox). DisableCompression stops the transport adding its own
	// Accept-Encoding and transparently decoding, so matching bodies arrive
	// encoded as the browser asked for them; we decode a copy for the record
	// and the browser gets the original bytes.
	proxy.KeepAcceptEncoding = true
	proxy.Tr.DisableCompression = true
	b.install(proxy)

	// HTTP/2 on both legs. TikTok tarpits one request per page load for an
	// HTTP/1.1 client on the box's address (sandbox, 2026-11-04), so the
	// browser gets h2 from the MITM (goproxy's embedded http2.Server runs the
	// same filters per stream) and the upstream transport attempts h2 as well.
	proxy.AllowHTTP2 = true
	proxy.Tr.ForceAttemptHTTP2 = true
	// goproxy's default upstream TLS config is an empty tls.Config (its name
	// suggests otherwise); this makes verification against the system roots
	// explicit and sets a TLS 1.2 floor.
	proxy.Tr.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}

	proxy.OnRequest().HandleConnect(goproxy.AlwaysMitm)

	proxy.OnRequest().DoFunc(func(req *http.Request, ctx *goproxy.ProxyCtx) (*http.Request, *http.Response) {
		// Fail closed: the boundary answers refusals and the 4CAT forward
		// here, before any capture rule or upstream is considered. CONNECT is
		// allowed (AlwaysMitm dials nothing), so the answer is delivered
		// inside the MITM'd TLS session. The response passes through
		// OnResponse below, which logs it. A forwarded 4CAT response never
		// has a capture rule.
		if resp := b.handle(proxy, req, ctx); resp != nil {
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
		nbytes := "-"
		if resp.ContentLength >= 0 {
			nbytes = strconv.FormatInt(resp.ContentLength, 10)
		}
		if m, ok := ctx.UserData.(*matchedCtx); ok && m != nil && resp.Body != nil {
			nbytes = capture(m.rule, req, resp, meta, out)
		}
		log.Printf("%s %s %s %d %s", req.Method, requestHost(req), req.URL.Path, resp.StatusCode, nbytes)
		return resp
	})
	return proxy
}

// capture buffers a matching response (at most rule.MaxBodyBytes+1 bytes),
// writes its records, and puts the original bytes back on resp for the
// browser. It returns the bytes field for the log line.
func capture(rule *platform.CaptureRule, req *http.Request, resp *http.Response, meta Meta, out *recordWriter) string {
	raw, err := io.ReadAll(io.LimitReader(resp.Body, rule.MaxBodyBytes+1))
	if err != nil {
		// The read failed midway: hand the browser what arrived and the error.
		resp.Body = &prefixBody{r: io.MultiReader(bytes.NewReader(raw), errReader{err}), c: resp.Body}
		sfx := writeRecords(out, []Record{errorRecord(newRecord(rule, req, resp, meta), "read error: "+err.Error())})
		return strconv.Itoa(len(raw)) + "!" + sfx
	}

	var recs []Record
	if int64(len(raw)) > rule.MaxBodyBytes {
		// Too big: stream the rest through to the browser, keep nothing.
		resp.Body = &prefixBody{r: io.MultiReader(bytes.NewReader(raw), resp.Body), c: resp.Body}
		recs = []Record{errorRecord(newRecord(rule, req, resp, meta), "body too large")}
		return strconv.Itoa(len(raw)) + "+" + writeRecords(out, recs)
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
	return strconv.Itoa(len(raw)) + writeRecords(out, recs)
}

// writeRecords returns the log suffix for the request line: " write-error"
// when the records could not be written (the single log line carries it, the
// error itself is not logged), else "".
func writeRecords(out *recordWriter, recs []Record) string {
	if err := out.write(recs); err != nil {
		return " write-error"
	}
	return ""
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
