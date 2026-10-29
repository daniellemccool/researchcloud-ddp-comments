"""Tests for desk/desklib/template.py: allowlist sanitising, empty-store proof, manifest, tar."""
import datetime, hashlib, json, os, shutil, sqlite3, subprocess, sys, tarfile, tempfile, unittest

REPO = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
sys.path.insert(0, os.path.join(REPO, "desk"))
from desklib import platform as pf, template as tp  # noqa: E402

CLI = os.path.join(REPO, "desk", "desk_template.py")
XID = "{677bb78c-7168-4aa5-8e9b-32e9f26c6157}"
XPI = f"extensions/{XID}.xpi"
ZS = "storage/default/moz-extension+++abc"
ZSCTX = "storage/default/moz-extension+++abc^userContextId=4294967295"
SECRET = b"SECRETCAPTURE"
ZV = "1.14.2"
BUILT = "2026-10-29"
BUILT_MS = 1793232000000     # 2026-10-29T00:00:00Z
POLICY_URL = f"https://github.com/digitalmethodsinitiative/zeeschuimer/releases/download/v{ZV}/zeeschuimer-{ZV}.xpi"
CPATH = f"/config/profile/{XPI}"
KEEP = (".tiktok.com", "cookie-consent")
COOKIES = [KEEP, ("www.tiktok.com", "cookie-consent"), (".tiktok.com", "ttwid"), ("desk.example.nl", "remember_token"),
           ("proxy.sram.surf.nl", "SATOSA_surf_ram_PROXY_STATE"), ("evil.example", "cookie-consent")]
KEPT_PREFS = ['"extensions.webextensions.uuids"', f'"extensions.webextensions.ExtensionStorageIDB.migrated.{XID}"',
              '"extensions.databaseSchema"', '"extensions.lastAppVersion"', '"extensions.lastPlatformVersion"',
              '"extensions.pendingOperations"', '"toolkit.legacyUserProfileCustomizations.stylesheets"']
DROPPED_PREFS = ["browser.startup.homepage", "network.proxy.type", "extensions.formautofill.x",
                 "browser.formfill.enable", "places.history.enabled", "app.update.lastUpdateTime"]


def prefs_text():
    uuids = json.dumps(json.dumps({XID: "abc", "other@x": "other"}))
    lines = [f'user_pref("extensions.webextensions.uuids", {uuids});']
    lines += [f'user_pref({k}, 1);' for k in KEPT_PREFS[1:]]
    lines += [f'user_pref("{k}", "https://secret.example/");' for k in DROPPED_PREFS]
    return "\n".join(lines) + "\n"


def make_idb(path, name, stores, rows, deleted=0, user_version=0):
    os.makedirs(os.path.dirname(path), exist_ok=True)
    c = sqlite3.connect(path)
    c.execute("CREATE TABLE database (name TEXT PRIMARY KEY, origin TEXT, version INTEGER)")
    c.execute("CREATE TABLE object_store (id INTEGER PRIMARY KEY, auto_increment INTEGER, name TEXT, key_path TEXT)")
    c.execute("CREATE TABLE object_data (object_store_id INTEGER, key BLOB, file_ids TEXT, data BLOB)")
    c.execute("INSERT INTO database (name, origin, version) VALUES (?, 'o', 1)", (name,))
    for i, s in enumerate(stores, 1):
        c.execute("INSERT INTO object_store (id, name) VALUES (?, ?)", (i, s))
        for k in range(rows.get(s, 0)):
            c.execute("INSERT INTO object_data (object_store_id, key, data) VALUES (?, ?, x'00')", (i, k))
    for k in range(deleted):
        c.execute("INSERT INTO object_data (object_store_id, key, data) VALUES (1, ?, ?)", (1000 + k, SECRET * 150))
    c.commit()
    c.execute("PRAGMA secure_delete = OFF")  # as Firefox runs it: deleted bytes stay in free pages
    c.execute("DELETE FROM object_data WHERE key >= 1000")
    c.execute(f"PRAGMA user_version = {user_version}")
    c.commit(); c.close()


