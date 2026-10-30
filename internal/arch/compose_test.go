package arch

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// composeFile is the part of a resolved `docker compose config` output that
// ADR-0006 and ADR-0007 constrain.
type composeFile struct {
	Services map[string]struct {
		Ports       []any  `yaml:"ports"`
		NetworkMode string `yaml:"network_mode"`
		Restart     string `yaml:"restart"`
	} `yaml:"services"`
	Networks map[string]struct {
		Internal bool `yaml:"internal"`
	} `yaml:"networks"`
}

// portString renders a resolved port entry (long form from `config`, or the
// short string form) as host_ip:published:target/protocol.
func portString(p any) string {
	m, ok := p.(map[string]any)
	if !ok {
		return fmt.Sprint(p)
	}
	return fmt.Sprintf("%v:%v:%v/%v", m["host_ip"], m["published"], m["target"], m["protocol"])
}

// checkCompose applies the compose rules to a resolved file: every port
// published is exactly an entry of wantPorts, owned by that service, no 0.0.0.0
// binding, no network_mode, and every service restarts unless stopped. Plan 2
// runs rendered per-user files through it with no wanted ports.
func checkCompose(t *testing.T, path string, wantPorts map[string][]string) composeFile {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var c composeFile
	if err := yaml.Unmarshal(raw, &c); err != nil {
		t.Fatal(err)
	}
	if len(c.Services) == 0 {
		t.Fatal("no services in resolved file")
	}
	var got, want []string
	for svc, ps := range wantPorts {
		for _, p := range ps {
			want = append(want, svc+" "+p)
		}
	}
	for name, s := range c.Services {
		t.Run(name, func(t *testing.T) {
			if s.NetworkMode != "" {
				t.Errorf("network_mode %q", s.NetworkMode)
			}
			if s.Restart != "unless-stopped" {
				t.Errorf("restart %q, want unless-stopped", s.Restart)
			}
			for _, p := range s.Ports {
				ps := portString(p)
				got = append(got, name+" "+ps)
				if strings.HasPrefix(ps, "0.0.0.0:") || strings.HasPrefix(ps, "<nil>:") {
					t.Errorf("port %q binds all interfaces", ps)
				}
			}
		})
	}
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Errorf("published ports (service entry) %v, want exactly %v", got, want)
	}
	return c
}

func TestCompose(t *testing.T) {
	path := filepath.Join("..", "..", "deploy", ".resolved.yml")
	if _, err := os.Stat(path); err != nil {
		t.Skip("deploy/.resolved.yml absent: run scripts/check.sh compose")
	}
	c := checkCompose(t, path, map[string][]string{"desk": {"127.0.0.1:8110:8110/tcp"}})
	if !c.Networks["4cat-internal"].Internal {
		t.Error("network 4cat-internal is not internal: true")
	}
}
