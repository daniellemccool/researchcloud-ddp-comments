#!/usr/bin/env python3
"""The desk: a bottomless queue beside each user's own streamed Firefox.

    python3 desk_stub.py --desk /data/onlinehate_zeeschuimer/desk --port 8110 [--batch 5]

nginx (desk.conf, rendered by make-sessions.sh) authenticates via SRAM and passes
REMOTE_USER and X-Desk-Admin on every request, overwriting anything the client sent.
Users must be in <desk>/sessions.tsv. All users draw from one shared <desk>/pool.csv
(video_id,bucket,note; grown with pool-add.py): "Get N more videos" claims the next
unclaimed rows, and is refused until every row of the user's current claims is opened
or marked with a problem code. A row click execs into desk-firefox-<user> only, and only
for ids that user claimed. Claims, opens and problems go to <desk>/state/desk-log.ndjson,
which is the only record of who has what. Everything outside /desk/ is 403: nginx sends
requests here when a user may not reach the real target.
"""
import argparse, csv, html, json, os, subprocess, threading, time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.parse import parse_qs, urlparse

PROBLEM_CODES = {"U", "P", "L", "W", "R", "C", "X"}
PAGE = """<!doctype html>
<html lang="en"><head><meta charset="utf-8"><title>desk — {user}</title>
<style>li{{margin:6px 0}}button{{font:inherit}}.done a{{color:#888}}small{{color:#666}}</style></head>
<body style="margin:0;font:14px system-ui;display:flex;flex-direction:row;height:100vh;overflow:hidden">
<aside style="width:320px;flex:0 0 320px;overflow:auto;border-right:1px solid #ddd;padding:12px;box-sizing:border-box">
<b>Your videos</b> — {user}{body}</aside>
<iframe src="/desk/vnc/" style="flex:1 1 auto;height:100vh;border:0;min-width:0" allow="clipboard-read; clipboard-write"></iframe>
<script>
async function open_(id){{const li=document.getElementById('r'+id),m=li.querySelector('.msg');
 const r=await fetch('open?id='+id,{{method:'POST'}});
 if(r.ok){{li.classList.add('done');m.textContent='';}}else{{m.textContent=' could not open; tell the operator';}}}}
async function problem(id){{const c=prompt('Problem code: U P L W R C X');if(!c)return;
 const k=c.trim().toUpperCase(),r=await fetch('problem?id='+id+'&code='+encodeURIComponent(k),{{method:'POST'}});
 document.getElementById('r'+id).querySelector('.msg').textContent=r.ok?' problem '+k:' unknown code';}}
async function more(){{await fetch('claim',{{method:'POST'}});location.reload();}}
</script></body></html>"""
FORBIDDEN = """<!doctype html><html lang="en"><head><meta charset="utf-8"><title>desk</title></head>
<body style="font:16px system-ui;margin:3em">You are not registered for the desk; ask Danielle.</body></html>"""


