"""Release templates: an allowlist sanitiser for a Firefox profile plus proof the extension store is empty.

A template carries Zeeschuimer's settings (capture switches, the 4CAT URL) and the platform's consent
cookie, nothing else. Release templates are public artefacts, so everything is dropped unless allowlisted,
and `verify_release` walks the whole tree to prove it (stdlib only).
"""
import contextlib
import datetime
import hashlib
import json
import os
import re
import shutil
import sqlite3
import tarfile
import tempfile

ALLOW_FILES = ("prefs.js", "extensions.json", "cookies.sqlite", "chrome/userContent.css")
ALLOW_DIRS = ("extensions", "storage/default")   # storage/default keeps only Zeeschuimer's two directories
XPI_ID = "{677bb78c-7168-4aa5-8e9b-32e9f26c6157}"
ALLOW_PREFS = frozenset((
    "extensions.webextensions.uuids",
    f"extensions.webextensions.ExtensionStorageIDB.migrated.{XPI_ID}",
    "extensions.databaseSchema", "extensions.lastAppVersion", "extensions.lastPlatformVersion",
    "extensions.pendingOperations", "toolkit.legacyUserProfileCustomizations.stylesheets"))
XPI_REL = f"extensions/{XPI_ID}.xpi"
XPI_CONTAINER_PATH = f"/config/profile/{XPI_REL}"
SYNC_GUID = "{00000000-0000-4000-8000-000000000000}"
ITEMS_DB = "zeeschuimer-items"
STORAGE_DB = "webExtensions-storage-local"
STORAGE_LABEL = "storage.local"
USER_CONTEXT = "^userContextId=4294967295"
SKIP_COPY = {"cache2", "startupCache", "sessionstore-backups", "crashes", "minidumps", "saved-telemetry-pings",
             "lock", ".parentlock"}
_UUID_RE = re.compile(r'^user_pref\("extensions\.webextensions\.uuids",\s*"((?:[^"\\]|\\.)*)"\)\s*;', re.M)
_PREF_RE = re.compile(r'^user_pref\("([^"]+)"\s*,')


class TemplateError(Exception):
    """A template cannot be built or fails verification; the message lists the problems."""


def _parents(rels):
    out = set()
    for r in rels:
        parts = r.split("/")
        out.update("/".join(parts[:i]) for i in range(1, len(parts)))
    return out


_ANCESTORS = _parents(ALLOW_FILES + ALLOW_DIRS)


def zeeschuimer_uuid(profile_dir):
    """Zeeschuimer's moz-extension uuid from the extensions.webextensions.uuids pref, or None."""
    try:
        with open(os.path.join(profile_dir, "prefs.js"), encoding="utf-8", errors="replace") as f:
            m = _UUID_RE.search(f.read())
        mapping = json.loads(json.loads('"' + m.group(1) + '"')) if m else None
    except (OSError, ValueError):
        return None
    u = mapping.get(XPI_ID) if isinstance(mapping, dict) else None
    return u if isinstance(u, str) and re.fullmatch(r"[A-Za-z0-9-]+", u) else None


def _ext_dirs(uuid):
    return (f"moz-extension+++{uuid}", f"moz-extension+++{uuid}{USER_CONTEXT}") if uuid else ()


def _remove(p):
    if os.path.isdir(p) and not os.path.islink(p):
        shutil.rmtree(p)
    else:
        os.unlink(p)


def _rebuild_idb(path):
    """Rewrite an IndexedDB sqlite file so deleted rows (free pages, WAL) leave no trace."""
    tmp = path + ".rebuilt"
    if os.path.exists(tmp):
        os.remove(tmp)
    con = sqlite3.connect(path)
    try:
        con.execute("PRAGMA wal_checkpoint(TRUNCATE)")
        con.execute("VACUUM INTO ?", (tmp,))
    finally:
        con.close()
    os.replace(tmp, path)
    for side in ("-wal", "-shm", "-journal"):
        if os.path.exists(path + side):
            os.remove(path + side)


def _idb_name(path):
    """The IndexedDB database name recorded in a sqlite file, or None if it is not one."""
    try:
        with _snapshot(path) as con:
            row = con.execute("SELECT name FROM database").fetchone()
        return row[0] if row else None
    except sqlite3.Error:
        return None


