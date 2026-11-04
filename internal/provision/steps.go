package provision

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/daniellemccool/researchcloud-ddp-comments/internal/ca"
	"github.com/daniellemccool/researchcloud-ddp-comments/internal/config"
	"github.com/daniellemccool/researchcloud-ddp-comments/internal/fourcat"
	"github.com/daniellemccool/researchcloud-ddp-comments/internal/locks"
	"github.com/daniellemccool/researchcloud-ddp-comments/internal/registry"
	"github.com/daniellemccool/researchcloud-ddp-comments/internal/render"
)

// fourcatError wraps an error returned by a fourcat.Client call. Those
// messages carry no secret by contract (type names, statuses, the validated
// username), so describe records them verbatim.
type fourcatError struct{ err error }

func (e *fourcatError) Error() string { return e.err.Error() }
func (e *fourcatError) Unwrap() error { return e.err }

// Provisioner advances one collector toward ready. Fourcat is nil when 4CAT
// is disabled, which skips both fourcat steps. Now is the clock for the
// step file's "updated" field; nil means time.Now.
type Provisioner struct {
	Cfg     *config.Config
	Reg     *registry.Registry
	Fourcat *fourcat.Client
	Locks   *locks.Set
	Now     func() time.Time
}

func (p *Provisioner) now() time.Time {
	if p.Now != nil {
		return p.Now()
	}
	return time.Now()
}

// Advance runs every step that is not done yet, in order, under the user's
// lock (the global lock is taken inside the registry step only), and returns
// the state after the run. It stops at the first failing step, records the
// error in the step file and returns; the next call retries that step.
func (p *Provisioner) Advance(ctx context.Context, user string, admin bool) State {
	if !userRe.MatchString(user) {
		return State{Steps: map[string]string{Steps[0]: "error: invalid username"}}
	}
	userLock := p.Locks.User(user)
	userLock.Lock()
	defer userLock.Unlock()

	st := Load(p.Cfg, user)
	if st.Steps == nil {
		st.Steps = map[string]string{}
	}
	// A crashed run may have left the template staging dir behind; clear it
	// before resuming, even when the template step is already done.
	_ = os.RemoveAll(filepath.Join(p.Cfg.DeskRoot, "state", "firefox", user, ".template.tmp"))
	for _, step := range Steps {
		if st.Steps[step] == "done" {
			continue
		}
		if err := p.runStep(ctx, step, user, admin); err != nil {
			st.Steps[step] = "error: " + describe(err)
			_ = p.save(user, &st) // best effort: the in-memory state is returned either way
			return st
		}
		st.Steps[step] = "done"
		if step == "render" {
			st.AppliedGeneration = render.Generation(p.Cfg)
		}
		if err := p.save(user, &st); err != nil {
			// The step is idempotent; record the persistence failure as the
			// step's error so the next visit retries it.
			st.Steps[step] = "error: " + describe(err)
			_ = p.save(user, &st)
			return st
		}
	}
	return st
}

func (p *Provisioner) runStep(ctx context.Context, step, user string, admin bool) error {
	switch step {
	case "registry":
		return p.stepRegistry(user)
	case "dirs":
		return p.stepDirs(user)
	case "template":
		return p.stepTemplate(user)
	case "fourcat_account":
		return p.stepFourcatAccount(ctx, user, admin)
	case "fourcat_token":
		return p.stepFourcatToken(ctx, user)
	case "render":
		return p.stepRender(user)
	}
	return &StepError{Msg: "unknown step " + step}
}

// stepRegistry allocates the seat index under the global lock, taken inside
// the user lock Advance already holds.
func (p *Provisioner) stepRegistry(user string) error {
	p.Locks.Global.Lock()
	defer p.Locks.Global.Unlock()
	_, err := p.Reg.Allocate(user)
	return err
}

