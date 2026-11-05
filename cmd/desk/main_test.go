package main

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/daniellemccool/researchcloud-ddp-comments/internal/config"
	"github.com/daniellemccool/researchcloud-ddp-comments/internal/ctl"
	"github.com/daniellemccool/researchcloud-ddp-comments/internal/docker"
	"github.com/daniellemccool/researchcloud-ddp-comments/internal/locks"
	"github.com/daniellemccool/researchcloud-ddp-comments/internal/provision"
	"github.com/daniellemccool/researchcloud-ddp-comments/internal/session"
)

func TestUsageExitCodes(t *testing.T) {
	for _, args := range [][]string{{"help"}, {"--help"}, {"-h"}} {
		var stdout, stderr bytes.Buffer
		code := dispatch(args, &stdout, &stderr)
		if code != 0 {
			t.Errorf("dispatch(%v) = %d, want 0", args, code)
		}
		if !strings.Contains(stdout.String(), "usage: desk") {
			t.Errorf("dispatch(%v) stdout = %q, want usage on stdout", args, stdout.String())
		}
		if stderr.Len() != 0 {
			t.Errorf("dispatch(%v) stderr = %q, want empty", args, stderr.String())
		}
	}

	// -h on the subcommands prints the usage on stdout and exits 0 too.
	for _, args := range [][]string{{"serve", "-h"}, {"status", "-h"}, {"stop", "-h"}, {"render", "--help"}} {
		var stdout, stderr bytes.Buffer
		code := dispatch(args, &stdout, &stderr)
		if code != 0 {
			t.Errorf("dispatch(%v) = %d, want 0", args, code)
		}
		if !strings.Contains(stdout.String(), "usage: desk") {
			t.Errorf("dispatch(%v) stdout = %q, want usage on stdout", args, stdout.String())
		}
		if stderr.Len() != 0 {
			t.Errorf("dispatch(%v) stderr = %q, want empty", args, stderr.String())
		}
	}

	var stdout, stderr bytes.Buffer
	if code := dispatch(nil, &stdout, &stderr); code != 2 {
		t.Errorf("dispatch(nil) = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "usage: desk") {
		t.Errorf("dispatch(nil) stderr = %q, want usage on stderr", stderr.String())
	}
	if stdout.Len() != 0 {
		t.Errorf("dispatch(nil) stdout = %q, want empty", stdout.String())
	}

	stdout.Reset()
	stderr.Reset()
	if code := dispatch([]string{"bogus"}, &stdout, &stderr); code != 2 {
		t.Errorf("dispatch([bogus]) = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "usage: desk") {
		t.Errorf("dispatch([bogus]) stderr = %q, want usage on stderr", stderr.String())
	}
	if stdout.Len() != 0 {
		t.Errorf("dispatch([bogus]) stdout = %q, want empty", stdout.String())
	}
}

func TestStatusFormat(t *testing.T) {
	done := provision.State{Steps: map[string]string{
		"registry": "done", "dirs": "done", "template": "done",
		"fourcat_account": "done", "fourcat_token": "done", "render": "done",
	}}
	rows := []session.Row{
		{User: "alice", Index: 1, Running: true, State: done},
		{User: "bob", Index: 2, Running: false, State: provision.State{Steps: map[string]string{
			"registry": "done", "dirs": "done", "template": "error: template missing",
		}}},
		{User: "carol", Index: 3, Running: false, State: done},
	}
	want := "alice\t1\trunning\tready\n" +
		"bob\t2\tstopped\tat template (template missing)\n" +
		"carol\t3\tstopped\tready"
	if got := statusText(rows); got != want {
		t.Errorf("statusText = %q, want %q", got, want)
	}
}

func TestStopValidatesUser(t *testing.T) {
	var stdout, stderr bytes.Buffer
	// No socket exists anywhere near the default desk root; the invalid
	// username must be rejected before any connection is attempted — in both
	// flag orders.
	for _, args := range [][]string{
		{"stop", "Bad Name"},
		{"stop", "Bad Name", "--desk-root", "/nonexistent"},
		{"stop", "--desk-root", "/nonexistent", "Bad Name"},
	} {
		stdout.Reset()
		stderr.Reset()
		code := dispatch(args, &stdout, &stderr)
		if code != 1 {
			t.Errorf("dispatch(%v) = %d, want 1", args, code)
		}
		if stdout.Len() != 0 {
			t.Errorf("dispatch(%v) stdout = %q, want empty", args, stdout.String())
		}
		if !strings.Contains(stderr.String(), "invalid username") {
			t.Errorf("dispatch(%v) stderr = %q, want an invalid username message", args, stderr.String())
		}
	}
}

// The serving desk validates the username itself: a socket client that skips
// the check never reaches the manager (review M5).
func TestOpsStopValidatesUser(t *testing.T) {
	fake := docker.NewFake()
	o := &ops{mgr: &session.Manager{
		Cfg:    &config.Config{DeskRoot: t.TempDir()},
		Docker: fake,
		Locks:  &locks.Set{},
	}}
	for _, user := range []string{"Bad Name", "../alice", "", "alice/../bob"} {
		err := o.Stop(context.Background(), user)
		if err == nil || !strings.Contains(err.Error(), "invalid username") {
			t.Errorf("Stop(%q) = %v, want an invalid username error", user, err)
		}
	}
	if len(fake.Calls) != 0 {
		t.Errorf("invalid names reached Docker: %v", fake.Calls)
	}
}

// The admin 4CAT proxy is mounted only when 4CAT is enabled; disabled, the
// handler is a nil interface so /fourcat/ answers 404 (review M6).
func TestFourcatProxyMountedOnlyWhenEnabled(t *testing.T) {
	if h := fourcatProxy(&config.Config{FQDN: "desk.example.org"}); h != nil {
		t.Errorf("fourcatProxy(disabled) = %#v, want nil", h)
	}
	if h := fourcatProxy(&config.Config{FQDN: "desk.example.org", FourcatEnabled: true}); h == nil {
		t.Error("fourcatProxy(enabled) = nil, want the proxy")
	}
}

// desk serve refuses a daemon below Docker Engine 28 / API 1.48, which
// cannot be trusted to honour ADR-0007's isolated gateway (ruling F91).
func TestCheckDockerVersion(t *testing.T) {
	cases := []struct {
		engine, api string
		ok          bool
	}{
		{"29.1.3", "1.52", true},
		{"28.0.0", "1.48", true},
		{"28.5.1+dfsg1", "1.51", true},
		{"27.5.1", "1.47", false},
		{"28.0.0", "1.47", false},
		{"27.5.1", "1.48", false},
		{"20.10.24", "1.41", false},
		{"1.13.1", "1.26", false},
		{"29.0.0", "1.9", false}, // the API's minor compares as a number, not as text
		{"", "", false},
		{"dev", "1.52", false},
		{"29.0.0", "bogus", false},
	}
	for _, c := range cases {
		fake := docker.NewFake()
		fake.Version = docker.Version{Engine: c.engine, API: c.api}
		err := checkDockerVersion(context.Background(), fake)
		if c.ok && err != nil {
			t.Errorf("engine %q api %q: %v, want accepted", c.engine, c.api, err)
		}
		if !c.ok {
			if err == nil {
				t.Errorf("engine %q api %q accepted, want refused", c.engine, c.api)
				continue
			}
			for _, want := range []string{"Docker Engine 28", "API 1.48", "ADR-0007", "isolated gateway"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("engine %q api %q: error %q does not name %q", c.engine, c.api, err, want)
				}
			}
		}
	}
	// A daemon that cannot answer is refused too.
	fake := docker.NewFake()
	fake.Version = docker.Version{Engine: "29.1.3", API: "1.52"}
	fake.Fail["ServerVersion"] = errors.New("daemon down")
	if err := checkDockerVersion(context.Background(), fake); err == nil || !strings.Contains(err.Error(), "daemon down") {
		t.Errorf("failing ServerVersion: %v, want an error carrying the cause", err)
	}
}

// fakeCtlHandler is the ctl.Handler of the socket tests.
type fakeCtlHandler struct {
	text    string
	stopped []string
}

func (f *fakeCtlHandler) Status(context.Context) (string, error) { return f.text, nil }
func (f *fakeCtlHandler) Render(context.Context) error           { return nil }
func (f *fakeCtlHandler) Stop(_ context.Context, user string) error {
	f.stopped = append(f.stopped, user)
	return nil
}

// startServe starts ctl.ServeReady in a goroutine with an error channel and
// waits until EITHER ServeReady signals readiness OR Serve returns an error —
// never a bare poll that hides the reason. A permission error on the socket
// call (a sandbox forbidding AF_UNIX) skips the test; any other error fails
// it. The registered cleanup cancels the context and asserts Serve then
// returns nil within 5 s.
func startServe(t *testing.T, dir string, h ctl.Handler) string {
	t.Helper()
	path := filepath.Join(dir, "state", "desk.sock")
	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	ready := make(chan struct{}, 1)
	go func() { errCh <- ctl.ServeReady(ctx, path, h, ready) }()

	gotErr := false
	t.Cleanup(func() {
		cancel()
		if gotErr {
			return // the error was already received (skip or fatal path)
		}
		select {
		case err := <-errCh:
			if err != nil {
				t.Errorf("Serve returned %v after cancel, want nil", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("Serve did not return within 5 s of context cancel")
		}
	})

	select {
	case err := <-errCh:
		gotErr = true
		if errors.Is(err, syscall.EPERM) || errors.Is(err, fs.ErrPermission) {
			t.Skipf("unix sockets not permitted here: %v", err)
		}
		t.Fatalf("ctl.Serve: %v", err)
	case <-ready:
	case <-time.After(5 * time.Second):
		t.Fatal("Serve neither signalled readiness nor returned within 5 s")
	}
	fi, statErr := os.Stat(path)
	if statErr != nil {
		t.Fatalf("stat %s after ready: %v", path, statErr)
	}
	if fi.Mode()&os.ModeSocket == 0 || fi.Mode().Perm() != 0o600 {
		t.Fatalf("socket %s after ready has mode %v, want a socket with mode 0600", path, fi.Mode())
	}
	return path
}

func TestStatusAgainstSocket(t *testing.T) {
	dir := t.TempDir()
	h := &fakeCtlHandler{text: "alice\t1\trunning\tready"}
	startServe(t, dir, h)

	var stdout, stderr bytes.Buffer
	if code := dispatch([]string{"status", "--desk-root", dir}, &stdout, &stderr); code != 0 {
		t.Fatalf("dispatch([status --desk-root %s]) = %d (stderr %q), want 0", dir, code, stderr.String())
	}
	if stdout.String() != "alice\t1\trunning\tready\n" {
		t.Errorf("stdout = %q, want the handler's text plus newline", stdout.String())
	}

	// Without the socket the call fails: exit 1, error on stderr.
	stdout.Reset()
	stderr.Reset()
	if code := dispatch([]string{"status", "--desk-root", t.TempDir()}, &stdout, &stderr); code != 1 {
		t.Errorf("dispatch([status --desk-root <empty>]) = %d, want 1", code)
	}
	if stdout.Len() != 0 {
		t.Errorf("stdout = %q, want empty", stdout.String())
	}
	if stderr.Len() == 0 {
		t.Error("stderr empty, want the socket error")
	}
}

func TestStopBothFlagOrders(t *testing.T) {
	dir := t.TempDir()
	h := &fakeCtlHandler{}
	startServe(t, dir, h)

	for _, args := range [][]string{
		{"stop", "alice", "--desk-root", dir},
		{"stop", "--desk-root", dir, "alice"},
		{"stop", "--desk-root=" + dir, "alice"},
	} {
		var stdout, stderr bytes.Buffer
		if code := dispatch(args, &stdout, &stderr); code != 0 {
			t.Errorf("dispatch(%v) = %d (stderr %q), want 0", args, code, stderr.String())
		}
		if stdout.String() != "stopped alice\n" {
			t.Errorf("dispatch(%v) stdout = %q, want %q", args, stdout.String(), "stopped alice\n")
		}
	}
	if len(h.stopped) != 3 {
		t.Errorf("handler stopped %v, want three stops of alice", h.stopped)
	}
	for i, u := range h.stopped {
		if u != "alice" {
			t.Errorf("h.stopped[%d] = %q, want alice", i, u)
		}
	}
}

func TestOpsSeatsLine(t *testing.T) {
	o := &ops{cfg: &config.Config{MaxRunning: 3}, configuredMax: 6, memorySeats: 3}
	if got, want := o.seatsLine(), "seats: 3 running at most (configured 6; memory allows 3)"; got != want {
		t.Errorf("seatsLine = %q, want %q", got, want)
	}
	o.memorySeats = 0
	if got, want := o.seatsLine(), "seats: 3 running at most (configured 6; memory allows unknown)"; got != want {
		t.Errorf("seatsLine = %q, want %q", got, want)
	}
}
