"""Routes under /desk/admin/: the page an admin works from during a study.

desk_stub.py has already checked SRAM membership and the admin flag; this module only routes.
Every action calls the same desklib code the operator commands call (Sessions, FourCat, pool,
export), never a parallel implementation.
"""
import email.parser
import os
import socket
import subprocess
import time
import urllib.error

import desk_page as page
from desklib import export, pool
from desklib.fourcat import FourCatError
from desklib.locks import flocked, user_lock
from desklib.registry import USER_RE

MAX_UPLOAD = 5 * 1024 * 1024
UPLOAD_TIMEOUT = 30   # seconds a stalled upload may hold a handler thread
OPS = ("reconcile", "prune", "stop", "retire", "repair")
NEEDS_USER = ("stop", "retire", "repair")
NO_4CAT = "4CAT is not enabled on this desk."
# What a Sessions/FourCat call may raise; the messages carry no secrets (Tasks 6 and 7).
ACTION_ERRORS = (ValueError, RuntimeError, FileNotFoundError, FourCatError, OSError, urllib.error.URLError,
                 subprocess.SubprocessError)
NO_STORE = [("Cache-Control", "no-store")]
FRAMING = [("X-Frame-Options", "DENY"), ("Content-Security-Policy", "frame-ancestors 'none'")]


def csrf_ok(headers) -> bool:
    """A browser form post from our own page says Sec-Fetch-Site: same-origin; a client without
    that header must send an Origin that matches Host. Anything else is refused."""
    site = headers.get("Sec-Fetch-Site")
    if site is not None:
        return site == "same-origin"
    origin, host = headers.get("Origin"), headers.get("Host")
    if not origin or not host:
        return False
    return origin.split("://", 1)[-1] == host and origin.split("://", 1)[0] in ("http", "https")


def read_upload(handler) -> tuple[int, bytes | str]:
    """(0, file bytes) from a multipart form's `file` field, else (status, reason).

    The socket gets a timeout first: a drip-fed body ends in 408 instead of pinning the thread."""
    try:
        n = int(handler.headers.get("Content-Length", ""))
    except ValueError:
        return 411, "Content-Length required"
    if n < 0:
        return 400, "bad Content-Length"
    handler.connection.settimeout(UPLOAD_TIMEOUT)
    try:
        if n > MAX_UPLOAD:
            handler.close_connection = True
            left = n if n <= 8 * MAX_UPLOAD else 0     # drain a modest overshoot so the client can read the 413
            while left > 0:
                chunk = handler.rfile.read(min(left, 65536))
                if not chunk:
                    break
                left -= len(chunk)
            return 413, "upload larger than 5 MB"
        body = handler.rfile.read(n)
    except socket.timeout:
        handler.close_connection = True
        return 408, "upload timed out"
    ctype = handler.headers.get("Content-Type", "")
    if not ctype.lower().startswith("multipart/form-data"):
        return 400, "expected a multipart form"
    msg = email.parser.BytesParser().parsebytes(b"Content-Type: " + ctype.encode("latin-1", "replace")
                                                + b"\r\n\r\n" + body)
    for part in msg.walk() if msg.is_multipart() else ():
        if part.get_param("name", header="content-disposition") == "file":
            return 0, part.get_payload(decode=True) or b""
    return 400, "no file field in the form"


def reject_text(rejects) -> str:
    if not rejects:
        return "no rejects"
    shown = ", ".join(f"{n}: {why}" for n, why in rejects[:20])
    more = f" (and {len(rejects) - 20} more)" if len(rejects) > 20 else ""
    return f"rejected lines: {shown}{more}"


