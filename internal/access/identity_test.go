package access

import (
	"net/http"
	"testing"
)

func TestStripIdentityHeaders(t *testing.T) {
	h := http.Header{}
	h.Set(headerUser, "alice")
	h.Set(headerRoles, "src_co_admin")
	h.Set(headerGate, "gate-secret")
	h.Set("Accept", "text/html")
	h.Set("X-Forwarded-Host", "desk.example.org")

	StripIdentityHeaders(h)

	for _, name := range []string{headerUser, headerRoles, headerGate} {
		if _, ok := h[http.CanonicalHeaderKey(name)]; ok {
			t.Errorf("header %s survived StripIdentityHeaders: %v", name, h)
		}
	}
	if got := h.Get("Accept"); got != "text/html" {
		t.Errorf("Accept = %q, want it untouched", got)
	}
	if got := h.Get("X-Forwarded-Host"); got != "desk.example.org" {
		t.Errorf("X-Forwarded-Host = %q, want it untouched", got)
	}

	// Stripping a header set that never had them is a no-op, not a panic.
	plain := http.Header{}
	plain.Set("Accept", "*/*")
	StripIdentityHeaders(plain)
	if len(plain) != 1 || plain.Get("Accept") != "*/*" {
		t.Errorf("StripIdentityHeaders on a clean header set changed it: %v", plain)
	}
}
