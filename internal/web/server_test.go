package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/daniellemccool/researchcloud-ddp-comments/internal/access"
	"github.com/daniellemccool/researchcloud-ddp-comments/internal/config"
	"github.com/daniellemccool/researchcloud-ddp-comments/internal/platform"
)

const (
	testGate      = "gate-secret-for-tests"
	collectorRole = "src_co_students"
	adminRole     = "src_co_admin"
	testContact   = "desk-admin@example.org"
)

// newTestServer builds the gated server over a temp desk root, with the
// repository's tiktok platform definition and a temp access log. It returns
// the server and the log path.
func newTestServer(t *testing.T) (*http.Server, string) {
	t.Helper()
	root := t.TempDir()
	plat, err := platform.Load(filepath.Join("..", "..", "platforms", "tiktok", "platform.json"))
	if err != nil {
		t.Fatalf("platform.Load: %v", err)
	}
	cfg := &config.Config{
		DeskRoot:      root,
		FQDN:          "desk.example.org",
		CollectorRole: collectorRole,
		AdminRole:     adminRole,
		Contact:       testContact,
		Platform:      "tiktok",
	}
	logPath := filepath.Join(root, "state", "access.ndjson")
	ac := access.Config{CollectorRole: collectorRole, AdminRole: adminRole, GateSecret: testGate}
	return New(cfg, ac, plat, logPath), logPath
}

// do sends one GET through the gated handler and returns the recorded
// response.
func do(t *testing.T, srv *http.Server, user, roles, gate, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	access.SetTestHeaders(req, user, roles, gate)
	rec := httptest.NewRecorder()
	srv.Handler.ServeHTTP(rec, req)
	return rec
}

func TestNonMember403HasContact(t *testing.T) {
	srv, _ := newTestServer(t)
	rec := do(t, srv, "someone", "src_co_other", testGate, "/desk/")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
	if b := rec.Body.String(); !strings.Contains(b, testContact) {
		t.Errorf("body %q does not contain the contact %q", b, testContact)
	}
}

func TestCollectorNotReadyPage(t *testing.T) {
	srv, _ := newTestServer(t)
	rec := do(t, srv, "dmccool2", collectorRole, testGate, "/desk/")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	b := rec.Body.String()
	if !strings.Contains(b, "not ready yet") || !strings.Contains(b, testContact) {
		t.Errorf("body %q lacks the not-ready wording or the contact", b)
	}
}

func TestAdminPlaceholder(t *testing.T) {
	srv, _ := newTestServer(t)
	rec := do(t, srv, "dmccool2", adminRole, testGate, "/desk/")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	b := rec.Body.String()
	if !strings.Contains(b, "Desk admin") || !strings.Contains(b, "dmccool2") {
		t.Errorf("body %q lacks the title or the username", b)
	}
}

func TestForgedGate(t *testing.T) {
	srv, _ := newTestServer(t)
	rec := do(t, srv, "dmccool2", adminRole, "forged-secret", "/desk/")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
	b := rec.Body.String()
	if strings.Contains(b, "dmccool2") || strings.Contains(b, testContact) {
		t.Errorf("body %q leaks the username or the contact", b)
	}
}

func TestFourcatNotYet(t *testing.T) {
	srv, _ := newTestServer(t)
	for _, path := range []string{"/fourcat/", "/fourcat/anything"} {
		rec := do(t, srv, "dmccool2", adminRole, testGate, path)
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("%s: status = %d, want 503", path, rec.Code)
		}
		if b := rec.Body.String(); !strings.Contains(b, "4CAT access through the desk is not yet available") {
			t.Errorf("%s: body %q lacks the not-yet text", path, b)
		}
	}
}

func TestContainerListenerOnlyHealthz(t *testing.T) {
	srv := NewContainer()
	cases := []struct {
		path     string
		headers  bool
		want     int
		wantBody string
	}{
		{"/healthz", false, http.StatusOK, "ok"},
		{"/desk/", false, http.StatusNotFound, ""},
		{"/healthz", true, http.StatusOK, "ok"},
		{"/desk/", true, http.StatusNotFound, ""},
	}
	for _, c := range cases {
		req := httptest.NewRequest(http.MethodGet, c.path, nil)
		if c.headers {
			access.SetTestHeaders(req, "dmccool2", adminRole, testGate)
		}
		rec := httptest.NewRecorder()
		srv.Handler.ServeHTTP(rec, req)
		if rec.Code != c.want {
			t.Errorf("%s (headers=%v): status = %d, want %d", c.path, c.headers, rec.Code, c.want)
		}
		if c.wantBody != "" && rec.Body.String() != c.wantBody {
			t.Errorf("%s: body = %q, want %q", c.path, rec.Body.String(), c.wantBody)
		}
	}
}

func TestAccessLoggedOncePerChange(t *testing.T) {
	srv, logPath := newTestServer(t)

	// Three requests by dmccool2 with the same roles → one line.
	for i := 0; i < 3; i++ {
		do(t, srv, "dmccool2", collectorRole, testGate, "/desk/")
	}
	// A fourth with changed roles → a second line.
	do(t, srv, "dmccool2", collectorRole+";"+adminRole, testGate, "/desk/")
	// A non-member's 403 is logged too (valid user).
	do(t, srv, "someone", "src_co_other", testGate, "/desk/")
	// A forged-gate request is never logged.
	do(t, srv, "dmccool2", adminRole, "forged-secret", "/desk/")

	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("reading log: %v", err)
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("log has %d lines, want 3:\n%s", len(lines), data)
	}
	type entry struct {
		T         int64    `json:"t"`
		User      string   `json:"user"`
		Roles     []string `json:"roles"`
		Collector bool     `json:"collector"`
		Admin     bool     `json:"admin"`
		Path      string   `json:"path"`
	}
	var entries []entry
	for _, line := range lines {
		var e entry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			t.Fatalf("line %q is not JSON: %v", line, err)
		}
		if e.T <= 0 || e.User == "" || e.Path == "" {
			t.Errorf("entry %+v lacks t, user or path", e)
		}
		entries = append(entries, e)
	}
	if entries[0].User != "dmccool2" || !entries[0].Collector || entries[0].Admin {
		t.Errorf("entry[0] = %+v, want dmccool2 collector not admin", entries[0])
	}
	if len(entries[1].Roles) != 2 || !entries[1].Admin || !entries[1].Collector {
		t.Errorf("entry[1] = %+v, want two roles, admin and collector", entries[1])
	}
	if entries[2].User != "someone" || entries[2].Collector || entries[2].Admin {
		t.Errorf("entry[2] = %+v, want someone, neither collector nor admin", entries[2])
	}
}
