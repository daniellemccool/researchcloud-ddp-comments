"""Tests for desk/desklib/fourcat.py: fake `docker` on PATH plus an in-process fake 4CAT."""
import os, stat, sys, tempfile, time, unittest
from unittest import mock

REPO = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
sys.path.insert(0, os.path.join(REPO, "desk"))
from desklib import fourcat as fc  # noqa: E402
from desklib.config import DeskConfig  # noqa: E402

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import fakes  # noqa: E402

PASSWORDS = {"alice": "pw-alice"}


def slurp(p):
    with open(p) as f:
        return f.read()


class FourCatTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        t = self.tmp.name
        run = fakes.fake_docker(t)
        self.calls = os.path.join(t, "calls")
        self.psql = os.path.join(t, "psql")
        self.t = t
        self.srv = fakes.FakeFourCat(PASSWORDS)
        self.addCleanup(self.srv.close)
        self.cfg = DeskConfig(desk_root=os.path.join(t, "desk"), repo_dir=REPO, platform="tiktok",
                              collector_role="src_co_c", admin_role="src_co_a", contact="x",
                              fourcat_url=self.srv.url)
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
        self.assertEqual(p, os.path.join(self.cfg.desk_root, "secrets", "4cat-tokens", "alice", "4cat-token"))
        self.assertEqual(os.path.basename(p), "4cat-token")   # the addon reads /run/desk/4cat-token
        self.assertEqual(stat.S_IMODE(os.stat(p).st_mode), 0o600)
        self.assertEqual(stat.S_IMODE(os.stat(os.path.dirname(p)).st_mode), 0o750)
        self.assertEqual(stat.S_IMODE(os.stat(os.path.dirname(os.path.dirname(p))).st_mode), 0o700)
        self.assertEqual(slurp(p), "tok-alice")

    def test_write_token_replaces_by_rename_in_its_directory(self):
        p = self.fc.write_token("alice", "tok-1")
        d_ino, f_ino = os.stat(os.path.dirname(p)).st_ino, os.stat(p).st_ino
        self.fc.write_token("alice", "tok-2")
        self.assertEqual(slurp(p), "tok-2")
        self.assertEqual(os.stat(os.path.dirname(p)).st_ino, d_ino)   # the mounted directory stays
        self.assertNotEqual(os.stat(p).st_ino, f_ino)                   # a new inode, renamed in
        self.assertEqual(os.listdir(os.path.dirname(p)), ["4cat-token"])

    def test_write_token_migrates_a_legacy_token_file(self):
        tokens = os.path.join(self.cfg.desk_root, "secrets", "4cat-tokens")
        os.makedirs(tokens)
        with open(os.path.join(tokens, "alice"), "w") as f:   # the old single-file layout
            f.write("old")
        self.fc.write_token("alice", "tok-new")
        self.assertEqual(slurp(self.fc.token_path("alice")), "tok-new")
        self.assertEqual(os.listdir(tokens), ["alice"])
        self.assertFalse(fc.migrate_legacy_token(self.cfg.desk_root, "alice"))   # nothing left to move

    def test_token_constant_matches_the_addon(self):
        with open(os.path.join(REPO, "desk", "mitm", "addons", "capture.py")) as f:
            self.assertIn('"DESK_FOURCAT_TOKEN_FILE", "/run/desk/4cat-token"', f.read())
        self.assertEqual(fc.TOKEN_NAME, "4cat-token")

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

    def test_sql_error_is_an_error_not_no_account(self):
        self.put("sql-error", "")   # psql prints an ERROR; exits 3 only under ON_ERROR_STOP=1, like real psql
        for call in (lambda: self.fc.is_admin("alice"), lambda: self.fc.set_admin("alice", True),
                     lambda: self.fc.account_exists("alice")):
            with self.assertRaises(fc.FourCatError) as cm:
                call()
            self.assertNotIn("no 4CAT account", str(cm.exception))
            self.assertEqual(str(cm.exception), "psql exited with status 3")   # no SQL or output echoed
        self.assertIn("psql -v ON_ERROR_STOP=1 -U", self.calls_text())

    def test_tags_must_be_a_list(self):
        for text in ("null", '{"admin": 1}', '"admin"', "1"):
            self.put("psql", text + "\n")
            with self.assertRaisesRegex(fc.FourCatError, "^unexpected tags value$", msg=text):
                self.fc.is_admin("alice")

    def test_migrated_legacy_token_is_0600(self):
        tokens = os.path.join(self.cfg.desk_root, "secrets", "4cat-tokens")
        os.makedirs(tokens)
        legacy = os.path.join(tokens, "alice")
        with open(legacy, "w") as f:
            f.write("old")
        os.chmod(legacy, 0o644)
        self.assertTrue(fc.migrate_legacy_token(self.cfg.desk_root, "alice"))
        self.assertEqual(stat.S_IMODE(os.stat(self.fc.token_path("alice")).st_mode), 0o600)
        self.assertEqual(slurp(self.fc.token_path("alice")), "old")

    def test_migration_tolerates_a_concurrent_migration(self):
        tokens = os.path.join(self.cfg.desk_root, "secrets", "4cat-tokens")
        os.makedirs(tokens)
        with open(os.path.join(tokens, "alice"), "w") as f:
            f.write("old")
        aside, real = os.path.join(tokens, ".alice.legacy-token"), os.replace

        def racing_replace(src, dst):
            if src == aside:          # the other caller publishes the aside file first
                real(src, dst)
            real(src, dst)            # ours then finds it gone: FileNotFoundError
        with mock.patch.object(fc.os, "replace", racing_replace):
            self.assertFalse(fc.migrate_legacy_token(self.cfg.desk_root, "alice"))   # no exception
        self.assertEqual(slurp(self.fc.token_path("alice")), "old")
        self.assertEqual(sorted(os.listdir(tokens)), ["alice"])

    def test_stale_temp_files_are_swept(self):
        pw_dir = os.path.join(self.cfg.desk_root, "secrets", "4cat-passwords")
        tok_dir = fc.token_dir(self.cfg.desk_root, "alice")
        os.makedirs(pw_dir)
        old_t = os.path.join(pw_dir, "alice.deadbeef.tmp")
        new_t = os.path.join(pw_dir, "bob.cafebabe.tmp")
        old_k = os.path.join(tok_dir, "4cat-token.deadbeef.tmp")
        for p in (old_t, new_t, old_k):
            with open(p, "w") as f:
                f.write("half")
        hour_ago = time.time() - 3600
        os.utime(old_t, (hour_ago, hour_ago)); os.utime(old_k, (hour_ago, hour_ago))
        self.fc.ensure_password("alice")
        self.assertFalse(os.path.exists(old_t)); self.assertFalse(os.path.exists(old_k))
        self.assertTrue(os.path.exists(new_t))     # young: may be a write in flight
        os.utime(new_t, (hour_ago, hour_ago))
        with open(old_k, "w") as f:
            f.write("half")
        os.utime(old_k, (hour_ago, hour_ago))
        self.fc.write_token("bob", "tok-bob")      # sweeps the passwords dir and bob's token dir
        self.assertFalse(os.path.exists(new_t))
        self.assertTrue(os.path.exists(old_k))     # alice's token dir is not bob's to sweep

    def test_ensure_password_reads_the_race_winner(self):
        real = os.link

        def lose(src, dst):
            with open(dst, "w") as f:    # the other provisioning run publishes first
                f.write("winner-pw")
            real_err = FileExistsError(17, "File exists")
            raise real_err
        with mock.patch.object(fc.os, "link", lose):
            self.assertEqual(self.fc.ensure_password("alice"), ("winner-pw", False))
        self.assertIs(os.link, real)
        d = os.path.dirname(self.fc.password_path("alice"))
        self.assertEqual(os.listdir(d), ["alice"])   # our temp file was cleaned up

    def test_rotate_failing_at_login_converges(self):
        old, _ = self.fc.ensure_password("alice")
        self.fc.write_token("alice", "old-token")
        PASSWORDS["alice"] = old
        self.addCleanup(PASSWORDS.__setitem__, "alice", "pw-alice")
        real_set = self.fc.set_password

        def set_password(user, password):     # the DB: what the fake 4CAT checks logins against
            real_set(user, password)
            PASSWORDS[user] = password
        self.fc.set_password = set_password
        up = self.cfg.fourcat_url
        self.cfg.fourcat_url = "http://127.0.0.1:1"      # 4CAT down for this one rotate
        with self.assertRaises(fc.FourCatError):
            self.fc.rotate("alice")
        new = slurp(self.fc.password_path("alice"))
        self.assertNotEqual(new, old)
        self.assertEqual(PASSWORDS["alice"], new)                       # DB and file agree, both new
        self.assertEqual(slurp(os.path.join(self.t, "stdin")), new)     # the hash was of the new one
        self.assertEqual(slurp(self.fc.token_path("alice")), "old-token")   # the proxy keeps working
        self.cfg.fourcat_url = up
        self.fc.rotate("alice")                                          # the retry converges
        self.assertEqual(slurp(self.fc.password_path("alice")), PASSWORDS["alice"])
        self.assertEqual(slurp(self.fc.token_path("alice")), "tok-alice")

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
