"""Tests for desk/desklib/capture_rules.py: the proxy's decisions and the safety-copy records."""
import dataclasses, json, os, re, sys, tempfile, unittest

REPO = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
sys.path.insert(0, os.path.join(REPO, "desk"))
from desklib import capture_rules as cr  # noqa: E402
from desklib import platform as pf  # noqa: E402

PLATFORMS = os.path.join(REPO, "platforms")
FIXTURES = os.path.join(PLATFORMS, "tiktok", "fixtures")
D = cr.Decision
CTX = {"t": 1700000000, "user": "u1", "session": "s1", "platform": "tiktok"}


def fixture(name):
    with open(os.path.join(FIXTURES, name), "rb") as f:
        return f.read()


class CaptureTests(unittest.TestCase):
    def setUp(self):
        self.p = pf.load_dir(PLATFORMS, "tiktok")
        self.rule = self.p.captures[0]
        self.request = json.loads(fixture("comments-list.request.json"))

    def test_decide_fourcat_allowed_and_forbidden(self):
        dec = lambda m, path, en=True: cr.decide(self.p, "fourcat.desk", m, path, en)[0]
        self.assertEqual(dec("POST", "/api/import-dataset/"), D.FOURCAT)
        self.assertEqual(dec("GET", "/api/check-query/"), D.FOURCAT)
        self.assertEqual(dec("GET", "/"), D.FORBID)
        self.assertEqual(dec("GET", "/api/import-dataset/"), D.FORBID)
        self.assertEqual(dec("POST", "/api/import-dataset/../x"), D.FORBID)
        self.assertEqual(dec("POST", "/api/import-dataset"), D.FORBID)
        self.assertEqual(dec("POST", "/api/import-dataset/", False), D.FORBID)
        self.assertEqual(dec("GET", "/api/check-query/", False), D.FORBID)
        self.assertEqual(cr.decide(self.p, "FourCat.Desk:80", "POST", "/api/import-dataset/", True)[0], D.FOURCAT)

    def test_decide_refuses_internal_hosts(self):
        for h in ["localhost", "127.0.0.1", "172.17.0.1:8080", "frontend", "[::1]", "other.desk",
                  "LOCALHOST.", "fourcat.desk.evil.desk", "127.1", "2130706433", "::1", "[::1]:5000",
                  "foo.localhost", "sub.fourcat.desk"]:
            self.assertEqual(cr.decide(self.p, h, "GET", "/", True)[0], D.REFUSE, h)
        self.assertEqual(cr.decide(self.p, "www.tiktok.com", "GET", "/", True)[0], D.PASS)
        self.assertEqual(cr.decide(self.p, "checkjefeedzee.onlinehate.src.surf-hosted.nl", "GET", "/", True)[0], D.PASS)

    def test_decide_capture_rule_on_platform_host(self):
        d, rule = cr.decide(self.p, "www.tiktok.com", "GET", "/api/comment/list/", True)
        self.assertEqual((d, rule.name), (D.CAPTURE, "comments"))
        d, rule = cr.decide(self.p, "www.tiktok.com:443", "GET", "/api/comment/list/reply/", True)
        self.assertEqual((d, rule.name), (D.CAPTURE, "comments"))
        self.assertEqual(cr.decide(self.p, "www.tiktok.com", "GET", "/api/other", True), (D.PASS, None))
        self.assertEqual(cr.decide(self.p, "evil.com", "GET", "/api/comment/list", True), (D.PASS, None))

    def test_extract_items_with_envelope_and_fields(self):
        recs = cr.extract(self.rule, CTX, self.request, 200, fixture("comments-list.json"))
        self.assertEqual(len(recs), 2)
        r = recs[0]
        for k, v in CTX.items():
            self.assertEqual(r[k], v)
        self.assertEqual(r["platform"], "tiktok")
        self.assertEqual(r["capture"], "comments")
        self.assertIn("kind", r)
        self.assertIsNone(r["kind"])
        self.assertEqual(r["post_id"], "7001")
        self.assertEqual(r["request"]["query"], {"aweme_id": "7001", "cursor": "0", "count": "20"})
        self.assertEqual(r["request"]["method"], "GET")
        self.assertEqual(r["request"]["host"], "www.tiktok.com")
        self.assertEqual(r["request"]["path"], "/api/comment/list/")
        self.assertEqual(r["response"], {"status": 200, "has_more": 1, "cursor": 20})
        self.assertIsInstance(r["item"], dict)
        self.assertEqual(r["text"], "comment one")
        self.assertEqual(r["author"], "user_a")
        self.assertEqual(r["id"], "1001")
        self.assertEqual(recs[1]["id"], "1002")
        self.assertEqual(recs[1]["author_uid"], "2002")

    def test_extract_raw_rule_stores_body(self):
        raw = dataclasses.replace(self.rule, items=None, id=None)
        recs = cr.extract(raw, CTX, self.request, 200, fixture("comments-list.json"))
        self.assertEqual(len(recs), 1)
        self.assertEqual(recs[0]["body"], json.loads(fixture("comments-list.json")))
        self.assertNotIn("id", recs[0])
        self.assertNotIn("item", recs[0])
        self.assertEqual(recs[0]["post_id"], "7001")

    def test_extract_body_too_large_is_error_record(self):
        small = dataclasses.replace(self.rule, max_body_bytes=10)
        body = fixture("comments-list.json")
        recs = cr.extract(small, CTX, self.request, 200, body)
        self.assertEqual(len(recs), 1)
        self.assertEqual(recs[0]["error"], "body_too_large")
        self.assertEqual(recs[0]["size"], len(body))
        self.assertEqual(recs[0]["capture"], "comments")
        self.assertEqual(recs[0]["response"]["status"], 200)
        self.assertNotIn("item", recs[0])

    def test_extract_bad_json_is_error_record(self):
        recs = cr.extract(self.rule, CTX, self.request, 200, b"<html>")
        self.assertEqual(len(recs), 1)
        self.assertEqual(recs[0]["error"], "json")
        self.assertEqual(recs[0]["user"], "u1")

    def test_post_id_sources(self):
        q = dataclasses.replace(self.rule, post_id={"from": "query", "param": "aweme_id"})
        self.assertEqual(cr.post_id_for(q, self.request, None), "7001")
        self.assertIsNone(cr.post_id_for(q, {"query": {}, "path": "/x"}, None))
        pth = dataclasses.replace(self.rule, path=re.compile(r"^/api/v1/media/(?P<post>[0-9]+)/comments/"),
                                  post_id={"from": "path", "group": "post"})
        self.assertEqual(cr.post_id_for(pth, {"path": "/api/v1/media/99/comments/", "query": {}}, None), "99")
        self.assertIsNone(cr.post_id_for(pth, {"path": "/elsewhere", "query": {}}, None))
        body = dataclasses.replace(self.rule, post_id={"from": "body", "field": "data.id"})
        self.assertEqual(cr.post_id_for(body, self.request, {"data": {"id": 5}}), "5")
        self.assertIsNone(cr.post_id_for(body, self.request, None))
        none = dataclasses.replace(self.rule, post_id=None)
        self.assertIsNone(cr.post_id_for(none, self.request, {}))

    def test_dotted(self):
        self.assertEqual(cr.dotted({"user": {"unique_id": "a"}}, "user.unique_id"), "a")
        self.assertIsNone(cr.dotted({"user": None}, "user.unique_id"))
        self.assertIsNone(cr.dotted({"user": "x"}, "user.unique_id"))
        self.assertEqual(cr.dotted({"n": 0}, "n"), 0)

    def test_page_open(self):
        html = "text/html; charset=utf-8"
        self.assertEqual(cr.page_open(self.p, "GET", "/@a/video/55", 200, html), {"kind": "video", "id": "55"})
        self.assertEqual(cr.page_open(self.p, "GET", "/@a/photo/56", 200, html), {"kind": "photo", "id": "56"})
        self.assertIsNone(cr.page_open(self.p, "GET", "/@a/video/55", 200, "application/json"))
        self.assertIsNone(cr.page_open(self.p, "GET", "/api/x", 200, html))
        self.assertIsNone(cr.page_open(self.p, "POST", "/@a/video/55", 200, html))

    def test_read_token_follows_mtime(self):
        cache = {}
        with tempfile.TemporaryDirectory() as tmp:
            path = os.path.join(tmp, "token")
            self.assertIsNone(cr.read_token(path, cache))
            with open(path, "w") as f:
                f.write("abc\n")
            os.utime(path, (1000, 1000))
            self.assertEqual(cr.read_token(path, cache), "abc")
            with open(path, "w") as f:
                f.write("def\n")
            os.utime(path, (2000, 2000))
            self.assertEqual(cr.read_token(path, cache), "def")
            os.remove(path)
            self.assertIsNone(cr.read_token(path, cache))


if __name__ == "__main__":
    unittest.main()
