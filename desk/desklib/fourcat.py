"""Per-user 4CAT accounts, passwords and remember_tokens, via `docker exec` and HTTP (stdlib only)."""
import json
import os
import re
import secrets
import stat
import subprocess
import time
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
_HASH_RE = re.compile(r"^\$2[aby]\$\d\d\$[./A-Za-z0-9]{53}\Z")
_HASH_PY = ("import bcrypt,sys;print(bcrypt.hashpw(sys.stdin.read().strip().encode(),"
            "bcrypt.gensalt()).decode())")


def _check(user):
    if not isinstance(user, str) or not USER_RE.fullmatch(user):
        raise ValueError(f"invalid username: {user!r}")
    return user


# The proxy mounts secrets/4cat-tokens/<user>/ at /run/desk and reads /run/desk/4cat-token
# (DESK_FOURCAT_TOKEN_FILE). A directory mount, so a token replaced by rename is seen at once.
TOKEN_NAME = "4cat-token"
PROXY_UID = 1000
TMP_MAX_AGE = 600  # seconds; a `_write` temp file older than this was left by a crash


def _secret_dir(desk_root, sub):
    d = desk_root
    for part in ("secrets", sub):
        d = os.path.join(d, part)
        os.makedirs(d, mode=0o700, exist_ok=True)
        os.chmod(d, 0o700)  # defeat the umask
    return d


def token_dir(desk_root, user):
    """secrets/4cat-tokens/<user>/, created: mode 0750, owned by the proxy's uid when we are root."""
    d = os.path.join(_secret_dir(desk_root, "4cat-tokens"), _check(user))
    os.makedirs(d, mode=0o750, exist_ok=True)
    os.chmod(d, 0o750)
    if os.geteuid() == 0:
        os.chown(d, PROXY_UID, PROXY_UID)
    return d


def migrate_legacy_token(desk_root, user) -> bool:
    """Move a single-file token (secrets/4cat-tokens/<user>, the old layout) into <user>/4cat-token.

    Idempotent and crash-safe: the file is first renamed aside to a name no user can have, then
    published 0600 (and owned by the proxy's uid when root). True if this call moved or dropped a
    legacy token. Two callers may race (a render under the render lock, a write_token under the
    user lock): if the file vanishes under us the other one migrated it, which is success too."""
    tokens = os.path.join(desk_root, "secrets", "4cat-tokens")
    legacy, aside = os.path.join(tokens, _check(user)), os.path.join(tokens, f".{user}.legacy-token")
    try:
        if os.path.isfile(legacy) and not os.path.islink(legacy):
            os.replace(legacy, aside)
        if not os.path.isfile(aside):
            return False
        dest = os.path.join(token_dir(desk_root, user), TOKEN_NAME)
        if os.path.exists(dest):
            os.remove(aside)  # a newer token was written since
        else:
            os.chmod(aside, 0o600)
            if os.geteuid() == 0:
                os.chown(aside, PROXY_UID, PROXY_UID)
            os.replace(aside, dest)
    except FileNotFoundError:
        return False  # the concurrent caller got there first: already migrated
    return True


def _sweep_tmp(d, now=None) -> None:
    """Remove `*.tmp` regular files older than TMP_MAX_AGE in d (crashed `_write`s); a younger
    one may belong to a write in flight and is left alone."""
    try:
        names = os.listdir(d)
    except (FileNotFoundError, NotADirectoryError):
        return
    cutoff = (time.time() if now is None else now) - TMP_MAX_AGE
    for name in names:
        if not name.endswith(".tmp"):
            continue
        p = os.path.join(d, name)
        try:
            st = os.lstat(p)
            if stat.S_ISREG(st.st_mode) and st.st_mtime < cutoff:
                os.remove(p)
        except FileNotFoundError:
            pass


