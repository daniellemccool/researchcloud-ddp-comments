package session

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/daniellemccool/researchcloud-ddp-comments/internal/docker"
	"github.com/daniellemccool/researchcloud-ddp-comments/internal/render"
)

// Stop removes user's pair: it stops and removes both containers, disconnects
// the desk from the network, removes the network and clears the recorded
// runtime. The ownership check runs before anything is touched: a foreign
// object is an error naming it and nothing is removed. When none of the three
// objects exists, Stop records only the inspects and returns nil, so a second
// Stop is a no-op. Every mutation step tolerates ErrNotFound; the disconnect
// additionally tolerates a not-connected error.
func (m *Manager) Stop(ctx context.Context, user string) error {
	userLock := m.Locks.User(user)
	userLock.Lock()
	defer userLock.Unlock()
	return m.stop(ctx, user)
}

// StopIf takes user's lock, evaluates cond under it and stops the pair only
// when cond holds; a false cond makes no Docker call. stopped reports whether
// cond held and the stop ran; err is the stop's error. The reaper uses it to
// re-check idleness after taking the lock, so a page load that starts or
// finds the pair between its selection and the stop wins.
func (m *Manager) StopIf(ctx context.Context, user string, cond func() bool) (stopped bool, err error) {
	userLock := m.Locks.User(user)
	userLock.Lock()
	defer userLock.Unlock()
	if !cond() {
		return false, nil
	}
	return true, m.stop(ctx, user)
}

// stop is Stop without the user lock: the caller must hold Locks.User(user).
func (m *Manager) stop(ctx context.Context, user string) error {
	netName := "net-" + user
	proxyName := "desk-proxy-" + user
	firefoxName := "desk-firefox-" + user

	// Ownership check first: inspect, and refuse foreign objects before
	// touching anything.
	net, netErr := m.Docker.NetworkInspect(ctx, netName)
	proxy, proxyErr := m.Docker.ContainerInspect(ctx, proxyName)
	firefox, firefoxErr := m.Docker.ContainerInspect(ctx, firefoxName)
	if netErr == nil {
		if err := checkOwnership(net.Labels, user, netName); err != nil {
			return err
		}
	} else if !errors.Is(netErr, docker.ErrNotFound) {
		return netErr
	}
	if proxyErr == nil {
		if err := checkOwnership(proxy.Labels, user, proxyName); err != nil {
			return err
		}
	} else if !errors.Is(proxyErr, docker.ErrNotFound) {
		return proxyErr
	}
	if firefoxErr == nil {
		if err := checkOwnership(firefox.Labels, user, firefoxName); err != nil {
			return err
		}
	} else if !errors.Is(firefoxErr, docker.ErrNotFound) {
		return firefoxErr
	}

	// Nothing exists: a second Stop is a no-op (inspects only).
	if netErr == nil || proxyErr == nil || firefoxErr == nil {
		if err := m.Docker.ContainerStop(ctx, firefoxName, stopTimeout); err != nil && !errors.Is(err, docker.ErrNotFound) {
			return fmt.Errorf("stop %s: %w", firefoxName, err)
		}
		if err := m.Docker.ContainerStop(ctx, proxyName, stopTimeout); err != nil && !errors.Is(err, docker.ErrNotFound) {
			return fmt.Errorf("stop %s: %w", proxyName, err)
		}
		if err := m.Docker.ContainerRemove(ctx, firefoxName); err != nil && !errors.Is(err, docker.ErrNotFound) {
			return fmt.Errorf("remove %s: %w", firefoxName, err)
		}
		if err := m.Docker.ContainerRemove(ctx, proxyName); err != nil && !errors.Is(err, docker.ErrNotFound) {
			return fmt.Errorf("remove %s: %w", proxyName, err)
		}
		if err := m.Docker.NetworkDisconnect(ctx, netName, m.SelfID); err != nil && !tolerateDisconnect(err) {
			return fmt.Errorf("disconnect %s from %s: %w", m.SelfID, netName, err)
		}
		if err := m.Docker.NetworkRemove(ctx, netName); err != nil && !errors.Is(err, docker.ErrNotFound) {
			return fmt.Errorf("remove %s: %w", netName, err)
		}
	}
	if err := render.WriteRuntime(m.Cfg, user, nil); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("clear runtime for %s: %w", user, err)
	}
	return nil
}

// tolerateDisconnect reports whether a disconnect error is harmless: the
// network or container is gone, or the desk was not connected to the network.
// Only the disconnect step tolerates these; every other step tolerates
// ErrNotFound only.
func tolerateDisconnect(err error) bool {
	if errors.Is(err, docker.ErrNotFound) {
		return true
	}
	msg := err.Error()
	return strings.Contains(msg, "not attached") || strings.Contains(msg, "not connected")
}
