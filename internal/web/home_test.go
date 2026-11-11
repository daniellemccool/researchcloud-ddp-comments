package web

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/daniellemccool/researchcloud-ddp-comments/internal/access"
	"github.com/daniellemccool/researchcloud-ddp-comments/internal/config"
	"github.com/daniellemccool/researchcloud-ddp-comments/internal/desklog"
	"github.com/daniellemccool/researchcloud-ddp-comments/internal/docker"
	"github.com/daniellemccool/researchcloud-ddp-comments/internal/fourcat"
	"github.com/daniellemccool/researchcloud-ddp-comments/internal/locks"
	"github.com/daniellemccool/researchcloud-ddp-comments/internal/provision"
	"github.com/daniellemccool/researchcloud-ddp-comments/internal/registry"
	"github.com/daniellemccool/researchcloud-ddp-comments/internal/session"
)

// blockingExec is a docker client whose Exec blocks until its context is
// done, standing in for a psql or create_user.py that hangs.
type blockingExec struct {
	*docker.Fake
	mu    sync.Mutex
	calls int
	errs  []error // ctx.Err() seen by each blocked Exec
}

func (b *blockingExec) Exec(ctx context.Context, _ string, _ []string, _ []string, _ []byte) (docker.Exec, error) {
	b.mu.Lock()
	b.calls++
	b.mu.Unlock()
	<-ctx.Done()
	b.mu.Lock()
	b.errs = append(b.errs, ctx.Err())
	b.mu.Unlock()
	return docker.Exec{}, ctx.Err()
}

func (b *blockingExec) seen() (int, []error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.calls, append([]error(nil), b.errs...)
}

