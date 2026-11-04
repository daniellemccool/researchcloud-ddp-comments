package provision

import (
	"errors"
	"io/fs"
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

// EnsurePassword returns the collector's stored 4CAT password, creating the
// file exclusively (0600) with a fresh random password when none exists.
// createdNow reports whether this call created it. An existing empty file is
// an error: the password is never reset silently.
func EnsurePassword(cfg *config.Config, user string) (pw string, createdNow bool, err error) {
	path := PasswordPath(cfg, user)
	for attempt := 0; attempt < 2; attempt++ {
		data, err := os.ReadFile(path)
		if err == nil {
			pw = strings.TrimSpace(string(data))
			if pw == "" {
				return "", false, &StepError{Msg: "4CAT password file is empty"}
			}
			return pw, false, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", false, err
		}
		pw = fourcat.NewPassword()
		if err := writeExclusive(path, []byte(pw), 0o600); err != nil {
			if errors.Is(err, fs.ErrExist) {
				continue // lost a race: read the winner's file
			}
			return "", false, err
		}
		return pw, true, nil
	}
	return "", false, &StepError{Msg: "4CAT password file unreadable"}
}

// writeExclusive publishes data at path the way the Python's
// _write(exclusive=True) does: a complete 0600 temp file in the same
// directory, fsynced and closed, is hard-linked over path, so the final file
// never exists empty — a failed write or a crash leaves only the temp file.
// A lost race (fs.ErrExist from the link) means another writer won; the temp
// is removed either way.
func writeExclusive(path string, data []byte, mode os.FileMode) error {
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
	defer func() { _ = os.Remove(tmp) }() // no-op once the link succeeded
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
	return os.Link(tmp, path)
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

// WriteToken writes the collector's 4CAT login token (ADR-0009) to
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
