package fourcat

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/daniellemccool/researchcloud-ddp-comments/internal/docker"
)

const (
	backendName = "desk-4cat-backend-1"
	dbName      = "desk-4cat-db-1"
)

var composeLabels = map[string]string{"com.docker.compose.project": "desk"}

// newFakeWith4CAT returns a Fake holding the two running 4CAT containers plus
// one unrelated container, so Exec works and FindContainers has something to
// find.
func newFakeWith4CAT(t *testing.T) *docker.Fake {
	t.Helper()
	f := docker.NewFake()
	ctx := context.Background()
	specs := []docker.ContainerSpec{
		{Name: backendName, Image: "4cat:1", Labels: withService("4cat-backend")},
		{Name: dbName, Image: "postgres:16", Labels: withService("4cat-db")},
		{Name: "desk-proxy-alice", Image: "proxy:1", Labels: map[string]string{"desk.user": "alice"}},
	}
	for _, spec := range specs {
		if _, err := f.ContainerCreate(ctx, spec); err != nil {
			t.Fatalf("ContainerCreate %s: %v", spec.Name, err)
		}
		if err := f.ContainerStart(ctx, spec.Name); err != nil {
			t.Fatalf("ContainerStart %s: %v", spec.Name, err)
		}
	}
	return f
}

func withService(service string) map[string]string {
	labels := map[string]string{"com.docker.compose.service": service}
	for k, v := range composeLabels {
		labels[k] = v
	}
	return labels
}

// setupServer stands in for the 4CAT frontend: while pending it serves
// /first-run/ with the "Complete 4CAT Setup" title; once set up it answers
// /first-run/ with a 302 to /. seen, when non-nil, receives the last path hit.
func setupServer(t *testing.T, pending bool, seen *string) *httptest.Server {
	t.Helper()
	page := "<html><head><title>4CAT</title></head><body>dashboard</body></html>"
	if pending {
		page = "<html><head><title>Complete 4CAT Setup</title></head><body>setup</body></html>"
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if seen != nil {
			*seen = r.URL.Path
		}
		if r.URL.Path == "/first-run/" && !pending {
			w.Header().Set("Location", "/")
			w.WriteHeader(http.StatusFound)
			return
		}
		_, _ = w.Write([]byte(page))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// loginServer stands in for the 4CAT frontend's /login/. mode "ok" answers
// 302 with the remember_token cookie when the form holds password "pw-alice"
// and 401 otherwise; mode "nocookie" answers 302 without any cookie.
func loginServer(t *testing.T, mode string, seen *map[string]string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/login/" {
			http.NotFound(w, r)
			return
		}
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad form", http.StatusBadRequest)
			return
		}
		if seen != nil {
			*seen = map[string]string{
				"method":       r.Method,
				"content-type": r.Header.Get("Content-Type"),
				"username":     r.PostForm.Get("username"),
				"password":     r.PostForm.Get("password"),
			}
		}
		if mode == "nocookie" {
			w.Header().Set("Location", "/")
			w.WriteHeader(http.StatusFound)
			return
		}
		if r.PostForm.Get("password") == "pw-alice" {
			w.Header().Add("Set-Cookie", "remember_token=tok-abc; Path=/")
			w.Header().Set("Location", "/")
			w.WriteHeader(http.StatusFound)
			return
		}
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// closedURL returns the URL of a server that is already shut down.
func closedURL(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	url := srv.URL
	srv.Close()
	return url
}

func TestSetupPending(t *testing.T) {
	ctx := context.Background()
	var seen string
	pending := setupServer(t, true, &seen)
	if ok, err := (&Client{FrontendURL: pending.URL}).SetupPending(ctx); err != nil || !ok {
		t.Errorf("SetupPending(pending) = %v, %v; want true, nil", ok, err)
	}
	if seen != "/first-run/" {
		t.Errorf("SetupPending requested %q; want /first-run/", seen)
	}
	done := setupServer(t, false, nil)
	if ok, err := (&Client{FrontendURL: done.URL}).SetupPending(ctx); err != nil || ok {
		t.Errorf("SetupPending(done) = %v, %v; want false, nil", ok, err)
	}
}

func TestSetupPendingRedirectMeansDone(t *testing.T) {
	// Once set up, /first-run/ answers with a redirect; never followed, and
	// it means not pending.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", "/")
		w.WriteHeader(http.StatusFound)
	}))
	t.Cleanup(srv.Close)
	if ok, err := (&Client{FrontendURL: srv.URL}).SetupPending(context.Background()); err != nil || ok {
		t.Errorf("SetupPending(302) = %v, %v; want false, nil", ok, err)
	}
}