def _clean_ext_dir(d, expected_db):
    for n in os.listdir(d):
        if n != "idb":
            _remove(os.path.join(d, n))
    idb = os.path.join(d, "idb")
    if not os.path.isdir(idb):
        return
    keep = set()
    for n in sorted(os.listdir(idb)):
        p = os.path.join(idb, n)
        if n.endswith(".sqlite") and os.path.isfile(p) and _idb_name(p) == expected_db:
            _rebuild_idb(p)
            keep.add(n)
        elif n.endswith(".files") and os.path.isdir(p):
            keep.add(n)
    for n in os.listdir(idb):
        if n not in keep:
            _remove(os.path.join(idb, n))


def _prune(root, uuid, rel=""):
    """Delete everything under root/rel that the allowlist does not name."""
    here = os.path.join(root, rel) if rel else root
    for name in sorted(os.listdir(here)):
        r = f"{rel}/{name}" if rel else name
        p = os.path.join(here, name)
        if os.path.islink(p):
            os.unlink(p)
        elif r in ALLOW_FILES:
            continue
        elif r == "extensions":
            for n in os.listdir(p):
                if not (n == f"{XPI_ID}.xpi" and os.path.isfile(os.path.join(p, n))):
                    _remove(os.path.join(p, n))
        elif r == "storage/default":
            keep = _ext_dirs(uuid)
            for n in os.listdir(p):
                if n in keep and os.path.isdir(os.path.join(p, n)) and not os.path.islink(os.path.join(p, n)):
                    _clean_ext_dir(os.path.join(p, n), ITEMS_DB if n == keep[0] else STORAGE_DB)
                else:
                    _remove(os.path.join(p, n))
        elif r in _ANCESTORS and os.path.isdir(p):
            _prune(root, uuid, r)
        else:
            _remove(p)


def _filter_prefs(path):
    keep = []
    with open(path, encoding="utf-8", errors="replace") as f:
        for line in f:
            m = _PREF_RE.match(line.strip())
            if m and m.group(1) in ALLOW_PREFS:
                keep.append(line if line.endswith("\n") else line + "\n")
    with open(path, "w", encoding="utf-8") as f:
        f.writelines(keep)


def _load_json(path):
    with open(path, encoding="utf-8") as f:
        return json.load(f)


def _policy_url(version):
    return f"https://github.com/digitalmethodsinitiative/zeeschuimer/releases/download/v{version}/zeeschuimer-{version}.xpi"


def _filter_extensions(data, zeeschuimer_version, built_on):
    """Only Zeeschuimer's addon entry, with installation-specific fields normalised."""
    if not isinstance(data, dict):
        return {"addons": []}
    addons = data.get("addons")
    out = {k: v for k, v in data.items() if k == "schemaVersion"}
    entries = [a for a in addons if isinstance(a, dict) and a.get("id") == XPI_ID] if isinstance(addons, list) else []
    ms = _mtime(built_on) * 1000
    norm = []
    for a in entries:
        e = {k: v for k, v in a.items() if "elemetry" not in k.lower()}
        e["path"] = XPI_CONTAINER_PATH
        e["syncGUID"] = SYNC_GUID
        e["installDate"] = e["updateDate"] = ms
        if e.get("sourceURI") != _policy_url(zeeschuimer_version):
            e.pop("sourceURI", None)
        if "rootURI" in e:
            e["rootURI"] = f"jar:file://{XPI_CONTAINER_PATH}!/"
        norm.append(e)
    out["addons"] = norm
    return out


def _filter_json_files(profile_dir, zeeschuimer_version, built_on):
    path = os.path.join(profile_dir, "extensions.json")
    if not os.path.isfile(path):
        return
    try:
        data = _load_json(path)
    except (OSError, ValueError) as e:
        raise TemplateError(f"extensions.json is not valid JSON: {e}")
    with open(path, "w", encoding="utf-8") as f:
        json.dump(_filter_extensions(data, zeeschuimer_version, built_on), f, separators=(",", ":"))


