"""Tests for desk/desklib/render.py: Compose, nginx maps and locations, Firefox policy."""
import os, re, shutil, subprocess, sys, tempfile, unittest

REPO = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
sys.path.insert(0, os.path.join(REPO, "desk"))
from desklib import locks, render  # noqa: E402
from desklib.config import DeskConfig  # noqa: E402
from desklib.registry import Registry  # noqa: E402


def block(text, start, end=None):
    """Lines from the one starting with `start` up to (not including) the next line starting with `end`."""
    out, on = [], False
    for line in text.splitlines():
        if line.startswith(start):
            on = True
        elif on and end is not None and line.startswith(end):
            break
        if on:
            out.append(line)
    return "\n".join(out)


class RenderBase(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.mkdtemp()
        self.addCleanup(shutil.rmtree, self.tmp, True)
        self.root = os.path.join(self.tmp, "desk")
        os.makedirs(self.root)
        self.reg = Registry(self.root)
        for u in ("alice", "bob"):
            self.reg.allocate(u)
        self.cfg = self.make_cfg()

    def make_cfg(self, **kw):
        base = dict(desk_root=self.root, repo_dir=REPO, platform="tiktok",
                    collector_role="src_co_students", admin_role="src_co_admin",
                    contact="staff@example.org")
        base.update(kw)
        return DeskConfig(**base)

    def compose(self, cfg=None, tokens=lambda u: False):
        return render.render_compose(cfg or self.cfg, self.reg, tokens)


class ComposeTests(RenderBase):
    def test_compose_has_isolated_pair_per_user(self):
        c = self.compose()
        for s in ("  mitm-alice:", "  firefox-alice:", "  mitm-bob:", "  firefox-bob:", "  net-alice: {}"):
            self.assertIn(s, c)
        self.assertIn("          - mitm\n", c)
        self.assertIn('"127.0.0.1:5801:5800"', c)
        self.assertIn('"127.0.0.1:5802:5800"', c)
        ff = block(c, "  firefox-alice:", "  mitm-bob:")
        self.assertIn("networks: [net-alice]", ff)
        self.assertNotIn("desk-fourcat", ff)
        mitm = block(c, "  mitm-alice:", "  firefox-alice:")
        self.assertIn("net-alice:", mitm)
        self.assertIn("desk-fourcat", mitm)
        self.assertRegex(c, r"(?m)^networks:\n(  .*\n)*  desk-fourcat:\n    external: true")

    def test_compose_mounts_platform_and_desklib(self):
        c = self.compose()
        mitm = block(c, "  mitm-alice:", "  firefox-alice:")
        self.assertIn("./platform:/platform:ro", mitm)
        self.assertIn(f"{REPO}/desk/desklib:/opt/desk/desklib:ro", mitm)
        self.assertIn(f"{REPO}/desk/mitm/addons:/addons:ro", mitm)
        self.assertIn("./state/captures/alice:/captures/alice", mitm)
        self.assertIn("connection_strategy=lazy", mitm)
        self.assertIn("-s /addons/capture.py", mitm)
        self.assertIn("--set body_size_limit=32m", mitm)
        for env in ("DESK_USER=alice", "DESK_SESSION=1", "DESK_FOURCAT_ENABLED=1",
                    "DESK_PLATFORM_JSON=/platform/platform.json", "DESK_CAPTURES=/captures",
                    "DESK_FOURCAT_UPSTREAM=http://frontend:5000",
                    "DESK_FOURCAT_TOKEN_FILE=/run/desk/4cat-token"):
            self.assertIn(env, mitm)

    def test_compose_token_mount_only_when_token_exists(self):
        c = self.compose(tokens=lambda u: u == "alice")
        self.assertIn("./secrets/4cat-tokens/alice:/run/desk:ro",
                      block(c, "  mitm-alice:", "  firefox-alice:"))
        self.assertNotIn("4cat-tokens", block(c, "  mitm-bob:", "  firefox-bob:"))

    def written_compose(self):
        render.write_all(self.cfg, self.reg)
        with open(os.path.join(self.root, "docker-compose.yml")) as f:
            return f.read()

    def test_token_mount_follows_the_token_directory(self):
        os.makedirs(os.path.join(self.root, "secrets", "4cat-tokens", "alice"))
        c = self.written_compose()
        self.assertIn("./secrets/4cat-tokens/alice:/run/desk:ro", block(c, "  mitm-alice:", "  firefox-alice:"))
        self.assertNotIn("4cat-tokens", block(c, "  mitm-bob:", "  firefox-bob:"))

    def test_legacy_token_file_moves_into_its_directory(self):
        tokens = os.path.join(self.root, "secrets", "4cat-tokens")
        os.makedirs(tokens)
        with open(os.path.join(tokens, "bob"), "w") as f:   # the single-file layout
            f.write("tok-bob")
        for _ in range(2):   # idempotent
            c = self.written_compose()
            self.assertIn("./secrets/4cat-tokens/bob:/run/desk:ro", block(c, "  mitm-bob:", "  firefox-bob:"))
            with open(os.path.join(tokens, "bob", "4cat-token")) as f:
                self.assertEqual(f.read(), "tok-bob")
            self.assertEqual(sorted(os.listdir(tokens)), ["bob"])
        self.assertNotIn("4cat-tokens", block(c, "  mitm-alice:", "  firefox-alice:"))

    def test_no_single_file_bind_mounts(self):
        c = self.compose(tokens=lambda u: True)
        self.assertIn("./firefox/policy/alice:/usr/lib/firefox/distribution:ro", c)
        self.assertIn("./secrets/4cat-tokens/alice:/run/desk:ro", c)
        mounts = re.findall(r"(?m)^      - ([./]\S*:/\S*)$", c)
        self.assertEqual(len(mounts), 2 * (5 + 1 + 3))   # per user: 5 mitm + the token dir, 3 firefox
        for m in mounts:   # every container path is a directory, never a file
            self.assertNotRegex(m.split(":")[1], r"\.(json|pem|txt|css)$|4cat-token$", m)

    def test_compose_without_fourcat(self):
        c = self.compose(self.make_cfg(fourcat_enabled=False))
        self.assertNotIn("desk-fourcat", c)
        self.assertIn("DESK_FOURCAT_ENABLED=0", c)
        m = render.render_maps(self.make_cfg(fourcat_enabled=False), self.reg)
        self.assertNotIn("8080", block(m, "map $desk_admin $fourcat_ui", "}"))

    def test_compose_with_no_users_is_valid(self):
        empty = Registry(os.path.join(self.tmp, "empty"))
        off = render.render_compose(self.make_cfg(fourcat_enabled=False), empty, lambda u: False)
        self.assertIn("services: {}", off)
        self.assertNotIn("networks:", off)
        on = render.render_compose(self.cfg, empty, lambda u: False)
        self.assertIn("services: {}", on)
        self.assertRegex(on, r"(?m)^networks:\n  desk-fourcat:\n    external: true\n$")

    def test_alice_block_identical_after_adding_bob(self):
        solo = Registry(os.path.join(self.tmp, "solo"))
        solo.allocate("alice")
        before = render.render_compose(self.cfg, solo, lambda u: False)
        after = self.compose()
        a = lambda c: block(c, "  mitm-alice:", "  mitm-bob:") if "mitm-bob" in c else block(c, "  mitm-alice:", "networks:")
        self.assertEqual(a(before), a(after))

    def test_dotted_username_in_compose_and_maps(self):
        self.reg.allocate("j.smith")
        self.assertIn("container_name: desk-firefox-j.smith", self.compose())
        self.assertIn('"1:j.smith" 127.0.0.1:5803;', render.render_maps(self.cfg, self.reg))


class MapTests(RenderBase):
    def test_maps_roles_mode(self):
        m = render.render_maps(self.cfg, self.reg)
        self.assertIn('"~(^|;)src_co_admin(;|$)" 1;', block(m, "map $src_roles $desk_admin", "}"))
        member = block(m, "map $src_roles $desk_member", "}")
        self.assertIn('"~(^|;)src_co_admin(;|$)" 1;', member)
        self.assertIn('"~(^|;)src_co_students(;|$)" 1;', member)
        self.assertNotIn("alice", member)
        self.assertNotIn("bob", member)
        self.assertIn('map "$desk_member:$username" $desk_vnc', m)
        self.assertIn('"1:alice" 127.0.0.1:5801;', block(m, 'map "$desk_member:$username"', "}"))
        self.assertIn('map "$desk_admin:$desk_target" $desk_admin_vnc', m)
        self.assertIn('"1:alice" 127.0.0.1:5801;', block(m, 'map "$desk_admin:$desk_target"', "}"))
        self.assertIn("1 127.0.0.1:8080;", block(m, "map $desk_admin $fourcat_ui", "}"))

    def test_every_map_defaults_to_stub_or_zero_and_no_zero_keys(self):
        for m in (render.render_maps(self.cfg, self.reg),
                  render.render_maps(self.make_cfg(gate_mode="api"), self.reg, {"alice": True})):
            defaults = re.findall(r"(?m)^    default (\S+);", m)
            self.assertEqual(len(defaults), 5)
            self.assertTrue(all(d in ("0", "127.0.0.1:8110") for d in defaults), defaults)
            self.assertNotIn('"0:', m)

    def test_maps_api_mode(self):
        cfg = self.make_cfg(gate_mode="api")
        m = render.render_maps(cfg, self.reg, {"alice": True, "bob": False, "carol": True, "Bad Name": True})
        member = block(m, "map $username $desk_member", "}")
        for u in ("alice", "bob", "carol"):   # every role-holder, registered or not
            self.assertIn(f'"{u}" 1;', member)
        self.assertNotIn("Bad Name", m)
        admin = block(m, "map $username $desk_admin", "}")
        self.assertIn('"alice" 1;', admin)
        self.assertIn('"carol" 1;', admin)
        self.assertNotIn("bob", admin)
        for vnc in ('map "$desk_member:$username"', 'map "$desk_admin:$desk_target"'):
            v = block(m, vnc, "}")   # the VNC maps stay live-only
            self.assertIn('"1:alice" 127.0.0.1:5801;', v)
            self.assertNotIn("carol", v)

    def test_api_mode_new_member_is_a_member_before_provisioning(self):
        cfg = self.make_cfg(gate_mode="api")
        m = render.render_maps(cfg, self.reg, {"alice": False, "dana": False})
        self.assertNotIn("dana", self.reg.live())
        self.assertIn('"dana" 1;', block(m, "map $username $desk_member", "}"))
        self.assertNotIn("dana", block(m, "map $username $desk_admin", "}"))
        self.assertNotIn("dana", block(m, 'map "$desk_member:$username"', "}"))

    def test_maps_retired_user_absent(self):
        with locks.flocked(locks.render_lock(self.root)):
            self.reg.retire("bob")
        self.assertNotIn("bob", render.render_maps(self.cfg, self.reg))
        self.assertNotIn("bob", self.compose())
        cfg = self.make_cfg(gate_mode="api")
        m = render.render_maps(cfg, self.reg, {"alice": True, "bob": True})
        # still a role-holder, so still a member (a visit re-provisions), but no screen to reach
        self.assertIn('"bob" 1;', block(m, "map $username $desk_member", "}"))
        for vnc in ('map "$desk_member:$username"', 'map "$desk_admin:$desk_target"'):
            self.assertNotIn("bob", block(m, vnc, "}"))
        self.assertNotIn("bob", render.render_maps(cfg, self.reg, {"alice": True}))


class LocationTests(RenderBase):
    GATE = ["error_page 401 = @custom_401;", "auth_request /validate;",
            "auth_request_set $username $upstream_http_username;",
            "auth_request_set $src_roles $upstream_http_src_co_roles;",
            "proxy_set_header REMOTE_USER $username;", "proxy_set_header X-Desk-Roles $src_roles;",
            "proxy_set_header X-Desk-Member $desk_member;", "proxy_set_header X-Desk-Admin $desk_admin;"]

    def locations(self, text):
        return re.split(r"(?m)^(?=location )", text)[1:]

    def test_desk_conf_gate_lines(self):
        d = render.render_desk_conf(self.cfg)
        locs = self.locations(d)
        self.assertEqual(len(locs), 3)
        for loc in locs:
            pos = [loc.index(g) for g in self.GATE]
            self.assertEqual(pos, sorted(pos), loc)
        self.assertIn("proxy_pass http://127.0.0.1:8110;", d)
        self.assertIn("proxy_pass http://$desk_vnc;", d)
        self.assertIn("proxy_pass http://$desk_admin_vnc;", d)
        self.assertNotIn("if (", d)
        self.assertNotIn("if(", d)

    def test_fourcat_conf_is_admin_ui_only(self):
        f = render.render_fourcat_conf(self.cfg)
        locs = self.locations(f)
        self.assertEqual(len(locs), 1)
        self.assertTrue(locs[0].startswith("location / {"))
        self.assertIn("proxy_pass http://$fourcat_ui;", f)
        self.assertNotIn("import-dataset", f)
        self.assertNotIn("check-query", f)
        for g in self.GATE:
            self.assertIn(g, f)


class OtherTests(RenderBase):
    def test_role_charset_enforced(self):
        with self.assertRaises(ValueError):
            self.make_cfg(collector_role="src_co_a.b").validate()
        self.make_cfg(collector_role="src_co_students-2026").validate()
        with self.assertRaises(ValueError):
            self.make_cfg(gate_mode="nope").validate()

    def test_policy_has_pinned_zeeschuimer(self):
        p = render.render_policy(self.cfg)
        self.assertIn("v1.14.2/zeeschuimer-1.14.2.xpi", p)
        self.assertNotIn("{{", p)
        self.assertIn('"HTTPProxy": "mitm:8080"', p)
        self.assertIn('"Locked": true', p)
        self.assertRegex(p, r'"toolkit.legacyUserProfileCustomizations.stylesheets": \{\s*"Value": true,\s*"Status": "locked"')

    def test_write_all_layout(self):
        os.makedirs(os.path.join(self.root, "state/firefox/alice/profile"))
        paths = render.write_all(self.cfg, self.reg)
        r = self.root
        css = os.path.join(r, "state/firefox/alice/profile/chrome/userContent.css")
        self.assertFalse(os.path.exists(os.path.join(r, "state/firefox/bob/profile")))
        for rel in ("docker-compose.yml", "nginx/desk-map.conf", "nginx/desk.conf", "nginx/fourcat.conf",
                    "firefox/policy/alice/policies.json", "firefox/policy/bob/policies.json", "platform/platform.json",
                    "state/firefox/alice/profile/chrome/userContent.css",
                    "state/mitm/bob", "state/captures/alice"):
            self.assertTrue(os.path.exists(os.path.join(r, rel)), rel)
        self.assertIn(os.path.join(r, "docker-compose.yml"), paths)
        render.write_all(self.make_cfg(hide_media=False), self.reg)
        self.assertFalse(os.path.exists(css))

    def test_policy_moves_to_a_directory_per_user(self):
        ff = os.path.join(self.root, "firefox")
        os.makedirs(ff)
        for name in ("policies-alice.json", "policies-gone.json"):   # the single-file layout
            with open(os.path.join(ff, name), "w") as f:
                f.write("{}")
        render.write_all(self.cfg, self.reg)
        self.assertEqual(os.listdir(ff), ["policy"])
        self.assertEqual(sorted(os.listdir(os.path.join(ff, "policy"))), ["alice", "bob"])
        pol = os.path.join(ff, "policy", "alice", "policies.json")
        d_ino, f_ino = os.stat(os.path.dirname(pol)).st_ino, os.stat(pol).st_ino
        render.write_all(self.cfg, self.reg)   # idempotent; the file is replaced, its directory kept
        self.assertEqual(os.listdir(ff), ["policy"])
        self.assertEqual(os.listdir(os.path.dirname(pol)), ["policies.json"])
        self.assertEqual(os.stat(os.path.dirname(pol)).st_ino, d_ino)
        self.assertNotEqual(os.stat(pol).st_ino, f_ino)
        with open(pol) as f:
            self.assertEqual(f.read(), render.render_policy(self.cfg))

    def test_write_all_keeps_platform_dir_inode(self):
        render.write_all(self.cfg, self.reg)
        d = os.path.join(self.root, "platform")
        ino = os.stat(d).st_ino
        stale = os.path.join(d, "stale.txt")
        open(stale, "w").close()
        render.write_all(self.cfg, self.reg)
        render.write_all(self.cfg, self.reg)
        self.assertEqual(os.stat(d).st_ino, ino)
        self.assertFalse(os.path.exists(stale))
        self.assertTrue(os.path.isfile(os.path.join(d, "platform.json")))

    def test_written_compose_is_complete(self):
        render.write_all(self.cfg, self.reg)
        with open(os.path.join(self.root, "docker-compose.yml")) as f:
            text = f.read()
        self.assertEqual(text, self.compose())
        self.assertTrue(text.endswith("    external: true\n"))
        self.assertEqual(os.listdir(self.root).count("docker-compose.yml"), 1)
        self.assertFalse([n for n in os.listdir(self.root) if n.startswith(".tmp.")])

    def test_install_nginx_restores_originals_when_nginx_t_fails(self):
        render.write_all(self.cfg, self.reg)
        conf_d, app_d = (os.path.join(self.tmp, n) for n in ("conf.d", "app.d"))
        os.makedirs(conf_d); os.makedirs(app_d)
        old = os.path.join(conf_d, "desk-map.conf")
        with open(old, "w") as f:
            f.write("ORIGINAL")
        calls = []

        def run(cmd, check=False):
            calls.append(cmd)
            if cmd[0] == "nginx":
                raise subprocess.CalledProcessError(1, cmd)

        with self.assertRaises(subprocess.CalledProcessError):
            render.install_nginx(self.cfg, run, conf_d=conf_d, app_d=app_d)
        with open(old) as f:
            self.assertEqual(f.read(), "ORIGINAL")
        self.assertFalse(os.path.exists(os.path.join(app_d, "desk.conf")))
        self.assertEqual(calls, [["nginx", "-t"]])
        calls.clear()
        render.install_nginx(self.cfg, lambda c, check=False: calls.append(c), conf_d=conf_d, app_d=app_d)
        self.assertTrue(os.path.exists(os.path.join(app_d, "fourcat.conf")))
        self.assertEqual(calls[-1], ["systemctl", "reload", "nginx"])

    @unittest.skipUnless(shutil.which("docker"), "docker not installed")
    def test_compose_config_validates(self):
        render.write_all(self.cfg, self.reg)
        if subprocess.run(["docker", "compose", "version"], capture_output=True).returncode:
            self.skipTest("docker compose not available")
        r = subprocess.run(["docker", "compose", "-f", os.path.join(self.root, "docker-compose.yml"),
                            "config", "-q"], capture_output=True, text=True)
        self.assertEqual(r.returncode, 0, r.stderr)


if __name__ == "__main__":
    unittest.main()
