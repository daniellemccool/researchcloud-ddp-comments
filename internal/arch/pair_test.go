package arch

import (
	"fmt"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/daniellemccool/researchcloud-ddp-comments/internal/config"
	"github.com/daniellemccool/researchcloud-ddp-comments/internal/docker"
	"github.com/daniellemccool/researchcloud-ddp-comments/internal/render"
)

// checkFirefoxMounts enforces ADR-0013/ADR-0009 on a Firefox's mounts: the
// source set is EXACTLY the three expected paths, the CA is the single
// read-only file ca.pem at /proxy-ca/ca.pem, no source is an ancestor of the
// CA key or of the secrets tree, and nothing under state/proxy/ is mounted
// except that exact file.
func checkFirefoxMounts(h, user string, mounts []docker.Mount) error {
	want := []string{
		filepath.Join(h, "state", "firefox", user),
		filepath.Join(h, "state", "proxy", user, "ca.pem"),
		filepath.Join(h, "firefox", user),
	}
	got := make([]string, 0, len(mounts))
	for _, m := range mounts {
		got = append(got, m.Source)
	}
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		return fmt.Errorf("firefox mount sources = %v, want exactly %v", got, want)
	}

	caFile := filepath.Join(h, "state", "proxy", user, "ca.pem")
	for _, m := range mounts {
		if m.Source == caFile && (!m.ReadOnly || m.Target != "/proxy-ca/ca.pem") {
			return fmt.Errorf("CA mount = %v, want read-only target /proxy-ca/ca.pem", m)
		}
	}

	// No source may be an ancestor of the CA key or of the secrets tree
	// (H itself would expose secrets/).
	for _, protected := range []string{
		filepath.Join(h, "state", "proxy", user, "ca-key.pem"),
		filepath.Join(h, "secrets"),
	} {
		for p := protected; ; p = filepath.Dir(p) {
			for _, m := range mounts {
				if m.Source == p {
					return fmt.Errorf("firefox mount source %q is an ancestor of %q", m.Source, protected)
				}
			}
			if p == filepath.Dir(p) {
				break
			}
		}
	}

	// Nothing under state/proxy/ except the exact ca.pem file.
	proxyDir := filepath.Join(h, "state", "proxy") + string(filepath.Separator)
	for _, m := range mounts {
		if strings.HasPrefix(m.Source, proxyDir) && m.Source != caFile {
			return fmt.Errorf("firefox mount source under state/proxy/ other than ca.pem: %q", m.Source)
		}
	}
	return nil
}

// checkTokenMounts enforces ADR-0009 on a proxy's mounts: exactly one mount
// under H/secrets/ when 4CAT is enabled — the owner's token directory, read-only,
// at /token — and none otherwise; never the secrets tree itself and never
// another user's token directory.
func checkTokenMounts(h, user string, fourcat bool, mounts []docker.Mount) error {
	secrets := filepath.Join(h, "secrets")
	tokens := filepath.Join(secrets, "4cat-tokens")

	var under []docker.Mount
	for _, m := range mounts {
		if m.Source == secrets || strings.HasPrefix(m.Source, secrets+string(filepath.Separator)) {
			under = append(under, m)
		}
		if m.Source == secrets || m.Source == tokens {
			return fmt.Errorf("proxy mounts the secrets tree itself: %q", m.Source)
		}
		if rest, ok := strings.CutPrefix(m.Source, tokens+string(filepath.Separator)); ok && rest != user {
			return fmt.Errorf("proxy mounts another user's token directory: %q", m.Source)
		}
	}

	if !fourcat {
		if len(under) != 0 {
			return fmt.Errorf("FourcatEnabled=false but %d mounts under secrets/: %v", len(under), under)
		}
		return nil
	}
	if len(under) != 1 {
		return fmt.Errorf("want exactly 1 mount under secrets/, got %d: %v", len(under), under)
	}
	tm := under[0]
	if want := filepath.Join(tokens, user); tm.Source != want {
		return fmt.Errorf("token mount source = %q, want %q", tm.Source, want)
	}
	if !tm.ReadOnly || tm.Target != "/token" {
		return fmt.Errorf("token mount = %v, want read-only target /token", tm)
	}
	return nil
}

