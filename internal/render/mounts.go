package render

import (
	"path/filepath"

	"github.com/daniellemccool/researchcloud-ddp-comments/internal/config"
	"github.com/daniellemccool/researchcloud-ddp-comments/internal/docker"
)

// ProxyMounts returns the proxy's bind mounts. Every source is under the host
// desk root (cfg.HostDeskRoot). The 4CAT token mount is the owner's token
// DIRECTORY, read-only, and only when 4CAT is enabled (ADR-0009).
func ProxyMounts(cfg *config.Config, user string) []docker.Mount {
	h := cfg.HostDeskRoot
	mounts := []docker.Mount{
		{Source: filepath.Join(h, "platform"), Target: "/platform", ReadOnly: true},
		{Source: filepath.Join(h, "state", "proxy", user), Target: "/ca"},
		{Source: filepath.Join(h, "state", "captures", user), Target: "/captures"},
	}
	if cfg.FourcatEnabled {
		mounts = append(mounts, docker.Mount{
			Source:   filepath.Join(h, "secrets", "4cat-tokens", user),
			Target:   "/token",
			ReadOnly: true,
		})
	}
	return mounts
}

// FirefoxMounts returns the Firefox's bind mounts. The proxy CA is mounted as
// the single file ca.pem, never the directory holding the key, and nothing
// under secrets/ is ever reachable from the browser (ADR-0009, ADR-0013).
func FirefoxMounts(cfg *config.Config, user string) []docker.Mount {
	h := cfg.HostDeskRoot
	return []docker.Mount{
		{Source: filepath.Join(h, "state", "firefox", user), Target: "/config"},
		{Source: filepath.Join(h, "state", "proxy", user, "ca.pem"), Target: "/proxy-ca/ca.pem", ReadOnly: true},
		{Source: filepath.Join(h, "firefox", user), Target: "/usr/lib/firefox/distribution", ReadOnly: true},
	}
}
