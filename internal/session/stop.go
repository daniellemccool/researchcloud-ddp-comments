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
// runtime. Every step tolerates ErrNotFound (and the disconnect a
// not-connected error), so a second Stop is a no-op returning nil. The
// ownership check runs before anything is touched: a foreign object is an
// error naming it and nothing is removed.
func (m *Manager) Stop(ctx context.Context, user string) error {
	userLock := m.Locks.User(user)
	userLock.Lock()
	defer userLock.Unlock()
	return m.stop(ctx, user)
}

// stop is Stop without the user lock: the caller must hold Locks.User(user).
func (m *Manager) stop(ctx context.Context, user string) error {
	netName := "net-" + user
	proxyName := "desk-proxy-" + user
	firefoxName := "desk-firefox-" + user

	// Ownership check first: inspect, and refuse foreign objects before
	// touching anything.
	if n, err := m.Docker.NetworkInspect(ctx, netName); err == nil {
		if err := checkOwnership(n.Labels, user, netName); err != nil {
			return err
		}
	} else if !errors.Is(err, docker.ErrNotFound) {
		return err
	}
	if c, err := m.Docker.ContainerInspect(ctx, proxyName); err == nil {
		if err := checkOwnership(c.Labels, user, proxyName); err != nil {
			return err
		}
	} else if !errors.Is(err, docker.ErrNotFound) {
		return err
	}
	if c, err := m.Docker.ContainerInspect(ctx, firefoxName); err == nil {
		if err := checkOwnership(c.Labels, user, firefoxName); err != nil {
			return err
		}
	} else if !errors.Is(err, docker.ErrNotFound) {
		return err
	}

	if err := m.Docker.ContainerStop(ctx, firefoxName, stopTimeout); err != nil && !tolerateStop(err) {
		return fmt.Errorf("stop %s: %w", firefoxName, err)
	}
	if err := m.Docker.ContainerStop(ctx, proxyName, stopTimeout); err != nil && !tolerateStop(err) {
		return fmt.Errorf("stop %s: %w", proxyName, err)
	}
	if err := m.Docker.ContainerRemove(ctx, firefoxName); err != nil && !tolerateStop(err) {
		return fmt.Errorf("remove %s: %w", firefoxName, err)
	}
	if err := m.Docker.ContainerRemove(ctx, proxyName); err != nil && !tolerateStop(err) {
		return fmt.Errorf("remove %s: %w", proxyName, err)
	}
	if err := m.Docker.NetworkDisconnect(ctx, netName, m.SelfID); err != nil && !tolerateStop(err) {
		return fmt.Errorf("disconnect %s from %s: %w", m.SelfID, netName, err)
	}
	if err := m.Docker.NetworkRemove(ctx, netName); err != nil && !tolerateStop(err) {
		return fmt.Errorf("remove %s: %w", netName, err)
	}
	if err := render.WriteRuntime(m.Cfg, user, nil); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("clear runtime for %s: %w", user, err)
	}
	return nil
}

// tolerateStop reports whether a stop-sequence error is expected and harmless:
// the object is already gone, or the desk was not connected to the network.
func tolerateStop(err error) bool {
	if errors.Is(err, docker.ErrNotFound) {
		return true
	}
	msg := err.Error()
	return strings.Contains(msg, "not attached") || strings.Contains(msg, "not connected")
}