func TestSetupPendingNotFoundMeansDone(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	if ok, err := (&Client{FrontendURL: srv.URL}).SetupPending(context.Background()); err != nil || ok {
		t.Errorf("SetupPending(404) = %v, %v; want false, nil", ok, err)
	}
}

func TestSetupUnreachable(t *testing.T) {
	_, err := (&Client{FrontendURL: closedURL(t)}).SetupPending(context.Background())
	if err == nil || !strings.HasPrefix(err.Error(), "4CAT unreachable:") {
		t.Errorf("SetupPending(closed) error = %v; want prefix \"4CAT unreachable:\"", err)
	}
}

func TestSetupPendingBadStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	t.Cleanup(srv.Close)
	_, err := (&Client{FrontendURL: srv.URL}).SetupPending(context.Background())
	if err == nil || err.Error() != "unexpected setup page status 502" {
		t.Errorf("SetupPending(502) error = %v; want \"unexpected setup page status 502\"", err)
	}
}

func TestAccountExists(t *testing.T) {
	ctx := context.Background()
	f := newFakeWith4CAT(t)
	c := &Client{Docker: f, Backend: backendName, DB: dbName}

	f.ExecResults["psql -v"] = docker.Exec{ExitCode: 0, Stdout: "1\n"}
	if ok, err := c.AccountExists(ctx, "alice"); err != nil || !ok {
		t.Errorf("AccountExists(stdout 1) = %v, %v; want true, nil", ok, err)
	}
	f.ExecResults["psql -v"] = docker.Exec{ExitCode: 0, Stdout: ""}
	if ok, err := c.AccountExists(ctx, "alice"); err != nil || ok {
		t.Errorf("AccountExists(empty) = %v, %v; want false, nil", ok, err)
	}
	f.ExecResults["psql -v"] = docker.Exec{ExitCode: 3, Stderr: "ERROR: relation does not exist"}
	_, err := c.AccountExists(ctx, "alice")
	if err == nil || err.Error() != "psql exited with status 3" {
		t.Errorf("AccountExists(exit 3) error = %v; want \"psql exited with status 3\"", err)
	}
	f.ExecResults["psql -v"] = docker.Exec{ExitCode: 0, Stdout: "1\n"}
	f.Fail["Exec "+dbName] = errors.New("daemon gone")
	_, err = c.AccountExists(ctx, "alice")
	if err == nil || err.Error() != "docker exec failed to run: *errors.errorString" {
		t.Errorf("AccountExists(transport) error = %v; want \"docker exec failed to run: *errors.errorString\"", err)
	}
	delete(f.Fail, "Exec "+dbName)

	// The exists query is the Python's, verbatim, in the DB container.
	if len(f.Execs) == 0 {
		t.Fatal("no Exec call recorded")
	}
	last := f.Execs[len(f.Execs)-1]
	if last.Container != dbName {
		t.Errorf("AccountExists exec container = %q; want %q", last.Container, dbName)
	}
	want := []string{"psql", "-v", "ON_ERROR_STOP=1", "-U", "fourcat", "-d", "fourcat", "-tAc",
		"SELECT 1 FROM users WHERE name = 'alice'"}
	if strings.Join(last.Cmd, "\x00") != strings.Join(want, "\x00") {
		t.Errorf("AccountExists argv = %q; want %q", last.Cmd, want)
	}
}

