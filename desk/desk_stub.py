#!/usr/bin/env python3
"""The desk: a bottomless queue beside each collector's own streamed Firefox.

    python3 desk_stub.py --desk-root <desk root> [--port 8110] [--no-reaper]

Everything else comes from <desk root>/desk.json (desklib.config) and the platform
definition it names. nginx authenticates via SRAM and sets REMOTE_USER, X-Desk-Roles,
X-Desk-Member and X-Desk-Admin on every request, overwriting anything the client sent.
Membership is nginx's decision: a request without X-Desk-Member: 1 is refused before
anything else happens. A member's first visit provisions their session (desklib.provision)
in a background thread while the page refreshes; once ready, opening /desk/ starts their
own stopped containers unless max_running sessions are already up.

All collectors draw from one shared <desk root>/pool.csv (id,kind,bucket,note): "Get N
more" claims the next unclaimed rows, refused until every row of the current claims is
opened or marked with a problem code. A row click execs into desk-firefox-<user> only,
and only for items that user claimed. Claims, opens and problems go to
state/desk-log.ndjson, the only record of who has what; roles-header changes go to
state/access.ndjson. Everything outside /desk/ is 403.

A reaper thread stops a Firefox that has had no screen connection (nginx's websocket to
its noVNC port, seen with `ss`) for idle_seconds. Stopping loses nothing.
"""
import argparse, csv, html, http.client, json, os, subprocess, sys, threading, time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.parse import parse_qs, urlparse

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import desk_page as page  # noqa: E402
from desk_admin_routes import handle_admin  # noqa: E402
from desk_sessions import build_sessions  # noqa: E402
from desklib import pool  # noqa: E402
from desklib.config import load_config  # noqa: E402
from desklib.platform import item_url, load_dir, problems  # noqa: E402
from desklib.registry import USER_RE  # noqa: E402

REGISTRY_FULL = "error: registry full"


