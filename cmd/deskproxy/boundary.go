package main

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/elazarl/goproxy"
)

// deniedBody is the body of every refusal the proxy itself answers.
const deniedBody = "deskproxy: denied by rule"

// fourcatHost is the name the browser uses for the 4CAT frontend; the proxy
// forwards it to --fourcat-upstream with this same Host.
const fourcatHost = "fourcat.desk"

type verdict int

// deny is the zero value: a request nothing allowed is refused.
const (
	deny verdict = iota
	allow
	forward4CAT
)

// fourcatRoutes is ADR-0001's allowlist, an exact method-and-path table for
// fourcat.desk. Adding a route means adding a row here and a row with a reason
// in TestAllowlistExact. query says whether a query string is accepted.
var fourcatRoutes = []struct {
	method, path string
	query        bool
}{
	{http.MethodPost, "/api/import-dataset/", false}, // the donation upload
	{http.MethodGet, "/api/check-query/", true},      // polling the import's progress
}

// normHost lower-cases a host, drops IPv6 brackets and a trailing dot.
func normHost(h string) string {
	return strings.ToLower(strings.TrimSuffix(strings.Trim(h, "[]"), "."))
}

// classify decides what the proxy does with req. fqdn is the workspace's own
// name, which is let through (nginx gates it).
func classify(req *http.Request, fqdn string) verdict {
	h := normHost(requestHost(req))
	switch {
	case h == "" || h == "localhost" || strings.HasSuffix(h, ".localhost"):
		return deny
	case net.ParseIP(h) != nil, !strings.Contains(h, "."):
		return deny
	case h == fourcatHost:
		for _, r := range fourcatRoutes {
			if req.Method == r.method && req.URL != nil && req.URL.Path == r.path &&
				req.URL.EscapedPath() == r.path && req.URL.Opaque == "" &&
				(r.query || req.URL.RawQuery == "") {
				return forward4CAT
			}
		}
		return deny
	case strings.HasSuffix(h, ".desk"):
		return deny
	case fqdn != "" && h == normHost(fqdn):
		return allow
	}
	return allow
}

// inwardAddr reports whether ip is an address the proxy must never connect to
// on a browser's behalf: loopback, RFC 1918 (which includes Docker's
// 172.16/12), link-local, unique-local fc00::/7, unspecified, and the
// IPv4-mapped IPv6 forms of those. A nil address is refused.
func inwardAddr(ip net.IP) bool {
	if ip == nil {
		return true
	}
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified() ||
		ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast()
}

// errInward is returned by the pass-through dialer for a refused destination.
var errInward = errors.New("destination address refused")

// resolver is the part of *net.Resolver the dialer uses; tests inject one.
type resolver interface {
	LookupIPAddr(ctx context.Context, host string) ([]net.IPAddr, error)
}

type dialFunc func(ctx context.Context, network, addr string) (net.Conn, error)

// newInwardDial returns a DialContext that resolves the host itself, refuses
// the dial when ANY resolved address is inward, and otherwise dials a checked
// IP:port, never the name again. Resolver errors refuse the dial too.
func newInwardDial(res resolver, dial dialFunc) dialFunc {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, err
		}
		var ips []net.IP
		if ip := net.ParseIP(host); ip != nil {
			ips = []net.IP{ip}
		} else {
			addrs, err := res.LookupIPAddr(ctx, host)
			if err != nil {
				return nil, err
			}
			for _, a := range addrs {
				ips = append(ips, a.IP)
			}
		}
		if len(ips) == 0 {
			return nil, errors.New("no addresses for host")
		}
		for _, ip := range ips {
			if inwardAddr(ip) {
				return nil, errInward
			}
		}
		var lastErr error
		for _, ip := range ips {
			c, err := dial(ctx, network, net.JoinHostPort(ip.String(), port))
			if err == nil {
				return c, nil
			}
			lastErr = err
		}
		return nil, lastErr
	}
}