// A hung exec in the background Advance is cut off by the kick's deadline:
// the step records its error the usual way, the single-flight entry is
// released and the next visit retries (controller ruling F89).
func TestKickAdvanceHasDeadline(t *testing.T) {
	root := t.TempDir()
	cfg := &config.Config{
		DeskRoot:           root,
		HostDeskRoot:       root,
		FQDN:               "desk.example.org",
		CollectorRole:      collectorRole,
		AdminRole:          adminRole,
		Contact:            testContact,
		Platform:           "test",
		FourcatEnabled:     true,
		MaxSessions:        9,
		ZeeschuimerVersion: "1.0",
	}
	// 4CAT's setup is done: /first-run/ redirects.
	frontend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/", http.StatusFound)
	}))
	t.Cleanup(frontend.Close)
	dock := &blockingExec{Fake: docker.NewFake()}
	prov := &provision.Provisioner{
		Cfg:   cfg,
		Reg:   &registry.Registry{Path: filepath.Join(root, "registry.tsv"), MaxSessions: cfg.MaxSessions},
		Locks: &locks.Set{},
		Fourcat: &fourcat.Client{
			Docker:      dock,
			Backend:     "desk-4cat-backend-1",
			DB:          "desk-4cat-db-1",
			FrontendURL: frontend.URL,
		},
	}
	for _, d := range []string{"secrets/4cat-passwords", "secrets/4cat-tokens"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	// Everything before the 4CAT account is done, so the kicked Advance goes
	// straight to the exec that hangs.
	writeProvisionState(t, root, "alice", map[string]string{"registry": "done", "dirs": "done", "template": "done"})

	srv := New(Deps{
		Cfg:      cfg,
		Access:   access.Config{CollectorRole: collectorRole, AdminRole: adminRole, GateSecret: testGate},
		Platform: testPlatform(),
		LogPath:  filepath.Join(root, "state", "access.ndjson"),
		Prov:     prov,
		Sessions: &fakeSessions{},
		Log:      &desklog.Log{Path: filepath.Join(root, "state", "desk-log.ndjson")},
	})
	srv.Handler.(*handler).advanceTimeout = 50 * time.Millisecond
	e := &collectorEnv{srv: srv, root: root, cfg: cfg}
	makeTemplateDir(t, e)

	if rec := e.get(t, "alice", collectorRole, "/desk/"); rec.Code != http.StatusOK {
		t.Fatalf("first visit: status = %d, want 200", rec.Code)
	}
	deadline := time.Now().Add(5 * time.Second)
	var got string
	for time.Now().Before(deadline) {
		got = provision.Load(cfg, "alice").Steps["fourcat_account"]
		if strings.HasPrefix(got, "error: ") {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !strings.HasPrefix(got, "error: ") {
		t.Fatalf("fourcat_account = %q after 5 s; want a recorded error (the hung exec was never cut off)", got)
	}
	_, errs := dock.seen()
	if len(errs) != 1 || !errors.Is(errs[0], context.DeadlineExceeded) {
		t.Errorf("blocked exec saw %v; want one context.DeadlineExceeded", errs)
	}

	// The single-flight entry is released, so the next visit kicks a retry.
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, busy := srv.Handler.(*handler).provisioning.Load("alice"); !busy {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	e.get(t, "alice", collectorRole, "/desk/")
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		n, _ := dock.seen()
		_, busy := srv.Handler.(*handler).provisioning.Load("alice")
		if n >= 2 && !busy {
			return // the retry ran and finished before TempDir cleanup
		}
		time.Sleep(10 * time.Millisecond)
	}
	n, _ := dock.seen()
	t.Errorf("exec calls after the second visit = %d, want 2 (the retry was never kicked or never ended)", n)
}

// fakeMembers is the CO poll view: absent users and a fixed status line.
type fakeMembers struct {
	absent map[string]bool
	line   string
}

func (f fakeMembers) Absent(user string) bool { return f.absent[user] }
func (f fakeMembers) StatusLine() string      { return f.line }

func accessRemovedText() string {
	return "Your access to the desk was removed. If you were just re-added, try again in a minute; otherwise contact " + testContact + "."
}

// A revoked collector the gate still admits is not reactivated while the
// last good CO list lacks them: the page says so plainly, with no kick and
// no reload.
func TestAbsentUserNotReactivated(t *testing.T) {
	e := newCollectorEnv(t, false)
	e.srv.Handler.(*handler).members = fakeMembers{absent: map[string]bool{"alice": true}}
	makeTemplateDir(t, e)
	steps := allDoneSteps()
	delete(steps, "registry") // what a revoke leaves
	writeProvisionState(t, e.root, "alice", steps)
	rec := e.get(t, "alice", collectorRole, "/desk/")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	b := rec.Body.String()
	if !strings.Contains(b, accessRemovedText()) {
		t.Errorf("body %q lacks %q", b, accessRemovedText())
	}
	if strings.Contains(b, "refresh") {
		t.Errorf("access-removed body %q carries a refresh meta", b)
	}
	time.Sleep(50 * time.Millisecond)
	if n := e.prov.callCount(); n != 0 {
		t.Errorf("Advance called %d times for an absent user, want 0", n)
	}
}

// A retired collector the list holds again is reactivated: the visit kicks
// provisioning, whose registry step reactivates the row.
func TestPresentRetiredUserReactivates(t *testing.T) {
	e := newCollectorEnv(t, false)
	e.srv.Handler.(*handler).members = fakeMembers{absent: map[string]bool{"bob": true}}
	makeTemplateDir(t, e)
	steps := allDoneSteps()
	delete(steps, "registry")
	writeProvisionState(t, e.root, "alice", steps)
	rec := e.get(t, "alice", collectorRole, "/desk/")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if b := rec.Body.String(); !strings.Contains(b, "Setting up your desk — registry") {
		t.Errorf("body %q lacks the setting-up text", b)
	}
	deadline := time.Now().Add(2 * time.Second)
	for e.prov.callCount() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if n := e.prov.callCount(); n != 1 {
		t.Errorf("Advance called %d times, want 1 (the kick)", n)
	}
}

// Reactivation that finds every seat taken gets the existing full page:
// Allocate's ErrFull is recorded as RegistryFull and the next visit says so.
func TestReactivateWhenFullShowsFull(t *testing.T) {
	e := newCollectorEnv(t, false)
	makeTemplateDir(t, e)
	reg := &registry.Registry{Path: filepath.Join(e.root, "registry.tsv"), MaxSessions: 1}
	if err := os.WriteFile(reg.Path, []byte("alice\t1\tretired\nbob\t1\tlive\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	members := fakeMembers{absent: map[string]bool{}}
	h := e.srv.Handler.(*handler)
	h.members = members
	h.prov = &provision.Provisioner{Cfg: e.cfg, Reg: reg, Locks: &locks.Set{}, Members: members}
	steps := allDoneSteps()
	delete(steps, "registry")
	writeProvisionState(t, e.root, "alice", steps)

	e.get(t, "alice", collectorRole, "/desk/") // kicks the registry step
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		_, busy := h.provisioning.Load("alice")
		if !busy && provision.Load(e.cfg, "alice").Steps["registry"] == provision.RegistryFull {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got := provision.Load(e.cfg, "alice").Steps["registry"]; got != provision.RegistryFull {
		t.Fatalf("registry step = %q, want %q", got, provision.RegistryFull)
	}
	rec := e.get(t, "alice", collectorRole, "/desk/")
	want := "The desk is full; ask " + testContact + "."
	if b := rec.Body.String(); !strings.Contains(b, want) {
		t.Errorf("body %q lacks %q", b, want)
	}
}

// serveHome restarts the idle clocks BEFORE Start as well as after it, so a
// reaper pass queued on the user lock cannot stop the pair the page is
// about to find (F105).
func TestHomeForgetsBeforeStart(t *testing.T) {
	e, act, clock := newActiveEnv(t)
	e.sess.startResult = session.Started
	writeProvisionState(t, e.root, "alice", allDoneSteps())
	makeTemplateDir(t, e)
	const idle = 15 * time.Minute
	closed := act.openScreen("alice", func() {})
	closed()
	*clock = clock.Add(idle + time.Hour)
	var idleAtStart bool
	e.sess.onStart = func() { idleAtStart, _ = act.Idle("alice", idle) }
	if rec := e.get(t, "alice", collectorRole, "/desk/"); rec.Code != http.StatusOK {
		t.Fatalf("home = %d", rec.Code)
	}
	if e.sess.startCount() != 1 {
		t.Fatalf("Start called %d times, want 1", e.sess.startCount())
	}
	if idleAtStart {
		t.Error("alice still idle when Start ran; serveHome must Forget before Start")
	}
}

// The desk page carries desk_contact into the denied banner.
func TestHomePassesContactToPage(t *testing.T) {
	e := readyPageEnv(t, false)
	rec := e.get(t, "alice", collectorRole, "/desk/")
	if rec.Code != http.StatusOK {
		t.Fatalf("home = %d, want 200", rec.Code)
	}
	want := "If it keeps failing, contact " + testContact + "."
	if b := rec.Body.String(); !strings.Contains(b, want) {
		t.Errorf("page lacks %q", want)
	}
}