class Desk:
    def __init__(self, sessions, exec_timeout, admin_flags):
        self.sessions, self.exec_timeout = sessions, exec_timeout
        self.cfg, self.reg, self.prov = sessions.cfg, sessions.reg, sessions.prov
        self.root = self.cfg.desk_root
        self.platform = load_dir(self.cfg.platforms_dir, self.cfg.platform)
        self.problems = {p.code: p for p in problems(self.platform, self.cfg.contact)}
        self.log_path = os.path.join(self.root, "state", "desk-log.ndjson")
        self.access_path = os.path.join(self.root, "state", "access.ndjson")
        self.lock, self.start_lock, self.prov_lock = threading.Lock(), threading.Lock(), threading.Lock()
        self.admin_flags = admin_flags  # user -> X-Desk-Admin of their last request (is_admin in roles mode)
        self.last_roles = {}    # user -> last roles header seen, so access.ndjson records changes only
        self.last_seen = {}     # user -> last time their screen was connected or their page loaded
        self.provisioning = set()  # users with an advance() thread running
        self.pool_error = None     # why the last pool.csv read failed, if it did
        self.notice = None         # one line from the last admin action, shown once on the next overview

    def set_notice(self, text):
        with self.lock:
            self.notice = text

    def take_notice(self):
        with self.lock:
            text, self.notice = self.notice, None
        return text

    # --- provisioning ----------------------------------------------------

    def kick(self, user):
        """Run prov.advance(user) in a daemon thread unless one is already running for user."""
        with self.prov_lock:
            if user in self.provisioning:
                return
            self.provisioning.add(user)

        def done():
            with self.prov_lock:
                self.provisioning.discard(user)

        def run():
            try:
                self.prov.advance(user)
            except Exception as e:   # step errors are recorded by advance; this is anything else
                # Provisioner and FourCat messages carry no secrets (Task 7's contract).
                print(f"provision {user}: {type(e).__name__}: {e}", file=sys.stderr, flush=True)
            finally:
                done()
        try:
            threading.Thread(target=run, daemon=True).start()
        except Exception as e:      # the next refresh of the setting-up page retries
            print(f"provision {user}: {type(e).__name__}: {e}", file=sys.stderr, flush=True)
            done()

    # --- containers ------------------------------------------------------

    @staticmethod
    def _docker(*args, timeout=30):
        try:
            return subprocess.run(["docker", *args], capture_output=True, text=True, timeout=timeout)
        except subprocess.TimeoutExpired:
            return None

    def running(self, name):
        r = self._docker("inspect", "-f", "{{.State.Running}}", name, timeout=10)
        return bool(r) and r.stdout.strip() == "true"

    def ensure_started(self, user):
        """Start the user's stopped pair unless max_running other sessions are up; False if busy.
        The count and the start are one unit, so two arrivals cannot both take the last slot."""
        with self.start_lock:
            try:
                running = self.sessions.running()
            except (subprocess.SubprocessError, OSError):
                running = {}            # can't count: the cap is a soft guard, let the user in
            if not running.get(user) and sum(running.values()) >= self.cfg.max_running:
                return False
            for name in (f"desk-mitm-{user}", f"desk-firefox-{user}"):
                if not self.running(name):
                    self._docker("start", name)
            self.last_seen[user] = time.time()
            return True

    def ready(self, user):
        """True once noVNC answers HTTP 200. A bare TCP accept is not enough: Docker's
        port proxy accepts before the container's server listens, then resets."""
        port = self.reg.port(user)
        if not port or not self.running(f"desk-firefox-{user}"):
            return False
        c = http.client.HTTPConnection("127.0.0.1", port, timeout=2)
        try:
            c.request("GET", "/")
            r = c.getresponse(); r.read()
            return r.status == 200
        except (OSError, http.client.HTTPException):
            return False
        finally:
            c.close()

    @staticmethod
    def connected_ports():
        try:
            out = subprocess.run(["ss", "-Htn"], capture_output=True, text=True, timeout=10).stdout
        except (subprocess.TimeoutExpired, FileNotFoundError):
            return None
        ports = set()
        for line in out.splitlines():
            f = line.split()
            if len(f) >= 4 and f[0] == "ESTAB" and f[3].rsplit(":", 1)[-1].isdigit():
                ports.add(int(f[3].rsplit(":", 1)[-1]))
        return ports

    def reap(self, now=None):
        """Stop Firefox sessions with no screen connection for longer than idle_seconds."""
        now = time.time() if now is None else now
        idle = self.cfg.idle_seconds
        conns = self.connected_ports()
        if conns is None:
            return                      # can't tell who is watching: stop nothing
        for user, row in self.reg.live().items():
            if not self.running(f"desk-firefox-{user}"):
                self.last_seen.pop(user, None)
                continue
            if self.reg.port_base + row.index in conns or user not in self.last_seen:
                self.last_seen[user] = now
                continue
            if now - self.last_seen[user] > idle:
                try:
                    self.sessions.stop(user)
                except subprocess.SubprocessError:
                    continue            # a hung stop must not end the sweep; the next one retries

                self.last_seen.pop(user, None)
                self.append({"t": int(now), "user": user, "action": "stop", "kind": "", "id": "", "code": "",
                             "idle_s": int(idle)})

    # --- pool and log ----------------------------------------------------

    def valid(self, kind, item_id):
        k = self.platform.item_kinds.get(kind)
        return k is not None and isinstance(item_id, str) and bool(k.id_re.fullmatch(item_id))

    def pool(self):
        """Pool rows whose kind and id this platform can open, in file order. An unreadable
        pool.csv reads as empty and its reason stays in self.pool_error for the admin page."""
        try:
            rows = pool.read_rows(self.root, self.platform.default_kind)
        except (ValueError, csv.Error) as e:
            self.pool_error = str(e) or type(e).__name__
            return []
        self.pool_error = None
        return [r for r in rows if self.valid(r.kind, r.id)]

    def key(self, e):
        """(kind, id) of a log event; legacy events carry video_id and no kind."""
        return (e.get("kind") or self.platform.default_kind, e.get("id") or e.get("video_id"))

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

    def note_access(self, user: str, roles: str, path: str) -> None:
        """Append {t, user, roles, path} to state/access.ndjson when the user's roles header changed."""
        with self.lock:
            if self.last_roles.get(user) == roles:
                return
            self.last_roles[user] = roles
            os.makedirs(os.path.dirname(self.access_path), exist_ok=True)
            with open(self.access_path, "a") as f:
                f.write(json.dumps({"t": int(time.time()), "user": user, "roles": roles, "path": path}) + "\n")

    def taken(self, events):
        return {self.key(e) for e in events if e.get("action") == "claim"}

    def view(self, user, events=None):
        """The user's claims in order as ((kind, id), batch), and which of them are done."""
        claims, done, problems_, opened = [], set(), {}, set()
        for e in self.events() if events is None else events:
            if e.get("user") != user:
                continue
            a, k = e.get("action"), self.key(e)
            if a == "claim":
                claims.append((k, e.get("batch", 0)))
            elif a == "open" and e.get("exec_rc") == 0:
                opened.add(k); done.add(k)
            elif a == "problem":
                problems_[k] = (e.get("code", ""), e.get("note", "")); done.add(k)
        return claims, done, opened, problems_

    def claim(self, user, n):
        """Claim the next n for user. Returns the claimed keys, or 'busy' / 'empty'."""
        with self.lock:
            events = self.events()
            claims, done, _, _ = self.view(user, events)
            if any(k not in done for k, _ in claims):
                return "busy"
            taken = self.taken(events)
            nxt = [(r.kind, r.id) for r in self.pool() if (r.kind, r.id) not in taken][:n]
            if not nxt:
                return "empty"
            b = max((b for _, b in claims), default=0) + 1
            t = int(time.time())
            self._append([{"t": t, "user": user, "action": "claim", "kind": k, "id": i, "batch": b, "code": ""}
                          for k, i in nxt])
            return nxt

    def open_url(self, user, url):
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

    def _send(self, status, body="", ctype="text/html; charset=utf-8", headers=()):
        body = body.encode() if isinstance(body, str) else body
        self.send_response(status)
        if body or ctype != "text/html; charset=utf-8":
            self.send_header("Content-Type", ctype)
        for k, v in headers:
            self.send_header(k, v)
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def _say(self, status, text, refresh=None):
        return self._send(status, page.message(text, refresh))

    def _forbidden(self):
        return self._say(403, f"You are not registered for the desk; ask {self.desk.cfg.contact}.")

    def _identity(self, path):
        """(user, admin) for a member on a /desk/ path, else None. Logs roles changes first."""
        user = (self.headers.get("REMOTE_USER") or "").strip()
        if user:
            self.desk.note_access(user, self.headers.get("X-Desk-Roles", ""), path)
        if not user or not USER_RE.fullmatch(user) or not path.startswith("/desk/"):
            return None
        if self.headers.get("X-Desk-Member") != "1":
            return None
        admin = self.headers.get("X-Desk-Admin") == "1"
        self.desk.admin_flags[user] = admin
        return user, admin

    def _route(self):
        u = urlparse(self.path)
        return u.path, parse_qs(u.query)

    def do_GET(self):
        path, _ = self._route()
        ident = self._identity(path)
        if ident is None:
            return self._forbidden()
        user, admin = ident
        if path == "/desk/":
            return self._home(user)
        if path == "/desk/status":
            return self._send(200, json.dumps({"ready": self.desk.ready(user)}), "application/json")
        if path == "/desk/admin":
            self.send_response(301); self.send_header("Location", "/desk/admin/")
            self.send_header("Content-Length", "0"); self.end_headers()
            return
        if path.startswith("/desk/admin/"):
            if not admin:
                return self._forbidden()
            if handle_admin(self, self.desk, user, "GET", path, self._route()[1]):
                return
        return self._send(404)

    def _home(self, user):
        d, contact = self.desk, self.desk.cfg.contact
        profile = os.path.join(d.root, "state", "firefox", user, "profile")
        if d.cfg.template_path is None and not os.path.isdir(profile):
            return self._say(503, f"The desk is not ready yet; ask {contact}.")
        st = d.prov.state(user)
        if not st.ready():
            if st.error() == REGISTRY_FULL:
                return self._say(200, f"The desk is full; ask {contact}.")
            if st.error():
                return self._say(200, f"Setup failed at {st.current()}; ask {contact}.")
            d.kick(user)
            return self._say(200, f"Setting up your desk — {st.current()}", refresh=3)
        if not d.ensure_started(user):
            return self._say(200, "The desk is busy, try again in a few minutes.")
        return self._send(200, self._page(user))

    def do_POST(self):
        path, q = self._route()
        ident = self._identity(path)
        if ident is None:
            return self._forbidden()
        user, admin = ident
        d = self.desk
        if path.startswith("/desk/admin/"):
            if not admin:
                return self._forbidden()
            if handle_admin(self, d, user, "POST", path, q):
                return
            return self._send(404)
        if path == "/desk/claim":
            n = q.get("n", [str(d.cfg.batch_sizes[0])])[0]
            if not n.isdigit() or int(n) not in d.cfg.batch_sizes:
                return self._send(400)
            if not d.prov.state(user).ready():
                return self._send(409)   # a hand-made POST must not claim before the desk exists
            return self._send(409 if isinstance(d.claim(user, int(n)), str) else 204)
        kind = q.get("kind", [""])[0] or d.platform.default_kind
        item_id = q.get("id", [""])[0]
        claims, _, _, _ = d.view(user)
        if not d.valid(kind, item_id) or (kind, item_id) not in {k for k, _ in claims}:
            return self._send(404)
        ev = {"t": int(time.time()), "user": user, "kind": kind, "id": item_id, "code": ""}
        if path == "/desk/open":
            rc, err = d.open_url(user, item_url(d.platform, kind, item_id))
            ev.update(action="open", exec_rc=rc, exec_err=err)
            d.append(ev)
            return self._send(204 if rc == 0 else 500)
        if path == "/desk/problem":
            code = q.get("code", [""])[0].strip()
            code = code if code in d.problems else code.upper()
            note = " ".join(q.get("note", [""])[0].split())[:300]
            p = d.problems.get(code)
            if p is None or (p.note and not note):
                return self._send(400)
            ev.update(action="problem", code=code, note=note)
            d.append(ev)
            return self._send(204)
        return self._send(404)

    def _page(self, user):
        d, cfg, plat = self.desk, self.desk.cfg, self.desk.platform
        events = d.events()
        claims, done, opened, probs = d.view(user, events)
        rows = d.pool()
        notes = {(r.kind, r.id): r.note for r in rows}
        latest = max((b for _, b in claims), default=0)
        shown = [k for k, b in claims if b == latest or k not in done]
        earlier = len(claims) - len(shown)
        noun = html.escape(plat.default_kind)
        options = page.problem_options(d.problems.values())
        items = []
        for n, k in enumerate(shown, 1):
            state = "problem" if k in probs else "done" if k in opened else "todo"
            msg = ""
            if k in probs:
                code, pnote = probs[k]
                label = d.problems[code].label if code in d.problems else code
                msg = "— " + html.escape(label) + (f": {html.escape(pnote)}" if pnote else "")
            title = notes.get(k) or f"{k[0].capitalize()} {n}"
            items.append(page.row(k[0], k[1], state, k in opened, title, msg, options, cfg.timer_minutes > 0))
        n_done = sum(1 for k in shown if k in done)
        body = ""
        if shown:
            body += (f'<button id="next" class="next" onclick="openNext()"'
                     f'{" disabled" if n_done == len(shown) else ""}>Open next {noun}</button>')
            body += '<ul class="rows">' + "".join(items) + "</ul>"
        if earlier:
            body += f'<p class="hint">{earlier} done earlier</p>'
        taken = d.taken(events)
        if any(k not in done for k in shown):
            body += '<p class="hint">Finish these first: open each one, or mark a problem. Then you can get more.</p>'
        elif any((r.kind, r.id) not in taken for r in rows):
            body += '<div class="more" id="getmore">' + "".join(
                f'<button onclick="more({n})">Get {n} more {noun}s</button>' for n in cfg.batch_sizes) + "</div>"
        else:
            body += f'<p class="hint" id="getmore">No more {noun}s right now; tell the operator.</p>'
        return page.desk(user=user, platform=plat, noun=plat.default_kind, seconds=cfg.timer_minutes * 60,
                         note_codes=[p.code for p in d.problems.values() if p.note],
                         progress_html=page.progress(n_done, len(shown)) if shown else "",
                         body=body, open_help=not claims,
                         footer_hint=plat.upload_hint if cfg.fourcat_enabled else None)


def make_server(desk_root, port, *, reaper=True, exec_timeout=15, sessions=None):
    admin_flags = {}
    if sessions is None:
        roles = load_config(desk_root).gate_mode == "roles"
        sessions = build_sessions(desk_root, is_admin=(lambda u: admin_flags.get(u, False)) if roles else None)
    desk = Desk(sessions, exec_timeout, admin_flags)
    handler = type("BoundHandler", (Handler,), {"desk": desk})
    if reaper:
        def loop():
            while True:
                time.sleep(60)
                try:
                    desk.reap()
                except Exception:   # a failed sweep must not kill the thread; the next one retries
                    pass
        threading.Thread(target=loop, daemon=True).start()
    return ThreadingHTTPServer(("127.0.0.1", port), handler)


if __name__ == "__main__":
    ap = argparse.ArgumentParser()
    ap.add_argument("--desk-root", required=True, help="the desk's directory, holding desk.json")
    ap.add_argument("--port", type=int, default=8110)
    ap.add_argument("--no-reaper", action="store_true", help="never stop idle sessions")
    a = ap.parse_args()
    make_server(a.desk_root, a.port, reaper=not a.no_reaper).serve_forever()