func TestCreateAccountCommand(t *testing.T) {
	ctx := context.Background()
	f := newFakeWith4CAT(t)
	c := &Client{Docker: f, Backend: backendName, DB: dbName}

	if err := c.CreateAccount(ctx, "alice", "s3cret", false); err != nil {
		t.Fatalf("CreateAccount(alice) error: %v", err)
	}
	if err := c.CreateAccount(ctx, "bob", "s3cret", true); err != nil {
		t.Fatalf("CreateAccount(bob, admin) error: %v", err)
	}
	if len(f.Execs) != 2 {
		t.Fatalf("len(Execs) = %d; want 2", len(f.Execs))
	}
	plain := f.Execs[0]
	if plain.Container != backendName {
		t.Errorf("exec container = %q; want %q", plain.Container, backendName)
	}
	wantPlain := []string{"python3", "helper-scripts/create_user.py", "-u", "alice", "-e", "-p", "s3cret"}
	if strings.Join(plain.Cmd, "\x00") != strings.Join(wantPlain, "\x00") {
		t.Errorf("CreateAccount argv = %q; want %q (no -a)", plain.Cmd, wantPlain)
	}
	admin := f.Execs[1]
	wantAdmin := []string{"python3", "helper-scripts/create_user.py", "-u", "bob", "-e", "-p", "s3cret", "-a"}
	if strings.Join(admin.Cmd, "\x00") != strings.Join(wantAdmin, "\x00") {
		t.Errorf("CreateAccount(admin) argv = %q; want %q (with -a)", admin.Cmd, wantAdmin)
	}

	f.ExecResults["python3 helper-scripts/create_user.py"] = docker.Exec{ExitCode: 1, Stderr: "boom"}
	err := c.CreateAccount(ctx, "alice", "s3cret", false)
	if err == nil || err.Error() != "create_user.py exited with status 1" {
		t.Errorf("CreateAccount(exit 1) error = %v; want \"create_user.py exited with status 1\"", err)
	}
}

func TestSetAdminSQL(t *testing.T) {
	ctx := context.Background()
	f := newFakeWith4CAT(t)
	f.ExecResults["psql -v"] = docker.Exec{ExitCode: 0, Stdout: "UPDATE 1\n"}
	c := &Client{Docker: f, Backend: backendName, DB: dbName}

	if err := c.SetAdmin(ctx, "alice", true); err != nil {
		t.Fatalf("SetAdmin(true) error: %v", err)
	}
	if err := c.SetAdmin(ctx, "alice", false); err != nil {
		t.Fatalf("SetAdmin(false) error: %v", err)
	}
	if len(f.Execs) != 2 {
		t.Fatalf("len(Execs) = %d; want 2", len(f.Execs))
	}
	// v1.57's create_user.py -a stores tags as the jsonb string "[\"admin\"]"
	// (a scalar), so the CASE normalises a non-array to '[]' before the
	// delete/|| (F81).
	wantTrue := `UPDATE users SET tags = (CASE WHEN jsonb_typeof(tags) = 'array' THEN tags - 'admin' ELSE '[]'::jsonb END) || '["admin"]'::jsonb WHERE name = 'alice'`
	wantFalse := `UPDATE users SET tags = (CASE WHEN jsonb_typeof(tags) = 'array' THEN tags - 'admin' ELSE '[]'::jsonb END) WHERE name = 'alice'`
	for i, want := range []string{wantTrue, wantFalse} {
		call := f.Execs[i]
		if call.Container != dbName {
			t.Errorf("SetAdmin exec container = %q; want %q", call.Container, dbName)
		}
		if len(call.Cmd) != 9 || call.Cmd[8] != want {
			t.Errorf("SetAdmin argv = %q; want psql argv with SQL %q", call.Cmd, want)
		}
	}
}

func TestSetAdminNoAccount(t *testing.T) {
	ctx := context.Background()
	f := newFakeWith4CAT(t)
	f.ExecResults["psql -v"] = docker.Exec{ExitCode: 0, Stdout: "UPDATE 0\n"}
	c := &Client{Docker: f, Backend: backendName, DB: dbName}

	err := c.SetAdmin(ctx, "alice", true)
	if err == nil || err.Error() != "no 4CAT account for alice" {
		t.Errorf("SetAdmin(UPDATE 0) error = %v; want \"no 4CAT account for alice\"", err)
	}
}

func TestLoginTokenOK(t *testing.T) {
	ctx := context.Background()
	// A caller-supplied HTTP client is hardened too: redirects refused, so
	// the 302 with Location: / is read as-is and its cookie is returned.
	var seen map[string]string
	srv := loginServer(t, "ok", &seen)
	c := &Client{FrontendURL: srv.URL, HTTP: &http.Client{}}

	token, err := c.LoginToken(ctx, "alice", "pw-alice")
	if err != nil || token != "tok-abc" {
		t.Fatalf("LoginToken = %q, %v; want \"tok-abc\", nil", token, err)
	}
	if seen["method"] != http.MethodPost {
		t.Errorf("login method = %q; want POST", seen["method"])
	}
	if seen["content-type"] != "application/x-www-form-urlencoded" {
		t.Errorf("login content-type = %q; want application/x-www-form-urlencoded", seen["content-type"])
	}
	if seen["username"] != "alice" || seen["password"] != "pw-alice" {
		t.Errorf("login form = %v; want username=alice password=pw-alice", seen)
	}
}

