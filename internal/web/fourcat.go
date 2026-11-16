package web

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"

	"github.com/daniellemccool/researchcloud-ddp-comments/internal/access"
	"github.com/daniellemccool/researchcloud-ddp-comments/internal/provision"
)

// fourcatPrefix is the mount path stripped before forwarding.
const fourcatPrefix = "/fourcat"

// fourcatStudy is what the proxy needs of fourcat.Study.
type fourcatStudy interface {
	Ready() bool
	SetupPending(ctx context.Context) (bool, error)
	Secret() (string, error)
}

// FourcatDeps wires the /fourcat/ proxy: the desk's FQDN and contact, the
// study (whether 4CAT's settings are asserted, its setup state, the proxy
// secret), the token reader (provision.APIToken) and the seatless account
// step (Provisioner.AdvanceFourcat).
type FourcatDeps struct {
	FQDN        string
	Contact     string
	Study       fourcatStudy
	Token       func(user string) (string, error)
	MakeAccount func(ctx context.Context, user string) provision.State
}

// FourcatProxy is the /fourcat/ handler: a reverse proxy to the bundled
// 4CAT web UI (http://4cat-frontend:5000), reached by admins and Tech Admins
// through the gated listener. Every request is signed in with the person's
// own API token (Authentication) and carries their standing as 4CAT's proxy
// tags (X-4CAT-Config-Via-Proxy with the desk's secret, X-4CAT-Config-Tag),
// from the roles of that request's verified identity; nothing about standing
// is stored in 4CAT. The browser's own credentials and standing headers, any
// header spelled with an underscore, the access-token query parameter and
// 4CAT's Set-Cookie never pass. A request from another website is refused,
// except a GET of the bare start page. Until 4CAT's first-run setup is
// complete it is a plain pass-through with cookies, so the setup form works;
// after that it answers the not-ready page until the study settings are
// asserted. The /fourcat prefix is stripped, Host and X-Forwarded-Host are the
// desk's FQDN and X-Forwarded-Proto is https; 4CAT's redirects reach the
// browser unmodified.
type FourcatProxy struct {
	deps  FourcatDeps
	pages *pages
	proxy *httputil.ReverseProxy

	// Dial is the transport's dial function; nil means a plain net.Dialer.
	Dial func(ctx context.Context, network, addr string) (net.Conn, error)
}

// fourcatCredentials are the request headers a browser could use to act in
// 4CAT as someone else or with a standing it does not hold.
var fourcatCredentials = []string{
	"Cookie", "Authentication", "Authorization", "X-4CAT-Config-Via-Proxy", "X-4CAT-Config-Tag",
}

// fourcatPass is the per-request decision the handler hands the proxy.
type fourcatPass struct {
	setup    bool   // first-run pass-through: cookies both ways, no sign-in
	token    string // the person's API token
	secret   string // the proxy secret
	tags     string // "studylead", "techadmin" or both
	hideLogs bool   // GET /logs/: hide the token and the secret in the body
}

type fourcatPassKey struct{}

// NewFourcatProxy returns the 4CAT proxy.
func NewFourcatProxy(d FourcatDeps) *FourcatProxy {
	p := &FourcatProxy{deps: d, pages: mustLoadPages()}
	transport := &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			if p.Dial != nil {
				return p.Dial(ctx, network, addr)
			}
			var dl net.Dialer
			return dl.DialContext(ctx, network, addr)
		},
	}
	p.proxy = &httputil.ReverseProxy{
		Transport:      transport,
		FlushInterval:  -1,
		Rewrite:        p.rewrite,
		ModifyResponse: p.modifyResponse,
		ErrorHandler:   badGateway("fourcat"),
	}
	return p
}

func (p *FourcatProxy) rewrite(pr *httputil.ProxyRequest) {
	pass, _ := pr.In.Context().Value(fourcatPassKey{}).(*fourcatPass)
	pr.Out.URL.Scheme = "http"
	pr.Out.URL.Host = "4cat-frontend:5000"
	pr.Out.URL.Path = strings.TrimPrefix(pr.In.URL.Path, fourcatPrefix)
	pr.Out.URL.RawPath = ""
	pr.Out.URL.RawQuery = withoutAccessToken(pr.In.URL.RawQuery)
	pr.SetXForwarded()
	pr.Out.Header.Set("X-Forwarded-Host", p.deps.FQDN)
	pr.Out.Header.Set("X-Forwarded-Proto", "https")
	pr.Out.Host = p.deps.FQDN
	// The gate secret and identity headers never reach 4CAT (F74).
	access.StripIdentityHeaders(pr.Out.Header)
	// A header spelled with an underscore reaches the WSGI app under the same
	// name as its dashed twin: none passes.
	for name := range pr.Out.Header {
		if strings.Contains(name, "_") {
			pr.Out.Header.Del(name)
		}
	}
	for _, h := range fourcatCredentials {
		if h == "Cookie" && pass != nil && pass.setup {
			continue // the setup form runs on 4CAT's own session
		}
		pr.Out.Header.Del(h)
	}
	if pass == nil || pass.setup {
		return
	}
	pr.Out.Header.Set("Authentication", pass.token)
	pr.Out.Header.Set("X-4CAT-Config-Via-Proxy", pass.secret)
	pr.Out.Header.Set("X-4CAT-Config-Tag", pass.tags)
	if pass.hideLogs {
		pr.Out.Header.Set("Accept-Encoding", "identity")
	}
}

