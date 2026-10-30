// Package access is the only package in the module that reads the three
// identity headers set by the authenticating reverse proxy (ADR-0003):
// the gate secret, the username and the roles. Every other package gets the
// identity through FromRequest.
package access

import (
	"errors"
	"net/http"
	"regexp"
)

// The three identity header names. They may appear only in this package;
// internal/arch/identity_test.go enforces that.
const (
	headerUser  = "REMOTE_USER"
	headerRoles = "X-Desk-Roles"
	headerGate  = "X-Desk-Gate"
)

// userRe is the only username shape the desk ever uses in a path or a
// container name.
var userRe = regexp.MustCompile(`^[a-z][a-z0-9._-]{0,31}$`)

// Identity is the authenticated requester as the desk sees it.
type Identity struct {
	User      string
	Roles     []string
	Collector bool
	Admin     bool
}

// Config carries the values FromRequest needs: the two roles that make a
// collector or an admin, and the gate secret the proxy was given.
type Config struct {
	CollectorRole string
	AdminRole     string
	GateSecret    string
}

// ErrGate means the gate secret was missing or wrong; no other header of
// such a request may be inspected or logged. ErrNoUser means the username
// header was missing or empty. ErrBadUser means the username does not match
// the allowed shape.
var (
	ErrGate    = errors.New("gate secret missing or wrong")
	ErrNoUser  = errors.New("no username")
	ErrBadUser = errors.New("username not allowed")
)

// FromRequest is the only reader of the three identity headers in the whole
// module. The gate is checked first, in constant time; a request whose gate
// is wrong has its other headers ignored entirely. Then the RAW username
// value is validated (no trimming: " dmccool2 " is not a valid name), then
// the roles are split into whole ';'-separated segments.
// Admin implies Collector.
func FromRequest(r *http.Request, c Config) (Identity, error) {
	if !gateOK(r, c.GateSecret) {
		return Identity{}, ErrGate
	}
	user := r.Header.Get(headerUser)
	if user == "" {
		return Identity{}, ErrNoUser
	}
	if !userRe.MatchString(user) {
		return Identity{}, ErrBadUser
	}
	rolesHeader := r.Header.Get(headerRoles)
	id := Identity{
		User:      user,
		Roles:     splitRoles(rolesHeader),
		Collector: IsRole(rolesHeader, c.CollectorRole),
		Admin:     IsRole(rolesHeader, c.AdminRole),
	}
	if id.Admin {
		id.Collector = true
	}
	return id, nil
}

// SetTestHeaders sets the three identity headers on r. It is exported for
// tests in other packages, which must not name the headers themselves
// (internal/arch/identity_test.go walks _test.go files too). An empty value
// still sets the header to the empty string.
func SetTestHeaders(r *http.Request, user, roles, gate string) {
	r.Header.Set(headerUser, user)
	r.Header.Set(headerRoles, roles)
	r.Header.Set(headerGate, gate)
}
