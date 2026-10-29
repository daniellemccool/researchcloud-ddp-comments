"""Tests for desk/desklib/template.py: allowlist sanitising, empty-store proof, manifest, tar."""
import hashlib, json, os, sqlite3, sys, tarfile, tempfile, unittest

REPO = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
sys.path.insert(0, os.path.join(REPO, "desk"))
from desklib import platform as pf, template as tp  # noqa: E402

XPI = "extensions/{677bb78c-7168-4aa5-8e9b-32e9f26c6157}.xpi"
KEEP = (".tiktok.com", "cookie-consent")
COOKIES = [KEEP, ("www.tiktok.com", "cookie-consent"), (".tiktok.com", "ttwid"), ("desk.example.nl", "remember_token"),
           ("proxy.sram.surf.nl", "SATOSA_surf_ram_PROXY_STATE"), ("evil.example", "cookie-consent")]
PREFS = "\n".join([
    'user_pref("extensions.webextensions.uuids", "{\\"z@x\\":\\"abc\\"}");',
    'user_pref("network.proxy.type", 1);',
    'user_pref("browser.startup.homepage", "about:blank");',
    'user_pref("toolkit.legacyUserProfileCustomizations.stylesheets", true);',
    'user_pref("extensions.activeThemeID", "x");',
    'user_pref("browser.formfill.enable", true);',
    'user_pref("places.history.enabled", true);',
    'user_pref("app.update.lastUpdateTime", 5);', ""])


def make_idb(path, name, stores, rows):
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
    c.commit(); c.close()


