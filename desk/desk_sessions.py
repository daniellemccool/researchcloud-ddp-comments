#!/usr/bin/env python3
"""desk-sessions: operate the desk's per-user sessions (init, add, render, up, stop, reconcile ...).

Exit 0 on success, 1 on a reported failure, 2 on usage errors.
"""
import argparse
import dataclasses
import json
import os
import subprocess
import sys
import urllib.error

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from desklib import coapi, pool  # noqa: E402
from desklib.config import DeskConfig, load_config, save_config  # noqa: E402
from desklib.fourcat import FourCat, FourCatError  # noqa: E402
from desklib.platform import load_dir  # noqa: E402
from desklib.provision import Provisioner  # noqa: E402
from desklib.registry import Registry  # noqa: E402
from desklib.sessions import Sessions  # noqa: E402


def build_sessions(desk_root: str, is_admin=None) -> Sessions:
    """The one place a Sessions object is assembled (also used by the stub and the admin page)."""
    cfg = load_config(desk_root)
    reg = Registry(desk_root, cfg.max_sessions)
    fourcat = FourCat(cfg) if cfg.fourcat_enabled else None

    def members():
        return coapi.membership(coapi.co_members(coapi.workspace_info()), cfg)
    if is_admin is None:
        def is_admin(u):
            return members().get(u, False)
    prov = Provisioner(cfg, reg, fourcat, is_admin, members=members)
    return Sessions(cfg, reg, prov, fourcat, members)


def cmd_init(a) -> int:
    root = a.desk_root
    try:
        cfg = load_config(root)
    except FileNotFoundError:
        cfg = DeskConfig(desk_root=root, repo_dir=a.repo, platform=a.platform,
                         collector_role=a.collector_role, admin_role=a.admin_role, contact=a.contact)
    updates = {"repo_dir": a.repo, "platform": a.platform, "collector_role": a.collector_role,
               "admin_role": a.admin_role, "contact": a.contact, "fourcat_enabled": a.fourcat,
               "gate_mode": a.gate_mode, "max_running": a.max_running,
               "max_sessions": a.max_sessions, "template_path": a.template}
    cfg = dataclasses.replace(cfg, **{k: v for k, v in updates.items() if v is not None})
    cfg.validate()
    save_config(cfg)
    migrated = pool.migrate_legacy(root, load_dir(cfg.platforms_dir, cfg.platform).default_kind)
    print(f"wrote {os.path.join(root, 'desk.json')}" + ("; migrated pool.csv" if migrated else ""))
    return 0


def cmd_add(s, a) -> int:
    st = s.add(a.user)
    if st.ready():
        print(f"{a.user}: ready")
        return 0
    print(f"{a.user}: stuck at {st.current()}: {st.error() or 'incomplete'}")
    return 1


def cmd_repair(s, a) -> int:
    st = s.repair(a.user)
    print(f"{a.user}: " + ("ready" if st.ready() else f"stuck at {st.current()}: {st.error() or 'incomplete'}"))
    return 0 if st.ready() else 1


def cmd_retire(s, a) -> int:
    err = s.retire(a.user)
    print(f"{a.user}: " + (f"retire failed: {err}" if err else "retired"))
    return 1 if err else 0


def cmd_prune(s, a) -> int:
    retired = s.prune()
    print("retired: " + (" ".join(retired) or "-"))
    for u, why in s.failed.items():
        print(f"{u}: retire failed: {why}", file=sys.stderr)
    return 1 if s.failed else 0


def cmd_reconcile(s, a) -> int:
    out = s.reconcile()
    for key in ("retired", "admin_set", "admin_cleared"):
        print(f"{key}: " + (" ".join(out[key]) or "-"))
    for u, why in out.get("failed", {}).items():
        print(f"{u}: failed: {why}", file=sys.stderr)
    return 1 if "failed" in out else 0


def cmd_status(s, a) -> int:
    for r in s.status():
        state = "ready" if r["ready"] else f"at {r['step']}" + (f" ({r['error']})" if r["error"] else "")
        print(f"{r['user']}\t{r['index']}\t{'running' if r['running'] else 'stopped'}\t{state}")
    return 0


def parser() -> argparse.ArgumentParser:
    p = argparse.ArgumentParser(prog="desk-sessions")
    sub = p.add_subparsers(dest="cmd", required=True)

    def add(name, user=None):
        sp = sub.add_parser(name)
        sp.add_argument("--desk-root", required=True)
        if user:
            sp.add_argument("user", nargs="?" if user == "?" else None)
        return sp
    i = add("init")
    for flag in ("repo", "platform", "collector-role", "admin-role", "contact"):
        i.add_argument("--" + flag, required=True)
    i.add_argument("--fourcat", action=argparse.BooleanOptionalAction, default=None)
    i.add_argument("--gate-mode", choices=("roles", "api"))
    i.add_argument("--max-running", type=int)
    i.add_argument("--max-sessions", type=int)
    i.add_argument("--template")
    for name in ("add", "stop", "retire", "repair"):
        add(name, "!")
    add("up", "?")
    for name in ("render", "reconcile", "prune", "status"):
        add(name)
    return p


def main(argv=None) -> int:
    a = parser().parse_args(argv)
    try:
        if a.cmd == "init":
            return cmd_init(a)
        s = build_sessions(a.desk_root)
        if a.cmd == "render":
            print("\n".join(s.render()))
        elif a.cmd == "up":
            s.up(a.user)
        elif a.cmd == "stop":
            s.stop(a.user)
        else:
            return globals()["cmd_" + a.cmd](s, a)
        return 0
    except (ValueError, FileNotFoundError, FourCatError, OSError, urllib.error.URLError,
            subprocess.SubprocessError, KeyError, json.JSONDecodeError) as e:
        print(f"desk-sessions {a.cmd}: {type(e).__name__}: {e}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    sys.exit(main())
