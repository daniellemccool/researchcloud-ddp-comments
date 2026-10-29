"""Operations on the desk's sessions, shared by the desk-sessions command and the admin page.

No argparse and no printing here. A failed docker call or 4CAT step is reported
through return values (`retire` returns an error text, `prune` fills `self.failed`).

Locks: `up` and the rotate in `sync_admins` hold the user's lock, `render` the render lock,
and retire goes through the provisioner (user -> render). None of them is called with a
lock already held: flocked() on a lock this call chain holds would deadlock. `start_pair`
takes no lock: its callers (`up`, the stub's ensure_started) hold the user's lock.
"""
import os
import subprocess
import sys
from typing import Callable

from .coapi import guarded_members
from .config import DeskConfig
from .fourcat import FourCat, FourCatError
from .locks import flocked, render_lock, user_lock
from .provision import Provisioner, State
from .registry import Registry
from .render import drop_legacy_policy, write_all


class Sessions:
    def __init__(self, cfg: DeskConfig, reg: Registry, prov: Provisioner, fourcat: FourCat | None,
                 members: Callable[[], dict[str, bool]], run=subprocess.run):
        self.cfg, self.reg, self.prov, self.fourcat = cfg, reg, prov, fourcat
        self.members, self.run = members, run
        self.failed: dict[str, str] = {}  # user -> why, from the last prune/sync_admins

    def _fourcat_on(self) -> bool:
        return self.fourcat is not None and self.cfg.fourcat_enabled

    def add(self, user) -> State:
        return self.prov.advance(user)

    def repair(self, user) -> State:
        return self.prov.repair(user)

    def render(self) -> list[str]:
        """Re-render under the render lock. In api mode the members are fetched here, under the
        lock (an older snapshot must never overwrite a newer map), and an empty answer while
        sessions exist is refused like everywhere else: it would revoke every member at once."""
        with flocked(render_lock(self.cfg.desk_root)):
            members = self._current_members() if self.cfg.gate_mode == "api" else None
            written = write_all(self.cfg, self.reg, members)
            self.prov.nginx(self.cfg)
        return written

    def start_pair(self, user, timeout: float | None = None) -> None:
        """`docker compose up -d --no-deps --pull never mitm-<u> firefox-<u>` (R33), then drop the
        user's legacy single-file policy once nothing can mount it any more.

        Compose recreates a container whose rendered spec drifted (a mount added, an image bumped)
        and only starts an unchanged one, which `docker start` never does. `--pull never`: images
        are pulled at install, a start must not reach the registry.

        Refuses (OSError) unless firefox/policy/<u>/policies.json is a regular file: a Compose file
        that mounts the directory would hand Firefox an empty one, i.e. no locked proxy policy.
        The legacy firefox/policies-<u>.json goes only when the Compose file just used no longer
        references it (a pre-R29 file bind-mounts it, and the next start would get an empty source);
        a failure dropping it is logged and harmless. Takes no lock: the caller holds the user's
        lock. Raises CalledProcessError or TimeoutExpired on a failed `up`."""
        root = self.cfg.desk_root
        policy = os.path.join(root, "firefox", "policy", user, "policies.json")
        if os.path.islink(policy) or not os.path.isfile(policy):
            raise FileNotFoundError(f"{policy} is not a regular file; render first, not starting {user}")
        compose = os.path.join(root, "docker-compose.yml")
        self.run(["docker", "compose", "-f", compose, "up", "-d", "--no-deps", "--pull", "never",
                  f"mitm-{user}", f"firefox-{user}"],
                 check=True, capture_output=True, timeout=self.prov.cmd_timeout if timeout is None else timeout)
        try:
            with open(compose) as f:
                still_mounted = f"policies-{user}.json" in f.read()
            if not still_mounted:
                drop_legacy_policy(root, user)
        except OSError as e:
            print(f"desk: could not drop the legacy policy of {user}: {e}", file=sys.stderr)

    def up(self, user=None) -> dict[str, str]:
        """start_pair under each user's lock, so it never races a retire or a provisioning run.

        The row is checked again once the lock is held: a user retired while we waited is skipped
        (never restarted) and reported in the returned {user: reason}. A user whose start fails
        lands in self.failed ({user: why}) and the others still go up."""
        skipped = {}
        self.failed = {}
        for u in ([user] if user else sorted(self.reg.live())):
            with flocked(user_lock(self.cfg.desk_root, u)):
                if u not in self.reg.live():
                    skipped[u] = "no live session (retired, or never added)"
                    continue
                try:
                    self.start_pair(u)
                except subprocess.CalledProcessError as e:
                    self.failed[u] = f"docker exited with status {e.returncode}"
                except subprocess.TimeoutExpired:
                    self.failed[u] = "docker timed out"
                except OSError as e:
                    self.failed[u] = str(e) or type(e).__name__
        return skipped

    def stop(self, user) -> bool:
        """Stop the firefox container only; mitm stays, as the idle reaper does.
        True when docker stop succeeded; False on a non-zero exit or a timeout."""
        try:
            r = self.run(["docker", "stop", f"desk-firefox-{user}"], timeout=120, capture_output=True)
        except subprocess.TimeoutExpired:
            return False
        return r.returncode == 0

    def running(self) -> dict[str, bool]:
        out = {}
        for u in self.reg.live():
            r = self.run(["docker", "inspect", "-f", "{{.State.Running}}", f"desk-firefox-{u}"],
                         capture_output=True, text=True, timeout=30)
            out[u] = r.returncode == 0 and r.stdout.strip() == "true"
        return out

    def retire(self, user) -> str | None:
        """Retire one user; None on success, else a short failure text."""
        try:
            self.prov.retire(user)
        except subprocess.TimeoutExpired:
            return "docker timed out"
        except subprocess.CalledProcessError as e:
            return f"docker exited with status {e.returncode}"
        except RuntimeError as e:  # "could not stop <name>; retire aborted": the row stays live
            return str(e) or type(e).__name__
        return None

    def _current_members(self) -> dict[str, bool]:
        """One CO fetch per operation; an empty answer while sessions exist is not believed."""
        return guarded_members(self.members, len(self.reg.live()))

    def prune(self, members: dict[str, bool] | None = None) -> list[str]:
        """Retire live users who are no longer members; failures land in self.failed."""
        if members is None:
            members = self._current_members()
        self.failed = {}
        retired = []
        for u in sorted(self.reg.live()):
            if u in members:
                continue
            err = self.retire(u)
            if err:
                self.failed[u] = err
            else:
                retired.append(u)
        return retired

    def sync_admins(self, members: dict[str, bool] | None = None) -> dict:
        """Make each ready member's 4CAT admin tag match the CO roles.

        On demotion the credentials rotate first and the tag is cleared after, so a
        failed rotate leaves the tag for the next run to retry. Users whose 4CAT
        account step is not done yet are listed under `skipped` (only when any).
        """
        out = {"admin_set": [], "admin_cleared": []}
        if not self._fourcat_on():
            return out
        if members is None:
            members = self._current_members()
        skipped = []
        for u in sorted(self.reg.live()):
            if u not in members:
                continue
            if self.prov.state(u).steps.get("fourcat_account") != "done":
                skipped.append(u)
                continue
            want = bool(members[u])
            self.failed.pop(u, None)
            try:
                if bool(self.fourcat.is_admin(u)) == want:
                    continue
                with flocked(user_lock(self.cfg.desk_root, u)):  # rotate rewrites the token files
                    if not want:
                        self.fourcat.rotate(u)
                    self.fourcat.set_admin(u, want)
            except (FourCatError, subprocess.SubprocessError, OSError) as e:
                self.failed[u] = str(e) or type(e).__name__
                continue
            out["admin_set" if want else "admin_cleared"].append(u)
        if skipped:
            out["skipped"] = skipped
        return out

    def reconcile(self) -> dict:
        """prune, sync_admins, then always a re-render: role changes must reach the nginx maps
        even when nobody was retired (render() reads the members again, under the render lock)."""
        members = self._current_members()
        retired = self.prune(members)
        failed = dict(self.failed)
        out = {"retired": retired, **self.sync_admins(members)}
        failed.update(self.failed)
        if failed:
            out["failed"] = failed
        self.render()
        return out

    def status(self) -> list[dict]:
        running = self.running()
        rows = []
        for u, row in sorted(self.reg.live().items()):
            st = self.prov.state(u)
            rows.append({"user": u, "index": row.index, "running": running.get(u, False),
                         "step": st.current(), "error": st.error(), "ready": st.ready()})
        return rows
