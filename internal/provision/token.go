package provision

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/daniellemccool/researchcloud-ddp-comments/internal/config"
	"github.com/daniellemccool/researchcloud-ddp-comments/internal/fourcat"
)

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
			if errors.Is(err, os.ErrExist) {
				continue // lost a race: read the winner's file
			}
			return "", false, err
		}
		return pw, true, nil
	}
	return "", false, &StepError{Msg: "4CAT password file unreadable"}
}

// writeExclusive creates path with O_EXCL (failing with os.ErrExist when
// someone else won the race) and sets the mode explicitly.
func writeExclusive(path string, data []byte, mode os.FileMode) error {
	if err := ensureDir(filepath.Dir(path), 0o700, 0, 0); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Chmod(mode); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
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
