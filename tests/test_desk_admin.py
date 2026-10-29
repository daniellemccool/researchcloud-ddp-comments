"""Tests for the desk-admin CLI (bin/desk-admin), run as a subprocess against the fake docker and 4CAT."""
import os, sys, tempfile, unittest

REPO = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
sys.path.insert(0, os.path.join(REPO, "desk"))
from desklib.config import DeskConfig, save_config  # noqa: E402

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import fakes  # noqa: E402

CLI = os.path.join(REPO, "bin", "desk-admin")


def slurp(p):
    with open(p) as f:
        return f.read()


class DeskAdminTests(unittest.TestCase):
    def setUp(self):
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        self.t = tmp.name
        self.run_ = fakes.fake_docker(self.t)
        self.srv = fakes.FakeFourCat(check=lambda u, p: True)
        self.addCleanup(self.srv.close)
        self.root = os.path.join(self.t, "desk")
        save_config(DeskConfig(desk_root=self.root, repo_dir=REPO, platform="tiktok",
                               collector_role="src_co_students", admin_role="src_co_admin",
                               contact="x", fourcat_url=self.srv.url))

    def cmd(self, *args):
        return self.run_([CLI, *args, "--desk-root", self.root], capture_output=True, text=True)

    def put(self, name, text):
        with open(os.path.join(self.t, name), "w") as f:
            f.write(text)

    def pw_path(self, u):
        return os.path.join(self.root, "secrets", "4cat-passwords", u)

    def tok_path(self, u):
        return os.path.join(self.root, "secrets", "4cat-tokens", u, "4cat-token")

    def test_show_refuses_without_password(self):
        r = self.cmd("4cat-user", "show", "alice")
        self.assertEqual(r.returncode, 1)
        self.assertIn("alice", r.stderr)
        self.assertEqual(r.stdout, "")
        os.makedirs(os.path.dirname(self.pw_path("alice")))
        with open(self.pw_path("alice"), "w") as f:
            f.write("s3cret\n")
        r = self.cmd("4cat-user", "show", "alice")
        self.assertEqual((r.returncode, r.stdout), (0, "s3cret\n"))

    def test_adopt_sets_password_and_token(self):
        r = self.cmd("4cat-user", "adopt", "alice")   # no 4CAT account: refused, nothing stored
        self.assertEqual(r.returncode, 1)
        self.assertFalse(os.path.exists(self.pw_path("alice")))
        self.put("psql", "1\n")
        r = self.cmd("4cat-user", "adopt", "alice")
        self.assertEqual(r.returncode, 0, r.stderr)
        pw = slurp(self.pw_path("alice"))
        self.assertTrue(pw)
        self.assertNotIn(pw, r.stdout + r.stderr)
        self.assertEqual(slurp(self.tok_path("alice")), "tok-alice")
        self.assertEqual(slurp(os.path.join(self.t, "stdin")).strip(), pw)  # hashed from the stored password
        self.assertEqual(self.srv.logins, ["alice"])

    def test_rotate(self):
        os.makedirs(os.path.dirname(self.pw_path("alice")))
        with open(self.pw_path("alice"), "w") as f:
            f.write("old")
        r = self.cmd("4cat-token", "rotate", "alice")
        self.assertEqual(r.returncode, 0, r.stderr)
        self.assertNotIn(slurp(self.pw_path("alice")), ("old", ""))
        self.assertEqual(slurp(self.tok_path("alice")), "tok-alice")

    def test_disabled_fourcat_and_bad_user_fail_cleanly(self):
        save_config(DeskConfig(desk_root=self.root, repo_dir=REPO, platform="tiktok",
                               collector_role="src_co_students", admin_role="src_co_admin",
                               contact="x", fourcat_enabled=False))
        r = self.cmd("4cat-user", "show", "alice")
        self.assertEqual(r.returncode, 1)
        self.assertIn("disabled", r.stderr)
        save_config(DeskConfig(desk_root=self.root, repo_dir=REPO, platform="tiktok",
                               collector_role="src_co_students", admin_role="src_co_admin",
                               contact="x", fourcat_url=self.srv.url))
        r = self.cmd("4cat-token", "rotate", "Bad Name")
        self.assertEqual(r.returncode, 1)
        self.assertIn("desk-admin 4cat-token rotate: ValueError", r.stderr)


if __name__ == "__main__":
    unittest.main()
