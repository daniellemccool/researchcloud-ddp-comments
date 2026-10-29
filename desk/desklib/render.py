"""Render the desk's Compose file, nginx gate and Firefox policy (stdlib only).

Every per-user decision in nginx is a map read in the content phase: `if` and
`rewrite` run before auth_request, when $username is still empty. Anything not
allowed lands on the stub (403).
"""
import os
import shutil
import subprocess
import tempfile
import urllib.parse
from typing import Callable

from .config import DeskConfig
from .registry import USER_RE, Registry

MITM_COMMAND = ("mitmdump --set block_global=false --set connection_strategy=lazy "
                "--set stream_large_bodies=1m --set rawtcp=false --set body_size_limit=32m "
                "-s /addons/capture.py")
HEADER = "# Rendered by desklib.render. Do not edit; the desk re-renders this file."


def _stub(cfg: DeskConfig) -> str:
    return f"127.0.0.1:{cfg.stub_port}"


def _fourcat_hostport(cfg: DeskConfig) -> str:
    return urllib.parse.urlsplit(cfg.fourcat_url).netloc


# --- Compose ---------------------------------------------------------------

def _service_pair(cfg: DeskConfig, u: str, index: int, port: int, token: bool) -> str:
    fc = cfg.fourcat_enabled
    env = [f"DESK_USER={u}", f"DESK_SESSION={index}", "DESK_PLATFORM_JSON=/platform/platform.json",
           f"DESK_FOURCAT_ENABLED={1 if fc else 0}", f"DESK_FOURCAT_UPSTREAM={cfg.fourcat_upstream}",
           "DESK_FOURCAT_TOKEN_FILE=/run/desk/4cat-token", "DESK_CAPTURES=/captures"]
    vols = [f"{cfg.repo_dir}/desk/mitm/addons:/addons:ro",
            f"{cfg.repo_dir}/desk/desklib:/opt/desk/desklib:ro",
            "./platform:/platform:ro",
            f"./state/mitm/{u}:/home/mitmproxy/.mitmproxy",
            f"./state/captures/{u}:/captures/{u}"]
    if token:  # a directory: a token replaced by rename is visible to the running proxy
        vols.append(f"./secrets/4cat-tokens/{u}:/run/desk:ro")
    out = [f"  mitm-{u}:",
           f"    image: {cfg.mitm_image}",
           f"    container_name: desk-mitm-{u}",
           f"    command: {MITM_COMMAND}",
           "    volumes:", *(f"      - {v}" for v in vols),
           "    environment:", *(f"      - {e}" for e in env),
           "    networks:",
           f"      net-{u}:", "        aliases:", "          - mitm"]
    if fc:
        out.append("      desk-fourcat: {}")
    out += ["    restart: unless-stopped",
            f"  firefox-{u}:",
            f"    image: {cfg.ff_image}",
            f"    container_name: desk-firefox-{u}",
            f"    depends_on: [mitm-{u}]",
            "    ports:",
            f'      - "127.0.0.1:{port}:5800"',
            "    volumes:",
            f"      - ./state/firefox/{u}:/config",
            f"      - ./state/mitm/{u}:/mitm-ca:ro",
            f"      - ./firefox/policy/{u}:/usr/lib/firefox/distribution:ro",
            "    environment:",
            "      - DISPLAY_WIDTH=1280", "      - DISPLAY_HEIGHT=900", "      - WEB_AUDIO=0",
            "      - KEEP_APP_RUNNING=1", "      - DARK_MODE=0", "      - FF_OPEN_URL=about:blank",
            "      - USER_ID=1000", "      - GROUP_ID=1000",
            '    shm_size: "1g"',
            f"    networks: [net-{u}]",
            "    restart: unless-stopped"]
    return "\n".join(out)


def render_compose(cfg: DeskConfig, reg: Registry, token_exists: Callable[[str], bool]) -> str:
    cfg.validate()   # every interpolated value, however the config was built
    live = reg.live()
    parts = [HEADER, "name: desk"]
    if live:
        parts.append("services:")
        for u, row in live.items():
            parts.append(_service_pair(cfg, u, row.index, reg.port(u), bool(token_exists(u))))
    else:
        parts.append("services: {}")
    nets = [f"  net-{u}: {{}}" for u in live]
    if cfg.fourcat_enabled:
        nets += ["  desk-fourcat:", "    external: true"]
    if nets:
        parts += ["networks:", *nets]
    return "\n".join(parts) + "\n"


