"""Tests for desk/make-template.py: only the 4CAT login survives, the source is untouched."""
import os, sqlite3, subprocess, sys, tarfile, tempfile, unittest

REPO = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
SCRIPT = os.path.join(REPO, "desk", "make-template.py")
FQDN = "desk.example.nl"
COOKIES = [(FQDN, "remember_token"), (FQDN, "Authorization"), (FQDN, "redirecturi"),
           ("proxy.sram.surf.nl", "SATOSA_surf_ram_PROXY_STATE"), (".tiktok.com", "ttwid"), ("evil." + FQDN, "remember_token")]


def make_profile(root):
    p = os.path.join(root, "profile")
    for d in ("cache2/entries", "startupCache", "sessionstore-backups",
              "storage/default/moz-extension+++abc", "storage/default/https+++www.tiktok.com",
              "storage/default/https+++ds.sram.surf.nl"):
        os.makedirs(os.path.join(p, d))
    for f in ("cache2/entries/x", "sessionstore.jsonlz4", "sessionstore-backups/recovery.jsonlz4",
              "storage/default/moz-extension+++abc/z.sqlite", "prefs.js"):
        with open(os.path.join(p, f), "w") as fh: fh.write("x")
    c = sqlite3.connect(os.path.join(p, "cookies.sqlite"))
    c.execute("CREATE TABLE moz_cookies (id INTEGER PRIMARY KEY, host TEXT, name TEXT, value TEXT, expiry INTEGER)")
    c.executemany("INSERT INTO moz_cookies (host, name, value, expiry) VALUES (?, ?, 'secret', 0)", COOKIES)
    c.commit(); c.close()
    os.makedirs(os.path.join(root, "xdg"))


class MakeTemplateTest(unittest.TestCase):
    def test_strips_identity_and_keeps_extension_and_4cat_login(self):
        with tempfile.TemporaryDirectory() as t:
            src = os.path.join(t, "firefox-1"); make_profile(src)
            out = os.path.join(t, "templates", "firefox-profile.tar")
            r = subprocess.run([sys.executable, SCRIPT, "--fqdn", FQDN, src, out], capture_output=True, text=True)
            self.assertEqual(r.returncode, 0, r.stderr)
            x = os.path.join(t, "x"); os.makedirs(x)
            with tarfile.open(out) as tar:
                self.assertTrue(all(m.uid == 1000 and m.gid == 1000 for m in tar.getmembers()))
                tar.extractall(x, filter="data")
            p = os.path.join(x, "profile")
            c = sqlite3.connect(os.path.join(p, "cookies.sqlite"))
            self.assertEqual(c.execute("SELECT host, name FROM moz_cookies").fetchall(), [(FQDN, "remember_token")])
            c.close()
            self.assertTrue(os.path.exists(os.path.join(p, "storage/default/moz-extension+++abc/z.sqlite")))
            self.assertEqual(os.listdir(os.path.join(p, "storage/default")), ["moz-extension+++abc"])
            for gone in ("cache2", "startupCache", "sessionstore-backups", "sessionstore.jsonlz4"):
                self.assertFalse(os.path.exists(os.path.join(p, gone)), gone)
            self.assertTrue(os.path.exists(os.path.join(p, "prefs.js")))
            self.assertTrue(os.path.isdir(os.path.join(x, "xdg")))
            # source untouched
            c = sqlite3.connect(os.path.join(src, "profile", "cookies.sqlite"))
            self.assertEqual(c.execute("SELECT count(*) FROM moz_cookies").fetchone()[0], len(COOKIES)); c.close()
            self.assertTrue(os.path.exists(os.path.join(src, "profile", "sessionstore.jsonlz4")))


if __name__ == "__main__":
    unittest.main()
