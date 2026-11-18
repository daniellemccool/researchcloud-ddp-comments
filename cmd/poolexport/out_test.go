package main

import (
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// files lists every regular file under root, relative to it.
func files(t *testing.T, root string) []string {
	t.Helper()
	var got []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			rel, _ := filepath.Rel(root, p)
			got = append(got, rel)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(got)
	return got
}

// oneVideoExport writes a folder export with one video ID and returns it.
func oneVideoExport(t *testing.T) string {
	t.Helper()
	export := filepath.Join(t.TempDir(), "export")
	if err := os.Mkdir(export, 0o700); err != nil {
		t.Fatal(err)
	}
	watchExport(t, export, "p1-tiktok.json", "7301444403200000001")
	return export
}

// TestRunRequiresOut: without -out the tool used to write into the current
// folder, which is the repository when run as documented.
func TestRunRequiresOut(t *testing.T) {
	export := oneVideoExport(t)
	cwd := t.TempDir()
	t.Chdir(cwd)
	_, err := runQuiet("-export", export)
	if err == nil || !strings.Contains(err.Error(), "-out is required") {
		t.Fatalf("run without -out: error = %v, want one saying -out is required", err)
	}
	if got := files(t, cwd); len(got) != 0 {
		t.Fatalf("run without -out wrote %v", got)
	}
}

// TestRunRefusesOutInsideRepository: the uploads and the sent list are the
// study's video list, so they must not land where git or box-sync picks them
// up.
func TestRunRefusesOutInsideRepository(t *testing.T) {
	export := oneVideoExport(t)
	cases := []struct {
		name  string
		setup func(t *testing.T, repo string)
	}{
		{"go.mod of this module", func(t *testing.T, repo string) {
			mod := "module github.com/daniellemccool/researchcloud-ddp-comments\n\ngo 1.26.0\n"
			if err := os.WriteFile(filepath.Join(repo, "go.mod"), []byte(mod), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{".git folder", func(t *testing.T, repo string) {
			if err := os.Mkdir(filepath.Join(repo, ".git"), 0o700); err != nil {
				t.Fatal(err)
			}
		}},
		{".git file (worktree)", func(t *testing.T, repo string) {
			if err := os.WriteFile(filepath.Join(repo, ".git"), []byte("gitdir: elsewhere\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			repo := t.TempDir()
			c.setup(t, repo)
			before := files(t, repo)
			for _, out := range []string{repo, filepath.Join(repo, "uploads", "pool")} {
				_, err := runQuiet("-export", export, "-out", out)
				if err == nil || !strings.Contains(err.Error(), "outside the repository") {
					t.Fatalf("run -out %s: error = %v, want one saying to choose a folder outside the repository", out, err)
				}
			}
			t.Run("relative -out", func(t *testing.T) {
				t.Chdir(repo)
				if _, err := runQuiet("-export", export, "-out", "."); err == nil {
					t.Fatal("run -out . inside the repository succeeded")
				}
			})
			t.Run("-sent inside", func(t *testing.T) {
				out := t.TempDir()
				_, err := runQuiet("-export", export, "-out", out, "-sent", filepath.Join(repo, "sent-ids.txt"))
				if err == nil || !strings.Contains(err.Error(), "outside the repository") {
					t.Fatalf("run -sent inside the repository: error = %v", err)
				}
				if got := files(t, out); len(got) != 0 {
					t.Fatalf("refused run wrote %v to -out", got)
				}
			})
			if got := files(t, repo); !slices.Equal(got, before) {
				t.Fatalf("refused runs changed the repository: %v, want %v", got, before)
			}
		})
	}
}

// TestRunWritesOutsideRepository: a folder beside a repository, or one under
// a go.mod of another module, is fine.
func TestRunWritesOutsideRepository(t *testing.T) {
	export := oneVideoExport(t)
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(root, "other")
	if err := os.Mkdir(other, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(other, "go.mod"), []byte("module example.com/other\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, out := range []string{filepath.Join(root, "checkjefeed-pool"), filepath.Join(other, "pool")} {
		if _, err := runQuiet("-export", export, "-out", out); err != nil {
			t.Fatalf("run -out %s: %v", out, err)
		}
		if got := uploads(t, out); !slices.Equal(got, []string{"7301444403200000001"}) {
			t.Fatalf("run -out %s wrote %v", out, got)
		}
		if _, err := os.Stat(filepath.Join(out, "sent-ids.txt")); err != nil {
			t.Fatalf("no sent list in %s: %v", out, err)
		}
	}
}
