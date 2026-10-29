"""Tests for desk/desklib/platform.py and the platform definitions under platforms/."""
import glob, json, os, sys, tempfile, unittest

REPO = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
sys.path.insert(0, os.path.join(REPO, "desk"))
from desklib import platform as pf  # noqa: E402

PLATFORMS = os.path.join(REPO, "platforms")


def tiktok_dict():
    with open(os.path.join(PLATFORMS, "tiktok", "platform.json")) as f:
        return json.load(f)


class PlatformTests(unittest.TestCase):
    def setUp(self):
        self.p = pf.load_dir(PLATFORMS, "tiktok")

    def load_dict(self, d):
        with tempfile.TemporaryDirectory() as tmp:
            path = os.path.join(tmp, "platform.json")
            with open(path, "w") as f:
                json.dump(d, f)
            return pf.load(path)

    def test_tiktok_definition_loads(self):
        self.assertEqual(self.p.label, "TikTok")
        self.assertEqual(self.p.default_kind, "video")
        self.assertEqual(len(self.p.captures), 1)
        self.assertEqual(self.p.login, "none")
        self.assertEqual(self.p.captures[0].max_body_bytes, 16 * 1024 * 1024)
        self.assertEqual(self.p.captures[0].response_keep, ["has_more", "cursor", "total", "status_code"])

    def test_response_keep_defaults_and_validates(self):
        d = tiktok_dict()
        del d["captures"][0]["response_keep"]
        self.assertEqual(self.load_dict(d).captures[0].response_keep, ["has_more", "cursor"])
        d["captures"][0]["response_keep"] = "has_more"
        with self.assertRaisesRegex(pf.PlatformError, r"captures\[0\]\.response_keep"):
            self.load_dict(d)
        d["captures"][0]["response_keep"] = ["ok", 3]
        with self.assertRaisesRegex(pf.PlatformError, r"captures\[0\]\.response_keep"):
            self.load_dict(d)

    def test_item_url_and_id_validation(self):
        self.assertEqual(pf.item_url(self.p, "video", "123"), "https://www.tiktok.com/@x/video/123/")
        with self.assertRaises(ValueError):
            pf.item_url(self.p, "video", "abc")
        with self.assertRaises(ValueError):
            pf.item_url(self.p, "post", "123")

    def test_problems_substitute_contact(self):
        ps = pf.problems(self.p, "Jo")
        self.assertTrue(ps[5].label.endswith("tell Jo"))
        self.assertEqual(ps[6].code, "X")
        self.assertTrue(ps[6].note)
        self.assertFalse(ps[0].note)

    def test_host_matches_subdomains_only(self):
        for h in ("tiktok.com", "www.tiktok.com", "m.tiktok.com"):
            self.assertTrue(pf.host_matches(self.p, h), h)
        for h in ("tiktok.com.evil", "nottiktok.com"):
            self.assertFalse(pf.host_matches(self.p, h), h)

    def test_invalid_definition_names_the_key(self):
        d = tiktok_dict(); del d["default_kind"]
        with self.assertRaisesRegex(pf.PlatformError, "default_kind"):
            self.load_dict(d)
        d = tiktok_dict(); d["default_kind"] = "post"
        with self.assertRaisesRegex(pf.PlatformError, "default_kind"):
            self.load_dict(d)
        d = tiktok_dict(); d["login"] = "sometimes"
        with self.assertRaisesRegex(pf.PlatformError, "login"):
            self.load_dict(d)
        d = tiktok_dict(); del d["captures"][0]["id"]
        with self.assertRaisesRegex(pf.PlatformError, r"captures\[0\]\.id"):
            self.load_dict(d)
        d = tiktok_dict(); d["problems"].append({"code": "U", "label": "again"})
        with self.assertRaisesRegex(pf.PlatformError, "problems"):
            self.load_dict(d)
        d = tiktok_dict(); d["captures"][0]["post_id"] = {"from": "query"}
        with self.assertRaisesRegex(pf.PlatformError, r"post_id"):
            self.load_dict(d)
        d = tiktok_dict(); d["captures"][0]["post_id"] = {"from": "cookie", "param": "x"}
        with self.assertRaisesRegex(pf.PlatformError, r"post_id"):
            self.load_dict(d)

    def test_every_definition_in_repo_is_valid(self):
        paths = glob.glob(os.path.join(PLATFORMS, "*", "platform.json"))
        self.assertTrue(paths)
        for path in paths:
            with self.subTest(path=path):
                pf.load(path)


if __name__ == "__main__":
    unittest.main()
