package web

import (
	"context"
	"net"
	"net/http"
	"net/http/httputil"
	"strings"

	"github.com/daniellemccool/researchcloud-ddp-comments/internal/access"
)

// fourcatPrefix is the mount path stripped before forwarding.
const fourcatPrefix = "/fourcat"

// FourcatProxy is the /fourcat/ handler: a reverse proxy to the bundled
// 4CAT web UI (http://4cat-frontend:5000), mounted admin-only by the gated
// listener. The /fourcat prefix is stripped (/fourcat/ → /), Host and
// X-Forwarded-Host are set to the desk's FQDN and X-Forwarded-Proto to
// https; nothing else is rewritten, so 4CAT's own redirects reach the
// browser unmodified.
//
// NewFourcatProxy returns the concrete *FourcatProxy (which implements
// http.Handler, so it drops straight into Deps.FourcatProxy); tests replace
// its Dial field to route the proxied connections to an in-memory backend.
type FourcatProxy struct {
	fqdn  string
	proxy *httputil.ReverseProxy

	// Dial is the transport's dial function; nil means a plain net.Dialer.
	Dial func(ctx context.Context, network, addr string) (net.Conn, error)
}

// NewFourcatProxy returns the 4CAT proxy for the desk at fqdn.
func NewFourcatProxy(fqdn string) *FourcatProxy {
	p := &FourcatProxy{fqdn: fqdn}
	transport := &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			if p.Dial != nil {
				return p.Dial(ctx, network, addr)
			}
			var d net.Dialer
			return d.DialContext(ctx, network, addr)
		},
	}
	p.proxy = &httputil.ReverseProxy{
		Transport:     transport,
		FlushInterval: -1,
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.Out.URL.Scheme = "http"
			pr.Out.URL.Host = "4cat-frontend:5000"
			pr.Out.URL.Path = strings.TrimPrefix(pr.In.URL.Path, fourcatPrefix)
			pr.Out.URL.RawPath = ""
			pr.SetXForwarded()
			pr.Out.Header.Set("X-Forwarded-Host", p.fqdn)
			pr.Out.Header.Set("X-Forwarded-Proto", "https")
			pr.Out.Host = p.fqdn
			// The gate secret and identity headers never reach 4CAT (F74).
			access.StripIdentityHeaders(pr.Out.Header)
		},
	}
	return p
}

// ServeHTTP proxies the request to the 4CAT frontend.
func (p *FourcatProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p.proxy.ServeHTTP(w, r)
}
