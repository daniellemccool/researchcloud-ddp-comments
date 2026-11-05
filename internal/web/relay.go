package web

import (
	"context"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"strings"

	"github.com/daniellemccool/researchcloud-ddp-comments/internal/access"
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
	running  runningChecker
	activity *Activity
	proxy    *httputil.ReverseProxy

	// Dial is the transport's dial function; nil means a plain net.Dialer.
	// Tests set it to observe or redirect the dialled host.
	Dial func(ctx context.Context, network, addr string) (net.Conn, error)

	// Members is the CO poll's view, set after NewRelay; nil means nobody
	// is ever absent. A user the last good list lacks is refused before the
	// running check, so noVNC's auto-reconnect cannot reopen a screen while
	// a revoke stops the pair (ADR-0008).
	Members interface{ Absent(user string) bool }
}

// NewRelay returns the screen relay. A request whose owner's pair is not
// running gets 409 "screen not running"; a dial failure gets 502 from the
// proxy's ErrorHandler.
func NewRelay(s runningChecker, a *Activity) *Relay {
	r := &Relay{running: s, activity: a}
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
			// Request-local values only: no shared mutable state, so
			// concurrent requests can never cross targets (ADR-0008).
			id := identity(pr.In)
			pr.Out.URL.Scheme = "http"
			pr.Out.URL.Host = "desk-firefox-" + id.User + ":5800"
			pr.Out.Host = pr.Out.URL.Host
			// /desk/vnc/ → /, /desk/vnc/websockify → /websockify; the query
			// is preserved (Out is a clone of In).
			pr.Out.URL.Path = strings.TrimPrefix(pr.In.URL.Path, vncPrefix)
			pr.Out.URL.RawPath = ""
			// The gate secret and identity headers never reach the
			// Firefox container (F74).
			access.StripIdentityHeaders(pr.Out.Header)
			// noVNC needs neither the collector's browser cookies nor
			// an Authorization header, and the Firefox container is the
			// least-trusted component: neither leaves the desk.
			pr.Out.Header.Del("Cookie")
			pr.Out.Header.Del("Authorization")
		},
		ErrorHandler: badGateway("relay"),
	}
	return r
}

// badGateway is the reverse proxies' ErrorHandler: it logs the error under
// name and answers a plain 502 "bad gateway", never the error text.
func badGateway(name string) func(http.ResponseWriter, *http.Request, error) {
	return func(w http.ResponseWriter, _ *http.Request, err error) {
		log.Printf("web: %s: %v", name, err)
		plain(w, http.StatusBadGateway, "bad gateway")
	}
}

// ServeHTTP relays the request to the identity's own Firefox container.
//
// A websocket upgrade is a screen: it runs under a cancellable context whose
// cancel is registered under the identity's user in the activity tracker,
// so CloseUser can end it (httputil.ReverseProxy closes an upgraded
// connection when the request context is done; the transport dials with a
// context that is never cancelled, so the dial seam cannot carry the close).
// Plain requests (noVNC's assets) are not screens and are not tracked.
func (r *Relay) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	id := identity(req)
	if r.Members != nil && r.Members.Absent(id.User) {
		plain(w, http.StatusForbidden, "access removed")
		return
	}
	if !r.running.IsRunning(req.Context(), id.User) {
		plain(w, http.StatusConflict, "screen not running")
		return
	}
	if isWebsocketUpgrade(req) {
		ctx, cancel := context.WithCancel(req.Context())
		defer cancel()
		closed := r.activity.openScreen(id.User, cancel)
		defer closed()
		req = req.WithContext(ctx)
	}
	r.proxy.ServeHTTP(w, req)
}

// isWebsocketUpgrade reports whether req asks to upgrade to a websocket: one
// of its Upgrade tokens is "websocket", in any case.
func isWebsocketUpgrade(req *http.Request) bool {
	for _, v := range req.Header.Values("Upgrade") {
		for _, tok := range strings.Split(v, ",") {
			if strings.EqualFold(strings.TrimSpace(tok), "websocket") {
				return true
			}
		}
	}
	return false
}
