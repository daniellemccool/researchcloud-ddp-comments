package fourcat

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// OwnedSettings are the settings the desk owns in 4CAT and re-asserts at
// every start (the design's §5.6, first table): the proxy secret; what the
// studylead and techadmin tags may do; restart off for every tag and upgrade
// off; collectors only create datasets and API tokens; no access requests.
func OwnedSettings(secret string) []Setting {
	out := []Setting{{Name: "flask.proxy_secret", Value: `"` + secret + `"`}}
	for _, n := range []string{
		"privileges.can_view_all_datasets", "privileges.can_view_private_datasets",
		"privileges.can_run_processors", "privileges.can_use_explorer", "privileges.can_export_datasets",
		"privileges.admin.can_view_status", "privileges.admin.can_manage_notifications",
	} {
		out = append(out, Setting{Name: n, Tag: "studylead", Value: "true"})
	}
	for _, n := range []string{
		"privileges.admin.can_manage_users", "privileges.admin.can_manage_settings",
		"privileges.admin.can_manage_tags", "privileges.admin.can_manipulate_all_datasets",
		"privileges.admin.can_manage_extensions",
		// Someone fixing things must see them.
		"privileges.admin.can_view_status", "privileges.can_view_all_datasets", "privileges.can_view_private_datasets",
	} {
		out = append(out, Setting{Name: n, Tag: "techadmin", Value: "true"})
	}
	for _, tag := range []string{"", "admin", "studylead", "techadmin"} {
		out = append(out, Setting{Name: "privileges.admin.can_restart", Tag: tag, Value: "false"})
	}
	out = append(out, Setting{Name: "privileges.can_upgrade_to_dev", Value: "false"})
	for _, n := range []string{"privileges.can_create_dataset", "privileges.can_create_api_token"} {
		out = append(out, Setting{Name: n, Value: "true"})
	}
	for _, n := range []string{
		"privileges.can_run_processors", "privileges.can_use_explorer",
		"privileges.can_export_datasets", "privileges.can_make_dataset_public",
		"4cat.allow_access_request",
	} {
		out = append(out, Setting{Name: n, Value: "false"})
	}
	return out
}

// OnceSettings are set once, at first setup; afterwards a Tech Admin may
// change them (the design's §5.6, second table): the platform's own data
// sources and no phoning home.
func OnceSettings(sources []string) []Setting {
	quoted := make([]string, len(sources))
	for i, s := range sources {
		quoted[i] = `"` + s + `"`
	}
	return []Setting{
		{Name: "datasources.enabled", Value: "[" + strings.Join(quoted, ",") + "]"},
		{Name: "4cat.phone_home_url", Value: `""`},
	}
}

var secretRe = regexp.MustCompile(`^[0-9a-f]{32}$`)

// EnsureProxySecret returns the proxy secret kept at path, creating it first
// when missing: 32 hex characters from crypto/rand, written to a 0600 temp
// file and linked into place, never over an existing file. A file that does not hold such a secret is an
// error, never silently replaced.
func EnsureProxySecret(path string) (string, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		buf := make([]byte, 16)
		if _, err := rand.Read(buf); err != nil {
			return "", err
		}
		secret := hex.EncodeToString(buf)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return "", err
		}
		// A complete temp file is linked into place, so the secret file never
		// exists empty, even after a crash.
		f, err := os.CreateTemp(filepath.Dir(path), ".proxy-secret-*")
		if err != nil {
			return "", err
		}
		tmp := f.Name()
		defer func() { _ = os.Remove(tmp) }()
		if _, err := f.WriteString(secret); err != nil {
			_ = f.Close()
			return "", err
		}
		if err := f.Sync(); err != nil {
			_ = f.Close()
			return "", err
		}
		if err := f.Close(); err != nil {
			return "", err
		}
		if err := os.Link(tmp, path); errors.Is(err, fs.ErrExist) {
			return EnsureProxySecret(path) // lost a race: read the winner's
		} else if err != nil {
			return "", err
		}
		return secret, nil
	}
	if err != nil {
		return "", err
	}
	secret := strings.TrimSpace(string(data))
	if !secretRe.MatchString(secret) {
		return "", fmt.Errorf("proxy secret %s is malformed", path)
	}
	return secret, nil
}

