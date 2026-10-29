"""Tests for desk/desklib/{coapi,pool,sessions}.py and the desk-sessions CLI."""
import io, json, os, subprocess, sys, tempfile, unittest

REPO = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
sys.path.insert(0, os.path.join(REPO, "desk"))
from desklib import coapi, pool, provision as pv  # noqa: E402
from desklib.fourcat import FourCatError  # noqa: E402
from desklib.config import DeskConfig, load_config, save_config  # noqa: E402
from desklib.locks import flocked, pool_lock, render_lock, user_lock  # noqa: E402
from desklib.registry import Registry  # noqa: E402
from desklib.sessions import Sessions  # noqa: E402

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))

API = [
    {"username": "lsabajo", "roles": ["src_co_developer", "rsc_developers", "src_co_students"], "x": 1},
    {"username": "jterbraak", "roles": ["src_co_students"]},
    {"username": "dmccool", "roles": ["src_co_wallet", "src_co_developer", "rsc_developers",
                                      "src_ws_admin", "src_co_admin", "src_co_students"]},
    {"username": "outsider", "roles": ["src_co_wallet"]},
    {"username": "Bad Name", "roles": ["src_co_students"]},
]

# docker stand-in: logs every call; `inspect` answers by a state dir; `stop` clears it, unless the
# file $FAKE_DOCKER_STOP_STUCK exists (a container that ignores the stop).
DOCKER = """#!/usr/bin/env bash
echo "$*" >> "$FAKE_DOCKER_CALLS"
case "$1" in
  inspect) [ -f "$FAKE_DOCKER_STATE/${@: -1}" ] && echo true || echo false;;
  stop) [ -f "$FAKE_DOCKER_STOP_STUCK" ] || rm -f "$FAKE_DOCKER_STATE/${@: -1}";;
esac
exit 0
"""


class FakeResp(io.BytesIO):
    def __enter__(self):
        return self

    def __exit__(self, *a):
        self.close()


class FakeFC:
    def __init__(self, admins):
        self.admins, self.calls = dict(admins), []
        self.rotate_fails = False

    def is_admin(self, u):
        return self.admins[u]

    def set_admin(self, u, a):
        self.admins[u] = a
        self.calls.append(("set_admin", u, a))

    def rotate(self, u):
        if self.rotate_fails:
            raise FourCatError("rotate failed")
        self.calls.append(("rotate", u))


def make_desk(t, **kw):
    root = os.path.join(t, "desk")
    cfg = DeskConfig(desk_root=root, repo_dir=REPO, platform="tiktok", collector_role="src_co_students",
                     admin_role="src_co_admin", contact="x", **kw)
    save_config(cfg)
    return root, load_config(root)


def put(path, text):
    os.makedirs(os.path.dirname(path), exist_ok=True)
    with open(path, "w") as f:
        f.write(text)


def slurp(path):
    with open(path) as f:
        return f.read()


def docker_env(t):
    bindir = os.path.join(t, "bin")
    os.makedirs(os.path.join(t, "state"), exist_ok=True)
    put(os.path.join(bindir, "docker"), DOCKER)
    os.chmod(os.path.join(bindir, "docker"), 0o755)
    return dict(os.environ, PATH=bindir + os.pathsep + os.environ["PATH"],
                FAKE_DOCKER_CALLS=os.path.join(t, "calls"), FAKE_DOCKER_STATE=os.path.join(t, "state"),
                FAKE_DOCKER_STOP_STUCK=os.path.join(t, "stop-stuck"))


class CoApiTests(unittest.TestCase):
    def test_co_members_parses_and_filters(self):
        seen = {}

        def urlopen(req, timeout=None):
            seen.update(url=req.full_url, auth=req.get_header("Authorization"),
                        accept=req.get_header("Accept"), timeout=timeout)
            return FakeResp(json.dumps(API).encode())
        info = {"co_user_api_endpoint": "https://co.example/api/users", "co_token": "sekret"}
        members = coapi.co_members(info, urlopen=urlopen)
        self.assertEqual(len(members), 5)
        self.assertEqual(seen, {"url": "https://co.example/api/users", "auth": "sekret",
                                "accept": "application/json", "timeout": 10})
        cfg = DeskConfig(desk_root="/x", repo_dir=REPO, platform="tiktok",
                         collector_role="src_co_students", admin_role="src_co_admin", contact="x")
        self.assertEqual(coapi.membership(members, cfg),
                         {"lsabajo": False, "jterbraak": False, "dmccool": True})

    def test_guarded_members(self):
        self.assertEqual(coapi.guarded_members(lambda: {}, 0), {})        # nobody live: empty is believable
        self.assertEqual(coapi.guarded_members(lambda: {"a": True}, 3), {"a": True})
        with self.assertRaisesRegex(RuntimeError, "^CO API returned no members; refusing "):
            coapi.guarded_members(lambda: {}, 1)
        self.assertTrue(issubclass(coapi.NoMembers, RuntimeError))

    def test_workspace_info_reads_json(self):
        with tempfile.TemporaryDirectory() as t:
            put(os.path.join(t, "w.json"), '{"co_token": "a"}')
            self.assertEqual(coapi.workspace_info(os.path.join(t, "w.json")), {"co_token": "a"})