class FourCat:
    def __init__(self, cfg, run=subprocess.run, urlopen=urllib.request.urlopen,
                 db_user="fourcat", db_name="fourcat"):
        self.cfg, self._run, self.db_user, self.db_name = cfg, run, db_user, db_name
        self._open = _NOREDIR_OPEN if urlopen is urllib.request.urlopen else urlopen

    def password_path(self, user):
        return os.path.join(self.cfg.desk_root, "secrets", "4cat-passwords", _check(user))

    def token_path(self, user):
        return os.path.join(self.cfg.desk_root, "secrets", "4cat-tokens", _check(user), TOKEN_NAME)

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
        """ON_ERROR_STOP: a failed statement exits non-zero (3) instead of 0 with empty output,
        so a permissions or schema error raises FourCatError rather than reading as "no account"."""
        return self._exec(self.cfg.fourcat_db, "psql", "-v", "ON_ERROR_STOP=1", "-U", self.db_user,
                          "-d", self.db_name, "-tAc", sql, what="psql").strip()

    def _sweep(self, user):
        """The two secrets directories a `_write` for `user` uses: the passwords and the user's tokens."""
        root = self.cfg.desk_root
        _sweep_tmp(os.path.join(root, "secrets", "4cat-passwords"))
        _sweep_tmp(os.path.join(root, "secrets", "4cat-tokens", _check(user)))

    def _write(self, path, text, exclusive=False, owner=None):
        """Write via a 0600 temp file in path's directory (which must exist); publish atomically.

        exclusive: os.link (FileExistsError if someone else won); else os.replace.
        owner: (uid, gid) given to the temp file before it is published."""
        tmp = f"{path}.{secrets.token_hex(4)}.tmp"
        fd = os.open(tmp, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
        try:
            with os.fdopen(fd, "w") as f:
                f.write(text)
            if owner is not None:
                os.chown(tmp, *owner)
            if exclusive:
                os.link(tmp, path)
            else:
                os.replace(tmp, path)
        finally:
            try:
                os.unlink(tmp)
            except FileNotFoundError:
                pass

    def ensure_password(self, user):
        path = self.password_path(user)
        self._sweep(user)
        for _ in range(2):
            try:
                with open(path) as f:
                    pw = f.read().strip()
                if not pw:
                    raise FourCatError(f"password file for {user} is empty")
                return pw, False
            except FileNotFoundError:
                pass
            pw = secrets.token_urlsafe(24)
            try:
                self._write_password(user, pw, exclusive=True)
                return pw, True
            except FileExistsError:
                continue  # lost a race: read the winner's file
        raise FourCatError("password file unreadable")

    def _update(self, user, sql):
        lines = self._psql(sql).splitlines()
        if not lines or lines[-1].strip() != "UPDATE 1":
            raise FourCatError(f"no 4CAT account for {user}")

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
        if not _HASH_RE.match(out):
            raise FourCatError("unexpected hash format")
        return out

    def set_password(self, user, password):
        h = self.hash_password(password)
        self._update(user, f"UPDATE users SET password = '{h}' WHERE name = '{_check(user)}'")

    def is_admin(self, user):
        out = self._psql(f"SELECT tags::text FROM users WHERE name = '{_check(user)}'")
        if not out:
            raise FourCatError(f"no 4CAT account for {user}")
        try:
            tags = json.loads(out)
        except ValueError:
            raise FourCatError("unparseable tags") from None
        if not isinstance(tags, list):  # a jsonb null or object: never a TypeError out of sync_admins
            raise FourCatError("unexpected tags value")
        return "admin" in tags

    def set_admin(self, user, admin):
        u = _check(user)
        if admin:
            sql = f"UPDATE users SET tags = (tags - 'admin') || '[\"admin\"]'::jsonb WHERE name = '{u}'"
        else:
            sql = f"UPDATE users SET tags = tags - 'admin' WHERE name = '{u}'"
        self._update(user, sql)

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

    def _write_password(self, user, pw, exclusive=False):
        _secret_dir(self.cfg.desk_root, "4cat-passwords")
        self._write(self.password_path(user), pw, exclusive=exclusive)

    def write_token(self, user, token):
        """<user>/4cat-token, 0600 and owned by the proxy's uid when root, replaced by rename."""
        migrate_legacy_token(self.cfg.desk_root, user)
        token_dir(self.cfg.desk_root, user)
        self._sweep(user)
        path = self.token_path(user)
        self._write(path, token, owner=(PROXY_UID, PROXY_UID) if os.geteuid() == 0 else None)
        return path

    def rotate(self, user):
        pw = secrets.token_urlsafe(24)
        self.set_password(user, pw)
        self._write_password(user, pw)  # DB already changed: the file must follow
        token = self.login_token(user, pw)
        self.write_token(user, token)