def summarise(op, user, result, sessions) -> str:
    """One line for the notice after a session action."""
    if op == "reconcile":
        parts = [f"{k.replace('_', ' ')}: {' '.join(result.get(k, [])) or '-'}"
                 for k in ("retired", "admin_set", "admin_cleared")]
        if result.get("skipped"):
            parts.append("skipped (no 4CAT account yet): " + " ".join(result["skipped"]))
        parts += [f"{u} failed: {why}" for u, why in result.get("failed", {}).items()]
        return "reconcile: " + "; ".join(parts)
    if op == "prune":
        return ("prune: retired " + (" ".join(result) or "-")
                + "".join(f"; {u}: retire failed: {why}" for u, why in sessions.failed.items()))
    if op == "stop":
        return f"stopped {user}" if result else f"could not stop {user}"
    if op == "retire":
        return f"{user}: retire failed: {result}" if result else f"retired {user}"
    return f"{user}: " + ("ready" if result.ready() else f"stuck at {result.current()}: {result.error() or 'incomplete'}")


def overview(desk, notice=None) -> str:
    sessions = desk.sessions
    r = desk._docker("ps", "-a", "--format", "{{.Names}}\t{{.Status}}", timeout=10)
    status = dict(l.split("\t", 1) for l in (r.stdout if r else "").splitlines() if "\t" in l)
    events = desk.events()
    rows = desk.pool()
    taken = desk.taken(events)
    free = sum(1 for x in rows if (x.kind, x.id) not in taken)
    noun = page.e(desk.platform.default_kind)
    pool_line = (f"pool.csv unreadable: {page.e(desk.pool_error)}" if desk.pool_error is not None
                 else f"{free} of {len(rows)} {noun}s unclaimed in the pool.")
    try:
        live = sessions.status()
    except (subprocess.SubprocessError, OSError):
        live = []
        notice = ((notice + " | ") if notice else "") + "could not read container states from docker"
    table = []
    for s in live:
        u = s["user"]
        claims, done, _, _ = desk.view(u, events)
        seen = desk.last_seen.get(u)
        table.append({**s, "seen": time.strftime("%H:%M", time.localtime(seen)) if seen else "—",
                      "firefox": status.get(f"desk-firefox-{u}", "missing"),
                      "mitm": status.get(f"desk-mitm-{u}", "missing"),
                      "claimed": len(claims), "done": sum(1 for k, _ in claims if k in done),
                      "open": sum(1 for k, _ in claims if k not in done)})
    try:
        expected, warning = sorted(set(sessions.members()) - set(desk.reg.live())), None
    except Exception:       # any CO API failure (network, parse, missing workspace file) must not break the page
        expected, warning = None, "could not read the CO member list; the expected-users list is unavailable"
    return page.admin(sessions=table, pool_line=pool_line, expected=expected, expected_warning=warning,
                      fourcat=desk.cfg.fourcat_enabled, notice=notice)


