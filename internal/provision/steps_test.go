package provision

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/daniellemccool/researchcloud-ddp-comments/internal/fourcat"
)

func TestStepsRunInOrderAndPersist(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()

	st := env.prov.Advance(ctx, "alice", false)
	if !st.Ready() {
		t.Fatalf("not ready: steps = %v", st.Steps)
	}
	for _, step := range Steps {
		if st.Steps[step] != "done" {
			t.Errorf("step %q = %q, want done", step, st.Steps[step])
		}
	}
	if st.AppliedGeneration != generation {
		t.Errorf("AppliedGeneration = %q, want %q", st.AppliedGeneration, generation)
	}
	if st.Updated <= 0 {
		t.Errorf("Updated = %d, want a unix timestamp", st.Updated)
	}

	// The step file: 0600, and Load round-trips the returned state.
	path := StatePath(env.cfg, "alice")
	if got := statMode(t, path); got != 0o600 {
		t.Errorf("provision.json mode %v, want 0600", got)
	}
	loaded := Load(env.cfg, "alice")
	if !reflect.DeepEqual(loaded, st) {
		t.Errorf("Load round-trip = %+v, want %+v", loaded, st)
	}

	// The steps' effects on disk.
	root := env.root
	modes := map[string]os.FileMode{
		filepath.Join(root, "state", "firefox", "alice"):                     0o755,
		filepath.Join(root, "state", "proxy", "alice"):                       0o700,
		filepath.Join(root, "state", "captures", "alice"):                    0o700,
		filepath.Join(root, "firefox", "alice"):                              0o755,
		filepath.Join(root, "state", "users", "alice"):                       0o700,
		filepath.Join(root, "state", "proxy", "alice", "ca.pem"):             0o644,
		filepath.Join(root, "state", "proxy", "alice", "ca-key.pem"):         0o600,
		filepath.Join(root, "secrets", "4cat-passwords"):                     0o700,
		filepath.Join(root, "secrets", "4cat-passwords", "alice"):            0o600,
		filepath.Join(root, "secrets", "4cat-tokens", "alice"):               0o750,
		filepath.Join(root, "secrets", "4cat-tokens", "alice", "4cat-token"): 0o600,
	}
	for path, want := range modes {
		if got := statMode(t, path); got != want {
			t.Errorf("%s mode %v, want %v", path, got, want)
		}
	}
	if got := readFile(t, filepath.Join(root, "state", "firefox", "alice", "profile", "prefs.js")); got == "" {
		t.Error("profile/prefs.js was not copied from the release template")
	}
	if got := readFile(t, filepath.Join(root, "secrets", "4cat-tokens", "alice", "4cat-token")); got != "tok-alice" {
		t.Errorf("4cat-token = %q, want %q", got, "tok-alice")
	}
	for _, p := range []string{
		filepath.Join(root, "firefox", "alice", "policies.json"),
		filepath.Join(root, "state", "users", "alice", "pair.json"),
	} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("render step did not write %s: %v", p, err)
		}
	}
	live, err := env.reg.Live()
	if err != nil || live["alice"].Index != 1 {
		t.Errorf("Live() = %v, %v; want alice at index 1", live, err)
	}
}

func TestErrorStopsAndRetriesThatStep(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	env.setFlags(false, true, false, false) // login refused

	st := env.prov.Advance(ctx, "alice", false)
	if st.Ready() {
		t.Fatal("ready despite a refused login")
	}
	if got := st.Steps["fourcat_token"]; got != "error: login refused for alice" {
		t.Errorf("fourcat_token = %q, want %q", got, "error: login refused for alice")
	}
	if _, ok := st.Steps["render"]; ok {
		t.Errorf("render step recorded %q after the token step failed", st.Steps["render"])
	}
	if st.Steps["fourcat_account"] != "done" {
		t.Errorf("fourcat_account = %q, want done", st.Steps["fourcat_account"])
	}
	if st.Current() != "fourcat_token" {
		t.Errorf("Current() = %q, want fourcat_token", st.Current())
	}
	if st.Error() != "error: login refused for alice" {
		t.Errorf("Error() = %q", st.Error())
	}

	env.setFlags(false, false, false, false) // login works now
	st2 := env.prov.Advance(ctx, "alice", false)
	if !st2.Ready() {
		t.Fatalf("second Advance not ready: %v", st2.Steps)
	}
	if n := env.creates(); n != 1 {
		t.Errorf("create_user.py execs = %d, want 1 (fourcat_account must not run again)", n)
	}
}

