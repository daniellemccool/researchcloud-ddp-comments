"""Release templates: an allowlist sanitiser for a Firefox profile plus proof the extension store is empty.

A template carries Zeeschuimer's settings (capture switches, the 4CAT URL) and the platform's consent
cookie, nothing else. Release templates are public artefacts, so everything is dropped unless allowlisted,
and `verify_release` checks the result (stdlib only).
"""
import contextlib
import datetime
import hashlib
import json
import os
import shutil
import sqlite3
import tarfile
import tempfile

ALLOW_FILES = ("prefs.js", "extensions.json", "extension-preferences.json", "extension-settings.json",
               "cookies.sqlite", "chrome/userContent.css")
ALLOW_DIRS = ("extensions", "storage/default")   # storage/default keeps only moz-extension+++* entries
PREF_PREFIXES = ("extensions.", "network.proxy", "toolkit.legacyUserProfileCustomizations", "browser.startup")
XPI_ID = "{677bb78c-7168-4aa5-8e9b-32e9f26c6157}"
ITEMS_DB = "zeeschuimer-items"
USER_CONTEXT = "^userContextId="
SKIP_COPY = {"cache2", "startupCache", "sessionstore-backups", "crashes", "minidumps", "saved-telemetry-pings",
             "lock", ".parentlock"}


class TemplateError(Exception):
    """A template cannot be built or fails verification; the message lists the problems."""


def _parents(rels):
    out = set()
    for r in rels:
        parts = r.split("/")
        out.update("/".join(parts[:i]) for i in range(1, len(parts)))
    return out


_ANCESTORS = _parents(ALLOW_FILES + ALLOW_DIRS)


def _remove(p):
    if os.path.isdir(p) and not os.path.islink(p):
        shutil.rmtree(p, ignore_errors=True)
    else:
        os.unlink(p)


def _prune(root, rel=""):
    """Delete everything under root/rel that the allowlist does not name."""
    here = os.path.join(root, rel) if rel else root
    for name in sorted(os.listdir(here)):
        r = f"{rel}/{name}" if rel else name
        p = os.path.join(here, name)
        if os.path.islink(p):
            os.unlink(p)
        elif r in ALLOW_FILES:
            continue
        elif r in ALLOW_DIRS:
            if r == "storage/default":
                for child in os.listdir(p):
                    if not child.startswith("moz-extension+++"):
                        _remove(os.path.join(p, child))
        elif r in _ANCESTORS and os.path.isdir(p):
            _prune(root, r)
        else:
            _remove(p)


def _drop_symlinks(root):
    for d, dirs, files in os.walk(root):
        for n in dirs + files:
            p = os.path.join(d, n)
            if os.path.islink(p):
                os.unlink(p)


def _filter_prefs(path):
    keep = []
    with open(path, encoding="utf-8", errors="replace") as f:
        for line in f:
            s = line.strip()
            if s.startswith(('user_pref("', "user_pref('")) and s[11:].startswith(PREF_PREFIXES):
                keep.append(line if line.endswith("\n") else line + "\n")
    with open(path, "w", encoding="utf-8") as f:
        f.writelines(keep)


def _filter_cookies(path, keep_cookies):
    keep = {(c["host"], c["name"]) for c in keep_cookies}
    db = sqlite3.connect(path)
    try:
        doomed = [(i,) for i, h, n in db.execute("SELECT id, host, name FROM moz_cookies") if (h, n) not in keep]
        db.executemany("DELETE FROM moz_cookies WHERE id = ?", doomed)
        db.commit()
        db.execute("PRAGMA wal_checkpoint(TRUNCATE)")
        db.execute("VACUUM")
    finally:
        db.close()
    for side in ("-wal", "-shm", "-journal"):
        if os.path.exists(path + side):
            os.remove(path + side)


def sanitise(profile_dir: str, keep_cookies: list) -> None:
    """Work on a COPY: delete everything not allowlisted, keep only keep_cookies, filter prefs.js."""
    _prune(profile_dir)
    _drop_symlinks(profile_dir)
    prefs = os.path.join(profile_dir, "prefs.js")
    if os.path.isfile(prefs):
        _filter_prefs(prefs)
    cookies = os.path.join(profile_dir, "cookies.sqlite")
    if os.path.isfile(cookies):
        _filter_cookies(cookies, keep_cookies)


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


