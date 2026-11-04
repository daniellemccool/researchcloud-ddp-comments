package session

import (
	"context"
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
// lists every object carrying desk.owner=desk, groups them by desk.user and,
// under that user's lock: adopts a running pair of the current generation
// (re-joining its network so the screen relay works again), stops a pair with
// an exited or missing container or network, stops a running pair whose
// generation changed, and logs owner-labelled objects without a live registry
// row as orphans without touching them. It then regenerates the per-user
// files for every live row. Reconcile never starts anything.
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

	byUser := map[string]*pair{}
	group := func(user string) *pair {
		p, ok := byUser[user]
		if !ok {
			p = &pair{}
			byUser[user] = p
		}
		return p
	}
	for i := range containers {
		c := &containers[i]
		p := group(c.Labels["desk.user"])
		switch c.Labels["desk.role"] {
		case "proxy":
			p.proxy = c
		case "firefox":
			p.firefox = c
		}
	}
	for i := range networks {
		n := &networks[i]
		if n.Labels["desk.role"] == "net" {
			group(n.Labels["desk.user"]).net = n
		}
	}

	generation := render.Generation(m.Cfg)
	for user, p := range byUser {
		userLock := m.Locks.User(user)
		userLock.Lock()
		_, isLive := live[user]
		switch {
		case !isLive:
			m.logf("orphan: %s", strings.Join(p.names(user), ", "))
		case p.complete() && p.firefox.Labels["desk.generation"] == generation:
			if err := m.connectSelf(ctx, "net-"+user); err != nil {
				m.logf("adopt %s: %v", user, err)
			} else {
				m.logf("adopted %s", user)
			}
		case p.complete():
			m.stopAndLog(ctx, user, "generation changed")
		default:
			m.stopAndLog(ctx, user, p.missingWhy(user))
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

// stopAndLog stops user's pair under the caller's user lock and logs why.
func (m *Manager) stopAndLog(ctx context.Context, user, why string) {
	if err := m.stop(ctx, user); err != nil {
		m.logf("stopped %s: %s (cleanup: %v)", user, why, err)
	} else {
		m.logf("stopped %s: %s", user, why)
	}
}

// complete reports whether both containers are present and running and the
// network exists.
func (p *pair) complete() bool {
	return p.proxy != nil && p.firefox != nil && p.net != nil &&
		p.proxy.Running && p.firefox.Running
}

// missingWhy describes why an incomplete pair is stopped.
func (p *pair) missingWhy(user string) string {
	var why []string
	if p.net == nil {
		why = append(why, "network net-"+user+" missing")
	}
	if p.proxy == nil {
		why = append(why, "container desk-proxy-"+user+" missing")
	} else if !p.proxy.Running {
		why = append(why, "container desk-proxy-"+user+" exited")
	}
	if p.firefox == nil {
		why = append(why, "container desk-firefox-"+user+" missing")
	} else if !p.firefox.Running {
		why = append(why, "container desk-firefox-"+user+" exited")
	}
	return strings.Join(why, "; ")
}

// names lists the pair's object names that exist, for the orphan log.
func (p *pair) names(user string) []string {
	names := make([]string, 0, 3)
	if p.net != nil {
		names = append(names, "net-"+user)
	}
	if p.proxy != nil {
		names = append(names, "desk-proxy-"+user)
	}
	if p.firefox != nil {
		names = append(names, "desk-firefox-"+user)
	}
	sort.Strings(names)
	return names
}