func TestLoginRefused(t *testing.T) {
	ctx := context.Background()
	srv := loginServer(t, "ok", nil)
	c := &Client{FrontendURL: srv.URL}

	_, err := c.LoginToken(ctx, "alice", "wrong")
	var loginErr *LoginError
	if !errors.As(err, &loginErr) {
		t.Fatalf("LoginToken(401) error = %v (%T); want *LoginError", err, err)
	}
	if err.Error() != "login refused for alice" {
		t.Errorf("LoginError text = %q; want \"login refused for alice\"", err.Error())
	}

	nocookie := loginServer(t, "nocookie", nil)
	_, err = (&Client{FrontendURL: nocookie.URL}).LoginToken(ctx, "alice", "pw-alice")
	if !errors.As(err, &loginErr) {
		t.Errorf("LoginToken(302 no cookie) error = %v (%T); want *LoginError", err, err)
	}
}

func TestLoginUnexpectedStatus(t *testing.T) {
	ctx := context.Background()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)
	_, err := (&Client{FrontendURL: srv.URL}).LoginToken(ctx, "alice", "pw-alice")
	if err == nil || err.Error() != "unexpected login response status 500" {
		t.Errorf("LoginToken(500) error = %v; want \"unexpected login response status 500\"", err)
	}
}

func TestLoginUnreachable(t *testing.T) {
	_, err := (&Client{FrontendURL: closedURL(t)}).LoginToken(context.Background(), "alice", "pw-alice")
	if err == nil || !strings.HasPrefix(err.Error(), "4CAT unreachable:") {
		t.Errorf("LoginToken(closed) error = %v; want prefix \"4CAT unreachable:\"", err)
	}
}

func TestFindContainersByLabels(t *testing.T) {
	ctx := context.Background()
	f := newFakeWith4CAT(t)

	// A `docker compose run` oneoff of the backend service is ignored.
	oneoff := docker.ContainerSpec{
		Name:  "desk-4cat-backend-run-1",
		Image: "4cat:1",
		Labels: map[string]string{
			"com.docker.compose.project": "desk",
			"com.docker.compose.service": "4cat-backend",
			"com.docker.compose.oneoff":  "True",
		},
	}
	if _, err := f.ContainerCreate(ctx, oneoff); err != nil {
		t.Fatalf("ContainerCreate oneoff: %v", err)
	}

	backend, db, err := FindContainers(ctx, f)
	if err != nil {
		t.Fatalf("FindContainers error: %v", err)
	}
	if backend != backendName || db != dbName {
		t.Errorf("FindContainers = %q, %q; want %q, %q", backend, db, backendName, dbName)
	}

	if err := f.ContainerRemove(ctx, backendName); err != nil {
		t.Fatalf("ContainerRemove: %v", err)
	}
	_, _, err = FindContainers(ctx, f)
	if err == nil || err.Error() != "4CAT containers not found" {
		t.Errorf("FindContainers(backend missing) error = %v; want \"4CAT containers not found\"", err)
	}
}

func TestLazyLookup(t *testing.T) {
	ctx := context.Background()

	// Empty Backend/DB: the first use finds the containers and stores them.
	f := newFakeWith4CAT(t)
	f.ExecResults["psql -v"] = docker.Exec{ExitCode: 0, Stdout: "1\n"}
	c := &Client{Docker: f}
	if ok, err := c.AccountExists(ctx, "alice"); err != nil || !ok {
		t.Fatalf("AccountExists(lazy) = %v, %v; want true, nil", ok, err)
	}
	if c.Backend != backendName || c.DB != dbName {
		t.Errorf("after lazy lookup Backend, DB = %q, %q; want %q, %q", c.Backend, c.DB, backendName, dbName)
	}
	if err := c.CreateAccount(ctx, "alice", "s3cret", false); err != nil {
		t.Errorf("CreateAccount(lazy) error: %v", err)
	}
	if len(f.Execs) != 2 {
		t.Fatalf("len(Execs) = %d; want 2", len(f.Execs))
	}
	if f.Execs[0].Container != dbName || f.Execs[1].Container != backendName {
		t.Errorf("lazy exec containers = %q, %q; want %q then %q",
			f.Execs[0].Container, f.Execs[1].Container, dbName, backendName)
	}

	// No containers at all: every container-needing method returns the
	// not-found error and records no exec.
	empty := docker.NewFake()
	c = &Client{Docker: empty}
	for name, call := range map[string]func() error{
		"AccountExists": func() error { _, err := c.AccountExists(ctx, "alice"); return err },
		"CreateAccount": func() error { return c.CreateAccount(ctx, "alice", "s3cret", false) },
		"SetAdmin":      func() error { return c.SetAdmin(ctx, "alice", true) },
	} {
		err := call()
		if err == nil || err.Error() != "4CAT containers not found" {
			t.Errorf("%s(no containers) error = %v; want \"4CAT containers not found\"", name, err)
		}
	}
	if len(empty.Execs) != 0 {
		t.Errorf("Execs recorded without containers = %v; want none", empty.Execs)
	}
}

