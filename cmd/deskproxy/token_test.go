package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestTokenReloadsOnMtime(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token")
	tf := newTokenFile(path)

	if _, err := tf.current(); err == nil {
		t.Fatal("missing file: want error")
	}

	if err := os.WriteFile(path, []byte("abc\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t0 := time.Now().Add(-time.Hour).Truncate(time.Second)
	if err := os.Chtimes(path, t0, t0); err != nil {
		t.Fatal(err)
	}
	if v, err := tf.current(); err != nil || v != "abc" {
		t.Fatalf("got %q, %v; want abc", v, err)
	}

	// Same size, same mtime: the cached value is served, the file is not re-read.
	if err := os.WriteFile(path, []byte("xyz\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, t0, t0); err != nil {
		t.Fatal(err)
	}
	if v, _ := tf.current(); v != "abc" {
		t.Fatalf("unchanged mtime re-read the file: got %q", v)
	}

	// New mtime: re-read.
	t1 := t0.Add(time.Minute)
	if err := os.Chtimes(path, t1, t1); err != nil {
		t.Fatal(err)
	}
	if v, err := tf.current(); err != nil || v != "xyz" {
		t.Fatalf("after mtime change got %q, %v; want xyz", v, err)
	}

	// Emptied: error, nothing stale is served.
	if err := os.WriteFile(path, []byte(" \n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if v, err := tf.current(); err == nil {
		t.Fatalf("empty file: got %q, want error", v)
	}

	// Removed: error.
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if v, err := tf.current(); err == nil {
		t.Fatalf("removed file: got %q, want error", v)
	}
}
