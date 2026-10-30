package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestTokenReadOnEveryCall(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token")
	tf := newTokenFile(path)

	if _, err := tf.current(); err == nil {
		t.Fatal("missing file: want error")
	}

	write := func(s string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(s), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("abc\n")
	if v, err := tf.current(); err != nil || v != "abc" {
		t.Fatalf("got %q, %v; want abc", v, err)
	}

	// Rotation, even with an identical size and mtime, is seen at once.
	fi, _ := os.Stat(path)
	write("xyz\n")
	if err := os.Chtimes(path, fi.ModTime(), fi.ModTime()); err != nil {
		t.Fatal(err)
	}
	if v, err := tf.current(); err != nil || v != "xyz" {
		t.Fatalf("after rotation got %q, %v; want xyz", v, err)
	}

	write(" \n")
	if v, err := tf.current(); err == nil {
		t.Fatalf("empty file: got %q, want error", v)
	}

	write("abc")
	if os.Geteuid() != 0 {
		if err := os.Chmod(path, 0); err != nil {
			t.Fatal(err)
		}
		if v, err := tf.current(); err == nil {
			t.Fatalf("unreadable file: got %q, want error", v)
		}
		if err := os.Chmod(path, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if v, err := tf.current(); err == nil {
		t.Fatalf("removed file: got %q, want error", v)
	}
}
