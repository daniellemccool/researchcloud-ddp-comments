#!/usr/bin/env python3
"""Append a selection file's items to the desk's shared pool, skipping ones already in it.

    pool-add.py --desk DIR selection.csv

DIR is an initialised desk root (desk.json present), which names the platform and so the item
kinds. The selection needs an id column (also "Video ID" / video_id); kind, bucket and note are
optional (a missing kind means the platform's default). Excel exports (BOM, CRLF, ';') are fine.
New rows go to <desk>/pool.csv in file order, deduped on (kind, id); rows that cannot be used are
listed with their line number. Claimed rows stay in the pool; the desk log records who claimed what.

Exit 0 when the file parsed (even if some rows were rejected). Exit 1, importing nothing, for a
file-level error (unreadable, no id column, empty, unbalanced quote), a corrupt pool.csv or an
uninitialised desk.
"""
import argparse
import os
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from desklib import pool  # noqa: E402
from desklib.config import load_config  # noqa: E402
from desklib.platform import load_dir  # noqa: E402


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--desk", required=True, help="the desk root (holds desk.json)")
    ap.add_argument("selection")
    a = ap.parse_args()

    try:
        cfg = load_config(a.desk)
        platform = load_dir(cfg.platforms_dir, cfg.platform)
    except (OSError, ValueError) as e:
        print(f"pool-add: {a.desk} is not an initialised desk ({e})", file=sys.stderr)
        return 1
    try:
        with open(a.selection, newline="", encoding="utf-8-sig") as f:
            text = f.read()
    except (OSError, UnicodeDecodeError) as e:
        print(f"pool-add: cannot read {a.selection}: {e}", file=sys.stderr)
        return 1

    try:
        rows, rejects = pool.parse_selection(text, platform)
    except pool.SelectionError as e:
        print(f"line {e.line}: {e.reason}\npool-add: nothing imported from {a.selection}", file=sys.stderr)
        return 1
    try:
        added, total = pool.add_rows(a.desk, rows, platform)
    except (OSError, ValueError) as e:
        print(f"pool-add: cannot update {os.path.join(a.desk, 'pool.csv')}: {e}", file=sys.stderr)
        return 1
    print(f"added {added} to {os.path.join(a.desk, 'pool.csv')} ({total} in pool)")
    for line, reason in rejects:
        print(f"line {line}: {reason}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
