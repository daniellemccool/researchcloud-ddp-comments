package access

import (
	"crypto/subtle"
	"net/http"
)

// gateOK reports whether the request carries the gate secret. The comparison
// is constant-time so the answer does not leak how much of a forged secret
// was right. A missing header reads as the empty string and fails the same
// way. An EMPTY configured secret never matches: the gate must fail closed,
// never open.
func gateOK(r *http.Request, secret string) bool {
	if secret == "" {
		return false
	}
	got := r.Header.Get(headerGate)
	return subtle.ConstantTimeCompare([]byte(got), []byte(secret)) == 1
}
