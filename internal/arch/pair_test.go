package arch

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/daniellemccool/researchcloud-ddp-comments/internal/config"
	"github.com/daniellemccool/researchcloud-ddp-comments/internal/docker"
	"github.com/daniellemccool/researchcloud-ddp-comments/internal/render"
)

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
				caDir := filepath.Join(cfg.HostDeskRoot, "state", "proxy", "alice")
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
				var token *docker.Mount
				for i, m := range s.Proxy.Mounts {
					if m.Target == "/token" {
						token = &s.Proxy.Mounts[i]
					}
				}
				if fourcat {
					if token == nil {
						t.Fatal("FourcatEnabled but no /token mount on the proxy")
					}
					if !strings.HasSuffix(token.Source, "/secrets/4cat-tokens/alice") {
						t.Errorf("token mount source = %q, want it to end in /secrets/4cat-tokens/alice", token.Source)
					}
					if filepath.Base(token.Source) != "alice" {
						t.Errorf("token mount source %q names a file, want the owner directory", token.Source)
					}
				} else if token != nil {
					t.Errorf("FourcatEnabled=false but /token mount present: %v", *token)
				}
				for _, m := range s.Firefox.Mounts {
					if strings.Contains(m.Source, "/secrets/") {
						t.Errorf("Firefox mount source under secrets: %v", m)
					}
					if m.Source == caDir {
						t.Errorf("Firefox mounts the proxy CA directory itself: %v", m)
					}
				}
			})
		}
	}
}
