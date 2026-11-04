package session

import (
	"context"
	"errors"
	"sort"
	"strings"

	"github.com/daniellemccool/researchcloud-ddp-comments/internal/docker"
	"github.com/daniellemccool/researchcloud-ddp-comments/internal/render"
)

// pair is what the desk found in Docker for one user: the two containers and
// the network, any of which may be missing.
type pair struct {
	proxy   *docker.Container
	firefox *docker.Container
	net     *docker.Network
}

// Reconcile brings Docker in line with the registry after a desk restart. It
// lists every object carrying desk.owner=desk to build the candidate set of
// users, then re-inspects each user's three objects under that user's lock
// before deciding: a running pair of the current generation (BOTH containers
// running, controller ruling F71) is adopted — the desk re-joins its network
// so the screen relay works again; a pair with an exited or missing container
// or network is stopped; a running pair whose generation changed is stopped;
// owner-labelled objects without a live registry row are logged as orphans
// and left untouched. It then regenerates the per-user files for every live
// row. Reconcile never starts anything and never removes an orphan.
func (m *Manager) Reconcile(ctx context.Context) error {
	containers, err := m.Docker.ContainerList(ctx, map[string]string{"desk.owner": render.Owner})
	if err != nil {
		return err
	}
	networks, err := m.Docker.NetworkList(ctx, map[string]string{"desk.owner": render.Owner})
	if err != nil {
		return err
	}
	live, err := m.Reg.Live()
	if err != nil {
		return err
	}

	// The lists above are only the candidate set; every decision below
	// re-inspects under the user lock.
	// An owner-labelled object without a desk.user label belongs to no
	// pair: it is logged and left, never turned into a candidate named
	// "net-" or "desk-proxy-".
	candidates := map[string]bool{}
	for _, c := range containers {
		if u := c.Labels["desk.user"]; u != "" {
			candidates[u] = true
		} else {
			m.logf("orphan: %s (no desk.user label)", c.Name)
		}
	}
	for _, n := range networks {
		if u := n.Labels["desk.user"]; u != "" {
			candidates[u] = true
		} else {
			m.logf("orphan: %s (no desk.user label)", n.Name)
		}
	}

	generation := render.Generation(m.Cfg)
	for user := range candidates {
		userLock := m.Locks.User(user)
		userLock.Lock()
		p, perr := m.inspectPair(ctx, user)
		_, isLive := live[user]
		switch {
		case perr != nil:
			// A foreign object or a failed inspect: touch nothing.
			m.logf("reconcile %s: %v", user, perr)
		case !isLive:
			m.logf("orphan: %s", strings.Join(p.names(), ", "))
		case p.complete() && p.firefox.Labels["desk.generation"] == generation:
			if err := m.connectSelf(ctx, "net-"+user); err != nil {
				m.logf("adopt %s: %v", user, err)
			} else {
				m.logf("adopted %s", user)
			}
		case p.complete():
			m.stopAndLog(ctx, user, "generation changed")
		default:
			m.stopAndLog(ctx, user, p.missingWhy())
		}
		userLock.Unlock()
	}

	// Regenerate the per-user files for every live row from the current
	// configuration; errors are logged, not fatal.
	for user, row := range live {
		userLock := m.Locks.User(user)
		userLock.Lock()
		if err := render.WriteUserFiles(m.Cfg, render.Build(m.Cfg, user, row.Index)); err != nil {
			m.logf("reconcile: WriteUserFiles %s: %v", user, err)
		}
		userLock.Unlock()
	}
	return nil
}

// inspectPair re-inspects user's three objects; the caller must hold
// Locks.User(user). The returned error is non-nil when a present object is
// foreign (the pair is then partial and must not be touched) or an inspect
// failed with something other than not-found.
func (m *Manager) inspectPair(ctx context.Context, user string) (*pair, error) {
	p := &pair{}
	if n, err := m.Docker.NetworkInspect(ctx, "net-"+user); err == nil {
		if err := checkOwnership(n.Labels, user, n.Name); err != nil {
			return p, err
		}
		p.net = &n
	} else if !errors.Is(err, docker.ErrNotFound) {
		return p, err
	}
	if c, err := m.Docker.ContainerInspect(ctx, "desk-proxy-"+user); err == nil {
		if err := checkOwnership(c.Labels, user, c.Name); err != nil {
			return p, err
		}
		p.proxy = &c
	} else if !errors.Is(err, docker.ErrNotFound) {
		return p, err
	}
	if c, err := m.Docker.ContainerInspect(ctx, "desk-firefox-"+user); err == nil {
		if err := checkOwnership(c.Labels, user, c.Name); err != nil {
			return p, err
		}
		p.firefox = &c
	} else if !errors.Is(err, docker.ErrNotFound) {
		return p, err
	}
	return p, nil
}

// stopAndLog stops user's pair under the caller's user lock and logs why.
func (m *Manager) stopAndLog(ctx context.Context, user, why string) {
	if err := m.stop(ctx, user); err != nil {
		m.logf("stopped %s: %s (cleanup: %v)", user, why, err)
	} else {
		m.logf("stopped %s: %s", user, why)
	}
}

// complete reports whether both containers are present and running and the
// network exists — "running" means both containers (F71).
func (p *pair) complete() bool {
	return p.proxy != nil && p.firefox != nil && p.net != nil &&
		p.proxy.Running && p.firefox.Running
}

// missingWhy describes why an incomplete pair is stopped.
func (p *pair) missingWhy() string {
	var why []string
	if p.net == nil {
		why = append(why, "network missing")
	}
	if p.proxy == nil || !p.proxy.Running {
		why = append(why, "proxy not running")
	}
	if p.firefox == nil || !p.firefox.Running {
		why = append(why, "firefox not running")
	}
	return strings.Join(why, "; ")
}

// names lists the pair's existing object names, for the orphan log.
func (p *pair) names() []string {
	var names []string
	if p.net != nil {
		names = append(names, p.net.Name)
	}
	if p.proxy != nil {
		names = append(names, p.proxy.Name)
	}
	if p.firefox != nil {
		names = append(names, p.firefox.Name)
	}
	sort.Strings(names)
	return names
}
