package ca

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestEnsureFixesKeyMode(t *testing.T) {
	dir := t.TempDir()
	certPath := filepath.Join(dir, "ca.pem")
	keyPath := filepath.Join(dir, "ca-key.pem")

	// A stale 0644 key with no cert: both files are regenerated together
	// and the key ends up 0600.
	if err := os.WriteFile(keyPath, []byte("stale"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Ensure(certPath, keyPath); err != nil {
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
	if _, err := Ensure(certPath, keyPath); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(certPath)
	if string(before) != string(after) {
		t.Error("existing CA was regenerated")
	}
}

func TestEnsureChmodsExistingFiles(t *testing.T) {
	dir := t.TempDir()
	certPath := filepath.Join(dir, "ca.pem")
	keyPath := filepath.Join(dir, "ca-key.pem")
	if _, err := Ensure(certPath, keyPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(keyPath, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(certPath, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Ensure(certPath, keyPath); err != nil {
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
}

// TestCAEnsureSharedWithProxy is the contract with the desk's provisioning:
// both binaries call Ensure on the same paths, so a second call returns the
// same certificate and leaves the modes the proxy relies on (cert 0644,
// key 0600).
func TestCAEnsureSharedWithProxy(t *testing.T) {
	dir := t.TempDir()
	certPath := filepath.Join(dir, "ca.pem")
	keyPath := filepath.Join(dir, "ca-key.pem")

	first, err := Ensure(certPath, keyPath)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Ensure(certPath, keyPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first.Certificate[0], second.Certificate[0]) {
		t.Error("Ensure returned a different certificate on the second call")
	}
	for path, want := range map[string]os.FileMode{certPath: 0o644, keyPath: 0o600} {
		fi, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm() != want {
			t.Errorf("%s mode %v, want %v", path, fi.Mode().Perm(), want)
		}
	}
}
