"""Pure decision and extraction logic for the desk's mitm sidecar (stdlib only; no mitmproxy import).

Security boundary: the only way a collector's browser may reach 4CAT is POST /api/import-dataset/ and
GET /api/check-query/ on fourcat.desk. Everything else on that host is refused, and so is every
internal-looking host (localhost, IP literals, dotless names, *.desk).
"""
import ipaddress
import json
import os
import socket
import re
import time
from enum import Enum

from .platform import host_matches

FOURCAT_HOST = "fourcat.desk"
ALLOWED_FOURCAT = frozenset({("POST", "/api/import-dataset/"), ("GET", "/api/check-query/")})


class Decision(Enum):
    PASS = "pass"
    CAPTURE = "capture"
    FOURCAT = "fourcat"
    FORBID = "forbid"
    REFUSE = "refuse"


def normalise_host(host: str) -> str:
    """Lowercase, strip IPv6 brackets, a :port and a trailing dot."""
    h = (host or "").strip().lower()
    if h.startswith("["):
        end = h.find("]")
        h = h[1:end] if end != -1 else h[1:]
    elif h.count(":") == 1:
        h = h.rsplit(":", 1)[0]
    return h.rstrip(".")


def is_internal_host(host: str) -> bool:
    h = normalise_host(host)
    if h == FOURCAT_HOST:
        return False
    if not h or ":" in h:                       # empty, or an IPv6 literal (valid or not)
        return True
    try:
        ipaddress.ip_address(h)
        return True
    except ValueError:
        pass
    if "." not in h:                            # dotless: docker service names, "localhost"
        return True
    labels = h.split(".")
    if "localhost" in labels or h.endswith(".desk"):
        return True
    return labels[-1].isdigit()                 # 127.1 and friends: not a real TLD, resolvers read it as an address


def hosts_agree(real: str, pretty: str) -> bool:
    """True when the connection target and the client-supplied Host header name the same host."""
    return normalise_host(real) == normalise_host(pretty)


def is_internal_address(ip: str) -> bool:
    try:
        a = ipaddress.ip_address(ip.split("%", 1)[0])
    except ValueError:
        return True
    if getattr(a, "ipv4_mapped", None) is not None:
        a = a.ipv4_mapped
    return (a.is_loopback or a.is_private or a.is_link_local or a.is_unspecified
            or a.is_reserved or a.is_multicast)


def resolves_internal(host: str, getaddrinfo=socket.getaddrinfo) -> bool:
    """True if any address `host` resolves to is internal; fails closed on resolution errors."""
    try:
        infos = getaddrinfo(normalise_host(host), None)
    except (OSError, UnicodeError):
        return True
    if not infos:
        return True
    return any(is_internal_address(info[4][0]) for info in infos)


_RESOLVE_TTL = 60.0
_resolve_cache = {}


def resolves_internal_cached(host: str, getaddrinfo=socket.getaddrinfo, now=time.monotonic) -> bool:
    """resolves_internal with a 60 s in-process cache keyed by normalised host.

    The lookup is still synchronous, so a cold miss blocks mitmproxy's event loop for one DNS round trip;
    making the hook async is out of scope. Failures are cached too (fail closed, same TTL).
    """
    key, t = normalise_host(host), now()
    hit = _resolve_cache.get(key)
    if hit and hit[1] > t:
        return hit[0]
    result = resolves_internal(host, getaddrinfo)
    _resolve_cache[key] = (result, t + _RESOLVE_TTL)
    return result


def decide(p, host: str, method: str, path: str, fourcat_enabled: bool):
    """Returns (Decision, CaptureRule | None). `path` is the path without the query string."""
    h = normalise_host(host)
    if h == FOURCAT_HOST:
        if fourcat_enabled and (method.upper(), path) in ALLOWED_FOURCAT:
            return Decision.FOURCAT, None
        return Decision.FORBID, None
    if is_internal_host(h):
        return Decision.REFUSE, None
    if host_matches(p, h):
        for rule in p.captures:
            if rule.path.search(path):
                return Decision.CAPTURE, rule
    return Decision.PASS, None


def page_open(p, method: str, path: str, status: int, content_type: str):
    if method.upper() != "GET" or "text/html" not in (content_type or "").lower():
        return None
    m = p.open_pattern.search(path)
    if not m:
        return None
    return {"kind": m.group(1), "id": m.group("id")}


def dotted(obj, path: str):
    for key in path.split("."):
        if not isinstance(obj, dict):
            return None
        obj = obj.get(key)
    return obj


def post_id_for(rule, request: dict, body_json):
    spec = rule.post_id
    if not spec:
        return None
    src = spec.get("from")
    if src == "query":
        val = (request.get("query") or {}).get(spec["param"])
    elif src == "path":
        m = rule.path.search(request.get("path") or "")
        val = m.group(spec["group"]) if m else None
    elif src == "body":
        val = dotted(body_json, spec["field"])
    else:
        return None
    return None if val is None else str(val)


def _envelope(rule, ctx, request, status):
    keep = {k: v for k, v in (request.get("query") or {}).items() if k in rule.query_keep}
    return {
        "t": ctx["t"], "user": ctx["user"], "session": ctx["session"], "platform": ctx["platform"],
        "capture": rule.name, "kind": None,
        "post_id": post_id_for(rule, request, None),
        "request": {"method": request.get("method"), "host": request.get("host"),
                    "path": request.get("path"), "query": keep},
        "response": {"status": status},
    }


def error_record(rule, ctx: dict, request: dict, status: int, error: str, **extra) -> dict:
    return dict(_envelope(rule, ctx, request, status), error=error, **extra)


def too_large_record(rule, ctx: dict, request: dict, status: int, size: int) -> dict:
    return dict(_envelope(rule, ctx, request, status), error="body_too_large", size=size)


def extract(rule, ctx: dict, request: dict, status: int, body: bytes) -> list:
    env = _envelope(rule, ctx, request, status)
    if len(body) > rule.max_body_bytes:
        return [too_large_record(rule, ctx, request, status, len(body))]
    try:
        data = json.loads(body)
    except ValueError:
        return [dict(env, error="json")]
    env["post_id"] = post_id_for(rule, request, data)
    if isinstance(data, dict):
        for k in rule.response_keep:
            if k in data:
                env["response"][k] = data[k]
    if rule.items is None:
        return [dict(env, body=data)]
    items = dotted(data, rule.items)
    if items is None:
        return [dict(env, error="items_missing")]
    if not isinstance(items, list):
        return [dict(env, error="items")]
    records = []
    for item in items:
        rec = dict(env, item=item, id=dotted(item, rule.id))
        for name, field_path in rule.fields.items():
            rec[name] = dotted(item, field_path)
        records.append(rec)
    return records


def read_token(path: str, cache: dict):
    """Token file contents, re-read when its mtime or size changes; None if missing or empty."""
    try:
        st = os.stat(path)
        sig = (st.st_mtime_ns, st.st_size)
        hit = cache.get(path)
        if hit and hit[0] == sig:
            return hit[1]
        with open(path, encoding="utf-8") as f:
            token = f.read().strip() or None
    except (OSError, UnicodeDecodeError):
        cache.pop(path, None)
        return None
    cache[path] = (sig, token)
    return token