// TestPairSpecInvariants walks a rendered pair spec across the FourcatEnabled
// x HideMedia matrix and fails on any broken rule from ADR-0006, ADR-0007,
// ADR-0009, ADR-0013 and ADR-0021.
func TestPairSpecInvariants(t *testing.T) {
	for _, fourcat := range []bool{true, false} {
		for _, hideMedia := range []bool{true, false} {
			name := "fourcat=" + map[bool]string{true: "on", false: "off"}[fourcat] +
				"/hideMedia=" + map[bool]string{true: "on", false: "off"}[hideMedia]
			t.Run(name, func(t *testing.T) {
				cfg := &config.Config{
					DeskRoot:           t.TempDir(),
					HostDeskRoot:       t.TempDir(),
					FQDN:               "desk.example.org",
					Platform:           "tiktok",
					HideMedia:          hideMedia,
					FourcatEnabled:     fourcat,
					DeskproxyImage:     "img/proxy:1",
					FirefoxImage:       "img/firefox:1",
					ZeeschuimerVersion: "1.14.3",
				}
				s := render.Build(cfg, "alice", 1)
				h := cfg.HostDeskRoot

				for role, spec := range map[string]docker.ContainerSpec{"proxy": s.Proxy, "firefox": s.Firefox} {
					if spec.Restart != "unless-stopped" {
						t.Errorf("%s Restart = %q, want unless-stopped", role, spec.Restart)
					}
					if spec.User != "" {
						t.Errorf("%s User = %q, want empty", role, spec.User)
					}
					wantLabels := map[string]string{
						"desk.owner":      "desk",
						"desk.user":       "alice",
						"desk.generation": "tiktok-1.14.3",
						"desk.role":       role,
					}
					for k, v := range wantLabels {
						if spec.Labels[k] != v {
							t.Errorf("%s label %s = %q, want %q", role, k, spec.Labels[k], v)
						}
					}
					for k := range spec.Labels {
						if strings.HasPrefix(k, "com.docker.compose.") {
							t.Errorf("%s has compose label %q (ADR-0021)", role, k)
						}
					}
				}

				// ADR-0006: the spec type has no port or network-mode field at all.
				specType := reflect.TypeOf(docker.ContainerSpec{})
				for i := 0; i < specType.NumField(); i++ {
					f := specType.Field(i).Name
					if strings.Contains(f, "Port") {
						t.Errorf("ContainerSpec has field %q containing Port", f)
					}
					if f == "NetworkMode" {
						t.Error("ContainerSpec has a NetworkMode field")
					}
				}

				// ADR-0007: the Firefox is on net-alice only.
				wantFF := []docker.Endpoint{{Network: "net-alice"}}
				if !reflect.DeepEqual(s.Firefox.Endpoints, wantFF) {
					t.Errorf("Firefox.Endpoints = %v, want %v", s.Firefox.Endpoints, wantFF)
				}

				// ADR-0009/ADR-0013: mount rules.
				caDir := filepath.Join(h, "state", "proxy", "alice")
				for _, m := range append(append([]docker.Mount{}, s.Proxy.Mounts...), s.Firefox.Mounts...) {
					switch m.Target {
					case "/platform", "/usr/lib/firefox/distribution", "/proxy-ca/ca.pem", "/token":
						if !m.ReadOnly {
							t.Errorf("mount %v with target %s is not read-only", m, m.Target)
						}
					}
					if strings.HasPrefix(m.Source, "/rsc/plugins/") {
						t.Errorf("mount source under /rsc/plugins/ (ADR-0013): %v", m)
					}
				}
				if err := checkFirefoxMounts(h, "alice", s.Firefox.Mounts); err != nil {
					t.Error(err)
				}
				if err := checkTokenMounts(h, "alice", fourcat, s.Proxy.Mounts); err != nil {
					t.Error(err)
				}
				// Kept as an explicit guard: the Firefox must never mount the
				// proxy CA directory itself.
				for _, m := range s.Firefox.Mounts {
					if m.Source == caDir {
						t.Errorf("Firefox mounts the proxy CA directory itself: %v", m)
					}
				}
			})
		}
	}
}

