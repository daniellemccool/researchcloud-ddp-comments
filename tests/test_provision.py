"""Tests for desk/desklib/provision.py: fake `docker` on PATH, fake 4CAT, a tiny template tar."""
import json, os, shutil, sys, tarfile, tempfile, threading, time, unittest

REPO = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
sys.path.insert(0, os.path.join(REPO, "desk"))
from desklib import locks, provision as pv, render  # noqa: E402
from desklib.config import DeskConfig, load_config, save_config  # noqa: E402
from desklib.fourcat import FourCat  # noqa: E402
from desklib.registry import Registry  # noqa: E402

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import fakes  # noqa: E402

DONE = {s: "done" for s in pv.STEPS}


def slurp(p):
    with open(p) as f:
        return f.read()


def make_template(path):
    src = tempfile.mkdtemp()
    try:
        os.makedirs(os.path.join(src, "profile"))
        with open(os.path.join(src, "profile", "prefs.js"), "w") as f:
            f.write('user_pref("desk.test", true);\n')

        def owned(ti):
            ti.uid = ti.gid = 1000
            ti.uname = ti.gname = ""
            return ti
        with tarfile.open(path, "w:gz") as tar:
            tar.add(os.path.join(src, "profile"), arcname="profile", filter=owned)
    finally:
        shutil.rmtree(src)


class ProvisionTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.t = t = self.tmp.name
        self.root = os.path.join(t, "desk")
        self.template = os.path.join(t, "template.tar.gz")
        make_template(self.template)
        self.run_docker = fakes.fake_docker(t)
        self.fake = fakes.FakeFourCat(check=self.password_matches)
        self.addCleanup(self.fake.close)
        save_config(DeskConfig(desk_root=self.root, repo_dir=REPO, platform="tiktok",
                               collector_role="src_co_c", admin_role="src_co_a", contact="x",
                               fourcat_url=self.fake.url, template_path=self.template))
        self.cfg = load_config(self.root)
        self.reg = Registry(self.root)
        self.fc = FourCat(self.cfg, run=self.run_docker)
        self.nginx_calls = []
        self.prov = self.make_prov(self.fc)

    def make_prov(self, fourcat):
        return pv.Provisioner(self.cfg, self.reg, fourcat, is_admin=lambda u: u == "root-admin",
                              run=self.run_docker, nginx=self.nginx_calls.append)

    def password_matches(self, user, password):
        try:
            return slurp(self.fc.password_path(user)) == password
        except FileNotFoundError:
            return False

    def calls(self, needle=""):
        return [c for c in fakes.docker_calls(self.t) if needle in c]

    def state_text(self, user="alice"):
        return slurp(os.path.join(self.root, "state", "users", user, "provision.json"))

    def assert_no_secrets(self, user="alice"):
        text = self.state_text(user)
        for p in (self.fc.password_path(user), self.fc.token_path(user)):
            if os.path.exists(p):
                self.assertNotIn(slurp(p), text)

    def test_happy_path_marks_all_steps_done(self):
        st = self.prov.advance("alice")
        self.assertTrue(st.ready(), st.steps)
        self.assertEqual(st.steps, DONE)
        self.assertIsNone(st.current())
        self.assertIsNone(st.error())
        saved = json.loads(self.state_text())
        self.assertEqual(saved["steps"], DONE)
        self.assertEqual(saved["applied_generation"], 0)
        self.assertGreater(saved["updated"], 0)
        self.assertEqual(self.prov.state("alice"), st)
        self.assertEqual(list(self.reg.live()), ["alice"])
        for kind in ("mitm", "firefox", "captures"):
            self.assertTrue(os.path.isdir(os.path.join(self.root, "state", kind, "alice")), kind)
        prefs = os.path.join(self.root, "state", "firefox", "alice", "profile", "prefs.js")
        self.assertIn("desk.test", slurp(prefs))
        self.assertEqual(slurp(self.fc.token_path("alice")), "tok-alice")
        self.assertEqual(self.fake.logins, ["alice"])
        create = self.calls("create_user.py")
        self.assertEqual(len(create), 1)
        self.assertIn("-u alice", create[0])
        self.assertFalse(create[0].endswith(" -a"))
        compose = slurp(os.path.join(self.root, "docker-compose.yml"))
        self.assertIn("  mitm-alice:", compose)
        self.assertIn("./secrets/4cat-tokens/alice:/run/desk/4cat-token:ro", compose)
        self.assertEqual(self.nginx_calls, [self.cfg])
        up = self.calls("compose")
        cf = os.path.join(self.root, "docker-compose.yml")
        self.assertEqual(up, [f"compose -f {cf} up -d mitm-alice", f"compose -f {cf} up -d firefox-alice"])
        self.assert_no_secrets()
        n = len(self.calls())
        self.assertTrue(self.prov.advance("alice").ready())
        self.assertEqual(len(self.calls()), n)  # a ready user costs nothing

    def test_steps_persist_and_retry_from_failure(self):
        self.cfg.fourcat_url = "http://127.0.0.1:1"  # 4CAT down
        st = self.prov.advance("alice")
        self.assertEqual(st.current(), "fourcat_token")
        self.assertTrue(st.error().startswith("error: "), st.error())
        for s in pv.STEPS[:pv.STEPS.index("fourcat_token")]:
            self.assertEqual(st.steps[s], "done", s)
        self.assertNotIn("render", st.steps)
        self.assertEqual(self.prov.state("alice"), st)  # persisted
        self.assertFalse(os.path.exists(os.path.join(self.root, "docker-compose.yml")))
        self.assert_no_secrets()
        self.assertNotIn(slurp(self.fc.password_path("alice")), st.error())
        self.cfg.fourcat_url = self.fake.url  # back up
        st = self.prov.advance("alice")
        self.assertTrue(st.ready(), st.steps)
        self.assertEqual(len(self.calls("create_user.py")), 1)

    def test_existing_account_without_password_needs_adopt(self):
        with open(os.path.join(self.t, "psql"), "w") as f:
            f.write("1\n")  # account_exists -> True
        st = self.prov.advance("alice")
        self.assertEqual(st.current(), "fourcat_account")
        self.assertEqual(st.error(), "error: 4CAT account exists; run desk-admin 4cat-user adopt alice")
        self.assertEqual(self.calls("create_user.py"), [])
        self.assertEqual(self.calls("UPDATE"), [])
        self.assertFalse(os.path.exists(self.fc.password_path("alice")))
        self.assertTrue(issubclass(pv.NeedsAdopt, RuntimeError))

    def test_concurrent_advance_same_user_provisions_once(self):
        results = []
        threads = [threading.Thread(target=lambda: results.append(self.prov.advance("alice")))
                   for _ in range(2)]
        for th in threads:
            th.start()
        for th in threads:
            th.join(30)
        self.assertEqual(len(results), 2)
        self.assertTrue(all(r.ready() for r in results), [r.steps for r in results])
        self.assertEqual(len(self.calls("create_user.py")), 1)
        self.assertEqual([r.user for r in self.reg.rows()], ["alice"])
        self.assertEqual(len(self.calls("up -d firefox-alice")), 1)

    def test_render_lock_not_held_during_fourcat(self):
        self.cfg.template_path = None
        self.assertEqual(self.prov.advance("alice").current(), "template")  # registry and dirs done
        self.cfg.template_path = self.template
        out = []
        with locks.flocked(locks.render_lock(self.root)):
            th = threading.Thread(target=lambda: out.append(self.prov.repair("alice")))
            th.start()
            deadline = time.monotonic() + 2
            while (not self.fake.logins or self.prov.state("alice").current() != "render") \
                    and time.monotonic() < deadline:
                time.sleep(0.02)
            self.assertEqual(self.fake.logins, ["alice"])
            st = self.prov.state("alice")
            self.assertEqual(st.current(), "render")
            self.assertIsNone(st.error())
            self.assertTrue(th.is_alive())  # blocked on the render lock
        th.join(30)
        self.assertTrue(out and out[0].ready(), out and out[0].steps)
        self.assertTrue(self.prov.state("alice").ready())

    def test_template_missing_is_an_error_not_a_crash(self):
        self.cfg.template_path = None
        st = self.prov.advance("alice")
        self.assertEqual(st.current(), "template")
        self.assertEqual(st.error(), "error: template missing")
        self.assertEqual((st.steps["registry"], st.steps["dirs"]), ("done", "done"))
        self.cfg.template_path = os.path.join(self.t, "nope.tar.gz")
        self.assertEqual(self.prov.repair("alice").error(), "error: template missing")
        # a profile already in place needs no template
        self.cfg.template_path = None
        os.makedirs(os.path.join(self.root, "state", "firefox", "alice", "profile"))
        self.assertTrue(self.prov.advance("alice").ready())

    def test_retire_stops_and_frees(self):
        self.assertTrue(self.prov.advance("alice").ready())
        self.prov.retire("alice")
        stops = self.calls("stop ")
        self.assertEqual(stops, ["stop desk-firefox-alice", "stop desk-mitm-alice"])
        self.assertEqual(self.reg.live(), {})
        self.assertEqual([(r.user, r.status) for r in self.reg.rows()], [("alice", "retired")])
        self.assertFalse(os.path.exists(os.path.join(self.root, "state", "users", "alice")))
        retired = os.path.join(self.root, "state", "retired", "alice")
        self.assertEqual(os.listdir(retired), ["provision.json"])
        self.assertEqual(json.loads(slurp(os.path.join(retired, "provision.json")))["steps"], DONE)
        for kind in ("firefox", "mitm", "captures"):
            self.assertTrue(os.path.isdir(os.path.join(self.root, "state", kind, "alice")), kind)
        self.assertNotIn("mitm-alice", slurp(os.path.join(self.root, "docker-compose.yml")))
        self.assertEqual(len(self.nginx_calls), 2)
        self.assertEqual(self.prov.state("alice").steps, {})

    def test_no_fourcat_skips_account_steps(self):
        st = self.make_prov(None).advance("alice")
        self.assertTrue(st.ready(), st.steps)
        self.assertEqual((st.steps["fourcat_account"], st.steps["fourcat_token"]), ("done", "done"))
        self.assertEqual(self.calls("exec"), [])
        self.assertEqual(self.fake.logins, [])
        self.assertFalse(os.path.exists(os.path.join(self.root, "secrets")))
        self.assertNotIn("4cat-token", slurp(os.path.join(self.root, "docker-compose.yml")).replace(
            "DESK_FOURCAT_TOKEN_FILE=/run/desk/4cat-token", ""))
        self.cfg.fourcat_enabled = False  # a FourCat client with 4CAT switched off is skipped too
        self.assertTrue(self.make_prov(self.fc).advance("bob").ready())
        self.assertEqual(self.calls("exec"), [])

    def test_mitm_without_ca_is_an_error(self):
        open(os.path.join(self.t, "no-ca"), "w").close()
        self.prov.ca_timeout, self.prov.ca_poll = 0.2, 0.02
        st = self.prov.advance("alice")
        self.assertEqual(st.current(), "up")
        self.assertEqual(st.error(), "error: mitm produced no CA")
        self.assertEqual(self.calls("up -d firefox-alice"), [])

    def test_real_renderer_and_bad_user(self):
        self.assertIs(pv.Provisioner(self.cfg, self.reg, None, lambda u: False).renderer, render.write_all)
        with self.assertRaises(ValueError):
            self.prov.advance("Bad/user")
        with self.assertRaises(ValueError):
            self.prov.state("../x")


if __name__ == "__main__":
    unittest.main()