// Study is the desk's side of 4CAT's configuration. Ensure asserts the owned
// settings (and the once settings, the first time) and removes the admin tag
// from the accounts the desk made; Ready reports whether that succeeded in
// this run, and the /fourcat/ proxy stays closed until it has.
type Study struct {
	Client       *Client
	SecretPath   string                   // <desk root>/secrets/4cat-proxy-secret
	OncePath     string                   // <desk root>/state/fourcat-settings-once
	Sources      []string                 // the platform's zeeschuimer_modules
	MadeAccounts func() ([]string, error) // users with a stored 4CAT password
	Logf         func(format string, args ...any)

	mu        sync.Mutex
	ready     bool
	setupDone bool
	secret    string
}

// Ensure runs once 4CAT's setup is complete: it writes the proxy secret and
// the owned settings and, when the once marker is missing, the once settings
// and the removal of the admin tag from every account the desk made. Any
// failure is returned and leaves Ready as it was; nothing is undone, since
// every part is idempotent.
func (s *Study) Ensure(ctx context.Context) error {
	pending, err := s.SetupPending(ctx)
	if err != nil {
		return err
	}
	if pending {
		return ErrSetupPending
	}
	secret, err := EnsureProxySecret(s.SecretPath)
	if err != nil {
		return err
	}
	if err := s.Client.EnsureSettings(ctx, OwnedSettings(secret)); err != nil {
		return err
	}
	if _, err := os.Stat(s.OncePath); errors.Is(err, fs.ErrNotExist) {
		if err := s.once(ctx); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	s.mu.Lock()
	s.secret, s.ready = secret, true
	s.mu.Unlock()
	return nil
}

// once writes the once settings and removes the admin tag from every
// account the desk made, then writes the marker; a failure leaves no marker,
// so the next Ensure does it all again. Accounts made later never get the
// tag, so once is enough, and a Tech Admin's later choice stands.
func (s *Study) once(ctx context.Context) error {
	if err := s.Client.EnsureSettings(ctx, OnceSettings(s.Sources)); err != nil {
		return err
	}
	users, err := s.MadeAccounts()
	if err != nil {
		return err
	}
	for _, u := range users {
		if err := s.Client.ClearAdmin(ctx, u); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(filepath.Dir(s.OncePath), 0o700); err != nil {
		return err
	}
	return os.WriteFile(s.OncePath, nil, 0o600)
}

// Run tries Ensure now and then every interval until it succeeds once, or
// until ctx is done.
func (s *Study) Run(ctx context.Context, every time.Duration) {
	for {
		err := s.Ensure(ctx)
		if err == nil {
			s.logf("fourcat: study settings asserted")
			return
		}
		s.logf("fourcat: study settings: %v", err)
		select {
		case <-ctx.Done():
			return
		case <-time.After(every):
		}
	}
}

// Ready reports whether Ensure has succeeded in this run.
func (s *Study) Ready() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ready
}

// SetupPending reports whether 4CAT still shows its first-run setup. Once it
// has seen the setup done it remembers that and answers false without asking;
// an unreachable 4CAT is an error, never "pending".
func (s *Study) SetupPending(ctx context.Context) (bool, error) {
	s.mu.Lock()
	done := s.setupDone
	s.mu.Unlock()
	if done {
		return false, nil
	}
	pending, err := s.Client.SetupPending(ctx)
	if err != nil {
		return false, err
	}
	if !pending {
		s.mu.Lock()
		s.setupDone = true
		s.mu.Unlock()
	}
	return pending, nil
}

// Secret returns the proxy secret asserted in this run; an error before
// Ensure has succeeded.
func (s *Study) Secret() (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.ready {
		return "", errors.New("4CAT study settings not asserted yet")
	}
	return s.secret, nil
}

func (s *Study) logf(format string, args ...any) {
	if s.Logf != nil {
		s.Logf(format, args...)
	}
}
