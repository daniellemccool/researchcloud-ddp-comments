package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/daniellemccool/researchcloud-ddp-comments/internal/config"
	"github.com/daniellemccool/researchcloud-ddp-comments/internal/ctl"
	"github.com/daniellemccool/researchcloud-ddp-comments/internal/render"
	"github.com/daniellemccool/researchcloud-ddp-comments/internal/session"
)

// userRe is the username grammar (^[a-z][a-z0-9._-]{0,31}$), the same shape
// internal/registry and internal/provision enforce; a name is validated
// before it is sent over the socket.
var userRe = regexp.MustCompile(`^[a-z][a-z0-9._-]{0,31}$`)

// sockPath returns the operator socket of the desk rooted at deskRoot.
func sockPath(deskRoot string) string {
	return filepath.Join(deskRoot, "state", "desk.sock")
}

// call sends one request to the running desk and maps the outcome to the
// command's exit code: a transport failure or OK false is the error text on
// stderr with exit 1.
func call(deskRoot string, req ctl.Request, stderr io.Writer) (ctl.Response, int) {
	resp, err := ctl.Call(sockPath(deskRoot), req)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "desk: %v\n", err)
		return resp, 1
	}
	if !resp.OK {
		_, _ = fmt.Fprintf(stderr, "desk: %s\n", resp.Error)
		return resp, 1
	}
	return resp, 0
}

// printText writes text to w, adding a trailing newline when it lacks one.
func printText(w io.Writer, text string) {
	if text == "" {
		return
	}
	_, _ = fmt.Fprint(w, text)
	if !strings.HasSuffix(text, "\n") {
		_, _ = fmt.Fprintln(w)
	}
}

// deskRootFlagSet returns a flag set with the shared --desk-root flag
// (default /desk), its output pointed at stderr and its own usage dump
// suppressed (the commands print the shared usage themselves).
func deskRootFlagSet(name string, stderr io.Writer) (*flag.FlagSet, *string) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {}
	deskRoot := fs.String("desk-root", "/desk", "the desk's directory holding state/desk.sock")
	return fs, deskRoot
}

// parseFlags parses args with fs: -h/--help prints the usage on stdout and
// exits 0, any other parse error prints it on stderr with exit 2.
func parseFlags(fs *flag.FlagSet, args []string, stdout, stderr io.Writer) int {
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			usage(stdout)
			return 0
		}
		usage(stderr)
		return 2
	}
	return -1 // parsed fine
}

// statusCmd implements "desk status [--desk-root /desk]": it asks the running
// desk for its status text and prints it.
func statusCmd(args []string, stdout, stderr io.Writer) int {
	fs, deskRoot := deskRootFlagSet("status", stderr)
	if code := parseFlags(fs, args, stdout, stderr); code >= 0 {
		return code
	}
	if fs.NArg() != 0 {
		usage(stderr)
		return 2
	}
	resp, code := call(*deskRoot, ctl.Request{Op: "status"}, stderr)
	if code != 0 {
		return code
	}
	printText(stdout, resp.Text)
	return 0
}

// renderCmd implements "desk render [--desk-root /desk]": it asks the running
// desk to regenerate its files.
func renderCmd(args []string, stdout, stderr io.Writer) int {
	fs, deskRoot := deskRootFlagSet("render", stderr)
	if code := parseFlags(fs, args, stdout, stderr); code >= 0 {
		return code
	}
	if fs.NArg() != 0 {
		usage(stderr)
		return 2
	}
	if _, code := call(*deskRoot, ctl.Request{Op: "render"}, stderr); code != 0 {
		return code
	}
	_, _ = fmt.Fprintln(stdout, "rendered")
	return 0
}