def _entry_problems(e, zeeschuimer_version):
    out = []

    def bad(field):
        msg = f"extensions.json entry not normalised: {field}"
        if msg not in out:
            out.append(msg)
    if e.get("path") != XPI_CONTAINER_PATH:
        bad("path")
    if e.get("syncGUID") != SYNC_GUID:
        bad("syncGUID")
    for k in e:
        if "elemetry" in k.lower():
            bad(k)
    if "rootURI" in e and e["rootURI"] != f"jar:file://{XPI_CONTAINER_PATH}!/":
        bad("rootURI")
    if "sourceURI" in e and (zeeschuimer_version is None or e["sourceURI"] != _policy_url(zeeschuimer_version)):
        bad("sourceURI")

    def walk(v, key):
        if isinstance(v, str):
            if v.startswith("/"):
                bad(key)
        elif isinstance(v, dict):
            for kk, vv in v.items():
                walk(vv, kk)
        elif isinstance(v, list):
            for vv in v:
                walk(vv, key)
    for k, v in e.items():
        if k != "path":                      # path is checked exactly above
            walk(v, k)
    return out


def _json_problems(profile_dir, zeeschuimer_version):
    path = os.path.join(profile_dir, "extensions.json")
    if not os.path.isfile(path):
        return ["extensions.json does not list the xpi"]
    try:
        data = _load_json(path)
    except (OSError, ValueError):
        return ["extensions.json is not valid JSON"]
    addons = data.get("addons") if isinstance(data, dict) else None
    if not isinstance(addons, list):
        return ["foreign addon id in extensions.json: <addons>", "extensions.json does not list the xpi"]
    mine = [a for a in addons if isinstance(a, dict) and a.get("id") == XPI_ID]
    out = [f"foreign addon id in extensions.json: {a.get('id') if isinstance(a, dict) else '<entry>'}"
           for a in addons if not (isinstance(a, dict) and a.get("id") == XPI_ID)]
    if not mine:
        out.append("extensions.json does not list the xpi")
    for e in mine:
        out += _entry_problems(e, zeeschuimer_version)
    return out


def _filter_cookies(path, keep_cookies):
    keep = {(c["host"], c["name"]) for c in keep_cookies}
    db = sqlite3.connect(path)
    try:
        doomed = [(i,) for i, h, n in db.execute("SELECT id, host, name FROM moz_cookies") if (h, n) not in keep]
        db.executemany("DELETE FROM moz_cookies WHERE id = ?", doomed)
        db.commit()
        db.execute("PRAGMA journal_mode=DELETE")
        db.execute("VACUUM")
    finally:
        db.close()
    for side in ("-wal", "-shm", "-journal"):
        if os.path.exists(path + side):
            os.remove(path + side)


def _unlink_symlinks(root):
    """Remove every symlink (file or directory) so nothing below can write through to a target."""
    for d, dirs, files in os.walk(root, followlinks=False):
        for n in list(dirs) + files:
            p = os.path.join(d, n)
            if os.path.islink(p):
                os.unlink(p)
                if n in dirs:
                    dirs.remove(n)


def sanitise(profile_dir: str, keep_cookies: list, zeeschuimer_version: str, built_on: str) -> None:
    """Work on a COPY: filter cookies (WAL included), then delete everything not allowlisted."""
    _unlink_symlinks(profile_dir)
    cookies = os.path.join(profile_dir, "cookies.sqlite")
    if os.path.isfile(cookies):
        _filter_cookies(cookies, keep_cookies)
    uuid = zeeschuimer_uuid(profile_dir)
    _prune(profile_dir, uuid)
    prefs = os.path.join(profile_dir, "prefs.js")
    if os.path.isfile(prefs):
        _filter_prefs(prefs)
    _filter_json_files(profile_dir, zeeschuimer_version, built_on)


@contextlib.contextmanager
def _snapshot(path):
    """Open a sqlite file via a temp copy (with its -wal/-shm siblings) so the original is never touched."""
    with tempfile.TemporaryDirectory() as t:
        dst = os.path.join(t, "db.sqlite")
        shutil.copy2(path, dst)
        for side in ("-wal", "-shm"):
            if os.path.exists(path + side):
                shutil.copy2(path + side, dst + side)
        con = sqlite3.connect(dst)
        try:
            yield con
        finally:
            con.close()


