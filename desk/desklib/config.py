"""The desk's configuration file, <desk_root>/desk.json (stdlib only)."""
import json
import os
import re
import tempfile
import urllib.parse
from dataclasses import asdict, dataclass, field, fields

# Every value below is interpolated into a rendered file (nginx maps, the Compose file, the Firefox
# policy), so each is held to a charset that cannot break out of its slot. Always fullmatch: `$`
# alone would let a trailing newline through.
ROLE_RE = re.compile(r"src_co_[A-Za-z0-9_-]+")
GATE_MODES = ("roles", "api")
NETLOC_RE = re.compile(r"[A-Za-z0-9._:-]+")
VERSION_RE = re.compile(r"[0-9A-Za-z][0-9A-Za-z._-]*")
PLATFORM_RE = re.compile(r"[a-z0-9-]+")
IMAGE_RE = re.compile(r"[A-Za-z0-9._/:@-]+")
REPO_RE = re.compile(r"/[A-Za-z0-9._/-]*")
PRINTABLE_RE = re.compile(r"[\x21-\x7e]+")    # no whitespace or control characters at all
INT_FIELDS = ("idle_seconds", "timer_minutes", "max_sessions", "max_running", "stub_port", "layout_version")
BOOL_FIELDS = ("hide_media", "fourcat_enabled")


def _is_int(v) -> bool:
    return isinstance(v, int) and not isinstance(v, bool)


def _check_url(name: str, value) -> None:
    """http(s)://host[:port] with at most a "/" path. The raw text is checked too: urlsplit
    silently drops tabs and newlines, and the raw value is what gets rendered."""
    bad = ValueError(f"{name}: {value!r} must be http(s)://host[:port] with no path, query or credentials")
    if not isinstance(value, str) or not PRINTABLE_RE.fullmatch(value):
        raise bad
    try:
        u = urllib.parse.urlsplit(value)
        u.port  # noqa: B018  (raises ValueError on a non-numeric or out-of-range port)
    except ValueError:
        raise bad from None
    if (u.scheme not in ("http", "https") or not NETLOC_RE.fullmatch(u.netloc)
            or u.path not in ("", "/") or u.query or u.fragment):
        raise bad


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
        """Raise ValueError on any value that is malformed or could break out of a rendered file.

        Called by load_config and again by every renderer, so a hand-edited desk.json or a
        directly constructed config fails loudly before anything is written."""
        def check(name, regex, what=None):
            value = getattr(self, name)
            if not isinstance(value, str) or not regex.fullmatch(value):
                raise ValueError(f"{name}: {value!r} must match {what or regex.pattern}")
        check("collector_role", ROLE_RE)
        check("admin_role", ROLE_RE)
        check("platform", PLATFORM_RE)          # before the path join: no ../ traversal
        check("repo_dir", REPO_RE, "an absolute path of [A-Za-z0-9._/-]")
        check("zeeschuimer_version", VERSION_RE)
        check("mitm_image", IMAGE_RE)
        check("ff_image", IMAGE_RE)
        _check_url("fourcat_url", self.fourcat_url)
        _check_url("fourcat_upstream", self.fourcat_upstream)
        if not isinstance(self.contact, str) or any(c in self.contact for c in "<>\r\n"):
            raise ValueError(f"contact: {self.contact!r} must be text without <, > or line breaks")
        for name in INT_FIELDS:
            if not _is_int(getattr(self, name)):
                raise ValueError(f"{name}: {getattr(self, name)!r} must be an integer")
        for name in BOOL_FIELDS:
            if not isinstance(getattr(self, name), bool):
                raise ValueError(f"{name}: {getattr(self, name)!r} must be true or false")
        if not 1 <= self.stub_port <= 65535:
            raise ValueError(f"stub_port: {self.stub_port!r} must be a TCP port")
        if not os.path.isdir(os.path.join(self.platforms_dir, self.platform)):
            raise ValueError(f"platform {self.platform!r}: no directory under {self.platforms_dir}")
        if (not isinstance(self.batch_sizes, list) or not self.batch_sizes
                or not all(_is_int(n) and n > 0 for n in self.batch_sizes)):
            raise ValueError(f"batch_sizes: {self.batch_sizes!r} must be positive integers")
        if self.gate_mode not in GATE_MODES:
            raise ValueError(f"gate_mode: {self.gate_mode!r} must be one of {GATE_MODES}")


def _path(desk_root: str) -> str:
    return os.path.join(desk_root, "desk.json")


def load_config(desk_root: str, validate: bool = True) -> DeskConfig:
    """Load and (by default) validate desk.json. validate=False is for `init` only, which
    must be able to load a broken file in order to repair it (it validates before saving)."""
    with open(_path(desk_root)) as f:
        data = json.load(f)
    known = {f.name for f in fields(DeskConfig)}
    cfg = DeskConfig(**{k: v for k, v in data.items() if k in known})
    if validate:
        cfg.validate()
    return cfg


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
