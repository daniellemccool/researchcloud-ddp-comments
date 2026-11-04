package session

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/daniellemccool/researchcloud-ddp-comments/internal/docker"
	"github.com/daniellemccool/researchcloud-ddp-comments/internal/provision"
	"github.com/daniellemccool/researchcloud-ddp-comments/internal/render"
)

// stopTimeout is how long ContainerStop waits before killing a container.
const stopTimeout = 10 * time.Second

// cleanupTimeout bounds the best-effort stop sequence a failed Start runs on
// a fresh context.
const cleanupTimeout = 30 * time.Second

// ADR-0007: an internal bridge network still routes to its own gateway, so a
// container on it can reach the host through that address. Docker's isolated
// gateway mode removes the gateway route, leaving the proxy as the browser's
// only path off the pair network.
const (
	networkGatewayModeOption   = "com.docker.network.bridge.gateway_mode_ipv4"
	networkGatewayModeIsolated = "isolated"
)

// Start creates user's pair (net-<user>, desk-proxy-<user>,
// desk-firefox-<user>) from the rendered spec. The running cap is reserved
// atomically: under Locks.User(u) and Locks.Global together, the size of the
// union of the users with a running Firefox container and the starts in
// flight must be below Cfg.MaxRunning (ADR-0015, controller ruling F88). The
// Started shortcut alone uses "running" as both containers of the pair
// (controller ruling F71); a user's own half pair is rebuilt in the user's
// own seat. Any failure after the network exists runs the stop sequence, so
// no half pair is left behind.
func (m *Manager) Start(ctx context.Context, user string) (StartResult, error) {
	userLock := m.Locks.User(user)
	userLock.Lock()
	defer userLock.Unlock()

	m.Locks.Global.Lock()
	live, err := m.Reg.Live()
	if err != nil {
		m.Locks.Global.Unlock()
		return StartFailed, fmt.Errorf("registry: %w", err)
	}
	row, ok := live[user]
	if !ok || !provision.Load(m.Cfg, user).Ready() {
		m.Locks.Global.Unlock()
		return Gone, nil
	}
	running, firefoxRunning, err := m.runningSets(ctx)
	if err != nil {
		m.Locks.Global.Unlock()
		return StartFailed, fmt.Errorf("running count: %w", err)
	}
	if running[user] {
		m.mu.Lock()
		delete(m.starting, user)
		m.mu.Unlock()
		m.Locks.Global.Unlock()
		return Started, nil
	}
	// The cap counts the union of the users with a running Firefox and the
	// starts in flight, so a user in both for a moment is not double counted.
	// The user's own half pair (a Firefox beside a dead proxy) is about to be
	// rebuilt in place, so it is not counted against the user.
	m.mu.Lock()
	reserved := make(map[string]bool, len(m.starting)+len(firefoxRunning))
	for u := range m.starting {
		reserved[u] = true
	}
	for u := range firefoxRunning {
		reserved[u] = true
	}
	delete(reserved, user)
	if len(reserved) >= m.Cfg.MaxRunning {
		m.mu.Unlock()
		m.Locks.Global.Unlock()
		return Busy, nil
	}
	if m.starting == nil {
		m.starting = map[string]bool{}
	}
	m.starting[user] = true
	m.mu.Unlock()
	m.Locks.Global.Unlock()
	// The reservation is released under the global lock whatever happens.
	defer func() {
		m.Locks.Global.Lock()
		m.mu.Lock()
		delete(m.starting, user)
		m.mu.Unlock()
		m.Locks.Global.Unlock()
	}()

	spec := render.Build(m.Cfg, user, row.Index)
	netName := spec.Network
	proxyName := spec.Proxy.Name
	firefoxName := spec.Firefox.Name

	// Leftovers of an earlier session: inspect all three first and refuse
	// foreign objects (an error naming the object, nothing removed) before
	// anything is touched. Objects of ours go through the stop sequence,
	// because the desk may still be attached to the network when the
	// Firefox died and a bare NetworkRemove would fail with the
	// active-endpoints conflict.
	net, netErr := m.Docker.NetworkInspect(ctx, netName)
	proxy, proxyErr := m.Docker.ContainerInspect(ctx, proxyName)
	firefox, firefoxErr := m.Docker.ContainerInspect(ctx, firefoxName)
	if netErr == nil {
		if err := checkOwnership(net.Labels, user, netName); err != nil {
			return StartFailed, err
		}
	} else if !errors.Is(netErr, docker.ErrNotFound) {
		return StartFailed, netErr
	}
	if proxyErr == nil {
		if err := checkOwnership(proxy.Labels, user, proxyName); err != nil {
			return StartFailed, err
		}
	} else if !errors.Is(proxyErr, docker.ErrNotFound) {
		return StartFailed, proxyErr
	}
	if firefoxErr == nil {
		if err := checkOwnership(firefox.Labels, user, firefoxName); err != nil {
			return StartFailed, err
		}
	} else if !errors.Is(firefoxErr, docker.ErrNotFound) {
		return StartFailed, firefoxErr
	}
	if netErr == nil || proxyErr == nil || firefoxErr == nil {
		if err := m.stop(ctx, user); err != nil {
			return m.failStart(ctx, user, err)
		}
	}

	// The pair itself.
	netLabels := make(map[string]string, len(spec.Labels)+1)
	for k, v := range spec.Labels {
		netLabels[k] = v
	}
	netLabels["desk.role"] = "net"
	net, err = m.Docker.NetworkCreate(ctx, netName, netLabels, map[string]string{
		networkGatewayModeOption: networkGatewayModeIsolated,
	})
	if err != nil {
		return m.failStart(ctx, user, err)
	}
	proxySpec := spec.Proxy
	proxySpec.Cmd = render.ProxyCmd(spec, net.Subnet)
	proxyID, err := m.Docker.ContainerCreate(ctx, proxySpec)
	if err != nil {
		return m.failStart(ctx, user, err)
	}
	firefoxID, err := m.Docker.ContainerCreate(ctx, spec.Firefox)
	if err != nil {
		return m.failStart(ctx, user, err)
	}
	if err := m.connectSelf(ctx, netName); err != nil {
		return m.failStart(ctx, user, err)
	}
	if err := m.Docker.ContainerStart(ctx, proxyName); err != nil {
		return m.failStart(ctx, user, err)
	}
	// The proxy's address comes from inspecting it after it started, never
	// from an assumed offset on the subnet.
	insp, err := m.Docker.ContainerInspect(ctx, proxyName)
	if err != nil {
		return m.failStart(ctx, user, err)
	}
	addr := insp.IPs[netName]
	if addr == "" {
		return m.failStart(ctx, user, fmt.Errorf("proxy %s has no address on %s", proxyName, netName))
	}
	if err := m.waitHealthy(ctx, "http://"+addr+":8081/healthz"); err != nil {
		return m.failStart(ctx, user, err)
	}
	if err := m.Docker.ContainerStart(ctx, firefoxName); err != nil {
		return m.failStart(ctx, user, err)
	}
	rt := &render.Runtime{
		Subnet:    net.Subnet,
		NetworkID: net.ID,
		ProxyID:   proxyID,
		FirefoxID: firefoxID,
		StartedAt: time.Now(),
	}
	if err := render.WriteRuntime(m.Cfg, user, rt); err != nil {
		return m.failStart(ctx, user, err)
	}
	return Started, nil
}

