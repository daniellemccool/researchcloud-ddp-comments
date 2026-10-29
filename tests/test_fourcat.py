"""Tests for desk/desklib/fourcat.py: fake `docker` on PATH plus an in-process fake 4CAT."""
import http.server, os, stat, subprocess, sys, tempfile, threading, unittest, urllib.parse

REPO = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
sys.path.insert(0, os.path.join(REPO, "desk"))
from desklib import fourcat as fc  # noqa: E402
from desklib.config import DeskConfig  # noqa: E402

GOOD_HASH = "$2b$12$" + "a" * 53
FAKE_DOCKER = """#!/usr/bin/env bash
echo "$*" >> "$FAKE_DOCKER_CALLS"
case "$*" in
  *create_user.py*) [ -f "$FAKE_DOCKER_FAIL" ] && { echo "boom" >&2; exit 1; };;
  *bcrypt*) cat > "$FAKE_DOCKER_STDIN"; if [ -f "$FAKE_DOCKER_HASH" ]; then cat "$FAKE_DOCKER_HASH"; else echo '""" + GOOD_HASH + """'; fi;;
  *UPDATE*) if [ -f "$FAKE_DOCKER_UPDATE" ]; then cat "$FAKE_DOCKER_UPDATE"; else echo "UPDATE 1"; fi;;
  *psql*) cat "$FAKE_DOCKER_PSQL" 2>/dev/null;;
esac
exit 0
"""

PASSWORDS = {"alice": "pw-alice"}


class Handler(http.server.BaseHTTPRequestHandler):
    def log_message(self, *a):
        pass

    def do_POST(self):
        n = int(self.headers.get("Content-Length", 0))
        form = urllib.parse.parse_qs(self.rfile.read(n).decode())
        user = form.get("username", [""])[0]
        if user == "nocookie":
            self.send_response(302)
        elif user == "broken":
            self.send_response(500)
        elif self.path == "/login/" and PASSWORDS.get(user) == form.get("password", [""])[0]:
            self.send_response(302)
            self.send_header("Location", "/")
            self.send_header("Set-Cookie", f"remember_token=tok-{user}; Path=/")
        else:
            self.send_response(401)
        self.send_header("Content-Length", "0")
        self.end_headers()


def slurp(p):
    with open(p) as f:
        return f.read()


class FourCatTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        t = self.tmp.name
        bindir = os.path.join(t, "bin")
        os.mkdir(bindir)
        d = os.path.join(bindir, "docker")
        with open(d, "w") as f:
            f.write(FAKE_DOCKER)
        os.chmod(d, 0o755)
        self.calls = os.path.join(t, "calls")
        self.psql = os.path.join(t, "psql")
        env = dict(os.environ, PATH=bindir + os.pathsep + os.environ["PATH"],
                   FAKE_DOCKER_CALLS=self.calls, FAKE_DOCKER_PSQL=self.psql,
                   FAKE_DOCKER_STDIN=os.path.join(t, "stdin"), FAKE_DOCKER_HASH=os.path.join(t, "hash"),
                   FAKE_DOCKER_UPDATE=os.path.join(t, "update"), FAKE_DOCKER_FAIL=os.path.join(t, "fail"))
        self.t = t

        def run(*a, **kw):
            return subprocess.run(*a, **{**kw, "env": env})
        self.srv = http.server.ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        threading.Thread(target=self.srv.serve_forever, daemon=True).start()
        self.addCleanup(self.srv.server_close)
        self.addCleanup(self.srv.shutdown)
        self.cfg = DeskConfig(desk_root=os.path.join(t, "desk"), repo_dir=REPO, platform="tiktok",
                              collector_role="src_co_c", admin_role="src_co_a", contact="x",
                              fourcat_url=f"http://127.0.0.1:{self.srv.server_address[1]}")
        self.fc = fc.FourCat(self.cfg, run=run)

    def put(self, name, text):
        with open(os.path.join(self.t, name), "w") as f:
            f.write(text)

    def calls_text(self):
        return slurp(self.calls) if os.path.exists(self.calls) else ""

    def test_ensure_password_creates_once_with_mode_600(self):
        pw, created = self.fc.ensure_password("alice")
        self.assertTrue(created)
        self.assertGreaterEqual(len(pw), 24)
        p = self.fc.password_path("alice")
        self.assertEqual(stat.S_IMODE(os.stat(p).st_mode), 0o600)
        self.assertEqual(stat.S_IMODE(os.stat(os.path.dirname(p)).st_mode), 0o700)
        self.assertEqual(slurp(p), pw)
        self.assertEqual(self.fc.ensure_password("alice"), (pw, False))
        for d in ("secrets", os.path.join("secrets", "4cat-passwords")):
            self.assertEqual(stat.S_IMODE(os.stat(os.path.join(self.cfg.desk_root, d)).st_mode), 0o700)

    def test_existing_password_wins_and_empty_is_corrupt(self):
        p = self.fc.password_path("alice")
        os.makedirs(os.path.dirname(p))
        with open(p, "w") as f:
            f.write("winner")
        self.assertEqual(self.fc.ensure_password("alice"), ("winner", False))
        with open(p, "w") as f:
            f.write(" \n")
        with self.assertRaises(fc.FourCatError):
            self.fc.ensure_password("alice")
        # exclusive publish loses cleanly and leaves no temp files
        with self.assertRaises(FileExistsError):
            self.fc._write(p, "x", exclusive=True)
        self.assertEqual(os.listdir(os.path.dirname(p)), ["alice"])

    def test_update_of_missing_user_is_an_error(self):
        self.put("update", "UPDATE 0\n")
        with self.assertRaises(fc.FourCatError):
            self.fc.set_admin("ghost", True)
        with self.assertRaises(fc.FourCatError):
            self.fc.set_password("ghost", "pw")
        self.put("psql", "")
        with self.assertRaises(fc.FourCatError):
            self.fc.is_admin("ghost")
        self.put("psql", "[]\n")
        self.assertFalse(self.fc.is_admin("alice"))

    def test_hash_on_stdin_and_never_on_argv(self):
        self.fc.set_password("alice", "hunter2-secret")
        self.assertEqual(slurp(os.path.join(self.t, "stdin")), "hunter2-secret")
        self.assertNotIn("hunter2-secret", self.calls_text())
        self.fc.create_account("alice", "hunter2-secret", False)
        leaks = [l for l in self.calls_text().splitlines() if "hunter2-secret" in l]
        self.assertEqual(len(leaks), 1)
        self.assertIn("create_user.py", leaks[0])

    def test_garbage_hash_rejected(self):
        self.put("hash", "not-a-hash\n")
        with self.assertRaises(fc.FourCatError) as cm:
            self.fc.set_password("alice", "pw")
        self.assertIn("unexpected hash format", str(cm.exception))
        self.assertNotIn("UPDATE", self.calls_text())

    def test_create_account_failure_is_fourcaterror_without_secret(self):
        self.put("fail", "")
        with self.assertRaises(fc.FourCatError) as cm:
            self.fc.create_account("alice", "hunter2-secret", False)
        self.assertNotIn("hunter2-secret", str(cm.exception))

    def test_login_edge_cases_and_messages(self):
        with self.assertRaises(fc.LoginFailed) as cm:
            self.fc.login_token("nocookie", "hunter2-secret")
        self.assertNotIn("hunter2-secret", str(cm.exception))
        with self.assertRaises(fc.FourCatError) as cm:
            self.fc.login_token("broken", "hunter2-secret")
        self.assertNotIsInstance(cm.exception, fc.LoginFailed)
        self.assertNotIn("hunter2-secret", str(cm.exception))
        with self.assertRaises(fc.LoginFailed) as cm:
            self.fc.login_token("alice", "hunter2-secret")
        self.assertNotIn("hunter2-secret", str(cm.exception))

    def test_bad_usernames_rejected(self):
        for u in ("Alice", "alice\n"):
            with self.assertRaises(ValueError):
                self.fc.password_path(u)

    def test_create_account_calls_create_user_with_flags(self):
        self.fc.create_account("alice", "s3cret", False)
        self.fc.create_account("bob", "s3cret", True)
        a, b = self.calls_text().strip().splitlines()
        self.assertIn("exec 4cat_backend python3 helper-scripts/create_user.py -u alice -e -p s3cret", a)
        self.assertNotIn(" -a", a)
        self.assertTrue(b.endswith(" -a"))

    def test_account_exists_queries_psql(self):
        with open(self.psql, "w") as f:
            f.write("1\n")
        self.assertTrue(self.fc.account_exists("alice"))
        self.assertIn("SELECT 1 FROM users WHERE name = 'alice'", self.calls_text())
        with open(self.psql, "w") as f:
            f.write("")
        self.assertFalse(self.fc.account_exists("alice"))
        n = len(self.calls_text())
        with self.assertRaises(ValueError):
            self.fc.account_exists("Bad;u")
        self.assertEqual(len(self.calls_text()), n)

    def test_login_token_parses_set_cookie(self):
        self.assertEqual(self.fc.login_token("alice", "pw-alice"), "tok-alice")
        with self.assertRaises(fc.LoginFailed):
            self.fc.login_token("alice", "wrong")
        self.cfg.fourcat_url = "http://127.0.0.1:1"
        with self.assertRaises(fc.FourCatError) as cm:
            self.fc.login_token("alice", "pw-alice")
        self.assertNotIsInstance(cm.exception, fc.LoginFailed)

    def test_write_token_mode(self):
        p = self.fc.write_token("alice", "tok-alice")
        self.assertEqual(p, self.fc.token_path("alice"))
        self.assertEqual(stat.S_IMODE(os.stat(p).st_mode), 0o600)
        self.assertEqual(slurp(p), "tok-alice")

    def test_set_admin_sql(self):
        self.fc.set_admin("alice", True)
        self.fc.set_admin("alice", False)
        a, b = self.calls_text().strip().splitlines()
        self.assertIn("tags - 'admin'", a)
        self.assertIn('"admin"', a)
        self.assertIn("tags - 'admin'", b)
        self.assertNotIn('"admin"', b)
        with open(self.psql, "w") as f:
            f.write('["admin", "x"]\n')
        self.assertTrue(self.fc.is_admin("alice"))

    def test_rotate_changes_password_and_token(self):
        old, _ = self.fc.ensure_password("alice")
        self.fc.write_token("alice", "old-token")
        real_set = self.fc.set_password
        PASSWORDS["alice"] = old
        self.addCleanup(PASSWORDS.__setitem__, "alice", "pw-alice")

        def set_password(user, password):
            real_set(user, password)
            PASSWORDS[user] = password
        self.fc.set_password = set_password
        self.fc.rotate("alice")
        self.assertNotEqual(slurp(self.fc.password_path("alice")), old)
        self.assertEqual(slurp(self.fc.token_path("alice")), "tok-alice")
        self.assertIn("UPDATE users SET password = '$2b$", self.calls_text())


if __name__ == "__main__":
    unittest.main()
