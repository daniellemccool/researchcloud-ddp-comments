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
	mainExtDir = "moz-extension+++" + extUUID
	ctxExtDir  = mainExtDir + userContext
	itemsIDB   = "2612651559zsemeestcih-uriem.sqlite"
	storageIDB = "3647222921wleabcEoxlt-eengsairo.sqlite"
	builtOn    = "2026-10-30T00:00:00Z"
	zsVersion  = "1.14.2"
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
	storageRows                   int    // rows in the storage.local database
	extVersion                    string // version in the extensions.json entry
	ctxHasStores                  bool   // the userContext dir holds the four stores (ambiguity)
	noExt                         bool   // no moz-extension directories at all
	noCtx                         bool   // no userContext sibling dir (storage.local missing)
	emptyStorage                  bool   // storage.local database exists but holds no rows
	noCookies                     bool   // no cookies.sqlite
	noPrefs                       bool   // no prefs.js
	noXPI                         bool   // no extensions/<xpiID>.xpi
	lock, parentlock              bool
	walEmpty, walNonEmpty         bool // idb -wal side file in the original
	shmNonEmpty                   bool // non-empty idb -shm side file in the original
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
	if !o.noPrefs {
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
	}
	extVersion := o.extVersion
	if extVersion == "" {
		extVersion = zsVersion
	}
	extensionsJSON := `{"schemaVersion":37,"addons":[` +
		`{"id":"` + xpiID + `","syncGUID":"aaaa","type":"extension","version":"` + extVersion + `","path":"/old/path.xpi",` +
		`"rootURI":"jar:file:///old/path.xpi!/","sourceURI":"` + policyURL(zsVersion) + `",` +
		`"installDate":1,"updateDate":2,"installTelemetryInfo":{"x":1}},` +
		`{"id":"webcompat@mozilla.org","syncGUID":"bbbb","type":"extension"}]}`
	files := map[string]string{
		"extensions.json":                             extensionsJSON,
		"extension-preferences.json":                  `{}`, // not on the allowlist: deleted
		"places.sqlite":                               "junk",
		"times.json":                                  "{}",
		"extensions/other.xpi":                        "junk",
		"chrome/userContent.css":                      "/* hide media */",
		"chrome/other.css":                            "junk",
		"storage/default/https+++www.tiktok.com/junk": "junk",
		"storage/permanent/chrome/idb/x.sqlite":       "junk",
	}
	if !o.noXPI {
		files["extensions/"+xpiID+".xpi"] = "xpi-bytes"
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
	if !o.noExt {
		mainDir := filepath.Join(dir, "storage", "default", mainExtDir)
		writeJunk(t, mainDir, ".metadata-v2", "ls/junk.txt")
		idbDir := filepath.Join(mainDir, "idb")
		writeItemsIDB(t, filepath.Join(idbDir, itemsIDB), itemsDB, o.items, o.uploads, o.nav, o.settings)
		writeJunk(t, idbDir, "other.sqlite")
		if err := os.MkdirAll(filepath.Join(idbDir, itemsIDB+".files"), 0o755); err != nil {
			t.Fatal(err)
		}
		wal := []byte{}
		if o.walNonEmpty {
			wal = []byte("wal")
		}
		if o.walEmpty || o.walNonEmpty {
			if err := os.WriteFile(filepath.Join(idbDir, itemsIDB+"-wal"), wal, 0o644); err != nil {
				t.Fatal(err)
			}
		}
		if o.shmNonEmpty {
			if err := os.WriteFile(filepath.Join(idbDir, itemsIDB+"-shm"), []byte("shm"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		if !o.noCtx {
			ctxDir := filepath.Join(dir, "storage", "default", ctxExtDir)
			writeJunk(t, ctxDir, "cache/junk")
			ctxIDB := filepath.Join(ctxDir, "idb")
			if o.ctxHasStores {
				// named webExtensions-storage-local so cleanExtDir keeps it,
				// but holding the four stores: two candidate Zeeschuimer dirs
				writeItemsIDB(t, filepath.Join(ctxIDB, storageIDB), storageDB, 0, 0, 0, 1)
			} else {
				rows := o.storageRows
				if rows == 0 && !o.emptyStorage {
					rows = 2
				}
				writeStorageIDB(t, filepath.Join(ctxIDB, storageIDB), rows)
			}
			if err := os.MkdirAll(filepath.Join(ctxIDB, storageIDB+".files"), 0o755); err != nil {
				t.Fatal(err)
			}
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

// writeJunk writes junk files (relative slash paths) under dir.
func writeJunk(t *testing.T, dir string, rels ...string) {
	t.Helper()
	for _, rel := range rels {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("junk"), 0o644); err != nil {
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

// writeItemsIDB writes a Firefox-style IndexedDB file with the four
// Zeeschuimer object stores, a file table and the update_refcount trigger.
func writeItemsIDB(t *testing.T, path, dbName string, items, uploads, nav, settings int) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	db := openSQL(t, path)
	exec(t, db, "CREATE TABLE database (id INTEGER PRIMARY KEY, name TEXT NOT NULL)")
	exec(t, db, "INSERT INTO database (name) VALUES ('"+dbName+"')")
	exec(t, db, "CREATE TABLE object_store (id INTEGER PRIMARY KEY, name TEXT NOT NULL)")
	exec(t, db, "CREATE TABLE object_data (id INTEGER PRIMARY KEY, object_store_id INTEGER NOT NULL, key BLOB, file_ids TEXT, data BLOB)")
	exec(t, db, "CREATE TABLE index_data (id INTEGER PRIMARY KEY, object_store_id INTEGER NOT NULL, index_id INTEGER NOT NULL, value BLOB)")
	exec(t, db, "CREATE TABLE unique_index_data (id INTEGER PRIMARY KEY, object_store_id INTEGER NOT NULL, index_id INTEGER NOT NULL, value BLOB)")
	exec(t, db, "CREATE TABLE file (id INTEGER PRIMARY KEY, refcount INTEGER NOT NULL)")
	// Firefox's trigger calls the browser-internal update_refcount function,
	// which plain SQLite does not have.
	exec(t, db, `CREATE TRIGGER object_data_delete_trigger AFTER DELETE ON object_data FOR EACH ROW WHEN OLD.file_ids IS NOT NULL BEGIN SELECT update_refcount(OLD.file_ids, NULL); END`)
	exec(t, db, "INSERT INTO file (id, refcount) VALUES (7, 1)")
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
			fileIDs := any(nil)
			if s.name == "nav" {
				fileIDs = "7"
			}
			exec(t, db, "INSERT INTO object_data (id, object_store_id, key, file_ids, data) VALUES (?, ?, ?, ?, ?)",
				next, s.id, []byte("key"), fileIDs, []byte("row"))
			if s.name == "nav" {
				exec(t, db, "INSERT INTO index_data (id, object_store_id, index_id, value) VALUES (?, ?, 1, ?)", next, s.id, []byte("idx"))
				exec(t, db, "INSERT INTO unique_index_data (id, object_store_id, index_id, value) VALUES (?, ?, 1, ?)", next, s.id, []byte("uidx"))
			}
			next++
		}
	}
}

// writeStorageIDB writes the webExtensions-storage-local database.
func writeStorageIDB(t *testing.T, path string, rows int) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	db := openSQL(t, path)
	exec(t, db, "CREATE TABLE database (id INTEGER PRIMARY KEY, name TEXT NOT NULL)")
	exec(t, db, "INSERT INTO database (name) VALUES ('"+storageDB+"')")
	exec(t, db, "CREATE TABLE object_store (id INTEGER PRIMARY KEY, name TEXT NOT NULL)")
	exec(t, db, "CREATE TABLE object_data (id INTEGER PRIMARY KEY, object_store_id INTEGER NOT NULL, key BLOB, file_ids TEXT, data BLOB)")
	exec(t, db, "INSERT INTO object_store (id, name) VALUES (1, 'storage-local-data')")
	for i := 0; i < rows; i++ {
		exec(t, db, "INSERT INTO object_data (id, object_store_id, key, data) VALUES (?, 1, ?, ?)", i+1, []byte("key"), []byte("value"))
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

// schemaOf returns the sorted (type, name, sql) rows of sqlite_master.
func schemaOf(t *testing.T, path string) []string {
	t.Helper()
	db := openSQL(t, path)
	rows, err := db.Query("SELECT type, name, sql FROM sqlite_master ORDER BY type, name")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var typ, name string
		var sqlStr *string
		if err := rows.Scan(&typ, &name, &sqlStr); err != nil {
			t.Fatal(err)
		}
		s := ""
		if sqlStr != nil {
			s = *sqlStr
		}
		out = append(out, typ+"|"+name+"|"+s)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func newWorkProfile(t *testing.T, o fixtureOpts) string {
	t.Helper()
	dir := t.TempDir()
	work := filepath.Join(dir, "profile")
	buildProfile(t, work, o)
	return work
}

// wantPaths is the exact allowlist the sanitised tree must hold.
func wantPaths() []string {
	return []string{
		"chrome",
		"chrome/userContent.css",
		"cookies.sqlite",
		"extensions",
		"extensions.json",
		"extensions/" + xpiID + ".xpi",
		"prefs.js",
		"storage",
		"storage/default",
		"storage/default/" + mainExtDir,
		"storage/default/" + mainExtDir + "/idb",
		"storage/default/" + mainExtDir + "/idb/" + itemsIDB,
		"storage/default/" + mainExtDir + "/idb/" + itemsIDB + ".files",
		"storage/default/" + ctxExtDir,
		"storage/default/" + ctxExtDir + "/idb",
		"storage/default/" + ctxExtDir + "/idb/" + storageIDB,
		"storage/default/" + ctxExtDir + "/idb/" + storageIDB + ".files",
	}
}

func TestSanitiseKeepsAllowlistOnly(t *testing.T) {
	work := newWorkProfile(t, fixtureOpts{settings: 1})
	p := loadTikTok(t)
	if err := Sanitise(work, p, zsVersion, builtOn); err != nil {
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
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	for _, line := range lines {
		m := prefLineRe.FindStringSubmatch(strings.TrimSpace(line))
		if m == nil || !allowPrefs[m[1]] {
			t.Errorf("prefs.js kept a non-allowlisted line: %q", line)
		}
	}
	if len(lines) != len(allowPrefs) {
		t.Errorf("prefs.js holds %d lines, want %d", len(lines), len(allowPrefs))
	}
	// extensions.json keeps only Zeeschuimer's normalised entry
	extData, err := os.ReadFile(filepath.Join(work, "extensions.json"))
	if err != nil {
		t.Fatal(err)
	}
	var ext struct {
		SchemaVersion int              `json:"schemaVersion"`
		Addons        []map[string]any `json:"addons"`
	}
	if err := json.Unmarshal(extData, &ext); err != nil {
		t.Fatal(err)
	}
	if ext.SchemaVersion != 37 || len(ext.Addons) != 1 {
		t.Fatalf("extensions.json = %s", extData)
	}
	e := ext.Addons[0]
	if e["id"] != xpiID || e["path"] != xpiContainerPath || e["syncGUID"] != syncGUID {
		t.Errorf("addon entry not normalised: %v", e)
	}
	if _, ok := e["installTelemetryInfo"]; ok {
		t.Errorf("telemetry key survived: %v", e)
	}
	if e["sourceURI"] != policyURL(zsVersion) {
		t.Errorf("sourceURI = %v, want %v", e["sourceURI"], policyURL(zsVersion))
	}
}

func TestCookiesFiltered(t *testing.T) {
	work := newWorkProfile(t, fixtureOpts{settings: 1})
	p := loadTikTok(t)
	if err := Sanitise(work, p, zsVersion, builtOn); err != nil {
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
	p := loadTikTok(t)
	if err := Sanitise(work, p, zsVersion, builtOn); err != nil {
		t.Fatalf("Sanitise: %v", err)
	}
	counts, err := Verify(work, p, zsVersion)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if counts != (Counts{Items: 0, Uploads: 0, Nav: 0, Settings: 1}) {
		t.Errorf("counts = %+v, want {0 0 0 1}", counts)
	}
}

func TestVerifyItemsFails(t *testing.T) {
	work := newWorkProfile(t, fixtureOpts{items: 1, settings: 1})
	_, err := Verify(work, loadTikTok(t), zsVersion)
	if err == nil || !strings.Contains(err.Error(), "template has captured items") {
		t.Fatalf("Verify error = %v, want it to contain %q", err, "template has captured items")
	}
}

func TestVerifyUploadsFails(t *testing.T) {
	work := newWorkProfile(t, fixtureOpts{uploads: 2, settings: 1})
	_, err := Verify(work, loadTikTok(t), zsVersion)
	if err == nil || !strings.Contains(err.Error(), "template has upload history") {
		t.Fatalf("Verify error = %v, want it to contain %q", err, "template has upload history")
	}
}

func TestVerifyNavFails(t *testing.T) {
	// Verify is called directly, so the nav rows were not emptied.
	work := newWorkProfile(t, fixtureOpts{nav: 1, settings: 1})
	_, err := Verify(work, loadTikTok(t), zsVersion)
	if err == nil || !strings.Contains(err.Error(), "template has navigation history") {
		t.Fatalf("Verify error = %v, want it to contain %q", err, "template has navigation history")
	}
}

func TestVerifyNoSettingsFails(t *testing.T) {
	work := newWorkProfile(t, fixtureOpts{})
	_, err := Verify(work, loadTikTok(t), zsVersion)
	if err == nil || !strings.Contains(err.Error(), "template has no settings (modules were never enabled)") {
		t.Fatalf("Verify error = %v, want it to contain %q", err, "template has no settings (modules were never enabled)")
	}
}

func TestNoStoreFails(t *testing.T) {
	src := newWorkProfile(t, fixtureOpts{noExt: true})
	out := filepath.Join(t.TempDir(), "out")
	if _, err := Release(src, loadTikTok(t), zsVersion, out); !errors.Is(err, ErrNoStore) {
		t.Fatalf("Release error = %v, want ErrNoStore", err)
	}
}

func TestTwoExtensionStores(t *testing.T) {
	src := newWorkProfile(t, fixtureOpts{settings: 1, ctxHasStores: true})
	out := filepath.Join(t.TempDir(), "out")
	if _, err := Release(src, loadTikTok(t), zsVersion, out); !errors.Is(err, ErrAmbiguousStore) {
		t.Fatalf("Release error = %v, want ErrAmbiguousStore", err)
	}
}

func TestNoCookiesDB(t *testing.T) {
	src := newWorkProfile(t, fixtureOpts{settings: 1, noCookies: true})
	out := filepath.Join(t.TempDir(), "out")
	if _, err := Release(src, loadTikTok(t), zsVersion, out); !errors.Is(err, ErrNoCookiesDB) {
		t.Fatalf("Release error = %v, want ErrNoCookiesDB", err)
	}
}

func TestLockedProfileRefused(t *testing.T) {
	p := loadTikTok(t)

	src := newWorkProfile(t, fixtureOpts{settings: 1, lock: true})
	out := filepath.Join(t.TempDir(), "out")
	if _, err := Release(src, p, zsVersion, out); !errors.Is(err, ErrLocked) {
		t.Fatalf("Release with lock: error = %v, want ErrLocked", err)
	}

	// a non-empty -wal beside an idb database in the ORIGINAL: refused (F29)
	src = newWorkProfile(t, fixtureOpts{settings: 1, walNonEmpty: true})
	out = filepath.Join(t.TempDir(), "out")
	_, err := Release(src, p, zsVersion, out)
	if !errors.Is(err, ErrLocked) {
		t.Fatalf("Release with non-empty idb -wal: error = %v, want ErrLocked", err)
	}
	if !strings.Contains(err.Error(), itemsIDB+"-wal") {
		t.Errorf("ErrLocked message %q does not name the -wal file", err.Error())
	}

	// a zero-byte -wal is tolerated (clean shutdown leftover)
	src = newWorkProfile(t, fixtureOpts{settings: 1, walEmpty: true})
	out = filepath.Join(t.TempDir(), "out")
	if _, err := Release(src, p, zsVersion, out); err != nil {
		t.Fatalf("Release with zero-byte idb -wal: %v", err)
	}

	// Verify itself refuses any -wal beside the idb
	work := newWorkProfile(t, fixtureOpts{settings: 1, walNonEmpty: true})
	if _, err := Verify(work, p, zsVersion); !errors.Is(err, ErrLocked) {
		t.Fatalf("Verify with idb -wal: error = %v, want ErrLocked", err)
	}

	// a non-empty -shm beside an idb database in the ORIGINAL: refused (F29)
	src = newWorkProfile(t, fixtureOpts{settings: 1, shmNonEmpty: true})
	out = filepath.Join(t.TempDir(), "out")
	_, err = Release(src, p, zsVersion, out)
	if !errors.Is(err, ErrLocked) {
		t.Fatalf("Release with non-empty idb -shm: error = %v, want ErrLocked", err)
	}
	if !strings.Contains(err.Error(), itemsIDB+"-shm") {
		t.Errorf("ErrLocked message %q does not name the -shm file", err.Error())
	}
}

func TestStorageLocalKeptAndVerified(t *testing.T) {
	p := loadTikTok(t)

	// the userContext sibling dir is absent: the release must fail
	src := newWorkProfile(t, fixtureOpts{settings: 1, noCtx: true})
	out := filepath.Join(t.TempDir(), "out")
	_, err := Release(src, p, zsVersion, out)
	if err == nil || !strings.Contains(err.Error(), "extension storage.local database not found or empty") {
		t.Fatalf("Release without the storage.local dir: error = %v, want the storage.local message", err)
	}

	// with it, the release succeeds and the tar holds both idb files
	src = newWorkProfile(t, fixtureOpts{settings: 1})
	out = filepath.Join(t.TempDir(), "out")
	if _, err := Release(src, p, zsVersion, out); err != nil {
		t.Fatalf("Release: %v", err)
	}
	_, names := readTar(t, filepath.Join(out, ArtifactName("tiktok", zsVersion)))
	joined := strings.Join(names, ",")
	if !strings.Contains(joined, "storage/default/"+mainExtDir+"/idb/"+itemsIDB) {
		t.Errorf("tar lacks the main idb file: %v", names)
	}
	if !strings.Contains(joined, "storage/default/"+ctxExtDir+"/idb/"+storageIDB) {
		t.Errorf("tar lacks the storage.local idb file: %v", names)
	}
}

func TestMissingPrefsIsError(t *testing.T) {
	src := newWorkProfile(t, fixtureOpts{settings: 1, noPrefs: true})
	out := filepath.Join(t.TempDir(), "out")
	_, err := Release(src, loadTikTok(t), zsVersion, out)
	if err == nil || !strings.Contains(err.Error(), "prefs.js") {
		t.Fatalf("Release without prefs.js: error = %v, want a prefs.js error", err)
	}
}

func TestMissingXPIRefused(t *testing.T) {
	src := newWorkProfile(t, fixtureOpts{settings: 1, noXPI: true})
	out := filepath.Join(t.TempDir(), "out")
	_, err := Release(src, loadTikTok(t), zsVersion, out)
	if err == nil || !strings.Contains(err.Error(), "extensions/"+xpiID+".xpi") {
		t.Fatalf("Release without the xpi: error = %v, want it to name the xpi path", err)
	}
}

func TestVersionMismatchRefused(t *testing.T) {
	src := newWorkProfile(t, fixtureOpts{settings: 1, extVersion: "9.9.9"})
	out := filepath.Join(t.TempDir(), "out")
	_, err := Release(src, loadTikTok(t), zsVersion, out)
	if err == nil ||
		!strings.Contains(err.Error(), `"9.9.9"`) ||
		!strings.Contains(err.Error(), `"`+zsVersion+`"`) {
		t.Fatalf("Release with version mismatch: error = %v, want it to name both versions", err)
	}
}

func TestEmptyStorageLocalRefused(t *testing.T) {
	src := newWorkProfile(t, fixtureOpts{settings: 1, emptyStorage: true})
	out := filepath.Join(t.TempDir(), "out")
	_, err := Release(src, loadTikTok(t), zsVersion, out)
	if err == nil || !strings.Contains(err.Error(), "extension storage.local database not found or empty") {
		t.Fatalf("Release with empty storage.local: error = %v, want the storage.local message", err)
	}
}

func TestEmptyDisallowedDirReported(t *testing.T) {
	work := newWorkProfile(t, fixtureOpts{settings: 1})
	p := loadTikTok(t)
	if err := Sanitise(work, p, zsVersion, builtOn); err != nil {
		t.Fatalf("Sanitise: %v", err)
	}
	// an empty disallowed directory left behind after Sanitise
	empty := filepath.Join(work, "storage", "default", "https+++example.com")
	if err := os.MkdirAll(empty, 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := Verify(work, p, zsVersion)
	if err == nil || !strings.Contains(err.Error(), "unexpected file: storage/default/https+++example.com") {
		t.Fatalf("Verify with empty disallowed dir: error = %v, want it to report the directory itself", err)
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
	if _, err := Release(src, p, zsVersion, out); err != nil {
		t.Fatalf("Release: %v", err)
	}
	dir, _ := readTar(t, filepath.Join(out, ArtifactName("tiktok", zsVersion)))
	idb := filepath.Join(dir, "storage", "default", mainExtDir, "idb", itemsIDB)
	db := openSQL(t, idb)
	var nav, navIdx, fileRows, settings, triggers int
	mustScan(t, db, "SELECT COUNT(*) FROM object_data WHERE object_store_id = (SELECT id FROM object_store WHERE name = 'nav')", &nav)
	mustScan(t, db, "SELECT COUNT(*) FROM index_data WHERE object_store_id = (SELECT id FROM object_store WHERE name = 'nav')", &navIdx)
	mustScan(t, db, "SELECT COUNT(*) FROM file", &fileRows)
	mustScan(t, db, "SELECT COUNT(*) FROM object_data WHERE object_store_id = (SELECT id FROM object_store WHERE name = 'settings')", &settings)
	mustScan(t, db, "SELECT COUNT(*) FROM sqlite_master WHERE type = 'trigger' AND tbl_name = 'object_data'", &triggers)
	if nav != 0 || navIdx != 0 {
		t.Errorf("tar idb nav rows = %d (index %d), want 0", nav, navIdx)
	}
	if fileRows != 0 {
		t.Errorf("tar idb file rows = %d, want 0", fileRows)
	}
	if settings != 1 {
		t.Errorf("tar idb settings rows = %d, want 1", settings)
	}
	if triggers != 1 {
		t.Errorf("tar idb object_data triggers = %d, want 1 (Firefox schema preserved)", triggers)
	}
}

func mustScan(t *testing.T, db *sql.DB, query string, dest *int) {
	t.Helper()
	if err := db.QueryRow(query).Scan(dest); err != nil {
		t.Fatal(err)
	}
}

func TestTarAndManifest(t *testing.T) {
	p := loadTikTok(t)
	src := newWorkProfile(t, fixtureOpts{settings: 1})
	before := treeHash(t, src)
	itemsSchemaBefore := schemaOf(t, filepath.Join(src, "storage", "default", mainExtDir, "idb", itemsIDB))
	storageSchemaBefore := schemaOf(t, filepath.Join(src, "storage", "default", ctxExtDir, "idb", storageIDB))

	out := filepath.Join(t.TempDir(), "out")
	man, err := Release(src, p, zsVersion, out)
	if err != nil {
		t.Fatalf("Release: %v", err)
	}
	if after := treeHash(t, src); after != before {
		t.Errorf("original profile changed by Release")
	}

	tarPath := filepath.Join(out, ArtifactName("tiktok", zsVersion))
	dir, names := readTar(t, tarPath)
	want := append(wantPaths(), "manifest.json")
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Errorf("tar entries:\n got %v\nwant %v", names, want)
	}

	// every entry uid/gid 1000, mode 0644/0755, no symlinks; manifest.json last
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
		if hdr.Typeflag == tar.TypeDir && hdr.Mode != 0o755 {
			t.Errorf("dir entry %q mode = %o, want 755", hdr.Name, hdr.Mode)
		}
		if hdr.Typeflag == tar.TypeReg && hdr.Mode != 0o644 {
			t.Errorf("file entry %q mode = %o, want 644", hdr.Name, hdr.Mode)
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

	// schema identity after the rebuild
	if got := schemaOf(t, filepath.Join(dir, "storage", "default", mainExtDir, "idb", itemsIDB)); strings.Join(got, "\n") != strings.Join(itemsSchemaBefore, "\n") {
		t.Errorf("main idb schema changed by the rebuild")
	}
	if got := schemaOf(t, filepath.Join(dir, "storage", "default", ctxExtDir, "idb", storageIDB)); strings.Join(got, "\n") != strings.Join(storageSchemaBefore, "\n") {
		t.Errorf("storage.local idb schema changed by the rebuild")
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
	if inner.Kind != "release" || inner.Platform != "tiktok" || inner.Zeeschuimer != zsVersion {
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
	if want := sum + "  " + ArtifactName("tiktok", zsVersion) + "\n"; string(shaData) != want {
		t.Errorf(".sha256 file = %q, want %q", shaData, want)
	}
}

func TestReleaseRefusesOriginalLock(t *testing.T) {
	p := loadTikTok(t)
	src := newWorkProfile(t, fixtureOpts{settings: 1, parentlock: true})
	out := filepath.Join(t.TempDir(), "out")

	// assert "no temp dir left" via a temp dir we own, not os.TempDir()
	own := t.TempDir()
	t.Setenv("TMPDIR", own)
	if _, err := Release(src, p, zsVersion, out); !errors.Is(err, ErrLocked) {
		t.Fatalf("Release with .parentlock: error = %v, want ErrLocked", err)
	}
	if entries, err := os.ReadDir(own); err != nil || len(entries) > 0 {
		t.Errorf("temp dir left behind in %s: %v (%v)", own, entries, err)
	}
	if entries, err := os.ReadDir(out); err == nil && len(entries) > 0 {
		t.Errorf("out dir not empty after refusal: %v", entries)
	}
}
