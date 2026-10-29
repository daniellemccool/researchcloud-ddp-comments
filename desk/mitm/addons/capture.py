"""mitmproxy shim: all decisions and extraction live in desklib.capture_rules; this only wires them to flows.

Per collector, the sidecar proxy: records the platform's capture rules as NDJSON, logs page opens, and is the
only door from the browser to 4CAT (fourcat.desk, two allowed calls, token injected here, never in the browser).

A captured request goes upstream with Accept-Encoding: identity. A server may compress anyway: such a response
is refused at responseheaders and recorded as `encoded`, so nothing is ever decompressed here and the
Content-Length checked against the rule's max_body_bytes is the real size.
"""
import datetime
import json
import os
import sys
import time
import urllib.parse

sys.path.insert(0, "/opt/desk")

from mitmproxy import ctx, http  # noqa: E402

from desklib import capture_rules as cr  # noqa: E402
from desklib import platform as pf  # noqa: E402

USER = os.environ.get("DESK_USER", "").strip()      # empty means the sidecar is misconfigured: running() stops it
SESSION = os.environ.get("DESK_SESSION", "0")
CAPTURES = os.environ.get("DESK_CAPTURES", "/captures")
FOURCAT_ENABLED = os.environ.get("DESK_FOURCAT_ENABLED", "0") == "1"
UPSTREAM = urllib.parse.urlsplit(os.environ.get("DESK_FOURCAT_UPSTREAM", "http://frontend:5000"))
TOKEN_FILE = os.environ.get("DESK_FOURCAT_TOKEN_FILE", "/run/desk/4cat-token")
try:
    PLATFORM = pf.load(os.environ.get("DESK_PLATFORM_JSON", "/platform/platform.json"))
    PLATFORM_ERROR = None
except Exception as _e:                             # never raise at import: mitmproxy would keep proxying without us
    PLATFORM, PLATFORM_ERROR = None, f"{type(_e).__name__}: {_e}"
WRITE_FAILURES = 0


def _append(name, obj):
    global WRITE_FAILURES
    try:
        day = datetime.datetime.now(datetime.timezone.utc).strftime("%Y-%m-%d")
        d = os.path.join(CAPTURES, USER, day)
        os.makedirs(d, exist_ok=True)
        with open(os.path.join(d, name), "a", encoding="utf-8") as f:
            f.write(json.dumps(obj, ensure_ascii=False) + "\n")
    except OSError as e:                            # a lost safety-copy write must be loud, never silent
        WRITE_FAILURES += 1
        ctx.log.error(f"desk: cannot write {name}: {e}")


def _path(flow):
    return flow.request.path.split("?", 1)[0]



def _request_dict(flow):
    return {"method": flow.request.method, "host": flow.request.pretty_host, "path": _path(flow),
            "query": dict(flow.request.query.items())}


