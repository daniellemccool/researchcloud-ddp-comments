#!/usr/bin/env python3
"""Append a selection.csv's videos to the desk's shared pool, skipping ids already in it.

    pool-add.py [--desk DIR] selection.csv

Reads video_id,bucket,note (other columns ignored; non-numeric ids skipped) and appends
new rows to <desk>/pool.csv in file order. Claimed rows stay in the pool; the desk log
records who claimed what.
"""
import argparse, csv, os

ap = argparse.ArgumentParser()
ap.add_argument("--desk", default="/data/onlinehate_zeeschuimer/desk")
ap.add_argument("selection")
a = ap.parse_args()

pool = os.path.join(a.desk, "pool.csv")
seen = set()
if os.path.exists(pool):
    with open(pool, newline="") as f:
        seen = {(r.get("video_id") or "").strip() for r in csv.DictReader(f)}
new = []
with open(a.selection, newline="") as f:
    for r in csv.DictReader(f):
        v = (r.get("video_id") or "").strip()
        if v.isdigit() and v not in seen:
            seen.add(v); new.append([v, r.get("bucket", ""), r.get("note", "")])
fresh = not os.path.exists(pool)
with open(pool, "a", newline="") as f:
    w = csv.writer(f)
    if fresh:
        w.writerow(["video_id", "bucket", "note"])
    w.writerows(new)
print(f"added {len(new)} to {pool} ({len(seen)} in pool)")
