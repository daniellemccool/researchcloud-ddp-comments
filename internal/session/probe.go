package session

import (
	"context"
	"fmt"
	"net/http"
	"time"
)

// defaultHealthWait is the proxy health wait when HealthWait is 0.
const defaultHealthWait = 10 * time.Second

// probePollInterval is how often the proxy health endpoint is polled.
const probePollInterval = 250 * time.Millisecond

// defaultProbeClient answers the default Probe. Its transport has Proxy nil,
// so the environment's HTTP_PROXY is never used; the timeout is 2 s.
var defaultProbeClient = &http.Client{
	Transport: &http.Transport{Proxy: nil},
	Timeout:   2 * time.Second,
}

// probe reports whether url answers. A nil Probe field means a GET with a
// 2 s timeout, true on 200.
func (m *Manager) probe(ctx context.Context, url string) bool {
	if m.Probe != nil {
		return m.Probe(ctx, url)
	}
	if err := ctx.Err(); err != nil {
		return false
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return false
	}
	resp, err := defaultProbeClient.Do(req)
	if err != nil {
		return false
	}
	defer func() { _ = resp.Body.Close() }()
	return resp.StatusCode == http.StatusOK
}

// healthWait returns the configured proxy health wait, defaulting to 10 s.
func (m *Manager) healthWait() time.Duration {
	if m.HealthWait > 0 {
		return m.HealthWait
	}
	return defaultHealthWait
}

// waitHealthy polls url every 250 ms until it answers or the health wait
// expires.
func (m *Manager) waitHealthy(ctx context.Context, url string) error {
	deadline := time.Now().Add(m.healthWait())
	for {
		if m.probe(ctx, url) {
			return nil
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return fmt.Errorf("proxy health at %s not ready within %s", url, m.healthWait())
		}
		if remaining > probePollInterval {
			remaining = probePollInterval
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(remaining):
		}
	}
}
