"""HTTP tests for the admin overview (desk_admin_routes.py): queue upload, session actions, 4CAT credentials, exports."""
import http.client, io, json, os, socket, sys, unittest, zipfile
from unittest import mock

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import test_desk_stub as base  # noqa: E402  (module import: StubTest itself must not be re-collected here)
import desk_admin_routes as routes  # noqa: E402  (importable: test_desk_stub put desk/ on sys.path)
from desklib import locks, pool  # noqa: E402
from desklib.platform import load_dir  # noqa: E402

BOUNDARY = "XdeskX"
SAME = {"Sec-Fetch-Site": "same-origin"}


def multipart(name, content: bytes, filename="pool.csv"):
    head = (f'--{BOUNDARY}\r\nContent-Disposition: form-data; name="{name}"; filename="{filename}"\r\n'
            f'Content-Type: text/csv\r\n\r\n').encode()
    return head + content + f"\r\n--{BOUNDARY}--\r\n".encode(), f"multipart/form-data; boundary={BOUNDARY}"


class AdminTest(base.StubHarness):
    def areq(self, method, path, user="alice", admin="1", body=None, ctype=None, headers=SAME, raw=False):
        """A member's request (admin by default) with a CSRF-clean header set; returns (status, body, headers)."""
        h = {"REMOTE_USER": user, "X-Desk-Member": "1", **({"X-Desk-Admin": admin} if admin is not None else {}),
             **headers}
        if ctype:
            h["Content-Type"] = ctype
        c = http.client.HTTPConnection("127.0.0.1", self.port, timeout=10)
        c.request(method, path, body=body, headers=h)
        r = c.getresponse(); data = r.read(); c.close()
        return r.status, (data if raw else data.decode()), {k.lower(): v for k, v in r.getheaders()}

    def members(self, mapping):
        self.desk_obj().sessions.members = lambda: mapping

    def upload(self, content: bytes, **kw):
        """POST the file; assert the 303 back to the overview and return (status, overview body, headers)."""
        body, ctype = multipart("file", content)
        s, _, h = self.areq("POST", "/desk/admin/pool", body=body, ctype=ctype, **kw)
        self.assertEqual((s, h["location"]), (303, "/desk/admin/"))
        return self.areq("GET", "/desk/admin/", **kw)

    def test_admin_page_sections_and_expected_vs_registered(self):
        self.members({"alice": True, "bob": False, "carol": False})
        s, body, _ = self.areq("GET", "/desk/admin/")
        self.assertEqual(s, 200)
        for section in ("Queue", "Sessions", "4CAT", "Export", "Problems"):
            self.assertIn(f"<h2>{section}</h2>", body)
        self.assertIn("expected, not yet visited", body)
        expected = body.split("expected, not yet visited", 1)[1]
        self.assertIn("carol", expected); self.assertNotIn("bob", expected.split("</ul>")[0])
        self.assertIn("/desk/admin/vnc/alice/", body)
        self.assertIn("op=stop&amp;user=alice", body)

    def test_expected_list_survives_co_api_failure(self):
        def boom():
            raise OSError("no route")
        self.desk_obj().sessions.members = boom
        s, body, _ = self.areq("GET", "/desk/admin/")
        self.assertEqual(s, 200); self.assertIn("could not read the CO member list", body)

    def test_pool_upload_reports_rejects(self):
        csv_text = "﻿Video ID;Bucket;Note\r\n9001;a;one\r\n9002;a;two\r\nabc;a;bad\r\n9001;b;dup\r\n"
        before = len(pool.read_rows(self.root, "video"))
        s, body, _ = self.upload(csv_text.encode("utf-8"))
        self.assertEqual(s, 200)
        self.assertIn("added 2 of 4; rejected lines: 4: id must match ^[0-9]+$", body)
        rows = pool.read_rows(self.root, "video")
        self.assertEqual(len(rows), before + 2)
        self.assertEqual([(r.id, r.kind, r.bucket, r.note) for r in rows[-2:]],
                         [("9001", "video", "a", "one"), ("9002", "video", "a", "two")])
        s, body, _ = self.upload(b"id,kind\n9001,video\n7,x\n")     # a duplicate adds nothing; unknown kind rejected
        self.assertIn("added 0 of 2; rejected lines: 3: unknown kind &#x27;x&#x27;", body)
        s, body, _ = self.upload(b"id\n9100\n")
        self.assertIn("added 1 of 1; no rejects", body)

    def test_pool_upload_file_level_error_imports_nothing(self):
        before = [r.id for r in pool.read_rows(self.root, "video")]
        s, body, _ = self.upload(b'id,note\n9301,ok\n9302,"unfinished\n9303,valid\n')
        self.assertEqual(s, 200)
        self.assertIn("nothing added: line 3: a quoted field runs over several lines", body)
        self.assertEqual([r.id for r in pool.read_rows(self.root, "video")], before)

    def test_pool_upload_limits_and_form_errors(self):
        body, ctype = multipart("file", b"x" * (5 * 1024 * 1024 + 1))
        self.assertEqual(self.areq("POST", "/desk/admin/pool", body=body, ctype=ctype)[0], 413)
        body, ctype = multipart("other", b"id\n1\n")
        self.assertEqual(self.areq("POST", "/desk/admin/pool", body=body, ctype=ctype)[0], 400)
        self.assertEqual(self.areq("POST", "/desk/admin/pool", body=b"id=1", ctype="text/plain")[0], 400)

    def test_session_actions_call_sessions(self):
        sess = self.desk_obj().sessions
        with mock.patch.object(sess, "reconcile", return_value={"retired": ["x"], "failed": {"y": "boom"}}) as rec, \
                mock.patch.object(sess, "prune", return_value=["z"]) as pr, \
                mock.patch.object(sess, "stop") as st, \
                mock.patch.object(sess, "retire", return_value=None) as ret, \
                mock.patch.object(sess, "repair", return_value=mock.Mock(ready=lambda: True)) as rep:
            for op in ("reconcile", "prune", "stop", "retire", "repair"):
                s, _, h = self.areq("POST", f"/desk/admin/sessions?op={op}&user=bob")
                self.assertEqual((s, h["location"]), (303, "/desk/admin/"), op)
            rec.assert_called_once_with(); pr.assert_called_once_with()
            st.assert_called_once_with("bob"); ret.assert_called_once_with("bob"); rep.assert_called_once_with("bob")
            self.assertEqual(self.areq("POST", "/desk/admin/sessions?op=explode&user=bob")[0], 400)
            for op in ("stop", "retire", "repair"):
                self.assertEqual(self.areq("POST", f"/desk/admin/sessions?op={op}")[0], 400, op)
                self.assertEqual(self.areq("POST", f"/desk/admin/sessions?op={op}&user=..%2Fx")[0], 400, op)
            self.assertEqual(st.call_count, 1)
        self.assertEqual(self.areq("GET", "/desk/admin/sessions?op=prune")[0], 404)

    def test_session_result_is_shown_once(self):
        sess = self.desk_obj().sessions
        with mock.patch.object(sess, "retire", return_value="docker timed out"):
            self.areq("POST", "/desk/admin/sessions?op=retire&user=bob")
        first = self.areq("GET", "/desk/admin/")[1]
        self.assertIn("bob: retire failed: docker timed out", first)
        self.assertNotIn("retire failed", self.areq("GET", "/desk/admin/")[1])

    def test_own_password_only_and_no_store(self):
        fc = self.desk_obj().sessions.fourcat
        for u, pw in (("alice", "alice-secret-pw"), ("bob", "bob-secret-pw")):
            base.put(fc.password_path(u), pw)
        s, body, h = self.areq("GET", "/desk/admin/4cat/password")
        self.assertEqual(s, 200); self.assertIn("alice-secret-pw", body); self.assertNotIn("bob-secret-pw", body)
        self.assertEqual(h["cache-control"], "no-store")
        for path in ("/desk/admin/", "/desk/admin/problems"):   # the password is rendered nowhere else
            self.assertNotIn("alice-secret-pw", self.areq("GET", path)[1])
        s, body, _ = self.areq("GET", "/desk/admin/4cat/password?user=bob")
        self.assertNotIn("bob-secret-pw", body)
        os.remove(fc.password_path("alice"))
        self.assertEqual(self.areq("GET", "/desk/admin/4cat/password")[0], 404)
        s, _, h = self.areq("POST", "/desk/admin/4cat/password")   # reissue: redirect, so a refresh cannot re-rotate
        self.assertEqual((s, h["location"]), (303, "/desk/admin/4cat/password"))
        s, body, h = self.areq("GET", h["location"])
        new = base.slurp(fc.password_path("alice"))
        self.assertEqual(s, 200); self.assertIn(new, body); self.assertEqual(h["cache-control"], "no-store")
        self.serve(fourcat_enabled=False)
        for m, p in (("GET", "/desk/admin/4cat/password"), ("POST", "/desk/admin/4cat/password"),
                     ("POST", "/desk/admin/4cat/rotate?user=bob")):
            s, body, _ = self.areq(m, p)
            self.assertEqual(s, 404, p); self.assertIn("4CAT is not enabled on this desk", body)

    def test_rotate_other_user_as_admin(self):
        fc = self.desk_obj().sessions.fourcat
        base.put(fc.password_path("bob"), "old-bob-pw")
        s, body, h = self.areq("POST", "/desk/admin/4cat/rotate?user=bob")
        self.assertEqual((s, h["location"]), (303, "/desk/admin/"))
        new = base.slurp(fc.password_path("bob"))
        self.assertNotEqual(new, "old-bob-pw")
        page = self.areq("GET", "/desk/admin/")[1]
        self.assertIn("rotated 4CAT credentials for bob", page)
        self.assertNotIn(new, page); self.assertNotIn("old-bob-pw", page)
        self.assertEqual(self.areq("POST", "/desk/admin/4cat/rotate")[0], 400)
        self.assertEqual(self.areq("POST", "/desk/admin/4cat/rotate?user=Bad%20Name")[0], 400)

    def test_rotate_holds_the_target_user_lock(self):
        fc, held = self.desk_obj().sessions.fourcat, []

        def rotate(u):
            try:
                with locks.flocked(locks.user_lock(self.root, u), timeout=0.05):
                    held.append((u, False))
            except TimeoutError:
                held.append((u, True))
        with mock.patch.object(fc, "rotate", rotate):
            self.assertEqual(self.areq("POST", "/desk/admin/4cat/rotate?user=bob")[0], 303)
            self.assertEqual(self.areq("POST", "/desk/admin/4cat/password")[0], 303)   # own reissue
        self.assertEqual(held, [("bob", True), ("alice", True)])

    def test_export_zip_contains_captures_and_log(self):
        base.put(os.path.join(self.root, "state", "captures", "alice", "a.ndjson"), '{"x":1}\n')
        base.put(os.path.join(self.root, "state", "captures", "bob", "sub", "b.ndjson"), '{"y":2}\n')
        self.mem("POST", "/desk/claim")
        s, data, h = self.areq("GET", "/desk/admin/export/captures.zip", raw=True)
        self.assertEqual(s, 200); self.assertEqual(h["content-type"], "application/zip")
        self.assertIn("attachment", h["content-disposition"])
        z = zipfile.ZipFile(io.BytesIO(data))
        self.assertEqual(z.namelist(), ["captures/alice/a.ndjson", "captures/bob/sub/b.ndjson", "desk-log.ndjson"])
        self.assertEqual(z.read("captures/alice/a.ndjson"), b'{"x":1}\n')
        self.assertIn(b'"action": "claim"', z.read("desk-log.ndjson"))
        s, data, h = self.areq("GET", "/desk/admin/export/desk-log.ndjson", raw=True)
        self.assertEqual((s, h["content-type"]), (200, "application/x-ndjson"))
        self.assertIn("attachment", h["content-disposition"]); self.assertIn(b'"action": "claim"', data)

    def test_problems_list_newest_first(self):
        d = self.desk_obj()
        for t, code, note in ((1000, "U", ""), (3000, "X", "late <b>one</b>"), (2000, "U", "")):
            d.append({"t": t, "user": "bob", "action": "problem", "kind": "video", "id": str(t), "code": code,
                      "note": note})
        d.append({"t": 4000, "user": "bob", "action": "open", "kind": "video", "id": "4000", "code": "", "exec_rc": 0})
        s, body, _ = self.areq("GET", "/desk/admin/problems")
        self.assertEqual(s, 200)
        self.assertLess(body.index("3000"), body.index("2000")); self.assertLess(body.index("2000"), body.index("1000"))
        self.assertNotIn("4000", body)
        self.assertIn("late &lt;b&gt;one&lt;/b&gt;", body)

    def test_non_admin_cannot_reach_any_admin_route(self):
        routes = [("GET", "/desk/admin/"), ("POST", "/desk/admin/pool"), ("POST", "/desk/admin/sessions?op=prune"),
                  ("GET", "/desk/admin/4cat/password"), ("POST", "/desk/admin/4cat/password"),
                  ("POST", "/desk/admin/4cat/rotate?user=bob"), ("GET", "/desk/admin/export/captures.zip"),
                  ("GET", "/desk/admin/export/desk-log.ndjson"), ("GET", "/desk/admin/problems")]
        sess = self.desk_obj().sessions
        with mock.patch.object(sess, "prune") as pr:
            for admin in (None, "0"):
                for m, p in routes:
                    self.assertEqual(self.areq(m, p, admin=admin)[0], 403, (m, p, admin))
            pr.assert_not_called()
        self.assertEqual(self.docker_calls(), [])

    def test_admin_posts_refuse_cross_site(self):
        sess = self.desk_obj().sessions
        port = self.port
        with mock.patch.object(sess, "prune", return_value=[]) as pr:
            p = "/desk/admin/sessions?op=prune"
            for hdrs in ({"Sec-Fetch-Site": "cross-site"}, {"Sec-Fetch-Site": "same-site"}, {},
                         {"Origin": "https://evil.example"}, {"Origin": "null"},
                         {"Sec-Fetch-Site": "cross-site", "Origin": f"http://127.0.0.1:{port}"}):
                self.assertEqual(self.areq("POST", p, headers=hdrs)[0], 403, hdrs)
            pr.assert_not_called()
            self.assertEqual(self.areq("POST", p, headers={"Origin": f"http://127.0.0.1:{port}"})[0], 303)
            self.assertEqual(self.areq("POST", p, headers={"Sec-Fetch-Site": "same-origin"})[0], 303)
            self.assertEqual(pr.call_count, 2)
        # a cross-site upload and a cross-site password reissue are refused as well
        body, ctype = multipart("file", b"id\n9500\n")
        self.assertEqual(self.areq("POST", "/desk/admin/pool", body=body, ctype=ctype,
                                   headers={"Sec-Fetch-Site": "cross-site"})[0], 403)
        self.assertEqual(self.areq("POST", "/desk/admin/4cat/password", headers={})[0], 403)
        self.assertNotIn("9500", [r.id for r in pool.read_rows(self.root, "video")])


    def raw_post(self, path, body: bytes, extra=b""):
        """One POST on a raw socket; returns everything the server sent before closing (or after 3 s)."""
        s = socket.create_connection(("127.0.0.1", self.port), timeout=3)
        try:
            s.sendall(b"POST " + path.encode() + b" HTTP/1.1\r\nHost: 127.0.0.1\r\nREMOTE_USER: alice\r\n"
                      b"X-Desk-Member: 1\r\nX-Desk-Admin: 1\r\n" + extra +
                      b"Content-Length: " + str(len(body)).encode() + b"\r\n\r\n" + body)
            out, closed = b"", False
            try:
                while chunk := s.recv(65536):
                    out += chunk
                closed = True                      # EOF: the server closed the connection
            except socket.timeout:
                pass
            return out, closed
        finally:
            s.close()

    def test_refused_post_with_a_body_closes_the_connection(self):
        # As if the stub spoke HTTP/1.1 keep-alive (it speaks 1.0 today): the unread body must die
        # with the connection, never be parsed as a second, forged request.
        self.srv.RequestHandlerClass.protocol_version = "HTTP/1.1"
        smuggled = (b"GET /desk/admin/ HTTP/1.1\r\nREMOTE_USER: x\r\nX-Desk-Member: 1\r\nX-Desk-Admin: 1\r\n"
                    b"Sec-Fetch-Site: same-origin\r\n\r\n")
        for path, extra in (("/desk/admin/sessions?op=prune", b"Sec-Fetch-Site: cross-site\r\n"),   # CSRF 403
                            ("/desk/admin/sessions?op=explode", b"Sec-Fetch-Site: same-origin\r\n"),  # 400
                            ("/desk/admin/nope", b"Sec-Fetch-Site: same-origin\r\n")):               # 404
            out, closed = self.raw_post(path, smuggled, extra)
            self.assertTrue(closed, path)
            self.assertEqual(out.count(b"HTTP/1.1 "), 1, out)        # one response, not two
            self.assertIn(b"\r\nConnection: close\r\n", out)
        self.assertNotIn("x", self.desk_obj().admin_flags)            # the forged request never ran

    def test_failed_stop_is_reported_and_keeps_last_seen(self):
        d = self.desk_obj()
        base.put(os.path.join(self.state, "desk-firefox-bob"), "")
        d.last_seen["bob"] = 123
        base.put(self.mode, "fail")                                   # docker stop exits 1
        self.areq("POST", "/desk/admin/sessions?op=stop&user=bob")
        body = self.areq("GET", "/desk/admin/")[1]
        self.assertEqual(body.split('role="status">', 1)[1].split("</p>", 1)[0], "could not stop bob")
        self.assertEqual(d.last_seen["bob"], 123)
        base.put(self.mode, "")
        self.areq("POST", "/desk/admin/sessions?op=stop&user=bob")
        self.assertIn("stopped bob", self.areq("GET", "/desk/admin/")[1])
        self.assertNotIn("bob", d.last_seen)

    def test_malformed_multipart_is_400(self):
        for ctype in ("multipart/form-data", "multipart/form-data; boundary=", "multipart/form-data; boundary=zz"):
            s, body, _ = self.areq("POST", "/desk/admin/pool", body=b"\x00garbage\r\n--x\r\n", ctype=ctype)
            self.assertEqual(s, 400, ctype); self.assertIn("no file field in the form", body)

    def test_csrf_refusal_reads_no_body(self):
        h = FakeUploadHandler({"Sec-Fetch-Site": "cross-site", "Content-Length": "10",
                               "Content-Type": "multipart/form-data; boundary=x"})
        self.assertTrue(routes.handle_admin(h, None, "alice", "POST", "/desk/admin/pool", {}))
        self.assertEqual((h.sent[0], h.rfile.reads, h.close_connection), (403, [], True))

    def notice_after(self, op, **result):
        sess = self.desk_obj().sessions
        with mock.patch.object(sess, op, **result):
            self.areq("POST", f"/desk/admin/sessions?op={op}&user=bob")
        body = self.areq("GET", "/desk/admin/")[1]
        return body.split('role="status">', 1)[1].split("</p>", 1)[0]

    def test_session_notice_texts(self):
        self.assertEqual(self.notice_after("reconcile", return_value={"retired": ["x"], "admin_set": ["a"]}),
                         "reconcile: retired: x; admin set: a; admin cleared: -")
        self.assertEqual(self.notice_after("reconcile", return_value={"retired": [], "admin_cleared": ["c"],
                                                                      "skipped": ["s"], "failed": {"y": "boom"}}),
                         "reconcile: retired: -; admin set: -; admin cleared: c; "
                         "skipped (no 4CAT account yet): s; y failed: boom")
        self.assertEqual(self.notice_after("reconcile", return_value={}),
                         "reconcile: retired: -; admin set: -; admin cleared: -")
        self.assertEqual(self.notice_after("prune", return_value=["z", "w"]), "prune: retired z w")
        self.assertEqual(self.notice_after("repair", return_value=mock.Mock(ready=lambda: False,
                                                                         current=lambda: "fourcat_account",
                                                                         error=lambda: "")),
                         "bob: stuck at fourcat_account: incomplete")
        self.assertEqual(self.notice_after("repair", return_value=mock.Mock(ready=lambda: True)), "bob: ready")
        self.assertEqual(self.notice_after("stop"), "stopped bob")
        self.assertEqual(self.notice_after("retire", side_effect=RuntimeError("no members")),
                         "retire bob failed: RuntimeError: no members")

    def test_notice_goes_to_the_acting_admin_only(self):
        sess = self.desk_obj().sessions
        with mock.patch.object(sess, "stop"):
            self.areq("POST", "/desk/admin/sessions?op=stop&user=bob", user="alice")
        self.assertNotIn("stopped bob", self.areq("GET", "/desk/admin/", user="bob")[1])
        self.assertIn("stopped bob", self.areq("GET", "/desk/admin/", user="alice")[1])

    def test_admin_responses_forbid_framing(self):
        base.put(os.path.join(self.root, "state", "captures", "a.ndjson"), "x\n")
        for path in ("/desk/admin/", "/desk/admin/problems", "/desk/admin/export/captures.zip",
                     "/desk/admin/export/desk-log.ndjson", "/desk/admin/4cat/password"):
            h = self.areq("GET", path, raw=True)[2]
            self.assertEqual(h["x-frame-options"], "DENY", path)
            self.assertEqual(h["content-security-policy"], "frame-ancestors 'none'", path)
        self.assertEqual(self.areq("POST", "/desk/admin/sessions?op=explode")[2]["x-frame-options"], "DENY")  # a 400

    @unittest.skipIf(os.geteuid() == 0, "root reads any file")
    def test_export_failure_is_a_500_not_a_dropped_connection(self):
        f = os.path.join(self.root, "state", "captures", "alice", "locked.ndjson")
        base.put(f, "secret\n"); os.chmod(f, 0)
        self.addCleanup(os.chmod, f, 0o600)
        s, body, h = self.areq("GET", "/desk/admin/export/captures.zip")
        self.assertEqual(s, 500); self.assertIn("export failed", body)
        self.assertIn("state/captures/alice/locked.ndjson", body); self.assertNotIn(self.root, body)
        self.assertEqual(h["x-frame-options"], "DENY")


