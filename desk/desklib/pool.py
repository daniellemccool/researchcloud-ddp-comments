"""The shared pool, <desk_root>/pool.csv (stdlib only). Header: id,kind,bucket,note.

The legacy header was video_id,bucket,note; its rows get the platform's default kind.
"""
import csv
import os
import tempfile
from dataclasses import dataclass

HEADER = ["id", "kind", "bucket", "note"]
LEGACY = ["video_id", "bucket", "note"]


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
        with open(_path(desk_root), newline="") as f:
            reader = csv.DictReader(f)
            legacy = reader.fieldnames is not None and "id" not in reader.fieldnames \
                and "video_id" in reader.fieldnames
            rows, seen = [], set()
            for r in reader:
                id_ = ((r.get("id") if not legacy else r.get("video_id")) or "").strip()
                kind = default_kind if legacy else ((r.get("kind") or "").strip() or default_kind)
                if not id_ or (kind, id_) in seen:
                    continue
                seen.add((kind, id_))
                rows.append(PoolRow(id_, kind, r.get("bucket") or "", r.get("note") or ""))
    except FileNotFoundError:
        return [], False
    return rows, legacy


def read_rows(desk_root: str, default_kind: str) -> list[PoolRow]:
    return _read(desk_root, default_kind)[0]


def migrate_legacy(desk_root: str, default_kind: str) -> bool:
    rows, legacy = _read(desk_root, default_kind)
    if not legacy:
        return False
    fd, tmp = tempfile.mkstemp(dir=desk_root, prefix=".pool.csv.")
    try:
        with os.fdopen(fd, "w", newline="") as f:
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
    return True