# --- nginx -----------------------------------------------------------------

def _map(key: str, var: str, default: str, entries: list[tuple[str, str]]) -> list[str]:
    return [f"map {key} {var} {{", f"    default {default};",
            *(f'    "{k}" {v};' for k, v in entries), "}"]


def render_maps(cfg: DeskConfig, reg: Registry, members: dict[str, bool] | None = None) -> str:
    """In api mode $desk_member/$desk_admin list every role-holder in `members`, registered
    or not (a first visit must reach the stub to be provisioned); the VNC maps stay live-only."""
    cfg.validate()   # the roles become regexes and the 4CAT netloc a map value
    live = reg.live()
    stub, fourcat = _stub(cfg), _fourcat_hostport(cfg)
    if cfg.gate_mode == "api":
        members = {u: a for u, a in (members or {}).items() if isinstance(u, str) and USER_RE.fullmatch(u)}
        member_map = _map("$username", "$desk_member", "0", [(u, "1") for u in members])
        admin_map = _map("$username", "$desk_admin", "0", [(u, "1") for u, a in members.items() if a])
    else:
        def rx(role):
            return (f"~(^|;){role}(;|$)", "1")
        member_map = _map("$src_roles", "$desk_member", "0", [rx(cfg.collector_role), rx(cfg.admin_role)])
        admin_map = _map("$src_roles", "$desk_admin", "0", [rx(cfg.admin_role)])
    lines = [f"# /etc/nginx/conf.d/desk-map.conf. {HEADER[2:]} http context.",
             "# $username and $src_roles are set by auth_request_set in each gated location;",
             "# maps are read lazily, after them.",
             *member_map, *admin_map,
             *_map('"$desk_member:$username"', "$desk_vnc", stub,
                   [(f"1:{u}", f"127.0.0.1:{reg.port(u)}") for u in live]),
             # $desk_target is set by the admin-vnc location's regex capture.
             *_map('"$desk_admin:$desk_target"', "$desk_admin_vnc", stub,
                   [(f"1:{u}", f"127.0.0.1:{reg.port(u)}") for u in live]),
             "map $desk_admin $fourcat_ui {", f"    default {stub};",
             *([f"    1 {fourcat};"] if cfg.fourcat_enabled else []), "}"]
    return "\n".join(lines) + "\n"


_GATE = """\
    error_page 401 = @custom_401;
    auth_request /validate;
    auth_request_set $username $upstream_http_username;
    auth_request_set $src_roles $upstream_http_src_co_roles;
    # Always overwrite every identity header: any location may fall through to the stub.
    proxy_set_header REMOTE_USER $username;
    proxy_set_header X-Desk-Roles $src_roles;
    proxy_set_header X-Desk-Member $desk_member;
    proxy_set_header X-Desk-Admin $desk_admin;
"""
_WS = """\
    proxy_http_version 1.1;
    proxy_set_header Upgrade $http_upgrade;
    proxy_set_header Connection $connection_upgrade;
    proxy_set_header Host $host;
    proxy_buffering off;
    proxy_read_timeout 86400s;
    proxy_send_timeout 86400s;
"""


def render_desk_conf(cfg: DeskConfig) -> str:
    return (f"# /etc/nginx/app-location-conf.d/desk.conf. {HEADER[2:]}\n"
            "# Needs conf.d/desk-map.conf and conf.d/websocket-map.conf.\n"
            "location /desk/ {\n" + _GATE +
            "    # NO trailing slash: the stub sees the full /desk/... path.\n"
            f"    proxy_pass http://{_stub(cfg)};\n"
            "    proxy_set_header Host $host;\n}\n"
            "location /desk/vnc/ {\n" + _GATE +
            "    rewrite ^/desk/vnc/(.*)$ /$1 break;\n"
            "    proxy_pass http://$desk_vnc;\n" + _WS + "}\n"
            "location ~ ^/desk/admin/vnc/([a-z][a-z0-9._-]*)/ {\n" + _GATE +
            "    set $desk_target $1;\n"
            "    rewrite ^/desk/admin/vnc/[^/]+/(.*)$ /$1 break;\n"
            "    proxy_pass http://$desk_admin_vnc;\n" + _WS + "}\n")


