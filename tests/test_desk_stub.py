"""HTTP tests for desk/desk_stub.py with a fake `docker` on PATH (no Docker, no nginx)."""
import csv, http.client, json, os, subprocess, sys, tempfile, threading, unittest

REPO = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
sys.path.insert(0, os.path.join(REPO, "desk"))
import desk_stub  # noqa: E402

FAKE_DOCKER = """#!/usr/bin/env bash
echo "$*" >> "$FAKE_DOCKER_CALLS"
case "$(cat "$FAKE_DOCKER_MODE" 2>/dev/null)" in
  fail) echo "boom" >&2; exit 1;;
  hang) sleep 5;;
esac
[ "$1" = ps ] && printf 'desk-firefox-alice\\tUp 2 hours\\ndesk-mitm-alice\\tUp 2 hours\\n'
exit 0
"""


def slurp(path):
    with open(path) as f: return f.read()


def put(path, text):
    with open(path, "w") as f: f.write(text)


def write_csv(path, rows):
    with open(path, "w", newline="") as f:
        w = csv.writer(f); w.writerow(["video_id", "bucket", "note"]); w.writerows(rows)


class StubTest(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory(); t = self.tmp.name
        self.desk = os.path.join(t, "desk"); os.makedirs(self.desk)
        put(os.path.join(self.desk, "sessions.tsv"), "alice\t1\nbob\t2\n")
        rows = [[str(100 + i), "training", ""] for i in range(1, 13)]   # 101..112
        rows[0][2] = 'nos, "quoted"'
        write_csv(os.path.join(self.desk, "pool.csv"), rows)
        bindir = os.path.join(t, "bin"); os.makedirs(bindir)
        put(os.path.join(bindir, "docker"), FAKE_DOCKER); os.chmod(os.path.join(bindir, "docker"), 0o755)
        self.calls = os.path.join(t, "calls"); self.mode = os.path.join(t, "mode")
        self.env = {"PATH": bindir + ":" + os.environ["PATH"], "FAKE_DOCKER_CALLS": self.calls, "FAKE_DOCKER_MODE": self.mode}
        self._old = {k: os.environ.get(k) for k in self.env}; os.environ.update(self.env)
        self.srv = desk_stub.make_server(self.desk, 0, exec_timeout=1, batch=5)
        self.port = self.srv.server_address[1]
        threading.Thread(target=self.srv.serve_forever, daemon=True).start()

    def tearDown(self):
        self.srv.shutdown(); self.srv.server_close()
        for k, v in self._old.items():
            if v is None: os.environ.pop(k, None)
            else: os.environ[k] = v
        self.tmp.cleanup()

    def req(self, method, path, user=None, admin=None):
        c = http.client.HTTPConnection("127.0.0.1", self.port, timeout=10)
        h = {}
        if user is not None: h["REMOTE_USER"] = user
        if admin is not None: h["X-Desk-Admin"] = admin
        c.request(method, path, headers=h); r = c.getresponse(); body = r.read().decode()
        c.close(); return r.status, body

    def docker_calls(self):
        return slurp(self.calls).splitlines() if os.path.exists(self.calls) else []

    def log(self):
        p = os.path.join(self.desk, "state", "desk-log.ndjson")
        return [json.loads(l) for l in slurp(p).splitlines()] if os.path.exists(p) else []

    def claimed(self, user):
        return [e["video_id"] for e in self.log() if e["action"] == "claim" and e["user"] == user]

    def finish(self, user, ids):
        for v in ids:
            self.assertEqual(self.req("POST", f"/desk/problem?id={v}&code=U", user=user)[0], 204)

    # identity
    def test_missing_remote_user_is_403(self):
        self.assertEqual(self.req("GET", "/desk/")[0], 403)
        self.assertEqual(self.req("POST", "/desk/claim")[0], 403)
        self.assertEqual(self.docker_calls(), [])

    def test_unregistered_user_is_403(self):
        s, body = self.req("GET", "/desk/", user="mallory")
        self.assertEqual(s, 403); self.assertIn("not registered", body)
        self.assertEqual(self.req("POST", "/desk/claim", user="mallory")[0], 403)
        self.assertEqual(self.claimed("mallory"), [])

    def test_paths_outside_desk_are_403(self):
        for p in ("/", "/websockify", "/results/abc/", "/api/import-dataset/"):
            self.assertEqual(self.req("GET", p, user="alice", admin="1")[0], 403, p)

    # claiming
    def test_new_user_starts_empty_with_a_get_button(self):
        s, body = self.req("GET", "/desk/", user="alice")
        self.assertEqual(s, 200); self.assertIn("Get 5 more videos", body)
        self.assertNotIn('id="r101"', body)
        self.assertIn('src="/desk/vnc/"', body)

    def test_claim_takes_next_five_in_pool_order(self):
        self.assertEqual(self.req("POST", "/desk/claim", user="alice")[0], 204)
        self.assertEqual(self.claimed("alice"), ["101", "102", "103", "104", "105"])
        _, body = self.req("GET", "/desk/", user="alice")
        self.assertLess(body.index('id="r101"'), body.index('id="r105"'))
        self.assertIn("nos, &quot;quoted&quot;", body)

    def test_two_users_never_share_a_video(self):
        self.req("POST", "/desk/claim", user="alice"); self.req("POST", "/desk/claim", user="bob")
        a, b = set(self.claimed("alice")), set(self.claimed("bob"))
        self.assertEqual(len(a), 5); self.assertEqual(len(b), 5); self.assertFalse(a & b)

    def test_concurrent_claims_never_overlap(self):
        put(os.path.join(self.desk, "sessions.tsv"), "".join(f"u{i}\t{i}\n" for i in range(1, 7)))
        ts = [threading.Thread(target=self.req, args=("POST", "/desk/claim"), kwargs={"user": f"u{i}"}) for i in range(1, 7)]
        [t.start() for t in ts]; [t.join() for t in ts]
        ids = [e["video_id"] for e in self.log() if e["action"] == "claim"]
        self.assertEqual(len(ids), 12); self.assertEqual(len(set(ids)), 12)

    def test_cannot_claim_again_until_current_set_done(self):
        self.req("POST", "/desk/claim", user="alice")
        self.assertEqual(self.req("POST", "/desk/claim", user="alice")[0], 409)
        _, body = self.req("GET", "/desk/", user="alice")
        self.assertIn("Finish these first", body)
        self.finish("alice", ["101", "102", "103", "104"])
        self.assertEqual(self.req("POST", "/desk/claim", user="alice")[0], 409)
        self.finish("alice", ["105"])
        self.assertEqual(self.req("POST", "/desk/claim", user="alice")[0], 204)
        self.assertEqual(self.claimed("alice")[5:], ["106", "107", "108", "109", "110"])

    def test_page_hides_earlier_finished_sets(self):
        self.req("POST", "/desk/claim", user="alice"); self.finish("alice", ["101", "102", "103", "104", "105"])
        self.req("POST", "/desk/claim", user="alice")
        _, body = self.req("GET", "/desk/", user="alice")
        self.assertNotIn('id="r101"', body); self.assertIn('id="r106"', body)
        self.assertIn("5 done earlier", body)

    def test_pool_runs_dry(self):
        self.req("POST", "/desk/claim", user="alice"); self.finish("alice", [str(v) for v in range(101, 106)])
        self.req("POST", "/desk/claim", user="alice"); self.finish("alice", [str(v) for v in range(106, 111)])
        self.assertEqual(self.req("POST", "/desk/claim", user="alice")[0], 204)
        self.assertEqual(self.claimed("alice")[10:], ["111", "112"])
        self.finish("alice", ["111", "112"])
        self.assertEqual(self.req("POST", "/desk/claim", user="alice")[0], 409)
        _, body = self.req("GET", "/desk/", user="alice")
        self.assertIn("No more videos right now", body)

    def test_pool_growth_is_picked_up(self):
        self.req("POST", "/desk/claim", user="alice")
        write_csv(os.path.join(self.desk, "pool.csv"), [["999", "training", ""]] + [[str(100 + i), "training", ""] for i in range(1, 13)])
        self.finish("alice", [str(v) for v in range(101, 106)])
        self.req("POST", "/desk/claim", user="alice")
        self.assertEqual(self.claimed("alice")[5], "999")

    # opens
    def test_open_execs_into_own_container(self):
        self.req("POST", "/desk/claim", user="alice")
        self.assertEqual(self.req("POST", "/desk/open?id=101", user="alice")[0], 204)
        calls = self.docker_calls(); self.assertEqual(len(calls), 1)
        self.assertIn("desk-firefox-alice", calls[0]); self.assertNotIn("desk-firefox-bob", calls[0])
        self.assertIn("su-exec 1000:1000", calls[0])
        self.assertIn("https://www.tiktok.com/@x/video/101/", calls[0])
        ev = self.log()[-1]
        self.assertEqual((ev["user"], ev["action"], ev["video_id"], ev["exec_rc"]), ("alice", "open", "101", 0))

    def test_unclaimed_or_foreign_id_is_404_without_exec(self):
        self.req("POST", "/desk/claim", user="alice"); self.req("POST", "/desk/claim", user="bob")
        self.assertEqual(self.req("POST", "/desk/open?id=106", user="alice")[0], 404)   # bob's
        self.assertEqual(self.req("POST", "/desk/open?id=112", user="alice")[0], 404)   # unclaimed
        self.assertEqual(self.req("POST", "/desk/open?id=abc", user="alice")[0], 404)
        self.assertEqual(self.req("POST", "/desk/problem?id=106&code=U", user="alice")[0], 404)
        self.assertEqual(self.docker_calls(), [])

    def test_failed_exec_is_500_and_not_done(self):
        self.req("POST", "/desk/claim", user="alice")
        put(self.mode, "fail")
        self.assertEqual(self.req("POST", "/desk/open?id=101", user="alice")[0], 500)
        self.assertEqual(self.log()[-1]["exec_rc"], 1)
        put(self.mode, "")
        _, body = self.req("GET", "/desk/", user="alice")
        self.assertNotIn('class="done"', body)

    def test_hanging_exec_times_out_and_is_logged(self):
        self.req("POST", "/desk/claim", user="alice")
        put(self.mode, "hang")
        self.assertEqual(self.req("POST", "/desk/open?id=101", user="alice")[0], 500)
        self.assertEqual(self.log()[-1]["exec_rc"], "timeout")

    def test_prior_opens_and_problems_render(self):
        self.req("POST", "/desk/claim", user="alice")
        self.req("POST", "/desk/open?id=101", user="alice")
        self.assertEqual(self.req("POST", "/desk/problem?id=102&code=P", user="alice")[0], 204)
        _, body = self.req("GET", "/desk/", user="alice")
        self.assertIn('id="r101" class="done"', body)
        self.assertIn("problem P", body)

    def test_bad_problem_code_is_400(self):
        self.req("POST", "/desk/claim", user="alice")
        self.assertEqual(self.req("POST", "/desk/problem?id=101&code=Q", user="alice")[0], 400)

    # admin
    def test_admin_page_needs_admin_header(self):
        self.assertEqual(self.req("GET", "/desk/admin/", user="alice")[0], 403)
        self.assertEqual(self.req("GET", "/desk/admin/", user="alice", admin="0")[0], 403)

    def test_admin_page_lists_sessions_and_pool(self):
        self.req("POST", "/desk/claim", user="alice"); self.req("POST", "/desk/open?id=101", user="alice")
        s, body = self.req("GET", "/desk/admin/", user="alice", admin="1")
        self.assertEqual(s, 200)
        self.assertIn("/desk/admin/vnc/alice/", body); self.assertIn("/desk/admin/vnc/bob/", body)
        self.assertIn("Up 2 hours", body)
        self.assertIn("7 of 12 videos unclaimed", body)


class PoolAddTest(unittest.TestCase):
    def test_appends_new_ids_only_and_keeps_notes(self):
        with tempfile.TemporaryDirectory() as t:
            desk = os.path.join(t, "desk"); os.makedirs(desk)
            add = os.path.join(REPO, "desk", "pool-add.py")
            a = os.path.join(t, "a.csv"); write_csv(a, [["1", "x", "n1, q"], ["2", "x", ""], ["2", "x", "dup"]])
            b = os.path.join(t, "b.csv"); write_csv(b, [["2", "y", ""], ["3", "y", ""], ["junk", "y", ""]])
            for f, want in ((a, "added 2"), (b, "added 1")):
                r = subprocess.run([sys.executable, add, "--desk", desk, f], capture_output=True, text=True)
                self.assertEqual(r.returncode, 0, r.stderr); self.assertIn(want, r.stdout)
            with open(os.path.join(desk, "pool.csv"), newline="") as f:
                rows = list(csv.DictReader(f))
            self.assertEqual([r["video_id"] for r in rows], ["1", "2", "3"])
            self.assertEqual(rows[0]["note"], "n1, q")


if __name__ == "__main__":
    unittest.main()
