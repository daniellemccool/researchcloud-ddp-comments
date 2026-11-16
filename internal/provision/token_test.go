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

// TestPreparePasswordNeverLeavesEmptyFile: neither the pending nor the final
// password file ever exists empty, and only CommitPassword gives the password
// its real name.
func TestPreparePasswordNeverLeavesEmptyFile(t *testing.T) {
	env := newTestEnv(t)
	cfg := env.cfg
	path := PasswordPath(cfg, "alice")
	pending := pendingPasswordPath(cfg, "alice")

	// Fresh: written complete under the pending name only.
	pw, isPending, err := PreparePassword(cfg, "alice")
	if err != nil || !isPending || pw == "" {
		t.Fatalf("PreparePassword: empty %v, pending %v, err %v; want a fresh pending password", pw == "", isPending, err)
	}
	if got := readFile(t, pending); got != pw {
		t.Error("pending file does not hold the returned password")
	}
	if got := statMode(t, pending); got != 0o600 {
		t.Errorf("pending file mode %v, want 0600", got)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("final password file exists before commit (err = %v)", err)
	}
	assertNoPwTemp(t, filepath.Dir(path))

	// A leftover pending file is replaced, never adopted.
	pw2, isPending2, err := PreparePassword(cfg, "alice")
	if err != nil || !isPending2 || pw2 == pw {
		t.Fatalf("second PreparePassword: same as first %v, pending %v, err %v; want a new pending password", pw2 == pw, isPending2, err)
	}
	if got := readFile(t, pending); got != pw2 {
		t.Error("pending file does not hold the replacement password")
	}

	// Commit: the final file holds the password, the pending name is gone.
	if err := CommitPassword(cfg, "alice"); err != nil {
		t.Fatalf("CommitPassword: %v", err)
	}
	if got := readFile(t, path); got != pw2 {
		t.Error("final file does not hold the committed password")
	}
	if got := statMode(t, path); got != 0o600 {
		t.Errorf("final file mode %v, want 0600", got)
	}
	if _, err := os.Stat(pending); !os.IsNotExist(err) {
		t.Errorf("pending file left after commit (err = %v)", err)
	}

	// Stored: returned as-is, not pending.
	pw3, isPending3, err := PreparePassword(cfg, "alice")
	if err != nil || isPending3 || pw3 != pw2 {
		t.Errorf("PreparePassword after commit: stored one %v, pending %v, err %v; want the stored password", pw3 == pw2, isPending3, err)
	}

	// A commit over an existing final file is an error and keeps it.
	writeFile(t, pending, "intruder")
	if err := CommitPassword(cfg, "alice"); err == nil {
		t.Error("CommitPassword over an existing final file succeeded")
	}
	if got := readFile(t, path); got != pw2 {
		t.Error("a refused commit changed the final file")
	}

	// Discard removes the pending name only.
	DiscardPassword(cfg, "alice")
	if _, err := os.Stat(pending); !os.IsNotExist(err) {
		t.Errorf("pending file left after discard (err = %v)", err)
	}
	if got := readFile(t, path); got != pw2 {
		t.Error("discard touched the final file")
	}

	// A crash inside CommitPassword (linked, pending not yet removed) leaves a
	// copy under the pending name; the next PreparePassword removes it.
	writeFile(t, pending, pw2)
	if _, isPending4, err := PreparePassword(cfg, "alice"); err != nil || isPending4 {
		t.Fatalf("PreparePassword with both names: pending %v, err %v; want the stored password", isPending4, err)
	}
	if _, err := os.Stat(pending); !os.IsNotExist(err) {
		t.Errorf("pending copy left beside the stored password (err = %v)", err)
	}

	// Pre-existing EMPTY final file: the "is empty" error, never a silent overwrite.
	env2 := newTestEnv(t)
	emptyPath := PasswordPath(env2.cfg, "bob")
	writeFile(t, emptyPath, "")
	if _, _, err := PreparePassword(env2.cfg, "bob"); err == nil || err.Error() != "4CAT password file is empty" {
		t.Errorf("PreparePassword(empty file) error = %v, want %q", err, "4CAT password file is empty")
	}
	if got := readFile(t, emptyPath); got != "" {
		t.Error("empty password file was overwritten")
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
		t.Error("password file changed by the loser of a race")
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
	if _, _, err := PreparePassword(env4.cfg, "dave"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Errorf("stale .pw-* temp still present (err = %v)", err)
	}
	assertNoPwTemp(t, dir)
}

func TestRemoveTokenMissingIsFine(t *testing.T) {
	env := newTestEnv(t)
	if err := RemoveToken(env.cfg, "alice"); err != nil {
		t.Errorf("RemoveToken without a file: %v", err)
	}
	if err := WriteToken(env.cfg, "alice", "x"); err != nil {
		t.Fatal(err)
	}
	if err := RemoveToken(env.cfg, "alice"); err != nil {
		t.Fatalf("RemoveToken: %v", err)
	}
	if _, err := os.Stat(filepath.Join(env.root, "secrets", "4cat-tokens", "alice", "4cat-token")); !os.IsNotExist(err) {
		t.Errorf("token file still present (err = %v)", err)
	}
}
