package main

import (
	"net"
	"net/http"
	"strings"

	"github.com/elazarl/goproxy"
)

// deniedBody is the body of every refusal the proxy itself answers.
const deniedBody = "deskproxy: denied by rule"

// deniedHost reports whether the proxy refuses to forward to host (a bare
// host, port already stripped). This is the fail-closed stub of the boundary:
// loopback names, IP literals, dotless names and every .desk name are
// refused, fourcat.desk included, until Task 4 adds the 4CAT allowlist.
func deniedHost(host string) bool {
	h := strings.ToLower(strings.TrimSuffix(strings.Trim(host, "[]"), "."))
	if h == "" || h == "localhost" || strings.HasSuffix(h, ".localhost") {
		return true
	}
	if net.ParseIP(h) != nil {
		return true
	}
	if !strings.Contains(h, ".") {
		return true
	}
	return strings.HasSuffix(h, ".desk")
}

// denyResponse answers a request to a refused host with 403 from the proxy
// itself, or returns nil when the host is not refused.
func denyResponse(req *http.Request) *http.Response {
	if !deniedHost(requestHost(req)) {
		return nil
	}
	return goproxy.NewResponse(req, goproxy.ContentTypeText, http.StatusForbidden, deniedBody)
}
