"""First-visit provisioning: reconcile one collector toward "ready" (stdlib only).

Each step is idempotent; state/users/<user>/provision.json records which are
done and is rewritten atomically after every step, so a crash or a retry
resumes at the first step not done (an errored step is retried too).

Locks: the per-user lock is held for a whole run; the global render lock only
inside the `registry` and `render` steps (and retire's registry write and
re-render), never while waiting on 4CAT or Docker. Order is user -> render.

Secrets: a 4CAT password or token is never written to provision.json or put
in an error text; FourCatError messages are secret-free by contract, and an
unexpected exception is recorded by type name only.
"""
import json
import os
import shutil
import subprocess
import tarfile
import tempfile
import time
from dataclasses import asdict, dataclass, field
from typing import Callable

from .config import DeskConfig
from .fourcat import FourCat, FourCatError
from .locks import flocked, render_lock, user_lock
from .registry import USER_RE, Registry, RegistryFull
from .render import _chown_tree, install_nginx, write_all

STEPS = ("registry", "dirs", "template", "fourcat_account", "fourcat_token", "render", "up")


@dataclass
class State:
    steps: dict[str, str] = field(default_factory=dict)  # step -> "done" | "error: <text>"
    applied_generation: int = 0
    updated: int = 0

    def current(self) -> str | None:
        return next((s for s in STEPS if self.steps.get(s) != "done"), None)

    def ready(self) -> bool:
        return self.current() is None

    def error(self) -> str | None:
        v = self.steps.get(self.current() or "", "")
        return v if v.startswith("error: ") else None


class NeedsAdopt(RuntimeError):
    """4CAT account exists but the desk has no password for it."""


class StepFailed(RuntimeError):
    """A step's expected failure; its message is recorded verbatim."""


def _check(user):
    if not isinstance(user, str) or not USER_RE.match(user):
        raise ValueError(f"invalid username: {user!r}")
    return user


def _describe(e: Exception) -> str:
    if isinstance(e, (StepFailed, NeedsAdopt, FourCatError, OSError, tarfile.TarError)):
        return str(e) or type(e).__name__
    if isinstance(e, RegistryFull):
        return "registry full"
    if isinstance(e, subprocess.CalledProcessError):
        cmd = e.cmd if isinstance(e.cmd, (list, tuple)) else [str(e.cmd)]
        return f"{' '.join(map(str, cmd[:2]))} exited with status {e.returncode}"
    return type(e).__name__  # the message of an unknown error might carry anything


def _rm(path):
    try:
        os.remove(path)
    except FileNotFoundError:
        pass


