#!/usr/bin/env python3
"""desk-admin: 4CAT account adoption, admin-tag sync and token rotation.

Exit 0 on success, 1 on a reported failure, 2 on usage errors.
"""
import argparse
import json
import os
import secrets
import subprocess
import sys
import urllib.error

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from desk_sessions import build_sessions  # noqa: E402
from desklib.fourcat import FourCatError  # noqa: E402


def cmd_show(s, a) -> int:
    try:
        with open(s.fourcat.password_path(a.user)) as f:
            pw = f.read().strip()
    except FileNotFoundError:
        pw = ""
    if not pw:
        print(f"desk-admin 4cat-user show: no stored password for {a.user}", file=sys.stderr)
        return 1
    print(pw)
    return 0


def cmd_adopt(s, a) -> int:
    fc = s.fourcat
    if not fc.account_exists(a.user):
        print(f"desk-admin 4cat-user adopt: no 4CAT account for {a.user}", file=sys.stderr)
        return 1
    pw = secrets.token_urlsafe(24)
    fc.hash_password(pw)               # fail early on a broken backend, before touching the account
    fc.set_password(a.user, pw)
    fc._write_password(a.user, pw)     # the DB has changed: the file must follow (as rotate does)
    fc.write_token(a.user, fc.login_token(a.user, pw))
    print(f"{a.user}: adopted")
    return 0


def cmd_sync(s, a) -> int:
    out = s.sync_admins()
    for key in ("admin_set", "admin_cleared"):
        print(f"{key}: " + (" ".join(out[key]) or "-"))
    if out.get("skipped"):
        print("skipped (no 4CAT account yet): " + " ".join(out["skipped"]))
    for u, why in s.failed.items():
        print(f"{u}: failed: {why}", file=sys.stderr)
    return 1 if s.failed else 0


def cmd_rotate(s, a) -> int:
    s.fourcat.rotate(a.user)
    print(f"{a.user}: rotated")
    return 0


def parser() -> argparse.ArgumentParser:
    p = argparse.ArgumentParser(prog="desk-admin")
    groups = p.add_subparsers(dest="group", required=True)

    def leaf(group, name, user):
        sp = group.add_parser(name)
        sp.add_argument("--desk-root", required=True)
        if user:
            sp.add_argument("user")
    users = groups.add_parser("4cat-user").add_subparsers(dest="action", required=True)
    leaf(users, "show", True)
    leaf(users, "adopt", True)
    leaf(users, "sync", False)
    tokens = groups.add_parser("4cat-token").add_subparsers(dest="action", required=True)
    leaf(tokens, "rotate", True)
    return p


COMMANDS = {("4cat-user", "show"): cmd_show, ("4cat-user", "adopt"): cmd_adopt,
            ("4cat-user", "sync"): cmd_sync, ("4cat-token", "rotate"): cmd_rotate}


def main(argv=None) -> int:
    a = parser().parse_args(argv)
    name = f"{a.group} {a.action}"
    try:
        s = build_sessions(a.desk_root)
        if s.fourcat is None:
            print(f"desk-admin {name}: 4CAT is disabled in this desk's config", file=sys.stderr)
            return 1
        return COMMANDS[(a.group, a.action)](s, a)
    except (ValueError, RuntimeError, FileNotFoundError, FourCatError, OSError, urllib.error.URLError,
            subprocess.SubprocessError, KeyError, json.JSONDecodeError) as e:
        print(f"desk-admin {name}: {type(e).__name__}: {e}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    sys.exit(main())
