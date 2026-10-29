"""Exports for the admin page (stdlib only): everything the desk has captured, as one zip."""
import io
import os
import zipfile

LOG = os.path.join("state", "desk-log.ndjson")


def captures_zip(desk_root: str) -> bytes:
    """A zip of state/captures/** at captures/... plus state/desk-log.ndjson at the root.

    Files are added in sorted order (captures first, then the log), so equal inputs give equal
    names in equal order. Symlinks are skipped: the export must not follow one out of the tree.
    """
    buf = io.BytesIO()
    top = os.path.join(desk_root, "state", "captures")
    with zipfile.ZipFile(buf, "w", zipfile.ZIP_DEFLATED) as z:
        for dirpath, dirs, files in os.walk(top):
            dirs.sort()
            for name in sorted(files):
                path = os.path.join(dirpath, name)
                if os.path.islink(path):
                    continue
                z.write(path, os.path.join("captures", os.path.relpath(path, top)))
        log = os.path.join(desk_root, LOG)
        if os.path.isfile(log) and not os.path.islink(log):
            z.write(log, "desk-log.ndjson")
    return buf.getvalue()