// boundary is the proxy's fail-closed edge: classification, the 4CAT forward
// with its token, and the inward-address refusal on every other dial.
type boundary struct {
	fqdn     string
	token    *tokenFile
	upstream *url.URL
	fourcat  http.RoundTripper
	res      resolver
}

// newBoundary builds the boundary. res nil means the system resolver.
func newBoundary(fqdn string, token *tokenFile, upstream *url.URL, res resolver) *boundary {
	if res == nil {
		res = net.DefaultResolver
	}
	return &boundary{
		fqdn:     fqdn,
		token:    token,
		upstream: upstream,
		res:      res,
		// The 4CAT forward has its own transport: 4cat-frontend resolves to a
		// Docker address and is exempt from the inward refusal. The browser's
		// Accept-Encoding passes through and the body is never decoded.
		fourcat: &http.Transport{
			Proxy:                 nil,
			DisableCompression:    true,
			ResponseHeaderTimeout: 5 * time.Minute,
		},
	}
}

// install wires the boundary into proxy: the pass-through transport gets the
// inward-refusing dialer, no environment proxy, and nothing can be dialled for
// a CONNECT (AlwaysMitm dials nothing; this is the belt to that brace).
func (b *boundary) install(proxy *goproxy.ProxyHttpServer) {
	d := &net.Dialer{Timeout: 30 * time.Second}
	proxy.Tr.Proxy = nil
	proxy.Tr.DialContext = newInwardDial(b.res, d.DialContext)
	proxy.ConnectDial = func(string, string) (net.Conn, error) { return nil, errInward }
	proxy.ConnectDialWithReq = nil
}

// denyResponse is the proxy's own 403.
func denyResponse(req *http.Request) *http.Response {
	return goproxy.NewResponse(req, goproxy.ContentTypeText, http.StatusForbidden, deniedBody)
}

// handle is the request hook: it returns a response when the proxy answers
// itself (a refusal or the 4CAT forward) and otherwise sets the round tripper
// that turns a refused dial into the standard 403.
func (b *boundary) handle(proxy *goproxy.ProxyHttpServer, req *http.Request, ctx *goproxy.ProxyCtx) *http.Response {
	switch classify(req, b.fqdn) {
	case forward4CAT:
		return b.forward(req)
	case allow:
		ctx.RoundTripper = goproxy.RoundTripperFunc(func(r *http.Request, _ *goproxy.ProxyCtx) (*http.Response, error) {
			resp, err := proxy.Tr.RoundTrip(r)
			if errors.Is(err, errInward) {
				return denyResponse(req), nil
			}
			return resp, err
		})
		return nil
	}
	return denyResponse(req)
}

// forward sends an allowed 4CAT request to the upstream with Host
// fourcat.desk and the client's Cookie replaced by the collector's token. The
// response, redirects included, goes back to the browser untouched. Anything
// that goes wrong is a refusal; the token is never in an error body or a log.
func (b *boundary) forward(req *http.Request) *http.Response {
	tok, err := b.token.current()
	if err != nil {
		return denyResponse(req)
	}
	out := req.Clone(req.Context())
	out.URL = &url.URL{
		Scheme:   b.upstream.Scheme,
		Host:     b.upstream.Host,
		Path:     req.URL.Path,
		RawQuery: req.URL.RawQuery,
	}
	out.Host = fourcatHost
	out.RequestURI = ""
	out.Header.Del("Cookie")
	out.Header.Set("Cookie", "remember_token="+tok)
	for _, h := range []string{"Proxy-Connection", "Proxy-Authorization", "Proxy-Authenticate"} {
		out.Header.Del(h)
	}
	resp, err := b.fourcat.RoundTrip(out)
	if err != nil {
		// The error is not echoed: it names the request, which carries the token.
		return goproxy.NewResponse(req, goproxy.ContentTypeText, http.StatusBadGateway, "deskproxy: 4CAT unreachable")
	}
	return resp
}
