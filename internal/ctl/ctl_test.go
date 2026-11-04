package ctl

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// fakeHandler records the calls and returns canned results.
type fakeHandler struct {
	statusText string
	statusErr  error
	renderErr  error
	stopErr    error
	stopped    []string
}

func (f *fakeHandler) Status(context.Context) (string, error) { return f.statusText, f.statusErr }
func (f *fakeHandler) Render(context.Context) error           { return f.renderErr }
func (f *fakeHandler) Stop(_ context.Context, user string) error {
	f.stopped = append(f.stopped, user)
	return f.stopErr
}

// startServe starts ServeReady in a goroutine with an error channel and waits
// until EITHER ServeReady signals readiness OR Serve returns an error — never
// a bare poll that hides the reason. A permission error on the socket call (a
// sandbox forbidding AF_UNIX) skips the test; any other error fails it. The
// returned path is the socket, verified to be a 0600 socket. The registered
// cleanup cancels the context and asserts Serve then returns nil within 5 s.
func startServe(t *testing.T, dir string, h Handler) string {
	t.Helper()
	path := filepath.Join(dir, "desk.sock")
	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	ready := make(chan struct{}, 1)
	go func() { errCh <- ServeReady(ctx, path, h, ready) }()

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
		t.Fatalf("Serve: %v", err)
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

func TestCtlRoundTrip(t *testing.T) {
	dir := t.TempDir()
	h := &fakeHandler{statusText: "alice\t1\trunning\tready"}
	path := startServe(t, dir, h)

	resp, err := Call(path, Request{Op: "status"})
	if err != nil {
		t.Fatalf("Call(status): %v", err)
	}
	if !resp.OK || resp.Text != "alice\t1\trunning\tready" || resp.Error != "" {
		t.Errorf("status response = %+v, want OK with text %q", resp, "alice\t1\trunning\tready")
	}

	resp, err = Call(path, Request{Op: "render"})
	if err != nil {
		t.Fatalf("Call(render): %v", err)
	}
	if !resp.OK || resp.Error != "" {
		t.Errorf("render response = %+v, want OK", resp)
	}

	resp, err = Call(path, Request{Op: "stop", User: "alice"})
	if err != nil {
		t.Fatalf("Call(stop): %v", err)
	}
	if !resp.OK || resp.Error != "" {
		t.Errorf("stop response = %+v, want OK", resp)
	}
	if len(h.stopped) != 1 || h.stopped[0] != "alice" {
		t.Errorf("handler stopped %v, want [alice]", h.stopped)
	}

	resp, err = Call(path, Request{Op: "bogus"})
	if err != nil {
		t.Fatalf("Call(bogus): %v", err)
	}
	if resp.OK || resp.Error != "unknown op bogus" {
		t.Errorf("unknown op response = %+v, want OK false with error %q", resp, "unknown op bogus")
	}

	// A handler error comes back as OK false with the error text.
	h.statusErr = errFake
	resp, err = Call(path, Request{Op: "status"})
	if err != nil {
		t.Fatalf("Call(status err): %v", err)
	}
	if resp.OK || resp.Error != "fake status failure" {
		t.Errorf("failing status response = %+v, want OK false with error %q", resp, "fake status failure")
	}
	// The clean stop on context cancel is asserted by startServe's cleanup.
}

var errFake = fakeError("fake status failure")

type fakeError string

func (e fakeError) Error() string { return string(e) }

func TestCtlStaleSocketReplaced(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "desk.sock")
	// A stale regular file sits where the socket must go.
	if err := os.WriteFile(path, []byte("stale"), 0o644); err != nil {
		t.Fatal(err)
	}
	h := &fakeHandler{statusText: "ok"}
	path = startServe(t, dir, h)

	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode()&os.ModeSocket == 0 {
		t.Fatalf("%s is not a socket after Serve (mode %v)", path, fi.Mode())
	}
	resp, err := Call(path, Request{Op: "status"})
	if err != nil {
		t.Fatalf("Call over replaced socket: %v", err)
	}
	if !resp.OK || resp.Text != "ok" {
		t.Errorf("response = %+v, want OK with text %q", resp, "ok")
	}
}

func TestCtlStopsOnContextCancel(t *testing.T) {
	dir := t.TempDir()
	path := startServe(t, dir, &fakeHandler{})

	// The server is up and answering; startServe's cleanup then cancels the
	// context and asserts Serve returns nil within 5 s.
	resp, err := Call(path, Request{Op: "status"})
	if err != nil {
		t.Fatalf("Call(status): %v", err)
	}
	if !resp.OK {
		t.Errorf("status response = %+v, want OK", resp)
	}
}

func TestServeFailsWhenPathUnusable(t *testing.T) {
	dir := t.TempDir()
	blocker := filepath.Join(dir, "blocker")
	if err := os.WriteFile(blocker, []byte("not a dir"), 0o644); err != nil {
		t.Fatal(err)
	}
	// The parent of the socket path is a regular file: MkdirAll fails and
	// Serve must return that error promptly (no socket is ever created, so
	// this test needs no AF_UNIX and never skips).
	path := filepath.Join(blocker, "desk.sock")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	err := Serve(ctx, path, &fakeHandler{})
	if err == nil {
		t.Fatal("Serve succeeded with a path whose parent is a regular file, want an error")
	}
}

func TestServeRefusesSecondServer(t *testing.T) {
	dir := t.TempDir()
	path := startServe(t, dir, &fakeHandler{})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	err := Serve(ctx, path, &fakeHandler{})
	if err == nil || !strings.Contains(err.Error(), "desk already serving at "+path) {
		t.Fatalf("second Serve error = %v, want it to refuse with %q", err, "desk already serving at "+path)
	}
}

func TestCtlRequestTooLong(t *testing.T) {
	dir := t.TempDir()
	path := startServe(t, dir, &fakeHandler{})

	conn, err := net.Dial("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	if _, err := conn.Write(bytes.Repeat([]byte("x"), maxLine+1)); err != nil {
		t.Fatal(err)
	}
	line, err := bufio.NewReader(conn).ReadBytes('\n')
	if err != nil {
		t.Fatal(err)
	}
	var resp Response
	if err := json.Unmarshal(line, &resp); err != nil {
		t.Fatal(err)
	}
	if resp.OK || resp.Error != "request too long" {
		t.Errorf("response = %+v, want OK false with error %q", resp, "request too long")
	}
}
