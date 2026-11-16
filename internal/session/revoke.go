package session

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/daniellemccool/researchcloud-ddp-comments/internal/provision"
	"github.com/daniellemccool/researchcloud-ddp-comments/internal/render"
)

// Revoke removes user's access after a good CO poll omitted them (ADR-0005).
// Under the user lock, in this order: the registry row is retired (under
// Global), the registry provisioning step is marked undone, the user's
// relayed screens are closed, the pair is stopped, and the screens are closed
// again whatever the stop returned. Then, when 4CAT is enabled (DropToken
// set), the user's API token is deleted in 4CAT, and only once that succeeded
// is the token file removed and the fourcat_apitoken step marked undone, so a
// retired row whose token step is still done marks an unfinished revoke.
// Retire and MarkUndone touch no Docker, so a crash or a Docker error at any
// later point leaves a retired row that the next poll or Reconcile finishes.
// A concurrent Start blocked on the user lock then finds no live row and
// returns Gone. Idempotent; profile, captures, password and the 4CAT account
// and its datasets are left as they are.
func (m *Manager) Revoke(ctx context.Context, user string, closeScreens func(string) int) error {
	userLock := m.Locks.User(user)
	userLock.Lock()
	defer userLock.Unlock()
	return m.revoke(ctx, user, closeScreens)
}

// revoke is Revoke without the user lock: the caller holds Locks.User(user).
// A failed Retire or MarkUndone does not skip the screen close or the stop:
// the user loses the running session either way, and the errors are joined.
func (m *Manager) revoke(ctx context.Context, user string, closeScreens func(string) int) error {
	if closeScreens == nil {
		closeScreens = func(string) int { return 0 }
	}
	var errs []error
	m.Locks.Global.Lock()
	if rerr := m.Reg.Retire(user); rerr != nil {
		errs = append(errs, fmt.Errorf("retire %s: %w", user, rerr))
	}
	m.Locks.Global.Unlock()
	if merr := provision.MarkUndone(m.Cfg, user, "registry"); merr != nil {
		errs = append(errs, fmt.Errorf("mark %s registry undone: %w", user, merr))
	}
	closeScreens(user)
	// A screen reopened while the stop ran (or a stop that failed half way)
	// must not stay open.
	defer closeScreens(user)
	if serr := m.stop(ctx, user); serr != nil {
		errs = append(errs, serr)
	}
	if m.DropToken != nil {
		if derr := m.dropToken(ctx, user); derr != nil {
			errs = append(errs, derr)
		}
	}
	return errors.Join(errs...)
}

// dropToken deletes user's token in 4CAT and then, only on success, the
// token file and the token step's done mark.
func (m *Manager) dropToken(ctx context.Context, user string) error {
	if err := m.DropToken(ctx, user); err != nil {
		return fmt.Errorf("delete %s's 4CAT token: %w", user, err)
	}
	if err := provision.RemoveToken(m.Cfg, user); err != nil {
		return fmt.Errorf("remove %s's token file: %w", user, err)
	}
	if err := provision.MarkUndone(m.Cfg, user, "fourcat_apitoken"); err != nil {
		return fmt.Errorf("mark %s token step undone: %w", user, err)
	}
	return nil
}

// RevokeAbsent revokes, from Reg.Rows() (Live cannot tell retired from
// missing): every live row whose user is absent from present, and every
// retired row whose user still has a running container (anyRunning) or whose
// provision.json still marks registry done — a revoke cut short, or a row
// retired by hand — or, with 4CAT enabled, still marks fourcat_apitoken done
// while a non-empty present lacks the user (the token deletion failed). The
// last sign needs the list's agreement: a re-added admin who opened 4CAT
// before /desk/ has a retired row and a fresh token, and keeps it. Each is re-checked under the user lock before Revoke, so
// a user re-added and reactivated since the selection keeps their pair. An
// empty present selects no live row (ADR-0005: an empty list changes
// nothing); retired rows are still finished. It returns the users whose
// revoke completed, sorted; errors are logged per user and the user is
// selected again on the next poll.
func (m *Manager) RevokeAbsent(ctx context.Context, present map[string]bool, closeScreens func(string) int) (revoked []string) {
	rows, err := m.Reg.Rows()
	if err != nil {
		m.logf("revoke: registry: %v", err)
		return nil
	}
	emptyList := len(present) == 0
	if emptyList {
		m.logf("revoke: CO list empty; nothing changes for live rows")
	}
	var running map[string]bool
	var users []string
	for _, r := range rows {
		switch r.Status {
		case "live":
			if !emptyList && !present[r.User] {
				users = append(users, r.User)
			}
		case "retired":
			if running == nil {
				if running, err = m.anyRunning(ctx); err != nil {
					// Without the list only the step file can say a revoke
					// is unfinished; a running leftover waits for the next poll.
					m.logf("revoke: container list: %v", err)
					running = map[string]bool{}
				}
			}
			st := provision.Load(m.Cfg, r.User)
			tokenLeft := m.DropToken != nil && !emptyList && !present[r.User] &&
				st.Steps["fourcat_apitoken"] == "done"
			if running[r.User] || st.Steps["registry"] == "done" || tokenLeft {
				users = append(users, r.User)
			}
		}
	}
	sort.Strings(users)
	for _, user := range users {
		ok, err := m.revokeIf(ctx, user, present, closeScreens)
		if err != nil {
			m.logf("revoke %s: %v", user, err)
			continue
		}
		if ok {
			m.logf("revoked %s", user)
			revoked = append(revoked, user)
		}
	}
	return revoked
}

// revokeIf takes user's lock, re-reads the registry and revokes unless the
// row is now live and present holds the user (reactivated since the
// selection) or the row is gone. ok reports whether the revoke ran.
func (m *Manager) revokeIf(ctx context.Context, user string, present map[string]bool, closeScreens func(string) int) (ok bool, err error) {
	userLock := m.Locks.User(user)
	userLock.Lock()
	defer userLock.Unlock()
	rows, err := m.Reg.Rows()
	if err != nil {
		return false, fmt.Errorf("registry: %w", err)
	}
	status := ""
	for _, r := range rows {
		if r.User == user {
			status = r.Status
		}
	}
	if status == "" || (status == "live" && present[user]) {
		return false, nil
	}
	return true, m.revoke(ctx, user, closeScreens)
}

// anyRunning reports the users with ANY running container labelled
// desk.owner=desk, in one ContainerList: unlike runningSets, a proxy-only
// leftover counts, since a revoke must remove it too.
func (m *Manager) anyRunning(ctx context.Context) (map[string]bool, error) {
	containers, err := m.Docker.ContainerList(ctx, map[string]string{"desk.owner": render.Owner})
	if err != nil {
		return nil, err
	}
	out := make(map[string]bool, len(containers))
	for _, c := range containers {
		if u := c.Labels["desk.user"]; c.Running && u != "" {
			out[u] = true
		}
	}
	return out, nil
}
