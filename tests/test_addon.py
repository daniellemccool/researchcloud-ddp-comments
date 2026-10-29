"""Tests for desk/mitm/addons/capture.py: the hooks, driven with fake flows behind a stub mitmproxy package."""
import importlib.util, json, os, shutil, sys, tempfile, types, unittest

REPO = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
sys.path.insert(0, os.path.join(REPO, "desk"))
from desklib import capture_rules as cr  # noqa: E402

ADDON = os.path.join(REPO, "desk", "mitm", "addons", "capture.py")
TIKTOK = os.path.join(REPO, "platforms", "tiktok")
ENV_KEYS = ["DESK_USER", "DESK_SESSION", "DESK_CAPTURES", "DESK_FOURCAT_ENABLED", "DESK_FOURCAT_UPSTREAM",
            "DESK_FOURCAT_TOKEN_FILE", "DESK_PLATFORM_JSON"]


class Headers:
    """Case-insensitive, just enough of mitmproxy's Headers."""
    def __init__(self, items=None):
        self._d = {}
        for k, v in (items or {}).items():
            self[k] = v

    def __setitem__(self, k, v): self._d[k.lower()] = (k, v)
    def __getitem__(self, k): return self._d[k.lower()][1]
    def __delitem__(self, k): del self._d[k.lower()]
    def __contains__(self, k): return k.lower() in self._d
    def get(self, k, default=None): return self._d[k.lower()][1] if k.lower() in self._d else default

    def pop(self, k, *default):
        if k.lower() in self._d:
            return self._d.pop(k.lower())[1]
        if default:
            return default[0]
        raise KeyError(k)


class FakeResponse:
    def __init__(self, status_code=200, content=b"", headers=None):
        self.status_code, self.content, self.headers = status_code, content, Headers(headers)
        self.raw_content, self.stream = content, True

    @classmethod
    def make(cls, status=200, content=b"", headers=None):
        return cls(status, content, headers)


class Query:
    def __init__(self, d): self.d = d
    def items(self): return list(self.d.items())


class Req:
    def __init__(self, host, path, method="GET", pretty_host=None, headers=None, scheme="https", port=443, query=None):
        self.host, self.path, self.method, self.scheme, self.port = host, path, method, scheme, port
        self.pretty_host = pretty_host or host
        self.headers = Headers(headers)
        self.query = Query(query or {})


class Flow:
    def __init__(self, req, response=None):
        self.request, self.response, self.metadata = req, response, {}


class Recorder:
    def __init__(self): self.errors, self.shutdowns = [], 0
    def error(self, msg): self.errors.append(msg)
    def shutdown(self): self.shutdowns += 1


def install_stub():
    rec = Recorder()
    mitm = types.ModuleType("mitmproxy")
    http = types.ModuleType("mitmproxy.http")
    http.Response, http.HTTPFlow = FakeResponse, Flow
    ctx = types.ModuleType("mitmproxy.ctx")
    ctx.log = rec
    ctx.master = rec
    mitm.http, mitm.ctx = http, ctx
    sys.modules.update({"mitmproxy": mitm, "mitmproxy.http": http, "mitmproxy.ctx": ctx})
    return rec


def load_addon(env):
    for k in ENV_KEYS:
        os.environ.pop(k, None)
    os.environ.update(env)
    spec = importlib.util.spec_from_file_location("desk_capture_addon", ADDON)
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
    return mod