// stepDirs creates the user's directories with their modes and owners, and
// generates their private CA for the capture proxy (so Firefox never waits
// on the proxy to produce one).
func (p *Provisioner) stepDirs(user string) error {
	root := p.Cfg.DeskRoot
	dirs := []struct {
		path     string
		mode     fs.FileMode
		uid, gid int
	}{
		{filepath.Join(root, "state", "firefox", user), 0o755, 1000, 1000},
		{filepath.Join(root, "state", "proxy", user), 0o700, 1000, 1000},
		{filepath.Join(root, "state", "captures", user), 0o700, 1000, 1000},
		{filepath.Join(root, "firefox", user), 0o755, 1000, 1000},
		{filepath.Join(root, "state", "users", user), 0o700, 0, 0},
		{filepath.Join(root, "secrets"), 0o700, 0, 0},
		{filepath.Join(root, "secrets", "4cat-passwords"), 0o700, 0, 0},
		{filepath.Join(root, "secrets", "4cat-tokens"), 0o700, 0, 0},
	}
	for _, d := range dirs {
		if err := ensureDir(d.path, d.mode, d.uid, d.gid); err != nil {
			return err
		}
	}
	proxyDir := filepath.Join(root, "state", "proxy", user)
	if _, err := ca.Ensure(filepath.Join(proxyDir, "ca.pem"), filepath.Join(proxyDir, "ca-key.pem")); err != nil {
		return err
	}
	if os.Geteuid() == 0 {
		// The proxy runs as uid 1000 and reads both files.
		for _, name := range []string{"ca.pem", "ca-key.pem"} {
			if err := os.Chown(filepath.Join(proxyDir, name), 1000, 1000); err != nil {
				return err
			}
		}
	}
	return nil
}

// stepTemplate copies the verified release template (the extracted release
// directory IS the profile: its entries, minus manifest.json, become the
// user's profile/) into the user's firefox dir via copyProfile, and applies
// the hide-media stylesheet.
func (p *Provisioner) stepTemplate(user string) error {
	firefoxDir := filepath.Join(p.Cfg.DeskRoot, "state", "firefox", user)
	artifact := filepath.Join("templates", "release", render.Generation(p.Cfg))
	releaseDir := filepath.Join(p.Cfg.DeskRoot, artifact)
	if err := copyProfile(releaseDir, firefoxDir, artifact); err != nil {
		return err
	}
	cssDst := filepath.Join(firefoxDir, "profile", "chrome", "userContent.css")
	if p.Cfg.HideMedia {
		src := filepath.Join(p.Cfg.DeskRoot, "platform", "userContent.css")
		if _, err := os.Stat(src); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return &StepError{Msg: "hide-media stylesheet missing: platform/userContent.css"}
			}
			return err
		}
		if err := copyUserContentCSS(src, cssDst); err != nil {
			return err
		}
	} else if err := os.Remove(cssDst); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// stepFourcatAccount creates the collector's own 4CAT account (admins
// tagged), or stops with ErrNeedsAdopt when the account already exists but
// the desk holds no password for it. Skipped when 4CAT is disabled.
func (p *Provisioner) stepFourcatAccount(ctx context.Context, user string, admin bool) error {
	if p.Fourcat == nil {
		return nil
	}
	pending, err := p.Fourcat.SetupPending(ctx)
	if err != nil {
		return &fourcatError{err}
	}
	if pending {
		return fourcat.ErrSetupPending
	}
	pw, createdNow, err := EnsurePassword(p.Cfg, user)
	if err != nil {
		return err
	}
	exists, err := p.Fourcat.AccountExists(ctx, user)
	if err != nil {
		if createdNow {
			// A retry must not mistake someone else's account for ours.
			_ = os.Remove(PasswordPath(p.Cfg, user))
		}
		return &fourcatError{err}
	}
	if exists && createdNow {
		_ = os.Remove(PasswordPath(p.Cfg, user))
		return fourcat.ErrNeedsAdopt
	}
	if !exists {
		if err := p.Fourcat.CreateAccount(ctx, user, pw, admin); err != nil {
			return &fourcatError{err}
		}
	}
	if err := p.Fourcat.SetAdmin(ctx, user, admin); err != nil {
		return &fourcatError{err}
	}
	return nil
}

// stepFourcatToken logs in with the stored password and writes the login
// token for the proxy. Skipped when 4CAT is disabled.
func (p *Provisioner) stepFourcatToken(ctx context.Context, user string) error {
	if p.Fourcat == nil {
		return nil
	}
	pw, err := readPassword(p.Cfg, user)
	if err != nil {
		return err
	}
	token, err := p.Fourcat.LoginToken(ctx, user, pw)
	if err != nil {
		return &fourcatError{err}
	}
	return WriteToken(p.Cfg, user, token)
}

// stepRender writes the pair spec and policy files for the user's live row.
func (p *Provisioner) stepRender(user string) error {
	live, err := p.Reg.Live()
	if err != nil {
		return err
	}
	row, ok := live[user]
	if !ok {
		return &StepError{Msg: "no live registry row for " + user}
	}
	return render.WriteUserFiles(p.Cfg, render.Build(p.Cfg, user, row.Index))
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
// directory: write, chmod, chown (only when running as root), close, rename.
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
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(dir, name))
}