// TestPairSpecInvariantsNegative proves the mount checks bite: each mutated
// spec must make the corresponding helper return an error, and the unmutated
// spec must pass both.
func TestPairSpecInvariantsNegative(t *testing.T) {
	cfg := &config.Config{
		DeskRoot:           t.TempDir(),
		HostDeskRoot:       t.TempDir(),
		FQDN:               "desk.example.org",
		Platform:           "tiktok",
		FourcatEnabled:     true,
		DeskproxyImage:     "img/proxy:1",
		FirefoxImage:       "img/firefox:1",
		ZeeschuimerVersion: "1.14.3",
	}
	s := render.Build(cfg, "alice", 1)
	h := cfg.HostDeskRoot

	if err := checkFirefoxMounts(h, "alice", s.Firefox.Mounts); err != nil {
		t.Fatalf("control: unmutated firefox mounts rejected: %v", err)
	}
	if err := checkTokenMounts(h, "alice", true, s.Proxy.Mounts); err != nil {
		t.Fatalf("control: unmutated proxy mounts rejected: %v", err)
	}

	bogusFirefox := map[string]docker.Mount{
		"ca-key.pem":   {Source: filepath.Join(h, "state", "proxy", "alice", "ca-key.pem"), Target: "/key"},
		"proxy CA dir": {Source: filepath.Join(h, "state", "proxy", "alice"), Target: "/ca"},
		"state dir":    {Source: filepath.Join(h, "state"), Target: "/state"},
		"desk root":    {Source: h, Target: "/host"},
	}
	for name, bogus := range bogusFirefox {
		t.Run("firefox rejects "+name, func(t *testing.T) {
			mounts := append(slices.Clone(s.Firefox.Mounts), bogus)
			if err := checkFirefoxMounts(h, "alice", mounts); err == nil {
				t.Errorf("checkFirefoxMounts accepted a %s mount: %v", name, mounts)
			}
		})
	}

	bogusProxy := map[string]docker.Mount{
		"other user's token dir": {Source: filepath.Join(h, "secrets", "4cat-tokens", "bob"), Target: "/other", ReadOnly: true},
		"secrets tree":           {Source: filepath.Join(h, "secrets"), Target: "/sec", ReadOnly: true},
		"tokens dir":             {Source: filepath.Join(h, "secrets", "4cat-tokens"), Target: "/tok", ReadOnly: true},
	}
	for name, bogus := range bogusProxy {
		t.Run("proxy rejects "+name, func(t *testing.T) {
			mounts := append(slices.Clone(s.Proxy.Mounts), bogus)
			if err := checkTokenMounts(h, "alice", true, mounts); err == nil {
				t.Errorf("checkTokenMounts accepted %s: %v", name, mounts)
			}
		})
	}

	t.Run("proxy rejects token mount when fourcat disabled", func(t *testing.T) {
		if err := checkTokenMounts(h, "alice", false, s.Proxy.Mounts); err == nil {
			t.Error("checkTokenMounts accepted a token mount with FourcatEnabled=false")
		}
	})
}

// TestPairContainersHaveMemoryLimits enforces ADR-0023: both pair containers
// carry a hard memory limit and an equal memory+swap limit (no swap).
func TestPairContainersHaveMemoryLimits(t *testing.T) {
	cfg := &config.Config{
		DeskRoot:           t.TempDir(),
		HostDeskRoot:       t.TempDir(),
		FQDN:               "desk.example.org",
		Platform:           "tiktok",
		DeskproxyImage:     "img/proxy:1",
		FirefoxImage:       "img/firefox:1",
		ZeeschuimerVersion: "1.14.3",
	}
	s := render.Build(cfg, "alice", 1)
	for role, c := range map[string]struct {
		spec docker.ContainerSpec
		want int64
	}{
		"firefox": {s.Firefox, config.FirefoxMemory},
		"proxy":   {s.Proxy, config.ProxyMemory},
	} {
		if c.spec.Memory != c.want {
			t.Errorf("%s Memory = %d, want %d", role, c.spec.Memory, c.want)
		}
		if c.spec.MemorySwap != c.spec.Memory {
			t.Errorf("%s MemorySwap = %d, want equal to Memory %d (no swap)", role, c.spec.MemorySwap, c.spec.Memory)
		}
	}
}
