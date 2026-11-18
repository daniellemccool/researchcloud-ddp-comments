package session

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/daniellemccool/researchcloud-ddp-comments/internal/docker"
)

// supervisedFF is the collector's own Firefox as busybox ps shows it: the
// image's supervisor starts it through /usr/bin/firefox, Alpine's wrapper
// that execs /usr/lib/firefox/firefox, with the params' --setDefaultBrowser.
const supervisedFF = "/usr/lib/firefox/firefox --profile /config/profile --setDefaultBrowser"

// psOut is a busybox `ps -o etime,args` answer with its header line.
func psOut(lines ...string) docker.Exec {
	return docker.Exec{Stdout: "ELAPSED COMMAND\n" + strings.Join(lines, "\n") + "\n"}
}

// readyPair runs both of user's containers with a healthy noVNC probe.
func (f *fixture) readyPair(t *testing.T, user string) {
	t.Helper()
	f.startFirefox(t, user)
	f.startBare(t, "desk-proxy-"+user, "proxy", user)
}

// TestReadyWaitsForTheCollectorsFirefox: the screen is ready only once the
// supervised Firefox (--setDefaultBrowser, not a content process) has run
// for browserSettle; hours or days count as settled, an unreadable elapsed
// time does not count.
func TestReadyWaitsForTheCollectorsFirefox(t *testing.T) {
	cases := []struct {
		name string
		ps   docker.Exec
		want bool
	}{
		{"up for 5 s", psOut("    0:05 " + supervisedFF), true},
		{"up for minutes, among other processes", psOut(
			"    8:47 /package/admin/s6/command/s6-svscan -d4 -- /run/service",
			"    8:40 "+supervisedFF,
			"    8:39 /usr/lib/firefox/firefox -contentproc -isForBrowser -prefsLen 1 tab",
		), true},
		{"busybox pads a long minute count", psOut("  125:03 " + supervisedFF), true},
		{"up for 3 s", psOut("    0:03 " + supervisedFF), false},
		{"only content processes", psOut(
			"    2:00 /usr/lib/firefox/firefox -contentproc -isForBrowser tab",
			"    2:00 /usr/lib/firefox/firefox -contentproc --setDefaultBrowser tab",
		), false},
		{"only the open command's parent-0 Firefox", psOut(
			"    8:47 /usr/lib/firefox/firefox --profile /config/profile https://www.tiktok.com/@x/video/1",
		), false},
		{"the supervisor's version check", psOut("    0:09 /usr/bin/firefox --version"), false},
		{"hours as 1h05", psOut("    1h05 " + supervisedFF), true},
		{"hours as h:mm:ss", psOut(" 1:02:03 " + supervisedFF), true},
		{"days", psOut("2-03:04:05 " + supervisedFF), true},
		{"unparseable etime", psOut("       ? " + supervisedFF), false},
		{"unparseable etime with a colon", psOut("    x:yz " + supervisedFF), false},
		{"no Firefox at all", psOut("    8:47 /package/admin/s6/command/s6-svscan"), false},
		{"empty answer", docker.Exec{}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t, 4, "alice")
			f.readyPair(t, "alice")
			f.fake.ExecResults["ps -o"] = c.ps
			if got := f.m.Ready(context.Background(), "alice"); got != c.want {
				t.Errorf("Ready = %v, want %v for ps output %q", got, c.want, c.ps.Stdout)
			}
		})
	}
}

// TestReadyBrowserCheckCommand: the check is one exec of ps in the user's
// firefox container, as the container's own user (no su-exec), no env.
func TestReadyBrowserCheckCommand(t *testing.T) {
	f := newFixture(t, 4, "alice")
	f.readyPair(t, "alice")
	f.fake.ExecResults["ps -o"] = psOut("    0:30 " + supervisedFF)
	if !f.m.Ready(context.Background(), "alice") {
		t.Fatal("Ready = false with the browser up for 30 s, want true")
	}
	if len(f.fake.Execs) != 1 {
		t.Fatalf("Execs = %+v, want exactly the one ps", f.fake.Execs)
	}
	ex := f.fake.Execs[0]
	if ex.Container != "desk-firefox-alice" || !reflect.DeepEqual(ex.Cmd, []string{"ps", "-o", "etime,args"}) || len(ex.Env) != 0 {
		t.Errorf("Exec = %+v, want ps -o etime,args in desk-firefox-alice with no env", ex)
	}
}

