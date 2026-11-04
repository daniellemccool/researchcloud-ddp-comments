package registry

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newRegistry(t *testing.T, maxSessions int) *Registry {
	t.Helper()
	return &Registry{Path: filepath.Join(t.TempDir(), "registry.tsv"), MaxSessions: maxSessions}
}

func writeFile(t *testing.T, path, text string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

func TestAllocateLowestFree(t *testing.T) {
	r := newRegistry(t, 99)
	row, err := r.Allocate("alice")
	if err != nil || row.Index != 1 {
		t.Fatalf("alice = %+v, %v; want index 1, nil", row, err)
	}
	row, err = r.Allocate("bob")
	if err != nil || row.Index != 2 {
		t.Fatalf("bob = %+v, %v; want index 2, nil", row, err)
	}
	row, err = r.Allocate("alice")
	if err != nil || row.Index != 1 {
		t.Fatalf("alice again = %+v, %v; want index 1 unchanged, nil", row, err)
	}
	rows, err := r.Rows()
	if err != nil {
		t.Fatalf("Rows: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("len(rows) = %d, want 2 (one row per user); rows = %+v", len(rows), rows)
	}
}

func TestReturningUserKeepsIndex(t *testing.T) {
	r := newRegistry(t, 99)
	if _, err := r.Allocate("alice"); err != nil {
		t.Fatalf("allocate alice: %v", err)
	}
	if _, err := r.Allocate("bob"); err != nil {
		t.Fatalf("allocate bob: %v", err)
	}
	if err := r.Retire("alice"); err != nil {
		t.Fatalf("retire alice: %v", err)
	}
	row, err := r.Allocate("alice")
	if err != nil || row.Index != 1 {
		t.Fatalf("alice again = %+v, %v; want index 1, nil", row, err)
	}
	if got := readFile(t, r.Path); len(strings.Split(strings.TrimRight(got, "\n"), "\n")) != 2 {
		t.Fatalf("file has %q, want exactly two rows", got)
	}
}

func TestReturningUserGetsLowestFreeOtherwise(t *testing.T) {
	r := newRegistry(t, 99)
	if _, err := r.Allocate("alice"); err != nil {
		t.Fatalf("allocate alice: %v", err)
	}
	if err := r.Retire("alice"); err != nil {
		t.Fatalf("retire alice: %v", err)
	}
	row, err := r.Allocate("carol")
	if err != nil || row.Index != 1 {
		t.Fatalf("carol = %+v, %v; want index 1, nil", row, err)
	}
	row, err = r.Allocate("alice")
	if err != nil || row.Index != 2 {
		t.Fatalf("alice = %+v, %v; want index 2 (1 taken by carol), nil", row, err)
	}
	rows, err := r.Rows()
	if err != nil {
		t.Fatalf("Rows: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("len(rows) = %d, want 2 (alice rewritten in place, carol); rows = %+v", len(rows), rows)
	}
}

func TestRegistryFull(t *testing.T) {
	r := newRegistry(t, 2)
	if _, err := r.Allocate("a"); err != nil {
		t.Fatalf("allocate a: %v", err)
	}
	if _, err := r.Allocate("b"); err != nil {
		t.Fatalf("allocate b: %v", err)
	}
	before := readFile(t, r.Path)
	_, err := r.Allocate("c")
	if !errors.Is(err, ErrFull) {
		t.Fatalf("err = %v, want ErrFull", err)
	}
	if !strings.Contains(err.Error(), "all 2 sessions are in use") {
		t.Fatalf("err = %q, want it to contain %q", err, "all 2 sessions are in use")
	}
	if got := readFile(t, r.Path); got != before {
		t.Fatalf("file changed on ErrFull: before %q after %q", before, got)
	}
}

func TestLegacyRowsAreLive(t *testing.T) {
	r := newRegistry(t, 99)
	writeFile(t, r.Path, "alice\t1\nbob\t2") // no trailing newline
	live, err := r.Live()
	if err != nil {
		t.Fatalf("Live: %v", err)
	}
	if live["alice"].Index != 1 || live["bob"].Index != 2 {
		t.Fatalf("live = %+v, want alice:1 bob:2", live)
	}
	row, err := r.Allocate("carol")
	if err != nil || row.Index != 3 {
		t.Fatalf("carol = %+v, %v; want index 3, nil", row, err)
	}
	got := readFile(t, r.Path)
	lines := strings.Split(strings.TrimRight(got, "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("file = %q, want 3 rows (no row glued to another)", got)
	}
	for _, line := range lines {
		if len(strings.Split(line, "\t")) != 3 {
			t.Fatalf("malformed row %q in %q", line, got)
		}
	}
}

func TestRewriteAtomicMode0644(t *testing.T) {
	r := newRegistry(t, 99)
	if _, err := r.Allocate("alice"); err != nil {
		t.Fatalf("allocate: %v", err)
	}
	fi, err := os.Stat(r.Path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if fi.Mode().Perm() != 0o644 {
		t.Fatalf("mode = %o, want 644", fi.Mode().Perm())
	}
	entries, err := os.ReadDir(filepath.Dir(r.Path))
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != "registry.tsv" {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("dir holds %v, want only registry.tsv (no temp file left)", names)
	}
}

func TestBadUsernameRejected(t *testing.T) {
	for _, bad := range []string{"alice\n", "Alice", ""} {
		r := newRegistry(t, 99)
		_, err := r.Allocate(bad)
		if err == nil {
			t.Fatalf("Allocate(%q) = nil error, want rejection", bad)
		}
		if !strings.Contains(err.Error(), "invalid username") {
			t.Fatalf("Allocate(%q) err = %q, want it to name the invalid username", bad, err)
		}
		if _, statErr := os.Stat(r.Path); !os.IsNotExist(statErr) {
			t.Fatalf("Allocate(%q) created the file (stat err = %v), want nothing written", bad, statErr)
		}
	}
}

func TestRetireNoLiveRowIsNoop(t *testing.T) {
	r := newRegistry(t, 99)
	if err := r.Retire("nobody"); err != nil {
		t.Fatalf("retire nobody: %v, want nil", err)
	}
	if _, statErr := os.Stat(r.Path); !os.IsNotExist(statErr) {
		t.Fatalf("retire nobody created the file, want no write")
	}
	if _, err := r.Allocate("alice"); err != nil {
		t.Fatalf("allocate alice: %v", err)
	}
	if err := r.Retire("alice"); err != nil {
		t.Fatalf("retire alice: %v", err)
	}
	if err := r.Retire("alice"); err != nil {
		t.Fatalf("retire alice again: %v, want nil (no live row)", err)
	}
	rows, err := r.Rows()
	if err != nil {
		t.Fatalf("Rows: %v", err)
	}
	if len(rows) != 1 || rows[0].Status != "retired" {
		t.Fatalf("rows = %+v, want one retired row", rows)
	}
}

func TestRowsBadIndex(t *testing.T) {
	r := newRegistry(t, 99)
	writeFile(t, r.Path, "alice\tone\n")
	if _, err := r.Rows(); err == nil {
		t.Fatal("Rows = nil error, want an error for a non-integer index")
	}
}

func TestRowsSkipsShortRows(t *testing.T) {
	r := newRegistry(t, 99)
	writeFile(t, r.Path, "\nalice\n\t5\nbob\t2\n")
	rows, err := r.Rows()
	if err != nil {
		t.Fatalf("Rows: %v", err)
	}
	if len(rows) != 1 || rows[0] != (Row{User: "bob", Index: 2, Status: "live"}) {
		t.Fatalf("rows = %+v, want only bob/2/live (short and empty-user rows skipped)", rows)
	}
}
