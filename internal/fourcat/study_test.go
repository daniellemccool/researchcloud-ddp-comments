package fourcat

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// TestOwnedSettingsTable: exactly the first table of the design's §5.6.
func TestOwnedSettingsTable(t *testing.T) {
	want := []Setting{
		{"flask.proxy_secret", "", `"0123456789abcdef0123456789abcdef"`},
	}
	for _, n := range []string{
		"privileges.can_view_all_datasets", "privileges.can_view_private_datasets",
		"privileges.can_run_processors", "privileges.can_use_explorer", "privileges.can_export_datasets",
		"privileges.admin.can_view_status", "privileges.admin.can_manage_notifications",
	} {
		want = append(want, Setting{n, "studylead", "true"})
	}
	for _, n := range []string{
		"privileges.admin.can_manage_users", "privileges.admin.can_manage_settings",
		"privileges.admin.can_manage_tags", "privileges.admin.can_manipulate_all_datasets",
		"privileges.admin.can_manage_extensions",
		"privileges.admin.can_view_status", "privileges.can_view_all_datasets", "privileges.can_view_private_datasets",
	} {
		want = append(want, Setting{n, "techadmin", "true"})
	}
	for _, tag := range []string{"", "admin", "studylead", "techadmin"} {
		want = append(want, Setting{"privileges.admin.can_restart", tag, "false"})
	}
	want = append(want,
		Setting{"privileges.can_upgrade_to_dev", "", "false"},
		Setting{"privileges.can_create_dataset", "", "true"},
		Setting{"privileges.can_create_api_token", "", "true"},
		Setting{"privileges.can_run_processors", "", "false"},
		Setting{"privileges.can_use_explorer", "", "false"},
		Setting{"privileges.can_export_datasets", "", "false"},
		Setting{"privileges.can_make_dataset_public", "", "false"},
		Setting{"4cat.allow_access_request", "", "false"},
	)
	got := OwnedSettings("0123456789abcdef0123456789abcdef")
	if !sameSettings(got, want) {
		t.Errorf("OwnedSettings =\n%v\nwant\n%v", got, want)
	}
}

func TestOnceSettingsTable(t *testing.T) {
	got := OnceSettings([]string{"tiktok-comments", "tiktok"})
	want := []Setting{
		{"datasources.enabled", "", `["tiktok-comments","tiktok"]`},
		{"4cat.phone_home_url", "", `""`},
	}
	if !sameSettings(got, want) {
		t.Errorf("OnceSettings = %v, want %v", got, want)
	}
}

// sameSettings compares as sets: the order of the rows does not matter.
func sameSettings(a, b []Setting) bool {
	key := func(s []Setting) []string {
		out := make([]string, len(s))
		for i, x := range s {
			out[i] = x.Name + "\x00" + x.Tag + "\x00" + x.Value
		}
		sort.Strings(out)
		return out
	}
	ka, kb := key(a), key(b)
	if len(ka) != len(kb) {
		return false
	}
	for i := range ka {
		if ka[i] != kb[i] {
			return false
		}
	}
	return true
}

func TestEnsureProxySecret(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secrets", "4cat-proxy-secret")
	s1, err := EnsureProxySecret(path)
	if err != nil {
		t.Fatalf("EnsureProxySecret: %v", err)
	}
	if !regexp.MustCompile(`^[0-9a-f]{32}$`).MatchString(s1) {
		t.Errorf("secret is not 32 hex characters (length %d)", len(s1))
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("secret file mode = %v, %v; want 0600", info, err)
	}
	s2, err := EnsureProxySecret(path)
	if err != nil || s2 != s1 {
		t.Errorf("second call: same %v, err %v; want the same secret", s2 == s1, err)
	}
	if err := os.WriteFile(path, []byte("not-hex"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := EnsureProxySecret(path); err == nil {
		t.Error("a malformed secret file is not an error")
	}
}

// fake4CAT answers /first-run/ as pending, done, or down (5xx).
type fake4CAT struct {
	state atomic.Value // "pending", "done", "down"
	hits  atomic.Int32
}

func newFake4CAT(t *testing.T, state string) (*fake4CAT, string) {
	t.Helper()
	f := &fake4CAT{}
	f.state.Store(state)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.hits.Add(1)
		switch f.state.Load().(string) {
		case "pending":
			_, _ = w.Write([]byte("<title>Complete 4CAT Setup</title>"))
		case "done":
			http.Redirect(w, r, "/", http.StatusFound)
		default:
			w.WriteHeader(http.StatusBadGateway)
		}
	}))
	t.Cleanup(srv.Close)
	return f, srv.URL
}

