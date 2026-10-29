"""HTTP tests for desk/desk_stub.py: a temp desk root with desk.json, fake docker/ss on PATH and a fake 4CAT."""
import contextlib, csv, http.client, io, json, os, subprocess, sys, tempfile, threading, time, unittest
from unittest import mock

REPO = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
sys.path.insert(0, os.path.join(REPO, "desk"))
import desk_stub  # noqa: E402
from desk_sessions import build_sessions  # noqa: E402
from desklib import locks  # noqa: E402
from desklib.config import DeskConfig, save_config  # noqa: E402

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import fakes  # noqa: E402
from test_provision import make_template  # noqa: E402

# The stub's own docker calls: container state lives in $FAKE_DOCKER_STATE. A Firefox exec, a
# `compose up` and a `stop` obey $FAKE_DOCKER_MODE (fail; hang for the exec only); `compose up`
# marks every mitm-/firefox- service it names running (`create` does not). Everything else
# (the rest of compose, 4CAT execs) goes to the shared fake.
OUTER_DOCKER = """#!/usr/bin/env bash
echo "$*" >> "$FAKE_DOCKER_CALLS"
mode="$(cat "$FAKE_DOCKER_MODE" 2>/dev/null)"
if [ "$1" = compose ] && [ "$4" = up ]; then
  [ "$mode" = fail ] && { echo "boom" >&2; exit 1; }
  for svc in "${@:5}"; do
    case "$svc" in mitm-*|firefox-*) touch "$FAKE_DOCKER_STATE/desk-$svc";; esac
  done
fi
case "$1" in
  ps) printf 'desk-firefox-alice\\tUp 2 hours\\ndesk-mitm-alice\\tUp 2 hours\\n'; exit 0;;
  inspect) [ -f "$FAKE_DOCKER_STATE/${@: -1}" ] && echo true || echo false; exit 0;;
  stop) [ "$mode" = fail ] && { echo "boom" >&2; exit 1; }
        rm -f "$FAKE_DOCKER_STATE/${@: -1}"; exit 0;;
esac
case "$*" in
  *" su-exec "*)
    case "$(cat "$FAKE_DOCKER_MODE" 2>/dev/null)" in
      fail) echo "boom" >&2; exit 1;;
      hang) exec sleep 5;;
    esac
    exit 0;;
esac
FAKE_DOCKER_CALLS=/dev/null exec "$FAKE_DOCKER_INNER" "$@"
"""
FAKE_SS = """#!/usr/bin/env bash
cat "$FAKE_SS_OUT" 2>/dev/null
exit 0
"""
HINT = ("Before you leave: click <b>Z</b> in the browser's toolbar, press <b>to 4CAT</b> on "
        "<i>TikTok (comments)</i>, then on <i>TikTok</i>, and wait until both say they're done.")


def slurp(path):
    with open(path) as f: return f.read()


def put(path, text):
    os.makedirs(os.path.dirname(path), exist_ok=True)
    with open(path, "w") as f: f.write(text)


def write_csv(path, rows, header=("video_id", "bucket", "note")):
    with open(path, "w", newline="") as f:
        w = csv.writer(f); w.writerow(header); w.writerows(rows)


def wait_for(pred, timeout=5.0):
    deadline = time.monotonic() + timeout
    while not pred():
        if time.monotonic() > deadline:
            return False
        time.sleep(0.02)
    return True