func TestUnreadableStateStartsOver(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()

	path := StatePath(env.cfg, "alice")
	writeFile(t, path, "this is not json {{{")
	if st := Load(env.cfg, "alice"); len(st.Steps) != 0 || st.Ready() || st.AppliedGeneration != "" || st.Updated != 0 {
		t.Errorf("Load(garbage) = %+v, want the zero State", st)
	}

	// Wrongly shaped: steps is a list, not a map.
	writeFile(t, path, `{"steps": ["registry"], "applied_generation": "x", "updated": 5}`)
	if st := Load(env.cfg, "alice"); len(st.Steps) != 0 {
		t.Errorf("Load(steps as list) = %+v, want the zero State", st)
	}

	// Unknown step names are dropped, known ones kept.
	writeFile(t, path, `{"steps": {"registry": "done", "bogus": "done", "dirs": "error: old"}, "applied_generation": "g", "updated": 7}`)
	st := Load(env.cfg, "alice")
	if len(st.Steps) != 2 || st.Steps["registry"] != "done" || st.Steps["dirs"] != "error: old" {
		t.Errorf("Load(unknown steps) = %+v, want registry+dirs only", st.Steps)
	}
	if st.AppliedGeneration != "g" || st.Updated != 7 {
		t.Errorf("Load = gen %q updated %d, want g/7", st.AppliedGeneration, st.Updated)
	}

	// A garbage file does not stop provisioning: every step is idempotent.
	writeFile(t, path, "garbage again")
	if st := env.prov.Advance(ctx, "alice", false); !st.Ready() {
		t.Fatalf("Advance after garbage state not ready: %v", st.Steps)
	}
}

func TestRegistryFullText(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	env.reg.MaxSessions = 1

	if st := env.prov.Advance(ctx, "alice", false); !st.Ready() {
		t.Fatalf("alice not ready: %v", st.Steps)
	}
	st := env.prov.Advance(ctx, "bob", false)
	if st.Ready() {
		t.Fatal("bob ready with a full registry")
	}
	if st.Error() != RegistryFull {
		t.Errorf("Error() = %q, want %q", st.Error(), RegistryFull)
	}
	if st.Steps["registry"] != RegistryFull {
		t.Errorf("registry step = %q, want %q", st.Steps["registry"], RegistryFull)
	}
	if !strings.Contains(RegistryFull, "registry full") {
		t.Errorf("RegistryFull = %q", RegistryFull)
	}
}

func TestTemplateIdempotent(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	if st := env.prov.Advance(ctx, "alice", false); !st.Ready() {
		t.Fatalf("not ready: %v", st.Steps)
	}

	profile := filepath.Join(env.root, "state", "firefox", "alice", "profile")
	marker := filepath.Join(profile, "marker.txt")
	writeFile(t, marker, "collector data")
	// A stale staging dir left by a crashed run.
	stale := filepath.Join(env.root, "state", "firefox", "alice", ".template.tmp")
	writeFile(t, filepath.Join(stale, "junk"), "junk")

	st := env.prov.Advance(ctx, "alice", false)
	if !st.Ready() {
		t.Fatalf("second Advance not ready: %v", st.Steps)
	}
	if got := readFile(t, marker); got != "collector data" {
		t.Errorf("marker file = %q, want it to survive the second Advance", got)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Errorf("stale .template.tmp still present (err = %v)", err)
	}
}

func TestTemplateMissing(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	if err := os.RemoveAll(filepath.Join(env.root, "templates", "release", generation)); err != nil {
		t.Fatal(err)
	}

	st := env.prov.Advance(ctx, "alice", false)
	if st.Ready() {
		t.Fatal("ready without a release template")
	}
	if got := st.Steps["template"]; got != "error: template missing" {
		t.Errorf("template step = %q, want %q", got, "error: template missing")
	}
	if st.Current() != "template" || st.Error() != "error: template missing" {
		t.Errorf("Current() = %q Error() = %q", st.Current(), st.Error())
	}
	if st.Steps["registry"] != "done" || st.Steps["dirs"] != "done" {
		t.Errorf("earlier steps = %v", st.Steps)
	}
}