func newTestStudy(t *testing.T, state string, made []string) (*Study, *settingsDocker, *fake4CAT) {
	t.Helper()
	d := newSettingsDocker(t)
	f, url := newFake4CAT(t, state)
	c := settingsClient(d)
	c.FrontendURL = url
	root := t.TempDir()
	s := &Study{
		Client:       c,
		SecretPath:   filepath.Join(root, "secrets", "4cat-proxy-secret"),
		OncePath:     filepath.Join(root, "state", "fourcat-settings-once"),
		Sources:      []string{"tiktok-comments", "tiktok"},
		MadeAccounts: func() ([]string, error) { return made, nil },
	}
	return s, d, f
}

func TestStudyEnsure(t *testing.T) {
	ctx := context.Background()
	s, d, f := newTestStudy(t, "pending", []string{"alice", "bob"})

	if err := s.Ensure(ctx); err == nil {
		t.Fatal("Ensure while setup pending: no error")
	}
	if s.Ready() {
		t.Error("Ready after a pending setup")
	}
	if _, _, sqls := d.snapshot(); len(sqls) != 0 {
		t.Errorf("SQL ran while setup pending: %q", sqls)
	}

	f.state.Store("done")
	if err := s.Ensure(ctx); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if !s.Ready() {
		t.Error("not Ready after Ensure")
	}
	secret, err := s.Secret()
	if err != nil || len(secret) != 32 {
		t.Fatalf("Secret: length %d, err %v", len(secret), err)
	}
	if d.rows["flask.proxy_secret\x00"] != `"`+secret+`"` {
		t.Error("flask.proxy_secret row does not hold the secret")
	}
	if d.rows["datasources.enabled\x00"] != `["tiktok-comments","tiktok"]` {
		t.Errorf("datasources.enabled = %q", d.rows["datasources.enabled\x00"])
	}
	if _, err := os.Stat(s.OncePath); err != nil {
		t.Errorf("once marker not written: %v", err)
	}
	_, _, sqls := d.snapshot()
	var cleared []string
	for _, q := range sqls {
		if strings.HasPrefix(q, "UPDATE users SET tags") {
			cleared = append(cleared, q[strings.Index(q, "WHERE name = '")+14:len(q)-1])
		}
	}
	if strings.Join(cleared, ",") != "alice,bob" {
		t.Errorf("ClearAdmin ran for %q, want alice,bob", cleared)
	}

	// A second Ensure re-asserts the owned settings but not the once ones:
	// a Tech Admin's change to datasources.enabled stays.
	d.rows["datasources.enabled\x00"] = `["tiktok"]`
	d.rows["privileges.admin.can_restart\x00admin"] = "true"
	if err := s.Ensure(ctx); err != nil {
		t.Fatalf("second Ensure: %v", err)
	}
	if d.rows["datasources.enabled\x00"] != `["tiktok"]` {
		t.Error("the once setting was written again")
	}
	if d.rows["privileges.admin.can_restart\x00admin"] != "false" {
		t.Error("an owned setting was not put back")
	}
	// The admin tag is removed once: a Tech Admin's later choice stands.
	_, _, sqls = d.snapshot()
	clears := 0
	for _, q := range sqls {
		if strings.HasPrefix(q, "UPDATE users SET tags") {
			clears++
		}
	}
	if clears != 2 {
		t.Errorf("ClearAdmin ran %d times over two Ensures, want 2 (once per account)", clears)
	}
}

