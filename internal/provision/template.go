package provision

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// CopyProfile copies the extracted release template's profile/ tree from
// releaseDir into firefoxDir, publishing it by rename: the profile is staged
// at <firefoxDir>/.template.tmp/profile and renamed to <firefoxDir>/profile,
// so the presence of profile/ means "templated". A stale .template.tmp is
// removed first; when profile/ already exists the copy is skipped entirely.
// A missing release directory or profile/ is a StepError "template missing".
// The copied tree is chowned to uid 1000 when running as root.
func CopyProfile(releaseDir, firefoxDir string) error {
	staging := filepath.Join(firefoxDir, ".template.tmp")
	_ = os.RemoveAll(staging) // left by a crashed run, even after the rename
	profile := filepath.Join(firefoxDir, "profile")
	if _, err := os.Stat(profile); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	src := filepath.Join(releaseDir, "profile")
	if fi, err := os.Stat(src); err != nil || !fi.IsDir() {
		return &StepError{Msg: "template missing"}
	}
	if err := copyTree(src, filepath.Join(staging, "profile")); err != nil {
		_ = os.RemoveAll(staging)
		return err
	}
	if err := os.Rename(filepath.Join(staging, "profile"), profile); err != nil {
		_ = os.RemoveAll(staging)
		return err
	}
	_ = os.RemoveAll(staging)
	return nil
}

// copyTree copies every regular file and subdirectory of src to dst with
// files 0644 and directories 0755, chowned to uid 1000 when running as root.
func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		switch {
		case d.IsDir():
			return ensureDir(target, 0o755, 1000, 1000)
		case d.Type().IsRegular():
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			return writeFileAtomic(filepath.Dir(target), filepath.Base(target), data, 0o644, 1000, 1000)
		default:
			return fmt.Errorf("template copy: %s is not a regular file or directory", path)
		}
	})
}

// copyUserContentCSS installs the hide-media stylesheet at dst (0644 in a
// 0755 chrome/ directory, uid 1000 when root).
func copyUserContentCSS(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	if err := ensureDir(filepath.Dir(dst), 0o755, 1000, 1000); err != nil {
		return err
	}
	return writeFileAtomic(filepath.Dir(dst), filepath.Base(dst), data, 0o644, 1000, 1000)
}