// TestReadyFollowsTheBrowserAcrossCalls: a page polling status sees the
// screen once the browser has settled.
func TestReadyFollowsTheBrowserAcrossCalls(t *testing.T) {
	f := newFixture(t, 4, "alice")
	f.readyPair(t, "alice")
	f.fake.ExecScript["ps -o"] = []docker.ExecStep{
		{Exec: psOut()},
		{Exec: psOut("    0:02 " + supervisedFF)},
		{Exec: psOut("    0:07 " + supervisedFF)},
	}
	ctx := context.Background()
	for i, want := range []bool{false, false, true} {
		if got := f.m.Ready(ctx, "alice"); got != want {
			t.Errorf("Ready call %d = %v, want %v", i+1, got, want)
		}
	}
}

// TestReadyFallsBackWhenTheCheckFails: a failed ps exec (transport error or
// a non-zero exit) leaves Ready at today's answer, noVNC alone, and logs one
// line, so a broken exec never hides the screen for good.
func TestReadyFallsBackWhenTheCheckFails(t *testing.T) {
	for _, c := range []struct {
		name  string
		setup func(f *fixture)
	}{
		{"transport error", func(f *fixture) { f.fake.Fail["Exec desk-firefox-alice"] = errors.New("transport boom") }},
		{"ps exits non-zero", func(f *fixture) { f.fake.ExecResults["ps -o"] = docker.Exec{ExitCode: 1, Stderr: "ps: not found"} }},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t, 4, "alice")
			f.readyPair(t, "alice")
			c.setup(f)
			ctx := context.Background()
			if !f.m.Ready(ctx, "alice") {
				t.Error("Ready = false with noVNC answering and the check failing, want true (noVNC alone)")
			}
			if n := strings.Count(f.logBuf.String(), "\n"); n != 1 {
				t.Errorf("log has %d lines, want 1: %q", n, f.logBuf.String())
			}
			f.m.Probe = func(context.Context, string) bool { return false }
			if f.m.Ready(ctx, "alice") {
				t.Error("Ready = true with noVNC not answering, want false")
			}
		})
	}
}

// TestOpenURLWaitsForTheBrowser: with the collector's Firefox not up, or the
// check failing, OpenURL runs nothing and answers ErrBrowserStarting.
func TestOpenURLWaitsForTheBrowser(t *testing.T) {
	for _, c := range []struct {
		name  string
		setup func(f *fixture)
	}{
		{"not started", func(f *fixture) { f.fake.ExecResults["ps -o"] = psOut() }},
		{"up for 3 s", func(f *fixture) { f.fake.ExecResults["ps -o"] = psOut("    0:03 " + supervisedFF) }},
		{"only the open command's Firefox", func(f *fixture) {
			f.fake.ExecResults["ps -o"] = psOut("    8:47 /usr/lib/firefox/firefox --profile /config/profile https://example.org/")
		}},
		{"check fails", func(f *fixture) { f.fake.ExecScript["ps -o"] = []docker.ExecStep{{Err: errors.New("transport boom")}} }},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t, 4, "alice")
			f.startFirefox(t, "alice")
			c.setup(f)
			rc, stderr, err := f.m.OpenURL(context.Background(), "alice", "https://example.org/watch?v=1")
			if !errors.Is(err, ErrBrowserStarting) || rc != -1 || stderr != "" {
				t.Errorf("OpenURL = %d, %q, %v; want -1, \"\", ErrBrowserStarting", rc, stderr, err)
			}
			for _, ex := range f.fake.Execs {
				if len(ex.Cmd) > 0 && ex.Cmd[0] == "su-exec" {
					t.Errorf("the open command ran: %v", ex.Cmd)
				}
			}
		})
	}
}
