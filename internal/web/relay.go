package web

import (
	"context"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
)

// runningChecker is what the relay needs of *session.Manager (controller
// ruling F54: small interfaces, so tests can use fakes).
type runningChecker interface {
	IsRunning(ctx context.Context, user string) bool
}

// vncPrefix is the mount path stripped before forwarding.
const vncPrefix = "/desk/vnc"

// Relay is the /desk/vnc/ handler: a reverse proxy to the requester's own
// Firefox noVNC server (ADR-0008). The target host is built from the
// gate-checked identity in the request context and from nothing else — no
// path or query parameter ever reaches the dial — so a request naming
// another user still lands on the requester's own container.
//
// NewRelay returns the concrete *Relay (which implements http.Handler, so it
// drops straight into Deps.Relay); tests replace its Dial field to route the
// proxied connections to in-memory backends.
type Relay struct {
	running runningChecker
	proxy   *httputil.ReverseProxy

	// Dial is the transport's dial function; nil means a plain net.Dialer.
	// Tests set it to observe or redirect the dialled host.
	Dial func(ctx context.Context, network, addr string) (net.Conn, error)
}

// NewRelay returns the screen relay. A request whose owner's pair is not
// running gets 409 "screen not running"; a dial failure gets 502 from the
// proxy's ErrorHandler.
func NewRelay(s runningChecker) *Relay {
	r := &Relay{running: s}
	target := &url.URL{Scheme: "http", Host: ""} // host set per request from the identity
	transport := &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			if r.Dial != nil {
				return r.Dial(ctx, network, addr)
			}
			var d net.Dialer
			return d.DialContext(ctx, network, addr)
		},
	}
	r.proxy = &httputil.ReverseProxy{
		Transport: transport,
		// Flush immediately: the noVNC websocket streams frames; no
		// ResponseHeaderTimeout, the screen may take a while to answer.
		FlushInterval: -1,
		Rewrite: func(pr *httputil.ProxyRequest) {
			id := identity(pr.In)
			target.Host = "desk-firefox-" + id.User + ":5800"
			pr.Out.URL.Scheme = target.Scheme
			pr.Out.URL.Host = target.Host
			// /desk/vnc/ → /, /desk/vnc/websockify → /websockify; the query
			// is preserved (Out is a clone of In).
			pr.Out.URL.Path = strings.TrimPrefix(pr.In.URL.Path, vncPrefix)
			pr.Out.URL.RawPath = ""
		},
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, err error) {
			log.Printf("web: relay: %v", err)
			plain(w, http.StatusBadGateway, "bad gateway")
		},
	}
	return r
}

// ServeHTTP relays the request to the identity's own Firefox container.
func (r *Relay) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	id := identity(req)
	if !r.running.IsRunning(req.Context(), id.User) {
		plain(w, http.StatusConflict, "screen not running")
		return
	}
	r.proxy.ServeHTTP(w, req)
}
