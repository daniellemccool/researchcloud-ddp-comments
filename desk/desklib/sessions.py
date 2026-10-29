"""Operations on the desk's sessions, shared by the desk-sessions command and the admin page.

No argparse and no printing here. A failed docker call or 4CAT step is reported
through return values (`retire` returns an error text, `prune` fills `self.failed`).
"""
import subprocess
from typing import Callable

from .config import DeskConfig
from .fourcat import FourCat, FourCatError
from .locks import flocked, render_lock
from .provision import Provisioner, State
from .registry import Registry
from .render import write_all


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
        with flocked(render_lock(self.cfg.desk_root)):
            members = self.members() if self.cfg.gate_mode == "api" else None
            written = write_all(self.cfg, self.reg, members)
            self.prov.nginx(self.cfg)
        return written

    def _compose_up(self, user) -> None:
        self.run(["docker", "compose", "-f", f"{self.cfg.desk_root}/docker-compose.yml", "up", "-d",
                  f"mitm-{user}", f"firefox-{user}"],
                 check=True, capture_output=True, timeout=self.prov.cmd_timeout)

    def up(self, user=None) -> None:
        for u in ([user] if user else sorted(self.reg.live())):
            self._compose_up(u)

    def stop(self, user) -> None:
        """Stop the firefox container only; mitm stays, as the idle reaper does."""
        self.run(["docker", "stop", f"desk-firefox-{user}"], timeout=120, capture_output=True)

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
        return None

    def prune(self) -> list[str]:
        """Retire live users who are no longer members; failures land in self.failed."""
        members = self.members()
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

    def sync_admins(self) -> dict:
        """Make each live member's 4CAT admin tag match the CO roles; rotate on demotion."""
        out = {"admin_set": [], "admin_cleared": []}
        if not self._fourcat_on():
            return out
        members = self.members()
        for u in sorted(self.reg.live()):
            if u not in members:
                continue
            want = bool(members[u])
            try:
                if bool(self.fourcat.is_admin(u)) == want:
                    continue
                self.fourcat.set_admin(u, want)
                if not want:
                    self.fourcat.rotate(u)
            except (FourCatError, subprocess.SubprocessError, OSError) as e:
                self.failed[u] = str(e) or type(e).__name__
                continue
            out["admin_set" if want else "admin_cleared"].append(u)
        return out

    def reconcile(self) -> dict:
        retired = self.prune()
        failed = dict(self.failed)
        out = {"retired": retired, **self.sync_admins()}
        failed.update(self.failed)
        if failed:
            out["failed"] = failed
        return out

    def status(self) -> list[dict]:
        running = self.running()
        rows = []
        for u, row in sorted(self.reg.live().items()):
            st = self.prov.state(u)
            rows.append({"user": u, "index": row.index, "running": running.get(u, False),
                         "step": st.current(), "error": st.error(), "ready": st.ready()})
        return rows