class AddonTests(unittest.TestCase):
    def setUp(self):
        self._saved = {k: os.environ.get(k) for k in ENV_KEYS}
        self._mods = {k: sys.modules.get(k) for k in ("mitmproxy", "mitmproxy.http", "mitmproxy.ctx")}
        self.rec = install_stub()
        self.tmp = tempfile.mkdtemp()
        self.addCleanup(shutil.rmtree, self.tmp, True)
        self.captures = os.path.join(self.tmp, "captures")
        self.pjson = os.path.join(self.tmp, "platform.json")
        shutil.copy(os.path.join(TIKTOK, "platform.json"), self.pjson)
        self.token = os.path.join(self.tmp, "token")
        with open(self.token, "w") as f:
            f.write("tok123\n")
        self.fake_resolver_internal = set()
        self._orig = cr.resolves_internal
        cr.resolves_internal = lambda host, getaddrinfo=None: cr.normalise_host(host) in self.fake_resolver_internal
        cr._resolve_cache.clear()
        self.addCleanup(self._restore)
        self.mod = self.load()
        self.addon = self.mod.addons[0]

    def _restore(self):
        cr.resolves_internal = self._orig
        cr._resolve_cache.clear()
        for k, v in self._saved.items():
            os.environ.pop(k, None)
            if v is not None:
                os.environ[k] = v
        for k, v in self._mods.items():
            if v is None:
                sys.modules.pop(k, None)
            else:
                sys.modules[k] = v

    def env(self, **over):
        e = {"DESK_USER": "u1", "DESK_SESSION": "s1", "DESK_CAPTURES": self.captures,
             "DESK_FOURCAT_ENABLED": "1", "DESK_FOURCAT_UPSTREAM": "http://frontend:5000",
             "DESK_FOURCAT_TOKEN_FILE": self.token, "DESK_PLATFORM_JSON": self.pjson}
        e.update(over)
        return {k: v for k, v in e.items() if v is not None}

    def load(self, **over):
        return load_addon(self.env(**over))

    def records(self, name):
        base = os.path.join(self.captures, "u1")
        out = []
        for day in sorted(os.listdir(base)) if os.path.isdir(base) else []:
            p = os.path.join(base, day, name)
            if os.path.exists(p):
                with open(p, encoding="utf-8") as f:
                    out += [json.loads(line) for line in f]
        return out

    def comments_flow(self):
        req = Req("www.tiktok.com", "/api/comment/list/?aweme_id=7&cursor=0&count=20",
                  query={"aweme_id": "7", "cursor": "0", "count": "20"})
        return Flow(req)

    # fourcat -----------------------------------------------------------------------------------------------------

    def test_fourcat_flow_gets_token_cookie_and_host(self):
        req = Req("fourcat.desk", "/api/import-dataset/", "POST", scheme="https", port=443,
                  headers={"Cookie": "sid=client; other=1", "Host": "fourcat.desk"})
        flow = Flow(req)
        self.addon.request(flow)
        self.assertIsNone(flow.response)
        self.assertEqual(req.headers.get("Cookie"), "remember_token=tok123")
        self.assertEqual(req.headers.get("Host"), "fourcat.desk")
        self.assertEqual((req.scheme, req.host, req.port), ("http", "frontend", 5000))

    def test_fourcat_response_loses_set_cookie(self):
        flow = Flow(Req("fourcat.desk", "/api/check-query/"))
        self.addon.request(flow)
        flow.response = FakeResponse(headers={"Set-Cookie": "remember_token=x"})
        self.addon.responseheaders(flow)
        self.assertNotIn("Set-Cookie", flow.response.headers)
        flow.response.headers["set-cookie"] = "again=1"
        self.addon.response(flow)
        self.assertNotIn("Set-Cookie", flow.response.headers)
        self.assertEqual(self.records("fourcat.ndjson")[0]["path"], "/api/check-query/")

    def test_fourcat_root_is_403(self):
        flow = Flow(Req("fourcat.desk", "/"))
        self.addon.request(flow)
        self.assertEqual(flow.response.status_code, 403)
        self.assertEqual(self.records("refused.ndjson")[0]["decision"], "forbid")

    def test_internal_host_is_403(self):
        for h in ("localhost", "frontend", "10.0.0.5", "other.desk"):
            flow = Flow(Req(h, "/"))
            self.addon.request(flow)
            self.assertEqual(flow.response.status_code, 403, h)

    def test_resolved_internal_is_403_and_public_passes(self):
        self.fake_resolver_internal.add("rebind.example.com")
        flow = Flow(Req("rebind.example.com", "/"))
        self.addon.request(flow)
        self.assertEqual(flow.response.status_code, 403)
        self.assertEqual(self.records("refused.ndjson")[-1]["decision"], "resolved-internal")
        ok = Flow(Req("example.com", "/"))
        self.addon.request(ok)
        self.assertIsNone(ok.response)

    def test_host_header_mismatch_is_403(self):
        flow = Flow(Req("www.tiktok.com", "/api/comment/list/", pretty_host="fourcat.desk"))
        self.addon.request(flow)
        self.assertEqual(flow.response.status_code, 403)
        self.assertEqual(self.records("refused.ndjson")[0]["decision"], "host-mismatch")

    def test_exception_in_request_is_403(self):
        flow = Flow(Req("www.tiktok.com", "/x"))
        orig = cr.hosts_agree
        cr.hosts_agree = lambda a, b: 1 / 0
        self.addCleanup(setattr, cr, "hosts_agree", orig)
        self.addon.request(flow)
        self.assertEqual(flow.response.status_code, 403)
        self.assertEqual(self.records("refused.ndjson")[0]["error"], "ZeroDivisionError")

    # capture -----------------------------------------------------------------------------------------------------

    def test_capture_request_asks_for_identity(self):
        flow = self.comments_flow()
        flow.request.headers["Accept-Encoding"] = "gzip, br"
        self.addon.request(flow)
        self.assertIsNone(flow.response)
        self.assertEqual(flow.request.headers.get("Accept-Encoding"), "identity")
        self.assertEqual(flow.metadata["desk"][0], "capture")

    def capture_roundtrip(self, body, headers=None):
        flow = self.comments_flow()
        self.addon.request(flow)
        flow.response = FakeResponse(content=body, headers=headers or {})
        self.addon.responseheaders(flow)
        self.addon.response(flow)
        return flow

    def test_capture_flow_lands_records(self):
        with open(os.path.join(TIKTOK, "fixtures", "comments-list.json"), "rb") as f:
            body = f.read()
        flow = self.capture_roundtrip(body, {"Content-Length": str(len(body))})
        self.assertFalse(flow.response.stream)
        recs = self.records("comments.ndjson")
        self.assertEqual(len(recs), 2)
        self.assertEqual((recs[0]["user"], recs[0]["session"], recs[0]["post_id"]), ("u1", "s1", "7"))
        day_dirs = os.listdir(os.path.join(self.captures, "u1"))
        self.assertEqual(len(day_dirs), 1)
        self.assertTrue(os.path.exists(os.path.join(self.captures, "u1", day_dirs[0], "comments.ndjson")))

    def test_oversize_content_length_records_and_keeps_streaming(self):
        rule = self.mod.PLATFORM.captures[0]
        flow = self.capture_roundtrip(b"{}", {"Content-Length": str(rule.max_body_bytes + 1)})
        self.assertTrue(flow.response.stream)
        recs = self.records("comments.ndjson")
        self.assertEqual([r["error"] for r in recs], ["body_too_large"])
        self.assertEqual(recs[0]["size"], rule.max_body_bytes + 1)

    def test_encoded_response_is_not_buffered(self):
        flow = self.capture_roundtrip(b"\x1f\x8b junk", {"Content-Encoding": "gzip"})
        self.assertTrue(flow.response.stream)
        recs = self.records("comments.ndjson")
        self.assertEqual([r["error"] for r in recs], ["encoded"])
        self.assertEqual(recs[0]["encoding"], "gzip")

    def test_identity_encoding_is_buffered(self):
        flow = self.capture_roundtrip(b'{"comments": []}', {"Content-Encoding": "identity"})
        self.assertFalse(flow.response.stream)
        self.assertEqual(self.records("comments.ndjson"), [])

    def test_missing_items_key_records_items_missing(self):
        self.capture_roundtrip(b'{"status_code": 0}')
        self.assertEqual([r["error"] for r in self.records("comments.ndjson")], ["items_missing"])

    def test_unbuffered_body_records_not_buffered(self):
        import dataclasses
        platform = self.mod.PLATFORM
        streamed = dataclasses.replace(platform.captures[0], no_stream=False)
        self.mod.PLATFORM = dataclasses.replace(platform, captures=[streamed])
        flow = self.capture_roundtrip(None)
        self.assertEqual([r["error"] for r in self.records("comments.ndjson")], ["not_buffered"])
        self.assertIsNotNone(flow)

    # fail closed -------------------------------------------------------------------------------------------------

    def test_platform_none_refuses_everything_and_running_shuts_down(self):
        os.remove(self.pjson)
        mod = self.load()
        self.assertIsNone(mod.PLATFORM)
        addon = mod.addons[0]
        for host in ("example.com", "www.tiktok.com", "fourcat.desk"):
            flow = Flow(Req(host, "/api/import-dataset/", "POST"))
            addon.request(flow)
            self.assertEqual(flow.response.status_code, 403, host)
        addon.running()
        self.assertEqual(self.rec.shutdowns, 1)
        self.assertIn("platform definition not loaded", self.rec.errors[-1])

    def test_malformed_platform_json_shuts_down(self):
        with open(self.pjson, "w") as f:
            f.write("{not json")
        mod = self.load()
        mod.addons[0].running()
        self.assertEqual(self.rec.shutdowns, 1)

    def test_missing_desk_user_shuts_down(self):
        for user in (None, "", "  "):
            self.rec.shutdowns = 0
            mod = self.load(DESK_USER=user)
            mod.addons[0].running()
            self.assertEqual(self.rec.shutdowns, 1, repr(user))
        self.assertIn("DESK_USER", self.rec.errors[-1])

    def test_healthy_running_does_not_shut_down(self):
        self.addon.running()
        self.assertEqual(self.rec.shutdowns, 0)

    # write failures ----------------------------------------------------------------------------------------------

    @unittest.skipIf(hasattr(os, "geteuid") and os.geteuid() == 0, "root ignores directory permissions")
    def test_unwritable_captures_dir_counts_failure(self):
        os.makedirs(self.captures)
        os.chmod(self.captures, 0o500)
        self.addCleanup(os.chmod, self.captures, 0o700)
        flow = Flow(Req("fourcat.desk", "/"))
        self.addon.request(flow)                      # refusal is logged to disk; must not raise
        self.assertEqual(flow.response.status_code, 403)
        self.assertEqual(self.mod.WRITE_FAILURES, 1)
        self.assertIn("cannot write", self.rec.errors[-1])


class ResolveCacheTests(unittest.TestCase):
    def test_cache_hits_within_ttl_and_expires(self):
        cr._resolve_cache.clear()
        calls, clock = [], [100.0]

        def gai(host, port):
            calls.append(host)
            return [(2, 1, 6, "", ("93.184.216.34", 0))]

        now = lambda: clock[0]
        self.assertFalse(cr.resolves_internal_cached("Example.com", gai, now))
        self.assertFalse(cr.resolves_internal_cached("example.com", gai, now))
        clock[0] += 59
        self.assertFalse(cr.resolves_internal_cached("example.com", gai, now))
        self.assertEqual(len(calls), 1)
        clock[0] += 2
        self.assertFalse(cr.resolves_internal_cached("example.com", gai, now))
        self.assertEqual(len(calls), 2)
        cr._resolve_cache.clear()


if __name__ == "__main__":
    unittest.main()
