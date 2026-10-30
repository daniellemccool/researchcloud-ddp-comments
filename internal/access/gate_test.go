package access

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const (
	testGate          = "0123456789abcdef0123456789abcdef"
	testCollectorRole = "src_co_students"
	testAdminRole     = "src_co_admin"
)

func testConfig() Config {
	return Config{CollectorRole: testCollectorRole, AdminRole: testAdminRole, GateSecret: testGate}
}

func newRequest(user, roles, gate string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/desk/", nil)
	SetTestHeaders(r, user, roles, gate)
	return r
}

func TestWrongSecretIs403(t *testing.T) {
	r := newRequest("dmccool2", testCollectorRole, "not-the-secret")
	if _, err := FromRequest(r, testConfig()); !errors.Is(err, ErrGate) {
		t.Fatalf("err = %v, want ErrGate", err)
	}
}

func TestMissingSecretIs403(t *testing.T) {
	r := newRequest("dmccool2", testCollectorRole, "")
	if _, err := FromRequest(r, testConfig()); !errors.Is(err, ErrGate) {
		t.Fatalf("err = %v, want ErrGate", err)
	}
}

func TestEmptyRolesIs403(t *testing.T) {
	// Right gate, valid user, roles header present but empty: no error, and
	// the identity is neither collector nor admin (the web layer answers 403).
	r := newRequest("dmccool2", "", testGate)
	id, err := FromRequest(r, testConfig())
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if id.User != "dmccool2" {
		t.Errorf("User = %q, want dmccool2", id.User)
	}
	if len(id.Roles) != 0 {
		t.Errorf("Roles = %v, want empty", id.Roles)
	}
	if id.Collector || id.Admin {
		t.Errorf("Collector=%v Admin=%v, want both false", id.Collector, id.Admin)
	}
}

func TestBadUsernameIs403(t *testing.T) {
	cases := []struct {
		user string
		want error
	}{
		{"J.Smith", ErrBadUser},
		{"../x", ErrBadUser},
		{"", ErrNoUser},
		{strings.Repeat("a", 33), ErrBadUser}, // 33 chars: one too many
		{"1abc", ErrBadUser},                  // leading digit
		{"Dmccool2", ErrBadUser},              // uppercase
		{" dmccool2 ", ErrBadUser},            // padded: the raw value is validated
	}
	for _, c := range cases {
		r := newRequest(c.user, testCollectorRole, testGate)
		if _, err := FromRequest(r, testConfig()); !errors.Is(err, c.want) {
			t.Errorf("user %q: err = %v, want %v", c.user, err, c.want)
		}
	}
}

func TestEmptyConfiguredSecretFailsClosed(t *testing.T) {
	// An empty configured secret must never match, not even an empty gate
	// header with admin roles forged on top.
	cfg := Config{CollectorRole: testCollectorRole, AdminRole: testAdminRole, GateSecret: ""}
	r := newRequest("dmccool2", testAdminRole, "")
	if _, err := FromRequest(r, cfg); !errors.Is(err, ErrGate) {
		t.Fatalf("err = %v, want ErrGate", err)
	}
}

func TestAdminIsCollector(t *testing.T) {
	r := newRequest("dmccool2", testAdminRole, testGate)
	id, err := FromRequest(r, testConfig())
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if !id.Admin || !id.Collector {
		t.Errorf("Admin=%v Collector=%v, want both true", id.Admin, id.Collector)
	}
}

func TestCollectorOnly(t *testing.T) {
	r := newRequest("dmccool2", testCollectorRole, testGate)
	id, err := FromRequest(r, testConfig())
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if !id.Collector || id.Admin {
		t.Errorf("Collector=%v Admin=%v, want Collector true, Admin false", id.Collector, id.Admin)
	}
	if len(id.Roles) != 1 || id.Roles[0] != testCollectorRole {
		t.Errorf("Roles = %v, want [%s]", id.Roles, testCollectorRole)
	}
}