def idb_databases(profile_dir: str) -> list:
    """[(sqlite path, IDB database name, {object store name: row count})] for every extension IndexedDB file."""
    out = []
    base = os.path.join(profile_dir, "storage", "default")
    if not os.path.isdir(base):
        return out
    for ext in sorted(os.listdir(base)):
        idb = os.path.join(base, ext, "idb")
        if not ext.startswith("moz-extension+++") or not os.path.isdir(idb):
            continue
        for fn in sorted(os.listdir(idb)):
            path = os.path.join(idb, fn)
            if not fn.endswith(".sqlite") or not os.path.isfile(path):
                continue
            try:
                with _snapshot(path) as con:
                    row = con.execute("SELECT name FROM database").fetchone()
                    counts = dict(con.execute(
                        "SELECT s.name, count(d.object_store_id) FROM object_store s "
                        "LEFT JOIN object_data d ON d.object_store_id = s.id GROUP BY s.id, s.name").fetchall())
            except sqlite3.Error:
                continue
            if row:
                out.append((path, row[0], counts))
    return out


def _has_xpi(profile_dir):
    if os.path.isfile(os.path.join(profile_dir, "extensions", f"{XPI_ID}.xpi")):
        return True
    try:
        with open(os.path.join(profile_dir, "extensions.json"), encoding="utf-8", errors="replace") as f:
            return XPI_ID in f.read()
    except OSError:
        return False


def _entries_under(path, rel):
    """Every file (and empty directory) below path, as paths relative to the tree, with rel as prefix."""
    out = []
    for d, dirs, files in os.walk(path):
        base = os.path.relpath(d, path)
        pre = rel if base == "." else f"{rel}/{base}"
        out += [f"{pre}/{n}" for n in files]
        out += [f"{pre}/{n}" for n in dirs if os.path.islink(os.path.join(d, n))]
        if not dirs and not files and base != ".":
            out.append(pre)
    return out


def _tree_problems(profile_dir, uuid, prefix):
    """'unexpected file: <path>' for every path the allowlist does not cover."""
    ext = _ext_dirs(uuid)
    ok_dirs = {"extensions", "chrome", "storage", "storage/default", *(f"storage/default/{e}" for e in ext),
               *(f"storage/default/{e}/idb" for e in ext)}

    idb_dir = "|".join(re.escape(f"storage/default/{e}/idb") for e in ext) or "(?!)"
    files_dir_re = re.compile(rf"(?:{idb_dir})/[^/]+\.files")
    idb_file_re = re.compile(rf"(?:{idb_dir})/[^/]+\.sqlite(?:-wal)?")        # wal: its own problem
    blob_re = re.compile(rf"(?:{idb_dir})/[^/]+\.files/[^/]+")                 # blobs: their own problem

    def dir_ok(r):
        return r in ok_dirs or bool(files_dir_re.fullmatch(r))

    def file_ok(r):
        if r in ALLOW_FILES or r == XPI_REL:
            return True
        return bool(idb_file_re.fullmatch(r) or blob_re.fullmatch(r))

    bad = []
    for d, dirs, files in os.walk(profile_dir):
        rd = os.path.relpath(d, profile_dir)
        rd = "" if rd == "." else rd
        keep = []
        for n in dirs:
            r = f"{rd}/{n}" if rd else n
            p = os.path.join(d, n)
            if os.path.islink(p):
                bad.append(r)
            elif dir_ok(r):
                keep.append(n)
            else:
                bad += _entries_under(p, r) or [r]
        dirs[:] = keep
        for n in files:
            r = f"{rd}/{n}" if rd else n
            if os.path.islink(os.path.join(d, n)) or not file_ok(r):
                bad.append(r)
    return [f"unexpected file: {prefix}{r}" for r in sorted(bad)]


def _idb_problems(profile_dir, uuid, prefix=""):
    problems = []
    for dname, label, expected in zip(_ext_dirs(uuid), (ITEMS_DB, STORAGE_LABEL), (ITEMS_DB, STORAGE_DB)):
        idb = os.path.join(profile_dir, "storage", "default", dname, "idb")
        if not os.path.isdir(idb):
            continue
        for fn in sorted(os.listdir(idb)):
            p = os.path.join(idb, fn)
            if fn.endswith("-wal"):
                problems.append(f"{label} has a wal file")
            elif fn.endswith(".files") and os.path.isdir(p) and os.listdir(p):
                problems.append(f"{label} has blob files")
            elif fn.endswith(".sqlite") and os.path.isfile(p):
                if _idb_name(p) != expected:
                    problems.append(f"unexpected file: {prefix}storage/default/{dname}/idb/{fn}")
                    continue
                try:
                    with _snapshot(p) as con:
                        if con.execute("PRAGMA freelist_count").fetchone()[0] != 0:
                            problems.append(f"{label} has free pages")
                except sqlite3.Error as e:
                    problems.append(f"{label} unreadable: {e}")
    return problems


