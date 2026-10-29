"""Tests for desk/desklib/{coapi,pool,sessions}.py and the desk-sessions CLI."""
import io, json, os, subprocess, sys, tempfile, unittest

REPO = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
sys.path.insert(0, os.path.join(REPO, "desk"))
from desklib import coapi, pool, provision as pv  # noqa: E402
from desklib.config import DeskConfig, load_config, save_config  # noqa: E402
from desklib.locks import flocked, render_lock  # noqa: E402
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

# docker stand-in: logs every call; `inspect` answers by a state dir; `stop` clears it.
DOCKER = """#!/usr/bin/env bash
echo "$*" >> "$FAKE_DOCKER_CALLS"
case "$1" in
  inspect) [ -f "$FAKE_DOCKER_STATE/${@: -1}" ] && echo true || echo false;;
  stop) rm -f "$FAKE_DOCKER_STATE/${@: -1}";;
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

    def is_admin(self, u):
        return self.admins[u]

    def set_admin(self, u, a):
        self.admins[u] = a
        self.calls.append(("set_admin", u, a))

    def rotate(self, u):
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
                FAKE_DOCKER_CALLS=os.path.join(t, "calls"), FAKE_DOCKER_STATE=os.path.join(t, "state"))


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
        self.assertIn("timed out", self.s.retire("carol"))
        self.assertEqual(self.s.prune(), [])
        self.assertEqual(sorted(self.s.failed), ["carol"])

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
