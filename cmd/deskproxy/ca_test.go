package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEnsureCAFixesKeyMode(t *testing.T) {
	dir := t.TempDir()
	certPath := filepath.Join(dir, "ca.pem")
	keyPath := filepath.Join(dir, "ca-key.pem")

	// A stale 0644 key with no cert: both files are regenerated together
	// and the key ends up 0600.
	if err := os.WriteFile(keyPath, []byte("stale"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ensureCA(certPath, keyPath); err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]os.FileMode{keyPath: 0o600, certPath: 0o644} {
		fi, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm() != want {
			t.Errorf("%s mode %v, want %v", path, fi.Mode().Perm(), want)
		}
	}

	// A second call loads the same CA rather than regenerating.
	before, _ := os.ReadFile(certPath)
	if _, err := ensureCA(certPath, keyPath); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(certPath)
	if string(before) != string(after) {
		t.Error("existing CA was regenerated")
	}
}