def render_fourcat_conf(cfg: DeskConfig) -> str:
    return (f"# /etc/nginx/app-location-conf.d/fourcat.conf. {HEADER[2:]}\n"
            "# 4CAT UI for admins only; uploads from the desk go through the proxy, not nginx.\n"
            "location / {\n" + _GATE +
            "    proxy_pass http://$fourcat_ui;\n"
            "    proxy_http_version 1.1;\n"
            "    proxy_set_header Host $host;\n"
            "    proxy_set_header X-Real-IP $remote_addr;\n"
            "    proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;\n"
            "    proxy_set_header X-Forwarded-Proto $scheme;\n"
            "    client_max_body_size 512m;\n"
            "    proxy_read_timeout 600s;\n}\n")


# --- Firefox policy --------------------------------------------------------

def render_policy(cfg: DeskConfig) -> str:
    cfg.validate()   # the version lands inside a JSON string: VERSION_RE keeps it there
    with open(os.path.join(cfg.repo_dir, "desk", "firefox", "policies.json")) as f:
        return f.read().replace("{{ZEESCHUIMER_VERSION}}", cfg.zeeschuimer_version)


# --- Writing ---------------------------------------------------------------

def _write_atomic(path: str, text: str) -> str:
    """Temp file + rename in path's directory. Every mounted file sits in a directory mount
    (never a single-file bind mount), so a running container sees the new inode."""
    d = os.path.dirname(path)
    os.makedirs(d, exist_ok=True)
    fd, tmp = tempfile.mkstemp(dir=d, prefix=".tmp.")
    try:
        with os.fdopen(fd, "w") as f:
            f.write(text)
        os.chmod(tmp, 0o644)
        os.replace(tmp, path)
    except BaseException:
        try:
            os.unlink(tmp)
        except FileNotFoundError:
            pass
        raise
    return path


def _copy_atomic(src: str, dst: str) -> str:
    """Copy src to dst by temp file + rename in dst's directory (0644): a reader under a
    directory mount sees the old file or the new one, never a half-written one."""
    fd, tmp = tempfile.mkstemp(dir=os.path.dirname(dst), prefix=".tmp.")
    try:
        with os.fdopen(fd, "wb") as out, open(src, "rb") as inp:
            shutil.copyfileobj(inp, out)
        os.chmod(tmp, 0o644)
        os.replace(tmp, dst)
    except BaseException:
        try:
            os.unlink(tmp)
        except FileNotFoundError:
            pass
        raise
    return dst


def _sync_dir(src: str, dest: str) -> None:
    """Make `dest` match `src` without replacing `dest` (it is bind-mounted into running proxies).
    Each file is replaced by rename inside dest; files src no longer has are deleted."""
    os.makedirs(dest, exist_ok=True)
    want = set()
    for dirpath, dirs, files in os.walk(src):
        rel = os.path.relpath(dirpath, src)
        os.makedirs(os.path.join(dest, rel), exist_ok=True)
        for name in files:
            r = os.path.normpath(os.path.join(rel, name))
            want.add(r)
            _copy_atomic(os.path.join(src, r), os.path.join(dest, r))
    for dirpath, dirs, files in os.walk(dest, topdown=False):
        rel = os.path.relpath(dirpath, dest)
        for name in files:
            if os.path.normpath(os.path.join(rel, name)) not in want:
                os.remove(os.path.join(dirpath, name))
        for name in dirs:
            p = os.path.join(dirpath, name)
            if not os.listdir(p):
                os.rmdir(p)


def _chown_tree(path: str) -> None:
    shutil.chown(path, 1000, 1000)
    for dirpath, dirs, files in os.walk(path):
        for name in dirs + files:
            shutil.chown(os.path.join(dirpath, name), 1000, 1000, follow_symlinks=False)


