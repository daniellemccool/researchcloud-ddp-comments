#!/usr/bin/env python3
"""desk-template: build and verify Firefox profile templates for the desk.

    desk-template release --platform P --zeeschuimer-version V [--platforms-dir DIR] SRC_CONFIG OUT_DIR
    desk-template verify  --platform P [--platforms-dir DIR] TAR

Exit 0 on success, 1 on a reported failure, 2 on usage errors.
"""
import argparse
import os
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from desklib import template  # noqa: E402
from desklib.platform import PlatformError, load_dir  # noqa: E402

DEFAULT_PLATFORMS = os.path.join(os.path.dirname(os.path.dirname(os.path.abspath(__file__))), "platforms")


def cmd_release(a) -> int:
    plat = load_dir(a.platforms_dir, a.platform)
    tar_path, man = template.release(a.src, a.out, plat, a.zeeschuimer_version)
    print(f"{tar_path} ({os.path.getsize(tar_path) // 1024} KiB) sha256 {man['sha256']}")
    return 0


def cmd_verify(a) -> int:
    plat = load_dir(a.platforms_dir, a.platform)
    problems = template.verify_tar(a.tar, plat)
    for p in problems:
        print(f"problem: {p}", file=sys.stderr)
    if not problems:
        print(f"{a.tar}: ok")
    return 1 if problems else 0


def main(argv=None) -> int:
    ap = argparse.ArgumentParser(prog="desk-template")
    sub = ap.add_subparsers(dest="cmd", required=True)
    for name in ("release", "verify"):
        p = sub.add_parser(name)
        p.add_argument("--platform", required=True)
        p.add_argument("--platforms-dir", default=DEFAULT_PLATFORMS)
        if name == "release":
            p.add_argument("--zeeschuimer-version", required=True)
            p.add_argument("src", metavar="SRC_CONFIG")
            p.add_argument("out", metavar="OUT_DIR")
        else:
            p.add_argument("tar", metavar="TAR")
    try:
        a = ap.parse_args(argv)
    except SystemExit as e:
        return 2 if e.code else 0
    try:
        return {"release": cmd_release, "verify": cmd_verify}[a.cmd](a)
    except (template.TemplateError, PlatformError, OSError) as e:
        print(f"desk-template: {e}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    sys.exit(main())