def make_profile(root, items_rows=0, uploads_rows=0, with_items_db=True, storage_rows=3):
    p = os.path.join(root, "profile")
    for d in ("cache2/entries", "startupCache", "sessionstore-backups", "crashes", "extensions",
              "storage/default/https+++www.tiktok.com", "storage/default/https+++ds.sram.surf.nl", "chrome"):
        os.makedirs(os.path.join(p, d))
    ext = os.path.join(p, "storage/default/moz-extension+++abc")
    if with_items_db:
        make_idb(os.path.join(ext, "idb", "h1.sqlite"), "zeeschuimer-items", ["items", "uploads", "nav", "settings"],
                 {"items": items_rows, "uploads": uploads_rows})
    make_idb(os.path.join(p, "storage/default/moz-extension+++abc^userContextId=4294967295/idb/h2.sqlite"),
             "webExtensions-storage-local", ["storage-local-data"], {"storage-local-data": storage_rows})
    for f in ("cache2/entries/x", "sessionstore.jsonlz4", "sessionstore-backups/recovery.jsonlz4", "crashes/c",
              "places.sqlite", "logins.json", "key4.db", "formhistory.sqlite", "extensions.json",
              "extension-preferences.json", "extension-settings.json", "chrome/userContent.css", "sessionstore-x.baklz4",
              "storage/default/https+++www.tiktok.com/z.sqlite", XPI):
        with open(os.path.join(p, f), "w") as fh: fh.write("x")
    with open(os.path.join(p, "extensions.json"), "w") as fh:
        fh.write('{"addons": [{"id": "{677bb78c-7168-4aa5-8e9b-32e9f26c6157}"}]}')
    with open(os.path.join(p, "prefs.js"), "w") as fh: fh.write(PREFS)
    c = sqlite3.connect(os.path.join(p, "cookies.sqlite"))
    c.execute("CREATE TABLE moz_cookies (id INTEGER PRIMARY KEY, host TEXT, name TEXT, value TEXT, expiry INTEGER)")
    c.executemany("INSERT INTO moz_cookies (host, name, value, expiry) VALUES (?, ?, 'secret', 0)", COOKIES)
    c.commit(); c.close()
    os.makedirs(os.path.join(root, "xdg"))
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

    def test_release_keeps_only_allowlist(self):
        src = os.path.join(self.t, "src"); p = make_profile(src)
        tp.sanitise(p, self.plat.keep_cookies)
        files = tree(p)
        for gone in ("places.sqlite", "logins.json", "key4.db", "formhistory.sqlite", "sessionstore.jsonlz4",
                     "sessionstore-x.baklz4", "cache2", "crashes", "startupCache", "storage/default/https+++www.tiktok.com"):
            self.assertFalse(any(f == gone or f.startswith(gone + "/") for f in files), gone)
        for kept in ("prefs.js", "extensions.json", "extension-preferences.json", "extension-settings.json",
                     "cookies.sqlite", "chrome/userContent.css", XPI,
                     "storage/default/moz-extension+++abc/idb/h1.sqlite",
                     "storage/default/moz-extension+++abc^userContextId=4294967295/idb/h2.sqlite"):
            self.assertIn(kept, files)
        self.assertEqual(sorted(os.listdir(os.path.join(p, "storage/default"))),
                         ["moz-extension+++abc", "moz-extension+++abc^userContextId=4294967295"])
        with open(os.path.join(p, "prefs.js")) as fh:
            prefs = fh.read()
        for kept in ("extensions.webextensions.uuids", "network.proxy.type", "browser.startup.homepage",
                     "toolkit.legacyUserProfileCustomizations", "extensions.activeThemeID"):
            self.assertIn(kept, prefs)
        for gone in ("browser.formfill", "places.history", "app.update"):
            self.assertNotIn(gone, prefs)

    def test_cookies_filtered_to_keep_list(self):
        src = os.path.join(self.t, "src"); p = make_profile(src)
        tp.sanitise(p, self.plat.keep_cookies)
        c = sqlite3.connect(os.path.join(p, "cookies.sqlite"))
        got = sorted(c.execute("SELECT host, name FROM moz_cookies").fetchall()); c.close()
        self.assertEqual(got, [(".tiktok.com", "cookie-consent"), ("www.tiktok.com", "cookie-consent")])
        self.assertFalse(any(f.startswith("cookies.sqlite-") for f in os.listdir(p)))
        self.assertEqual(tp.verify_release(p, self.plat), [])
        c = sqlite3.connect(os.path.join(p, "cookies.sqlite"))
        c.execute("INSERT INTO moz_cookies (host, name, value) VALUES ('x.example', 'sid', 'v')"); c.commit(); c.close()
        self.assertIn("cookie outside keep list: x.example sid", tp.verify_release(p, self.plat))

    def test_items_store_must_exist_and_be_empty(self):
        src = os.path.join(self.t, "a"); p = make_profile(src, with_items_db=False)
        tp.sanitise(p, self.plat.keep_cookies)
        self.assertIn("zeeschuimer-items database not found", tp.verify_release(p, self.plat))
        src = os.path.join(self.t, "b"); p = make_profile(src, items_rows=1)
        tp.sanitise(p, self.plat.keep_cookies)
        self.assertIn("zeeschuimer-items contains 1 captured items", tp.verify_release(p, self.plat))
        with self.assertRaises(tp.TemplateError) as cm:
            tp.release(src, os.path.join(self.t, "out"), self.plat, "1.14.2")
        self.assertIn("contains 1 captured items", str(cm.exception))
        self.assertFalse(os.path.exists(os.path.join(self.t, "out", "ddp-comments-template-tiktok-1.14.2.tar")))

    def test_upload_history_is_a_problem(self):
        p = make_profile(os.path.join(self.t, "src"), uploads_rows=2)
        tp.sanitise(p, self.plat.keep_cookies)
        self.assertIn("zeeschuimer-items has upload history", tp.verify_release(p, self.plat))

    def test_storage_local_must_be_present(self):
        p = make_profile(os.path.join(self.t, "src"), storage_rows=0)
        tp.sanitise(p, self.plat.keep_cookies)
        self.assertIn("extension storage.local database not found or empty", tp.verify_release(p, self.plat))

    def test_xpi_id_missing(self):
        p = make_profile(os.path.join(self.t, "src"))
        tp.sanitise(p, self.plat.keep_cookies)
        os.remove(os.path.join(p, XPI))
        self.assertTrue(tp.verify_release(p, self.plat) == [])  # extensions.json still names the id
        os.remove(os.path.join(p, "extensions.json"))
        self.assertIn("xpi id missing", tp.verify_release(p, self.plat))

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

    def test_verify_tar_roundtrip(self):
        _, (tar_path, _) = self.release()
        self.assertEqual(tp.verify_tar(tar_path, self.plat), [])
        with tarfile.open(tar_path) as tar:
            self.assertTrue(all(m.uid == 1000 and m.gid == 1000 for m in tar.getmembers()))
            self.assertIn("xdg", tar.getnames())
        with open(tar_path, "ab") as f: f.write(b"\0" * 512)
        self.assertTrue(any("sha256" in x for x in tp.verify_tar(tar_path, self.plat)))

    def test_source_untouched(self):
        src, _ = self.release()
        before = tree(src)
        c = sqlite3.connect(os.path.join(src, "profile", "cookies.sqlite"))
        self.assertEqual(c.execute("SELECT count(*) FROM moz_cookies").fetchone()[0], len(COOKIES)); c.close()
        self.assertIn("places.sqlite", [os.path.basename(f) for f in before])
        self.assertTrue(os.path.exists(os.path.join(src, "profile", "sessionstore.jsonlz4")))
        self.assertIn("profile/logins.json", before)


if __name__ == "__main__":
    unittest.main()
