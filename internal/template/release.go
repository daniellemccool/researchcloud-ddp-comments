// Package template builds release template artifacts from a disposable
// Firefox profile: it sanitises a copy to an explicit allowlist, verifies the
// Zeeschuimer store holds no captured data, and writes a deterministic tar
// with its manifest and sha256 (ADR-0012; profiles come only from a verified
// release template, ADR-0011).
package template

import (
	"archive/tar"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/daniellemccool/researchcloud-ddp-comments/internal/platform"
)

// Manifest describes a release artifact. It is stored inside the tar as
// manifest.json (without SHA256, which is the digest of the tar itself) and
// returned by Release with SHA256 set.
type Manifest struct {
	Kind        string   `json:"kind"` // "release"
	Platform    string   `json:"platform"`
	Zeeschuimer string   `json:"zeeschuimer"`
	Modules     []string `json:"modules"`
	BuiltOn     string   `json:"built_on"` // RFC 3339 UTC
	SHA256      string   `json:"sha256,omitempty"`
}

// Counts are the object-store row counts of the Zeeschuimer IndexedDB.
type Counts struct {
	Items, Uploads, Nav, Settings int
}

var (
	// ErrNoStore: no moz-extension directory holds Zeeschuimer's stores.
	ErrNoStore = errors.New("no Zeeschuimer IndexedDB store found in the profile")
	// ErrAmbiguousStore: several extension stores look like Zeeschuimer's.
	ErrAmbiguousStore = errors.New("several extension stores look like Zeeschuimer; refusing to guess")
	// ErrNoCookiesDB: the profile has no cookies.sqlite.
	ErrNoCookiesDB = errors.New("cookies.sqlite missing from the profile")
	// ErrLocked: the profile is locked or was not shut down cleanly.
	ErrLocked = errors.New("profile is locked")
)

// Release sanitises a COPY of profile (never the original), verifies it, and
// writes <out>/ddp-comments-template-<platform>-<zsVersion>.tar plus the
// sibling .sha256 file ("<hex>  <filename>\n"). It returns the manifest,
// which is also stored inside the tar as manifest.json. It refuses with
// ErrLocked when profile/lock or profile/.parentlock exists.
func Release(profile string, p *platform.Platform, zsVersion, out string) (Manifest, error) {
	if err := checkUnlocked(profile); err != nil {
		return Manifest{}, err
	}
	tmp, err := os.MkdirTemp("", "ddp-template-")
	if err != nil {
		return Manifest{}, err
	}
	defer func() { _ = os.RemoveAll(tmp) }()

	work := filepath.Join(tmp, "profile")
	if err := copyTree(profile, work); err != nil {
		return Manifest{}, err
	}
	if err := Sanitise(work, p); err != nil {
		return Manifest{}, err
	}
	if _, err := Verify(work); err != nil {
		return Manifest{}, err
	}

	if err := os.MkdirAll(out, 0o755); err != nil {
		return Manifest{}, err
	}
	name := ArtifactName(p.Name, zsVersion)
	tarPath := filepath.Join(out, name)
	tmpTar := filepath.Join(out, name+".tmp")
	defer func() { _ = os.Remove(tmpTar) }()

	man := Manifest{
		Kind:        "release",
		Platform:    p.Name,
		Zeeschuimer: zsVersion,
		Modules:     p.ZeeschuimerModules,
		BuiltOn:     time.Now().UTC().Format(time.RFC3339),
	}
	if err := writeTar(work, tmpTar, man); err != nil {
		return Manifest{}, err
	}
	sum, err := sha256File(tmpTar)
	if err != nil {
		return Manifest{}, err
	}
	if err := os.Rename(tmpTar, tarPath); err != nil {
		return Manifest{}, err
	}
	if err := os.WriteFile(tarPath+".sha256", []byte(sum+"  "+name+"\n"), 0o644); err != nil {
		return Manifest{}, err
	}
	man.SHA256 = sum
	return man, nil
}

// checkUnlocked refuses a profile that Firefox still holds.
func checkUnlocked(profile string) error {
	for _, name := range []string{"lock", ".parentlock"} {
		path := filepath.Join(profile, name)
		if _, err := os.Lstat(path); err == nil {
			return fmt.Errorf("%w: %s exists; stop Firefox before releasing", ErrLocked, path)
		}
	}
	return nil
}

// writeTar writes the deterministic tar of dir: sorted relative paths,
// regular files and directories only, uid/gid 1000, mode 0644/0755, mtimes
// preserved, and manifest.json (without SHA256) added last.
func writeTar(dir, tarPath string, man Manifest) error {
	var paths []string
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		if d.Type()&os.ModeSymlink != 0 || (!d.Type().IsRegular() && !d.IsDir()) {
			return nil // no symlinks, no irregular files
		}
		paths = append(paths, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		return err
	}
	sort.Strings(paths)

	f, err := os.Create(tarPath)
	if err != nil {
		return err
	}
	tw := tar.NewWriter(f)
	for _, rel := range paths {
		info, err := os.Lstat(filepath.Join(dir, rel))
		if err != nil {
			_ = f.Close()
			return err
		}
		hdr := &tar.Header{
			Name:    rel,
			ModTime: info.ModTime(),
			Uid:     1000,
			Gid:     1000,
		}
		if info.IsDir() {
			hdr.Typeflag = tar.TypeDir
			hdr.Mode = 0o755
		} else {
			hdr.Typeflag = tar.TypeReg
			hdr.Mode = 0o644
			hdr.Size = info.Size()
		}
		if err := tw.WriteHeader(hdr); err != nil {
			_ = f.Close()
			return err
		}
		if !info.IsDir() {
			src, err := os.Open(filepath.Join(dir, rel))
			if err != nil {
				_ = f.Close()
				return err
			}
			_, err = io.Copy(tw, src)
			_ = src.Close()
			if err != nil {
				_ = f.Close()
				return err
			}
		}
	}

	inner := man
	inner.SHA256 = ""
	data, err := json.MarshalIndent(inner, "", "  ")
	if err != nil {
		_ = f.Close()
		return err
	}
	data = append(data, '\n')
	builtOn, err := time.Parse(time.RFC3339, man.BuiltOn)
	if err != nil {
		builtOn = time.Now().UTC()
	}
	hdr := &tar.Header{
		Name:     "manifest.json",
		Typeflag: tar.TypeReg,
		Mode:     0o644,
		Size:     int64(len(data)),
		ModTime:  builtOn,
		Uid:      1000,
		Gid:      1000,
	}
	if err := tw.WriteHeader(hdr); err != nil {
		_ = f.Close()
		return err
	}
	if _, err := tw.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	if err := tw.Close(); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}
