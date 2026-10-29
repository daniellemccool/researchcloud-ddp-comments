"""The shared pool, <desk_root>/pool.csv (stdlib only). Header: id,kind,bucket,note.

The legacy header was video_id,bucket,note; its rows get the platform's default kind.
"""
import csv
import io
import os
import re
import tempfile
from dataclasses import dataclass

from .locks import flocked, pool_lock

HEADER = ["id", "kind", "bucket", "note"]
LEGACY = ["video_id", "bucket", "note"]


class SelectionError(ValueError):
    """The whole selection file is unusable (as opposed to single bad rows, which are rejects)."""

    def __init__(self, line: int, reason: str):
        super().__init__(f"line {line}: {reason}")
        self.line = line
        self.reason = reason


@dataclass
class PoolRow:
    id: str
    kind: str
    bucket: str
    note: str


def _path(desk_root: str) -> str:
    return os.path.join(desk_root, "pool.csv")


def _read(desk_root: str, default_kind: str) -> tuple[list[PoolRow], bool]:
    try:
        with open(_path(desk_root), newline="", encoding="utf-8-sig") as f:
            text = f.read()
    except FileNotFoundError:
        return [], False
    # Every writer swaps in a whole file (_replace), so a final line without a newline is a
    # complete row (a hand-edited file often ends that way), never a write in progress.
    reader = csv.DictReader(io.StringIO(text, newline=""))
    names = reader.fieldnames
    if names is not None and "id" not in names and "video_id" not in names:
        raise ValueError(f"pool.csv: unrecognised header {names!r}")
    legacy = names is not None and "id" not in names
    rows, seen = [], set()
    for r in reader:
        id_ = ((r.get("id") if not legacy else r.get("video_id")) or "").strip()
        kind = default_kind if legacy else ((r.get("kind") or "").strip() or default_kind)
        if not id_ or (kind, id_) in seen:
            continue
        seen.add((kind, id_))
        rows.append(PoolRow(id_, kind, r.get("bucket") or "", r.get("note") or ""))
    return rows, legacy


def _replace(desk_root: str, rows: list[PoolRow]) -> None:
    """Write header + rows to a temp file beside pool.csv and swap it in: readers see old or new, whole."""
    os.makedirs(desk_root, exist_ok=True)
    fd, tmp = tempfile.mkstemp(dir=desk_root, prefix=".pool.csv.")
    try:
        with os.fdopen(fd, "w", newline="", encoding="utf-8") as f:
            w = csv.writer(f)
            w.writerow(HEADER)
            w.writerows([r.id, r.kind, r.bucket, r.note] for r in rows)
        os.chmod(tmp, 0o644)
        os.replace(tmp, _path(desk_root))
    except BaseException:
        try:
            os.unlink(tmp)
        except FileNotFoundError:
            pass
        raise


def read_rows(desk_root: str, default_kind: str) -> list[PoolRow]:
    return _read(desk_root, default_kind)[0]


_ID_NAMES = {"id", "videoid", "video_id"}


def _norm(name: str) -> str:
    return re.sub(r"[\s\-]+", "_", name.strip().lower())


def parse_selection(text: str, platform) -> tuple[list[PoolRow], list[tuple[int, str]]]:
    """Parse an uploaded selection into (valid rows, [(line_no, reason)] rejects).

    Row-level problems are rejects. File-level ones raise SelectionError(line, reason) and import
    nothing: no header or no id column, any csv.Error, a cell holding a newline (an unbalanced quote
    swallowing the rows after it).

    Accepts a BOM, CRLF and a ';' or ',' delimiter. The header names are case-, space- and
    dash-insensitive ("Video ID" == "video_id" == "id"); kind, bucket and note are optional.
    line_no is the physical line in the upload, 1-based.
    """
    text = text.lstrip("﻿")
    try:
        delim = csv.Sniffer().sniff(text[:4096], delimiters=",;").delimiter
    except csv.Error:
        delim = ","
    reader = csv.reader(io.StringIO(text, newline=""), delimiter=delim)
    cols: dict[str, int] | None = None
    rows, rejects = [], []
    prev = 0
    try:
        for cells in reader:
            line = reader.line_num
            start, prev = prev + 1, line
            if any("\n" in c or "\r" in c for c in cells):
                raise SelectionError(start, "a quoted field runs over several lines (unbalanced quote?)")
            if not any(c.strip() for c in cells):
                continue
            if cols is None:
                cols = {}
                for i, name in enumerate(cells):
                    n = _norm(name)
                    cols.setdefault("id" if n in _ID_NAMES else n, i)
                if "id" not in cols:
                    raise SelectionError(line, "header has no id column")
                continue

            def get(name):
                i = cols.get(name)
                return cells[i].strip() if i is not None and i < len(cells) else ""
            item_id, kind = get("id"), get("kind") or platform.default_kind
            k = platform.item_kinds.get(kind)
            if k is None:
                rejects.append((line, f"unknown kind {kind!r}"))
            elif not item_id:
                rejects.append((line, "id is empty"))
            elif not k.id_re.fullmatch(item_id):
                rejects.append((line, f"id must match {k.id_re.pattern}"))
            else:
                rows.append(PoolRow(item_id, kind, get("bucket"), get("note")))
    except csv.Error as e:
        raise SelectionError(reader.line_num, f"unreadable: {e}") from e
    if cols is None:
        raise SelectionError(1, "file is empty (no header)")
    return rows, rejects


def add_rows(desk_root: str, rows: list[PoolRow], platform) -> tuple[int, int]:
    """Append the rows not already in pool.csv (dedupe on (kind, id), also within the batch).

    Returns (added, total rows now). Rows the platform cannot open are skipped, never written.
    """
    with flocked(pool_lock(desk_root)):
        existing, _ = _read(desk_root, platform.default_kind)
        seen = {(r.kind, r.id) for r in existing}
        fresh = []
        for r in rows:
            k = platform.item_kinds.get(r.kind)
            if k is None or not k.id_re.fullmatch(r.id) or (r.kind, r.id) in seen:
                continue
            seen.add((r.kind, r.id))
            fresh.append(r)
        if fresh:       # the whole new content, swapped in atomically (also upgrades a legacy header)
            _replace(desk_root, existing + fresh)
        return len(fresh), len(existing) + len(fresh)


def migrate_legacy(desk_root: str, default_kind: str) -> bool:
    with flocked(pool_lock(desk_root)):
        return _migrate(desk_root, default_kind)


def _migrate(desk_root: str, default_kind: str) -> bool:
    rows, legacy = _read(desk_root, default_kind)
    if not legacy:
        return False
    _replace(desk_root, rows)
    return True