class Capture:
    def __init__(self):
        self.token_cache = {}

    def _refuse(self, flow, decision, **extra):
        flow.response = http.Response.make(403, b"desk: refused", {"Content-Type": "text/plain"})
        _append("refused.ndjson", dict({"t": int(time.time()), "action": "refused", "host": flow.request.host,
                                        "method": flow.request.method, "path": _path(flow),
                                        "decision": decision}, **extra))

    def running(self):
        """Fail closed: a sidecar without a platform or a user is a dead proxy, never an open one."""
        problem = (f"platform definition not loaded ({PLATFORM_ERROR})" if PLATFORM is None
                   else "DESK_USER is not set" if not USER else None)
        if problem:
            ctx.log.error(f"desk: refusing to run: {problem}")
            ctx.master.shutdown()

    def request(self, flow: http.HTTPFlow):
        if PLATFORM is None or not USER:            # misconfigured: refuse even before running() shuts us down
            return self._refuse(flow, "no-platform" if PLATFORM is None else "no-user")
        try:
            self._request(flow)
        except Exception as e:                      # fail closed: a broken hook must never forward the flow
            self._refuse(flow, "error", error=type(e).__name__)

    def _request(self, flow):
        req = flow.request
        # Decide on the destination we really connect to (req.host), never on the client's Host header.
        if not cr.hosts_agree(req.host, req.pretty_host):
            return self._refuse(flow, "host-mismatch")
        decision, rule = cr.decide(PLATFORM, req.host, req.method, _path(flow), FOURCAT_ENABLED)
        if decision in (cr.Decision.FORBID, cr.Decision.REFUSE):
            return self._refuse(flow, decision.value)
        if decision is cr.Decision.FOURCAT:
            req.scheme = UPSTREAM.scheme
            req.host = UPSTREAM.hostname
            req.port = UPSTREAM.port or (443 if UPSTREAM.scheme == "https" else 80)
            req.headers["Host"] = cr.FOURCAT_HOST
            req.headers.pop("Cookie", None)
            token = cr.read_token(TOKEN_FILE, self.token_cache)
            if token:
                req.headers["Cookie"] = "remember_token=" + token
            flow.metadata["desk"] = ("fourcat", None)
            return
        if cr.resolves_internal_cached(req.host):
            return self._refuse(flow, "resolved-internal")
        if decision is cr.Decision.CAPTURE:
            req.headers["Accept-Encoding"] = "identity"     # the size check sees the real body size
            flow.metadata["desk"] = ("capture", rule)

    @staticmethod
    def _ctx(now=None):
        return {"t": now or int(time.time()), "user": USER, "session": SESSION, "platform": PLATFORM.name}

    def responseheaders(self, flow: http.HTTPFlow):
        kind, rule = flow.metadata.get("desk", (None, None))
        if kind == "fourcat":
            flow.response.headers.pop("Set-Cookie", None)   # never hand a 4CAT credential to the browser
        elif kind == "capture":
            enc = flow.response.headers.get("Content-Encoding", "").strip().lower()
            if enc and enc != "identity":                   # we asked for identity; never decompress here
                flow.metadata["desk_skip"] = True
                return _append(rule.name + ".ndjson", cr.error_record(
                    rule, self._ctx(), _request_dict(flow), flow.response.status_code, "encoded", encoding=enc))
            if not rule.no_stream:
                return
            try:
                size = int(flow.response.headers.get("Content-Length", ""))
            except ValueError:
                size = None
            if size is not None and size > rule.max_body_bytes:
                flow.metadata["desk_skip"] = True           # leave streaming on; record only the size
                _append(rule.name + ".ndjson", cr.too_large_record(
                    rule, self._ctx(), _request_dict(flow), flow.response.status_code, size))
            else:
                flow.response.stream = False

    def response(self, flow: http.HTTPFlow):
        now, path, resp = int(time.time()), _path(flow), flow.response
        kind, rule = flow.metadata.get("desk", (None, None))
        if kind == "fourcat":
            resp.headers.pop("Set-Cookie", None)
            _append("fourcat.ndjson", {"t": now, "action": "fourcat", "path": path, "status": resp.status_code})
            return
        if flow.metadata.get("desk_skip"):
            return
        if kind != "capture":
            # page opens come from platform hosts that matched no capture rule; the desk reconciles them
            if pf.host_matches(PLATFORM, flow.request.pretty_host):
                opened = cr.page_open(PLATFORM, flow.request.method, path, resp.status_code,
                                      resp.headers.get("content-type", ""))
                if opened:
                    _append("opens.ndjson", dict(opened, t=now, session=SESSION, user=USER, status=resp.status_code))
            return
        cx = self._ctx(now)
        if not rule.no_stream and not resp.content:         # streamed: the body was never held, so say so
            return _append(rule.name + ".ndjson", cr.error_record(
                rule, cx, _request_dict(flow), resp.status_code, "not_buffered"))
        for rec in cr.extract(rule, cx, _request_dict(flow), resp.status_code, resp.content or b""):
            _append(rule.name + ".ndjson", rec)


addons = [Capture()]
