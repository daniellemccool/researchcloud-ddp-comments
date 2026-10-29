"""Shared test fakes: a `docker` script for PATH and an in-process fake 4CAT login endpoint."""
import http.server, os, subprocess, threading, urllib.parse

GOOD_HASH = "$2b$12$" + "a" * 53

# Every call is appended to $FAKE_DOCKER_CALLS. `compose -f <root>/docker-compose.yml up -d mitm-<u>`
# drops the CA a real mitmproxy would write, unless the file $FAKE_DOCKER_NO_CA exists.
FAKE_DOCKER = """#!/usr/bin/env bash
echo "$*" >> "$FAKE_DOCKER_CALLS"
if [ "$1" = compose ] && [ "$4" = up ]; then
  case "$6" in
    mitm-*) [ -f "$FAKE_DOCKER_NO_CA" ] || touch "$(dirname "$3")/state/mitm/${6#mitm-}/mitmproxy-ca-cert.pem";;
  esac
  exit 0
fi
[ "$1" = stop ] && exit 0
case "$*" in
  *create_user.py*) [ -f "$FAKE_DOCKER_FAIL" ] && { echo "boom" >&2; exit 1; };;
  *bcrypt*) cat > "$FAKE_DOCKER_STDIN"; if [ -f "$FAKE_DOCKER_HASH" ]; then cat "$FAKE_DOCKER_HASH"; else echo '""" + GOOD_HASH + """'; fi;;
  *UPDATE*) if [ -f "$FAKE_DOCKER_UPDATE" ]; then cat "$FAKE_DOCKER_UPDATE"; else echo "UPDATE 1"; fi;;
  *psql*) cat "$FAKE_DOCKER_PSQL" 2>/dev/null;;
esac
exit 0
"""

# Control files the fake reads, relative to the directory passed to fake_docker().
CONTROL = {"FAKE_DOCKER_CALLS": "calls", "FAKE_DOCKER_PSQL": "psql", "FAKE_DOCKER_STDIN": "stdin",
           "FAKE_DOCKER_HASH": "hash", "FAKE_DOCKER_UPDATE": "update", "FAKE_DOCKER_FAIL": "fail",
           "FAKE_DOCKER_NO_CA": "no-ca"}


def fake_docker(t):
    """Install the fake under <t>/bin; return a `run` that sees it first on PATH (control files in <t>)."""
    bindir = os.path.join(t, "bin")
    os.makedirs(bindir, exist_ok=True)
    path = os.path.join(bindir, "docker")
    with open(path, "w") as f:
        f.write(FAKE_DOCKER)
    os.chmod(path, 0o755)
    env = dict(os.environ, PATH=bindir + os.pathsep + os.environ["PATH"],
               **{k: os.path.join(t, v) for k, v in CONTROL.items()})

    def run(*a, **kw):
        return subprocess.run(*a, **{**kw, "env": env})
    return run


def docker_calls(t):
    p = os.path.join(t, CONTROL["FAKE_DOCKER_CALLS"])
    if not os.path.exists(p):
        return []
    with open(p) as f:
        return f.read().splitlines()


class FakeFourCat:
    """POST /login/: 302 + remember_token=tok-<user> when check(user, password); else 401.

    Users `nocookie` (302 without a cookie) and `broken` (500) exercise the client's edge cases.
    `logins` records each username that attempted a login, in order."""

    def __init__(self, passwords=None, check=None):
        self.passwords = passwords if passwords is not None else {}
        self.check = check or (lambda u, p: self.passwords.get(u) == p)
        self.logins = []
        fake = self

        class Handler(http.server.BaseHTTPRequestHandler):
            def log_message(self, *a):
                pass

            def do_POST(self):
                n = int(self.headers.get("Content-Length", 0))
                form = urllib.parse.parse_qs(self.rfile.read(n).decode())
                user = form.get("username", [""])[0]
                fake.logins.append(user)
                if user == "nocookie":
                    self.send_response(302)
                elif user == "broken":
                    self.send_response(500)
                elif self.path == "/login/" and fake.check(user, form.get("password", [""])[0]):
                    self.send_response(302)
                    self.send_header("Location", "/")
                    self.send_header("Set-Cookie", f"remember_token=tok-{user}; Path=/")
                else:
                    self.send_response(401)
                self.send_header("Content-Length", "0")
                self.end_headers()

        self.srv = http.server.ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        self.url = f"http://127.0.0.1:{self.srv.server_address[1]}"
        threading.Thread(target=self.srv.serve_forever, args=(0.05,), daemon=True).start()

    def close(self):
        self.srv.shutdown()
        self.srv.server_close()