class FakeRfile:
    def __init__(self, exc=None):
        self.reads, self.exc = [], exc

    def read(self, n=-1):
        self.reads.append(n)
        if self.exc:
            raise self.exc
        return b""


class FakeUploadHandler:
    """Just what handle_admin/read_upload touch on a BaseHTTPRequestHandler."""
    def __init__(self, headers, exc=None):
        self.headers, self.rfile, self.close_connection, self.sent = headers, FakeRfile(exc), False, None
        self.timeouts = []
        self.connection = mock.Mock(settimeout=self.timeouts.append)

    def _send(self, status, body="", ctype="", headers=()):
        self.sent = (status, body)


class UploadUnitTest(unittest.TestCase):
    def test_stalled_upload_is_408_and_closes(self):
        h = FakeUploadHandler({"Content-Length": "100", "Content-Type": "multipart/form-data; boundary=x"},
                              exc=socket.timeout("timed out"))
        self.assertEqual(routes.read_upload(h), (408, "upload timed out"))
        self.assertEqual(h.timeouts, [30])                 # set before the read
        self.assertEqual(h.rfile.reads, [100])
        self.assertTrue(h.close_connection)


class PoolUnitTest(unittest.TestCase):
    def setUp(self):
        self.platform = load_dir(os.path.join(base.REPO, "platforms"), "tiktok")

    def test_parse_selection_header_aliases_and_line_numbers(self):
        text = "﻿video-id , Kind\r\n\r\n12,video\r\n,video\r\nzz,video\r\n5,nope\r\n"
        rows, rejects = pool.parse_selection(text, self.platform)
        self.assertEqual([(r.id, r.kind, r.bucket, r.note) for r in rows], [("12", "video", "", "")])
        self.assertEqual(rejects, [(4, "id is empty"), (5, "id must match ^[0-9]+$"), (6, "unknown kind 'nope'")])
        with self.assertRaises(pool.SelectionError) as cm:
            pool.parse_selection("name\nx\n", self.platform)
        self.assertEqual((cm.exception.line, cm.exception.reason), (1, "header has no id column"))

    def test_add_rows_replaces_atomically_and_ends_with_newline(self):
        import tempfile
        with tempfile.TemporaryDirectory() as root:
            path = os.path.join(root, "pool.csv")
            with open(path, "w", newline="") as f:
                f.write("video_id,bucket,note\r\n11,a,x")          # legacy header, no final newline
            before = os.stat(path).st_ino
            rows, _ = pool.parse_selection("id\n22\n", self.platform)
            self.assertEqual(pool.add_rows(root, rows, self.platform), (1, 2))  # the unterminated "11" row is kept
            self.assertNotEqual(os.stat(path).st_ino, before)       # a new file was swapped in, not appended to
            with open(path, "rb") as f:
                data = f.read()
            self.assertTrue(data.endswith(b"\n")); self.assertTrue(data.startswith(b"id,kind,bucket,note"))
            self.assertEqual([r.id for r in pool.read_rows(root, "video")], ["11", "22"])
            self.assertEqual([n for n in os.listdir(root) if n.startswith(".pool.csv.")], [])

    def test_read_keeps_a_last_line_without_newline(self):
        import tempfile
        with tempfile.TemporaryDirectory() as root:   # writes are whole-file swaps: this is a complete row
            with open(os.path.join(root, "pool.csv"), "w", newline="") as f:
                f.write("id,kind,bucket,note\r\n9001234567,video,,\r\n90")
            self.assertEqual([r.id for r in pool.read_rows(root, "video")], ["9001234567", "90"])

    def test_add_rows_creates_file_with_header_and_dedupes(self):
        import tempfile
        with tempfile.TemporaryDirectory() as root:
            rows, _ = pool.parse_selection("id,bucket,note\n1,a,x\n2,b,y\n1,c,z\n", self.platform)
            self.assertEqual(pool.add_rows(root, rows, self.platform), (2, 2))
            self.assertEqual(pool.add_rows(root, rows, self.platform), (0, 2))
            with open(os.path.join(root, "pool.csv"), encoding="utf-8") as f:
                self.assertEqual(f.read().splitlines()[0], "id,kind,bucket,note")


if __name__ == "__main__":
    unittest.main()
