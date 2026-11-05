// Command desk is the data-donation desk: one long-lived container serving
// the gated listener (collector and admin pages) and the container listener
// (health endpoint only), plus the operator commands (status, render, stop)
// that talk to the serving desk over its unix socket.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/daniellemccool/researchcloud-ddp-comments/internal/access"
	"github.com/daniellemccool/researchcloud-ddp-comments/internal/config"
	"github.com/daniellemccool/researchcloud-ddp-comments/internal/ctl"
	"github.com/daniellemccool/researchcloud-ddp-comments/internal/desklog"
	"github.com/daniellemccool/researchcloud-ddp-comments/internal/docker"
	"github.com/daniellemccool/researchcloud-ddp-comments/internal/fourcat"
	"github.com/daniellemccool/researchcloud-ddp-comments/internal/locks"
	"github.com/daniellemccool/researchcloud-ddp-comments/internal/platform"
	"github.com/daniellemccool/researchcloud-ddp-comments/internal/provision"
	"github.com/daniellemccool/researchcloud-ddp-comments/internal/registry"
	"github.com/daniellemccool/researchcloud-ddp-comments/internal/render"
	"github.com/daniellemccool/researchcloud-ddp-comments/internal/session"
	"github.com/daniellemccool/researchcloud-ddp-comments/internal/web"
)

func main() {
	os.Exit(dispatch(os.Args[1:], os.Stdout, os.Stderr))
}

// dispatch runs one command line and returns the process exit code. It is a
// pure function of its arguments (serve's blocking loop aside), so the tests
// exercise it without building a binary.
func dispatch(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		usage(stderr)
		return 2
	}
	switch args[0] {
	case "help", "--help", "-h":
		usage(stdout)
		return 0
	case "serve":
		code, err := serve(args[1:], stdout, stderr)
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "desk: %v\n", err)
			return 1
		}
		return code
	case "template":
		return templateCmd(args[1:])
	case "status":
		return statusCmd(args[1:], stdout, stderr)
	case "render":
		return renderCmd(args[1:], stdout, stderr)
	case "stop":
		return stopCmd(args[1:], stdout, stderr)
	default:
		usage(stderr)
		return 2
	}
}

// usage writes the command summary to w (stdout for help, stderr otherwise).
func usage(w io.Writer) {
	_, _ = fmt.Fprintln(w, "usage: desk serve --desk-root /desk [--repo /repo] [--listen :8110] [--container-listen :8111]")
	_, _ = fmt.Fprintln(w, "       desk status [--desk-root /desk]")
	_, _ = fmt.Fprintln(w, "       desk render [--desk-root /desk]")
	_, _ = fmt.Fprintln(w, "       desk stop [--desk-root /desk] <user>")
	_, _ = fmt.Fprintln(w, "       desk template release --profile DIR --platform NAME --platforms-dir DIR --zeeschuimer VERSION --out DIR")
	_, _ = fmt.Fprintln(w, "       desk help")
}