class SessionsTests(unittest.TestCase):
    def setUp(self):
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        self.t = tmp.name
        self.env = docker_env(self.t)
        self.root, self.cfg = make_desk(self.t, gate_mode="api")
        self.reg = Registry(self.root)
        with flocked(render_lock(self.root)):
            for u in ("alice", "bob", "carol"):
                self.reg.allocate(u)
        for u in ("alice", "bob", "carol"):
            put(os.path.join(self.root, "state", "users", u, "provision.json"),
                json.dumps({"steps": {"fourcat_account": "done"}}))
        for u in ("alice", "bob", "carol"):   # what render writes; start_pair refuses to start without it
            put(os.path.join(self.root, "firefox", "policy", u, "policies.json"), "{}")
        put(os.path.join(self.root, "docker-compose.yml"), "services: {}\n")
        self.fc = FakeFC({"alice": True, "bob": False, "carol": False})
        self.members = {"alice": False, "bob": True}  # carol left; alice demoted; bob promoted
        self.prov = pv.Provisioner(self.cfg, self.reg, self.fc, is_admin=lambda u: False,
                                   members=lambda: self.members, run=self.run_cmd,
                                   renderer=lambda *a: self.rendered.append(a[2]), nginx=lambda c: None)
        self.rendered = []
        self.s = Sessions(self.cfg, self.reg, self.prov, self.fc, lambda: self.members, run=self.run_cmd)

    def run_cmd(self, *a, **kw):
        return subprocess.run(*a, **{**kw, "env": self.env})

    def test_reconcile_retires_non_members_and_syncs_admin(self):
        out = self.s.reconcile()
        self.assertEqual(out, {"retired": ["carol"], "admin_set": ["bob"], "admin_cleared": ["alice"]})
        self.assertEqual(sorted(self.reg.live()), ["alice", "bob"])
        self.assertEqual(self.fc.admins, {"alice": False, "bob": True, "carol": False})
        self.assertIn(("rotate", "alice"), self.fc.calls)
        self.assertNotIn(("rotate", "bob"), self.fc.calls)
        self.assertEqual(self.rendered, [self.members])  # members() consulted under the render lock
        self.assertEqual(self.s.reconcile(), {"retired": [], "admin_set": [], "admin_cleared": []})

    def test_retire_reports_timeout(self):
        def hang(*a, **kw):
            raise subprocess.TimeoutExpired(a[0], 1)
        self.prov.run = hang
        self.assertEqual(self.s.retire("carol"), "could not stop desk-firefox-carol; retire aborted")
        self.assertEqual(self.s.prune(), [])
        self.assertEqual(self.s.failed, {"carol": "could not stop desk-firefox-carol; retire aborted"})
        self.assertIn("carol", self.reg.live())

    def provision_json(self, user, where="users"):
        return os.path.join(self.root, "state", where, user, "provision.json")

    def test_retire_with_a_failing_members_fetch_keeps_the_row_and_state(self):
        self.members = {}   # NoMembers while sessions exist
        err = self.s.retire("carol")
        self.assertIn("CO API returned no members", err)
        self.assertIn("carol", self.reg.live())
        self.assertTrue(os.path.exists(self.provision_json("carol")))
        self.assertFalse(os.path.exists(self.provision_json("carol", "retired")))
        self.assertEqual(self.rendered, [])

    def test_retire_aborts_when_a_container_keeps_running(self):
        for c in ("desk-firefox-carol", "desk-mitm-carol"):
            put(os.path.join(self.t, "state", c), "")
        put(os.path.join(self.t, "stop-stuck"), "")
        with self.assertRaisesRegex(RuntimeError, r"^could not stop desk-firefox-carol; retire aborted$"):
            self.prov.retire("carol")
        self.assertIn("carol", self.reg.live())                     # the row stays live
        self.assertTrue(os.path.exists(self.provision_json("carol")))  # and the state is not moved
        self.assertFalse(os.path.exists(self.provision_json("carol", "retired")))
        self.assertEqual(self.rendered, [])                          # nothing re-rendered
        self.assertEqual(self.s.retire("carol"), "could not stop desk-firefox-carol; retire aborted")
        os.remove(os.path.join(self.t, "state", "desk-firefox-carol"))   # only the proxy is stuck now
        self.assertEqual(self.s.retire("carol"), "could not stop desk-mitm-carol; retire aborted")
        self.assertIn("carol", self.reg.live())
        os.remove(os.path.join(self.t, "stop-stuck"))                # a stop that works
        self.assertIsNone(self.s.retire("carol"))
        self.assertNotIn("carol", self.reg.live())
        self.assertFalse(os.path.exists(self.provision_json("carol")))
        self.assertTrue(os.path.exists(self.provision_json("carol", "retired")))
        calls = slurp(os.path.join(self.t, "calls")).splitlines()
        self.assertIn("inspect -f {{.State.Running}} desk-mitm-carol", calls)

    def test_prune_reports_a_stuck_stop(self):
        put(os.path.join(self.t, "state", "desk-mitm-carol"), "")
        put(os.path.join(self.t, "stop-stuck"), "")
        self.assertEqual(self.s.prune(), [])
        self.assertEqual(self.s.failed, {"carol": "could not stop desk-mitm-carol; retire aborted"})

    def test_up_holds_the_user_lock(self):
        held = []

        def run(cmd, **kw):
            if "compose" in cmd:
                user = cmd[-1].split("-", 1)[1]
                try:
                    with flocked(user_lock(self.root, user), timeout=0.05):
                        held.append((user, False))
                except TimeoutError:
                    held.append((user, True))
            return self.run_cmd(cmd, **kw)
        self.s.run = run
        self.s.up("bob")
        self.s.up()
        self.assertEqual(held, [("bob", True), ("alice", True), ("bob", True), ("carol", True)])

    def compose_calls(self):
        p = os.path.join(self.t, "calls")
        return [c for c in slurp(p).splitlines() if c.startswith("compose")] if os.path.exists(p) else []

    def test_up_is_compose_up_no_deps_on_the_pair(self):
        self.assertEqual(self.s.up("bob"), {})
        self.assertEqual(self.compose_calls(),
                         [f"compose -f {self.root}/docker-compose.yml up -d --no-deps --pull never mitm-bob firefox-bob"])

    def test_up_skips_a_user_retired_meanwhile(self):
        with flocked(render_lock(self.root)):
            self.reg.retire("carol")
        self.assertEqual(self.s.up("carol"), {"carol": "no live session (retired, or never added)"})
        self.assertEqual(self.compose_calls(), [])
        with flocked(render_lock(self.root)):
            self.reg.allocate("carol")
        real = self.s.run

        def run(cmd, **kw):   # bob is retired while up() works on alice: up() has already listed him
            if "compose" in cmd and cmd[-1] == "firefox-alice":
                with flocked(render_lock(self.root)):
                    self.reg.retire("bob")
            return real(cmd, **kw)
        self.s.run = run
        self.assertEqual(self.s.up(), {"bob": "no live session (retired, or never added)"})
        self.assertEqual([c.rsplit(" ", 1)[1] for c in self.compose_calls()], ["firefox-alice", "firefox-carol"])

    def legacy(self, u):
        return os.path.join(self.root, "firefox", f"policies-{u}.json")

    def test_legacy_policy_survives_render_and_goes_after_up(self):
        legacy = {u: self.legacy(u) for u in ("alice", "bob")}
        for p in legacy.values():
            put(p, "{}")
        self.s.render()
        self.assertTrue(all(os.path.exists(p) for p in legacy.values()))   # write_all leaves them
        self.s.up("alice")
        self.assertFalse(os.path.exists(legacy["alice"]))                  # alice's pair was recreated
        self.assertTrue(os.path.exists(legacy["bob"]))                     # bob's not yet
        self.assertTrue(os.path.isfile(os.path.join(self.root, "firefox", "policy", "alice", "policies.json")))

    def test_legacy_policy_survives_while_the_compose_file_still_mounts_it(self):
        put(self.legacy("alice"), "{}")
        put(os.path.join(self.root, "docker-compose.yml"),
            "services:\n  firefox-alice:\n    volumes:\n      - ./firefox/policies-alice.json:/etc/x:ro\n")
        self.s.up("alice")
        self.assertEqual(len(self.compose_calls()), 1)                      # the start itself went ahead
        self.assertTrue(os.path.exists(self.legacy("alice")))               # but the old bind source stays
        put(os.path.join(self.root, "docker-compose.yml"), "services: {}\n")   # re-rendered, no reference
        self.s.up("alice")
        self.assertFalse(os.path.exists(self.legacy("alice")))

    def test_start_refuses_without_the_policy_file(self):
        put(self.legacy("alice"), "{}")
        policy = os.path.join(self.root, "firefox", "policy", "alice", "policies.json")
        for make in (lambda: None, lambda: os.mkdir(policy)):               # absent, then a directory
            os.remove(policy) if os.path.isfile(policy) else None
            make()
            with self.assertRaises(OSError):
                self.s.start_pair("alice")
            self.assertEqual(self.compose_calls(), [])                      # never reached compose
            self.assertTrue(os.path.exists(self.legacy("alice")))
            if os.path.isdir(policy):
                os.rmdir(policy)

    def test_failed_up_keeps_the_legacy_policy(self):
        put(self.legacy("alice"), "{}")

        def fail(cmd, **kw):
            raise subprocess.CalledProcessError(1, cmd)
        self.s.run = fail
        with self.assertRaises(subprocess.CalledProcessError):
            self.s.start_pair("alice")
        self.assertTrue(os.path.exists(self.legacy("alice")))

    def test_a_failing_legacy_drop_does_not_fail_the_start(self):
        from unittest import mock
        err = io.StringIO()
        with mock.patch("desklib.sessions.drop_legacy_policy", side_effect=PermissionError("nope")), \
                mock.patch("sys.stderr", err):
            self.assertEqual(self.s.up("alice"), {})
        self.assertEqual(len(self.compose_calls()), 1)
        self.assertIn("alice", err.getvalue())
        self.assertIn("nope", err.getvalue())

    def test_up_continues_past_a_failing_user_and_reports_it(self):
        from unittest import mock
        import desk_sessions
        real = self.s.run

        def run(cmd, **kw):
            if "compose" in cmd and cmd[-1] == "firefox-alice":
                raise subprocess.CalledProcessError(3, cmd)
            return real(cmd, **kw)
        self.s.run = run
        os.remove(os.path.join(self.root, "firefox", "policy", "bob", "policies.json"))   # a second kind of failure
        self.assertEqual(self.s.up(), {})                                   # nobody skipped
        self.assertEqual(sorted(self.s.failed), ["alice", "bob"])
        self.assertIn("status 3", self.s.failed["alice"])
        self.assertEqual([c.rsplit(" ", 1)[1] for c in self.compose_calls()], ["firefox-carol"])
        err = io.StringIO()
        with mock.patch("sys.stderr", err):
            rc = desk_sessions.cmd_up(self.s, mock.Mock(user=None))
        self.assertEqual(rc, 1)
        self.assertIn("alice", err.getvalue())
        self.assertIn("bob", err.getvalue())
        self.s.run = real
        put(os.path.join(self.root, "firefox", "policy", "bob", "policies.json"), "{}")
        self.assertEqual(desk_sessions.cmd_up(self.s, mock.Mock(user=None)), 0)   # all well: exit 0

    def test_stop_reports_success(self):
        self.assertTrue(self.s.stop("alice"))
        self.s.run = lambda cmd, **kw: subprocess.CompletedProcess(cmd, 1)
        self.assertFalse(self.s.stop("alice"))

        def hang(cmd, **kw):
            raise subprocess.TimeoutExpired(cmd, 120)
        self.s.run = hang
        self.assertFalse(self.s.stop("alice"))

    def test_demotion_rotates_under_the_user_lock(self):
        held = []

        def rotate(u):
            try:
                with flocked(user_lock(self.root, u), timeout=0.05):
                    held.append(False)
            except TimeoutError:
                held.append(True)
        self.fc.rotate = rotate
        self.assertEqual(self.s.sync_admins(self.members)["admin_cleared"], ["alice"])
        self.assertEqual(held, [True])

    def test_reconcile_renders_even_when_nobody_retired(self):
        self.members = {"alice": False, "bob": True, "carol": False}   # nobody leaves; bob promoted
        out = self.s.reconcile()
        self.assertEqual(out["retired"], [])
        self.assertEqual(self.rendered, [])   # no retire, so the provisioner never rendered
        m = slurp(os.path.join(self.root, "nginx", "desk-map.conf"))
        admin = m.split("map $username $desk_admin", 1)[1].split("}", 1)[0]
        self.assertIn('"bob" 1;', admin)
        self.assertNotIn("alice", admin)
        self.members = {"alice": True, "bob": True, "carol": False, "dana": False}   # roles change again
        self.s.reconcile()
        m = slurp(os.path.join(self.root, "nginx", "desk-map.conf"))
        admin = m.split("map $username $desk_admin", 1)[1].split("}", 1)[0]
        self.assertIn('"alice" 1;', admin)
        self.assertIn('"dana" 1;', m.split("map $username $desk_member", 1)[1].split("}", 1)[0])

    def test_render_refuses_an_empty_member_list(self):
        self.members = {}
        with self.assertRaisesRegex(RuntimeError, "CO API returned no members"):
            self.s.render()
        self.assertFalse(os.path.exists(os.path.join(self.root, "nginx", "desk-map.conf")))

    def test_stop_status_and_no_fourcat(self):
        put(os.path.join(self.t, "state", "desk-firefox-alice"), "")
        self.assertEqual(self.s.running(), {"alice": True, "bob": False, "carol": False})
        self.s.stop("alice")
        self.assertIn("stop desk-firefox-alice", slurp(os.path.join(self.t, "calls")))
        rows = self.s.status()
        self.assertEqual([r["user"] for r in rows], ["alice", "bob", "carol"])
        self.assertEqual(rows[0], {"user": "alice", "index": 1, "running": False, "step": "registry",
                                   "error": None, "ready": False})
        s = Sessions(self.cfg, self.reg, self.prov, None, lambda: self.members, run=self.run_cmd)
        self.assertEqual(s.sync_admins(), {"admin_set": [], "admin_cleared": []})

    def test_empty_member_list_refuses_to_retire(self):
        self.members = {}
        for op in (self.s.reconcile, self.s.prune, self.s.sync_admins):
            with self.assertRaisesRegex(RuntimeError, "CO API returned no members"):
                op()
        self.assertEqual(sorted(self.reg.live()), ["alice", "bob", "carol"])
        self.assertFalse(os.path.exists(os.path.join(self.t, "calls")))

    def test_reconcile_decides_on_one_fetch_and_renders_from_a_fresh_one(self):
        n = []

        def members():
            try:
                with flocked(render_lock(self.root), timeout=0.01):
                    n.append("unlocked")
            except TimeoutError:
                n.append("under render lock")
            return self.members
        self.s.members = members
        self.s.reconcile()
        # one fetch for every prune/sync decision, one under the render lock for the maps
        self.assertEqual(n, ["unlocked", "under render lock"])

    def test_demotion_rotates_before_clearing_and_retries(self):
        self.fc.rotate_fails = True
        out = self.s.sync_admins(self.members)
        self.assertEqual(out["admin_cleared"], [])
        self.assertIn("alice", self.s.failed)
        self.assertTrue(self.fc.admins["alice"])  # tag left for the retry
        self.fc.rotate_fails = False
        out = self.s.sync_admins(self.members)
        self.assertEqual(out["admin_cleared"], ["alice"])
        self.assertFalse(self.fc.admins["alice"])
        self.assertNotIn("alice", self.s.failed)

    def test_sync_admins_skips_users_without_a_4cat_account(self):
        put(os.path.join(self.root, "state", "users", "bob", "provision.json"),
            json.dumps({"steps": {"fourcat_account": "error: x"}}))
        out = self.s.sync_admins(self.members)
        self.assertEqual(out, {"admin_set": [], "admin_cleared": ["alice"], "skipped": ["bob"]})
        self.assertNotIn(("set_admin", "bob", True), self.fc.calls)

    def test_render_fetches_members_under_lock_in_api_mode_only(self):
        held, n = [], []

        def members():
            n.append(1)
            try:
                with flocked(render_lock(self.root), timeout=0.01):
                    held.append(False)
            except TimeoutError:
                held.append(True)
            return self.members
        s = Sessions(self.cfg, self.reg, self.prov, self.fc, members, run=self.run_cmd)
        s.render()
        self.assertEqual((n, held), ([1], [True]))
        s.cfg = self.prov.cfg = DeskConfig(**{**self.cfg.__dict__, "gate_mode": "roles"})
        s.render()
        self.assertEqual(n, [1])


