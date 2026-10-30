// Command desk is the data-donation desk: one long-lived container serving
// the gated listener (collector and admin pages) and the container listener
// (health endpoint only).
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/daniellemccool/researchcloud-ddp-comments/internal/access"
	"github.com/daniellemccool/researchcloud-ddp-comments/internal/config"
	"github.com/daniellemccool/researchcloud-ddp-comments/internal/platform"
	"github.com/daniellemccool/researchcloud-ddp-comments/internal/web"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	switch os.Args[1] {
	case "serve":
		if err := serve(os.Args[2:]); err != nil {
			fmt.Fprintf(os.Stderr, "desk: %v\n", err)
			os.Exit(1)
		}
	default:
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: desk serve --desk-root /desk --repo /repo [--listen :8110] [--container-listen :8111]")
}

func serve(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	deskRoot := fs.String("desk-root", "", "the desk's directory, holding desk.json (required)")
	repo := fs.String("repo", "", "read-only repository copy holding platforms/ (required)")
	listen := fs.String("listen", ":8110", "gated listener address")
	containerListen := fs.String("container-listen", ":8111", "container listener address")
	if err := fs.Parse(args); err != nil {
		usage()
		os.Exit(2)
	}
	if *deskRoot == "" || *repo == "" {
		usage()
		os.Exit(2)
	}

	cfg, err := config.Load(filepath.Join(*deskRoot, "desk.json"))
	if err != nil {
		return err
	}
	p, err := platform.Load(filepath.Join(*repo, "platforms", cfg.Platform, "platform.json"))
	if err != nil {
		return err
	}

	ac := access.Config{
		CollectorRole: cfg.CollectorRole,
		AdminRole:     cfg.AdminRole,
		GateSecret:    cfg.GateSecret,
	}
	logPath := filepath.Join(cfg.DeskRoot, "state", "access.ndjson")

	gated := web.New(cfg, ac, p, logPath)
	gated.Addr = *listen
	container := web.NewContainer()
	container.Addr = *containerListen

	errCh := make(chan error, 2)
	go func() { errCh <- gated.ListenAndServe() }()
	go func() { errCh <- container.ListenAndServe() }()
	fmt.Fprintf(os.Stderr, "desk: gated listener on %s\n", gated.Addr)
	fmt.Fprintf(os.Stderr, "desk: container listener on %s\n", container.Addr)

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
	select {
	case sig := <-sigCh:
		fmt.Fprintf(os.Stderr, "desk: %s received, shutting down\n", sig)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = gated.Shutdown(ctx)
		_ = container.Shutdown(ctx)
		return nil
	case err := <-errCh:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	}
}