// stopCmd implements "desk stop [--desk-root /desk] <user>". The --desk-root
// flag is scanned out of args before anything else, so both
// "desk stop <user> --desk-root /x" and "desk stop --desk-root /x <user>"
// work (the flag package stops at the first positional). The user is
// validated against the username grammar before anything is sent.
func stopCmd(args []string, stdout, stderr io.Writer) int {
	deskRoot := "/desk"
	var positional []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "-h" || a == "--help":
			usage(stdout)
			return 0
		case a == "--desk-root" || a == "-desk-root":
			if i+1 >= len(args) {
				usage(stderr)
				return 2
			}
			i++
			deskRoot = args[i]
		case strings.HasPrefix(a, "--desk-root="):
			deskRoot = strings.TrimPrefix(a, "--desk-root=")
		case strings.HasPrefix(a, "-desk-root="):
			deskRoot = strings.TrimPrefix(a, "-desk-root=")
		default:
			positional = append(positional, a)
		}
	}
	if len(positional) != 1 {
		usage(stderr)
		return 2
	}
	user := positional[0]
	if !userRe.MatchString(user) {
		_, _ = fmt.Fprintf(stderr, "desk: invalid username: %q\n", user)
		return 1
	}
	if _, code := call(deskRoot, ctl.Request{Op: "stop", User: user}, stderr); code != 0 {
		return code
	}
	_, _ = fmt.Fprintf(stdout, "stopped %s\n", user)
	return 0
}

// statusText renders one line per registry row (live and retired, in file
// order): <user>\t<index>\t<running|stopped>\t<ready|at <step>>, with
// " (<error text>)" appended after "at <step>" when the step holds an error
// (the step value without its "error: " prefix).
func statusText(rows []session.Row) string {
	lines := make([]string, 0, len(rows))
	for _, r := range rows {
		running := "stopped"
		if r.Running {
			running = "running"
		}
		state := "ready"
		if !r.State.Ready() {
			state = "at " + r.State.Current()
			if errText := r.State.Error(); errText != "" {
				state += " (" + strings.TrimPrefix(errText, "error: ") + ")"
			}
		}
		lines = append(lines, fmt.Sprintf("%s\t%d\t%s\t%s", r.User, r.Index, running, state))
	}
	return strings.Join(lines, "\n")
}

// ops is the ctl.Handler the serving desk exposes on state/desk.sock: every
// operator command runs inside the running desk's locks, through the session
// manager.
type ops struct {
	mgr           *session.Manager
	cfg           *config.Config
	platformDir   string // <repo>/platforms/<cfg.Platform>
	configuredMax int    // desk_max_running as configured, before the memory cap
	memorySeats   int    // pairs the box memory allows; 0 = unknown (meminfo unreadable)
}

// Status returns the one-line-per-row text of the manager's Status.
func (o *ops) Status(ctx context.Context) (string, error) {
	rows, err := o.mgr.Status(ctx)
	if err != nil {
		return "", err
	}
	return o.seatsLine() + "\n" + statusText(rows), nil
}

// seatsLine is the first line of Status: the effective running cap beside the
// configured value and what memory allows (ADR-0023).
func (o *ops) seatsLine() string {
	allows := "unknown"
	if o.memorySeats > 0 {
		allows = strconv.Itoa(o.memorySeats)
	}
	return fmt.Sprintf("seats: %d running at most (configured %d; memory allows %s)", o.cfg.MaxRunning, o.configuredMax, allows)
}

// Render re-runs the start-up file regeneration: the platform copy plus the
// manager's Reconcile, which regenerates the per-user files for every live
// row. The manager exposes no narrower file-regeneration call, so Reconcile
// is the call used.
func (o *ops) Render(ctx context.Context) error {
	if err := render.WritePlatformCopy(o.cfg, o.platformDir); err != nil {
		return err
	}
	return o.mgr.Reconcile(ctx)
}

// Stop stops the user's pair through the manager (under the user's lock).
// The name is validated here as well as in the client: the socket is the
// trust boundary, not the client that happens to use it.
func (o *ops) Stop(ctx context.Context, user string) error {
	if !userRe.MatchString(user) {
		return fmt.Errorf("invalid username: %q", user)
	}
	return o.mgr.Stop(ctx, user)
}