def _pref_problems(profile_dir):
    try:
        with open(os.path.join(profile_dir, "prefs.js"), encoding="utf-8", errors="replace") as f:
            lines = [ln.strip() for ln in f if ln.strip()]
    except OSError:
        return []
    out = []
    for ln in lines:
        m = _PREF_RE.match(ln)
        if not m or m.group(1) not in ALLOW_PREFS:
            out.append(f"unexpected pref: {m.group(1) if m else ln[:60]}")
    return out


def verify_release(profile_dir: str, platform, rel_prefix: str = "", zeeschuimer_version: str = None) -> list:
    problems = []
    uuid = zeeschuimer_uuid(profile_dir)
    if uuid is None:
        problems.append("uuid not found in prefs")
    if not _has_xpi(profile_dir):
        problems.append("xpi id missing")
    cookies = os.path.join(profile_dir, "cookies.sqlite")
    keep = {(c["host"], c["name"]) for c in platform.keep_cookies}
    present = set()
    if os.path.isfile(cookies):
        try:
            with _snapshot(cookies) as con:
                for h, n in con.execute("SELECT host, name FROM moz_cookies ORDER BY host, name"):
                    if (h, n) not in keep:
                        problems.append(f"cookie outside keep list: {h} {n}")
                    else:
                        present.add((h, n))
        except sqlite3.Error as e:
            problems.append(f"cookies.sqlite unreadable: {e}")
    if keep and not present:
        problems.append("no keep cookie present")
    problems += _pref_problems(profile_dir)
    problems += _json_problems(profile_dir, zeeschuimer_version)
    items_dir, ctx_dir = (_ext_dirs(uuid) or (None, None))
    dbs = [(os.path.basename(os.path.dirname(os.path.dirname(p))), n, c) for p, n, c in idb_databases(profile_dir)]
    items = [c for d, n, c in dbs if d == items_dir and n == ITEMS_DB]
    if not items:
        problems.append(f"{ITEMS_DB} database not found")
    for counts in items:
        if counts.get("items", 0) > 0:
            problems.append(f"{ITEMS_DB} contains {counts['items']} captured items")
        if counts.get("uploads", 0) > 0 or counts.get("nav", 0) > 0:
            problems.append(f"{ITEMS_DB} has upload history")
    if not any(d == ctx_dir and sum(c.values()) > 0 for d, _, c in dbs):
        problems.append("extension storage.local database not found or empty")
    problems += _idb_problems(profile_dir, uuid, rel_prefix)
    return problems + _tree_problems(profile_dir, uuid, rel_prefix)


def manifest(platform, zeeschuimer_version: str, sha256: str, kind: str, built_on: str = None) -> dict:
    return {"kind": kind, "platform": platform.name, "zeeschuimer_version": zeeschuimer_version,
            "modules": platform.zeeschuimer_modules, "built_on": built_on or datetime.date.today().isoformat(),
            "sha256": sha256}


def _sha256(path):
    h = hashlib.sha256()
    with open(path, "rb") as f:
        for chunk in iter(lambda: f.read(1 << 20), b""):
            h.update(chunk)
    return h.hexdigest()


def _mtime(built_on):
    d = datetime.date.fromisoformat(built_on)
    return int(datetime.datetime(d.year, d.month, d.day, tzinfo=datetime.timezone.utc).timestamp())


def _owned(mtime):
    def f(ti):
        ti.uid = ti.gid = 1000
        ti.uname = ti.gname = ""
        ti.mtime = mtime
        return ti
    return f


