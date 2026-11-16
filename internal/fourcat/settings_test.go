package fourcat

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/daniellemccool/researchcloud-ddp-comments/internal/docker"
)

const memcachedName = "desk-4cat-memcached-1"

// settingsDocker wraps the Fake with a small settings table and memcached:
// SELECT reads rows, INSERT … ON CONFLICT upserts them, UPDATE users SET
// tags answers updateTags, and nc in the memcached container answers
// cacheAnswer (or fails with cacheExit) and records the deleted keys.
type settingsDocker struct {
	*docker.Fake
	mu          sync.Mutex
	rows        map[string]string // name + "\x00" + tag → value
	upserts     []string          // name + "\x00" + tag, in order
	deleted     []string          // memcached keys, in order
	sqls        []string
	cacheAnswer string
	cacheExit   int
	updateTags  string
	failUpsert  string // a setting name whose upsert fails
}

var (
	selectRe = regexp.MustCompile(`^SELECT value FROM settings WHERE name = '([^']*)' AND tag = '([^']*)'$`)
	upsertRe = regexp.MustCompile(`^INSERT INTO settings \(name, value, tag\) VALUES \('([^']*)', '([^']*)', '([^']*)'\) ON CONFLICT \(name, tag\) DO UPDATE SET value = EXCLUDED\.value$`)
	deleteRe = regexp.MustCompile(`^delete (\S+)\r\nquit\r\n$`)
)

func newSettingsDocker(t *testing.T) *settingsDocker {
	t.Helper()
	f := newFakeWith4CAT(t)
	ctx := context.Background()
	spec := docker.ContainerSpec{Name: memcachedName, Image: "memcached:alpine", Labels: withService("4cat-memcached")}
	if _, err := f.ContainerCreate(ctx, spec); err != nil {
		t.Fatal(err)
	}
	if err := f.ContainerStart(ctx, memcachedName); err != nil {
		t.Fatal(err)
	}
	return &settingsDocker{Fake: f, rows: map[string]string{}, cacheAnswer: "DELETED\r\n", updateTags: "UPDATE 1\n"}
}

func (d *settingsDocker) Exec(ctx context.Context, container string, cmd []string, env []string, stdin []byte) (docker.Exec, error) {
	if _, err := d.Fake.Exec(ctx, container, cmd, env, stdin); err != nil {
		return docker.Exec{}, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	switch {
	case len(cmd) > 0 && cmd[0] == "psql":
		sql := cmd[len(cmd)-1]
		d.sqls = append(d.sqls, sql)
		if m := selectRe.FindStringSubmatch(sql); m != nil {
			return docker.Exec{Stdout: d.rows[m[1]+"\x00"+m[2]] + "\n"}, nil
		}
		if m := upsertRe.FindStringSubmatch(sql); m != nil {
			if m[1] == d.failUpsert {
				return docker.Exec{ExitCode: 1}, nil
			}
			d.rows[m[1]+"\x00"+m[3]] = m[2]
			d.upserts = append(d.upserts, m[1]+"\x00"+m[3])
			return docker.Exec{Stdout: "INSERT 0 1\n"}, nil
		}
		if strings.HasPrefix(sql, "UPDATE users SET tags") {
			return docker.Exec{Stdout: d.updateTags}, nil
		}
		return docker.Exec{ExitCode: 3, Stderr: "unexpected SQL"}, nil
	case len(cmd) > 0 && cmd[0] == "nc":
		if container != memcachedName {
			return docker.Exec{ExitCode: 9}, nil
		}
		if d.cacheExit != 0 {
			return docker.Exec{ExitCode: d.cacheExit}, nil
		}
		m := deleteRe.FindSubmatch(stdin)
		if m == nil {
			return docker.Exec{Stdout: "ERROR\r\n"}, nil
		}
		d.deleted = append(d.deleted, string(m[1]))
		return docker.Exec{Stdout: d.cacheAnswer}, nil
	}
	return docker.Exec{}, nil
}

func (d *settingsDocker) snapshot() (upserts, deleted, sqls []string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.upserts...), append([]string(nil), d.deleted...), append([]string(nil), d.sqls...)
}

func settingsClient(d *settingsDocker) *Client {
	return &Client{Docker: d, Backend: backendName, DB: dbName}
}

func TestEnsureSettingsWritesOnlyDifferences(t *testing.T) {
	d := newSettingsDocker(t)
	d.rows["a.right\x00studylead"] = "true"
	d.rows["a.list\x00"] = `["x", "y"]` // 4CAT's own spelling of the same JSON
	d.rows["a.wrong\x00"] = "true"
	c := settingsClient(d)
	err := c.EnsureSettings(context.Background(), []Setting{
		{Name: "a.right", Tag: "studylead", Value: "true"},
		{Name: "a.list", Value: `["x","y"]`},
		{Name: "a.wrong", Value: "false"},
		{Name: "a.missing", Tag: "techadmin", Value: "true"},
	})
	if err != nil {
		t.Fatalf("EnsureSettings: %v", err)
	}
	upserts, _, _ := d.snapshot()
	want := []string{"a.wrong\x00", "a.missing\x00techadmin"}
	if fmt.Sprint(upserts) != fmt.Sprint(want) {
		t.Errorf("upserts = %q, want %q", upserts, want)
	}
	if d.rows["a.wrong\x00"] != "false" || d.rows["a.missing\x00techadmin"] != "true" {
		t.Errorf("rows after = %v", d.rows)
	}
}