def handle_admin(handler, desk, user, method, path, query) -> bool:
    """Serve a /desk/admin/ request from an admin member; False if the path is not an admin route."""
    q = lambda k: query.get(k, [""])[0]          # noqa: E731
    def send(status, body="", ctype="text/html; charset=utf-8", headers=()):
        handler._send(status, body, ctype, [*headers, *FRAMING])

    def say(status, text):
        send(status, page.message(text))

    def redirect(where="/desk/admin/"):
        send(303, headers=[("Location", where)])
    if method == "POST":
        # Only a complete upload reads the body; every other answer leaves it unread, so the
        # connection must close after the response (never parse leftover bytes as a request).
        handler.close_connection = True
    if method == "POST" and not csrf_ok(handler.headers):
        say(403, "Refused: this request did not come from the admin page.")
        return True
    fourcat = desk.sessions.fourcat if desk.cfg.fourcat_enabled else None
    if method == "GET":
        if path == "/desk/admin/":
            send(200, overview(desk, desk.take_notice(user)))
        elif path == "/desk/admin/problems":
            evs = [e for e in desk.events() if e.get("action") == "problem"][-200:][::-1]
            evs.sort(key=lambda e: -e.get("t", 0))      # stable: equal times keep newest-appended first
            send(200, page.admin_problems([{"t": e.get("t", 0), "user": e.get("user", ""),
                                            "kind": desk.key(e)[0], "id": desk.key(e)[1] or "",
                                            "code": e.get("code", ""), "note": e.get("note", "")} for e in evs]))
        elif path in ("/desk/admin/export/captures.zip", "/desk/admin/export/desk-log.ndjson"):
            zipped = path.endswith(".zip")
            try:
                if zipped:
                    data = export.captures_zip(desk.root)
                else:
                    try:
                        with open(desk.log_path, "rb") as f:
                            data = f.read()
                    except FileNotFoundError:
                        data = b""
            except OSError as err:
                where = os.path.relpath(err.filename, desk.root) if isinstance(err.filename, str) else "?"
                send(500, f"export failed: {err.strerror or type(err).__name__} ({where})",
                     "text/plain; charset=utf-8")
                return True
            name = "captures.zip" if zipped else "desk-log.ndjson"
            send(200, data, "application/zip" if zipped else "application/x-ndjson",
                 [("Content-Disposition", f'attachment; filename="{name}"')])
        elif path == "/desk/admin/4cat/password":
            if fourcat is None:
                say(404, NO_4CAT)
                return True
            try:        # only ever the requesting admin's own file; no query or path input reaches it
                with open(fourcat.password_path(user)) as f:
                    pw = f.read().strip()
            except FileNotFoundError:
                send(404, page.admin_shell("Your 4CAT password", "<p>No 4CAT password on file for you yet.</p>"),
                     headers=NO_STORE)
                return True
            send(200, page.admin_password(user, pw), headers=NO_STORE)
        else:
            return False
        return True
    if method != "POST":
        return False
    if path == "/desk/admin/pool":
        status, data = read_upload(handler)
        if status:
            say(status, data)
            return True
        text = data.decode("utf-8", errors="replace")
        try:
            rows, rejects = pool.parse_selection(text, desk.platform)
        except pool.SelectionError as e:
            desk.set_notice(user, f"nothing added: line {e.line}: {e.reason}")
            redirect()
            return True
        try:
            added, _ = pool.add_rows(desk.root, rows, desk.platform)
        except (ValueError, OSError) as e:
            send(409, overview(desk, f"could not add to the pool: {e or type(e).__name__}"))
            return True
        desk.set_notice(user, f"added {added} of {len(rows) + len(rejects)}; {reject_text(rejects)}")
        redirect()      # a refresh then repeats the GET, not the upload
    elif path == "/desk/admin/sessions":
        op, target = q("op"), q("user")
        if op not in OPS or (op in NEEDS_USER and not USER_RE.fullmatch(target)):
            send(400)
            return True
        s = desk.sessions
        try:
            result = getattr(s, op)(*([target] if op in NEEDS_USER else []))
        except ACTION_ERRORS as e:
            notice = f"{op}{' ' + target if target else ''} failed: {type(e).__name__}: {e}"
        else:
            notice = summarise(op, target, result, s)
            if op == "stop" and result:        # a failed stop leaves the reaper's clock alone
                desk.last_seen.pop(target, None)
        desk.set_notice(user, notice)
        redirect()
    elif path in ("/desk/admin/4cat/password", "/desk/admin/4cat/rotate"):
        if fourcat is None:
            say(404, NO_4CAT)
            return True
        own = path.endswith("password")
        target = user if own else q("user")
        if not USER_RE.fullmatch(target):
            send(400)
            return True
        try:
            # the user lock: a provisioning run or retire may be writing the same token files
            with flocked(user_lock(desk.root, target)):
                fourcat.rotate(target)
        except ACTION_ERRORS as e:
            if own:
                send(500, page.admin_shell("Your 4CAT password",
                                           f"<p>Could not reissue: {page.e(str(e) or type(e).__name__)}</p>"),
                     headers=NO_STORE)
                return True
            desk.set_notice(user, f"rotate 4CAT for {target} failed: {type(e).__name__}: {e}")
            redirect()
            return True
        if own:
            redirect("/desk/admin/4cat/password")   # shows the current password; a refresh re-rotates nothing
        else:
            desk.set_notice(user, f"rotated 4CAT credentials for {target}")
            redirect()
    else:
        return False
    return True
