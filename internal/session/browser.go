package session

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
)

// browserSettle is how long the collector's own Firefox must have run before
// the screen counts as ready and a link may be opened. An Open before that
// Firefox exists starts a second, unsupervised one that takes the profile
// over, and the supervisor's Firefox then hands over and exits, forever.
const browserSettle = 5 * time.Second

// browserCheckTimeout bounds the browser check's exec, so a hung Docker call
// cannot hold a check-in.
const browserCheckTimeout = 5 * time.Second

// supervisedFirefox is the first word of the collector's own Firefox in ps:
// the image's supervisor runs /usr/bin/firefox, Alpine's wrapper that execs
// this binary.
const supervisedFirefox = "/usr/lib/firefox/firefox"

// psArgv lists the firefox container's processes with their elapsed time
// (busybox ps). It runs as the container's own user, not through su-exec.
var psArgv = []string{"ps", "-o", "etime,args"}

// ErrBrowserStarting is OpenURL's answer when the collector's own Firefox is
// not up yet (or the check could not be made): nothing was run.
var ErrBrowserStarting = errors.New("the browser is still starting")

// browserUp reports whether the collector's own Firefox is up in user's
// firefox container: a process whose args start with /usr/lib/firefox/firefox,
// carry --setDefaultBrowser (passed only by the image's supervisor, never by
// OpenURL) and are not a content process, running for at least browserSettle.
// err is a check that could not be made: a transport error or a non-zero exit
// of ps.
func (m *Manager) browserUp(ctx context.Context, user string) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, browserCheckTimeout)
	defer cancel()
	ex, err := m.Docker.Exec(ctx, "desk-firefox-"+user, psArgv, nil, nil)
	if err != nil {
		return false, err
	}
	if ex.ExitCode != 0 {
		return false, fmt.Errorf("ps exited %d: %s", ex.ExitCode, stderrTail(ex.Stderr, 200))
	}
	return supervisedFirefoxUp(ex.Stdout), nil
}

// supervisedFirefoxUp reads `ps -o etime,args` output; the header line and
// any line whose elapsed time cannot be read do not count.
func supervisedFirefoxUp(ps string) bool {
	for _, line := range strings.Split(ps, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		elapsed, ok := parseEtime(fields[0])
		if !ok || elapsed < browserSettle {
			continue
		}
		args := fields[1:]
		if args[0] == supervisedFirefox &&
			slices.Contains(args, "--setDefaultBrowser") &&
			!slices.Contains(args, "-contentproc") {
			return true
		}
	}
	return false
}

// parseEtime reads ps's elapsed time. m:ss or mm:ss (busybox prints minutes
// only, so the minute count may run past 59) is read exactly; any other
// readable form (hours or days: an h, a d, a '-' or h:mm:ss) counts as at
// least browserSettle. Anything else cannot be read: ok is false.
func parseEtime(s string) (elapsed time.Duration, ok bool) {
	if s == "" || s[0] < '0' || s[0] > '9' {
		return 0, false
	}
	if strings.ContainsAny(s, "hd-") || strings.Count(s, ":") >= 2 {
		for _, r := range s {
			if !strings.ContainsRune("0123456789:hd-", r) {
				return 0, false
			}
		}
		return browserSettle, true
	}
	mm, ss, found := strings.Cut(s, ":")
	if !found || len(ss) != 2 {
		return 0, false
	}
	minutes, err := strconv.Atoi(mm)
	if err != nil {
		return 0, false
	}
	seconds, err := strconv.Atoi(ss)
	if err != nil || ss[0] < '0' || ss[0] > '5' {
		return 0, false
	}
	return time.Duration(minutes)*time.Minute + time.Duration(seconds)*time.Second, true
}
