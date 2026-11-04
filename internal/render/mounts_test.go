package render

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/daniellemccool/researchcloud-ddp-comments/internal/docker"
)

func TestExactlyOneTokenMountOfTheOwner(t *testing.T) {
	cfg := testConfig(t)
	cfg.FourcatEnabled = true
	h := cfg.HostDeskRoot

	tokenSource := func(user string) string {
		var got []docker.Mount
		for _, m := range ProxyMounts(cfg, user) {
			if strings.Contains(m.Source, "secrets/4cat-tokens") {
				got = append(got, m)
			}
		}
		if len(got) != 1 {
			t.Fatalf("%s: %d token mounts, want exactly 1: %v", user, len(got), got)
		}
		return got[0].Source
	}

	alice := tokenSource("alice")
	bob := tokenSource("bob")

	if want := filepath.Join(h, "secrets", "4cat-tokens", "alice"); alice != want {
		t.Errorf("alice token source = %q, want %q", alice, want)
	}
	if want := filepath.Join(h, "secrets", "4cat-tokens", "bob"); bob != want {
		t.Errorf("bob token source = %q, want %q", bob, want)
	}
	if alice == bob {
		t.Error("alice and bob share a token source")
	}
	// A directory path with no file name: the base is the user, not 4cat-token.
	if filepath.Base(alice) != "alice" {
		t.Errorf("token source %q looks like a file mount (base %q)", alice, filepath.Base(alice))
	}
	for _, m := range ProxyMounts(cfg, "alice") {
		if strings.Contains(m.Source, "secrets/4cat-tokens") && !m.ReadOnly {
			t.Errorf("token mount %v is not read-only", m)
		}
	}
}

// checkFirefoxMountSources enforces ADR-0013/ADR-0009 on a Firefox's mounts:
// the source set is EXACTLY the three expected paths, the CA is the single
// read-only file ca.pem at /proxy-ca/ca.pem, no source is an ancestor of the
// CA key or of the secrets tree, and nothing under state/proxy/ is mounted
// except that exact file.
func checkFirefoxMountSources(h, user string, mounts []docker.Mount) error {
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

func TestFirefoxCannotMountSecrets(t *testing.T) {
	cfg := testConfig(t)
	h := cfg.HostDeskRoot
	u := "alice"

	mounts := FirefoxMounts(cfg, u)
	if err := checkFirefoxMountSources(h, u, mounts); err != nil {
		t.Fatal(err)
	}

	// Explicit guards kept from the original test.
	for _, m := range mounts {
		if strings.HasPrefix(m.Source, filepath.Join(h, "secrets")) {
			t.Errorf("Firefox mount source under secrets: %v", m)
		}
		if m.Source == filepath.Join(h, "state", "proxy", u) {
			t.Errorf("Firefox mounts the proxy CA directory itself: %v", m)
		}
		if m.Target == "/platform" {
			t.Errorf("Firefox has a /platform mount: %v", m)
		}
	}
	var ca *docker.Mount
	for i, m := range mounts {
		if m.Target == "/proxy-ca/ca.pem" {
			ca = &mounts[i]
		}
	}
	if ca == nil {
		t.Fatalf("no /proxy-ca/ca.pem mount in %v", mounts)
	}
	if want := filepath.Join(h, "state", "proxy", u, "ca.pem"); ca.Source != want {
		t.Errorf("CA mount source = %q, want %q", ca.Source, want)
	}
	if !ca.ReadOnly {
		t.Errorf("CA mount is not read-only: %v", *ca)
	}

	// The check must bite: each bogus mount has to be rejected.
	bogus := map[string]docker.Mount{
		"ca-key.pem":   {Source: filepath.Join(h, "state", "proxy", u, "ca-key.pem"), Target: "/key"},
		"proxy CA dir": {Source: filepath.Join(h, "state", "proxy", u), Target: "/ca"},
		"state dir":    {Source: filepath.Join(h, "state"), Target: "/state"},
		"desk root":    {Source: h, Target: "/host"},
	}
	for name, b := range bogus {
		t.Run("rejects "+name, func(t *testing.T) {
			m := append(slices.Clone(mounts), b)
			if err := checkFirefoxMountSources(h, u, m); err == nil {
				t.Errorf("checkFirefoxMountSources accepted a %s mount: %v", name, m)
			}
		})
	}
}
