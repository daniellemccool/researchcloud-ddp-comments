"""mitmproxy shim: all decisions and extraction live in desklib.capture_rules; this only wires them to flows.

Per collector, the sidecar proxy: records the platform's capture rules as NDJSON, logs page opens, and is the
only door from the browser to 4CAT (fourcat.desk, two allowed calls, token injected here, never in the browser).
"""
import datetime
import json
import os
import sys
import time
import urllib.parse

sys.path.insert(0, "/addons")

from mitmproxy import http  # noqa: E402

from desklib import capture_rules as cr  # noqa: E402
from desklib import platform as pf  # noqa: E402

USER = os.environ.get("DESK_USER", "unknown")
SESSION = os.environ.get("DESK_SESSION", "0")
CAPTURES = os.environ.get("DESK_CAPTURES", "/captures")
FOURCAT_ENABLED = os.environ.get("DESK_FOURCAT_ENABLED", "0") == "1"
UPSTREAM = urllib.parse.urlsplit(os.environ.get("DESK_FOURCAT_UPSTREAM", "http://frontend:5000"))
TOKEN_FILE = os.environ.get("DESK_FOURCAT_TOKEN_FILE", "/run/desk/4cat-token")
PLATFORM = pf.load(os.environ.get("DESK_PLATFORM_JSON", "/platform/platform.json"))


def _append(name, obj):
    day = datetime.datetime.now(datetime.timezone.utc).strftime("%Y-%m-%d")
    d = os.path.join(CAPTURES, USER, day)
    os.makedirs(d, exist_ok=True)
    with open(os.path.join(d, name), "a", encoding="utf-8") as f:
        f.write(json.dumps(obj, ensure_ascii=False) + "\n")


def _path(flow):
    return flow.request.path.split("?", 1)[0]


class Capture:
    def __init__(self):
        self.token_cache = {}

    def request(self, flow: http.HTTPFlow):
        req = flow.request
        decision, rule = cr.decide(PLATFORM, req.pretty_host, req.method, _path(flow), FOURCAT_ENABLED)
        if decision in (cr.Decision.FORBID, cr.Decision.REFUSE):
            flow.response = http.Response.make(403, b"desk: refused", {"Content-Type": "text/plain"})
            _append("refused.ndjson", {"t": int(time.time()), "action": "refused", "host": req.pretty_host,
                                       "method": req.method, "path": _path(flow), "decision": decision.value})
        elif decision is cr.Decision.FOURCAT:
            req.scheme = UPSTREAM.scheme
            req.host = UPSTREAM.hostname
            req.port = UPSTREAM.port or (443 if UPSTREAM.scheme == "https" else 80)
            req.headers["Host"] = cr.FOURCAT_HOST
            req.headers.pop("Cookie", None)
            token = cr.read_token(TOKEN_FILE, self.token_cache)
            if token:
                req.headers["Cookie"] = "remember_token=" + token
            flow.metadata["desk"] = ("fourcat", None)
        elif decision is cr.Decision.CAPTURE:
            flow.metadata["desk"] = ("capture", rule)

    def responseheaders(self, flow: http.HTTPFlow):
        kind, rule = flow.metadata.get("desk", (None, None))
        if kind == "capture" and rule.no_stream:
            flow.response.stream = False

    def response(self, flow: http.HTTPFlow):
        now, path, resp = int(time.time()), _path(flow), flow.response
        kind, rule = flow.metadata.get("desk", (None, None))
        if kind == "fourcat":
            _append("fourcat.ndjson", {"t": now, "action": "fourcat", "path": path, "status": resp.status_code})
            return
        if kind != "capture":
            # page opens come from platform hosts that matched no capture rule; the desk reconciles them
            if pf.host_matches(PLATFORM, flow.request.pretty_host):
                opened = cr.page_open(PLATFORM, flow.request.method, path, resp.status_code,
                                      resp.headers.get("content-type", ""))
                if opened:
                    _append("opens.ndjson", dict(opened, t=now, session=SESSION, user=USER, status=resp.status_code))
            return
        request = {"method": flow.request.method, "host": flow.request.pretty_host, "path": path,
                   "query": dict(flow.request.query.items())}
        ctx = {"t": now, "user": USER, "session": SESSION, "platform": PLATFORM.name}
        for rec in cr.extract(rule, ctx, request, resp.status_code, resp.content or b""):
            _append(rule.name + ".ndjson", rec)


addons = [Capture()]
