package provision

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"

	"github.com/daniellemccool/researchcloud-ddp-comments/internal/config"
	"github.com/daniellemccool/researchcloud-ddp-comments/internal/docker"
	"github.com/daniellemccool/researchcloud-ddp-comments/internal/fourcat"
	"github.com/daniellemccool/researchcloud-ddp-comments/internal/locks"
	"github.com/daniellemccool/researchcloud-ddp-comments/internal/registry"
)

const (
	backendName = "desk-4cat-backend-1"
	dbName      = "desk-4cat-db-1"
	generation  = "tiktok-1.14.3"
)

// testEnv is one temp desk root wired end to end: a config, a registry, a
// docker.Fake holding the two running 4CAT containers, and an httptest
// server standing in for the 4CAT frontend (setup page + login form).
type testEnv struct {
	t    *testing.T
	cfg  *config.Config
	root string
	reg  *registry.Registry
	fake *docker.Fake
	prov *Provisioner

	mu            sync.Mutex
	pending       bool // the frontend still serves "Complete 4CAT Setup"
	refuse        bool // /login/ answers 401
	hijackLogin   bool // /login/ drops the connection (transport failure)
	accountExists bool // the users table already has the account
	logins        int  // /login/ calls seen
	sqls          []string
}

// fourcatDocker wraps the Fake so psql answers come from the env's scenario
// (account exists or not, UPDATE 1) instead of one shared ExecResults key.
type fourcatDocker struct {
	*docker.Fake
	env *testEnv
}

func (d *fourcatDocker) Exec(ctx context.Context, container string, cmd []string, env []string, stdin []byte) (docker.Exec, error) {
	if len(cmd) > 0 && cmd[0] == "psql" {
		sql := cmd[len(cmd)-1]
		d.env.mu.Lock()
		d.env.sqls = append(d.env.sqls, sql)
		exists := d.env.accountExists
		d.env.mu.Unlock()
		switch {
		case strings.HasPrefix(sql, "SELECT 1 FROM users"):
			if exists {
				return docker.Exec{ExitCode: 0, Stdout: "1\n"}, nil
			}
			return docker.Exec{ExitCode: 0, Stdout: ""}, nil
		case strings.HasPrefix(sql, "UPDATE users SET tags"):
			return docker.Exec{ExitCode: 0, Stdout: "UPDATE 1\n"}, nil
		}
	}
	return d.Fake.Exec(ctx, container, cmd, env, stdin)
}

func writeFile(t *testing.T, path, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	root := t.TempDir()
	cfg := &config.Config{
		DeskRoot:           root,
		HostDeskRoot:       root,
		FQDN:               "desk.example.org",
		Platform:           "tiktok",
		HideMedia:          true,
		FourcatEnabled:     true,
		MaxSessions:        99,
		DeskproxyImage:     "deskproxy:1",
		FirefoxImage:       "firefox:1",
		ZeeschuimerVersion: "1.14.3",
	}
	// The extracted release template (deploy/lib.sh install_template) and
	// the platform's hide-media stylesheet.
	rel := filepath.Join(root, "templates", "release", generation)
	writeFile(t, filepath.Join(rel, "manifest.json"), `{"generation":"`+generation+`"}`)
	writeFile(t, filepath.Join(rel, "profile", "prefs.js"), "user_pref(\"app.update.auto\", false);\n")
	writeFile(t, filepath.Join(rel, "profile", "extensions", "zeeschuimer.xpi"), "xpi-bytes")
	writeFile(t, filepath.Join(root, "platform", "userContent.css"), "/* hide media */\n")

	reg := &registry.Registry{Path: filepath.Join(root, "registry.tsv"), MaxSessions: cfg.MaxSessions}

	fake := docker.NewFake()
	ctx := context.Background()
	for _, spec := range []docker.ContainerSpec{
		{Name: backendName, Image: "4cat:1", Labels: map[string]string{
			"com.docker.compose.project": "desk", "com.docker.compose.service": "4cat-backend"}},
		{Name: dbName, Image: "postgres:16", Labels: map[string]string{
			"com.docker.compose.project": "desk", "com.docker.compose.service": "4cat-db"}},
	} {
		if _, err := fake.ContainerCreate(ctx, spec); err != nil {
			t.Fatalf("ContainerCreate %s: %v", spec.Name, err)
		}
		if err := fake.ContainerStart(ctx, spec.Name); err != nil {
			t.Fatalf("ContainerStart %s: %v", spec.Name, err)
		}
	}

	env := &testEnv{t: t, cfg: cfg, root: root, reg: reg, fake: fake}
	srv := httptest.NewServer(http.HandlerFunc(env.serve4CAT))
	t.Cleanup(srv.Close)

	fc := &fourcat.Client{
		Docker:      &fourcatDocker{Fake: fake, env: env},
		Backend:     backendName,
		DB:          dbName,
		FrontendURL: srv.URL,
	}
	env.prov = &Provisioner{Cfg: cfg, Reg: reg, Fourcat: fc, Locks: &locks.Set{}}
	return env
}

// serve4CAT stands in for the 4CAT frontend: "/" is the setup page while
// pending, "/login/" answers per the env's mode.
func (e *testEnv) serve4CAT(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/":
		e.mu.Lock()
		pending := e.pending
		e.mu.Unlock()
		if pending {
			_, _ = w.Write([]byte("<html><head><title>Complete 4CAT Setup</title></head></html>"))
			return
		}
		_, _ = w.Write([]byte("<html><head><title>4CAT</title></head><body>dashboard</body></html>"))
	case "/login/":
		e.mu.Lock()
		e.logins++
		refuse, hijack := e.refuse, e.hijackLogin
		e.mu.Unlock()
		if hijack {
			if hj, ok := w.(http.Hijacker); ok {
				conn, _, err := hj.Hijack()
				if err == nil {
					_ = conn.Close()
					return
				}
			}
			http.Error(w, "cannot hijack", http.StatusInternalServerError)
			return
		}
		if refuse {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad form", http.StatusBadRequest)
			return
		}
		user := r.PostForm.Get("username")
		want := ""
		if data, err := os.ReadFile(PasswordPath(e.cfg, user)); err == nil {
			want = strings.TrimSpace(string(data))
		}
		if want != "" && r.PostForm.Get("password") == want {
			w.Header().Add("Set-Cookie", "remember_token=tok-"+user+"; Path=/")
			w.WriteHeader(http.StatusFound)
			return
		}
		w.WriteHeader(http.StatusUnauthorized)
	default:
		http.NotFound(w, r)
	}
}

// creates returns how many create_user.py execs the fake recorded.
func (e *testEnv) creates() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	n := 0
	for _, call := range e.fake.Execs {
		if len(call.Cmd) > 0 && call.Cmd[0] == "python3" {
			n++
		}
	}
	return n
}

func (e *testEnv) loginCount() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.logins
}

func (e *testEnv) sqlLog() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.sqls...)
}

func (e *testEnv) setFlags(pending, refuse, hijack, exists bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.pending, e.refuse, e.hijackLogin, e.accountExists = pending, refuse, hijack, exists
}

func statMode(t *testing.T, path string) os.FileMode {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	return fi.Mode().Perm()
}

func statUID(t *testing.T, path string) int {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	return int(fi.Sys().(*syscall.Stat_t).Uid)
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}