func TestHideMediaCSS(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	if st := env.prov.Advance(ctx, "alice", false); !st.Ready() {
		t.Fatalf("not ready: %v", st.Steps)
	}
	css := filepath.Join(env.root, "state", "firefox", "alice", "profile", "chrome", "userContent.css")
	if got := readFile(t, css); got != "/* hide media */\n" {
		t.Errorf("userContent.css = %q, want the platform stylesheet", got)
	}
	if got := statMode(t, css); got != 0o644 {
		t.Errorf("userContent.css mode %v, want 0644", got)
	}

	// HideMedia off: the stylesheet is removed on the next run of the step.
	env.cfg.HideMedia = false
	if err := os.Remove(StatePath(env.cfg, "alice")); err != nil {
		t.Fatal(err)
	}
	if st := env.prov.Advance(ctx, "alice", false); !st.Ready() {
		t.Fatalf("second Advance not ready: %v", st.Steps)
	}
	if _, err := os.Stat(css); !os.IsNotExist(err) {
		t.Errorf("userContent.css still present with HideMedia off (err = %v)", err)
	}
}

func TestFourcatSetupPending(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	env.setFlags(true, false, false, false) // setup page still served

	st := env.prov.Advance(ctx, "alice", false)
	want := "error: " + fourcat.ErrSetupPending.Error()
	if got := st.Steps["fourcat_account"]; got != want {
		t.Errorf("fourcat_account = %q, want %q", got, want)
	}
	if st.Error() != want {
		t.Errorf("Error() = %q, want %q", st.Error(), want)
	}

	env.setFlags(false, false, false, false) // an admin completed setup
	st2 := env.prov.Advance(ctx, "alice", false)
	if !st2.Ready() {
		t.Fatalf("second Advance not ready: %v", st2.Steps)
	}
	if n := env.creates(); n != 1 {
		t.Errorf("create_user.py execs = %d, want exactly 1", n)
	}
}

func TestNeedsAdoptRemovesFreshPassword(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	env.setFlags(false, false, false, true) // account already exists, no password file

	st := env.prov.Advance(ctx, "alice", false)
	want := "error: " + fourcat.ErrNeedsAdopt.Error()
	if got := st.Steps["fourcat_account"]; got != want {
		t.Errorf("fourcat_account = %q, want %q", got, want)
	}
	if _, err := os.Stat(PasswordPath(env.cfg, "alice")); !os.IsNotExist(err) {
		t.Errorf("fresh password file left behind (err = %v)", err)
	}
	if n := env.creates(); n != 0 {
		t.Errorf("create_user.py execs = %d, want 0 (existing account is never reset)", n)
	}
}

func TestAdminTagFollowsRole(t *testing.T) {
	for _, admin := range []bool{true, false} {
		t.Run(fmtBool(admin), func(t *testing.T) {
			env := newTestEnv(t)
			st := env.prov.Advance(context.Background(), "alice", admin)
			if !st.Ready() {
				t.Fatalf("not ready: %v", st.Steps)
			}
			var create []string
			for _, call := range env.fake.Execs {
				if len(call.Cmd) > 0 && call.Cmd[0] == "python3" {
					create = call.Cmd
				}
			}
			if len(create) == 0 {
				t.Fatal("no create_user.py exec recorded")
			}
			hasFlag := false
			for _, a := range create {
				if a == "-a" {
					hasFlag = true
				}
			}
			if hasFlag != admin {
				t.Errorf("create argv %v: -a present = %v, want %v", create, hasFlag, admin)
			}
			var update string
			for _, sql := range env.sqlLog() {
				if strings.HasPrefix(sql, "UPDATE users SET tags") {
					update = sql
				}
			}
			if update == "" {
				t.Fatal("no admin-tag UPDATE recorded")
			}
			if admin && !strings.Contains(update, `'["admin"]'::jsonb`) {
				t.Errorf("admin UPDATE = %q, want the tag added", update)
			}
			if !admin && strings.Contains(update, "::jsonb") {
				t.Errorf("non-admin UPDATE = %q, want the tag removed", update)
			}
		})
	}
}