class StubHarness(unittest.TestCase):
    """The shared setup (temp desk root, fake docker/ss/4CAT, served stub, request helpers); no tests of its own."""
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory(); self.addCleanup(self.tmp.cleanup)
        t = self.t = self.tmp.name
        self.root = os.path.join(t, "desk")
        self.template = os.path.join(t, "template.tar.gz"); make_template(self.template)
        fakes.fake_docker(t)   # the shared fake, at <t>/bin/docker; ours wraps it
        outer = os.path.join(t, "outer")
        for name, text in (("docker", OUTER_DOCKER), ("ss", FAKE_SS)):
            put(os.path.join(outer, name), text); os.chmod(os.path.join(outer, name), 0o755)
        self.calls = os.path.join(t, fakes.CONTROL["FAKE_DOCKER_CALLS"]); self.mode = os.path.join(t, "mode")
        self.state = os.path.join(t, "running"); os.makedirs(self.state); self.ss_out = os.path.join(t, "ss")
        env = {"PATH": outer + os.pathsep + os.environ["PATH"], "FAKE_DOCKER_INNER": os.path.join(t, "bin", "docker"),
               "FAKE_DOCKER_MODE": self.mode, "FAKE_DOCKER_STATE": self.state, "FAKE_SS_OUT": self.ss_out,
               **{k: os.path.join(t, v) for k, v in fakes.CONTROL.items()}}
        old = {k: os.environ.get(k) for k in env}; os.environ.update(env)
        self.addCleanup(lambda: [os.environ.pop(k, None) if v is None else os.environ.__setitem__(k, v)
                                 for k, v in old.items()])
        self.fourcat = fakes.FakeFourCat(check=self.password_ok); self.addCleanup(self.fourcat.close)
        self.srv = None
        self.serve()
        rows = [[str(100 + i), "training", ""] for i in range(1, 13)]   # 101..112, legacy header
        rows[0][2] = 'nos, "quoted"'
        write_csv(os.path.join(self.root, "pool.csv"), rows)
        for u in ("alice", "bob"):   # registered and ready: index 1 and 2, containers stopped
            self.assertTrue(self.desk_obj().prov.advance(u).ready())
        os.remove(self.calls)

    def serve(self, **overrides):
        if self.srv:
            self.srv.shutdown(); self.srv.server_close()
        cfg = dict(desk_root=self.root, repo_dir=REPO, platform="tiktok", collector_role="src_co_students",
                   admin_role="src_co_admin", contact="Jo", batch_sizes=[5, 10], timer_minutes=3, max_running=2,
                   template_path=self.template, fourcat_url=self.fourcat.url)
        save_config(DeskConfig(**{**cfg, **overrides}))
        s = build_sessions(self.root, is_admin=lambda u: self.desk_obj().admin_flags.get(u, False))
        s.prov.nginx = lambda cfg: None   # never touch the host's nginx
        s.prov.ca_poll = 0.01
        self.srv = desk_stub.make_server(self.root, 0, reaper=False, exec_timeout=1, sessions=s)
        self.port = self.srv.server_address[1]
        threading.Thread(target=self.srv.serve_forever, args=(0.05,), daemon=True).start()

    def tearDown(self):
        wait_for(lambda: not self.desk_obj().provisioning)   # no background step may outlive the temp dir
        self.srv.shutdown(); self.srv.server_close()

    def password_ok(self, user, password):
        try:
            return slurp(os.path.join(self.root, "secrets", "4cat-passwords", user)) == password
        except FileNotFoundError:
            return False

    def req(self, method, path, user=None, member=None, admin=None, roles=None):
        c = http.client.HTTPConnection("127.0.0.1", self.port, timeout=10)
        h = {k: v for k, v in (("REMOTE_USER", user), ("X-Desk-Member", member),
                               ("X-Desk-Admin", admin), ("X-Desk-Roles", roles)) if v is not None}
        c.request(method, path, headers=h); r = c.getresponse(); body = r.read().decode()
        c.close(); return r.status, body

    def mem(self, method, path, user="alice", **kw):
        """A request from a member (nginx's X-Desk-Member: 1)."""
        return self.req(method, path, user=user, member="1", **kw)

    def desk_obj(self):
        return self.srv.RequestHandlerClass.desk

    def docker_calls(self):
        return fakes.docker_calls(self.t)

    def starts(self):
        """The `compose up -d --no-deps` pair starts (R33), the only way a pair is started on open."""
        return [c for c in self.docker_calls() if " up -d --no-deps " in c]

    def start_call(self, user):
        return f"compose -f {self.root}/docker-compose.yml up -d --no-deps --pull never mitm-{user} firefox-{user}"

    def log(self):
        p = os.path.join(self.root, "state", "desk-log.ndjson")
        return [json.loads(l) for l in slurp(p).splitlines()] if os.path.exists(p) else []

    def access(self):
        return [json.loads(l) for l in slurp(os.path.join(self.root, "state", "access.ndjson")).splitlines()]

    def claimed(self, user):
        return [e.get("id") or e.get("video_id") for e in self.log() if e["action"] == "claim" and e["user"] == user]

    def finish(self, user, ids):
        for v in ids:
            self.assertEqual(self.mem("POST", f"/desk/problem?id={v}&code=U", user=user)[0], 204)

    def users_dir(self, user):
        return os.path.join(self.root, "state", "users", user)


