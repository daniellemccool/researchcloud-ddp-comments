"""Per-user 4CAT accounts, passwords and remember_tokens, via `docker exec` and HTTP (stdlib only)."""
import json
import os
import secrets
import subprocess
import urllib.error
import urllib.parse
import urllib.request
from http.cookies import SimpleCookie

from .registry import USER_RE


class FourCatError(RuntimeError):
    pass


class LoginFailed(FourCatError):
    pass


class _NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, *a, **kw):
        return None


_NOREDIR_OPEN = urllib.request.build_opener(_NoRedirect).open
_HASH_PY = ("import bcrypt,sys;print(bcrypt.hashpw(sys.stdin.read().strip().encode(),"
            "bcrypt.gensalt()).decode())")


def _check(user):
    if not isinstance(user, str) or not USER_RE.match(user):
        raise ValueError(f"invalid username: {user!r}")
    return user


class FourCat:
    def __init__(self, cfg, run=subprocess.run, urlopen=urllib.request.urlopen,
                 db_user="fourcat", db_name="fourcat"):
        self.cfg, self._run, self.db_user, self.db_name = cfg, run, db_user, db_name
        self._open = _NOREDIR_OPEN if urlopen is urllib.request.urlopen else urlopen

    def password_path(self, user):
        return os.path.join(self.cfg.desk_root, "secrets", "4cat-passwords", _check(user))

    def token_path(self, user):
        return os.path.join(self.cfg.desk_root, "secrets", "4cat-tokens", _check(user))

    def _exec(self, container, *args, input=None, what="docker exec"):
        cmd = ["docker", "exec", *(["-i"] if input is not None else []), container, *args]
        try:
            r = self._run(cmd, input=input, capture_output=True, text=True, timeout=60)
        except (OSError, subprocess.SubprocessError) as e:
            raise FourCatError(f"{what} failed to run: {type(e).__name__}") from None
        if r.returncode != 0:  # never echo the command line or output: they may hold secrets
            raise FourCatError(f"{what} exited with status {r.returncode}")
        return r.stdout or ""

    def _psql(self, sql):
        return self._exec(self.cfg.fourcat_db, "psql", "-U", self.db_user, "-d", self.db_name,
                          "-tAc", sql, what="psql").strip()

    @staticmethod
    def _write(path, text, exclusive=False):
        os.makedirs(os.path.dirname(path), mode=0o700, exist_ok=True)
        if exclusive:
            fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
        else:
            tmp = f"{path}.{secrets.token_hex(4)}.tmp"
            fd = os.open(tmp, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
        try:
            with os.fdopen(fd, "w") as f:
                f.write(text)
            if not exclusive:
                os.replace(tmp, path)
        except BaseException:
            try:
                os.unlink(path if exclusive else tmp)
            except FileNotFoundError:
                pass
            raise

    def ensure_password(self, user):
        path = self.password_path(user)
        for _ in range(2):
            try:
                with open(path) as f:
                    return f.read().strip(), False
            except FileNotFoundError:
                pass
            pw = secrets.token_urlsafe(24)
            try:
                self._write(path, pw, exclusive=True)
                return pw, True
            except FileExistsError:
                continue  # lost a race: read the winner's file
        raise FourCatError("password file unreadable")

    def account_exists(self, user):
        return self._psql(f"SELECT 1 FROM users WHERE name = '{_check(user)}'") == "1"

    def create_account(self, user, password, admin):
        args = ["python3", "helper-scripts/create_user.py", "-u", _check(user), "-e", "-p", password]
        if admin:
            args.append("-a")
        self._exec(self.cfg.fourcat_backend, *args, what="create_user.py")

    def hash_password(self, password):
        out = self._exec(self.cfg.fourcat_backend, "python3", "-c", _HASH_PY,
                         input=password, what="bcrypt").strip()
        if not out.startswith("$2"):
            raise FourCatError("bcrypt produced no hash")
        return out

    def set_password(self, user, password):
        h = self.hash_password(password)
        self._psql(f"UPDATE users SET password = '{h}' WHERE name = '{_check(user)}'")

    def is_admin(self, user):
        out = self._psql(f"SELECT tags::text FROM users WHERE name = '{_check(user)}'")
        try:
            return "admin" in json.loads(out or "[]")
        except ValueError:
            raise FourCatError("unparseable tags") from None

    def set_admin(self, user, admin):
        u = _check(user)
        if admin:
            sql = f"UPDATE users SET tags = (tags - 'admin') || '[\"admin\"]'::jsonb WHERE name = '{u}'"
        else:
            sql = f"UPDATE users SET tags = tags - 'admin' WHERE name = '{u}'"
        self._psql(sql)

    def login_token(self, user, password):
        _check(user)
        body = urllib.parse.urlencode({"username": user, "password": password}).encode()
        req = urllib.request.Request(self.cfg.fourcat_url.rstrip("/") + "/login/", data=body, method="POST",
                                     headers={"Content-Type": "application/x-www-form-urlencoded"})
        try:
            resp = self._open(req, timeout=15)
        except urllib.error.HTTPError as e:
            resp = e  # 302 (success) and 401 (failure) both arrive here: no redirect is followed
        except (urllib.error.URLError, OSError) as e:
            raise FourCatError(f"4CAT unreachable: {type(e).__name__}") from None
        try:
            cookies = resp.headers.get_all("Set-Cookie") or []
            status = getattr(resp, "status", None) or getattr(resp, "code", None)
        finally:
            resp.close()
        for line in cookies:
            c = SimpleCookie()
            try:
                c.load(line)
            except Exception:
                continue
            if "remember_token" in c and c["remember_token"].value:
                return c["remember_token"].value
        if status == 401 or status in (200, 302):
            raise LoginFailed(f"login refused for {user}")
        raise FourCatError(f"unexpected login response status {status}")

    def write_token(self, user, token):
        path = self.token_path(user)
        self._write(path, token)
        if os.geteuid() == 0:
            os.chown(path, 1000, 1000)
        return path

    def rotate(self, user):
        pw = secrets.token_urlsafe(24)
        self.set_password(user, pw)
        self._write(self.password_path(user), pw)  # DB already changed: the file must follow
        token = self.login_token(user, pw)
        self.write_token(user, token)
