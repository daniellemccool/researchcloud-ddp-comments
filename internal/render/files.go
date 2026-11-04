package render

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/daniellemccool/researchcloud-ddp-comments/internal/config"
)

// Runtime is the live state of a running pair, recorded in pair.json next to
// the spec so a re-render never forgets a running pair.
type Runtime struct {
	Subnet    string    `json:"subnet"`
	NetworkID string    `json:"network_id"`
	ProxyID   string    `json:"proxy_id"`
	FirefoxID string    `json:"firefox_id"`
	StartedAt time.Time `json:"started_at"`
}

// pairFile is the on-disk shape of state/users/<user>/pair.json.
type pairFile struct {
	Spec    Spec     `json:"spec"`
	Runtime *Runtime `json:"runtime"`
}

// WriteUserFiles writes the per-user files on the volume: the rendered policy
// at firefox/<user>/policies.json (0644, uid 1000, in a 0755 uid-1000
// directory — ADR-0013) and state/users/<user>/pair.json (0600, root's) holding
// the spec plus the existing runtime, or null when none is recorded yet.
func WriteUserFiles(cfg *config.Config, s Spec) error {
	policy, err := Policy(cfg)
	if err != nil {
		return err
	}

	firefoxDir := filepath.Join(cfg.DeskRoot, "firefox")
	userDir := filepath.Join(firefoxDir, s.User)
	for _, d := range []string{firefoxDir, userDir} {
		if err := ensureDir(d, 0o755, 1000, 1000); err != nil {
			return err
		}
	}
	if err := writeFileAtomic(userDir, "policies.json", policy, 0o644, 1000, 1000); err != nil {
		return err
	}

	pairDir := filepath.Join(cfg.DeskRoot, "state", "users", s.User)
	if err := ensureDir(pairDir, 0o755, 0, 0); err != nil {
		return err
	}
	pf, err := readPairFile(cfg, s.User)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		pf = pairFile{} // no pair yet: the runtime stays null
	}
	pf.Spec = s
	data, err := json.Marshal(pf)
	if err != nil {
		return err
	}
	return writeFileAtomic(pairDir, "pair.json", data, 0o600, 0, 0)
}

// WriteRuntime rewrites state/users/<user>/pair.json keeping the recorded
// spec and replacing the runtime; a nil rt clears the runtime to null. The
// pair file must already exist (WriteUserFiles creates it).
func WriteRuntime(cfg *config.Config, user string, rt *Runtime) error {
	pf, err := readPairFile(cfg, user)
	if err != nil {
		return err
	}
	pf.Runtime = rt
	data, err := json.Marshal(pf)
	if err != nil {
		return err
	}
	pairDir := filepath.Join(cfg.DeskRoot, "state", "users", user)
	return writeFileAtomic(pairDir, "pair.json", data, 0o600, 0, 0)
}

// ReadPair reads state/users/<user>/pair.json. A missing file returns an error
// wrapping os.ErrNotExist; a null runtime comes back as nil.
func ReadPair(cfg *config.Config, user string) (Spec, *Runtime, error) {
	pf, err := readPairFile(cfg, user)
	if err != nil {
		return Spec{}, nil, err
	}
	return pf.Spec, pf.Runtime, nil
}

// readPairFile loads pair.json, preserving a missing file as os.ErrNotExist.
func readPairFile(cfg *config.Config, user string) (pairFile, error) {
	var pf pairFile
	path := filepath.Join(cfg.DeskRoot, "state", "users", user, "pair.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return pf, fmt.Errorf("pair.json for %s: %w", user, err)
	}
	if err := json.Unmarshal(data, &pf); err != nil {
		return pf, fmt.Errorf("pair.json for %s: %w", user, err)
	}
	return pf, nil
}

// WritePlatformCopy replaces <DeskRoot>/platform with a copy of platformDir
// (every regular file 0644, every directory 0755). The swap is atomic: the
// tree is built at <DeskRoot>/platform.tmp-<random>, a previous platform/ is
// renamed aside, the new tree is renamed into place, and the old tree is
// deleted after the swap.
func WritePlatformCopy(cfg *config.Config, platformDir string) error {
	deskRoot := cfg.DeskRoot
	tmp := filepath.Join(deskRoot, "platform.tmp-"+randomSuffix())
	if err := copyTree(platformDir, tmp); err != nil {
		_ = os.RemoveAll(tmp)
		return err
	}

	dst := filepath.Join(deskRoot, "platform")
	aside := filepath.Join(deskRoot, "platform.old-"+randomSuffix())
	if _, err := os.Lstat(dst); err == nil {
		if err := os.Rename(dst, aside); err != nil {
			_ = os.RemoveAll(tmp)
			return err
		}
		if err := os.Rename(tmp, dst); err != nil {
			_ = os.Rename(aside, dst) // put the old tree back, best effort
			_ = os.RemoveAll(tmp)
			return err
		}
		// The new copy is already published; failing to delete the old tree
		// is not an error for the caller.
		_ = os.RemoveAll(aside)
		return nil
	} else if !os.IsNotExist(err) {
		_ = os.RemoveAll(tmp)
		return err
	}
	return os.Rename(tmp, dst)
}

// copyTree copies every regular file and subdirectory of src to dst with the
// modes ADR-0013 requires (files 0644, directories 0755).
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
			return fmt.Errorf("platform copy: %s is not a regular file or directory", path)
		}
	})
}

// ensureDir creates dir (and parents) and sets the mode and, when running as
// root, the owner explicitly (umask-independent).
func ensureDir(dir string, mode fs.FileMode, uid, gid int) error {
	if err := os.MkdirAll(dir, mode); err != nil {
		return err
	}
	if err := os.Chmod(dir, mode); err != nil {
		return err
	}
	if os.Geteuid() == 0 {
		if err := os.Chown(dir, uid, gid); err != nil {
			return err
		}
	}
	return nil
}

// writeFileAtomic writes data to dir/name via a temp file in the same
// directory: write, chmod, chown (only when running as root), fsync, rename.
func writeFileAtomic(dir, name string, data []byte, mode fs.FileMode, uid, gid int) error {
	f, err := os.CreateTemp(dir, name+".tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer func() { _ = os.Remove(tmp) }() // no-op once the rename succeeded
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Chmod(mode); err != nil {
		_ = f.Close()
		return err
	}
	if os.Geteuid() == 0 {
		if err := f.Chown(uid, gid); err != nil {
			_ = f.Close()
			return err
		}
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(dir, name))
}

// randomSuffix returns 16 hex chars from crypto/rand for temp names.
func randomSuffix() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b[:])
}