class Desk:
    def __init__(self, root, exec_timeout, batch):
        self.root, self.exec_timeout, self.batch = root, exec_timeout, batch
        self.log_path = os.path.join(root, "state", "desk-log.ndjson")
        self.lock = threading.Lock()

    def users(self):
        try:
            with open(os.path.join(self.root, "sessions.tsv")) as f:
                return [l.split("\t")[0] for l in f.read().splitlines() if "\t" in l]
        except FileNotFoundError:
            return []

    def pool(self):
        rows, seen = [], set()
        try:
            with open(os.path.join(self.root, "pool.csv"), newline="") as f:
                for r in csv.DictReader(f):
                    v = (r.get("video_id") or "").strip()
                    if v.isdigit() and v not in seen:
                        seen.add(v); rows.append(dict(r, video_id=v))
        except FileNotFoundError:
            pass
        return rows

    def events(self):
        try:
            with open(self.log_path) as f:
                return [json.loads(l) for l in f if l.strip()]
        except FileNotFoundError:
            return []

    def _append(self, evs):
        os.makedirs(os.path.dirname(self.log_path), exist_ok=True)
        with open(self.log_path, "a") as f:
            for ev in evs:
                f.write(json.dumps(ev) + "\n")

    def append(self, ev):
        with self.lock:
            self._append([ev])

    def view(self, user, events=None):
        """The user's claims in order, with batch numbers, and which of them are done."""
        claims, done, problems, opened = [], set(), {}, set()
        for e in self.events() if events is None else events:
            if e.get("user") != user:
                continue
            a = e.get("action")
            if a == "claim":
                claims.append((e["video_id"], e.get("batch", 0)))
            elif a == "open" and e.get("exec_rc") == 0:
                opened.add(e["video_id"]); done.add(e["video_id"])
            elif a == "problem":
                problems[e["video_id"]] = e["code"]; done.add(e["video_id"])
        return claims, done, opened, problems

    def claim(self, user):
        """Claim the next batch for user. Returns the claimed ids, or 'busy' / 'empty'."""
        with self.lock:
            events = self.events()
            claims, done, _, _ = self.view(user, events)
            if any(v not in done for v, _ in claims):
                return "busy"
            taken = {e["video_id"] for e in events if e.get("action") == "claim"}
            nxt = [r["video_id"] for r in self.pool() if r["video_id"] not in taken][: self.batch]
            if not nxt:
                return "empty"
            b = max((n for _, n in claims), default=0) + 1
            t = int(time.time())
            self._append([{"t": t, "user": user, "action": "claim", "video_id": v, "batch": b, "code": ""} for v in nxt])
            return nxt

    def open_video(self, user, vid):
        url = f"https://www.tiktok.com/@x/video/{vid}/"
        cmd = ["docker", "exec", "-e", "DISPLAY=:0", "-e", "DBUS_SESSION_BUS_ADDRESS=unix:path=/tmp/dbus.base",
               "-e", "HOME=/config", "-e", "XDG_RUNTIME_DIR=/tmp/run/user/app", f"desk-firefox-{user}",
               "su-exec", "1000:1000", "/usr/lib/firefox/firefox", "--profile", "/config/profile", url]
        try:
            r = subprocess.run(cmd, capture_output=True, text=True, timeout=self.exec_timeout)
            return r.returncode, r.stderr[-200:]
        except subprocess.TimeoutExpired:
            return "timeout", ""


