package provision

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func assertNoPwTemp(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".pw-") {
			t.Errorf("temp file %s left behind in %s", e.Name(), dir)
		}
	}
}

func TestEnsurePasswordNeverLeavesEmptyFile(t *testing.T) {
	env := newTestEnv(t)
	cfg := env.cfg

	// Fresh: the file is published complete (never empty) and no temp remains.
	pw, createdNow, err := EnsurePassword(cfg, "alice")
	if err != nil || !createdNow || pw == "" {
		t.Fatalf("EnsurePassword = %q, %v, %v; want a fresh non-empty password", pw, createdNow, err)
	}
	path := PasswordPath(cfg, "alice")
	if got := readFile(t, path); strings.TrimSpace(got) == "" {
		t.Error("password file is empty after EnsurePassword")
	}
	if got := statMode(t, path); got != 0o600 {
		t.Errorf("password file mode %v, want 0600", got)
	}
	assertNoPwTemp(t, filepath.Dir(path))

	// Existing non-empty: returned as-is, not recreated.
	pw2, createdNow2, err := EnsurePassword(cfg, "alice")
	if err != nil || createdNow2 || pw2 != pw {
		t.Errorf("second EnsurePassword = %q, %v, %v; want %q, false, nil", pw2, createdNow2, err, pw)
	}

	// Pre-existing EMPTY file: the "is empty" error, never a silent overwrite.
	env2 := newTestEnv(t)
	emptyPath := PasswordPath(env2.cfg, "bob")
	writeFile(t, emptyPath, "")
	if _, _, err := EnsurePassword(env2.cfg, "bob"); err == nil || err.Error() != "4CAT password file is empty" {
		t.Errorf("EnsurePassword(empty file) error = %v, want %q", err, "4CAT password file is empty")
	}
	if got := readFile(t, emptyPath); got != "" {
		t.Errorf("empty password file was overwritten: %q", got)
	}

	// Lost race: the link fails with fs.ErrExist, the winner's file stays
	// intact, and no temp is left behind.
	env3 := newTestEnv(t)
	racePath := PasswordPath(env3.cfg, "carol")
	if err := writeExclusive(racePath, []byte("winner-pw"), 0o600); err != nil {
		t.Fatal(err)
	}
	err = writeExclusive(racePath, []byte("loser-pw"), 0o600)
	if !errors.Is(err, fs.ErrExist) {
		t.Errorf("second writeExclusive error = %v, want fs.ErrExist", err)
	}
	if got := readFile(t, racePath); got != "winner-pw" {
		t.Errorf("password file = %q after a lost race, want %q", got, "winner-pw")
	}
	assertNoPwTemp(t, filepath.Dir(racePath))

	// A stale .pw-* temp (older than an hour) is swept before writing.
	env4 := newTestEnv(t)
	dir := filepath.Dir(PasswordPath(env4.cfg, "dave"))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	stale := filepath.Join(dir, ".pw-stale")
	writeFile(t, stale, "old")
	old := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(stale, old, old); err != nil {
		t.Fatal(err)
	}
	if _, _, err := EnsurePassword(env4.cfg, "dave"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Errorf("stale .pw-* temp still present (err = %v)", err)
	}
	assertNoPwTemp(t, dir)
}