// serve runs the desk. It returns the exit code (2 for a usage error, after
// printing the usage itself) and a fatal error.
func serve(args []string, stdout, stderr io.Writer) (int, error) {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {}
	deskRoot := fs.String("desk-root", "", "the desk's directory, holding desk.json (required)")
	repo := fs.String("repo", "/repo", "read-only repository copy holding platforms/ (the compose mount)")
	listen := fs.String("listen", ":8110", "gated listener address")
	containerListen := fs.String("container-listen", ":8111", "container listener address")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			usage(stdout)
			return 0, nil
		}
		usage(stderr)
		return 2, nil
	}
	if *deskRoot == "" {
		usage(stderr)
		return 2, nil
	}

	cfg, err := config.Load(filepath.Join(*deskRoot, "desk.json"))
	if err != nil {
		return 0, err
	}
	p, err := platform.Load(filepath.Join(*repo, "platforms", cfg.Platform, "platform.json"))
	if err != nil {
		return 0, err
	}

	// The desk cannot run without Docker: a client error is fatal.
	dc, err := docker.New()
	if err != nil {
		return 0, fmt.Errorf("docker client: %w", err)
	}
	vctx, vcancel := context.WithTimeout(context.Background(), 30*time.Second)
	err = checkDockerVersion(vctx, dc)
	vcancel()
	if err != nil {
		return 0, err
	}
	selfID, err := docker.SelfID()
	if err != nil {
		return 0, fmt.Errorf("container id: %w", err)
	}

	// When 4CAT is enabled, find its containers now; a missing 4CAT is not
	// fatal, the client looks the names up lazily per visit instead.
	var fc *fourcat.Client
	if cfg.FourcatEnabled {
		backend, db, ferr := fourcat.FindContainers(context.Background(), dc)
		if ferr != nil {
			_, _ = fmt.Fprintf(stderr, "desk: 4CAT containers not found yet (%v); provisioning will retry per visit\n", ferr)
			backend, db = "", ""
		}
		fc = &fourcat.Client{Docker: dc, Backend: backend, DB: db, FrontendURL: "http://4cat-frontend:5000"}
	}

	platformDir := filepath.Join(*repo, "platforms", cfg.Platform)
	if err := render.WritePlatformCopy(cfg, platformDir); err != nil {
		return 0, err
	}

	reg := &registry.Registry{Path: filepath.Join(cfg.DeskRoot, "registry.tsv"), MaxSessions: cfg.MaxSessions}
	lockSet := &locks.Set{}
	prov := &provision.Provisioner{Cfg: cfg, Reg: reg, Fourcat: fc, Locks: lockSet}
	dlog := &desklog.Log{Path: filepath.Join(cfg.DeskRoot, "state", "desk-log.ndjson"), DefaultKind: p.DefaultKind}
	mgr := &session.Manager{
		Cfg:    cfg,
		Docker: dc,
		Reg:    reg,
		Locks:  lockSet,
		SelfID: selfID,
		Log:    log.New(stderr, "desk: ", log.LstdFlags),
	}

	// Adopt the pairs that survived a desk restart; errors are logged, not
	// fatal. The reconcile gets a 2-minute budget.
	rctx, rcancel := context.WithTimeout(context.Background(), 2*time.Minute)
	if err := mgr.Reconcile(rctx); err != nil {
		_, _ = fmt.Fprintf(stderr, "desk: reconcile: %v\n", err)
	}
	rcancel()

	// The operator socket: status, render and stop run inside this process's
	// locks. A socket failure is fatal — the desk must not serve HTTP with no
	// operator socket.
	socketPath := filepath.Join(cfg.DeskRoot, "state", "desk.sock")
	ctlCtx, ctlCancel := context.WithCancel(context.Background())
	ctlErr := make(chan error, 1)
	ctlReady := make(chan struct{}, 1)
	go func() {
		ctlErr <- ctl.ServeReady(ctlCtx, socketPath, &ops{mgr: mgr, cfg: cfg, platformDir: platformDir}, ctlReady)
	}()
	select {
	case <-ctlReady:
		_, _ = fmt.Fprintf(stderr, "desk: operator socket on %s\n", socketPath)
	case err := <-ctlErr:
		ctlCancel()
		return 0, fmt.Errorf("operator socket: %w", err)
	}

	ac := access.Config{
		CollectorRole: cfg.CollectorRole,
		AdminRole:     cfg.AdminRole,
		GateSecret:    cfg.GateSecret,
	}
	logPath := filepath.Join(cfg.DeskRoot, "state", "access.ndjson")
	// One activity tracker for the whole desk: the relay records open
	// screens in it and can close a user's screens through it.
	activity := web.NewActivity(time.Now)
	deps := web.Deps{
		Cfg:          cfg,
		Access:       ac,
		Platform:     p,
		LogPath:      logPath,
		Prov:         prov,
		Sessions:     mgr,
		Log:          dlog,
		FourcatProxy: fourcatProxy(cfg),
		Relay:        web.NewRelay(mgr, activity),
	}
	deps.Admin = web.NewAdmin(deps)
	gated := web.New(deps)
	gated.Addr = *listen
	container := web.NewContainer()
	container.Addr = *containerListen

	errCh := make(chan error, 2)
	go func() { errCh <- gated.ListenAndServe() }()
	go func() { errCh <- container.ListenAndServe() }()
	_, _ = fmt.Fprintf(stderr, "desk: gated listener on %s\n", gated.Addr)
	_, _ = fmt.Fprintf(stderr, "desk: container listener on %s\n", container.Addr)

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
	select {
	case sig := <-sigCh:
		_, _ = fmt.Fprintf(stderr, "desk: %s received, shutting down\n", sig)
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		ctlCancel()
		_ = gated.Shutdown(shutdownCtx)
		_ = container.Shutdown(shutdownCtx)
		select {
		case <-ctlErr:
		case <-shutdownCtx.Done():
		}
		return 0, nil
	case err := <-ctlErr:
		// The operator socket died while serving: fatal, same path as a
		// listener error.
		_, _ = fmt.Fprintf(stderr, "desk: operator socket: %v\n", err)
		ctlCancel()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = gated.Shutdown(shutdownCtx)
		_ = container.Shutdown(shutdownCtx)
		return 0, err
	case err := <-errCh:
		ctlCancel()
		select {
		case <-ctlErr:
		case <-time.After(10 * time.Second):
		}
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return 0, err
		}
		return 0, nil
	}
}