def drop_legacy_policy(root: str, user: str) -> None:
    """Remove firefox/policies-<user>.json, the single-file-mount layout (now firefox/policy/<user>/).

    Called only after that user's pair was (re)created by `compose up` (R33): until then an
    existing container may still have the old file bind-mounted."""
    if not isinstance(user, str) or not USER_RE.fullmatch(user):
        raise ValueError(f"invalid username: {user!r}")
    p = os.path.join(root, "firefox", f"policies-{user}.json")
    if os.path.lexists(p) and not os.path.isdir(p):
        try:
            os.remove(p)
        except FileNotFoundError:
            pass


def write_all(cfg: DeskConfig, reg: Registry, members: dict[str, bool] | None = None) -> list[str]:
    """Render everything. Order: token migration and the per-user policy and state directories
    first, the Compose file last, so every directory the Compose file mounts exists before any
    `compose up` can read it. Legacy firefox/policies-<u>.json files are left alone here."""
    from .fourcat import migrate_legacy_token
    from .platform import load_dir
    cfg.validate()   # before anything is written
    root = cfg.desk_root
    platform = load_dir(cfg.platforms_dir, cfg.platform)
    live = reg.live()
    for u in live:
        migrate_legacy_token(root, u)  # before the compose: its token mount needs the directory
    written = []
    policy = render_policy(cfg)
    css_src = os.path.join(platform.dir, "userContent.css")
    for u in live:
        written.append(_write_atomic(os.path.join(root, "firefox", "policy", u, "policies.json"), policy))
        dirs = [os.path.join(root, "state", kind, u) for kind in ("mitm", "firefox", "captures")]
        for d in dirs:
            os.makedirs(d, exist_ok=True)
        css = os.path.join(dirs[1], "profile", "chrome", "userContent.css")
        if not os.path.isdir(os.path.join(dirs[1], "profile")):
            pass  # not templated yet: a chrome/ dir here would defeat the "untar if empty" step
        elif cfg.hide_media and os.path.isfile(css_src):
            os.makedirs(os.path.dirname(css), exist_ok=True)
            written.append(_copy_atomic(css_src, css))
        elif not cfg.hide_media and os.path.exists(css):
            os.remove(css)
        if os.geteuid() == 0:
            for d in dirs:
                _chown_tree(d)
    dest = os.path.join(root, "platform")
    _sync_dir(platform.dir, dest)
    written.append(dest)
    written += [
        _write_atomic(os.path.join(root, "nginx", "desk-map.conf"), render_maps(cfg, reg, members)),
        _write_atomic(os.path.join(root, "nginx", "desk.conf"), render_desk_conf(cfg)),
        _write_atomic(os.path.join(root, "nginx", "fourcat.conf"), render_fourcat_conf(cfg)),
        _write_atomic(os.path.join(root, "docker-compose.yml"), render_compose(
            cfg, reg, lambda u: os.path.isdir(os.path.join(root, "secrets", "4cat-tokens", u)))),
    ]
    return written


CONF_D = "/etc/nginx/conf.d"
APP_D = "/etc/nginx/app-location-conf.d"


def install_nginx(cfg: DeskConfig, run=subprocess.run, conf_d: str = CONF_D, app_d: str = APP_D) -> None:
    """Install the rendered nginx files; on a failed `nginx -t` put the originals back and re-raise."""
    nginx = os.path.join(cfg.desk_root, "nginx")
    plan = ((os.path.join(nginx, "desk-map.conf"), conf_d),
            (os.path.join(cfg.repo_dir, "desk", "nginx", "websocket-map.conf"), conf_d),
            (os.path.join(nginx, "desk.conf"), app_d),
            (os.path.join(nginx, "fourcat.conf"), app_d))
    with tempfile.TemporaryDirectory() as aside:
        saved = []  # (destination path, backup path or None if it did not exist)
        for n, (src, dst_dir) in enumerate(plan):
            dst = os.path.join(dst_dir, os.path.basename(src))
            bak = None
            if os.path.exists(dst):
                bak = os.path.join(aside, str(n))
                shutil.copy2(dst, bak)
            saved.append((dst, bak))
        try:
            for src, dst_dir in plan:
                shutil.copy(src, dst_dir)
            run(["nginx", "-t"], check=True)
        except BaseException:
            for dst, bak in saved:
                if bak:
                    shutil.copy2(bak, dst)
                elif os.path.exists(dst):
                    os.remove(dst)
            raise
    run(["systemctl", "reload", "nginx"], check=True)