class Provisioner:
    ca_timeout = 60.0  # seconds `up` waits for mitmproxy's CA; instance-overridable for tests
    ca_poll = 0.5
    cmd_timeout = 300.0  # seconds for each docker compose / docker stop

    def __init__(self, cfg: DeskConfig, reg: Registry, fourcat: FourCat | None,
                 is_admin: Callable[[str], bool], members: Callable[[], dict[str, bool]] | None = None,
                 run=subprocess.run, renderer=write_all, nginx=install_nginx):
        self.cfg, self.reg, self.fourcat, self.is_admin = cfg, reg, fourcat, is_admin
        self.members, self.run, self.renderer, self.nginx = members, run, renderer, nginx

    def _dir(self, *parts):
        return os.path.join(self.cfg.desk_root, "state", *parts)

    def _state_path(self, user):
        return self._dir("users", _check(user), "provision.json")

    # --- state ---------------------------------------------------------

    def state(self, user) -> State:
        path = self._state_path(user)  # validates the name (outside the ValueError handler)
        try:
            with open(path) as f:
                data = json.load(f)
        except FileNotFoundError:
            return State()
        except ValueError:
            return State()  # unreadable: every step is idempotent, so start over
        steps = data.get("steps") if isinstance(data, dict) else None
        if not isinstance(steps, dict):
            return State()  # wrong shape: treated like unparseable JSON
        steps = {k: v for k, v in steps.items() if k in STEPS and isinstance(v, str)}
        gen, upd = data.get("applied_generation"), data.get("updated")
        return State(steps, gen if isinstance(gen, int) else 0, upd if isinstance(upd, int) else 0)

    def _save(self, user, st: State) -> None:
        st.updated = int(time.time())
        path = self._state_path(user)
        d = os.path.dirname(path)
        os.makedirs(d, exist_ok=True)
        fd, tmp = tempfile.mkstemp(dir=d, prefix=".provision.json.")
        try:
            with os.fdopen(fd, "w") as f:
                json.dump(asdict(st), f, indent=2, sort_keys=True)
                f.write("\n")
            os.chmod(tmp, 0o644)
            os.replace(tmp, path)
        except BaseException:
            _rm(tmp)
            raise

    # --- public operations ---------------------------------------------

    def advance(self, user) -> State:
        with flocked(user_lock(self.cfg.desk_root, user)):
            st = self.state(user)
            for step in STEPS:
                if st.steps.get(step) == "done":
                    continue
                try:
                    getattr(self, f"_step_{step}")(user)
                except Exception as e:
                    st.steps[step] = f"error: {_describe(e)}"
                    self._save(user, st)
                    return st
                st.steps[step] = "done"
                self._save(user, st)
            return st

    def repair(self, user) -> State:
        with flocked(user_lock(self.cfg.desk_root, user)):
            st = self.state(user)
            st.steps = {k: v for k, v in st.steps.items() if v == "done"}
            self._save(user, st)
        return self.advance(user)

    def retire(self, user) -> None:
        _check(user)
        with flocked(user_lock(self.cfg.desk_root, user)):
            for c in (f"desk-firefox-{user}", f"desk-mitm-{user}"):
                # non-zero (already gone) is fine; a hang raises TimeoutExpired
                self.run(["docker", "stop", c], capture_output=True, timeout=self.cmd_timeout)
            # Move the state before freeing the row: a crash in between must not
            # leave "ready" state for a user without a live row.
            users_dir = self._dir("users", user)
            src = os.path.join(users_dir, "provision.json")
            if os.path.exists(src):
                dest = self._dir("retired", user)
                os.makedirs(dest, exist_ok=True)
                os.replace(src, os.path.join(dest, "provision.json"))
            shutil.rmtree(users_dir, ignore_errors=True)
            with flocked(render_lock(self.cfg.desk_root)):
                self.reg.retire(user)
                self.renderer(self.cfg, self.reg, self._members())
                self.nginx(self.cfg)

    # --- steps -----------------------------------------------------------

    def _members(self):
        """Called under the render lock, right before rendering: an older snapshot
        must never overwrite a newer map (a revocation would briefly come undone)."""
        return self.members() if self.cfg.gate_mode == "api" and self.members else None

    def _fourcat_on(self):
        return self.fourcat is not None and self.cfg.fourcat_enabled

    def _step_registry(self, user):
        with flocked(render_lock(self.cfg.desk_root)):
            self.reg.allocate(user)

    def _step_dirs(self, user):
        for kind in ("mitm", "firefox", "captures"):
            d = self._dir(kind, user)
            os.makedirs(d, exist_ok=True)
            if os.geteuid() == 0:
                os.chown(d, 1000, 1000)

    def _step_template(self, user):
        ff = self._dir("firefox", user)
        staging = os.path.join(ff, ".template.tmp")
        shutil.rmtree(staging, ignore_errors=True)  # left by a crashed run, even after the rename
        if os.path.isdir(os.path.join(ff, "profile")):
            return
        tpl = self.cfg.template_path
        if not tpl or not os.path.isfile(tpl):
            raise StepFailed("template missing")
        with tarfile.open(tpl) as tar:
            prof, rest = [], []
            for m in tar.getmembers():
                name = m.name[2:] if m.name.startswith("./") else m.name
                (prof if name.split("/")[0] == "profile" else rest).append(m)
            if not prof:
                raise StepFailed("template has no profile/")
            # profile/ is published last, by rename: its presence means "templated".
            tar.extractall(ff, members=rest, filter="data")
            tar.extractall(staging, members=prof, filter="data")
        os.rename(os.path.join(staging, "profile"), os.path.join(ff, "profile"))
        shutil.rmtree(staging)
        if os.geteuid() == 0:
            _chown_tree(ff)

    def _step_fourcat_account(self, user):
        if not self._fourcat_on():
            return
        fc, admin = self.fourcat, bool(self.is_admin(user))
        pw, created_now = fc.ensure_password(user)
        try:
            exists = fc.account_exists(user)
            if exists and created_now:
                raise NeedsAdopt(f"4CAT account exists; run desk-admin 4cat-user adopt {user}")
        except BaseException:
            if created_now:  # a retry must not mistake someone else's account for ours
                _rm(fc.password_path(user))
            raise
        if not exists:
            fc.create_account(user, pw, admin)
        fc.set_admin(user, admin)

    def _step_fourcat_token(self, user):
        if not self._fourcat_on():
            return
        fc = self.fourcat
        try:
            with open(fc.password_path(user)) as f:
                pw = f.read().strip()
        except FileNotFoundError:
            raise StepFailed("no 4CAT password file") from None
        if not pw:
            raise StepFailed("4CAT password file is empty")
        fc.write_token(user, fc.login_token(user, pw))

    def _step_render(self, user):
        with flocked(render_lock(self.cfg.desk_root)):
            self.renderer(self.cfg, self.reg, self._members())
            self.nginx(self.cfg)

    def _step_up(self, user):
        self._compose_up(f"mitm-{user}")
        ca = self._dir("mitm", user, "mitmproxy-ca-cert.pem")
        deadline = time.monotonic() + self.ca_timeout
        while not os.path.isfile(ca):
            if time.monotonic() >= deadline:
                raise StepFailed("mitm produced no CA")
            time.sleep(self.ca_poll)
        self._compose_up(f"firefox-{user}")

    def _compose_up(self, service):
        cmd = ["docker", "compose", "-f", os.path.join(self.cfg.desk_root, "docker-compose.yml"),
               "up", "-d", service]
        try:
            self.run(cmd, check=True, capture_output=True, timeout=self.cmd_timeout)
        except subprocess.TimeoutExpired:
            raise StepFailed("docker compose timed out") from None
        except subprocess.CalledProcessError as e:
            raise StepFailed(f"docker compose up {service} exited with status {e.returncode}") from None