// fourcatProxy returns the admin 4CAT proxy, or a nil handler when 4CAT is
// disabled so the /fourcat/ route answers 404 (review M6).
func fourcatProxy(cfg *config.Config) http.Handler {
	if !cfg.FourcatEnabled {
		return nil
	}
	return web.NewFourcatProxy(cfg.FQDN)
}

// The daemon floor for ADR-0007's isolated gateway
// (com.docker.network.bridge.gateway_mode_ipv4=isolated): Docker Engine 28,
// which speaks API 1.48.
const (
	minEngineMajor = 28
	minAPIMajor    = 1
	minAPIMinor    = 48
)

// checkDockerVersion refuses a daemon below Docker Engine 28 / API 1.48
// (controller ruling F91): an older daemon may ignore the isolated gateway
// option silently, and the pair network would then route to the host. A
// version that cannot be read or parsed is refused too, so the check fails
// closed.
func checkDockerVersion(ctx context.Context, dc docker.Client) error {
	const need = "the desk needs Docker Engine 28 / API 1.48 or newer for the isolated gateway of its pair networks (ADR-0007)"
	v, err := dc.ServerVersion(ctx)
	if err != nil {
		return fmt.Errorf("%s; reading the daemon version failed: %w", need, err)
	}
	engineMajor, _, engineOK := majorMinor(v.Engine)
	apiMajor, apiMinor, apiOK := majorMinor(v.API)
	if !engineOK || !apiOK || engineMajor < minEngineMajor ||
		apiMajor < minAPIMajor || (apiMajor == minAPIMajor && apiMinor < minAPIMinor) {
		return fmt.Errorf("%s; this daemon is Engine %q, API %q", need, v.Engine, v.API)
	}
	return nil
}

// majorMinor parses the leading "<major>.<minor>" of a version string such
// as "28.5.1+dfsg1" or "1.48"; ok is false when either part is not a number.
func majorMinor(s string) (major, minor int, ok bool) {
	parts := strings.SplitN(s, ".", 3)
	if len(parts) < 2 {
		return 0, 0, false
	}
	major, err1 := strconv.Atoi(parts[0])
	minor, err2 := strconv.Atoi(parts[1])
	if err1 != nil || err2 != nil {
		return 0, 0, false
	}
	return major, minor, true
}
