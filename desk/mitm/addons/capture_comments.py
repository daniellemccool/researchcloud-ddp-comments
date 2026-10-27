"""mitmproxy addon: record TikTok comment-list responses as NDJSON, one line per comment.

Writes /captures/<DESK_USER>/<YYYY-MM-DD>/session-<DESK_SESSION>.ndjson (append), plus a
page-open log (/captures/.../opens.ndjson) for every /video/ or /photo/ page the browser loads.
Nothing else is stored: no other hosts, no bodies of other endpoints.
"""
import json, os, time, datetime, re
from mitmproxy import http

SESSION = os.environ.get("DESK_SESSION", "0")
USER = os.environ.get("DESK_USER", "unknown")
ROOT = "/captures"

def _path(name):
    d = os.path.join(ROOT, USER, datetime.datetime.utcnow().strftime("%Y-%m-%d"))
    os.makedirs(d, exist_ok=True)
    return os.path.join(d, name)

def _append(name, obj):
    with open(_path(name), "a", encoding="utf-8") as f:
        f.write(json.dumps(obj, ensure_ascii=False) + "\n")

class Capture:
    def response(self, flow: http.HTTPFlow):
        host = flow.request.pretty_host.lower()
        if not (host == "tiktok.com" or host.endswith(".tiktok.com")):
            return
        path = flow.request.path
        now = int(time.time())
        # page opens: the desk reconciles these against the worklist
        if flow.request.method == "GET" and re.search(r"^/@[^/]+/(video|photo)/\d+", path) and "text/html" in (flow.response.headers.get("content-type") or ""):
            m = re.search(r"/(video|photo)/(\d+)", path)
            _append("opens.ndjson", {"t": now, "session": SESSION, "user": USER, "kind": m.group(1), "video_id": m.group(2), "status": flow.response.status_code})
            return
        if "/api/comment/list" not in path:      # covers /api/comment/list/ and /api/comment/list/reply/
            return
        try:
            data = json.loads(flow.response.get_text() or "{}")
        except Exception as e:
            _append("errors.ndjson", {"t": now, "session": SESSION, "path": path, "error": str(e)[:200]})
            return
        comments = data.get("comments") or []
        meta = {"t": now, "session": SESSION, "user": USER, "endpoint": "reply" if "/reply" in path else "list",
                "status_code": data.get("status_code"), "total": data.get("total"), "has_more": data.get("has_more"), "cursor": data.get("cursor")}
        _append("api-calls.ndjson", dict(meta, n=len(comments), aweme_id=(comments[0].get("aweme_id") if comments else None)))
        for c in comments:
            user = c.get("user") or {}
            _append(f"session-{SESSION}.ndjson", {
                "t": now, "session": SESSION, "user": USER,
                "cid": c.get("cid"), "post_id": c.get("aweme_id"), "text": c.get("text"),
                "create_time": c.get("create_time"), "digg_count": c.get("digg_count"),
                "reply_id": c.get("reply_id"), "reply_comment_total": c.get("reply_comment_total"),
                "author_unique_id": user.get("unique_id"), "author_nickname": user.get("nickname"), "author_uid": user.get("uid"),
                "comment_language": c.get("comment_language"), "author_pin": c.get("author_pin"), "stick_position": c.get("stick_position"),
                "raw": c,
            })

addons = [Capture()]
