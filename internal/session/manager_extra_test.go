package session

import (
	"context"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/daniellemccool/researchcloud-ddp-comments/internal/docker"
)

func TestReadyNeedsRunningAndProbe(t *testing.T) {
	f := newFixture(t, 4, "alice")
	ctx := context.Background()
	var urls []string
	f.m.Probe = func(_ context.Context, url string) bool {
		urls = append(urls, url)
		return true
	}
	if f.m.Ready(ctx, "alice") {
		t.Error("Ready = true with nothing running, want false")
	}
	f.startFirefox(t, "alice")
	if !f.m.Ready(ctx, "alice") {
		t.Error("Ready = false with a running firefox and a healthy probe, want true")
	}
	if len(urls) == 0 || urls[len(urls)-1] != "http://desk-firefox-alice:5800/" {
		t.Errorf("probe urls = %v, want the last to be http://desk-firefox-alice:5800/", urls)
	}
	f.m.Probe = func(context.Context, string) bool { return false }
	if f.m.Ready(ctx, "alice") {
		t.Error("Ready = true with a failing probe, want false")
	}
}

func TestOpenURLCommand(t *testing.T) {
	f := newFixture(t, 4, "alice")
	ctx := context.Background()
	f.startFirefox(t, "alice")
	f.fake.ExecResults["su-exec 1000:1000"] = docker.Exec{ExitCode: 7, Stderr: strings.Repeat("x", 300)}

	rc, stderr, err := f.m.OpenURL(ctx, "alice", "https://example.org/watch?v=1")
	if err != nil || rc != 7 {
		t.Fatalf("OpenURL = %d, %q, %v; want 7, <200 x's>, nil", rc, stderr, err)
	}
	if stderr != strings.Repeat("x", 200) {
		t.Errorf("stderr tail = %d bytes, want the last 200", len(stderr))
	}
	if len(f.fake.Execs) != 1 {
		t.Fatalf("Execs = %+v, want exactly one entry", f.fake.Execs)
	}
	ex := f.fake.Execs[0]
	if ex.Container != "desk-firefox-alice" {
		t.Errorf("Exec container = %q, want desk-firefox-alice", ex.Container)
	}
	wantCmd := []string{"su-exec", "1000:1000", "/usr/lib/firefox/firefox", "--profile", "/config/profile", "https://example.org/watch?v=1"}
	if !reflect.DeepEqual(ex.Cmd, wantCmd) {
		t.Errorf("Exec cmd = %v, want %v", ex.Cmd, wantCmd)
	}
	wantEnv := []string{
		"DISPLAY=:0",
		"DBUS_SESSION_BUS_ADDRESS=unix:path=/tmp/dbus.base",
		"HOME=/config",
		"XDG_RUNTIME_DIR=/tmp/run/user/app",
	}
	if !reflect.DeepEqual(ex.Env, wantEnv) {
		t.Errorf("Exec env = %v, want %v", ex.Env, wantEnv)
	}

	// A transport error maps to (-1, "", err).
	f.fake.Fail["Exec desk-firefox-alice"] = errors.New("transport boom")
	rc, stderr, err = f.m.OpenURL(ctx, "alice", "https://example.org/")
	if rc != -1 || stderr != "" || err == nil {
		t.Errorf("transport error = %d, %q, %v; want -1, \"\", err", rc, stderr, err)
	}
}

func TestStatusRows(t *testing.T) {
	f := newFixture(t, 4, "alice")
	// Add a retired row for bob (no provision state, not running).
	if err := os.WriteFile(f.reg.Path, []byte("alice\t1\tlive\nbob\t2\tretired\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f.startFirefox(t, "alice")
	rows, err := f.m.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("Status rows = %+v, want 2 rows", rows)
	}
	byUser := map[string]Row{}
	for _, r := range rows {
		byUser[r.User] = r
	}
	a, ok := byUser["alice"]
	if !ok || a.Index != 1 || !a.Running || !a.State.Ready() {
		t.Errorf("alice row = %+v, want index 1, running, ready", a)
	}
	b, ok := byUser["bob"]
	if !ok || b.Index != 2 || b.Running || b.State.Ready() {
		t.Errorf("bob row = %+v, want index 2, not running, not ready", b)
	}
}