func (p *FourcatProxy) modifyResponse(resp *http.Response) error {
	pass, _ := resp.Request.Context().Value(fourcatPassKey{}).(*fourcatPass)
	if pass == nil || pass.setup {
		return nil
	}
	resp.Header.Del("Set-Cookie")
	resp.Header.Del("Set-Cookie2")
	if !pass.hideLogs {
		return nil
	}
	// 4CAT's logs page prints the headers of the request that opened it.
	if ce := resp.Header.Get("Content-Encoding"); ce != "" && ce != "identity" {
		_ = resp.Body.Close()
		return errors.New("logs page came back encoded")
	}
	body, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		return err
	}
	for _, s := range []string{pass.token, pass.secret} {
		body = bytes.ReplaceAll(body, []byte(s), []byte("(hidden)"))
	}
	resp.Body = io.NopCloser(bytes.NewReader(body))
	resp.ContentLength = int64(len(body))
	resp.Header.Set("Content-Length", strconv.Itoa(len(body)))
	return nil
}

// ServeHTTP decides how the request may reach 4CAT and proxies it.
func (p *FourcatProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	id := identity(r)
	tags := standingTags(id)
	if tags == "" {
		p.pages.render(w, http.StatusForbidden, "403", pageData{Title: "Not registered", Contact: p.deps.Contact})
		return
	}
	if !p.fromOurSite(r) {
		p.pages.render(w, http.StatusForbidden, "elsewhere", pageData{Title: "Open 4CAT from the desk"})
		return
	}
	if !p.deps.Study.Ready() {
		pending, err := p.deps.Study.SetupPending(r.Context())
		if err != nil || !pending {
			p.notReady(w, "")
			return
		}
		p.proxy.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), fourcatPassKey{}, &fourcatPass{setup: true})))
		return
	}
	token, err := p.deps.Token(id.User)
	if errors.Is(err, provision.ErrNoToken) {
		st := p.deps.MakeAccount(r.Context(), id.User)
		if e := st.Steps["fourcat_account"]; strings.HasPrefix(e, "error: ") {
			p.notReady(w, strings.TrimPrefix(e, "error: "))
			return
		}
		if e := st.Steps["fourcat_apitoken"]; strings.HasPrefix(e, "error: ") {
			p.notReady(w, strings.TrimPrefix(e, "error: "))
			return
		}
		token, err = p.deps.Token(id.User)
	}
	if err != nil {
		log.Printf("web: fourcat: token for %s: %T", id.User, err)
		p.notReady(w, "")
		return
	}
	secret, err := p.deps.Study.Secret()
	if err != nil {
		p.notReady(w, "")
		return
	}
	pass := &fourcatPass{
		token:    token,
		secret:   secret,
		tags:     tags,
		hideLogs: r.Method == http.MethodGet && r.URL.Path == fourcatPrefix+"/logs/",
	}
	p.proxy.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), fourcatPassKey{}, pass)))
}

func (p *FourcatProxy) notReady(w http.ResponseWriter, detail string) {
	p.pages.render(w, http.StatusServiceUnavailable, "notready", pageData{Title: "Desk", Contact: p.deps.Contact, Detail: detail})
}

// standingTags are the 4CAT tags of an identity's standing: studylead for
// an admin, techadmin for a Tech Admin, both comma-separated, or "".
func standingTags(id access.Identity) string {
	var tags []string
	if id.Admin {
		tags = append(tags, "studylead")
	}
	if id.TechAdmin {
		tags = append(tags, "techadmin")
	}
	return strings.Join(tags, ",")
}

// fromOurSite applies the cross-site rule. Every gated request is signed in
// with the person's standing, and 4CAT neither protects its forms against
// other sites nor keeps changes off plain links, so a request passes only
// when the browser says it came from our own pages or a typed address
// (Sec-Fetch-Site same-origin or none), or, from a browser that sends no such
// header, when Origin or Referer names exactly our host. One exception: a GET
// of the bare start page passes from anywhere, because the return from the
// SRC sign-in is a cross-site navigation and the start page changes nothing.
// Under /api/, where 4CAT changes state on plain GETs, "none" is refused too:
// a browser sends it for a link opened from a desktop mail or chat program as
// much as for a typed address (Danielle, 2026-11-16, after the sandbox test).
func (p *FourcatProxy) fromOurSite(r *http.Request) bool {
	if r.Method == http.MethodGet && r.URL.Path == fourcatPrefix+"/" && r.URL.RawQuery == "" {
		return true
	}
	if site := r.Header.Get("Sec-Fetch-Site"); site != "" {
		if site == "none" {
			return !strings.HasPrefix(r.URL.Path, fourcatPrefix+"/api/")
		}
		return site == "same-origin"
	}
	for _, h := range []string{"Origin", "Referer"} {
		if v := r.Header.Get(h); v != "" {
			u, err := url.Parse(v)
			return err == nil && u.Host == p.deps.FQDN
		}
	}
	return false
}

// withoutAccessToken drops every access-token pair from a raw query, pair by
// pair, so the other pairs reach 4CAT byte for byte; a pair's name is
// unescaped before the comparison.
func withoutAccessToken(raw string) string {
	if raw == "" {
		return ""
	}
	kept := make([]string, 0, 2)
	for _, pair := range strings.Split(raw, "&") {
		name, _, _ := strings.Cut(pair, "=")
		if n, err := url.QueryUnescape(name); err == nil && n == "access-token" {
			continue
		}
		kept = append(kept, pair)
	}
	return strings.Join(kept, "&")
}
