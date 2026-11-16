package provision

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/daniellemccool/researchcloud-ddp-comments/internal/config"
	"github.com/daniellemccool/researchcloud-ddp-comments/internal/fourcat"
)

// staleTempMaxAge is how old a leftover .pw-* temp file must be before the
// sweep removes it (the Python's _sweep).
const staleTempMaxAge = time.Hour

// PasswordPath returns <DeskRoot>/secrets/4cat-passwords/<user>, the
// collector's 4CAT password (root's, 0600).
func PasswordPath(cfg *config.Config, user string) string {
	return filepath.Join(cfg.DeskRoot, "secrets", "4cat-passwords", user)
}

// pendingPasswordPath returns <DeskRoot>/secrets/4cat-passwords/.pending-<user>,
// where a fresh password waits until the desk has checked that no 4CAT
// account of that name exists (M9): a crash before that check leaves only
// this name, so a later run never mistakes someone else's account for ours.
func pendingPasswordPath(cfg *config.Config, user string) string {
	return filepath.Join(cfg.DeskRoot, "secrets", "4cat-passwords", ".pending-"+user)
}

// PreparePassword returns the collector's stored 4CAT password (pending
// false) when the final file exists, removing any pending copy. Otherwise it writes a fresh random
// password under the pending name (0600, replacing any leftover) and returns
// it with pending true; only CommitPassword gives it its real name. An
// existing empty final file is an error: the password is never reset silently.
func PreparePassword(cfg *config.Config, user string) (pw string, pending bool, err error) {
	data, err := os.ReadFile(PasswordPath(cfg, user))
	if err == nil {
		pw = strings.TrimSpace(string(data))
		if pw == "" {
			return "", false, &StepError{Msg: "4CAT password file is empty"}
		}
		// A crash inside CommitPassword leaves a copy under the pending name.
		DiscardPassword(cfg, user)
		return pw, false, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", false, err
	}
	pw = fourcat.NewPassword()
	if err := publish(pendingPasswordPath(cfg, user), []byte(pw), 0o600, os.Rename); err != nil {
		return "", false, err
	}
	return pw, true, nil
}

// CommitPassword gives the pending password its real name by hard-linking it
// to PasswordPath, then removes the pending name. An existing final file is
// an error (fs.ErrExist) and is left as it is.
func CommitPassword(cfg *config.Config, user string) error {
	pending := pendingPasswordPath(cfg, user)
	if err := os.Link(pending, PasswordPath(cfg, user)); err != nil {
		return err
	}
	return os.Remove(pending)
}

// DiscardPassword removes the pending password, if any.
func DiscardPassword(cfg *config.Config, user string) {
	_ = os.Remove(pendingPasswordPath(cfg, user))
}

// writeExclusive publishes data at path the way the Python's
// _write(exclusive=True) does: a complete temp file is hard-linked over path,
// so the final file never exists empty. A lost race (fs.ErrExist from the
// link) means another writer won.
func writeExclusive(path string, data []byte, mode os.FileMode) error {
	return publish(path, data, mode, os.Link)
}

// publish writes data to a complete 0600 .pw-* temp file in path's directory,
// fsynced and closed, and then puts it at path with place (os.Link: never
// over an existing file; os.Rename: replacing one). The file at path never
// exists empty; a failed write or a crash leaves only the temp file, which is
// removed either way.
func publish(path string, data []byte, mode os.FileMode, place func(oldpath, newpath string) error) error {
	dir := filepath.Dir(path)
	if err := ensureDir(dir, 0o700, 0, 0); err != nil {
		return err
	}
	sweepTemp(dir)
	f, err := os.CreateTemp(dir, ".pw-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer func() { _ = os.Remove(tmp) }() // no-op once a rename succeeded
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Chmod(mode); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return place(tmp, path)
}

// sweepTemp removes leftover .pw-* temp files older than staleTempMaxAge in
// dir (left by a crashed run). Best effort.
func sweepTemp(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-staleTempMaxAge)
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), ".pw-") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if info.ModTime().Before(cutoff) {
			_ = os.Remove(filepath.Join(dir, e.Name()))
		}
	}
}

// readPassword reads the stored 4CAT password for the token step.
func readPassword(cfg *config.Config, user string) (string, error) {
	data, err := os.ReadFile(PasswordPath(cfg, user))
	if errors.Is(err, os.ErrNotExist) {
		return "", &StepError{Msg: "no 4CAT password file"}
	}
	if err != nil {
		return "", err
	}
	pw := strings.TrimSpace(string(data))
	if pw == "" {
		return "", &StepError{Msg: "4CAT password file is empty"}
	}
	return pw, nil
}

// WriteToken writes the collector's 4CAT API token (ADR-0009) to
// <DeskRoot>/secrets/4cat-tokens/<user>/4cat-token: the directory is uid 1000
// 0750, the file uid 1000 0600, published by temp-file-and-rename so a
// rotation is seen at once.
func WriteToken(cfg *config.Config, user, token string) error {
	dir := filepath.Join(cfg.DeskRoot, "secrets", "4cat-tokens", user)
	if err := ensureDir(dir, 0o750, 1000, 1000); err != nil {
		return err
	}
	return writeFileAtomic(dir, "4cat-token", []byte(token), 0o600, 1000, 1000)
}

// RemoveToken removes the collector's token file; a missing file is success.
// Revocation calls it only after the token was deleted in 4CAT (ADR-0005).
func RemoveToken(cfg *config.Config, user string) error {
	if !userRe.MatchString(user) {
		return fmt.Errorf("invalid username: %q", user)
	}
	err := os.Remove(filepath.Join(cfg.DeskRoot, "secrets", "4cat-tokens", user, "4cat-token"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}
