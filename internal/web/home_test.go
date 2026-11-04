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
