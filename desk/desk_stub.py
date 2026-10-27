#!/usr/bin/env python3
"""Spike stand-in for the desk: a sidebar worklist beside the streamed Firefox.

    python3 desk_stub.py --worklist selection.csv --port 8110
Clicking a row runs `docker exec desk-firefox-1 firefox <url>` (S3) and logs it; the
Problem… button logs a code. Both go to state/desk-log.ndjson. REMOTE_USER comes from nginx.
"""
import argparse, csv, json, subprocess, time, html
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.parse import parse_qs, urlparse

ROWS = []; LOG = "state/desk-log.ndjson"
PAGE = """<!doctype html>
<html lang="en"><head><meta charset="utf-8"><title>desk spike</title>
<style>li{margin:6px 0}button{font:inherit}.done{color:#888}</style></head>
<body style="margin:0;font:14px system-ui;display:flex;flex-direction:row;height:100vh;overflow:hidden">
<aside style="width:320px;flex:0 0 320px;overflow:auto;border-right:1px solid #ddd;padding:12px;box-sizing:border-box">
<b>Worklist</b> — {user}<ol>{items}</ol></aside>
<iframe src="/desk/s/1/" style="flex:1 1 auto;height:100vh;border:0;min-width:0" allow="clipboard-read; clipboard-write"></iframe>
<script>
async function open_(id){await fetch('open?id='+id,{method:'POST'});document.getElementById('r'+id).classList.add('done');}
async function problem(id){const c=prompt('Problem code: U P L W R C X');if(c)await fetch('problem?id='+id+'&code='+encodeURIComponent(c),{method:'POST'});}
</script></body></html>"""

class H(BaseHTTPRequestHandler):
    def _user(self): return self.headers.get("REMOTE_USER", "?")
    def do_GET(self):
        items = "".join(f'<li id="r{r}"><a href="#" onclick="open_(\'{r}\');return false">{r}</a> <button onclick="problem(\'{r}\')">Problem…</button></li>' for r in ROWS)
        body = PAGE.replace("{user}", html.escape(self._user())).replace("{items}", items).encode()
        self.send_response(200); self.send_header("Content-Type", "text/html; charset=utf-8"); self.end_headers(); self.wfile.write(body)
    def do_POST(self):
        u = urlparse(self.path); q = parse_qs(u.query); vid = q.get("id", [""])[0]
        ev = {"t": int(time.time()), "user": self._user(), "action": u.path.strip("/"), "video_id": vid, "code": q.get("code", [""])[0]}
        if u.path.endswith("open") and vid.isdigit():
            url = f"https://www.tiktok.com/@x/video/{vid}/"
            r = subprocess.run(["docker", "exec", "-e", "DISPLAY=:0", "-e", "DBUS_SESSION_BUS_ADDRESS=unix:path=/tmp/dbus.base", "-e", "HOME=/config", "-e", "XDG_RUNTIME_DIR=/tmp/run/user/app", "desk-firefox-1", "su-exec", "1000:1000", "/usr/lib/firefox/firefox", "--profile", "/config/profile", url], capture_output=True, text=True, timeout=15)
            ev["exec_rc"] = r.returncode; ev["exec_err"] = r.stderr[-200:]
        with open(LOG, "a") as f: f.write(json.dumps(ev) + "\n")
        self.send_response(204); self.end_headers()

if __name__ == "__main__":
    ap = argparse.ArgumentParser(); ap.add_argument("--worklist", required=True); ap.add_argument("--port", type=int, default=8110)
    a = ap.parse_args()
    ROWS[:] = [r["video_id"] for r in csv.DictReader(open(a.worklist))]
    ThreadingHTTPServer(("127.0.0.1", a.port), H).serve_forever()