def release(src_config_dir: str, out_dir: str, platform, zeeschuimer_version: str) -> tuple:
    """Sanitise a copy of a stopped /config's profile, verify it, write the tar and its manifest.

    The tar holds exactly profile/ and an empty xdg/. The source is untouched.
    """
    src_profile = os.path.join(src_config_dir, "profile")
    if not os.path.isdir(src_profile):
        raise TemplateError(f"{src_config_dir} has no profile/ directory")
    if os.path.islink(os.path.join(src_profile, "lock")):
        raise TemplateError("profile is locked: stop the Firefox container first")
    stem = f"ddp-comments-template-{platform.name}-{zeeschuimer_version}"
    tar_path = os.path.join(out_dir, stem + ".tar")
    man_path = os.path.join(out_dir, stem + ".json")
    with tempfile.TemporaryDirectory() as t:
        profile = os.path.join(t, "profile")
        shutil.copytree(src_profile, profile, symlinks=True,
                        ignore=lambda d, names: [n for n in names if n in SKIP_COPY])
        built_on = datetime.date.today().isoformat()
        sanitise(profile, platform.keep_cookies, zeeschuimer_version, built_on)
        problems = verify_release(profile, platform, "", zeeschuimer_version)
        if problems:
            raise TemplateError("template failed verification: " + "; ".join(problems))
        os.makedirs(out_dir, exist_ok=True)
        tmp_tar = tar_path + ".tmp"
        for stale in (tar_path, man_path, tmp_tar):
            if os.path.exists(stale):
                os.remove(stale)
        owned = _owned(_mtime(built_on))
        try:
            with tarfile.open(tmp_tar, "w") as tar:
                xdg = tarfile.TarInfo("xdg")
                xdg.type, xdg.mode = tarfile.DIRTYPE, 0o755
                tar.addfile(owned(xdg))
                tar.add(profile, arcname="profile", filter=owned)
            fd = os.open(tmp_tar, os.O_RDONLY)
            try:
                os.fsync(fd)
            finally:
                os.close(fd)
            os.replace(tmp_tar, tar_path)
        except BaseException:
            if os.path.exists(tmp_tar):
                os.remove(tmp_tar)
            raise
    man = manifest(platform, zeeschuimer_version, _sha256(tar_path), "release", built_on)
    with open(man_path, "w", encoding="utf-8") as f:
        json.dump(man, f, indent=2)
        f.write("\n")
    return tar_path, man


def _root_problems(root):
    bad = []
    for name in sorted(os.listdir(root)):
        p = os.path.join(root, name)
        if name == "profile" and os.path.isdir(p) and not os.path.islink(p):
            continue
        if name == "xdg" and os.path.isdir(p) and not os.path.islink(p):
            bad += _entries_under(p, "xdg")
        elif os.path.isdir(p) and not os.path.islink(p):
            bad += _entries_under(p, name) or [name]
        else:
            bad.append(name)
    return [f"unexpected file: {r}" for r in bad]


def verify_tar(tar_path: str, platform) -> list:
    problems = []
    man_path = os.path.splitext(tar_path)[0] + ".json"
    base = os.path.basename(tar_path)
    pre = f"ddp-comments-template-{platform.name}-"
    version = base[len(pre):-len(".tar")] if base.startswith(pre) and base.endswith(".tar") else None
    try:
        with open(man_path, encoding="utf-8") as f:
            man = json.load(f)
        if man.get("sha256") != _sha256(tar_path):
            problems.append("sha256 does not match the manifest")
        if man.get("platform") != platform.name:
            problems.append(f"manifest platform {man.get('platform')!r} is not {platform.name!r}")
        if man.get("kind") != "release":
            problems.append(f"manifest kind is {man.get('kind')}, not release")
        if version is None or man.get("zeeschuimer_version") != version:
            problems.append(f"manifest zeeschuimer_version is {man.get('zeeschuimer_version')}, not {version}")
        if man.get("modules") != platform.zeeschuimer_modules:
            problems.append(f"manifest modules are {man.get('modules')}, not {platform.zeeschuimer_modules}")
    except (OSError, ValueError, AttributeError):
        problems.append(f"manifest {os.path.basename(man_path)} missing or unreadable")
    with tempfile.TemporaryDirectory() as t:
        try:
            with tarfile.open(tar_path) as tar:
                tar.extractall(t, filter="data")
        except (tarfile.TarError, OSError) as e:
            return problems + [f"cannot extract tar: {e}"]
        problems += _root_problems(t)
        profile = os.path.join(t, "profile")
        if not os.path.isdir(profile):
            return problems + ["profile/ missing from tar"]
        return problems + verify_release(profile, platform, "profile/", version)
