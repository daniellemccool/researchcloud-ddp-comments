package render

import (
	"path/filepath"
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

func TestFirefoxCannotMountSecrets(t *testing.T) {
	cfg := testConfig(t)
	h := cfg.HostDeskRoot
	u := "alice"

	mounts := FirefoxMounts(cfg, u)
	for _, m := range mounts {
		if strings.Contains(m.Source, filepath.Join(h, "secrets")) || strings.HasPrefix(m.Source, filepath.Join(h, "secrets")) {
			t.Errorf("Firefox mount source under secrets: %v", m)
		}
		if m.Source == filepath.Join(h, "state", "proxy", u) {
			t.Errorf("Firefox mounts the proxy CA directory itself: %v", m)
		}
		if m.Target == "/platform" {
			t.Errorf("Firefox has a /platform mount: %v", m)
		}
	}

	// The CA is a single read-only file mount.
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
}
