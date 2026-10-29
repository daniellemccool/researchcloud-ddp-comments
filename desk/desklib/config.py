"""The desk's configuration file, <desk_root>/desk.json (stdlib only)."""
import json
import os
import re
import tempfile
from dataclasses import asdict, dataclass, field, fields

ROLE_RE = re.compile(r"^src_co_[A-Za-z0-9_-]+$")
GATE_MODES = ("roles", "api")


def _default_max_running() -> int:
    return max(1, (os.cpu_count() or 2) - 1)


@dataclass
class DeskConfig:
    desk_root: str
    repo_dir: str
    platform: str
    collector_role: str
    admin_role: str
    contact: str
    batch_sizes: list[int] = field(default_factory=lambda: [5, 10])
    idle_seconds: int = 900
    timer_minutes: int = 3
    max_sessions: int = 99
    max_running: int = field(default_factory=_default_max_running)
    hide_media: bool = True
    gate_mode: str = "roles"
    fourcat_enabled: bool = True
    fourcat_url: str = "http://127.0.0.1:8080"
    fourcat_backend: str = "4cat_backend"
    fourcat_db: str = "4cat_db"
    zeeschuimer_version: str = "1.14.2"
    mitm_image: str = "mitmproxy/mitmproxy:12.2.3"
    ff_image: str = "jlesage/firefox:v26.08.3"
    stub_port: int = 8110
    layout_version: int = 1
    package_version: str = ""
    template_path: str | None = None  # tar the `template` step untars; None -> "not ready" page
    fourcat_upstream: str = "http://frontend:5000"  # what the proxy forwards fourcat.desk to

    @property
    def platforms_dir(self) -> str:
        return os.path.join(self.repo_dir, "platforms")

    def validate(self) -> None:
        for name in ("collector_role", "admin_role"):
            value = getattr(self, name)
            if not isinstance(value, str) or not ROLE_RE.match(value):
                raise ValueError(f"{name}: {value!r} must match {ROLE_RE.pattern}")
        if not os.path.isdir(os.path.join(self.platforms_dir, self.platform)):
            raise ValueError(f"platform {self.platform!r}: no directory under {self.platforms_dir}")
        if (not self.batch_sizes
                or not all(isinstance(n, int) and not isinstance(n, bool) and n > 0
                           for n in self.batch_sizes)):
            raise ValueError(f"batch_sizes: {self.batch_sizes!r} must be positive integers")
        if self.gate_mode not in GATE_MODES:
            raise ValueError(f"gate_mode: {self.gate_mode!r} must be one of {GATE_MODES}")


def _path(desk_root: str) -> str:
    return os.path.join(desk_root, "desk.json")


def load_config(desk_root: str) -> DeskConfig:
    with open(_path(desk_root)) as f:
        data = json.load(f)
    known = {f.name for f in fields(DeskConfig)}
    return DeskConfig(**{k: v for k, v in data.items() if k in known})


def save_config(cfg: DeskConfig) -> None:
    os.makedirs(cfg.desk_root, exist_ok=True)
    fd, tmp = tempfile.mkstemp(dir=cfg.desk_root, prefix=".desk.json.")
    try:
        with os.fdopen(fd, "w") as f:
            json.dump(asdict(cfg), f, indent=2, sort_keys=True)
            f.write("\n")
        os.chmod(tmp, 0o644)
        os.replace(tmp, _path(cfg.desk_root))
    except BaseException:
        try:
            os.unlink(tmp)
        except FileNotFoundError:
            pass
        raise