def verify_release(profile_dir: str, platform) -> list:
    problems = []
    if not _has_xpi(profile_dir):
        problems.append("xpi id missing")
    cookies = os.path.join(profile_dir, "cookies.sqlite")
    if os.path.isfile(cookies):
        keep = {(c["host"], c["name"]) for c in platform.keep_cookies}
        with _snapshot(cookies) as con:
            for h, n in con.execute("SELECT host, name FROM moz_cookies ORDER BY host, name"):
                if (h, n) not in keep:
                    problems.append(f"cookie outside keep list: {h} {n}")
    dbs = idb_databases(profile_dir)
    items = [c for _, name, c in dbs if name == ITEMS_DB]
    if not items:
        problems.append(f"{ITEMS_DB} database not found")
    for counts in items:
        if counts.get("items", 0) > 0:
            problems.append(f"{ITEMS_DB} contains {counts['items']} captured items")
        if counts.get("uploads", 0) > 0 or counts.get("nav", 0) > 0:
            problems.append(f"{ITEMS_DB} has upload history")
    if not any(USER_CONTEXT in path and sum(c.values()) > 0 for path, _, c in dbs):
        problems.append("extension storage.local database not found or empty")
    return problems


def manifest(platform, zeeschuimer_version: str, sha256: str, kind: str) -> dict:
    return {"kind": kind, "platform": platform.name, "zeeschuimer_version": zeeschuimer_version,
            "modules": platform.zeeschuimer_modules, "built_on": datetime.date.today().isoformat(),
            "sha256": sha256}


def _sha256(path):
    h = hashlib.sha256()
    with open(path, "rb") as f:
        for chunk in iter(lambda: f.read(1 << 20), b""):
            h.update(chunk)
    return h.hexdigest()


def _owned(ti):
    ti.uid = ti.gid = 1000
    ti.uname = ti.gname = ""
    return ti


def release(src_config_dir: str, out_dir: str, platform, zeeschuimer_version: str) -> tuple:
    """Sanitise a copy of a stopped /config, verify it, write the tar and its manifest. Source is untouched."""
    src_profile = os.path.join(src_config_dir, "profile")
    if not os.path.isdir(src_profile):
        raise TemplateError(f"{src_config_dir} has no profile/ directory")
    if os.path.islink(os.path.join(src_profile, "lock")):
        raise TemplateError("profile is locked: stop the Firefox container first")
    stem = f"ddp-comments-template-{platform.name}-{zeeschuimer_version}"
    with tempfile.TemporaryDirectory() as t:
        work = os.path.join(t, "config")
        shutil.copytree(src_config_dir, work, symlinks=True,
                        ignore=lambda d, names: [n for n in names if n in SKIP_COPY])
        profile = os.path.join(work, "profile")
        sanitise(profile, platform.keep_cookies)
        problems = verify_release(profile, platform)
        if problems:
            raise TemplateError("template failed verification: " + "; ".join(problems))
        os.makedirs(out_dir, exist_ok=True)
        tar_path = os.path.join(out_dir, stem + ".tar")
        with tarfile.open(tar_path, "w") as tar:
            for name in sorted(os.listdir(work)):
                tar.add(os.path.join(work, name), arcname=name, filter=_owned)
    man = manifest(platform, zeeschuimer_version, _sha256(tar_path), "release")
    with open(os.path.join(out_dir, stem + ".json"), "w", encoding="utf-8") as f:
        json.dump(man, f, indent=2)
        f.write("\n")
    return tar_path, man


def verify_tar(tar_path: str, platform) -> list:
    problems = []
    man_path = os.path.splitext(tar_path)[0] + ".json"
    try:
        with open(man_path, encoding="utf-8") as f:
            man = json.load(f)
        if man.get("sha256") != _sha256(tar_path):
            problems.append("sha256 does not match the manifest")
        if man.get("platform") != platform.name:
            problems.append(f"manifest platform {man.get('platform')!r} is not {platform.name!r}")
    except (OSError, ValueError):
        problems.append(f"manifest {os.path.basename(man_path)} missing or unreadable")
    with tempfile.TemporaryDirectory() as t:
        try:
            with tarfile.open(tar_path) as tar:
                tar.extractall(t, filter="data")
        except (tarfile.TarError, OSError) as e:
            return problems + [f"cannot extract tar: {e}"]
        profile = os.path.join(t, "profile")
        if not os.path.isdir(profile):
            return problems + ["profile/ missing from tar"]
        return problems + verify_release(profile, platform)