class InitAndCliTests(unittest.TestCase):
    def setUp(self):
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        self.t = tmp.name
        self.env = docker_env(self.t)
        self.root = os.path.join(self.t, "desk")
        self.cli = [sys.executable, os.path.join(REPO, "desk", "desk_sessions.py")]

    def cmd(self, *args):
        return subprocess.run(self.cli + list(args), env=self.env, capture_output=True, text=True)

    def init_args(self, *extra):
        return ["init", "--desk-root", self.root, "--repo", REPO, "--platform", "tiktok",
                "--collector-role", "src_co_students", "--admin-role", "src_co_admin",
                "--contact", "Danielle", *extra]

    def test_init_writes_config_and_migrates_pool(self):
        put(os.path.join(self.root, "pool.csv"), "video_id,bucket,note\n1,a,x\n1,a,dup\n2,b,y\n")
        r = self.cmd(*self.init_args("--no-fourcat", "--gate-mode", "api", "--max-sessions", "7",
                                     "--template", "/t/tpl.tar.gz"))
        self.assertEqual(r.returncode, 0, r.stderr)
        cfg = load_config(self.root)
        self.assertEqual((cfg.fourcat_enabled, cfg.gate_mode, cfg.max_sessions, cfg.template_path),
                         (False, "api", 7, "/t/tpl.tar.gz"))
        self.assertTrue(slurp(os.path.join(self.root, "pool.csv")).startswith("id,kind,bucket,note\n1,video,a,x"))
        self.assertEqual([(p.id, p.kind) for p in pool.read_rows(self.root, "video")],
                         [("1", "video"), ("2", "video")])
        # idempotent: re-running with fewer flags keeps the rest
        r = self.cmd(*self.init_args("--max-running", "3"))
        self.assertEqual(r.returncode, 0, r.stderr)
        cfg = load_config(self.root)
        self.assertEqual((cfg.max_running, cfg.max_sessions, cfg.gate_mode), (3, 7, "api"))
        self.assertEqual(self.cmd(*self.init_args("--platform", "nope")).returncode, 1)

    def test_bom_crlf_legacy_pool_reads_and_migrates(self):
        os.makedirs(self.root)
        with open(os.path.join(self.root, "pool.csv"), "wb") as f:
            f.write("\ufeffvideo_id,bucket,note\r\n1,a,x\r\n2,b,y\r\n".encode("utf-8"))
        self.assertEqual(len(pool.read_rows(self.root, "video")), 2)
        self.assertTrue(pool.migrate_legacy(self.root, "video"))
        self.assertEqual(slurp(os.path.join(self.root, "pool.csv")).splitlines()[:2],
                         ["id,kind,bucket,note", "1,video,a,x"])
        self.assertFalse(pool.migrate_legacy(self.root, "video"))

    def test_unknown_pool_header_raises(self):
        put(os.path.join(self.root, "pool.csv"), "foo,bar\n1,2\n")
        with self.assertRaisesRegex(ValueError, "unrecognised header"):
            pool.read_rows(self.root, "video")

    def test_migrate_holds_the_pool_lock(self):
        self.assertEqual(pool_lock(self.root), os.path.join(self.root, ".pool.lock"))
        put(os.path.join(self.root, "pool.csv"), "video_id,bucket,note\n1,a,x\n")
        seen = []
        real = os.replace

        def slow(src, dst):
            try:
                with flocked(pool_lock(self.root), timeout=0.05):
                    seen.append(False)
            except TimeoutError:
                seen.append(True)
            real(src, dst)
        pool.os.replace = slow
        try:
            pool.migrate_legacy(self.root, "video")
        finally:
            pool.os.replace = real
        self.assertEqual(seen, [True])

    def test_cli_status_lists_rows(self):
        self.assertEqual(self.cmd(*self.init_args("--no-fourcat")).returncode, 0)
        reg = Registry(self.root)
        with flocked(render_lock(self.root)):
            reg.allocate("alice")
            reg.allocate("bob")
        put(os.path.join(self.t, "state", "desk-firefox-bob"), "")
        r = self.cmd("status", "--desk-root", self.root)
        self.assertEqual(r.returncode, 0, r.stderr)
        lines = r.stdout.splitlines()
        self.assertEqual(len(lines), 2)
        self.assertTrue(lines[0].startswith("alice") and "stopped" in lines[0], lines[0])
        self.assertTrue(lines[1].startswith("bob") and "running" in lines[1], lines[1])
        self.assertEqual(self.cmd("status").returncode, 2)


if __name__ == "__main__":
    unittest.main()