func fmtBool(b bool) string {
	if b {
		return "admin"
	}
	return "collector"
}

func TestTokenFileModes(t *testing.T) {
	env := newTestEnv(t)
	if st := env.prov.Advance(context.Background(), "alice", false); !st.Ready() {
		t.Fatalf("not ready: %v", st.Steps)
	}
	tokenDir := filepath.Join(env.root, "secrets", "4cat-tokens", "alice")
	token := filepath.Join(tokenDir, "4cat-token")
	if got := statMode(t, tokenDir); got != 0o750 {
		t.Errorf("token dir mode %v, want 0750", got)
	}
	if got := statMode(t, token); got != 0o600 {
		t.Errorf("token file mode %v, want 0600", got)
	}
	if got := statMode(t, PasswordPath(env.cfg, "alice")); got != 0o600 {
		t.Errorf("password file mode %v, want 0600", got)
	}
	if os.Geteuid() == 0 {
		if uid := statUID(t, tokenDir); uid != 1000 {
			t.Errorf("token dir uid %d, want 1000", uid)
		}
		if uid := statUID(t, token); uid != 1000 {
			t.Errorf("token file uid %d, want 1000", uid)
		}
		if uid := statUID(t, filepath.Join(env.root, "state", "proxy", "alice", "ca.pem")); uid != 1000 {
			t.Errorf("ca.pem uid %d, want 1000", uid)
		}
		if uid := statUID(t, filepath.Join(env.root, "state", "firefox", "alice")); uid != 1000 {
			t.Errorf("state/firefox/alice uid %d, want 1000", uid)
		}
	}
}

func TestAdvanceConcurrentSameUser(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()

	const n = 10
	var wg sync.WaitGroup
	results := make([]State, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i] = env.prov.Advance(ctx, "alice", false)
		}(i)
	}
	wg.Wait()
	for i, st := range results {
		if !st.Ready() {
			t.Errorf("goroutine %d not ready: %v", i, st.Steps)
		}
	}
	live, err := env.reg.Live()
	if err != nil || len(live) != 1 {
		t.Errorf("Live() = %v, %v; want exactly one row", live, err)
	}
	if got := env.creates(); got != 1 {
		t.Errorf("create_user.py execs = %d, want 1", got)
	}
	if got := env.loginCount(); got != 1 {
		t.Errorf("login calls = %d, want 1 (one token write)", got)
	}
}

func TestFourcatDisabledSkipsSteps(t *testing.T) {
	env := newTestEnv(t)
	env.prov.Fourcat = nil

	st := env.prov.Advance(context.Background(), "alice", false)
	if !st.Ready() {
		t.Fatalf("not ready: %v", st.Steps)
	}
	if st.Steps["fourcat_account"] != "done" || st.Steps["fourcat_token"] != "done" {
		t.Errorf("fourcat steps = %q, %q; want both done", st.Steps["fourcat_account"], st.Steps["fourcat_token"])
	}
	if n := len(env.fake.Execs); n != 0 {
		t.Errorf("docker execs = %d, want 0 with 4CAT disabled", n)
	}
	if _, err := os.Stat(filepath.Join(env.root, "secrets", "4cat-tokens", "alice", "4cat-token")); !os.IsNotExist(err) {
		t.Errorf("token file written with 4CAT disabled (err = %v)", err)
	}
}

func TestUnknownErrorRecordsTypeOnly(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	env.setFlags(false, false, true, false) // /login/ drops the connection

	st := env.prov.Advance(ctx, "alice", false)
	got := st.Steps["fourcat_token"]
	if got != "error: *errors.errorString" {
		t.Errorf("fourcat_token = %q, want %q (type name only)", got, "error: *errors.errorString")
	}
	raw := readFile(t, StatePath(env.cfg, "alice"))
	if strings.Contains(raw, "unreachable") || strings.Contains(raw, "url.Error") {
		t.Errorf("state file leaks the error message: %s", raw)
	}
}
