package provision

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// CopyProfile copies the extracted release template into firefoxDir,
// publishing it by rename: the release directory's entries (everything
// except its manifest.json — the artifact tars the sanitised profile's
// entries at the top level, so the release directory IS the profile) are
// staged at <firefoxDir>/.template.tmp/profile and renamed to
// <firefoxDir>/profile, so the presence of profile/ means "templated". A
// stale .template.tmp is removed first; when profile/ already exists the copy
// is skipped entirely. A missing release directory, or one holding no
// prefs.js (the one file every Firefox profile has), is a StepError naming
// the artifact ("template missing: <artifact>"). The copied tree is chowned
// to uid 1000 when running as root.
func CopyProfile(releaseDir, firefoxDir string) error {
	return copyProfile(releaseDir, firefoxDir, releaseDir)
}

// copyProfile is CopyProfile with artifact named in the "template missing"
// error text; stepTemplate passes the release-relative artifact path
// (templates/release/<generation>).
func copyProfile(releaseDir, firefoxDir, artifact string) error {
	staging := filepath.Join(firefoxDir, ".template.tmp")
	_ = os.RemoveAll(staging) // left by a crashed run, even after the rename
	profile := filepath.Join(firefoxDir, "profile")
	if _, err := os.Stat(profile); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if fi, err := os.Stat(filepath.Join(releaseDir, "prefs.js")); err != nil || !fi.Mode().IsRegular() {
		return &StepError{Msg: "template missing: " + artifact}
	}
	if err := copyTree(releaseDir, filepath.Join(staging, "profile"), "manifest.json"); err != nil {
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
// files 0644 and directories 0755, chowned to uid 1000 when running as root,
// skipping the top-level entry named skip (the release manifest).
func copyTree(src, dst, skip string) error {
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		if rel == skip {
			return nil // the release manifest is not part of the profile
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