class StubTest(StubHarness):
    # identity and the gate
    def test_roles_header_is_logged_once_per_change(self):
        self.mem("GET", "/desk/", roles="src_co_students;src_co_admin")
        self.mem("GET", "/desk/", roles="src_co_students;src_co_admin")
        self.mem("GET", "/desk/", roles="")
        self.assertEqual([(a["user"], a["roles"]) for a in self.access()],
                         [("alice", "src_co_students;src_co_admin"), ("alice", "")])

    def test_failed_access_append_is_retried_and_reported_once(self):
        os.makedirs(os.path.join(self.root, "state", "access.ndjson"))   # a directory: every append fails
        err = io.StringIO()
        with contextlib.redirect_stderr(err):
            for _ in range(3):
                self.assertEqual(self.mem("GET", "/desk/", roles="src_co_students")[0], 200)
        self.assertEqual(len(err.getvalue().splitlines()), 1, err.getvalue())
        self.assertIn("access log: IsADirectoryError", err.getvalue())
        self.assertNotIn("alice", self.desk_obj().last_roles)   # not recorded, so it is tried again
        os.rmdir(os.path.join(self.root, "state", "access.ndjson"))
        self.mem("GET", "/desk/", roles="src_co_students")
        self.assertEqual([(a["user"], a["roles"]) for a in self.access()], [("alice", "src_co_students")])
        self.assertEqual(self.desk_obj().last_roles["alice"], "src_co_students")

    def test_missing_remote_user_is_403(self):
        self.assertEqual(self.req("GET", "/desk/", member="1")[0], 403)
        self.assertEqual(self.req("POST", "/desk/claim", member="1")[0], 403)
        self.assertEqual(self.docker_calls(), [])

    def test_non_member_is_403_with_contact(self):
        s, body = self.req("GET", "/desk/", user="mallory")
        self.assertEqual(s, 403); self.assertIn("You are not registered for the desk; ask Jo.", body)
        self.assertEqual(self.req("POST", "/desk/claim", user="mallory")[0], 403)
        self.assertEqual(self.claimed("mallory"), [])
        self.assertFalse(os.path.exists(self.users_dir("mallory")))   # refused before any provisioning

    def test_non_member_is_403_even_if_registered(self):
        self.assertIn("alice", self.desk_obj().reg.live())
        self.assertEqual(self.req("GET", "/desk/", user="alice", member="0")[0], 403)
        self.assertEqual(self.req("GET", "/desk/status", user="alice", member="0")[0], 403)
        self.assertEqual(self.req("POST", "/desk/claim", user="alice", member="0")[0], 403)
        self.assertEqual(self.docker_calls(), [])

    def test_empty_roles_header_is_403_with_contact(self):
        s, body = self.req("GET", "/desk/", user="mallory", member="0", roles="")
        self.assertEqual(s, 403); self.assertIn("ask Jo", body)
        last = self.access()[-1]
        self.assertEqual((last["user"], last["roles"]), ("mallory", ""))

    def test_invalid_username_is_403(self):
        for u in ("Bad Name", "../alice", "1abc"):
            self.assertEqual(self.req("GET", "/desk/", user=u, member="1")[0], 403, u)
        self.assertEqual(self.docker_calls(), [])

    def test_paths_outside_desk_are_403(self):
        for p in ("/", "/websockify", "/results/abc/", "/api/import-dataset/"):
            self.assertEqual(self.mem("GET", p, admin="1")[0], 403, p)

    # first-visit provisioning
    def test_member_first_visit_provisions_and_shows_setup_page(self):
        s, body = self.mem("GET", "/desk/", user="carol")
        self.assertEqual(s, 200); self.assertIn("Setting up your desk — registry", body)
        self.assertIn('<meta http-equiv="refresh" content="3">', body)
        self.assertNotIn("'/desk/vnc/'", body)
        self.assertIn("Setting up your desk", self.mem("GET", "/desk/", user="carol")[1])   # a reload, maybe mid-run
        self.assertTrue(wait_for(lambda: self.desk_obj().prov.state("carol").ready()))
        self.assertTrue(wait_for(lambda: not self.desk_obj().provisioning))
        s, body = self.mem("GET", "/desk/", user="carol")
        self.assertEqual(s, 200); self.assertIn("'/desk/vnc/'", body); self.assertIn("Get 5 more videos", body)
        create = [c for c in self.docker_calls() if "create_user.py" in c]
        self.assertEqual(len(create), 1); self.assertIn("-u carol", create[0]); self.assertFalse(create[0].endswith(" -a"))

    def test_admin_not_in_collector_group_gets_a_session(self):
        s, body = self.mem("GET", "/desk/", user="dana", admin="1", roles="src_co_admin")
        self.assertEqual(s, 200); self.assertIn("Setting up your desk", body)
        self.assertTrue(wait_for(lambda: self.desk_obj().prov.state("dana").ready()))
        self.assertIn("dana", self.desk_obj().reg.live())
        create = [c for c in self.docker_calls() if "create_user.py" in c]
        self.assertEqual(len(create), 1); self.assertTrue(create[0].endswith(" -a"))   # admin from the header

    def test_missing_template_is_not_ready_page(self):
        self.serve(template_path=None)
        s, body = self.mem("GET", "/desk/", user="carol")
        self.assertEqual(s, 503); self.assertIn("The desk is not ready yet; ask Jo.", body)
        self.assertFalse(os.path.exists(self.users_dir("carol")))
        self.assertEqual(self.mem("GET", "/desk/")[0], 200)   # alice already has her profile

    def test_full_registry_page(self):
        self.serve(max_sessions=2)   # alice and bob hold both indexes
        self.assertEqual(self.desk_obj().prov.advance("carol").error(), "error: registry full")
        s, body = self.mem("GET", "/desk/", user="carol")
        self.assertEqual(s, 200); self.assertIn("The desk is full; ask Jo.", body)
        self.assertNotIn("Setup failed", body)

    def test_setup_error_shows_step_and_contact(self):
        self.serve(fourcat_url="http://127.0.0.1:1")   # 4CAT down
        self.assertEqual(self.desk_obj().prov.advance("carol").current(), "fourcat_token")
        s, body = self.mem("GET", "/desk/", user="carol")
        self.assertEqual(s, 200); self.assertIn("Setup failed at fourcat_token; ask Jo.", body)
        self.assertNotIn("refresh", body)
        self.assertEqual(self.desk_obj().provisioning, set())   # an error waits for an operator

    def test_first_visit_at_cap_ends_busy_not_running(self):
        self.serve(max_running=1)
        put(os.path.join(self.state, "desk-firefox-bob"), "")
        self.assertIn("Setting up your desk", self.mem("GET", "/desk/", user="carol")[1])
        self.assertTrue(wait_for(lambda: self.desk_obj().prov.state("carol").ready()))
        self.assertTrue(wait_for(lambda: not self.desk_obj().provisioning))
        s, body = self.mem("GET", "/desk/", user="carol")
        self.assertEqual(s, 200); self.assertIn("The desk is busy, try again in a few minutes.", body)
        calls = self.docker_calls()
        self.assertTrue(any(c.endswith("create firefox-carol") for c in calls), calls)
        self.assertFalse(any("firefox-carol" in c and (" up " in c or c.startswith("start")) for c in calls))
        self.assertFalse(self.running("desk-firefox-carol"))

    def test_cap_check_and_start_are_one_unit(self):
        self.serve(max_running=1)
        ts = [threading.Thread(target=self.mem, args=("GET", "/desk/"), kwargs={"user": u}) for u in ("alice", "bob")]
        [t.start() for t in ts]; [t.join() for t in ts]
        self.assertEqual(sum(self.running(f"desk-firefox-{u}") for u in ("alice", "bob")), 1)

    def test_failed_provision_run_is_logged_and_not_stuck(self):
        d = self.desk_obj(); real = d.prov.advance; tries = []

        def flaky(user):
            tries.append(user)
            if len(tries) == 1:
                raise OSError("disk went away")
            return real(user)
        d.prov.advance = flaky
        err = io.StringIO()
        with contextlib.redirect_stderr(err):
            self.mem("GET", "/desk/", user="carol")
            self.assertTrue(wait_for(lambda: not d.provisioning))
        self.assertIn("provision carol: OSError: disk went away", err.getvalue())
        self.assertIn("Setting up your desk", self.mem("GET", "/desk/", user="carol")[1])   # a new run starts
        self.assertTrue(wait_for(lambda: d.prov.state("carol").ready()))
        self.assertEqual(tries, ["carol", "carol"])

    def test_thread_start_failure_does_not_stick(self):
        d, err = self.desk_obj(), io.StringIO()
        with mock.patch.object(threading.Thread, "start", side_effect=RuntimeError("can't start new thread")), \
                contextlib.redirect_stderr(err):
            d.kick("carol")
        self.assertEqual(d.provisioning, set())
        self.assertIn("provision carol: RuntimeError: can't start new thread", err.getvalue())

    def test_busy_when_running_cap_reached(self):
        self.serve(max_running=1)
        put(os.path.join(self.state, "desk-firefox-bob"), "")
        s, body = self.mem("GET", "/desk/")
        self.assertEqual(s, 200); self.assertIn("The desk is busy, try again in a few minutes.", body)
        self.assertNotIn("<iframe", body)
        self.assertEqual(self.starts(), [])
        self.assertIn("'/desk/vnc/'", self.mem("GET", "/desk/", user="bob")[1])   # a running user still gets in

    # claiming
    def test_new_user_starts_empty_with_a_get_button(self):
        s, body = self.mem("GET", "/desk/")
        self.assertEqual(s, 200); self.assertIn("Get 5 more videos", body)
        self.assertNotIn('id="r101"', body)
        self.assertIn("'/desk/vnc/'", body)
        self.assertIn("<title>TikTok desk — alice</title>", body)

    def test_claim_takes_next_five_in_pool_order(self):
        self.assertEqual(self.mem("POST", "/desk/claim")[0], 204)
        self.assertEqual(self.claimed("alice"), ["101", "102", "103", "104", "105"])
        self.assertEqual({e["kind"] for e in self.log()}, {"video"})
        _, body = self.mem("GET", "/desk/")
        self.assertLess(body.index('id="r101"'), body.index('id="r105"'))
        self.assertIn('data-kind="video"', body)
        self.assertIn("nos, &quot;quoted&quot;", body)

    def test_page_has_howto_next_button_progress_and_upload_reminder(self):
        self.mem("POST", "/desk/claim")
        self.mem("POST", "/desk/open?id=101")
        self.mem("POST", "/desk/problem?id=102&code=U")
        _, body = self.mem("GET", "/desk/")
        self.assertIn("How to collect", body)
        self.assertIn('id="next"', body); self.assertIn("Open next video", body)
        self.assertIn("2 of 5 done", body)
        self.assertIn("Before you leave", body); self.assertIn("to 4CAT", body)
        self.assertIn('id="r103" data-state="todo"', body)
        self.assertIn('data-state="problem"', body)

    def test_rows_show_note_as_title_or_a_number(self):
        self.mem("POST", "/desk/claim")
        _, body = self.mem("GET", "/desk/")
        self.assertIn(">nos, &quot;quoted&quot;</a>", body)
        self.assertIn(">Video 2</a>", body)

    def test_page_offers_5_and_10(self):
        _, body = self.mem("GET", "/desk/")
        self.assertIn("Get 5 more videos", body); self.assertIn("Get 10 more videos", body)

    def test_claim_ten(self):
        self.assertEqual(self.mem("POST", "/desk/claim?n=10")[0], 204)
        self.assertEqual(self.claimed("alice"), [str(v) for v in range(101, 111)])

    def test_other_claim_sizes_refused(self):
        # %C2%B9 is "¹" and %D9%A5 is "٥": str.isdigit() says yes to both, int("¹") raises
        for n in ("7", "100", "0", "abc", "%C2%B9", "%D9%A5", "5%0A"):
            self.assertEqual(self.mem("POST", f"/desk/claim?n={n}")[0], 400, n)
        self.assertEqual(self.claimed("alice"), [])

    def test_timer_and_batch_sizes_from_config(self):
        self.assertIn("SECONDS=180", self.mem("GET", "/desk/")[1])
        self.serve(batch_sizes=[3, 6], timer_minutes=0)
        _, body = self.mem("GET", "/desk/")
        self.assertIn("Get 3 more videos", body); self.assertIn("Get 6 more videos", body)
        self.assertNotIn("Get 5 more", body)
        self.assertEqual(self.mem("POST", "/desk/claim?n=5")[0], 400)
        self.assertEqual(self.mem("POST", "/desk/claim?n=3")[0], 204)
        _, body = self.mem("GET", "/desk/")
        self.assertIn("SECONDS=0", body); self.assertIn('id="r101"', body)
        self.assertNotIn('class="timer"', body); self.assertNotIn("timer reaches", body)

    def test_two_users_never_share_a_video(self):
        self.mem("POST", "/desk/claim"); self.mem("POST", "/desk/claim", user="bob")
        a, b = set(self.claimed("alice")), set(self.claimed("bob"))
        self.assertEqual(len(a), 5); self.assertEqual(len(b), 5); self.assertFalse(a & b)

    def test_concurrent_claims_never_overlap(self):
        for i in range(1, 7):
            self.assertTrue(self.desk_obj().prov.advance(f"u{i}").ready())
        ts =[threading.Thread(target=self.mem, args=("POST", "/desk/claim"), kwargs={"user": f"u{i}"})
              for i in range(1, 7)]
        [t.start() for t in ts]; [t.join() for t in ts]
        ids = [e["id"] for e in self.log() if e["action"] == "claim"]
        self.assertEqual(len(ids), 12); self.assertEqual(len(set(ids)), 12)

    def test_cannot_claim_again_until_current_set_done(self):
        self.mem("POST", "/desk/claim")
        self.assertEqual(self.mem("POST", "/desk/claim")[0], 409)
        _, body = self.mem("GET", "/desk/")
        self.assertIn("Finish these first", body)
        self.finish("alice", ["101", "102", "103", "104"])
        self.assertEqual(self.mem("POST", "/desk/claim")[0], 409)
        self.finish("alice", ["105"])
        self.assertEqual(self.mem("POST", "/desk/claim")[0], 204)
        self.assertEqual(self.claimed("alice")[5:], ["106", "107", "108", "109", "110"])

    def test_page_hides_earlier_finished_sets(self):
        self.mem("POST", "/desk/claim"); self.finish("alice", ["101", "102", "103", "104", "105"])
        self.mem("POST", "/desk/claim")
        _, body = self.mem("GET", "/desk/")
        self.assertNotIn('id="r101"', body); self.assertIn('id="r106"', body)
        self.assertIn("5 done earlier", body)

    def test_pool_runs_dry(self):
        self.mem("POST", "/desk/claim"); self.finish("alice", [str(v) for v in range(101, 106)])
        self.mem("POST", "/desk/claim"); self.finish("alice", [str(v) for v in range(106, 111)])
        self.assertEqual(self.mem("POST", "/desk/claim")[0], 204)
        self.assertEqual(self.claimed("alice")[10:], ["111", "112"])
        self.finish("alice", ["111", "112"])
        self.assertEqual(self.mem("POST", "/desk/claim")[0], 409)
        _, body = self.mem("GET", "/desk/")
        self.assertIn("No more videos right now", body)

    def test_pool_growth_is_picked_up(self):
        self.mem("POST", "/desk/claim")
        write_csv(os.path.join(self.root, "pool.csv"),
                  [["999", "video", "training", ""], ["bad id", "video", "x", ""], ["5", "photo", "x", ""]]
                  + [[str(100 + i), "video", "training", ""] for i in range(1, 13)],
                  header=("id", "kind", "bucket", "note"))
        self.finish("alice", [str(v) for v in range(101, 106)])
        self.mem("POST", "/desk/claim")
        self.assertEqual(self.claimed("alice")[5:7], ["999", "106"])   # invalid id and unknown kind skipped

    def test_unreadable_pool_is_reported(self):
        cases = ((("foo", "bar"), [["101", "x"]], "unrecognised header"),
                 (("id", "kind", "bucket", "note"), [["101", "video", "x", "n" * 200_000]], "field larger"))
        for header, rows, reason in cases:
            write_csv(os.path.join(self.root, "pool.csv"), rows, header=header)
            s, body = self.mem("GET", "/desk/")
            self.assertEqual(s, 200, reason); self.assertIn("No more videos right now; tell the operator.", body)
            self.assertEqual(self.mem("POST", "/desk/claim")[0], 409)
            s, body = self.mem("GET", "/desk/admin/", admin="1")
            self.assertEqual(s, 200); self.assertIn("pool.csv unreadable: ", body); self.assertIn(reason, body)
            self.assertNotIn("unclaimed in the pool", body)
        write_csv(os.path.join(self.root, "pool.csv"), [["101", "x", ""]])
        self.assertIn("1 of 1 videos unclaimed", self.mem("GET", "/desk/admin/", admin="1")[1])   # error clears

    def test_pool_note_is_not_a_placeholder(self):
        write_csv(os.path.join(self.root, "pool.csv"), [["101", "x", "%%FOOTER%%"], ["102", "x", "<b>%%BODY%%"]])
        self.mem("POST", "/desk/claim")
        _, body = self.mem("GET", "/desk/")
        self.assertIn(">%%FOOTER%%</a>", body); self.assertIn(">&lt;b&gt;%%BODY%%</a>", body)
        self.assertEqual(body.count("Before you leave"), 1)

    def test_member_not_ready_cannot_claim(self):
        self.assertEqual(self.mem("POST", "/desk/claim", user="carol")[0], 409)
        self.assertEqual(self.claimed("carol"), [])
        self.assertFalse(os.path.exists(self.users_dir("carol")))   # a claim never provisions

    # opens
    def test_open_execs_into_own_container(self):
        self.mem("POST", "/desk/claim")
        self.assertEqual(self.mem("POST", "/desk/open?id=101")[0], 204)
        calls = self.docker_calls(); self.assertEqual(len(calls), 1)
        self.assertIn("desk-firefox-alice", calls[0]); self.assertNotIn("desk-firefox-bob", calls[0])
        self.assertIn("su-exec 1000:1000", calls[0])
        ev = self.log()[-1]
        self.assertEqual((ev["user"], ev["action"], ev["id"], ev["exec_rc"]), ("alice", "open", "101", 0))

    def test_open_builds_url_from_platform_and_kind(self):
        self.mem("POST", "/desk/claim")
        self.assertEqual(self.mem("POST", "/desk/open?id=101&kind=video")[0], 204)
        self.assertIn("https://www.tiktok.com/@x/video/101/", self.docker_calls()[-1])
        self.assertEqual((self.log()[-1]["kind"], self.log()[-1]["id"]), ("video", "101"))
        self.assertEqual(self.mem("POST", "/desk/open?id=102&kind=photo")[0], 404)   # not a kind of this platform
        self.assertEqual(len(self.docker_calls()), 1)

    def test_legacy_video_id_events_still_count(self):
        put(os.path.join(self.root, "state", "desk-log.ndjson"), json.dumps(
            {"t": 1, "user": "alice", "action": "claim", "video_id": "101", "batch": 1, "code": ""}) + "\n")
        _, body = self.mem("GET", "/desk/")
        self.assertIn('id="r101"', body); self.assertIn("Finish these first", body)
        self.assertEqual(self.mem("POST", "/desk/open?id=101")[0], 204)
        self.mem("POST", "/desk/claim", user="bob")
        self.assertEqual(self.claimed("bob")[0], "102")   # the legacy claim still holds 101

    def test_unclaimed_or_foreign_id_is_404_without_exec(self):
        self.mem("POST", "/desk/claim"); self.mem("POST", "/desk/claim", user="bob")
        self.assertEqual(self.mem("POST", "/desk/open?id=106")[0], 404)   # bob's
        self.assertEqual(self.mem("POST", "/desk/open?id=112")[0], 404)   # unclaimed
        self.assertEqual(self.mem("POST", "/desk/open?id=abc")[0], 404)
        self.assertEqual(self.mem("POST", "/desk/problem?id=106&code=U")[0], 404)
        self.assertEqual(self.docker_calls(), [])

    def test_failed_exec_is_500_and_not_done(self):
        self.mem("POST", "/desk/claim")
        put(self.mode, "fail")
        self.assertEqual(self.mem("POST", "/desk/open?id=101")[0], 500)
        self.assertEqual(self.log()[-1]["exec_rc"], 1)
        put(self.mode, "")
        _, body = self.mem("GET", "/desk/")
        self.assertNotIn('class="done"', body)

    def test_hanging_exec_times_out_and_is_logged(self):
        self.mem("POST", "/desk/claim")
        put(self.mode, "hang")
        self.assertEqual(self.mem("POST", "/desk/open?id=101")[0], 500)
        self.assertEqual(self.log()[-1]["exec_rc"], "timeout")

    def test_prior_opens_and_problems_render(self):
        self.mem("POST", "/desk/claim")
        self.mem("POST", "/desk/open?id=101")
        self.assertEqual(self.mem("POST", "/desk/problem?id=102&code=P")[0], 204)
        _, body = self.mem("GET", "/desk/")
        self.assertIn('id="r101" class="done"', body)
        self.assertIn("— Private video", body)

    def test_problem_choices_are_spelled_out(self):
        self.mem("POST", "/desk/claim")
        _, body = self.mem("GET", "/desk/")
        for label in ("Video unavailable or removed", "Private video", "Needs a login to view",
                      "Login wall cut the comments short", "Not available in this region",
                      "Captcha or blank page: stop for today and tell Jo", "Something else (add a note)"):
            self.assertIn(label, body)
        self.assertIn('NOTE_CODES=["X"]', body)
        self.assertNotIn("prompt('Problem code", body)

    def test_other_needs_a_note_which_is_logged(self):
        self.mem("POST", "/desk/claim")
        self.assertEqual(self.mem("POST", "/desk/problem?id=101&code=X")[0], 400)
        self.assertEqual(self.mem("POST", "/desk/problem?id=101&kind=video&code=X&note=video%20is%20a%20livestream")[0], 204)
        ev = self.log()[-1]
        self.assertEqual((ev["code"], ev["note"], ev["kind"]), ("X", "video is a livestream", "video"))
        _, body = self.mem("GET", "/desk/")
        self.assertIn("— Something else (add a note): video is a livestream", body)

    def test_note_is_escaped_on_the_page(self):
        self.mem("POST", "/desk/claim")
        self.mem("POST", "/desk/problem?id=101&code=X&note=%3Cscript%3Ex%3C/script%3E")
        _, body = self.mem("GET", "/desk/")
        self.assertNotIn("<script>x</script>", body); self.assertIn("&lt;script&gt;", body)

    def test_bad_problem_code_is_400(self):
        self.mem("POST", "/desk/claim")
        self.assertEqual(self.mem("POST", "/desk/problem?id=101&code=Q")[0], 400)

    def test_footer_uses_upload_hint_and_hides_without_fourcat(self):
        _, body = self.mem("GET", "/desk/")
        self.assertIn(HINT, body)
        self.assertNotIn("enableUploads", body)
        self.assertEqual(self.mem("POST", "/desk/enable-uploads")[0], 404)
        self.assertEqual(self.mem("GET", "/desk/uploads-ready/")[0], 404)
        self.serve(fourcat_enabled=False)
        _, body = self.mem("GET", "/desk/")
        self.assertNotIn("Before you leave", body); self.assertNotIn("<footer", body)

    # admin
    def test_admin_page_needs_admin_header(self):
        self.assertEqual(self.mem("GET", "/desk/admin/")[0], 403)
        self.assertEqual(self.mem("GET", "/desk/admin/", admin="0")[0], 403)
        self.assertEqual(self.req("GET", "/desk/admin/", user="alice", admin="1")[0], 403)   # not a member

    def test_member_non_admin_post_to_admin_routes_is_403(self):
        sess = self.desk_obj().sessions
        before = slurp(os.path.join(self.root, "pool.csv"))
        with mock.patch.object(sess, "prune") as pr:
            for p in ("/desk/admin/sessions?op=prune", "/desk/admin/pool"):
                self.assertEqual(self.mem("POST", p, admin="0")[0], 403, p)
            pr.assert_not_called()
        self.assertEqual(slurp(os.path.join(self.root, "pool.csv")), before)
        self.assertEqual(self.docker_calls(), [])

    def test_admin_without_trailing_slash_redirects(self):
        c = http.client.HTTPConnection("127.0.0.1", self.port, timeout=10)
        c.request("GET", "/desk/admin", headers={"REMOTE_USER": "alice", "X-Desk-Member": "1", "X-Desk-Admin": "1"})
        r = c.getresponse(); r.read(); c.close()
        self.assertEqual((r.status, r.getheader("Location")), (301, "/desk/admin/"))

    def test_admin_page_lists_sessions_and_pool(self):
        self.mem("POST", "/desk/claim"); self.mem("POST", "/desk/open?id=101")
        s, body = self.mem("GET", "/desk/admin/", admin="1")
        self.assertEqual(s, 200)
        self.assertIn("/desk/admin/vnc/alice/", body); self.assertIn("/desk/admin/vnc/bob/", body)
        self.assertIn("Up 2 hours", body)
        self.assertIn("7 of 12 videos unclaimed", body)
        self.assertIn('<a href="/">4CAT</a>', body)
        self.serve(fourcat_enabled=False)
        self.assertNotIn("4CAT</a>", self.mem("GET", "/desk/admin/", admin="1")[1])

    # start on open, stop when idle
    def running(self, name):
        return os.path.exists(os.path.join(self.state, name))

    def test_page_starts_own_stopped_session_only(self):
        for u in ("alice", "bob"):   # the single-file policy layout, still on disk
            put(os.path.join(self.root, "firefox", f"policies-{u}.json"), "{}")
        self.assertEqual(self.mem("GET", "/desk/")[0], 200)
        self.assertTrue(self.running("desk-mitm-alice")); self.assertTrue(self.running("desk-firefox-alice"))
        self.assertFalse(self.running("desk-firefox-bob"))
        self.assertEqual(self.starts(), [self.start_call("alice")])   # compose up, so drift is recreated
        self.assertFalse(any(c.startswith("start") for c in self.docker_calls()))   # never a bare docker start
        # alice's pair went through Compose, so her legacy policy goes; bob's waits for his own start
        self.assertFalse(os.path.exists(os.path.join(self.root, "firefox", "policies-alice.json")))
        self.assertTrue(os.path.exists(os.path.join(self.root, "firefox", "policies-bob.json")))

    def test_start_failure_is_reported(self):
        put(os.path.join(self.root, "firefox", "policies-alice.json"), "{}")
        put(self.mode, "fail")
        s, body = self.mem("GET", "/desk/")
        self.assertEqual(s, 200)
        self.assertIn("Your browser could not be started; try again in a minute or ask Jo.", body)
        self.assertNotIn("'/desk/vnc/'", body)
        self.assertNotIn("alice", self.desk_obj().last_seen)
        # mitm is up since provisioning; the pair's compose up is what fails
        self.assertEqual(self.starts(), [self.start_call("alice")])
        self.assertFalse(self.running("desk-firefox-alice"))
        self.assertTrue(os.path.exists(os.path.join(self.root, "firefox", "policies-alice.json")))   # kept
        put(self.mode, "")
        self.assertIn("'/desk/vnc/'", self.mem("GET", "/desk/")[1])   # the next visit starts it

    def test_start_without_the_policy_file_is_start_failed(self):
        os.remove(os.path.join(self.root, "firefox", "policy", "alice", "policies.json"))
        s, body = self.mem("GET", "/desk/")
        self.assertIn("Your browser could not be started", body)
        self.assertEqual(self.starts(), [])                      # compose was never run
        self.assertFalse(self.running("desk-firefox-alice"))
        self.assertNotIn("alice", self.desk_obj().last_seen)

    def test_legacy_policy_kept_while_compose_file_references_it(self):
        put(os.path.join(self.root, "firefox", "policies-alice.json"), "{}")
        compose = os.path.join(self.root, "docker-compose.yml")
        put(compose, slurp(compose) + "\n# - ./firefox/policies-alice.json:/x\n")   # a pre-R29 layout
        self.assertIn("'/desk/vnc/'", self.mem("GET", "/desk/")[1])
        self.assertTrue(os.path.exists(os.path.join(self.root, "firefox", "policies-alice.json")))

    def test_failing_legacy_drop_still_starts(self):
        from unittest import mock
        put(os.path.join(self.root, "firefox", "policies-alice.json"), "{}")
        with mock.patch("desklib.sessions.drop_legacy_policy", side_effect=PermissionError("nope")):
            self.assertIn("'/desk/vnc/'", self.mem("GET", "/desk/")[1])
        self.assertTrue(self.running("desk-firefox-alice"))
        self.assertIn("alice", self.desk_obj().last_seen)

    def hold_alice_lock_during_visit(self, while_held=lambda: None):
        """GET /desk/ for alice while this thread holds her user lock; returns (calls while held, response)."""
        out = []
        with locks.flocked(locks.user_lock(self.root, "alice")):
            th = threading.Thread(target=lambda: out.append(self.mem("GET", "/desk/")))
            th.start()
            time.sleep(0.5)
            held_calls = list(self.docker_calls())
            self.assertTrue(th.is_alive())   # the request waits on the lock
            while_held()
        th.join(10)
        return held_calls, out[0]

    def test_start_on_open_waits_for_retire(self):
        calls, (s, body) = self.hold_alice_lock_during_visit()
        self.assertFalse([c for c in calls if c.startswith("inspect") or " up -d " in c], calls)   # nothing while held
        self.assertEqual(s, 200); self.assertIn("'/desk/vnc/'", body)
        self.assertTrue(self.running("desk-firefox-alice"))

        def retire_meanwhile():   # what Provisioner.retire does under that lock (after its stops)
            os.remove(os.path.join(self.users_dir("alice"), "provision.json"))
            with locks.flocked(locks.render_lock(self.root)):
                self.desk_obj().reg.retire("alice")
        os.remove(os.path.join(self.state, "desk-firefox-alice")); os.remove(os.path.join(self.state, "desk-mitm-alice"))
        os.remove(self.calls)
        calls, (s, body) = self.hold_alice_lock_during_visit(retire_meanwhile)
        self.assertEqual(s, 200); self.assertNotIn("'/desk/vnc/'", body)
        self.assertIn("Your desk is changing", body)
        self.assertEqual(self.starts(), [])   # a retired desk stays down
        self.assertFalse(self.running("desk-firefox-alice"))

    def test_ensure_started_holds_user_lock_then_start_lock(self):
        d, seen = self.desk_obj(), []
        real = d.sessions.running

        def probe():
            try:
                with locks.flocked(locks.user_lock(self.root, "alice"), timeout=0.05):
                    seen.append("user lock free")
            except TimeoutError:
                seen.append("user lock held")
            seen.append("start_lock held" if d.start_lock.locked() else "start_lock free")
            return real()
        with mock.patch.object(d.sessions, "running", probe):
            self.assertIn("'/desk/vnc/'", self.mem("GET", "/desk/")[1])
        self.assertEqual(seen, ["user lock held", "start_lock held"])

    def test_page_does_not_restart_running_session(self):
        put(os.path.join(self.state, "desk-mitm-alice"), ""); put(os.path.join(self.state, "desk-firefox-alice"), "")
        self.mem("GET", "/desk/")
        self.assertEqual(self.starts(), [])   # a fully running pair is never recreated under the user

    def test_page_waits_for_screen_before_loading_it(self):
        _, body = self.mem("GET", "/desk/")
        self.assertIn("Starting your browser", body)
        self.assertNotIn('src="/desk/vnc/"', body)   # the iframe gets its src once /desk/status says ready
        import socket
        with socket.socket() as probe:
            probe.bind(("127.0.0.1", 0)); free = probe.getsockname()[1]   # a port nothing listens on, on any host
        self.desk_obj().reg.port_base = free - 1                          # alice is index 1
        s, st = self.mem("GET", "/desk/status")
        self.assertEqual(s, 200); self.assertEqual(json.loads(st)["ready"], False)

    def _listener(self, handler):
        import socket
        s = socket.socket(); s.bind(("127.0.0.1", 0)); s.listen(4)
        def serve():
            try:
                while True:
                    conn, _ = s.accept(); handler(conn); conn.close()
            except OSError:
                pass
        threading.Thread(target=serve, daemon=True).start()
        self.addCleanup(s.close)
        self.desk_obj().reg.port_base = s.getsockname()[1] - 1   # alice is index 1
        put(os.path.join(self.state, "desk-firefox-alice"), "")

    def test_not_ready_while_proxy_accepts_but_resets(self):
        self._listener(lambda conn: None)   # accept, then hang up: Docker's proxy before noVNC listens
        self.assertFalse(self.desk_obj().ready("alice"))

    def test_ready_when_novnc_answers_200(self):
        def ok(conn):
            conn.recv(4096); conn.sendall(b"HTTP/1.1 200 OK\r\nContent-Length: 2\r\nConnection: close\r\n\r\nok")
        self._listener(ok)
        self.assertTrue(self.desk_obj().ready("alice"))

    def test_status_needs_a_member(self):
        self.assertEqual(self.req("GET", "/desk/status", user="mallory")[0], 403)

    def test_reaper_stops_idle_session_without_screen(self):
        put(os.path.join(self.state, "desk-firefox-alice"), ""); put(os.path.join(self.state, "desk-mitm-alice"), "")
        d = self.desk_obj(); d.last_seen["alice"] = 1000
        d.reap(now=1000 + 901)
        self.assertFalse(self.running("desk-firefox-alice"))
        self.assertTrue(self.running("desk-mitm-alice"))
        self.assertEqual(self.log()[-1]["action"], "stop")

    def test_reaper_logs_a_stop_only_when_it_worked(self):
        put(os.path.join(self.state, "desk-firefox-alice"), "")
        d = self.desk_obj(); d.last_seen["alice"] = 1000
        put(self.mode, "fail")                      # docker stop exits 1
        d.reap(now=1000 + 901)
        self.assertTrue(self.running("desk-firefox-alice"))
        self.assertEqual([e for e in self.log() if e["action"] == "stop"], [])   # no false record
        self.assertEqual(d.last_seen["alice"], 1000)                              # the next sweep retries
        put(self.mode, "")
        d.reap(now=1000 + 960)
        self.assertFalse(self.running("desk-firefox-alice"))
        self.assertEqual([e["user"] for e in self.log() if e["action"] == "stop"], ["alice"])
        self.assertNotIn("alice", d.last_seen)

    def test_reaper_keeps_session_with_screen_connected(self):
        put(os.path.join(self.state, "desk-firefox-alice"), "")
        put(self.ss_out, "ESTAB 0 0 127.0.0.1:5801 127.0.0.1:43210\nESTAB 0 0 127.0.0.1:5802 127.0.0.1:43211\n")
        d = self.desk_obj(); d.last_seen["alice"] = 1000
        d.reap(now=1000 + 5000)
        self.assertTrue(self.running("desk-firefox-alice"))
        self.assertEqual(d.last_seen["alice"], 6000)

    def test_reaper_grace_after_page_load(self):
        self.mem("GET", "/desk/")
        d = self.desk_obj()
        d.reap(now=d.last_seen["alice"] + 60)
        self.assertTrue(self.running("desk-firefox-alice"))

    def test_reaper_grace_for_sessions_found_running(self):
        put(os.path.join(self.state, "desk-firefox-bob"), "")
        d = self.desk_obj()
        d.reap(now=5000)            # first sight: start the clock, don't stop
        self.assertTrue(self.running("desk-firefox-bob"))
        d.reap(now=5000 + 901)
        self.assertFalse(self.running("desk-firefox-bob"))


if __name__ == "__main__":
    unittest.main()