def make_cookies(p, rows, wal_only=()):
    path = os.path.join(p, "cookies.sqlite")
    c = sqlite3.connect(path)
    c.execute("CREATE TABLE moz_cookies (id INTEGER PRIMARY KEY, host TEXT, name TEXT, value TEXT, expiry INTEGER)")
    sql = "INSERT INTO moz_cookies (host, name, value, expiry) VALUES (?, ?, ?, 0)"
    n = iter(range(10_000))
    val = lambda r: "consent-ok" if r[1] == "cookie-consent" and r[0].endswith("tiktok.com") else f"deleted-secret-{next(n)}"  # noqa: E731
    c.executemany(sql, [(h, nm, val((h, nm))) for h, nm in rows])
    c.commit()
    if wal_only:
        c.execute("PRAGMA journal_mode=WAL"); c.execute("PRAGMA wal_autocheckpoint=0")
        c.executemany(sql, [(h, nm, val((h, nm))) for h, nm in wal_only]); c.commit()
        shutil.copy(path, path + ".snap"); shutil.copy(path + "-wal", path + ".snap-wal")  # while still open
    c.close()
    if wal_only:
        os.replace(path + ".snap", path); os.replace(path + ".snap-wal", path + "-wal")
        for side in ("-shm",):
            if os.path.exists(path + side): os.remove(path + side)


def write_json(p, name, data):
    with open(os.path.join(p, name), "w") as fh:
        json.dump(data, fh)


def read_json(p, name):
    with open(os.path.join(p, name)) as fh:
        return json.load(fh)


def zs_entry(**over):
    e = {"id": XID, "syncGUID": "{1b2c3d4e-0000-4000-8000-123456789abc}", "version": ZV, "type": "extension",
         "loader": None, "updateURL": None, "installOrigins": None, "manifestVersion": 2,
         "optionalPermissions": {"permissions": [], "origins": []}, "location": "app-profile",
         "active": True, "userDisabled": False, "appDisabled": False, "signedState": 2, "signedDate": 1700000000000,
         "installDate": 1761000000000, "updateDate": 1761000001000,
         "path": f"/home/someone/.mozilla/firefox/x/{XPI}", "sourceURI": POLICY_URL,
         "installTelemetryInfo": {"source": "amo", "method": "sideload"},
         "defaultLocale": {"name": "Zeeschuimer"}, "targetApplications": [{"id": "toolkit@mozilla.org"}],
         "startupData": {"persistentListeners": {}}}
    e.update(over)
    return e


def make_profile(root, items_rows=0, uploads_rows=0, with_items_db=True, storage_rows=3, sl_uuid="abc",
                 cookies=COOKIES, wal_only=(), deleted=0, user_version=0, entry=None):
    p = os.path.join(root, "profile")
    for d in ("cache2/entries", "startupCache", "sessionstore-backups", "crashes", "extensions",
              "storage/default/https+++www.tiktok.com", "storage/default/https+++ds.sram.surf.nl", "chrome",
              "log", "downloads", "certs", "xdg/data"):
        os.makedirs(os.path.join(root if d in ("log", "downloads", "certs", "xdg/data") else p, d))
    if with_items_db:
        make_idb(os.path.join(p, ZS, "idb", "h1.sqlite"), "zeeschuimer-items", ["items", "uploads", "nav", "settings"],
                 {"items": items_rows, "uploads": uploads_rows}, deleted, user_version)
    make_idb(os.path.join(p, f"storage/default/moz-extension+++{sl_uuid}^userContextId=4294967295/idb/h2.sqlite"),
             "webExtensions-storage-local", ["storage-local-data"], {"storage-local-data": storage_rows})
    make_idb(os.path.join(p, "storage/default/moz-extension+++other/idb/h3.sqlite"), "zeeschuimer-items",
             ["items"], {"items": 5})
    os.makedirs(os.path.join(p, ZS), exist_ok=True)
    for f in ("cache2/entries/x", "sessionstore.jsonlz4", "sessionstore-backups/recovery.jsonlz4", "crashes/c",
              "places.sqlite", "logins.json", "key4.db", "formhistory.sqlite",
              "chrome/userContent.css", "sessionstore-x.baklz4", ZS + "/.metadata-v2",
              "storage/default/https+++www.tiktok.com/z.sqlite", XPI, "extensions/other.txt",
              "extensions/other@example.xpi"):
        with open(os.path.join(p, f), "w") as fh: fh.write("x")
    for f in ("log/access.log", "downloads/x.mp4", "certs/key.pem", "xdg/data/recently-used.xbel", ".vncpass_clear"):
        with open(os.path.join(root, f), "w") as fh: fh.write("x")
    write_json(p, "extensions.json", {"schemaVersion": 35, "addons": [
        entry or zs_entry(),
        {"id": "other@example", "location": "app-profile", "path": "/config/profile/extensions/other@example.xpi"}]})
    write_json(p, "extension-preferences.json", {XID: {"permissions": ["tabs"]}})
    write_json(p, "extension-settings.json", {"version": 2, "prefs": {XID: {"initialValue": {}}}})
    with open(os.path.join(p, "prefs.js"), "w") as fh: fh.write(prefs_text())
    make_cookies(p, cookies, wal_only)
    return p