func TestLazyLookupConcurrent(t *testing.T) {
	ctx := context.Background()
	f := newFakeWith4CAT(t)
	f.ExecResults["psql -v"] = docker.Exec{ExitCode: 0, Stdout: "1\n"}
	c := &Client{Docker: f}

	const n = 20
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ok, err := c.AccountExists(ctx, "alice")
			if err == nil && !ok {
				err = errors.New("AccountExists returned false")
			}
			errs[i] = err
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Errorf("goroutine %d: %v", i, err)
		}
	}
	if c.Backend != backendName || c.DB != dbName {
		t.Errorf("after concurrent lookup Backend, DB = %q, %q; want %q, %q", c.Backend, c.DB, backendName, dbName)
	}
	lists := 0
	for _, call := range f.Calls {
		if call == "ContainerList" {
			lists++
		}
	}
	if lists != 2 {
		t.Errorf("ContainerList calls = %d; want 2 (one pair, the lookup ran once)", lists)
	}
}

func TestBadUsernameRefusedBeforeSQL(t *testing.T) {
	ctx := context.Background()
	f := newFakeWith4CAT(t)
	c := &Client{Docker: f, Backend: backendName, DB: dbName, FrontendURL: "http://4cat-frontend:5000"}

	for _, user := range []string{"alice; drop", "Alice"} {
		if _, err := c.AccountExists(ctx, user); err == nil || !strings.Contains(err.Error(), "invalid username") {
			t.Errorf("AccountExists(%q) error = %v; want invalid username", user, err)
		}
		if err := c.CreateAccount(ctx, user, "pw", false); err == nil || !strings.Contains(err.Error(), "invalid username") {
			t.Errorf("CreateAccount(%q) error = %v; want invalid username", user, err)
		}
		if err := c.SetAdmin(ctx, user, true); err == nil || !strings.Contains(err.Error(), "invalid username") {
			t.Errorf("SetAdmin(%q) error = %v; want invalid username", user, err)
		}
		if _, err := c.LoginToken(ctx, user, "pw"); err == nil || !strings.Contains(err.Error(), "invalid username") {
			t.Errorf("LoginToken(%q) error = %v; want invalid username", user, err)
		}
	}
	if len(f.Execs) != 0 {
		t.Errorf("Execs recorded for bad usernames = %v; want none", f.Execs)
	}
}

func TestNewPassword(t *testing.T) {
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"
	a, b := NewPassword(), NewPassword()
	if len(a) != 32 {
		t.Errorf("len(NewPassword()) = %d; want 32", len(a))
	}
	for _, ch := range a {
		if !strings.ContainsRune(alphabet, ch) {
			t.Errorf("NewPassword() = %q contains %q, outside the URL-safe alphabet", a, ch)
		}
	}
	if a == b {
		t.Errorf("two NewPassword() calls both = %q; want different", a)
	}
	// A leading '-' or '_' would break create_user.py's -p argv; 2000 draws
	// must never produce one.
	for i := 0; i < 2000; i++ {
		p := NewPassword()
		if len(p) != 32 {
			t.Fatalf("NewPassword() = %q, len %d; want 32", p, len(p))
		}
		if p[0] == '-' || p[0] == '_' {
			t.Fatalf("NewPassword() = %q starts with %q; want neither '-' nor '_'", p, p[0])
		}
	}
}
