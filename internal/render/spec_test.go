package render

import (
	"reflect"
	"slices"
	"testing"

	"github.com/daniellemccool/researchcloud-ddp-comments/internal/config"
	"github.com/daniellemccool/researchcloud-ddp-comments/internal/docker"
)

// testConfig returns the fields render needs, with temp roots.
func testConfig(t *testing.T) *config.Config {
	t.Helper()
	return &config.Config{
		DeskRoot:           t.TempDir(),
		HostDeskRoot:       t.TempDir(),
		FQDN:               "desk.example.org",
		Platform:           "tiktok",
		ZeeschuimerVersion: "1.14.3",
		FourcatEnabled:     true,
		DeskproxyImage:     "registry.example.org/deskproxy:1.2.3",
		FirefoxImage:       "registry.example.org/firefox:4.5.6",
	}
}

func TestBuildNames(t *testing.T) {
	cfg := testConfig(t)
	s := Build(cfg, "alice", 3)

	if s.User != "alice" || s.Index != 3 {
		t.Errorf("User/Index = %q/%d, want alice/3", s.User, s.Index)
	}
	if s.Network != "net-alice" {
		t.Errorf("Network = %q, want net-alice", s.Network)
	}
	if s.Generation != "tiktok-1.14.3" {
		t.Errorf("Generation = %q, want tiktok-1.14.3", s.Generation)
	}
	if Generation(cfg) != "tiktok-1.14.3" {
		t.Errorf("Generation(cfg) = %q, want tiktok-1.14.3", Generation(cfg))
	}
	if Owner != "desk" {
		t.Errorf("Owner = %q, want desk", Owner)
	}

	if s.Proxy.Name != "desk-proxy-alice" {
		t.Errorf("Proxy.Name = %q, want desk-proxy-alice", s.Proxy.Name)
	}
	if s.Firefox.Name != "desk-firefox-alice" {
		t.Errorf("Firefox.Name = %q, want desk-firefox-alice", s.Firefox.Name)
	}
	if s.Proxy.Image != cfg.DeskproxyImage {
		t.Errorf("Proxy.Image = %q, want %q", s.Proxy.Image, cfg.DeskproxyImage)
	}
	if s.Firefox.Image != cfg.FirefoxImage {
		t.Errorf("Firefox.Image = %q, want %q", s.Firefox.Image, cfg.FirefoxImage)
	}

	// Spec.Labels carries the shared labels and no role.
	wantLabels := map[string]string{
		"desk.owner":      "desk",
		"desk.user":       "alice",
		"desk.generation": "tiktok-1.14.3",
	}
	if !reflect.DeepEqual(s.Labels, wantLabels) {
		t.Errorf("Spec.Labels = %v, want %v", s.Labels, wantLabels)
	}

	// Every label on both containers, with the role differing.
	for name, spec := range map[string]docker.ContainerSpec{"proxy": s.Proxy, "firefox": s.Firefox} {
		want := map[string]string{
			"desk.owner":      "desk",
			"desk.user":       "alice",
			"desk.generation": "tiktok-1.14.3",
			"desk.role":       name,
		}
		if !reflect.DeepEqual(spec.Labels, want) {
			t.Errorf("%s labels = %v, want %v", name, spec.Labels, want)
		}
	}
	if s.Proxy.Labels["desk.role"] == s.Firefox.Labels["desk.role"] {
		t.Error("role labels do not differ between proxy and firefox")
	}
}

func TestBuildProxyCmdAndListenCidr(t *testing.T) {
	cfg := testConfig(t)
	s := Build(cfg, "alice", 3)

	want := []string{
		"--platform", "/platform/platform.json",
		"--user", "alice",
		"--session", "3",
		"--fqdn", "desk.example.org",
		"--captures", "/captures",
		"--ca", "/ca",
		"--token-file", "/token/4cat-token",
		"--fourcat-upstream", "http://4cat-frontend:5000",
		"--listen", ":8080",
		"--health", ":8081",
	}
	if !reflect.DeepEqual(s.Proxy.Cmd, want) {
		t.Errorf("Proxy.Cmd = %v, want %v", s.Proxy.Cmd, want)
	}

	before := slices.Clone(s.Proxy.Cmd)
	cmd := ProxyCmd(s, "10.200.1.0/24")
	if len(cmd) != len(before)+2 || cmd[len(before)] != "--listen-cidr" || cmd[len(before)+1] != "10.200.1.0/24" {
		t.Errorf("ProxyCmd tail = %v, want [--listen-cidr 10.200.1.0/24]", cmd[len(before):])
	}
	if !slices.Equal(s.Proxy.Cmd, before) {
		t.Errorf("ProxyCmd modified s.Proxy.Cmd: %v, want %v", s.Proxy.Cmd, before)
	}
	// The returned slice must not alias s.Proxy.Cmd: appending to it leaves
	// the original untouched.
	_ = append(cmd, "extra")
	if !slices.Equal(s.Proxy.Cmd, before) {
		t.Errorf("ProxyCmd result aliases s.Proxy.Cmd: after append, s.Proxy.Cmd = %v, want %v", s.Proxy.Cmd, before)
	}
}

func TestFirefoxOnlyOnUserNetwork(t *testing.T) {
	cfg := testConfig(t)
	s := Build(cfg, "alice", 1)

	wantFF := []docker.Endpoint{{Network: "net-alice"}}
	if !reflect.DeepEqual(s.Firefox.Endpoints, wantFF) {
		t.Errorf("Firefox.Endpoints = %v, want %v", s.Firefox.Endpoints, wantFF)
	}

	wantProxy := []docker.Endpoint{
		{Network: "net-alice", Aliases: []string{"proxy"}},
		{Network: "desk_desk-egress"},
		{Network: "desk_desk-fourcat"},
	}
	if !reflect.DeepEqual(s.Proxy.Endpoints, wantProxy) {
		t.Errorf("Proxy.Endpoints = %v, want %v", s.Proxy.Endpoints, wantProxy)
	}
}

func TestFourcatDisabledDropsFourcatNetworkAndTokenMount(t *testing.T) {
	cfg := testConfig(t)
	cfg.FourcatEnabled = false
	s := Build(cfg, "alice", 1)

	for _, ep := range s.Proxy.Endpoints {
		if ep.Network == "desk_desk-fourcat" {
			t.Errorf("Proxy.Endpoints still has desk_desk-fourcat: %v", s.Proxy.Endpoints)
		}
	}
	for _, m := range s.Proxy.Mounts {
		if m.Target == "/token" {
			t.Errorf("Proxy.Mounts still has /token: %v", s.Proxy.Mounts)
		}
	}
	// The egress bridge stays.
	found := false
	for _, ep := range s.Proxy.Endpoints {
		if ep.Network == "desk_desk-egress" {
			found = true
		}
	}
	if !found {
		t.Errorf("Proxy.Endpoints lost desk_desk-egress: %v", s.Proxy.Endpoints)
	}
}

func TestFirefoxEnvAndShm(t *testing.T) {
	cfg := testConfig(t)
	s := Build(cfg, "alice", 1)

	want := []string{
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
	if !reflect.DeepEqual(s.Firefox.Env, want) {
		t.Errorf("Firefox.Env = %v, want %v", s.Firefox.Env, want)
	}
	if s.Firefox.ShmSize != 1<<30 {
		t.Errorf("Firefox.ShmSize = %d, want %d", s.Firefox.ShmSize, int64(1<<30))
	}
	if s.Proxy.ShmSize != 0 {
		t.Errorf("Proxy.ShmSize = %d, want 0", s.Proxy.ShmSize)
	}
}
