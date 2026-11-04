package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"regexp"
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
func call(deskRoot string, req ctl.Request, stdout, stderr io.Writer) (ctl.Response, int) {
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
// (default /desk), its output pointed at stderr.
func deskRootFlagSet(name string, stderr io.Writer) (*flag.FlagSet, *string) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	deskRoot := fs.String("desk-root", "/desk", "the desk's directory holding state/desk.sock")
	return fs, deskRoot
}

// statusCmd implements "desk status [--desk-root /desk]": it asks the running
// desk for its status text and prints it.
func statusCmd(args []string, stdout, stderr io.Writer) int {
	fs, deskRoot := deskRootFlagSet("status", stderr)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 0 {
		usage(stderr)
		return 2
	}
	resp, code := call(*deskRoot, ctl.Request{Op: "status"}, stdout, stderr)
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
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 0 {
		usage(stderr)
		return 2
	}
	if _, code := call(*deskRoot, ctl.Request{Op: "render"}, stdout, stderr); code != 0 {
		return code
	}
	_, _ = fmt.Fprintln(stdout, "rendered")
	return 0
}

// stopCmd implements "desk stop <user> [--desk-root /desk]": the user is
// validated against the username grammar before anything is sent.
func stopCmd(args []string, stdout, stderr io.Writer) int {
	fs, deskRoot := deskRootFlagSet("stop", stderr)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		usage(stderr)
		return 2
	}
	user := fs.Arg(0)
	if !userRe.MatchString(user) {
		_, _ = fmt.Fprintf(stderr, "desk: invalid username: %q\n", user)
		return 1
	}
	if _, code := call(*deskRoot, ctl.Request{Op: "stop", User: user}, stdout, stderr); code != 0 {
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
	mgr         *session.Manager
	cfg         *config.Config
	platformDir string // <repo>/platforms/<cfg.Platform>
}

// Status returns the one-line-per-row text of the manager's Status.
func (o *ops) Status(ctx context.Context) (string, error) {
	rows, err := o.mgr.Status(ctx)
	if err != nil {
		return "", err
	}
	return statusText(rows), nil
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
func (o *ops) Stop(ctx context.Context, user string) error {
	return o.mgr.Stop(ctx, user)
}