// TestStudyOnceStageFailure: a failure while writing the once settings
// leaves no marker and the study not ready; the retry writes them.
func TestStudyOnceStageFailure(t *testing.T) {
	ctx := context.Background()
	s, d, _ := newTestStudy(t, "done", []string{"alice"})
	d.failUpsert = "datasources.enabled"
	if err := s.Ensure(ctx); err == nil || s.Ready() {
		t.Fatalf("err %v, Ready %v; want an error and not ready", err, s.Ready())
	}
	if _, err := os.Stat(s.OncePath); !os.IsNotExist(err) {
		t.Errorf("once marker written although the once settings failed (err = %v)", err)
	}
	d.failUpsert = ""
	if err := s.Ensure(ctx); err != nil || !s.Ready() {
		t.Fatalf("retry: err %v, Ready %v", err, s.Ready())
	}
	if d.rows["datasources.enabled\x00"] == "" {
		t.Error("the once settings were not written on the retry")
	}
	if _, err := os.Stat(s.OncePath); err != nil {
		t.Errorf("once marker missing after the retry: %v", err)
	}
}

func TestStudyEnsureFailureLeavesNotReady(t *testing.T) {
	ctx := context.Background()
	s, d, _ := newTestStudy(t, "done", []string{"alice"})
	d.cacheExit = 1
	if err := s.Ensure(ctx); err == nil || s.Ready() {
		t.Errorf("cache failure: err %v, Ready %v; want an error and not ready", err, s.Ready())
	}
	s, _, _ = newTestStudy(t, "done", nil)
	s.MadeAccounts = func() ([]string, error) { return nil, errors.New("unreadable") }
	if err := s.Ensure(ctx); err == nil || s.Ready() {
		t.Errorf("MadeAccounts failure: err %v, Ready %v; want an error and not ready", err, s.Ready())
	}
	s, d, _ = newTestStudy(t, "done", []string{"alice"})
	d.Fail = map[string]error{"Exec " + dbName: errors.New("boom")}
	if err := s.Ensure(ctx); err == nil || s.Ready() {
		t.Errorf("psql failure: err %v, Ready %v; want an error and not ready", err, s.Ready())
	}
	if _, err := os.Stat(s.OncePath); !os.IsNotExist(err) {
		t.Errorf("once marker written although the settings failed (err = %v)", err)
	}
}

func TestStudySetupPendingRemembersDone(t *testing.T) {
	ctx := context.Background()
	s, _, f := newTestStudy(t, "down", nil)
	if _, err := s.SetupPending(ctx); err == nil {
		t.Error("unreachable before any answer: no error")
	}
	f.state.Store("pending")
	if p, err := s.SetupPending(ctx); !p || err != nil {
		t.Errorf("pending: %v, %v; want true, nil", p, err)
	}
	f.state.Store("done")
	if p, err := s.SetupPending(ctx); p || err != nil {
		t.Errorf("done: %v, %v; want false, nil", p, err)
	}
	f.state.Store("down")
	hits := f.hits.Load()
	if p, err := s.SetupPending(ctx); p || err != nil {
		t.Errorf("down after done: %v, %v; want false, nil", p, err)
	}
	if f.hits.Load() != hits {
		t.Error("4CAT was asked again after the setup was seen done")
	}
}

func TestStudyRunRetriesUntilReady(t *testing.T) {
	s, d, _ := newTestStudy(t, "done", nil)
	var attempts atomic.Int32
	s.MadeAccounts = func() ([]string, error) {
		if attempts.Add(1) < 3 {
			return nil, errors.New("not yet")
		}
		return nil, nil
	}
	done := make(chan struct{})
	go func() {
		s.Run(context.Background(), time.Millisecond)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after a successful Ensure")
	}
	if n := attempts.Load(); n != 3 {
		t.Errorf("attempts = %d, want 3", n)
	}
	if !s.Ready() {
		t.Error("not Ready after Run")
	}
	_ = d

	// A cancelled context ends Run without success.
	s2, d2, _ := newTestStudy(t, "done", nil)
	d2.cacheExit = 1
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	s2.Run(ctx, time.Hour)
	if s2.Ready() {
		t.Error("Ready after a cancelled Run")
	}
}
