package render

import (
	"strconv"

	"github.com/daniellemccool/researchcloud-ddp-comments/internal/config"
	"github.com/daniellemccool/researchcloud-ddp-comments/internal/docker"
)

// Owner is the value of the desk.owner label on every object the desk creates.
const Owner = "desk"

// Generation returns the "<platform>-<zeeschuimer_version>" label value that
// ties a pair to the release it was rendered from.
func Generation(cfg *config.Config) string {
	return cfg.Platform + "-" + cfg.ZeeschuimerVersion
}

// Spec is the rendered pair for one collector seat: the private network name,
// the shared labels, and the two container specs. It round-trips through
// encoding/json (state/users/<user>/pair.json).
type Spec struct {
	User       string
	Index      int
	Generation string
	Network    string            // "net-<user>"
	Labels     map[string]string // desk.owner, desk.user, desk.generation (no role)
	Proxy      docker.ContainerSpec
	Firefox    docker.ContainerSpec
}

// Build renders the pair spec for user at seat index. The proxy Cmd carries no
// --listen-cidr; the session manager appends it via ProxyCmd once it has read
// the subnet back from the created network.
func Build(cfg *config.Config, user string, index int) Spec {
	gen := Generation(cfg)
	labels := map[string]string{
		"desk.owner":      Owner,
		"desk.user":       user,
		"desk.generation": gen,
	}

	proxy := docker.ContainerSpec{
		Name:      "desk-proxy-" + user,
		Image:     cfg.DeskproxyImage,
		Cmd:       proxyCmd(cfg, user, index),
		Labels:    withRole(labels, "proxy"),
		Mounts:    ProxyMounts(cfg, user),
		Endpoints: proxyEndpoints(cfg, user),
		Restart:   "unless-stopped",
	}
	firefox := docker.ContainerSpec{
		Name:      "desk-firefox-" + user,
		Image:     cfg.FirefoxImage,
		Env:       firefoxEnv(),
		Labels:    withRole(labels, "firefox"),
		Mounts:    FirefoxMounts(cfg, user),
		Endpoints: []docker.Endpoint{{Network: "net-" + user}},
		ShmSize:   1 << 30,
		Restart:   "unless-stopped",
	}

	return Spec{
		User:       user,
		Index:      index,
		Generation: gen,
		Network:    "net-" + user,
		Labels:     labels,
		Proxy:      proxy,
		Firefox:    firefox,
	}
}

// ProxyCmd returns a copy of the proxy's Cmd with "--listen-cidr", subnet
// appended; s.Proxy.Cmd is left unchanged.
func ProxyCmd(s Spec, subnet string) []string {
	cmd := make([]string, len(s.Proxy.Cmd)+2)
	copy(cmd, s.Proxy.Cmd)
	cmd[len(s.Proxy.Cmd)] = "--listen-cidr"
	cmd[len(s.Proxy.Cmd)+1] = subnet
	return cmd
}

// withRole copies the shared labels and adds the container's desk.role.
func withRole(labels map[string]string, role string) map[string]string {
	out := make(map[string]string, len(labels)+1)
	for k, v := range labels {
		out[k] = v
	}
	out["desk.role"] = role
	return out
}

// proxyCmd is the deskproxy command line in its exact order.
func proxyCmd(cfg *config.Config, user string, index int) []string {
	return []string{
		"--platform", "/platform/platform.json",
		"--user", user,
		"--session", strconv.Itoa(index),
		"--fqdn", cfg.FQDN,
		"--captures", "/captures",
		"--ca", "/ca",
		"--token-file", "/token/4cat-token",
		"--fourcat-upstream", "http://4cat-frontend:5000",
		"--listen", ":8080",
		"--health", ":8081",
	}
}

// proxyEndpoints: the user's private network (aliased "proxy" so the policy's
// proxy:8080 resolves), the shared egress bridge, and the 4CAT bridge when
// 4CAT is enabled (ADR-0007). The compose project is "desk", so the shared
// networks carry the desk_ prefix.
func proxyEndpoints(cfg *config.Config, user string) []docker.Endpoint {
	eps := []docker.Endpoint{
		{Network: "net-" + user, Aliases: []string{"proxy"}},
		{Network: "desk_desk-egress"},
	}
	if cfg.FourcatEnabled {
		eps = append(eps, docker.Endpoint{Network: "desk_desk-fourcat"})
	}
	return eps
}

// firefoxEnv is the Firefox container's environment in its exact order.
func firefoxEnv() []string {
	return []string{
		"DISPLAY_WIDTH=1280",
		"DISPLAY_HEIGHT=900",
		"WEB_AUDIO=0",
		"KEEP_APP_RUNNING=1",
		"DARK_MODE=0",
		"USER_ID=1000",
		"GROUP_ID=1000",
		"VNC_LISTENING_PORT=-1",
		"FF_OPEN_URL=about:blank",
	}
}
