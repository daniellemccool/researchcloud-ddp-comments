"""Tests for the desk-admin CLI (bin/desk-admin), run as a subprocess against the fake docker and 4CAT."""
import contextlib, io, os, sys, tempfile, unittest
from unittest import mock

REPO = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
sys.path.insert(0, os.path.join(REPO, "desk"))
import desk_admin  # noqa: E402
from desklib import coapi  # noqa: E402
from desklib.config import DeskConfig, save_config  # noqa: E402
from desklib.fourcat import FourCat  # noqa: E402
from desklib.locks import flocked, user_lock  # noqa: E402
from desklib.registry import Registry  # noqa: E402
from desklib.locks import render_lock  # noqa: E402

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
        self.srv = fakes.FakeFourCat(check=lambda u, p: self.stored_pw(u) == p)   # login only with the file's password
        self.addCleanup(self.srv.close)
        self.root = os.path.join(self.t, "desk")
        save_config(DeskConfig(desk_root=self.root, repo_dir=REPO, platform="tiktok",
                               collector_role="src_co_students", admin_role="src_co_admin",
                               contact="x", fourcat_url=self.srv.url))

    def stored_pw(self, u):
        try:
            return slurp(self.pw_path(u))
        except FileNotFoundError:
            return None

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
        for bad in ("Bad Name", "../x"):
            r = self.cmd("4cat-token", "rotate", bad)
            self.assertEqual(r.returncode, 2)
            self.assertIn("invalid username", r.stderr)
        self.assertEqual(fakes.docker_calls(self.t), [])

    def test_adopt_and_rotate_run_under_the_user_lock(self):
        self.put("psql", "1\n")
        for argv in (["4cat-user", "adopt", "alice"], ["4cat-token", "rotate", "alice"]):
            r = self.cmd(*argv)
            self.assertEqual(r.returncode, 0, r.stderr)
            self.assertTrue(os.path.exists(user_lock(self.root, "alice")))
        held = []

        def rotate(fc, u):
            try:
                with flocked(user_lock(self.root, u), timeout=0.05):
                    held.append(False)
            except TimeoutError:
                held.append(True)
        with mock.patch.object(FourCat, "rotate", rotate), contextlib.redirect_stdout(io.StringIO()), \
                mock.patch.object(FourCat, "account_exists", lambda fc, u: True):
            for argv in (["4cat-user", "adopt", "alice"], ["4cat-token", "rotate", "alice"]):
                self.assertEqual(desk_admin.main([*argv, "--desk-root", self.root]), 0)
        self.assertEqual(held, [True, True])

    def test_sync_sets_admin_from_members(self):
        reg = Registry(self.root)
        with flocked(render_lock(self.root)):
            reg.allocate("alice")
        os.makedirs(os.path.join(self.root, "state", "users", "alice"))
        with open(os.path.join(self.root, "state", "users", "alice", "provision.json"), "w") as f:
            f.write('{"steps": {"fourcat_account": "done"}}')
        self.put("psql", "[]\n")   # alice's tags: not an admin yet
        env = {k: os.path.join(self.t, v) for k, v in fakes.CONTROL.items()}
        env["PATH"] = os.path.join(self.t, "bin") + os.pathsep + os.environ["PATH"]
        out, err = io.StringIO(), io.StringIO()
        with mock.patch.dict(os.environ, env), \
                mock.patch.object(coapi, "workspace_info", lambda: {}), \
                mock.patch.object(coapi, "co_members", lambda info: [
                    {"username": "alice", "roles": ["src_co_students", "src_co_admin"]}]), \
                contextlib.redirect_stdout(out), contextlib.redirect_stderr(err):
            rc = desk_admin.main(["4cat-user", "sync", "--desk-root", self.root])
        self.assertEqual((rc, err.getvalue()), (0, ""))
        self.assertEqual(out.getvalue(), "admin_set: alice\nadmin_cleared: -\n")
        self.assertTrue(any("UPDATE users SET tags" in c and "admin" in c for c in fakes.docker_calls(self.t)))


if __name__ == "__main__":
    unittest.main()
