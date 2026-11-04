package ctl

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
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

// startServe starts Serve in a goroutine with an error channel and waits until
// EITHER the socket at <dir>/desk.sock exists with mode 0600 OR Serve returns
// an error — never a bare poll that hides the reason. A permission error on
// the socket call (the sandbox forbids AF_UNIX) skips the test; any other
// error fails it. The returned path is the socket. The registered cleanup
// cancels the context and asserts Serve then returns nil within 5 s.
func startServe(t *testing.T, dir string, h Handler) string {
	t.Helper()
	path := filepath.Join(dir, "desk.sock")
	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- Serve(ctx, path, h) }()

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

	deadline := time.Now().Add(5 * time.Second)
	for {
		select {
		case err := <-errCh:
			gotErr = true
			if errors.Is(err, syscall.EPERM) || errors.Is(err, fs.ErrPermission) {
				t.Skipf("unix sockets not permitted here: %v", err)
			}
			t.Fatalf("Serve: %v", err)
		default:
		}
		fi, statErr := os.Stat(path)
		if statErr == nil && fi.Mode()&os.ModeSocket != 0 && fi.Mode().Perm() == 0o600 {
			return path
		}
		if time.Now().After(deadline) {
			t.Fatalf("socket %s never appeared with mode 0600 (last stat: %v, %v)", path, fi, statErr)
		}
		time.Sleep(5 * time.Millisecond)
	}
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