func TestEnsureSettingsAlwaysClearsCacheKeys(t *testing.T) {
	d := newSettingsDocker(t)
	d.rows["flask.proxy_secret\x00"] = `"s"`
	d.rows["privileges.admin.can_restart\x00admin"] = "false"
	c := settingsClient(d)
	err := c.EnsureSettings(context.Background(), []Setting{
		{Name: "flask.proxy_secret", Value: `"s"`},
		{Name: "privileges.admin.can_restart", Tag: "admin", Value: "false"},
	})
	if err != nil {
		t.Fatalf("EnsureSettings: %v", err)
	}
	upserts, deleted, _ := d.snapshot()
	if len(upserts) != 0 {
		t.Errorf("upserts = %q, want none (rows already right)", upserts)
	}
	want := []string{"4cat-configflask.proxy_secret", "4cat-configprivileges.admin.can_restart-admin"}
	if fmt.Sprint(deleted) != fmt.Sprint(want) {
		t.Errorf("cache keys deleted = %q, want %q", deleted, want)
	}
}

func TestEnsureSettingsCacheFailureIsAnError(t *testing.T) {
	s := []Setting{{Name: "a.b", Value: "true"}}
	for _, answer := range []string{"DELETED\r\n", "NOT_FOUND\r\n"} {
		d := newSettingsDocker(t)
		d.cacheAnswer = answer
		if err := settingsClient(d).EnsureSettings(context.Background(), s); err != nil {
			t.Errorf("memcached %q: err = %v, want nil", answer, err)
		}
	}
	d := newSettingsDocker(t)
	d.cacheAnswer = "ERROR\r\n"
	if err := settingsClient(d).EnsureSettings(context.Background(), s); err == nil {
		t.Error("memcached ERROR: no error")
	}
	d = newSettingsDocker(t)
	d.cacheExit = 1
	if err := settingsClient(d).EnsureSettings(context.Background(), s); err == nil {
		t.Error("nc failing: no error")
	}
}

func TestEnsureSettingsRefusesOddInput(t *testing.T) {
	for _, s := range []Setting{
		{Name: "a'b", Value: "true"},
		{Name: "", Value: "true"},
		{Name: "a.b", Tag: "study-lead", Value: "true"},
		{Name: "a.b", Value: `"it's"`},
		{Name: "a.b", Value: "tru"},
		{Name: "a.b", Value: ""},
	} {
		d := newSettingsDocker(t)
		err := settingsClient(d).EnsureSettings(context.Background(), []Setting{{Name: "ok.one", Value: "true"}, s})
		if err == nil {
			t.Errorf("%+v: no error", s)
		}
		if _, _, sqls := d.snapshot(); len(sqls) != 0 || len(d.Execs) != 0 {
			t.Errorf("%+v: %d execs ran before the input was checked", s, len(d.Execs))
		}
	}
}

func TestClearAdminDeletesCachedTags(t *testing.T) {
	for _, answer := range []string{"UPDATE 1\n", "UPDATE 0\n"} {
		d := newSettingsDocker(t)
		d.updateTags = answer
		if err := settingsClient(d).ClearAdmin(context.Background(), "alice"); err != nil {
			t.Fatalf("%q: ClearAdmin: %v", answer, err)
		}
		_, deleted, sqls := d.snapshot()
		wantSQL := `UPDATE users SET tags = (CASE WHEN jsonb_typeof(tags) = 'array' THEN tags - 'admin' ELSE '[]'::jsonb END) WHERE name = 'alice'`
		if len(sqls) != 1 || sqls[0] != wantSQL {
			t.Errorf("%q: SQL = %q, want %q", answer, sqls, wantSQL)
		}
		if fmt.Sprint(deleted) != "[4cat-config_usertags-alice]" {
			t.Errorf("%q: cache keys deleted = %q", answer, deleted)
		}
	}
	d := newSettingsDocker(t)
	if err := settingsClient(d).ClearAdmin(context.Background(), "a'b"); err == nil || len(d.Execs) != 0 {
		t.Errorf("bad username: err %v, %d execs", err, len(d.Execs))
	}
	d = newSettingsDocker(t)
	d.Fail = map[string]error{"Exec " + dbName: errors.New("boom")}
	if err := settingsClient(d).ClearAdmin(context.Background(), "alice"); err == nil {
		t.Error("psql failing: no error")
	}
}