def tiktok():
    return pf.load_dir(os.path.join(REPO, "platforms"), "tiktok")


def tree(p):
    return sorted(os.path.relpath(os.path.join(d, f), p) for d, _, fs in os.walk(p) for f in fs)


class TemplateTest(unittest.TestCase):
    def setUp(self):
        self._t = tempfile.TemporaryDirectory(); self.t = self._t.name
        self.addCleanup(self._t.cleanup)
        self.plat = tiktok()

    def release(self, **kw):
        src = os.path.join(self.t, "src"); make_profile(src, **kw)
        return src, tp.release(src, os.path.join(self.t, "out"), self.plat, "1.14.2")

    def extract(self, tar_path, name="x"):
        x = os.path.join(self.t, name); shutil.rmtree(x, ignore_errors=True); os.makedirs(x)
        with tarfile.open(tar_path) as tar:
            tar.extractall(x, filter="data")
        return x

    def test_release_keeps_only_allowlist(self):
        src = os.path.join(self.t, "src"); p = make_profile(src)
        tp.sanitise(p, self.plat.keep_cookies, ZV, BUILT)
        files = tree(p)
        for gone in ("places.sqlite", "logins.json", "key4.db", "formhistory.sqlite", "sessionstore.jsonlz4",
                     "sessionstore-x.baklz4", "cache2", "crashes", "startupCache", "storage/default/https+++www.tiktok.com",
                     "storage/default/moz-extension+++other", ZS + "/.metadata-v2", "extensions/other.txt"):
            self.assertFalse(any(f == gone or f.startswith(gone + "/") for f in files), gone)
        for gone in ("extension-preferences.json", "extension-settings.json"):
            self.assertNotIn(gone, files)
        for kept in ("prefs.js", "extensions.json",
                     "cookies.sqlite", "chrome/userContent.css", XPI, ZS + "/idb/h1.sqlite", ZSCTX + "/idb/h2.sqlite"):
            self.assertIn(kept, files)
        self.assertEqual(sorted(os.listdir(os.path.join(p, "storage/default"))),
                         ["moz-extension+++abc", "moz-extension+++abc^userContextId=4294967295"])
        self.assertEqual(tp.verify_release(p, self.plat, "", ZV), [])

    def test_prefs_are_an_exact_allowlist(self):
        p = make_profile(os.path.join(self.t, "src"))
        tp.sanitise(p, self.plat.keep_cookies, ZV, BUILT)
        with open(os.path.join(p, "prefs.js")) as fh:
            prefs = fh.read()
        for k in KEPT_PREFS:
            self.assertIn(f"user_pref({k}", prefs)
        for k in DROPPED_PREFS:
            self.assertNotIn(k, prefs)
        self.assertNotIn("secret.example", prefs)

    def test_cookies_filtered_to_keep_list(self):
        p = make_profile(os.path.join(self.t, "src"))
        tp.sanitise(p, self.plat.keep_cookies, ZV, BUILT)
        c = sqlite3.connect(os.path.join(p, "cookies.sqlite"))
        got = sorted(c.execute("SELECT host, name FROM moz_cookies").fetchall()); c.close()
        self.assertEqual(got, [(".tiktok.com", "cookie-consent"), ("www.tiktok.com", "cookie-consent")])
        self.assertFalse(any(f.startswith("cookies.sqlite-") for f in os.listdir(p)))
        self.assertEqual(tp.verify_release(p, self.plat, "", ZV), [])
        c = sqlite3.connect(os.path.join(p, "cookies.sqlite"))
        c.execute("INSERT INTO moz_cookies (host, name, value) VALUES ('x.example', 'sid', 'v')"); c.commit(); c.close()
        self.assertIn("cookie outside keep list: x.example sid", tp.verify_release(p, self.plat, "", ZV))

    def test_cookie_only_in_wal_survives(self):
        p = make_profile(os.path.join(self.t, "src"), cookies=[(".tiktok.com", "ttwid")], wal_only=[KEEP])
        self.assertTrue(os.path.exists(os.path.join(p, "cookies.sqlite-wal")))
        tp.sanitise(p, self.plat.keep_cookies, ZV, BUILT)
        c = sqlite3.connect(os.path.join(p, "cookies.sqlite"))
        self.assertEqual(c.execute("SELECT host, name FROM moz_cookies").fetchall(), [KEEP]); c.close()

    def test_no_keep_cookie_fails_verify(self):
        p = make_profile(os.path.join(self.t, "src"), cookies=[(".tiktok.com", "ttwid")])
        tp.sanitise(p, self.plat.keep_cookies, ZV, BUILT)
        self.assertIn("no keep cookie present", tp.verify_release(p, self.plat, "", ZV))

    def test_items_store_must_exist_and_be_empty(self):
        src = os.path.join(self.t, "a"); p = make_profile(src, with_items_db=False)
        tp.sanitise(p, self.plat.keep_cookies, ZV, BUILT)
        self.assertIn("zeeschuimer-items database not found", tp.verify_release(p, self.plat, "", ZV))
        src = os.path.join(self.t, "b"); p = make_profile(src, items_rows=1)
        tp.sanitise(p, self.plat.keep_cookies, ZV, BUILT)
        self.assertIn("zeeschuimer-items contains 1 captured items", tp.verify_release(p, self.plat, "", ZV))
        with self.assertRaises(tp.TemplateError) as cm:
            tp.release(src, os.path.join(self.t, "out"), self.plat, "1.14.2")
        self.assertIn("contains 1 captured items", str(cm.exception))
        self.assertFalse(os.path.exists(os.path.join(self.t, "out", "ddp-comments-template-tiktok-1.14.2.tar")))

    def test_upload_history_is_a_problem(self):
        p = make_profile(os.path.join(self.t, "src"), uploads_rows=2)
        tp.sanitise(p, self.plat.keep_cookies, ZV, BUILT)
        self.assertIn("zeeschuimer-items has upload history", tp.verify_release(p, self.plat, "", ZV))

    def test_storage_local_must_be_present(self):
        p = make_profile(os.path.join(self.t, "src"), storage_rows=0)
        tp.sanitise(p, self.plat.keep_cookies, ZV, BUILT)
        self.assertIn("extension storage.local database not found or empty", tp.verify_release(p, self.plat, "", ZV))

    def test_storage_local_under_wrong_uuid_does_not_count(self):
        p = make_profile(os.path.join(self.t, "src"), sl_uuid="other")
        tp.sanitise(p, self.plat.keep_cookies, ZV, BUILT)
        self.assertFalse(os.path.exists(os.path.join(p, "storage/default/moz-extension+++other^userContextId=4294967295")))
        self.assertIn("extension storage.local database not found or empty", tp.verify_release(p, self.plat, "", ZV))

    def test_uuid_missing_from_prefs(self):
        p = make_profile(os.path.join(self.t, "src"))
        tp.sanitise(p, self.plat.keep_cookies, ZV, BUILT)
        with open(os.path.join(p, "prefs.js"), "w") as fh: fh.write("")
        self.assertIn("uuid not found in prefs", tp.verify_release(p, self.plat, "", ZV))

    def test_xpi_id_missing(self):
        p = make_profile(os.path.join(self.t, "src"))
        tp.sanitise(p, self.plat.keep_cookies, ZV, BUILT)
        os.remove(os.path.join(p, XPI))
        self.assertEqual(tp.verify_release(p, self.plat, "", ZV), [])  # extensions.json still names the id
        os.remove(os.path.join(p, "extensions.json"))
        self.assertIn("xpi id missing", tp.verify_release(p, self.plat, "", ZV))

    def test_idb_rebuilt_without_deleted_rows(self):
        src = os.path.join(self.t, "src"); p = make_profile(src, deleted=40, user_version=7)
        rel = ZS + "/idb/h1.sqlite"
        with open(os.path.join(p, rel), "rb") as fh:
            self.assertIn(SECRET, fh.read())  # precondition: the deleted rows are still in the free pages
        tar_path, _ = tp.release(src, os.path.join(self.t, "out"), self.plat, "1.14.2")
        out = os.path.join(self.extract(tar_path), "profile", rel)
        with open(out, "rb") as fh:
            self.assertNotIn(SECRET, fh.read())
        c = sqlite3.connect(out)
        self.assertEqual(c.execute("PRAGMA freelist_count").fetchone()[0], 0)
        self.assertEqual(c.execute("PRAGMA user_version").fetchone()[0], 7)
        self.assertEqual(c.execute("SELECT name FROM database").fetchall(), [("zeeschuimer-items",)])
        self.assertEqual(c.execute("SELECT name FROM object_store ORDER BY id").fetchall(),
                         [("items",), ("uploads",), ("nav",), ("settings",)])
        c.close()

    def test_verify_flags_free_pages_wal_and_blobs(self):
        p = make_profile(os.path.join(self.t, "src"), deleted=40)
        tp.sanitise(p, self.plat.keep_cookies, ZV, BUILT)
        self.assertEqual(tp.verify_release(p, self.plat, "", ZV), [])
        idb = os.path.join(p, ZS, "idb")
        with open(os.path.join(idb, "h1.sqlite-wal"), "w") as fh: fh.write("x")
        os.makedirs(os.path.join(idb, "h1.files"))
        with open(os.path.join(idb, "h1.files", "1"), "w") as fh: fh.write("x")
        got = tp.verify_release(p, self.plat, "", ZV)
        self.assertIn("zeeschuimer-items has a wal file", got)
        self.assertIn("zeeschuimer-items has blob files", got)
        os.remove(os.path.join(idb, "h1.sqlite-wal"))
        c = sqlite3.connect(os.path.join(idb, "h1.sqlite"))
        c.execute("PRAGMA journal_mode=DELETE")
        for k in range(40):
            c.execute("INSERT INTO object_data (object_store_id, key, data) VALUES (2, ?, ?)", (k, b"x" * 2000))
        c.commit(); c.execute("DELETE FROM object_data WHERE object_store_id = 2"); c.commit(); c.close()
        self.assertIn("zeeschuimer-items has free pages", tp.verify_release(p, self.plat, "", ZV))

    def test_manifest_and_sha256(self):
        _, (tar_path, man) = self.release()
        self.assertEqual(os.path.basename(tar_path), "ddp-comments-template-tiktok-1.14.2.tar")
        with open(tar_path, "rb") as f:
            digest = hashlib.sha256(f.read()).hexdigest()
        self.assertEqual(man["sha256"], digest)
        self.assertEqual((man["kind"], man["platform"], man["zeeschuimer_version"], man["modules"]),
                         ("release", "tiktok", "1.14.2", ["tiktok-comments", "tiktok"]))
        self.assertRegex(man["built_on"], r"^\d{4}-\d{2}-\d{2}$")
        with open(tar_path[:-4] + ".json") as f:
            self.assertEqual(json.load(f), man)
        self.assertEqual(tp.manifest(self.plat, "9", "abc", "freeze")["kind"], "freeze")

    def test_release_replaces_stale_outputs(self):
        src = os.path.join(self.t, "src"); make_profile(src)
        out = os.path.join(self.t, "out")
        first = tp.release(src, out, self.plat, "1.14.2")[0]
        second = tp.release(src, out, self.plat, "1.14.2")[0]
        self.assertEqual(first, second)
        self.assertEqual(sorted(os.listdir(out)), ["ddp-comments-template-tiktok-1.14.2.json",
                                                   "ddp-comments-template-tiktok-1.14.2.tar"])

    def test_verify_tar_roundtrip(self):
        _, (tar_path, _) = self.release()
        self.assertEqual(tp.verify_tar(tar_path, self.plat), [])
        with open(tar_path, "ab") as f: f.write(b"\0" * 512)
        self.assertTrue(any("sha256" in x for x in tp.verify_tar(tar_path, self.plat)))

    def test_tar_holds_only_profile_and_empty_xdg(self):
        _, (tar_path, _) = self.release()
        with tarfile.open(tar_path) as tar:
            self.assertTrue(all(m.uid == 1000 and m.gid == 1000 for m in tar.getmembers()))
            names = tar.getnames()
        self.assertIn("xdg", names)
        for n in names:
            self.assertTrue(n == "xdg" or n == "profile" or n.startswith("profile/"), n)
        for leak in ("log", "downloads", "certs", ".vncpass_clear", "xdg/data"):
            self.assertFalse(any(n == leak or n.startswith(leak + "/") for n in names), leak)

    def test_symlinked_cookies_not_written_through(self):
        out = os.path.join(self.t, "outside"); os.makedirs(out)
        p = make_profile(os.path.join(self.t, "src"))
        os.replace(os.path.join(p, "cookies.sqlite"), os.path.join(out, "cookies.sqlite"))
        os.symlink(os.path.join(out, "cookies.sqlite"), os.path.join(p, "cookies.sqlite"))
        with open(os.path.join(out, "cookies.sqlite"), "rb") as fh: before = fh.read()
        tp.sanitise(p, self.plat.keep_cookies, ZV, BUILT)
        with open(os.path.join(out, "cookies.sqlite"), "rb") as fh: self.assertEqual(fh.read(), before)
        self.assertFalse(os.path.lexists(os.path.join(p, "cookies.sqlite")))
        self.assertIn("no keep cookie present", tp.verify_release(p, self.plat, "", ZV))

    def test_symlinked_extension_dir_not_followed(self):
        out = os.path.join(self.t, "outside"); os.makedirs(out)
        p = make_profile(os.path.join(self.t, "src"))
        os.replace(os.path.join(p, ZS), os.path.join(out, "ext"))
        os.symlink(os.path.join(out, "ext"), os.path.join(p, ZS))
        before = {}
        for f in tree(out):
            with open(os.path.join(out, f), "rb") as fh: before[f] = fh.read()
        tp.sanitise(p, self.plat.keep_cookies, ZV, BUILT)
        self.assertEqual(tree(out), sorted(before))
        for f, data in before.items():
            with open(os.path.join(out, f), "rb") as fh: self.assertEqual(fh.read(), data)
        self.assertFalse(os.path.lexists(os.path.join(p, ZS)))
        self.assertIn("zeeschuimer-items database not found", tp.verify_release(p, self.plat, "", ZV))

    def test_verify_rejects_stray_idb_entries(self):
        _, (tar_path, _) = self.release()
        self._retar(tar_path, [f"profile/{ZS}/idb/private.txt", f"profile/{ZS}/idb/sub/y"],
                    hook=lambda x: make_idb(os.path.join(x, "profile", ZS, "idb", "h9.sqlite"), "other-db",
                                            ["items"], {}))
        got = tp.verify_tar(tar_path, self.plat)
        for rel in ("private.txt", "sub/y", "h9.sqlite"):
            self.assertIn(f"unexpected file: profile/{ZS}/idb/{rel}", got)

    def test_only_zeeschuimer_xpi_survives(self):
        p = make_profile(os.path.join(self.t, "pre"))
        self.assertTrue(os.path.exists(os.path.join(p, "extensions/other@example.xpi")))
        tp.sanitise(p, self.plat.keep_cookies, ZV, BUILT)
        self.assertEqual(os.listdir(os.path.join(p, "extensions")), [f"{XID}.xpi"])
        _, (tar_path, _) = self.release()
        self._retar(tar_path, ["profile/extensions/other@example.xpi"])
        self.assertIn("unexpected file: profile/extensions/other@example.xpi", tp.verify_tar(tar_path, self.plat))

    def test_extension_json_filtered_and_normalised(self):
        p = make_profile(os.path.join(self.t, "src"))
        tp.sanitise(p, self.plat.keep_cookies, ZV, BUILT)
        ext = read_json(p, "extensions.json")
        self.assertEqual(ext["schemaVersion"], 35)
        self.assertEqual([a["id"] for a in ext["addons"]], [XID])
        e = ext["addons"][0]
        self.assertEqual(e["path"], CPATH)
        self.assertEqual(e["syncGUID"], "{00000000-0000-4000-8000-000000000000}")
        self.assertEqual((e["installDate"], e["updateDate"]), (BUILT_MS, BUILT_MS))
        self.assertNotIn("installTelemetryInfo", e)
        self.assertEqual(e["sourceURI"], POLICY_URL)
        for k in ("signedState", "startupData", "targetApplications", "defaultLocale", "userDisabled"):
            self.assertIn(k, e)
        with open(os.path.join(p, "extensions.json")) as fh:
            txt = fh.read()
        self.assertNotIn("other@example", txt)
        self.assertNotIn("/home/someone", txt)
        self.assertFalse(os.path.exists(os.path.join(p, "extension-settings.json")))
        self.assertFalse(os.path.exists(os.path.join(p, "extension-preferences.json")))
        self.assertEqual(tp.verify_release(p, self.plat, "", ZV), [])

    def test_foreign_source_uri_removed_and_other_telemetry_keys_and_rooturi(self):
        e = zs_entry(sourceURI="https://example.org/private/x.xpi", fooTelemetryBar=1,
                     rootURI="jar:file:///home/someone/x.xpi!/")
        p = make_profile(os.path.join(self.t, "src"), entry=e)
        tp.sanitise(p, self.plat.keep_cookies, ZV, BUILT)
        got = read_json(p, "extensions.json")["addons"][0]
        self.assertNotIn("sourceURI", got)
        self.assertNotIn("fooTelemetryBar", got)
        self.assertEqual(got["rootURI"], f"jar:file://{CPATH}!/")
        self.assertEqual(tp.verify_release(p, self.plat, "", ZV), [])

    def test_verify_requires_normalised_entry(self):
        _, (tar_path, _) = self.release()
        def hook(x):
            pr = os.path.join(x, "profile")
            ext = read_json(pr, "extensions.json")
            ext["addons"][0].update(path=f"/home/someone/{XPI}", syncGUID="{abc}", sourceURI="https://evil.example/x",
                                    installTelemetryInfo={}, other="/home/someone/y")
            write_json(pr, "extensions.json", ext)
        self._retar(tar_path, [], hook=hook)
        got = tp.verify_tar(tar_path, self.plat)
        for f in ("path", "syncGUID", "sourceURI", "installTelemetryInfo", "other"):
            self.assertIn(f"extensions.json entry not normalised: {f}", got)

    def test_extension_settings_file_in_tar_is_unexpected(self):
        _, (tar_path, _) = self.release()
        self._retar(tar_path, ["profile/extension-settings.json"])
        self.assertIn("unexpected file: profile/extension-settings.json", tp.verify_tar(tar_path, self.plat))

    def test_unparseable_extensions_json_is_an_error(self):
        p = make_profile(os.path.join(self.t, "src"))
        with open(os.path.join(p, "extensions.json"), "w") as fh: fh.write("{nope")
        with self.assertRaises(tp.TemplateError):
            tp.sanitise(p, self.plat.keep_cookies, ZV, BUILT)

    def test_verify_flags_foreign_ids_and_missing_xpi_listing(self):
        _, (tar_path, _) = self.release()
        def hook(x):
            write_json(os.path.join(x, "profile"), "extensions.json", {"schemaVersion": 35, "addons": [
                {"id": XID}, {"id": "evil@example"}]})
        self._retar(tar_path, [], hook=hook)
        got = tp.verify_tar(tar_path, self.plat)
        self.assertIn("foreign addon id in extensions.json: evil@example", got)
        self._retar(tar_path, [], hook=lambda x: write_json(os.path.join(x, "profile"), "extensions.json",
                                                            {"schemaVersion": 35, "addons": []}))
        self.assertIn("extensions.json does not list the xpi", tp.verify_tar(tar_path, self.plat))

    def test_tar_mtimes_normalised(self):
        src = os.path.join(self.t, "src"); p = make_profile(src)
        old = os.path.join(p, "chrome", "userContent.css")
        os.utime(old, (1_000_000_000, 1_000_000_000))
        _, (tar_path, man) = self.release_from(src)
        y, m, d = map(int, man["built_on"].split("-"))
        want = int(datetime.datetime(y, m, d, tzinfo=datetime.timezone.utc).timestamp())
        with tarfile.open(tar_path) as tar:
            mt = {x.mtime for x in tar.getmembers()}
        self.assertEqual(mt, {want})

    def release_from(self, src):
        return src, tp.release(src, os.path.join(self.t, "out"), self.plat, "1.14.2")

    def _manifest_edit(self, tar_path, **changes):
        mp = tar_path[:-4] + ".json"
        with open(mp) as fh: man = json.load(fh)
        man.update(changes)
        with open(mp, "w") as fh: json.dump(man, fh)
        man["sha256"] = tp._sha256(tar_path)
        with open(mp, "w") as fh: json.dump(man, fh)

    def test_verify_tar_checks_manifest_fields(self):
        _, (tar_path, _) = self.release()
        self._manifest_edit(tar_path, kind="freeze")
        self.assertIn("manifest kind is freeze, not release", tp.verify_tar(tar_path, self.plat))
        self._manifest_edit(tar_path, kind="release", zeeschuimer_version="9.9")
        self.assertIn("manifest zeeschuimer_version is 9.9, not 1.14.2", tp.verify_tar(tar_path, self.plat))
        self._manifest_edit(tar_path, zeeschuimer_version="1.14.2", modules=["other"])
        self.assertTrue(any(x.startswith("manifest modules are ['other']") for x in tp.verify_tar(tar_path, self.plat)))
        self._manifest_edit(tar_path, modules=self.plat.zeeschuimer_modules)
        self.assertEqual(tp.verify_tar(tar_path, self.plat), [])

    def test_deleted_cookie_values_do_not_survive(self):
        src = os.path.join(self.t, "src"); p = make_profile(src, wal_only=[(".x.example", "sid")])
        with open(os.path.join(p, "cookies.sqlite"), "rb") as fh:
            self.assertIn(b"deleted-secret-", fh.read())  # precondition
        tar_path, _ = tp.release(src, os.path.join(self.t, "out"), self.plat, "1.14.2")
        with open(os.path.join(self.extract(tar_path), "profile", "cookies.sqlite"), "rb") as fh:
            data = fh.read()
        self.assertNotIn(b"deleted-secret-", data)
        self.assertIn(b"consent-ok", data)

    def test_symlink_member_in_profile_is_flagged(self):
        _, (tar_path, _) = self.release()
        self._retar(tar_path, [], hook=lambda x: os.symlink("prefs.js", os.path.join(x, "profile", "link")))
        self.assertIn("unexpected file: profile/link", tp.verify_tar(tar_path, self.plat))

    def test_leftover_rebuilt_and_tmp_files_in_idb(self):
        p = make_profile(os.path.join(self.t, "src"))
        tp.sanitise(p, self.plat.keep_cookies, ZV, BUILT)
        idb = os.path.join(p, ZS, "idb")
        for n in ("h1.sqlite.rebuilt", "h1.sqlite.tmp"):
            with open(os.path.join(idb, n), "w") as fh: fh.write("x")
        got = tp.verify_release(p, self.plat, "", ZV)
        for n in ("h1.sqlite.rebuilt", "h1.sqlite.tmp"):
            self.assertIn(f"unexpected file: storage/default/moz-extension+++abc/idb/{n}", got)
        # the sanitiser itself sweeps such leftovers out of a source copy
        src = os.path.join(self.t, "src2"); q = make_profile(src)
        with open(os.path.join(q, ZS, "idb", "h1.sqlite.rebuilt"), "w") as fh: fh.write("x")
        tar_path, _ = tp.release(src, os.path.join(self.t, "out"), self.plat, "1.14.2")
        with tarfile.open(tar_path) as tar:
            self.assertFalse(any(n.endswith((".rebuilt", ".tmp")) for n in tar.getnames()))

    def test_tar_written_atomically(self):
        out = os.path.join(self.t, "out")
        self.release()
        self.assertFalse(any(f.endswith(".tmp") for f in os.listdir(out)))
        src = os.path.join(self.t, "src2"); make_profile(src)
        out2 = os.path.join(self.t, "out2")
        real = tarfile.open
        def boom(name, mode="r", *a, **kw):
            real(name, mode).close()
            raise OSError("disk full")
        tarfile.open = boom
        try:
            with self.assertRaises(OSError):
                tp.release(src, out2, self.plat, "1.14.2")
        finally:
            tarfile.open = real
        self.assertEqual(os.listdir(out2), [])

    def _retar(self, tar_path, extra, hook=None):
        x = self.extract(tar_path, "tamper")
        if hook: hook(x)
        for rel in extra:
            os.makedirs(os.path.dirname(os.path.join(x, rel)), exist_ok=True)
            with open(os.path.join(x, rel), "w") as fh: fh.write("x")
        with tarfile.open(tar_path, "w") as tar:
            for n in sorted(os.listdir(x)):
                tar.add(os.path.join(x, n), arcname=n)

    def test_verify_tar_reports_unexpected_files(self):
        _, (tar_path, _) = self.release()
        self._retar(tar_path, ["profile/logins.json", "log/x", "profile/places.sqlite"])
        got = tp.verify_tar(tar_path, self.plat)
        for want in ("unexpected file: profile/logins.json", "unexpected file: log/x",
                     "unexpected file: profile/places.sqlite"):
            self.assertIn(want, got)

    def test_source_untouched(self):
        src, _ = self.release()
        before = tree(src)
        c = sqlite3.connect(os.path.join(src, "profile", "cookies.sqlite"))
        self.assertEqual(c.execute("SELECT count(*) FROM moz_cookies").fetchone()[0], len(COOKIES)); c.close()
        for f in ("profile/places.sqlite", "profile/logins.json", "profile/sessionstore.jsonlz4", "log/access.log"):
            self.assertIn(f, before)

    def test_cli_release_and_verify(self):
        src = os.path.join(self.t, "src"); make_profile(src)
        out = os.path.join(self.t, "out")
        common = ["--platform", "tiktok", "--platforms-dir", os.path.join(REPO, "platforms")]
        r = subprocess.run([sys.executable, CLI, "release", *common, "--zeeschuimer-version", "1.14.2", src, out],
                           capture_output=True, text=True)
        self.assertEqual(r.returncode, 0, r.stderr)
        tar_path = os.path.join(out, "ddp-comments-template-tiktok-1.14.2.tar")
        r = subprocess.run([sys.executable, CLI, "verify", *common, tar_path], capture_output=True, text=True)
        self.assertEqual(r.returncode, 0, r.stderr)
        with open(tar_path, "ab") as f: f.write(b"\0" * 512)
        r = subprocess.run([sys.executable, CLI, "verify", *common, tar_path], capture_output=True, text=True)
        self.assertEqual(r.returncode, 1)
        self.assertIn("problem: sha256", r.stderr)
        r = subprocess.run([sys.executable, CLI, "verify"], capture_output=True, text=True)
        self.assertEqual(r.returncode, 2)


if __name__ == "__main__":
    unittest.main()
