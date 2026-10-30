package template

import (
	"archive/tar"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/daniellemccool/researchcloud-ddp-comments/internal/platform"
	_ "modernc.org/sqlite"
)

const (
	extUUID    = "11111111-aaaa-bbbb-cccc-222222222222"
	extDirName = "moz-extension+++" + extUUID
	idbName    = "2612651559zsemeestcih-uriem.sqlite"
	otherUUID  = "99999999-dddd-eeee-ffff-000000000000"
)

func loadTikTok(t *testing.T) *platform.Platform {
	t.Helper()
	p, err := platform.Load(filepath.Join("..", "..", "platforms", "tiktok", "platform.json"))
	if err != nil {
		t.Fatalf("platform.Load: %v", err)
	}
	return p
}

// fixtureOpts varies the built fixture profile.
type fixtureOpts struct {
	items, uploads, nav, settings int
	secondExt                     bool // a second extension dir holding the four stores
	noExt                         bool // no moz-extension directory at all
	noCookies                     bool // no cookies.sqlite
	lock, parentlock, wal         bool
}

// buildProfile writes a fixture Firefox profile into dir.
func buildProfile(t *testing.T, dir string, o fixtureOpts) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if !o.noCookies {
		writeCookies(t, filepath.Join(dir, "cookies.sqlite"))
	}
	if !o.noExt {
		writeIDB(t, filepath.Join(dir, "storage", "default", extDirName, "idb", idbName),
			o.items, o.uploads, o.nav, o.settings)
		if o.secondExt {
			writeIDB(t, filepath.Join(dir, "storage", "default", "moz-extension+++"+otherUUID, "idb", "1.sqlite"),
				0, 0, 0, 1)
		}
		if o.wal {
			p := filepath.Join(dir, "storage", "default", extDirName, "idb", idbName+"-wal")
			if err := os.WriteFile(p, []byte("wal"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	prefs := strings.Join([]string{
		`user_pref("extensions.webextensions.uuids", "{\"{677bb78c-7168-4aa5-8e9b-32e9f26c6157}\":\"` + extUUID + `\"}");`,
		`user_pref("extensions.webextensions.ExtensionStorageIDB.migrated.{677bb78c-7168-4aa5-8e9b-32e9f26c6157}", true);`,
		`user_pref("extensions.databaseSchema", 37);`,
		`user_pref("extensions.lastAppVersion", "151.0.3");`,
		`user_pref("extensions.lastPlatformVersion", "151.0.3");`,
		`user_pref("extensions.pendingOperations", false);`,
		`user_pref("toolkit.legacyUserProfileCustomizations.stylesheets", true);`,
		`user_pref("browser.startup.homepage", "https://www.tiktok.com/");`,
		`user_pref("privacy.trackingprotection.enabled", true);`,
		``,
	}, "\n")
	if err := os.WriteFile(filepath.Join(dir, "prefs.js"), []byte(prefs), 0o644); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"places.sqlite":                      "junk",
		"extensions.json":                    `{"schemaVersion":35,"addons":[]}`,
		"extension-preferences.json":         `{}`,
		"extensions/zeeschuimer@example.xpi": "xpi-bytes",
		"chrome/userContent.css":             "/* hide media */",
	}
	for rel, content := range files {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if o.lock {
		if err := os.WriteFile(filepath.Join(dir, "lock"), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if o.parentlock {
		if err := os.WriteFile(filepath.Join(dir, ".parentlock"), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func writeCookies(t *testing.T, path string) {
	t.Helper()
	db := openSQL(t, path)
	exec(t, db, `CREATE TABLE moz_cookies (
		id INTEGER PRIMARY KEY, originAttributes TEXT NOT NULL DEFAULT '', name TEXT NOT NULL,
		value TEXT NOT NULL, host TEXT NOT NULL, path TEXT NOT NULL DEFAULT '/', expiry INTEGER NOT NULL,
		lastAccessed INTEGER NOT NULL, creationTime INTEGER NOT NULL, isSecure INTEGER NOT NULL,
		isHttpOnly INTEGER NOT NULL, inBrowserElement INTEGER NOT NULL DEFAULT 0, sameSite INTEGER NOT NULL DEFAULT 0,
		rawSameSite INTEGER NOT NULL DEFAULT 0, schemeMap INTEGER NOT NULL DEFAULT 0)`)
	rows := [][2]string{
		{".tiktok.com", "cookie-consent"},
		{"www.tiktok.com", "cookie-consent"},
		{".tiktok.com", "tt_webid"},
	}
	for i, r := range rows {
		exec(t, db, `INSERT INTO moz_cookies (id, name, value, host, path, expiry, lastAccessed, creationTime, isSecure, isHttpOnly) VALUES (?, ?, 'v', ?, '/', 1, 1, 1, 0, 0)`,
			i+1, r[1], r[0])
	}
}

// writeIDB writes a Firefox-style IndexedDB sqlite file with the four
// Zeeschuimer object stores and the given row counts.
func writeIDB(t *testing.T, path string, items, uploads, nav, settings int) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	db := openSQL(t, path)
	exec(t, db, "CREATE TABLE database (id INTEGER PRIMARY KEY, name TEXT NOT NULL)")
	exec(t, db, "INSERT INTO database (name) VALUES ('zeeschuimer-items')")
	exec(t, db, "CREATE TABLE object_store (id INTEGER PRIMARY KEY, name TEXT NOT NULL)")
	exec(t, db, "CREATE TABLE object_data (id INTEGER PRIMARY KEY, object_store_id INTEGER NOT NULL, key BLOB, file_ids TEXT, data BLOB)")
	exec(t, db, "CREATE TABLE index_data (id INTEGER PRIMARY KEY, object_store_id INTEGER NOT NULL, index_id INTEGER NOT NULL, value BLOB)")
	exec(t, db, "CREATE TABLE unique_index_data (id INTEGER PRIMARY KEY, object_store_id INTEGER NOT NULL, index_id INTEGER NOT NULL, value BLOB)")
	// Firefox's trigger calls the browser-internal update_refcount function,
	// which plain SQLite does not have.
	exec(t, db, `CREATE TRIGGER object_data_delete_trigger AFTER DELETE ON object_data FOR EACH ROW WHEN OLD.file_ids IS NOT NULL BEGIN SELECT update_refcount(OLD.file_ids, NULL); END`)
	stores := []struct {
		id, n int
		name  string
	}{
		{1, items, "items"},
		{2, uploads, "uploads"},
		{3, nav, "nav"},
		{4, settings, "settings"},
	}
	next := 1
	for _, s := range stores {
		exec(t, db, "INSERT INTO object_store (id, name) VALUES (?, ?)", s.id, s.name)
		for i := 0; i < s.n; i++ {
			exec(t, db, "INSERT INTO object_data (id, object_store_id, data) VALUES (?, ?, ?)", next, s.id, []byte("row"))
			if s.name == "nav" {
				exec(t, db, "INSERT INTO index_data (id, object_store_id, index_id, value) VALUES (?, ?, 1, ?)", next, s.id, []byte("idx"))
				exec(t, db, "INSERT INTO unique_index_data (id, object_store_id, index_id, value) VALUES (?, ?, 1, ?)", next, s.id, []byte("uidx"))
			}
			next++
		}
	}
}

func openSQL(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("sql.Open %s: %v", path, err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func exec(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := db.Exec(query, args...); err != nil {
		t.Fatalf("exec %q: %v", query, err)
	}
}

// walkPaths returns the sorted relative paths of all files and directories
// under dir.
func walkPaths(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		if rel != "." {
			out = append(out, filepath.ToSlash(rel))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(out)
	return out
}

// treeHash hashes the whole tree under dir (paths and file contents).
func treeHash(t *testing.T, dir string) string {
	t.Helper()
	h := sha256.New()
	for _, rel := range walkPaths(t, dir) {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		info, err := os.Lstat(p)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(h, rel); err != nil {
			t.Fatal(err)
		}
		if info.Mode().IsRegular() {
			data, err := os.ReadFile(p)
			if err != nil {
				t.Fatal(err)
			}
			h.Write(data)
		}
	}
	return hex.EncodeToString(h.Sum(nil))
}

func newWorkProfile(t *testing.T, o fixtureOpts) string {
	t.Helper()
	dir := t.TempDir()
	work := filepath.Join(dir, "profile")
	buildProfile(t, work, o)
	return work
}

func wantPaths() []string {
	return []string{
		"chrome",
		"chrome/userContent.css",
		"cookies.sqlite",
		"extension-preferences.json",
		"extensions",
		"extensions.json",
		"extensions/zeeschuimer@example.xpi",
		"prefs.js",
		"storage",
		"storage/default",
		"storage/default/" + extDirName,
		"storage/default/" + extDirName + "/idb",
		"storage/default/" + extDirName + "/idb/" + idbName,
	}
}

func TestSanitiseKeepsAllowlistOnly(t *testing.T) {
	work := newWorkProfile(t, fixtureOpts{settings: 1})
	p := loadTikTok(t)
	if err := Sanitise(work, p); err != nil {
		t.Fatalf("Sanitise: %v", err)
	}
	got := walkPaths(t, work)
	want := wantPaths()
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("paths after Sanitise:\n got %v\nwant %v", got, want)
	}
	data, err := os.ReadFile(filepath.Join(work, "prefs.js"))
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(strings.TrimRight(string(data), "\n"), "\n") {
		m := prefLineRe.FindStringSubmatch(strings.TrimSpace(line))
		if m == nil || !allowPrefs[m[1]] {
			t.Errorf("prefs.js kept a non-allowlisted line: %q", line)
		}
	}
	if len(strings.Split(strings.TrimRight(string(data), "\n"), "\n")) != len(allowPrefs) {
		t.Errorf("prefs.js holds %d lines, want %d", len(strings.Split(strings.TrimRight(string(data), "\n"), "\n")), len(allowPrefs))
	}
}

func TestCookiesFiltered(t *testing.T) {
	work := newWorkProfile(t, fixtureOpts{settings: 1})
	p := loadTikTok(t)
	if err := Sanitise(work, p); err != nil {
		t.Fatalf("Sanitise: %v", err)
	}
	db := openSQL(t, filepath.Join(work, "cookies.sqlite"))
	rows, err := db.Query("SELECT host, name FROM moz_cookies ORDER BY host, name")
	if err != nil {
		t.Fatal(err)
	}
	var got [][2]string
	for rows.Next() {
		var host, name string
		if err := rows.Scan(&host, &name); err != nil {
			t.Fatal(err)
		}
		got = append(got, [2]string{host, name})
	}
	_ = rows.Close()
	want := [][2]string{{".tiktok.com", "cookie-consent"}, {"www.tiktok.com", "cookie-consent"}}
	if len(got) != len(want) {
		t.Fatalf("cookies = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("cookies = %v, want %v", got, want)
		}
	}
}

func TestVerifyCleanProfilePasses(t *testing.T) {
	work := newWorkProfile(t, fixtureOpts{settings: 1})
	counts, err := Verify(work)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if counts != (Counts{Items: 0, Uploads: 0, Nav: 0, Settings: 1}) {
		t.Errorf("counts = %+v, want {0 0 0 1}", counts)
	}
}

func TestVerifyFailures(t *testing.T) {
	cases := []struct {
		name    string
		opts    fixtureOpts
		wantErr string
	}{
		{"items", fixtureOpts{items: 1, settings: 1}, "template has captured items"},
		{"uploads", fixtureOpts{uploads: 2, settings: 1}, "template has upload history"},
		{"nav", fixtureOpts{nav: 1, settings: 1}, "template has navigation history"},
		{"no settings", fixtureOpts{}, "template has no settings (modules were never enabled)"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			work := newWorkProfile(t, tc.opts)
			_, err := Verify(work)
			if err == nil || err.Error() != tc.wantErr {
				t.Fatalf("Verify error = %v, want %q", err, tc.wantErr)
			}
		})
	}
}

func TestNoStoreFails(t *testing.T) {
	work := newWorkProfile(t, fixtureOpts{noExt: true})
	if _, err := Verify(work); !errors.Is(err, ErrNoStore) {
		t.Fatalf("Verify error = %v, want ErrNoStore", err)
	}
}

func TestTwoExtensionStores(t *testing.T) {
	work := newWorkProfile(t, fixtureOpts{settings: 1, secondExt: true})
	if _, err := Verify(work); !errors.Is(err, ErrAmbiguousStore) {
		t.Fatalf("Verify error = %v, want ErrAmbiguousStore", err)
	}
}

func TestNoCookiesDB(t *testing.T) {
	work := newWorkProfile(t, fixtureOpts{settings: 1, noCookies: true})
	p := loadTikTok(t)
	if err := Sanitise(work, p); !errors.Is(err, ErrNoCookiesDB) {
		t.Fatalf("Sanitise error = %v, want ErrNoCookiesDB", err)
	}
}

func TestLockedProfileRefused(t *testing.T) {
	p := loadTikTok(t)

	src := newWorkProfile(t, fixtureOpts{settings: 1, lock: true})
	out := filepath.Join(t.TempDir(), "out")
	if _, err := Release(src, p, "1.14.2", out); !errors.Is(err, ErrLocked) {
		t.Fatalf("Release with lock: error = %v, want ErrLocked", err)
	}

	work := newWorkProfile(t, fixtureOpts{settings: 1, wal: true})
	_, err := Verify(work)
	if !errors.Is(err, ErrLocked) {
		t.Fatalf("Verify with idb -wal: error = %v, want ErrLocked", err)
	}
	if !strings.Contains(err.Error(), idbName+"-wal") {
		t.Errorf("ErrLocked message %q does not name the -wal file", err.Error())
	}
}

// readTar extracts the tar at path into a temp dir and returns the dir plus
// the entry names in order.
func readTar(t *testing.T, path string) (string, []string) {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	tr := tar.NewReader(f)
	dir := t.TempDir()
	var names []string
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		names = append(names, hdr.Name)
		target := filepath.Join(dir, filepath.FromSlash(hdr.Name))
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				t.Fatal(err)
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				t.Fatal(err)
			}
			out, err := os.Create(target)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := io.Copy(out, tr); err != nil {
				_ = out.Close()
				t.Fatal(err)
			}
			if err := out.Close(); err != nil {
				t.Fatal(err)
			}
		default:
			t.Fatalf("tar entry %q has typeflag %d, want dir or reg", hdr.Name, hdr.Typeflag)
		}
	}
	return dir, names
}

func TestReleaseEmptiesNavThenVerifies(t *testing.T) {
	p := loadTikTok(t)
	src := newWorkProfile(t, fixtureOpts{nav: 3, settings: 1})
	out := filepath.Join(t.TempDir(), "out")
	if _, err := Release(src, p, "1.14.2", out); err != nil {
		t.Fatalf("Release: %v", err)
	}
	dir, _ := readTar(t, filepath.Join(out, ArtifactName("tiktok", "1.14.2")))
	idb := filepath.Join(dir, "storage", "default", extDirName, "idb", idbName)
	db := openSQL(t, "file:"+idb+"?mode=ro")
	var nav, navIdx, settings int
	if err := db.QueryRow("SELECT COUNT(*) FROM object_data WHERE object_store_id = (SELECT id FROM object_store WHERE name = 'nav')").Scan(&nav); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow("SELECT COUNT(*) FROM index_data WHERE object_store_id = (SELECT id FROM object_store WHERE name = 'nav')").Scan(&navIdx); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow("SELECT COUNT(*) FROM object_data WHERE object_store_id = (SELECT id FROM object_store WHERE name = 'settings')").Scan(&settings); err != nil {
		t.Fatal(err)
	}
	if nav != 0 || navIdx != 0 {
		t.Errorf("tar idb nav rows = %d (index %d), want 0", nav, navIdx)
	}
	if settings != 1 {
		t.Errorf("tar idb settings rows = %d, want 1", settings)
	}
	var triggers int
	if err := db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type = 'trigger' AND tbl_name = 'object_data'").Scan(&triggers); err != nil {
		t.Fatal(err)
	}
	if triggers != 1 {
		t.Errorf("tar idb object_data triggers = %d, want 1 (Firefox schema preserved)", triggers)
	}
}

func TestTarAndManifest(t *testing.T) {
	p := loadTikTok(t)
	src := newWorkProfile(t, fixtureOpts{settings: 1})
	before := treeHash(t, src)
	out := filepath.Join(t.TempDir(), "out")
	man, err := Release(src, p, "1.14.2", out)
	if err != nil {
		t.Fatalf("Release: %v", err)
	}
	if after := treeHash(t, src); after != before {
		t.Errorf("original profile changed by Release")
	}

	tarPath := filepath.Join(out, ArtifactName("tiktok", "1.14.2"))
	dir, names := readTar(t, tarPath)
	want := append(wantPaths(), "manifest.json")
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Errorf("tar entries:\n got %v\nwant %v", names, want)
	}

	// every entry uid/gid 1000, no symlinks
	f, err := os.Open(tarPath)
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(f)
	last := ""
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if hdr.Uid != 1000 || hdr.Gid != 1000 {
			t.Errorf("entry %q uid/gid = %d/%d, want 1000/1000", hdr.Name, hdr.Uid, hdr.Gid)
		}
		if hdr.Typeflag != tar.TypeDir && hdr.Typeflag != tar.TypeReg {
			t.Errorf("entry %q is not a regular file or directory", hdr.Name)
		}
		last = hdr.Name
	}
	_ = f.Close()
	if last != "manifest.json" {
		t.Errorf("last tar entry = %q, want manifest.json", last)
	}

	// manifest inside the tar
	data, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var inner Manifest
	if err := json.Unmarshal(data, &inner); err != nil {
		t.Fatal(err)
	}
	if inner.Kind != "release" || inner.Platform != "tiktok" || inner.Zeeschuimer != "1.14.2" {
		t.Errorf("inner manifest = %+v", inner)
	}
	if strings.Join(inner.Modules, ",") != strings.Join(p.ZeeschuimerModules, ",") {
		t.Errorf("inner manifest modules = %v, want %v", inner.Modules, p.ZeeschuimerModules)
	}
	if inner.SHA256 != "" {
		t.Errorf("inner manifest carries sha256 %q, want it omitted", inner.SHA256)
	}

	// returned manifest and the .sha256 file
	if man.Kind != "release" {
		t.Errorf("Manifest.Kind = %q, want release", man.Kind)
	}
	if strings.Join(man.Modules, ",") != strings.Join(p.ZeeschuimerModules, ",") {
		t.Errorf("Manifest.Modules = %v, want %v", man.Modules, p.ZeeschuimerModules)
	}
	sum, err := sha256File(tarPath)
	if err != nil {
		t.Fatal(err)
	}
	if man.SHA256 != sum {
		t.Errorf("Manifest.SHA256 = %q, want %q", man.SHA256, sum)
	}
	shaData, err := os.ReadFile(tarPath + ".sha256")
	if err != nil {
		t.Fatal(err)
	}
	if want := sum + "  " + ArtifactName("tiktok", "1.14.2") + "\n"; string(shaData) != want {
		t.Errorf(".sha256 file = %q, want %q", shaData, want)
	}
}

func TestReleaseRefusesOriginalLock(t *testing.T) {
	p := loadTikTok(t)
	src := newWorkProfile(t, fixtureOpts{settings: 1, parentlock: true})
	out := filepath.Join(t.TempDir(), "out")

	before, err := os.ReadDir(os.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Release(src, p, "1.14.2", out); !errors.Is(err, ErrLocked) {
		t.Fatalf("Release with .parentlock: error = %v, want ErrLocked", err)
	}
	after, err := os.ReadDir(os.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) {
		t.Errorf("temp dir left behind: %d entries before, %d after", len(before), len(after))
	}
	if entries, err := os.ReadDir(out); err == nil && len(entries) > 0 {
		t.Errorf("out dir not empty after refusal: %v", entries)
	}
}