class Handler(BaseHTTPRequestHandler):
    desk: Desk = None

    def log_message(self, fmt, *args):  # keep journald quiet; the NDJSON log is the record
        pass

    def _send(self, status, body=b"", ctype="text/html; charset=utf-8"):
        self.send_response(status)
        if body:
            self.send_header("Content-Type", ctype)
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def _user(self):
        u = (self.headers.get("REMOTE_USER") or "").strip()
        return u if u and u in self.desk.users() else None

    def _route(self):
        u = urlparse(self.path)
        return u.path, parse_qs(u.query)

    def do_GET(self):
        path, _ = self._route()
        user = self._user()
        if user is None or not path.startswith("/desk/"):
            return self._send(403, FORBIDDEN.encode())
        if path == "/desk/":
            return self._send(200, self._page(user).encode())
        if path == "/desk/admin":
            self.send_response(301); self.send_header("Location", "/desk/admin/")
            self.send_header("Content-Length", "0"); self.end_headers()
            return
        if path == "/desk/admin/":
            if self.headers.get("X-Desk-Admin") != "1":
                return self._send(403, FORBIDDEN.encode())
            return self._send(200, self._admin().encode())
        return self._send(404)

    def do_POST(self):
        path, q = self._route()
        user = self._user()
        if user is None or not path.startswith("/desk/"):
            return self._send(403, FORBIDDEN.encode())
        if path == "/desk/claim":
            return self._send(409 if isinstance(self.desk.claim(user), str) else 204)
        vid = q.get("id", [""])[0]
        claims, _, _, _ = self.desk.view(user)
        if not vid.isdigit() or vid not in {v for v, _ in claims}:
            return self._send(404)
        ev = {"t": int(time.time()), "user": user, "video_id": vid, "code": ""}
        if path == "/desk/open":
            rc, err = self.desk.open_video(user, vid)
            ev.update(action="open", exec_rc=rc, exec_err=err)
            self.desk.append(ev)
            return self._send(204 if rc == 0 else 500)
        if path == "/desk/problem":
            code = q.get("code", [""])[0].strip().upper()
            if code not in PROBLEM_CODES:
                return self._send(400)
            ev.update(action="problem", code=code)
            self.desk.append(ev)
            return self._send(204)
        return self._send(404)

    def _page(self, user):
        claims, done, opened, problems = self.desk.view(user)
        notes = {r["video_id"]: r.get("note") or "" for r in self.desk.pool()}
        latest = max((b for _, b in claims), default=0)
        shown = [v for v, b in claims if b == latest or v not in done]
        earlier = len(claims) - len(shown)
        items = []
        for v in shown:
            cls = ' class="done"' if v in opened else ""
            msg = f" problem {problems[v]}" if v in problems else ""
            note = f" <small>{html.escape(notes[v])}</small>" if notes.get(v) else ""
            items.append(f'<li id="r{v}"{cls}><a href="#" onclick="open_(\'{v}\');return false">{v}</a>{note} '
                         f'<button onclick="problem(\'{v}\')">Problem…</button><span class="msg">{msg}</span></li>')
        body = ("<ol>" + "".join(items) + "</ol>") if items else ""
        if earlier:
            body += f"<p><small>{earlier} done earlier</small></p>"
        if any(v not in done for v in shown):
            body += "<p><small>Finish these first: open each one, or mark a problem. Then you can get more.</small></p>"
        elif any(r["video_id"] not in {e["video_id"] for e in self.desk.events() if e.get("action") == "claim"}
                 for r in self.desk.pool()):
            body += f'<p><button onclick="more()">Get {self.desk.batch} more videos</button></p>'
        else:
            body += "<p>No more videos right now; tell the operator.</p>"
        return PAGE.format(user=html.escape(user), body=body)

    def _admin(self):
        try:
            ps = subprocess.run(["docker", "ps", "-a", "--format", "{{.Names}}\t{{.Status}}"],
                                capture_output=True, text=True, timeout=10).stdout
        except subprocess.TimeoutExpired:
            ps = ""
        status = dict(l.split("\t", 1) for l in ps.splitlines() if "\t" in l)
        events = self.desk.events()
        pool = self.desk.pool()
        taken = {e["video_id"] for e in events if e.get("action") == "claim"}
        free = sum(1 for r in pool if r["video_id"] not in taken)
        rows = []
        for u in self.desk.users():
            claims, done, _, _ = self.desk.view(u, events)
            mine = [e for e in events if e.get("user") == u]
            last = time.strftime("%H:%M", time.localtime(mine[-1]["t"])) if mine else "—"
            open_rows = sum(1 for v, _ in claims if v not in done)
            rows.append(f"<tr><td>{html.escape(u)}</td><td>{html.escape(status.get(f'desk-firefox-{u}', 'missing'))}</td>"
                        f"<td>{html.escape(status.get(f'desk-mitm-{u}', 'missing'))}</td>"
                        f"<td>{len(claims)}</td><td>{len(claims) - open_rows}</td><td>{open_rows}</td><td>{last}</td>"
                        f'<td><a href="/desk/admin/vnc/{html.escape(u)}/" target="_blank">watch</a></td></tr>')
        return ("<!doctype html><html lang=\"en\"><head><meta charset=\"utf-8\"><title>desk admin</title></head>"
                "<body style=\"font:14px system-ui;margin:2em\"><h1>Desk sessions</h1>"
                f"<p>{free} of {len(pool)} videos unclaimed in the pool.</p>"
                "<table cellpadding=\"6\"><tr><th>user</th><th>firefox</th><th>mitm</th><th>claimed</th>"
                "<th>done</th><th>open</th><th>last event</th><th>screen</th></tr>" + "".join(rows) +
                "</table><p><a href=\"/\">4CAT</a></p></body></html>")


def make_server(root, port, exec_timeout=15, batch=5):
    handler = type("BoundHandler", (Handler,), {"desk": Desk(root, exec_timeout, batch)})
    return ThreadingHTTPServer(("127.0.0.1", port), handler)


if __name__ == "__main__":
    ap = argparse.ArgumentParser()
    ap.add_argument("--desk", default="/data/onlinehate_zeeschuimer/desk")
    ap.add_argument("--port", type=int, default=8110)
    ap.add_argument("--batch", type=int, default=5)
    a = ap.parse_args()
    make_server(a.desk, a.port, batch=a.batch).serve_forever()
