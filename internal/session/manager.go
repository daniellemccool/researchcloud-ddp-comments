package session

import (
	"context"
	"log"
	"sync"
	"time"

	"github.com/daniellemccool/researchcloud-ddp-comments/internal/config"
	"github.com/daniellemccool/researchcloud-ddp-comments/internal/docker"
	"github.com/daniellemccool/researchcloud-ddp-comments/internal/locks"
	"github.com/daniellemccool/researchcloud-ddp-comments/internal/provision"
	"github.com/daniellemccool/researchcloud-ddp-comments/internal/registry"
	"github.com/daniellemccool/researchcloud-ddp-comments/internal/render"
)

// Manager owns the collector pairs on this desk. Lock order is always
// Locks.User(u) first, then Locks.Global, never the reverse; Docker calls
// never happen while Global is held except the running-count list inside
// Start's reservation, which ADR-0015 requires to be atomic with the
// reservation itself. Every write of pair.json happens under Locks.User(u).
type Manager struct {
	Cfg        *config.Config
	Docker     docker.Client
	Reg        *registry.Registry
	Locks      *locks.Set
	SelfID     string        // this desk container's id (docker.SelfID())
	HealthWait time.Duration // 0 → 10 s
	// Probe reports whether url answers; nil → GET with a 2 s timeout,
	// true on 200.
	Probe func(ctx context.Context, url string) bool
	Log   *log.Logger // nil → log.Default()

	mu       sync.Mutex      // guards starting
	starting map[string]bool // starts in flight, reserved under Locks.Global
}

// StartResult is the outcome of Start.
type StartResult int

const (
	// Started: the pair is (now) running.
	Started StartResult = iota
	// Busy: the running cap (Cfg.MaxRunning) is reserved by other sessions.
	Busy
	// StartFailed: the pair could not be created; nothing is left behind.
	StartFailed
	// Gone: the user has no live registry row or is not provisioned.
	Gone
)

// Row is one registry row's status: whether the pair runs and the
// provisioning state on disk.
type Row struct {
	User    string
	Index   int
	Running bool
	State   provision.State
}

// Running counts the running firefox containers by label, keyed by their
// desk.user label.
func (m *Manager) Running(ctx context.Context) (map[string]bool, error) {
	containers, err := m.Docker.ContainerList(ctx, map[string]string{
		"desk.owner": render.Owner,
		"desk.role":  "firefox",
	})
	if err != nil {
		return nil, err
	}
	out := make(map[string]bool, len(containers))
	for _, c := range containers {
		if c.Running {
			out[c.Labels["desk.user"]] = true
		}
	}
	return out, nil
}

// IsRunning reports whether user's firefox container is running.
func (m *Manager) IsRunning(ctx context.Context, user string) bool {
	running, err := m.Running(ctx)
	if err != nil {
		return false
	}
	return running[user]
}

// Ready reports whether user's pair is running and the Firefox's noVNC port
// answers.
func (m *Manager) Ready(ctx context.Context, user string) bool {
	if !m.IsRunning(ctx, user) {
		return false
	}
	return m.probe(ctx, "http://desk-firefox-"+user+":5800/")
}

// OpenURL opens url in the user's Firefox via su-exec inside the firefox
// container. It returns the command's exit code and the last 200 bytes of
// its stderr; a transport error returns (-1, "", err).
func (m *Manager) OpenURL(ctx context.Context, user, url string) (rc int, stderr string, err error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	argv := []string{"su-exec", "1000:1000", "/usr/lib/firefox/firefox", "--profile", "/config/profile", url}
	env := []string{
		"DISPLAY=:0",
		"DBUS_SESSION_BUS_ADDRESS=unix:path=/tmp/dbus.base",
		"HOME=/config",
		"XDG_RUNTIME_DIR=/tmp/run/user/app",
	}
	ex, err := m.Docker.Exec(ctx, "desk-firefox-"+user, argv, env, nil)
	if err != nil {
		return -1, "", err
	}
	return ex.ExitCode, stderrTail(ex.Stderr, 200), nil
}

// Status returns one Row per registry row (live and retired): Running from
// Running(), State from the provisioning step file.
func (m *Manager) Status(ctx context.Context) ([]Row, error) {
	rows, err := m.Reg.Rows()
	if err != nil {
		return nil, err
	}
	running, err := m.Running(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Row, 0, len(rows))
	for _, r := range rows {
		out = append(out, Row{
			User:    r.User,
			Index:   r.Index,
			Running: running[r.User],
			State:   provision.Load(m.Cfg, r.User),
		})
	}
	return out, nil
}

// logger returns the manager's logger, defaulting to log.Default().
func (m *Manager) logger() *log.Logger {
	if m.Log != nil {
		return m.Log
	}
	return log.Default()
}

// logf logs a line through the manager's logger.
func (m *Manager) logf(format string, args ...any) {
	m.logger().Printf(format, args...)
}

// stderrTail returns the last n bytes of s.
func stderrTail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}