// failStart runs the stop sequence on a fresh context — the caller's ctx may
// be the very reason the start failed (cancelled or timed out while the
// collector closed the tab), and cleanup must still happen — clears the
// recorded runtime and reports StartFailed with the cause.
func (m *Manager) failStart(ctx context.Context, user string, cause error) (StartResult, error) {
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
	defer cancel()
	if err := m.stop(cleanupCtx, user); err != nil {
		m.logf("start %s failed (%v); cleanup: %v", user, cause, err)
	}
	if err := render.WriteRuntime(m.Cfg, user, nil); err != nil && !errors.Is(err, os.ErrNotExist) {
		m.logf("start %s: clearing runtime: %v", user, err)
	}
	return StartFailed, cause
}

// connectSelf joins the desk's own container to the pair network, tolerating
// an already-connected conflict.
func (m *Manager) connectSelf(ctx context.Context, netName string) error {
	err := m.Docker.NetworkConnect(ctx, netName, m.SelfID)
	if err == nil {
		return nil
	}
	if errors.Is(err, docker.ErrConflict) && strings.Contains(err.Error(), "already") {
		return nil
	}
	return err
}

// checkOwnership refuses objects the desk does not own for this user: the
// error names the object.
func checkOwnership(labels map[string]string, user, object string) error {
	if labels["desk.owner"] != render.Owner || labels["desk.user"] != user {
		return fmt.Errorf("refusing to touch %s: labels %v are not the desk's pair for %s", object, labels, user)
	}
	return nil
}
